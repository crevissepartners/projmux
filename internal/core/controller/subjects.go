package controller

import (
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// Handle is everything the policy and the guards need to know about one
// observed tmux object, indexed by the exact id tmux reported for it.
type Handle struct {
	// ID is the stable tmux id -- $N, @N, or %N -- and the only safe write
	// target. Every other spelling an operator or a planner may use resolves to
	// this one before a guard is built.
	ID string `json:"id"`
	// Kind is the runtime object kind.
	Kind resourcegraph.ObjectKind `json:"kind"`
	// Class is the attribution the resolved graph gave this object.
	Class resourcegraph.Class `json:"class"`
	// UID is the mirrored projmux uid this object carried at observation time,
	// empty when it carried none. It is the current-binding guard value.
	UID string `json:"uid,omitempty"`
	// ContainerID is the stable id of the enclosing object, and the owner guard
	// value. Empty for a session, which has nothing above it.
	ContainerID string `json:"containerID,omitempty"`
	// ManagedEnclosure reports whether an enclosing object is a bound managed
	// resource.
	ManagedEnclosure bool `json:"managedEnclosure,omitempty"`
	// Reason is the graph's one-clause explanation of Class.
	Reason string `json:"reason,omitempty"`
}

// Subject projects the handle onto the policy input under grant.
func (h Handle) Subject(grant Grant) Subject { return Subject{Class: h.Class, Grant: grant} }

// GuardFields names the tmux format fields the guards are read through.
//
// The spellings themselves belong to the option catalog in the adapter layer.
// Taking them as input is what keeps this package free of any tmux dependency:
// a pure kernel that hardcoded `@projmux_pane_uid` would be one import away
// from being able to run a tmux command.
type GuardFields struct {
	// SessionUID carries the mirrored Project uid of a session.
	SessionUID string
	// WindowUID carries the mirrored Window uid.
	WindowUID string
	// PaneUID carries the mirrored Pane uid.
	PaneUID string
	// SessionID is the containment field of a window.
	SessionID string
	// WindowID is the containment field of a pane.
	WindowID string
}

// Guards returns the exact evidence a write on this handle must re-prove
// immediately before the first live mutation.
//
// Two facts are guarded and they fail differently. The uid guard catches a
// recycled or re-mirrored handle -- tmux reuses %7 the moment its pane closes,
// and a plan built against the old %7 would otherwise land on the new one. The
// owner guard catches a move: `join-pane` and `move-window` keep the id and
// change the containment, so a plan authorized by an enclosure the object has
// since left must not run. An object with no mirrored uid is guarded on exactly
// that: the empty value is the observation, and a uid appearing between plan and
// execute means somebody else claimed the object first.
func (h Handle) Guards(fields GuardFields) []Guard {
	var guards []Guard
	switch h.Kind {
	case resourcegraph.ObjectSession:
		guards = append(guards, Guard{Field: fields.SessionUID, Expect: h.UID})
	case resourcegraph.ObjectWindow:
		guards = append(guards,
			Guard{Field: fields.WindowUID, Expect: h.UID},
			Guard{Field: fields.SessionID, Expect: h.ContainerID})
	case resourcegraph.ObjectPane:
		guards = append(guards,
			Guard{Field: fields.PaneUID, Expect: h.UID},
			Guard{Field: fields.WindowID, Expect: h.ContainerID})
	}
	return slices.DeleteFunc(guards, func(g Guard) bool { return strings.TrimSpace(g.Field) == "" })
}

// Handles is the lookup from any spelling of a tmux target to the one observed
// handle behind it.
type Handles struct {
	byID     map[string]Handle
	byTarget map[string]string
}

// IndexHandles builds the lookup from one resolved graph.
//
// Both the id and the operator-typed target are indexed because the two halves
// of the system spell targets differently: the observation reports `@6`, while a
// planner that walked a session reports `projmux:2`. Resolving both to the same
// handle is what lets one policy gate and one guard set cover writes from either
// producer without a second matcher.
func IndexHandles(graph resourcegraph.Graph) Handles {
	managedID := map[string]bool{}
	for _, node := range graph.Runtime {
		if node.Class == resourcegraph.ClassManaged {
			managedID[node.Ref.ID] = true
		}
	}
	// A pane's enclosure is managed when its window is, or when the session
	// holding that window is. Containment is walked through observed ids only;
	// a name join here would reintroduce the heuristic the graph refuses.
	containerOf := map[string]string{}
	for _, node := range graph.Runtime {
		containerOf[node.Ref.ID] = node.ContainerID
	}
	handles := Handles{byID: map[string]Handle{}, byTarget: map[string]string{}}
	for _, node := range graph.Runtime {
		enclosure := false
		for id := containerOf[node.Ref.ID]; id != ""; id = containerOf[id] {
			if managedID[id] {
				enclosure = true
				break
			}
		}
		handle := Handle{
			ID: node.Ref.ID, Kind: node.Ref.Kind, Class: node.Class, UID: node.UID,
			ContainerID: node.ContainerID, ManagedEnclosure: enclosure, Reason: node.Reason,
		}
		handles.byID[handle.ID] = handle
		handles.byTarget[handle.ID] = handle.ID
		if target := strings.TrimSpace(node.Ref.Target); target != "" {
			// An id always wins over a name-shaped target. A session named `%7`
			// is legal in tmux and must not shadow the pane of the same
			// spelling.
			if _, taken := handles.byTarget[target]; !taken {
				handles.byTarget[target] = handle.ID
			}
		}
	}
	for id := range handles.byID {
		handles.byTarget[id] = id
	}
	return handles
}

// Lookup resolves any spelling of a tmux target to its observed handle.
func (h Handles) Lookup(target string) (Handle, bool) {
	id, known := h.byTarget[strings.TrimSpace(target)]
	if !known {
		return Handle{}, false
	}
	handle, ok := h.byID[id]
	return handle, ok
}

// IDs returns every observed handle id in ascending tmux order.
func (h Handles) IDs() []string {
	out := make([]string, 0, len(h.byID))
	for id := range h.byID {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
