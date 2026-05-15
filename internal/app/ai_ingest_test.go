package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
)

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
	if got.Text != "Codex · 승인 필요 · Bash: go test ./internal/app" {
		t.Fatalf("Text = %q", got.Text)
	}
	if got.Severity != notify.SeverityCritical {
		t.Fatalf("Severity = %q", got.Severity)
	}
	if got.Metadata["agent"] != "codex" || got.Metadata["event"] != "PermissionRequest" || got.Metadata["model"] != "gpt-5.1-codex" || got.Metadata["tool_input.command"] != "go test ./internal/app" {
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
	if got.ID != "ai:codex:stop:codex-session:turn-456" || got.Text != "Codex · 응답 완료" || got.Severity != notify.SeverityInfo {
		t.Fatalf("pushed = %#v", got)
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
	if got.Text != "Codex · PreToolUse · Bash" || got.Severity != notify.SeverityInfo {
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
	if got := store.pushed[0].Text; got != "Codex · PostToolUse · Edit" {
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
	cmd := testAICommand(home)
	cmd.notifyStore = store
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		`{"at":"2026-01-01T00:00:00Z","source":"codex-hook","event":"Stop","result":"notify","pane":"%1"}`,
		`{"at":"2026-01-01T00:00:01Z","source":"claude-hook","event":"SubagentStop","result":"quiet","reason":"high-volume event","pane":"%2"}`,
	}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmd.Run([]string{"ingest", "log", "--tail", "1"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run ingest log error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "claude-hook SubagentStop quiet") || strings.Contains(got, "codex-hook Stop notify") {
		t.Fatalf("tail output = %q", got)
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
			want: aiNotifyBody{Text: "Codex · 승인 필요 · Bash: go test ./internal/app", Severity: notify.SeverityCritical},
		},
		{
			name: "codex hook stop",
			body: formatCodexHookStopNotifyBody(codexHookPayload{}),
			want: aiNotifyBody{Text: "Codex · 응답 완료", Severity: notify.SeverityInfo},
		},
		{
			name: "claude notification permission prompt",
			body: formatClaudeNotificationNotifyBody(claudeHookPayload{
				NotificationType: "permission_prompt",
				Message:          "Approve Bash?",
			}),
			want: aiNotifyBody{Text: "Claude · 승인 필요 · Approve Bash?", Severity: notify.SeverityCritical},
		},
		{
			name: "claude notification idle",
			body: formatClaudeNotificationNotifyBody(claudeHookPayload{
				NotificationType: "idle_prompt",
				Message:          "Waiting for your next request",
			}),
			want: aiNotifyBody{Text: "Claude · 응답 완료 · Waiting for your next request", Severity: notify.SeverityInfo},
		},
		{
			name: "claude permission request bash command",
			body: formatClaudePermissionNotifyBody(claudeHookPayload{
				ToolName:  "Bash",
				ToolUseID: "tool-123",
				ToolInput: map[string]any{"command": "rm -rf /tmp/old-cache"},
			}),
			want: aiNotifyBody{Text: "Claude · 승인 필요 · Bash: rm -rf /tmp/old-cache", Severity: notify.SeverityCritical},
		},
		{
			name: "claude stop transcript summary",
			body: formatClaudeStopNotifyBody("implemented and verified"),
			want: aiNotifyBody{Text: "Claude · 응답 완료 · implemented and verified", Severity: notify.SeverityInfo},
		},
		{
			name: "claude stop failure error labels",
			body: formatClaudeStopFailureNotifyBody(claudeHookPayload{
				ErrorType:    "timeout",
				ErrorMessage: "tool call exceeded deadline",
			}),
			want: aiNotifyBody{Text: "Claude · 오류 · timeout · tool call exceeded deadline", Severity: notify.SeverityCritical},
		},
		{
			name: "claude subagent stop labels",
			body: formatClaudeSubagentStopNotifyBody(claudeHookPayload{
				SubagentType: "reviewer",
				SubagentID:   "sub-7",
			}),
			want: aiNotifyBody{Text: "Claude · 서브에이전트 종료 · reviewer · sub-7", Severity: notify.SeverityInfo},
		},
		{
			name: "claude teammate idle labels",
			body: formatClaudeTeammateIdleNotifyBody(claudeHookPayload{
				TeammateName:    "sam",
				TeammateID:      "team-3",
				TeammateContext: "waiting for review",
			}),
			want: aiNotifyBody{Text: "Claude · 팀메이트 대기 · sam · team-3 · waiting for review", Severity: notify.SeverityInfo},
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

	text := "Codex · 승인 필요 · Bash: go test ./internal/app"
	notification := cmd.aiTextNotification("%7", text, notify.SeverityCritical)
	if notification.Summary != text {
		t.Fatalf("Summary = %q, want queue text %q", notification.Summary, text)
	}
	if notification.Urgency != "normal" {
		t.Fatalf("Urgency = %q, want OS urgency normal for critical queue text", notification.Urgency)
	}
	if !strings.Contains(notification.Body, "projmux/feat/hooks") || !strings.Contains(notification.Body, "workspace:editor") {
		t.Fatalf("Body = %q, want project/session context", notification.Body)
	}

	if err := cmd.notifyAIText("%7", text, notify.SeverityCritical, true); err != nil {
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
	if !reflect.DeepEqual(notifySend.args[len(notifySend.args)-2:], []string{text, notification.Body}) {
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
	if got.Text != "Claude · 승인 필요 · Bash: go test ./internal/app" {
		t.Fatalf("Text = %q", got.Text)
	}
	if got.Severity != notify.SeverityCritical {
		t.Fatalf("Severity = %q", got.Severity)
	}
	if got.Metadata["agent"] != "claude" || got.Metadata["event"] != "PermissionRequest" || got.Metadata["tool_input.command"] != "go test ./internal/app" {
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
	if len(store.pushed) != 1 || store.pushed[0].Text != "Claude · 응답 완료 · implemented and verified" {
		t.Fatalf("pushed = %#v", store.pushed)
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
	if len(store.pushed) != 1 || store.pushed[0].Text != "Claude · 응답 완료" {
		t.Fatalf("fallback pushed = %#v", store.pushed)
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
	if got.Text != "Claude · 입력 필요 · Need deployment target" || got.Severity != notify.SeverityCritical {
		t.Fatalf("pushed = %#v", got)
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
			wantText:     "Claude · 오류 · timeout · tool call exceeded deadline",
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
			wantText:     "Claude · 팀메이트 대기 · sam · team-3 · waiting for review",
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
	if got := store.pushed[0].Text; got != "Claude · 서브에이전트 종료 · reviewer · sub-7" {
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
