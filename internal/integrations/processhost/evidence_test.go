package processhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestWaitEvidenceUsesMetadataGuards(t *testing.T) {
	p := start(t, testHost(t, nil), "exit0")
	s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "exited" })
	receipt, ok := s.Termination(time.Now())
	if !ok || receipt.Classification != metadata.TerminationNormal {
		t.Fatal(receipt)
	}
	reg := metadata.NewRegistry()
	reg.Panes = []metadata.Pane{{Metadata: metadata.ObjectMeta{UID: "pane", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindAgent, UID: "agent"}}, Status: metadata.PaneStatus{Activation: metadata.PaneActivation{Generation: "gen", AgentUID: "agent"}}}}
	reg.Agents = []metadata.Agent{{Metadata: metadata.ObjectMeta{UID: "agent"}, Status: metadata.AgentStatus{PaneRef: "pane"}}}
	mutator := metadata.Mutator{}
	for _, change := range []func(*metadata.TerminationEvidence){func(e *metadata.TerminationEvidence) { e.Generation = "old" }, func(e *metadata.TerminationEvidence) { e.AgentUID = "foreign" }} {
		stale := receipt
		change(&stale)
		before, _ := json.Marshal(reg)
		outcome, err := mutator.RecordTermination(&reg, stale)
		after, _ := json.Marshal(reg)
		if err != nil || !outcome.Stale || !bytes.Equal(before, after) {
			t.Fatalf("stale receipt wrote: %+v %v", outcome, err)
		}
	}
	outcome, err := mutator.RecordTermination(&reg, receipt)
	if err != nil || !outcome.Applied {
		t.Fatal(outcome, err)
	}
	before, _ := json.Marshal(reg)
	outcome, err = mutator.RecordTermination(&reg, receipt)
	after, _ := json.Marshal(reg)
	if err != nil || !outcome.Duplicate || !bytes.Equal(before, after) {
		t.Fatal("duplicate changed registry")
	}
	s.State = "unknown"
	s.Exit = nil
	if _, ok := s.Termination(time.Now()); ok {
		t.Fatal("host disappearance fabricated exit")
	}
}

func TestProtocolBoundsAndRequestDisconnection(t *testing.T) {
	p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.Events = 8 }), "normal")
	turn(t, p, "hold", "hold")
	observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" })
	// Direct deterministic NDJSON boundary: duplicate interrupt success is a
	// single transition even when the provider repeats it thousands of times.
	p.mu.Lock()
	p.interrupt = "interrupt-hold"
	p.mu.Unlock()
	ack := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"interrupt-hold"}}`)
	for range 10000 {
		if err := p.consume(ack); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for _, e := range events(p) {
		if e.Kind == "interrupt-ack" {
			count++
		}
	}
	if count != 1 {
		t.Fatal(count)
	}
	for range 100 {
		if err := p.consume([]byte(`{"type":"stream_event","session_id":"session"}`)); err != nil {
			t.Fatal(err)
		}
	}
	last := uint64(0)
	for _, e := range events(p) {
		if e.Sequence <= last {
			t.Fatalf("out of order %d after %d", e.Sequence, last)
		}
		last = e.Sequence
	}
	request := []byte(`{"type":"control_request","request_id":"pending","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{}}}`)
	if err := p.consume(request); err != nil {
		t.Fatal(err)
	}
	s, _ := p.Observe(binding())
	a := authority(p)
	_ = p.Stop(binding())
	if err := p.Respond(context.Background(), a, s.Pending[0], Response{Allow: true}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if hasEvent(p, "control-answered") || !hasEvent(p, "control-expired") {
		t.Fatal("disconnect allowed request")
	}
}

func TestReadinessAndOwnershipFailureRollBack(t *testing.T) {
	t.Run("init-timeout", func(t *testing.T) {
		p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.Startup = 50 * time.Millisecond }), "blocked")
		turn(t, p, "first", "hello")
		s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "exited" })
		if !strings.Contains(s.Failure, "initialization timeout") || hasEvent(p, "ready") {
			t.Fatal(s)
		}
	})
	t.Run("current-chain", func(t *testing.T) {
		p := start(t, testHost(t, func(tx *Transactions, _ *Limits) {
			tx.Current = func(context.Context, Binding) error { return ErrStale }
		}), "normal")
		if err := p.Turn(context.Background(), authority(p), "1", "normal"); err != ErrStale {
			t.Fatal(err)
		}
		if hasEvent(p, "turn-submitted") {
			t.Fatal("foreign owner wrote")
		}
	})
	t.Run("supervisor-crash", func(t *testing.T) {
		if err := prepareReaper(); err != nil {
			t.Fatal(err)
		}
		p := start(t, testHost(t, nil), "normal")
		s, _ := p.Observe(binding())
		supervisor, err := os.FindProcess(s.SupervisorPID)
		if err != nil {
			t.Fatal(err)
		}
		if err := supervisor.Kill(); err != nil {
			t.Fatal(err)
		}
		s = observeUntil(t, p, func(s Snapshot) bool { return s.State == "unknown" })
		if s.Exit != nil {
			t.Fatal("supervisor crash fabricated receipt")
		}
		// The product deliberately does not signal an orphan by stored PID after
		// losing its supervisor authority. This test owns the isolated fixture and
		// cleans up the injected crash explicitly.
		_ = syscall.Kill(-s.PID, syscall.SIGKILL)
		// Only fixture descendants may have been reparented to this test's Linux
		// subreaper by the independent lifetime test. Reap this exact owned child.
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			var status syscall.WaitStatus
			_, _ = syscall.Wait4(s.PID, &status, syscall.WNOHANG, nil)
			if syscall.Kill(s.PID, 0) == syscall.ESRCH {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("owned child remains after supervisor crash")
	})
}

