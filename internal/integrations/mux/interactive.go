package mux

import (
	"context"
	"strconv"
	"strings"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type PopupOptions = inttmux.PopupOptions
type PopupCloseBehavior = inttmux.PopupCloseBehavior

const (
	PopupCloseOnExit = inttmux.PopupCloseOnExit
	PopupKeepOpen    = inttmux.PopupKeepOpen
)

// ClosePopupOptions describes a scoped `display-popup -C` close command.
type ClosePopupOptions struct {
	Socket string
	Client string
	Target string
}

// CapturePaneOptions describes a `capture-pane -p` read.
type CapturePaneOptions struct {
	Socket    string
	Target    string
	StartLine int
	JoinLines bool
}

// SwitchClientOptions describes a `switch-client` command.
type SwitchClientOptions struct {
	Socket string
	Client string
	Target string
}

// SelectPaneOptions describes a `select-pane` command.
type SelectPaneOptions struct {
	Socket   string
	Target   string
	Title    string
	SetTitle bool
}

// SelectWindowOptions describes a `select-window` command.
type SelectWindowOptions struct {
	Socket string
	Target string
}

// DisplayPopup opens a tmux popup and executes the provided shell command.
func DisplayPopup(ctx context.Context, command string, options PopupOptions) error {
	return DefaultRunner().DisplayPopup(ctx, command, options)
}

// ClosePopup closes a scoped tmux popup.
func ClosePopup(ctx context.Context, opts ClosePopupOptions) error {
	return DefaultRunner().ClosePopup(ctx, opts)
}

// CapturePane reads visible text from a tmux pane.
func CapturePane(ctx context.Context, opts CapturePaneOptions) (string, error) {
	return DefaultRunner().CapturePane(ctx, opts)
}

// SwitchClient switches a tmux client to the target.
func SwitchClient(ctx context.Context, opts SwitchClientOptions) error {
	return DefaultRunner().SwitchClient(ctx, opts)
}

// SelectPane selects or retitles a tmux pane.
func SelectPane(ctx context.Context, opts SelectPaneOptions) error {
	return DefaultRunner().SelectPane(ctx, opts)
}

// SelectWindow selects a tmux window.
func SelectWindow(ctx context.Context, opts SelectWindowOptions) error {
	return DefaultRunner().SelectWindow(ctx, opts)
}

// DisplayPopup opens a tmux popup and executes the provided shell command.
func (r Runner) DisplayPopup(ctx context.Context, command string, options PopupOptions) error {
	args, err := inttmux.BuildDisplayPopupArgs(command, options)
	if err != nil {
		return err
	}
	return r.Run(ctx, args...)
}

// ClosePopup closes a scoped tmux popup.
func (r Runner) ClosePopup(ctx context.Context, opts ClosePopupOptions) error {
	args := appendSocketArgs(nil, opts.Socket)
	args = append(args, "display-popup")
	if client := strings.TrimSpace(opts.Client); client != "" {
		args = append(args, "-c", client)
	}
	if target := strings.TrimSpace(opts.Target); target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, "-C")
	return r.Run(ctx, args...)
}

// CapturePane reads visible text from a tmux pane.
func (r Runner) CapturePane(ctx context.Context, opts CapturePaneOptions) (string, error) {
	args := appendSocketArgs(nil, opts.Socket)
	args = append(args, "capture-pane", "-p")
	if opts.JoinLines {
		args = append(args, "-J", "-S", strconv.Itoa(opts.StartLine))
		args = appendPaneTargetArgs(args, opts.Target)
	} else {
		args = appendPaneTargetArgs(args, opts.Target)
		args = append(args, "-S", strconv.Itoa(opts.StartLine))
	}
	out, err := r.Read(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// SwitchClient switches a tmux client to the target.
func (r Runner) SwitchClient(ctx context.Context, opts SwitchClientOptions) error {
	args := appendSocketArgs(nil, opts.Socket)
	args = append(args, "switch-client")
	if client := strings.TrimSpace(opts.Client); client != "" {
		args = append(args, "-c", client)
	}
	args = append(args, "-t", strings.TrimSpace(opts.Target))
	return r.Run(ctx, args...)
}

// SelectPane selects or retitles a tmux pane.
func (r Runner) SelectPane(ctx context.Context, opts SelectPaneOptions) error {
	args := appendSocketArgs(nil, opts.Socket)
	args = append(args, "select-pane")
	if opts.SetTitle {
		args = append(args, "-T", opts.Title)
	}
	args = appendPaneTargetArgs(args, opts.Target)
	return r.Run(ctx, args...)
}

// SelectWindow selects a tmux window.
func (r Runner) SelectWindow(ctx context.Context, opts SelectWindowOptions) error {
	args := appendSocketArgs(nil, opts.Socket)
	args = append(args, "select-window", "-t", strings.TrimSpace(opts.Target))
	return r.Run(ctx, args...)
}

func appendSocketArgs(args []string, socket string) []string {
	if resolved := strings.TrimSpace(socket); resolved != "" {
		args = append(args, "-S", resolved)
	}
	return args
}
