package runtime

import (
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// runtimeObjectKind names one class of tmux object an operation can create.
type RuntimeObjectKind string

const (
	RuntimeSession RuntimeObjectKind = "session"
	RuntimeWindow  RuntimeObjectKind = "window"
	RuntimePane    RuntimeObjectKind = "pane"
)

// runtimeObject is one ledger entry: a tmux object this operation created,
// pinned by its stable tmux id and by the Projmux uid mirrored onto it.
type RuntimeObject struct {
	Kind RuntimeObjectKind
	// ID is the stable tmux handle ($N, @N, %N). Indexes are deliberately not
	// used: they shift when a sibling is created or destroyed.
	ID string
	// UID is the Projmux uid mirrored onto ID at creation time.
	UID string
}

// ownershipOption is the mirrored option that proves this operation owns a
// runtime object.
func (o RuntimeObject) OwnershipOption() string {
	switch o.Kind {
	case RuntimeSession:
		return tmuxopts.ProjectUIDSession
	case RuntimeWindow:
		return tmuxopts.WindowUID
	default:
		return tmuxopts.PaneUID
	}
}
