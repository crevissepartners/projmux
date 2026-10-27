package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func addProjectStopProcessFixture(t *testing.T, backing *fakeResourceStore) (string, string) {
	t.Helper()
	addSecondRuntimeStopAgent(t, backing)
	pane, _ := backing.registry.Pane("pan-alpha-reviewer")
	agent, _ := backing.registry.Agent("agt-alpha-review")
	binding := coremetadata.ProcessBinding{HostInstanceID: "host", ProjectUID: "prj-alpha", WindowUID: agent.Metadata.OwnerUID(), AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: "gen-process", OperationID: "op-process"}
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{Kind: coremetadata.RuntimeProcess, Generation: binding.Generation, AgentUID: agent.Metadata.UID, OperationID: binding.OperationID, Process: &coremetadata.ProcessActivation{Binding: binding, HostProcess: coremetadata.ProcessIdentity{PID: 11, OwnerUID: uint32(os.Getuid()), Start: "host-start"}, Child: coremetadata.ProcessIdentity{PID: 12, OwnerUID: uint32(os.Getuid()), Start: "child-start"}}}
	pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: agent.Spec.Provider, Binding: binding, ThreadID: "thread", ConnectionID: binding.OperationID, ResumeState: coremetadata.ProcessResumeUnknown}
	agent.Status.Phase = coremetadata.PhaseRunning
	return agent.Metadata.UID, pane.Metadata.UID
}

func addSecondProjectStopProcessFixture(t *testing.T, backing *fakeResourceStore) (string, string) {
	t.Helper()
	agent, _ := backing.registry.Agent("agt-alpha-review")
	pane, _ := backing.registry.Pane("pan-alpha-reviewer")
	second := agent.Clone()
	second.Metadata.UID = "agt-process-two"
	second.Metadata.Name = "process-two"
	second.Status.PaneRef = "pan-process-two"
	secondPane := pane.Clone()
	secondPane.Metadata.UID = second.Status.PaneRef
	secondPane.Metadata.Name = "process-two-pane"
	secondPane.Metadata.OwnerRef.UID = second.Metadata.UID
	b := &secondPane.Status.ProcessSession.Binding
	b.AgentUID = second.Metadata.UID
	b.PaneUID = secondPane.Metadata.UID
	secondPane.Status.Activation.AgentUID = second.Metadata.UID
	secondPane.Status.Activation.Process.Binding = *b
	backing.registry.Agents = append(backing.registry.Agents, second)
	backing.registry.Panes = append(backing.registry.Panes, secondPane)
	backing.registry.NameReservations = append(backing.registry.NameReservations,
		coremetadata.NameReservation{Scope: secondPane.Status.ProcessSession.Binding.ProjectUID, Kind: coremetadata.KindAgent, Name: second.Metadata.Name, UID: second.Metadata.UID},
		coremetadata.NameReservation{Scope: secondPane.Status.ProcessSession.Binding.ProjectUID, Kind: coremetadata.KindPane, Name: secondPane.Metadata.Name, UID: secondPane.Metadata.UID})
	if err := backing.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	return second.Metadata.UID, secondPane.Metadata.UID
}

