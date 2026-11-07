package metadata

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

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
			// The expired call leaves its kernel wait behind on purpose; it
			// may be granted the lock late and then releases and closes it.
			// Wait for that to finish before any other transaction or
			// observer probe, so none of them races the late grant.
			waitForRegistryFlockDescriptorsClosed(t, store)
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

// waitForRegistryFlockDescriptorsClosed blocks until this process holds no
// descriptor on the Registry flock file, which is the observable end of an
// abandoned lock waiter: it was granted, unlocked and closed its descriptor.
func waitForRegistryFlockDescriptorsClosed(t *testing.T, store *Store) {
	t.Helper()
	var want unix.Stat_t
	if err := unix.Stat(store.flockPath, &want); err != nil {
		t.Fatalf("stat registry flock: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		open := 0
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Fatalf("list descriptors: %v", err)
		}
		for _, entry := range entries {
			fd, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			var got unix.Stat_t
			// The listing's own descriptor and ones closed meanwhile fail
			// here; neither can be the lock file.
			if unix.Fstat(fd, &got) != nil {
				continue
			}
			if uint64(got.Dev) == uint64(want.Dev) && got.Ino == want.Ino {
				open++
			}
		}
		if open == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("abandoned waiter never closed its descriptor: %d open on the registry flock", open)
		}
		time.Sleep(time.Millisecond)
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
		// Schema versions are normalized; an orphan Pane remains invalid.
		reg.Panes = append(reg.Panes, coremetadata.Pane{
			APIVersion: coremetadata.APIVersion,
			Kind:       coremetadata.KindPane,
			Metadata: coremetadata.ObjectMeta{
				UID:      "pane-orphan",
				Name:     "zsh",
				OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "window-missing"},
			},
			Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
		})
		return nil
	}); !errors.Is(err, coremetadata.ErrInvalidRegistry) {
		t.Fatalf("invalid context transaction: %v, want ErrInvalidRegistry", err)
	}
	if after, err := os.ReadFile(store.Path()); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("invalid context transaction changed Registry: %v", err)
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
