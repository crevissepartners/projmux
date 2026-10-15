package projmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestDeleteProcessSDKExplicitRootsAndInactiveParity(t *testing.T) {
	for _, mode := range []string{"witness", "legacy-offline", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			first, _, uid, puid := sdkStopFixture(t)
			second, _, other, _ := sdkStopFixture(t)
			path := store.PathFor(first.paths.StateDir)
			if mode != "witness" {
				entries, _ := os.ReadDir(filepath.Join(first.paths.StateDir, "process-retirements"))
				for _, e := range entries {
					if err := os.Remove(filepath.Join(first.paths.StateDir, "process-retirements", e.Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "unknown" {
				_, err := store.NewStore(path).Update(func(r *m.Registry) error {
					p, _ := r.Pane(puid)
					a, _ := r.Agent(uid)
					p.Status.LastTermination = nil
					a.Status.LastTermination = nil
					a.Status.Phase = m.PhaseRunning
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			poison := t.TempDir()
			t.Setenv("HOME", poison)
			t.Setenv("PROJMUX_STATE_DIR", poison)
			t.Setenv("XDG_STATE_HOME", poison)
			t.Setenv("TMUX", "poison")
			t.Setenv("TMUX_PANE", "%999")
			before, _ := os.ReadFile(path)
			otherPath := store.PathFor(second.paths.StateDir)
			otherBefore, _ := os.ReadFile(otherPath)
			req := core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + uid, DryRun: true}
			result, err := first.Delete(context.Background(), req)
			if err != nil || result.Committed || len(result.Plan.Targets) != 1 {
				t.Fatalf("dry %+v %v", result, err)
			}
			if len(result.Processes) != 1 || result.Processes[0].AgentUID != uid {
				t.Fatalf("process plan missing: %+v", result.Processes)
			}
			wantProcess := result.Processes[0]
			if mode == "unknown" {
				if wantProcess.Runtime != "unknown" || wantProcess.Evidence != "activation-absent" {
					t.Fatalf("unknown evidence: %+v", wantProcess)
				}
			} else if wantProcess.Runtime != "offline" || wantProcess.Evidence != "wait:normal exit=0" {
				t.Fatalf("offline evidence: %+v", wantProcess)
			}
			result.Processes[0].Evidence = "caller changed"
			result.Plan.Targets[0].Descendants[0].Name = "caller changed"
			again, err := first.Delete(context.Background(), req)
			if err != nil || !reflect.DeepEqual(again.Processes, []core.DeleteProcess{wantProcess}) || again.Plan.Targets[0].Descendants[0].Name == "caller changed" {
				t.Fatalf("caller-owned preview: %+v %v", again, err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("dry changed Registry")
			}
			req.DryRun = false
			result, err = first.Delete(context.Background(), req)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeConfirmationRequired || result.Committed {
				t.Fatalf("confirm %+v %v", result, err)
			}
			if !reflect.DeepEqual(result.Processes, []core.DeleteProcess{wantProcess}) {
				t.Fatalf("confirmation lost process plan: %+v", result.Processes)
			}
			req.Confirm = true
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result, err = first.Delete(ctx, req)
			if !errors.Is(err, context.Canceled) || result.Committed {
				t.Fatalf("cancel %+v %v", result, err)
			}
			result, err = second.Delete(context.Background(), req)
			if !errors.As(err, &refusal) || refusal.Code != CodeResourceNotFound || result.Committed {
				t.Fatalf("foreign %+v %v", result, err)
			}
			req.SocketPath = "relative"
			result, err = first.Delete(context.Background(), req)
			if !errors.As(err, &refusal) || refusal.Code != CodeRuntimeRequestInvalid || result.Committed {
				t.Fatalf("invalid %+v %v", result, err)
			}
			req.SocketPath = ""
			result, err = first.Delete(context.Background(), req)
			if err != nil || !result.Committed || result.OperationID == "" || result.Receipt.Target.UID != uid {
				t.Fatalf("delete %+v %v", result, err)
			}
			if !reflect.DeepEqual(result.Processes, []core.DeleteProcess{wantProcess}) {
				t.Fatalf("committed process plan: %+v", result.Processes)
			}
			latest, err := store.NewStore(path).LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := latest.Agent(uid); ok {
				t.Fatal("Agent retained")
			}
			if _, ok := latest.Pane(puid); ok {
				t.Fatal("Pane retained")
			}
			otherAfter, _ := os.ReadFile(otherPath)
			if !bytes.Equal(otherBefore, otherAfter) {
				t.Fatal("other root changed")
			}
			if _, ok := latest.Agent(other); ok {
				t.Fatal("foreign Agent borrowed")
			}
			entries, _ := os.ReadDir(poison)
			if len(entries) != 0 {
				t.Fatal("ambient poison written")
			}
		})
	}
}
func TestDeleteProcessSDKProtectedBeforeAnyEffect(t *testing.T) {
	c, _, uid, _ := sdkStopFixture(t)
	path := store.PathFor(c.paths.StateDir)
	_, err := store.NewStore(path).Update(func(r *m.Registry) error {
		a, _ := r.Agent(uid)
		a.Metadata.Labels = map[string]string{m.LabelProtected: "true"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	for _, dry := range []bool{true, false} {
		r, err := c.Delete(context.Background(), core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + uid, Confirm: true, DryRun: dry})
		var f *Refusal
		if !errors.As(err, &f) || f.Code != CodeAgentProtected || f.AgentUID != uid || r.Committed {
			t.Fatalf("protected %+v %v", r, err)
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("protected mutated")
	}
}

func TestDeleteProcessSDKClaimGuardCancellationAndStaleBirth(t *testing.T) {
	for _, mode := range []string{"cancel", "birth"} {
		t.Run(mode, func(t *testing.T) {
			c, reg, uid, puid := sdkStopFixture(t)
			path := mutationruntime.DeferredClaimPath(store.PathFor(c.paths.StateDir), uid)
			owner := exec.Command("sleep", "10")
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = owner.Process.Kill(); _ = owner.Wait() }()
			identity, _, err := localipc.Process(owner.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			pane, _ := reg.Pane(puid)
			unlock, err := mutationruntime.LockDeferredClaim(path)
			if err != nil {
				t.Fatal(err)
			}
			claim := mutationruntime.DeferredClaimRecord{Version: 1, Agent: uid, Binding: pane.Status.ProcessSession.Binding, Provider: "codex", Thread: pane.Status.ProcessSession.ThreadID, Process: identity, Nonce: "claim", Detached: true}
			if err = mutationruntime.WriteDeferredClaim(path, claim); err != nil {
				unlock()
				t.Fatal(err)
			}
			before, _ := os.ReadFile(store.PathFor(c.paths.StateDir))
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			done := make(chan struct{})
			go func() {
				time.Sleep(40 * time.Millisecond)
				if mode == "cancel" {
					cancel()
				} else {
					_, err = store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(r *m.Registry) error {
						p, _ := r.Pane(puid)
						p.Status.ProcessSession.Binding.Generation = "replacement"
						return nil
					})
				}
				unlock()
				close(done)
			}()
			result, deleteErr := c.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + uid, Confirm: true})
			<-done
			if result.Committed || deleteErr == nil {
				t.Fatalf("race false success %+v %v", result, deleteErr)
			}
			if mode == "cancel" {
				if !errors.Is(deleteErr, context.Canceled) {
					t.Fatal(deleteErr)
				}
				after, _ := os.ReadFile(store.PathFor(c.paths.StateDir))
				if !bytes.Equal(before, after) {
					t.Fatal("cancel changed Registry")
				}
			}
			current, readErr := mutationruntime.ReadDeferredClaim(path)
			if readErr != nil || current.StopRequested || !mutationruntime.DeferredClaimLive(claim) {
				t.Fatalf("unadmitted claim changed %+v %v", current, readErr)
			}
			latest, err := store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			p, _ := latest.Pane(puid)
			if !reflect.DeepEqual(p.Status.LastTermination, pane.Status.LastTermination) {
				t.Fatal("race invented Wait")
			}
		})
	}
}

func TestDeleteProcessSDKAdmittedDeadlineHasPublicRefusal(t *testing.T) {
	cause := &mutationruntime.ProcessDeleteError{Token: "process-delete-stop-unconfirmed", Runtime: "running", Cause: fmt.Errorf("owner retirement: %w", context.DeadlineExceeded)}
	err := deleteRefusal(CodeDeleteExecutionFailed, cause)
	var refused *Refusal
	if !errors.As(err, &refused) || refused.Code != CodeDeleteExecutionFailed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admitted failure lost public code/cause: %T %v", err, err)
	}
	if err = deleteRefusal(CodeDeleteExecutionFailed, context.Canceled); err != context.Canceled {
		t.Fatalf("pre-admission cancellation changed: %v", err)
	}
}

func TestDeleteProcessSDKPreservesStopCauses(t *testing.T) {
	for _, cause := range []error{processhost.ErrBusy, processhost.ErrStale, mutationruntime.ErrProcessStopUnsupported} {
		t.Run(cause.Error(), func(t *testing.T) {
			// The runtime executor's fallback is covered separately. This exercises
			// the public Refusal boundary with the same typed runtime failure.
			runtimeFailure := mutationruntime.ProcessDeleteRefusal("process-host-unavailable", "running",
				"owner host pid %d of process Agent uid %q did not accept Stop: %w; nothing was changed", 2000000000, "agent-fixture", cause)
			err := deleteRefusal(CodeDeleteExecutionFailed, runtimeFailure)
			var refusal *Refusal
			var preserved *mutationruntime.ProcessDeleteError
			if !errors.As(err, &refusal) || refusal.Code != CodeDeleteExecutionFailed || !errors.As(err, &preserved) || preserved != runtimeFailure || !errors.Is(err, cause) {
				t.Fatalf("public SDK lost code/typed cause: %T %v", err, err)
			}
			want := fmt.Sprintf("process-host-unavailable: owner host pid %d of process Agent uid %q did not accept Stop: %v; nothing was changed", 2000000000, "agent-fixture", cause)
			if err.Error() != want {
				t.Fatalf("public refusal receipt changed: got %q want %q", err.Error(), want)
			}
		})
	}
}
