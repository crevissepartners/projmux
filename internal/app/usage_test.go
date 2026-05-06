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

func TestFormatStatusUsageRendersBothModelsHUD(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Tokens: 420, Limit: 1000, Pct: 42.0, UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Tokens: 180, Limit: 1000, Pct: 18.0, UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Tokens: 710, Limit: 1000, Pct: 71.0, UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Tokens: 550, Limit: 1000, Pct: 55.0, UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0)

	// Long-form HUD — labels are full-word and color-wrapped, bars present
	// for both 5h and wk pairs.
	if !strings.Contains(got, "Claude") || !strings.Contains(got, "Codex") {
		t.Fatalf("missing model labels: %q", got)
	}
	if !strings.Contains(got, "5h ") || !strings.Contains(got, "wk ") {
		t.Fatalf("missing window labels: %q", got)
	}
	// Check at least one filled bar cell exists for the 42% claude bar.
	if !strings.Contains(got, "█") || !strings.Contains(got, "░") {
		t.Fatalf("missing bar runes: %q", got)
	}
	// Numeric percentages must be present.
	for _, want := range []string{"42%", "18%", "71%", "55%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	// Color escapes for cyan label and green/yellow/red bars must appear.
	if !strings.Contains(got, "#[fg=cyan,bold]") {
		t.Fatalf("missing cyan label color: %q", got)
	}
	if !strings.HasSuffix(got, "#[default]") {
		t.Fatalf("must end with #[default]: %q", got)
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
	if strings.Contains(got, "Claude") {
		t.Fatalf("claude has no limit but appears in output: %q", got)
	}
	if !strings.Contains(got, "Codex") {
		t.Fatalf("codex must appear: %q", got)
	}
	if !strings.Contains(got, "50%") || !strings.Contains(got, "25%") {
		t.Fatalf("missing codex percentages: %q", got)
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

func TestFormatStatusUsageWidthTiers(t *testing.T) {
	t.Parallel()

	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Limit: 1000, Pct: 42},
		{Model: "claude", Window: usage.WindowWeekly, Limit: 1000, Pct: 18},
		{Model: "codex", Window: usage.Window5h, Limit: 1000, Pct: 71},
		{Model: "codex", Window: usage.WindowWeekly, Limit: 1000, Pct: 55},
	}

	// Tier 1: long HUD with both bars per model.
	long := formatStatusUsage(snaps, 200)
	if !strings.Contains(long, "Claude") || !strings.Contains(long, "wk ") {
		t.Fatalf("tier1 long HUD missing wk bar: %q", long)
	}
	if visualLen(long) > 200 {
		t.Fatalf("tier1 visualLen=%d > 200", visualLen(long))
	}

	// Tier 2: drop wk bars (label + 5h only).
	tier2 := formatStatusUsage(snaps, 60)
	if visualLen(tier2) > 60 {
		t.Fatalf("tier2 visualLen=%d > 60: %q", visualLen(tier2), tier2)
	}
	if !strings.Contains(tier2, "Claude") || !strings.Contains(tier2, "Codex") {
		t.Fatalf("tier2 missing labels: %q", tier2)
	}
	if !strings.Contains(tier2, "5h ") {
		t.Fatalf("tier2 must keep 5h bar: %q", tier2)
	}
	if strings.Contains(tier2, "wk ") {
		t.Fatalf("tier2 must drop wk bar: %q", tier2)
	}

	// Tier 3: drop bars, keep long labels.
	// Long-label text form is 42 cells: `Claude 5h:42% wk:18%` (20) + 3
	// separator + `Codex 5h:71% wk:55%` (19) = 42.
	tier3 := formatStatusUsage(snaps, 45)
	if visualLen(tier3) > 45 {
		t.Fatalf("tier3 visualLen=%d > 45: %q", visualLen(tier3), tier3)
	}
	if !strings.Contains(tier3, "Claude 5h:42% wk:18%") {
		t.Fatalf("tier3 long-label form missing: %q", tier3)
	}
	if strings.Contains(tier3, "█") || strings.Contains(tier3, "░") {
		t.Fatalf("tier3 must not contain bar runes: %q", tier3)
	}

	// Tier 4: single-letter labels.
	tier4 := formatStatusUsage(snaps, 30)
	if visualLen(tier4) > 30 {
		t.Fatalf("tier4 visualLen=%d > 30: %q", visualLen(tier4), tier4)
	}
	if !strings.Contains(tier4, "C 5h:42%") || !strings.Contains(tier4, "X 5h:71%") {
		t.Fatalf("tier4 short-label form missing: %q", tier4)
	}
	if strings.Contains(tier4, "Claude") {
		t.Fatalf("tier4 must drop long labels: %q", tier4)
	}

	// Tier 5: hard truncate with ellipsis.
	tier5 := formatStatusUsage(snaps, 15)
	if visualLen(tier5) > 15 {
		t.Fatalf("tier5 visualLen=%d > 15: %q", visualLen(tier5), tier5)
	}
	if !strings.HasSuffix(tier5, "…") {
		t.Fatalf("tier5 must end with ellipsis: %q", tier5)
	}
}

func TestFormatStatusUsageOverLimitShowsActualPercent(t *testing.T) {
	t.Parallel()

	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Limit: 1000, Pct: 319},
		{Model: "claude", Window: usage.WindowWeekly, Limit: 1000, Pct: 110},
	}
	got := formatStatusUsage(snaps, 0)
	if !strings.Contains(got, "319%") {
		t.Fatalf("missing actual over-limit percent: %q", got)
	}
	if !strings.Contains(got, "red,bold") {
		t.Fatalf("over-limit must use red,bold color: %q", got)
	}
}

func TestVisualLenIgnoresTmuxEscapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"#[fg=red]abc#[default]", 3},
		{"#[fg=cyan,bold]Claude#[default] 5h", 9}, // "Claude 5h"
		{"a#[fg=red]b#[default]c", 3},
		// Unterminated escape: stripper preserves verbatim → counts as runes.
		{"abc#[broken", 11},
		// `#` not followed by `[` stays literal.
		{"hash#tag", 8},
	}
	for _, tc := range cases {
		if got := visualLen(tc.in); got != tc.want {
			t.Fatalf("visualLen(%q) = %d, want %d", tc.in, got, tc.want)
		}
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
	if !strings.Contains(out, "Claude") || !strings.Contains(out, "Codex") {
		t.Fatalf("status output = %q, want HUD model labels", out)
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

func TestUsageStatusMaybeCollectThrottledOnSecondCall(t *testing.T) {
	t.Parallel()

	c := newUsageCommand()
	mgr, err := newStubManager(t, []usage.Snapshot{
		{Model: "claude", Tokens: 100},
		{Model: "codex", Tokens: 100},
	})
	if err != nil {
		t.Fatalf("newStubManager: %v", err)
	}
	c.managerFn = func() (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	// First call refreshes the cache (no marker yet → MaybeCollect runs).
	if err := c.runStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("first runStatus: %v", err)
	}
	first := stdout.String()
	stdout.Reset()
	// Second call within the throttle window: MaybeCollect short-circuits
	// but the segment still renders by reading the cache.
	if err := c.runStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("second runStatus: %v", err)
	}
	second := stdout.String()
	if first == "" || second == "" {
		t.Fatalf("expected non-empty status output, got %q / %q", first, second)
	}
}

func TestUsageStatusSwallowsAdapterErrorByDefault(t *testing.T) {
	t.Parallel()

	c := newUsageCommand()
	dir := t.TempDir()
	registry := usage.NewRegistry()
	_ = registry.Replace(&stubAdapter{
		name: "claude",
		err:  errors.New("network down"),
	})
	store := usage.NewStore(dir)
	mgr := usage.NewManager(registry, store, usage.DefaultLimits, func() time.Time {
		return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	})
	c.managerFn = func() (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.runStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty (adapter failures must be silent)", stderr.String())
	}
}

func TestUsageStatusEchoesAdapterErrorWithDebugEnv(t *testing.T) {
	t.Parallel()

	c := newUsageCommand()
	c.lookupEnv = func(name string) string {
		if name == usageDebugEnvVar {
			return "1"
		}
		return ""
	}
	dir := t.TempDir()
	registry := usage.NewRegistry()
	_ = registry.Replace(&stubAdapter{
		name: "claude",
		err:  errors.New("network down"),
	})
	store := usage.NewStore(dir)
	mgr := usage.NewManager(registry, store, usage.DefaultLimits, func() time.Time {
		return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	})
	c.managerFn = func() (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.runStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(stderr.String(), "network down") {
		t.Fatalf("stderr = %q, want adapter error surfaced under PROJMUX_USAGE_DEBUG", stderr.String())
	}
}
