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
		{Model: "claude", Window: usage.Window5h, Pct: 42.0, ResetsAt: now.Add(5 * time.Hour), UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18.0, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Pct: 71.0, ResetsAt: now.Add(5 * time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55.0, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)

	if !strings.Contains(got, "Claude") || !strings.Contains(got, "Codex") {
		t.Fatalf("missing model labels: %q", got)
	}
	if !strings.Contains(got, "5h ") || !strings.Contains(got, "weekly ") {
		t.Fatalf("missing window labels: %q", got)
	}
	if !strings.Contains(got, "█") || !strings.Contains(got, "░") {
		t.Fatalf("missing bar runes: %q", got)
	}
	for _, want := range []string{"42%", "18%", "71%", "55%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !strings.Contains(got, "#[fg=cyan,bold]") {
		t.Fatalf("missing cyan label color: %q", got)
	}
	if !strings.HasSuffix(got, "#[default]") {
		t.Fatalf("must end with #[default]: %q", got)
	}
}

func TestFormatStatusUsageOmitsPlaceholderRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	// Claude rows here have Pct=0 AND ResetsAt zero AND Limit=0 → treated
	// as "no data" placeholders that the HUD must skip. Codex has real
	// percentages and must still appear.
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 0, UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 0, UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 25, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)
	if strings.Contains(got, "Claude") {
		t.Fatalf("claude has no data but appears in output: %q", got)
	}
	if !strings.Contains(got, "Codex") {
		t.Fatalf("codex must appear: %q", got)
	}
	if !strings.Contains(got, "50%") || !strings.Contains(got, "25%") {
		t.Fatalf("missing codex percentages: %q", got)
	}
}

func TestFormatStatusUsageRendersGenuineZeroWithResetTime(t *testing.T) {
	t.Parallel()

	// A genuine 0% from a healthy account (Pct=0 but ResetsAt is real)
	// must still render — that's a real measurement, not a placeholder.
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 0, ResetsAt: now.Add(5 * time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "Claude") {
		t.Fatalf("genuine 0%% must still render label: %q", got)
	}
	if !strings.Contains(got, "0%") {
		t.Fatalf("missing 0%% text: %q", got)
	}
}

func TestFormatStatusUsageAllEmpty(t *testing.T) {
	t.Parallel()

	if got := formatStatusUsage(nil, 0, time.Time{}); got != "" {
		t.Fatalf("formatStatusUsage(nil) = %q, want empty", got)
	}
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h},
	}
	if got := formatStatusUsage(snaps, 0, time.Time{}); got != "" {
		t.Fatalf("formatStatusUsage(no data) = %q, want empty", got)
	}
}

