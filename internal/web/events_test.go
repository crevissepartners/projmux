package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventBackend serves a system body the test controls and signals on demand.
type eventBackend struct {
	fakeBackend
	mu      sync.Mutex
	system  any
	fail    error
	signals chan struct{}
}

func (b *eventBackend) System(context.Context) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.system, b.fail
}

func (b *eventBackend) set(system any, fail error) {
	b.mu.Lock()
	b.system, b.fail = system, fail
	b.mu.Unlock()
	b.signals <- struct{}{}
}

func (b *eventBackend) Changes(ctx context.Context, topic string) (<-chan struct{}, error) {
	if topic != TopicSystem {
		return nil, errors.New("only system in this test")
	}
	return b.signals, nil
}

type sseFrame struct{ event, data, id string }

func readFrames(t *testing.T, scanner *bufio.Scanner, frames chan<- sseFrame) {
	t.Helper()
	var frame sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			frame.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			frame.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			frame.data = strings.TrimPrefix(line, "data: ")
		case line == "" && frame.event != "":
			frames <- frame
			frame = sseFrame{}
		}
	}
	close(frames)
}

func nextFrame(t *testing.T, frames <-chan sseFrame) sseFrame {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if !ok {
			t.Fatal("stream closed")
		}
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("no frame")
	}
	return sseFrame{}
}

func TestEventsSendOnChangeOnly(t *testing.T) {
	backend := &eventBackend{system: map[string]int{"cpu": 1}, signals: make(chan struct{}, 4)}
	srv := httptest.NewServer(New(backend, nil).Handler())
	defer srv.Close()

	ctx := t.Context()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events?topics=system", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	frames := make(chan sseFrame, 8)
	go readFrames(t, bufio.NewScanner(res.Body), frames)

	// The current body is sent at once, without waiting for a change.
	if got := nextFrame(t, frames); got.event != TopicSystem || got.data != `{"cpu":1}` {
		t.Fatalf("first frame = %+v", got)
	}
	// A signal that changed nothing sends nothing; the next real change does.
	backend.set(map[string]int{"cpu": 1}, nil)
	backend.set(map[string]int{"cpu": 2}, nil)
	if got := nextFrame(t, frames); got.data != `{"cpu":2}` {
		t.Fatalf("frame after change = %+v, want cpu 2 and no repeat of cpu 1", got)
	}
	// A failed read is a topic-error frame, and the stream stays open.
	backend.set(nil, NotFound("gone"))
	if got := nextFrame(t, frames); got.event != TopicErrorEvent || !strings.Contains(got.data, `"topic":"system"`) || !strings.Contains(got.data, CodeNotFound) {
		t.Fatalf("error frame = %+v", got)
	}
	backend.set(map[string]int{"cpu": 3}, nil)
	if got := nextFrame(t, frames); got.data != `{"cpu":3}` {
		t.Fatalf("frame after error = %+v", got)
	}
}

// An EventSource fires its own `error` event when the connection drops, so a
// topic failure named `error` reads as a disconnect in the browser.
func TestEventsNameATopicFailureTopicErrorNeverError(t *testing.T) {
	backend := &eventBackend{fail: NewError(http.StatusConflict, CodeNotLive, "down"), signals: make(chan struct{}, 4)}
	srv := httptest.NewServer(New(backend, nil).Handler())
	defer srv.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/events?topics=system", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	frames := make(chan sseFrame, 8)
	go readFrames(t, bufio.NewScanner(res.Body), frames)

	got := nextFrame(t, frames)
	if got.event != "topic-error" {
		t.Fatalf("failure frame event = %q, want topic-error", got.event)
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(got.data), &body); err != nil || body.Error.Code != CodeNotLive || body.Error.Details["topic"] != TopicSystem {
		t.Fatalf("failure frame body = %s (%v)", got.data, err)
	}
	backend.set(map[string]int{"cpu": 1}, nil)
	if got := nextFrame(t, frames); got.event == "error" {
		t.Fatalf("a frame was named error: %+v", got)
	}
}

// The client clears a topic's error when a frame for the topic arrives, so a
// good read after a failure is sent even when its body did not change.
func TestEventsResendAnUnchangedBodyAfterAFailure(t *testing.T) {
	backend := &eventBackend{system: map[string]int{"cpu": 1}, signals: make(chan struct{}, 4)}
	srv := httptest.NewServer(New(backend, nil).Handler())
	defer srv.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/events?topics=system", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	frames := make(chan sseFrame, 8)
	go readFrames(t, bufio.NewScanner(res.Body), frames)

	if got := nextFrame(t, frames); got.event != TopicSystem || got.data != `{"cpu":1}` {
		t.Fatalf("first frame = %+v", got)
	}
	backend.set(nil, NotFound("blip"))
	if got := nextFrame(t, frames); got.event != TopicErrorEvent {
		t.Fatalf("failure frame = %+v", got)
	}
	backend.set(map[string]int{"cpu": 1}, nil)
	if got := nextFrame(t, frames); got.event != TopicSystem || got.data != `{"cpu":1}` {
		t.Fatalf("frame after the failure = %+v, want the unchanged body sent again", got)
	}
	// Without a failure in between, the same body is still skipped.
	backend.set(map[string]int{"cpu": 1}, nil)
	backend.set(map[string]int{"cpu": 2}, nil)
	if got := nextFrame(t, frames); got.data != `{"cpu":2}` {
		t.Fatalf("frame after a repeat = %+v, want cpu 2", got)
	}
}

func TestEventsRefuseUnknownTopics(t *testing.T) {
	backend := &eventBackend{signals: make(chan struct{})}
	srv := httptest.NewServer(New(backend, nil).Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/v1/events?topics=system,secrets")
	if err != nil {
		t.Fatal(err)
	}
	body := decode(t, res)
	if res.StatusCode != http.StatusBadRequest || body["error"].(map[string]any)["code"] != CodeInvalidRequest {
		t.Fatalf("unknown topic = %d %v", res.StatusCode, body)
	}
}

func TestEventsNeedAWatcher(t *testing.T) {
	srv := httptest.NewServer(New(&fakeBackend{}, nil).Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("events without a watcher = %d", res.StatusCode)
	}
}
