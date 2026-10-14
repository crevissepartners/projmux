package app

import (
	"context"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestCodexAsyncAttentionRetainsRunningAndCompletedQuestion(t *testing.T) {
	f := newAsyncProcessFixture(t, nativeAppAsyncFrames[:2], false)
	b := f.endpoint.binding
	store, queue, projection := newProcessAttentionFixture(t, f.root, b, "codex")
	f.control.attention = projection
	f.turn(t, "first", "fixture")
	snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.AsyncQuestions) == 1 })
	result := processAgentCreateResult{Binding: b, Provider: aiModeCodex, Handle: f.endpoint.handle, codexEndpoint: f.endpoint, registryPath: f.path}
	if err := result.recordProcessSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	r := records[b.Pane]
	if r.Badge != aibadge.InProgress || r.badge() != aibadge.InputRequired || len(r.Pending) != 1 {
		t.Fatal("running and question conflated", r)
	}
	// Simulate a resync of the independently retained snapshot alongside a
	// blocking permission. Closing the async item must retain that permission.
	snap.Pending = []processhost.Request{{ID: "permission", Kind: "permission"}}
	if err = r.reconcilePending(snap, projection.push); err != nil {
		t.Fatal(err)
	}
	if len(r.Pending) != 2 {
		t.Fatal("independent pending lost")
	}
	snap.Turn = ""
	if _, err = r.applyEvent(processhost.Event{Kind: "turn-result"}); err != nil {
		t.Fatal(err)
	}
	if err = r.reconcilePending(snap, projection.push); err != nil || len(r.Pending) != 2 || r.badge() != aibadge.InputRequired {
		t.Fatal("turn completion removed async", err)
	}
	snap.AsyncQuestions[0].Closed = true
	if err = r.reconcilePending(snap, projection.push); err != nil || len(r.Pending) != 1 || r.badge() != aibadge.ApprovalRequired {
		t.Fatal("async close cleared permission", err)
	}
	notifications, err := queue.List()
	if err != nil || len(notifications) == 0 {
		t.Fatal("no attention", err)
	}
	if err = f.endpoint.handle.Stop(b); err != nil {
		t.Fatal(err)
	}
}
