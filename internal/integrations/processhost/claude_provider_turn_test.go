package processhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func fixtureFrame(t *testing.T, p *Handle, frame map[string]any) {
	t.Helper()
	p.mu.Lock()
	err := p.writeLocked(context.Background(), frame)
	p.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func eventsOf(p *Handle, kind string) []Event {
	var found []Event
	for _, e := range events(p) {
		if e.Kind == kind {
			found = append(found, e)
		}
	}
	return found
}

func startNotified(t *testing.T, completed chan Binding) *Handle {
	t.Helper()
	p, err := testHost(t, nil).Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal"), TurnCompleted: &TurnCompletion{Notify: func(b Binding) { completed <- b }}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Stop(binding())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = p.Wait(ctx, binding())
	})
	return p
}

func awaitCompleted(t *testing.T, completed chan Binding) {
	t.Helper()
	select {
	case b := <-completed:
		if b != binding() {
			t.Fatal(b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("result wake missing")
	}
}

func bound(t *testing.T, p *Handle) Authority {
	t.Helper()
	turn(t, p, "first", "ordinary")
	observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	return authority(p)
}

// A second init on the bound session with no admitted turn is a turn Claude
// opened itself (measured: background task completion). It must close on its
// result, wake Task 0's held-message release, and keep the session alive.
func TestClaudeProviderTurnSurvivesAndWakesRelease(t *testing.T) {
	completed := make(chan Binding, 8)
	p := startNotified(t, completed)
	a := bound(t, p)
	awaitCompleted(t, completed)
	fixtureFrame(t, p, map[string]any{"type": "fixture-provider-turn"})
	s := observeUntil(t, p, func(Snapshot) bool { return len(eventsOf(p, "provider-turn-ended")) == 1 })
	if s.Exit != nil || s.Failure != "" || s.State != "ready" || s.Turn != "" || s.Session != a.Session {
		t.Fatalf("provider turn did not close cleanly: %+v", s)
	}
	awaitCompleted(t, completed)
	started := eventsOf(p, "provider-turn-started")
	if len(started) != 1 || !strings.HasPrefix(started[0].Turn, "provider-") || !strings.Contains(string(started[0].Raw), `"attribution":"provider"`) {
		t.Fatalf("provider start evidence: %+v", started)
	}
	results := eventsOf(p, "turn-result")
	if len(results) != 2 || results[1].Turn != started[0].Turn {
		t.Fatalf("provider result not correlated: %+v", results)
	}
	for _, e := range eventsOf(p, "output") {
		if strings.Contains(string(e.Raw), "noticed") && e.Turn != started[0].Turn {
			t.Fatalf("provider output outside its turn: %+v", e)
		}
	}
	turn(t, p, "after-provider", "ordinary")
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && len(eventsOf(p, "turn-result")) == 3 })
}

// A result or provider frame with no admitted turn still opens and closes one.
func TestClaudeProviderTurnOpensOnResultOrOutput(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && len(eventsOf(p, "provider-turn-ended")) == 1 })
	fixtureFrame(t, p, map[string]any{"type": "fixture-provider-output"})
	s := observeUntil(t, p, func(s Snapshot) bool { return strings.HasPrefix(s.Turn, "provider-") })
	if err := p.ReserveClaudeMessage(context.Background(), a, "peer"); err != ErrClaudeTurnActive {
		t.Fatalf("peer admitted into provider turn: %v", err)
	}
	after, _ := p.Observe(binding())
	if after.Turn != s.Turn || after.MessageReservation != "" {
		t.Fatalf("ownership changed: %+v", after)
	}
	admission, err := p.UserInput(context.Background(), a, "joined-op", "joined")
	if err != nil || !admission.Joined || admission.Turn != s.Turn || admission.Origin != TurnOriginProvider {
		t.Fatalf("operator input did not join provider turn: %+v %v", admission, err)
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && len(eventsOf(p, "provider-turn-ended")) == 2 })
	if err = p.ReserveClaudeMessage(context.Background(), a, "peer"); err != nil {
		t.Fatalf("busy refusal consumed peer ID: %v", err)
	}
}

