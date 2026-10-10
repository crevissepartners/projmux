package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func hostLostPreparedFixture(t *testing.T) (*agentCommand, processAgentResumeOptions, *intmetadata.Store, coremetadata.ProcessActivation) {
	t.Helper()
	root := t.TempDir()
	for key, value := range map[string]string{"HOME": root, "XDG_CONFIG_HOME": root + "/config", "XDG_STATE_HOME": root + "/state", "XDG_CACHE_HOME": root + "/cache"} {
		t.Setenv(key, value)
	}
	binary := filepath.Join(root, "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := New().agent
	c.processIdentity = absentProcess
	reg, a := hostLostFixture(t)
	agent, _ := reg.Agent(a.Binding.AgentUID)
	agent.Spec.Workspace.CWD = root
	pane, _ := reg.Pane(a.Binding.PaneUID)
	pane.Spec.CWD = root
	project, _ := reg.Project(a.Binding.ProjectUID)
	project.Spec.Root = root
	recipe, err := c.planProcessRelaunchRecipe(reg, agent.Clone(), pane.Clone(), agentRelaunchRequest{}, func(_, detail string) error { return fmt.Errorf("fixture recipe: %s", detail) })
	if err != nil {
		t.Fatal(err)
	}
	agent.Metadata.Annotations = recipe.annotations
	agent.Spec.Workspace = recipe.workspace
	state, err := c.rebind.create.store.stateDir()
	if err != nil {
		t.Fatal(err)
	}
	store := intmetadata.NewStore(intmetadata.PathFor(state))
	if _, _, err := store.UpdateConvergent(func(r *coremetadata.Registry) error { *r = reg; return nil }); err != nil {
		t.Fatal(err)
	}
	return c, processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: a.Binding.AgentUID}, allowHostLost: true}, store, a
}

func TestHostLostClaudePreparedPreservesRegistry(t *testing.T) {
	c, opts, store, a := hostLostPreparedFixture(t)
	before, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.prepareOwnedClaudeResume(context.Background(), opts)
	if err != nil {
		t.Fatal("host-lost preparation", err)
	}
	if got.State != agentProcessPrepared || got.Prepared == nil || got.Owned != nil || got.Prepared.Conversation != "session" {
		t.Fatalf("Prepared=%+v", got)
	}
	after, err := store.LoadReadOnly()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("preparation mutated Registry", err)
	}
	claim, err := readDeferredClaim(c.deferredClaimPath(a.Binding.AgentUID))
	if err != nil || claim.Nonce != "" {
		t.Fatal("preparation retained a claim", err)
	}
}

func requireResumeRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil || !(strings.HasPrefix(err.Error(), processResumeNotResumable+":") || strings.HasPrefix(err.Error(), processResumeRefused+":") || strings.HasPrefix(err.Error(), processResumeOwned+":")) {
		t.Fatalf("missing resume refusal prefix: %v", err)
	}
}

func TestHostLostClaudePreparedRejectsLiveAndChangedSource(t *testing.T) {
	for _, stage := range []string{"prepare", "first-input"} {
		for _, change := range []string{"owner-live", "child-live", "unknown", "activation", "session", "recipe"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				c, opts, store, a := hostLostPreparedFixture(t)
				if stage == "first-input" {
					if _, err := c.prepareOwnedClaudeResume(context.Background(), opts); err != nil {
						t.Fatal(err)
					}
				}
				switch change {
				case "owner-live", "child-live", "unknown":
					c.processIdentity = func(pid int) (coremetadata.ProcessIdentity, int, error) {
						if change == "unknown" {
							return coremetadata.ProcessIdentity{}, 0, errors.New("denied")
						}
						if change == "owner-live" && pid == a.HostProcess.PID {
							return a.HostProcess, 0, nil
						}
						if change == "child-live" && pid == a.Child.PID {
							return a.Child, 0, nil
						}
						return absentProcess(pid)
					}
				default:
					if stage == "prepare" {
						return
					} // A preparation can adopt the current absent source.
					if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
						pane, _ := reg.Pane(a.Binding.PaneUID)
						agent, _ := reg.Agent(a.Binding.AgentUID)
						switch change {
						case "activation":
							pane.Status.Activation.Process.Child.Start = "changed"
						case "session":
							pane.Status.ProcessSession.SessionID = "changed"
							pane.Status.ProcessSession.Pending = nil
						case "recipe":
							agent.Metadata.Annotations[coremetadata.AnnotationAgentModel] = "changed"
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := os.ReadFile(store.Path())
				var err error
				if stage == "prepare" {
					_, err = c.prepareOwnedClaudeResume(context.Background(), opts)
				} else {
					opts.Prompt = processResumeFirstFrame{Kind: "user", Text: "first input"}
					_, err = c.resumeProcessAgent(context.Background(), processAgentResumeRequest{options: opts})
				}
				requireResumeRefusal(t, err)
				after, _ := os.ReadFile(store.Path())
				if !bytes.Equal(before, after) {
					t.Fatal("refusal changed Registry")
				}
			})
		}
	}
}

