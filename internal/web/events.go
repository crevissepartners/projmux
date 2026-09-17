package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Event topics. Each is also the SSE event name its frames carry, and each
// frame's data is the same body the topic's GET route returns.
//
// A topic that fails to read is reported as a TopicErrorEvent frame. It is not
// called `error`: an EventSource fires its own `error` event when the
// connection drops, and a listener for one would take the other for it.
const (
	TopicGraph         = "graph"
	TopicNotifications = "notifications"
	TopicUsage         = "usage"
	TopicSystem        = "system"
)

// TopicErrorEvent is the SSE event name of a failed topic read. Its data is
// the error envelope with the topic in `details.topic`.
const TopicErrorEvent = "topic-error"

var eventTopics = []string{TopicGraph, TopicNotifications, TopicUsage, TopicSystem}

// keepalive bounds silence on a stream, so an idle one does not look dropped
// to the browser or anything in between.
const keepalive = 20 * time.Second

// Watcher tells the event stream when a topic may have changed. A signal is a
// hint, not a promise: the stream re-reads the topic and sends a frame only
// when the body differs from the last one it sent, so a spurious signal costs
// one read and nothing on the wire.
type Watcher interface {
	Changes(ctx context.Context, topic string) (<-chan struct{}, error)
}

func (s *Server) readTopic(ctx context.Context, topic string) (any, error) {
	switch topic {
	case TopicGraph:
		return s.backend.Graph(ctx)
	case TopicNotifications:
		return s.backend.Notifications(ctx)
	case TopicUsage:
		return s.backend.Usage(ctx)
	case TopicSystem:
		return s.backend.System(ctx)
	}
	return nil, fmt.Errorf("unknown topic %q", topic)
}

// sseWriter serializes frames from several topic loops onto one response.
type sseWriter struct {
	mu      sync.Mutex
	w       io.Writer
	flusher http.Flusher
	last    time.Time
}

func (w *sseWriter) event(name string, body []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := fmt.Fprintf(w.w, "event: %s\ndata: %s\n\n", name, body); err != nil {
		return err
	}
	w.flusher.Flush()
	w.last = time.Now()
	return nil
}

// eventID sends a frame carrying an id, which an EventSource echoes back as
// Last-Event-ID when it reconnects.
func (w *sseWriter) eventID(name, id string, body []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := fmt.Fprintf(w.w, "id: %s\nevent: %s\ndata: %s\n\n", id, name, body); err != nil {
		return err
	}
	w.flusher.Flush()
	w.last = time.Now()
	return nil
}

func (w *sseWriter) keepalive() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if time.Since(w.last) < keepalive {
		return nil
	}
	if _, err := io.WriteString(w.w, ": keepalive\n\n"); err != nil {
		return err
	}
	w.flusher.Flush()
	w.last = time.Now()
	return nil
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, r, NewError(http.StatusInternalServerError, CodeInternal, "streaming is not supported"))
		return
	}
	watcher, ok := s.backend.(Watcher)
	if !ok {
		s.fail(w, r, NewError(http.StatusNotImplemented, CodeUnsupported, "this server has no change feed"))
		return
	}
	topics := eventTopics
	if raw := r.URL.Query().Get("topics"); raw != "" {
		topics = nil
		for topic := range strings.SplitSeq(raw, ",") {
			topic = strings.TrimSpace(topic)
			if !slices.Contains(eventTopics, topic) {
				s.fail(w, r, InvalidRequest(fmt.Sprintf("unknown topic %q; topics are %s", topic, strings.Join(eventTopics, ","))))
				return
			}
			if !slices.Contains(topics, topic) {
				topics = append(topics, topic)
			}
		}
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	signals := make(map[string]<-chan struct{}, len(topics))
	for _, topic := range topics {
		changes, err := watcher.Changes(ctx, topic)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		signals[topic] = changes
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	out := &sseWriter{w: w, flusher: flusher, last: time.Now()}

	var wg sync.WaitGroup
	for _, topic := range topics {
		wg.Add(1)
		go func(topic string, changes <-chan struct{}) {
			defer wg.Done()
			defer cancel() // one topic's broken write ends the stream
			s.followTopic(ctx, out, topic, changes)
		}(topic, signals[topic])
	}

	ticker := time.NewTicker(keepalive / 4)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C:
			if out.keepalive() != nil {
				cancel()
			}
		}
	}
}

// followTopic sends the topic once at once, then again whenever a signal
// leads to a different body. A failed read sends a TopicErrorEvent frame and
// forgets the last body, so the next good read is sent even when it is the
// same body as before: that frame is what tells the client the topic
// recovered.
func (s *Server) followTopic(ctx context.Context, out *sseWriter, topic string, changes <-chan struct{}) {
	var digest [32]byte
	fail := func(err error) bool {
		digest = [32]byte{}
		frame, _ := json.Marshal(map[string]*Error{"error": withTopic(asError(err), topic)})
		return out.event(TopicErrorEvent, frame) == nil
	}
	send := func() bool {
		body, err := s.readTopic(ctx, topic)
		if ctx.Err() != nil {
			return false
		}
		if err != nil {
			return fail(err)
		}
		frame, err := json.Marshal(body)
		if err != nil {
			return fail(err)
		}
		next := sha256.Sum256(frame)
		if next == digest {
			return true
		}
		digest = next
		return out.event(topic, frame) == nil
	}
	if !send() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-changes:
			if !ok || !send() {
				return
			}
		}
	}
}

func withTopic(e *Error, topic string) *Error {
	copied := *e
	copied.Details = map[string]any{"topic": topic}
	maps.Copy(copied.Details, e.Details)
	return &copied
}
