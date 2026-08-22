package app

import (
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func testCodexLifecycleIdentity() codexLifecycleIdentity {
	return codexLifecycleIdentity{
		AgentUID: "agent-1", PaneUID: "pane-1", RuntimeID: "%9",
		Generation: "generation-1", ThreadID: "thread-1",
	}
}

func TestCodexLifecycleApprovalRequiresExactPendingAndWaitingPair(t *testing.T) {
	r := &codexLifecycleReducer{}
	identity := testCodexLifecycleIdentity()
	begin := r.begin(1, identity, codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	})
	if !begin.Accepted || begin.Interaction != coremetadata.InteractionInProgress {
		t.Fatalf("begin = %#v", begin)
	}

	waiting := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleThreadStatus, ThreadID: "thread-1",
		ThreadState: codexappserver.ThreadStateWaitingOnApproval,
	})
	if waiting.Interaction != coremetadata.InteractionInProgress || len(waiting.Notices) != 0 {
		t.Fatalf("waiting without request = %#v", waiting)
	}

	autoApproved := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "never-pending",
	})
	if autoApproved.Accepted {
		t.Fatalf("resolved non-pending request changed state: %#v", autoApproved)
	}

	pending := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleApprovalPending, ThreadID: "thread-1", TurnID: "turn-1",
		ItemID: "item-1", RequestID: "request-1", ApprovalKind: codexappserver.ApprovalCommand,
	})
	if pending.Interaction != coremetadata.InteractionApprovalRequired || len(pending.Notices) != 1 {
		t.Fatalf("exact pending+waiting = %#v", pending)
	}
	if pending.Notices[0].TurnID != "turn-1" || pending.Notices[0].ItemID != "item-1" || pending.Notices[0].RequestID != "request-1" {
		t.Fatalf("approval identity = %#v", pending.Notices[0])
	}
	leftWaiting := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleThreadStatus, ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
	})
	if leftWaiting.Interaction != coremetadata.InteractionInProgress || len(leftWaiting.ClearNoticeIDs) != 1 {
		t.Fatalf("approval no longer waiting must clear critical queue: %#v", leftWaiting)
	}
	returnedWaiting := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleThreadStatus, ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval,
	})
	if returnedWaiting.Interaction != coremetadata.InteractionApprovalRequired || len(returnedWaiting.Notices) != 1 {
		t.Fatalf("still unresolved and waiting again must re-notify: %#v", returnedWaiting)
	}

	duplicate := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleApprovalPending, ThreadID: "thread-1", TurnID: "turn-1",
		ItemID: "item-1", RequestID: "request-1", ApprovalKind: codexappserver.ApprovalCommand,
	})
	if len(duplicate.Notices) != 0 {
		t.Fatalf("duplicate approval notified: %#v", duplicate)
	}

	resolved := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "request-1",
	})
	if resolved.Interaction != coremetadata.InteractionInProgress || len(resolved.ClearNoticeIDs) != 1 {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestCodexLifecycleAutoApprovedRequestNeverProjectsAttention(t *testing.T) {
	r := &codexLifecycleReducer{}
	r.begin(1, testCodexLifecycleIdentity(), codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	})
	sequence := []codexappserver.LifecycleEvent{
		{Kind: codexappserver.LifecycleApprovalPending, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-auto", RequestID: "request-auto", ApprovalKind: codexappserver.ApprovalCommand},
		{Kind: codexappserver.LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "request-auto"},
		{Kind: codexappserver.LifecycleThreadStatus, ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval},
	}
	for index, event := range sequence {
		projection := r.apply(1, event)
		if !projection.Accepted || projection.Interaction != coremetadata.InteractionInProgress || len(projection.Notices) != 0 {
			t.Fatalf("auto-approved step %d projected badge/queue/desktop attention: %#v", index, projection)
		}
	}
}

func TestCodexLifecycleResolvedRequestUsesStoredExactTuple(t *testing.T) {
	r := &codexLifecycleReducer{}
	r.begin(1, testCodexLifecycleIdentity(), codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	})
	original := codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleApprovalPending, ThreadID: "thread-1", TurnID: "turn-1",
		ItemID: "item-original", RequestID: "request-shared", ApprovalKind: codexappserver.ApprovalCommand,
	}
	projected := r.apply(1, original)
	if !projected.Accepted || len(projected.Notices) != 1 {
		t.Fatalf("original approval = %#v", projected)
	}
	originalNoticeID := projected.Notices[0].ID

	collision := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleApprovalPending, ThreadID: "thread-1", TurnID: "turn-1",
		ItemID: "item-collision", RequestID: "request-shared", ApprovalKind: codexappserver.ApprovalCommand,
	})
	if collision.Accepted || len(collision.Notices) != 0 || len(collision.ClearNoticeIDs) != 0 {
		t.Fatalf("request ID collision replaced exact tuple: %#v", collision)
	}

	resolved := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "request-shared",
	})
	if !resolved.Accepted || len(resolved.ClearNoticeIDs) != 1 || resolved.ClearNoticeIDs[0] != originalNoticeID {
		t.Fatalf("resolved did not clear stored exact tuple: %#v", resolved)
	}

	r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleApprovalPending, ThreadID: "thread-1", TurnID: "turn-1",
		ItemID: "item-stale", RequestID: "request-stale", ApprovalKind: codexappserver.ApprovalCommand,
	})
	r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleTurnStarted, ThreadID: "thread-1", TurnID: "turn-2",
	})
	late := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "request-stale",
	})
	if late.Accepted || len(late.Notices) != 0 || len(late.ClearNoticeIDs) != 0 {
		t.Fatalf("prior-turn resolution changed current turn: %#v", late)
	}
}

