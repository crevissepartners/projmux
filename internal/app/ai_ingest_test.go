package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	antigravityadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/antigravity"
	"github.com/crevissepartners/projmux/internal/i18n"
)

func TestPersistAntigravityContextUsage(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	cmd.now = func() time.Time { return now }

	// Numeric context_window is persisted so the adapter surfaces it.
	cmd.persistAntigravityContextUsage(antigravityHookPayload{ContextWindow: "42%"})

	baseDir, err := cmd.usageStateDir()
	if err != nil {
		t.Fatalf("usageStateDir: %v", err)
	}
	snaps, err := antigravityadapter.New(baseDir).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Pct != 42 || snaps[0].Model != "antigravity" {
		t.Fatalf("snapshots = %#v, want single antigravity 42%% context row", snaps)
	}

	// Non-numeric / empty values are ignored (clean degrade, no garbage file
	// churn) — the previously persisted value stays intact.
	cmd.persistAntigravityContextUsage(antigravityHookPayload{ContextWindow: ""})
	cmd.persistAntigravityContextUsage(antigravityHookPayload{ContextWindow: "n/a"})
	snaps, err = antigravityadapter.New(baseDir).Collect(context.Background())
	if err != nil || len(snaps) != 1 || snaps[0].Pct != 42 {
		t.Fatalf("after non-numeric writes snapshots = %#v err = %v, want unchanged 42%%", snaps, err)
	}
}

func TestPersistAntigravityQuotaUsageKeepsContextAndSurvivesContextOnlyPayload(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	now := time.Date(2026, 8, 12, 3, 0, 0, 0, time.UTC)
	cmd.now = func() time.Time { return now }
	reset := now.Add(2 * time.Hour)
	seconds := int64(7200)
	cmd.persistAntigravityContextUsage(antigravityHookPayload{
		ConversationID:           "conversation-local",
		ContextUsedPercentage:    25,
		ContextUsedPercentageSet: true,
	})
	cmd.persistAntigravityQuotaUsage(antigravityHookPayload{
		QuotaSet: true,
		QuotaBuckets: []antigravityadapter.QuotaBucketRecord{{
			ID: "context", RemainingFraction: 0.75, ResetTime: reset, ResetInSeconds: &seconds,
		}},
	})
	// A later context-only payload must not erase the independent quota file.
	cmd.persistAntigravityQuotaUsage(antigravityHookPayload{})

	baseDir, err := cmd.usageStateDir()
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := antigravityadapter.New(baseDir).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].Window != "context" || snaps[1].Window != "quota" || snaps[1].Bucket != "context" {
		t.Fatalf("snapshots = %#v, want distinct context and quota/context rows", snaps)
	}
}

func TestParseCodexHookPayload(t *testing.T) {
	t.Parallel()

	payload, err := parseCodexHookPayload([]byte(`{
		"hook_event_name": "PermissionRequest",
		"thread_id": "thread-123",
		"session_id": "codex-session",
		"turn_id": "turn-456",
		"cwd": "/repo/projmux",
		"transcript_path": "/tmp/codex.jsonl",
		"model": "gpt-5.1-codex",
		"tool": {"name": "Bash"},
		"tool_input": {"command": "go test ./internal/app"}
	}`))
	if err != nil {
		t.Fatalf("parseCodexHookPayload() error = %v", err)
	}
	if payload.EventName != "PermissionRequest" || payload.ThreadID != "thread-123" || payload.SessionID != "codex-session" || payload.TurnID != "turn-456" || payload.CWD != "/repo/projmux" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.TranscriptPath != "/tmp/codex.jsonl" || payload.Model != "gpt-5.1-codex" || payload.ToolName != "Bash" {
		t.Fatalf("payload fields = %+v", payload)
	}
	if got := stringFromAny(payload.ToolInput["command"]); got != "go test ./internal/app" {
		t.Fatalf("tool input command = %q", got)
	}

	payload, err = parseCodexHookPayload([]byte(`{
		"event_name": "PermissionRequest",
		"session-id": "codex-session",
		"workspace": {"path": "/repo/projmux"},
		"tool": {"name": "Shell", "command": "make test"}
	}`))
	if err != nil {
		t.Fatalf("parseCodexHookPayload() alias error = %v", err)
	}
	if payload.EventName != "PermissionRequest" || payload.CWD != "/repo/projmux" || payload.ToolName != "Shell" {
		t.Fatalf("alias payload = %+v", payload)
	}
	if got := stringFromAny(payload.ToolInput["command"]); got != "make test" {
		t.Fatalf("alias tool input command = %q", got)
	}
}

func TestMatchAIPanePriority(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX_PANE" {
			return "%env"
		}
		return ""
	}
	if got := cmd.matchAIPane(aiPaneMatchInput{CWD: "/repo/projmux", ThreadID: "thread-2"}); got != "%env" {
		t.Fatalf("env match = %q, want %%env", got)
	}

	cmd.lookupEnv = func(string) string { return "" }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-a", "-F", aiIngestListPanesFormat}) {
			return []byte("%cwd\x1f/repo/projmux\x1fthread-1\x1fsession-1\n%thread\x1f/repo/other\x1fthread-2\x1fsession-2\n"), nil
		}
		return nil, os.ErrNotExist
	}
	if got := cmd.matchAIPane(aiPaneMatchInput{CWD: "/repo/projmux", ThreadID: "thread-2"}); got != "%cwd" {
		t.Fatalf("cwd match = %q, want %%cwd", got)
	}
	if got := cmd.matchAIPane(aiPaneMatchInput{ThreadID: "thread-2"}); got != "%thread" {
		t.Fatalf("thread match = %q, want %%thread", got)
	}
	if got := cmd.matchAIPane(aiPaneMatchInput{SessionID: "session-2"}); got != "%thread" {
		t.Fatalf("session match = %q, want %%thread", got)
	}
}

func TestIngestCodexHookPermissionPushesCriticalQueueEntryAndMetadata(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "PermissionRequest",
		"session_id": "codex-session",
		"turn_id": "turn-456",
		"cwd": "/repo/projmux",
		"model": "gpt-5.1-codex",
		"tool_name": "Bash",
		"tool_input": {"command": "go test ./internal/app"}
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run ingest codex-hook PermissionRequest error = %v", err)
	}
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneAgentOption, aiModeCodex}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneSessionIDOption, "codex-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneThreadIDOption, "codex-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneResumeIDOption, "codex-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneResumeSourceOption, "hook"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneResumeUpdatedAtOption, "1970-01-01T00:00:00Z"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneStateOption, "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneBadgeKindOption, aiBadgeKindApprovalRequired}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:codex:permission:codex-session:turn-456:Bash:go test ./internal/app" {
		t.Fatalf("ID = %q", got.ID)
	}
	if got.Text != "Bash: go test ./internal/app" {
		t.Fatalf("Text = %q", got.Text)
	}
	if got.Severity != notify.SeverityCritical {
		t.Fatalf("Severity = %q", got.Severity)
	}
	if got.Metadata["agent"] != "codex" || got.Metadata["category"] != "approval_required" || got.Metadata["event"] != "PermissionRequest" || got.Metadata["model"] != "gpt-5.1-codex" || got.Metadata["tool_input.command"] != "go test ./internal/app" {
		t.Fatalf("Metadata = %#v", got.Metadata)
	}
	assertNoAIPaneTopicWrite(t, cmdRecorder(cmd).commands)
}

func TestIngestCodexHookStopPushesInfoQueueEntry(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "Stop",
		"session_id": "codex-session",
		"turn_id": "turn-456",
		"cwd": "/repo/projmux"
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest codex-hook Stop error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:codex:stop:codex-session:turn-456" || got.Text != "Ready" || got.Severity != notify.SeverityInfo || got.Metadata["category"] != "response_complete" {
		t.Fatalf("pushed = %#v", got)
	}
	if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneBadgeKindOption, aiBadgeKindResponseComplete}}) {
		t.Fatalf("commands = %#v, want response_complete semantic badge", cmdRecorder(cmd).commands)
	}
}

func TestIngestCodexHookUserPromptSetsThinkingWithoutQueue(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "UserPromptSubmit",
		"session_id": "codex-session",
		"cwd": "/repo/projmux",
		"model": "gpt-5.1-codex"
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest codex-hook UserPromptSubmit error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0", len(store.pushed))
	}
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneAgentOption, aiModeCodex}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneStateOption, "thinking"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneBadgeKindOption, aiBadgeKindInProgress}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", attentionStateOption, attentionStateBusy}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
	assertNoAIPaneTopicWrite(t, cmdRecorder(cmd).commands)
}