// A provider turn's permission request is pending and answerable; operator
// input over it stays busy with zero writes.
func TestClaudeProviderTurnControlPendingAndAnswerable(t *testing.T) {
	for _, frame := range []map[string]any{{"type": "fixture-provider-turn", "shape": "control"}, {"type": "fixture-provider-control"}} {
		t.Run(frame["type"].(string), func(t *testing.T) {
			p := start(t, testHost(t, nil), "normal")
			a := bound(t, p)
			fixtureFrame(t, p, frame)
			s := observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
			if !strings.HasPrefix(s.Turn, "provider-") || s.Pending[0].Turn != s.Turn {
				t.Fatalf("pending request not owned by provider turn: %+v", s)
			}
			sequence := s.Sequence
			if _, err := p.UserInput(context.Background(), a, "over-dialog", "joined"); err != ErrClaudeControlPending || !errors.Is(err, ErrBusy) {
				t.Fatalf("input over pending control: %v", err)
			}
			if err := p.Turn(context.Background(), a, "strict", "ordinary"); err != ErrBusy {
				t.Fatalf("strict turn admitted: %v", err)
			}
			if after, _ := p.Observe(binding()); after.Sequence != sequence {
				t.Fatalf("busy refusal wrote or emitted: %d -> %d", sequence, after.Sequence)
			}
			if err := p.Respond(context.Background(), a, s.Pending[0], Response{Allow: true}); err != nil {
				t.Fatal(err)
			}
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && len(eventsOf(p, "provider-turn-ended")) == 1 })
			if _, err := p.UserInput(context.Background(), a, "over-dialog", "ordinary"); err != nil {
				t.Fatalf("zero-write refusal consumed the operation: %v", err)
			}
			observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
		})
	}
}

// Operator input into a visibly open host turn is written and accepted as
// joined; the CLI folds it in and the turn still ends with one result.
func TestClaudeOperatorInputJoinsRunningTurnPreservesActiveOwnership(t *testing.T) {
	completed := make(chan Binding, 8)
	p := startNotified(t, completed)
	a := bound(t, p)
	awaitCompleted(t, completed)
	turn(t, p, "running", "hold")
	observeUntil(t, p, func(s Snapshot) bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.turnOpen
	})
	if err := p.Turn(context.Background(), a, "strict", "ordinary"); err != ErrBusy {
		t.Fatalf("Turn kept its one-turn contract: %v", err)
	}
	admission, err := p.UserInput(context.Background(), a, "join-1", "joined-result")
	if err != nil || admission != (TurnAdmission{Joined: true, Turn: "running", Origin: TurnOriginHost}) {
		t.Fatalf("join admission: %+v %v", admission, err)
	}
	if _, err = p.UserInput(context.Background(), a, "join-1", "joined"); err != ErrStale {
		t.Fatalf("duplicate joined operation: %v", err)
	}
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	awaitCompleted(t, completed)
	joined := eventsOf(p, "input-joined")
	if len(joined) != 1 || joined[0].Turn != "running" || !strings.Contains(string(joined[0].Raw), `"operation":"join-1"`) {
		t.Fatalf("joined evidence: %+v", joined)
	}
	results := eventsOf(p, "turn-result")
	if len(results) != 2 || results[1].Turn != "running" || hasEvent(p, "provider-turn-started") {
		t.Fatalf("joined input changed result correlation: %+v", results)
	}
}

// Input count and byte bounds refuse with zero writes and name the limit.
func TestClaudeJoinedInputBoundedBytesAndDuplicateOperations(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	open := func(id string) {
		turn(t, p, id, "hold")
		observeUntil(t, p, func(Snapshot) bool { p.mu.Lock(); defer p.mu.Unlock(); return p.turn == id && p.turnOpen })
	}
	open("count")
	for i := range ClaudeJoinedInputs {
		if _, err := p.UserInput(context.Background(), a, "count-"+string(rune('a'+i)), "joined"); err != nil {
			t.Fatal(i, err)
		}
	}
	before, _ := p.Observe(binding())
	_, err := p.UserInput(context.Background(), a, "count-over", "joined")
	if err != ErrClaudeJoinLimit || !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "8 inputs") || !strings.Contains(err.Error(), "262144 bytes") {
		t.Fatalf("count limit: %v", err)
	}
	if after, _ := p.Observe(binding()); after.Sequence != before.Sequence {
		t.Fatal("limit refusal emitted or wrote")
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	turn(t, p, "fresh", "ordinary")
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	if got := len(eventsOf(p, "input-joined")); got != ClaudeJoinedInputs {
		t.Fatalf("joined writes: %d", got)
	}

	open("bytes")
	large := "joined" + strings.Repeat("x", 200<<10)
	if _, err = p.UserInput(context.Background(), a, "bytes-1", large); err != nil {
		t.Fatal(err)
	}
	if _, err = p.UserInput(context.Background(), a, "bytes-2", large); err != ErrClaudeJoinLimit {
		t.Fatalf("byte limit: %v", err)
	}
	if _, err = p.UserInput(context.Background(), a, "bytes-2", "joined-result"); err != nil {
		t.Fatalf("refused operation stayed consumed: %v", err)
	}
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
}

