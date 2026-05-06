package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

func newStatusNotifyCommand(store notifyStore) *statusCommand {
	cmd := testStatusCommand("/tmp")
	cmd.notifyStoreFn = func() (notifyStore, error) { return store, nil }
	return cmd
}

func TestStatusNotifyEmptyQueueIsEmptyOutput(t *testing.T) {
	t.Parallel()

	cmd := newStatusNotifyCommand(&stubNotifyStore{})
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
}

// External-source info entry renders an ` INFO ` severity badge (no agent
// prefix is available) and bright body text followed by dim metadata.
func TestStatusNotifyExternalEntryRendersInfoBadge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:        "a",
			Text:      "hello world",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceExternal,
			Session:   "main",
			Window:    "1",
			Pane:      "0",
			CreatedAt: now.Add(-2 * time.Minute),
		},
	}}
	cmd := newStatusNotifyCommand(store)
	cmd.now = func() time.Time { return now }
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "#[bg=brightcyan,fg=black,bold] INFO #[default]") {
		t.Fatalf("stdout = %q, want INFO badge prefix", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Fatalf("stdout = %q, want body 'hello world'", got)
	}
	if !strings.Contains(got, "main:1.0") {
		t.Fatalf("stdout = %q, want compact target 'main:1.0'", got)
	}
	if !strings.Contains(got, "2m") {
		t.Fatalf("stdout = %q, want age '2m'", got)
	}
	// Body text appears after the badge reset and BEFORE any dim escape —
	// so the literal " hello world  " region must contain no `#[fg=colour`.
	bodyStart := strings.Index(got, "#[default]") + len("#[default]")
	dimStart := strings.Index(got, "#[fg=colour245]")
	if bodyStart <= 0 || dimStart <= bodyStart {
		t.Fatalf("stdout = %q, expected default-styled body before dim metadata", got)
	}
	body := got[bodyStart:dimStart]
	if strings.Contains(body, "#[") {
		t.Fatalf("body region %q must be unstyled (default fg)", body)
	}
	if !strings.HasSuffix(got, "#[default]") {
		t.Fatalf("stdout = %q, want trailing '#[default]'", got)
	}
}

// AI-source entry whose text begins with `claude:` strips the prefix and
// renders the agent name inside a brightcyan badge.
func TestStatusNotifyAIEntryRendersAgentBadge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:        "a",
			Text:      "claude: reply ready · review",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "s",
			Window:    "1",
			Pane:      "0",
			CreatedAt: now.Add(-2 * time.Minute),
		},
	}}
	cmd := newStatusNotifyCommand(store)
	cmd.now = func() time.Time { return now }
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify", "--max-width", "80"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "#[bg=brightcyan,fg=black,bold] claude #[default]") {
		t.Fatalf("stdout = %q, want claude badge prefix", got)
	}
	if strings.Contains(got, "claude:") {
		t.Fatalf("stdout = %q, agent prefix should have been stripped from text", got)
	}
	if !strings.Contains(got, "reply ready · review") {
		t.Fatalf("stdout = %q, want body 'reply ready · review'", got)
	}
	if !strings.Contains(got, "s:1.0") {
		t.Fatalf("stdout = %q, want compact target", got)
	}
	if !strings.Contains(got, "#[fg=colour245]") {
		t.Fatalf("stdout = %q, want dim metadata", got)
	}
}

func TestStatusNotifyWarnEntryRendersYellowBadge(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "a", Text: "warn-me", Severity: notify.SeverityWarn, Source: notify.SourceExternal, Session: "ops"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "#[bg=yellow,fg=black,bold] WARN #[default]") {
		t.Fatalf("stdout = %q, want yellow WARN badge prefix", got)
	}
}

func TestStatusNotifyCriticalEntryRendersRedBadge(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "a", Text: "boom", Severity: notify.SeverityCritical, Source: notify.SourceExternal, Session: "prod"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "#[bg=red,fg=white,bold] CRIT #[default]") {
		t.Fatalf("stdout = %q, want red CRIT badge prefix", got)
	}
}

