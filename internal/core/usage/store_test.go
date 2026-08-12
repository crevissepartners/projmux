package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed.UTC()
}

func TestStoreLoadAllMissingReturnsEmpty(t *testing.T) {
	t.Parallel()

	s := NewStore(t.TempDir())
	got, last, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if got != nil {
		t.Fatalf("LoadAll() snapshots = %v, want nil", got)
	}
	if !last.IsZero() {
		t.Fatalf("LoadAll() last = %v, want zero", last)
	}
}

func TestStoreSaveAllAndLoadRoundtrip(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "usage")
	s := NewStore(dir)
	now := mustTime(t, "2026-05-06T12:00:00Z")
	resetA := mustTime(t, "2026-05-06T17:00:00Z")
	resetB := mustTime(t, "2026-05-11T00:00:00Z")
	snaps := []Snapshot{
		{Model: "claude", Window: Window5h, Pct: 9.0, ResetsAt: resetA, UpdatedAt: now},
		{Model: "claude", Window: WindowWeekly, Pct: 12.0, ResetsAt: resetB, UpdatedAt: now},
		{Model: "codex", Window: Window5h, Pct: 0.0, ResetsAt: resetA, UpdatedAt: now},
	}
	if err := s.SaveAll(snaps, now); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	got, last, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if !last.Equal(now) {
		t.Fatalf("last = %v, want %v", last, now)
	}
	// SaveAll sorts by (model, window). Verify ordering and Pct round-trip.
	if got[0].Model != "claude" || got[0].Window != Window5h || got[0].Pct != 9.0 {
		t.Fatalf("got[0] = %+v, want claude/5h/9.0", got[0])
	}
	if got[1].Window != WindowWeekly {
		t.Fatalf("got[1].Window = %v, want weekly", got[1].Window)
	}
	if got[2].Model != "codex" {
		t.Fatalf("got[2].Model = %v, want codex", got[2].Model)
	}
	if !got[0].ResetsAt.Equal(resetA) {
		t.Fatalf("ResetsAt round-trip = %v, want %v", got[0].ResetsAt, resetA)
	}
	// Sanity: file is on disk.
	if _, err := os.Stat(s.FilePath()); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}
	if filepath.Base(s.FilePath()) != snapshotFileName {
		t.Fatalf("FilePath base = %s, want %s", filepath.Base(s.FilePath()), snapshotFileName)
	}
	assertUsageMode(t, dir, 0o700)
	assertUsageMode(t, s.FilePath(), 0o600)
}

func assertUsageMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}

func TestStoreSaveAllReplacesPriorContents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := NewStore(dir)
	now := mustTime(t, "2026-05-06T12:00:00Z")

	if err := s.SaveAll([]Snapshot{
		{Model: "claude", Window: Window5h, Pct: 50, UpdatedAt: now},
	}, now); err != nil {
		t.Fatalf("first SaveAll: %v", err)
	}
	if err := s.SaveAll([]Snapshot{
		{Model: "codex", Window: Window5h, Pct: 25, UpdatedAt: now},
	}, now); err != nil {
		t.Fatalf("second SaveAll: %v", err)
	}
	got, _, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (replace not merge)", len(got))
	}
	if got[0].Model != "codex" {
		t.Fatalf("got[0].Model = %s, want codex", got[0].Model)
	}
}

func TestStoreCleanupLegacyArtifacts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"claude.json", "codex.json", "last_collect"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	s := NewStore(dir)
	s.CleanupLegacyArtifacts()
	for _, name := range []string{"claude.json", "codex.json", "last_collect"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy file %s still present (err=%v)", name, err)
		}
	}
}

func TestSortedSnapshotsStable(t *testing.T) {
	t.Parallel()

	in := []Snapshot{
		{Model: "codex", Window: WindowWeekly, Pct: 4},
		{Model: "claude", Window: WindowWeekly, Pct: 2},
		{Model: "codex", Window: Window5h, Pct: 3},
		{Model: "claude", Window: Window5h, Pct: 1},
	}
	got := SortedSnapshots(in)
	want := []float64{1, 2, 3, 4}
	for i, s := range got {
		if s.Pct != want[i] {
			t.Fatalf("got[%d].Pct = %v, want %v", i, s.Pct, want[i])
		}
	}
}