func TestIngestCodexHookQuietEventsMarkPaneAndLogWithoutNotify(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "PreToolUse",
		"session_id": "codex-session",
		"turn_id": "turn-456",
		"cwd": "/repo/projmux",
		"tool_name": "Bash",
		"tool_input": {"command": "go test ./internal/app"}
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest codex-hook PreToolUse error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
	}
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneAgentOption, aiModeCodex}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
	assertNoAIPaneTopicWrite(t, cmdRecorder(cmd).commands)
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"source":"codex-hook"`) || !strings.Contains(got, `"event":"PreToolUse"`) || !strings.Contains(got, `"result":"quiet"`) {
		t.Fatalf("log output = %q", got)
	}
}

func TestIngestCodexHookRuntimeNotifyPushesGenericQueueOnlyRow(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
		Version: 1,
		Providers: map[string]config.AIHookProviderActions{
			aiHookProviderCodex: {Events: map[string]string{"PreToolUse": aiHookActionNotify}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_NOTIFY_HOOK":
			return "/tmp/projmux-notify-hook"
		default:
			return ""
		}
	}
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		if name == "/tmp/projmux-notify-hook" {
			t.Fatalf("generic hook notify must not dispatch desktop notifier: %s %#v", name, args)
		}
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		return nil
	}
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "PreToolUse",
		"session_id": "codex-session",
		"turn_id": "turn-456",
		"cwd": "/repo/projmux",
		"model": "gpt-5.1-codex",
		"tool_name": "Bash",
		"tool_input": {"command": "go test ./internal/app", "description": "run focused tests"}
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest codex-hook PreToolUse error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1: %#v", len(store.pushed), store.pushed)
	}
	got := store.pushed[0]
	if got.Text != "PreToolUse · Bash" || got.Severity != notify.SeverityInfo {
		t.Fatalf("pushed = %#v", got)
	}
	if got.Metadata["provider"] != "codex" || got.Metadata["event"] != "PreToolUse" || got.Metadata["tool"] != "Bash" || got.Metadata["cwd"] != "/repo/projmux" || got.Metadata["session_id"] != "codex-session" || got.Metadata["turn_id"] != "turn-456" {
		t.Fatalf("metadata = %#v", got.Metadata)
	}
	for key := range got.Metadata {
		if strings.HasPrefix(key, "tool_input.") {
			t.Fatalf("generic metadata includes tool input key %q in %#v", key, got.Metadata)
		}
	}
	for _, disallowed := range []string{desktopNotifyModeEnv, "@projmux_desktop_notified", "@projmux_desktop_notification_key", "@projmux_desktop_notification_at"} {
		if containsAICommandArg(cmdRecorder(cmd).commands, disallowed) {
			t.Fatalf("commands = %#v, generic notify touched desktop notifier state %q", cmdRecorder(cmd).commands, disallowed)
		}
	}
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	gotLog := out.String()
	if !strings.Contains(gotLog, `"event":"PreToolUse"`) || !strings.Contains(gotLog, `"result":"notify"`) {
		t.Fatalf("log output = %q", gotLog)
	}
}

func TestIngestCodexHookRuntimeNotifySuppressesSendNotiHook(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
		Version: 1,
		Providers: map[string]config.AIHookProviderActions{
			aiHookProviderCodex: {Events: map[string]string{"PostToolUse": aiHookActionNotify}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	store := &stubNotifyStore{}
	runner := &recordingNotifyHookRunner{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{
		store: store,
		ttl:   time.Minute,
		hooks: &sendNotiHookDispatcher{
			runner:    runner,
			lookupEnv: func(string) string { return "" },
			getwd:     func() (string, error) { return t.TempDir(), nil },
		},
	}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "PostToolUse",
		"session_id": "codex-session",
		"turn_id": "turn-456",
		"cwd": "/repo/projmux",
		"tool_name": "Edit",
		"tool_input": {"file_path": "/repo/projmux/internal/app/ai_ingest.go"}
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest codex-hook PostToolUse error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1: %#v", len(store.pushed), store.pushed)
	}
	if got := store.pushed[0].Text; got != "PostToolUse · Edit" {
		t.Fatalf("Text = %q", got)
	}
	if runner.calls != 0 {
		t.Fatalf("send-noti RunAsync call count = %d, want 0", runner.calls)
	}
}

func TestIngestCodexHookCatalogOverrideEventFallsBackToQuiet(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".xdg-config")
	writeCodexTestFile(t, filepath.Join(configHome, "projmux", "ai-hooks.d", "codex.json"), `{
  "provider": "codex",
  "events": [
    { "name": "ExperimentalEvent", "install": true, "action": "quiet" }
  ]
}
`)
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return configHome
		default:
			return ""
		}
	}
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "ExperimentalEvent",
		"session_id": "codex-session",
		"cwd": "/repo/projmux",
		"tool_name": "Bash",
		"tool_input": {"command": "make test"}
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest codex-hook ExperimentalEvent error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
	}
	if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}}) {
		t.Fatalf("commands = %#v, want hook-active mark", cmdRecorder(cmd).commands)
	}
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"event":"ExperimentalEvent"`) || !strings.Contains(got, `"result":"quiet"`) || !strings.Contains(got, `"reason":"catalog quiet event"`) {
		t.Fatalf("log output = %q", got)
	}
}

func TestIngestCodexHookRuntimeQuietAppliesToKnownNotifyEvent(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
		Version: 1,
		Providers: map[string]config.AIHookProviderActions{
			aiHookProviderCodex: {Events: map[string]string{"Stop": aiHookActionQuiet}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "Stop",
		"session_id": "codex-session",
		"turn_id": "turn-456",
		"cwd": "/repo/projmux"
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest codex-hook Stop error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
	}
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"event":"Stop"`) || !strings.Contains(got, `"result":"quiet"`) || !strings.Contains(got, `"reason":"runtime quiet event"`) {
		t.Fatalf("log output = %q", got)
	}
}

func TestAIHookRuntimeActionDoesNotChangeInstallEvents(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
		Version: 1,
		Providers: map[string]config.AIHookProviderActions{
			aiHookProviderCodex: {Events: map[string]string{"Stop": aiHookActionQuiet}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	events, err := cmd.aiHookInstallEvents(aiHookProviderCodex)
	if err != nil {
		t.Fatalf("aiHookInstallEvents error = %v", err)
	}
	if !containsString(events, "Stop") {
		t.Fatalf("install events = %#v, want Stop preserved despite runtime quiet", events)
	}
	if got := cmd.aiHookEffectiveAction(aiHookProviderCodex, "Stop"); got.Action != aiHookActionQuiet || got.Source != aiHookActionSourceRuntime {
		t.Fatalf("effective Stop action = %#v, want runtime quiet", got)
	}
}

func TestIngestCodexHookBlankSessionIDDoesNotRewriteResumeMetadata(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "UserPromptSubmit",
		"cwd": "/repo/projmux",
		"model": "gpt-5.1-codex"
	}`)
	cmd.readCommand = codexHookIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest codex-hook blank session id error = %v", err)
	}
	for _, option := range []string{aiPaneResumeIDOption, aiPaneResumeSourceOption, aiPaneTranscriptPathOption, aiPaneResumeUpdatedAtOption} {
		if hasRecordedAISetOption(cmdRecorder(cmd).commands, option) {
			t.Fatalf("commands = %#v, did not want %s write for blank session id", cmdRecorder(cmd).commands, option)
		}
	}
}

func TestIngestBellPushesQueueEntryAndDedupesPane(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	events := &stubNotifyQueueEvents{publishErr: errors.New("listener unavailable")}
	cmd := testAICommand(home)
	cmd.notifyStore = store
	cmd.events = events
	cmd.now = func() time.Time { return time.Unix(100, 0) }

	lastBellAt := ""
	recorder := cmdRecorder(cmd)
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		recorder.commands = append(recorder.commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "tmux" && reflect.DeepEqual(args, []string{"set-option", "-p", "-t", "%7", aiBellDedupeOption, "100"}) {
			lastBellAt = "100"
		}
		return nil
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", aiBellPaneFormat}):
			return []byte("workspace\\037@1\\037editor\\037%7\\037Claude CLI\\037node\\037/tmp/tmux-1000/projmux\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#{" + aiBellDedupeOption + "}"}):
			return []byte(lastBellAt + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"ingest", "bell", "--pane", "%7"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest bell error = %v", err)
	}
	if err := cmd.Run([]string{"ingest", "bell", "--pane", "%7"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest bell duplicate error = %v", err)
	}

	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	if events.publishCalls != 1 {
		t.Fatalf("publish calls = %d, want one successful queue write event", events.publishCalls)
	}
	got := store.pushed[0]
	if got.ID != "ai:bell:workspace:%7" {
		t.Fatalf("ID = %q", got.ID)
	}
	if got.Text != "bell · Claude CLI" {
		t.Fatalf("Text = %q", got.Text)
	}
	if got.Source != notify.SourceAI || got.Severity != notify.SeverityInfo {
		t.Fatalf("source/severity = %q/%q", got.Source, got.Severity)
	}
	if got.Target.Session != "workspace" || got.Target.Window != "@1" || got.Target.Pane != "%7" || got.Target.Socket != "/tmp/tmux-1000/projmux" {
		t.Fatalf("Target = %+v", got.Target)
	}
	if got.Metadata["agent"] != "bell" || got.Metadata["event"] != "bell" || got.Metadata["pane"] != "%7" || got.Metadata["pane_title"] != "Claude CLI" || got.Metadata["window_name"] != "editor" {
		t.Fatalf("Metadata = %#v", got.Metadata)
	}
	if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiBellDedupeOption, "100"}}) {
		t.Fatalf("commands = %#v, want bell dedupe timestamp", cmdRecorder(cmd).commands)
	}
	if hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}}) {
		t.Fatalf("commands = %#v, did not expect tmux bell fallback to mark pane hook-active", cmdRecorder(cmd).commands)
	}
}

