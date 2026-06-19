package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
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

// AI-source entry renders project and topic badges from metadata without
// repeating state or agent badges in the statusbar body.
func TestStatusNotifyAIEntryRendersTopicBadge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:        "a",
			Text:      "review needed before shipping",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Metadata:  map[string]string{"agent": "claude", "category": "response_complete", "state": "need", "topic": "shipping"},
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
		renderNotifyTopicBadge(notify.Notification{Source: notify.SourceAI, Metadata: map[string]string{"topic": "shipping"}}),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout = %q, want badge %q", got, want)
		}
	}
	if strings.Contains(got, "claude:") || strings.Contains(got, "reply ready") {
		t.Fatalf("stdout = %q, body should not include agent/category prefix", got)
	}
	if !strings.Contains(got, "review") {
		t.Fatalf("stdout = %q, want body 'review'", got)
	}
	if strings.Contains(got, " NEED ") || strings.Contains(got, " claude ") {
		t.Fatalf("stdout = %q, must not include state/agent badges for AI statusbar", got)
	}
	if strings.Contains(got, "w1.p0") {
		t.Fatalf("stdout = %q, must not include pane target", got)
	}
	if !strings.Contains(got, notifyLineDimOpen) {
		t.Fatalf("stdout = %q, want dim metadata", got)
	}
}

func TestStatusNotifyPaletteSeparatesAttentionAndAI(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify([]notify.Notification{{
		ID:        "a",
		Text:      "review needed before shipping",
		Severity:  notify.SeverityInfo,
		Source:    notify.SourceAI,
		Metadata:  map[string]string{"agent": "codex", "category": "response_complete", "state": "need", "topic": "review"},
		Session:   "s",
		CreatedAt: now,
	}}, 80, now)

	for _, want := range []string{
		"#[bg=" + tmuxAccentAttentionBg + ",fg=" + tmuxStateProgressFg + "]",
		"#[bg=" + tmuxAccentAIBg + ",fg=colour16,bold] review ",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status notify palette output = %q, want %q", out, want)
		}
	}
	for _, notWant := range []string{"brightcyan", "bg=colour45", "bg=colour29", "bg=colour51"} {
		if strings.Contains(out, notWant) {
			t.Fatalf("status notify palette output = %q, must not use action/legacy color %q", out, notWant)
		}
	}
	if !strings.Contains(out, "#[bg="+tmuxAccentAttentionBg+",fg="+theme.TmuxStateAheadFg+"]") {
		t.Fatalf("status notify palette output = %q, want blue age foreground", out)
	}
}

func TestStatusNotifyWarnEntryRendersAmberBadge(t *testing.T) {
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
		t.Fatalf("stdout = %q, want project + amber WARN badges", got)
	}
}

func TestCriticalNotifySeverityDoesNotDriveAIStatusBadgePalette(t *testing.T) {
	t.Parallel()

	entry := notify.Notification{
		ID:        "ai:main:%7",
		Text:      "codex: PermissionRequest · Bash",
		Severity:  notify.SeverityCritical,
		Source:    notify.SourceAI,
		Session:   "main",
		Metadata:  map[string]string{"agent": "codex", "category": aiBadgeKindApprovalRequired, "state": "need"},
		CreatedAt: time.Date(2026, time.May, 22, 12, 0, 0, 0, time.UTC),
	}

	if got := notifyBadgeOpen(entry.Severity); got != notifyBadgeCritOpen {
		t.Fatalf("notify severity palette = %q, want critical queue palette %q", got, notifyBadgeCritOpen)
	}
	if got := tmuxAIBadgeKindFg(aiBadgeKindApprovalRequired); got != theme.TmuxAIBadgeActionRequiredFg {
		t.Fatalf("approval-required status badge fg = %q, want action-required %q", got, theme.TmuxAIBadgeActionRequiredFg)
	}
	if got := tmuxAIBadgeKindFg(aiBadgeKindApprovalRequired); got == tmuxStateCriticalFg {
		t.Fatalf("approval-required status badge fg = %q, must not follow critical notify severity", got)
	}
	if got := tmuxAIBadgeKindFg(aiBadgeKindResponseComplete); got != theme.TmuxAIBadgeSuccessFg {
		t.Fatalf("response-complete status badge fg = %q, want success %q", got, theme.TmuxAIBadgeSuccessFg)
	}
	if got := tmuxAIBadgeKindFg(aiBadgeKindInProgress); got != theme.TmuxAIBadgeProgressFg {
		t.Fatalf("in-progress status badge fg = %q, want progress %q", got, theme.TmuxAIBadgeProgressFg)
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
			Text:      "review needed before shipping",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Metadata:  map[string]string{"agent": "claude", "category": "response_complete", "state": "need", "topic": "shipping"},
			Session:   "s",
			Window:    "1",
			Pane:      "0",
			CreatedAt: now.Add(-2 * time.Minute),
		},
		{
			ID:        "b",
			Text:      "another",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Metadata:  map[string]string{"agent": "claude", "category": "response_complete", "state": "need", "topic": "shipping"},
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
			Text:      "reply ready with a very long body that should clip before metadata disappears",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Metadata:  map[string]string{"agent": "claude", "category": "response_complete", "state": "need", "topic": "long"},
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
		renderNotifyTopicBadge(notify.Notification{Source: notify.SourceAI, Metadata: map[string]string{"topic": "long"}}),
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
	out := formatStatusNotify(fixtureAIEntry(now), 96, now)
	if visualLen(out) > 96 {
		t.Fatalf("tier1 visualLen=%d > 96: %q", visualLen(out), out)
	}
	for _, want := range []string{
		notifyLineOpen + renderNotifyProjectBadge("s"),
		renderNotifyTopicBadge(notify.Notification{Source: notify.SourceAI, Metadata: map[string]string{"topic": "shipping"}}),
		"Claude · Response complete · review needed before shipping",
		"2m ago",
		"+1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tier1 missing %q in %q", want, out)
		}
	}
}

