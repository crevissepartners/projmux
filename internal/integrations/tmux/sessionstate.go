package tmux

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	sessionStateAITopicManualOption  = "@projmux_ai_topic_manual"
	sessionStateAISessionIDOption    = "@projmux_ai_session_id"
	sessionStateAIResumeIDOption     = "@projmux_ai_resume_id"
	sessionStateAIResumeSourceOption = "@projmux_ai_resume_source"
	sessionStateAIResumeAtOption     = "@projmux_ai_resume_updated_at"
	sessionStateAITranscriptOption   = "@projmux_ai_transcript_path"
	sessionStateSourceOption         = "@projmux_sessionstate_source"
)

const sessionStateMaxClaudeTranscriptBytes = 1024 * 1024
const sessionStateMaxCodexRolloutPrefixBytes = 256 * 1024

type sessionStateWindowRow struct {
	index  int
	name   string
	layout string
}

type sessionStatePaneRow struct {
	windowIndex    int
	paneIndex      int
	title          string
	label          string
	active         bool
	cwd            string
	recipeKind     string
	startupCommand string
	aiManaged      string
	aiAgent        string
	aiTopic        string
	aiTopicManual  string
	aiResumeID     string
	aiResumeSource string
	aiResumeAt     string
}

type sessionStateResumeRefreshPaneRow struct {
	paneID           string
	cwd              string
	aiManaged        string
	aiAgent          string
	aiSessionID      string
	aiResumeID       string
	aiTranscriptPath string
}

type sessionStateCodexRolloutCandidate struct {
	path   string
	mtime  time.Time
	id     string
	cwd    string
	hasCWD bool
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
				Label:  pane.label,
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
		if candidate == "" && isCodexSessionStateRefreshPane(pane) && strings.TrimSpace(pane.aiResumeID) == "" {
			candidate = c.codexResumeIDFromRolloutLogs(pane.cwd)
			source = "codex-log"
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
	case "antigravity", "codex", "claude":
		return true
	default:
		return false
	}
}

func isClaudeSessionStateRefreshPane(pane sessionStateResumeRefreshPaneRow) bool {
	return strings.EqualFold(strings.TrimSpace(pane.aiAgent), "claude")
}

func isCodexSessionStateRefreshPane(pane sessionStateResumeRefreshPaneRow) bool {
	return strings.EqualFold(strings.TrimSpace(pane.aiAgent), "codex")
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

func (c *Client) codexResumeIDFromRolloutLogs(paneCWD string) string {
	paneCWD = filepath.Clean(strings.TrimSpace(paneCWD))
	if paneCWD == "." {
		return ""
	}
	root := c.codexSessionsRoot()
	if root == "" {
		return ""
	}
	candidates := c.codexRolloutCandidates(root)
	if len(candidates) == 0 {
		return ""
	}

	matchingCWD := make([]sessionStateCodexRolloutCandidate, 0, len(candidates))
	withoutCWD := make([]sessionStateCodexRolloutCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.hasCWD {
			if filepath.Clean(candidate.cwd) == paneCWD {
				matchingCWD = append(matchingCWD, candidate)
			}
			continue
		}
		withoutCWD = append(withoutCWD, candidate)
	}
	if id, ok := newestUniqueCodexRolloutID(matchingCWD); ok {
		return id
	}
	if len(matchingCWD) > 0 {
		return ""
	}
	if len(withoutCWD) != 1 {
		return ""
	}
	return withoutCWD[0].id
}

func (c *Client) codexSessionsRoot() string {
	if c.lookupEnv != nil {
		if home := strings.TrimSpace(c.lookupEnv("HOME")); home != "" {
			return filepath.Join(home, ".codex", "sessions")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

func (c *Client) codexRolloutCandidates(root string) []sessionStateCodexRolloutCandidate {
	var candidates []sessionStateCodexRolloutCandidate
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		if matched, matchErr := filepath.Match("rollout-*.jsonl", entry.Name()); matchErr != nil || !matched {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		id, cwd, hasCWD, ok := scanCodexRolloutSessionMeta(path)
		if !ok {
			return nil
		}
		candidates = append(candidates, sessionStateCodexRolloutCandidate{
			path:   path,
			mtime:  info.ModTime(),
			id:     id,
			cwd:    cwd,
			hasCWD: hasCWD,
		})
		return nil
	})
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].mtime.Equal(candidates[j].mtime) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].mtime.After(candidates[j].mtime)
	})
	return candidates
}

func newestUniqueCodexRolloutID(candidates []sessionStateCodexRolloutCandidate) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].mtime.Equal(candidates[j].mtime) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].mtime.After(candidates[j].mtime)
	})
	if len(candidates) > 1 && candidates[0].mtime.Equal(candidates[1].mtime) {
		return "", false
	}
	return candidates[0].id, true
}

func scanCodexRolloutSessionMeta(path string) (id, cwd string, hasCWD, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false, false
	}
	defer f.Close()

	limited, err := io.ReadAll(io.LimitReader(f, sessionStateMaxCodexRolloutPrefixBytes))
	if err != nil {
		return "", "", false, false
	}
	return codexRolloutSessionMetaFromPrefix(limited)
}

