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
	if !strings.HasPrefix(got, notifyLineOpen+renderNotifyProjectBadge("main")) {
		t.Fatalf("stdout = %q, want project badge prefix", got)
	}
	if !strings.Contains(got, renderNotifyBadge("INFO", notify.SeverityInfo)) {
		t.Fatalf("stdout = %q, want INFO badge", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Fatalf("stdout = %q, want body 'hello world'", got)
	}
	if strings.Contains(got, "w1.p0") {
		t.Fatalf("stdout = %q, must not include pane target", got)
	}
	if !strings.Contains(got, "2m") {
		t.Fatalf("stdout = %q, want age '2m'", got)
	}
	// Body text appears after the badge reset and before any dim metadata.
	bodyStart := strings.Index(got, " hello world")
	dimStart := strings.Index(got, notifyLineDimOpen)
	if bodyStart < 0 || dimStart <= bodyStart {
		t.Fatalf("stdout = %q, expected body before dim metadata", got)
	}
	if !strings.HasSuffix(got, "#[default]") {
		t.Fatalf("stdout = %q, want trailing '#[default]'", got)
	}
}

// AI-source entry whose text begins with `claude:` strips the prefix and
// renders project, reply-needed state, and agent badges.
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
	for _, want := range []string{
		notifyLineOpen + renderNotifyProjectBadge("s"),
		renderNotifyBadge("NEED", notify.SeverityInfo),
		renderNotifyAgentBadge("claude"),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout = %q, want badge %q", got, want)
		}
	}
	if strings.Contains(got, "claude:") {
		t.Fatalf("stdout = %q, agent prefix should have been stripped from text", got)
	}
	if !strings.Contains(got, "reply ready · review") {
		t.Fatalf("stdout = %q, want body 'reply ready · review'", got)
	}
	if strings.Contains(got, "w1.p0") {
		t.Fatalf("stdout = %q, must not include pane target", got)
	}
	if !strings.Contains(got, notifyLineDimOpen) {
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
	if !strings.HasPrefix(got, notifyLineOpen+renderNotifyProjectBadge("ops")) || !strings.Contains(got, renderNotifyBadge("WARN", notify.SeverityWarn)) {
		t.Fatalf("stdout = %q, want project + yellow WARN badges", got)
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
	if !strings.HasPrefix(got, notifyLineOpen+renderNotifyProjectBadge("prod")) || !strings.Contains(got, renderNotifyBadge("CRIT", notify.SeverityCritical)) {
		t.Fatalf("stdout = %q, want project + red CRIT badges", got)
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
	if !strings.HasPrefix(got, notifyLineOpen+renderNotifyProjectBadge("s")) || !strings.Contains(got, renderNotifyBadge("INFO", notify.SeverityInfo)) {
		t.Fatalf("stdout = %q, want project + INFO fallback badges", got)
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
	if !strings.Contains(got, notifyLineCountOpen+"+2"+notifyLineOpen) {
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
// by the width-tier table.
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

func TestStatusNotifyLongBodyClipsBeforeDroppingAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{
			ID:        "a",
			Text:      "claude: reply ready with a very long body that should clip before metadata disappears",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "project",
			CreatedAt: now,
		},
		{ID: "b", Text: "older", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "project"},
		{ID: "c", Text: "oldest", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "project"},
	}
	out := formatStatusNotify(entries, 80, now)
	if visualLen(out) > 80 {
		t.Fatalf("visualLen=%d > 80: %q", visualLen(out), out)
	}
	for _, want := range []string{
		notifyLineOpen + renderNotifyProjectBadge("project"),
		renderNotifyBadge("NEED", notify.SeverityInfo),
		renderNotifyAgentBadge("claude"),
		"just now",
		"+2",
		"…",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
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
		notifyLineOpen + renderNotifyProjectBadge("s"),
		renderNotifyBadge("NEED", notify.SeverityInfo),
		renderNotifyAgentBadge("claude"),
		"reply ready",
		"2m",
		"+1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tier1 missing %q in %q", want, out)
		}
	}
}

func TestStatusNotifyWidthTier2ClipsTextBeforeAge(t *testing.T) {
	t.Parallel()

	// The project/state/agent badge stack makes this budget tight enough to
	// clip body text while retaining contextual badges and age.
	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 45, now)
	if visualLen(out) > 45 {
		t.Fatalf("tier2 visualLen=%d > 45: %q", visualLen(out), out)
	}
	for _, want := range []string{
		notifyLineOpen + renderNotifyProjectBadge("s"),
		renderNotifyBadge("NEED", notify.SeverityInfo),
		renderNotifyAgentBadge("claude"),
		"reply ready",
		"2m",
		"+1",
		"…",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tier2 missing %q in %q", want, out)
		}
	}
}

