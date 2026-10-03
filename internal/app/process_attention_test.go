package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

func newProcessAttentionFixture(t *testing.T, root string, b processhost.Binding, provider string) (*processAttentionStore, *notify.Store, *processAttentionProjection) {
	t.Helper()
	s := &processAttentionStore{path: filepath.Join(root, "attention.json")}
	q := notify.NewStore(filepath.Join(root, "notify.json"))
	if err := s.activate(b, provider, ""); err != nil {
		t.Fatal(err)
	}
	return s, q, &processAttentionProjection{store: s, queue: q}
}

func TestProcessAttentionWriterCrashHelper(t *testing.T) {
	if os.Getenv("PMX_TEST_ATTENTION_CRASH") != "1" {
		return
	}
	path := os.Getenv("PMX_TEST_ATTENTION_PATH")
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	_, _ = os.Stdin.Read(make([]byte, 1))
	os.Exit(0) // deliberately bypasses deferred unlock and close
}

func TestProcessAttentionWriterCrashReleasesPersistentLock(t *testing.T) {
	root := t.TempDir()
	s := newProcessAttentionStore(root)
	b := processhost.Binding{Pane: "pane", Host: "host", Generation: "generation"}
	if err := s.activate(b, "claude", ""); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(root, "attention.test")
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(copyPath, raw, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(copyPath, "-test.run=^TestProcessAttentionWriterCrashHelper$")
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key != "HOME" && key != "TMUX" && key != "TMUX_PANE" && key != "__PROJMUX_RUNTIME_ANCHOR_PANE" {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+root, "PMX_TEST_ATTENTION_CRASH=1", "PMX_TEST_ATTENTION_PATH="+s.path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	buf := make([]byte, 7)
	if _, err = io.ReadFull(stdout, buf); err != nil || string(buf) != "locked\n" {
		t.Fatalf("helper not locked: %q %v", buf, err)
	}
	done := make(chan error, 1)
	go func() { done <- s.clear(b, 0) }()
	select {
	case err := <-done:
		t.Fatalf("writer passed live lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// Kill this exact, owned helper. In a race binary os.Exit delays kernel
	// teardown for the race report, which is not an abrupt writer death.
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("writer unexpectedly exited normally")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.path + ".lock"); err != nil {
		t.Fatal("persistent lock inode removed", err)
	}
	if attentionRecord(t, &processAttentionStore{path: s.path}, b.Pane).Binding != b {
		t.Fatal("restart lost binding")
	}
}

func TestProcessAttentionCodexFailedTurnAndPendingExit(t *testing.T) {
	f := newProcessCodexFixture(t, func(root, binary string, env []string) processhost.Command {
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", strings.ReplaceAll(processCodexProviderFixture, "'status':'completed'", "'status':'failed'")}, Env: env, Dir: root}
	})
	b := f.endpoint.binding
	s, q, p := newProcessAttentionFixture(t, f.root, b, "codex")
	f.control.attention = p
	f.turn(t, "failed", "success")
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	notices, _ := q.List()
	if len(notices) != 1 || notices[0].Metadata[notify.MetaEvent] != "stop-failure" || notices[0].Severity != notify.SeverityCritical {
		t.Fatalf("failed turn: %+v", notices)
	}
	f.turn(t, "pending-exit", "controls")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.endpoint.handle.Stop(b); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.endpoint.handle.Wait(ctx, b); err != nil {
		t.Fatal(err)
	}
	_ = f.control.sync(context.Background())
	r := attentionRecord(t, s, b.Pane)
	if !r.Terminal || len(r.Pending) != 0 {
		t.Fatalf("Codex pending after exit: %+v", r)
	}
}

func TestProcessAttentionClaudeFailedResultRemainsSeparateFromExit(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	s, q, p := newProcessAttentionFixture(t, f.root, f.binding, "claude")
	f.control.attention = p
	f.turn(t, "failure-interrupt", "interrupt")
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
	snap, _ := f.handle.Observe(f.binding)
	if err := f.handle.Interrupt(context.Background(), processhost.Authority{Binding: f.binding, Connection: snap.Connection, Session: snap.Session}, snap.Turn); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := attentionRecord(t, s, f.binding.Pane)
	if r.Terminal || r.NoticeKind != "error" || r.badge() != aibadge.ResponseComplete {
		t.Fatalf("failed turn became exit: %+v", r)
	}
	n, _ := q.List()
	if len(n) != 1 || !protectedAINotifyEvent(n[0]) {
		t.Fatalf("failed result missing: %+v", n)
	}
}

func attentionRecord(t *testing.T, s *processAttentionStore, pane string) processAttentionRecord {
	t.Helper()
	r, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	return r[pane]
}

type processAttentionRetryMeasurement struct {
	attempts, timeouts int
	longest            time.Duration
}

// Each attempt models a fresh sync: only a lock timeout is retried, and the
// store callback is never replayed automatically after a timed-out acquisition.
func activateProcessAttentionWithRetry(t *testing.T, s *processAttentionStore, b processhost.Binding) (processAttentionRetryMeasurement, error) {
	t.Helper()
	var measured processAttentionRetryMeasurement
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		began := time.Now()
		err = s.activate(b, "claude", "")
		measured.attempts++
		measured.longest = max(measured.longest, time.Since(began))
		if !errors.Is(err, localstate.ErrLockTimeout) {
			return measured, err
		}
		measured.timeouts++
		t.Logf("writer %s attempt %d: explicit lock timeout: %v", b.Pane, attempt, err)
	}
	return measured, err
}

func TestProcessAttentionConcurrentWritersRestartAndStaleGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.json")
	var wg sync.WaitGroup
	measurements := make(chan processAttentionRetryMeasurement, 24)
	for i := range 24 {
		wg.Go(func() {
			s := &processAttentionStore{path: path}
			b := processhost.Binding{Pane: fmt.Sprintf("pane-%d", i), Host: "host", Generation: "old"}
			measured, err := activateProcessAttentionWithRetry(t, s, b)
			if err != nil {
				t.Errorf("writer %s exhausted bounded attempts: %v", b.Pane, err)
			}
			measurements <- measured
		})
	}
	wg.Wait()
	close(measurements)
	var retries, timeouts int
	var longest time.Duration
	for measured := range measurements {
		retries += measured.attempts - 1
		timeouts += measured.timeouts
		longest = max(longest, measured.longest)
	}
	t.Logf("24 durable writers: retries=%d explicit timeouts=%d max attempt duration (lock-wait upper bound)=%s", retries, timeouts, longest)
	s := &processAttentionStore{path: path}
	records, err := s.read()
	if err != nil || len(records) != 24 {
		t.Fatalf("lost writer: %d %v", len(records), err)
	}
	old := records["pane-0"].Binding
	if err = s.update(func(r map[string]processAttentionRecord) error {
		x := r[old.Pane]
		x.Sequence = 7
		x.Badge = aibadge.ResponseComplete
		r[old.Pane] = x
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := old
	next.Generation = "next"
	if err = s.activate(next, "codex", "old"); err != nil {
		t.Fatal(err)
	}
	if err = s.activate(old, "claude", ""); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("old activation: %v", err)
	}
	if err = s.clear(old, 7); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("old ack: %v", err)
	}
	if err = s.update(func(r map[string]processAttentionRecord) error {
		x := r[next.Pane]
		x.Sequence = 8
		x.Pending["request"] = processAttentionPending{Kind: aibadge.InputRequired, Sequence: 8}
		r[next.Pane] = x
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restarted := &processAttentionStore{path: path}
	if err = restarted.clear(next, 7); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("stale same-generation ack: %v", err)
	}
	if got := attentionRecord(t, restarted, next.Pane).badge(); got != aibadge.InputRequired {
		t.Fatalf("new question cleared: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private store: %v %v", info, err)
	}
}

func TestProcessAttentionClaudeConsumerAndHostTermination(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	s, q, projection := newProcessAttentionFixture(t, f.root, f.binding, "claude")
	f.control.attention = projection
	f.turn(t, "attention-question", "question")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := attentionRecord(t, s, f.binding.Pane)
	if r.badge() != aibadge.InputRequired {
		t.Fatalf("question badge: %+v", r)
	}
	consumer := &processAttentionConsumer{store: s, bindings: []processhost.Binding{f.binding}}
	cmd := &attentionCommand{process: consumer}
	if err := cmd.runClear([]string{f.binding.Pane}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := attentionRecord(t, s, f.binding.Pane).badge(); got != aibadge.InputRequired {
		t.Fatalf("question ack: %s", got)
	}
	questions, _ := f.control.questions.List(f.binding.Agent)
	if _, err := f.control.questions.Answer(questions[0].ID, f.binding.Agent, map[string]string{"Color?": "blue"}); err != nil {
		t.Fatal(err)
	}
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := attentionRecord(t, s, f.binding.Pane).badge(); got != aibadge.ResponseComplete {
		t.Fatalf("complete badge: %s", got)
	}
	before, _ := q.List()
	if err := cmd.runClear([]string{f.binding.Pane}, io.Discard); err != nil {
		t.Fatal(err)
	}
	after, _ := q.List()
	if len(before) != 2 || len(after) != len(before) {
		t.Fatalf("clear consumed queue: %d/%d", len(before), len(after))
	}
	if got := attentionRecord(t, s, f.binding.Pane).badge(); got != "" {
		t.Fatalf("complete not consumed: %s", got)
	}
	f.turn(t, "attention-permission", "permission")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := attentionRecord(t, s, f.binding.Pane).badge(); got != aibadge.ApprovalRequired {
		t.Fatalf("approval badge: %s", got)
	}
	if err := f.handle.Stop(f.binding); err != nil {
		t.Fatal(err)
	}
	_ = f.control.sync(context.Background()) // an intermediate stopping snapshot must retain the cancellation evidence
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.handle.Wait(ctx, f.binding); err != nil {
		t.Fatal(err)
	}
	_ = f.control.sync(context.Background()) // authority closes; attention still observes actual Wait
	r = attentionRecord(t, s, f.binding.Pane)
	if !r.Terminal || len(r.Pending) != 0 {
		t.Fatalf("pending survived exit: %+v", r)
	}
	entries, err := q.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range entries {
		if n.Metadata[notify.MetaEvent] == "stop-failure" {
			found = true
			if !protectedAINotifyEvent(n) {
				t.Fatal("unprotected error")
			}
		}
	}
	if !found {
		t.Fatal("exit lost attention error")
	}
	raw, _ := os.ReadFile(s.path)
	for _, secret := range []string{"Color?", "Blue", "printf fixture", "tool_input", "wire_echo"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("stored conversation: %s", secret)
		}
	}
}

func TestProcessAttentionQueueFailureRetriesWithoutLosingCursor(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	b := f.endpoint.binding
	s, q, p := newProcessAttentionFixture(t, f.root, b, "codex")
	blocker := filepath.Join(f.root, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	p.queue = notify.NewStore(filepath.Join(blocker, "notify.json"))
	f.turn(t, "queue-retry", "success")
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if err := p.sync(f.endpoint.handle, b); err == nil {
		t.Fatal("queue failure concealed")
	}
	if attentionRecord(t, s, b.Pane).Sequence != 0 {
		t.Fatal("cursor committed without notice")
	}
	p.queue = q
	if err := p.sync(f.endpoint.handle, b); err != nil {
		t.Fatal(err)
	}
	if err := p.sync(f.endpoint.handle, b); err != nil {
		t.Fatal(err)
	}
	n, _ := q.List()
	if len(n) != 1 {
		t.Fatalf("retry duplicated notice: %d", len(n))
	}
	f.turn(t, "next-completion", "success")
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if err := p.sync(f.endpoint.handle, b); err != nil {
		t.Fatal(err)
	}
	n, _ = q.List()
	if len(n) != 1 {
		t.Fatalf("older noncritical completion not compacted: %d", len(n))
	}
	old := b
	next := b
	next.Generation = "next-generation"
	if err := s.activate(next, "codex", old.Generation); err != nil {
		t.Fatal(err)
	}
	if err := p.sync(f.endpoint.handle, old); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("old host event after restart: %v", err)
	}
	if got := attentionRecord(t, s, b.Pane); got.Binding != next || got.Sequence != 0 {
		t.Fatalf("new generation overwritten: %+v", got)
	}
}

func TestProcessAttentionStreamGapKeepsRetainedCriticalRequests(t *testing.T) {
	f := newProcessCodexFixtureWithEvents(t, func(root, binary string, env []string) processhost.Command {
		script := strings.ReplaceAll(processCodexProviderFixture, "import os,sys,json", "import os,sys,json,time")
		script = strings.ReplaceAll(script, "emit(a);emit(q);emit(a);emit(q)", "emit(a);emit(q);emit(a);emit(q)\n   for i in range(32):\n    token=os.path.join(os.environ['HOME'],'attention-next')\n    while not os.path.exists(token):time.sleep(0.001)\n    os.unlink(token)\n    notify('item/agentMessage/delta',{'threadId':'process-thread','turnId':current,'delta':'not-stored'})")
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", script}, Dir: root, Env: env}
	}, 8)
	b := f.endpoint.binding
	s, q, p := newProcessAttentionFixture(t, f.root, b, "codex")
	f.turn(t, "gap-controls", "controls")
	snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	// Advance each output only after the previous host observation. This fills
	// the output ring without racing the separate app-server transport backlog.
	for range 32 {
		if err := os.WriteFile(filepath.Join(f.root, "attention-next"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		after := snap.Sequence
		snap = f.wait(t, func(s processhost.Snapshot) bool { return s.Sequence > after })
	}
	if err := p.sync(f.endpoint.handle, b); err != nil {
		t.Fatal(err)
	}
	r := attentionRecord(t, s, b.Pane)
	if len(r.Pending) != 2 || aibadge.Priority(r.badge()) != 3 {
		t.Fatalf("gap hid pending: %+v", r)
	}
	n, _ := q.List()
	if len(n) != 3 {
		t.Fatalf("retained request notices lost: %+v", n)
	}
	raw, _ := os.ReadFile(s.path)
	if bytes.Contains(raw, []byte("not-stored")) {
		t.Fatal("stored provider output")
	}
}

func TestProcessAttentionCodexPriorityAckRetentionAndConsumerIntegration(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	b := f.endpoint.binding
	s, q, projection := newProcessAttentionFixture(t, f.root, b, "codex")
	f.control.attention = projection
	f.turn(t, "attention-controls", "controls")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := attentionRecord(t, s, b.Pane)
	if aibadge.Priority(r.badge()) != 3 || len(r.Pending) != 2 {
		t.Fatalf("priority: %+v", r)
	}
	consumer := &processAttentionConsumer{store: s, bindings: []processhost.Binding{b}}
	lister := attentionLivePaneLister{process: consumer}
	rows, err := lister.ListLivePanes()
	if err != nil || len(rows) != 2 {
		t.Fatalf("process-only rows %v %v", rows, err)
	}
	cmd := &notifyCommand{store: q, livePanes: lister, process: consumer}
	if err = cmd.runReconcile(nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	entries, _ := q.List()
	if len(entries) != 2 {
		t.Fatalf("reconcile duplicated: %d", len(entries))
	}
	for _, n := range entries {
		if n.Severity != notify.SeverityCritical || n.ExpiresAt.Sub(n.CreatedAt) != attentionNotifyTTL {
			t.Fatalf("request policy: %+v", n)
		}
		if err = cmd.focusNotification(n, "", "", ""); err == nil {
			t.Fatal("process focus admitted tmux")
		}
	}
	f.answerControls(t)
	if err = f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	r = attentionRecord(t, s, b.Pane)
	if r.badge() != aibadge.ResponseComplete || len(r.Pending) != 0 {
		t.Fatalf("turn completion: %+v", r)
	}
	entries, _ = q.List()
	if len(entries) != 3 {
		t.Fatalf("completion queue: %d", len(entries))
	}
	q.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	evicted, err := q.Reconcile(func(notify.Notification) bool { return true })
	if err != nil || evicted.Removed() != 0 {
		t.Fatalf("expired live retained: %+v %v", evicted, err)
	}
	old := b
	old.Generation = "old"
	if err = projection.sync(f.endpoint.handle, old); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("stale event: %v", err)
	}
	if err = s.clear(old, r.Sequence); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("stale ack: %v", err)
	}
	if err = s.clear(b, r.Sequence); err != nil {
		t.Fatal(err)
	}
	entries, _ = q.List()
	if len(entries) != 3 {
		t.Fatal("badge clear changed retention")
	}
	if err = q.Ack(processAttentionID(b, r.NoticeSequence)); err != nil {
		t.Fatal(err)
	}
	entries, _ = q.List()
	if len(entries) != 2 {
		t.Fatal("explicit ack failed")
	}
	evicted, err = q.Reconcile(func(notify.Notification) bool { return false })
	if err != nil || evicted.Removed() != 2 {
		t.Fatalf("expired gone parity: %+v %v", evicted, err)
	}
}

func TestProcessAttentionMixedInventoryFailurePreservesUnknownTmux(t *testing.T) {
	b := processhost.Binding{Pane: "process-pane", Project: "process-project", Window: "window", Host: "host", Generation: "generation"}
	s, q, _ := newProcessAttentionFixture(t, t.TempDir(), b, "claude")
	if err := s.update(func(records map[string]processAttentionRecord) error {
		r := records[b.Pane]
		r.Badge = aibadge.ResponseComplete
		r.NoticeSequence = 1
		r.NoticeKind = aibadge.ResponseComplete
		records[b.Pane] = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	consumer := &processAttentionConsumer{store: s, bindings: []processhost.Binding{b}}
	failure := errors.New("tmux fixture unavailable")
	runner := &recordingAttentionRunner{err: failure}
	attention := &attentionCommand{runner: runner, process: consumer}
	rows, err := attention.listAttentionPanes()
	if !errors.Is(err, failure) || len(rows) != 1 {
		t.Fatalf("partial attention: %v %v", rows, err)
	}
	var out bytes.Buffer
	if err = attention.runList([]string{"--json"}, &out, io.Discard); !errors.Is(err, failure) || !strings.Contains(out.String(), b.Pane) {
		t.Fatalf("attention output: %s %v", out.String(), err)
	}
	_, _, err = q.Push(notify.PushInput{ID: "ai:tmux-old", Text: "retained", TTL: time.Minute, Target: notify.Target{Session: "tmux-session", Pane: "%1"}})
	if err != nil {
		t.Fatal(err)
	}
	q.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	cmd := &notifyCommand{store: q, livePanes: attentionLivePaneLister{runner: runner, process: consumer}}
	out.Reset()
	if err = cmd.runReconcile([]string{"--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), failure.Error()) || !strings.Contains(out.String(), `"evicted": 0`) {
		t.Fatalf("summary: %s", out.String())
	}
	entries, err := q.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("retention: %+v %v", entries, err)
	}
	report, err := cmd.buildNotifyLiveReport(entries)
	if err != nil || len(report.Live) != 1 || len(report.Errors) != 1 || !strings.Contains(report.Errors[0], failure.Error()) {
		t.Fatalf("report: %+v %v", report, err)
	}
}

func TestProcessAttentionEventStateMachine(t *testing.T) {
	for _, tc := range []struct {
		event       processhost.Event
		kind, badge string
		terminal    bool
	}{
		{event: processhost.Event{Kind: "turn-submitted"}, badge: aibadge.InProgress},
		{event: processhost.Event{Kind: "control-pending", Request: &processhost.Request{ID: "q", Kind: "question"}}, kind: aibadge.InputRequired},
		{event: processhost.Event{Kind: "control-pending", Request: &processhost.Request{ID: "a", Kind: "permission"}}, kind: aibadge.ApprovalRequired},
		{event: processhost.Event{Kind: "turn-result", Raw: []byte(`{"subtype":"success"}`)}, kind: aibadge.ResponseComplete, badge: aibadge.ResponseComplete},
		{event: processhost.Event{Kind: "turn-result", Raw: []byte(`{"is_error":true}`)}, kind: "error", badge: aibadge.ResponseComplete},
		{event: processhost.Event{Kind: "stream-gap"}, kind: "error", badge: aibadge.ResponseComplete},
		{event: processhost.Event{Kind: "process-exited"}, terminal: true},
	} {
		t.Run(tc.event.Kind+tc.kind, func(t *testing.T) {
			r := processAttentionRecord{Pending: map[string]processAttentionPending{}}
			kind, err := r.applyEvent(tc.event)
			if err != nil || kind != tc.kind || r.Badge != tc.badge || r.Terminal != tc.terminal {
				t.Fatalf("state: %+v kind %s err %v", r, kind, err)
			}
		})
	}
	r := processAttentionRecord{Pending: map[string]processAttentionPending{"q": {Kind: aibadge.InputRequired, Sequence: 1}, "a": {Kind: aibadge.ApprovalRequired, Sequence: 2}}}
	_, err := r.applyEvent(processhost.Event{Kind: "control-answered", Request: &processhost.Request{ID: "q"}})
	if err != nil || len(r.Pending) != 1 {
		t.Fatalf("answer: %+v %v", r, err)
	}
	pushes := 0
	if err = r.reconcilePending(processhost.Snapshot{Sequence: 3, Pending: []processhost.Request{{ID: "a", Kind: "permission"}}}, func(notify.PushInput) error { pushes++; return nil }); err != nil || pushes != 0 || r.Pending["a"].Sequence != 2 {
		t.Fatalf("reconcile: %+v %v", r, err)
	}
	_, err = r.applyEvent(processhost.Event{Kind: "control-expired", Request: &processhost.Request{ID: "a"}})
	if err != nil || !r.PendingEnded {
		t.Fatal("expiry lost")
	}
	kind, err := r.applyEvent(processhost.Event{Kind: "process-exited"})
	if err != nil || kind != "error" {
		t.Fatal("terminal pending loss")
	}
	if err = r.reconcilePending(processhost.Snapshot{Sequence: 4, Pending: []processhost.Request{{ID: "a"}}}, func(notify.PushInput) error { t.Fatal("terminal replay"); return nil }); err != nil || len(r.Pending) != 0 {
		t.Fatalf("terminal: %+v %v", r, err)
	}
}

// Holding the real lock is the input; contention and the persisted result are
// the assertions. The delay models a durable writer slower than the picker
// budget, rather than a window in which a goroutine is assumed to run.
func TestProcessAttentionLockWaitsBeyondPickerBudget(t *testing.T) {
	s := newProcessAttentionStore(t.TempDir())
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	contended := make(chan struct{})
	s.afterContendedLock = func() { close(contended) }
	b := processhost.Binding{Pane: "pane", Host: "host", Generation: "generation"}
	done := make(chan error, 1)
	go func() { done <- s.activate(b, "claude", "") }()
	<-contended
	time.Sleep(600 * time.Millisecond)
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if attentionRecord(t, s, b.Pane).Binding != b {
		t.Fatal("writer lost after contention")
	}
}

func TestProcessAttentionLockTimeoutDoesNotRunChange(t *testing.T) {
	s := newProcessAttentionStore(t.TempDir())
	b := processhost.Binding{Pane: "pane", Host: "host", Generation: "generation"}
	if err := s.activate(b, "claude", ""); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	s.lockWaitLimit = 20 * time.Millisecond
	calls := 0
	change := func(records map[string]processAttentionRecord) error {
		calls++
		r := records[b.Pane]
		r.Sequence++
		records[b.Pane] = r
		return nil
	}
	if err = s.update(change); !errors.Is(err, localstate.ErrLockTimeout) {
		t.Fatalf("held lock: %v", err)
	}
	if calls != 0 || attentionRecord(t, s, b.Pane).Sequence != 0 {
		t.Fatal("timed out writer changed state")
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	s.lockWaitLimit = 0
	if err = s.update(change); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || attentionRecord(t, s, b.Pane).Sequence != 1 {
		t.Fatal("retry did not commit exactly once")
	}
}

func assertProcessAttentionControlProgress(t *testing.T, s *processAttentionStore, syncControl func() error, waitCompleted func(), nextTurn func()) {
	t.Helper()
	lock, err := os.OpenFile(s.path+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	contended := make(chan struct{}, 2)
	s.afterContendedLock = func() { contended <- struct{}{} }
	done := make(chan error, 2)
	calls := 0
	defer func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		for range calls {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	began := time.Now()
	go func() { done <- syncControl() }()
	calls++
	<-contended
	// The provider's completion is observed while the attention lock remains
	// held. A second control reconciliation must also pass the first waiter.
	waitCompleted()
	nextTurn()
	go func() { done <- syncControl() }()
	calls++
	<-contended
	waitCompleted()
	t.Logf("response and next turn completed with attention locked: %s", time.Since(began))
}

func TestProcessAttentionLockDoesNotBlockClaudeControl(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	s, _, p := newProcessAttentionFixture(t, f.root, f.binding, "claude")
	f.control.attention = p
	f.turn(t, "question", "question")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	q, err := f.control.questions.List(f.binding.Agent)
	if err != nil || len(q) != 1 {
		t.Fatalf("questions: %v %v", q, err)
	}
	if _, err = f.control.questions.Answer(q[0].ID, f.binding.Agent, map[string]string{"Color?": "blue"}); err != nil {
		t.Fatal(err)
	}
	assertProcessAttentionControlProgress(t, s,
		func() error { return f.control.sync(context.Background()) },
		func() { f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" }) },
		func() { f.turn(t, "next", "success") })
}

func TestProcessAttentionLockDoesNotBlockCodexControl(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	b := f.endpoint.binding
	s, _, p := newProcessAttentionFixture(t, f.root, b, "codex")
	f.control.attention = p
	f.turn(t, "controls", "controls")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	q, err := f.control.questions.List(b.Agent)
	if err != nil || len(q) != 1 {
		t.Fatalf("questions: %v %v", q, err)
	}
	a, err := f.control.approvals.List(b.Agent)
	if err != nil || len(a) != 1 {
		t.Fatalf("approvals: %v %v", a, err)
	}
	if _, err = f.control.questions.Answer(q[0].ID, b.Agent, map[string]string{"q": `["blue"]`}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.control.approvals.Answer(a[0].ID, b.Agent, false, "fixture"); err != nil {
		t.Fatal(err)
	}
	assertProcessAttentionControlProgress(t, s,
		func() error { return f.control.sync(context.Background()) },
		func() { f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" }) },
		func() { f.turn(t, "next", "success") })
}

func TestProcessAttentionConcurrentWritersRetryAfterTimeout(t *testing.T) {
	root := t.TempDir()
	path := newProcessAttentionStore(root).path
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	contended := make(chan struct{}, 24)
	callbacks := make(chan struct{}, 24)
	first := make(chan error, 24)
	stores := make([]*processAttentionStore, 24)
	bindings := make([]processhost.Binding, 24)
	for i := range 24 {
		b := processhost.Binding{Pane: fmt.Sprintf("pane-%d", i), Host: "host", Generation: "generation"}
		s := &processAttentionStore{path: path, lockWaitLimit: 20 * time.Millisecond, afterContendedLock: func() { contended <- struct{}{} }}
		stores[i], bindings[i] = s, b
		go func() {
			first <- s.update(func(records map[string]processAttentionRecord) error {
				callbacks <- struct{}{}
				records[b.Pane] = processAttentionRecord{Binding: b, Sequence: 1}
				return nil
			})
		}()
	}
	for range 24 {
		<-contended
	}
	for range 24 {
		if err = <-first; !errors.Is(err, localstate.ErrLockTimeout) {
			t.Fatalf("first attempt: %v", err)
		}
	}
	records, err := (&processAttentionStore{path: path}).read()
	if err != nil || len(records) != 0 || len(callbacks) != 0 {
		t.Fatalf("timeout committed: records=%d callbacks=%d err=%v", len(records), len(callbacks), err)
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}

	results := make(chan processAttentionRetryMeasurement, 24)
	var wg sync.WaitGroup
	for i := range 24 {
		stores[i].lockWaitLimit, stores[i].afterContendedLock = 0, nil
		wg.Go(func() {
			measured, retryErr := activateProcessAttentionWithRetry(t, stores[i], bindings[i])
			if retryErr != nil {
				t.Errorf("retry %s: %v", bindings[i].Pane, retryErr)
			}
			results <- measured
		})
	}
	wg.Wait()
	close(results)
	var retries = 24 // each writer starts a new sync after its explicit timeout
	var longest time.Duration
	for measured := range results {
		retries += measured.attempts - 1
		longest = max(longest, measured.longest)
	}
	records, err = (&processAttentionStore{path: path}).read()
	if err != nil || len(records) != 24 || len(callbacks) != 0 {
		t.Fatalf("retry lost state: records=%d callbacks=%d err=%v", len(records), len(callbacks), err)
	}
	for _, b := range bindings {
		if r := records[b.Pane]; r.Binding != b || r.Sequence != 0 {
			t.Fatalf("late timed-out callback or lost writer: %+v", r)
		}
	}
	t.Logf("explicit first timeouts=24 callbacks=0; final records=24 retries=%d max retry duration (lock-wait upper bound)=%s", retries, longest)
}

func TestProcessAttentionProjectionLockTimeoutRetriesCursor(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	b := f.endpoint.binding
	s, queue, projection := newProcessAttentionFixture(t, f.root, b, "codex")
	f.turn(t, "attention-cursor", "controls")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	before := attentionRecord(t, s, b.Pane)
	lock, err := os.OpenFile(s.path+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	s.lockWaitLimit = 20 * time.Millisecond
	if err = projection.sync(f.endpoint.handle, b); !errors.Is(err, localstate.ErrLockTimeout) {
		t.Fatalf("first sync: %v", err)
	}
	after := attentionRecord(t, s, b.Pane)
	notices, err := queue.List()
	if err != nil || after.Sequence != before.Sequence || len(after.Pending) != 0 || len(notices) != 0 {
		t.Fatalf("timeout consumed events: %+v notices=%d err=%v", after, len(notices), err)
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	s.lockWaitLimit = 0
	for range 2 {
		if err = projection.sync(f.endpoint.handle, b); err != nil {
			t.Fatal(err)
		}
	}
	after = attentionRecord(t, s, b.Pane)
	notices, err = queue.List()
	if err != nil || after.Sequence <= before.Sequence || len(after.Pending) != 2 || len(notices) != 2 {
		t.Fatalf("retry lost or duplicated events: %+v notices=%d err=%v", after, len(notices), err)
	}
}