func stopProcessFixture(t *testing.T, failure string) projectProcessStopper {
	t.Helper()
	return func(ctx context.Context, store *resourceStore, uid string) ([]cli.ReceiptResource, error) {
		var mutation sync.Mutex
		copied := *store
		root := t.TempDir()
		copied.stateDir = func() (string, error) { return root, nil }
		originalLoad, originalUpdate, originalConvergent := store.load, store.update, store.updateConvergent
		originalContext := store.updateConvergentContext
		copied.load = func() (coremetadata.Registry, error) {
			mutation.Lock()
			defer mutation.Unlock()
			reg, err := originalLoad()
			return reg.Clone(), err
		}
		copied.update = func(apply func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
			mutation.Lock()
			defer mutation.Unlock()
			reg, err := originalUpdate(apply)
			return reg.Clone(), err
		}
		if originalConvergent != nil {
			copied.updateConvergent = func(apply func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
				mutation.Lock()
				defer mutation.Unlock()
				reg, changed, err := originalConvergent(apply)
				return reg.Clone(), changed, err
			}
		}
		if originalContext != nil {
			copied.updateConvergentContext = func(ctx context.Context, apply func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
				mutation.Lock()
				defer mutation.Unlock()
				if err := ctx.Err(); err != nil { return coremetadata.Registry{}, false, err }
				reg, changed, err := originalContext(ctx, apply)
				return reg.Clone(), changed, err
			}
		}
		store = &copied
		reg, err := store.load()
		if err != nil {
			t.Fatal(err)
		}
		type retirement struct {
			activation              coremetadata.ProcessActivation
			agent                   coremetadata.Agent
			pane                    coremetadata.Pane
			published               bool
			hostAbsent, childAbsent bool
		}
		prepared := map[string]*retirement{}
		path := intmetadata.PathFor(root)
		for _, target := range runningProjectProcessAgents(reg, uid) {
			if target.pane.Status.Activation.Process == nil {
				t.Fatal("fixture lacks exact process activation")
			}
			activation := *target.pane.Status.Activation.Process
			private := reg.Clone()
			recordFixtureWait(t, &private, target.pane.Metadata.UID)
			agent, _ := private.Agent(target.agent.Metadata.UID)
			pane, _ := private.Pane(target.pane.Metadata.UID)
			// Disk preparation is not part of the Stop deadline. Registry Wait
			// publication still belongs exclusively to the admitted callback.
			if err := mutationruntime.WriteProcessRetirement(path, activation, pane.Status.LastTermination); err != nil {
				t.Fatal(err)
			}
			prepared[target.agent.Metadata.UID] = &retirement{activation: activation, agent: agent.Clone(), pane: pane.Clone()}
		}
		// Successful receipts use a hang watchdog. Only the missing-Wait
		// injection has the short shared deadline; dedicated deadline tests
		// below continue to check admission and batch timing independently.
		budget := 5 * time.Second
		if failure == "timeout" {
			budget = 50 * time.Millisecond
		}
		stopped, err := stopProjectProcessAgentsWith(ctx, store, uid, func(_ context.Context, current coremetadata.Registry, agent coremetadata.Agent, _, action, _ string) (string, error) {
			mutation.Lock()
			defer mutation.Unlock()
			r := prepared[agent.Metadata.UID]
			pane, ok := current.Pane(agent.Status.PaneRef)
			if action != "stop" || r == nil || !ok || pane.Status.ProcessSession.Binding != r.activation.Binding || pane.Status.LastTermination != nil || r.published {
				return "", errors.New("fixture Stop admission binding or Wait order mismatch")
			}
			if failure == "reject" && agent.Metadata.UID == "agt-alpha-review" {
				return "", errors.New("injected stop rejection")
			}
			if failure != "timeout" || agent.Metadata.UID != "agt-alpha-review" {
				// mutation is already held by this admitted fixture callback.
				_, err := originalUpdate(func(reg *coremetadata.Registry) error {
					return (coremetadata.Mutator{}).RecordProcessWait(reg, r.activation, *r.pane.Status.LastTermination)
				})
				r.published = err == nil
				return "op-stop", err
			}
			return "op-stop", nil
		}, func(pid int) (coremetadata.ProcessIdentity, int, error) {
			mutation.Lock()
			defer mutation.Unlock()
			current, err := originalLoad()
			if err != nil {
				return coremetadata.ProcessIdentity{}, 0, err
			}
			matched := false
			for _, r := range prepared {
				if !r.published || (pid != r.activation.HostProcess.PID && pid != r.activation.Child.PID) {
					continue
				}
				agent, hasAgent := current.Agent(r.activation.Binding.AgentUID)
				pane, hasPane := current.Pane(r.activation.Binding.PaneUID)
				if !hasAgent || !hasPane || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != r.activation.Binding || !coremetadata.SameProcessWait(r.agent.Status.LastTermination, agent.Status.LastTermination) || !coremetadata.SameProcessWait(r.pane.Status.LastTermination, pane.Status.LastTermination) {
					return coremetadata.ProcessIdentity{}, 0, errors.New("fixture identity checked without published exact Wait")
				}
				witness, err := mutationruntime.ReadProcessRetirement(path, r.activation.Binding, r.pane.Status.LastTermination)
				if err != nil || !reflect.DeepEqual(witness, r.activation) {
					return coremetadata.ProcessIdentity{}, 0, errors.New("fixture identity checked without exact durable witness")
				}
				matched = true
				if pid == r.activation.HostProcess.PID {
					r.hostAbsent = true
				} else {
					r.childAbsent = true
				}
			}
			if !matched {
				return coremetadata.ProcessIdentity{}, 0, errors.New("fixture identity checked before admitted Wait publication")
			}
			return coremetadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
		}, budget)
		if failure == "timeout" {
			var targetFailure *mutationruntime.ProjectProcessStopFailure
			var waitFailure *mutationruntime.ProcessStopFailure
			if !errors.As(err, &targetFailure) || targetFailure.AgentUID != "agt-alpha-review" || !errors.As(targetFailure, &waitFailure) || waitFailure.Code != "agent-stop-wait-unconfirmed" || !errors.Is(waitFailure, context.DeadlineExceeded) {
				t.Fatalf("missing Wait did not produce the exact typed deadline failure: %v", err)
			}
		}
		latest, loadErr := store.load()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		for _, r := range prepared {
			pane, ok := latest.Pane(r.pane.Metadata.UID)
			// Actual Wait can succeed while final context admission fails.
			marked := r.published && failure != "missing-context"
			if !ok || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.StopRequested != marked {
				t.Fatalf("explicit stop mark disagrees with confirmed retirement: %+v", pane)
			}
		}
		for _, receipt := range stopped {
			r := prepared[receipt.UID]
			if r == nil || !r.published || !r.hostAbsent || !r.childAbsent {
				t.Fatalf("receipt precedes exact Wait, witness, or host/child absence: %+v", receipt)
			}
		}
		return stopped, err
	}
}

