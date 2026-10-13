package projmux

import (
	"context"
	"errors"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func sdkStopFixture(t *testing.T) (*Client, m.Registry, string, string) {
	t.Helper()
	state := t.TempDir()
	storage := store.NewStore(store.PathFor(state))
	var agentUID, paneUID string
	_, err := storage.Update(func(reg *m.Registry) error {
		mut := store.DefaultMutator()
		created, e := mut.RegisterProject(reg, m.RegisterProjectOptions{Root: t.TempDir(), Name: "stop-project", DefaultShell: "/bin/sh", OperationID: "register"})
		if e != nil {
			return e
		}
		_, a, p, e := mut.CreateProcessWindow(reg, created.Project.Metadata.UID, m.BootstrapWindow{Name: "stop-window"}, m.CreateAgentOptions{Name: "stop-agent", Provider: "codex", OperationID: "create"}, m.ProcessBinding{HostInstanceID: "host", Generation: "generation", OperationID: "op"})
		if e != nil {
			return e
		}
		agentUID, paneUID = a.Metadata.UID, p.Metadata.UID
		pane, _ := reg.Pane(paneUID)
		agent, _ := reg.Agent(agentUID)
		b := pane.Status.ProcessSession.Binding
		pane.Status.ProcessSession.ThreadID = "conversation"
		pane.Status.ProcessSession.ConnectionID = b.OperationID
		code := 0
		wait := &m.TerminationEvidence{Source: m.TerminationSourceSupervisor, Classification: m.TerminationNormal, ObservedAt: time.Now(), AgentUID: agentUID, PaneUID: paneUID, Generation: b.Generation, OperationID: b.OperationID, ExitCode: &code}
		pane.Status.LastTermination = wait
		agent.Status.LastTermination = wait
		agent.Status.Phase = m.PhaseOffline
		return mutationruntime.WriteProcessRetirement(store.PathFor(state), m.ProcessActivation{Binding: b, HostProcess: m.ProcessIdentity{PID: 2000000000, OwnerUID: uint32(os.Getuid()), Start: "retired-owner"}, Child: m.ProcessIdentity{PID: 2000000001, OwnerUID: uint32(os.Getuid()), Start: "retired-child"}}, wait)
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := storage.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Options{ConfigDir: filepath.Join(state, "config"), StateDir: state, HomeDir: state})
	if err != nil {
		t.Fatal(err)
	}
	return c, reg, agentUID, paneUID
}
func TestStopAgentSDKExplicitRootsIdempotenceStaleAndNoEffects(t *testing.T) {
	first, reg, uid, puid := sdkStopFixture(t)
	second, other, otherUID, _ := sdkStopFixture(t)
	poison := t.TempDir()
	t.Setenv("PROJMUX_STATE_DIR", poison)
	t.Setenv("HOME", poison)
	t.Setenv("XDG_STATE_HOME", poison)
	path := store.PathFor(first.paths.StateDir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(puid)
	for range 2 {
		result, err := first.StopAgent(context.Background(), core.StopAgentRequest{Ref: "uid:" + uid})
		if err != nil || !result.Stopped || result.ClaimReleased || result.Termination == nil || result.Binding.Generation != pane.Status.ProcessSession.Binding.Generation || result.PaneUID != puid {
			t.Fatalf("stop %+v %v", result, err)
		}
	}
	wrong := core.ProcessBinding{Generation: "replacement"}
	result, err := first.StopAgent(context.Background(), core.StopAgentRequest{Ref: "uid:" + uid, Binding: wrong})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeStopAgentStale || result.Stopped || result.AgentUID != uid || result.PaneUID != puid {
		t.Fatalf("stale %+v %v", result, err)
	}
	_, err = second.StopAgent(context.Background(), core.StopAgentRequest{Ref: "uid:" + uid})
	if !errors.As(err, &refusal) || refusal.Code != CodeResourceNotFound {
		t.Fatalf("borrowed first root: %v", err)
	}
	result, err = second.StopAgent(context.Background(), core.StopAgentRequest{Ref: "uid:" + otherUID})
	if err != nil || !result.Stopped {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("idempotent/stale stop changed Registry")
	}
	latest, err := store.NewStore(store.PathFor(second.paths.StateDir)).LoadReadOnly()
	if err != nil || !reflect.DeepEqual(other, latest) {
		t.Fatal("other root changed")
	}
	entries, _ := os.ReadDir(poison)
	if len(entries) != 0 {
		t.Fatal("ambient root used")
	}
	if os.Getenv("PROJMUX_STATE_DIR") != poison || os.Getenv("HOME") != poison {
		t.Fatal("global environment changed")
	}
}
func TestStopAgentSDKUnsupportedCancellationAndOwnerUnavailable(t *testing.T) {
	c, reg, uid, puid := sdkStopFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + uid})
	if !errors.Is(err, context.Canceled) || r.Stopped {
		t.Fatal(err)
	}
	_, err = c.StopAgent(context.Background(), core.StopAgentRequest{})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeSelectorInvalid {
		t.Fatal(err)
	}
	shell := reg.Panes[0]
	r, err = c.StopAgent(context.Background(), core.StopAgentRequest{Ref: "uid:" + shell.Metadata.OwnerUID()})
	if err == nil || r.Stopped {
		t.Fatal("unsupported accepted")
	}
	storage := store.NewStore(store.PathFor(c.paths.StateDir))
	_, err = storage.Update(func(reg *m.Registry) error {
		p, _ := reg.Pane(puid)
		a, _ := reg.Agent(uid)
		p.Status.LastTermination = nil
		a.Status.LastTermination = nil
		a.Status.Phase = m.PhaseRunning
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err = c.StopAgent(context.Background(), core.StopAgentRequest{Ref: "uid:" + uid})
	if !errors.As(err, &refusal) || refusal.Code != CodeStopAgentWaitUnconfirmed || r.Stopped || r.AgentUID != uid || r.PaneUID != puid {
		t.Fatalf("unconfirmed %+v %v", r, err)
	}
}

func TestStopAgentSDKRetiredOwnerStillLiveAndLegacyUnconfirmed(t *testing.T) {
	c, reg, uid, puid := sdkStopFixture(t)
	owner := exec.Command("sleep", "5")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Process.Kill(); _ = owner.Wait() }()
	identity, _, err := localipc.Process(owner.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(puid)
	activation := m.ProcessActivation{Binding: pane.Status.ProcessSession.Binding, HostProcess: identity, Child: m.ProcessIdentity{PID: 2000000001, OwnerUID: uint32(os.Getuid()), Start: "retired-child"}}
	// Replace only this test's prior synthetic witness, not Registry evidence.
	dir := filepath.Join(c.paths.StateDir, "process-retirements")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if err = os.Remove(filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err = mutationruntime.WriteProcessRetirement(store.PathFor(c.paths.StateDir), activation, pane.Status.LastTermination); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + uid})
	var f *Refusal
	if r.Stopped || !errors.As(err, &f) || f.Code != CodeStopAgentWaitUnconfirmed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("owner still live %+v %v", r, err)
	}
	current, _, err := localipc.Process(identity.PID)
	if err != nil || current != identity {
		t.Fatal("Stop signaled retired owner")
	}
	entries, _ = os.ReadDir(dir)
	for _, e := range entries {
		if err = os.Remove(filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	r, err = c.StopAgent(context.Background(), core.StopAgentRequest{Ref: "uid:" + uid})
	if r.Stopped || !errors.As(err, &f) || f.Code != CodeStopAgentWaitUnconfirmed {
		t.Fatalf("legacy false success %+v %v", r, err)
	}
}
func TestStopAgentSDKClaimGuardDeadlineAndCancel(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			c, _, uid, _ := sdkStopFixture(t)
			path := mutationruntime.DeferredClaimPath(store.PathFor(c.paths.StateDir), uid)
			unlock, err := mutationruntime.LockDeferredClaim(path)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { time.Sleep(250 * time.Millisecond); unlock(); close(done) }()
			defer func() { <-done }()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			if mode == "cancel" {
				ctx, cancel = context.WithCancel(context.Background())
				defer cancel()
				go func() { time.Sleep(30 * time.Millisecond); cancel() }()
			}
			start := time.Now()
			r, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + uid})
			if r.Stopped || (!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)) || time.Since(start) > 200*time.Millisecond {
				t.Fatalf("guard ignored cancellation %+v %v %s", r, err, time.Since(start))
			}
		})
	}
}
