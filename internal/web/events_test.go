package web

import (
	"bufio"
	"context"
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

type sseFrame struct{ event, data string }

func readFrames(t *testing.T, scanner *bufio.Scanner, frames chan<- sseFrame) {
	t.Helper()
	var frame sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			frame.event = strings.TrimPrefix(line, "event: ")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	// A failed read is an error frame, and the stream stays open.
	backend.set(nil, NotFound("gone"))
	if got := nextFrame(t, frames); got.event != "error" || !strings.Contains(got.data, `"topic":"system"`) || !strings.Contains(got.data, CodeNotFound) {
		t.Fatalf("error frame = %+v", got)
	}
	backend.set(map[string]int{"cpu": 3}, nil)
	if got := nextFrame(t, frames); got.data != `{"cpu":3}` {
		t.Fatalf("frame after error = %+v", got)
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
