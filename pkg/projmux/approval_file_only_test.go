package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"golang.org/x/sys/unix"
)

type approvalReadTrap struct{}

func (approvalReadTrap) ListApprovals(context.Context, string) ([]core.PendingApproval, error) {
	panic("file-only contacted native control")
}
func (approvalReadTrap) ReviewApproval(context.Context, string, string, core.OfferedDecision) error {
	panic("file-only submitted a decision")
}

type sdkReadFileImage struct {
	Bytes    string
	Mode     os.FileMode
	Modified time.Time
}

func sdkReadSnapshot(t *testing.T, root string) map[string]sdkReadFileImage {
	t.Helper()
	files := map[string]sdkReadFileImage{}
	if e := filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		v := sdkReadFileImage{Mode: info.Mode(), Modified: info.ModTime()}
		if !info.IsDir() {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			v.Bytes = string(b)
		}
		files[path] = v
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return files
}
func TestSDKFileOnlyApprovalsWithHeldLocksAndCanceledContexts(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			c, _, uid, puid := sdkStopFixture(t)
			registry := store.NewStore(store.PathFor(c.paths.StateDir))
			if provider == "claude" {
				if _, e := registry.Update(func(r *m.Registry) error {
					a, _ := r.Agent(uid)
					p, _ := r.Pane(puid)
					a.Spec.Provider = "claude"
					p.Status.ProcessSession.Provider = "claude"
					p.Status.ProcessSession.SessionID = "conversation"
					p.Status.ProcessSession.ThreadID = ""
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			}
			approvals := agentapproval.NewStore(c.paths.StateDir)
			if _, e := approvals.Create(agentapproval.Record{ID: "permission-0000000000000001", AgentUID: uid, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"display fixture"}`), Deadline: time.Now().Add(time.Hour)}); e != nil {
				t.Fatal(e)
			}
			c.approvalControl = approvalReadTrap{}
			var locks atomic.Int32
			restore := store.SetLockObserver(func(store.LockObservation) { locks.Add(1) })
			defer restore()
			// Both locks already exist before the read. A byte snapshot alone would not
			// reveal an acquisition of one of these unchanged lock files.
			for _, path := range []string{registry.Path() + ".flock", approvals.Path() + ".flock"} {
				f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
				if e != nil {
					t.Fatal(e)
				}
				if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
					f.Close()
					t.Fatal(e)
				}
				defer f.Close()
				defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
			}
			before := sdkReadSnapshot(t, c.paths.StateDir)
			list, e := c.ListApprovalsWithOptions(context.Background(), "uid:"+uid, core.ApprovalListOptions{FileOnly: true})
			mode := core.ApprovalReadModeSingleRead
			if provider == "claude" {
				mode = core.ApprovalReadModeStored
			}
			if e != nil || list.ReadMode != mode || list.Provider != provider {
				t.Fatal(list, e)
			}
			for _, expired := range []bool{false, true} {
				var ctx context.Context
				var cancel context.CancelFunc
				if expired {
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				} else {
					ctx, cancel = context.WithCancel(context.Background())
					cancel()
				}
				result, e := c.ListApprovalsWithOptions(ctx, "uid:"+uid, core.ApprovalListOptions{FileOnly: true})
				cancel()
				if !errors.Is(e, ctx.Err()) || result.AgentUID != "" {
					t.Fatal(result, e)
				}
			}
			if locks.Load() != 0 || !reflect.DeepEqual(before, sdkReadSnapshot(t, c.paths.StateDir)) {
				t.Fatal("file-only took a Registry lock or changed source files", locks.Load())
			}
		})
	}
	// The observer is live, rather than an inert counter that would always pass.
	c, _, _, _ := sdkStopFixture(t)
	var locks atomic.Int32
	restore := store.SetLockObserver(func(store.LockObservation) { locks.Add(1) })
	defer restore()
	if _, e := store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(*m.Registry) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if locks.Load() == 0 {
		t.Fatal("Registry lock positive control did not observe acquisition")
	}
}
