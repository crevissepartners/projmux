package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

// fakeAttentionLookup is a deterministic in-memory lookup for the producer
// tests. Every read goes through a single map keyed by `paneID|key` so a
// single fixture configures both PaneOption and PaneFormat reads.
type fakeAttentionLookup struct {
	values map[string]string
}

func (l fakeAttentionLookup) PaneOption(paneID, option string) string {
	return l.values[paneID+"|"+option]
}

func (l fakeAttentionLookup) PaneFormat(paneID, format string) string {
	return l.values[paneID+"|"+format]
}

func newFakeAttentionLookup(v map[string]string) fakeAttentionLookup {
	return fakeAttentionLookup{values: v}
}

func TestStoreAttentionNotifyProducerPushReplyReadyHappyPath(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute}

	lookup := newFakeAttentionLookup(map[string]string{
		"%9|@projmux_ai_agent": "claude",
		"%9|@projmux_ai_topic": "",
		"%9|#S":                "main",
		"%9|#{window_id}":      "@4",
		"%9|#{pane_id}":        "%9",
		"%9|#{socket_path}":    "/tmp/tmux-1000/projmux",
	})

	producer.PushReplyReady(attentionNotifyInput{PaneID: "%9", Lookup: lookup})

	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:main:%9" {
		t.Fatalf("ID = %q, want ai:main:%%9", got.ID)
	}
	if got.Text != "claude: reply ready" {
		t.Fatalf("Text = %q, want %q", got.Text, "claude: reply ready")
	}
	if got.Source != notify.SourceAI {
		t.Fatalf("Source = %q, want %q", got.Source, notify.SourceAI)
	}
	if got.Severity != notify.SeverityInfo {
		t.Fatalf("Severity = %q, want %q", got.Severity, notify.SeverityInfo)
	}
	if got.TTL != 10*time.Minute {
		t.Fatalf("TTL = %s, want 10m", got.TTL)
	}
	if got.Target.Session != "main" || got.Target.Window != "@4" || got.Target.Pane != "%9" || got.Target.Socket != "/tmp/tmux-1000/projmux" {
		t.Fatalf("Target = %+v", got.Target)
	}
}

func TestStoreAttentionNotifyProducerPushReplyReadyWithTopic(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store, ttl: time.Minute}

	lookup := newFakeAttentionLookup(map[string]string{
		"%2|@projmux_ai_agent": "Codex",
		"%2|@projmux_ai_topic": "wire-producer",
		"%2|#S":                "feat-notify-producer-attention",
		"%2|#{window_id}":      "@1",
		"%2|#{pane_id}":        "%2",
	})

	producer.PushReplyReady(attentionNotifyInput{PaneID: "%2", Lookup: lookup})

	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	wantText := "codex: reply ready · wire-producer"
	if got.Text != wantText {
		t.Fatalf("Text = %q, want %q", got.Text, wantText)
	}
	if got.ID != "ai:feat-notify-producer-attention:%2" {
		t.Fatalf("ID = %q", got.ID)
	}
}

func TestStoreAttentionNotifyProducerSkipsPaneWithoutAgent(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store, ttl: time.Minute}

	lookup := newFakeAttentionLookup(map[string]string{
		"%3|@projmux_ai_agent": "",
		"%3|#S":                "shell",
		"%3|#{pane_id}":        "%3",
	})

	producer.PushReplyReady(attentionNotifyInput{PaneID: "%3", Lookup: lookup})

	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0 (no AI agent guard)", len(store.pushed))
	}
}

func TestStoreAttentionNotifyProducerSkipsWhenSessionMissing(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store, ttl: time.Minute}

	lookup := newFakeAttentionLookup(map[string]string{
		"%4|@projmux_ai_agent": "claude",
		"%4|#S":                "",
		"%4|#{pane_id}":        "%4",
	})

	producer.PushReplyReady(attentionNotifyInput{PaneID: "%4", Lookup: lookup})

	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0 (session missing)", len(store.pushed))
	}
}

func TestStoreAttentionNotifyProducerAckReplyReadyDoesNotAck(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store, ttl: time.Minute}

	lookup := newFakeAttentionLookup(map[string]string{
		"%9|#S":         "main",
		"%9|#{pane_id}": "%9",
	})

	producer.AckReplyReady(attentionNotifyInput{PaneID: "%9", Lookup: lookup})

	if store.ackedID != "" {
		t.Fatalf("ackedID = %q, want empty", store.ackedID)
	}
}