func TestProjectStopServiceNeverPrewritesProcessPanes(t *testing.T) {
	for _, kind := range []coremetadata.RuntimeKind{coremetadata.RuntimeProcess, ""} {
		backing := freshStartFixtureStore(t)
		activateRuntimeStopAgent(t, backing, "gen-tmux")
		agentUID, paneUID := addProjectStopProcessFixture(t, backing)
		pane, _ := backing.registry.Pane(paneUID)
		if kind == "" {
			pane.Status.ProcessSession = nil
		}
		before := pane.Clone()
		interrupted, err := recordProjectStopInterruptions(backing.store(), "prj-alpha", "op-stop")
		if err != nil || len(interrupted) != 1 || interrupted[0].AgentUID == agentUID {
			t.Fatalf("selected=%+v err=%v", interrupted, err)
		}
		if err := mutationruntime.VerifyProjectStopInterruptions(stopServiceStore(backing.store()), "prj-alpha", interrupted, "op-stop"); err != nil {
			t.Fatal(err)
		}
		if err := projectProjectStopInterruptions(backing.store(), interrupted, "op-stop"); err != nil {
			t.Fatal(err)
		}
		pane, _ = backing.registry.Pane(paneUID)
		agent, _ := backing.registry.Agent(agentUID)
		if !reflect.DeepEqual(before, *pane) || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.LastTermination != nil {
			t.Fatal("process record changed")
		}
	}
}

func TestProjectStopHeadlessOnlyAndReceipt(t *testing.T) {
	cmd, backing, _ := lifecycleVerbFixture(t, projectLifecycleStop, false, nil)
	activateRuntimeStopAgent(t, backing, "gen-tmux")
	agentUID, paneUID := addProjectStopProcessFixture(t, backing)
	before, _ := backing.registry.Pane(paneUID)
	binding := before.Status.ProcessSession.Binding
	cmd.switcher.stopProcessAgents = stopProcessFixture(t, "")
	out, _, err := runRoute(t, cmd, "project", "uid:prj-alpha", "-o", "receipt")
	if err != nil || !strings.Contains(out, agentUID) || !strings.Contains(out, "stopped") {
		t.Fatalf("out=%s err=%v", out, err)
	}
	var receipt cli.OperationReceipt
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Effects.Runtime != "agents-stopped" || len(receipt.AffectedUIDs) != 2 || receipt.AffectedUIDs[0].Action != cli.ActionUnchanged || receipt.AffectedUIDs[1].Action != cli.ActionStopped {
		t.Fatalf("process-only receipt overstates Project stop: %s", out)
	}
	pane, _ := backing.registry.Pane(paneUID)
	agent, _ := backing.registry.Agent(agentUID)
	if pane.Status.ProcessSession.Binding != binding || !coremetadata.SameProcessWait(agent.Status.LastTermination, pane.Status.LastTermination) || agent.Status.Phase != coremetadata.PhaseOffline || pane.Status.ProcessSession.ResumeState != coremetadata.ProcessResumable || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
		t.Fatalf("agent=%+v pane=%+v", agent.Status, pane.Status)
	}
}

