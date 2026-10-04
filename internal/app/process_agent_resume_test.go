package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/crevissepartners/projmux/internal/cli"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessSupportedResumeFirstFrameDistinguishesUserAndPeer(t *testing.T) {
	agent := selector.Ref{Kind: coremetadata.KindAgent, UID: "agent-resume"}
	peer, err := providerCoordinationContent(dialogueEnvelope("resume-peer", time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []processResumeFirstFrame{{}, {Kind: "user", Text: "literal user task"}, {Kind: "peer", Text: peer}} {
		req, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: agent, Prompt: frame})
		if err != nil || req.options.Prompt != frame {
			t.Fatal("first frame was rewritten", err)
		}
	}
	for _, frame := range []processResumeFirstFrame{{Text: "without kind"}, {Kind: "user", Text: " "}, {Kind: "peer", Text: "plain peer task"}, {Kind: "unknown", Text: "task"}} {
		if _, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: agent, Prompt: frame}); err == nil {
			t.Fatal("untyped or unwrapped first frame accepted", frame)
		}
	}
	var content map[string]any
	if json.Unmarshal([]byte(peer), &content) != nil || content["authority"] != "untrusted-coordination-only" {
		t.Fatal("peer content lost the live delivery envelope")
	}
}

func TestProcessSupportedResumeRefusesBeforePreparingProvider(t *testing.T) {
	reg := processResumeQueryFixture(t)
	candidate := listResumableProcessAgents(reg, processResumeFilter{})[0]
	command := &agentCommand{loadRegistry: func() (coremetadata.Registry, error) { return reg, nil }}
	request, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: candidate.Agent.Metadata.UID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = command.resumeProcessAgent(context.Background(), request)
	if !errors.Is(err, processhost.ErrResumeRefused) || !strings.Contains(err.Error(), "explicit first frame") {
		t.Fatal("missing first frame reached provider", err)
	}
}

func TestProcessSupportedResumeReceiptPreservesExistingIdentity(t *testing.T) {
	receipt := processResumeReceipt(processhost.Binding{Agent: "existing-agent", Window: "existing-window"}, "existing")
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := receipt.WriteJSON(&output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"operation":"agent.resume"`, `"identity":"reused"`, `"address":"unchanged"`, `"topology":"unchanged"`, `"desiredState":"unchanged"`, `"runtime":"materialized"`, `"focus":"unchanged"`} {
		if !strings.Contains(strings.ReplaceAll(output.String(), " ", ""), want) {
			t.Fatalf("dishonest resume receipt missing %s: %s", want, output.String())
		}
	}
	if receipt.Operation == cli.OperationCreateAgent {
		t.Fatal("resume claimed creation")
	}
}

func TestProcessSupportedResumeKeepsTmuxGrammarAndDetachedResult(t *testing.T) {
	for _, args := range [][]string{{"resume", "codex", "--project", "beta"}, {"resume", "--project", "beta", "--", "codex"}} {
		store := newFakeResourceStore(t)
		setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
		command, launcher, _, _ := newTestAgentResumeCommand(t, store, newFakeTmux())
		enablePinnedNativeResumeFixture(t, command, store, "agt-beta-codex", launcher)
		stdout, stderr, err := runRoute(t, command, args...)
		if err != nil || stdout != "agent/codex resumed\n" || stderr != "" {
			t.Fatalf("tmux bytes changed for %q: %q %q %v", args, stdout, stderr, err)
		}
	}
}