func TestFormatStatusUsageWidthTiers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: now.Add(time.Hour)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: now.Add(7 * 24 * time.Hour)},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: now.Add(time.Hour)},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55, ResetsAt: now.Add(7 * 24 * time.Hour)},
	}

	// Tier 1: long HUD with both bars per model.
	long := formatStatusUsage(snaps, 200, now)
	if !strings.Contains(long, "Claude") || !strings.Contains(long, "weekly ") {
		t.Fatalf("tier1 long HUD missing weekly bar: %q", long)
	}
	if visualLen(long) > 200 {
		t.Fatalf("tier1 visualLen=%d > 200", visualLen(long))
	}

	// Tier 2: drop weekly bars (label + 5h only).
	tier2 := formatStatusUsage(snaps, 60, now)
	if visualLen(tier2) > 60 {
		t.Fatalf("tier2 visualLen=%d > 60: %q", visualLen(tier2), tier2)
	}
	if !strings.Contains(tier2, "Claude") || !strings.Contains(tier2, "Codex") {
		t.Fatalf("tier2 missing labels: %q", tier2)
	}
	if !strings.Contains(tier2, "5h ") {
		t.Fatalf("tier2 must keep 5h bar: %q", tier2)
	}
	if strings.Contains(tier2, "weekly ") {
		t.Fatalf("tier2 must drop weekly bar: %q", tier2)
	}

	// Tier 3: drop bars, keep long labels.
	tier3 := formatStatusUsage(snaps, 50, now)
	if visualLen(tier3) > 50 {
		t.Fatalf("tier3 visualLen=%d > 50: %q", visualLen(tier3), tier3)
	}
	if !strings.Contains(tier3, "Claude 5h:42% weekly:18%") {
		t.Fatalf("tier3 long-label form missing: %q", tier3)
	}
	if strings.Contains(tier3, "█") || strings.Contains(tier3, "░") {
		t.Fatalf("tier3 must not contain bar runes: %q", tier3)
	}

	// Tier 4: single-letter labels.
	tier4 := formatStatusUsage(snaps, 45, now)
	if visualLen(tier4) > 45 {
		t.Fatalf("tier4 visualLen=%d > 45: %q", visualLen(tier4), tier4)
	}
	if !strings.Contains(tier4, "C 5h:42%") || !strings.Contains(tier4, "X 5h:71%") {
		t.Fatalf("tier4 short-label form missing: %q", tier4)
	}
	if strings.Contains(tier4, "Claude") {
		t.Fatalf("tier4 must drop long labels: %q", tier4)
	}

	// Tier 5: hard truncate with ellipsis.
	tier5 := formatStatusUsage(snaps, 15, now)
	if visualLen(tier5) > 15 {
		t.Fatalf("tier5 visualLen=%d > 15: %q", visualLen(tier5), tier5)
	}
	if !strings.HasSuffix(tier5, "…") {
		t.Fatalf("tier5 must end with ellipsis: %q", tier5)
	}
}

func TestFormatStatusUsageOverLimitShowsActualPercent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 319, ResetsAt: now.Add(time.Hour)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 110, ResetsAt: now.Add(7 * 24 * time.Hour)},
	}
	got := formatStatusUsage(snaps, 0, now)
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
		{"#[fg=cyan,bold]Claude#[default] 5h", 9},
		{"a#[fg=red]b#[default]c", 3},
		{"abc#[broken", 11},
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

// stubAdapter emits Snapshots directly under the v2 contract.
type stubAdapter struct {
	name  string
	snaps []usage.Snapshot
	err   error
}

func (s *stubAdapter) Name() string { return s.name }
func (s *stubAdapter) Collect(ctx context.Context) ([]usage.Snapshot, error) {
	return s.snaps, s.err
}

func newStubManager(t *testing.T, adapters []*stubAdapter) *usage.Manager {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	registry := usage.NewRegistry()
	for _, a := range adapters {
		_ = registry.Replace(a)
	}
	store := usage.NewStore(dir)
	return usage.NewManager(registry, store, func() time.Time { return now })
}