type projectStopRouteFixture struct {
	switcher                                         *switchCommand
	runner                                           *sidebarPopupStopRunner
	backing                                          *fakeResourceStore
	session, agentUID, paneUID, goodUID, goodPaneUID string
}

func newProjectStopRouteFixture(t *testing.T) projectStopRouteFixture {
	t.Helper()
	switcher, runner, executor := sidebarPopupStopFixture(t, "/src/alpha", "/tmp/fake-tmux/primary", "4242", "%9", nil)
	bindSidebarPopupManagedRow(t, switcher, runner, "/src/alpha")
	session := runner.sessionName
	executor.exists[session] = true
	backing := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, backing, "gen-tmux")
	agentUID, paneUID := addProjectStopProcessFixture(t, backing)
	goodUID, goodPaneUID := addSecondProjectStopProcessFixture(t, backing)
	for i := range backing.registry.Projects {
		if backing.registry.Projects[i].Metadata.UID == "prj-alpha" {
			backing.registry.Projects[i].Metadata.UID = runtimeFixtureProject
		}
	}
	for i := range backing.registry.Windows {
		if backing.registry.Windows[i].Metadata.OwnerUID() == "prj-alpha" {
			backing.registry.Windows[i].Metadata.OwnerRef.UID = runtimeFixtureProject
		}
	}
	for i := range backing.registry.NameReservations {
		if backing.registry.NameReservations[i].Scope == "prj-alpha" {
			backing.registry.NameReservations[i].Scope = runtimeFixtureProject
		}
		if backing.registry.NameReservations[i].UID == "prj-alpha" {
			backing.registry.NameReservations[i].UID = runtimeFixtureProject
		}
	}
	for i := range backing.registry.Panes {
		processPane := &backing.registry.Panes[i]
		if processPane.Status.ProcessSession != nil {
			processPane.Status.ProcessSession.Binding.ProjectUID = runtimeFixtureProject
			processPane.Status.Activation.Process.Binding.ProjectUID = runtimeFixtureProject
		}
	}
	switcher.navigation.reader.reader.loadRegistry = backing.store().load
	switcher.managedStopStore = backing.store()
	switcher.stopProcessAgents = stopProcessFixture(t, "")
	oldEnv := switcher.lookupEnv
	switcher.lookupEnv = func(key string) string {
		if key == "TMUX_PANE" {
			return "%9"
		}
		return oldEnv(key)
	}
	return projectStopRouteFixture{switcher, runner, backing, session, agentUID, paneUID, goodUID, goodPaneUID}
}

