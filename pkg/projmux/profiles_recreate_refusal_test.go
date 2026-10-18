package projmux

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/profile"
)

func TestProfileRestoreRefusalPreservesBothCauses(t *testing.T) {
	writeErr := &profile.Error{Reason: profile.ReasonInstructionsNotFound, Name: "candidate", Detail: "missing"}
	restoreErr := &os.LinkError{Op: "rename", Old: ".candidate.restore-123", New: "candidate.toml", Err: fs.ErrPermission}
	err := profileRefusal(&profile.RestoreError{Name: "candidate", BackupPath: ".candidate.restore-123", WriteError: writeErr, RestoreError: restoreErr})
	var refusal *Refusal
	var link *os.LinkError
	if !errors.As(err, &refusal) || refusal.Code != CodeProfileRestoreFailed || refusal.Name != "candidate" || !strings.Contains(err.Error(), ".candidate.restore-123") {
		t.Fatalf("refusal %v", err)
	}
	if !errors.As(err, &link) || link != restoreErr || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("restore cause %v", err)
	}
	var original *Refusal
	if !errors.As(refusal.Unwrap(), &original) || original.Code != CodeProfileInstructionsNotFound {
		t.Fatalf("write refusal lost %v", err)
	}
}