func TestStoreQuotaBucketIdentityOrderingAndResetRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)
	now := mustTime(t, "2026-08-12T00:00:00Z")
	reset := mustTime(t, "2026-08-13T00:00:00Z")
	zero := int64(0)
	in := []Snapshot{
		{Model: "antigravity", Window: WindowQuota, Bucket: "weekly", Pct: 20, ResetsAt: reset, UpdatedAt: now},
		{Model: "antigravity", Window: WindowContext, Pct: 10, UpdatedAt: now},
		{Model: "antigravity", Window: WindowQuota, Bucket: "5h", Pct: 30, ResetsAt: reset, UpdatedAt: now},
		{Model: "antigravity", Window: WindowQuota, Bucket: "context", Pct: 0, ResetInSeconds: &zero, UpdatedAt: now},
	}
	if err := store.SaveState(State{Snapshots: in}); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("snapshots = %#v", got)
	}
	want := []struct {
		window Window
		bucket string
	}{{WindowContext, ""}, {WindowQuota, "5h"}, {WindowQuota, "context"}, {WindowQuota, "weekly"}}
	for i := range want {
		if got[i].Window != want[i].window || got[i].Bucket != want[i].bucket {
			t.Fatalf("got[%d] = %#v, want window=%q bucket=%q", i, got[i], want[i].window, want[i].bucket)
		}
	}
	if got[2].ResetInSeconds == nil || *got[2].ResetInSeconds != 0 {
		t.Fatalf("explicit reset zero lost: %#v", got[2])
	}
	if got[1].ResetInSeconds != nil || !got[1].ResetsAt.Equal(reset) {
		t.Fatalf("absolute/absent-relative reset changed: %#v", got[1])
	}
}

func TestStoreNamedQuotaMetadataRoundTripPreservesNullableOpaqueIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)
	now := mustTime(t, "2031-02-03T04:05:06Z")
	reset := mustTime(t, "2031-02-04T00:00:00Z")
	modelID := "model-redacted-id"
	surface := "surface-redacted"
	in := []Snapshot{
		{
			Model: "claude", Window: WindowQuota, Bucket: "opaque/group\nidentity", Pct: 37.5,
			ResetsAt: reset, UpdatedAt: now,
			NamedQuota: &NamedQuota{
				Kind: "kind-redacted", Group: "opaque/group\nidentity", Severity: "severity-redacted", IsActive: false,
				Scope: &NamedQuotaScope{
					Model:   &NamedQuotaModel{ID: &modelID, DisplayName: "Model\nRedacted"},
					Surface: &surface,
				},
			},
		},
		{
			Model: "claude", Window: WindowQuota, Bucket: "opaque-null", Pct: 0,
			ResetsAt: reset, UpdatedAt: now,
			NamedQuota: &NamedQuota{Kind: "kind-redacted", Group: "opaque-null", Severity: "", IsActive: true, Scope: nil},
		},
	}
	if err := store.SaveState(State{Snapshots: in}); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("snapshots = %#v", got)
	}
	byBucket := map[string]Snapshot{}
	for _, snapshot := range got {
		byBucket[snapshot.Bucket] = snapshot
	}
	full := byBucket["opaque/group\nidentity"]
	if full.NamedQuota == nil || full.NamedQuota.Group != full.Bucket || full.NamedQuota.IsActive || full.NamedQuota.Scope == nil || full.NamedQuota.Scope.Model == nil {
		t.Fatalf("typed metadata changed: %#v", full)
	}
	if full.NamedQuota.Scope.Model.ID == nil || *full.NamedQuota.Scope.Model.ID != modelID || full.NamedQuota.Scope.Model.DisplayName != "Model\nRedacted" || full.NamedQuota.Scope.Surface == nil || *full.NamedQuota.Scope.Surface != surface {
		t.Fatalf("opaque scope changed: %#v", full.NamedQuota.Scope)
	}
	if nullScope := byBucket["opaque-null"].NamedQuota; nullScope == nil || nullScope.Scope != nil {
		t.Fatalf("nullable scope changed: %#v", nullScope)
	}
}

func TestSortedSnapshotsUnknownWindowsDoNotShareDefaultRank(t *testing.T) {
	t.Parallel()
	got := SortedSnapshots([]Snapshot{
		{Model: "m", Window: "zzz"},
		{Model: "m", Window: WindowQuota, Bucket: "b"},
		{Model: "m", Window: "aaa"},
		{Model: "m", Window: Window5h},
	})
	want := []Window{Window5h, WindowQuota, "aaa", "zzz"}
	for i := range want {
		if got[i].Window != want[i] {
			t.Fatalf("order[%d] = %q, want %q: %#v", i, got[i].Window, want[i], got)
		}
	}
}
