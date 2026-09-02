package app

import (
	"sort"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// CodexLifecycleTestHarness is a test-only adapter around the production
// reducer. External property tests use this narrow seam so their reference
// transition model cannot read or reuse production reducer state as its
// expected oracle.
type CodexLifecycleTestHarness struct {
	reducer codexLifecycleReducer
}

type CodexLifecycleTestSnapshot struct {
	Epoch       uint64
	ThreadID    string
	ThreadState string
	TurnID      string
	TurnState   string
}

type CodexLifecycleTestEvent struct {
	Epoch        uint64
	Kind         string
	ThreadID     string
	TurnID       string
	ItemID       string
	RequestID    string
	ThreadState  string
	TurnState    string
	ApprovalKind string
}

type CodexLifecycleTestPending struct {
	TurnID       string
	ItemID       string
	RequestID    string
	ApprovalKind string
	Notified     bool
}

type CodexLifecycleTestState struct {
	Epoch       uint64
	Active      bool
	Interaction string
	Pending     []CodexLifecycleTestPending
}

type CodexLifecycleTestResult struct {
	Accepted    bool
	Invalidated bool
	State       CodexLifecycleTestState
}

func (h *CodexLifecycleTestHarness) Snapshot(snapshot CodexLifecycleTestSnapshot) CodexLifecycleTestResult {
	projection := h.reducer.begin(snapshot.Epoch, testCodexLifecycleIdentity(), codexappserver.LifecycleSnapshot{
		ThreadID:    snapshot.ThreadID,
		ThreadState: codexappserver.ThreadState(snapshot.ThreadState),
		TurnID:      snapshot.TurnID,
		TurnState:   codexappserver.TurnState(snapshot.TurnState),
	})
	return h.result(projection)
}

func (h *CodexLifecycleTestHarness) Event(event CodexLifecycleTestEvent) CodexLifecycleTestResult {
	projection := h.reducer.apply(event.Epoch, codexappserver.LifecycleEvent{
		Kind:         codexappserver.LifecycleEventKind(event.Kind),
		ThreadID:     event.ThreadID,
		TurnID:       event.TurnID,
		ItemID:       event.ItemID,
		RequestID:    event.RequestID,
		ThreadState:  codexappserver.ThreadState(event.ThreadState),
		TurnState:    codexappserver.TurnState(event.TurnState),
		ApprovalKind: codexappserver.ApprovalKind(event.ApprovalKind),
	})
	return h.result(projection)
}

func (h *CodexLifecycleTestHarness) Invalidate(epoch uint64) CodexLifecycleTestResult {
	return h.result(h.reducer.invalidate(epoch))
}

func (h *CodexLifecycleTestHarness) State() CodexLifecycleTestState {
	return h.state()
}

func (h *CodexLifecycleTestHarness) result(projection codexLifecycleProjection) CodexLifecycleTestResult {
	return CodexLifecycleTestResult{
		Accepted: projection.Accepted, Invalidated: projection.Invalidated, State: h.state(),
	}
}

func (h *CodexLifecycleTestHarness) state() CodexLifecycleTestState {
	pending := make([]CodexLifecycleTestPending, 0, len(h.reducer.pending))
	for _, request := range h.reducer.pending {
		pending = append(pending, CodexLifecycleTestPending{
			TurnID: request.TurnID, ItemID: request.ItemID, RequestID: request.RequestID,
			ApprovalKind: string(request.Kind), Notified: request.Notified,
		})
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].RequestID < pending[j].RequestID })
	return CodexLifecycleTestState{
		Epoch: h.reducer.epoch, Active: h.reducer.active,
		Interaction: string(h.reducer.interaction), Pending: pending,
	}
}