// Input waits for a turn Claude has visibly opened, a completed peer handoff,
// and no pending interrupt; each refusal is a zero-write busy.
func TestClaudeJoinedInputRequiresVisibleUnfencedTurn(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	turn(t, p, "silent", "silent")
	if _, err := p.UserInput(context.Background(), a, "early", "joined"); err != ErrBusy {
		t.Fatalf("joined before the provider opened the turn: %v", err)
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })

	if err := p.ReserveClaudeMessage(context.Background(), a, "message"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UserInput(context.Background(), a, "during-handoff", "joined"); err != ErrBusy {
		t.Fatalf("joined a pending peer handoff: %v", err)
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "message", false, false); err != nil {
		t.Fatal(err)
	}

	turn(t, p, "interrupt", "interrupt")
	observeUntil(t, p, func(Snapshot) bool { p.mu.Lock(); defer p.mu.Unlock(); return p.turnOpen })
	if err := p.Interrupt(context.Background(), a, "interrupt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UserInput(context.Background(), a, "during-interrupt", "joined"); err != ErrBusy {
		t.Fatalf("joined an interrupted turn: %v", err)
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-release-interrupt"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	if hasEvent(p, "input-joined") {
		t.Fatal("refused input was written")
	}
}

// A joined input written as its turn ended may open the next turn in the CLI.
// The stream cannot prove that, so the next provider-opened turn records the
// input as written with unknown result attribution, and nothing is resent.
func TestClaudeJoinedInputAfterResultUnknownWriteNeverRetries(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	a := bound(t, p)
	turn(t, p, "running", "hold")
	observeUntil(t, p, func(Snapshot) bool { p.mu.Lock(); defer p.mu.Unlock(); return p.turnOpen })
	if _, err := p.UserInput(context.Background(), a, "late", "joined"); err != nil {
		t.Fatal(err)
	}
	fixtureFrame(t, p, map[string]any{"type": "fixture-result"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	fixtureFrame(t, p, map[string]any{"type": "fixture-provider-turn"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && hasEvent(p, "provider-turn-ended") })
	started := eventsOf(p, "provider-turn-started")
	var attribution struct {
		Attribution  string
		JoinedInputs []string
	}
	if len(started) != 1 || json.Unmarshal(started[0].Raw, &attribution) != nil || attribution.Attribution != "unknown" || len(attribution.JoinedInputs) != 1 || attribution.JoinedInputs[0] != "late" {
		t.Fatalf("carried attribution: %+v %+v", started, attribution)
	}
	unattributed := eventsOf(p, "joined-input-unattributed")
	if len(unattributed) != 1 || unattributed[0].Turn != started[0].Turn || !strings.Contains(string(unattributed[0].Raw), `"late"`) {
		t.Fatalf("unattributed evidence: %+v", unattributed)
	}
	if len(eventsOf(p, "input-joined")) != 1 {
		t.Fatal("joined input was resent")
	}
	// The next provider turn carries nothing; a host turn clears the carry.
	fixtureFrame(t, p, map[string]any{"type": "fixture-provider-turn"})
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" && len(eventsOf(p, "provider-turn-ended")) == 2 })
	if got := eventsOf(p, "provider-turn-started"); !strings.Contains(string(got[1].Raw), `"attribution":"provider"`) {
		t.Fatalf("carry repeated: %s", got[1].Raw)
	}
	if len(eventsOf(p, "joined-input-unattributed")) != 1 {
		t.Fatal("unattributed repeated")
	}
}

// Before the session is bound there is no ownership proof: an unowned init,
// control request or result stays a protocol failure, as does a provider turn
// on a different session after binding.
func TestClaudeUnownedProviderFramesRemainProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bind  bool
		frame map[string]any
	}{
		{"init-before-binding", false, map[string]any{"type": "fixture-provider-turn"}},
		{"control-before-binding", false, map[string]any{"type": "fixture-provider-control"}},
		{"result-before-binding", false, map[string]any{"type": "fixture-result"}},
		{"foreign-session", true, map[string]any{"type": "fixture-provider-turn", "session": "foreign"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := start(t, testHost(t, nil), "normal")
			if tc.bind {
				bound(t, p)
			}
			fixtureFrame(t, p, tc.frame)
			s := observeUntil(t, p, func(s Snapshot) bool { return s.Exit != nil })
			if !hasEvent(p, "protocol-error") || hasEvent(p, "provider-turn-started") {
				t.Fatalf("unowned frame admitted: %+v", s)
			}
			if !tc.bind && s.Session != "" {
				t.Fatalf("unowned init committed a session: %+v", s)
			}
		})
	}
}

// Stop during a provider turn reaches actual Wait without leaving the turn.
func TestClaudeProviderTurnStopWaitsAndLeavesNoOwnedChild(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	bound(t, p)
	fixtureFrame(t, p, map[string]any{"type": "fixture-provider-turn", "shape": "open"})
	observeUntil(t, p, func(s Snapshot) bool { return strings.HasPrefix(s.Turn, "provider-") })
	if err := p.Stop(binding()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := p.Wait(ctx, binding())
	if err != nil || s.Exit == nil || s.PID == 0 {
		t.Fatalf("Wait: %+v %v", s, err)
	}
	if _, ok := s.Termination(time.Now()); !ok {
		t.Fatal("no termination evidence")
	}
}
