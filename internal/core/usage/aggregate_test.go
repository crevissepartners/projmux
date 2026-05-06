package usage

import (
	"os"
	"testing"
	"time"
)

func TestAggregate5hWindowSumsAndComputesPct(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	buckets := []Bucket{
		{Minute: mustTime(t, "2026-05-06T08:00:00Z"), Tokens: 100}, // 4h ago — in.
		{Minute: mustTime(t, "2026-05-06T11:30:00Z"), Tokens: 50},  // 30m ago — in.
		{Minute: mustTime(t, "2026-05-06T05:00:00Z"), Tokens: 25},  // 7h ago — out.
	}
	limits := ModelLimits{Window5h: 1000, WindowWeekly: 5000}

	snaps := Aggregate("claude", buckets, limits, now)
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	five := snaps[0]
	if five.Window != Window5h {
		t.Fatalf("snaps[0].Window = %q, want %q", five.Window, Window5h)
	}
	if five.Tokens != 150 {
		t.Fatalf("5h tokens = %d, want 150", five.Tokens)
	}
	if five.Limit != 1000 {
		t.Fatalf("5h limit = %d, want 1000", five.Limit)
	}
	if five.Pct < 14.99 || five.Pct > 15.01 {
		t.Fatalf("5h pct = %v, want ~15", five.Pct)
	}
	want := mustTime(t, "2026-05-06T08:00:00Z").Add(5 * time.Hour)
	if !five.ResetsAt.Equal(want) {
		t.Fatalf("5h ResetsAt = %v, want earliest+5h = %v", five.ResetsAt, want)
	}
}

func TestAggregate5hEmptyResetsAtIsNowPlus5h(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	snaps := Aggregate("claude", nil, ModelLimits{}, now)
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	if snaps[0].Tokens != 0 {
		t.Fatalf("tokens = %d, want 0", snaps[0].Tokens)
	}
	if snaps[0].Pct != 0 {
		t.Fatalf("pct = %v, want 0 (limit unknown)", snaps[0].Pct)
	}
	want := now.Add(5 * time.Hour)
	if !snaps[0].ResetsAt.Equal(want) {
		t.Fatalf("ResetsAt = %v, want %v", snaps[0].ResetsAt, want)
	}
}

func TestAggregateWeeklyResetsAtNextMondayMidnight(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// 2026-05-06 is a Wednesday in Asia/Seoul. Next Monday = 2026-05-11 00:00.
	now := time.Date(2026, 5, 6, 15, 30, 0, 0, loc)
	snaps := Aggregate("claude", nil, ModelLimits{}, now)
	weekly := snaps[1]
	if weekly.Window != WindowWeekly {
		t.Fatalf("snaps[1].Window = %q, want %q", weekly.Window, WindowWeekly)
	}
	got := weekly.ResetsAt.In(loc)
	want := time.Date(2026, 5, 11, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("weekly ResetsAt = %v, want %v", got, want)
	}
}

func TestNextMondayMidnightOnMondayRollsForwardOneWeek(t *testing.T) {
	t.Parallel()

	loc := time.UTC
	monday := time.Date(2026, 5, 4, 0, 0, 0, 0, loc) // Monday 00:00.
	got := nextMondayMidnight(monday)
	want := time.Date(2026, 5, 11, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("nextMondayMidnight(Mon 00:00) = %v, want %v", got, want)
	}

	// Sunday 23:59 should land on the next day's midnight (Monday).
	sunday := time.Date(2026, 5, 10, 23, 59, 0, 0, loc)
	got = nextMondayMidnight(sunday)
	want = time.Date(2026, 5, 11, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("nextMondayMidnight(Sun 23:59) = %v, want %v", got, want)
	}
}

func TestSumWithinIgnoresOutOfRange(t *testing.T) {
	t.Parallel()

	now := mustTime(t, "2026-05-06T12:00:00Z")
	cutoff := now.Add(-time.Hour)
	buckets := []Bucket{
		{Minute: mustTime(t, "2026-05-06T13:00:00Z"), Tokens: 99}, // future — drop.
		{Minute: mustTime(t, "2026-05-06T11:30:00Z"), Tokens: 5},  // in.
	}
	tokens, earliest := sumWithin(buckets, cutoff, now)
	if tokens != 5 {
		t.Fatalf("tokens = %d, want 5", tokens)
	}
	if !earliest.Equal(mustTime(t, "2026-05-06T11:30:00Z")) {
		t.Fatalf("earliest = %v, want 11:30", earliest)
	}
}

func TestLoadLimitsAppliesOverrideOnTopOfDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/limits.json"
	body := `{"claude": {"5h": 12345}, "newmodel": {"weekly": 999}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	limits, err := LoadLimits(path)
	if err != nil {
		t.Fatalf("LoadLimits: %v", err)
	}
	if limits.For("claude").For(Window5h) != 12345 {
		t.Fatalf("claude 5h = %d, want 12345 (override)", limits.For("claude").For(Window5h))
	}
	// Unmodified field falls through to default.
	if limits.For("claude").For(WindowWeekly) != DefaultLimits["claude"][WindowWeekly] {
		t.Fatalf("claude weekly = %d, want default", limits.For("claude").For(WindowWeekly))
	}
	if limits.For("newmodel").For(WindowWeekly) != 999 {
		t.Fatalf("newmodel weekly = %d, want 999", limits.For("newmodel").For(WindowWeekly))
	}
}

func TestLoadLimitsMissingFileReturnsDefaults(t *testing.T) {
	t.Parallel()

	limits, err := LoadLimits("/does/not/exist.json")
	if err != nil {
		t.Fatalf("LoadLimits missing path error = %v", err)
	}
	if limits.For("claude").For(Window5h) != DefaultLimits["claude"][Window5h] {
		t.Fatalf("default 5h not preserved")
	}
}
