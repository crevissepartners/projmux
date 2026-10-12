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

// AgentCreateRefusal retains the existing create reason without parsing diagnostic text.
type AgentCreateRefusal struct {
	Reason, Message string
	Cause           error
}

func (e *AgentCreateRefusal) Error() string            { return e.Message }
func (e *AgentCreateRefusal) Unwrap() error            { return e.Cause }
func (e *AgentCreateRefusal) MetadataUsageError() bool { return true }
func createAgentUsage(reason, message string, causes ...error) error {
	var cause error
	if len(causes) > 0 {
		cause = causes[0]
	}
	return &AgentCreateRefusal{Reason: reason, Message: message, Cause: cause}
}
