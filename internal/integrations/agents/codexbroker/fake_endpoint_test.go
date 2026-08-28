package codexbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// errOpenerExhausted is returned once a scripted opener has handed out every
// connection its test declared. It keeps the supervisor in its normal backoff
// path instead of ending the test with a panic.
var errOpenerExhausted = errors.New("fake endpoint script exhausted")

// fakeEndpoint is an in-memory app-server connection. It has no process, no
// socket, and no wire, so every ordering a test needs is produced by explicit
// gates rather than by timing.
//
// Each call site the broker uses can be held: BootstrapThread on "snapshot:"
// plus the thread id, Request on "request:" plus the method. A held call
// blocks until the test releases it, the endpoint closes, or the context ends,
// which is exactly the three ways the real client can finish.
type fakeEndpoint struct {
	mu        sync.Mutex
	closed    bool
	events    chan codexappserver.Notification
	gates     map[string]chan struct{}
	failures  map[string]error
	requests  []string
	snapshots []string
	answers   []string

	visits map[string]int
	dead   chan struct{}
}

func newFakeEndpoint() *fakeEndpoint {
	return &fakeEndpoint{
		events:   make(chan codexappserver.Notification, 256),
		gates:    make(map[string]chan struct{}),
		failures: make(map[string]error),
		visits:   make(map[string]int),
		dead:     make(chan struct{}),
	}
}

func (f *fakeEndpoint) Notifications() <-chan codexappserver.Notification { return f.events }

func (f *fakeEndpoint) Request(ctx context.Context, method string, _, result any) error {
	key := "request:" + method
	f.mu.Lock()
	f.requests = append(f.requests, method)
	f.mu.Unlock()
	f.note(key)
	if err := f.wait(ctx, key); err != nil {
		return err
	}
	f.mu.Lock()
	failure := f.failures[key]
	f.mu.Unlock()
	if failure != nil {
		return failure
	}
	// Echoing the method into the caller's result target is what lets a
	// concurrency test prove that an out-of-order response reached the exact
	// caller that asked for it.
	if target, ok := result.(*string); ok {
		*target = method
	}
	return nil
}

func (f *fakeEndpoint) RespondServerRequest(ctx context.Context, rawID json.RawMessage, _ any) error {
	key := "respond:" + string(rawID)
	f.note(key)
	if err := f.wait(ctx, key); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return codexappserver.ErrDisconnected
	}
	f.answers = append(f.answers, string(rawID))
	return nil
}

func (f *fakeEndpoint) BootstrapThread(ctx context.Context, threadID, cwd string, _ []string) (codexappserver.ThreadSnapshot, error) {
	key := "snapshot:" + threadID
	f.note(key)
	if err := f.wait(ctx, key); err != nil {
		return codexappserver.ThreadSnapshot{}, err
	}
	f.mu.Lock()
	failure := f.failures[key]
	if failure == nil && !f.closed {
		f.snapshots = append(f.snapshots, threadID)
	}
	closed := f.closed
	f.mu.Unlock()
	switch {
	case failure != nil:
		return codexappserver.ThreadSnapshot{}, failure
	case closed:
		return codexappserver.ThreadSnapshot{}, codexappserver.ErrDisconnected
	}
	return codexappserver.ThreadSnapshot{ThreadID: threadID, CWD: cwd, RuntimeStatus: "idle"}, nil
}

func (f *fakeEndpoint) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	close(f.events)
	close(f.dead)
	f.mu.Unlock()
	return nil
}

// hold registers a gate so the next call at key blocks until release.
func (f *fakeEndpoint) hold(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.gates[key]; !exists {
		f.gates[key] = make(chan struct{})
	}
}

// release opens a previously held gate.
func (f *fakeEndpoint) release(key string) {
	f.mu.Lock()
	gate := f.gates[key]
	delete(f.gates, key)
	f.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

// fail makes the call at key return err after its gate opens.
func (f *fakeEndpoint) fail(key string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[key] = err
}

func (f *fakeEndpoint) wait(ctx context.Context, key string) error {
	f.mu.Lock()
	gate := f.gates[key]
	f.mu.Unlock()
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-f.dead:
		return codexappserver.ErrDisconnected
	case <-ctx.Done():
		return ctx.Err()
	}
}

// note records that a call site was entered, so a test can wait for the broker
// to reach it instead of sleeping.
func (f *fakeEndpoint) note(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visits[key]++
}

// visited reports how often the broker has entered one call site.
func (f *fakeEndpoint) visited(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.visits[key]
}

// push delivers one notification unless the endpoint has already closed. The
// send is non-blocking so a fault-injection test can never wedge the endpoint
// lock against Close.
func (f *fakeEndpoint) push(notification codexappserver.Notification) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	select {
	case f.events <- notification:
	default:
	}
}

func (f *fakeEndpoint) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeEndpoint) bootstrapped() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.snapshots...)
}

func (f *fakeEndpoint) answered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.answers...)
}

// scriptedOpener hands out one declared connection per open attempt. A nil
// endpoint entry is a failed attach, which is how a test drives the reconnect
// backoff without an unreachable socket.
type scriptedOpener struct {
	mu    sync.Mutex
	steps []*fakeEndpoint
	calls int
}

