package mutations

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"golang.org/x/sys/unix"
)

func labelCancellationRegistry(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metadata", "registry.json")
	s := NewStore(path)
	result, err := RegisterProject(context.Background(), s, metadata.RegisterProjectOptions{Root: t.TempDir(), Name: "project"})
	if err != nil {
		t.Fatal(err)
	}
	var agent metadata.Agent
	_, err = s.Update(func(reg *metadata.Registry) error {
		var err error
		agent, err = s.Mutator().CreateAgent(reg, result.Windows[0].Metadata.UID, metadata.CreateAgentOptions{Name: "reader", Provider: "codex", Labels: map[string]string{"other": "kept"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return path, agent.Metadata.UID
}

// The real Store keeps its existing 30-second admission timeout. A held kernel
// lock prevents the callback from ever running, covering cancellation that the
// callback's own ctx.Err check cannot see. No global lock policy is changed.
func TestLabelProtectionCancellationBeforeLockAdmission(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			path, uid := labelCancellationRegistry(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			held, err := os.OpenFile(path+".flock", os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if err = unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				held.Close()
				t.Fatal(err)
			}
			release := sync.OnceFunc(func() {
				if err := unix.Flock(int(held.Fd()), unix.LOCK_UN); err != nil {
					t.Error(err)
				}
				if err := held.Close(); err != nil {
					t.Error(err)
				}
			})
			t.Cleanup(release)
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				want = context.DeadlineExceeded
			}
			defer cancel()
			raw := store.NewStore(path)
			observed := make(chan store.LockObservation, 1)
			raw.SetLockObserver(func(o store.LockObservation) { observed <- o })
			var callbacks atomic.Int32
			s := Store{Mutator: store.DefaultMutator, Update: func(fn func(*metadata.Registry) error) (metadata.Registry, error) {
				if ctx.Err() != nil {
					t.Fatal("context ended before entering Store.Update")
				}
				if mode == "cancel" {
					timer := time.AfterFunc(100*time.Millisecond, cancel)
					defer timer.Stop()
				}
				return raw.Update(func(reg *metadata.Registry) error { callbacks.Add(1); return fn(reg) })
			}}
			result, gotErr := SetLabels(ctx, s, metadata.KindAgent, uid, map[string]string{metadata.LabelProtected: "true"}, nil)
			observation := <-observed
			// Release and drain the real late-granted kernel waiter before asserting
			// the error, so a red test also cleans up and proves no late write occurs.
			raw.SetLockObserver(nil)
			release()
			if err := raw.WithAdmissionBarrier(func(metadata.Registry) error { return nil }); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Outcome != store.LockOutcomeTimeout || observation.Held != 0 {
				t.Fatalf("did not reproduce pre-callback lock timeout: %+v", observation)
			}
			if callbacks.Load() != 0 || !bytes.Equal(before, after) || !reflect.DeepEqual(result, LabelResult{}) {
				t.Fatalf("late callback/write or nonzero result: callbacks=%d result=%+v", callbacks.Load(), result)
			}
			if !errors.Is(gotErr, want) {
				t.Fatalf("pre-admission %s lost context cause: got %T %v; want errors.Is(%v)", mode, gotErr, gotErr, want)
			}
		})
	}
}

func TestLabelProtectionAdmittedOutcome(t *testing.T) {
	for _, writeFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "io-failure"}[writeFailure], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("injected admitted write I/O failure")
			reg := metadata.Registry{Agents: []metadata.Agent{{Metadata: metadata.ObjectMeta{UID: "agent-one", Name: "one"}}}}
			s := Store{Mutator: func() metadata.Mutator { return metadata.Mutator{} }, Update: func(fn func(*metadata.Registry) error) (metadata.Registry, error) {
				working := reg.Clone()
				if err := fn(&working); err != nil {
					return metadata.Registry{}, err
				}
				cancel()
				if writeFailure {
					return metadata.Registry{}, cause
				}
				reg = working
				return working.Clone(), nil
			}}
			result, err := SetLabels(ctx, s, metadata.KindAgent, "agent-one", map[string]string{metadata.LabelProtected: "true"}, nil)
			if writeFailure {
				if !errors.Is(err, cause) || errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, LabelResult{}) {
					t.Fatalf("admitted I/O cause overwritten: %+v %v", result, err)
				}
			} else if err != nil || !result.Changed || result.Registry.Agents[0].Metadata.Labels[metadata.LabelProtected] != "true" {
				t.Fatalf("admitted commit overwritten: %+v %v", result, err)
			}
		})
	}
}
