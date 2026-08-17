package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

const aiDiagnosticsPrivacySeed = "prompt-tool-path-uuid-conversation-session-title-SEED-1234"

func TestAIDiagnosticsProductionGraphWiresAIRecorder(t *testing.T) {
	writer := &appLifecycleWriter{}
	lifecycle := diagnostics.NewLifecycleRecorder(writer, "ai-production-graph", "0.10.0", "tmux")
	app := NewWithLifecycleDiagnostics(lifecycle)
	if app.ai.operationalDiagnostics == nil {
		t.Fatal("production app graph did not wire the common AI recorder")
	}
	err := app.ai.Run([]string{"ingest", "bell"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("blank bell unexpectedly succeeded")
	}
	if len(writer.events) != 1 || writer.events[0].Event != "ai.ingest.outcome" || writer.events[0].Failure != "target-invalid" {
		t.Fatalf("production graph events = %#v", writer.events)
	}
	if !lifecycle.RecordedOutcome() {
		t.Fatal("production graph AI terminal did not own the top level")
	}
}

func testAIOperationalCommand(t *testing.T) (*aiCommand, *diagnostics.Store, *diagnostics.LifecycleRecorder, string) {
	t.Helper()
	home := t.TempDir()
	stateHome := filepath.Join(home, "state")
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "XDG_STATE_HOME":
			return stateHome
		case "TMUX_PANE":
			return "%7"
		default:
			return ""
		}
	}
	store := diagnostics.NewStore(filepath.Join(stateHome, "projmux", diagnostics.LogDirName, diagnostics.LogFileName))
	lifecycle := diagnostics.NewLifecycleRecorder(store, "ai-app-run", "0.10.0", diagnostics.MuxBackend())
	cmd.operationalDiagnostics = lifecycle.AI()
	return cmd, store, lifecycle, stateHome
}

