package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

func deleteProcessFixture(t *testing.T) (m.Registry, DeletePlan, ProcessDeleteTarget) {
	t.Helper()
	r := m.NewRegistry()
	mut := store.DefaultMutator()
	created, err := mut.RegisterProject(&r, m.RegisterProjectOptions{Root: t.TempDir(), Name: "delete-project", DefaultShell: "/bin/sh", OperationID: "register"})
	if err != nil {
		t.Fatal(err)
	}
	_, a, p, err := mut.CreateProcessWindow(&r, created.Project.Metadata.UID, m.BootstrapWindow{Name: "process"}, m.CreateAgentOptions{Name: "worker", Provider: "codex", OperationID: "create"}, m.ProcessBinding{HostInstanceID: "host", Generation: "generation", OperationID: "op"})
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := r.Pane(p.Metadata.UID)
	binding := pane.Status.ProcessSession.Binding
	activation := m.ProcessActivation{Binding: binding, HostProcess: m.ProcessIdentity{PID: 2000000000, OwnerUID: uint32(os.Getuid()), Start: "owner"}, Child: m.ProcessIdentity{PID: 2000000001, OwnerUID: uint32(os.Getuid()), Start: "child"}}
	pane.Status.Activation = m.PaneActivation{Kind: m.RuntimeProcess, Generation: binding.Generation, AgentUID: a.Metadata.UID, OperationID: binding.OperationID, Process: &activation}
	agent, _ := r.Agent(a.Metadata.UID)
	agent.Status.Phase = m.PhaseRunning
	plan := DeletePlan(mutations.BuildDeletePlan(r, m.KindAgent, selector.Resolution{Kind: m.KindAgent, Matches: []selector.Match{{Kind: m.KindAgent, UID: a.Metadata.UID, Name: a.Metadata.Name}}}))
	plan.ExactUID = true
	return r, plan, ProcessDeleteTarget{Agent: a.Metadata.UID, Pane: p.Metadata.UID, Window: a.Metadata.OwnerUID(), RootKind: m.KindProject, RootUID: created.Project.Metadata.UID, Binding: binding, Activation: &activation, Runtime: "running", HostPID: activation.HostProcess.PID}
}
func deleteFixtureWait(t *testing.T, r *m.Registry, target ProcessDeleteTarget) {
	t.Helper()
	code := 0
	w := m.TerminationEvidence{Source: m.TerminationSourceSupervisor, Classification: m.TerminationNormal, ObservedAt: time.Now(), AgentUID: target.Agent, PaneUID: target.Pane, Generation: target.Binding.Generation, OperationID: target.Binding.OperationID, ExitCode: &code}
	if err := (m.Mutator{}).RecordProcessWait(r, *target.Activation, w); err != nil {
		t.Fatal(err)
	}
}
func TestProcessDeleteActualWaitKernelRetirementAndAdmission(t *testing.T) {
	for _, mode := range []string{"normal", "cancel-before", "cancel-after", "metadata-only", "no-wait", "stale", "cleanup-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			r, plan, target := deleteProcessFixture(t)
			initial := r.Clone()
			calls, mutationsCount := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel-before" {
				cancel()
			}
			d := &ProcessAgentDeleter{Load: func() (m.Registry, error) { return r.Clone(), nil }, Alive: func(m.ProcessIdentity) bool { return false }, ReadIdentity: func(pid int) (m.ProcessIdentity, int, error) {
				if mode == "metadata-only" {
					return target.Activation.HostProcess, 0, nil
				}
				return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
			}, WaitLimit: 30 * time.Millisecond, StopAdmission: time.Millisecond, Poll: time.Millisecond, Stop: func(admitted context.Context, _ m.Registry, _ m.Agent) error {
				calls++
				if mode == "cancel-after" {
					cancel()
					if admitted.Err() != nil {
						t.Fatal("caller cancellation reached admitted owner Stop")
					}
				}
				if mode == "stale" {
					p, _ := r.Pane(target.Pane)
					p.Status.ProcessSession.Binding.Generation = "replacement"
				} else if mode != "no-wait" {
					deleteFixtureWait(t, &r, target)
				}
				return nil
			}, Mutate: func(_ m.Kind, _ []string, f func(*m.Registry, m.Mutator) error) error {
				mutationsCount++
				if mode == "cleanup-uncertain" {
					return errors.New("injected durable cleanup failure")
				}
				candidate := r.Clone()
				if err := f(&candidate, m.Mutator{}); err != nil {
					return err
				}
				r = candidate
				return nil
			}}
			out, err := d.Execute(ctx, plan, []ProcessDeleteTarget{target}, nil)
			if mode == "normal" || mode == "cancel-after" {
				if err != nil || calls != 1 || mutationsCount != 1 || len(out.Targets) != 1 || out.Targets[0].Runtime != "stopped" {
					t.Fatalf("out%+v calls%d mutations%d err%v", out, calls, mutationsCount, err)
				}
				if _, ok := r.Agent(target.Agent); ok {
					t.Fatal("Agent kept")
				}
			} else {
				if err == nil {
					t.Fatal("false success")
				}
				if _, ok := r.Agent(target.Agent); !ok {
					t.Fatal("failure deleted Agent")
				}
				if mode == "cancel-before" && (!errors.Is(err, context.Canceled) || calls != 0 || mutationsCount != 0 || !reflect.DeepEqual(initial, r)) {
					t.Fatal("pre-admission effect")
				}
				if mode == "metadata-only" || mode == "no-wait" {
					var f *ProcessDeleteError
					if !errors.As(err, &f) || f.Token != processDeleteStopUnconfirmedToken || mutationsCount != 0 {
						t.Fatalf("unconfirmed %v", err)
					}
				}
			}
		})
	}
}
func TestProcessDeleteInactiveWitnessStillLiveAndLegacyCleanup(t *testing.T) {
	r, plan, target := deleteProcessFixture(t)
	deleteFixtureWait(t, &r, target)
	path := filepath.Join(t.TempDir(), "metadata", "registry.json")
	d := &ProcessAgentDeleter{RegistryPath: path, ReadIdentity: func(int) (m.ProcessIdentity, int, error) { return target.Activation.HostProcess, 0, nil }}
	// No witness: Delete retains CLI cleanup parity, unlike Stop.
	if got, err := d.ClassifyPlan(r, plan); err != nil || got[0].Runtime != "offline" {
		t.Fatalf("legacy %+v %v", got, err)
	}
	p, _ := r.Pane(target.Pane)
	if err := WriteProcessRetirement(path, *target.Activation, p.Status.LastTermination); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClassifyPlan(r, plan); err == nil {
		t.Fatal("live witnessed owner falsely retired")
	}
	uncertain := errors.New("kernel read uncertain")
	d.ReadIdentity = func(int) (m.ProcessIdentity, int, error) { return m.ProcessIdentity{}, 0, uncertain }
	if _, err := d.ClassifyPlan(r, plan); err == nil || !errors.Is(err, uncertain) {
		t.Fatalf("uncertain birth cause lost: %v", err)
	}
	d.ReadIdentity = func(int) (m.ProcessIdentity, int, error) { return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent }
	if _, err := d.ClassifyPlan(r, plan); err != nil {
		t.Fatal(err)
	}
}

