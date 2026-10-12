package app

import (
	"context"
	"errors"
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
	pane.Status.Activation = coremetadata.PaneActivation{Kind: coremetadata.RuntimeProcess, Generation: binding.Generation, AgentUID: agent.Metadata.UID, OperationID: binding.OperationID, Process: &coremetadata.ProcessActivation{Binding: binding, HostProcess: coremetadata.ProcessIdentity{PID: 11, OwnerUID: 1000, Start: "host-start"}, Child: coremetadata.ProcessIdentity{PID: 12, OwnerUID: 1000, Start: "child-start"}}}
	pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: agent.Spec.Provider, Binding: binding, ThreadID: "thread", ConnectionID: binding.OperationID, ResumeState: coremetadata.ProcessResumeUnknown}
	agent.Status.Phase = coremetadata.PhaseRunning
	return agent.Metadata.UID, pane.Metadata.UID
}

func stopProcessFixture(t *testing.T, failure string) projectProcessStopper {
	return func(ctx context.Context, store *resourceStore, uid string) ([]cli.ReceiptResource, error) {
		return stopProjectProcessAgentsWith(ctx, store, uid, func(_ coremetadata.Registry, agent coremetadata.Agent, _, action, _ string) (string, error) {
			if action != "stop" {
				t.Fatalf("action=%s", action)
			}
			if failure == "reject" {
				return "", errors.New("injected stop rejection")
			}
			if failure != "timeout" {
				_, err := store.update(func(reg *coremetadata.Registry) error { recordFixtureWait(t, reg, agent.Status.PaneRef); return nil })
				return "op-stop", err
			}
			return "op-stop", nil
		}, func(int) (coremetadata.ProcessIdentity, int, error) {
			return coremetadata.ProcessIdentity{}, 0, errors.New("host exited")
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

func TestProjectStopMixedSidebarAndPickerWaitAndPartialFailure(t *testing.T) {
	for _, surface := range []string{"cli", "sidebar", "picker"} {
		for _, failure := range []string{"", "reject", "timeout"} {
			t.Run(surface+"/"+failure, func(t *testing.T) {
				switcher, runner, executor := sidebarPopupStopFixture(t, "/src/alpha", "/tmp/fake-tmux/primary", "4242", "%9", nil)
				bindSidebarPopupManagedRow(t, switcher, runner, "/src/alpha")
				session := runner.sessionName
				executor.exists[session] = true
				backing := freshStartFixtureStore(t)
				activateRuntimeStopAgent(t, backing, "gen-tmux")
				agentUID, paneUID := addProjectStopProcessFixture(t, backing)
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
				processPane, _ := backing.registry.Pane(paneUID)
				processPane.Status.ProcessSession.Binding.ProjectUID = runtimeFixtureProject
				processPane.Status.Activation.Process.Binding.ProjectUID = runtimeFixtureProject
				switcher.navigation.reader.reader.loadRegistry = backing.store().load
				before, _ := backing.registry.Pane(paneUID)
				beforeCopy := before.Clone()
				switcher.managedStopStore = backing.store()
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
	_, paneUID := addProjectStopProcessFixture(t, backing)
	agent, _ := backing.registry.Agent("agt-alpha-review")
	pane, _ := backing.registry.Pane(paneUID)
	second := agent.Clone()
	second.Metadata.UID = "agt-process-two"
	second.Metadata.Name = "process-two"
	second.Status.PaneRef = "pan-process-two"
	secondPane := pane.Clone()
	secondPane.Metadata.UID = second.Status.PaneRef
	secondPane.Metadata.OwnerRef.UID = second.Metadata.UID
	b := &secondPane.Status.ProcessSession.Binding
	b.AgentUID = second.Metadata.UID
	b.PaneUID = secondPane.Metadata.UID
	secondPane.Status.Activation.AgentUID = second.Metadata.UID
	secondPane.Status.Activation.Process.Binding = *b
	backing.registry.Agents = append(backing.registry.Agents, second)
	backing.registry.Panes = append(backing.registry.Panes, secondPane)
	var lock sync.Mutex
	raw := backing.store()
	store := *raw
	store.load = func() (coremetadata.Registry, error) { lock.Lock(); defer lock.Unlock(); return raw.load() }
	admitted := make(chan struct{}, 2)
	var count atomic.Int32
	all := make(chan struct{})
	stop := func(_ coremetadata.Registry, agent coremetadata.Agent, _, _, _ string) (string, error) {
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
		return coremetadata.ProcessIdentity{}, 0, errors.New("gone")
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
	stop := func(coremetadata.Registry, coremetadata.Agent, string, string, string) (string, error) {
		timer := time.NewTimer(timeout / 2)
		defer timer.Stop()
		<-timer.C
		admitted = time.Now()
		return "op-stop", nil
	}
	_, err := stopProjectProcessAgentsWith(context.Background(), backing.store(), "prj-alpha", stop, func(int) (coremetadata.ProcessIdentity, int, error) {
		return coremetadata.ProcessIdentity{}, 0, errors.New("gone")
	}, timeout)
	if err == nil || !strings.Contains(err.Error(), "durable Wait unconfirmed") {
		t.Fatalf("err=%v", err)
	}
	if afterAdmission := time.Since(admitted); afterAdmission >= 3*timeout/4 {
		t.Fatalf("Wait restarted the deadline after Stop admission: %s", afterAdmission)
	}
}
