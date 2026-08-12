package tmux

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resources"
)

var (
	errResourceSessionIDRequired = errors.New("tmux resource session id is required")
	errResourceWindowIDRequired  = errors.New("tmux resource window id is required")
	errResourcePaneIDRequired    = errors.New("tmux resource pane id is required")
	errResourcePanePIDInvalid    = errors.New("tmux resource pane pid is invalid")
)

// ListResourcePanes reads the Linux attribution inventory without changing
// the established cross-backend Pane model. psmux therefore does not need to
// invent unsupported PID or TTY values.
func (c *Client) ListResourcePanes(ctx context.Context) ([]resources.PaneInventory, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-a", "-F", tmuxFormat(
		"#{socket_path}",
		"#{session_id}",
		"#{session_name}",
		"#{window_id}",
		"#{pane_id}",
		"#{pane_pid}",
		"#{pane_tty}",
		"#{@projmux_project_path}",
	))
	if err != nil {
		return nil, fmt.Errorf("list tmux resource panes: %w", err)
	}
	rows, err := parseResourcePanes(output, c.socket)
	if err != nil {
		return nil, fmt.Errorf("list tmux resource panes: %w", err)
	}
	return rows, nil
}

func parseResourcePanes(output []byte, fallbackSocket string) ([]resources.PaneInventory, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}
	lines := strings.Split(string(output), "\n")
	rows := make([]resources.PaneInventory, 0, len(lines))
	for _, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields := splitTmuxFields(raw, 8)
		if len(fields) != 8 {
			return nil, fmt.Errorf("parse tmux resource panes: malformed row %q", raw)
		}
		sessionID := strings.TrimSpace(fields[1])
		if sessionID == "" {
			return nil, errResourceSessionIDRequired
		}
		windowID := strings.TrimSpace(fields[3])
		if windowID == "" {
			return nil, errResourceWindowIDRequired
		}
		paneID := strings.TrimSpace(fields[4])
		if paneID == "" {
			return nil, errResourcePaneIDRequired
		}
		pid, err := strconv.Atoi(strings.TrimSpace(fields[5]))
		if err != nil || pid <= 0 {
			return nil, errResourcePanePIDInvalid
		}
		socket := strings.TrimSpace(fields[0])
		if socket == "" {
			socket = strings.TrimSpace(fallbackSocket)
		}
		rows = append(rows, resources.PaneInventory{
			Socket:        socket,
			SessionID:     sessionID,
			SessionName:   strings.TrimSpace(fields[2]),
			WindowID:      windowID,
			PaneID:        paneID,
			PanePID:       pid,
			PaneTTY:       strings.TrimSpace(fields[6]),
			ProjectAnchor: strings.TrimSpace(fields[7]),
		})
	}
	return rows, nil
}