func TestAIIngestOperationalProjectionPrivacyVolumeAndLegacyParity(t *testing.T) {
	cmd, store, _, stateHome := testAIOperationalCommand(t)

	// Known normal quiet/state/notify/dedupe outcomes remain legacy-only. The
	// common journal is reserved for bounded anomalous outcomes.
	for _, entry := range []aiIngestLogEntry{
		{Source: "codex-hook", Event: "PreToolUse", Result: "quiet", Reason: aiDiagnosticsPrivacySeed, CWD: "/private/" + aiDiagnosticsPrivacySeed},
		{Source: "claude-hook", Event: "UserPromptSubmit", Result: "state", SessionID: aiDiagnosticsPrivacySeed},
		{Source: "antigravity-hook", Event: "Stop", Result: "notify", ThreadID: aiDiagnosticsPrivacySeed},
		{Source: "tmux-bell", Event: "bell", Result: "deduped", Pane: aiDiagnosticsPrivacySeed},
	} {
		cmd.appendAIIngestLog(entry)
	}
	if events, err := store.Read(); err != nil || len(events) != 0 {
		t.Fatalf("normal common events = %#v err=%v, want zero", events, err)
	}

	// Each legacy anomaly is projected through a closed provider/kind/result/
	// failure tuple. Raw legacy fields remain available only in ai-ingest.log.
	for _, entry := range []aiIngestLogEntry{
		{Source: "codex-hook", Result: "error", Reason: "parse " + aiDiagnosticsPrivacySeed},
		{Source: "claude-hook", Event: "Stop", Result: "ignored", Reason: "no matching pane", CWD: "/private/" + aiDiagnosticsPrivacySeed},
		{Source: "antigravity-hook", Event: "FutureSecretEvent-" + aiDiagnosticsPrivacySeed, Result: "quiet", Reason: aiDiagnosticsPrivacySeed},
		{Source: "tmux-bell", Event: "bell", Result: "ignored", Reason: "blank pane"},
		{Source: "antigravity-hook", Event: "PreToolUse", Result: "error", Reason: "route " + aiDiagnosticsPrivacySeed, Pane: aiDiagnosticsPrivacySeed},
	} {
		cmd.appendAIIngestLog(entry)
	}
	events, err := store.Read()
	if err != nil || len(events) != 5 {
		t.Fatalf("common events = %#v err=%v, want five anomaly classes", events, err)
	}
	for _, event := range events {
		if event.Event != "ai.ingest.outcome" || event.Provider == "" || event.AIKind == "" || event.AIResult == "" || event.Failure == "" || event.Message != "" {
			t.Fatalf("unsafe/incomplete common event = %#v", event)
		}
	}
	commonRaw, _ := json.Marshal(events)
	for _, raw := range []string{aiDiagnosticsPrivacySeed, "/private/", "FutureSecretEvent", "PermissionRequest", "no matching pane", "blank pane"} {
		if bytes.Contains(commonRaw, []byte(raw)) {
			t.Fatalf("common operations leaked %q: %s", raw, commonRaw)
		}
	}

	legacyPath := filepath.Join(stateHome, "projmux", aiIngestLogName)
	legacy, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(nonEmptyLines(string(legacy))); got != 9 {
		t.Fatalf("legacy rows = %d, want all nine compatibility records", got)
	}
	var stdout bytes.Buffer
	if err := cmd.runIngestLog([]string{"--json", "--tail", "20"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := len(nonEmptyLines(stdout.String())); got != 9 {
		t.Fatalf("legacy consumer rows = %d, want 9", got)
	}

	report := &diagnosticsCommand{lookupEnv: cmd.lookupEnv, homeDir: cmd.homeDir}
	data, manifest := report.supportAIIngestSummary()
	if manifest.Status != "included" || manifest.RecordCount == 0 || bytes.Contains(data, []byte(aiDiagnosticsPrivacySeed)) {
		t.Fatalf("legacy support consumer manifest=%#v data=%s", manifest, data)
	}
}

func TestAIIngestActualCodexNotifyLeavesAIQuietAndPhase4OwnsTransition(t *testing.T) {
	cmd, store, lifecycle, _ := testAIOperationalCommand(t)
	cmd.notifyDiagnostics = lifecycle.NotifyFocus()
	queue := &stubNotifyStore{}
	cmd.producer = &storeAttentionNotifyProducer{store: queue, ttl: time.Minute, diagnostics: cmd.notifyDiagnostics}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name":"Stop",
		"session_id":"codex-session",
		"turn_id":"` + aiDiagnosticsPrivacySeed + `",
		"cwd":"/repo/projmux",
		"last_assistant_message":"` + aiDiagnosticsPrivacySeed + `"
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")
	started := time.Now()
	err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	events := readAIOperationalEvents(t, store)
	if got := countOperationalFamily(events, "ai.ingest.outcome"); got != 0 {
		t.Fatalf("common AI outcomes = %d in %#v, want zero", got, events)
	}
	if got := countOperationalFamily(events, "notify.transition"); got == 0 {
		t.Fatalf("events = %#v, want Phase 4 notify transition", events)
	}
	if len(queue.pushed) != 1 {
		t.Fatalf("queue pushes = %d, want actual Codex notify enqueue", len(queue.pushed))
	}
	if lifecycle.RecordedOutcome() {
		t.Fatal("secondary Phase 4 notify transition claimed the ingest top level")
	}
	if err := diagnostics.RecordOutcome(store, []string{"internal", "agent-hook", "ingest", "codex-hook"}, "ai-app-run", "0.10.0", "tmux", started, err, false, lifecycle.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	events = readAIOperationalEvents(t, store)
	if got := countOperationalFamily(events, "command.outcome"); got != 0 {
		t.Fatalf("generic outcomes = %d in %#v, want automatic success zero-volume", got, events)
	}
	raw, _ := json.Marshal(events)
	if bytes.Contains(raw, []byte(aiDiagnosticsPrivacySeed)) {
		t.Fatalf("operations leaked Codex hook data: %s", raw)
	}
}

func TestAIIngestActualClaudeNormalUnknownAndUnmatched(t *testing.T) {
	t.Run("known normal", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		cmd.stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","session_id":"` + aiDiagnosticsPrivacySeed + `","cwd":"/repo/projmux","tool_input":{"path":"/private/` + aiDiagnosticsPrivacySeed + `"}}`)
		cmd.readCommand = claudeIngestReadCommand("%8")
		if err := cmd.Run([]string{"ingest", "claude-hook"}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		if events := readAIOperationalEvents(t, store); len(events) != 0 {
			t.Fatalf("known quiet Claude hook events = %#v, want zero", events)
		}
		if lifecycle.RecordedOutcome() {
			t.Fatal("known quiet Claude hook unexpectedly claimed top level")
		}
	})

	t.Run("unknown classified", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		cmd.stdin = strings.NewReader(`{"hook_event_name":"Future-` + aiDiagnosticsPrivacySeed + `","session_id":"` + aiDiagnosticsPrivacySeed + `","cwd":"/repo/projmux"}`)
		cmd.readCommand = claudeIngestReadCommand("%8")
		if err := cmd.Run([]string{"ingest", "claude-hook"}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		events := readAIOperationalEvents(t, store)
		if len(events) != 1 || events[0].Provider != "claude" || events[0].AIKind != "unknown" || events[0].AIResult != "ignored" || events[0].Failure != "unsupported-event" {
			t.Fatalf("unknown Claude events = %#v", events)
		}
		if !lifecycle.RecordedOutcome() {
			t.Fatal("terminal unknown classification did not claim top level")
		}
	})

	t.Run("unmatched target", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		lookupEnv := cmd.lookupEnv
		cmd.lookupEnv = func(name string) string {
			if name == "TMUX_PANE" {
				return ""
			}
			return lookupEnv(name)
		}
		cmd.stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"` + aiDiagnosticsPrivacySeed + `","cwd":"/private/` + aiDiagnosticsPrivacySeed + `"}`)
		if err := cmd.Run([]string{"ingest", "claude-hook"}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		events := readAIOperationalEvents(t, store)
		if len(events) != 1 || events[0].Provider != "claude" || events[0].AIKind != "stop" || events[0].Failure != "target-unmatched" {
			t.Fatalf("unmatched Claude events = %#v", events)
		}
		if !lifecycle.RecordedOutcome() {
			t.Fatal("terminal unmatched target did not claim top level")
		}
	})
}

func TestAIIngestActualAntigravityKnownQuietAndRouteFailure(t *testing.T) {
	t.Run("known quiet", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		cmd.stdin = strings.NewReader(`{"conversation_id":"` + aiDiagnosticsPrivacySeed + `","cwd":"/repo/projmux","tool_call":{"path":"/private/` + aiDiagnosticsPrivacySeed + `"}}`)
		if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "PostToolUse"}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		if events := readAIOperationalEvents(t, store); len(events) != 0 {
			t.Fatalf("known quiet Antigravity events = %#v, want zero", events)
		}
		if lifecycle.RecordedOutcome() {
			t.Fatal("known quiet Antigravity hook unexpectedly claimed top level")
		}
	})

	t.Run("unsupported response route", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		cmd.stdin = strings.NewReader(`{"conversation_id":"` + aiDiagnosticsPrivacySeed + `","cwd":"/repo/projmux","tool_call":{"path":"/private/` + aiDiagnosticsPrivacySeed + `"}}`)
		started := time.Now()
		err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "PreToolUse"}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "intentionally unsupported") {
			t.Fatalf("Antigravity route error = %v", err)
		}
		events := readAIOperationalEvents(t, store)
		if len(events) != 1 || events[0].Provider != "antigravity" || events[0].AIKind != "tool" || events[0].AIResult != "failed" || events[0].Failure != "route-failed" {
			t.Fatalf("Antigravity route events = %#v", events)
		}
		assertNoGenericAIOutcome(t, store, lifecycle, []string{"internal", "agent-hook", "ingest", "antigravity-hook", "--event", "PreToolUse"}, started, err)
	})
}

func TestAIHookDiagnosticClassificationUsesProviderCatalogs(t *testing.T) {
	tests := []struct {
		name     string
		provider diagnostics.Provider
		event    string
		want     diagnostics.AIKind
	}{
		{"codex tool", diagnostics.ProviderCodex, "PreToolUse", diagnostics.AIKindTool},
		{"codex rejects Claude notification", diagnostics.ProviderCodex, "Notification", diagnostics.AIKindUnknown},
		{"claude notification", diagnostics.ProviderClaude, "Notification", diagnostics.AIKindNotification},
		{"claude rejects Antigravity invocation", diagnostics.ProviderClaude, "PreInvocation", diagnostics.AIKindUnknown},
		{"antigravity pre tool", diagnostics.ProviderAntigravity, "PreToolUse", diagnostics.AIKindTool},
		{"antigravity invocation", diagnostics.ProviderAntigravity, "PostInvocation", diagnostics.AIKindInvocation},
		{"antigravity rejects Claude lifecycle", diagnostics.ProviderAntigravity, "SessionStart", diagnostics.AIKindUnknown},
		{"bell ignores raw event name", diagnostics.ProviderTmuxBell, aiDiagnosticsPrivacySeed, diagnostics.AIKindBell},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyAIHookKind(tt.provider, tt.event); got != tt.want {
				t.Fatalf("classifyAIHookKind(%q, %q) = %q, want %q", tt.provider, tt.event, got, tt.want)
			}
		})
	}
}

func TestAIIngestActualBellValidInvalidUnmatchedAndRouteFailure(t *testing.T) {
	t.Run("valid notification stays AI quiet", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		queue := &stubNotifyStore{}
		cmd.notifyStore = queue
		wireAIDiagnosticsBellPane(cmd, "%9")
		if err := cmd.Run([]string{"ingest", "bell", "--pane", "%9"}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		if len(queue.pushed) != 1 {
			t.Fatalf("bell queue pushes = %d, want one", len(queue.pushed))
		}
		if events := readAIOperationalEvents(t, store); len(events) != 0 {
			t.Fatalf("valid bell events = %#v, want zero common AI outcomes", events)
		}
		if lifecycle.RecordedOutcome() {
			t.Fatal("valid bell unexpectedly claimed top level")
		}
	})

	t.Run("blank CLI target", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		started := time.Now()
		err := cmd.Run([]string{"ingest", "bell"}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "requires --pane") {
			t.Fatalf("blank bell error = %v", err)
		}
		assertSingleAIBellOutcome(t, store, "ignored", "target-invalid")
		assertNoGenericAIOutcome(t, store, lifecycle, []string{"internal", "agent-hook", "ingest", "bell"}, started, err)
	})

	t.Run("pane not found", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		if err := cmd.Run([]string{"ingest", "bell", "--pane", "%404"}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		assertSingleAIBellOutcome(t, store, "ignored", "target-unmatched")
		if !lifecycle.RecordedOutcome() {
			t.Fatal("pane-not-found bell did not claim terminal ownership")
		}
	})

	t.Run("queue route failure", func(t *testing.T) {
		cmd, store, lifecycle, _ := testAIOperationalCommand(t)
		cmd.notifyStore = &stubNotifyStore{pushErr: errors.New("queue " + aiDiagnosticsPrivacySeed)}
		wireAIDiagnosticsBellPane(cmd, "%9")
		started := time.Now()
		err := cmd.Run([]string{"ingest", "bell", "--pane", "%9"}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "queue") {
			t.Fatalf("bell route error = %v", err)
		}
		assertSingleAIBellOutcome(t, store, "failed", "route-failed")
		assertNoGenericAIOutcome(t, store, lifecycle, []string{"internal", "agent-hook", "ingest", "bell", "--pane", "%9"}, started, err)
	})
}

func TestAIIngestMalformedReadOversizeUnknownAndBellSurfaces(t *testing.T) {
	cmd, store, lifecycle, _ := testAIOperationalCommand(t)
	for _, provider := range []string{"codex-hook", "claude-hook", "antigravity-hook"} {
		cmd.stdin = strings.NewReader(`{"hook_event_name":` + aiDiagnosticsPrivacySeed)
		if err := cmd.Run([]string{"ingest", provider}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("%s malformed payload unexpectedly succeeded", provider)
		}
	}
	cmd.stdin = strings.NewReader(`{"hook_event_name":"Future-` + aiDiagnosticsPrivacySeed + `","prompt":"` + aiDiagnosticsPrivacySeed + `"}`)
	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("unknown codex event error = %v", err)
	}
	if err := cmd.Run([]string{"ingest", "bell"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("blank bell CLI unexpectedly succeeded")
	}

	cmd.stdin = errorReader{err: errors.New("read " + aiDiagnosticsPrivacySeed)}
	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("read failure unexpectedly succeeded")
	}
	cmd.stdin = strings.NewReader(strings.Repeat("x", 1024*1024+1))
	if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("oversized payload unexpectedly succeeded")
	}

	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 7 {
		t.Fatalf("events = %d %#v, want parse(3)+unknown+bell+read+oversize", len(events), events)
	}
	if !lifecycle.RecordedOutcome() {
		t.Fatal("failed ingest did not claim generic top-level ownership")
	}
	raw, _ := json.Marshal(events)
	if bytes.Contains(raw, []byte(aiDiagnosticsPrivacySeed)) {
		t.Fatalf("operations leaked seeded payload: %s", raw)
	}
}

func TestAIWatcherLifecycleIsBoundedAndSuppressesGenericOutcome(t *testing.T) {
	for _, tt := range []struct {
		name       string
		terminal   diagnostics.AIResult
		gateOutput func(int) ([]byte, error)
	}{
		{name: "pane gone after polling", terminal: diagnostics.AIResultPaneGone, gateOutput: func(check int) ([]byte, error) {
			if check > 40 {
				return nil, os.ErrNotExist
			}
			return []byte("%44__PROJMUX_TMUX_AI_GATE_SEP__\n"), nil
		}},
		{name: "hook active", terminal: diagnostics.AIResultHookActive, gateOutput: func(int) ([]byte, error) {
			return []byte("%44__PROJMUX_TMUX_AI_GATE_SEP__1\n"), nil
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, store, lifecycle, _ := testAIOperationalCommand(t)
			checks := 0
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "tmux" {
					return nil, os.ErrNotExist
				}
				if reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%44", "#{pane_id}__PROJMUX_TMUX_AI_GATE_SEP__#{@projmux_ai_hook_active}"}) {
					checks++
					return tt.gateOutput(checks)
				}
				return []byte("\n"), nil
			}
			started := time.Now()
			err := cmd.Run([]string{"watch-title", "%44"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			events := readAIOperationalEvents(t, store)
			if len(events) != 2 || events[0].AIResult != string(diagnostics.AIResultStarted) || events[1].AIResult != string(tt.terminal) {
				t.Fatalf("watcher transitions = %#v, want bounded start+%s", events, tt.terminal)
			}
			if !lifecycle.RecordedOutcome() {
				t.Fatal("watcher terminal did not own the top-level outcome")
			}
			assertNoGenericAIOutcome(t, store, lifecycle, []string{"internal", "agent-hook", "watch-title", "%44"}, started, err)
		})
	}
}

func TestAIWatcherActualLaunchFailureWiring(t *testing.T) {
	cmd, store, lifecycle, _ := testAIOperationalCommand(t)
	launchErr := errors.New("launch " + aiDiagnosticsPrivacySeed)
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		if name == "tmux" && len(args) >= 1 && args[0] == "run-shell" {
			return launchErr
		}
		return nil
	}
	cmd.startAIWatchTitle("%55")
	events := readAIOperationalEvents(t, store)
	if len(events) != 1 || events[0].Event != "ai.watcher.transition" || events[0].AIResult != "failed" || events[0].Failure != "watcher-launch-failed" {
		t.Fatalf("launch failure events = %#v", events)
	}
	if lifecycle.RecordedOutcome() {
		t.Fatal("nested watcher launch failure claimed the outer split top level")
	}
	raw, _ := json.Marshal(events)
	if bytes.Contains(raw, []byte(aiDiagnosticsPrivacySeed)) || bytes.Contains(raw, []byte("%55")) {
		t.Fatalf("watcher launch event leaked error/target: %s", raw)
	}
}

func TestAIOperationalAppendFailureDoesNotChangeIngestError(t *testing.T) {
	home := t.TempDir()
	blocker := filepath.Join(home, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	lifecycle := diagnostics.NewLifecycleRecorder(diagnostics.NewStore(filepath.Join(blocker, "operations.jsonl")), "append-fails", "0.10.0", "tmux")
	cmd := testAICommand(home)
	cmd.operationalDiagnostics = lifecycle.AI()
	cmd.stdin = strings.NewReader(`{"hook_event_name":` + aiDiagnosticsPrivacySeed)
	err := cmd.Run([]string{"ingest", "codex-hook"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "parse codex hook payload") {
		t.Fatalf("ingest error = %v, want original parse semantics", err)
	}
	if !lifecycle.RecordedOutcome() {
		t.Fatal("append failure lost logical top-level ownership")
	}
	fallback := diagnostics.NewStore(filepath.Join(t.TempDir(), "operations.jsonl"))
	if err := diagnostics.RecordOutcome(fallback, []string{"internal", "agent-hook", "ingest", "codex-hook"}, "append-fails", "0.10.0", "tmux", time.Now(), err, false, lifecycle.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	if events := readAIOperationalEvents(t, fallback); len(events) != 0 {
		t.Fatalf("generic fallback events = %#v, want append-failure ownership suppression", events)
	}
}

func readAIOperationalEvents(t *testing.T, store *diagnostics.Store) []diagnostics.Event {
	t.Helper()
	events, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func countOperationalFamily(events []diagnostics.Event, family string) int {
	count := 0
	for _, event := range events {
		if event.Event == family {
			count++
		}
	}
	return count
}

func assertNoGenericAIOutcome(t *testing.T, store *diagnostics.Store, lifecycle *diagnostics.LifecycleRecorder, args []string, started time.Time, commandErr error) {
	t.Helper()
	if err := diagnostics.RecordOutcome(store, args, "ai-app-run", "0.10.0", "tmux", started, commandErr, false, lifecycle.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	if got := countOperationalFamily(readAIOperationalEvents(t, store), "command.outcome"); got != 0 {
		t.Fatalf("generic outcomes = %d, want zero", got)
	}
}

func assertSingleAIBellOutcome(t *testing.T, store *diagnostics.Store, result, failure string) {
	t.Helper()
	events := readAIOperationalEvents(t, store)
	if len(events) != 1 || events[0].Provider != "tmux-bell" || events[0].AIKind != "bell" || events[0].AIResult != result || events[0].Failure != failure {
		t.Fatalf("bell events = %#v, want %s/%s", events, result, failure)
	}
}

func wireAIDiagnosticsBellPane(cmd *aiCommand, paneID string) {
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, aiBellPaneFormat}):
			return []byte("workspace\\037@1\\037editor\\037" + paneID + "\\037" + aiDiagnosticsPrivacySeed + "\\037node\\037/tmp/tmux-safe\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{" + aiBellDedupeOption + "}"}):
			return []byte("\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
