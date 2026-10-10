package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func TestVirtualWindowCreatesOnlyRequestedTmuxPane(t *testing.T) {
	for _, agent := range []bool{false, true} {
		for _, stopped := range []bool{false, true} {
			t.Run(map[bool]string{false: "shell", true: "agent"}[agent]+map[bool]string{false: "/live", true: "/stopped"}[stopped], func(t *testing.T) {
				store := newFakeResourceStore(t)
				tmux := newFakeTmux()
				project := virtualPrimaryProjectFixture(t, store)
				windowUID := project.Spec.PrimaryWindowRef
				oldPane, _ := store.registry.WindowAnchor(windowUID)
				old := oldPane.Clone()
				command, _ := newTestAgentCreateCommand(t, store, tmux)
				if !stopped {
					seedOwnedSession(tmux.addSession("alpha"), project.Metadata.UID, project.Spec.Root)
				}
				args := []string{"pane", "--project", "uid:" + project.Metadata.UID, "--window", "uid:" + windowUID, "--name", "requested"}
				if agent {
					args = append([]string{"agent", "--provider", "codex", "--interactive-only"}, args[1:]...)
				}
				stdout, stderr, err := runRoute(t, command, args...)
				if err != nil {
					t.Fatalf("create: %v stdout=%s stderr=%s", err, stdout, stderr)
				}
				w, _ := store.registry.Window(windowUID)
				if store.registry.IsVirtualWindow(windowUID) || w.Status.RuntimeID == "" || w.Status.RuntimeSessionID == "" {
					t.Fatalf("Window not materialized: %+v", w)
				}
				anchor, _ := store.registry.WindowAnchor(windowUID)
				if anchor == nil || anchor.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeTmux {
					t.Fatal("no tmux anchor")
				}
				retained, _ := store.registry.Pane(old.Metadata.UID)
				if !reflect.DeepEqual(old, *retained) {
					t.Fatal("process Pane changed")
				}
				var count int
				for _, session := range tmux.sessions {
					for _, window := range session.windows {
						if window.opts[tmuxopts.WindowUID] == windowUID {
							count += len(window.panes)
						}
					}
				}
				if count != 1 || virtualTestPaneCount(store.registry, windowUID) != 2 {
					t.Fatalf("runtime Panes=%d Registry Panes=%d", count, virtualTestPaneCount(store.registry, windowUID))
				}
				if agent && w.Spec.DefaultShellPaneRef != "" {
					t.Fatal("Agent create added shell")
				}
				if err := store.registry.Validate(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestVirtualWindowMaterializationRollback(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		for _, failure := range []string{"create", "mirror"} {
			t.Run(map[bool]string{false: "live", true: "stopped"}[stopped]+"/"+failure, func(t *testing.T) {
				store := newFakeResourceStore(t)
				tmux := newFakeTmux()
				project := virtualPrimaryProjectFixture(t, store)
				if !stopped {
					seedOwnedSession(tmux.addSession("alpha"), project.Metadata.UID, project.Spec.Root)
				}
				before := store.registry.Clone()
				command, _ := newTestResourceCreateCommand(t, store, tmux)
				if failure == "create" {
					verb := "new-window"
					if stopped {
						verb = "new-session"
					}
					tmux.fail, tmux.failAfterMutation = []string{verb}, true
				} else {
					tmux.fail = []string{"set-option", tmuxopts.PaneUID}
				}
				_, _, err := runRoute(t, command, "pane", "--project", "uid:"+project.Metadata.UID, "--window", "uid:"+project.Spec.PrimaryWindowRef)
				if err == nil {
					t.Fatal("expected failure")
				}
				if !reflect.DeepEqual(before, store.registry) {
					t.Fatalf("Registry changed after failure: %v", err)
				}
				if len(windowsWithUID(tmux, project.Spec.PrimaryWindowRef)) != 0 {
					t.Fatalf("Window survived rollback: %v", err)
				}
			})
		}
	}
}

func TestFocusVirtualWindowMaterializesAndProcessPaneStillRefuses(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	project := virtualPrimaryProjectFixture(t, store)
	command, _ := newTestResourceCreateCommand(t, store, tmux)
	focus := newFocusCommand()
	focus.runner, focus.lookupEnv, focus.loadRegistry = tmux, func(string) string { return "" }, store.store().load
	focus.materializeVirtualWindow = func(_ context.Context, uid, socket string) error { return command.materializeVirtualShell(uid) }
	_, _, mismatch := focus.resolveUIDNavigation(context.Background(), focusOptions{NavKind: "window", NavRef: "uid:" + project.Spec.PrimaryWindowRef, NavProject: "wrong-project"})
	if mismatch == nil || !store.registry.IsVirtualWindow(project.Spec.PrimaryWindowRef) || len(tmux.sessions) != 0 {
		t.Fatalf("scope mismatch mutated virtual Window: %v", mismatch)
	}
	coordinate, _, err := focus.resolveUIDNavigation(context.Background(), focusOptions{NavKind: "window", NavRef: "uid:" + project.Spec.PrimaryWindowRef})
	if err != nil || !strings.Contains(coordinate, ":@") {
		t.Fatalf("focus resolution=%s %v", coordinate, err)
	}
	window, _ := store.registry.Window(project.Spec.PrimaryWindowRef)
	if window.Spec.DefaultShellPaneRef == "" || virtualTestPaneCount(store.registry, window.Metadata.UID) != 2 {
		t.Fatal("focus did not add exactly one shell")
	}
	process := store.registry.AgentsOf(window.Metadata.UID)[0]
	focus.processRuntime = newProcessPaneRuntime()
	_, _, err = focus.resolveUIDNavigation(context.Background(), focusOptions{NavKind: "pane", NavRef: "uid:" + process.Status.PaneRef})
	if err == nil || !strings.Contains(err.Error(), "process-focus-unsupported") {
		t.Fatalf("process focus: %v", err)
	}
}

func virtualTestPaneCount(reg coremetadata.Registry, windowUID string) int {
	count := 0
	for _, pane := range reg.Panes {
		if _, ok := reg.PaneInWindow(windowUID, pane.Metadata.UID); ok {
			count++
		}
	}
	return count
}

func TestOpenAttachAllVirtualProjectMaterializesPrimary(t *testing.T) {
	for _, route := range []string{"open", "attach"} {
		t.Run(route, func(t *testing.T) {
			live := map[string]bool{}
			verb, store, executor := lifecycleVerbFixture(t, projectLifecycleOpen, true, live)
			project := virtualPrimaryProjectFixture(t, store)
			create, _ := newTestResourceCreateCommand(t, store, newFakeTmux())
			verb.switcher.managedStopStore = store.store()
			verb.switcher.materializeVirtualWindow = func(ctx context.Context, uid string) (virtualWindowShellMaterialization, error) {
				result, err := create.materializeVirtualShellResult(uid)
				if err == nil {
					live["alpha"] = true
				}
				return result, err
			}
			var err error
			var stdout string
			if route == "open" {
				stdout, _, err = runRoute(t, verb, "project", "uid:"+project.Metadata.UID, "-o", "receipt")
			} else {
				attach := &attachCommand{store: store.store(), switcher: verb.switcher, lookupEnv: func(string) string { return "" }}
				_, _, err = runRoute(t, attach, "project", "uid:"+project.Metadata.UID)
			}
			if err != nil {
				t.Fatal(err)
			}
			window, _ := store.registry.Window(project.Spec.PrimaryWindowRef)
			if store.registry.IsVirtualWindow(window.Metadata.UID) || window.Spec.DefaultShellPaneRef == "" || virtualTestPaneCount(store.registry, window.Metadata.UID) != 2 {
				t.Fatal("open did not materialize exactly one shell")
			}
			if len(executor.calls) == 0 {
				t.Fatal("no session handoff")
			}
			if route == "open" {
				var receipt cli.OperationReceipt
				if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Target.UID != project.Metadata.UID || receipt.Effects.Identity != cli.IdentityCreated || receipt.Effects.Address != cli.AddressAllocated || receipt.Effects.Topology != cli.TopologyEstablished || receipt.Effects.DesiredState != cli.DesiredStateCreated || receipt.Effects.Runtime != cli.RuntimeMaterialized || receipt.Effects.Focus != cli.FocusMovedCurrentClient || receipt.Cardinality != (cli.ReceiptCardinality{Projects: 1, Windows: 1, Panes: 1}) || !reflect.DeepEqual(receipt.SelectedWindowUIDs, []string{window.Metadata.UID}) {
					t.Fatalf("virtual open receipt=%+v", receipt)
				}
				shell, _ := store.registry.WindowDefaultShell(window.Metadata.UID)
				want := []cli.ReceiptResource{
					{Kind: "Project", UID: project.Metadata.UID, Name: project.Metadata.Name, Action: cli.ActionMaterialized},
					{Kind: "Window", UID: window.Metadata.UID, Name: window.Metadata.Name, Action: cli.ActionMaterialized},
					{Kind: "Pane", UID: shell.Metadata.UID, Name: shell.Metadata.Name, Action: cli.ActionCreated},
				}
				if !reflect.DeepEqual(receipt.AffectedUIDs, want) {
					t.Fatalf("affected resources=%+v, want %+v", receipt.AffectedUIDs, want)
				}
				// Reopening the same now-real Window must not claim another creation.
				stdout, _, err = runRoute(t, verb, "project", "uid:"+project.Metadata.UID, "-o", "receipt")
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Effects.Identity != cli.IdentityUnchanged || receipt.Effects.Address != cli.AddressUnchanged || receipt.Effects.Topology != cli.TopologyUnchanged || receipt.Effects.DesiredState != cli.DesiredStateUnchanged || receipt.Effects.Runtime != cli.RuntimeAlreadyLive || receipt.Cardinality != (cli.ReceiptCardinality{Projects: 1}) || len(receipt.AffectedUIDs) != 1 || len(receipt.SelectedWindowUIDs) != 0 || virtualTestPaneCount(store.registry, window.Metadata.UID) != 2 {
					t.Fatalf("repeated open changed resources or creation receipt: %+v", receipt)
				}
			}
		})
	}
}

func TestOpenVirtualPrimaryUsesExistingTerminalWindow(t *testing.T) {
	for _, currentReal := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-real", true: "current-real"}[currentReal], func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			project := virtualPrimaryProjectFixture(t, store)
			mut := store.mutator()
			first, panes, err := mut.AddWindow(&store.registry, project.Metadata.UID, coremetadata.BootstrapWindow{Name: "terminal"}, "/bin/sh", "op-sibling")
			if err != nil {
				t.Fatal(err)
			}
			session := tmux.addSession("alpha")
			session.windows = nil
			seedOwnedSession(session, project.Metadata.UID, project.Spec.Root)
			live := seedLiveWindow(t, tmux, session, first.Metadata.UID, panes[0].Metadata.UID)
			if _, err := mut.ObserveWindowRuntimeBinding(&store.registry, first.Metadata.UID, session.id, live.id); err != nil {
				t.Fatal(err)
			}
			if _, err := mut.BindLiveProjectSession(&store.registry, project.Metadata.UID, "alpha", tmux.socketPath); err != nil {
				t.Fatal(err)
			}
			second, secondPanes, err := mut.AddWindow(&store.registry, project.Metadata.UID, coremetadata.BootstrapWindow{Name: "second-terminal"}, "/bin/sh", "op-second-sibling")
			if err != nil {
				t.Fatal(err)
			}
			secondLive := seedLiveWindow(t, tmux, session, second.Metadata.UID, secondPanes[0].Metadata.UID)
			if _, err := mut.ObserveWindowRuntimeBinding(&store.registry, second.Metadata.UID, session.id, secondLive.id); err != nil {
				t.Fatal(err)
			}
			session.current = secondLive.id
			if !currentReal {
				session.current = "@missing"
			}
			before := store.registry.Clone()
			switcher := &switchCommand{managedStopStore: store.store(), tmuxRunner: tmux, lookupEnv: func(string) string { return "" }, allowVirtualTerminal: true}
			if _, err := switcher.prepareVirtualTerminalProject(context.Background(), project.Spec.Root); err != nil {
				t.Fatal(err)
			}
			if err := switcher.selectVirtualTerminalArrival(context.Background(), "alpha"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, store.registry) || len(session.windows) != 2 {
				t.Fatal("mixed Project changed topology")
			}
			expected := live.id
			if currentReal {
				expected = secondLive.id
			}
			if session.current != expected {
				t.Fatalf("arrival=%s expected=%s", session.current, expected)
			}
		})
	}
}
