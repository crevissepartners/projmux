package mux

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

var (
	ErrPopupCommandRequired      = errors.New("tmux popup command is required")
	ErrPopupCloseBehaviorInvalid = errors.New("tmux popup close behavior is invalid")
)

type PopupCloseBehavior string

const (
	PopupCloseOnExit PopupCloseBehavior = "close-on-exit"
	PopupKeepOpen    PopupCloseBehavior = "keep-open"
)

type PopupOptions struct {
	Client        string
	Target        string
	Cwd           string
	Env           map[string]string
	NoBorder      bool
	BodyStyle     string
	X             string
	Y             string
	Width         string
	Height        string
	Title         string
	CloseBehavior PopupCloseBehavior
}

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
	args, err := BuildDisplayPopupArgs(command, options)
	if err != nil {
		return err
	}
	return r.Run(ctx, args...)
}

// BuildDisplayPopupArgs maps structured popup options to tmux display-popup args.
func BuildDisplayPopupArgs(command string, options PopupOptions) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, ErrPopupCommandRequired
	}

	resolved, err := resolvePopupOptions(options)
	if err != nil {
		return nil, err
	}

	args := []string{"display-popup"}
	if resolved.Client != "" {
		args = append(args, "-c", resolved.Client)
	}
	if resolved.Target != "" {
		args = append(args, "-t", resolved.Target)
	}
	if resolved.CloseBehavior == PopupCloseOnExit {
		args = append(args, "-E")
	}
	if resolved.NoBorder {
		args = append(args, "-B")
	}
	if resolved.Cwd != "" {
		args = append(args, "-d", resolved.Cwd)
	}
	args = appendEnvArgs(args, resolved.Env)
	if resolved.X != "" {
		args = append(args, "-x", resolved.X)
	}
	if resolved.Y != "" {
		args = append(args, "-y", resolved.Y)
	}
	if resolved.Width != "" {
		args = append(args, "-w", resolved.Width)
	}
	if resolved.Height != "" {
		args = append(args, "-h", resolved.Height)
	}
	if resolved.Title != "" {
		args = append(args, "-T", resolved.Title)
	}
	if resolved.BodyStyle != "" {
		args = append(args, "-s", resolved.BodyStyle)
	}
	args = append(args, command)
	return args, nil
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

func resolvePopupOptions(options PopupOptions) (PopupOptions, error) {
	resolved := PopupOptions{
		Client:        strings.TrimSpace(options.Client),
		Target:        strings.TrimSpace(options.Target),
		Cwd:           strings.TrimSpace(options.Cwd),
		Env:           cleanPopupEnv(options.Env),
		NoBorder:      options.NoBorder,
		BodyStyle:     strings.TrimSpace(options.BodyStyle),
		X:             strings.TrimSpace(options.X),
		Y:             strings.TrimSpace(options.Y),
		Width:         strings.TrimSpace(options.Width),
		Height:        strings.TrimSpace(options.Height),
		Title:         strings.TrimSpace(options.Title),
		CloseBehavior: options.CloseBehavior,
	}

	if resolved.Width == "" {
		resolved.Width = "80%"
	}
	if resolved.Height == "" {
		resolved.Height = "80%"
	}
	if resolved.CloseBehavior == "" {
		resolved.CloseBehavior = PopupCloseOnExit
	}

	switch resolved.CloseBehavior {
	case PopupCloseOnExit, PopupKeepOpen:
		return resolved, nil
	default:
		return PopupOptions{}, ErrPopupCloseBehaviorInvalid
	}
}

func cleanPopupEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(env))
	for key, value := range env {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		cleaned[key] = value
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}
