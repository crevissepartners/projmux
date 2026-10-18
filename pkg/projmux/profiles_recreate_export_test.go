package projmux

import "github.com/crevissepartners/projmux/internal/core/profile"

// ProfileRestoreRefusalForTest exercises facade projection for fault injection.
// It exists only in the test binary, not in the public SDK.
func ProfileRestoreRefusalForTest(writeErr, restoreErr error) error {
	return profileRefusal(&profile.RestoreError{Name: "candidate", BackupPath: ".candidate.restore-123", WriteError: writeErr, RestoreError: restoreErr})
}
