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

// followerStub hands out queued reads; a nil entry is a read error. Each
// turn read moves the offset by ten bytes.
type followerStub struct {
	mu     sync.Mutex
	offset int64
	queue  [][]any
}

func (f *followerStub) Offset() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.offset
}

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
	f.offset += int64(10 * len(next))
	return next, nil
}

type clientStub struct {
	fakeBackend
	// followers are per agent; an agent without one has no transcript.
	followers map[string]*followerStub
	captures  []any
	from      []int64
	mu        sync.Mutex
}

func (c *clientStub) Messages(context.Context) (any, error) {
	return map[string]any{"locale": "en-US"}, nil
}
func (c *clientStub) Transcript(_ context.Context, agent string, limit int) (any, error) {
	return map[string]any{"agent": agent, "limit": limit}, nil
}
func (c *clientStub) FollowTranscript(_ context.Context, agent string, offset int64) (Follower, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.from = append(c.from, offset)
	follower, ok := c.followers[agent]
	if !ok {
		return nil, NewError(http.StatusConflict, "no-transcript", "no transcript for "+agent)
	}
	follower.mu.Lock()
	defer follower.mu.Unlock()
	if offset < 0 {
		offset = 100 // the end of the file
	}
	follower.offset = offset
	return follower, nil
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
func (c *clientStub) LaunchOptions(context.Context) (any, error)            { return nil, nil }
func (c *clientStub) Settings(context.Context) (any, error)                 { return nil, nil }
func (c *clientStub) UpdateSetting(context.Context, SettingRequest) (any, error) {
	return nil, nil
}
func (c *clientStub) Statusbar(context.Context) (any, error)       { return nil, nil }
func (c *clientStub) PaneGit(context.Context, string) (any, error) { return nil, nil }
func (c *clientStub) PreviewAgent(context.Context, string, string, CreateAgentRequest) (any, error) {
	return nil, nil
}
func (c *clientStub) AnswerQuestion(context.Context, string, QuestionAnswer) (any, error) {
	return map[string]bool{"ok": true}, nil
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
