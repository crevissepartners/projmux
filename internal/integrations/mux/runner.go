// Package mux provides the minimal command-runner boundary for mux backend
// subprocess calls. Phase 1 keeps tmux as the only production backend and
// intentionally passes tmux arguments through unchanged.
package mux

import (
	"context"
	"strings"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// Backend is the low-level command runner contract used by the mux boundary.
type Backend interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Runner invokes mux backend commands. The default production backend is tmux.
type Runner struct {
	backend Backend
}

// NewRunner builds a mux runner over backend. A nil backend uses the tmux
// backend so production callers can stay concise.
func NewRunner(backend Backend) Runner {
	if backend == nil {
		backend = inttmux.ExecRunner{}
	}
	return Runner{backend: backend}
}

// DefaultRunner returns the production tmux-backed mux runner.
func DefaultRunner() Runner {
	return NewRunner(inttmux.ExecRunner{})
}

// Run executes tmux with args and discards output.
func Run(ctx context.Context, args ...string) error {
	return DefaultRunner().Run(ctx, args...)
}

// Read executes tmux with args and returns the raw backend output.
func Read(ctx context.Context, args ...string) ([]byte, error) {
	return DefaultRunner().Read(ctx, args...)
}

// ReadTrimmed executes tmux with args and trims surrounding whitespace.
func ReadTrimmed(ctx context.Context, args ...string) (string, error) {
	return DefaultRunner().ReadTrimmed(ctx, args...)
}

// Run executes tmux with args and discards output.
func (r Runner) Run(ctx context.Context, args ...string) error {
	_, err := r.Read(ctx, args...)
	return err
}

// Read executes tmux with args and returns the raw backend output.
func (r Runner) Read(ctx context.Context, args ...string) ([]byte, error) {
	return r.runner().Run(ctx, "tmux", args...)
}

// ReadTrimmed executes tmux with args and trims surrounding whitespace.
func (r Runner) ReadTrimmed(ctx context.Context, args ...string) (string, error) {
	out, err := r.Read(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r Runner) runner() Backend {
	if r.backend == nil {
		return inttmux.ExecRunner{}
	}
	return r.backend
}
