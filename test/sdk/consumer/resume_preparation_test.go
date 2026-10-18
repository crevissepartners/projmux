package consumer_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The caller-exit lifecycle test also exercises the positive Prepare -> Validate
// -> Resume path against the same fixture CLI with only public imports.
func TestSDKPrepareAgentResumePublicConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var prepared core.PrepareAgentResumeResult
	prepared, err = c.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: "uid:agent", Binding: core.ProcessBinding{Generation: "observed"}, AllowHostLost: true})
	if !errors.Is(err, context.Canceled) || prepared != (core.PrepareAgentResumeResult{}) {
		t.Fatal(prepared, err)
	}
	var proof core.AgentResumePreparation
	proof, err = c.ValidateAgentResumePreparation(ctx, core.ValidateAgentResumePreparationRequest{Ref: "uid:agent", Preparation: core.AgentResumePreparation{AgentUID: "agent", HostLost: true}})
	if !errors.Is(err, context.Canceled) || proof != (core.AgentResumePreparation{}) {
		t.Fatal(proof, err)
	}
	_, err = c.PrepareAgentResume(context.Background(), core.PrepareAgentResumeRequest{})
	var refused *projmux.Refusal
	if !errors.As(err, &refused) || refused.Code != projmux.CodeSelectorInvalid {
		t.Fatal(err)
	}
	if projmux.CodeResumePreparationStale != "process-resume-preparation-stale" {
		t.Fatal("public code changed")
	}
}

// Crash only this test's exact process identities. A paused owner cannot create
// a notification lock after the lock-free observation and before SIGKILL.
func externalLoseResumeFixture(t *testing.T, ctx context.Context, root string, activation core.ProcessActivation) {
	t.Helper()
	owner, child := activation.HostProcess, activation.Child
	if externalStopIdentity(t, owner.PID) != owner || externalStopIdentity(t, child.PID) != child {
		t.Fatal("fixture identity changed")
	}
	paused := false
	defer func() {
		if paused {
			_ = syscall.Kill(owner.PID, syscall.SIGCONT)
		}
	}()
	for {
		if err := syscall.Kill(owner.PID, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		paused = true
		for {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", owner.PID))
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+2:])
			if fields[0] == "T" || fields[0] == "t" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
		_, err := os.Lstat(filepath.Join(root, "deep/custom-records/notify.json.lock"))
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = syscall.Kill(owner.PID, syscall.SIGCONT); err != nil {
			t.Fatal(err)
		}
		paused = false
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := syscall.Kill(owner.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	paused = false
	if err := syscall.Kill(child.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		t.Fatal(err)
	}
	for _, identity := range []core.ProcessIdentity{owner, child} {
		for {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", identity.PID))
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+2:])
			if fields[0] == "Z" || fields[0] == "X" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("fixture did not retire", ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}
