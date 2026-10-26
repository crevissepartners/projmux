package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Exercise the public entrypoint: an injected runner would hide PATH lookup.
func TestDeleteAgentCapturedTmuxPATH(t *testing.T) {
	liveguard.RequireActive(t)
	binary, err := exec.LookPath("tmux")
	if err != nil {
		if os.Getenv("PROJMUX_REAL_TMUX_STRICT") == "1" {
			t.Fatal(err)
		}
		t.Skip("tmux unavailable")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ambient-missing", "ambient-fake", "captured-missing", "cleanup-failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("TMUX", "")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("__PROJMUX_RUNTIME_ANCHOR_PANE", "")
			reg, window, state, _ := deleteFailureFixture(t)
			a, e := store.DefaultMutator().CreateAgent(&reg, window, metadata.CreateAgentOptions{Name: "worker", Provider: "codex", OperationID: "op-agent"})
			if e != nil {
				t.Fatal(e)
			}
			p := &reg.Panes[0]
			p.Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindAgent, UID: a.Metadata.UID}
			p.Spec.Role = metadata.PaneRoleAgent
			agent, _ := reg.Agent(a.Metadata.UID)
			agent.Status.Phase = metadata.PhaseRunning
			agent.Status.PaneRef = p.Metadata.UID
			w, _ := reg.Window(window)
			w.Spec.DefaultShellPaneRef = ""
			if e := reg.Validate(); e != nil {
				t.Fatal(e)
			}
			data, e := json.Marshal(reg)
			if e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(store.PathFor(state), data, 0600); e != nil {
				t.Fatal(e)
			}
			root, e := os.MkdirTemp(os.Getenv("TMUX_TMPDIR"), "sdk-path-")
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			if st, e := os.Stat(root); e != nil || !st.IsDir() {
				t.Fatal("socket root absent")
			}
			socket := filepath.Join(root, "s")
			run := func(args ...string) string {
				t.Helper()
				out, e := exec.Command(binary, append([]string{"-S", socket}, args...)...).CombinedOutput()
				if e != nil {
					t.Fatalf("tmux %v: %v: %s", args, e, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("-f", "/dev/null", "new-session", "-d", "-s", "alpha", "/bin/sleep 300")
			// Keep the server alive even if deletion removes the Agent's last Pane.
			run("new-session", "-d", "-s", "keeper", "/bin/sleep 300")
			t.Cleanup(func() {
				if run("display-message", "-p", "#{socket_path}") != socket {
					t.Error("cleanup socket mismatch")
					return
				}
				run("kill-server")
			})
			run("set-option", "-g", tmuxopts.AppGlobal, "1")
			run("set-option", "-g", "@projmux_socket_name", "projmux")
			run("set-option", "-t", "alpha", tmuxopts.ProjectUIDSession, reg.Projects[0].Metadata.UID)
			run("set-option", "-w", "-t", "alpha", tmuxopts.WindowUID, window)
			paneID := run("display-message", "-p", "-t", "alpha", "#{pane_id}")
			run("set-option", "-pq", "-t", paneID, tmuxopts.PaneUID, p.Metadata.UID)

			captured, ambient := t.TempDir(), t.TempDir()
			marker := filepath.Join(t.TempDir(), "ambient-used")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			script := "#!/bin/sh\nexec " + quote(binary) + " \"$@\"\n"
			if mode == "cleanup-failure" {
				script = "#!/bin/sh\nfor arg do\n if [ \"$arg\" = kill-pane ]; then echo captured-cleanup-failed >&2; exit 71; fi\ndone\nexec " + quote(binary) + " \"$@\"\n"
			}
			if mode != "captured-missing" {
				if e := os.WriteFile(filepath.Join(captured, "tmux"), []byte(script), 0700); e != nil {
					t.Fatal(e)
				}
			}
			if e := os.WriteFile(filepath.Join(ambient, "tmux"), []byte("#!/bin/sh\nprintf used > "+quote(marker)+"\nexit 72\n"), 0700); e != nil {
				t.Fatal(e)
			}
			t.Setenv("PATH", captured)
			c, e := NewClient(Options{StateDir: state, SocketPath: socket})
			if e != nil {
				t.Fatal(e)
			}
			if mode == "ambient-missing" {
				t.Setenv("PATH", t.TempDir())
			} else {
				t.Setenv("PATH", ambient)
			}
			out, e := c.Delete(context.Background(), core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + a.Metadata.UID, Confirm: true})
			if mode == "captured-missing" || mode == "cleanup-failure" {
				var refusal *Refusal
				if e == nil || !errors.As(e, &refusal) || out.Committed {
					t.Fatalf("failure recorded as success: %+v %v", out, e)
				}
				if mode == "cleanup-failure" {
					var exit *exec.ExitError
					if !errors.As(e, &exit) || exit.ExitCode() != 71 {
						t.Errorf("cleanup failure cause not preserved: %v", e)
					}
				} else if !strings.Contains(e.Error(), "tmux unavailable on captured PATH") {
					t.Errorf("captured lookup failure not exposed: %v", e)
				}
			} else if e != nil || !out.Committed {
				t.Errorf("captured tmux deletion failed: %+v %v", out, e)
			}
			if _, e := os.Stat(marker); !os.IsNotExist(e) {
				t.Errorf("ambient tmux was executed: %v", e)
			}
		})
	}
}
