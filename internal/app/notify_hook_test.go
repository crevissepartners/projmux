package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
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

func TestSendNotiHookDispatcherResolvesHookCWD(t *testing.T) {
	t.Parallel()

	mkdir := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	tests := []struct {
		name string
		// setup returns the PROJMUX_CWD value, the getwd result, and the
		// Context.CWD the dispatcher must hand to the runner.
		setup func(t *testing.T) (envCWD string, getwd func() (string, error), want string)
	}{
		{
			name: "inherited PROJMUX_CWD wins over marker root",
			setup: func(t *testing.T) (string, func() (string, error), string) {
				repo := t.TempDir()
				mkdir(t, filepath.Join(repo, ".git"))
				inherited := t.TempDir()
				wd := filepath.Join(repo, "subdir")
				return "  " + inherited + "  ", func() (string, error) { return wd, nil }, inherited
			},
		},
		{
			name: "blank PROJMUX_CWD falls through to marker root",
			setup: func(t *testing.T) (string, func() (string, error), string) {
				repo := t.TempDir()
				mkdir(t, filepath.Join(repo, ".git"))
				wd := filepath.Join(repo, "subdir")
				return " \t ", func() (string, error) { return wd, nil }, repo
			},
		},
		{
			name: "projmux marker directory is a root",
			setup: func(t *testing.T) (string, func() (string, error), string) {
				repo := t.TempDir()
				mkdir(t, filepath.Join(repo, ".projmux"))
				wd := filepath.Join(repo, "a", "b")
				mkdir(t, wd)
				return "", func() (string, error) { return wd, nil }, repo
			},
		},
		{
			name: "nearest marker wins over outer marker",
			setup: func(t *testing.T) (string, func() (string, error), string) {
				outer := t.TempDir()
				mkdir(t, filepath.Join(outer, ".git"))
				inner := filepath.Join(outer, "nested")
				mkdir(t, filepath.Join(inner, ".projmux"))
				wd := filepath.Join(inner, "subdir")
				return "", func() (string, error) { return wd, nil }, inner
			},
		},
		{
			name: "no marker uses working directory",
			setup: func(t *testing.T) (string, func() (string, error), string) {
				wd := filepath.Join(t.TempDir(), "plain")
				mkdir(t, wd)
				if root := nearestProjectMarker(wd); root != "" {
					t.Skipf("temp dir %s has a .projmux or .git ancestor at %s; the walk reaches /", wd, root)
				}
				return "", func() (string, error) { return wd, nil }, wd
			},
		},
		{
			name: "getwd error yields empty",
			setup: func(t *testing.T) (string, func() (string, error), string) {
				return "", func() (string, error) { return "", errors.New("getwd failed") }, ""
			},
		},
		{
			name: "whitespace working directory yields empty",
			setup: func(t *testing.T) (string, func() (string, error), string) {
				return "", func() (string, error) { return "   ", nil }, ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			envCWD, getwd, want := tt.setup(t)
			runner := &recordingNotifyHookRunner{}
			dispatcher := &sendNotiHookDispatcher{
				runner: runner,
				lookupEnv: func(name string) string {
					if name == "PROJMUX_CWD" {
						return envCWD
					}
					return ""
				},
				getwd: getwd,
			}

			dispatcher.Dispatch(notify.Notification{ID: "n"}, notifyHookMeta{})

			if runner.calls != 1 {
				t.Fatalf("RunAsync call count = %d, want 1", runner.calls)
			}
			if runner.context.CWD != want {
				t.Fatalf("Context.CWD = %q, want %q", runner.context.CWD, want)
			}
		})
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

// gatedNotifyHookRunner keeps the hook result channel open until the test
// closes release, so a test can tell whether a caller waited for the result.
type gatedNotifyHookRunner struct {
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	delivered   atomic.Bool
	calls       atomic.Int32
	onStart     func()
}

func newGatedNotifyHookRunner(t *testing.T) *gatedNotifyHookRunner {
	t.Helper()
	r := &gatedNotifyHookRunner{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(r.open)
	return r
}

func (r *gatedNotifyHookRunner) open() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func (r *gatedNotifyHookRunner) RunAsync(context.Context, hooks.Event, hooks.Context) <-chan hooks.AsyncResult {
	r.calls.Add(1)
	if r.onStart != nil {
		r.onStart()
	}
	ch := make(chan hooks.AsyncResult, 1)
	go func() {
		<-r.release
		r.delivered.Store(true)
		ch <- hooks.AsyncResult{}
		close(ch)
	}()
	r.startOnce.Do(func() { close(r.started) })
	return ch
}

// assertCallWaitsForHookResult runs call in a goroutine and proves it does not
// return until the gated hook result has been delivered. The short select
// guard only bounds how long an early return is looked for; ordering is
// proven by the delivered flag sampled at return time.
func assertCallWaitsForHookResult(t *testing.T, runner *gatedNotifyHookRunner, call func()) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		call()
		done <- runner.delivered.Load()
	}()
	select {
	case <-runner.started:
	case <-time.After(10 * time.Second):
		t.Fatal("send-noti hook was never dispatched")
	}
	select {
	case deliveredAtReturn := <-done:
		t.Fatalf("call returned while the hook result was still pending (delivered at return = %v)", deliveredAtReturn)
	case <-time.After(50 * time.Millisecond):
	}
	runner.open()
	select {
	case deliveredAtReturn := <-done:
		if !deliveredAtReturn {
			t.Fatal("call returned before the hook result was delivered")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call did not return after the hook result was delivered")
	}
}

func TestSendNotiHookDispatcherDispatchWaitsForHookResult(t *testing.T) {
	t.Parallel()

	runner := newGatedNotifyHookRunner(t)
	dispatcher := &sendNotiHookDispatcher{
		runner:    runner,
		lookupEnv: func(string) string { return "" },
		getwd:     func() (string, error) { return t.TempDir(), nil },
	}
	assertCallWaitsForHookResult(t, runner, func() {
		dispatcher.Dispatch(notify.Notification{ID: "n_wait", Text: "Ready"}, notifyHookMeta{Type: notify.SourceExternal})
	})
}

func TestNotifyPushWaitsForSendNotiHookResult(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		pushResult: notify.PushResult{ID: "abc", QueueLen: 1},
		pushEntry:  notify.Notification{ID: "abc", Text: "deploy ok", Source: notify.SourceExternal, Session: "main"},
	}
	cmd := newCmd(store)
	events := &stubNotifyQueueEvents{}
	cmd.events = events
	runner := newGatedNotifyHookRunner(t)
	publishedBeforeHook := 0
	runner.onStart = func() { publishedBeforeHook = events.publishCalls }
	cmd.hooks = &sendNotiHookDispatcher{
		runner:    runner,
		lookupEnv: func(string) string { return "" },
		getwd:     func() (string, error) { return t.TempDir(), nil },
	}
	var runErr error
	assertCallWaitsForHookResult(t, runner, func() {
		runErr = cmd.Run([]string{"push", "--text", "deploy ok", "--target", "main:1.0"}, &bytes.Buffer{}, &bytes.Buffer{})
	})
	if runErr != nil {
		t.Fatalf("Run error = %v", runErr)
	}
	if publishedBeforeHook != 1 {
		t.Fatalf("refresh publishes before the hook = %d, want 1 so a slow hook cannot delay open sidebars", publishedBeforeHook)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
}

// realSendNotiHookPush runs `create notification` against a real notify store
// and a real hooks.Runner whose global config holds only run.
func realSendNotiHookPush(t *testing.T, run string, timeout time.Duration) (dir string, stdout, stderr string, queue []notify.Notification, err error) {
	t.Helper()
	dir = t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[hooks.send-noti]\nrun = "+strconv.Quote(run)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	store := notify.NewStore(filepath.Join(dir, "notify.json"))
	cmd := newCmd(store)
	var logs bytes.Buffer
	cmd.hooks = &sendNotiHookDispatcher{
		runner:    &hooks.Runner{GlobalConfigPath: cfg, Logger: &logs, Timeout: timeout},
		lookupEnv: func(string) string { return "" },
		getwd:     func() (string, error) { return dir, nil },
	}
	var out bytes.Buffer
	err = cmd.Run([]string{"push", "--id", "n_real", "--text", "deploy ok", "--target", "main:1.0"}, &out, &bytes.Buffer{})
	queue, listErr := store.List()
	if listErr != nil {
		t.Fatalf("list queue: %v", listErr)
	}
	return dir, out.String(), logs.String(), queue, err
}

func TestNotifyPushRealSendNotiHookCompletesBeforeReturn(t *testing.T) {
	t.Parallel()

	markerDir := t.TempDir()
	_, _, _, _, err := realSendNotiHookPush(t, `touch "`+markerDir+`/marker-$PROJMUX_NOTIFY_ID"`, hooks.DefaultPostCreateTimeout)
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "marker-n_real")); statErr != nil {
		t.Fatalf("send-noti hook marker missing right after create notification returned: %v", statErr)
	}
}

