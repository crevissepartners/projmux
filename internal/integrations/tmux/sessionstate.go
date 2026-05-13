package tmux

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

const (
	sessionStateStartupKindOption    = "@projmux_recipe_kind"
	sessionStateStartupCommandOption = "@projmux_startup_command"
	sessionStateAIManagedOption      = "@projmux_ai_managed"
	sessionStateAIAgentOption        = "@projmux_ai_agent"
	sessionStateAITopicOption        = "@projmux_ai_topic"
	sessionStateAIResumeIDOption     = "@projmux_ai_resume_id"
	sessionStateSourceOption         = "@projmux_sessionstate_source"
)

type sessionStateWindowRow struct {
	index  int
	name   string
	layout string
}

type sessionStatePaneRow struct {
	windowIndex    int
	paneIndex      int
	title          string
	active         bool
	cwd            string
	recipeKind     string
	startupCommand string
	aiManaged      string
	aiAgent        string
	aiTopic        string
	aiResumeID     string
}

// CaptureSessionSnapshot captures tmux window/pane metadata for a sessionstate
// restore snapshot. Process commands are intentionally not captured; only
// explicit projmux recipe metadata is converted into replay recipes.
func (c *Client) CaptureSessionSnapshot(ctx context.Context, sessionName string, now time.Time) (sessionstate.Snapshot, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return sessionstate.Snapshot{}, errSessionNameRequired
	}
	if now.IsZero() {
		now = time.Now()
	}

	windows, err := c.listSessionStateWindows(ctx, sessionName)
	if err != nil {
		return sessionstate.Snapshot{}, err
	}
	panes, err := c.listSessionStatePanes(ctx, sessionName)
	if err != nil {
		return sessionstate.Snapshot{}, err
	}

	panesByWindow := make(map[int][]sessionStatePaneRow)
	defaultCWD := ""
	for _, pane := range panes {
		panesByWindow[pane.windowIndex] = append(panesByWindow[pane.windowIndex], pane)
		if defaultCWD == "" && pane.cwd != "" {
			defaultCWD = pane.cwd
		}
	}
	for windowIndex := range panesByWindow {
		sort.SliceStable(panesByWindow[windowIndex], func(i, j int) bool {
			return panesByWindow[windowIndex][i].paneIndex < panesByWindow[windowIndex][j].paneIndex
		})
	}

	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    sessionName,
		Source:     sessionstate.SourceLabel(c.SessionStateSource(ctx, sessionName)),
		DefaultCWD: defaultCWD,
		SavedAt:    now,
		Windows:    make([]sessionstate.Window, 0, len(windows)),
	}
	for _, row := range windows {
		windowPanes := panesByWindow[row.index]
		activePaneIndex := 0
		if len(windowPanes) > 0 {
			activePaneIndex = windowPanes[0].paneIndex
		}
		panesOut := make([]sessionstate.Pane, 0, len(windowPanes))
		for _, pane := range windowPanes {
			if pane.active {
				activePaneIndex = pane.paneIndex
			}
			panesOut = append(panesOut, sessionstate.Pane{
				Index:  pane.paneIndex,
				Title:  pane.title,
				CWD:    pane.cwd,
				Recipe: classifySessionStatePane(pane),
			})
		}
		snap.Windows = append(snap.Windows, sessionstate.Window{
			Index:           row.index,
			Name:            row.name,
			Layout:          row.layout,
			ActivePaneIndex: activePaneIndex,
			Panes:           panesOut,
		})
	}

	if err := snap.Validate(); err != nil {
		return sessionstate.Snapshot{}, err
	}
	return snap, nil
}

// SaveSessionSnapshot captures and atomically stores a tmux session snapshot.
func (c *Client) SaveSessionSnapshot(ctx context.Context, store sessionstate.Store, sessionName string, now time.Time) (sessionstate.Snapshot, error) {
	snap, err := c.CaptureSessionSnapshot(ctx, sessionName, now)
	if err != nil {
		return sessionstate.Snapshot{}, err
	}
	if err := store.Save(snap); err != nil {
		return sessionstate.Snapshot{}, err
	}
	return snap, nil
}

