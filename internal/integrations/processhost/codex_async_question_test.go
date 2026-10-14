package processhost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// Native 0.162.1 raw frames from the active-turn and after-turn probes.
var nativeAsyncFrames = []string{
	`{"method":"item/started","params":{"item":{"type":"agentMessage","id":"call_42babc3b4f8d4f14806598aaf0439781","text":"CORE576 native probe: which color?\n- cobalt\n- amber","phase":"final_answer","memoryCitation":null,"delivery":"async","questions":[{"title":"CORE576 native probe: which color?","options":["cobalt","amber"]}]},"threadId":"01a13b1c-72ab-7a13-a794-a815f52c29d2","turnId":"01a13b1c-72d8-7b72-ad20-5f18583ed08c","startedAtMs":1791993085978},"emittedAtMs":1791993085978}`,
	`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"call_42babc3b4f8d4f14806598aaf0439781","text":"CORE576 native probe: which color?\n- cobalt\n- amber","phase":"final_answer","memoryCitation":null,"delivery":"async","questions":[{"title":"CORE576 native probe: which color?","options":["cobalt","amber"]}]},"threadId":"01a13b1c-72ab-7a13-a794-a815f52c29d2","turnId":"01a13b1c-72d8-7b72-ad20-5f18583ed08c","completedAtMs":1791993085978},"emittedAtMs":1791993085978}`,
	`{"method":"item/started","params":{"item":{"type":"agentMessage","id":"call_98dc8036f5814ba4a7b8fc6770b58047","text":"CORE576 after-turn probe: which shape?\n- circle\n- square","phase":"final_answer","memoryCitation":null,"delivery":"async","questions":[{"title":"CORE576 after-turn probe: which shape?","options":["circle","square"]}]},"threadId":"01a13b1e-7bdc-7e32-b9b1-e532526a6d92","turnId":"01a13b1e-7c09-7d82-ac84-4248b585ddaf","startedAtMs":1791993219280},"emittedAtMs":1791993219280}`,
	`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"call_98dc8036f5814ba4a7b8fc6770b58047","text":"CORE576 after-turn probe: which shape?\n- circle\n- square","phase":"final_answer","memoryCitation":null,"delivery":"async","questions":[{"title":"CORE576 after-turn probe: which shape?","options":["circle","square"]}]},"threadId":"01a13b1e-7bdc-7e32-b9b1-e532526a6d92","turnId":"01a13b1e-7c09-7d82-ac84-4248b585ddaf","completedAtMs":1791993219280},"emittedAtMs":1791993219280}`,
}

func asyncTestAdapter(t *testing.T, raw string) (*codexAdapter, codexappserver.Notification) {
	t.Helper()
	var n codexappserver.Notification
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	var identity struct {
		Thread string `json:"threadId"`
		Turn   string `json:"turnId"`
	}
	_ = json.Unmarshal(n.Params, &identity)
	p := &Handle{host: &Host{instance: "host", limits: DefaultLimits(), tx: Transactions{Current: func(context.Context, Binding) error { return nil }}}, launch: Launch{provider: "codex", Binding: Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}}, state: "ready", session: identity.Thread, connection: "connection", turn: identity.Turn, requests: map[string]Request{}, usedRequests: map[string]bool{}}
	c := &codexAdapter{p: p, control: make(chan struct{}, 1)}
	p.adapter = c
	return c, n
}

