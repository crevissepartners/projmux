package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

// TestClassifyNotifyRowStateMatchesLiveReport pins the contract that the new
// display classifier and the long-standing `--live` row state machine agree
// on STALE/GONE/LIVE for every queue entry. Drift here means a sidebar/status
// badge that disagrees with `projmux notify list --live`.
func TestClassifyNotifyRowStateMatchesLiveReport(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{
			ID: "ai:main:%2", Text: "codex: reply ready",
			Severity: notify.SeverityInfo, Source: notify.SourceAI,
			Session: "main", Window: "@1", Pane: "%2",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		},
		{
			ID: "ai:gone:%9", Text: "claude: reply ready",
			Severity: notify.SeverityInfo, Source: notify.SourceAI,
			Session: "gone", Pane: "%9",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		},
		{
			ID: "ext:1", Text: "deploy ok",
			Severity: notify.SeverityInfo, Source: notify.SourceExternal,
			Session:   "ops",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		},
		{
			ID: "unroutable", Text: "no target",
			Severity: notify.SeverityInfo, Source: notify.SourceExternal,
			// Session intentionally empty: this is the GONE condition.
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		},
	}
	store := &stubNotifyStore{listEntries: entries}
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			if containsArg(args, "list-panes") {
				return notifyLivePaneRows(
					[]string{"main", "@1", "%2", "0", "codex", "reply", "waiting", "codex", "topic", "/tmp/tmux/default"},
				), nil
			}
			return nil, nil
		},
	}
	cmd := newCmd(store)
	cmd.runner = runner

	report, err := cmd.buildNotifyLiveReport(entries)
	if err != nil {
		t.Fatalf("buildNotifyLiveReport error = %v", err)
	}

	rowStates := make(map[string]string, len(report.Rows))
	for _, row := range report.Rows {
		rowStates[row.ID] = row.State
	}

	liveByID := notifyLiveShouldQueueByID(report.Live)
	cases := []struct {
		id           string
		wantDisplay  notifyRowDisplayState
		wantRowState string
	}{
		// AI entry matched to a live ShouldQueue pane → live both ways.
		{"ai:main:%2", notifyDisplayLive, "live-ai-reply-queued"},
		// AI entry without a live match → STALE both ways.
		{"ai:gone:%9", notifyDisplayStale, "queue-stale"},
		// External entry with a routable target stays live.
		{"ext:1", notifyDisplayLive, "queue-only"},
		// Empty session → GONE in both surfaces.
		{"unroutable", notifyDisplayGone, "queue-gone"},
	}
	for _, tc := range cases {
		entry := findEntryByIDOrFail(t, entries, tc.id)
		gotDisplay := classifyNotifyRowState(entry, liveByID)
		if gotDisplay != tc.wantDisplay {
			t.Fatalf("classify(%s) = %v, want %v", tc.id, gotDisplay, tc.wantDisplay)
		}
		gotRow, ok := rowStates[tc.id]
		if !ok {
			t.Fatalf("row state for %s missing from report; rows = %#v", tc.id, report.Rows)
		}
		if gotRow != tc.wantRowState {
			t.Fatalf("--live row state for %s = %q, want %q", tc.id, gotRow, tc.wantRowState)
		}
	}
}

// TestClassifyNotifyRowStateFallsBackToLiveWhenLiveUnavailable pins the
// best-effort contract: when live data could not be read (nil map), every
// routable entry classifies as LIVE so the surface does not falsely dim
// every row during a tmux outage.
func TestClassifyNotifyRowStateFallsBackToLiveWhenLiveUnavailable(t *testing.T) {
	t.Parallel()

	entry := notify.Notification{
		ID: "ai:main:%2", Session: "main", Pane: "%2",
		Source: notify.SourceAI, Severity: notify.SeverityInfo,
		Text: "codex: reply ready",
	}
	if got := classifyNotifyRowState(entry, nil); got != notifyDisplayLive {
		t.Fatalf("classify with nil live map = %v, want live", got)
	}
	// Empty session still classifies as GONE even without live data,
	// because that decision needs no tmux input.
	gone := notify.Notification{ID: "x", Text: "no target"}
	if got := classifyNotifyRowState(gone, nil); got != notifyDisplayGone {
		t.Fatalf("classify gone with nil live map = %v, want gone", got)
	}
}