func TestUsageRunJSONEmptyCacheReturnsArray(t *testing.T) {
	t.Parallel()

	c := newUsageCommand()
	c.managerFn = func() (*usage.Manager, error) {
		dir := t.TempDir()
		registry := usage.NewRegistry()
		_ = registry.Register(&stubAdapter{name: "claude"})
		store := usage.NewStore(dir)
		return usage.NewManager(registry, store, func() time.Time {
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

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	c := newUsageCommand()
	mgr := newStubManager(t, []*stubAdapter{
		{name: "claude", snaps: []usage.Snapshot{
			{Model: "claude", Window: usage.Window5h, Pct: 30, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		}},
		{name: "codex", snaps: []usage.Snapshot{
			{Model: "codex", Window: usage.Window5h, Pct: 70, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		}},
	})
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

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	c := newUsageCommand()
	mgr := newStubManager(t, []*stubAdapter{
		{name: "claude", snaps: []usage.Snapshot{
			{Model: "claude", Window: usage.Window5h, Pct: 5, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		}},
		{name: "codex", snaps: []usage.Snapshot{
			{Model: "codex", Window: usage.Window5h, Pct: 10, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		}},
	})
	c.managerFn = func() (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.runStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("first runStatus: %v", err)
	}
	first := stdout.String()
	stdout.Reset()
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
	mgr := usage.NewManager(registry, store, func() time.Time {
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
	mgr := usage.NewManager(registry, store, func() time.Time {
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

func TestFormatStatusUsageHUDOmitsTildeStaleMarker(t *testing.T) {
	t.Parallel()

	// Schema v3 of the HUD replaces the legacy `~` / `~~` stale
	// markers with an explicit age indicator (see TestFormatStatusUsageAge*).
	// The colored marker forms must never appear in the rendered output.
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-15 * time.Minute)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: stale},
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)
	if strings.Contains(got, "~") {
		t.Fatalf("HUD must not emit `~` stale marker: %q", got)
	}
	// Codex stays clean too — no age, no `~`.
	if strings.Contains(got, "(") && strings.Contains(got, ")Codex") {
		t.Fatalf("codex must not carry an age indicator: %q", got)
	}
}

func TestFormatStatusUsageTextTiersOmitTildeMarker(t *testing.T) {
	t.Parallel()

	// Text tiers used to append `~` / `~~` to stale rows. Those
	// markers are gone everywhere now (the long HUD tier carries the
	// signal via the age indicator; verbose CLI inspection uses the
	// `STALE` column in `projmux usage`'s table form).
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-15 * time.Minute)
	veryStale := now.Add(-90 * time.Minute)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: now.Add(time.Hour), UpdatedAt: veryStale},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: stale},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
	}
	// Width=45 lands us in the long-text tier. Width=30 lands us in
	// the short-label tier. Both must be `~`-free.
	for _, w := range []int{45, 30} {
		got := formatStatusUsage(snaps, w, now)
		if strings.Contains(got, "~") {
			t.Fatalf("text tier (width=%d) emitted `~`: %q", w, got)
		}
	}
}

func TestUsageTableShowsStaleColumn(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-30 * time.Minute)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: stale},
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	out := &bytes.Buffer{}
	if err := writeUsageTable(out, snaps, now); err != nil {
		t.Fatalf("writeUsageTable: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "STALE") {
		t.Fatalf("table header missing STALE column: %q", body)
	}
	// Claude row must have a `*` in the STALE column; codex row must not.
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("table too short: %q", body)
	}
	var claudeLine, codexLine string
	for _, l := range lines[1:] {
		if strings.Contains(l, "claude") {
			claudeLine = l
		}
		if strings.Contains(l, "codex") {
			codexLine = l
		}
	}
	if !strings.Contains(claudeLine, "*") {
		t.Fatalf("claude row missing stale marker: %q", claudeLine)
	}
	if strings.Contains(codexLine, "*") {
		t.Fatalf("codex row should be fresh, not stale-marked: %q", codexLine)
	}
}

func TestUsageJSONIncludesStaleAndBackoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-30 * time.Minute)},
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	state := usage.State{
		Backoff: map[string]usage.BackoffState{
			"claude": {Until: now.Add(5 * time.Minute), Consecutive: 1},
		},
	}
	out := &bytes.Buffer{}
	if err := writeUsageJSON(out, snaps, state, now); err != nil {
		t.Fatalf("writeUsageJSON: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `"stale": true`) {
		t.Fatalf("missing stale=true for claude row: %s", body)
	}
	if !strings.Contains(body, `"stale": false`) {
		t.Fatalf("missing stale=false for codex row: %s", body)
	}
	if !strings.Contains(body, `"backoff"`) {
		t.Fatalf("missing backoff block: %s", body)
	}
	if !strings.Contains(body, `"claude"`) {
		t.Fatalf("backoff block missing claude entry: %s", body)
	}
}

func TestUsageJSONHealthyOmitsBackoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	out := &bytes.Buffer{}
	if err := writeUsageJSON(out, snaps, usage.State{}, now); err != nil {
		t.Fatalf("writeUsageJSON: %v", err)
	}
	body := strings.TrimSpace(out.String())
	if !strings.HasPrefix(body, "[") {
		t.Fatalf("healthy --json should emit bare array: %s", body)
	}
	if !strings.Contains(body, `"stale": false`) {
		t.Fatalf("missing per-row stale field: %s", body)
	}
}

