package metadata

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestUpdateConvergentContextHeldLockDeadlineAndObserver(t *testing.T) {
	for _, observe := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "observed"}[observe], func(t *testing.T) {
			store := NewStore(PathFor(t.TempDir()))
			if _, err := store.Update(func(*coremetadata.Registry) error { return nil }); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			var observations *[]LockObservation
			if observe {
				observations = recordLockObservations(t, store)
			}
			holdRegistryFlock(t, store)
			var calls atomic.Int32
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, changed, err := store.UpdateConvergentContext(ctx, func(*coremetadata.Registry) error {
				calls.Add(1)
				return nil
			})
			if !errors.Is(err, context.DeadlineExceeded) || changed || calls.Load() != 0 {
				t.Fatalf("lock deadline: changed=%v calls=%d err=%v", changed, calls.Load(), err)
			}
			if observe && (len(*observations) != 1 || (*observations)[0].Operation != LockOperationUpdateConvergent || (*observations)[0].Outcome != LockOutcomeTimeout) {
				t.Fatalf("deadline observation: %+v", *observations)
			}
			releaseRegistryFlock(t, store)
			// Fence through the same ordinary transaction after release. The
			// expired call must never run a deferred writer or lose the lock.
			if _, changed, err = store.UpdateConvergent(func(*coremetadata.Registry) error { return nil }); err != nil || changed {
				t.Fatalf("ordinary no-op after release: %v %v", changed, err)
			}
			after, err := os.ReadFile(store.Path())
			if err != nil || !bytes.Equal(before, after) || calls.Load() != 0 {
				t.Fatalf("late write after unlock: calls=%d err=%v", calls.Load(), err)
			}
			if store.lockTimeout != defaultLockTimeout || defaultLockTimeout != 30*time.Second {
				t.Fatal("ordinary Registry lock policy changed")
			}
		})
	}
}

func TestUpdateConvergentContextNoopValidationAndCancellation(t *testing.T) {
	store := NewStore(PathFor(t.TempDir()))
	if _, err := store.Update(func(*coremetadata.Registry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := store.UpdateConvergentContext(context.Background(), func(*coremetadata.Registry) error { return nil }); err != nil || changed {
		t.Fatalf("context no-op: %v %v", changed, err)
	}
	if _, _, err := store.UpdateConvergentContext(context.Background(), func(reg *coremetadata.Registry) error {
		reg.SchemaVersion = -1
		return nil
	}); err == nil {
		t.Fatal("context writer bypassed validation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err = store.UpdateConvergentContext(ctx, func(reg *coremetadata.Registry) error {
		reg.UpdatedAt = time.Now()
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled callback committed: %v", err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed context transaction changed Registry: %v", err)
	}
}
