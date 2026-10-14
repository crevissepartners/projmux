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
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func stopFixture() (m.Registry, m.Agent, m.Pane) {
	b := m.ProcessBinding{HostInstanceID: "host", ProjectUID: "project", WindowUID: "window", AgentUID: "agent", PaneUID: "pane", Generation: "generation", OperationID: "op"}
	a := m.Agent{Metadata: m.ObjectMeta{UID: "agent", Name: "agent", OwnerRef: &m.OwnerRef{Kind: m.KindWindow, UID: "window"}}}
	a.Spec.Provider = "codex"
	a.Status.PaneRef = "pane"
	a.Status.Phase = m.PhaseRunning
	p := m.Pane{Metadata: m.ObjectMeta{UID: "pane", Name: "pane", OwnerRef: &m.OwnerRef{Kind: m.KindAgent, UID: "agent"}}}
	p.Spec.Runtime.Kind = m.RuntimeProcess
	p.Status.ProcessSession = &m.ProcessSessionRecord{Provider: "codex", Binding: b, ThreadID: "thread", ConnectionID: "op"}
	p.Status.Activation = m.PaneActivation{Kind: m.RuntimeProcess, Generation: b.Generation, AgentUID: "agent", OperationID: "op", Process: &m.ProcessActivation{Binding: b, HostProcess: m.ProcessIdentity{PID: 100, OwnerUID: uint32(os.Getuid()), Start: "host"}, Child: m.ProcessIdentity{PID: 101, OwnerUID: uint32(os.Getuid()), Start: "child"}}}
	return m.Registry{Agents: []m.Agent{a}, Panes: []m.Pane{p}}, a.Clone(), p.Clone()
}
func writeStopWait(reg *m.Registry) {
	a, p := &reg.Agents[0], &reg.Panes[0]
	b := p.Status.ProcessSession.Binding
	code := 0
	wait := &m.TerminationEvidence{Source: m.TerminationSourceSupervisor, Classification: m.TerminationNormal, ObservedAt: time.Now(), AgentUID: a.Metadata.UID, PaneUID: p.Metadata.UID, Generation: b.Generation, OperationID: b.OperationID, ExitCode: &code}
	a.Status.Phase = m.PhaseOffline
	a.Status.LastTermination = wait
	p.Status.LastTermination = wait
	p.Status.Activation = m.PaneActivation{}
}
func TestProcessAgentStopWaitRetirementCancellationAndGeneration(t *testing.T) {
	for _, mode := range []string{"retire", "cancel-before", "cancel-at-admission", "cancel-after", "stale", "timeout", "no-wait"} {
		t.Run(mode, func(t *testing.T) {
			reg, a, p := stopFixture()
			before := reg.Clone()
			path := filepath.Join(t.TempDir(), "metadata", "registry.json")
			activation := *p.Status.Activation.Process
			prepared := reg.Clone()
			writeStopWait(&prepared)
			wait := prepared.Panes[0].Status.LastTermination
			// Prepare disk evidence outside the Stop budget, but keep Wait private
			// until CallStop admits this exact binding.
			if mode == "retire" || mode == "cancel-after" || mode == "timeout" {
				if err := WriteProcessRetirement(path, activation, wait); err != nil {
					t.Fatal(err)
				}
			}
			calls, reads := 0, 0
			var order []string
			var admittedContext context.Context
			hostAliveObserved := false
			// Success has a hang watchdog, not a scheduler/fsync speed contract.
			budget := 5 * time.Second
			if mode == "timeout" || mode == "no-wait" {
				budget = 65 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if mode == "cancel-before" {
				cancel()
			}
			stopper := ProcessAgentStopper{RegistryPath: path, Load: func() (m.Registry, error) {
				reads++
				if mode == "cancel-at-admission" {
					cancel()
				}
				return reg.Clone(), nil
			}, ReadIdentity: func(pid int) (m.ProcessIdentity, int, error) {
				if admittedContext == nil || (mode == "cancel-after" && admittedContext.Err() != nil) || calls != 1 || !m.MatchesProcessWait(activation.Binding, reg.Panes[0].Status.LastTermination) || !m.SameProcessWait(wait, reg.Agents[0].Status.LastTermination) {
					t.Fatal("identity checked before admitted exact Wait publication")
				}
				witness, err := ReadProcessRetirement(path, activation.Binding, wait)
				if err != nil || !reflect.DeepEqual(witness, activation) {
					t.Fatalf("identity checked without exact durable witness: %+v %v", witness, err)
				}
				identity := activation.HostProcess
				if pid == activation.Child.PID {
					identity = activation.Child
				} else if pid != activation.HostProcess.PID {
					t.Fatalf("unexpected identity pid %d", pid)
				}
				if mode == "timeout" {
					return identity, 0, nil
				}
				if !hostAliveObserved {
					hostAliveObserved = true
					order = append(order, "witness", "host-alive")
					return identity, 0, nil
				}
				if pid == activation.HostProcess.PID {
					order = append(order, "host-absent")
				} else {
					order = append(order, "child-absent")
				}
				return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
			}, CallStop: func(admitted context.Context, current m.Registry, target m.Agent) error {
				calls++
				admittedContext = admitted
				if target.Metadata.UID != a.Metadata.UID || target.Status.PaneRef != p.Metadata.UID || current.Panes[0].Status.ProcessSession.Binding != activation.Binding || current.Panes[0].Status.LastTermination != nil {
					t.Fatal("Stop admitted a different binding or prepublished Wait")
				}
				order = append(order, "admitted")
				if mode == "cancel-after" {
					cancel()
					if admitted.Err() != nil {
						t.Fatal("caller cancellation reached admitted Stop")
					}
				}
				if mode == "stale" {
					reg.Panes[0].Status.ProcessSession.Binding.Generation = "replacement"
				} else if mode != "no-wait" {
					reg = prepared.Clone()
					order = append(order, "wait-published")
				}
				return nil
			}}
			err := stopper.StopExact(ctx, before, a, p)
			switch mode {
			case "retire", "cancel-after":
				if err != nil || calls != 1 || !reflect.DeepEqual(order, []string{"admitted", "wait-published", "witness", "host-alive", "host-absent", "child-absent"}) || !m.SameProcessWait(wait, stopper.Termination) {
					t.Fatalf("calls%d reads%d order%v err%v", calls, reads, order, err)
				}
			case "cancel-before", "cancel-at-admission":
				if !errors.Is(err, context.Canceled) || calls != 0 || len(order) != 0 || !reflect.DeepEqual(before, reg) {
					t.Fatalf("pre-admission effect: %v", err)
				}
			case "stale":
				if !errors.Is(err, processhost.ErrStale) {
					t.Fatal(err)
				}
			default:
				var f *ProcessStopFailure
				if !errors.As(err, &f) || f.Code != "agent-stop-wait-unconfirmed" || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("unconfirmed: %v", err)
				}
			}
		})
	}
}
func TestProcessAgentStopIdempotenceAndStalePreflight(t *testing.T) {
	reg, _, original := stopFixture()
	activation := *original.Status.Activation.Process
	writeStopWait(&reg)
	a, p := reg.Agents[0].Clone(), reg.Panes[0].Clone()
	before := reg.Clone()
	path := filepath.Join(t.TempDir(), "metadata", "registry.json")
	if err := WriteProcessRetirement(path, activation, p.Status.LastTermination); err != nil {
		t.Fatal(err)
	}
	s := ProcessAgentStopper{RegistryPath: path, Load: func() (m.Registry, error) { return reg, nil }, ReadIdentity: func(int) (m.ProcessIdentity, int, error) { return m.ProcessIdentity{}, 0, localipc.ErrProcessAbsent }, CallStop: func(context.Context, m.Registry, m.Agent) error { t.Fatal("retired generation controlled"); return nil }}
	if err := s.StopExact(context.Background(), reg, a, p); err != nil || !reflect.DeepEqual(before, reg) {
		t.Fatal(err)
	}
	reg.Panes[0].Status.ProcessSession.Binding.Generation = "new"
	if err := s.StopExact(context.Background(), before, a, p); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("stale retired success: %v", err)
	}
}