func TestHostLostClaudePreparedOptInAndDigest(t *testing.T) {
	c, opts, store, a := hostLostPreparedFixture(t)
	implicit := opts
	implicit.allowHostLost = false
	before, _ := os.ReadFile(store.Path())
	_, err := c.prepareOwnedClaudeResume(context.Background(), implicit)
	requireResumeRefusal(t, err)
	_, err = c.claimDeferredProcessAgent(context.Background(), implicit)
	requireResumeRefusal(t, err)
	_, err = c.resumeProcessAgent(context.Background(), processAgentResumeRequest{options: implicit})
	requireResumeRefusal(t, err)
	reg, _ := store.LoadReadOnly()
	agent, _ := reg.Agent(a.Binding.AgentUID)
	if _, err = c.runOwnedProcessRelaunch(context.Background(), reg, agent.Clone(), agentRelaunchRequest{}); err == nil {
		t.Fatal("ordinary relaunch accepted host-lost")
	}
	after, _ := os.ReadFile(store.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("implicit call changed Registry")
	}
	prepared, err := c.prepareOwnedClaudeResume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	record, err := c.readDeferredLaunch(a.Binding.AgentUID)
	if err != nil || record == nil || deferredLaunchDigest(record) != prepared.Prepared.LaunchDigest {
		t.Fatal("digest not stable after read", err)
	}
	reused, err := c.prepareOwnedClaudeResume(context.Background(), opts)
	if err != nil || !reflect.DeepEqual(prepared.Prepared, reused.Prepared) {
		t.Fatal("reuse changed digest", err)
	}
	// Even direct sidecar consumers require opt-in; old records omit the field.
	if err = c.reconcileDeferredLaunch(context.Background(), record, false); err == nil {
		t.Fatal("implicit reconciliation accepted host-lost")
	}
	opts.allowHostLost = false
	_, err = c.prepareOwnedClaudeResume(context.Background(), opts)
	requireResumeRefusal(t, err)
	legacy := *record
	legacy.HostLost = nil
	raw, err := json.Marshal(&legacy)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &fields) != nil || fields["HostLost"] != nil {
		t.Fatal("legacy representation changed", err)
	}
}

func TestHostLostClaudePreparedSpawnFailureRestoresAndRetries(t *testing.T) {
	c, opts, store, a := hostLostPreparedFixture(t)
	if _, err := c.prepareOwnedClaudeResume(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	original, _ := store.LoadReadOnly()
	opts.Prompt = processResumeFirstFrame{Kind: "user", Text: "first input"}
	for attempt := range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		update := c.rebind.create.store.update
		c.rebind.create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
			reg, err := update(fn)
			if err == nil {
				pane, _ := reg.Pane(a.Binding.PaneUID)
				if pane.Status.ProcessSession.Binding != a.Binding {
					cancel()
				}
			}
			return reg, err
		}
		result, err := c.resumeProcessAgent(ctx, processAgentResumeRequest{options: opts})
		cancel()
		c.rebind.create.store.update = update
		requireResumeRefusal(t, err)
		if !result.Previous.MayBeTruncated {
			t.Fatal("missing truncation indication", attempt)
		}
		reg, e := store.LoadReadOnly()
		pane, _ := reg.Pane(a.Binding.PaneUID)
		oldPane, _ := original.Pane(a.Binding.PaneUID)
		if e != nil || !reflect.DeepEqual(pane.Status.Activation, oldPane.Status.Activation) || !reflect.DeepEqual(pane.Status.ProcessSession, oldPane.Status.ProcessSession) {
			t.Fatal("lost source not restored", e)
		}
		if pane.Status.LastTermination == nil || pane.Status.LastTermination.Source != coremetadata.TerminationSourceReconcile || pane.Status.LastTermination.Classification != coremetadata.TerminationUnknown || pane.Status.LastTermination.ExitCode != nil {
			t.Fatal("invented Wait")
		}
	}
}