func TestStatusNotifyLocaleFormatsAgeAITextAndCount(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotifyWithLiveLocale([]notify.Notification{
		{
			ID:        "a",
			Text:      "Ready",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Metadata:  map[string]string{"agent": "codex", "category": "response_complete", "topic": "shipping"},
			Session:   "s",
			CreatedAt: now.Add(-36 * time.Second),
		},
		{
			ID:        "b",
			Text:      "older",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "s",
			CreatedAt: now.Add(-2 * time.Minute),
		},
	}, 80, now, nil, nil, i18n.Locale("ko-KR"))

	for _, want := range []string{
		renderNotifyProjectBadge("s"),
		renderNotifyTopicBadge(notify.Notification{Source: notify.SourceAI, Metadata: map[string]string{"topic": "shipping"}}),
		"Codex · 응답 완료",
		"36초 전",
		"+1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("localized status notify missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "Ready") {
		t.Fatalf("localized status notify = %q, response-complete Ready detail should be suppressed", out)
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
		renderNotifyTopicBadge(notify.Notification{Source: notify.SourceAI, Metadata: map[string]string{"topic": "shipping"}}),
		"Claude",
		"2m ago",
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
	out := formatStatusNotify(fixtureAIEntry(now), 24, now)
	if visualLen(out) > 24 {
		t.Fatalf("tier3 visualLen=%d > 24: %q", visualLen(out), out)
	}
	for _, want := range []string{
		notifyLineOpen + renderNotifyProjectBadge("s"),
		renderNotifyTopicBadge(notify.Notification{Source: notify.SourceAI, Metadata: map[string]string{"topic": "shipping"}}),
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

// Tier 5 drops the block badges and keeps only clipped text plus count. It
// intentionally avoids the old standalone severity dot in very narrow cells.
func TestStatusNotifyWidthTier5DropsBadgeAndDot(t *testing.T) {
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
	if strings.Contains(out, notifyIcon) || strings.Contains(out, notifySeverityInfo+notifyIcon+notifyLineOpen) {
		t.Fatalf("tier5 must not show the standalone severity dot: %q", out)
	}
	if !strings.Contains(out, "+1") {
		t.Fatalf("tier5 should still show count: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("tier5 should clip text with ellipsis: %q", out)
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

func TestStatusNotifyCriticalVeryNarrowFallbackHasNoStandaloneDot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	out := formatStatusNotify([]notify.Notification{{
		ID:        "a",
		Text:      "critical failure needs attention",
		Severity:  notify.SeverityCritical,
		Source:    notify.SourceExternal,
		Session:   "prod",
		CreatedAt: now,
	}}, 6, now)
	if visualLen(out) > 6 {
		t.Fatalf("critical narrow visualLen=%d > 6: %q", visualLen(out), out)
	}
	if strings.Contains(out, notifyIcon) {
		t.Fatalf("critical narrow fallback must not render standalone dot: %q", out)
	}
	if strings.Contains(out, notifySeverityCrit) {
		t.Fatalf("critical narrow fallback must not render critical severity escape: %q", out)
	}
	if strings.Contains(out, renderNotifyBadge("CRIT", notify.SeverityCritical)) {
		t.Fatalf("critical narrow fallback should drop the CRIT badge: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("critical narrow fallback should clip text: %q", out)
	}
	if !strings.HasSuffix(out, "#[default]") {
		t.Fatalf("critical narrow fallback must end with #[default]: %q", out)
	}
}

func TestStatusNotifyAgentPrefixGracefulFallback(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		text string
	}{
		{name: "no colon", text: "reply ready"},
		{name: "unknown agent", text: "gpt: reply ready"},
		{name: "empty body after colon", text: "claude:"},
		{name: "leading colon", text: ":hello"},
		{name: "codex prefix", text: "codex: thought"},
		{name: "claude prefix uppercase", text: "Claude: ready"},
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
			if strings.Contains(out, " NEED ") || strings.Contains(out, " INFO ") || strings.Contains(out, " codex ") || strings.Contains(out, " claude ") {
				t.Fatalf("did not expect state/agent badge in AI statusbar output %q", out)
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
		{30 * time.Second, "30s ago"},
		{59 * time.Second, "59s ago"},
		{60 * time.Second, "1m ago"},
		{2 * time.Minute, "2m ago"},
		{45 * time.Minute, "45m ago"},
		{59*time.Minute + 59*time.Second, "59m ago"},
		{time.Hour, "1h ago"},
		{3 * time.Hour, "3h ago"},
		{23*time.Hour + 59*time.Minute, "23h ago"},
		{24 * time.Hour, "1d ago"},
		{73 * time.Hour, "3d ago"},
		{-time.Minute, "just now"},
	}
	for _, tc := range cases {
		if got := formatRelativeAge(tc.dur); got != tc.want {
			t.Fatalf("formatRelativeAge(%v) = %q, want %q", tc.dur, got, tc.want)
		}
	}
}