func TestIngestBellRequiresPaneFlag(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	err := cmd.Run([]string{"ingest", "bell"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run ingest bell expected error, got nil")
	}
	if !strings.Contains(err.Error(), "requires --pane") {
		t.Fatalf("error = %v, want pane flag guidance", err)
	}
}

func TestAIIngestLogPrintsTailAndPath(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path, err := cmd.aiIngestLogPath()
	if err != nil {
		t.Fatalf("aiIngestLogPath() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		`{"at":"2026-01-01T00:00:00Z","source":"codex-hook","event":"Stop","result":"notify","pane":"%1"}`,
		`{"at":"2026-01-01T00:00:01Z","source":"claude-hook","event":"SubagentStop","result":"quiet","reason":"high-volume event","pane":"%2"}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--tail", "1"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "claude-hook SubagentStop quiet") || strings.Contains(got, "codex-hook Stop notify") {
		t.Fatalf("tail output = %q", got)
	}
	for modePath, want := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		info, err := os.Stat(modePath)
		if err != nil {
			t.Fatalf("Stat(%q): %v", modePath, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %#o, want %#o", modePath, got, want)
		}
	}

	out.Reset()
	if err := cmd.Run([]string{"ingest", "log", "--path"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --path error = %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != path {
		t.Fatalf("path output = %q, want %q", got, path)
	}
}

func TestAIIngestLogTrimsLargeFile(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path, err := cmd.aiIngestLogPath()
	if err != nil {
		t.Fatalf("aiIngestLogPath() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	oldLine := `{"at":"2026-01-01T00:00:00Z","source":"codex-hook","result":"ignored","reason":"` + strings.Repeat("x", 2048) + `"}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(oldLine, 600)), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: "Stop", Result: "notify"})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > aiIngestLogMaxSize {
		t.Fatalf("log size = %d, want <= %d", info.Size(), aiIngestLogMaxSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[0] != '{' {
		t.Fatalf("trimmed log does not start at a JSON line: %.40q", string(data))
	}
	if !strings.Contains(string(data), `"event":"Stop"`) {
		t.Fatalf("trimmed log missing appended entry")
	}
}

func TestParseClaudeHookPayload(t *testing.T) {
	t.Parallel()

	payload, err := parseClaudeHookPayload([]byte(`{
		"hook_event_name": "PermissionRequest",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"transcript_path": "/tmp/transcript.jsonl",
		"tool_name": "Bash",
		"tool_use_id": "tool-123",
		"tool_input": {"command": "go test ./internal/app", "description": "run focused tests"}
	}`))
	if err != nil {
		t.Fatalf("parseClaudeHookPayload() error = %v", err)
	}
	if payload.EventName != "PermissionRequest" || payload.SessionID != "claude-session" || payload.CWD != "/repo/projmux" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.TranscriptPath != "/tmp/transcript.jsonl" || payload.ToolName != "Bash" || payload.ToolUseID != "tool-123" {
		t.Fatalf("tool fields = %+v", payload)
	}
	if got := stringFromAny(payload.ToolInput["command"]); got != "go test ./internal/app" {
		t.Fatalf("tool input command = %q", got)
	}

	payload, err = parseClaudeHookPayload([]byte(`{
		"hook_event_name": "StopFailure",
		"session-id": "claude-session",
		"workspace": {"path": "/repo/projmux"},
		"error": {"type": "timeout", "message": "tool call exceeded deadline"},
		"subagent": {"kind": "reviewer", "id": "sub-7"},
		"teammate": {"name": "sam", "id": "team-3", "context": "waiting for review"}
	}`))
	if err != nil {
		t.Fatalf("parseClaudeHookPayload() extra error = %v", err)
	}
	if payload.SessionID != "claude-session" || payload.CWD != "/repo/projmux" {
		t.Fatalf("extra payload identity = %+v", payload)
	}
	if payload.ErrorType != "timeout" || payload.ErrorMessage != "tool call exceeded deadline" {
		t.Fatalf("error fields = %+v", payload)
	}
	if payload.SubagentType != "reviewer" || payload.SubagentID != "sub-7" {
		t.Fatalf("subagent fields = %+v", payload)
	}
	if payload.TeammateName != "sam" || payload.TeammateID != "team-3" || payload.TeammateContext != "waiting for review" {
		t.Fatalf("teammate fields = %+v", payload)
	}
}

func TestClaudeTranscriptReaderReturnsLastAssistantText(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join([]string{
		`{"role":"assistant","content":[{"type":"text","text":"first assistant"}]}`,
		`{"role":"user","content":"thanks"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"last assistant"}]}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readClaudeTranscriptLastAssistantText(path); got != "last assistant" {
		t.Fatalf("readClaudeTranscriptLastAssistantText() = %q", got)
	}
	if got := readClaudeTranscriptLastAssistantText(filepath.Join(t.TempDir(), "missing.jsonl")); got != "" {
		t.Fatalf("missing transcript text = %q, want empty fallback", got)
	}
}

func TestAIHookNotifyBodyCatalog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body aiNotifyBody
		want aiNotifyBody
	}{
		{
			name: "codex hook permission bash command",
			body: formatCodexHookPermissionNotifyBody(codexHookPayload{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": "go test ./internal/app"},
			}),
			want: aiNotifyBody{Text: "Bash: go test ./internal/app", Severity: notify.SeverityCritical, Agent: "codex", Category: "approval_required"},
		},
		{
			name: "codex hook stop",
			body: formatCodexHookStopNotifyBody(codexHookPayload{}),
			want: aiNotifyBody{Text: "Ready", Severity: notify.SeverityInfo, Agent: "codex", Category: "response_complete"},
		},
		{
			name: "claude notification permission prompt",
			body: formatClaudeNotificationNotifyBody(claudeHookPayload{
				NotificationType: "permission_prompt",
				Message:          "Approve Bash?",
			}),
			want: aiNotifyBody{Text: "Approve Bash?", Severity: notify.SeverityCritical, Agent: "claude", Category: "approval_required"},
		},
		{
			name: "claude notification idle",
			body: formatClaudeNotificationNotifyBody(claudeHookPayload{
				NotificationType: "idle_prompt",
				Message:          "Waiting for your next request",
			}),
			want: aiNotifyBody{Text: "Waiting for your next request", Severity: notify.SeverityInfo, Agent: "claude", Category: "response_complete"},
		},
		{
			name: "claude permission request bash command",
			body: formatClaudePermissionNotifyBody(claudeHookPayload{
				ToolName:  "Bash",
				ToolUseID: "tool-123",
				ToolInput: map[string]any{"command": "rm -rf /tmp/old-cache"},
			}),
			want: aiNotifyBody{Text: "Bash: rm -rf /tmp/old-cache", Severity: notify.SeverityCritical, Agent: "claude", Category: "approval_required"},
		},
		{
			name: "claude stop transcript summary",
			body: formatClaudeStopNotifyBody("implemented and verified"),
			want: aiNotifyBody{Text: "implemented and verified", Severity: notify.SeverityInfo, Agent: "claude", Category: "response_complete"},
		},
		{
			name: "claude stop failure error labels",
			body: formatClaudeStopFailureNotifyBody(claudeHookPayload{
				ErrorType:    "timeout",
				ErrorMessage: "tool call exceeded deadline",
			}),
			want: aiNotifyBody{Text: "timeout · tool call exceeded deadline", Severity: notify.SeverityCritical, Agent: "claude", Category: "error"},
		},
		{
			name: "claude subagent stop labels",
			body: formatClaudeSubagentStopNotifyBody(claudeHookPayload{
				SubagentType: "reviewer",
				SubagentID:   "sub-7",
			}),
			want: aiNotifyBody{Text: "reviewer · sub-7", Severity: notify.SeverityInfo, Agent: "claude", Category: "subagent_stopped"},
		},
		{
			name: "claude teammate idle labels",
			body: formatClaudeTeammateIdleNotifyBody(claudeHookPayload{
				TeammateName:    "sam",
				TeammateID:      "team-3",
				TeammateContext: "waiting for review",
			}),
			want: aiNotifyBody{Text: "sam · team-3 · waiting for review", Severity: notify.SeverityInfo, Agent: "claude", Category: "teammate_waiting"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.body != tc.want {
				t.Fatalf("body = %#v, want %#v", tc.body, tc.want)
			}
		})
	}
}

func TestAIHookRenderedNotifyTextLocalizesCategoryAndPreservesLiterals(t *testing.T) {
	t.Parallel()

	stop := formatCodexHookStopNotifyBody(codexHookPayload{})
	stopMetadata := mergeAINotifyBodyMetadata(nil, stop)
	if got, want := renderAINotifyText(stop.Text, stopMetadata, i18n.FallbackLocale).Summary, "Codex · Response complete"; got != want {
		t.Fatalf("en-US stop summary = %q, want %q", got, want)
	}
	if got, want := renderAINotifyText(stop.Text, stopMetadata, i18n.Locale("ko-KR")).Summary, "Codex · 응답 완료"; got != want {
		t.Fatalf("ko-KR stop summary = %q, want %q", got, want)
	}

	permission := formatCodexHookPermissionNotifyBody(codexHookPayload{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "go test ./internal/app"},
	})
	rendered := renderAINotifyText(permission.Text, mergeAINotifyBodyMetadata(nil, permission), i18n.Locale("ko-KR"))
	for _, want := range []string{"Codex", "승인 필요", "Bash", "go test ./internal/app"} {
		if !strings.Contains(rendered.Full, want) {
			t.Fatalf("rendered permission = %q, want literal/category %q", rendered.Full, want)
		}
	}
}

func TestClaudeHookRenderedNotifyTextCatalogCoveragePreservesPayloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body aiNotifyBody
		want []string
	}{
		{
			name: "permission notification message",
			body: formatClaudeNotificationNotifyBody(claudeHookPayload{
				NotificationType: "permission_prompt",
				Message:          "Approve Bash?",
			}),
			want: []string{"Claude", "승인 필요", "Approve Bash?"},
		},
		{
			name: "idle notification message",
			body: formatClaudeNotificationNotifyBody(claudeHookPayload{
				NotificationType: "idle_prompt",
				Message:          "Waiting for your next request",
			}),
			want: []string{"Claude", "응답 완료", "Waiting for your next request"},
		},
		{
			name: "stop transcript body",
			body: formatClaudeStopNotifyBody("implemented and verified"),
			want: []string{"Claude", "응답 완료", "implemented and verified"},
		},
		{
			name: "stop failure error",
			body: formatClaudeStopFailureNotifyBody(claudeHookPayload{
				ErrorType:    "timeout",
				ErrorMessage: "tool call exceeded deadline",
			}),
			want: []string{"Claude", "오류", "timeout", "tool call exceeded deadline"},
		},
		{
			name: "subagent stopped",
			body: formatClaudeSubagentStopNotifyBody(claudeHookPayload{
				SubagentType: "reviewer",
				SubagentID:   "sub-7",
			}),
			want: []string{"Claude", "서브에이전트 종료", "reviewer", "sub-7"},
		},
		{
			name: "teammate waiting",
			body: formatClaudeTeammateIdleNotifyBody(claudeHookPayload{
				TeammateName:    "sam",
				TeammateID:      "team-3",
				TeammateContext: "waiting for review",
			}),
			want: []string{"Claude", "팀메이트 대기", "sam", "team-3", "waiting for review"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderAINotifyText(tc.body.Text, mergeAINotifyBodyMetadata(nil, tc.body), i18n.Locale("ko-KR"))
			for _, want := range tc.want {
				if !strings.Contains(rendered.Full, want) {
					t.Fatalf("rendered = %q, want %q", rendered.Full, want)
				}
			}
		})
	}
}

func TestAIHookDesktopNotificationUsesQueueTextPayload(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case desktopNotifyModeEnv:
			return "notify"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#S"}):
			return []byte("workspace\n"), nil
		case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#W"}):
			return []byte("editor\n"), nil
		case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#{pane_current_path}"}):
			return []byte("/repo/projmux\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo/projmux", "rev-parse", "--is-inside-work-tree"}):
			return []byte("true\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo/projmux", "symbolic-ref", "--quiet", "--short", "HEAD"}):
			return []byte("feat/hooks\n"), nil
		case name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}):
			return []byte("/usr/bin/notify-send\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	text := "Bash: go test ./internal/app"
	metadata := map[string]string{"agent": "codex", "category": "approval_required"}
	notification := cmd.aiTextNotificationWithMetadata("%7", text, notify.SeverityCritical, metadata)
	if notification.Summary != "Codex · Approval required" {
		t.Fatalf("Summary = %q", notification.Summary)
	}
	if notification.Urgency != "normal" {
		t.Fatalf("Urgency = %q, want OS urgency normal for critical queue text", notification.Urgency)
	}
	if !strings.Contains(notification.Body, text) || !strings.Contains(notification.Body, "projmux/feat/hooks") || strings.Contains(notification.Body, "workspace:editor") {
		t.Fatalf("Body = %q, want actionable text and project context only", notification.Body)
	}
	queueText := notifyQueueDisplayText(notify.Notification{Text: text, Source: notify.SourceAI, Metadata: metadata}, i18n.FallbackLocale)
	if queueText != "Codex · Approval required · Bash: go test ./internal/app" || !strings.Contains(queueText, text) {
		t.Fatalf("queue text = %q, want shared rendered category + literal body", queueText)
	}

	if err := cmd.notifyAITextWithMetadata("%7", text, notify.SeverityCritical, true, metadata); err != nil {
		t.Fatalf("notifyAIText error = %v", err)
	}
	var notifySend recordedAICommand
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			notifySend = command
			break
		}
	}
	if notifySend.name == "" {
		t.Fatalf("commands = %#v, want notify-send dispatch", cmdRecorder(cmd).commands)
	}
	if !reflect.DeepEqual(notifySend.args[len(notifySend.args)-2:], []string{notification.Summary, notification.Body}) {
		t.Fatalf("notify-send args = %#v, want summary/body from shared payload", notifySend.args)
	}
	for _, want := range []string{"--urgency=normal", "--expire-time=5000"} {
		found := slices.Contains(notifySend.args, want)
		if !found {
			t.Fatalf("notify-send args = %#v, want %q", notifySend.args, want)
		}
	}
	toastScript := buildToastPowerShell(notification.Summary, notification.Body, notification.AppName, notification.Tag, notification.Group, "", "", defaultAINotifyExpireMS)
	if !strings.Contains(toastScript, text) || !strings.Contains(toastScript, notification.Body) {
		t.Fatalf("toast script missing shared payload:\n%s", toastScript)
	}
}

func TestIngestClaudeUserPromptSetsThinkingWithoutQueue(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "UserPromptSubmit",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"prompt": "implement claude hook ingest"
	}`)
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX_PANE" {
			return "%9"
		}
		return ""
	}

	err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run ingest claude-hook UserPromptSubmit error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0", len(store.pushed))
	}
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%9", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeClaude}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%9", aiPaneStateOption, "thinking"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%9", aiPaneBadgeKindOption, aiBadgeKindInProgress}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%9", attentionStateOption, attentionStateBusy}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
	assertNoAIPaneTopicWrite(t, cmdRecorder(cmd).commands)
}

