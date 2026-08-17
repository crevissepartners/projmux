// Package mux provides the minimal command-runner boundary for mux backend
// subprocess calls. Phase 1 keeps tmux as the only production backend and
// intentionally passes tmux arguments through unchanged.
package mux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
		backend = execRunner{}
	}
	return Runner{backend: backend}
}

// DefaultRunner returns the production tmux-backed mux runner.
func DefaultRunner() Runner {
	return NewRunner(execRunner{})
}

// Run executes tmux with args and discards output.
func Run(ctx context.Context, args ...string) error {
	return DefaultRunner().Run(ctx, args...)
}

// SetPaneOption writes a pane-scoped tmux option.
func SetPaneOption(ctx context.Context, paneTarget, option, value string) error {
	return DefaultRunner().SetPaneOption(ctx, paneTarget, option, value)
}

// UnsetPaneOption removes a pane-scoped tmux option.
func UnsetPaneOption(ctx context.Context, paneTarget, option string) error {
	return DefaultRunner().UnsetPaneOption(ctx, paneTarget, option)
}

// ShowPaneOption reads a pane-scoped tmux option through display-message.
func ShowPaneOption(ctx context.Context, paneTarget, option string) (string, error) {
	return DefaultRunner().ShowPaneOption(ctx, paneTarget, option)
}

// DisplayMessage executes `tmux display-message -p` and returns raw output.
func DisplayMessage(ctx context.Context, opts DisplayMessageOptions) ([]byte, error) {
	return DefaultRunner().DisplayMessage(ctx, opts)
}

// DisplayMessageTrimmed executes `tmux display-message -p` and trims output.
func DisplayMessageTrimmed(ctx context.Context, opts DisplayMessageOptions) (string, error) {
	return DefaultRunner().DisplayMessageTrimmed(ctx, opts)
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

// SetPaneOption writes a pane-scoped tmux option.
func (r Runner) SetPaneOption(ctx context.Context, paneTarget, option, value string) error {
	args := []string{"set-option", "-p"}
	args = appendPaneTargetArgs(args, paneTarget)
	args = append(args, strings.TrimSpace(option), value)
	return r.Run(ctx, args...)
}

// UnsetPaneOption removes a pane-scoped tmux option.
func (r Runner) UnsetPaneOption(ctx context.Context, paneTarget, option string) error {
	args := []string{"set-option", "-p", "-u"}
	args = appendPaneTargetArgs(args, paneTarget)
	args = append(args, strings.TrimSpace(option))
	return r.Run(ctx, args...)
}

// ShowPaneOption reads a pane-scoped tmux option through display-message.
func (r Runner) ShowPaneOption(ctx context.Context, paneTarget, option string) (string, error) {
	return r.DisplayMessageTrimmed(ctx, DisplayMessageOptions{
		Target: paneTarget,
		Format: PaneOptionFormat(option),
	})
}

// DisplayMessageOptions describes a `display-message -p` read.
type DisplayMessageOptions struct {
	Target string
	Format string
}

// DisplayMessage executes `tmux display-message -p` and returns raw output.
func (r Runner) DisplayMessage(ctx context.Context, opts DisplayMessageOptions) ([]byte, error) {
	args := []string{"display-message", "-p"}
	args = appendPaneTargetArgs(args, opts.Target)
	if strings.TrimSpace(opts.Format) != "" {
		args = append(args, opts.Format)
	}
	return r.Read(ctx, args...)
}

// DisplayMessageTrimmed executes `tmux display-message -p` and trims output.
func (r Runner) DisplayMessageTrimmed(ctx context.Context, opts DisplayMessageOptions) (string, error) {
	out, err := r.DisplayMessage(ctx, opts)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
		return execRunner{}
	}
	return r.backend
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "tmux" && len(args) > 0 && (args[0] == "attach-session" || args[0] == "switch-client") {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return nil, nil
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
		}
		return output, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output, nil
}

// PaneOptionFormat renders a tmux format for a pane option.
func PaneOptionFormat(option string) string {
	return TmuxFormat(strings.TrimSpace(option))
}

// TmuxFormat renders a tmux braced format token.
func TmuxFormat(name string) string {
	return "#{" + strings.TrimSpace(name) + "}"
}

// JoinFormats joins pre-rendered tmux formats with a caller-owned delimiter.
func JoinFormats(delimiter string, formats ...string) string {
	return strings.Join(formats, delimiter)
}

func appendPaneTargetArgs(args []string, paneTarget string) []string {
	if target := strings.TrimSpace(paneTarget); target != "" {
		args = append(args, "-t", target)
	}
	return args
}
