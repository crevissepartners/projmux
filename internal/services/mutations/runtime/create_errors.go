package runtime

import coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"

type createUsageError struct{ message string }

func (e *createUsageError) Error() string            { return e.message }
func (e *createUsageError) MetadataUsageError() bool { return true }
func MapMetadataError(err error) error {
	if err != nil && coremetadata.IsUsageError(err) {
		return &createUsageError{err.Error()}
	}
	return err
}