func TestIngestClaudePermissionPushesCriticalQueueEntryAndHookMarker(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "PermissionRequest",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"transcript_path": "/tmp/claude-transcript.jsonl",
		"tool_name": "Bash",
		"tool_use_id": "tool-123",
		"tool_input": {"command": "go test ./internal/app"}
	}`)
	cmd.readCommand = claudeIngestReadCommand("%7")

	err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run ingest claude-hook PermissionRequest error = %v", err)
	}
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneSessionIDOption, "claude-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneResumeIDOption, "claude-session"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneResumeSourceOption, "hook"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneTranscriptPathOption, "/tmp/claude-transcript.jsonl"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneResumeUpdatedAtOption, "1970-01-01T00:00:00Z"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneStateOption, "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneBadgeKindOption, aiBadgeKindApprovalRequired}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:claude:permission:claude-session:tool-123" {
		t.Fatalf("ID = %q", got.ID)
	}
	if got.Text != "Bash: go test ./internal/app" {
		t.Fatalf("Text = %q", got.Text)
	}
	if got.Severity != notify.SeverityCritical {
		t.Fatalf("Severity = %q", got.Severity)
	}
	if got.Metadata["agent"] != "claude" || got.Metadata["category"] != "approval_required" || got.Metadata["event"] != "PermissionRequest" || got.Metadata["tool_input.command"] != "go test ./internal/app" {
		t.Fatalf("Metadata = %#v", got.Metadata)
	}
	assertNoAIPaneTopicWrite(t, cmdRecorder(cmd).commands)
}

func TestIngestClaudeStopUsesTranscriptOrGenericFallback(t *testing.T) {
	home := t.TempDir()
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"role":"assistant","content":[{"type":"text","text":"implemented and verified"}]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "Stop",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"transcript_path": "` + transcript + `"
	}`)
	cmd.readCommand = claudeIngestReadCommand("%7")
	if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest claude-hook Stop error = %v", err)
	}
	if len(store.pushed) != 1 || store.pushed[0].Text != "implemented and verified" || store.pushed[0].Metadata["category"] != "response_complete" {
		t.Fatalf("pushed = %#v", store.pushed)
	}
	if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneBadgeKindOption, aiBadgeKindResponseComplete}}) {
		t.Fatalf("commands = %#v, want response_complete semantic badge", cmdRecorder(cmd).commands)
	}

	store = &stubNotifyStore{}
	cmd = testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "Stop",
		"session_id": "claude-session",
		"cwd": "/repo/projmux"
	}`)
	cmd.readCommand = claudeIngestReadCommand("%7")
	if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest claude-hook Stop without transcript error = %v", err)
	}
	if len(store.pushed) != 1 || store.pushed[0].Text != "Ready" || store.pushed[0].Metadata["category"] != "response_complete" {
		t.Fatalf("fallback pushed = %#v", store.pushed)
	}
	if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneBadgeKindOption, aiBadgeKindResponseComplete}}) {
		t.Fatalf("fallback commands = %#v, want response_complete semantic badge", cmdRecorder(cmd).commands)
	}
}

func TestIngestClaudeNotificationMapsInputReady(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "Notification",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"notification_type": "elicitation_dialog",
		"message": "Need deployment target"
	}`)
	cmd.readCommand = claudeIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest claude-hook Notification error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.Text != "Need deployment target" || got.Severity != notify.SeverityCritical || got.Metadata["category"] != "input_required" {
		t.Fatalf("pushed = %#v", got)
	}
	if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneBadgeKindOption, aiBadgeKindInputRequired}}) {
		t.Fatalf("commands = %#v, want input_required semantic badge", cmdRecorder(cmd).commands)
	}
}

