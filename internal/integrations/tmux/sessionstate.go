package tmux

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	sessionStateAISessionIDOption    = "@projmux_ai_session_id"
	sessionStateAIResumeIDOption     = "@projmux_ai_resume_id"
	sessionStateAIResumeSourceOption = "@projmux_ai_resume_source"
	sessionStateAIResumeAtOption     = "@projmux_ai_resume_updated_at"
	sessionStateAITranscriptOption   = "@projmux_ai_transcript_path"
	sessionStateSourceOption         = "@projmux_sessionstate_source"
)

const sessionStateMaxClaudeTranscriptBytes = 1024 * 1024

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

type sessionStateResumeRefreshPaneRow struct {
	paneID           string
	aiManaged        string
	aiAgent          string
	aiSessionID      string
	aiResumeID       string
	aiTranscriptPath string
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
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return sessionstate.Snapshot{}, errSessionNameRequired
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := c.refreshSessionStateAIResumeMetadata(ctx, sessionName, now); err != nil {
		return sessionstate.Snapshot{}, err
	}
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

func (c *Client) refreshSessionStateAIResumeMetadata(ctx context.Context, sessionName string, now time.Time) error {
	panes, err := c.listSessionStateResumeRefreshPanes(ctx, sessionName)
	if err != nil {
		return err
	}
	updatedAt := now.UTC().Format(time.RFC3339)
	for _, pane := range panes {
		if !isSessionStateRefreshAgent(pane) {
			continue
		}
		candidate := strings.TrimSpace(pane.aiSessionID)
		source := "session-id"
		if candidate == "" && isClaudeSessionStateRefreshPane(pane) {
			candidate = c.claudeResumeIDFromTranscript(pane.aiTranscriptPath)
			source = "claude-transcript"
		}
		if candidate == "" || candidate == strings.TrimSpace(pane.aiResumeID) {
			continue
		}
		c.setSessionStateAIResumeMetadata(ctx, pane.paneID, candidate, source, updatedAt)
	}
	return nil
}

func (c *Client) setSessionStateAIResumeMetadata(ctx context.Context, paneID, resumeID, source, updatedAt string) {
	if _, err := c.runner.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, sessionStateAIResumeIDOption, resumeID); err != nil {
		return
	}
	if _, err := c.runner.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, sessionStateAIResumeSourceOption, source); err != nil {
		return
	}
	_, _ = c.runner.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, sessionStateAIResumeAtOption, updatedAt)
}

func isSessionStateRefreshAgent(pane sessionStateResumeRefreshPaneRow) bool {
	if strings.TrimSpace(pane.aiManaged) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(pane.aiAgent)) {
	case "codex", "claude":
		return true
	default:
		return false
	}
}

func isClaudeSessionStateRefreshPane(pane sessionStateResumeRefreshPaneRow) bool {
	return strings.EqualFold(strings.TrimSpace(pane.aiAgent), "claude")
}

func (c *Client) claudeResumeIDFromTranscript(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || c.readFile == nil {
		return ""
	}
	if info, err := os.Stat(path); err == nil && info.Size() > sessionStateMaxClaudeTranscriptBytes {
		return ""
	}
	content, err := c.readFile(path)
	if err != nil || len(content) > sessionStateMaxClaudeTranscriptBytes {
		return ""
	}
	if id := claudeResumeIDFromTranscriptJSONL(content); id != "" {
		return id
	}
	return claudeResumeIDFromTranscriptFilename(path)
}

func claudeResumeIDFromTranscriptJSONL(content []byte) string {
	var fallback string
	for rawLine := range strings.SplitSeq(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			continue
		}
		if id := stringJSONField(fields, "sessionId"); id != "" {
			return id
		}
		if fallback == "" {
			fallback = stringJSONField(fields, "session_id")
		}
	}
	return fallback
}

func stringJSONField(fields map[string]any, key string) string {
	value, ok := fields[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func claudeResumeIDFromTranscriptFilename(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if isUUIDLikeSessionStateID(stem) {
		return stem
	}
	return ""
}

func isUUIDLikeSessionStateID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
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

func (c *Client) listSessionStateResumeRefreshPanes(ctx context.Context, sessionName string) ([]sessionStateResumeRefreshPaneRow, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-s", "-t", sessionName, "-F", tmuxFormat(
		"#{pane_id}",
		"#{"+sessionStateAIManagedOption+"}",
		"#{"+sessionStateAIAgentOption+"}",
		"#{"+sessionStateAISessionIDOption+"}",
		"#{"+sessionStateAIResumeIDOption+"}",
		"#{"+sessionStateAITranscriptOption+"}",
	))
	if err != nil {
		return nil, fmt.Errorf("refresh tmux session %q AI resume metadata: %w", sessionName, err)
	}
	panes, err := parseSessionStateResumeRefreshPanes(output)
	if err != nil {
		return nil, fmt.Errorf("refresh tmux session %q AI resume metadata: %w", sessionName, err)
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

func parseSessionStateResumeRefreshPanes(output []byte) ([]sessionStateResumeRefreshPaneRow, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	lines := strings.Split(string(output), "\n")
	panes := make([]sessionStateResumeRefreshPaneRow, 0, len(lines))
	for _, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}
		fields := splitTmuxFields(rawLine, 6)
		if len(fields) != 6 {
			return nil, fmt.Errorf("parse tmux sessionstate AI resume metadata panes: malformed row %q", rawLine)
		}
		panes = append(panes, sessionStateResumeRefreshPaneRow{
			paneID:           strings.TrimSpace(fields[0]),
			aiManaged:        strings.TrimSpace(fields[1]),
			aiAgent:          strings.TrimSpace(fields[2]),
			aiSessionID:      strings.TrimSpace(fields[3]),
			aiResumeID:       strings.TrimSpace(fields[4]),
			aiTranscriptPath: strings.TrimSpace(fields[5]),
		})
	}
	return panes, nil
}
