package projmux

import (
	"context"
	"errors"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"os"
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
		return nil
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