func TestCodexLifecycleCompletionIsExactSuccessfulAndOnce(t *testing.T) {
	for _, test := range []struct {
		name        string
		state       codexappserver.TurnState
		interaction coremetadata.AgentInteractionKind
		notices     int
	}{
		{name: "completed", state: codexappserver.TurnStateCompleted, interaction: coremetadata.InteractionResponseComplete, notices: 1},
		{name: "failed", state: codexappserver.TurnStateFailed, interaction: coremetadata.InteractionIdle},
		{name: "interrupted", state: codexappserver.TurnStateInterrupted, interaction: coremetadata.InteractionIdle},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &codexLifecycleReducer{}
			r.begin(7, testCodexLifecycleIdentity(), codexappserver.LifecycleSnapshot{
				ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
				TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
			})
			got := r.apply(7, codexappserver.LifecycleEvent{
				Kind: codexappserver.LifecycleTurnCompleted, ThreadID: "thread-1", TurnID: "turn-1", TurnState: test.state,
			})
			if got.Interaction != test.interaction || len(got.Notices) != test.notices {
				t.Fatalf("terminal = %#v", got)
			}
			if duplicate := r.apply(7, codexappserver.LifecycleEvent{
				Kind: codexappserver.LifecycleTurnCompleted, ThreadID: "thread-1", TurnID: "turn-1", TurnState: test.state,
			}); duplicate.Accepted {
				t.Fatalf("duplicate terminal accepted: %#v", duplicate)
			}
		})
	}
}

func TestCodexLifecycleInvalidationRejectsLateAndReplacementEvents(t *testing.T) {
	r := &codexLifecycleReducer{}
	r.begin(1, testCodexLifecycleIdentity(), codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	})
	if got := r.invalidate(1); !got.Invalidated || got.Interaction != coremetadata.InteractionUnknown {
		t.Fatalf("invalidate = %#v", got)
	}
	late := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleTurnCompleted, ThreadID: "thread-1", TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted,
	})
	if late.Accepted || len(late.Notices) != 0 {
		t.Fatalf("late event accepted: %#v", late)
	}
	r.begin(2, testCodexLifecycleIdentity(), codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle,
	})
	if stale := r.apply(1, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleTurnStarted, ThreadID: "thread-1", TurnID: "turn-old",
	}); stale.Accepted {
		t.Fatalf("old epoch event accepted: %#v", stale)
	}
	if foreign := r.apply(2, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleTurnStarted, ThreadID: "thread-foreign", TurnID: "turn-foreign",
	}); foreign.Accepted {
		t.Fatalf("foreign thread event accepted: %#v", foreign)
	}
}

func TestCodexLifecycleNotLoadedSnapshotIsImmediateInvalidation(t *testing.T) {
	r := &codexLifecycleReducer{}
	projection := r.begin(3, testCodexLifecycleIdentity(), codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateNotLoaded,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted,
	})
	if !projection.Accepted || !projection.Invalidated || projection.Interaction != coremetadata.InteractionUnknown || len(projection.Notices) != 0 {
		t.Fatalf("not-loaded snapshot = %#v", projection)
	}
	if late := r.apply(3, codexappserver.LifecycleEvent{
		Kind: codexappserver.LifecycleTurnCompleted, ThreadID: "thread-1", TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted,
	}); late.Accepted {
		t.Fatalf("not-loaded epoch accepted late completion: %#v", late)
	}
}

func FuzzCodexLifecycleEventSequence(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{3, 2, 4, 2, 3, 5})
	f.Fuzz(func(t *testing.T, sequence []byte) {
		if len(sequence) > 128 {
			sequence = sequence[:128]
		}
		r := &codexLifecycleReducer{}
		r.begin(1, testCodexLifecycleIdentity(), codexappserver.LifecycleSnapshot{
			ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
			TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
		})
		for index, raw := range sequence {
			epoch := uint64(1)
			threadID := "thread-1"
			if raw&0x40 != 0 {
				epoch = 99
			}
			if raw&0x80 != 0 {
				threadID = "foreign"
			}
			requestID := "request-1"
			if index%2 == 1 {
				requestID = "request-2"
			}
			var event codexappserver.LifecycleEvent
			switch raw % 6 {
			case 0:
				event = codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleTurnStarted, ThreadID: threadID, TurnID: "turn-1"}
			case 1:
				event = codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleThreadStatus, ThreadID: threadID, ThreadState: codexappserver.ThreadStateWaitingOnApproval}
			case 2:
				event = codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleApprovalPending, ThreadID: threadID, TurnID: "turn-1", ItemID: "item-1", RequestID: requestID, ApprovalKind: codexappserver.ApprovalCommand}
			case 3:
				event = codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleRequestResolved, ThreadID: threadID, RequestID: requestID}
			case 4:
				event = codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleTurnCompleted, ThreadID: threadID, TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted}
			case 5:
				event = codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleTurnCompleted, ThreadID: threadID, TurnID: "turn-1", TurnState: codexappserver.TurnStateFailed}
			}
			projection := r.apply(epoch, event)
			if projection.Accepted && !coremetadata.ValidAgentInteractionKind(projection.Interaction) {
				t.Fatalf("open interaction %q after %#v", projection.Interaction, event)
			}
			for _, notice := range projection.Notices {
				if notice.Category == "approval_required" && (notice.ThreadID != "thread-1" || notice.TurnID == "" || notice.ItemID == "" || notice.RequestID == "") {
					t.Fatalf("inexact approval notice: %#v", notice)
				}
				if notice.Category == "response_complete" && event.TurnState != codexappserver.TurnStateCompleted {
					t.Fatalf("non-success completion notice: event=%#v notice=%#v", event, notice)
				}
			}
		}
	})
}
