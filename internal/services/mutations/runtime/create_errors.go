package runtime

import coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"

type createUsageError struct {
	message string
	cause   error
}

func (e *createUsageError) Unwrap() error            { return e.cause }
func (e *createUsageError) Error() string            { return e.message }
func (e *createUsageError) MetadataUsageError() bool { return true }
func MapMetadataError(err error) error {
	if err != nil && coremetadata.IsUsageError(err) {
		return &createUsageError{message: err.Error(), cause: err}
	}
	return err
}