func TestStoreAttentionNotifyProducerAckSwallowsErrNotFound(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{ackErr: notify.ErrNotFound}
	producer := &storeAttentionNotifyProducer{store: store, ttl: time.Minute}

	lookup := newFakeAttentionLookup(map[string]string{
		"%9|#S":         "main",
		"%9|#{pane_id}": "%9",
	})

	// Should not panic / propagate.
	producer.AckReplyReady(attentionNotifyInput{PaneID: "%9", Lookup: lookup})

	if !errors.Is(store.ackErr, notify.ErrNotFound) {
		t.Fatalf("ackErr lost = %v", store.ackErr)
	}
}

func TestStoreAttentionNotifyProducerAckSkipsBlankInput(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	producer := &storeAttentionNotifyProducer{store: store, ttl: time.Minute}

	producer.AckReplyReady(attentionNotifyInput{})
	producer.AckReplyReady(attentionNotifyInput{PaneID: "%9"}) // nil Lookup

	if store.ackedID != "" {
		t.Fatalf("ackedID = %q, want empty", store.ackedID)
	}
}

func TestComposeAttentionReplyTextDefaultsAgent(t *testing.T) {
	t.Parallel()

	if got := composeAttentionReplyText("", ""); got != "agent: reply ready" {
		t.Fatalf("default = %q", got)
	}
	if got := composeAttentionReplyText("CLAUDE", ""); got != "claude: reply ready" {
		t.Fatalf("uppercase = %q", got)
	}
	if got := composeAttentionReplyText("codex", "  refactor split  "); got != "codex: reply ready · refactor split" {
		t.Fatalf("topic trim = %q", got)
	}
}

// TestAttentionToggleNotifiesQueueOnReply checks the integration point: a
// runToggle that sets state to reply on an AI pane triggers a queue push
// with the expected id and text, and the existing tmux call sequence is
// preserved.
func TestAttentionToggleNotifiesQueueOnReply(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %1 #{pane_title}":        []byte("server\n"),
			"tmux display-message -p -t %1 #{@projmux_ai_agent}": []byte("claude\n"),
			"tmux display-message -p -t %1 #{@projmux_ai_topic}": []byte("worker loop\n"),
			"tmux display-message -p -t %1 #S":                   []byte("main\n"),
			"tmux display-message -p -t %1 #{window_id}":         []byte("@5\n"),
			"tmux display-message -p -t %1 #{pane_id}":           []byte("%1\n"),
			"tmux display-message -p -t %1 #{socket_path}":       []byte("/tmp/tmux/default\n"),
		},
	}
	store := &stubNotifyStore{}
	cmd := &attentionCommand{
		runner:   runner,
		producer: &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute},
	}

	if err := cmd.Run([]string{"toggle", "%1"}, nopWriter{}, nopWriter{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:main:%1" {
		t.Fatalf("ID = %q", got.ID)
	}
	wantText := "claude: reply ready · worker loop"
	if got.Text != wantText {
		t.Fatalf("Text = %q, want %q", got.Text, wantText)
	}
	if got.Source != notify.SourceAI {
		t.Fatalf("Source = %q", got.Source)
	}
	if got.Target.Session != "main" || got.Target.Pane != "%1" || got.Target.Window != "@5" {
		t.Fatalf("Target = %+v", got.Target)
	}
}