func TestIngestClaudeExtraEvents(t *testing.T) {
	tests := []struct {
		name         string
		payload      string
		wantID       string
		wantText     string
		wantSeverity string
		wantPush     bool
		wantMetadata map[string]string
	}{
		{
			name: "stop failure",
			payload: `{
				"hook_event_name": "StopFailure",
				"session_id": "claude-session",
				"cwd": "/repo/projmux",
				"error_type": "timeout",
				"error_message": "tool call exceeded deadline"
			}`,
			wantID:       "ai:claude:stop-failure:claude-session:timeout:tool call exceeded deadline",
			wantText:     "timeout · tool call exceeded deadline",
			wantSeverity: notify.SeverityCritical,
			wantPush:     true,
			wantMetadata: map[string]string{
				"event":         "StopFailure",
				"error_type":    "timeout",
				"error_message": "tool call exceeded deadline",
			},
		},
		{
			name: "subagent stop",
			payload: `{
				"hook_event_name": "SubagentStop",
				"session_id": "claude-session",
				"cwd": "/repo/projmux",
				"subagent": {"type": "reviewer", "id": "sub-7"}
			}`,
			wantPush: false,
		},
		{
			name: "teammate idle",
			payload: `{
				"hook_event_name": "TeammateIdle",
				"session_id": "claude-session",
				"cwd": "/repo/projmux",
				"teammate": {"name": "sam", "id": "team-3", "context": "waiting for review"}
			}`,
			wantID:       "ai:claude:teammate-idle:claude-session:sam:team-3:waiting for review",
			wantText:     "sam · team-3 · waiting for review",
			wantSeverity: notify.SeverityInfo,
			wantPush:     true,
			wantMetadata: map[string]string{
				"event":            "TeammateIdle",
				"teammate_name":    "sam",
				"teammate_id":      "team-3",
				"teammate_context": "waiting for review",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			store := &stubNotifyStore{}
			cmd := testAICommand(home)
			cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
			cmd.stdin = strings.NewReader(tc.payload)
			cmd.readCommand = claudeIngestReadCommand("%7")

			if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run ingest claude-hook error = %v", err)
			}
			if !tc.wantPush {
				if len(store.pushed) != 0 {
					t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
				}
				return
			}
			if len(store.pushed) != 1 {
				t.Fatalf("push count = %d, want 1", len(store.pushed))
			}
			got := store.pushed[0]
			if got.ID != tc.wantID || got.Text != tc.wantText || got.Severity != tc.wantSeverity {
				t.Fatalf("pushed = %#v", got)
			}
			if got.Metadata["agent"] != "claude" || got.Metadata["session_id"] != "claude-session" || got.Metadata["cwd"] != "/repo/projmux" {
				t.Fatalf("base metadata = %#v", got.Metadata)
			}
			for key, want := range tc.wantMetadata {
				if got.Metadata[key] != want {
					t.Fatalf("metadata[%s] = %q, want %q in %#v", key, got.Metadata[key], want, got.Metadata)
				}
			}
		})
	}
}

func TestIngestClaudeSubagentStopLogsQuietWithoutNotify(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "SubagentStop",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"subagent": {"type": "reviewer", "id": "sub-7"}
	}`)
	cmd.readCommand = claudeIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest claude-hook SubagentStop error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
	}
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"source":"claude-hook"`) || !strings.Contains(got, `"event":"SubagentStop"`) || !strings.Contains(got, `"result":"quiet"`) {
		t.Fatalf("log output = %q", got)
	}
}

func TestIngestClaudeRuntimeNotifyAppliesToKnownQuietEvent(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	if err := config.SaveAIHookActionsFile(paths.AIHookActionsFile(), config.AIHookActionsFile{
		Version: 1,
		Providers: map[string]config.AIHookProviderActions{
			aiHookProviderClaude: {Events: map[string]string{"SubagentStop": aiHookActionNotify}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "SubagentStop",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"subagent": {"type": "reviewer", "id": "sub-7"}
	}`)
	cmd.readCommand = claudeIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest claude-hook SubagentStop error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	if got := store.pushed[0].Text; got != "reviewer · sub-7" {
		t.Fatalf("Text = %q", got)
	}
}

func TestIngestClaudeQuietEventsMarkPaneAndLogWithoutNotify(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "PreToolUse",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"tool_name": "Bash",
		"tool_input": {"command": "go test ./internal/app"}
	}`)
	cmd.readCommand = claudeIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest claude-hook PreToolUse error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
	}
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneAgentOption, aiModeClaude}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
	assertNoAIPaneTopicWrite(t, cmdRecorder(cmd).commands)
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"source":"claude-hook"`) || !strings.Contains(got, `"event":"PreToolUse"`) || !strings.Contains(got, `"result":"quiet"`) {
		t.Fatalf("log output = %q", got)
	}
}

func TestIngestClaudeUnknownEventFallsBackToQuiet(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"hook_event_name": "FutureClaudeEvent",
		"session_id": "claude-session",
		"cwd": "/repo/projmux",
		"tool_name": "Bash",
		"tool_input": {"command": "make test"}
	}`)
	cmd.readCommand = claudeIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "claude-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest claude-hook FutureClaudeEvent error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
	}
	if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}}) {
		t.Fatalf("commands = %#v, want hook-active mark", cmdRecorder(cmd).commands)
	}
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"event":"FutureClaudeEvent"`) || !strings.Contains(got, `"result":"quiet"`) || !strings.Contains(got, `"reason":"unknown event"`) {
		t.Fatalf("log output = %q", got)
	}
}

func TestParseAntigravityHookPayloadObservedFields(t *testing.T) {
	t.Parallel()

	payload, err := parseAntigravityHookPayload([]byte(`{
		"eventName": "Stop",
		"conversationId": "ag-conv-123",
		"workspace": {"path": "/repo/projmux"},
		"transcriptPath": "/tmp/ag.jsonl",
		"terminationReason": "completed",
		"fullyIdle": true,
		"statusline": {
			"agent_state": "idle",
			"tool_confirmation_pending": false,
			"context_window": "42%"
		}
	}`), "")
	if err != nil {
		t.Fatalf("parseAntigravityHookPayload() error = %v", err)
	}
	if payload.EventName != "Stop" || payload.ConversationID != "ag-conv-123" || payload.CWD != "/repo/projmux" || payload.TranscriptPath != "/tmp/ag.jsonl" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.TerminationReason != "completed" || !payload.FullyIdle || !payload.FullyIdleSet || payload.ToolConfirmationPending || payload.AgentState != "idle" || payload.ContextWindow != "42%" {
		t.Fatalf("observed fields = %+v", payload)
	}

	payload, err = parseAntigravityHookPayload([]byte(`{"conversation_id":"ag-conv-123","tool_confirmation_pending":true}`), "Statusline")
	if err != nil {
		t.Fatalf("parseAntigravityHookPayload() statusline-only error = %v", err)
	}
	if payload.EventName != "Statusline" || !payload.ToolConfirmationPending {
		t.Fatalf("statusline payload = %+v, want Statusline approval signal", payload)
	}

	payload, err = parseAntigravityHookPayload([]byte(`{"conversation_id":"ag-conv-123","agent_state":"idle","context_window":"38%"}`), "")
	if err != nil {
		t.Fatalf("parseAntigravityHookPayload() statusline-shaped error = %v", err)
	}
	if payload.EventName != "Unknown" || payload.ToolConfirmationPending || payload.ToolConfirmationPendingSet {
		t.Fatalf("statusline-shaped payload = %+v, want Unknown without an explicit event or payload alias", payload)
	}
	if metadata := payload.antigravityMetadata(); metadata["tool_confirmation_pending"] != "" {
		t.Fatalf("metadata = %#v, want absent tool_confirmation_pending when field was absent", metadata)
	}

	payload, err = parseAntigravityHookPayload([]byte(`{"conversation_id":"ag-conv-123","toolConfirmationPending":false}`), "Statusline")
	if err != nil {
		t.Fatalf("parseAntigravityHookPayload() explicit false statusline error = %v", err)
	}
	if payload.EventName != "Statusline" || payload.ToolConfirmationPending || !payload.ToolConfirmationPendingSet {
		t.Fatalf("explicit false statusline payload = %+v, want Statusline with pending=false", payload)
	}
}

func TestParseAntigravityStatusLineV1112OfficialFixture(t *testing.T) {
	t.Parallel()
	// Authority: https://antigravity.google/docs/cli/statusline, versioned as
	// Antigravity CLI v1.1.12 when this fixture was captured.
	data, err := os.ReadFile(filepath.Join("testdata", "antigravity", "statusline_v1_1_12.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseAntigravityHookPayload(data, "Statusline")
	if err != nil {
		t.Fatal(err)
	}
	if got.CWD != "/workspace/sanitized-project" || got.ConversationID != "123e4567-e89b-12d3-a456-426614174000" || got.TranscriptPath != "/sanitized/transcript.jsonl" {
		t.Fatalf("identity = %+v", got)
	}
	if got.AgentState != "tool_use" || !got.ToolConfirmationPending || !got.ToolConfirmationPendingSet {
		t.Fatalf("attention fields = %+v", got)
	}
	if !got.ContextUsedPercentageSet || got.ContextUsedPercentage != 14.24 || !got.ContextRemainingPercentSet || got.ContextRemainingPercentage != 85.76 || got.ContextTotalInputTokens != 88244 || got.ContextTotalOutputTokens != 61074 || got.ContextWindowSize != 1048576 || got.ContextCurrentInputTokens != 63382 || got.ContextCurrentOutputTokens != 346 || got.ContextCacheReadTokens != 20857 {
		t.Fatalf("context fields = %+v", got)
	}
	if !got.QuotaSet || len(got.QuotaBuckets) != 1 {
		t.Fatalf("quota fields = %+v", got)
	}
	bucket := got.QuotaBuckets[0]
	if bucket.ID != "gemini-weekly" || bucket.RemainingFraction != 0.9378 || !bucket.ResetTime.Equal(time.Date(2026, 7, 6, 7, 50, 32, 0, time.UTC)) || bucket.ResetInSeconds == nil || *bucket.ResetInSeconds != 560580 {
		t.Fatalf("quota bucket = %+v", bucket)
	}
}

func TestParseAntigravityQuotaBucketsRejectsInvalidAndSortsOpaqueIDs(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"quota": {
			"z-new": {"remaining_fraction": 0.25, "reset_time": "2026-08-13T00:00:00Z"},
			"context": {"remaining_fraction": 1, "reset_time": "2026-08-14T00:00:00Z", "reset_in_seconds": 0},
			"too-high": {"remaining_fraction": 1.01, "reset_time": "2026-08-13T00:00:00Z"},
			"negative": {"remaining_fraction": -0.1, "reset_time": "2026-08-13T00:00:00Z"},
			"disabled": null,
			"missing": {"reset_time": "2026-08-13T00:00:00Z"},
			"bad-relative": {"remaining_fraction": 0.5, "reset_in_seconds": -1}
		}
	}`)
	got, err := parseAntigravityHookPayload(data, "Statusline")
	if err != nil {
		t.Fatal(err)
	}
	if !got.QuotaSet || len(got.QuotaBuckets) != 2 {
		t.Fatalf("quota = set:%v buckets:%#v, want two valid buckets", got.QuotaSet, got.QuotaBuckets)
	}
	if got.QuotaBuckets[0].ID != "context" || got.QuotaBuckets[1].ID != "z-new" {
		t.Fatalf("bucket order = %#v, want exact lexical IDs", got.QuotaBuckets)
	}
	if got.QuotaBuckets[0].ResetInSeconds == nil || *got.QuotaBuckets[0].ResetInSeconds != 0 {
		t.Fatalf("explicit reset_in_seconds zero was not preserved: %#v", got.QuotaBuckets[0])
	}
	if got.QuotaBuckets[1].ResetInSeconds != nil {
		t.Fatalf("absent reset_in_seconds became present: %#v", got.QuotaBuckets[1])
	}
}

