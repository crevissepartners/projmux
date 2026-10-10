package app

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

func TestVirtualWindowTmuxToProcessRelaunchActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	liveguard.RequireActive(t)
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			var f processCreateCLI
			var source coremetadata.Agent
			var socket string
			if provider == "claude" {
				f, source, _, socket = hostMoveCLIFixture(t)
			} else {
				f, source, _, socket = codexHostMoveCLIFixture(t)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			old, _ := reg.Window(f.window)
			oldRuntime := old.Status.RuntimeID
			// Keep another Window: server-final-Pane retirement is the existing
			// relaunch limitation, outside this Window lifecycle contract.
			if out, err := exec.Command("tmux", "-S", socket, "new-window", "-d", "-t", old.Status.RuntimeSessionID, "-n", "keeper", "sleep 300").CombinedOutput(); err != nil {
				t.Fatalf("keeper Window: %v %s", err, out)
			}
			// Add a durable process sibling, then remove each human shell explicitly.
			_, err = f.store.Update(func(r *coremetadata.Registry) error {
				m := intmetadata.DefaultMutator()
				a, e := m.CreateAgent(r, f.window, coremetadata.CreateAgentOptions{Name: "headless-sibling", Provider: provider, Workspace: coremetadata.AgentWorkspace{CWD: f.root}})
				if e != nil {
					return e
				}
				p, e := m.AttachAgentPane(r, a.Metadata.UID, coremetadata.BootstrapPane{}, "op-sibling")
				if e != nil {
					return e
				}
				return m.ReserveProcessBinding(r, coremetadata.ProcessBinding{HostInstanceID: "sibling", ProjectUID: f.project, WindowUID: f.window, AgentUID: a.Metadata.UID, PaneUID: p.Metadata.UID, Generation: "gen-sibling", OperationID: "op-sibling"})
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, pane := range reg.PanesOf(f.window) {
				hostCLIOutput(t, f, "delete", "pane", "uid:"+pane.Metadata.UID, "--socket-path", socket, "--yes")
			}
			owner, _ := startProcessRelaunchCLI(t, ctx, f, "uid:"+source.Metadata.UID, "--host", "process", "--socket-path", socket, "--yes", "--", "continue")
			defer owner.shutdown(t)
			reg, err = f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			w, _ := reg.Window(f.window)
			if !reg.IsVirtualWindow(f.window) || w.Status.RuntimeID != "" || w.Status.RuntimeSessionID != "" || w.Spec.DefaultShellPaneRef != "" || len(reg.PanesOf(f.window)) != 0 {
				t.Fatalf("relaunch left tmux state: %+v", w)
			}
			out, err := exec.Command("tmux", "-S", socket, "list-windows", "-a", "-F", "#{window_id}").CombinedOutput()
			if err == nil {
				for id := range strings.FieldsSeq(string(out)) {
					if id == oldRuntime {
						t.Fatal("old tmux Window remains")
					}
				}
			}
			if err := reg.Validate(); err != nil {
				t.Fatal(err)
			}
			t.Logf("provider=%s retired-window=%s shell=0 socket=%s", provider, oldRuntime, socket)
		})
	}
}
