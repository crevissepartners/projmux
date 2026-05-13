package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

func TestParseCodexNotifyPayload(t *testing.T) {
	t.Parallel()

	payload, err := parseCodexNotifyPayload([]byte(`{
		"type": "agent-turn-complete",
		"thread-id": "thread-123",
		"turn-id": "turn-456",
		"cwd": "/repo/projmux",
		"client": {"name": "codex-cli", "model": "gpt-5.1-codex"},
		"input-messages": [{"role": "user", "content": "run tests"}],
		"last-assistant-message": "Tests passed."
	}`))
	if err != nil {
		t.Fatalf("parseCodexNotifyPayload() error = %v", err)
	}
	if payload.Type != "agent-turn-complete" || payload.ThreadID != "thread-123" || payload.TurnID != "turn-456" || payload.CWD != "/repo/projmux" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Client != "codex-cli" || payload.Model != "gpt-5.1-codex" {
		t.Fatalf("client/model = %q/%q", payload.Client, payload.Model)
	}
	if len(payload.InputMessages) != 1 {
		t.Fatalf("InputMessages len = %d, want 1", len(payload.InputMessages))
	}
	if payload.LastAssistantMessage != "Tests passed." {
		t.Fatalf("LastAssistantMessage = %q", payload.LastAssistantMessage)
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

func TestIngestCodexNotifyPushesWaitingStatusAndMetadata(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: time.Minute}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"list-panes", "-a", "-F", aiIngestListPanesFormat}):
			return []byte("%7\x1f/repo/projmux\x1f\x1f\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#{@projmux_ai_agent}"}):
			return []byte("codex\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#S"}):
			return []byte("workspace\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#{window_id}"}):
			return []byte("@1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#{pane_id}"}):
			return []byte("%7\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "#{socket_path}"}):
			return []byte("/tmp/tmux-1000/projmux\n"), nil
		}
		return nil, os.ErrNotExist
	}

	err := cmd.Run([]string{"ingest", "codex-notify", `{
		"type": "agent-turn-complete",
		"thread-id": "thread-123",
		"turn-id": "turn-456",
		"cwd": "/repo/projmux",
		"model": "gpt-5.1-codex",
		"client": "codex-cli",
		"last-assistant-message": "Implemented hook notify ingest."
	}`}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run ingest codex-notify error = %v", err)
	}

	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", aiPaneStateOption, "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", attentionStateOption, attentionStateReply}},
	} {
		if !hasRecordedAICommand(cmdRecorder(cmd).commands, want) {
			t.Fatalf("commands = %#v, missing %#v", cmdRecorder(cmd).commands, want)
		}
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:codex:thread-123:turn-456" {
		t.Fatalf("ID = %q", got.ID)
	}
	if got.Text != "Codex · 응답 완료 · Implemented hook notify ingest." {
		t.Fatalf("Text = %q", got.Text)
	}
	if got.Metadata["agent"] != "codex" || got.Metadata["thread_id"] != "thread-123" || got.Metadata["turn_id"] != "turn-456" || got.Metadata["cwd"] != "/repo/projmux" || got.Metadata["model"] != "gpt-5.1-codex" || got.Metadata["client"] != "codex-cli" {
		t.Fatalf("Metadata = %#v", got.Metadata)
	}
	if got.Target.Session != "workspace" || got.Target.Window != "@1" || got.Target.Pane != "%7" {
		t.Fatalf("Target = %+v", got.Target)
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
			name: "codex turn complete assistant summary",
			body: formatCodexTurnCompleteNotifyBody(codexNotifyPayload{
				LastAssistantMessage: "Implemented hook notify ingest.",
			}),
			want: aiNotifyBody{Text: "Codex · 응답 완료 · Implemented hook notify ingest."},
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
			wantID:       "ai:claude:subagent-stop:claude-session:reviewer:sub-7",
			wantText:     "Claude · 서브에이전트 종료 · reviewer · sub-7",
			wantSeverity: notify.SeverityInfo,
			wantMetadata: map[string]string{
				"event":         "SubagentStop",
				"subagent_type": "reviewer",
				"subagent_id":   "sub-7",
			},
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

func TestAIWatchTitleSkipsHookActivePane(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	paneIDReads := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%5", "#{" + aiPaneHookActiveOption + "}"}):
			return []byte("1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%5", "#{pane_id}"}):
			paneIDReads++
			return []byte("%5\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	if err := cmd.Run([]string{"watch-title", "%5"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}
	if paneIDReads != 0 {
		t.Fatalf("paneIDReads = %d, want 0", paneIDReads)
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

func hasRecordedAICommand(commands []recordedAICommand, want recordedAICommand) bool {
	for _, got := range commands {
		if got.name == want.name && reflect.DeepEqual(got.args, want.args) {
			return true
		}
	}
	return false
}
