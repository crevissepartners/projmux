package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func stopRequestedStore(t *testing.T) (*store.Store, m.Registry, m.Agent, m.Pane) {
	t.Helper()
	storage := store.NewStore(filepath.Join(t.TempDir(), "registry.json"))
	reg, err := storage.Update(func(reg *m.Registry) error {
		mut := store.DefaultMutator()
		project, err := mut.RegisterProject(reg, m.RegisterProjectOptions{Root: t.TempDir(), Name: "project", DefaultShell: "/bin/sh", OperationID: "register"})
		if err != nil {
			return err
		}
		_, a, p, err := mut.CreateProcessWindow(reg, project.Project.Metadata.UID, m.BootstrapWindow{Name: "window"}, m.CreateAgentOptions{Name: "agent", Provider: "codex", OperationID: "create"}, m.ProcessBinding{HostInstanceID: "host", Generation: "generation", OperationID: "operation"})
		if err != nil {
			return err
		}
		pane, _ := reg.Pane(p.Metadata.UID)
		agent, _ := reg.Agent(a.Metadata.UID)
		b := pane.Status.ProcessSession.Binding
		pane.Status.ProcessSession.ThreadID, pane.Status.ProcessSession.ConnectionID = "thread", b.OperationID
		activation := m.ProcessActivation{Binding: b, HostProcess: m.ProcessIdentity{PID: 2000000000, OwnerUID: uint32(os.Getuid()), Start: "host"}, Child: m.ProcessIdentity{PID: 2000000001, OwnerUID: uint32(os.Getuid()), Start: "child"}}
		pane.Status.Activation = m.PaneActivation{Kind: m.RuntimeProcess, AgentUID: b.AgentUID, Generation: b.Generation, OperationID: b.OperationID, Process: &activation}
		agent.Status.Phase = m.PhaseRunning
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, pane := range reg.Panes {
		if pane.Status.ProcessSession != nil {
			agent, _ := reg.Agent(pane.Metadata.OwnerUID())
			return storage, reg, agent.Clone(), pane.Clone()
		}
	}
	t.Fatal("no process Pane")
	return nil, m.Registry{}, m.Agent{}, m.Pane{}
}

func retireRequestedFixture(t *testing.T, storage *store.Store, pane m.Pane) {
	t.Helper()
	_, err := storage.Update(func(reg *m.Registry) error {
		code := 0
		b := pane.Status.ProcessSession.Binding
		wait := m.TerminationEvidence{Source: m.TerminationSourceSupervisor, Classification: m.TerminationNormal, ObservedAt: time.Now(), AgentUID: b.AgentUID, PaneUID: b.PaneUID, Generation: b.Generation, OperationID: b.OperationID, ExitCode: &code}
		return store.DefaultMutator().RecordProcessWait(reg, *pane.Status.Activation.Process, wait)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStopRequestedExplicitVersusCleanupAndFailure(t *testing.T) {
	for _, mode := range []string{"explicit", "cleanup", "natural", "natural-after-snapshot", "cancel", "cancel-at-admission", "cancel-after", "stop-error", "no-wait", "stale", "write-error"} {
		t.Run(mode, func(t *testing.T) {
			storage, reg, agent, pane := stopRequestedStore(t)
			activation := *pane.Status.Activation.Process
			s := ProcessAgentStopper{RegistryPath: storage.Path(), Load: storage.LoadReadOnly}
			budget := 5 * time.Second
			if mode == "no-wait" {
				budget = 65 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			if mode == "cancel-at-admission" {
				s.Load = func() (m.Registry, error) {
					cancel()
					return storage.LoadReadOnly()
				}
			}
			s.CallStop = func(context.Context, m.Registry, m.Agent) error {
				if mode == "cancel-after" {
					cancel()
				}
				if mode == "stop-error" {
					return errors.New("injected Stop failure")
				}
				if mode != "no-wait" {
					retireRequestedFixture(t, storage, pane)
				}
				return nil
			}
			if mode == "natural" || mode == "natural-after-snapshot" {
				retireRequestedFixture(t, storage, pane)
				var err error
				reg, err = storage.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				a, _ := reg.Agent(agent.Metadata.UID)
				p, _ := reg.Pane(pane.Metadata.UID)
				if mode == "natural" {
					agent, pane = a.Clone(), p.Clone()
				}
				if err = WriteProcessRetirement(storage.Path(), activation, p.Status.LastTermination); err != nil {
					t.Fatal(err)
				}
				s.CallStop = func(context.Context, m.Registry, m.Agent) error {
					if mode == "natural" {
						t.Fatal("natural exit controlled")
					}
					return nil
				}
			}
			changed := false
			s.ReadIdentity = func(pid int) (m.ProcessIdentity, int, error) {
				if !changed && pid == 2000000001 {
					changed = true
					if mode == "stale" {
						_, err := storage.Update(func(r *m.Registry) error {
							p, _ := r.Pane(pane.Metadata.UID)
							b := p.Status.ProcessSession.Binding
							next := b
							next.Generation, next.OperationID, next.HostInstanceID = "next-generation", "next-operation", "next-host"
							return store.DefaultMutator().ReserveProcessResume(r, b, next)
						})
						if err != nil {
							t.Fatal(err)
						}
					}
					if mode == "write-error" {
						block := filepath.Join(t.TempDir(), "not-a-directory")
						if err := os.WriteFile(block, []byte("blocked"), 0600); err != nil {
							t.Fatal(err)
						}
						s.RegistryPath = filepath.Join(block, "registry.json")
					}
				}
				return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
			}
			var err error
			if mode == "cleanup" {
				err = s.StopExact(ctx, reg, agent, pane)
			} else {
				err = s.StopExplicit(ctx, reg, agent, pane)
			}
			success := mode == "explicit" || mode == "cancel-after" || mode == "cleanup" || mode == "natural" || mode == "natural-after-snapshot"
			if (err == nil) != success {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if mode == "stale" && !errors.Is(err, processhost.ErrStale) {
				t.Fatalf("stale generation: %v", err)
			}
			latest, loadErr := storage.LoadReadOnly()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			p, _ := latest.Pane(pane.Metadata.UID)
			if p.Status.ProcessSession.StopRequested != (mode == "explicit" || mode == "cancel-after") {
				t.Fatalf("intent=%v mode=%s", p.Status.ProcessSession.StopRequested, mode)
			}
			if mode == "explicit" && (!m.SameProcessWait(s.Termination, p.Status.LastTermination) || !m.MatchesProcessWait(p.Status.ProcessSession.Binding, s.Termination)) {
				t.Fatal("intent replaced actual Wait")
			}
		})
	}
}

func TestStopRequestedChildFreeClaim(t *testing.T) {
	for _, mode := range []string{"released", "no-prior-wait", "new-generation"} {
		t.Run(mode, func(t *testing.T) { testStopRequestedChildFreeClaim(t, mode) })
	}
}

func testStopRequestedChildFreeClaim(t *testing.T, mode string) {
	storage, _, agent, pane := stopRequestedStore(t)
	retireRequestedFixture(t, storage, pane)
	if mode == "no-prior-wait" {
		if _, err := storage.Update(func(r *m.Registry) error {
			p, _ := r.Pane(pane.Metadata.UID)
			a, _ := r.Agent(agent.Metadata.UID)
			p.Status.LastTermination, a.Status.LastTermination = nil, nil
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := storage.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Agent(agent.Metadata.UID)
	p, _ := reg.Pane(pane.Metadata.UID)
	agent, pane = a.Clone(), p.Clone()
	before := pane.Status.LastTermination.Clone()
	claimant := exec.Command("sleep", "10")
	if err = claimant.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claimant.Process.Kill() })
	identity, _, err := localipc.Process(claimant.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	path := DeferredClaimPath(storage.Path(), agent.Metadata.UID)
	unlock, err := LockDeferredClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	claim := DeferredClaimRecord{Version: 1, Agent: agent.Metadata.UID, Binding: pane.Status.ProcessSession.Binding, Provider: "codex", Thread: "thread", Process: identity, Nonce: "claim", Detached: true}
	if err = WriteDeferredClaim(path, claim); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	raceError := make(chan error, 1)
	go func() {
		_ = claimant.Wait()
		var err error
		if mode == "new-generation" {
			_, err = storage.Update(func(r *m.Registry) error {
				b := pane.Status.ProcessSession.Binding
				next := b
				next.Generation, next.OperationID, next.HostInstanceID = "next-generation", "next-operation", "next-host"
				return store.DefaultMutator().ReserveProcessResume(r, b, next)
			})
		}
		raceError <- errors.Join(err, os.Remove(path))
		close(done)
	}()
	s := ProcessAgentStopper{RegistryPath: storage.Path(), Load: storage.LoadReadOnly, CallStop: func(context.Context, m.Registry, m.Agent) error { t.Fatal("claim controlled provider"); return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = s.StopExplicit(ctx, reg, agent, pane)
	if mode == "new-generation" {
		if !errors.Is(err, processhost.ErrStale) || s.ClaimReleased {
			t.Fatalf("claim generation race: %v", err)
		}
	} else if err != nil || !s.ClaimReleased {
		t.Fatalf("claim stop: %v", err)
	}
	<-done
	if err = <-raceError; err != nil {
		t.Fatal(err)
	}
	latest, err := storage.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	p, _ = latest.Pane(pane.Metadata.UID)
	if p.Status.ProcessSession.StopRequested != (mode != "new-generation") || !reflect.DeepEqual(before, p.Status.LastTermination) || !p.Status.Activation.IsZero() {
		t.Fatal("claim lost intent or fabricated Wait")
	}
}
