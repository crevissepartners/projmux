package codexinstalled

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestOwnedProcessObservationErrorRetainsCauseWithoutContent(t *testing.T) {
	for _, cause := range []error{&os.PathError{Op: "open", Path: "/proc/321/stat", Err: syscall.EACCES}, errors.New("PRIVATE-PROCESS-CONTENT")} {
		err := &OwnedProcessObservationError{Operation: "read-birth", PID: 321, Leaf: "stat", cause: cause}
		if !errors.Is(err, cause) || err.Unwrap() != cause || strings.Contains(err.Error(), "/proc/") || strings.Contains(err.Error(), "PRIVATE-") {
			t.Fatal("observation wrapper changed or exposed the underlying failure")
		}
		var retained *OwnedProcessObservationError
		if !errors.As(err, &retained) || retained.PID != 321 || retained.Leaf != "stat" || retained.Operation != "read-birth" {
			t.Fatal("observation wrapper lost its exact failure boundary")
		}
	}
}