func TestCodexAsyncNativeRetentionAndReplay(t *testing.T) {
	for i := 0; i < len(nativeAsyncFrames); i += 2 {
		c, n := asyncTestAdapter(t, nativeAsyncFrames[i])
		if err := c.consumeLocked(n); err != nil {
			t.Fatal(err)
		}
		var completed codexappserver.Notification
		_ = json.Unmarshal([]byte(nativeAsyncFrames[i+1]), &completed)
		if err := c.consumeLocked(completed); err != nil {
			t.Fatal(err)
		}
		p := c.p
		original := p.snapshotLocked().AsyncQuestions
		if len(original) != 1 || len(p.requests) != 0 {
			t.Fatal("async entered native Pending")
		}
		prompts, err := agentquestion.ParseCodexQuestions(original[0].Request.Input)
		if err != nil || !prompts[0].IsOther || len(prompts[0].Options) != 2 {
			t.Fatal(prompts, err)
		}
		turn := p.turn
		params, _ := json.Marshal(map[string]any{"threadId": p.session, "turn": map[string]string{"id": turn, "status": "completed"}})
		if err = c.consumeLocked(codexappserver.Notification{Method: "turn/completed", Params: params}); err != nil {
			t.Fatal(err)
		}
		if err = c.consumeLocked(completed); err != nil {
			t.Fatal("late replay", err)
		}
		p.turn = "next-turn"
		for j := 0; j < p.host.limits.Events+5; j++ {
			p.emitLocked("provider-event", []byte(`{}`), nil)
		}
		events, snap, err := p.Events(p.launch.Binding, 0)
		if err != nil || len(events) == 0 || !reflect.DeepEqual(original, snap.AsyncQuestions) {
			t.Fatal("ring gap lost questions", err)
		}
		snap.AsyncQuestions[0].Request.Input[0] = 'x'
		if p.snapshotLocked().AsyncQuestions[0].Request.Input[0] == 'x' {
			t.Fatal("snapshot aliases retention")
		}
		h := &CodexHandle{handle: p}
		before := p.seq
		err = h.RespondQuestion(context.Background(), Authority{Binding: p.launch.Binding, Session: p.session, Connection: p.connection}, original[0].Request, map[int]agentquestion.Selection{0: {Labels: []string{prompts[0].Options[0].Label}}})
		if err == nil || p.seq != before || len(p.requests) != 0 {
			t.Fatal("async native response gained effects", err)
		}
		deadline := original[0].CreatedAt.Add(time.Minute)
		if err = h.RetainAsyncQuestion(p.launch.Binding, original[0].Request, deadline, true); err != nil {
			t.Fatal(err)
		}
		if err = c.consumeLocked(completed); err != nil {
			t.Fatal(err)
		}
		if !p.snapshotLocked().AsyncQuestions[0].Closed {
			t.Fatal("terminal replay reopened")
		}
		if err = h.RetainAsyncQuestion(p.launch.Binding, original[0].Request, deadline.Add(time.Minute), false); !errors.Is(err, ErrStale) {
			t.Fatal("renewed deadline", err)
		}
	}
}

func TestCodexAsyncMalformedIdentityCapacityAndFence(t *testing.T) {
	for _, change := range []struct{ name, old, new string }{
		{"thread", "01a13b1c-72ab-7a13-a794-a815f52c29d2", "wrong"},
		{"turn", "01a13b1c-72d8-7b72-ad20-5f18583ed08c", "wrong"},
		{"id", "call_42babc3b4f8d4f14806598aaf0439781", "wrong"},
		{"title", "CORE576 native probe: which color?", ""},
		{"option", "cobalt", ""},
	} {
		t.Run(change.name, func(t *testing.T) {
			c, _ := asyncTestAdapter(t, nativeAsyncFrames[0])
			var n codexappserver.Notification
			_ = json.Unmarshal([]byte(strings.ReplaceAll(nativeAsyncFrames[0], change.old, change.new)), &n)
			if err := c.consumeLocked(n); err == nil || len(c.p.asyncQuestions) != 0 {
				t.Fatal(err)
			}
		})
	}
	c, n := asyncTestAdapter(t, nativeAsyncFrames[0])
	c.p.host.limits.Requests = 1
	if err := c.consumeLocked(n); err != nil {
		t.Fatal(err)
	}
	var second codexappserver.Notification
	_ = json.Unmarshal([]byte(strings.ReplaceAll(nativeAsyncFrames[0], "call_42babc3b4f8d4f14806598aaf0439781", "call_second")), &second)
	if err := c.consumeLocked(second); !errors.Is(err, ErrBusy) || len(c.p.asyncQuestions) != 1 {
		t.Fatal("capacity evicted", err)
	}
	for _, state := range []string{"stopping", "exited", "unknown"} {
		c, n := asyncTestAdapter(t, nativeAsyncFrames[0])
		_ = c.consumeLocked(n)
		c.p.state = state
		if !c.p.snapshotLocked().AsyncQuestions[0].Closed {
			t.Fatal(state)
		}
	}
	c, n = asyncTestAdapter(t, nativeAsyncFrames[0])
	_ = c.consumeLocked(n)
	q := c.p.asyncQuestions[0]
	c.p.asyncQuestions[0].Deadline = q.CreatedAt.Add(time.Nanosecond)
	if !c.p.snapshotLocked().AsyncQuestions[0].Closed {
		t.Fatal("expiry")
	}
	wrong := c.p.launch.Binding
	wrong.Generation = "other"
	if _, err := c.p.Observe(wrong); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	c, n = asyncTestAdapter(t, nativeAsyncFrames[0])
	c.p.host.limits.FrameBytes = 256
	if err := c.consumeLocked(n); !errors.Is(err, ErrBusy) {
		t.Fatal("oversize", err)
	}
}

