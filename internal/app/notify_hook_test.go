package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

type recordingNotifyHookRunner struct {
	event   hooks.Event
	context hooks.Context
	calls   int
}

func (r *recordingNotifyHookRunner) RunAsync(_ context.Context, event hooks.Event, c hooks.Context) <-chan hooks.AsyncResult {
	r.calls++
	r.event = event
	r.context = c
	ch := make(chan hooks.AsyncResult, 1)
	ch <- hooks.AsyncResult{}
	close(ch)
	return ch
}

func TestSendNotiHookDispatcherDispatchesPayloadAndEnv(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeCredibleGitMarker(t, repo)
	runner := &recordingNotifyHookRunner{}
	dispatcher := &sendNotiHookDispatcher{
		runner:    runner,
		lookupEnv: func(string) string { return "" },
		getwd:     func() (string, error) { return filepath.Join(repo, "subdir"), nil },
	}
	createdAt := time.Date(2026, time.May, 12, 2, 3, 4, 0, time.UTC)

	dispatcher.Dispatch(notify.Notification{
		ID:        "n_123",
		Text:      "Ready",
		Source:    notify.SourceAI,
		Metadata:  map[string]string{"agent": "claude", "category": "response_complete"},
		Socket:    "/tmp/tmux.sock",
		Session:   "main",
		Pane:      "%9",
		CreatedAt: createdAt,
	}, notifyHookMeta{
		Type:    "ai-reply-ready",
		Agent:   "claude",
		Topic:   "worker loop",
		Message: "Ready",
	})

	if runner.calls != 1 {
		t.Fatalf("RunAsync call count = %d, want 1", runner.calls)
	}
	if runner.event != hooks.EventSendNoti {
		t.Fatalf("event = %q, want %q", runner.event, hooks.EventSendNoti)
	}
	if runner.context.CWD != repo {
		t.Fatalf("Context.CWD = %q, want %q", runner.context.CWD, repo)
	}
	if got := runner.context.Env[notifyHookDepthEnv]; got != "1" {
		t.Fatalf("%s = %q, want 1", notifyHookDepthEnv, got)
	}
	if got := runner.context.Env["PROJMUX_NOTIFY_AGENT"]; got != "claude" {
		t.Fatalf("PROJMUX_NOTIFY_AGENT = %q", got)
	}
	if got := runner.context.Env["PROJMUX_NOTIFY_TOPIC"]; got != "worker loop" {
		t.Fatalf("PROJMUX_NOTIFY_TOPIC = %q", got)
	}

	var payload notifyHookPayload
	if err := json.Unmarshal(runner.context.Stdin, &payload); err != nil {
		t.Fatalf("decode stdin json: %v", err)
	}
	if payload.Event != "send-noti" || payload.ID != "n_123" || payload.Type != "ai-reply-ready" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Agent != "claude" || payload.Topic != "worker loop" || payload.Session != "main" || payload.Pane != "%9" {
		t.Fatalf("payload = %+v", payload)
	}
	if !payload.CreatedAt.Equal(createdAt) {
		t.Fatalf("payload.CreatedAt = %s, want %s", payload.CreatedAt, createdAt)
	}
}

func TestSendNotiHookDispatcherDepthGuardSkipsDispatch(t *testing.T) {
	t.Parallel()

	runner := &recordingNotifyHookRunner{}
	dispatcher := &sendNotiHookDispatcher{
		runner: runner,
		lookupEnv: func(name string) string {
			if name == notifyHookDepthEnv {
				return "1"
			}
			return ""
		},
	}
	dispatcher.Dispatch(notify.Notification{ID: "n"}, notifyHookMeta{})
	if runner.calls != 0 {
		t.Fatalf("RunAsync call count = %d, want 0", runner.calls)
	}
}

func TestNotifyPushDispatchesSendNotiHook(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		pushResult: notify.PushResult{ID: "abc", QueueLen: 1},
		pushEntry: notify.Notification{
			ID:        "abc",
			Text:      "deploy ok",
			Source:    notify.SourceExternal,
			Session:   "main",
			Pane:      "%7",
			CreatedAt: time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC),
		},
	}
	cmd := newCmd(store)
	runner := &recordingNotifyHookRunner{}
	cmd.hooks = &sendNotiHookDispatcher{
		runner:    runner,
		lookupEnv: func(string) string { return "" },
		getwd:     func() (string, error) { return t.TempDir(), nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"push", "--text", "deploy ok", "--target", "main:1.0"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("RunAsync call count = %d, want 1", runner.calls)
	}

	var payload notifyHookPayload
	if err := json.Unmarshal(runner.context.Stdin, &payload); err != nil {
		t.Fatalf("decode stdin json: %v", err)
	}
	if payload.Type != notify.SourceExternal {
		t.Fatalf("payload.Type = %q, want %q", payload.Type, notify.SourceExternal)
	}
	if payload.Message != "deploy ok" {
		t.Fatalf("payload.Message = %q, want deploy ok", payload.Message)
	}
}

func TestNotifyPushSkipsSendNotiHookWhenStorePushFails(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{pushErr: context.DeadlineExceeded}
	cmd := newCmd(store)
	runner := &recordingNotifyHookRunner{}
	cmd.hooks = &sendNotiHookDispatcher{
		runner:    runner,
		lookupEnv: func(string) string { return "" },
		getwd:     func() (string, error) { return t.TempDir(), nil },
	}

	err := cmd.Run([]string{"push", "--text", "deploy ok", "--target", "main:1.0"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("Run error = nil, want push error")
	}
	if runner.calls != 0 {
		t.Fatalf("RunAsync call count = %d, want 0", runner.calls)
	}
}

func TestNotifyPushDepthGuardSuppressesDispatch(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		pushResult: notify.PushResult{ID: "abc", QueueLen: 1},
		pushEntry: notify.Notification{
			ID:        "abc",
			Text:      "deploy ok",
			Source:    notify.SourceExternal,
			Session:   "main",
			CreatedAt: time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC),
		},
	}
	cmd := newCmd(store)
	runner := &recordingNotifyHookRunner{}
	cmd.hooks = &sendNotiHookDispatcher{
		runner: runner,
		lookupEnv: func(name string) string {
			if name == notifyHookDepthEnv {
				return "1"
			}
			return ""
		},
		getwd: func() (string, error) { return t.TempDir(), nil },
	}

	if err := cmd.Run([]string{"push", "--text", "deploy ok", "--target", "main:1.0"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("RunAsync call count = %d, want 0", runner.calls)
	}
}
