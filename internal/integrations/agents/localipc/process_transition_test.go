package localipc

import (
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"testing"
)

func TestParentTransitionWitnessIsNotAbsence(t *testing.T) {
	witness := ParentTransitionWitness{Process: coremetadata.ProcessIdentity{PID: 42, OwnerUID: 1000, Start: "birth"}, OldParent: 7, NewParent: 8}
	err := fmt.Errorf("peer unavailable: %w", witness)
	got, ok := ProcessParentTransition(err)
	if !ok || got != witness || errors.Is(err, ErrProcessAbsent) {
		t.Fatalf("unexpected witness: %+v %v", got, err)
	}
	for _, err := range []error{nil, errors.New("peer unavailable"), ErrProcessAbsent, ParentTransitionWitness{}, ParentTransitionWitness{Process: witness.Process, OldParent: 7, NewParent: 7}} {
		if _, ok := ProcessParentTransition(err); ok {
			t.Fatalf("ordinary error became transition: %v", err)
		}
	}
}
