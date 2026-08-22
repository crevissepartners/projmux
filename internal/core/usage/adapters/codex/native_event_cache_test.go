package codex

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

func nativeEventTestSnapshot(now time.Time, pct float64) usage.Snapshot {
	limitID := "codex"
	label := "General"
	cadence := int64(300)
	return usage.Snapshot{
		Model: Name, Window: usage.Window5h, Bucket: "codex", Pct: pct,
		ResetsAt: now.Add(time.Hour), UpdatedAt: now,
		Source: usage.SourceAppServer,
		RateLimit: &usage.RateLimitMetadata{
			BucketKey: "codex", LimitID: &limitID, Label: &label,
			Slot: "primary", CadenceMinutes: &cadence,
		},
	}
}

func TestNativeEventCacheRejectsMixedSourceWithoutReplacingLastGoodBatch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	cache := NewNativeEventCache(t.TempDir(), func() time.Time { return now })
	good := nativeEventTestSnapshot(now, 73)
	if err := cache.Publish([]usage.Snapshot{good}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cache.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.Source = usage.SourceRollout
	bad.FallbackReason = usage.ReasonAppServerUnavailable
	if err := cache.Publish([]usage.Snapshot{bad}); err == nil {
		t.Fatal("fallback event publish error = nil")
	}
	after, err := os.ReadFile(cache.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected mixed-source event changed durable last-good batch")
	}
	batch, err := cache.Load()
	if err != nil || len(batch.Snapshots) != 1 || batch.Snapshots[0].Pct != 73 {
		t.Fatalf("Load after rejection = %#v err %v", batch, err)
	}
	info, err := os.Stat(cache.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %v, want 0600", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(cache.FilePath()), ".codex-native-rate-limit-events.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary sidecars = %v err %v, want none", matches, err)
	}
}

func TestNativeEventCacheAtomicReplaceNeverExposesPartialEnvelope(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	now := base
	cache := NewNativeEventCache(t.TempDir(), func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	})
	if err := cache.Publish([]usage.Snapshot{nativeEventTestSnapshot(base, 0)}); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for pct := 1; pct <= 64; pct++ {
			clockMu.Lock()
			now = base.Add(time.Duration(pct) * time.Second)
			observed := now
			clockMu.Unlock()
			if err := cache.Publish([]usage.Snapshot{nativeEventTestSnapshot(observed, float64(pct))}); err != nil {
				errCh <- err
				return
			}
		}
	}()
	for {
		select {
		case err := <-errCh:
			t.Fatal(err)
		case <-done:
			batch, err := cache.Load()
			if err != nil || len(batch.Snapshots) != 1 || batch.Snapshots[0].Pct != 64 {
				t.Fatalf("final batch = %#v err %v", batch, err)
			}
			return
		default:
			batch, err := cache.Load()
			if err != nil || batch.Version != nativeEventSnapshotVersion || len(batch.Snapshots) != 1 {
				t.Fatalf("concurrent Load observed partial envelope: %#v err %v", batch, err)
			}
		}
	}
}
