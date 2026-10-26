package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// markedOfflinePreparedFixture commits mark-offline's exact lost-host
// retirement: reconcile/unknown on Pane and Agent, no Wait, no activation.
func markedOfflinePreparedFixture(t *testing.T) (*agentCommand, processAgentResumeOptions, *intmetadata.Store, coremetadata.ProcessActivation) {
	t.Helper()
	c, opts, store, a := hostLostPreparedFixture(t)
	opts.allowHostLost = false
	if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		return intmetadata.DefaultMutator().MarkProcessHostLostOffline(reg, a)
	}); err != nil {
		t.Fatal(err)
	}
	reg, err := store.LoadReadOnly()
	if err != nil || !reg.ProcessHostLostOffline(a.Binding.PaneUID) {
		t.Fatal("mark-offline retirement missing", err)
	}
	pane, _ := reg.Pane(a.Binding.PaneUID)
	if coremetadata.MatchesProcessWait(a.Binding, pane.Status.LastTermination) {
		t.Fatal("fixture invented Wait")
	}
	return c, opts, store, a
}

// Each change leaves a receipt that is not this binding's exact retirement.
var markedOfflineRefusals = map[string]func(*coremetadata.Registry, coremetadata.ProcessActivation){
	"other-generation": func(reg *coremetadata.Registry, a coremetadata.ProcessActivation) {
		pane, _ := reg.Pane(a.Binding.PaneUID)
		agent, _ := reg.Agent(a.Binding.AgentUID)
		pane.Status.LastTermination.Generation = "other-generation"
		agent.Status.LastTermination.Generation = "other-generation"
	},
	"other-operation": func(reg *coremetadata.Registry, a coremetadata.ProcessActivation) {
		pane, _ := reg.Pane(a.Binding.PaneUID)
		agent, _ := reg.Agent(a.Binding.AgentUID)
		pane.Status.LastTermination.OperationID = "other-operation"
		agent.Status.LastTermination.OperationID = "other-operation"
	},
	"agent-receipt-missing": func(reg *coremetadata.Registry, a coremetadata.ProcessActivation) {
		agent, _ := reg.Agent(a.Binding.AgentUID)
		agent.Status.LastTermination = nil
	},
}

func TestProcessHostLostRetiredRequiresExactBinding(t *testing.T) {
	reg, a := hostLostFixture(t)
	running := reg.Clone()
	if err := (coremetadata.Mutator{}).MarkProcessHostLostOffline(&reg, a); err != nil {
		t.Fatal(err)
	}
	retired := func(r coremetadata.Registry, b coremetadata.ProcessBinding) bool {
		pane, _ := r.Pane(a.Binding.PaneUID)
		agent, _ := r.Agent(a.Binding.AgentUID)
		return coremetadata.ProcessHostLostRetired(b, *pane, *agent)
	}
	if !retired(reg, a.Binding) {
		t.Fatal("exact lost-host retirement refused")
	}
	other := a.Binding
	other.Generation = "other-generation"
	if retired(reg, other) {
		t.Fatal("another binding borrowed the retirement")
	}
	if retired(running, a.Binding) {
		t.Fatal("a Running Agent with its activation counted as retired")
	}
	for name, change := range markedOfflineRefusals {
		changed := reg.Clone()
		change(&changed, a)
		if retired(changed, a.Binding) {
			t.Fatal("inexact receipt counted as retired:", name)
		}
	}
	waited, w := hostLostFixture(t)
	recordFixtureWait(t, &waited, w.Binding.PaneUID)
	waitedPane, _ := waited.Pane(w.Binding.PaneUID)
	waitedAgent, _ := waited.Agent(w.Binding.AgentUID)
	if !coremetadata.MatchesProcessWait(w.Binding, waitedPane.Status.LastTermination) || coremetadata.ProcessHostLostRetired(w.Binding, *waitedPane, *waitedAgent) {
		t.Fatal("supervisor Wait counted as a lost-host retirement")
	}
}

func TestMarkedOfflineRelaunchRetirementAcceptsExactReceipt(t *testing.T) {
	c, _, store, a := markedOfflinePreparedFixture(t)
	reg, _ := store.LoadReadOnly()
	agent, _ := reg.Agent(a.Binding.AgentUID)
	pane, _ := reg.Pane(a.Binding.PaneUID)
	candidate, err := c.awaitProcessRelaunchRetirement(context.Background(), agent.Clone(), pane.Clone(), false)
	if err != nil || candidate.HostLost != nil || candidate.Record.Binding != a.Binding {
		t.Fatalf("exact lost-host retirement refused: %+v %v", candidate.Record.Binding, err)
	}
}