// forceTrackingAdapter records whether ForceCollect was the entry
// point: `--force` clears the persisted backoff via the Manager, so
// the adapter sees an empty BackoffState even when one was on disk.
// Save echoes whatever was loaded so the on-disk state round-trips
// (mirroring how the real claude adapter preserves backoff during a
// short-circuit).
type forceTrackingAdapter struct {
	stubAdapter
	loadedBackoff usage.BackoffState
	collectCalls  int
}

func (a *forceTrackingAdapter) LoadBackoff(state usage.BackoffState) {
	a.loadedBackoff = state
}
func (a *forceTrackingAdapter) SaveBackoff() usage.BackoffState {
	return a.loadedBackoff
}
func (a *forceTrackingAdapter) Collect(ctx context.Context) ([]usage.Snapshot, error) {
	a.collectCalls++
	return a.snaps, a.err
}

func TestUsageRunForceBypassesBackoffAndThrottle(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store := usage.NewStore(dir)
	// Seed: claude in active backoff.
	if err := store.SaveState(usage.State{
		Backoff: map[string]usage.BackoffState{
			"claude": {Until: now.Add(30 * time.Minute), Consecutive: 4},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	registry := usage.NewRegistry()
	claudeAd := &forceTrackingAdapter{
		stubAdapter: stubAdapter{
			name: "claude",
			snaps: []usage.Snapshot{
				{Model: "claude", Window: usage.Window5h, Pct: 9.0, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
			},
		},
	}
	if err := registry.Replace(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	mgr := usage.NewManager(registry, store, func() time.Time { return now })

	c := newUsageCommand()
	c.managerFn = func() (*usage.Manager, error) { return mgr, nil }
	c.now = func() time.Time { return now }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--force"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	if claudeAd.collectCalls != 1 {
		t.Fatalf("collect calls = %d, want 1 (--force must invoke despite backoff)", claudeAd.collectCalls)
	}
	if !claudeAd.loadedBackoff.Until.IsZero() {
		t.Fatalf("LoadBackoff received Until=%v, want zero (--force clears)", claudeAd.loadedBackoff.Until)
	}
	if claudeAd.loadedBackoff.Consecutive != 0 {
		t.Fatalf("LoadBackoff received Consecutive=%d, want 0 (--force clears)", claudeAd.loadedBackoff.Consecutive)
	}
	out := stdout.String()
	if !strings.Contains(out, "claude") || !strings.Contains(out, "9%") {
		t.Fatalf("expected refreshed claude row in output: %q", out)
	}
}

func TestUsageRunDefaultRespectsBackoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store := usage.NewStore(dir)
	prior := usage.Snapshot{Model: "claude", Window: usage.Window5h, Pct: 18.0, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-time.Minute)}
	if err := store.SaveState(usage.State{
		Snapshots: []usage.Snapshot{prior},
		Backoff: map[string]usage.BackoffState{
			"claude": {Until: now.Add(30 * time.Minute), Consecutive: 1},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	registry := usage.NewRegistry()
	claudeAd := &forceTrackingAdapter{
		stubAdapter: stubAdapter{name: "claude"},
	}
	if err := registry.Replace(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	mgr := usage.NewManager(registry, store, func() time.Time { return now })

	c := newUsageCommand()
	c.managerFn = func() (*usage.Manager, error) { return mgr, nil }
	c.now = func() time.Time { return now }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run(nil, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	if !claudeAd.loadedBackoff.Until.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("LoadBackoff Until = %v, want preserved 30m (no --force)", claudeAd.loadedBackoff.Until)
	}
	out := stdout.String()
	// Output should still show prior 18% (preserved during backoff
	// short-circuit) AND a backoff note pointing at --force.
	if !strings.Contains(out, "18%") {
		t.Fatalf("expected preserved 18%% in output: %q", out)
	}
	if !strings.Contains(out, "claude is in backoff") {
		t.Fatalf("expected backoff note in output: %q", out)
	}
	if !strings.Contains(out, "--force") {
		t.Fatalf("backoff note must mention --force: %q", out)
	}
}

func TestWriteBackoffNoteEmitsWhenActive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	state := usage.State{
		Backoff: map[string]usage.BackoffState{
			"claude": {Until: now.Add(12 * time.Minute), Consecutive: 2},
		},
	}
	out := &bytes.Buffer{}
	writeBackoffNote(out, state, now)
	got := out.String()
	if !strings.Contains(got, "claude is in backoff") {
		t.Fatalf("missing backoff note: %q", got)
	}
	if !strings.Contains(got, "12m") {
		t.Fatalf("missing remaining duration: %q", got)
	}
	if !strings.Contains(got, "--force") {
		t.Fatalf("must point at --force: %q", got)
	}
}

func TestWriteBackoffNoteSilentWhenHealthy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	state := usage.State{Backoff: map[string]usage.BackoffState{}}
	out := &bytes.Buffer{}
	writeBackoffNote(out, state, now)
	if out.Len() != 0 {
		t.Fatalf("expected silent on healthy state, got %q", out.String())
	}

	// Expired backoff (Until in the past) is also no-op.
	state = usage.State{Backoff: map[string]usage.BackoffState{
		"claude": {Until: now.Add(-time.Minute), Consecutive: 1},
	}}
	out = &bytes.Buffer{}
	writeBackoffNote(out, state, now)
	if out.Len() != 0 {
		t.Fatalf("expected silent on expired backoff, got %q", out.String())
	}
}

func TestFormatBackoffDurationShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "1s"},
		{45 * time.Second, "45s"},
		{2 * time.Minute, "2m"},
		{12 * time.Minute, "12m"},
		{60 * time.Minute, "1h"},
		{75 * time.Minute, "1h15m"},
	}
	for _, tc := range cases {
		if got := formatBackoffDuration(tc.in); got != tc.want {
			t.Fatalf("formatBackoffDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatStatusUsageAgeFreshOmitsIndicator covers the `now` case
// from the spec: an age below 1 minute keeps the bar tight by
// suppressing the `(<age>)` block entirely.
func TestFormatStatusUsageAgeFreshOmitsIndicator(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-30 * time.Second)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-30 * time.Second)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if strings.Contains(got, "(") {
		t.Fatalf("fresh data must not render an age indicator: %q", got)
	}
	// Sanity: the label and bar still render.
	if !strings.Contains(got, "Claude") {
		t.Fatalf("missing Claude label: %q", got)
	}
}

// TestFormatStatusUsageAgeMinutesGrey covers the spec's `(3m)`
// scenario — minute-scale age renders in dim grey.
func TestFormatStatusUsageAgeMinutesGrey(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-3 * time.Minute)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-3 * time.Minute)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "#[fg=colour245](3m)#[default]") {
		t.Fatalf("missing dim-grey (3m) age indicator: %q", got)
	}
}

