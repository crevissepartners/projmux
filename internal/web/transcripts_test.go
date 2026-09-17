package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func fastTranscriptPoll(t *testing.T) {
	t.Helper()
	saved := transcriptPoll
	transcriptPoll = 10 * time.Millisecond
	t.Cleanup(func() { transcriptPoll = saved })
}

func openTranscripts(t *testing.T, url, query, lastID string) (*http.Response, <-chan sseFrame) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/api/v1/web/transcripts/events"+query, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	frames := make(chan sseFrame, 16)
	if res.StatusCode == http.StatusOK {
		go readFrames(t, bufio.NewScanner(res.Body), frames)
	}
	return res, frames
}

type turnFrameBody struct {
	Stream string          `json:"stream"`
	Agent  string          `json:"agent"`
	Turn   json.RawMessage `json:"turn"`
	Error  *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func parseTurnFrame(t *testing.T, frame sseFrame) turnFrameBody {
	t.Helper()
	var body turnFrameBody
	if err := json.Unmarshal([]byte(frame.data), &body); err != nil {
		t.Fatalf("frame %+v: %v", frame, err)
	}
	return body
}

func TestTranscriptEventsCarryEveryAgentOnOneStream(t *testing.T) {
	fastTranscriptPoll(t)
	stub := &clientStub{followers: map[string]*followerStub{
		"agt-a": {queue: [][]any{{map[string]string{"text": "one"}, map[string]string{"text": "two"}}}},
		"agt-b": {queue: [][]any{{map[string]string{"text": "three"}}}},
	}}
	srv := httptest.NewServer(New(stub, nil).Handler())
	t.Cleanup(srv.Close)
	res, frames := openTranscripts(t, srv.URL, "?stream=1:agt-a:10&stream=2:agt-b:end", "")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("open = %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}

	// The stream starts where each transcript read ended, and says so in an
	// id a reconnect sends back.
	ready := nextFrame(t, frames)
	if ready.event != "ready" || ready.id != "1:10,2:100" || ready.data != `{"streams":{"1":10,"2":100}}` {
		t.Fatalf("ready = %+v", ready)
	}
	want := []struct{ stream, agent, turn, id string }{
		{"1", "agt-a", `{"text":"one"}`, ""}, // not the end of its read: no id
		{"1", "agt-a", `{"text":"two"}`, "1:30,2:100"},
		{"2", "agt-b", `{"text":"three"}`, "1:30,2:110"},
	}
	for _, w := range want {
		got := nextFrame(t, frames)
		body := parseTurnFrame(t, got)
		if got.event != "turn" || body.Stream != w.stream || body.Agent != w.agent || string(body.Turn) != w.turn || got.id != w.id {
			t.Fatalf("frame = %+v, want %+v", got, w)
		}
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !slices.Equal(stub.from, []int64{10, -1}) {
		t.Fatalf("follow offsets = %v, want [10 -1]", stub.from)
	}
}

func TestTranscriptEventsResumeEachStreamFromLastEventID(t *testing.T) {
	stub := &clientStub{followers: map[string]*followerStub{"a": {}, "b": {}, "c": {}}}
	srv := httptest.NewServer(New(stub, nil).Handler())
	t.Cleanup(srv.Close)
	open := func(query, lastID string) int {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/web/transcripts/events"+query, nil)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if code := open("?stream=1:a:120&stream=2:b:start&stream=3:c:end", ""); code != http.StatusOK {
		t.Fatalf("open = %d", code)
	}
	// A reconnect resumes the keys its last id names; the others start where
	// the query said.
	if code := open("?stream=1:a:120&stream=2:b:start&stream=3:c:end", "1:300,3:40"); code != http.StatusOK {
		t.Fatalf("reconnect = %d", code)
	}
	stub.mu.Lock()
	got := slices.Clone(stub.from)
	stub.mu.Unlock()
	if want := []int64{120, 0, -1, 300, 0, 40}; !slices.Equal(got, want) {
		t.Fatalf("follow offsets = %v, want %v", got, want)
	}

	for _, bad := range []struct{ query, lastID string }{
		{"", ""},
		{"?stream=a:120", ""},
		{"?stream=1:a:-4", ""},
		{"?stream=bad%20key:a:1", ""},
		{"?stream=1:a:1&stream=1:b:1", ""},
		{"?stream=1::1", ""},
		{"?stream=1:a:1", "1:x"},
	} {
		if code := open(bad.query, bad.lastID); code != http.StatusBadRequest {
			t.Errorf("open %q (Last-Event-ID %q) = %d, want 400", bad.query, bad.lastID, code)
		}
	}
}

func TestTranscriptEventsReportFailuresPerStreamAndKeepTheRest(t *testing.T) {
	fastTranscriptPoll(t)
	stub := &clientStub{followers: map[string]*followerStub{
		"agt-a": {queue: [][]any{
			{map[string]string{"text": "one"}},
			nil, // a read error
			nil, // still failing: not reported again
			{map[string]string{"text": "two"}},
		}},
	}}
	srv := httptest.NewServer(New(stub, nil).Handler())
	t.Cleanup(srv.Close)
	// agt-gone has no transcript to open.
	_, frames := openTranscripts(t, srv.URL, "?stream=g:agt-gone:5&stream=a:agt-a:0", "")

	opened := nextFrame(t, frames)
	if body := parseTurnFrame(t, opened); opened.event != "transcript-error" || body.Stream != "g" || body.Agent != "agt-gone" || body.Error == nil || body.Error.Code != "no-transcript" {
		t.Fatalf("open failure = %+v", opened)
	}
	// The failed key keeps the offset it asked for, so a reconnect retries it
	// from there.
	if ready := nextFrame(t, frames); ready.event != "ready" || ready.id != "g:5,a:0" {
		t.Fatalf("ready = %+v", ready)
	}
	for _, want := range []struct{ event, turn string }{
		{"turn", `{"text":"one"}`},
		{"transcript-error", ""},
		{"transcript-recovered", ""},
		{"turn", `{"text":"two"}`},
	} {
		got := nextFrame(t, frames)
		body := parseTurnFrame(t, got)
		if got.event != want.event || body.Stream != "a" || body.Agent != "agt-a" || (want.turn != "" && string(body.Turn) != want.turn) {
			t.Fatalf("frame = %+v, want %+v", got, want)
		}
		if got.event == "error" {
			t.Fatalf("a frame was named error: %+v", got)
		}
	}
}

func TestPerAgentTranscriptEventsRouteIsGone(t *testing.T) {
	srv := httptest.NewServer(New(&clientStub{}, nil).Handler())
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/api/v1/web/agents/a/transcript/events")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("per-agent transcript events = %d, want 404", res.StatusCode)
	}
}
