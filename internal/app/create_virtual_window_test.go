package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

func TestProcessVirtualWindowReservationAndRollback(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			command, _ := newTestAgentCreateCommand(t, store, tmux)
			opts := processAgentCreateOptions{Project: selector.Ref{Kind: coremetadata.KindProject, Name: "alpha"}, Window: selector.Ref{Kind: coremetadata.KindWindow, Name: "virtual"}, NewWindow: &coremetadata.BootstrapWindow{Name: "virtual"}, Provider: aiModeCodex, Name: "owned"}
			if existing {
				opts.Window.Name = "review"
				opts.NewWindow.Name = "review"
			}
			before := store.registry.Clone()
			project, window, err := resolveProcessCreateScope(store.registry, opts)
			if err != nil {
				t.Fatal(err)
			}
			plan := processAgentCreatePlan{project: project, window: window, workspace: coremetadata.AgentWorkspace{CWD: project.Spec.Root}, flags: resourceCreateFlags{provider: aiModeCodex}}
			result, err := command.reserveProcessAgent(context.Background(), plan, opts, "op-virtual", "gen-virtual")
			if err != nil {
				t.Fatal(err)
			}
			w, _ := store.registry.Window(result.Binding.Window)
			if existing {
				if w.Metadata.UID != window.Metadata.UID || result.createdWindow {
					t.Fatal("existing Window not reused")
				}
			} else {
				if !store.registry.IsVirtualWindow(w.Metadata.UID) || w.Spec.AnchorPaneRef != result.Binding.Pane || w.Spec.DefaultShellPaneRef != "" || w.Status.RuntimeID != "" || w.Status.RuntimeSessionID != "" {
					t.Fatalf("wrong Window: %+v", w)
				}
				whole := pbtPlan(t, store.registry, project)
				for _, planned := range whole.windows {
					if planned.window.Metadata.UID == w.Metadata.UID {
						t.Fatal("Project plan included virtual Window")
					}
				}
				topology := registryTopologyPlan{}
				if err := topology.planWindow(context.Background(), tmux, store.registry, project, *w, 0, nil, nil); err != nil || len(topology.windows) != 0 || topology.hasRefusal() {
					t.Fatalf("virtual topology materialized: %+v %v", topology, err)
				}
				_, err := command.planPaneTargets(store.registry, project, createScope{project: opts.Project}, resourceCreateFlags{windows: repeatedFlag{"uid:" + w.Metadata.UID}}, selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindWindow}, canonicalCreateAgent)
				var capability resourcegraph.ProcessCapabilityError
				if !errors.As(err, &capability) || capability.Action != resourcegraph.ProcessCreatePane {
					t.Fatalf("virtual admission: %v", err)
				}
			}
			if err := command.rollbackProcessAgent(&result); err != nil {
				t.Fatal(err)
			}
			if len(store.registry.Windows) != len(before.Windows) || len(store.registry.Agents) != len(before.Agents) || len(store.registry.Panes) != len(before.Panes) {
				t.Fatal("rollback left resources")
			}
			if len(tmux.calls) != 0 {
				t.Fatalf("tmux calls: %v", tmux.calls)
			}
		})
	}
}

func TestProcessWindowProviderRefusedBeforeWrite(t *testing.T) {
	for _, provider := range []string{"", "shell", "antigravity"} {
		t.Run(provider, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			command, _ := newTestAgentCreateCommand(t, store, tmux)
			before := store.registry.Clone()
			args := []string{"--host", "process", "-p", "alpha"}
			token := "process-window-provider-required"
			if provider != "" {
				args = append(args, "--provider", provider)
				token = "process-window-provider-unsupported"
			}
			var out, stderr bytes.Buffer
			err := command.runResourceWindow(args, &out, &stderr)
			if err == nil || !strings.Contains(err.Error(), token) || cli.ClassifyFailure(err, coremetadata.IsUsageError(err)).ExitCode != 2 || !reflect.DeepEqual(before, store.registry) || store.writes != 0 || len(tmux.calls) != 0 || out.Len() != 0 {
				t.Fatalf("unsafe refusal: %v", err)
			}
		})
	}
}

func TestProcessVirtualWindowCLIRequests(t *testing.T) {
	flags, err := parseResourceCreateFlags(canonicalCreateAgent, []string{"--host", "process", "-p", "alpha", "--create-window", "--window", "virtual"}, &bytes.Buffer{}, resourceCreateShape{split: true, provider: true, host: true})
	if err != nil {
		t.Fatal(err)
	}
	req, _, err := processCLIRequest(flags)
	if err != nil || req.options.NewWindow == nil || req.options.NewWindow.Name != "virtual" {
		t.Fatalf("new request: %+v %v", req, err)
	}
	if _, err := parseResourceCreateFlags(canonicalCreateAgent, []string{"--host", "process", "-p", "alpha", "--create-window", "--window", "uid:win-one"}, &bytes.Buffer{}, resourceCreateShape{split: true, provider: true, host: true}); err == nil {
		t.Fatal("UID accepted for creation")
	}
	req, _, err = processCLIRequest(resourceCreateFlags{processWindow: true, host: "process", projects: repeatedFlag{"alpha"}, provider: aiModeClaude, name: "virtual"})
	if err != nil || req.options.NewWindow == nil || req.options.Name != "" || req.options.Window != (selector.Ref{}) {
		t.Fatalf("Window request: %+v %v", req, err)
	}
}
