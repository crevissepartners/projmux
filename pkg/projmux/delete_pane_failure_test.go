package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type paneFailureRunner struct {
	socket, uid, failure, paneID string
	pidReads, kills, queued      int
	serverPID                    string
	delegate                     tmux.ExecRunner
}

func (r *paneFailureRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) < 2 || args[0] != "-S" || args[1] != r.socket {
		return nil, errors.New("unbound failure runner")
	}
	for _, a := range args[2:] {
		if a == "run-shell" {
			r.queued++
			return nil, errors.New("SDK queued self kill")
		}
		if a == "kill-pane" && slices.Contains(args, r.paneID) {
			r.kills++
			if r.failure == "kill" {
				return nil, errors.New("injected exact Pane kill failure")
			}
		}
	}
	out, err := r.delegate.Run(ctx, name, args...)
	if err != nil {
		return out, err
	}
	for _, a := range args[2:] {
		if strings.Contains(a, "#{pid}") {
			r.pidReads++
			if r.failure == "pid" && r.pidReads > 1 {
				pid, _ := strconv.Atoi(r.serverPID)
				out = []byte(strings.ReplaceAll(string(out), r.serverPID, strconv.Itoa(pid+1)))
			}
		}
	}
	if r.failure == "uid" {
		out = []byte(strings.ReplaceAll(string(out), r.uid, "foreign-pane"))
	}
	return out, nil
}
func TestDeletePaneAgentSDKRealGuardsAndReplacementRollback(t *testing.T) {
	liveguard.RequireActive(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("PROJMUX_REAL_TMUX_STRICT") == "1" {
			t.Fatal(err)
		}
		t.Skip("tmux unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	for _, kind := range []core.Kind{core.KindPane, core.KindAgent} {
		for _, failure := range []string{"pid", "uid", "kill"} {
			t.Run(string(kind)+"/"+failure, func(t *testing.T) {
				reg, window, state, c := deleteFailureFixture(t)
				p := &reg.Panes[0]
				uid, ref := p.Metadata.UID, p.Metadata.UID
				if kind == core.KindAgent {
					a, e := store.DefaultMutator().CreateAgent(&reg, window, metadata.CreateAgentOptions{Name: "worker", Provider: "codex", OperationID: "op-agent"})
					if e != nil {
						t.Fatal(e)
					}
					p = &reg.Panes[0]
					p.Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindAgent, UID: a.Metadata.UID}
					p.Spec.Role = metadata.PaneRoleAgent
					arow, _ := reg.Agent(a.Metadata.UID)
					arow.Status.Phase = metadata.PhaseRunning
					arow.Status.PaneRef = p.Metadata.UID
					w, _ := reg.Window(window)
					w.Spec.DefaultShellPaneRef = ""
					ref = a.Metadata.UID
				}
				if e := reg.Validate(); e != nil {
					t.Fatal(e)
				}
				data, _ := json.Marshal(reg)
				path := store.PathFor(state)
				if e := os.WriteFile(path, data, 0600); e != nil {
					t.Fatal(e)
				}
				root := t.TempDir()
				if st, e := os.Stat(root); e != nil || !st.IsDir() {
					t.Fatal("socket root absent")
				}
				socket := filepath.Join(root, "s")
				direct := tmux.ExecRunner{}
				run := func(args ...string) string {
					t.Helper()
					out, e := direct.Run(context.Background(), "tmux", append([]string{"-S", socket}, args...)...)
					if e != nil {
						t.Fatalf("%v: %v %s", args, e, out)
					}
					return strings.TrimSpace(string(out))
				}
				run("-f", "/dev/null", "new-session", "-d", "-s", "alpha", "tail -f /dev/null")
				run("set-option", "-g", tmuxopts.AppGlobal, "1")
				run("set-option", "-g", "@projmux_socket_name", "projmux")
				run("set-option", "-t", "alpha", tmuxopts.ProjectUIDSession, reg.Projects[0].Metadata.UID)
				run("set-option", "-w", "-t", "alpha", tmuxopts.WindowUID, window)
				paneID := run("display-message", "-p", "-t", "alpha", "#{pane_id}")
				run("set-option", "-pq", "-t", paneID, tmuxopts.PaneUID, uid)
				t.Cleanup(func() {
					if run("display-message", "-p", "#{socket_path}") != socket {
						t.Error("cleanup route proof")
						return
					}
					run("kill-server")
				})
				t.Setenv("TMUX", socket+",999,1")
				t.Setenv("TMUX_PANE", paneID)
				r := &paneFailureRunner{socket: socket, uid: uid, failure: failure, paneID: paneID, serverPID: run("display-message", "-p", "#{pid}")}
				preimage, e := store.NewStore(path).LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				result, e := c.deleteWithRunner(context.Background(), core.DeleteRequest{Kind: kind, Ref: "uid:" + ref, SocketPath: socket, Confirm: true}, r)
				var refusal *Refusal
				if !errors.As(e, &refusal) || result.Committed || (refusal.Code != CodeDeleteExecutionFailed && refusal.Code != CodeDeleteRuntimeRefused) {
					t.Fatalf("%+v %v", result, e)
				}
				after, e := store.NewStore(path).LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				after.UpdatedAt = preimage.UpdatedAt
				if !reflect.DeepEqual(after, preimage) {
					t.Fatalf("Registry/intent changed: %v", e)
				}
				if got := run("list-panes", "-a", "-F", "#{pane_id}|#{@projmux_pane_uid}"); got != paneID+"|"+uid {
					t.Fatalf("replacement/tombstone drift: %s", got)
				}
				if r.queued != 0 {
					t.Fatal("SDK self kill")
				}
				if failure == "kill" && r.kills != 1 {
					t.Fatalf("kill not exercised: %v", e)
				}
				if failure != "kill" && r.kills != 0 {
					t.Fatal("guard allowed kill")
				}
			})
		}
	}
}