func TestMarkedOfflineChecksRefuseInexactReceiptWithoutWrites(t *testing.T) {
	for name, change := range markedOfflineRefusals {
		t.Run(name, func(t *testing.T) {
			c, opts, store, a := markedOfflinePreparedFixture(t)
			prepared, err := c.prepareOwnedClaudeResume(context.Background(), opts)
			if err != nil || prepared.State != agentProcessPrepared {
				t.Fatal("exact retirement not prepared", err)
			}
			record, err := c.readDeferredLaunch(a.Binding.AgentUID)
			if err != nil || record == nil || record.HostLost != nil {
				t.Fatal("prepared record", err)
			}
			candidate, err := c.processResumeCandidate(processAgentResumeRequest{options: opts})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = store.UpdateConvergent(func(reg *coremetadata.Registry) error { change(reg, a); return nil }); err != nil {
				t.Fatal(err)
			}
			reg, _ := store.LoadReadOnly()
			agent, _ := reg.Agent(a.Binding.AgentUID)
			pane, _ := reg.Pane(a.Binding.PaneUID)
			path := c.deferredStatePath("deferred-launches", a.Binding.AgentUID)
			before, _ := os.ReadFile(store.Path())
			launch, _ := os.ReadFile(path)
			unchanged := func(check string) {
				t.Helper()
				after, _ := os.ReadFile(store.Path())
				current, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) || !bytes.Equal(launch, current) {
					t.Fatal(check, "refusal wrote state")
				}
			}
			if _, err = c.awaitProcessRelaunchRetirement(context.Background(), agent.Clone(), pane.Clone(), false); err == nil || !strings.Contains(err.Error(), "exact old child Wait is unavailable") {
				t.Fatal("relaunch retirement accepted inexact receipt", err)
			}
			unchanged("relaunch retirement")
			if err = c.reconcileDeferredLaunch(context.Background(), record, false); err == nil || !strings.Contains(err.Error(), "prepared relaunch conversation changed") {
				t.Fatal("prepared launch accepted inexact receipt", err)
			}
			unchanged("prepared launch")
			binding := processSchemaBinding(candidate.Record.Binding)
			binding.Host, binding.Generation, binding.Operation = "fresh-host", "fresh-generation", "fresh-operation"
			if err = c.prepareDeferredAttempt(candidate, binding, record); err == nil || !strings.Contains(err.Error(), "attempt source changed") {
				t.Fatal("attempt accepted inexact receipt", err)
			}
			unchanged("attempt")
			opts.Prompt = processResumeFirstFrame{Kind: "user", Text: "first input"}
			if _, err = c.resumeProcessAgent(context.Background(), processAgentResumeRequest{options: opts}); err == nil {
				t.Fatal("first input started from inexact receipt")
			}
			unchanged("first input")
		})
	}
}

func TestMarkedOfflineClaudeRequiresRetirementBeforePreparation(t *testing.T) {
	// The unknown state keeps its activation and is not Offline. Without the
	// explicit host-lost opt-in it stays refused, exactly as before.
	c, opts, store, a := hostLostPreparedFixture(t)
	opts.allowHostLost = false
	before, _ := os.ReadFile(store.Path())
	if _, err := c.prepareOwnedClaudeResume(context.Background(), opts); err == nil {
		t.Fatal("unretired lost host prepared without opt-in")
	}
	after, _ := os.ReadFile(store.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("refusal wrote Registry")
	}
	if record, err := c.readDeferredLaunch(a.Binding.AgentUID); err != nil || record != nil {
		t.Fatal("refusal wrote a prepared launch", err)
	}
}

func TestMarkedOfflineClaudePreparedFirstInputUsesSameSession(t *testing.T) {
	c, opts, store, a := markedOfflinePreparedFixture(t)
	root := os.Getenv("HOME")
	// The test binary dispatches only this marked fixture supervisor; no real provider.
	t.Setenv("PMX_TEST_DEFERRED_INTERNAL", "1")
	trace := filepath.Join(root, "resume-wire.json")
	script := filepath.Join(root, "provider.py")
	source := fmt.Sprintf(`import sys,json
open(%q,'w').write(json.dumps(sys.argv))
for line in sys.stdin:
 frame=json.loads(line)
 if frame.get('type')=='user':
  open(%q,'a').write('\n'+json.dumps(frame))
  print(json.dumps({'type':'system','subtype':'init','session_id':'session'}),flush=True)
  print(json.dumps({'type':'result','subtype':'success','session_id':'session'}),flush=True)
`, trace, trace)
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\nexec python3 -u "+script+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prepared, err := c.prepareOwnedClaudeResume(ctx, opts)
	if err != nil || prepared.State != agentProcessPrepared || prepared.Prepared == nil || prepared.Prepared.Conversation != "session" {
		t.Fatalf("prompt-less resume after mark-offline: %+v %v", prepared, err)
	}
	waiting, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if pane, _ := waiting.Pane(a.Binding.PaneUID); !waiting.ProcessHostLostOffline(a.Binding.PaneUID) || pane.Status.ProcessSession.Binding != a.Binding {
		t.Fatal("preparation reserved a generation or changed the retirement")
	}
	if _, err = os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("preparation started a child", err)
	}
	record, err := c.readDeferredLaunch(a.Binding.AgentUID)
	if err != nil || record == nil || record.HostLost != nil || record.Retired.Binding != a.Binding {
		t.Fatal("prepared record", err)
	}
	opts.Prompt = processResumeFirstFrame{Kind: "user", Text: "first input after mark-offline"}
	result, err := c.resumeProcessAgent(ctx, processAgentResumeRequest{options: opts})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		end, stop := context.WithCancel(context.Background())
		stop()
		_, _ = result.owner.waitProcessAgent(end, nil)
	}()
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(a.Binding.PaneUID)
	session := pane.Status.ProcessSession
	if session.SessionID != "session" || session.Binding.Generation == a.Binding.Generation || session.Binding.AgentUID != a.Binding.AgentUID || session.Binding.PaneUID != a.Binding.PaneUID {
		t.Fatalf("conversation or identity replaced: %+v", session)
	}
	if receipt := pane.Status.LastTermination; receipt == nil || receipt.Source != coremetadata.TerminationSourceReconcile || receipt.ExitCode != nil || coremetadata.MatchesProcessWait(a.Binding, receipt) {
		t.Fatal("invented Wait", receipt)
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("--resume")) || !bytes.Contains(raw, []byte("session")) || !bytes.Contains(raw, []byte(opts.Prompt.Text)) {
		t.Fatal("first input or same session missing", string(raw))
	}
	if current, err := c.readDeferredLaunch(a.Binding.AgentUID); err != nil || current != nil {
		t.Fatal("Prepared not consumed", err)
	}
}