func TestHostLostClaudePreparedFirstInputUsesFrozenSession(t *testing.T) {
	c, opts, store, a := hostLostPreparedFixture(t)
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
	provider := filepath.Join(root, "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\nexec python3 -u "+script+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prepared, err := c.prepareOwnedClaudeResume(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	record, err := c.readDeferredLaunch(a.Binding.AgentUID)
	if err != nil {
		t.Fatal(err)
	}
	opts.Prompt = processResumeFirstFrame{Kind: "user", Text: "first input exact"}
	reads := 0
	c.processIdentity = func(pid int) (coremetadata.ProcessIdentity, int, error) { reads++; return absentProcess(pid) }
	result, err := c.resumeProcessAgent(ctx, processAgentResumeRequest{options: opts})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		end, stop := context.WithCancel(context.Background())
		stop()
		_, _ = result.owner.waitProcessAgent(end, nil)
	}()
	if reads < 4 || !result.Previous.MayBeTruncated {
		t.Fatal("absence not rechecked or missing truncation", reads)
	}
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(a.Binding.PaneUID)
	receipt := pane.Status.LastTermination
	if receipt == nil || receipt.Source != coremetadata.TerminationSourceReconcile || receipt.Classification != coremetadata.TerminationUnknown || receipt.ExitCode != nil || receipt.Signal != "" {
		t.Fatal("invented Wait", receipt)
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("--resume")) || !bytes.Contains(raw, []byte("session")) || !bytes.Contains(raw, []byte(opts.Prompt.Text)) {
		t.Fatal("first input or frozen session missing", string(raw))
	}
	for _, arg := range record.Command.Args {
		if !bytes.Contains(raw, []byte(arg)) {
			t.Fatal("frozen argument missing", arg)
		}
	}
	if prepared.Prepared.Conversation != pane.Status.ProcessSession.SessionID {
		t.Fatal("conversation replaced")
	}
	if current, err := c.readDeferredLaunch(a.Binding.AgentUID); err != nil || current != nil {
		t.Fatal("Prepared not consumed", err)
	}

}

func TestHostLostClaudePreparedReservationRechecksAbsence(t *testing.T) {
	for _, change := range []string{"owner-live", "child-live", "unknown", "activation"} {
		t.Run(change, func(t *testing.T) {
			c, opts, store, a := hostLostPreparedFixture(t)
			if _, err := c.prepareOwnedClaudeResume(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			candidate, err := c.processResumeCandidate(processAgentResumeRequest{options: opts})
			if err != nil {
				t.Fatal(err)
			}
			candidate, prepared, err := c.prepareDeferredLaunch(context.Background(), candidate, opts)
			if err != nil {
				t.Fatal(err)
			}
			if change == "activation" {
				if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					pane, _ := reg.Pane(a.Binding.PaneUID)
					pane.Status.Activation.Process.Child.Start = "changed"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				c.processIdentity = func(pid int) (coremetadata.ProcessIdentity, int, error) {
					if change == "unknown" {
						return coremetadata.ProcessIdentity{}, 0, errors.New("denied")
					}
					if change == "owner-live" && pid == a.HostProcess.PID {
						return a.HostProcess, 0, nil
					}
					if change == "child-live" && pid == a.Child.PID {
						return a.Child, 0, nil
					}
					return absentProcess(pid)
				}
			}
			before, _ := os.ReadFile(store.Path())
			next := processSchemaBinding(a.Binding)
			next.Host = "fresh-host"
			next.Generation = "fresh-gen"
			next.Operation = "fresh-op"
			err = c.reserveProcessResume(context.Background(), candidate, agentSettingsLaunch{}, next, nil, prepared)
			requireResumeRefusal(t, err)
			after, _ := os.ReadFile(store.Path())
			if !bytes.Equal(before, after) {
				t.Fatal("rejected reservation changed Registry")
			}
		})
	}
}

func TestHostLostClaudePreparedDoesNotWeakenOrdinaryRetirement(t *testing.T) {
	c, opts, store, a := hostLostPreparedFixture(t)
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(a.Binding.AgentUID)
	pane, _ := reg.Pane(a.Binding.PaneUID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = c.awaitProcessRelaunchRetirement(ctx, agent.Clone(), pane.Clone(), true); err == nil {
		t.Fatal("ordinary Running retirement accepted kernel absence without exact Wait")
	}
	// The existing Offline path still prepares without opting in.
	if _, _, err = store.UpdateConvergent(func(reg *coremetadata.Registry) error { recordFixtureWait(t, reg, a.Binding.PaneUID); return nil }); err != nil {
		t.Fatal(err)
	}
	opts.allowHostLost = false
	if got, err := c.prepareOwnedClaudeResume(context.Background(), opts); err != nil || got.State != agentProcessPrepared {
		t.Fatal("ordinary Offline preparation changed", err)
	}
	record, err := c.readDeferredLaunch(a.Binding.AgentUID)
	if err != nil || record.HostLost != nil {
		t.Fatal("ordinary preparation gained host-lost witness", err)
	}
}

func TestHostLostClaudePreparedFailedChildWithPriorHistoryRetries(t *testing.T) {
	c, opts, store, a := hostLostPreparedFixture(t)
	t.Setenv("PMX_TEST_DEFERRED_INTERNAL", "1")
	if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		pane, _ := reg.Pane(a.Binding.PaneUID)
		old := a.Binding
		old.Generation, old.OperationID, old.HostInstanceID = "older-generation", "older-operation", "older-host"
		pane.Status.ProcessSession.TurnID, pane.Status.ProcessSession.Pending = "", nil
		pane.Status.ProcessSession.History = &coremetadata.ProcessResumeHistory{Binding: old, SessionID: "session", InterruptedTurnID: "older-turn"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := c.prepareOwnedClaudeResume(ctx, opts); err != nil {
		t.Fatal(err)
	}
	first := opts
	first.Prompt = processResumeFirstFrame{Kind: "user", Text: "first input"}
	if result, err := c.resumeProcessAgent(ctx, processAgentResumeRequest{options: first}); err == nil {
		result.fail(nil)
		t.Fatal("fixture must fail after child birth")
	}
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(a.Binding.PaneUID)
	s := pane.Status.ProcessSession
	if s.Binding == a.Binding || !coremetadata.MatchesProcessWait(s.Binding, pane.Status.LastTermination) || s.History == nil || s.History.Binding != a.Binding || s.SessionID != "session" {
		t.Fatal("failed child did not retain actual Wait and lost source history", s, pane.Status.LastTermination)
	}
	for range 3 {
		prepared, err := c.prepareOwnedClaudeResume(ctx, opts)
		if err != nil {
			t.Fatal("actual failed writer cannot prepare retry", err)
		}
		if prepared.Prepared == nil || prepared.Prepared.Conversation != "session" {
			t.Fatal("retry lost conversation", prepared)
		}
	}
	root := os.Getenv("HOME")
	trace := filepath.Join(root, "retry-wire.json")
	script := filepath.Join(root, "retry-provider.py")
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
	first.Prompt.Text = "retry input exact"
	result, err := c.resumeProcessAgent(ctx, processAgentResumeRequest{options: first})
	if err != nil {
		t.Fatal("prepared retry did not start", err)
	}
	defer func() {
		end, stop := context.WithCancel(context.Background())
		stop()
		_, _ = result.owner.waitProcessAgent(end, nil)
	}()
	raw, err := os.ReadFile(trace)
	if err != nil || !bytes.Contains(raw, []byte("--resume")) || !bytes.Contains(raw, []byte("session")) || !bytes.Contains(raw, []byte(first.Prompt.Text)) {
		t.Fatal("retry did not use same session and input", string(raw), err)
	}
	reg, err = store.LoadReadOnly()
	pane, _ = reg.Pane(a.Binding.PaneUID)
	if err != nil || pane.Status.ProcessSession.SessionID != "session" {
		t.Fatal("retry replaced conversation", err)
	}
	if record, err := c.readDeferredLaunch(a.Binding.AgentUID); err != nil || record != nil {
		t.Fatal("retry did not consume Prepared", err)
	}
}