func TestProcessDeleteFinalClaimAndCommitGuard(t *testing.T) {
	for _, mode := range []string{"late-claim", "commit-guard"} {
		t.Run(mode, func(t *testing.T) {
			reg, plan, target := deleteProcessFixture(t)
			deleteFixtureWait(t, &reg, target)
			path := filepath.Join(t.TempDir(), "metadata", "registry.json")
			claimPath := DeferredClaimPath(path, target.Agent)
			d := NewProcessAgentDeleter(path)
			reads, mutated := 0, false
			before := reg.Clone()
			d.Load = func() (m.Registry, error) {
				reads++
				if reads == 1 && mode == "late-claim" {
					release, err := LockDeferredClaim(claimPath)
					if err != nil {
						t.Fatal(err)
					}
					claim := DeferredClaimRecord{Version: 1, Agent: target.Agent, Binding: target.Binding, Provider: "codex", Process: target.Activation.HostProcess, Nonce: "new-claim", Detached: true}
					err = WriteDeferredClaim(claimPath, claim)
					release()
					if err != nil {
						t.Fatal(err)
					}
				}
				return reg.Clone(), nil
			}
			d.Mutate = func(_ m.Kind, _ []string, apply func(*m.Registry, m.Mutator) error) error {
				mutated = true
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				defer cancel()
				release, err := LockDeferredClaimContext(ctx, claimPath)
				if err == nil {
					release()
					t.Fatal("claim birth guard was not held through Registry commit")
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				return apply(&reg, store.DefaultMutator())
			}
			targets, err := d.ClassifyPlan(reg, plan)
			if err != nil {
				t.Fatal(err)
			}
			_, err = d.Execute(context.Background(), plan, targets, nil)
			if mode == "late-claim" {
				var f *ProcessDeleteError
				if !errors.As(err, &f) || f.Token != processDeleteRefusedToken || mutated || !reflect.DeepEqual(before, reg) {
					t.Fatalf("late claim deletion effects: %v mutated%t", err, mutated)
				}
			} else {
				if err != nil || !mutated {
					t.Fatalf("guard commit %v", err)
				}
			}
		})
	}
}

func TestProcessDeleteRetiredReplacementGenerationIsPreserved(t *testing.T) {
	for _, mode := range []string{"retired", "starting"} {
		t.Run(mode, func(t *testing.T) {
			reg, plan, target := deleteProcessFixture(t)
			d := NewProcessAgentDeleter(filepath.Join(t.TempDir(), "metadata", "registry.json"))
			stops, commits := 0, 0
			replacementBorn := false
			d.Load = func() (m.Registry, error) { return reg.Clone(), nil }
			d.Alive = func(m.ProcessIdentity) bool { return true }
			d.Stop = func(context.Context, m.Registry, m.Agent) error {
				stops++
				deleteFixtureWait(t, &reg, target)
				return nil
			}
			d.ReadIdentity = func(pid int) (m.ProcessIdentity, int, error) {
				if pid == target.Activation.Child.PID && !replacementBorn {
					replacementBorn = true
					p, _ := reg.Pane(target.Pane)
					a, _ := reg.Agent(target.Agent)
					b := target.Binding
					b.Generation = "replacement-generation"
					b.OperationID = "replacement-operation"
					b.HostInstanceID = "replacement-host"
					next := m.ProcessActivation{Binding: b, HostProcess: m.ProcessIdentity{PID: 2000000002, OwnerUID: uint32(os.Getuid()), Start: "replacement-owner"}, Child: m.ProcessIdentity{PID: 2000000003, OwnerUID: uint32(os.Getuid()), Start: "replacement-child"}}
					p.Status.ProcessSession.Binding = b
					p.Status.Activation = m.PaneActivation{Kind: m.RuntimeProcess, Generation: b.Generation, AgentUID: b.AgentUID, OperationID: b.OperationID, Process: &next}
					a.Status.Phase = m.PhaseRunning
					replacement := target
					replacement.Binding = b
					replacement.Activation = &next
					if mode == "retired" {
						deleteFixtureWait(t, &reg, replacement)
					} else {
						p.Status.Activation = m.PaneActivation{}
						a.Status.Phase = m.PhasePending
					}
				}
				return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
			}
			d.Mutate = func(_ m.Kind, _ []string, _ func(*m.Registry, m.Mutator) error) error { commits++; return nil }
			targets, err := d.ClassifyPlan(reg, plan)
			if err != nil {
				t.Fatal(err)
			}
			_, err = d.Execute(context.Background(), plan, targets, nil)
			var refused *ProcessDeleteError
			if !errors.As(err, &refused) || refused.Token != processDeleteRefusedToken || !errors.Is(err, processhost.ErrStale) || stops != 1 || commits != 0 || !replacementBorn {
				t.Fatalf("replacement retired generation deleted: stops%d commits%d born%t err%v", stops, commits, replacementBorn, err)
			}
			a, _ := reg.Agent(target.Agent)
			p, _ := reg.Pane(target.Pane)
			if p.Status.ProcessSession.Binding.Generation != "replacement-generation" {
				t.Fatal("replacement binding was changed")
			}
			if mode == "retired" && (a.Status.Phase != m.PhaseOffline || p.Status.LastTermination.Generation != "replacement-generation") {
				t.Fatal("replacement Wait evidence was changed")
			}
			if mode == "starting" && (a.Status.Phase != m.PhasePending || !p.Status.Activation.IsZero() || p.Status.LastTermination.Generation != target.Binding.Generation) {
				t.Fatal("replacement starting reservation was changed")
			}
			if !strings.Contains(refused.Cause.Error(), "prior delete-stop retired=true") {
				t.Fatal("prior admitted effect evidence lost")
			}
		})
	}
}

func TestProcessDeletePreservesStopCause(t *testing.T) {
	for _, cause := range []error{processhost.ErrBusy, processhost.ErrStale, ErrProcessStopUnsupported} {
		t.Run(cause.Error(), func(t *testing.T) {
			r, plan, target := deleteProcessFixture(t)
			calls := 0
			d := &ProcessAgentDeleter{Load: func() (m.Registry, error) { return r.Clone(), nil }, WaitLimit: 50 * time.Millisecond, StopAdmission: time.Nanosecond, Poll: time.Millisecond, Stop: func(context.Context, m.Registry, m.Agent) error { calls++; return cause }}
			_, err := d.Execute(t.Context(), plan, []ProcessDeleteTarget{target}, nil)
			var typed *ProcessDeleteError
			if !errors.As(err, &typed) || calls != 1 {
				t.Fatalf("expected one Stop and typed refusal, calls=%d err=%v", calls, err)
			}
			t.Logf("typed=true calls=%d errors.Is=%t text=%v", calls, errors.Is(err, cause), err)
			want := fmt.Sprintf("process-host-unavailable: owner host pid %d of process Agent uid %q did not accept Stop: %v; nothing was changed", target.HostPID, target.Agent, cause)
			if err.Error() != want {
				t.Errorf("receipt changed: got %q want %q", err.Error(), want)
			}
			if !errors.Is(err, cause) {
				t.Errorf("owner Stop cause lost: want errors.Is(%v), got %v", cause, err)
			}
		})
	}
}
func TestProcessDeleteRejectsReplacedClaimBeforeEffect(t *testing.T) {
	for _, mode := range []string{"nonce-and-owner", "nonce-only", "owner-only", "same-pid-different-birth"} {
		t.Run(mode, func(t *testing.T) {
			r, plan, target := deleteProcessFixture(t)
			deleteFixtureWait(t, &r, target)
			path := filepath.Join(t.TempDir(), "metadata", "registry.json")
			st := store.NewStore(path)
			if _, err := st.Update(func(dst *m.Registry) error { *dst = r.Clone(); return nil }); err != nil {
				t.Fatal(err)
			}
			spawn := func() *exec.Cmd {
				c := exec.Command("sleep", "10")
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
				return c
			}
			first := spawn()
			second := spawn()
			oldID, _, err := localipc.Process(first.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			newID, _, err := localipc.Process(second.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			p, _ := r.Pane(target.Pane)
			session := p.Status.ProcessSession
			claimPath := DeferredClaimPath(path, target.Agent)
			write := func(c DeferredClaimRecord) {
				unlock, e := LockDeferredClaim(claimPath)
				if e != nil {
					t.Fatal(e)
				}
				defer unlock()
				if e = WriteDeferredClaim(claimPath, c); e != nil {
					t.Fatal(e)
				}
			}
			claim := DeferredClaimRecord{Version: 1, Agent: target.Agent, Binding: target.Binding, Provider: session.Provider, Session: session.SessionID, Thread: session.ThreadID, Process: oldID, Nonce: "first-claim", Detached: true}
			// Model a previous birth at the same PID; never force kernel PID reuse.
			if mode == "same-pid-different-birth" {
				claim.Process.Start += "-prior-birth"
			}
			write(claim)
			d := NewProcessAgentDeleter(path)
			d.WaitLimit = 80 * time.Millisecond
			d.Poll = time.Millisecond
			targets, err := d.ClassifyPlan(r, plan)
			if err != nil {
				t.Fatal(err)
			}
			if !targets[0].Claim || targets[0].HostPID != oldID.PID {
				t.Fatal("fixture did not classify first owner")
			}
			if mode != "nonce-only" && mode != "same-pid-different-birth" {
				_ = first.Process.Kill()
				_ = first.Wait()
			}
			if mode != "owner-only" && mode != "same-pid-different-birth" {
				claim.Nonce = "replacement-claim"
			}
			if mode == "same-pid-different-birth" {
				claim.Process = oldID
			} else if mode != "nonce-only" {
				claim.Process = newID
			}
			write(claim)
			registryBefore, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			claimBefore, e := os.ReadFile(claimPath)
			if e != nil {
				t.Fatal(e)
			}
			_, err = d.Execute(context.Background(), plan, targets, nil)
			if !errors.Is(err, processhost.ErrStale) {
				t.Errorf("expected typed stale cause, got %v", err)
			}
			registryAfter, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			claimAfter, e := os.ReadFile(claimPath)
			if e != nil {
				t.Fatal(e)
			}
			if string(registryBefore) != string(registryAfter) || string(claimBefore) != string(claimAfter) {
				t.Error("replacement refusal changed Registry or claim bytes")
			}
			observed, e := ReadDeferredClaim(claimPath)
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("expectedNonce=%s expectedOwner=%+v observedNonce=%s observedOwner=%+v sameBinding=true StopRequested=%t ownerLive=%t err=%v", targets[0].ClaimNonce, targets[0].ClaimOwner, observed.Nonce, observed.Process, observed.StopRequested, DeferredClaimLive(observed), err)
			if !DeferredClaimLive(observed) {
				t.Error("replacement owner retired unexpectedly")
			}
			if observed.StopRequested {
				t.Error("preflight claim replaced: wrote StopRequested to the replacement owner")
			}
		})
	}
}

// Terminal tasks are represented here by the reader's confirmed-absence result;
// the Linux reader's S-to-Z/X race is covered in localipc, where its seam lives.
func TestProcessDeleteUnknownOwnerIdentityEvidence(t *testing.T) {
	for _, mode := range []string{"both-absent", "uncertain", "host-live", "child-live", "binding-changed"} {
		t.Run(mode, func(t *testing.T) {
			reg, plan, target := deleteProcessFixture(t)
			before := reg.Clone()
			d := NewProcessAgentDeleter(filepath.Join(t.TempDir(), "metadata", "registry.json"))
			d.Load = func() (m.Registry, error) { return reg.Clone(), nil }
			d.ReadIdentity = func(pid int) (m.ProcessIdentity, int, error) {
				if mode == "uncertain" {
					return m.ProcessIdentity{}, 0, errors.New("process identity unavailable")
				}
				if mode == "host-live" && pid == target.Activation.HostProcess.PID {
					return target.Activation.HostProcess, 0, nil
				}
				if mode == "child-live" && pid == target.Activation.Child.PID {
					return target.Activation.Child, 0, nil
				}
				return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
			}
			d.Alive = func(identity m.ProcessIdentity) bool {
				actual, _, err := d.ReadIdentity(identity.PID)
				return err == nil && actual == identity
			}
			stops, commits := 0, 0
			d.Stop = func(context.Context, m.Registry, m.Agent) error {
				stops++
				return errors.New("owner must not be stopped without evidence")
			}
			d.Mutate = func(_ m.Kind, _ []string, apply func(*m.Registry, m.Mutator) error) error {
				commits++
				return apply(&reg, store.DefaultMutator())
			}
			targets, err := d.ClassifyPlan(reg, plan)
			if mode == "host-live" && (err != nil || len(targets) != 1 || targets[0].Runtime != processDeleteRuntimeRunning) {
				t.Fatalf("live host was not classified running: %+v %v", targets, err)
			}
			if mode == "binding-changed" {
				if err != nil {
					t.Fatal(err)
				}
				pane, _ := reg.Pane(target.Pane)
				pane.Status.ProcessSession.Binding.Generation = "replacement-generation"
				before = reg.Clone()
			}
			var outcome ProcessDeleteOutcome
			if err == nil {
				outcome, err = d.Execute(context.Background(), plan, targets, nil)
			}
			if mode == "both-absent" {
				if err != nil || stops != 0 || commits != 1 || len(outcome.Targets) != 1 || outcome.Targets[0].Runtime != processDeleteRuntimeUnknown || outcome.Targets[0].Evidence != "owner-host-and-child-absent" {
					t.Fatalf("outcome=%+v err=%v stops=%d commits=%d", outcome, err, stops, commits)
				}
				if _, present := reg.Agent(target.Agent); present {
					t.Fatal("confirmed-absent unknown owner was retained")
				}
				return
			}
			if err == nil || commits != 0 || !reflect.DeepEqual(before, reg) {
				t.Fatalf("unsafe deletion: err=%v stops=%d commits=%d", err, stops, commits)
			}
		})
	}
}