func TestParseAntigravityQuotaMissingNullAndEmpty(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload string
		set     bool
	}{
		{name: "missing", payload: `{}`, set: false},
		{name: "null", payload: `{"quota":null}`, set: true},
		{name: "empty", payload: `{"quota":{}}`, set: true},
		{name: "disabled non-object", payload: `{"quota":"disabled"}`, set: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAntigravityHookPayload([]byte(tc.payload), "Statusline")
			if err != nil {
				t.Fatal(err)
			}
			if got.QuotaSet != tc.set || len(got.QuotaBuckets) != 0 {
				t.Fatalf("payload %s => set=%v buckets=%#v", tc.payload, got.QuotaSet, got.QuotaBuckets)
			}
		})
	}
}

func TestParseAntigravityHookPayloadV1112FixturesWithExplicitEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		file  string
		event string
		check func(*testing.T, antigravityHookPayload)
	}{
		{
			name:  "pre invocation common and invocation fields",
			file:  "pre_invocation_v1_1_12.json",
			event: "PreInvocation",
			check: func(t *testing.T, got antigravityHookPayload) {
				if got.CWD != "/workspace/sanitized-project" || !reflect.DeepEqual(got.WorkspacePaths, []string{"/workspace/sanitized-project", "/workspace/ignored-second"}) {
					t.Fatalf("workspace fields = %#v, cwd %q", got.WorkspacePaths, got.CWD)
				}
				if got.InvocationNum != 0 || !got.InvocationNumSet || got.InitialNumSteps != 1 || !got.InitialNumStepsSet {
					t.Fatalf("invocation fields = %+v", got)
				}
			},
		},
		{
			name:  "post tool fields",
			file:  "post_tool_use_v1_1_12.json",
			event: "PostToolUse",
			check: func(t *testing.T, got antigravityHookPayload) {
				if got.StepIdx != 5 || !got.StepIdxSet || got.Error != "sanitized tool failure" || firstString(got.ToolCall, "name") != "read_file" {
					t.Fatalf("post tool fields = %+v", got)
				}
			},
		},
		{
			name:  "stop fields with empty workspace",
			file:  "stop_no_tool_call_v1_1_12.json",
			event: "Stop",
			check: func(t *testing.T, got antigravityHookPayload) {
				if got.CWD != "" || len(got.WorkspacePaths) != 0 || got.ExecutionNum != 0 || !got.ExecutionNumSet || got.TerminationReason != "NO_TOOL_CALL" || !got.FullyIdle {
					t.Fatalf("stop fields = %+v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "antigravity", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseAntigravityHookPayload(data, tt.event)
			if err != nil {
				t.Fatal(err)
			}
			if got.EventName != tt.event {
				t.Fatalf("event = %q, want %q", got.EventName, tt.event)
			}
			if got.ConversationID == "" || got.TranscriptPath == "" || got.ArtifactDirectoryPath == "" || got.ModelName != "sanitized-model" {
				t.Fatalf("common fields = %+v", got)
			}
			tt.check(t, got)
		})
	}
}

func TestParseAntigravityHookPayloadExplicitEventPrecedesLegacyAlias(t *testing.T) {
	t.Parallel()

	got, err := parseAntigravityHookPayload([]byte(`{"eventName":"Stop","conversationId":"sanitized"}`), "post_tool_use")
	if err != nil {
		t.Fatal(err)
	}
	if got.EventName != "PostToolUse" {
		t.Fatalf("event = %q, want authoritative PostToolUse", got.EventName)
	}

	legacy, err := parseAntigravityHookPayload([]byte(`{"eventName":"Stop","conversationId":"sanitized"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.EventName != "Stop" {
		t.Fatalf("legacy event = %q, want payload fallback Stop", legacy.EventName)
	}
}

func TestParseAntigravityHookPayloadWorkspacePathsAbsentAndEmptyDegrade(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`{}`, `{"workspacePaths":[]}`, `{"workspacePaths":["", "  "]}`} {
		got, err := parseAntigravityHookPayload([]byte(raw), "Stop")
		if err != nil {
			t.Fatal(err)
		}
		if got.CWD != "" || len(got.WorkspacePaths) != 0 {
			t.Fatalf("payload for %s = %+v, want no invented cwd", raw, got)
		}
	}
}

func TestFormatAntigravityStopExplicitErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reason   string
		error    string
		severity string
		category string
		class    string
	}{
		{name: "no tool call", reason: "NO_TOOL_CALL", severity: notify.SeverityInfo, category: "response_complete", class: "completion"},
		{name: "model stop", reason: "MODEL_STOP", severity: notify.SeverityInfo, category: "response_complete", class: "completion"},
		{name: "explicit error text", reason: "MODEL_STOP", error: "sanitized failure", severity: notify.SeverityCritical, category: "error", class: "error"},
		{name: "error reason", reason: "ERROR", severity: notify.SeverityCritical, category: "error", class: "error"},
		{name: "max steps family", reason: "MAX_STEPS_EXCEEDED_RETRY", severity: notify.SeverityCritical, category: "error", class: "error"},
		{name: "unknown is not critical", reason: "FUTURE_SAFE_REASON", severity: notify.SeverityInfo, category: "response_complete", class: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := antigravityHookPayload{TerminationReason: tt.reason, Error: tt.error}
			body := formatAntigravityStopNotifyBody(payload)
			if body.Severity != tt.severity || body.Category != tt.category {
				t.Fatalf("body = %#v, want severity %q category %q", body, tt.severity, tt.category)
			}
			if got := antigravityTerminationClassification(payload); got != tt.class {
				t.Fatalf("classification = %q, want %q", got, tt.class)
			}
		})
	}
}

func TestFormatAntigravityHookResponseJSON(t *testing.T) {
	t.Parallel()

	for _, event := range []string{"PreInvocation", "PostInvocation", "PostToolUse", "FutureEvent"} {
		response, err := antigravityHookResponse(event)
		if err != nil {
			t.Fatalf("response for %s: %v", event, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(response, &decoded); err != nil || len(decoded) != 0 {
			t.Fatalf("response for %s = %s, err = %v; want {}", event, response, err)
		}
	}
	response, err := antigravityHookResponse("Stop")
	if err != nil {
		t.Fatal(err)
	}
	var stop map[string]any
	if err := json.Unmarshal(response, &stop); err != nil || stop["decision"] != "stop" {
		t.Fatalf("Stop response = %s, decoded %#v, err %v", response, stop, err)
	}
	if _, err := antigravityHookResponse("PreToolUse"); err == nil {
		t.Fatal("PreToolUse response must not invent a permission decision")
	}
}

func TestAntigravityV1112HookCatalogHasOfficialFiveEvents(t *testing.T) {
	t.Parallel()

	catalog, err := defaultAIHookCatalog(aiHookProviderAntigravity)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PreToolUse", "PostToolUse", "PreInvocation", "PostInvocation", "Stop"}
	got := make([]string, 0, len(catalog.Events))
	for _, event := range catalog.Events {
		got = append(got, event.Name)
		wantInstall := event.Name != "PreToolUse"
		if event.Install != wantInstall {
			t.Fatalf("event %s install = %t, want %t", event.Name, event.Install, wantInstall)
		}
	}
	if !reflect.DeepEqual(got, want) || catalog.ObservedVersion != "Antigravity CLI 1.1.12" {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestIngestAntigravityExplicitEventUsesTmuxPaneAndWritesHookResponse(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX_PANE" {
			return "%inherited"
		}
		return ""
	}
	data, err := os.ReadFile(filepath.Join("testdata", "antigravity", "stop_no_tool_call_v1_1_12.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.stdin = bytes.NewReader(data)
	readCommand := antigravityIngestReadCommand("%inherited")
	cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
			t.Fatal("inherited TMUX_PANE must precede workspace matching")
		}
		return readCommand(ctx, name, args...)
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Stop"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "{\"decision\":\"stop\"}\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if len(store.pushed) != 1 || store.pushed[0].Severity != notify.SeverityInfo || store.pushed[0].Metadata[notify.MetaCategory] != "response_complete" {
		t.Fatalf("pushed = %#v, commands = %#v, stdout = %q", store.pushed, cmdRecorder(cmd).commands, stdout.String())
	}
	if !hasRecordedAICommand(cmdRecorder(cmd).commands, recordedAICommand{name: "tmux", args: []string{"set-option", "-p", "-t", "%inherited", aiPaneHookActiveOption, "1"}}) {
		t.Fatalf("commands = %#v, want inherited pane", cmdRecorder(cmd).commands)
	}
}

func TestIngestAntigravityRawV1112FixtureRoutesExplicitPreInvocationByWorkspace(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	data, err := os.ReadFile(filepath.Join("testdata", "antigravity", "pre_invocation_v1_1_12.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.stdin = bytes.NewReader(data)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-a", "-F", aiIngestListPanesFormat}) {
			return []byte("%workspace\x1f/workspace/sanitized-project\x1f\x1f\n"), nil
		}
		return nil, os.ErrNotExist
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "PreInvocation"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "{}\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%workspace", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%workspace", aiPaneContextOption, "/workspace/sanitized-project"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%workspace", aiPaneStateOption, "thinking"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%workspace", aiPaneBadgeKindOption, aiBadgeKindInProgress}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%workspace", attentionStateOption, attentionStateBusy}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
}

func TestIngestAntigravityPostToolUseRetainsErrorDiagnosticAndStaysQuiet(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{"conversationId":"ag-conv","cwd":"/repo/projmux","error":"sanitized tool failure"}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "PostToolUse"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "{}\n" || len(store.pushed) != 0 {
		t.Fatalf("stdout = %q, pushes = %#v; want quiet {} response", stdout.String(), store.pushed)
	}
	var log bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &log, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"event":"PostToolUse"`, `"result":"quiet"`, `tool error: sanitized tool failure`} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("log = %q, want %q", log.String(), want)
		}
	}
}

