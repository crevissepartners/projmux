package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

func TestParsePercent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   float64
		wantOK bool
	}{
		{"42%", 42, true},
		{"42", 42, true},
		{"42.5 %", 42.5, true},
		{"  0% ", 0, true},
		{"", 0, false},
		{"n/a", 0, false},
		{"%", 0, false},
		{"-3%", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParsePercent(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("ParsePercent(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestCollectMissingFile(t *testing.T) {
	t.Parallel()
	a := New(t.TempDir())
	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("Collect with no sidecar = %v, want empty", snaps)
	}
}

func TestCollectEmptyBaseDir(t *testing.T) {
	t.Parallel()
	a := New("")
	snaps, err := a.Collect(context.Background())
	if err != nil || len(snaps) != 0 {
		t.Fatalf("Collect with empty baseDir = (%v, %v), want (nil, nil)", snaps, err)
	}
}

func TestWriteContextPreservesPrivateSidecarWithoutUsageRow(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "usage")
	updated := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	if err := WriteContext(dir, ContextRecord{Pct: 42, UpdatedAt: updated}); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}
	a := New(dir)
	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("Collect = %#v, conversation context must not become usage rows", snaps)
	}
	data, err := os.ReadFile(filepath.Join(dir, ContextFileName))
	if err != nil {
		t.Fatal(err)
	}
	var record ContextRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Pct != 42 || !record.UpdatedAt.Equal(updated) {
		t.Fatalf("context sidecar = %#v, want preserved diagnostic metadata", record)
	}
	for path, want := range map[string]os.FileMode{
		dir:                                 0o700,
		filepath.Join(dir, ContextFileName): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q): %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %#o, want %#o", path, got, want)
		}
	}
}

func TestWriteQuotaThenCollectKeepsOpaqueBucketsDistinct(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "usage")
	updated := time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	zero := int64(0)
	if err := WriteContext(dir, ContextRecord{ConversationID: "local", Pct: 12, UpdatedAt: updated}); err != nil {
		t.Fatal(err)
	}
	if err := WriteQuota(dir, QuotaRecord{
		UpdatedAt: updated,
		Buckets: []QuotaBucketRecord{
			{ID: "weekly", RemainingFraction: 0.2, ResetTime: reset},
			{ID: "5h", RemainingFraction: 0.4, ResetTime: reset},
			{ID: "context", RemainingFraction: 1, ResetTime: reset, ResetInSeconds: &zero},
			{ID: "quota", RemainingFraction: 0.7, ResetTime: reset},
			{ID: "   ", RemainingFraction: 0.6, ResetTime: reset},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snaps, err := New(dir).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 5 {
		t.Fatalf("snapshots = %#v, want five quota buckets only", snaps)
	}
	wantIDs := []string{"   ", "5h", "context", "quota", "weekly"}
	for i, wantID := range wantIDs {
		got := snaps[i]
		if got.Window != usage.WindowQuota || got.Bucket != wantID {
			t.Fatalf("snapshot[%d] = %#v, want quota bucket %q", i, got, wantID)
		}
	}
	contextBucket := snaps[2]
	if contextBucket.Pct != 0 || contextBucket.ResetInSeconds == nil || *contextBucket.ResetInSeconds != 0 {
		t.Fatalf("quota/context = %#v, want genuine 0%% and explicit relative reset zero", contextBucket)
	}
	if got := snaps[0].Pct; got != 40 {
		t.Fatalf("used pct for remaining 0.6 = %v, want 40", got)
	}
	assertMode := func(path string, want os.FileMode) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode %s = %v, want %#o", path, info.Mode().Perm(), want)
		}
	}
	assertMode(filepath.Join(dir, QuotaFileName), 0o600)
}

func TestCollectRejectsInvalidQuotaRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now().UTC()
	negative := int64(-1)
	if err := WriteQuota(dir, QuotaRecord{UpdatedAt: now, Buckets: []QuotaBucketRecord{
		{ID: "", RemainingFraction: 0.5},
		{ID: "high", RemainingFraction: 1.1},
		{ID: "low", RemainingFraction: -0.1},
		{ID: "nan", RemainingFraction: math.NaN()},
		{ID: "relative", RemainingFraction: 0.5, ResetInSeconds: &negative},
	}}); err == nil {
		// encoding/json rejects NaN before invalid data reaches the reader.
		t.Fatal("WriteQuota with NaN succeeded, want safe rejection")
	}
	// Seed finite invalid records directly to verify defensive read filtering.
	data := []byte(`{"buckets":[{"id":"","remaining_fraction":0.5},{"id":"high","remaining_fraction":1.1},{"id":"low","remaining_fraction":-0.1},{"id":"relative","remaining_fraction":0.5,"reset_in_seconds":-1}],"updated_at":"2026-08-12T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, QuotaFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	snaps, err := New(dir).Collect(context.Background())
	if len(snaps) != 0 {
		t.Fatalf("Collect invalid quota rows = %#v, want none fabricated", snaps)
	}
	// Row-level skip rule: the buckets are dropped rather than coerced, and
	// the drop is now visible instead of silently degrading to nothing.
	want := "skipped 4 usage rows: bucket 0: invalid quota bucket; bucket 1: invalid quota bucket; bucket 2: invalid quota bucket; bucket 3: invalid quota bucket"
	if err == nil || err.Error() != want {
		t.Fatalf("Collect warning = %v, want %q", err, want)
	}
	if !errors.Is(err, usage.ErrRowsSkipped) {
		t.Fatalf("Collect error %v is not classified as a row-skip warning", err)
	}
}

// TestCollectSkipsInvalidBucketsAndKeepsTheRest is the row-level-skip
// contract: a broken bucket must not cost the user the healthy ones, and the
// reason must carry the row index only — bucket IDs are opaque upstream
// identities that must never reach a warning or the operations journal.
func TestCollectSkipsInvalidBucketsAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := []byte(`{"buckets":[` +
		`{"id":"aaa-keep","remaining_fraction":0.25},` +
		`{"id":"bbb-secret-bucket-id","remaining_fraction":1.5},` +
		`{"id":"ccc-keep","remaining_fraction":0.75},` +
		`{"id":"ccc-keep","remaining_fraction":0.10}` +
		`],"updated_at":"2026-08-12T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, QuotaFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	snaps, err := New(dir).Collect(context.Background())
	if len(snaps) != 2 {
		t.Fatalf("snaps = %#v, want the two healthy buckets", snaps)
	}
	if snaps[0].Bucket != "aaa-keep" || snaps[1].Bucket != "ccc-keep" {
		t.Fatalf("surviving buckets = %#v", snaps)
	}
	if snaps[0].Pct != 75 || snaps[1].Pct != 25 {
		t.Fatalf("surviving percentages = %#v, want the upstream values untouched", snaps)
	}
	want := "skipped 2 usage rows: bucket 1: invalid quota bucket; bucket 3: duplicate bucket id"
	if err == nil || err.Error() != want {
		t.Fatalf("Collect warning = %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), "bbb-secret-bucket-id") {
		t.Fatalf("warning leaked an opaque bucket identity: %v", err)
	}
}

func TestValidQuotaBucketRejectsNonFiniteFractions(t *testing.T) {
	t.Parallel()
	for _, fraction := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01, 1.01} {
		if ValidQuotaBucket(QuotaBucketRecord{ID: "bucket", RemainingFraction: fraction}) {
			t.Fatalf("fraction %v accepted", fraction)
		}
	}
	if !ValidQuotaBucket(QuotaBucketRecord{ID: "   ", RemainingFraction: 0.5}) {
		t.Fatal("whitespace-only opaque bucket ID should remain distinct and accepted")
	}
}

func TestCollectMalformedQuotaDoesNotReturnPartialContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := WriteContext(dir, ContextRecord{Pct: 42, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, QuotaFileName), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	snaps, err := New(dir).Collect(context.Background())
	if err == nil || len(snaps) != 0 {
		t.Fatalf("Collect malformed quota = (%#v, %v), want error and no partial rows", snaps, err)
	}
}

func TestExplicitEmptyQuotaHonorsManagerReplaceSemantics(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		withContext bool
	}{
		{name: "zero adapter rows preserve prior model"},
		{name: "private context does not alter quota replacement", withContext: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := usage.NewStore(dir)
			if err := store.SaveState(usage.State{Snapshots: []usage.Snapshot{{
				Model: Name, Window: usage.WindowQuota, Bucket: "old", Pct: 80, UpdatedAt: now.Add(-time.Hour),
			}}}); err != nil {
				t.Fatal(err)
			}
			if err := WriteQuota(dir, QuotaRecord{Buckets: []QuotaBucketRecord{}, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if tc.withContext {
				if err := WriteContext(dir, ContextRecord{Pct: 10, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			registry := usage.NewRegistry()
			if err := registry.Register(New(dir)); err != nil {
				t.Fatal(err)
			}
			got, err := usage.NewManager(registry, store, func() time.Time { return now }).Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			priorQuota, contexts := false, 0
			for _, snap := range got {
				priorQuota = priorQuota || (snap.Window == usage.WindowQuota && snap.Bucket == "old")
				if snap.Window == usage.WindowContext {
					contexts++
				}
			}
			if !priorQuota || contexts != 0 {
				t.Fatalf("snapshots = %#v, priorQuota=%v contexts=%d", got, priorQuota, contexts)
			}
		})
	}
}

func TestCollectIgnoresMalformedPrivateContextSidecar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ContextFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := New(dir)
	snaps, err := a.Collect(context.Background())
	if err != nil || len(snaps) != 0 {
		t.Fatalf("Collect on malformed private context = (%#v, %v), want empty usage", snaps, err)
	}
}

func TestContextSidecarDoesNotMaskQuotaRows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now().UTC()
	if err := WriteContext(dir, ContextRecord{Pct: -5, UpdatedAt: now}); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}
	if err := WriteQuota(dir, QuotaRecord{UpdatedAt: now, Buckets: []QuotaBucketRecord{{ID: "gemini-weekly", RemainingFraction: 0.25}}}); err != nil {
		t.Fatalf("WriteQuota: %v", err)
	}
	snaps, err := New(dir).Collect(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("Collect = (%v, %v), want 1 snapshot", snaps, err)
	}
	if snaps[0].Window != usage.WindowQuota || snaps[0].Bucket != "gemini-weekly" || snaps[0].Pct != 75 {
		t.Fatalf("quota snapshot = %#v", snaps[0])
	}
}
