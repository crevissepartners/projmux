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
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

const codexUpgradeCommandTimeout = 45 * time.Second

type codexUpgradeCommand struct {
	coordinator *codexupgrade.Coordinator
	readFile    func(string) ([]byte, error)
	timeout     time.Duration
}

func (command *codexUpgradeCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("agent app-server upgrade requires plan, apply, resume, or abort")
	}
	switch args[0] {
	case "plan", "apply":
		return command.runRequest(args[0], args[1:], stdout, stderr)
	case "resume", "abort":
		return command.runOperation(args[0], args[1:], stdout, stderr)
	default:
		return usageError("agent app-server upgrade requires plan, apply, resume, or abort")
	}
}

func (command *codexUpgradeCommand) runRequest(action string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent app-server upgrade "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var requestPath string
	fs.StringVar(&requestPath, "request", "", "absolute path to an exact private rolling-upgrade request")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 || !filepath.IsAbs(requestPath) || filepath.Clean(requestPath) != requestPath {
		return usageError("agent app-server upgrade " + action + " requires exactly --request <absolute-json>")
	}
	request, err := command.loadRequest(requestPath)
	if err != nil {
		return err
	}
	if command.coordinator == nil {
		return errors.New("codex rolling upgrade coordinator is not configured")
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
	return writeCodexUpgradeJSON(stdout, contentFreeUpgradeReceipt(journal))
}

func (command *codexUpgradeCommand) runOperation(action string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent app-server upgrade "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var operationRef string
	fs.StringVar(&operationRef, "operation", "", "exact rolling operation ref")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 || strings.TrimSpace(operationRef) == "" || operationRef != strings.TrimSpace(operationRef) {
		return usageError("agent app-server upgrade " + action + " requires exactly --operation <ref>")
	}
	if command.coordinator == nil {
		return errors.New("codex rolling upgrade coordinator is not configured")
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
	return writeCodexUpgradeJSON(stdout, contentFreeUpgradeReceipt(journal))
}

func (command *codexUpgradeCommand) timeoutValue() time.Duration {
	if command != nil && command.timeout > 0 {
		return command.timeout
	}
	return codexUpgradeCommandTimeout
}

func (command *codexUpgradeCommand) loadRequest(path string) (codexupgrade.Request, error) {
	read := command.readFile
	if read == nil {
		read = os.ReadFile
	}
	body, err := read(path)
	if err != nil {
		return codexupgrade.Request{}, fmt.Errorf("read Codex rolling upgrade request: %w", err)
	}
	if len(body) > 1024*1024 {
		return codexupgrade.Request{}, errors.New("codex rolling upgrade request exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request codexupgrade.Request
	if err := decoder.Decode(&request); err != nil {
		return codexupgrade.Request{}, fmt.Errorf("decode Codex rolling upgrade request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return codexupgrade.Request{}, errors.New("codex rolling upgrade request has trailing JSON")
	}
	return request, nil
}

type codexUpgradeGenerationReceipt struct {
	EndpointGenerationID string                          `json:"endpointGenerationID"`
	State                codexgeneration.GenerationState `json:"state"`
}

type codexUpgradeReceipt struct {
	OperationRef        string                                  `json:"operationRef"`
	CurrentGenerationID string                                  `json:"currentGenerationID"`
	Generations         []codexUpgradeGenerationReceipt         `json:"generations"`
	Operation           codexgeneration.RollingUpgradeOperation `json:"operation"`
}

func contentFreeUpgradeReceipt(journal codexupgrade.Journal) codexUpgradeReceipt {
	receipt := codexUpgradeReceipt{CurrentGenerationID: journal.CurrentGenerationID}
	if journal.Operation != nil {
		receipt.OperationRef, receipt.Operation = journal.Operation.OperationRef, *journal.Operation
	}
	for _, route := range journal.Routes {
		receipt.Generations = append(receipt.Generations, codexUpgradeGenerationReceipt{
			EndpointGenerationID: route.Generation.Endpoint.EndpointGenerationID, State: route.Generation.State,
		})
	}
	return receipt
}

func writeCodexUpgradeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
