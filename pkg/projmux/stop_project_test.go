package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type stopFailureRunner struct {
	path, session, uid, failure string
	reads, kills, calls         int
	cancel                      context.CancelFunc
}

func (r *stopFailureRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != r.path {
		return nil, errors.New("unbound stop runner")
	}
	args = args[2:]
	var out strings.Builder
	for len(args) > 0 {
		n := len(args)
		for i, a := range args {
			if a == ";" {
				n = i
				break
			}
		}
		cmd := args[:n]
		if n == len(args) {
			args = nil
		} else {
			args = args[n+1:]
		}
		format := cmd[len(cmd)-1]
		switch cmd[0] {
		case "display-message":
			if strings.Contains(format, "#{pid}") {
				r.reads++
			}
			pid := "4242"
			if r.failure == "pid" && r.reads >= 4 {
				pid = "4243"
			}
			out.WriteString(strings.NewReplacer("#{socket_path}", r.path, "#{pid}", pid).Replace(format) + "\n")
		case "show-options":
			if format == tmuxopts.AppGlobal || format == mutationruntime.RuntimeMutationSocketNameOption {
				out.WriteString("\n")
			}
		case "list-sessions":
			uid := r.uid
			if r.failure == "uid" {
				uid = "other"
			}
			out.WriteString(strings.NewReplacer("#{session_id}", "$1", "#{session_name}", r.session, "#{"+tmuxopts.ProjectUIDSession+"}", uid, "#{"+tmuxopts.SessionRole+"}", "").Replace(format) + "\n")
			if r.failure == "cancel-after-admission" && r.cancel != nil {
				r.cancel()
				r.cancel = nil
			}
		case "kill-session":
			r.kills++
			if r.cancel != nil {
				r.cancel()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
			return nil, errors.New("injected kill failure")
		default:
			return nil, errors.New("unexpected stop verb: " + cmd[0])
		}
	}
	return []byte(out.String()), nil
}

func TestStopProjectPIDUIDGuardsAndCancellation(t *testing.T) {
	for _, failure := range []string{"pid", "uid", "kill", "cancel-before", "cancel-after-admission"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			c, err := NewClient(Options{StateDir: filepath.Join(root, "state"), HomeDir: root})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "project"})
			if err != nil {
				t.Fatal(err)
			}
			reg, err := store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			runner := &stopFailureRunner{path: filepath.Join(root, "socket"), session: c.projectSessionName(root), uid: reg.Projects[0].Metadata.UID, failure: failure}
			// Snapshot the actual bytes: compensation must retain all desired state.
			path := store.PathFor(c.paths.StateDir)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancel-before" {
				cancel()
			}
			if failure == "cancel-after-admission" {
				// Cancel at the admitted kill, after StopProject has detached the context.
				runner.cancel = cancel
				runner.failure = "kill"
			}
			result, err := c.stopProjectWithRunner(ctx, core.StopProjectRequest{Ref: "project", SocketPath: runner.path}, runner)
			if result.Stopped || err == nil {
				t.Fatalf("unexpected success %+v %v", result, err)
			}
			if failure == "cancel-before" {
				if !errors.Is(err, context.Canceled) || runner.calls != 0 {
					t.Fatal("cancelled call reached tmux")
				}
			} else {
				var refusal *Refusal
				if !errors.As(err, &refusal) {
					t.Fatalf("undeclared refusal %v", err)
				}
				if refusal.Code != CodeProjectStopExecutionFailed && refusal.Code != CodeProjectStopRouteRefused {
					t.Fatalf("code %s", refusal.Code)
				}
			}
			if failure == "cancel-after-admission" && ctx.Err() == nil {
				t.Fatal("admitted operation did not exercise cancellation")
			}
			if (failure == "pid" || failure == "uid") && runner.kills != 0 {
				t.Fatal("drift killed")
			}
			if (failure == "kill" || failure == "cancel-after-admission") && runner.kills != 1 {
				t.Fatalf("kill count %s: %v", strconv.Itoa(runner.kills), err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("failed stop changed Registry")
			}
		})
	}
}

// Keep request shape refusal messages SDK-facing without rewriting user values.
func TestStopProjectInputCodes(t *testing.T) {
	c, err := NewClient(Options{StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.StopProject(context.Background(), core.StopProjectRequest{})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeSelectorInvalid || !strings.Contains(err.Error(), "StopProjectRequest.Ref") {
		t.Fatal(err)
	}
}
