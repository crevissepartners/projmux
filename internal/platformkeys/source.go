package platformkeys

import (
	"context"
	"errors"
)

// ErrPermissionRequired indicates that the Darwin event tap is waiting for
// one-time macOS Accessibility approval.
var ErrPermissionRequired = errors.New("macOS Accessibility permission is required for native projmux keybindings")

// Source captures supported physical key chords and emits their canonical tmux
// chord. Implementations must not emit while disabled.
type Source interface {
	Replace([]Binding) error
	SetEnabled(bool)
	Ready() <-chan struct{}
	Events() <-chan string
	Run(context.Context) error
}
