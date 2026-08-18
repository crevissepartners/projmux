package mux

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const newSessionResultSeparator = "\\037"

// NewSessionResult is the atomic transport identity returned by one tmux
// new-session command. Stable ids, rather than names or indexes, are the only
// handles safe to hand to a caller that will claim the new runtime.
type NewSessionResult struct {
	Created   bool
	SessionID string
	WindowID  string
	PaneID    string
}

// NewSessionOptions describes a `new-session` lifecycle command.
type NewSessionOptions struct {
	Socket       string
	ConfigPath   string
	Attach       bool
	Detached     bool
	Session      string
	Cwd          string
	Env          map[string]string
	ReturnPaneID bool
	Command      []string
}

// NewWindowOptions describes a `new-window` command.
type NewWindowOptions struct {
	Detached bool
	Target   string
	Cwd      string
	Name     string
	Command  []string
}

// SplitWindowOptions describes a `split-window` command.
type SplitWindowOptions struct {
	Detached     bool
	ReturnPaneID bool
	Direction    string
	Target       string
	Cwd          string
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

const (
	SplitRight = "right"
	SplitDown  = "down"
)

// NewSession creates a tmux session and optionally returns the first pane id.
func NewSession(ctx context.Context, opts NewSessionOptions) (string, error) {
	return DefaultRunner().NewSession(ctx, opts)
}

// NewWindow creates a tmux window.
func NewWindow(ctx context.Context, opts NewWindowOptions) error {
	return DefaultRunner().NewWindow(ctx, opts)
}

// SplitWindow creates a tmux pane split and optionally returns the new pane id.
func SplitWindow(ctx context.Context, opts SplitWindowOptions) (string, error) {
	return DefaultRunner().SplitWindow(ctx, opts)
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

// NewSession creates a tmux session and optionally returns the first pane id.
func (r Runner) NewSession(ctx context.Context, opts NewSessionOptions) (string, error) {
	args := appendSocketArgs(nil, opts.Socket)
	if config := strings.TrimSpace(opts.ConfigPath); config != "" {
		args = append(args, "-f", config)
	}
	args = append(args, "new-session")
	if opts.Detached {
		args = append(args, "-d")
	}
	if opts.Attach {
		args = append(args, "-A")
	}
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

// NewSessionWithResult creates a session with one composite -P/-F projection,
// then proves the exact returned pane still belongs to the returned window and
// session. The verification target is the stable pane id, so a same-name
// session or a renumbered window cannot redirect it.
func (r Runner) NewSessionWithResult(ctx context.Context, opts NewSessionOptions) (NewSessionResult, error) {
	if opts.Attach {
		return NewSessionResult{}, fmt.Errorf("create tmux session with result: attach-or-create cannot prove Created attribution")
	}
	args := appendSocketArgs(nil, opts.Socket)
	if config := strings.TrimSpace(opts.ConfigPath); config != "" {
		args = append(args, "-f", config)
	}
	args = append(args, "new-session")
	if opts.Detached {
		args = append(args, "-d")
	}
	if sessionName := strings.TrimSpace(opts.Session); sessionName != "" {
		args = append(args, "-s", sessionName)
	}
	if cwd := strings.TrimSpace(opts.Cwd); cwd != "" {
		args = append(args, "-c", cwd)
	}
	args = appendEnvArgs(args, opts.Env)
	format := strings.Join([]string{TmuxFormat("session_id"), TmuxFormat("window_id"), TmuxFormat("pane_id")}, newSessionResultSeparator)
	args = append(args, "-P", "-F", format)
	args = append(args, opts.Command...)

	output, err := r.ReadTrimmed(ctx, args...)
	result, parseErr := parseNewSessionResult(output)
	if err != nil {
		return result, err
	}
	if parseErr != nil {
		return NewSessionResult{}, parseErr
	}

	ownerArgs := appendSocketArgs(nil, opts.Socket)
	ownerArgs = append(ownerArgs, "display-message", "-p", "-t", result.PaneID, "-F", format)
	owner, ownerErr := r.ReadTrimmed(ctx, ownerArgs...)
	if ownerErr != nil {
		return result, fmt.Errorf("verify created tmux session owner: %w", ownerErr)
	}
	verified, parseErr := parseNewSessionResult(owner)
	if parseErr != nil || verified.SessionID != result.SessionID || verified.WindowID != result.WindowID || verified.PaneID != result.PaneID {
		return result, fmt.Errorf("verify created tmux session owner: returned %s/%s/%s, observed %q",
			result.SessionID, result.WindowID, result.PaneID, strings.TrimSpace(owner))
	}
	result.Created = true
	return result, nil
}

func parseNewSessionResult(output string) (NewSessionResult, error) {
	rows := strings.Split(strings.TrimSpace(output), "\n")
	if len(rows) != 1 || strings.TrimSpace(rows[0]) == "" {
		return NewSessionResult{}, fmt.Errorf("parse created tmux session: expected one result row, got %q", strings.TrimSpace(output))
	}
	row := strings.ReplaceAll(strings.TrimSpace(rows[0]), newSessionResultSeparator, "\x1f")
	fields := strings.Split(row, "\x1f")
	if len(fields) != 3 || stableTmuxID(fields[0], '$') == "" || stableTmuxID(fields[1], '@') == "" || stableTmuxID(fields[2], '%') == "" {
		return NewSessionResult{}, fmt.Errorf("parse created tmux session: malformed result row %q", strings.TrimSpace(rows[0]))
	}
	return NewSessionResult{SessionID: fields[0], WindowID: fields[1], PaneID: fields[2]}, nil
}

func stableTmuxID(value string, prefix byte) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != prefix {
		return ""
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return ""
		}
	}
	return value
}

// NewWindow creates a tmux window.
func (r Runner) NewWindow(ctx context.Context, opts NewWindowOptions) error {
	args := []string{"new-window"}
	if opts.Detached {
		args = append(args, "-d")
	}
	args = appendPaneTargetArgs(args, opts.Target)
	if cwd := strings.TrimSpace(opts.Cwd); cwd != "" {
		args = append(args, "-c", cwd)
	}
	if name := strings.TrimSpace(opts.Name); name != "" {
		args = append(args, "-n", name)
	}
	args = append(args, opts.Command...)
	return r.Run(ctx, args...)
}

// SplitWindow creates a tmux pane split and optionally returns the new pane id.
func (r Runner) SplitWindow(ctx context.Context, opts SplitWindowOptions) (string, error) {
	args := []string{"split-window"}
	if opts.Detached {
		args = append(args, "-d")
	}
	if opts.ReturnPaneID {
		args = append(args, "-P", "-F", TmuxFormat("pane_id"))
	}
	if flag := splitDirectionFlag(opts.Direction); flag != "" {
		args = append(args, flag)
	}
	args = appendPaneTargetArgs(args, opts.Target)
	if cwd := strings.TrimSpace(opts.Cwd); cwd != "" {
		args = append(args, "-c", cwd)
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

func splitDirectionFlag(direction string) string {
	switch strings.TrimSpace(direction) {
	case SplitRight, "horizontal", "-h":
		return "-h"
	case SplitDown, "vertical", "-v":
		return "-v"
	default:
		return ""
	}
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