func TestProcessAgentStopChildFreeClaim(t *testing.T) {
	reg, a, p := stopFixture()
	writeStopWait(&reg)
	a, p = reg.Agents[0].Clone(), reg.Panes[0].Clone()
	before := reg.Clone()
	root := t.TempDir()
	path := DeferredClaimPath(filepath.Join(root, "registry", "registry.json"), a.Metadata.UID)
	unlock, err := LockDeferredClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	child := exec.Command("sleep", "10")
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill() }()
	identity, _, err := localipc.Process(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	record := DeferredClaimRecord{Version: 1, Agent: a.Metadata.UID, Binding: p.Status.ProcessSession.Binding, Provider: "codex", Thread: "thread", Process: identity, Nonce: "claim", Detached: true}
	if err = WriteDeferredClaim(path, record); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = child.Wait(); _ = os.Remove(path); close(done) }()
	s := ProcessAgentStopper{RegistryPath: filepath.Join(root, "registry", "registry.json"), Load: func() (m.Registry, error) { return reg.Clone(), nil }, CallStop: func(context.Context, m.Registry, m.Agent) error { t.Fatal("claim called provider"); return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.StopExact(ctx, reg, a, p); err != nil || !s.ClaimReleased || !reflect.DeepEqual(before, reg) {
		t.Fatalf("claim fabricated Wait: %v", err)
	}
	<-done
}
