package runtime

import (
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func PaneWindowUID(registry coremetadata.Registry, pane coremetadata.Pane) (string, bool) {
	owner := pane.Metadata.OwnerRef
	if owner == nil {
		return "", false
	}
	if owner.Kind == coremetadata.KindWindow {
		_, ok := registry.Window(owner.UID)
		return owner.UID, ok
	}
	if owner.Kind != coremetadata.KindAgent {
		return "", false
	}
	agent, ok := registry.Agent(owner.UID)
	if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
		return "", false
	}
	_, ok = registry.Window(agent.Metadata.OwnerRef.UID)
	return agent.Metadata.OwnerRef.UID, ok
}

// LifecycleProjectionTargets is the pure decision half: which Panes in registry
// still need a lifecycle projection against the observation in live.
//
// A Pane qualifies when all of the following hold, and the order matters only
// for cost:
//
//   - the event does not narrow it away, by uid, by activation generation, or,
//     for window-unlinked, by the one exact Window that is gone;
//   - no live tmux pane mirrors its uid in this observation;
//   - the projection has work left, which is either evidence that has not been
//     recorded or an Agent still bound to the dead Pane.
//
// The last check is what makes a repeat pass free. Without it every offline Pane
// in the registry would re-enter a write transaction on every pane exit in every
// session, forever, and change nothing each time.
//
// liveWindows is the same observation's mirrored Window uids. Only a typed
// window-unlinked event reads it; every other caller passes nil.
func LifecycleProjectionTargets(registry coremetadata.Registry, live, liveWindows map[string]bool, event LifecycleProjectionEvent) []coremetadata.TerminationProjectionInput {
	windowUID := ""
	if event.TeardownKind == coremetadata.TeardownEventWindowUnlinked {
		// A typed window-unlinked event must never widen into a whole-host
		// absence projection: that would turn an unknown/foreign/empty
		// inventory into replacement authority. It projects only the Panes of
		// the one Registry Window its exact `$N/@N` handles name, and only once
		// the same observation proves that Window gone from the hook server.
		// A Window that is still live -- moved to another session, or unlinked
		// from one of several -- projects nothing, and its Panes are live anyway.
		if event.Target.Flag() == "" || event.Target.Value == "" || len(live) == 0 || liveWindows == nil {
			return nil
		}
		window, ok := LifecycleWindowUnlinkedWindow(registry, event)
		if !ok || liveWindows[window.Metadata.UID] {
			return nil
		}
		windowUID = window.Metadata.UID
	}
	narrowed := strings.TrimSpace(event.PaneUID)
	RuntimePane := strings.TrimSpace(event.RuntimePaneID)
	var out []coremetadata.TerminationProjectionInput
	for i := range registry.Panes {
		paneUID := registry.Panes[i].Metadata.UID
		if event.Processes.Declares(registry.Panes[i]) {
			continue
		}
		// A clean last-Pane receipt retains the complete subtree until its exact
		// window-unlinked half arrives. A generic absence projection must not
		// release the Agent binding or invalidate the Window anchor in between.
		if registry.Panes[i].Status.Teardown != nil {
			continue
		}
		if narrowed != "" && paneUID != narrowed {
			continue
		}
		if windowUID != "" {
			if owner, ok := PaneWindowUID(registry, registry.Panes[i]); !ok || owner != windowUID {
				continue
			}
		}
		if RuntimePane != "" && registry.Panes[i].Status.Activation.RuntimeID != RuntimePane {
			continue
		}
		if live[paneUID] {
			continue
		}
		if generation := strings.TrimSpace(event.Generation); generation != "" {
			// The generation guard is applied here as well as inside the
			// projection. Inside, it is correctness: the registry may have been
			// relaunched while the event queued. Here, it is cost -- a stale event
			// must not take the write lock to discover it had nothing to say.
			if pane, ok := registry.Pane(paneUID); !ok || pane.Status.Activation.Generation != generation {
				continue
			}
		}
		if !coremetadata.NeedsTerminationProjection(registry, paneUID) {
			continue
		}
		out = append(out, coremetadata.TerminationProjectionInput{
			PaneUID:    paneUID,
			Generation: event.Generation,
		})
	}
	return out
}

// ProjectTerminations applies the lifecycle projection to every named Pane and
// returns what it did.
//
// It is the one transition body both trigger paths share: the reconciler's
// observation step, which rides inside somebody else's mutation transaction, and
// the standalone one-shot below. Sharing it is what keeps the two from
// disagreeing about a classification.
//
// A projection that fails is skipped rather than propagated. This body runs as
// maintenance inside another operation on the reconciler path, and one Pane that
// cannot be projected must not fail the operation that happened to trigger it.
func ProjectTerminations(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	inputs []coremetadata.TerminationProjectionInput,
) []coremetadata.TerminationProjection {
	out := make([]coremetadata.TerminationProjection, 0, len(inputs))
	for _, input := range inputs {
		projection, err := mutator.ProjectTermination(working, input)
		if err != nil {
			continue
		}
		out = append(out, projection)
	}
	return out
}