func TestClaudeCommandPreservesLaunchPolicy(t *testing.T) {
	h := testHost(t, nil)
	if _, err := NewHost("host", Command{Path: os.Args[0]}, h.tx, DefaultLimits()); err == nil {
		t.Fatal("supervisor accepted implicit inherited environment")
	}
	args := []string{"--permission-mode", "manual", "--settings", "configured.json", "--model", "haiku"}
	cmd, err := ClaudeCommand("claude", "/work", []string{"HOME=/home/explicit"}, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.Join(cmd.Args, " "), strings.Join(args, " ")) || cmd.Env[0] != "HOME=/home/explicit" {
		t.Fatal(cmd)
	}
	for _, bad := range []string{"--output-format=json", "--input-format", "--permission-prompt-tool", "--"} {
		if _, err := ClaudeCommand("claude", "", nil, []string{bad}); err == nil {
			t.Fatal("transport override accepted", bad)
		}
	}
}

func TestOversizedResponseExpiresWithoutWireAllow(t *testing.T) {
	p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.FrameBytes = 1024 }), "normal")
	turn(t, p, "q", "question")
	s := observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
	err := p.Respond(context.Background(), authority(p), s.Pending[0], Response{Answers: map[string]string{"Color?": strings.Repeat("x", 2048)}})
	if err == nil || hasEvent(p, "control-answered") || !hasEvent(p, "control-expired") {
		t.Fatal("oversized response lost terminal state", err)
	}
	if err := p.Respond(context.Background(), authority(p), s.Pending[0], Response{Answers: map[string]string{"Color?": "blue"}}); err != ErrStale {
		t.Fatal("failed response retried", err)
	}
}

func TestProviderCancellationExpiresExactRequest(t *testing.T) {
	for _, prompt := range []string{"question", "allow"} {
		t.Run(prompt, func(t *testing.T) {
			p := start(t, testHost(t, nil), "normal")
			turn(t, p, "cancelled", prompt)
			s := observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
			req := s.Pending[0]
			send := func(id string) {
				p.mu.Lock()
				err := p.writeLocked(context.Background(), map[string]any{"type": "fixture-cancel", "request_id": id})
				p.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			echoes := func() int {
				events, _, _ := p.Events(binding(), 0)
				n := 0
				for _, e := range events {
					if bytes.Contains(e.Raw, []byte("cancel_echo")) {
						n++
					}
				}
				return n
			}
			send("foreign-request")
			s = observeUntil(t, p, func(Snapshot) bool { return echoes() == 1 })
			if len(s.Pending) != 1 {
				t.Fatal("foreign cancellation consumed pending request")
			}
			send(req.ID)
			send(req.ID)
			s = observeUntil(t, p, func(Snapshot) bool { return echoes() == 3 })
			if len(s.Pending) != 0 || s.Turn != "cancelled" || s.State != "ready" {
				t.Fatal(s)
			}
			if err := p.Respond(context.Background(), authority(p), req, Response{Allow: true}); err != ErrStale {
				t.Fatal("cancelled request wrote", err)
			}
			events, _, _ := p.Events(binding(), 0)
			expired := 0
			for _, event := range events {
				if event.Kind == "control-expired" {
					expired++
				}
				if bytes.Contains(event.Raw, []byte("response_echo")) {
					t.Fatal("cancel wrote provider response")
				}
			}
			if expired != 1 || hasEvent(p, "control-answered") {
				t.Fatalf("non-idempotent cancel: %d", expired)
			}
			p.mu.Lock()
			err := p.writeLocked(context.Background(), map[string]any{"type": "fixture-release-interrupt"})
			p.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
			turn(t, p, "after-cancel", "normal")
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
		})
	}
}