// TestNotifySidebarStateBadgeStaleAndGonePalette pins the sidebar badge
// renderer: STALE/GONE land in the muted grey palette so users can tell them
// apart from the active info/warn/crit badges at a glance.
func TestNotifySidebarStateBadgeStaleAndGonePalette(t *testing.T) {
	t.Parallel()

	stale := notifySidebarStateBadge("STALE")
	if !strings.HasPrefix(stale, notifySidebarStale) || !strings.Contains(stale, " STALE ") {
		t.Fatalf("STALE badge = %q, want stale palette + label", stale)
	}
	gone := notifySidebarStateBadge("GONE")
	if !strings.HasPrefix(gone, notifySidebarGone) || !strings.Contains(gone, " GONE ") {
		t.Fatalf("GONE badge = %q, want gone palette + label", gone)
	}
	// Live labels keep their original palette — no accidental regression.
	if got := notifySidebarStateBadge("INFO"); strings.HasPrefix(got, notifySidebarStale) || strings.HasPrefix(got, notifySidebarGone) {
		t.Fatalf("INFO badge = %q, must not leak stale/gone palette", got)
	}
}

func TestNotifySidebarPaletteSeparatesAttentionAndAI(t *testing.T) {
	t.Parallel()

	need := notifySidebarStateBadge("NEED")
	if !strings.HasPrefix(need, "\x1b[1;38;5;16;48;5;220m") {
		t.Fatalf("NEED badge = %q, want amber pending palette", need)
	}
	warn := notifySidebarStateBadge("WARN")
	if !strings.HasPrefix(warn, "\x1b[1;38;5;16;48;5;214m") {
		t.Fatalf("WARN badge = %q, want amber warning palette", warn)
	}
	agent := notifySidebarAgentBadge("codex")
	if !strings.HasPrefix(agent, "\x1b[1;38;5;16;48;5;37m") {
		t.Fatalf("agent badge = %q, want AI palette", agent)
	}
	for _, label := range []string{need, warn, agent} {
		for _, notWant := range []string{"48;5;29", "48;5;45", "48;5;51"} {
			if strings.Contains(label, notWant) {
				t.Fatalf("sidebar label = %q, must not use action/legacy color %q", label, notWant)
			}
		}
	}
}

func TestNotifySidebarAgeUsesBluePalette(t *testing.T) {
	t.Parallel()

	age := notifySidebarAge("11s")
	if !strings.HasPrefix(age, "\x1b[38;5;153m") || !strings.Contains(age, " age 11s ") {
		t.Fatalf("age = %q, want blue age palette", age)
	}
}

// TestNotifySidebarLabelDimsStaleAndGoneText pins that stale/gone rows
// render their body text inside the dim escape so they visually recede.
func TestNotifySidebarLabelDimsStaleAndGoneText(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entry := notify.Notification{
		ID:        "ai:gone:%2",
		Text:      "codex: reply ready",
		Severity:  notify.SeverityInfo,
		Source:    notify.SourceAI,
		Session:   "main",
		Window:    "1",
		Pane:      "%2",
		CreatedAt: now.Add(-time.Minute),
	}

	live := notifySidebarLabelFor(entry, now, notifyDisplayLive)
	stale := notifySidebarLabelFor(entry, now, notifyDisplayStale)
	gone := notifySidebarLabelFor(entry, now, notifyDisplayGone)

	if !strings.Contains(live, " NEED ") {
		t.Fatalf("live label = %q, want NEED badge", live)
	}
	if strings.Contains(live, notifySidebarDimOpen+"reply ready") {
		t.Fatalf("live label = %q, must not dim its text", live)
	}
	if !strings.Contains(stale, " STALE ") || !strings.Contains(stale, notifySidebarDimOpen) {
		t.Fatalf("stale label = %q, want STALE badge + dim body", stale)
	}
	if !strings.Contains(gone, " GONE ") || !strings.Contains(gone, notifySidebarDimOpen) {
		t.Fatalf("gone label = %q, want GONE badge + dim body", gone)
	}
}

// TestNotifySidebarEntriesWithLivePassesClassification pins that the
// sidebar's entries builder produces labels whose badge matches the live
// classification it received.
func TestNotifySidebarEntriesWithLivePassesClassification(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{ID: "ai:live:%1", Text: "claude: reply ready", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "live", Pane: "%1", CreatedAt: now},
		{ID: "ai:stale:%2", Text: "codex: reply ready", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "stale", Pane: "%2", CreatedAt: now},
	}
	liveByID := map[string]notifyLivePane{
		"ai:live:%1": {ID: "ai:live:%1", Session: "live", Pane: "%1", ShouldQueue: true},
	}

	out := notifySidebarEntriesWithLive(entries, now, liveByID)
	if len(out) != 2 {
		t.Fatalf("got %d entries, want 2", len(out))
	}
	if !strings.Contains(out[0].Label, " NEED ") {
		t.Fatalf("live entry label = %q, want NEED badge", out[0].Label)
	}
	if !strings.Contains(out[1].Label, " STALE ") {
		t.Fatalf("stale entry label = %q, want STALE badge", out[1].Label)
	}
}