func TestIngestAntigravityUnknownExplicitEventSafelyDegrades(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX_PANE" {
			return "%7"
		}
		return ""
	}
	cmd.stdin = strings.NewReader(`{"eventName":"Stop","conversationId":"sanitized"}`)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "FutureEvent"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "{}\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	var log bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &log, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), `"event":"FutureEvent"`) || !strings.Contains(log.String(), `"reason":"unknown event"`) {
		t.Fatalf("log = %q", log.String())
	}
}

func TestIngestAntigravityStopUnknownReasonStaysInfoWithDiagnostic(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"conversationId": "123e4567-e89b-12d3-a456-426614174099",
		"workspacePaths": ["/repo/projmux"],
		"terminationReason": "FUTURE_SAFE_REASON",
		"error": "",
		"fullyIdle": true
	}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Stop"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "{\"decision\":\"stop\"}\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if len(store.pushed) != 1 {
		t.Fatalf("pushed = %#v, want one completion", store.pushed)
	}
	got := store.pushed[0]
	if got.Severity != notify.SeverityInfo || got.Metadata[notify.MetaCategory] != "response_complete" || got.Metadata["termination_class"] != "unknown" {
		t.Fatalf("pushed = %#v, want info response_complete with unknown classification", got)
	}
	var log bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &log, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), `"result":"notify"`) || !strings.Contains(log.String(), `"reason":"unknown termination reason: FUTURE_SAFE_REASON"`) {
		t.Fatalf("log = %q, want noncritical unknown-reason diagnostic", log.String())
	}
}

func TestIngestAntigravityStopPushesCompletionMetadataAndResumeState(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	conversationID := "123e4567-e89b-12d3-a456-426614174000"
	cmd.stdin = strings.NewReader(`{
		"eventName": "Stop",
		"conversationId": "` + conversationID + `",
		"cwd": "/repo/projmux",
		"transcriptPath": "/tmp/ag.jsonl",
		"terminationReason": "completed",
		"fullyIdle": true
	}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "antigravity-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest antigravity-hook Stop error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:antigravity:stop:"+conversationID || got.Text != "Ready" || got.Severity != notify.SeverityInfo {
		t.Fatalf("pushed = %#v", got)
	}
	for key, want := range map[string]string{
		"agent":              "antigravity",
		"event":              "Stop",
		"conversation_id":    conversationID,
		"cwd":                "/repo/projmux",
		"transcript_path":    "/tmp/ag.jsonl",
		"termination_reason": "completed",
		"fully_idle":         "true",
		"category":           "response_complete",
	} {
		if got.Metadata[key] != want {
			t.Fatalf("metadata[%s] = %q, want %q in %#v", key, got.Metadata[key], want, got.Metadata)
		}
	}
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneAgentOption, aiModeAntigravity}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneThreadIDOption, conversationID}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneSessionIDOption, conversationID}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneResumeIDOption, conversationID}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneResumeSourceOption, "hook"}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
	if !hasRecordedAISetOption(cmdRecorder(cmd).commands, aiPaneResumeUpdatedAtOption) {
		t.Fatalf("commands = %#v, want Antigravity resume updated timestamp", cmdRecorder(cmd).commands)
	}
}

func TestIngestAntigravityStopIgnoresToolConfirmationPendingForApproval(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"eventName": "Stop",
		"conversationId": "ag-conv-123",
		"cwd": "/repo/projmux",
		"tool_confirmation_pending": true,
		"terminationReason": "completed"
	}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "antigravity-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest antigravity-hook Stop error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:antigravity:stop:ag-conv-123" || got.Metadata["category"] != "response_complete" || got.Severity != notify.SeverityInfo {
		t.Fatalf("pushed = %#v, want Stop completion mapping", got)
	}
}

func TestIngestAntigravityStopErrorPushesCritical(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"eventName": "Stop",
		"conversationId": "ag-conv-123",
		"cwd": "/repo/projmux",
		"terminationReason": "error",
		"error": {"message": "tool failed"}
	}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "antigravity-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest antigravity-hook Stop error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.Severity != notify.SeverityCritical || got.Metadata["category"] != "error" || got.Metadata["error"] != "tool failed" {
		t.Fatalf("pushed = %#v", got)
	}
}

func TestIngestAntigravityStatuslineApprovalPushesCritical(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"conversationId": "ag-conv-123",
		"cwd": "/repo/projmux",
		"statusline": {
			"agent_state": "waiting_for_tool",
			"tool_confirmation_pending": true
		}
	}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Statusline"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest antigravity-hook Statusline error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:antigravity:approval:ag-conv-123" || got.Severity != notify.SeverityCritical || got.Metadata["category"] != "approval_required" {
		t.Fatalf("pushed = %#v", got)
	}
	if got.Metadata["agent"] != "antigravity" || got.Metadata["tool_confirmation_pending"] != "true" || got.Metadata["agent_state"] != "waiting_for_tool" {
		t.Fatalf("metadata = %#v", got.Metadata)
	}
}

func TestIngestAntigravityStatuslineConfirmationDedupeAndFalseSilence(t *testing.T) {
	t.Run("repeated true replaces one queue row", func(t *testing.T) {
		home := t.TempDir()
		queue := notify.NewStore(filepath.Join(home, "notify.json"))
		cmd := testAICommand(home)
		cmd.producer = &storeAttentionNotifyProducer{store: queue, ttl: time.Minute}
		cmd.readCommand = antigravityIngestReadCommand("%7")
		payload := `{"conversation_id":"123e4567-e89b-12d3-a456-426614174000","cwd":"/repo/projmux","agent_state":"tool_use","tool_confirmation_pending":true}`
		for range 2 {
			cmd.stdin = strings.NewReader(payload)
			if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Statusline"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := queue.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("queue entries = %#v, want one replaced approval row", entries)
		}
		if got, want := entries[0].ID, "ai:antigravity:approval:123e4567-e89b-12d3-a456-426614174000"; got != want {
			t.Fatalf("queue ID = %q, want stable %q", got, want)
		}
		if entries[0].Severity != notify.SeverityCritical || entries[0].Metadata[notify.MetaCategory] != "approval_required" {
			t.Fatalf("queue entry = %#v, want critical approval_required", entries[0])
		}
	})

	t.Run("repeated false creates no queue row", func(t *testing.T) {
		home := t.TempDir()
		queue := notify.NewStore(filepath.Join(home, "notify.json"))
		cmd := testAICommand(home)
		cmd.producer = &storeAttentionNotifyProducer{store: queue, ttl: time.Minute}
		cmd.readCommand = antigravityIngestReadCommand("%7")
		payload := `{"conversation_id":"123e4567-e89b-12d3-a456-426614174000","cwd":"/repo/projmux","agent_state":"idle","tool_confirmation_pending":false}`
		for range 2 {
			cmd.stdin = strings.NewReader(payload)
			if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Statusline"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := queue.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("queue entries = %#v, want none for repeated false", entries)
		}
	})
}

func TestIngestAntigravityStatuslineWithoutPendingQuietLogsReason(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"conversationId": "ag-conv-123",
		"cwd": "/repo/projmux",
		"agent_state": "idle",
		"context_window": "38%"
	}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Statusline"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest antigravity-hook Statusline quiet error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
	}
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		`"source":"antigravity-hook"`,
		`"event":"Statusline"`,
		`"result":"quiet"`,
		`"reason":"statusline agent_state is idle; preserving existing completion or attention state"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output = %q, want %s", got, want)
		}
	}
}