func TestProjectStopMixedSidebarAndPickerWaitAndPartialFailure(t *testing.T) {
	for _, surface := range []string{"cli", "sidebar", "picker"} {
		for _, failure := range []string{"", "reject", "timeout"} {
			t.Run(surface+"/"+failure, func(t *testing.T) {
				f := newProjectStopRouteFixture(t)
				switcher, runner, backing, session := f.switcher, f.runner, f.backing, f.session
				agentUID, paneUID, goodUID, goodPaneUID := f.agentUID, f.paneUID, f.goodUID, f.goodPaneUID
				before, _ := backing.registry.Pane(paneUID)
				beforeCopy := before.Clone()
				switcher.stopProcessAgents = stopProcessFixture(t, failure)
				var err error
				var out string
				switch surface {
				case "sidebar":
					err = switcher.stopManagedProjectSession(context.Background(), "uid:"+runtimeFixtureProject, session, "", "%9")
				case "cli":
					oldEnv := switcher.lookupEnv
					switcher.lookupEnv = func(k string) string {
						if k == "TMUX_PANE" {
							return "%9"
						}
						return oldEnv(k)
					}
					cmd := &projectLifecycleCommand{verb: projectLifecycleStop, store: backing.store(), switcher: switcher, lookupEnv: switcher.lookupEnv}
					out, _, err = runRoute(t, cmd, "project", "uid:"+runtimeFixtureProject, "-o", "receipt")
				case "picker":
					picker := &sessionsCommand{mutationRunner: runner, lookupEnv: func(k string) string {
						if k == "TMUX_PANE" {
							return "%9"
						}
						return switcher.lookupEnv(k)
					}, managedStopStore: backing.store(), stopProcessAgents: switcher.stopProcessAgents, navigation: switcher.navigation.reader, recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) { return nil, nil })}
					attribution := sessionsAttribution{resolved: true, byID: map[string]managedSessionAttribution{runner.sessionID: {ResourceKind: "Project", ResourceUID: runtimeFixtureProject}}}
					_, err = picker.killFocusedSession(context.Background(), []inttmux.RecentSessionSummary{{ID: runner.sessionID, Name: session}}, attribution, session)
				}
				good, _ := backing.registry.Agent(goodUID)
				goodPane, _ := backing.registry.Pane(goodPaneUID)
				if good.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(goodPane.Status.ProcessSession.Binding, goodPane.Status.LastTermination) {
					t.Fatal("other headless Agent was not stopped")
				}
				if surface == "cli" && !strings.Contains(out, goodUID) {
					t.Fatalf("successful headless Agent missing from receipt: %s", out)
				}
				if !runner.killed {
					t.Fatalf("tmux not stopped: %v", err)
				}
				pane, _ := backing.registry.Pane(paneUID)
				agent, _ := backing.registry.Agent(agentUID)
				tmuxAgent, _ := backing.registry.Agent("agt-alpha-codex")
				if tmuxAgent.Status.Phase != coremetadata.PhaseOffline {
					t.Fatal("tmux agent not Offline")
				}
				if failure == "" {
					if err != nil || agent.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
						t.Fatalf("err=%v phase=%s", err, agent.Status.Phase)
					}
					if surface == "cli" && !strings.Contains(out, agentUID) {
						t.Fatalf("receipt=%s", out)
					}
				} else if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), projectStopAgentsStillRunning) || !strings.Contains(err.Error(), agentUID) || !strings.Contains(err.Error(), agent.Metadata.Name) || agent.Status.Phase != coremetadata.PhaseRunning || !reflect.DeepEqual(beforeCopy, *pane) {
					t.Fatalf("partial failure err=%v phase=%s", err, agent.Status.Phase)
				}
			})
		}
	}
}

func TestProjectStopDispatchesAllStopsBeforeSharedWait(t *testing.T) {
	backing := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, backing, "gen-tmux")
	addProjectStopProcessFixture(t, backing)
	addSecondProjectStopProcessFixture(t, backing)
	var lock sync.Mutex
	raw := projectStopFixtureStore(t, backing)
	store := *raw
	store.load = func() (coremetadata.Registry, error) { lock.Lock(); defer lock.Unlock(); return raw.load() }
	admitted := make(chan struct{}, 2)
	var count atomic.Int32
	all := make(chan struct{})
	stop := func(_ context.Context, _ coremetadata.Registry, agent coremetadata.Agent, _, _, _ string) (string, error) {
		admitted <- struct{}{}
		if count.Add(1) == 2 {
			close(all)
		}
		select {
		case <-all:
		case <-time.After(time.Second):
			return "", errors.New("Stops dispatched serially")
		}
		return "op", nil
	}
	start := time.Now()
	_, err := stopProjectProcessAgentsWith(context.Background(), &store, "prj-alpha", stop, func(int) (coremetadata.ProcessIdentity, int, error) {
		return coremetadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
	}, 80*time.Millisecond)
	if len(admitted) != 2 || err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("admitted=%d duration=%s err=%v", len(admitted), time.Since(start), err)
	}
}