// TestFormatStatusNotifyWithLiveSubstitutesStaleBadge pins that the
// statusbar segment swaps NEED → STL and uses the muted palette when the
// head entry has no matching live pane.
func TestFormatStatusNotifyWithLiveSubstitutesStaleBadge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{{
		ID:        "ai:gone:%9",
		Text:      "codex: reply ready",
		Severity:  notify.SeverityInfo,
		Source:    notify.SourceAI,
		Session:   "gone",
		Pane:      "%9",
		CreatedAt: now.Add(-30 * time.Second),
	}}
	out := formatStatusNotifyWithLive(entries, 80, now, map[string]notifyLivePane{})
	if !strings.Contains(out, " STL ") {
		t.Fatalf("stale status segment = %q, want STL abbreviation", out)
	}
	if !strings.Contains(out, notifyBadgeStaleOpen) {
		t.Fatalf("stale status segment = %q, want stale palette", out)
	}
	if strings.Contains(out, " NEED ") {
		t.Fatalf("stale status segment must not still carry the live NEED label: %q", out)
	}
}

// TestFormatStatusNotifyWithLiveSubstitutesGoneBadge pins the same for
// GONE → GON when the head entry has no routable target.
func TestFormatStatusNotifyWithLiveSubstitutesGoneBadge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{{
		ID:        "ext:orphan",
		Text:      "needs attention",
		Severity:  notify.SeverityInfo,
		Source:    notify.SourceExternal,
		Session:   "",
		CreatedAt: now,
	}}
	out := formatStatusNotifyWithLive(entries, 80, now, nil)
	if !strings.Contains(out, " GON ") {
		t.Fatalf("gone status segment = %q, want GON abbreviation", out)
	}
	if !strings.Contains(out, notifyBadgeGoneOpen) {
		t.Fatalf("gone status segment = %q, want gone palette", out)
	}
}

// TestFormatStatusNotifyLiveEntryKeepsExistingNeedBadge guards the
// regression contract: live entries keep their original NEED/INFO/WARN/CRIT
// labels.
func TestFormatStatusNotifyLiveEntryKeepsExistingNeedBadge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{{
		ID:        "ai:s:%1",
		Text:      "claude: reply ready",
		Severity:  notify.SeverityInfo,
		Source:    notify.SourceAI,
		Session:   "s",
		Pane:      "%1",
		CreatedAt: now,
	}}
	liveByID := map[string]notifyLivePane{
		"ai:s:%1": {ID: "ai:s:%1", Session: "s", Pane: "%1", ShouldQueue: true},
	}
	out := formatStatusNotifyWithLive(entries, 80, now, liveByID)
	if !strings.Contains(out, " NEED ") {
		t.Fatalf("live status segment = %q, want NEED badge", out)
	}
	if strings.Contains(out, " STL ") || strings.Contains(out, " GON ") {
		t.Fatalf("live status segment must not carry stale/gone abbreviations: %q", out)
	}
}

