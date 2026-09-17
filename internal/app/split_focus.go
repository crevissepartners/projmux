package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// paneCreatedUnfocusedMessage leads the line a pressing client sees when a UI
// split committed but projmux could not make the new Pane active. The Pane
// stays; nothing is rolled back.
const paneCreatedUnfocusedMessage = "Created Pane, but projmux could not focus it: "

// focusCreatedSplitPane makes a Pane a UI split just committed the active Pane
// of its Window, for exactly the client that pressed the key.
//
// It is the one focus step behind every UI split: the saved-default key, the
// provider and shell direct keys, the AI split picker, the resume picker, and
// the pane menu. Public `create pane|agent` never reaches it; the create route
// itself stays detached.
//
// Nothing moves unless the pressing client is still attached and still shows
// the Window that holds the committed `%N`: both are compared as exact tmux
// handles (`@N`), never as names. A client that is gone, unnamed, or on another
// Window is left alone, and no client is ever dragged to the Window -- there is
// no switch-client or select-window here. A returned error means the focus
// attempt itself failed; the split is kept either way.
func focusCreatedSplitPane(ctx context.Context, runner tmuxRunner, client string, created createdPaneRuntime) error {
	paneID := exactTmuxHandle(created.paneID, "%")
	client = strings.TrimSpace(client)
	if paneID == "" || client == "" {
		return nil
	}
	if runner == nil {
		return errors.New("tmux runner is not configured")
	}
	focus := &focusCommand{runner: runner}
	clients, err := focus.listClientWindows(ctx)
	if err != nil {
		return err
	}
	clientWindow := ""
	attached := false
	for _, candidate := range clients {
		if candidate.Name == client {
			attached = true
			clientWindow = exactTmuxHandle(candidate.WindowID, "@")
			break
		}
	}
	if !attached || clientWindow == "" {
		return nil
	}
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", "#{window_id}")
	if err != nil {
		return fmt.Errorf("read the Window of Pane %s: %w", paneID, err)
	}
	paneWindow := exactTmuxHandle(strings.TrimSpace(string(out)), "@")
	if paneWindow == "" {
		return fmt.Errorf("pane %s has no exact runtime Window (%q)", paneID, strings.TrimSpace(string(out)))
	}
	if paneWindow != clientWindow {
		return nil
	}
	return focus.selectPane(ctx, "", paneID)
}

// splitFocusRunner adapts the aiCommand run/read seams to the tmuxRunner the
// focus step takes: select-pane is a write, every other command is a read.
type splitFocusRunner struct {
	runCommand  func(ctx context.Context, name string, args ...string) error
	readCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (r splitFocusRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "select-pane" {
		if r.runCommand == nil {
			return nil, errors.New("ai command runner is not configured")
		}
		return nil, r.runCommand(ctx, name, args...)
	}
	if r.readCommand == nil {
		return nil, errors.New("ai command reader is not configured")
	}
	return r.readCommand(ctx, name, args...)
}

// splitFocusFailureLine is the one client line for a kept split whose focus
// step failed. A split start notice rides on the same line so the client still
// sees exactly one message.
func splitFocusFailureLine(focusErr error, notice string) string {
	line := paneCreatedUnfocusedMessage + strings.TrimSpace(focusErr.Error())
	if notice = strings.TrimSpace(notice); notice != "" {
		line += "; " + notice
	}
	return strings.Join(strings.Fields(line), " ")
}