func TestProjectStopWaitDeadlineIncludesControlAdmission(t *testing.T) {
	backing := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, backing, "gen-tmux")
	addProjectStopProcessFixture(t, backing)
	const timeout = 200 * time.Millisecond
	var admitted time.Time
	stop := func(context.Context, coremetadata.Registry, coremetadata.Agent, string, string, string) (string, error) {
		timer := time.NewTimer(timeout / 2)
		defer timer.Stop()
		<-timer.C
		admitted = time.Now()
		return "op-stop", nil
	}
	_, err := stopProjectProcessAgentsWith(context.Background(), projectStopFixtureStore(t, backing), "prj-alpha", stop, func(int) (coremetadata.ProcessIdentity, int, error) {
		return coremetadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
	}, timeout)
	if err == nil || !strings.Contains(err.Error(), "durable Wait unconfirmed") {
		t.Fatalf("err=%v", err)
	}
	if afterAdmission := time.Since(admitted); afterAdmission >= 3*timeout/4 {
		t.Fatalf("Wait restarted the deadline after Stop admission: %s", afterAdmission)
	}
}

type projectStopKillFailureRunner struct {
	base *sidebarPopupStopRunner
	fail bool
}

func (r *projectStopKillFailureRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.fail && len(args) > 2 && args[2] == "kill-session" {
		return nil, errors.New("injected tmux kill failure")
	}
	return r.base.Run(ctx, name, args...)
}

func TestProjectStopConfirmedAgentReceiptsSurviveTmuxFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "tmux success", true: "tmux failure"}[fail], func(t *testing.T) {
			f := newProjectStopRouteFixture(t)
			f.switcher.tmuxRunner = &projectStopKillFailureRunner{base: f.runner, fail: fail}
			stopped, tmuxStopped, err := f.switcher.stopManagedProjectSessionResult(context.Background(), "uid:"+runtimeFixtureProject, f.session, "", "%9", nil)
			if len(stopped) != 2 || tmuxStopped == fail || (err != nil) != fail {
				t.Fatalf("stopped=%+v tmux=%t err=%v", stopped, tmuxStopped, err)
			}
			for _, result := range stopped {
				a, _ := f.backing.registry.Agent(result.UID)
				p, _ := processResumePane(f.backing.registry, result.UID)
				if a.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(p.Status.ProcessSession.Binding, p.Status.LastTermination) {
					t.Fatal("fixture lacks confirmed Wait")
				}
			}
			f = newProjectStopRouteFixture(t)
			f.switcher.tmuxRunner = &projectStopKillFailureRunner{base: f.runner, fail: fail}
			cmd := &projectLifecycleCommand{verb: projectLifecycleStop, store: f.backing.store(), switcher: f.switcher, lookupEnv: f.switcher.lookupEnv}
			out, _, err := runRoute(t, cmd, "project", "uid:"+runtimeFixtureProject, "-o", "receipt")
			var receipt cli.OperationReceipt
			if json.Unmarshal([]byte(out), &receipt) != nil || receipt.Validate() != nil || (err != nil) != fail {
				t.Fatalf("out=%s err=%v", out, err)
			}
			actions := map[string]cli.ReceiptAction{}
			for _, r := range receipt.AffectedUIDs {
				actions[r.UID] = r.Action
			}
			if actions[f.agentUID] != cli.ActionStopped || actions[f.goodUID] != cli.ActionStopped {
				t.Fatalf("confirmed stops missing: %s", out)
			}
			expectedProject := cli.ActionStopped
			expectedRuntime := cli.RuntimeEffect("stopped")
			if fail {
				expectedProject = cli.ActionUnchanged
				expectedRuntime = "partially-stopped"
			}
			if receipt.Effects.Runtime != expectedRuntime {
				t.Fatalf("runtime=%s, want %s: %s", receipt.Effects.Runtime, expectedRuntime, out)
			}
			if actions[runtimeFixtureProject] != expectedProject || (fail && receipt.Effects.Focus != cli.FocusUnchanged) {
				t.Fatalf("false Project stop/focus: %s", out)
			}
			if f.runner.killed == fail {
				t.Fatal("tmux failure control was not injected")
			}
		})
	}
}

