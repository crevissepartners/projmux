package app

import (
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

type processCreateRuntime string

const (
	processCreateRuntimeNone    processCreateRuntime = "none"
	processCreateRuntimeOffline processCreateRuntime = "offline"
	processCreateRuntimeUnknown processCreateRuntime = "unknown"
)

type processCreateRemaining struct{ Agent, Pane selector.Ref }

// processCreateError describes cleanup evidence without parsing the CLI's
// recovery text. None means no created resources remain; offline requires a
// durably recorded supervisor Wait. Unknown never implies the child exited.
// The original error remains intact for text and exit-code classification.
type processCreateError struct {
	Token     string
	Runtime   processCreateRuntime
	Remaining processCreateRemaining
	cause     error
}

func (e *processCreateError) Error() string { return e.cause.Error() }
func (e *processCreateError) Unwrap() error { return e.cause }

func newProcessCreateError(cause error, runtime processCreateRuntime, binding processCreateRemaining) *processCreateError {
	token := ""
	// Refusal tokens already occupy the leading colon-delimited field. Do
	// not invent tokens for transport, filesystem, or provider errors.
	if prefix, _, found := strings.Cut(cause.Error(), ":"); found && strings.HasPrefix(prefix, "process-") && !strings.ContainsAny(prefix, " \n\t") {
		token = prefix
	}
	return &processCreateError{Token: token, Runtime: runtime, Remaining: binding, cause: cause}
}

func processCreateRemainingRefs(result processAgentCreateResult) processCreateRemaining {
	return processCreateRemaining{
		Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: result.Binding.Agent},
		Pane:  selector.Ref{Kind: coremetadata.KindPane, UID: result.Binding.Pane},
	}
}
