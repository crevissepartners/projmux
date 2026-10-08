package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// One turn path serves both providers. Each provider keeps its own host
// socket and wire envelope, byte for byte.
func TestProcessHostRouteKeepsProviderSocketsAndEnvelopes(t *testing.T) {
	const registryPath, pane, generation = "/state/registry.json", "pane-a", "gen-1"
	if got, want := processHostSocket(aiModeClaude, registryPath, pane, generation), processClaudeHostSocket(registryPath, pane, generation); got != want {
		t.Fatalf("Claude socket = %q, want %q", got, want)
	}
	if got, want := processHostSocket(aiModeCodex, registryPath, pane, generation), processCodexHostSocket(registryPath, pane, generation); got != want {
		t.Fatalf("Codex socket = %q, want %q", got, want)
	}
	binding := processhost.Binding{Agent: "agent-a", Pane: pane, Generation: generation, Operation: "op-1"}
	foreground := &processForegroundRequest{Authority: processhost.Authority{Binding: binding, Connection: "op-1", Session: "session"}, Action: "turn", Operation: "op-2", Prompt: "hello", Turn: "turn-1"}
	for _, test := range []struct {
		name string
		got  any
		want any
	}{
		{"Claude turn", processHostRequest(aiModeClaude, nil, foreground), claudeProcessCheck{Foreground: foreground}},
		{"Codex turn", processHostRequest(aiModeCodex, nil, foreground), codexProcessExchange{Foreground: foreground}},
		{"Claude observation", processHostRequest(aiModeClaude, &binding, nil), claudeProcessCheck{Observe: &binding}},
		{"Codex observation", processHostRequest(aiModeCodex, &binding, nil), codexProcessExchange{Observe: &binding}},
	} {
		got, err := json.Marshal(test.got)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(test.want)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s envelope changed:\n got %s\nwant %s", test.name, got, want)
		}
	}
}

// The shared turn path refuses before any socket is inspected unless the
// Agent's provider, current Pane session, and current activation all agree.
func TestProcessTurnRefusesWithoutExactProviderSession(t *testing.T) {
	binding := coremetadata.ProcessBinding{AgentUID: "agent-a", Generation: "gen-1", OperationID: "op-1"}
	pane := coremetadata.Pane{Metadata: coremetadata.ObjectMeta{UID: "pane-a"}}
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: aiModeClaude, Binding: binding, SessionID: "session"}
	agent := coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent-a"}}
	agent.Spec.Provider = aiModeClaude
	agent.Status.PaneRef = "pane-a"
	reg := coremetadata.Registry{Panes: []coremetadata.Pane{pane}, Agents: []coremetadata.Agent{agent}}
	paths := func() (config.Paths, error) { return config.Paths{StateDir: t.TempDir()}, nil }

	missing := agent
	missing.Status.PaneRef = "pane-missing"
	noSession := reg.Clone()
	noSession.Panes[0].Status.ProcessSession = nil
	for _, test := range []struct {
		name     string
		command  *agentCommand
		reg      coremetadata.Registry
		agent    coremetadata.Agent
		provider string
	}{
		{"provider mismatch", &agentCommand{controlPaths: paths}, reg, agent, aiModeCodex},
		{"missing Pane", &agentCommand{controlPaths: paths}, reg, missing, aiModeClaude},
		{"no session", &agentCommand{controlPaths: paths}, noSession, agent, aiModeClaude},
		{"no state paths", &agentCommand{}, reg, agent, aiModeClaude},
		{"no current activation", &agentCommand{controlPaths: paths}, reg, agent, aiModeClaude},
	} {
		operation, err := test.command.callProcessTurn(test.reg, test.agent, test.provider, "turn", "hello")
		if !errors.Is(err, processhost.ErrStale) || operation != "" {
			t.Fatalf("%s: operation=%q err=%v, want stale refusal", test.name, operation, err)
		}
	}
}

func TestProcessTurnAcceptanceMapsHostResult(t *testing.T) {
	for _, test := range []struct {
		result processForegroundResult
		want   error
	}{
		{processForegroundResult{Accepted: true}, nil},
		{processForegroundResult{Busy: true}, processhost.ErrBusy},
		{processForegroundResult{Closed: true}, processhost.ErrClosed},
		{processForegroundResult{Stale: true}, processhost.ErrStale},
		{processForegroundResult{}, processhost.ErrStale},
	} {
		if err := processTurnAcceptance(test.result); !errors.Is(err, test.want) || (test.want == nil && err != nil) {
			t.Fatalf("%+v: err=%v, want %v", test.result, err, test.want)
		}
	}
}

// Bounded Claude refusals keep Busy and name their limit; a joined acceptance
// adds a field without changing Accepted or the Codex delivery receipt.
func TestProcessTurnAcceptanceNamesBoundedClaudeRefusals(t *testing.T) {
	for _, test := range []struct {
		err    error
		reason string
		want   []string
	}{
		{processhost.ErrClaudeJoinLimit, processBusyJoinLimit, []string{"joined-input-limit", "8 joined inputs", "262144 bytes"}},
		{processhost.ErrClaudeControlPending, processBusyControlPending, []string{"control-pending", "pending permission or question"}},
		{processhost.ErrBusy, "", []string{"process admission capacity exhausted"}},
	} {
		if got := processBusyReason(test.err); got != test.reason {
			t.Fatalf("%v: reason %q, want %q", test.err, got, test.reason)
		}
		err := processTurnAcceptance(processForegroundResult{Busy: true, BusyReason: test.reason})
		if !errors.Is(err, processhost.ErrBusy) {
			t.Fatalf("%v: lost busy classification: %v", test.err, err)
		}
		for _, want := range test.want {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%v: %q lacks %q", test.err, err, want)
			}
		}
	}
	raw, err := json.Marshal(processForegroundResult{Accepted: true})
	if err != nil || strings.Contains(string(raw), "Join") || strings.Contains(string(raw), "BusyReason") {
		t.Fatalf("unjoined result changed its wire shape: %s %v", raw, err)
	}
	var joined processForegroundResult
	if err = json.Unmarshal([]byte(`{"Accepted":true,"Join":{"Turn":"running","Origin":"provider"}}`), &joined); err != nil || !joined.Accepted || joined.Join == nil || *joined.Join != (processTurnJoin{Turn: "running", Origin: "provider"}) || joined.UserDelivery != nil {
		t.Fatalf("joined result: %+v %v", joined, err)
	}
	if err = processTurnAcceptance(joined); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	agent := coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent-a"}}
	if err = (&agentCommand{}).writeJoinedProcessTurn(&out, agentActionSendTurn, agent, "operation-1", *joined.Join); err != nil {
		t.Fatal(err)
	}
	if want := "Send new turn agent=uid:agent-a turn=operation-1 runtime=process delivery=joined running-turn=running origin=provider\n"; out.String() != want {
		t.Fatalf("joined output %q, want %q", out.String(), want)
	}
}
