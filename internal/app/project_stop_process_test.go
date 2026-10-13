package app

import (
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
	return func(ctx context.Context, store *resourceStore, uid string) ([]cli.ReceiptResource, error) {
		var mutation sync.Mutex
		copied := *store
		root := t.TempDir()
		copied.stateDir = func() (string, error) { return root, nil }
		originalLoad := store.load
		copied.load = func() (coremetadata.Registry, error) { mutation.Lock(); defer mutation.Unlock(); return originalLoad() }
		store = &copied
		return stopProjectProcessAgentsWith(ctx, store, uid, func(_ context.Context, _ coremetadata.Registry, agent coremetadata.Agent, _, action, _ string) (string, error) {
			mutation.Lock()
			defer mutation.Unlock()
			if action != "stop" {
				t.Fatalf("action=%s", action)
			}
			if failure == "reject" && agent.Metadata.UID == "agt-alpha-review" {
				return "", errors.New("injected stop rejection")
			}
			if failure != "timeout" || agent.Metadata.UID != "agt-alpha-review" {
				_, err := store.update(func(reg *coremetadata.Registry) error { recordFixtureWait(t, reg, agent.Status.PaneRef); return nil })
				return "op-stop", err
			}
			return "op-stop", nil
		}, func(int) (coremetadata.ProcessIdentity, int, error) {
			return coremetadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
		}, 50*time.Millisecond)
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
	cmd.switcher.stopProcessAgents = stopProcessFixture(t, "")
	out, _, err := runRoute(t, cmd, "project", "uid:prj-alpha", "-o", "receipt")
	if err != nil || !strings.Contains(out, agentUID) || !strings.Contains(out, "stopped") {
		t.Fatalf("out=%s err=%v", out, err)
	}
	pane, _ := backing.registry.Pane(paneUID)
	agent, _ := backing.registry.Agent(agentUID)
	if agent.Status.Phase != coremetadata.PhaseOffline || pane.Status.ProcessSession.ResumeState != coremetadata.ProcessResumable || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
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
			if fail {
				expectedProject = cli.ActionUnchanged
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
