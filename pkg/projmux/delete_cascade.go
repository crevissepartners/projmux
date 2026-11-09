package projmux

import (
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// DeleteCascade returns the resources one delete of (kind, uid) removes besides
// the target itself, as the same items in the same order the delete plan prints
// as Descendants. The collecting rule has one definition,
// mutations.CascadeOf; this method only copies its items into core.DeleteResource.
//
//   - a Project cascades to each of its Windows, each followed by that Window's
//     own cascade.
//   - a Window cascades to each Agent followed by that Agent's Panes, then the
//     Panes the Window owns directly.
//   - an Agent cascades to its Panes.
//   - a Pane is a leaf.
//
// A virtual Window is removed with its last Pane or Agent, so deleting that
// Agent or Pane also lists the parent Window and, after it, the Agents still in
// that Window (the deleted Agent itself is not repeated). The cascade is
// therefore not only the resources below the target.
//
// An item carries no Pane runtime. A caller that needs it reads the Pane row
// from Snapshot() of the same handle. A handle that was not prepared, a uid the
// rows do not hold and a kind other than Project, Window, Agent or Pane return
// nil, as does a target with nothing to cascade; there is no error. The
// handle's rows are read and not changed, and the caller owns the returned
// slice.
func (p *PreparedRegistrySnapshot) DeleteCascade(kind core.Kind, uid string) []core.DeleteResource {
	if !p.valid() {
		return nil
	}
	cascade := mutations.CascadeOf(p.registry, metadata.Kind(kind), uid)
	if len(cascade) == 0 {
		return nil
	}
	out := make([]core.DeleteResource, 0, len(cascade))
	for _, item := range cascade {
		out = append(out, core.DeleteResource{Kind: core.Kind(item.Kind), UID: item.UID, Name: item.Name})
	}
	return out
}