// Defensive: an entry with an unknown severity falls back to the INFO
// badge palette so we never emit a stripped escape directive.
func TestStatusNotifyUnknownSeverityFallsBackToInfoBadge(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "a", Text: "huh", Severity: "mystery", Source: notify.SourceExternal, Session: "s"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "#[bg=brightcyan,fg=black,bold] INFO #[default]") {
		t.Fatalf("stdout = %q, want INFO fallback badge", got)
	}
}

func TestStatusNotifyMultipleEntriesAppendsPlusCount(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "a", Text: "newest", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main"},
		{ID: "b", Text: "older1", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main"},
		{ID: "c", Text: "older2", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "+2") {
		t.Fatalf("stdout = %q, want '+2'", got)
	}
	if !strings.Contains(got, "newest") {
		t.Fatalf("stdout = %q, want headline 'newest'", got)
	}
	if !strings.Contains(got, "#[fg=colour244]+2#[default]") {
		t.Fatalf("stdout = %q, want dim count escape", got)
	}
}

func TestStatusNotifyStableTimestampOrdering(t *testing.T) {
	t.Parallel()

	now := time.Now()
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "newer", Text: "newer", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main", CreatedAt: now},
		{ID: "older", Text: "older", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main", CreatedAt: now.Add(-time.Minute)},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "newer") {
		t.Fatalf("stdout = %q, want newest entry first ('newer')", got)
	}
}

func TestStatusNotifyMissingStoreFactoryReturnsEmpty(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand("/tmp")
	cmd.notifyStoreFn = nil
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// fixtureAIEntry returns a canonical ai-source claude entry (2m old) used
// by the width-tier table. Target is the spec example `s:1.0` so the long
// form fits inside the 80-rune budget.
func fixtureAIEntry(now time.Time) []notify.Notification {
	return []notify.Notification{
		{
			ID:        "a",
			Text:      "claude: reply ready · review",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "s",
			Window:    "1",
			Pane:      "0",
			CreatedAt: now.Add(-2 * time.Minute),
		},
		{
			ID:        "b",
			Text:      "claude: another",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "s",
			Window:    "1",
			Pane:      "0",
			CreatedAt: now.Add(-3 * time.Minute),
		},
	}
}

func TestStatusNotifyWidthTier1Long(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 80, now)
	if visualLen(out) > 80 {
		t.Fatalf("tier1 visualLen=%d > 80: %q", visualLen(out), out)
	}
	for _, want := range []string{
		"#[bg=brightcyan,fg=black,bold] claude #[default]",
		"reply ready · review",
		"s:1.0",
		"2m",
		"+1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tier1 missing %q in %q", want, out)
		}
	}
}

func TestStatusNotifyWidthTier2DropsAge(t *testing.T) {
	t.Parallel()

	// With the canonical fixture the long form is 48 cells, so we pick a
	// budget tight enough to force the tier-2 fallback (no age) but loose
	// enough to keep the target.
	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 45, now)
	if visualLen(out) > 45 {
		t.Fatalf("tier2 visualLen=%d > 45: %q", visualLen(out), out)
	}
	for _, want := range []string{
		"#[bg=brightcyan,fg=black,bold] claude #[default]",
		"reply ready · review",
		"s:1.0",
		"+1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tier2 missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "2m") {
		t.Fatalf("tier2 must drop age: %q", out)
	}
}

func TestStatusNotifyWidthTier3DropsTarget(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 40, now)
	if visualLen(out) > 40 {
		t.Fatalf("tier3 visualLen=%d > 40: %q", visualLen(out), out)
	}
	for _, want := range []string{
		"#[bg=brightcyan,fg=black,bold] claude #[default]",
		"reply ready · review",
		"+1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tier3 missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "s:1.0") {
		t.Fatalf("tier3 must drop target: %q", out)
	}
	if strings.Contains(out, "2m") {
		t.Fatalf("tier3 must drop age: %q", out)
	}
}

func TestStatusNotifyWidthTier4TruncatesText(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 24, now)
	if visualLen(out) > 24 {
		t.Fatalf("tier4 visualLen=%d > 24: %q", visualLen(out), out)
	}
	if !strings.Contains(out, "#[bg=brightcyan,fg=black,bold] claude #[default]") {
		t.Fatalf("tier4 should still show badge: %q", out)
	}
	if !strings.Contains(out, "+1") {
		t.Fatalf("tier4 should still show count: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("tier4 should truncate text with ellipsis: %q", out)
	}
}

