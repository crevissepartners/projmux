package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryManagerRecordsFreshOnlyAndPreservesSnapshotOnHistoryFailure(t *testing.T) {
	now := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	store := NewStore(t.TempDir())
	adapter := &countingAdapter{name: "claude", snaps: []Snapshot{{Model: "claude", Window: WindowQuota, Bucket: "named", Pct: 17, ResetsAt: now.Add(time.Hour)}}}
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(registry, store, func() time.Time { return now })
	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := store.ReadHistory(HistoryFilter{Now: now})
	if err != nil || len(first) != 1 || first[0].Bucket != "named" || first[0].ResetsAt == nil {
		t.Fatalf("fresh: %v %v", first, err)
	}
	if _, err := mgr.LoadAll(); err != nil {
		t.Fatal(err)
	}
	adapter.snaps = nil
	adapter.err = errors.New("adapter unavailable")
	now = now.Add(2 * time.Minute)
	if _, err := mgr.Collect(context.Background()); err == nil {
		t.Fatal("adapter failure hidden")
	}
	got, err := store.ReadHistory(HistoryFilter{Now: now})
	if err != nil || len(got) != 1 {
		t.Fatalf("stale row duplicated: %v %v", got, err)
	}
	adapter.err = nil
	adapter.snaps = []Snapshot{{Model: "claude", Window: WindowQuota, Bucket: "named", Pct: 17}}
	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = store.ReadHistory(HistoryFilter{Now: now})
	if err != nil || len(got) != 2 || got[1].Value != 17 {
		t.Fatalf("same value fresh: %v %v", got, err)
	}
	path := filepath.Join(store.HistoryPath(), segmentName(now))
	if err := os.WriteFile(path, []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	adapter.snaps[0].Pct = 29
	_, err = mgr.Collect(context.Background())
	if !errors.Is(err, ErrHistoryWrite) {
		t.Fatalf("history failure: %v", err)
	}
	state, loadErr := store.LoadState()
	if loadErr != nil || len(state.Snapshots) != 1 || state.Snapshots[0].Pct != 29 {
		t.Fatalf("snapshot lost: %v %v", state, loadErr)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
}
