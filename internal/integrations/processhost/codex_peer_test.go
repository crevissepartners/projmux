package processhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func peerCurrent(context.Context) error { return nil }

func TestCodexPeerIdleActiveAndOperatorParity(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-peer-normal")
	a := codexAuthority(c)
	first, err := c.DeliverPeerContext(context.Background(), a, "peer-1", "context", peerCurrent)
	if err != nil || first.Mode != UserTurnStart {
		t.Fatalf("start=%+v %v", first, err)
	}
	// The fixture completes the idle peer turn. Start an operator task normally.
	observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	if err := c.Turn(context.Background(), a, "operator", "hold"); err != nil {
		t.Fatal(err)
	}
	before, _ := c.Observe(a.Binding)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			r, err := c.DeliverPeerContext(context.Background(), a, fmt.Sprintf("peer-%d", i+2), "context", peerCurrent)
			if err != nil || r.Mode != PeerHistoryInject || r.TurnID != before.Turn {
				t.Errorf("inject=%+v %v", r, err)
			}
		})
	}
	wg.Wait()
	after, _ := c.Observe(a.Binding)
	if after.Turn != before.Turn || after.State != "ready" {
		t.Fatalf("task changed=%+v", after)
	}
	if _, err := c.DeliverPeerContext(context.Background(), a, "peer-2", "retry", peerCurrent); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if _, err := c.DeliverUserTurn(context.Background(), a, "operator-steer", "more"); err != nil {
		t.Fatal(err)
	}
	wire := codexWire(t, log)
	if countMethod(wire, "turn/start") != 2 || countMethod(wire, "thread/inject_items") != 8 || countMethod(wire, "turn/steer") != 1 {
		t.Fatalf("wire=%v", wire)
	}
}

func TestCodexPeerNegativeAndCompletionRaceNoFallback(t *testing.T) {
	for _, mode := range []string{"codex-peer-queue", "codex-peer-refused", "codex-peer-malformed", "codex-peer-lost"} {
		t.Run(mode, func(t *testing.T) {
			c, _, log := codexStart(t, testHost(t, nil), mode)
			a := codexAuthority(c)
			if err := c.Turn(context.Background(), a, "operator", "hold"); err != nil {
				t.Fatal(err)
			}
			_, err := c.DeliverPeerContext(context.Background(), a, "peer", "context", peerCurrent)
			if (err == nil) != (mode == "codex-peer-queue") {
				t.Fatal(err)
			}
			if _, err := c.DeliverPeerContext(context.Background(), a, "peer", "retry", peerCurrent); err == nil {
				t.Fatal("uncertain/refused claim replayed")
			}
			wire := codexWire(t, log)
			if countMethod(wire, "turn/start") != 1 || countMethod(wire, "turn/steer") != 0 || countMethod(wire, "thread/inject_items") != 1 {
				t.Fatalf("fallback=%v", wire)
			}
			if mode == "codex-peer-queue" {
				s := observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
				if s.State != "ready" {
					t.Fatalf("completion=%+v", s)
				}
			}
		})
	}
}

func TestCodexPeerAdmissionFenceAndUnsupportedWriteZero(t *testing.T) {
	for _, mode := range []string{"codex-normal", "codex-peer-normal"} {
		t.Run(mode, func(t *testing.T) {
			c, _, log := codexStart(t, testHost(t, nil), mode)
			a := codexAuthority(c)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := c.DeliverPeerContext(ctx, a, "cancel", "x", peerCurrent); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			_, err := c.DeliverPeerContext(context.Background(), a, "peer", "x", func(context.Context) error {
				// Observe the same handle to prove route validation holds no p.mu.
				if _, err := c.Observe(a.Binding); err != nil {
					return err
				}
				return ErrStale
			})
			want := ErrStale
			if mode == "codex-normal" {
				want = codexappserver.ErrUnsupported
			}
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
			wire := codexWire(t, log)
			if countMethod(wire, "turn/start")+countMethod(wire, "turn/steer")+countMethod(wire, "thread/inject_items") != 0 {
				t.Fatal(wire)
			}
		})
	}
}