// TestAttentionToggleKeepsQueueWhenClearingReply checks that toggling away
// from reply does not consume the notification queue row.
func TestAttentionToggleKeepsQueueWhenClearingReply(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %2 #{pane_title}": []byte("✳ worker\n"),
			"tmux display-message -p -t %2 #S":            []byte("main\n"),
			"tmux display-message -p -t %2 #{pane_id}":    []byte("%2\n"),
		},
	}
	store := &stubNotifyStore{}
	cmd := &attentionCommand{
		runner:   runner,
		producer: &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute},
	}

	if err := cmd.Run([]string{"toggle", "%2"}, nopWriter{}, nopWriter{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if store.ackedID != "" {
		t.Fatalf("ackedID = %q, want empty", store.ackedID)
	}
}

// TestAttentionClearKeepsQueue checks that clearing attention does not ack
// the queue entry.
func TestAttentionClearKeepsQueue(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %3 #{@projmux_attention_state}":       []byte("reply\n"),
			"tmux display-message -p -t %3 #{@projmux_attention_focus_armed}": []byte("1\n"),
			"tmux display-message -p -t %3 #{pane_title}":                     []byte("✔ done\n"),
			"tmux display-message -p -t %3 #S":                                []byte("main\n"),
			"tmux display-message -p -t %3 #{pane_id}":                        []byte("%3\n"),
		},
	}
	store := &stubNotifyStore{}
	cmd := &attentionCommand{
		runner:   runner,
		producer: &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute},
	}

	if err := cmd.Run([]string{"clear", "%3"}, nopWriter{}, nopWriter{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if store.ackedID != "" {
		t.Fatalf("ackedID = %q, want empty", store.ackedID)
	}
}

// TestAttentionToggleSkipsQueueWithoutAgent guards: a manual toggle on a
// non-AI pane (no `@projmux_ai_agent`) should not produce a queue entry.
func TestAttentionToggleSkipsQueueWithoutAgent(t *testing.T) {
	t.Parallel()

	runner := &recordingAttentionRunner{
		outputs: map[string][]byte{
			"tmux display-message -p -t %1 #{pane_title}":        []byte("server\n"),
			"tmux display-message -p -t %1 #{@projmux_ai_agent}": []byte("\n"),
			"tmux display-message -p -t %1 #S":                   []byte("main\n"),
			"tmux display-message -p -t %1 #{pane_id}":           []byte("%1\n"),
		},
	}
	store := &stubNotifyStore{}
	cmd := &attentionCommand{
		runner:   runner,
		producer: &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute},
	}

	if err := cmd.Run([]string{"toggle", "%1"}, nopWriter{}, nopWriter{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0 for non-AI pane", len(store.pushed))
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestAIStatusSetWaitingPushesQueueWhenInactive verifies the AI flow's
// integration: a transition to `waiting` on an inactive pane (the path that
// flips attention to reply) pushes a queue entry with the right id and
// claude-prefixed text.
func TestAIStatusSetWaitingPushesQueueWhenInactive(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testAICommand(home)
	store := &stubNotifyStore{}
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute}

	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, nil
		}
		if len(args) >= 5 && args[0] == "display-message" && args[1] == "-p" && args[2] == "-t" {
			pane := args[3]
			format := args[4]
			if pane != "%30" {
				return nil, nil
			}
			switch format {
			case "#{pane_active}":
				return []byte("0\n"), nil
			case "#{@projmux_ai_agent}":
				return []byte("claude\n"), nil
			case "#{@projmux_ai_topic}":
				return []byte("notify wiring\n"), nil
			case "#S":
				return []byte("main\n"), nil
			case "#{window_id}":
				return []byte("@7\n"), nil
			case "#{pane_id}":
				return []byte("%30\n"), nil
			case "#{socket_path}":
				return []byte("/tmp/tmux/default\n"), nil
			}
		}
		return nil, nil
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%30"}, nopWriter{}, nopWriter{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	got := store.pushed[0]
	if got.ID != "ai:main:%30" {
		t.Fatalf("ID = %q, want ai:main:%%30", got.ID)
	}
	wantText := "claude: reply ready · notify wiring"
	if got.Text != wantText {
		t.Fatalf("Text = %q, want %q", got.Text, wantText)
	}
	if got.Source != notify.SourceAI {
		t.Fatalf("Source = %q", got.Source)
	}
	if got.Target.Session != "main" || got.Target.Window != "@7" || got.Target.Pane != "%30" {
		t.Fatalf("Target = %+v", got.Target)
	}
}

// TestAIStatusSetThinkingKeepsQueue checks that the busy transition does not
// consume any outstanding queue entry for the pane.
func TestAIStatusSetThinkingKeepsQueue(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testAICommand(home)
	store := &stubNotifyStore{}
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute}

	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, nil
		}
		if len(args) >= 5 && args[0] == "display-message" && args[1] == "-p" && args[2] == "-t" {
			switch args[4] {
			case "#S":
				return []byte("main\n"), nil
			case "#{pane_id}":
				return []byte("%30\n"), nil
			}
		}
		return nil, nil
	}

	if err := cmd.Run([]string{"status", "set", "thinking", "%30"}, nopWriter{}, nopWriter{}); err != nil {
		t.Fatalf("Run status set thinking error = %v", err)
	}

	if store.ackedID != "" {
		t.Fatalf("ackedID = %q, want empty", store.ackedID)
	}
}

// TestAIStatusSetIdleKeepsQueue checks the idle/empty transition path does
// not consume the notification row.
func TestAIStatusSetIdleKeepsQueue(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testAICommand(home)
	store := &stubNotifyStore{}
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute}

	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, nil
		}
		if len(args) >= 5 && args[0] == "display-message" && args[1] == "-p" && args[2] == "-t" {
			switch args[4] {
			case "#S":
				return []byte("main\n"), nil
			case "#{pane_id}":
				return []byte("%30\n"), nil
			}
		}
		return nil, nil
	}

	if err := cmd.Run([]string{"status", "set", "idle", "%30"}, nopWriter{}, nopWriter{}); err != nil {
		t.Fatalf("Run status set idle error = %v", err)
	}

	if store.ackedID != "" {
		t.Fatalf("ackedID = %q, want empty", store.ackedID)
	}
}