func TestIngestAntigravityManagedStatusLineWritesEmptyStdout(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX_PANE":
			return "%7"
		default:
			return ""
		}
	}
	data, err := os.ReadFile(filepath.Join("testdata", "antigravity", "statusline_v1_1_12.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.stdin = bytes.NewReader(data)
	cmd.readCommand = antigravityIngestReadCommand("%7")
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Statusline"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("statusline stdout = %q, want empty so built-in stacking remains visible", stdout.String())
	}
	baseDir, err := cmd.usageStateDir()
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := antigravityadapter.New(baseDir).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].Window != "context" || snaps[1].Window != "quota" || snaps[1].Bucket != "gemini-weekly" {
		t.Fatalf("statusline persisted snapshots = %#v, want separate context and quota rows", snaps)
	}
}

func TestIngestAntigravityStatusLineBusyAndIdleStateMapping(t *testing.T) {
	for _, agentState := range []string{"thinking", "working", "tool_use"} {
		t.Run(agentState, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			cmd.stdin = strings.NewReader(`{"cwd":"/repo/projmux","conversation_id":"ag-conv","agent_state":"` + agentState + `","tool_confirmation_pending":false}`)
			cmd.readCommand = antigravityIngestReadCommand("%7")
			if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Statusline"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			for _, want := range []recordedAICommand{
				{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneStateOption, "thinking"}},
				{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", attentionStateOption, attentionStateBusy}},
			} {
				if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
					t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
				}
			}
		})
	}

	cmd := testAICommand(t.TempDir())
	cmd.stdin = strings.NewReader(`{"cwd":"/repo/projmux","conversation_id":"ag-conv","agent_state":"idle","tool_confirmation_pending":false}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")
	if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", "Statusline"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, command := range cmdRecorder(cmd).commands {
		if len(command.args) > 5 && command.args[0] == "set-option" && (command.args[4] == aiPaneStateOption || command.args[4] == attentionStateOption) {
			t.Fatalf("idle statusline overwrote completion/attention state: %#v", command)
		}
	}
}

func TestIngestAntigravityStopThenLateBusyAndIdlePreservesCompletion(t *testing.T) {
	home := t.TempDir()
	queue := notify.NewStore(filepath.Join(home, "notify.json"))
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: queue, ttl: time.Minute}
	baseRead := antigravityIngestReadCommand("%7")
	aiState := ""
	attentionState := ""
	thinkingWrites := 0
	cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#{" + aiPaneStateOption + "}"}) {
			return []byte(aiState + "\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#{" + attentionStateOption + "}"}) {
			return []byte(attentionState + "\n"), nil
		}
		return baseRead(ctx, name, args...)
	}
	baseRun := cmd.runCommand
	cmd.runCommand = func(ctx context.Context, name string, args ...string) error {
		if name == "tmux" && len(args) == 6 && reflect.DeepEqual(args[:4], []string{"set-option", "-p", "-t", "%7"}) {
			switch args[4] {
			case aiPaneStateOption:
				aiState = args[5]
				if aiState == "thinking" {
					thinkingWrites++
				}
			case attentionStateOption:
				attentionState = args[5]
			}
		}
		if name == "tmux" && len(args) == 6 && reflect.DeepEqual(args[:5], []string{"set-option", "-p", "-u", "-t", "%7"}) && args[5] == attentionStateOption {
			attentionState = ""
		}
		return baseRun(ctx, name, args...)
	}

	run := func(event, payload string) {
		t.Helper()
		cmd.stdin = strings.NewReader(payload)
		if err := cmd.Run([]string{"ingest", "antigravity-hook", "--event", event}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	identity := `"conversation_id":"123e4567-e89b-12d3-a456-426614174000","cwd":"/repo/projmux"`
	run("Stop", `{`+identity+`,"terminationReason":"completed"}`)
	if aiState != "waiting" {
		t.Fatalf("state after Stop = %q, want waiting", aiState)
	}
	run("Statusline", `{`+identity+`,"agent_state":"tool_use","tool_confirmation_pending":false}`)
	run("Statusline", `{`+identity+`,"agent_state":"idle","tool_confirmation_pending":false}`)
	if aiState != "waiting" {
		t.Fatalf("state after Stop -> busy -> idle = %q, want completion waiting preserved", aiState)
	}
	entries, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Metadata[notify.MetaCategory] != "response_complete" {
		t.Fatalf("queue entries = %#v, want one response_complete", entries)
	}

	// A real next generation starts with PreInvocation, which resets the pane
	// to thinking; the following statusline busy update is then accepted.
	run("PreInvocation", `{`+identity+`}`)
	if aiState != "thinking" || thinkingWrites != 1 {
		t.Fatalf("state after new PreInvocation = %q, want thinking", aiState)
	}
	run("Statusline", `{`+identity+`,"agent_state":"working","tool_confirmation_pending":false}`)
	if aiState != "thinking" || thinkingWrites != 2 {
		t.Fatalf("state after new-generation busy statusline = %q writes=%d, want accepted thinking update", aiState, thinkingWrites)
	}
}

func TestPersistAntigravityStructuredContextPrecedesStringFallback(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	cmd.now = func() time.Time { return now }
	cmd.persistAntigravityContextUsage(antigravityHookPayload{
		ConversationID:           "conversation-local",
		ContextWindow:            "99%",
		ContextUsedPercentage:    14.24,
		ContextUsedPercentageSet: true,
	})
	baseDir, err := cmd.usageStateDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(baseDir, antigravityadapter.ContextFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"conversation_id": "conversation-local"`) || !strings.Contains(string(data), `"pct": 14.24`) {
		t.Fatalf("context sidecar = %s", data)
	}
}

func TestIngestAntigravityPostInvocationQuietWithoutNotify(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.stdin = strings.NewReader(`{
		"eventName": "PostInvocation",
		"conversationId": "ag-conv-123",
		"cwd": "/repo/projmux"
	}`)
	cmd.readCommand = antigravityIngestReadCommand("%7")

	if err := cmd.Run([]string{"ingest", "antigravity-hook"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest antigravity-hook PostInvocation error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0: %#v", len(store.pushed), store.pushed)
	}
	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--json"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log --json error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"source":"antigravity-hook"`) || !strings.Contains(got, `"event":"PostInvocation"`) || !strings.Contains(got, `"result":"quiet"`) {
		t.Fatalf("log output = %q", got)
	}
}

func TestAIWatchTitleSkipsHookActivePane(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	titleCaptureReads := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%5", "#{pane_id}__PROJMUX_TMUX_AI_GATE_SEP__#{" + aiPaneHookActiveOption + "}"}):
			return []byte("%5__PROJMUX_TMUX_AI_GATE_SEP__1\n"), nil
		case len(args) >= 5 && args[0] == "display-message" && args[3] == "%5" && strings.Contains(args[4], "#{pane_title}"):
			titleCaptureReads++
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%5"}):
			titleCaptureReads++
			return []byte("\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	if err := cmd.Run([]string{"watch-title", "%5"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}
	if titleCaptureReads != 0 {
		t.Fatalf("titleCaptureReads = %d, want 0", titleCaptureReads)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no writes for hook-active pane", cmdRecorder(cmd).commands)
	}
}

func claudeIngestReadCommand(paneID string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"list-panes", "-a", "-F", aiIngestListPanesFormat}):
			return []byte(paneID + "\x1f/repo/projmux\x1f\x1f\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{@projmux_ai_agent}"}):
			return []byte("claude\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#S"}):
			return []byte("workspace\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{window_id}"}):
			return []byte("@1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{pane_id}"}):
			return []byte(paneID + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{socket_path}"}):
			return []byte("/tmp/tmux-1000/projmux\n"), nil
		}
		return nil, os.ErrNotExist
	}
}

func codexHookIngestReadCommand(paneID string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"list-panes", "-a", "-F", aiIngestListPanesFormat}):
			return []byte(paneID + "\x1f/repo/projmux\x1f\x1fcodex-session\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{@projmux_ai_agent}"}):
			return []byte("codex\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#S"}):
			return []byte("workspace\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{window_id}"}):
			return []byte("@1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{pane_id}"}):
			return []byte(paneID + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{socket_path}"}):
			return []byte("/tmp/tmux-1000/projmux\n"), nil
		}
		return nil, os.ErrNotExist
	}
}

func antigravityIngestReadCommand(paneID string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"list-panes", "-a", "-F", aiIngestListPanesFormat}):
			return []byte(paneID + "\x1f/repo/projmux\x1f\x1f\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{@projmux_ai_agent}"}):
			return []byte("antigravity\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#S"}):
			return []byte("workspace\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{window_id}"}):
			return []byte("@1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{pane_id}"}):
			return []byte(paneID + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", paneID, "#{socket_path}"}):
			return []byte("/tmp/tmux-1000/projmux\n"), nil
		}
		return nil, os.ErrNotExist
	}
}

func hasRecordedAICommand(commands []recordedAICommand, want recordedAICommand) bool {
	for _, got := range commands {
		if got.name == want.name && reflect.DeepEqual(got.args, want.args) {
			return true
		}
	}
	return false
}

func hasRecordedAISetOption(commands []recordedAICommand, option string) bool {
	for _, got := range commands {
		if got.name == "tmux" && len(got.args) >= 6 && got.args[0] == "set-option" && got.args[4] == option {
			return true
		}
	}
	return false
}

func assertNoAIPaneTopicWrite(t *testing.T, commands []recordedAICommand) {
	t.Helper()
	if hasRecordedAISetOption(commands, aiPaneTopicOption) {
		t.Fatalf("commands = %#v, did not want hook ingest to write topic", commands)
	}
}