// SessionStateSource reads the live session-state source marker for a tmux
// session. Empty or unreadable markers are returned as empty so callers can
// fall back to snapshot metadata before defaulting to autosave.
func (c *Client) SessionStateSource(ctx context.Context, sessionName string) string {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return ""
	}
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-t", sessionName, "#{"+sessionStateSourceOption+"}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// MarkSessionStateSource writes the live source marker used by status surfaces
// and autosave policy.
func (c *Client) MarkSessionStateSource(ctx context.Context, sessionName, source string) error {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return errSessionNameRequired
	}
	source = sessionstate.SourceLabel(source)
	_, err := c.runner.Run(ctx, "tmux", "set-option", "-t", sessionName, "-q", sessionStateSourceOption, source)
	if err != nil {
		return fmt.Errorf("mark tmux sessionstate source: %w", err)
	}
	return nil
}

func (c *Client) listSessionStateWindows(ctx context.Context, sessionName string) ([]sessionStateWindowRow, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-windows", "-t", sessionName, "-F", tmuxFormat("#{window_index}", "#{window_name}", "#{window_layout}"))
	if err != nil {
		return nil, fmt.Errorf("capture tmux session %q windows: %w", sessionName, err)
	}
	windows, err := parseSessionStateWindows(output)
	if err != nil {
		return nil, fmt.Errorf("capture tmux session %q windows: %w", sessionName, err)
	}
	return windows, nil
}

func (c *Client) listSessionStatePanes(ctx context.Context, sessionName string) ([]sessionStatePaneRow, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-s", "-t", sessionName, "-F", tmuxFormat(
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{"+sessionStateStartupKindOption+"}",
		"#{"+sessionStateStartupCommandOption+"}",
		"#{"+sessionStateAIManagedOption+"}",
		"#{"+sessionStateAIAgentOption+"}",
		"#{"+sessionStateAITopicOption+"}",
		"#{"+sessionStateAIResumeIDOption+"}",
	))
	if err != nil {
		return nil, fmt.Errorf("capture tmux session %q panes: %w", sessionName, err)
	}
	panes, err := parseSessionStatePanes(output)
	if err != nil {
		return nil, fmt.Errorf("capture tmux session %q panes: %w", sessionName, err)
	}
	return panes, nil
}

func classifySessionStatePane(pane sessionStatePaneRow) sessionstate.Recipe {
	if strings.TrimSpace(pane.recipeKind) == sessionstate.RecipeKindStartup && strings.TrimSpace(pane.startupCommand) != "" {
		return sessionstate.StartupRecipe(pane.startupCommand)
	}
	if strings.TrimSpace(pane.aiManaged) != "" && strings.TrimSpace(pane.aiAgent) != "" && strings.TrimSpace(pane.aiResumeID) != "" {
		return sessionstate.AgentRecipe(pane.aiAgent, pane.aiResumeID, pane.aiTopic)
	}
	return sessionstate.ShellRecipe()
}

func parseSessionStateWindows(output []byte) ([]sessionStateWindowRow, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	lines := strings.Split(string(output), "\n")
	windows := make([]sessionStateWindowRow, 0, len(lines))
	for _, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}
		fields := splitTmuxFields(rawLine, 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("parse tmux sessionstate windows: malformed row %q", rawLine)
		}
		index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			return nil, errWindowIndexInvalid
		}
		windows = append(windows, sessionStateWindowRow{
			index:  index,
			name:   strings.TrimSpace(fields[1]),
			layout: strings.TrimSpace(fields[2]),
		})
	}
	return windows, nil
}

func parseSessionStatePanes(output []byte) ([]sessionStatePaneRow, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	lines := strings.Split(string(output), "\n")
	panes := make([]sessionStatePaneRow, 0, len(lines))
	for _, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}
		fields := splitTmuxFields(rawLine, 11)
		if len(fields) != 11 {
			return nil, fmt.Errorf("parse tmux sessionstate panes: malformed row %q", rawLine)
		}
		windowIndex, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			return nil, errWindowIndexInvalid
		}
		paneIndex, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			return nil, errPaneIndexInvalid
		}
		active, err := parseActiveFlag(fields[3])
		if err != nil {
			return nil, err
		}
		panes = append(panes, sessionStatePaneRow{
			windowIndex:    windowIndex,
			paneIndex:      paneIndex,
			title:          strings.TrimSpace(fields[2]),
			active:         active,
			cwd:            strings.TrimSpace(fields[4]),
			recipeKind:     strings.TrimSpace(fields[5]),
			startupCommand: strings.TrimSpace(fields[6]),
			aiManaged:      strings.TrimSpace(fields[7]),
			aiAgent:        strings.TrimSpace(fields[8]),
			aiTopic:        strings.TrimSpace(fields[9]),
			aiResumeID:     strings.TrimSpace(fields[10]),
		})
	}
	return panes, nil
}
