package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Model a Project whose first and only Window was created by the process route.
func virtualPrimaryProjectFixture(t *testing.T, store *fakeResourceStore) coremetadata.Project {
	t.Helper()
	project, _ := store.registry.Project("prj-alpha")
	for _, window := range store.registry.WindowsOf(project.Metadata.UID) {
		if err := store.mutator().DeleteWindow(&store.registry, window.Metadata.UID); err != nil {
			t.Fatal(err)
		}
	}
	window, _, _, err := store.mutator().CreateProcessWindow(&store.registry, project.Metadata.UID,
		coremetadata.BootstrapWindow{Name: "headless"},
		coremetadata.CreateAgentOptions{Name: "headless-agent", Provider: aiModeCodex, Workspace: coremetadata.AgentWorkspace{CWD: project.Spec.Root}, OperationID: "op-virtual-primary"},
		coremetadata.ProcessBinding{HostInstanceID: "op-virtual-primary", Generation: "gen-virtual-primary", OperationID: "op-virtual-primary"})
	if err != nil {
		t.Fatal(err)
	}
	project, _ = store.registry.Project(project.Metadata.UID)
	if project.Spec.PrimaryWindowRef != window.Metadata.UID || !store.registry.IsVirtualWindow(window.Metadata.UID) {
		t.Fatal("fixture did not create a virtual primary")
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	return *project
}

func TestVirtualPrimaryProjectPickerNavigationMaterializes(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, route := range []string{"popup-selection", "sidebar-selection", "sidebar-continuation", "sidebar-command"} {
			if (route == "sidebar-selection" || route == "sidebar-command") && !live {
				continue
			} // closed sidebar selection launches the validated continuation tested below
			t.Run(route+map[bool]string{false: "/offline", true: "/live"}[live], func(t *testing.T) {
				sessions := map[string]bool{}
				if live {
					sessions["alpha"] = true
				}
				verb, store, executor := lifecycleVerbFixture(t, projectLifecycleOpen, true, sessions)
				project := virtualPrimaryProjectFixture(t, store)
				create, _ := newTestResourceCreateCommand(t, store, newFakeTmux())
				switcher := verb.switcher
				switcher.managedStopStore = store.store()
				checks := 0
				switcher.validateProjectOpenRoute = func(context.Context, string) error { checks++; return nil }
				calls := 0
				switcher.materializeVirtualWindow = func(ctx context.Context, uid string) (virtualWindowShellMaterialization, error) {
					if (route == "sidebar-continuation" || route == "sidebar-command") && checks == 0 {
						t.Fatal("materialized before route validation")
					}
					calls++
					result, err := create.materializeVirtualShellResult(uid)
					if err == nil {
						sessions["alpha"] = true
					}
					return result, err
				}
				var out bytes.Buffer
				var err error
				switch route {
				case "popup-selection", "sidebar-selection":
					ui := switchUIPopup
					if route == "sidebar-selection" {
						ui = switchUISidebar
					}
					_, err = switcher.execute(context.Background(), switchPlan{UI: ui, Selection: project.Spec.Root, SessionName: "alpha"}, &out)
				case "sidebar-command":
					err = switcher.runSidebarOpen([]string{"--path", project.Spec.Root, "--session", "alpha", "--anchor", "%42", "--mode", projectStartupValueTopology}, &out)
				case "sidebar-continuation":
					err = switcher.openSidebarClosedProject(context.Background(), project.Spec.Root, "alpha", "%42", projectStartupCandidate{Kind: projectStartupKindTopology})
				}
				if err != nil {
					t.Fatal(err)
				}
				window, _ := store.registry.Window(project.Spec.PrimaryWindowRef)
				if calls != 1 || store.registry.IsVirtualWindow(window.Metadata.UID) || window.Spec.DefaultShellPaneRef == "" || virtualTestPaneCount(store.registry, window.Metadata.UID) != 2 || len(executor.calls) == 0 {
					t.Fatalf("calls=%d window=%+v handoff=%v", calls, window, executor.calls)
				}
				if switcher.allowVirtualTerminal {
					t.Fatal("picker leaked opt-in into another route")
				}
			})
		}
	}
}

func TestVirtualPickerProjectDeniedTrustDoesNotMaterialize(t *testing.T) {
	for _, route := range []string{"popup-selection", "sidebar-selection", "sidebar-continuation"} {
		t.Run(route, func(t *testing.T) {
			verb, store, executor := lifecycleVerbFixture(t, projectLifecycleOpen, true, map[string]bool{"alpha": true})
			project := virtualPrimaryProjectFixture(t, store)
			executor.authorizeResult = false
			switcher := verb.switcher
			switcher.managedStopStore = store.store()
			called := false
			switcher.materializeVirtualWindow = func(context.Context, string) (virtualWindowShellMaterialization, error) {
				called = true
				return virtualWindowShellMaterialization{}, nil
			}
			before := store.registry.Clone()
			var err error
			switch route {
			case "popup-selection":
				err = switcher.openTarget(context.Background(), project.Spec.Root)
			case "sidebar-selection":
				err = switcher.openProjectTargetPathFromSidebar(context.Background(), switchPlan{Selection: project.Spec.Root, SessionName: "alpha"})
			case "sidebar-continuation":
				err = switcher.openSidebarClosedProject(context.Background(), project.Spec.Root, "alpha", "%42", projectStartupCandidate{Kind: projectStartupKindTopology})
			}
			if err == nil || called || store.writes != 0 || !reflect.DeepEqual(before, store.registry) {
				t.Fatalf("denied trust err=%v called=%v writes=%d", err, called, store.writes)
			}
		})
	}
}

func TestVirtualSidebarTrustRouteChangeDoesNotMaterialize(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		t.Run(map[bool]string{false: "selection", true: "continuation"}[continuation], func(t *testing.T) {
			verb, store, _ := lifecycleVerbFixture(t, projectLifecycleOpen, true, map[string]bool{"alpha": true})
			project := virtualPrimaryProjectFixture(t, store)
			switcher := verb.switcher
			switcher.managedStopStore = store.store()
			checks := 0
			switcher.validateProjectOpenRoute = func(context.Context, string) error {
				checks++
				// The public continuation's first check occurs before its trust dialog;
				// the later reobservation finds an anchor that no longer authorizes writes.
				if continuation && checks == 1 {
					return nil
				}
				return errors.New("exact sidebar anchor changed")
			}
			called := false
			switcher.materializeVirtualWindow = func(context.Context, string) (virtualWindowShellMaterialization, error) {
				called = true
				return virtualWindowShellMaterialization{}, nil
			}
			before := store.registry.Clone()
			var out bytes.Buffer
			var err error
			if continuation {
				err = switcher.runSidebarOpen([]string{"--path", project.Spec.Root, "--session", "alpha", "--anchor", "%42", "--mode", projectStartupValueTopology}, &out)
			} else {
				err = switcher.openProjectTargetPathFromSidebar(context.Background(), switchPlan{Selection: project.Spec.Root, SessionName: "alpha", Anchor: "%42"})
			}
			if err == nil || called || store.writes != 0 || !reflect.DeepEqual(before, store.registry) {
				t.Fatalf("route change err=%v materialized=%v writes=%d", err, called, store.writes)
			}
		})
	}
}