func TestProjectStopControlAdmissionReceivesSharedDeadline(t *testing.T) {
	backing := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, backing, "gen-tmux")
	addProjectStopProcessFixture(t, backing)
	addSecondProjectStopProcessFixture(t, backing)
	var deadlines sync.Map
	stop := func(ctx context.Context, _ coremetadata.Registry, agent coremetadata.Agent, _, _, _ string) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("host control has no shared deadline")
			return "", errors.New("missing deadline")
		}
		deadlines.Store(agent.Metadata.UID, deadline)
		<-ctx.Done()
		return "", ctx.Err()
	}
	start := time.Now()
	_, err := stopProjectProcessAgentsWith(context.Background(), projectStopFixtureStore(t, backing), "prj-alpha", stop, func(int) (coremetadata.ProcessIdentity, int, error) {
		return coremetadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
	}, 50*time.Millisecond)
	a, okA := deadlines.Load("agt-alpha-review")
	b, okB := deadlines.Load("agt-process-two")
	if !okA || !okB || a != b || err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("deadlines=%v/%v elapsed=%s err=%v", a, b, time.Since(start), err)
	}
}

func TestProjectStopHostControlRPCUsesParentDeadline(t *testing.T) {
	reg, agentUID, paneUID := processDeleteFixture(t, aiModeCodex)
	pane, _ := reg.Pane(paneUID)
	pane.Status.ProcessSession.ConnectionID = pane.Status.ProcessSession.Binding.OperationID
	pane.Status.ProcessSession.ThreadID = "thread"
	host, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pane.Status.Activation.Process.HostProcess = host
	paths := config.Paths{StateDir: filepath.Join(t.TempDir(), "state")}
	socket := processHostSocket(aiModeCodex, intmetadata.PathFor(paths.StateDir), paneUID, pane.Status.ProcessSession.Binding.Generation)
	listener, closeLease, err := listenProcessHost(socket)
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	accepted := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Unix.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-release
	}()
	defer func() {
		close(release)
		if err := closeLease(context.Background()); err != nil {
			t.Error(err)
		}
		<-done
	}()
	command := newAgentCommand()
	command.controlPaths = func() (config.Paths, error) { return paths, nil }
	command.controlTimeout = time.Second
	parent, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	agent, _ := reg.Agent(agentUID)
	start := time.Now()
	_, err = command.callProcessTurnContext(parent, *reg, *agent, aiModeCodex, "stop", "")
	select {
	case <-accepted:
	default:
		t.Fatalf("control never reached fixture IPC: %v", err)
	}
	if err == nil || time.Since(start) > 700*time.Millisecond {
		t.Fatalf("RPC ignored shared parent deadline: elapsed=%s err=%v", time.Since(start), err)
	}
}

func projectStopFixtureStore(t *testing.T, backing *fakeResourceStore) *resourceStore {
	t.Helper()
	s := backing.store()
	root := t.TempDir()
	s.stateDir = func() (string, error) { return root, nil }
	return s
}

