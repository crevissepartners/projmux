package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

func TestFormatStatusUsageRendersBothModels(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Tokens: 420, Limit: 1000, Pct: 42.0, UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Tokens: 180, Limit: 1000, Pct: 18.0, UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Tokens: 710, Limit: 1000, Pct: 71.0, UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Tokens: 550, Limit: 1000, Pct: 55.0, UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0)
	want := "c:42% w:18% | x:71% w:55%"
	if got != want {
		t.Fatalf("formatStatusUsage = %q, want %q", got, want)
	}
}

func TestFormatStatusUsageOmitsModelsWithNoLimit(t *testing.T) {
	t.Parallel()

	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Tokens: 1, Limit: 0, Pct: 0},
		{Model: "claude", Window: usage.WindowWeekly, Tokens: 1, Limit: 0, Pct: 0},
		{Model: "codex", Window: usage.Window5h, Tokens: 100, Limit: 200, Pct: 50},
		{Model: "codex", Window: usage.WindowWeekly, Tokens: 50, Limit: 200, Pct: 25},
	}
	got := formatStatusUsage(snaps, 0)
	want := "x:50% w:25%"
	if got != want {
		t.Fatalf("formatStatusUsage = %q, want %q", got, want)
	}
}

func TestFormatStatusUsageAllEmpty(t *testing.T) {
	t.Parallel()

	if got := formatStatusUsage(nil, 0); got != "" {
		t.Fatalf("formatStatusUsage(nil) = %q, want empty", got)
	}
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Limit: 0},
	}
	if got := formatStatusUsage(snaps, 0); got != "" {
		t.Fatalf("formatStatusUsage(no limits) = %q, want empty", got)
	}
}

func TestFormatStatusUsageWidthDropsTrailingGroups(t *testing.T) {
	t.Parallel()

	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Limit: 1000, Pct: 42},
		{Model: "claude", Window: usage.WindowWeekly, Limit: 1000, Pct: 18},
		{Model: "codex", Window: usage.Window5h, Limit: 1000, Pct: 71},
		{Model: "codex", Window: usage.WindowWeekly, Limit: 1000, Pct: 55},
	}
	full := formatStatusUsage(snaps, 0)
	if !strings.Contains(full, "|") {
		t.Fatalf("full = %q, want contains separator", full)
	}
	// Width that fits only the first group ("c:42% w:18%" = 11 runes).
	got := formatStatusUsage(snaps, 11)
	if got != "c:42% w:18%" {
		t.Fatalf("width=11 got %q, want %q", got, "c:42% w:18%")
	}
}

func TestFormatStatusUsageWidthTruncatesSingleGroupWithEllipsis(t *testing.T) {
	t.Parallel()

	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Limit: 1000, Pct: 42},
		{Model: "claude", Window: usage.WindowWeekly, Limit: 1000, Pct: 18},
	}
	got := formatStatusUsage(snaps, 5)
	rs := []rune(got)
	if len(rs) != 5 {
		t.Fatalf("len(got runes) = %d, want 5; got=%q", len(rs), got)
	}
	if rs[len(rs)-1] != '…' {
		t.Fatalf("trailing rune = %q, want ellipsis", rs[len(rs)-1])
	}
}

func TestFilterSnapshotsByModelAndWindow(t *testing.T) {
	t.Parallel()

	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h},
		{Model: "claude", Window: usage.WindowWeekly},
		{Model: "codex", Window: usage.Window5h},
		{Model: "codex", Window: usage.WindowWeekly},
	}
	got := filterSnapshots(snaps, "claude", "all")
	if len(got) != 2 {
		t.Fatalf("model=claude got %d, want 2", len(got))
	}
	got = filterSnapshots(snaps, "all", "5h")
	if len(got) != 2 {
		t.Fatalf("window=5h got %d, want 2", len(got))
	}
	got = filterSnapshots(snaps, "codex", "weekly")
	if len(got) != 1 || got[0].Model != "codex" || got[0].Window != usage.WindowWeekly {
		t.Fatalf("codex+weekly got %+v, want one codex weekly", got)
	}
}

type stubAdapter struct {
	name   string
	events []usage.TokenEvent
	err    error
}

func (s *stubAdapter) Name() string { return s.name }
func (s *stubAdapter) Collect(ctx context.Context) ([]usage.TokenEvent, error) {
	return s.events, s.err
}

func newStubManager(t *testing.T, snaps []usage.Snapshot) (*usage.Manager, error) {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	registry := usage.NewRegistry()
	for _, s := range snaps {
		_ = registry.Replace(&stubAdapter{
			name:   s.Model,
			events: []usage.TokenEvent{{At: now.Add(-time.Hour), Tokens: s.Tokens}},
		})
	}
	limits := usage.Limits{
		"claude": {usage.Window5h: 1000, usage.WindowWeekly: 5000},
		"codex":  {usage.Window5h: 1000, usage.WindowWeekly: 5000},
	}
	store := usage.NewStore(dir)
	return usage.NewManager(registry, store, limits, func() time.Time { return now }), nil
}

func TestUsageRunJSONEmptyCacheReturnsArray(t *testing.T) {
	t.Parallel()

	c := newUsageCommand()
	c.managerFn = func() (*usage.Manager, error) {
		dir := t.TempDir()
		registry := usage.NewRegistry()
		_ = registry.Register(&stubAdapter{name: "claude"})
		store := usage.NewStore(dir)
		return usage.NewManager(registry, store, usage.DefaultLimits, func() time.Time {
			return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
		}), nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v err=%s", err, stderr.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "[") {
		t.Fatalf("stdout = %q, want JSON array", stdout.String())
	}
}

func TestUsageStatusEmitsFormattedSegment(t *testing.T) {
	t.Parallel()

	c := newUsageCommand()
	mgr, err := newStubManager(t, []usage.Snapshot{
		{Model: "claude", Tokens: 300},
		{Model: "codex", Tokens: 700},
	})
	if err != nil {
		t.Fatalf("newStubManager: %v", err)
	}
	// Prime the cache via Collect so LoadAll has data.
	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	c.managerFn = func() (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.runStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "c:") || !strings.Contains(out, "x:") {
		t.Fatalf("status output = %q, want both prefixes", out)
	}
}

func TestUsageStatusManagerErrorIsSilent(t *testing.T) {
	t.Parallel()

	c := newUsageCommand()
	c.managerFn = func() (*usage.Manager, error) {
		return nil, errors.New("boom")
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.runStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("runStatus must swallow error, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