func TestStatusNotifyWidthTier3DropsAgeAfterClippingText(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 28, now)
	if visualLen(out) > 28 {
		t.Fatalf("tier3 visualLen=%d > 28: %q", visualLen(out), out)
	}
	for _, want := range []string{
		notifyLineOpen + renderNotifyProjectBadge("s"),
		renderNotifyBadge("NEED", notify.SeverityInfo),
		renderNotifyAgentBadge("claude"),
		"+1",
		"…",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tier3 missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "2m") {
		t.Fatalf("tier3 must drop age: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("tier3 should truncate text with ellipsis: %q", out)
	}
}

func TestStatusNotifyWidthTier4TruncatesText(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 24, now)
	if visualLen(out) > 24 {
		t.Fatalf("tier4 visualLen=%d > 24: %q", visualLen(out), out)
	}
	if strings.Contains(out, " NEED ") || strings.Contains(out, " claude ") {
		t.Fatalf("tier4 should drop badges before icon fallback at this width: %q", out)
	}
	if !strings.Contains(out, "+1") {
		t.Fatalf("tier4 should still show count: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("tier4 should truncate text with ellipsis: %q", out)
	}
}

// Tier 5 drops the block badges. The standalone severity-tinted `●`
// icon preserves a minimal severity hint for very narrow statuslines.
func TestStatusNotifyWidthTier5DropsBadge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify(fixtureAIEntry(now), 14, now)
	if visualLen(out) > 14 {
		t.Fatalf("tier5 visualLen=%d > 14: %q", visualLen(out), out)
	}
	if strings.Contains(out, " NEED ") || strings.Contains(out, " claude ") {
		t.Fatalf("tier5 must drop the block badges: %q", out)
	}
	if strings.Contains(out, "claude") {
		t.Fatalf("tier5 must drop the agent label: %q", out)
	}
	if !strings.Contains(out, notifySeverityInfo+notifyIcon+notifyLineOpen) {
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
		wantState string
	}{
		{name: "no colon", text: "reply ready", wantAgent: "", wantState: "NEED"},
		{name: "unknown agent", text: "gpt: reply ready", wantAgent: "", wantState: "NEED"},
		{name: "empty body after colon", text: "claude:", wantAgent: "", wantState: "INFO"},
		{name: "leading colon", text: ":hello", wantAgent: "", wantState: "INFO"},
		{name: "codex prefix", text: "codex: thought", wantAgent: "codex", wantState: "INFO"},
		{name: "claude prefix uppercase", text: "Claude: ready", wantAgent: "claude", wantState: "INFO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := formatStatusNotify([]notify.Notification{{
				ID: "a", Text: tc.text, Severity: notify.SeverityInfo,
				Source: notify.SourceAI, Session: "s", CreatedAt: now,
			}}, 0, now)
			if !strings.HasPrefix(out, notifyLineOpen+renderNotifyProjectBadge("s")) {
				t.Fatalf("expected project badge at start of %q", out)
			}
			wantStateBadge := renderNotifyBadge(tc.wantState, notify.SeverityInfo)
			if !strings.Contains(out, wantStateBadge) {
				t.Fatalf("expected state badge %q in %q", wantStateBadge, out)
			}
			wantAgentBadge := renderNotifyAgentBadge(tc.wantAgent)
			if tc.wantAgent != "" && !strings.Contains(out, wantAgentBadge) {
				t.Fatalf("expected agent badge %q in %q", wantAgentBadge, out)
			}
			if tc.wantAgent == "" && (strings.Contains(out, notifyAgentOpen) || strings.Contains(out, notifyAgentClaude) || strings.Contains(out, notifyAgentCodex)) {
				t.Fatalf("did not expect agent badge in %q", out)
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
