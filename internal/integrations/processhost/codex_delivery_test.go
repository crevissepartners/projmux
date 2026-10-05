package processhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestCodexUserDeliveryIdleAndExactRunning(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	first, err := c.DeliverUserTurn(context.Background(), a, "input-1", "hold")
	if err != nil || first != (UserTurnDelivery{UserTurnStart, "input-1", "turn-1"}) {
		t.Fatalf("start=%+v %v", first, err)
	}
	if err := c.Turn(context.Background(), a, "explicit", "hold"); !errors.Is(err, ErrBusy) {
		t.Fatalf("explicit running=%v", err)
	}
	next, err := c.DeliverUserTurn(context.Background(), a, "input-2", "more")
	if err != nil || next != (UserTurnDelivery{UserTurnSteer, "input-2", first.TurnID}) {
		t.Fatalf("steer=%+v %v", next, err)
	}
	for _, op := range []string{"input-1", "input-2"} {
		if got, err := c.DeliverUserTurn(context.Background(), a, op, "retry"); !errors.Is(err, ErrStale) || got != (UserTurnDelivery{}) {
			t.Fatalf("replay=%+v %v", got, err)
		}
	}
	wire := codexWire(t, log)
	if countMethod(wire, "turn/start") != 1 || countMethod(wire, "turn/steer") != 1 {
		t.Fatalf("wire=%v", wire)
	}
	for _, msg := range wire {
		if string(msg["method"]) != `"turn/steer"` {
			continue
		}
		var got map[string]any
		_ = json.Unmarshal(msg["params"], &got)
		want := map[string]any{"threadId": "thread", "expectedTurnId": "turn-1", "input": []any{map[string]any{"type": "text", "text": "more"}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("exact wire=%v", got)
		}
	}
}

func TestCodexUserDeliveryConcurrentSelection(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	var wg sync.WaitGroup
	results := make(chan UserTurnDelivery, 8)
	for i := range 8 {
		wg.Go(func() {
			r, e := c.DeliverUserTurn(context.Background(), a, fmt.Sprintf("input-%d", i), "hold")
			if e != nil {
				t.Error(e)
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	starts := 0
	for r := range results {
		if r.Mode == UserTurnStart {
			starts++
		}
		if r.TurnID != "turn-1" {
			t.Errorf("turn=%+v", r)
		}
	}
	wire := codexWire(t, log)
	if starts != 1 || countMethod(wire, "turn/start") != 1 || countMethod(wire, "turn/steer") != 7 {
		t.Fatalf("starts=%d wire=%v", starts, wire)
	}
}

func TestCodexUserDeliveryQueuedCompletionAndRefusal(t *testing.T) {
	for _, mode := range []string{"codex-steer-queue", "codex-steer-refusal-queue", "codex-steer-refusal"} {
		t.Run(mode, func(t *testing.T) {
			c, _, log := codexStart(t, testHost(t, nil), mode)
			a := codexAuthority(c)
			if _, err := c.DeliverUserTurn(context.Background(), a, "start", "hold"); err != nil {
				t.Fatal(err)
			}
			got, err := c.DeliverUserTurn(context.Background(), a, "steer", "more")
			refused := codexappserver.IsResponseError(err)
			if refused != (mode != "codex-steer-queue") {
				t.Fatalf("delivery=%+v %v", got, err)
			}
			if refused && got != (UserTurnDelivery{}) {
				t.Fatalf("false receipt=%+v", got)
			}
			queue := mode != "codex-steer-refusal"
			s := observeUntil(t, c.handle, func(s Snapshot) bool { return !queue || s.Turn == "" })
			if s.State != "ready" || s.Exit != nil || (!queue && s.Turn != "turn-1") {
				t.Fatalf("lifecycle=%+v", s)
			}
			events, _, _ := c.Events(a.Binding, 0)
			deltas, results := 0, 0
			for _, event := range events {
				if event.Kind == "provider-event" && string(event.Raw) != "" {
					var v struct {
						Delta string `json:"delta"`
					}
					_ = json.Unmarshal(event.Raw, &v)
					if v.Delta == "before steer reply" {
						deltas++
					}
				}
				if event.Kind == "turn-result" {
					results++
					var v codexTurnResult
					_ = json.Unmarshal(event.Raw, &v)
					if v.Turn.Status == "failed" {
						t.Fatal("synthetic steer failure")
					}
					if queue && deltas != 96 {
						t.Fatalf("wire order deltas=%d", deltas)
					}
				}
			}
			if queue && (deltas != 96 || results != 1) {
				t.Fatalf("drain deltas=%d results=%d", deltas, results)
			}
			if _, err := c.DeliverUserTurn(context.Background(), a, "steer", "replay"); !errors.Is(err, ErrStale) {
				t.Fatalf("refusal replay=%v", err)
			}
			wire := codexWire(t, log)
			if countMethod(wire, "turn/start") != 1 || countMethod(wire, "turn/steer") != 1 {
				t.Fatalf("fallback wire=%v", wire)
			}
			if queue {
				r, e := c.DeliverUserTurn(context.Background(), a, "next", "hold")
				if e != nil || r.Mode != UserTurnStart || r.TurnID != "turn-2" {
					t.Fatalf("after completion=%+v %v", r, e)
				}
			}
		})
	}
}

func TestCodexUserDeliveryRejectsAuthorityBeforeWire(t *testing.T) {
	var current atomic.Bool
	current.Store(true)
	h := testHost(t, func(tx *Transactions, _ *Limits) {
		tx.Current = func(context.Context, Binding) error {
			if !current.Load() {
				return ErrStale
			}
			return nil
		}
	})
	c, _, log := codexStart(t, h, "codex-normal")
	a := codexAuthority(c)
	_, err := c.DeliverUserTurn(context.Background(), a, "start", "hold")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Authority){func(a *Authority) { a.Binding.Generation = "old" }, func(a *Authority) { a.Binding.Agent = "other" }, func(a *Authority) { a.Binding.Operation = "old" }, func(a *Authority) { a.Connection = "old" }, func(a *Authority) { a.Session = "old" }} {
		wrong := a
		change(&wrong)
		if r, e := c.DeliverUserTurn(context.Background(), wrong, "bad", "more"); !errors.Is(e, ErrStale) || r != (UserTurnDelivery{}) {
			t.Fatalf("authority=%+v %v", r, e)
		}
	}
	current.Store(false)
	if _, e := c.DeliverUserTurn(context.Background(), a, "registry", "more"); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	current.Store(true)
	if e := c.Stop(a.Binding); e != nil {
		t.Fatal(e)
	}
	if _, e := c.DeliverUserTurn(context.Background(), a, "stopped", "more"); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	wire := codexWire(t, log)
	if countMethod(wire, "turn/steer") != 0 || countMethod(wire, "turn/start") != 1 {
		t.Fatalf("stale write=%v", wire)
	}
}

func TestCodexUserDeliveryUncertainStopsOwnedChild(t *testing.T) {
	h := testHost(t, func(_ *Transactions, l *Limits) { l.Startup = 300 * time.Millisecond })
	c, _, log := codexStart(t, h, "codex-steer-stall")
	a := codexAuthority(c)
	if _, e := c.DeliverUserTurn(context.Background(), a, "start", "hold"); e != nil {
		t.Fatal(e)
	}
	r, e := c.DeliverUserTurn(context.Background(), a, "steer", "more")
	if e == nil || r != (UserTurnDelivery{}) {
		t.Fatalf("unknown=%+v %v", r, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, e := c.Wait(ctx, a.Binding)
	if e != nil || s.Exit == nil || s.State != "exited" {
		t.Fatalf("actual Wait=%+v %v", s, e)
	}
	wire := codexWire(t, log)
	if countMethod(wire, "turn/start") != 1 || countMethod(wire, "turn/steer") != 1 {
		t.Fatalf("uncertain fallback=%v", wire)
	}
}

func TestCodexUserDeliveryPreservesPendingAndAnsweredTokens(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	if _, e := c.DeliverUserTurn(context.Background(), a, "start", "controls"); e != nil {
		t.Fatal(e)
	}
	before := observeUntil(t, c.handle, func(s Snapshot) bool { return len(s.Pending) == 2 })
	if _, e := c.DeliverUserTurn(context.Background(), a, "steer", "more"); e != nil {
		t.Fatal(e)
	}
	after, _ := c.Observe(a.Binding)
	slices.SortFunc(before.Pending, func(a, b Request) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(after.Pending, func(a, b Request) int { return strings.Compare(a.ID, b.ID) })
	if !reflect.DeepEqual(before.Pending, after.Pending) {
		t.Fatalf("tokens changed before=%v after=%v", before.Pending, after.Pending)
	}
	var approval, question Request
	for _, req := range before.Pending {
		if req.Kind == "question" {
			question = req
		} else {
			approval = req
		}
	}
	if e := c.RespondApproval(context.Background(), a, approval, codexappserver.DecisionDecline); e != nil {
		t.Fatal(e)
	}
	if _, e := c.DeliverUserTurn(context.Background(), a, "steer-answered", "more"); e != nil {
		t.Fatal(e)
	}
	c.handle.mu.Lock()
	fenced := c.handle.usedRequests[approval.ID] && c.handle.usedRequests[question.ID]
	c.handle.mu.Unlock()
	if !fenced {
		t.Fatal("steer cleared raw request fences")
	}
	var replay codexappserver.Notification
	if e := json.Unmarshal(approval.Input, &replay); e != nil {
		t.Fatal(e)
	}
	if e := c.handle.adapter.(*codexAdapter).consume(replay); e != nil {
		t.Fatal(e)
	}
	after, _ = c.Observe(a.Binding)
	if len(after.Pending) != 1 {
		t.Fatal("answered raw token replay admitted")
	}
	if e := c.RespondApproval(context.Background(), a, approval, codexappserver.DecisionAccept); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	if e := c.RespondQuestion(context.Background(), a, question, map[int]agentquestion.Selection{0: {Labels: []string{"B"}}}); e != nil {
		t.Fatal(e)
	}
	observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	wire := codexWire(t, log)
	if countMethod(wire, "turn/start") != 1 || countMethod(wire, "turn/steer") != 2 {
		t.Fatalf("wire=%v", wire)
	}
}

func TestCodexUserDeliveryBoundedReplayHorizon(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, func(_ *Transactions, l *Limits) { l.Events = 8 }), "codex-normal")
	a := codexAuthority(c)
	if _, e := c.DeliverUserTurn(context.Background(), a, "start", "hold"); e != nil {
		t.Fatal(e)
	}
	for i := range 9 {
		if _, e := c.DeliverUserTurn(context.Background(), a, fmt.Sprintf("steer-%d", i), "more"); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := c.DeliverUserTurn(context.Background(), a, "steer-8", "retry"); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	if _, e := c.DeliverUserTurn(context.Background(), a, "steer-0", "evicted"); e != nil {
		t.Fatal(e)
	}
	c.handle.mu.Lock()
	n := len(c.handle.adapter.(*codexAdapter).usedDeliveries)
	c.handle.mu.Unlock()
	if n != 8 || countMethod(codexWire(t, log), "turn/steer") != 10 {
		t.Fatalf("bounded fence=%d", n)
	}
}

func TestCodexUserDeliveryAdmissionAfterCompletionStarts(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	if _, e := c.DeliverUserTurn(context.Background(), a, "start", "hold"); e != nil {
		t.Fatal(e)
	}
	adapter := c.handle.adapter.(*codexAdapter)
	if e := adapter.lock(context.Background()); e != nil {
		t.Fatal(e)
	}
	result := make(chan UserTurnDelivery, 1)
	errs := make(chan error, 1)
	go func() { r, e := c.DeliverUserTurn(context.Background(), a, "next", "hold"); result <- r; errs <- e }()
	if e := adapter.consume(codexappserver.Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn-1","status":"completed"}}`)}); e != nil {
		t.Fatal(e)
	}
	adapter.unlock()
	r, e := <-result, <-errs
	if e != nil || r != (UserTurnDelivery{UserTurnStart, "next", "turn-2"}) {
		t.Fatalf("admission=%+v %v", r, e)
	}
	if countMethod(codexWire(t, log), "turn/steer") != 0 {
		t.Fatal("completed turn steered")
	}
}

func TestCodexUserDeliveryWaitingAdmissionRechecksOwnership(t *testing.T) {
	for _, reason := range []string{"host-replaced", "generation-restarted", "stopping"} {
		t.Run(reason, func(t *testing.T) {
			var current atomic.Bool
			current.Store(true)
			c, _, log := codexStart(t, testHost(t, func(tx *Transactions, _ *Limits) {
				tx.Current = func(context.Context, Binding) error {
					if !current.Load() {
						return ErrStale
					}
					return nil
				}
			}), "codex-normal")
			a := codexAuthority(c)
			if _, e := c.DeliverUserTurn(context.Background(), a, "start", "hold"); e != nil {
				t.Fatal(e)
			}
			adapter := c.handle.adapter.(*codexAdapter)
			if e := adapter.lock(context.Background()); e != nil {
				t.Fatal(e)
			}
			result := make(chan error, 1)
			go func() { _, e := c.DeliverUserTurn(context.Background(), a, "queued", "more"); result <- e }()
			want := ErrStale
			if reason == "stopping" {
				want = ErrClosed
				if e := c.Stop(a.Binding); e != nil {
					t.Fatal(e)
				}
			} else {
				current.Store(false)
			}
			adapter.unlock()
			if e := <-result; !errors.Is(e, want) {
				t.Fatalf("ownership=%v", e)
			}
			if countMethod(codexWire(t, log), "turn/steer") != 0 {
				t.Fatal("replaced ownership wrote")
			}
		})
	}
}

func TestCodexUserDeliveryExpiredTokenAndInterrupt(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	if _, e := c.DeliverUserTurn(context.Background(), a, "start", "controls"); e != nil {
		t.Fatal(e)
	}
	s := observeUntil(t, c.handle, func(s Snapshot) bool { return len(s.Pending) == 2 })
	token := s.Pending[0]
	if e := c.Expire(a, token); e != nil {
		t.Fatal(e)
	}
	if _, e := c.DeliverUserTurn(context.Background(), a, "steer", "more"); e != nil {
		t.Fatal(e)
	}
	after, _ := c.Observe(a.Binding)
	if len(after.Pending) != 1 {
		t.Fatalf("expired token restored=%+v", after)
	}
	c.handle.mu.Lock()
	used := c.handle.usedRequests[token.ID]
	c.handle.mu.Unlock()
	if !used {
		t.Fatal("expired raw ID unfenced")
	}
	if e := c.Interrupt(context.Background(), a, s.Turn); e != nil {
		t.Fatal(e)
	}
	after = observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	if len(after.Pending) != 0 || after.Exit != nil {
		t.Fatalf("interrupt=%+v", after)
	}
	if e := c.Expire(a, token); !errors.Is(e, ErrStale) {
		t.Fatalf("late expire=%v", e)
	}
	if countMethod(codexWire(t, log), "turn/interrupt") != 1 {
		t.Fatal("interrupt parity")
	}
}

func TestCodexUserDeliveryRetainsStreamAndControlLimits(t *testing.T) {
	for _, mode := range []string{"codex-steer-overflow", "codex-steer-requests", "codex-steer-frame"} {
		t.Run(mode, func(t *testing.T) {
			c, _, log := codexStart(t, testHost(t, func(_ *Transactions, l *Limits) { l.Events = 8; l.Requests = 1; l.FrameBytes = 2048 }), mode)
			a := codexAuthority(c)
			if _, e := c.DeliverUserTurn(context.Background(), a, "start", "hold"); e != nil {
				t.Fatal(e)
			}
			_, _ = c.DeliverUserTurn(context.Background(), a, "steer", "more")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, e := c.Wait(ctx, a.Binding)
			if e != nil || s.Exit == nil || s.Failure == "" {
				t.Fatalf("bounded actual Wait=%+v %v", s, e)
			}
			if len(s.Pending) > 1 {
				t.Fatalf("requests=%d", len(s.Pending))
			}
			wire := codexWire(t, log)
			if countMethod(wire, "turn/start") != 1 || countMethod(wire, "turn/steer") != 1 {
				t.Fatal("limit fallback")
			}
		})
	}
}
