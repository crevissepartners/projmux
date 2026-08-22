package mux

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// NewSessionResult is the atomic transport identity returned by one tmux
// new-session command. Stable ids, rather than names or indexes, are the only
// handles safe to hand to a caller that will claim the new runtime.
type NewSessionResult struct {
	Created   bool
	SessionID string
	WindowID  string
	PaneID    string
}

// EphemeralSessionOptions is intentionally incapable of expressing Project or
// attach-or-create lifecycle. The caller owns the explicit ephemeral marker;
// this transport can only create one detached ephemeral candidate.
type EphemeralSessionOptions struct {
	Session      string
	Cwd          string
	Env          map[string]string
	ReturnPaneID bool
	Command      []string
}

// SetHookOptions describes a `set-hook` command.
type SetHookOptions struct {
	Global  bool
	Append  bool
	Unset   bool
	Hook    string
	Command string
}

// SetOptionOptions describes a `set-option` command.
type SetOptionOptions struct {
	Global bool
	Append bool
	Unset  bool
	Target string
	Quiet  bool
	Option string
	Value  string
}

// ShowOptionOptions describes a `show-options` read.
type ShowOptionOptions struct {
	Global    bool
	Quiet     bool
	ValueOnly bool
	Target    string
	Option    string
}

// SetHook installs, appends, or unsets a tmux hook.
func SetHook(ctx context.Context, opts SetHookOptions) error {
	return DefaultRunner().SetHook(ctx, opts)
}

// SetOption writes or unsets a tmux option.
func SetOption(ctx context.Context, opts SetOptionOptions) error {
	return DefaultRunner().SetOption(ctx, opts)
}

// ShowOption reads a tmux option.
func ShowOption(ctx context.Context, opts ShowOptionOptions) (string, error) {
	return DefaultRunner().ShowOption(ctx, opts)
}

// NewEphemeralSession creates only a detached ephemeral candidate.
func (r Runner) NewEphemeralSession(ctx context.Context, opts EphemeralSessionOptions) (string, error) {
	args := []string{"new-session", "-d"}
	if sessionName := strings.TrimSpace(opts.Session); sessionName != "" {
		args = append(args, "-s", sessionName)
	}
	if cwd := strings.TrimSpace(opts.Cwd); cwd != "" {
		args = append(args, "-c", cwd)
	}
	args = appendEnvArgs(args, opts.Env)
	if opts.ReturnPaneID {
		args = append(args, "-P", "-F", TmuxFormat("pane_id"))
	}
	args = append(args, opts.Command...)
	if opts.ReturnPaneID {
		return r.ReadTrimmed(ctx, args...)
	}
	return "", r.Run(ctx, args...)
}

// SetHook installs, appends, or unsets a tmux hook.
func (r Runner) SetHook(ctx context.Context, opts SetHookOptions) error {
	args := []string{"set-hook"}
	if flags := hookFlags(opts); flags != "" {
		args = append(args, flags)
	}
	args = append(args, strings.TrimSpace(opts.Hook))
	if !opts.Unset {
		args = append(args, opts.Command)
	}
	return r.Run(ctx, args...)
}

// SetOption writes or unsets a tmux option.
func (r Runner) SetOption(ctx context.Context, opts SetOptionOptions) error {
	args := []string{"set-option"}
	if opts.Global {
		args = append(args, "-g")
	}
	if opts.Append {
		args = append(args, "-a")
	}
	if opts.Unset {
		args = append(args, "-u")
	}
	args = appendPaneTargetArgs(args, opts.Target)
	if opts.Quiet {
		args = append(args, "-q")
	}
	args = append(args, strings.TrimSpace(opts.Option))
	if !opts.Unset {
		args = append(args, opts.Value)
	}
	return r.Run(ctx, args...)
}

// ShowOption reads a tmux option.
func (r Runner) ShowOption(ctx context.Context, opts ShowOptionOptions) (string, error) {
	args := []string{"show-options"}
	if flags := showOptionFlags(opts); flags != "" {
		args = append(args, flags)
	}
	args = appendPaneTargetArgs(args, opts.Target)
	args = append(args, strings.TrimSpace(opts.Option))
	return r.ReadTrimmed(ctx, args...)
}

func appendEnvArgs(args []string, env map[string]string) []string {
	for _, key := range sortedEnvKeys(env) {
		args = append(args, "-e", key+"="+env[key])
	}
	return args
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func hookFlags(opts SetHookOptions) string {
	if opts.Global && opts.Append && !opts.Unset {
		return "-ag"
	}
	if opts.Global && opts.Unset && !opts.Append {
		return "-gu"
	}
	var builder strings.Builder
	builder.WriteByte('-')
	if opts.Global {
		builder.WriteByte('g')
	}
	if opts.Append {
		builder.WriteByte('a')
	}
	if opts.Unset {
		builder.WriteByte('u')
	}
	if builder.Len() == 1 {
		return ""
	}
	return builder.String()
}

func showOptionFlags(opts ShowOptionOptions) string {
	var builder strings.Builder
	builder.WriteByte('-')
	if opts.Global {
		builder.WriteByte('g')
	}
	if opts.Quiet {
		builder.WriteByte('q')
	}
	if opts.ValueOnly {
		builder.WriteByte('v')
	}
	if builder.Len() == 1 {
		return ""
	}
	return builder.String()
}