// TestFormatStatusUsageAgeWarnYellow covers the 1h..6h band — the
// indicator switches to dim yellow to flag attention.
func TestFormatStatusUsageAgeWarnYellow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-90 * time.Minute)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-90 * time.Minute)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "#[fg=yellow](1h)#[default]") {
		t.Fatalf("missing dim-yellow (1h) age indicator: %q", got)
	}
}

// TestFormatStatusUsageAgeAlertRedBold covers the >=6h band — the
// indicator switches to bold red. The unit stays the actual hours
// value (e.g. `8h`) so the user knows exactly how stale the cache is.
func TestFormatStatusUsageAgeAlertRedBold(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-8 * time.Hour)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-8 * time.Hour)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "#[fg=red,bold](8h)#[default]") {
		t.Fatalf("missing bold-red (8h) age indicator: %q", got)
	}
}

// TestFormatStatusUsageCodexNoAgeIndicator confirms the Codex block
// never carries the age indicator (its rate_limits payload is sourced
// from the latest rollout file every call — always near-current).
func TestFormatStatusUsageCodexNoAgeIndicator(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	// Even a deliberately ancient Codex UpdatedAt must not produce
	// an age block.
	snaps := []usage.Snapshot{
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-12 * time.Hour)},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 25, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-12 * time.Hour)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if strings.Contains(got, "(") {
		t.Fatalf("codex rendered an age indicator: %q", got)
	}
}