func (o *scriptedOpener) open(context.Context) (Endpoint, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	index := o.calls
	o.calls++
	if index >= len(o.steps) || o.steps[index] == nil {
		return nil, errOpenerExhausted
	}
	return o.steps[index], nil
}

// recyclingOpener always succeeds and always hands out a brand new endpoint,
// so a stress test can replace the connection as often as it likes.
type recyclingOpener struct {
	mu      sync.Mutex
	current *fakeEndpoint
	calls   int
}

func (o *recyclingOpener) open(context.Context) (Endpoint, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.current = newFakeEndpoint()
	return o.current, nil
}

func (o *recyclingOpener) live() *fakeEndpoint {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.current
}

func (o *scriptedOpener) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

// fakeClock is the injected supervisor clock. Advancing it is the only way
// time passes, so a multi-second outage costs a test no wall time and produces
// exactly one reconnect schedule.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	deadline time.Time
	signal   chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1700000000, 0).UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	signal := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{deadline: c.now.Add(d), signal: signal})
	return signal
}

// Advance moves the clock forward and fires every waiter it passes.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var fired []fakeWaiter
	var kept []fakeWaiter
	for _, waiter := range c.waiters {
		if !waiter.deadline.After(c.now) {
			fired = append(fired, waiter)
			continue
		}
		kept = append(kept, waiter)
	}
	c.waiters = kept
	now := c.now
	c.mu.Unlock()
	for _, waiter := range fired {
		waiter.signal <- now
	}
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// threadEvent builds one notification attributed to threadID.
func threadEvent(threadID, method string) codexappserver.Notification {
	return codexappserver.Notification{
		Method: method,
		Params: json.RawMessage(fmt.Sprintf(`{"threadId":%q,"itemId":"item-1"}`, threadID)),
	}
}

// serverRequestEvent builds one inbound server request attributed to threadID.
func serverRequestEvent(threadID, method, rawID string) codexappserver.Notification {
	return codexappserver.Notification{
		Method:       method,
		Params:       json.RawMessage(fmt.Sprintf(`{"threadId":%q,"turnId":"turn-1","itemId":"item-1"}`, threadID)),
		RequestID:    rawID,
		RawRequestID: json.RawMessage(rawID),
	}
}

// waitUntil polls a mutex-guarded condition to a bounded deadline. Every
// condition it is used with is broker or endpoint state that a test has
// already caused, so this waits for a scheduler, never for a timeout.
func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Microsecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// nextEvent reads one delivery, or fails the test if the stream stalls or ends.
func nextEvent(t *testing.T, binding *Binding) Event {
	t.Helper()
	select {
	case event, ok := <-binding.Events():
		if !ok {
			t.Fatalf("%s stream closed early: %s", binding.ThreadID(), binding.Revocation())
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatalf("%s delivered no event", binding.ThreadID())
		return Event{}
	}
}

// awaitSnapshot reads the one snapshot event that opens every epoch's stream.
func awaitSnapshot(t *testing.T, binding *Binding) Event {
	t.Helper()
	event := nextEvent(t, binding)
	if event.Origin != EventOriginSnapshot || event.Snapshot.ThreadID != binding.ThreadID() {
		t.Fatalf("first event = %+v, want the barrier snapshot for %s", event, binding.ThreadID())
	}
	return event
}

// assertQuiet fails when a binding has any undelivered event.
func assertQuiet(t *testing.T, binding *Binding) {
	t.Helper()
	select {
	case event, ok := <-binding.Events():
		if ok {
			t.Fatalf("%s received an unexpected event: %+v", binding.ThreadID(), event)
		}
		t.Fatalf("%s stream closed: %s", binding.ThreadID(), binding.Revocation())
	default:
	}
}

// assertNoGoroutineLeak requires the broker's goroutines to be gone. One
// scheduler slot of tolerance matches the existing app-server leak assertions.
func assertNoGoroutineLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

// newTestBroker starts a broker over the declared connection script.
func newTestBroker(t *testing.T, backlog int, steps ...*fakeEndpoint) (*Broker, *scriptedOpener, *fakeClock) {
	t.Helper()
	opener := &scriptedOpener{steps: steps}
	clock := newFakeClock()
	broker, err := NewBroker(Config{
		Opener:  opener.open,
		Clock:   clock,
		Jitter:  func() float64 { return 1 },
		Backlog: backlog,
	})
	if err != nil {
		t.Fatalf("NewBroker() = %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	return broker, opener, clock
}

// boundBinding binds one thread and waits until its barrier has closed.
func boundBinding(t *testing.T, broker *Broker, threadID string) (*Binding, Fence) {
	t.Helper()
	binding, err := broker.Bind(threadID, "/work/project", nil)
	if err != nil {
		t.Fatalf("Bind(%s) = %v", threadID, err)
	}
	awaitSnapshot(t, binding)
	fence, err := binding.ControlAuthority()
	if err != nil {
		t.Fatalf("ControlAuthority(%s) = %v", threadID, err)
	}
	return binding, fence
}
