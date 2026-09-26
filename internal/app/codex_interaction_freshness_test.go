package app

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

type codexFreshnessClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *codexFreshnessClock) current() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *codexFreshnessClock) advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

type codexFreshnessSink struct {
	*recordingCodexLifecycleSink
	clock *codexFreshnessClock
	mu    sync.Mutex
	agent coremetadata.Agent
}

func (s *codexFreshnessSink) Apply(identity codexLifecycleIdentity, p codexLifecycleProjection) error {
	s.mu.Lock()
	s.agent.Status.Interaction = coremetadata.AgentInteraction{
		Kind: p.Interaction, ObservedAt: s.clock.current(), Source: string(coremetadata.InteractionSourceProviderControl),
	}
	s.mu.Unlock()
	return s.recordingCodexLifecycleSink.Apply(identity, p)
}

func (s *codexFreshnessSink) effective() coremetadata.AgentInteractionKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agent.EffectiveInteraction(s.clock.current()).Kind
}

func TestCodexNativeObserverRefreshesLongTurnAndWaits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   coremetadata.AgentInteractionKind
		events []codexappserver.Notification
	}{
		{name: "turn", want: coremetadata.InteractionInProgress},
		{name: "approval", want: coremetadata.InteractionApprovalRequired, events: []codexappserver.Notification{
			{Method: "item/commandExecution/requestApproval", RequestID: "approval-1", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1"}`)},
			{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"thread-1","status":{"type":"active","activeFlags":["waitingOnApproval"]}}`)},
		}},
		{name: "input", want: coremetadata.InteractionInputRequired, events: []codexappserver.Notification{
			{Method: "item/tool/requestUserInput", RequestID: "input-1", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","questions":[{"question":"PRIVATE"}]}`)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity := testCodexLifecycleIdentity()
			clock := &codexFreshnessClock{now: time.Unix(1_700_000_000, 0).UTC()}
			sink := &codexFreshnessSink{
				recordingCodexLifecycleSink: newRecordingCodexLifecycleSink(), clock: clock,
				agent: coremetadata.Agent{Status: coremetadata.AgentStatus{Phase: coremetadata.PhaseRunning, PaneRef: identity.PaneUID}},
			}
			conn := &fakeCodexLifecycleConnection{
				snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
				events:   make(chan codexappserver.Notification, 4),
			}
			ticks := make(chan time.Time)
			sendTick := func() {
				t.Helper()
				select {
				case ticks <- clock.current():
				case <-time.After(time.Second):
					t.Fatal("observer did not receive refresh tick")
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			observer := codexNativeObserver{
				identity: identity, sink: sink, now: clock.current, refreshTicks: ticks,
				open: func(context.Context) (codexLifecycleConnection, error) { return conn, nil },
			}
			done := make(chan error, 1)
			go func() { done <- observer.Run(ctx) }()
			waitForCodexObserverEvents(t, sink.recordingCodexLifecycleSink, 2)
			for _, event := range tc.events {
				conn.events <- event
			}
			writes := 2 + len(tc.events)
			waitForCodexObserverEvents(t, sink.recordingCodexLifecycleSink, writes)
			if got := sink.effective(); got != tc.want {
				t.Fatalf("initial interaction = %s, want %s", got, tc.want)
			}
			// Quiet recommits keep the same state past the read-model horizon.
			for i := 0; i < 3; i++ {
				clock.advance(codexObserverInteractionRefreshInterval)
				sendTick()
				writes++
				waitForCodexObserverEvents(t, sink.recordingCodexLifecycleSink, writes)
				if got := sink.effective(); got != tc.want {
					t.Fatalf("refreshed interaction = %s, want %s", got, tc.want)
				}
			}
			clock.advance(time.Minute)
			if got := sink.effective(); got != tc.want {
				t.Fatalf("interaction after %s = %s, want %s", coremetadata.AgentInteractionFreshFor+time.Minute, got, tc.want)
			}
			// Release the state, then a later tick must not resurrect it.
			conn.events <- codexappserver.Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`)}
			writes++
			waitForCodexObserverEvents(t, sink.recordingCodexLifecycleSink, writes)
			if got := sink.effective(); got != coremetadata.InteractionResponseComplete {
				t.Fatalf("released interaction = %s, want response_complete", got)
			}
			clock.advance(codexObserverInteractionRefreshInterval)
			sendTick()
			// Receiving the next unbuffered tick proves the first tick was
			// processed. A completed turn must not regain an active projection.
			sendTick()
			if got := len(sink.snapshot()); got != writes {
				t.Fatalf("post-completion refresh wrote %d events, want %d", got, writes)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodexUserInputRequestResolvesWithoutAnswerChannel(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	var reducer codexLifecycleReducer
	reducer.begin(1, identity, codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress})
	request := codexappserver.Notification{Method: "item/tool/requestUserInput", RequestID: "input-1", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","questions":[{"question":"PRIVATE"}]}`)}
	projection, recognized, err := reducer.applyUserInputRequest(1, request)
	if err != nil || !recognized || projection.Interaction != coremetadata.InteractionInputRequired || len(projection.Notices) != 0 {
		t.Fatalf("question projection = %+v recognized=%t err=%v", projection, recognized, err)
	}
	waiting := reducer.apply(1, codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleThreadStatus, ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateWaitingOnUserInput})
	if waiting.Interaction != coremetadata.InteractionInputRequired {
		t.Fatalf("waiting interaction = %+v, want input_required", waiting)
	}
	resolved := reducer.apply(1, codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleRequestResolved, ThreadID: identity.ThreadID, RequestID: "input-1"})
	if resolved.Interaction != coremetadata.InteractionInProgress {
		t.Fatalf("resolved interaction = %+v, want in_progress", resolved)
	}
	reducer.apply(1, codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleTurnCompleted, ThreadID: identity.ThreadID, TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted})
	if refresh := reducer.refreshProjection(1); refresh.Accepted {
		t.Fatalf("completed turn can no longer refresh an active state: %+v", refresh)
	}
}