// TestFormatStatusUsageAgeDropsOnTier2 verifies that when the budget
// can't fit the long-with-age tier, the renderer falls back to the
// (current default) long form WITHOUT the age block — rather than
// jumping straight to the bar-less text tier. This matches the spec's
// width-tier degradation table.
func TestFormatStatusUsageAgeDropsOnTier2(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-3 * time.Minute)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: now.Add(time.Hour), UpdatedAt: stale},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: stale},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
	}
	// Tier 1 (with age) renders to width 70.
	tier1 := formatStatusUsage(snaps, 200, now)
	if !strings.Contains(tier1, "(3m)") {
		t.Fatalf("tier1 missing age indicator at unconstrained width: %q", tier1)
	}
	tier1Width := visualLen(tier1)
	// Pick a budget that fits tier 2 but not tier 1.
	budget := tier1Width - 1
	tier2 := formatStatusUsage(snaps, budget, now)
	if strings.Contains(tier2, "(3m)") {
		t.Fatalf("tier2 must drop age indicator: %q", tier2)
	}
	if !strings.Contains(tier2, "weekly ") {
		t.Fatalf("tier2 must keep the weekly bar: %q", tier2)
	}
	if visualLen(tier2) > budget {
		t.Fatalf("tier2 visualLen=%d > budget=%d: %q", visualLen(tier2), budget, tier2)
	}
}

// TestFormatLastSyncAgeUnits covers the formatLastSyncAge unit
// ladder: <1m → "" (omit), 1m..1h → minutes, 1h..24h → hours, >=24h
// → days.
func TestFormatLastSyncAgeUnits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, ""},
		{59 * time.Second, ""},
		{60 * time.Second, "1m"},
		{3 * time.Minute, "3m"},
		{59 * time.Minute, "59m"},
		{60 * time.Minute, "1h"},
		{8 * time.Hour, "8h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	}
	for _, tc := range cases {
		if got := formatLastSyncAge(tc.in); got != tc.want {
			t.Fatalf("formatLastSyncAge(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStaleLevelCorrectness(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		age  time.Duration
		want int
	}{
		{"fresh", 1 * time.Minute, 0},
		{"just under stale", 9 * time.Minute, 0},
		{"stale", 30 * time.Minute, 1},
		{"just under very stale", 59 * time.Minute, 1},
		{"very stale", 2 * time.Hour, 2},
	}
	for _, tc := range cases {
		s := usage.Snapshot{UpdatedAt: now.Add(-tc.age)}
		if got := staleLevel(s, now); got != tc.want {
			t.Fatalf("staleLevel(%s) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestUsageHelpDocumentsForceFlag(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}
	printUsageHelp(out)
	body := out.String()
	if !strings.Contains(body, "--force") {
		t.Fatalf("help missing --force flag: %s", body)
	}
	if !strings.Contains(body, "-f") {
		t.Fatalf("help missing -f shorthand: %s", body)
	}
}

func TestResolveStateDirHonoursEnvOverride(t *testing.T) {
	t.Parallel()

	c := newUsageCommand()
	want := "/tmp/projmux-shared-usage"
	c.lookupEnv = func(name string) string {
		if name == stateDirEnvVar {
			return want
		}
		return ""
	}
	got, err := c.resolveStateDir()
	if err != nil {
		t.Fatalf("resolveStateDir: %v", err)
	}
	if got != want {
		t.Fatalf("resolveStateDir = %q, want %q (env override)", got, want)
	}
}