func codexRolloutSessionMetaFromPrefix(content []byte) (id, cwd string, hasCWD, ok bool) {
	for rawLine := range strings.SplitSeq(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			continue
		}
		if !isCodexSessionMetaRecord(fields) {
			continue
		}
		payload, _ := fields["payload"].(map[string]any)
		if payload == nil {
			payload = fields
		}
		id = firstNestedString(payload, "id", "session_id", "sessionId")
		if id == "" {
			continue
		}
		cwd = firstNestedString(payload, "cwd", "current_dir", "currentDir", "project_dir", "projectDir", "project_path", "projectPath", "working_directory", "workingDirectory")
		return id, cwd, cwd != "", true
	}
	return "", "", false, false
}

func isCodexSessionMetaRecord(fields map[string]any) bool {
	if strings.EqualFold(stringJSONField(fields, "type"), "session_meta") {
		return true
	}
	payload, _ := fields["payload"].(map[string]any)
	return payload != nil && strings.EqualFold(stringJSONField(payload, "type"), "session_meta")
}

func firstNestedString(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringJSONField(fields, key); value != "" {
			return value
		}
	}
	for _, raw := range fields {
		nested, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if value := firstNestedString(nested, keys...); value != "" {
			return value
		}
	}
	return ""
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
		"#{@projmux_pane_label}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{"+sessionStateStartupKindOption+"}",
		"#{"+sessionStateStartupCommandOption+"}",
		"#{"+sessionStateAIManagedOption+"}",
		"#{"+sessionStateAIAgentOption+"}",
		"#{"+sessionStateAITopicOption+"}",
		"#{"+sessionStateAITopicManualOption+"}",
		"#{"+sessionStateAIResumeIDOption+"}",
		"#{"+sessionStateAIResumeSourceOption+"}",
		"#{"+sessionStateAIResumeAtOption+"}",
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
		"#{pane_current_path}",
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
	if strings.TrimSpace(pane.aiManaged) != "" && strings.TrimSpace(pane.aiAgent) != "" {
		recipe := sessionstate.AgentRecipeWithResumeMetadata(pane.aiAgent, pane.aiResumeID, pane.aiTopic, pane.aiResumeSource, pane.aiResumeAt)
		recipe.TopicManual = strings.TrimSpace(pane.aiTopicManual) != ""
		return recipe
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
		fields := splitTmuxFields(rawLine, 15)
		switch len(fields) {
		case 14:
			// Phase 0 current format had a label but no topic ownership.
			fields = append(fields[:11], append([]string{""}, fields[11:]...)...)
		case 13:
			// The pre-label format already carried resume provenance.
			fields = append(fields[:3], append([]string{""}, fields[3:]...)...)
			fields = append(fields[:11], append([]string{""}, fields[11:]...)...)
		case 12:
			// Phase 0 label format before resume provenance was added.
			fields = append(fields[:11], append([]string{""}, fields[11:]...)...)
			fields = append(fields, "", "")
		}
		if len(fields) != 15 {
			legacy := splitTmuxFields(rawLine, 11)
			if len(legacy) == 11 {
				fields = append(legacy[:3], append([]string{""}, legacy[3:]...)...)
				fields = append(fields[:11], append([]string{""}, fields[11:]...)...)
				fields = append(fields, "", "")
			}
		}
		if len(fields) != 15 {
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
		active, err := parseActiveFlag(fields[4])
		if err != nil {
			return nil, err
		}
		panes = append(panes, sessionStatePaneRow{
			windowIndex:    windowIndex,
			paneIndex:      paneIndex,
			title:          strings.TrimSpace(fields[2]),
			label:          strings.TrimSpace(fields[3]),
			active:         active,
			cwd:            strings.TrimSpace(fields[5]),
			recipeKind:     strings.TrimSpace(fields[6]),
			startupCommand: strings.TrimSpace(fields[7]),
			aiManaged:      strings.TrimSpace(fields[8]),
			aiAgent:        strings.TrimSpace(fields[9]),
			aiTopic:        strings.TrimSpace(fields[10]),
			aiTopicManual:  strings.TrimSpace(fields[11]),
			aiResumeID:     strings.TrimSpace(fields[12]),
			aiResumeSource: strings.TrimSpace(fields[13]),
			aiResumeAt:     strings.TrimSpace(fields[14]),
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
		fields := splitTmuxFields(rawLine, 7)
		if len(fields) != 7 {
			return nil, fmt.Errorf("parse tmux sessionstate AI resume metadata panes: malformed row %q", rawLine)
		}
		panes = append(panes, sessionStateResumeRefreshPaneRow{
			paneID:           strings.TrimSpace(fields[0]),
			cwd:              strings.TrimSpace(fields[1]),
			aiManaged:        strings.TrimSpace(fields[2]),
			aiAgent:          strings.TrimSpace(fields[3]),
			aiSessionID:      strings.TrimSpace(fields[4]),
			aiResumeID:       strings.TrimSpace(fields[5]),
			aiTranscriptPath: strings.TrimSpace(fields[6]),
		})
	}
	return panes, nil
}
