package localipc

import (
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// ErrProcessAbsent means the kernel proved that the PID has no live process.
// Other errors, including permission and identity parsing errors, prove nothing.
var ErrProcessAbsent = errors.New("process unavailable")

// ParentTransitionWitness describes a rejected Linux snapshot. It grants no
// identity, absence, or authority; consumers must obtain an independent snapshot.
type ParentTransitionWitness struct {
	Process              coremetadata.ProcessIdentity
	OldParent, NewParent int
}

func (ParentTransitionWitness) Error() string { return "process identity unavailable" }

func ProcessParentTransition(err error) (ParentTransitionWitness, bool) {
	var witness ParentTransitionWitness
	ok := errors.As(err, &witness)
	return witness, ok && witness.Process.Valid() && witness.OldParent > 0 && witness.NewParent > 0 && witness.OldParent != witness.NewParent
}