func TestNotifyPushRealSendNotiHookFailureWarnsAndSucceeds(t *testing.T) {
	t.Parallel()

	_, stdout, logs, queue, err := realSendNotiHookPush(t, "exit 3", hooks.DefaultPostCreateTimeout)
	if err != nil {
		t.Fatalf("Run error = %v, want success despite hook failure", err)
	}
	if !strings.Contains(stdout, "queued n_real") {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(queue) != 1 || queue[0].ID != "n_real" {
		t.Fatalf("queue = %#v, want the n_real entry", queue)
	}
	if !strings.Contains(logs, "exited with status 3") {
		t.Fatalf("hook warning = %q, want exited with status 3", logs)
	}
}

func TestNotifyPushRealSendNotiHookTimeoutWarnsAndReturns(t *testing.T) {
	t.Parallel()

	started := time.Now()
	_, _, logs, queue, err := realSendNotiHookPush(t, "sleep 10", 200*time.Millisecond)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run error = %v, want success despite hook timeout", err)
	}
	if len(queue) != 1 || queue[0].ID != "n_real" {
		t.Fatalf("queue = %#v, want the n_real entry", queue)
	}
	if !strings.Contains(logs, "timed out after 200ms") {
		t.Fatalf("hook warning = %q, want timed out after 200ms", logs)
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("create notification took %s, want it bounded by the hook timeout", elapsed)
	}
}