// Tier 5 drops the bg-filled badge. The standalone severity-tinted `●`
// icon (no bg fill) preserves a minimal severity hint for very narrow
// statuslines.
func TestStatusNotifyWidthTier5DropsBadge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 14, now)
	if visualLen(out) > 14 {
		t.Fatalf("tier5 visualLen=%d > 14: %q", visualLen(out), out)
	}
	if strings.Contains(out, "bg=brightcyan") {
		t.Fatalf("tier5 must drop the bg-filled badge: %q", out)
	}
	if strings.Contains(out, "claude") {
		t.Fatalf("tier5 must drop the agent label: %q", out)
	}
	if !strings.Contains(out, "#[fg=brightcyan]●#[default]") {
		t.Fatalf("tier5 should still show the icon-only severity hint: %q", out)
	}
	if !strings.Contains(out, "+1") {
		t.Fatalf("tier5 should still show count: %q", out)
	}
}

func TestStatusNotifyWidthTier6HardTruncate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 8, now)
	if visualLen(out) > 8 {
		t.Fatalf("tier6 visualLen=%d > 8: %q", visualLen(out), out)
	}
	if !strings.HasSuffix(out, "#[default]") {
		t.Fatalf("tier6 must end with #[default]: %q", out)
	}
}

func TestStatusNotifyAgentPrefixGracefulFallback(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		text      string
		wantAgent string // "" means INFO label badge
	}{
		{name: "no colon", text: "reply ready", wantAgent: ""},
		{name: "unknown agent", text: "gpt: reply ready", wantAgent: ""},
		{name: "empty body after colon", text: "claude:", wantAgent: ""},
		{name: "leading colon", text: ":hello", wantAgent: ""},
		{name: "codex prefix", text: "codex: thought", wantAgent: "codex"},
		{name: "claude prefix uppercase", text: "Claude: ready", wantAgent: "claude"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := formatStatusNotify([]notify.Notification{{
				ID: "a", Text: tc.text, Severity: notify.SeverityInfo,
				Source: notify.SourceAI, Session: "s", CreatedAt: now,
			}}, 0, now)
			wantBadge := "#[bg=brightcyan,fg=black,bold] INFO #[default]"
			if tc.wantAgent != "" {
				wantBadge = "#[bg=brightcyan,fg=black,bold] " + tc.wantAgent + " #[default]"
			}
			if !strings.HasPrefix(out, wantBadge) {
				t.Fatalf("expected badge %q at start of %q", wantBadge, out)
			}
		})
	}
}

func TestFormatRelativeAgeBuckets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		dur  time.Duration
		want string
	}{
		{0, "just now"},
		{30 * time.Second, "just now"},
		{59 * time.Second, "just now"},
		{60 * time.Second, "1m"},
		{2 * time.Minute, "2m"},
		{45 * time.Minute, "45m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{3 * time.Hour, "3h"},
		{23*time.Hour + 59*time.Minute, "23h"},
		{24 * time.Hour, "1d"},
		{73 * time.Hour, "3d"},
		{-time.Minute, "just now"},
	}
	for _, tc := range cases {
		if got := formatRelativeAge(tc.dur); got != tc.want {
			t.Fatalf("formatRelativeAge(%v) = %q, want %q", tc.dur, got, tc.want)
		}
	}
}

func TestStatusNotifyCompactTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		n    notify.Notification
		want string
	}{
		{notify.Notification{Session: "s"}, "s"},
		{notify.Notification{Session: "s", Window: "1"}, "s:1"},
		{notify.Notification{Session: "s", Window: "1", Pane: "0"}, "s:1.0"},
		{notify.Notification{Session: ""}, ""},
		{notify.Notification{Session: "  s  ", Window: " 1 ", Pane: " 0 "}, "s:1.0"},
	}
	for _, tc := range cases {
		if got := compactTarget(tc.n); got != tc.want {
			t.Fatalf("compactTarget(%+v) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
