package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"golang.org/x/sys/unix"
)

// A real child returns actual Wait; the host is also reaped before the final
// metadata transaction. Only that new transaction's lock is held by the test.
func TestStopRequestedFinalLockDeadlineAndAdmittedCancellation(t *testing.T) {
	for _, entry := range []string{"agent", "project"} {
		for _, mode := range []string{"deadline", "cancel-after"} {
			t.Run(entry+"/"+mode, func(t *testing.T) {
				storage, _, agent, pane := stopRequestedStore(t)
				child := exec.Command("/bin/sh", "-c", "read ignored || exit 0")
				input, err := child.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err = child.Start(); err != nil {
					t.Fatal(err)
				}
				host := exec.Command("sleep", "30")
				if err = host.Start(); err != nil {
					_ = child.Process.Kill()
					_ = child.Wait()
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = input.Close()
					if child.ProcessState == nil { _ = child.Process.Kill(); _ = child.Wait() }
					if host.ProcessState == nil { _ = host.Process.Kill(); _ = host.Wait() }
				})
				hostID, _, err := localipc.Process(host.Process.Pid)
				if err != nil { t.Fatal(err) }
				childID, _, err := localipc.Process(child.Process.Pid)
				if err != nil { t.Fatal(err) }
				binding := pane.Status.ProcessSession.Binding
				activation := m.ProcessActivation{Binding: binding, HostProcess: hostID, Child: childID}
				reg, err := storage.Update(func(r *m.Registry) error {
					p, _ := r.Pane(pane.Metadata.UID)
					p.Status.Activation.Process = &activation
					return nil
				})
				if err != nil { t.Fatal(err) }
				a, _ := reg.Agent(agent.Metadata.UID)
				p, _ := reg.Pane(pane.Metadata.UID)
				agent, pane = a.Clone(), p.Clone()
				bounded, end := context.WithTimeout(context.Background(), 2*time.Second)
				defer end()
				ctx, cancel := context.WithCancel(bounded)
				defer cancel()
				deadline, _ := bounded.Deadline()
				var callbackCalls atomic.Int32
				var unlock func()
				var before []byte
				entered := false
				callStop := func(context.Context, m.Registry, m.Agent) error {
					if err := input.Close(); err != nil { return err }
					if err := child.Wait(); err != nil { return err }
					_ = host.Process.Kill()
					_ = host.Wait()
					code := child.ProcessState.ExitCode()
					wait := m.TerminationEvidence{Source: m.TerminationSourceSupervisor, Classification: m.TerminationNormal,
						ObservedAt: time.Now(), AgentUID: binding.AgentUID, PaneUID: binding.PaneUID,
						Generation: binding.Generation, OperationID: binding.OperationID, ExitCode: &code}
					_, err := storage.Update(func(r *m.Registry) error {
						return (m.Mutator{}).RecordProcessWait(r, activation, wait)
					})
					if mode == "cancel-after" { cancel() }
					return err
				}
				update := func(admitted context.Context, apply func(*m.Registry) error) (m.Registry, bool, error) {
					entered = true
					got, ok := admitted.Deadline()
					if !ok || got.After(deadline.Add(20*time.Millisecond)) || admitted.Err() != nil {
						return m.Registry{}, false, fmt.Errorf("lost admitted deadline/cancellation policy: %v %v %v", got, ok, admitted.Err())
					}
					latest, err := storage.LoadReadOnly()
					if err != nil { return m.Registry{}, false, err }
					p, _ := latest.Pane(binding.PaneUID)
					if !m.MatchesProcessWait(binding, p.Status.LastTermination) || !ProcessActivationAbsent(activation, localipc.Process) {
						return m.Registry{}, false, errors.New("held final lock before actual retirement")
					}
					held, err := os.OpenFile(storage.Path()+".flock", os.O_CREATE|os.O_RDWR, 0600)
					if err != nil { return m.Registry{}, false, err }
					if err = unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil { _ = held.Close(); return m.Registry{}, false, err }
					unlock = sync.OnceFunc(func() { _ = unix.Flock(int(held.Fd()), unix.LOCK_UN); _ = held.Close() })
					t.Cleanup(unlock)
					before, err = os.ReadFile(storage.Path())
					if err != nil { return m.Registry{}, false, err }
					if mode == "cancel-after" { unlock() }
					return storage.UpdateConvergentContext(admitted, func(r *m.Registry) error {
						callbackCalls.Add(1)
						return apply(r)
					})
				}
				started := time.Now()
				if entry == "agent" {
					s := ProcessAgentStopper{RegistryPath: storage.Path(), Load: storage.LoadReadOnly, CallStop: callStop, Update: update}
					err = s.StopExplicit(ctx, reg, agent, pane)
				} else {
					s := ProjectProcessStopExecutor{RegistryPath: storage.Path(), Load: storage.LoadReadOnly, CallStop: callStop, Update: update}
					_, err = s.Stop(ctx, binding.ProjectUID)
				}
				if time.Since(started) > 5*time.Second { t.Fatal("final lock exceeded the admitted stop deadline watchdog") }
				if !entered { t.Fatalf("final writer was not reached: %v", err) }
				if mode == "deadline" {
					if !errors.Is(err, context.DeadlineExceeded) || callbackCalls.Load() != 0 {
						t.Fatalf("held final lock: calls=%d err=%v", callbackCalls.Load(), err)
					}
				} else if err != nil || callbackCalls.Load() != 1 {
					t.Fatalf("admitted cancellation reached writer: calls=%d err=%v", callbackCalls.Load(), err)
				}
				unlock()
				if _, _, e := storage.UpdateConvergent(func(*m.Registry) error { return nil }); e != nil { t.Fatal(e) }
				after, e := os.ReadFile(storage.Path())
				if e != nil { t.Fatal(e) }
				latest, e := storage.LoadReadOnly()
				if e != nil { t.Fatal(e) }
				p, _ = latest.Pane(binding.PaneUID)
				if p.Status.ProcessSession.StopRequested != (mode == "cancel-after") {
					t.Fatal("deadline wrote a late mark or admitted stop lost its mark")
				}
				if mode == "deadline" && (!bytes.Equal(before, after) || callbackCalls.Load() != 0) {
					t.Fatal("writer survived its deadline and changed Registry after unlock")
				}
			})
		}
	}
}
