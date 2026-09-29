package app

import (
	"context"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// lifecycle_window_unlinked_projection_test.go covers the window-unlinked
// absence projection: a raw `tmux kill-session` fires only window-unlinked, so
// that hook is where the Agents of the killed session learn their Panes are
// gone. The projection is confined to the one Registry Window the exact `$N/@N`
// hook handles name, and only after the same observation proves that Window
// and each projected Pane absent from the hook server.

// windowUnlinkedProjectionFixture binds agt-alpha-codex's Pane in
// win-alpha-main to the exact `$1/@4` handles the unlink event names, and
// observes every other Registry Pane and Window live. Only the win-alpha-main
// subtree is absent: the shape a killed session leaves behind.
func windowUnlinkedProjectionFixture(t *testing.T) (*fakeResourceStore, *exactPaneExitInventory, lifecycleDirtyEvent) {
	t.Helper()
	store := newFakeResourceStore(t)
	activateExactPane(t, store, "pan-alpha-codex", "agt-alpha-codex", "gen-kill-session", "%9")
	live, windows := map[string]bool{}, map[string]bool{}
	for _, pane := range store.registry.Panes {
		if owner, ok := paneWindowUID(store.registry, pane); ok && owner != "win-alpha-main" {
			live[pane.Metadata.UID] = true
		}
	}
	for _, window := range store.registry.Windows {
		if window.Metadata.UID != "win-alpha-main" {
			windows[window.Metadata.UID] = true
		}
	}
	event := exactPaneExitDirty()
	event.teardownKind = coremetadata.TeardownEventWindowUnlinked
	event.runtimePaneID = ""
	event.runtimeSessionID = "$1"
	event.runtimeWindowID = "@4"
	return store, &exactPaneExitInventory{uids: live, windows: windows, windowSessions: map[string]int{}}, event
}

// TestWindowUnlinkedProjectsTheAbsentPanesOfItsExactWindowOffline is the
// kill-session case: the hook's exact Window and its Agent Pane are gone, so one
// window-unlinked pass lowers the Agent to Offline and releases its paneRef.
// Nothing outside that Window is written.
func TestWindowUnlinkedProjectsTheAbsentPanesOfItsExactWindowOffline(t *testing.T) {
	t.Parallel()
	store, inventory, event := windowUnlinkedProjectionFixture(t)
	beta, _ := store.registry.Agent("agt-beta-codex")
	betaBefore := beta.Clone()
	review, _ := store.registry.Pane("pan-alpha-review")
	reviewBefore := review.Clone()

	result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil {
		t.Fatal(err)
	}
	if result.transactions != 1 || len(result.rootCascaded) != 0 || len(result.cascaded) != 0 || len(result.pending) != 0 {
		t.Fatalf("window-unlinked result = %+v, want one projection-only transaction", result)
	}
	projected := map[string]bool{}
	for _, projection := range result.projected {
		projected[projection.PaneUID] = true
	}
	for _, uid := range []string{"pan-alpha-zsh", "pan-alpha-log", "pan-alpha-codex"} {
		if !projected[uid] {
			t.Fatalf("projected %v, want the absent Pane %s of the exact Window", result.projected, uid)
		}
	}
	if len(projected) != 3 {
		t.Fatalf("projected %v, want only the three Panes of win-alpha-main", result.projected)
	}
	agent, _ := store.registry.Agent("agt-alpha-codex")
	if agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("Agent after window-unlinked = %+v, want Offline with no paneRef", agent.Status)
	}
	if beta, _ := store.registry.Agent("agt-beta-codex"); beta.Status.Phase != betaBefore.Status.Phase ||
		beta.Status.PaneRef != betaBefore.Status.PaneRef || beta.Status.Reason != betaBefore.Status.Reason {
		t.Fatalf("another Project's Agent = %+v, want %+v", beta.Status, betaBefore.Status)
	}
	if review, _ := store.registry.Pane("pan-alpha-review"); len(review.Status.Conditions) != len(reviewBefore.Status.Conditions) ||
		review.Status.LastTermination != nil {
		t.Fatalf("sibling Window Pane = %+v, want it untouched", review.Status)
	}

	// The same hook again finds nothing left to project and opens no transaction.
	writes := store.writes
	again, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
	if err != nil || again.transactions != 0 || store.writes != writes {
		t.Fatalf("repeat window-unlinked = %+v (%v), writes %d -> %d, want a no-op", again, err, writes, store.writes)
	}
}

// TestWindowUnlinkedNeverWidensItsAbsenceProjection holds every refusal of the
// window-unlinked projection. Each case keeps the Agent Pane absent, so each
// would lower the Agent if the event were read as whole-host absence.
func TestWindowUnlinkedNeverWidensItsAbsenceProjection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		adjust func(*fakeResourceStore, *exactPaneExitInventory, *lifecycleDirtyEvent)
	}{
		{
			// move-window or unlink-window: the Window survives on the server,
			// so the event is not evidence that anything under it ended.
			name: "the exact Window is still live",
			adjust: func(_ *fakeResourceStore, inventory *exactPaneExitInventory, _ *lifecycleDirtyEvent) {
				inventory.windows["win-alpha-main"] = true
			},
		},
		{
			name: "the handles name two Registry Windows",
			adjust: func(store *fakeResourceStore, _ *exactPaneExitInventory, _ *lifecycleDirtyEvent) {
				for i := range store.registry.Windows {
					if store.registry.Windows[i].Metadata.UID == "win-alpha-review" {
						store.registry.Windows[i].Status.RuntimeSessionID = "$1"
						store.registry.Windows[i].Status.RuntimeID = "@4"
					}
				}
			},
		},
		{
			name: "the handles name no Registry Window",
			adjust: func(_ *fakeResourceStore, _ *exactPaneExitInventory, event *lifecycleDirtyEvent) {
				event.runtimeSessionID = "$2"
			},
		},
		{
			name: "the observation holds no managed Pane",
			adjust: func(_ *fakeResourceStore, inventory *exactPaneExitInventory, _ *lifecycleDirtyEvent) {
				inventory.uids = map[string]bool{}
			},
		},
		{
			name: "the event names no exact server",
			adjust: func(_ *fakeResourceStore, _ *exactPaneExitInventory, event *lifecycleDirtyEvent) {
				event.target = tmuxTransport{}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, inventory, event := windowUnlinkedProjectionFixture(t)
			tc.adjust(store, inventory, &event)
			before := store.snapshot()
			result, err := reconcileLifecycle(context.Background(), event, inventory, store.store())
			if err != nil {
				t.Fatal(err)
			}
			if result.transactions != 0 || len(result.projected) != 0 || store.snapshot() != before {
				t.Fatalf("window-unlinked result = %+v, want no transaction and an unchanged Registry", result)
			}
			if agent, _ := store.registry.Agent("agt-alpha-codex"); agent.Status.Phase != coremetadata.PhaseRunning ||
				agent.Status.PaneRef != "pan-alpha-codex" {
				t.Fatalf("Agent = %+v, want it still Running on pan-alpha-codex", agent.Status)
			}
		})
	}
}
