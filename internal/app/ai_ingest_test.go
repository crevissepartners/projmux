package app

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"
	"time"
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

func hasRecordedAICommand(commands []recordedAICommand, want recordedAICommand) bool {
	for _, got := range commands {
		if got.name == want.name && reflect.DeepEqual(got.args, want.args) {
			return true
		}
	}
	return false
}
