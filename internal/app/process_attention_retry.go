package app

import (
	"errors"
	"os"
	"time"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

// Space retries from completion, including quick failures, without polling an
// idle successful projection. Store lock budgets remain unchanged.
const processAttentionRetryInterval = time.Second

type processAttentionWriteError struct{ error }

func (e processAttentionWriteError) Unwrap() error { return e.error }

func processAttentionRetryable(err error) bool {
	var write processAttentionWriteError
	var notification processAttentionNotifyError
	return errors.As(err, &write) || errors.As(err, &notification)
}

type processAttentionNotifyError struct{ error }

func (e processAttentionNotifyError) Unwrap() error { return e.error }

// Journal classifications contain no paths, provider content, or error text.
func processAttentionErrorKind(err error) string {
	var notification processAttentionNotifyError
	var ioError *os.PathError
	var linkError *os.LinkError
	switch {
	case errors.As(err, &notification):
		return "notify-write"
	case errors.Is(err, localstate.ErrLockTimeout):
		return "lock-timeout"
	case errors.As(err, &ioError) || errors.As(err, &linkError):
		return "io"
	default:
		return "projection"
	}
}
