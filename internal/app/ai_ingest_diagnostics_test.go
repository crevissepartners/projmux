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

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

const aiDiagnosticsPrivacySeed = "prompt-tool-path-uuid-conversation-session-title-SEED-1234"

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
		{Source: "codex-hook", Event: "PermissionRequest", Result: "error", Reason: "route " + aiDiagnosticsPrivacySeed, Pane: aiDiagnosticsPrivacySeed},
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
	if err := cmd.ingestBell(""); err != nil {
		t.Fatalf("blank bell error = %v", err)
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
	cmd, store, lifecycle, _ := testAIOperationalCommand(t)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		if reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%44", "#{pane_id}"}) {
			checks++
			if checks > 40 {
				return nil, os.ErrNotExist
			}
			return []byte("%44\n"), nil
		}
		return []byte("\n"), nil
	}
	if err := cmd.Run([]string{"watch-title", "%44"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read()
	if err != nil || len(events) != 2 {
		t.Fatalf("watcher events = %#v err=%v, want bounded start+stop", events, err)
	}
	if events[0].AIResult != string(diagnostics.AIResultStarted) || events[1].AIResult != string(diagnostics.AIResultPaneGone) {
		t.Fatalf("watcher transitions = %#v", events)
	}
	if !lifecycle.RecordedOutcome() {
		t.Fatal("watcher stop did not own the top-level outcome")
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
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
