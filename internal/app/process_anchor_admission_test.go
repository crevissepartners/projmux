package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessAdmissionCreatePaneAnchor(t *testing.T) {
	for _, stored := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "stored"}[stored], func(t *testing.T) {
			store := newFakeResourceStore(t)
			pane, _ := store.registry.Pane("pan-beta-zsh")
			pane.Status.Activation.RuntimeID = ""
			pane.Status.Activation.Generation = "process-generation"
			runtime := &processPaneRuntime{targets: []processhost.InventoryTarget{{Binding: processhost.Binding{Host: "host", Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation}}}}
			tmux := newFakeTmux()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			create.processRuntime = runtime
			create.reconciler.processes = runtime.inventory()
			routeCalls := 0
			create.bindExplicitRuntime = func(context.Context) error { routeCalls++; return nil }
			create.bindRuntime = create.bindExplicitRuntime
			before, _ := json.Marshal(store.registry)
			args := []string{"pane", "--project", "beta", "--window", "main"}
			if !stored {
				args = append(args, "--pane", "uid:"+pane.Metadata.UID)
			}
			stdout, _, err := runRoute(t, create, args...)
			after, _ := json.Marshal(store.registry)
			t.Logf("argv=%v error=%v tmux_calls=%d registry_writes=%d calls=%v", args, err, len(tmux.calls), store.writes, tmux.calls)
			if err == nil || !strings.Contains(err.Error(), "process-capability-unsupported") || !strings.Contains(err.Error(), "same Window") || stdout != "" || len(tmux.calls) != 0 || store.writes != 0 || routeCalls != 0 || string(before) != string(after) {
				t.Fatal("process anchor did not refuse before tmux and Registry writes")
			}
		})
	}
}

func TestProcessAdmissionSplitAnchor(t *testing.T) {
	reg, runtime, paneUID := processInventoryFixture(t)
	tmux := newFakeTmux()
	materializer := &materializer{runner: tmux, processAnchor: &processTerminalTarget{runtime: runtime, registry: reg, paneUID: paneUID}}
	before, _ := json.Marshal(reg)
	id, err := materializer.splitPane(context.Background(), "", "right", "/fixture", nil)
	after, _ := json.Marshal(reg)
	t.Logf("anchor=%s id=%q error=%v tmux_calls=%d registry_writes=0 calls=%v", paneUID, id, err, len(tmux.calls), tmux.calls)
	if err == nil || !strings.Contains(err.Error(), "process-capability-unsupported") || !strings.Contains(err.Error(), "same Window") || id != "" || len(tmux.calls) != 0 || string(before) != string(after) {
		t.Fatal("process split did not refuse before tmux")
	}
}

// A declaration elsewhere in the same Project does not change a tmux Pane or
// Window anchor. Compare the complete Registry and tmux call trace, not only exit.
func TestProcessAdmissionCreatePaneTmuxParity(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "Window", true: "tmux-Pane"}[explicit], func(t *testing.T) {
			run := func(process bool) (string, string) {
				store := newFakeResourceStore(t)
				created, err := store.mutator().AddPane(&store.registry, "win-beta-main", coremetadata.BootstrapPane{Name: "process"}, "/bin/zsh", "fixture")
				if err != nil {
					t.Fatal(err)
				}
				pane, _ := store.registry.Pane(created.Metadata.UID)
				pane.Status.Activation.Generation = "process-generation"
				pane.Status.Activation.RuntimeID = ""
				tmux := newFakeTmux()
				create, _ := newTestResourceCreateCommand(t, store, tmux)
				if process {
					create.processRuntime = &processPaneRuntime{targets: []processhost.InventoryTarget{{Binding: processhost.Binding{Host: "host", Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation}}}}
				}
				args := []string{"pane", "--project", "beta", "--window", "main"}
				if explicit {
					args = append(args, "--pane", "uid:pan-beta-zsh")
				}
				_, _, err = runRoute(t, create, args...)
				if err != nil || store.writes != 1 {
					t.Fatalf("tmux parity create: %v writes=%d", err, store.writes)
				}
				registry, _ := json.Marshal(store.registry)
				calls, _ := json.Marshal(tmux.calls)
				return string(registry), string(calls)
			}
			beforeReg, beforeCalls := run(false)
			afterReg, afterCalls := run(true)
			if beforeReg != afterReg || beforeCalls != afterCalls {
				t.Fatal("process admission changed tmux or Window anchor behavior")
			}
		})
	}
}