func TestCodexAsyncIndependentItemsAndNativePending(t *testing.T) {
	c, n := asyncTestAdapter(t, nativeAsyncFrames[0])
	p := c.p
	pending := Request{ID: "number:1", Kind: "permission", Session: p.session, Connection: p.connection, Turn: p.turn}
	p.requests[pending.ID] = pending
	if err := c.consumeLocked(n); err != nil {
		t.Fatal(err)
	}
	var second codexappserver.Notification
	_ = json.Unmarshal([]byte(strings.ReplaceAll(nativeAsyncFrames[1], "call_42babc3b4f8d4f14806598aaf0439781", "call_independent")), &second)
	if err := c.consumeLocked(second); err != nil {
		t.Fatal(err)
	}
	s := p.snapshotLocked()
	if len(s.AsyncQuestions) != 2 || len(s.Pending) != 1 || !reflect.DeepEqual(s.Pending[0], pending) {
		t.Fatal("async replaced native request")
	}
	// Completed can be the first observed source, with text-only input.
	c, _ = asyncTestAdapter(t, nativeAsyncFrames[1])
	raw := strings.ReplaceAll(nativeAsyncFrames[1], `["cobalt","amber"]`, `[]`)
	_ = json.Unmarshal([]byte(raw), &second)
	if err := c.consumeLocked(second); err != nil || len(c.p.asyncQuestions) != 1 {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.ReplaceAll(nativeAsyncFrames[0], `["cobalt","amber"]`, `[{}]`),
		strings.ReplaceAll(nativeAsyncFrames[0], `"title":"CORE576 native probe: which color?"`, `"title":null`),
		strings.ReplaceAll(nativeAsyncFrames[0], `"questions":[{"title":"CORE576 native probe: which color?","options":["cobalt","amber"]}]`, `"questions":[]`),
	} {
		c, _ = asyncTestAdapter(t, nativeAsyncFrames[0])
		var bad codexappserver.Notification
		_ = json.Unmarshal([]byte(raw), &bad)
		if err := c.consumeLocked(bad); err == nil || len(c.p.asyncQuestions) != 0 {
			t.Fatal("malformed accepted", err)
		}
	}
	c, n = asyncTestAdapter(t, nativeAsyncFrames[0])
	_ = c.consumeLocked(n)
	replay := n
	replay.Params = json.RawMessage(strings.ReplaceAll(string(n.Params), "cobalt", "changed"))
	if err := c.consumeLocked(replay); !errors.Is(err, ErrStale) {
		t.Fatal("replay replaced content", err)
	}
	c.p.connection = "replacement-connection"
	if err := c.consumeLocked(n); !errors.Is(err, ErrStale) {
		t.Fatal("replay crossed connection", err)
	}
}

func TestCodexAsyncIdleDeadlineWakesOwnerAndRetiresTimer(t *testing.T) {
	c, n := asyncTestAdapter(t, nativeAsyncFrames[0])
	p := c.p
	if err := c.consumeLocked(n); err != nil {
		t.Fatal(err)
	}
	p.turn = ""
	h := &CodexHandle{handle: p}
	q := p.asyncQuestions[0]
	if err := h.RetainAsyncQuestion(p.launch.Binding, q.Request, time.Now().Add(50*time.Millisecond), false); err != nil {
		t.Fatal(err)
	}
	changed, err := p.Changed(p.launch.Binding)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("idle deadline did not wake owner")
	}
	snap, err := p.Observe(p.launch.Binding)
	if err != nil || !snap.AsyncQuestions[0].Closed {
		t.Fatal("expired snapshot", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asyncQuestionTimer != nil {
		t.Fatal("terminal timer retained")
	}
}