// Pin each partial-result direction and both receipt projections at the CLI boundary.
func TestProjectStopReceiptOutcomesAndProjections(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		live, failTmux, failAgent bool
		runtime                   cli.RuntimeEffect
		project                   cli.ReceiptAction
	}{
		{"all stopped", true, false, false, cli.RuntimeStopped, cli.ActionStopped},
		{"Agents stopped tmux failed", true, true, false, cli.RuntimePartiallyStopped, cli.ActionUnchanged},
		{"tmux stopped Agent unconfirmed", true, false, true, cli.RuntimePartiallyStopped, cli.ActionStopped},
		{"Agents only", false, false, false, cli.RuntimeAgentsStopped, cli.ActionUnchanged},
		{"Agents only partial", false, false, true, cli.RuntimePartiallyStopped, cli.ActionUnchanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProjectStopRouteFixture(t)
			f.switcher.tmuxRunner = &projectStopKillFailureRunner{base: f.runner, fail: tc.failTmux}
			failure := ""
			if tc.failAgent {
				failure = "reject"
			}
			f.switcher.stopProcessAgents = stopProcessFixture(t, failure)
			cmd := &projectLifecycleCommand{verb: projectLifecycleStop, store: f.backing.store(), switcher: f.switcher, lookupEnv: f.switcher.lookupEnv}
			project, _ := f.backing.registry.Project(runtimeFixtureProject)
			r, err := cmd.execute(context.Background(), canonicalStopProject, *project, project.Spec.Root, f.session, tc.live, nil)
			if (err != nil) != (tc.failAgent || tc.failTmux) || r.Effects.Runtime != tc.runtime || r.AffectedUIDs[0].Action != tc.project {
				t.Fatalf("receipt=%+v err=%v", r, err)
			}
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
			wantAgents := 2
			if tc.failAgent {
				wantAgents--
			}
			if len(r.AffectedUIDs) != 1+wantAgents {
				t.Fatalf("affected=%+v", r.AffectedUIDs)
			}
			for _, row := range r.AffectedUIDs[1:] {
				if row.Kind != "Agent" || row.Action != cli.ActionStopped {
					t.Fatalf("Agent row=%+v", row)
				}
			}
			if tc.failAgent && !strings.Contains(err.Error(), projectStopAgentsStillRunning) {
				t.Fatal(err)
			}
			if tc.failTmux && !strings.Contains(err.Error(), "injected tmux kill failure") {
				t.Fatal(err)
			}
			if err != nil && exitCodeOf(err) != 1 {
				t.Fatalf("exit=%d", exitCodeOf(err))
			}
			if !tc.live && f.runner.killed {
				t.Fatal("process-only stop killed a session")
			}
			var human, encoded bytes.Buffer
			if err := writeOperationReceipt(&human, cli.OutputModeDefault, r); err != nil {
				t.Fatal(err)
			}
			if err := writeOperationReceipt(&encoded, cli.OutputModeReceipt, r); err != nil {
				t.Fatal(err)
			}
			var decoded cli.OperationReceipt
			if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			var projected bytes.Buffer
			if err := decoded.WriteHuman(&projected); err != nil {
				t.Fatal(err)
			}
			if human.String() != projected.String() || !strings.Contains(human.String(), "runtime="+string(tc.runtime)) {
				t.Fatalf("human/JSON differ: %s / %s", human.String(), encoded.String())
			}
		})
	}
}

func TestProjectStopExplicitMarkUsesSameStore(t *testing.T) {
	for _, mode := range []string{"convergent", "context-without-legacy-convergent"} {
		t.Run(mode, func(t *testing.T) {
			backing := freshStartFixtureStore(t)
			activateRuntimeStopAgent(t, backing, "gen-tmux")
			agentUID, paneUID := addProjectStopProcessFixture(t, backing)
			store := backing.store()
			if mode == "context-without-legacy-convergent" {
				store.updateConvergent = nil
			}
			stopped, err := stopProcessFixture(t, "")(context.Background(), store, "prj-alpha")
			if err != nil || len(stopped) != 1 || stopped[0].UID != agentUID {
				t.Fatalf("same-store stop: %+v %v", stopped, err)
			}
			pane, _ := backing.registry.Pane(paneUID)
			if !pane.Status.ProcessSession.StopRequested {
				t.Fatal("final metadata write bypassed the caller's Registry")
			}
		})
	}
}

func TestProjectStopContextWriterDoesNotFallBack(t *testing.T) {
	backing := freshStartFixtureStore(t)
	activateRuntimeStopAgent(t, backing, "gen-tmux")
	agentUID, paneUID := addProjectStopProcessFixture(t, backing)
	store := backing.store()
	store.updateConvergentContext = nil
	_, err := stopProcessFixture(t, "missing-context")(context.Background(), store, "prj-alpha")
	if err == nil || !strings.Contains(err.Error(), "context write store is not configured") {
		t.Fatalf("context-free writer accepted: %v", err)
	}
	pane, _ := backing.registry.Pane(paneUID)
	agent, _ := backing.registry.Agent(agentUID)
	if pane.Status.ProcessSession.StopRequested || agent.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
		t.Fatal("failed final writer changed intent or lost actual retirement")
	}
}
