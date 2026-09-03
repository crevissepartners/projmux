package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexhandover"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

const codexHandoverCommandTimeout = 2 * time.Minute

type codexHandoverCommand struct {
	coordinator *codexhandover.Coordinator
	readFile    func(string) ([]byte, error)
	timeout     time.Duration
}

func (command *codexHandoverCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("agent app-server handover requires plan, apply, resume, or abort")
	}
	switch args[0] {
	case "plan", "apply":
		return command.runRequest(args[0], args[1:], stdout, stderr)
	case "resume", "abort":
		return command.runOperation(args[0], args[1:], stdout, stderr)
	default:
		return usageError("agent app-server handover requires plan, apply, resume, or abort")
	}
}

func (command *codexHandoverCommand) runRequest(action string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent app-server handover "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var requestPath string
	fs.StringVar(&requestPath, "request", "", "absolute path to an exact generation handover request")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 || !filepath.IsAbs(requestPath) || filepath.Clean(requestPath) != requestPath {
		return usageError("agent app-server handover " + action + " requires exactly --request <absolute-json>")
	}
	request, err := command.loadRequest(requestPath)
	if err != nil {
		return err
	}
	if command.coordinator == nil {
		return errors.New("codex generation handover coordinator is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), command.timeoutValue())
	defer cancel()
	if action == "plan" {
		return writeCodexUpgradeJSON(stdout, command.coordinator.Plan(ctx, request))
	}
	journal, err := command.coordinator.Apply(ctx, request)
	if err != nil {
		return err
	}
	return writeCodexUpgradeJSON(stdout, contentFreeHandoverReceipt(journal))
}

func (command *codexHandoverCommand) runOperation(action string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent app-server handover "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var operationRef string
	fs.StringVar(&operationRef, "operation", "", "exact handover operation ref")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 || strings.TrimSpace(operationRef) == "" || operationRef != strings.TrimSpace(operationRef) {
		return usageError("agent app-server handover " + action + " requires exactly --operation <ref>")
	}
	if command.coordinator == nil {
		return errors.New("codex generation handover coordinator is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), command.timeoutValue())
	defer cancel()
	var journal codexupgrade.Journal
	var err error
	if action == "resume" {
		journal, err = command.coordinator.Resume(ctx, operationRef)
	} else {
		journal, err = command.coordinator.Abort(ctx, operationRef)
	}
	if err != nil {
		return err
	}
	return writeCodexUpgradeJSON(stdout, contentFreeHandoverReceipt(journal))
}

func (command *codexHandoverCommand) timeoutValue() time.Duration {
	if command != nil && command.timeout > 0 {
		return command.timeout
	}
	return codexHandoverCommandTimeout
}

func (command *codexHandoverCommand) loadRequest(path string) (codexhandover.Request, error) {
	read := command.readFile
	if read == nil {
		read = os.ReadFile
	}
	body, err := read(path)
	if err != nil {
		return codexhandover.Request{}, fmt.Errorf("read Codex handover request: %w", err)
	}
	if len(body) > 1024*1024 {
		return codexhandover.Request{}, errors.New("codex handover request exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request codexhandover.Request
	if err := decoder.Decode(&request); err != nil {
		return codexhandover.Request{}, fmt.Errorf("decode Codex handover request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return codexhandover.Request{}, errors.New("codex handover request has trailing JSON")
	}
	return request, nil
}

type codexHandoverReceipt struct {
	OperationRef string                            `json:"operationRef"`
	Phase        codexgeneration.HandoverPhase     `json:"phase"`
	Targets      []codexgeneration.HandoverTarget  `json:"targets,omitempty"`
	Choices      []codexgeneration.NoTurnChoice    `json:"choices,omitempty"`
	Mutations    codexgeneration.HandoverMutations `json:"mutations"`
}

func contentFreeHandoverReceipt(journal codexupgrade.Journal) codexHandoverReceipt {
	if journal.Handover == nil {
		return codexHandoverReceipt{}
	}
	op := journal.Handover
	return codexHandoverReceipt{OperationRef: op.OperationRef, Phase: op.Phase, Targets: op.Targets, Choices: op.Choices, Mutations: op.Mutations}
}
