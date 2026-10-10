package app

import (
	"bytes"
	"context"
	"reflect"
	"strings"
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

func TestVirtualPrimaryProjectRefusesTerminalNavigation(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, route := range []string{"open", "attach", "popup-selection", "sidebar-selection", "sidebar-continuation"} {
			t.Run(route+map[bool]string{false: "/offline", true: "/live"}[live], func(t *testing.T) {
				sessions := map[string]bool{}
				if live {
					sessions["alpha"] = true
				}
				command, store, executor := lifecycleVerbFixture(t, projectLifecycleOpen, true, sessions)
				project := virtualPrimaryProjectFixture(t, store)
				switcher := command.switcher
				switcher.managedStopStore = store.store()
				before := store.registry.Clone()
				var stdout, stderr bytes.Buffer
				var err error
				switch route {
				case "open":
					err = command.Run([]string{"project", "uid:" + project.Metadata.UID}, &stdout, &stderr)
				case "attach":
					attach := &attachCommand{store: store.store(), switcher: switcher, lookupEnv: func(string) string { return "" }}
					err = attach.Run([]string{"project", "uid:" + project.Metadata.UID}, &stdout, &stderr)
				case "popup-selection", "sidebar-selection":
					ui := switchUIPopup
					if route == "sidebar-selection" {
						ui = switchUISidebar
					}
					_, err = switcher.execute(context.Background(), switchPlan{UI: ui, Selection: project.Spec.Root, SessionName: "alpha"}, &stdout)
				case "sidebar-continuation":
					err = switcher.openSidebarClosedProject(context.Background(), project.Spec.Root, "alpha", "%42", projectStartupCandidate{Kind: projectStartupKindTopology})
				}
				if exitCodeOf(err) != 2 || !strings.Contains(err.Error(), "virtual-primary-window") || !strings.Contains(err.Error(), "projmux agent sessions project uid:"+project.Metadata.UID) {
					t.Fatalf("refusal = %v", err)
				}
				if stdout.Len() != 0 || len(executor.calls) != 0 || store.writes != 0 || !reflect.DeepEqual(store.registry, before) {
					t.Fatalf("navigation changed state: stdout=%q calls=%v writes=%d", stdout.String(), executor.calls, store.writes)
				}
				if tmux := switcher.tmuxRunner.(*lifecycleAppServerRunner); len(tmux.calls) != 0 {
					t.Fatalf("tmux calls: %v", tmux.calls)
				}
			})
		}
	}
}