// TestStatusbarClickNotifyStaleHeadSkipsFocus pins the click fast path:
// when the head entry classifies as STALE the statusbar emits the ack-only
// toast, skips the `projmux focus` subprocess, and still acks the entry so
// the same row does not re-toast on the next click. The fast-path contract
// is "skip the focus round-trip", not "leave the row pending forever" —
// leaving it pending strands the user on a row they can no longer route to.
//
// We force the stale classification with a non-empty live response that
// excludes the head entry. An empty live result is intentionally treated as
// "no live data; fall back to live" by classifyHeadDisplayBestEffort (the
// docker e2e harness hits that fallback), so we have to seed at least one
// unrelated reply-state pane to keep the live map non-empty here.
func TestStatusbarClickNotifyStaleHeadSkipsFocus(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{
		respond: func(name string, args []string) ([]byte, error) {
			if name == "tmux" && containsArg(args, "list-panes") {
				// One unrelated reply-state pane → liveByID is non-empty but
				// does not contain the head id → ai-prefixed head is stale.
				return notifyLivePaneRows(
					[]string{"other", "@9", "%9", "0", "claude", "reply", "waiting", "claude", "topic", "/tmp/tmux/default"},
				), nil
			}
			return nil, nil
		},
	}
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:        "ai:main:%2",
			Text:      "codex: reply ready",
			Severity:  notify.SeverityCritical,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%2",
			CreatedAt: now,
		},
		{
			ID:        "ai:main:%2:older-info",
			Text:      "older same-pane info",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%2",
			CreatedAt: now.Add(-time.Minute),
		},
		{
			ID:        "ai:main:%2:older-critical",
			Text:      "older same-pane critical",
			Severity:  notify.SeverityCritical,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%2",
			CreatedAt: now.Add(-2 * time.Minute),
		},
		{
			ID:        "ai:main:%3:older-info",
			Text:      "older other-pane info",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "main",
			Window:    "1",
			Pane:      "%3",
			CreatedAt: now.Add(-3 * time.Minute),
		},
	}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if call.name == "/usr/local/bin/projmux" {
			t.Fatalf("focus subprocess was invoked despite stale head; call = %#v", call)
		}
	}
	if !sawTmuxDisplayMessage(runner.calls, notifyAckOnlyToast(notifyDisplayStale)) {
		t.Fatalf("missing stale ack-only toast; calls = %#v", runner.calls)
	}
	wantAcked := []string{"ai:main:%2", "ai:main:%2:older-info"}
	if !equalStringSlices(store.ackedIDs, wantAcked) {
		t.Fatalf("ackedIDs = %#v, want %#v (stale fast path must clear selected row and older same-pane non-critical AI rows)", store.ackedIDs, wantAcked)
	}
}

// TestStatusbarClickNotifyGoneHeadSkipsFocus pins the same fast path for
// the GONE classification (unroutable head). The legacy "target empty"
// guard short-circuits with its own toast before the fast path runs; that
// pre-existing toast does not ack, so we keep its semantics untouched here.
// The new-style fast path is exercised by other tests.
func TestStatusbarClickNotifyGoneHeadSkipsFocus(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	store := &stubNotifyStore{listEntries: []notify.Notification{{
		ID:     "ext:orphan",
		Text:   "stale ext entry",
		Source: notify.SourceExternal,
	}}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if call.name == "/usr/local/bin/projmux" {
			t.Fatalf("focus subprocess was invoked despite gone head; call = %#v", call)
		}
	}
}

// TestStatusbarClickNotifyEmptyLiveResultFallsBackToFocus pins the
// best-effort safety net: when the live-pane probe returns *no* panes
// (the docker e2e harness with a default tmux socket and no projmux
// options registered server-side hits this shape), `ai:`-prefixed head
// entries must NOT be falsely tagged STALE. Instead the click must
// proceed through the normal focus subprocess → ack flow so the user's
// queue actually drains.
func TestStatusbarClickNotifyEmptyLiveResultFallsBackToFocus(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{
		respond: func(name string, args []string) ([]byte, error) {
			if name == "tmux" && containsArg(args, "list-panes") {
				// Empty output → liveByID is an empty map. The fast path
				// must treat that as "no data; assume live" so it does
				// not strand the entry.
				return []byte(""), nil
			}
			return nil, nil
		},
	}
	store := &stubNotifyStore{listEntries: []notify.Notification{{
		ID:      "ai:e2e-alpha:%0",
		Text:    "codex: reply ready · docker e2e",
		Source:  notify.SourceAI,
		Session: "e2e-alpha",
		Pane:    "%0",
	}}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var sawFocus bool
	for _, call := range runner.calls {
		if call.name == "/usr/local/bin/projmux" && len(call.args) >= 1 && call.args[0] == "focus" {
			sawFocus = true
			break
		}
	}
	if !sawFocus {
		t.Fatalf("focus subprocess must run when live data is unavailable; calls = %#v", runner.calls)
	}
	if store.ackedID != "ai:e2e-alpha:%0" {
		t.Fatalf("ackedID = %q, want %q (focus success must ack the head entry)", store.ackedID, "ai:e2e-alpha:%0")
	}
}

func findEntryByIDOrFail(t *testing.T, entries []notify.Notification, id string) notify.Notification {
	t.Helper()
	for _, e := range entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("entry %q not in fixture", id)
	return notify.Notification{}
}

// TestStatusCommandNotifyLiveByIDBestEffortSwallowsRunnerError pins that a
// tmux failure during the status segment refresh degrades to nil (no live
// data) instead of bubbling up. Status segments must never fail loudly.
func TestStatusCommandNotifyLiveByIDBestEffortSwallowsRunnerError(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand("/tmp")
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux not running")
	}
	if got := cmd.notifyLiveByIDBestEffort(); got != nil {
		t.Fatalf("notifyLiveByIDBestEffort = %v, want nil on runner error", got)
	}
}
