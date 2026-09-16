package web

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type followerStub struct {
	mu    sync.Mutex
	queue [][]any
}

func (f *followerStub) Offset() int64 { return 42 }

func (f *followerStub) Next() ([]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return nil, nil
	}
	next := f.queue[0]
	f.queue = f.queue[1:]
	if next == nil {
		return nil, errors.New("rotated")
	}
	return next, nil
}

type clientStub struct {
	fakeBackend
	follower *followerStub
	captures []any
	from     []int64
	mu       sync.Mutex
}

func (c *clientStub) Messages(context.Context) (any, error) {
	return map[string]any{"locale": "en-US"}, nil
}
func (c *clientStub) Transcript(_ context.Context, agent string, limit int) (any, error) {
	return map[string]any{"agent": agent, "limit": limit}, nil
}
func (c *clientStub) FollowTranscript(_ context.Context, _ string, offset int64) (Follower, error) {
	c.mu.Lock()
	c.from = append(c.from, offset)
	c.mu.Unlock()
	return c.follower, nil
}
func (c *clientStub) Layout(context.Context, string, bool) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.captures) == 0 {
		return nil, NewError(http.StatusConflict, CodeNotLive, "gone")
	}
	next := c.captures[0]
	c.captures = c.captures[1:]
	return next, nil
}
func (c *clientStub) Screen(context.Context, string) (any, error)           { return nil, nil }
func (c *clientStub) ResumeCandidates(context.Context, string) (any, error) { return nil, nil }
func (c *clientStub) PreviewAgent(context.Context, string, string, CreateAgentRequest) (any, error) {
	return nil, nil
}

func TestTranscriptEventsSendEachTurnAndSurviveAReadError(t *testing.T) {
	stub := &clientStub{follower: &followerStub{queue: [][]any{
		{map[string]string{"text": "one"}, map[string]string{"text": "two"}},
		nil, // a read error
		{map[string]string{"text": "three"}},
	}}}
	srv := httptest.NewServer(New(stub, nil).Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/v1/web/agents/a/transcript/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	frames := make(chan sseFrame, 8)
	go readFrames(t, bufio.NewScanner(res.Body), frames)
	for _, want := range []sseFrame{ // turn frames carry the follower's offset as their id
		{"turn", `{"text":"one"}`},
		{"turn", `{"text":"two"}`},
		{"error", ""},
		{"turn", `{"text":"three"}`},
	} {
		got := nextFrame(t, frames)
		if got.event != want.event || (want.data != "" && got.data != want.data) {
			t.Fatalf("frame = %+v, want %+v", got, want)
		}
	}
}

func TestLayoutEventsSkipRepeatsAndEndWithGone(t *testing.T) {
	saved := layoutPoll
	layoutPoll = 20 * time.Millisecond
	t.Cleanup(func() { layoutPoll = saved })
	stub := &clientStub{captures: []any{
		map[string]any{"window": "@1", "at": "t1"},
		map[string]any{"window": "@1", "at": "t2"}, // only the timestamp moved
		map[string]any{"window": "@2", "at": "t3"},
	}}
	srv := httptest.NewServer(New(stub, nil).Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/v1/web/windows/w/layout/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	frames := make(chan sseFrame, 8)
	go readFrames(t, bufio.NewScanner(res.Body), frames)
	if got := nextFrame(t, frames); got.event != "layout" || got.data != `{"at":"t1","window":"@1"}` {
		t.Fatalf("first = %+v", got)
	}
	if got := nextFrame(t, frames); got.event != "layout" || got.data != `{"at":"t3","window":"@2"}` {
		t.Fatalf("second = %+v, want the t2 repeat skipped", got)
	}
	if got := nextFrame(t, frames); got.event != "gone" {
		t.Fatalf("third = %+v, want gone", got)
	}
}

func TestClientRoutesNeedAClientBackend(t *testing.T) {
	srv := httptest.NewServer(New(&fakeBackend{}, nil).Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/v1/web/i18n")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("client route without a client backend = %d", res.StatusCode)
	}
	stub := &clientStub{}
	srv2 := httptest.NewServer(New(stub, nil).Handler())
	defer srv2.Close()
	res, err = http.Get(srv2.URL + "/api/v1/web/agents/a/transcript?limit=5000")
	if err != nil {
		t.Fatal(err)
	}
	body := decode(t, res)
	if res.StatusCode != 400 || body["error"].(map[string]any)["code"] != CodeInvalidRequest {
		t.Fatalf("limit 5000 = %d %v", res.StatusCode, body)
	}
}

func TestTranscriptEventsResumeFromTheGivenOffset(t *testing.T) {
	stub := &clientStub{follower: &followerStub{}}
	srv := httptest.NewServer(New(stub, nil).Handler())
	defer srv.Close()
	open := func(query string, lastID string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/web/agents/a/transcript/events"+query, nil)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		res, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	open("", "")
	open("?from=start", "")
	open("?from=120", "")
	open("?from=120", "300") // a reconnect resumes where the stream got to
	if code := open("?from=-4", ""); code != http.StatusBadRequest {
		t.Errorf("negative offset = %d", code)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	want := []int64{-1, 0, 120, 300}
	if len(stub.from) != len(want) {
		t.Fatalf("offsets = %v, want %v", stub.from, want)
	}
	for i := range want {
		if stub.from[i] != want[i] {
			t.Fatalf("offsets = %v, want %v", stub.from, want)
		}
	}
}
