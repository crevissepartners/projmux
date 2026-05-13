package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
)

const aiIngestListPanesFormat = "#{pane_id}\x1f#{pane_current_path}\x1f#{" + aiPaneThreadIDOption + "}\x1f#{" + aiPaneSessionIDOption + "}"
const claudeTranscriptTailLimit = 256 * 1024
const (
	aiBellDedupeOption = "@projmux_ai_bell_notified_at"
	aiBellDedupeWindow = 5 * time.Second
	aiBellPaneFormat   = "#{session_name}\x1f#{window_id}\x1f#{window_name}\x1f#{pane_id}\x1f#{pane_title}\x1f#{pane_current_command}\x1f#{socket_path}"
)

type codexNotifyPayload struct {
	Type                 string
	ThreadID             string
	SessionID            string
	TurnID               string
	CWD                  string
	Client               string
	Model                string
	InputMessages        []json.RawMessage
	LastAssistantMessage string
}

type aiPaneMatchInput struct {
	CWD       string
	ThreadID  string
	SessionID string
}

type aiPaneMatchRow struct {
	PaneID    string
	CWD       string
	ThreadID  string
	SessionID string
}

type claudeHookPayload struct {
	EventName        string
	SessionID        string
	CWD              string
	TranscriptPath   string
	NotificationType string
	Message          string
	Prompt           string
	ToolName         string
	ToolUseID        string
	ToolInput        map[string]any
	ErrorType        string
	ErrorMessage     string
	SubagentType     string
	SubagentID       string
	TeammateName     string
	TeammateID       string
	TeammateContext  string
}

func (c *aiCommand) runIngest(args []string, stderr io.Writer) error {
	if len(args) < 1 {
		printAIUsage(stderr)
		return errors.New("ai ingest requires <agent-kind>")
	}
	switch args[0] {
	case "codex-notify":
		if len(args) != 2 {
			printAIUsage(stderr)
			return errors.New("ai ingest codex-notify requires a JSON payload argument")
		}
		return c.ingestCodexNotify([]byte(args[1]))
	case "claude-hook":
		if len(args) != 1 {
			printAIUsage(stderr)
			return errors.New("ai ingest claude-hook reads JSON from stdin and accepts no payload arguments")
		}
		reader := c.stdin
		if reader == nil {
			reader = os.Stdin
		}
		data, err := io.ReadAll(io.LimitReader(reader, 1024*1024+1))
		if err != nil {
			return fmt.Errorf("read claude hook payload: %w", err)
		}
		if len(data) > 1024*1024 {
			return errors.New("claude hook payload exceeds 1 MiB")
		}
		return c.ingestClaudeHook(data)
	case "bell":
		return c.runIngestBell(args[1:], stderr)
	case "help", "--help", "-h":
		printAIUsage(stderr)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai ingest agent-kind: %s", args[0])
	}
}

func (c *aiCommand) runIngestBell(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai ingest bell", flag.ContinueOnError)
	fs.SetOutput(stderr)
	paneID := fs.String("pane", "", "target tmux pane id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai ingest bell does not accept positional arguments")
	}
	if strings.TrimSpace(*paneID) == "" {
		printAIUsage(stderr)
		return errors.New("ai ingest bell requires --pane <pane_id>")
	}
	return c.ingestBell(*paneID)
}

type bellPaneInfo struct {
	Session string
	Window  string
	WinName string
	Pane    string
	Title   string
	Command string
	Socket  string
}

func (c *aiCommand) ingestBell(paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return nil
	}
	info, ok := c.readBellPaneInfo(paneID)
	if !ok {
		return nil
	}
	if c.duplicateBellRecent(info.Pane) {
		return nil
	}
	store, err := c.aiNotifyStore()
	if err != nil {
		return err
	}
	text := composeBellNotifyText(info)
	metadata := map[string]string{
		"agent": "bell",
		"event": "bell",
		"pane":  info.Pane,
	}
	if info.Session != "" {
		metadata["session"] = info.Session
	}
	if info.Window != "" {
		metadata["window"] = info.Window
	}
	if info.WinName != "" {
		metadata["window_name"] = info.WinName
	}
	if info.Title != "" {
		metadata["pane_title"] = info.Title
	}
	if info.Command != "" {
		metadata["pane_command"] = info.Command
	}
	if info.Socket != "" {
		metadata["socket"] = info.Socket
	}

	if _, _, err := store.Push(notify.PushInput{
		ID:       "ai:bell:" + info.Session + ":" + info.Pane,
		Text:     text,
		Severity: notify.SeverityInfo,
		Source:   notify.SourceAI,
		Metadata: metadata,
		TTL:      attentionNotifyTTL,
		Target: notify.Target{
			Socket:  info.Socket,
			Session: info.Session,
			Window:  info.Window,
			Pane:    info.Pane,
		},
	}); err != nil {
		return err
	}
	c.recordBellNotification(info.Pane)
	return nil
}

func (c *aiCommand) aiNotifyStore() (notifyStore, error) {
	if c.notifyStore != nil {
		return c.notifyStore, nil
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("resolve notify store paths: %w", err)
	}
	return notify.NewDefaultStore(paths), nil
}

func (c *aiCommand) readBellPaneInfo(paneID string) (bellPaneInfo, bool) {
	out := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, aiBellPaneFormat)
	if out == "" {
		return bellPaneInfo{}, false
	}
	fields := strings.Split(out, "\x1f")
	if len(fields) < 7 {
		return bellPaneInfo{}, false
	}
	info := bellPaneInfo{
		Session: strings.TrimSpace(fields[0]),
		Window:  strings.TrimSpace(fields[1]),
		WinName: strings.TrimSpace(fields[2]),
		Pane:    strings.TrimSpace(fields[3]),
		Title:   strings.TrimSpace(fields[4]),
		Command: strings.TrimSpace(fields[5]),
		Socket:  strings.TrimSpace(fields[6]),
	}
	if info.Pane == "" {
		info.Pane = paneID
	}
	return info, info.Session != ""
}

func (c *aiCommand) duplicateBellRecent(paneID string) bool {
	lastAt := parsePositiveInt(c.readTmuxPaneOption(paneID, aiBellDedupeOption))
	if lastAt <= 0 {
		return false
	}
	return c.now().Unix()-int64(lastAt) < int64(aiBellDedupeWindow/time.Second)
}

func (c *aiCommand) recordBellNotification(paneID string) {
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiBellDedupeOption, fmt.Sprintf("%d", c.now().Unix()))
}

func composeBellNotifyText(info bellPaneInfo) string {
	context := strings.TrimSpace(info.Title)
	if context == "" {
		context = strings.TrimSpace(info.Command)
	}
	if context == "" {
		context = strings.TrimSpace(info.WinName)
	}
	if context == "" {
		return "bell"
	}
	return "bell · " + context
}

func (c *aiCommand) ingestCodexNotify(data []byte) error {
	payload, err := parseCodexNotifyPayload(data)
	if err != nil {
		return err
	}
	if payload.Type != "agent-turn-complete" {
		return nil
	}

	paneID := c.matchAIPane(aiPaneMatchInput{
		CWD:       payload.CWD,
		ThreadID:  payload.ThreadID,
		SessionID: payload.SessionID,
	})
	if paneID == "" {
		return nil
	}

	metadata := payload.codexMetadata()
	topic := truncateRunes(payload.LastAssistantMessage, 80)
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneHookActiveOption, "1")
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneManagedOption, "1")
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneAgentOption, aiModeCodex)
	if payload.CWD != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneContextOption, payload.CWD)
	}
	if payload.ThreadID != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneThreadIDOption, payload.ThreadID)
	}
	if payload.SessionID != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneSessionIDOption, payload.SessionID)
	}
	if topic != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, topic)
	}

	body := formatCodexTurnCompleteNotifyBody(payload)
	notifyInput := attentionNotifyInput{
		ID:       codexNotifyID(payload),
		Text:     body.Text,
		Metadata: metadata,
		Force:    true,
	}
	return c.applyAIStatusWithNotify("waiting", paneID, notifyInput)
}

func (c *aiCommand) ingestClaudeHook(data []byte) error {
	payload, err := parseClaudeHookPayload(data)
	if err != nil {
		return err
	}

	paneID := c.matchAIPane(aiPaneMatchInput{
		CWD:       payload.CWD,
		SessionID: payload.SessionID,
	})
	if paneID == "" {
		return nil
	}

	c.markAIHookPane(paneID, aiModeClaude, payload.CWD, "", payload.SessionID, "")
	metadata := payload.claudeMetadata()

	switch payload.EventName {
	case "UserPromptSubmit":
		if topic := truncateRunes(payload.Prompt, 80); topic != "" {
			_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, topic)
		}
		return c.applyAIStatusWithNotify("thinking", paneID, attentionNotifyInput{
			Metadata: metadata,
		})
	case "Notification":
		body := formatClaudeNotificationNotifyBody(payload)
		return c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeNotifyID(payload),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		})
	case "PermissionRequest":
		body := formatClaudePermissionNotifyBody(payload)
		return c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudePermissionNotifyID(payload),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		})
	case "Stop":
		message := readClaudeTranscriptLastAssistantText(payload.TranscriptPath)
		body := formatClaudeStopNotifyBody(message)
		return c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeStopNotifyID(payload),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		})
	case "StopFailure":
		body := formatClaudeStopFailureNotifyBody(payload)
		return c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "stop-failure", payload.ErrorType, payload.ErrorMessage),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		})
	case "SubagentStop":
		body := formatClaudeSubagentStopNotifyBody(payload)
		return c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "subagent-stop", payload.SubagentType, payload.SubagentID),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		})
	case "TeammateIdle":
		body := formatClaudeTeammateIdleNotifyBody(payload)
		return c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "teammate-idle", payload.TeammateName, payload.TeammateID, payload.TeammateContext),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		})
	default:
		return nil
	}
}

func parseCodexNotifyPayload(data []byte) (codexNotifyPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return codexNotifyPayload{}, fmt.Errorf("parse codex notify payload: %w", err)
	}
	payload := codexNotifyPayload{
		Type:                 stringFromAny(raw["type"]),
		ThreadID:             firstString(raw, "thread-id", "thread_id"),
		SessionID:            firstString(raw, "session-id", "session_id"),
		TurnID:               firstString(raw, "turn-id", "turn_id"),
		CWD:                  stringFromAny(raw["cwd"]),
		Model:                stringFromAny(raw["model"]),
		LastAssistantMessage: stringFromAny(raw["last-assistant-message"]),
	}
	payload.Client = stringFromAny(raw["client"])
	if payload.Client == "" {
		payload.Client = firstNestedString(raw["client"], "name", "version")
	}
	if payload.Model == "" {
		payload.Model = firstNestedString(raw["client"], "model")
	}
	if messages, ok := raw["input-messages"].([]any); ok {
		payload.InputMessages = make([]json.RawMessage, 0, len(messages))
		for _, message := range messages {
			encoded, err := json.Marshal(message)
			if err == nil {
				payload.InputMessages = append(payload.InputMessages, encoded)
			}
		}
	}
	return payload, nil
}

func parseClaudeHookPayload(data []byte) (claudeHookPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return claudeHookPayload{}, fmt.Errorf("parse claude hook payload: %w", err)
	}
	payload := claudeHookPayload{
		EventName:        firstString(raw, "hook_event_name", "event_name"),
		SessionID:        firstString(raw, "session_id", "session-id"),
		CWD:              firstString(raw, "cwd", "workspace", "project_dir"),
		TranscriptPath:   firstString(raw, "transcript_path", "transcriptPath"),
		NotificationType: firstString(raw, "notification_type", "notificationType"),
		Message:          firstString(raw, "message", "text"),
		Prompt:           firstString(raw, "prompt", "user_prompt"),
		ToolName:         firstString(raw, "tool_name", "toolName"),
		ToolUseID:        firstString(raw, "tool_use_id", "toolUseID", "id"),
		ErrorType:        firstString(raw, "error_type", "errorType", "failure_type", "failureType"),
		ErrorMessage:     firstString(raw, "error_message", "errorMessage", "message", "reason"),
		SubagentType:     firstString(raw, "subagent_type", "subagentType", "agent_type", "agentType"),
		SubagentID:       firstString(raw, "subagent_id", "subagentId", "agent_id", "agentId"),
		TeammateName:     firstString(raw, "teammate_name", "teammateName", "teammate"),
		TeammateID:       firstString(raw, "teammate_id", "teammateId"),
		TeammateContext:  firstString(raw, "teammate_context", "teammateContext", "context", "reason", "message"),
	}
	if payload.CWD == "" {
		payload.CWD = firstNestedString(raw["workspace"], "cwd", "path")
	}
	if payload.Message == "" {
		payload.Message = firstNestedString(raw["notification"], "message", "text")
	}
	if payload.NotificationType == "" {
		payload.NotificationType = firstNestedString(raw["notification"], "notification_type", "type")
	}
	if payload.ToolName == "" {
		payload.ToolName = firstNestedString(raw["tool"], "name", "tool_name")
	}
	if payload.ToolUseID == "" {
		payload.ToolUseID = firstNestedString(raw["tool"], "id", "tool_use_id")
	}
	if payload.ErrorType == "" {
		payload.ErrorType = firstNestedString(raw["error"], "type", "name", "code")
	}
	if payload.ErrorMessage == "" {
		payload.ErrorMessage = firstNestedString(raw["error"], "message", "text", "reason")
	}
	if payload.SubagentType == "" {
		payload.SubagentType = firstNestedString(raw["subagent"], "type", "name", "kind")
	}
	if payload.SubagentID == "" {
		payload.SubagentID = firstNestedString(raw["subagent"], "id", "subagent_id", "agent_id")
	}
	if payload.TeammateName == "" {
		payload.TeammateName = firstNestedString(raw["teammate"], "name", "type", "kind")
	}
	if payload.TeammateID == "" {
		payload.TeammateID = firstNestedString(raw["teammate"], "id", "teammate_id")
	}
	if payload.TeammateContext == "" {
		payload.TeammateContext = firstNestedString(raw["teammate"], "context", "status", "reason", "message")
	}
	payload.ToolInput = mapFromAny(raw["tool_input"])
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["input"])
	}
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["tool"])
		delete(payload.ToolInput, "name")
		delete(payload.ToolInput, "tool_name")
		delete(payload.ToolInput, "id")
		delete(payload.ToolInput, "tool_use_id")
	}
	return payload, nil
}

func (p codexNotifyPayload) codexMetadata() map[string]string {
	metadata := map[string]string{
		"agent":      aiModeCodex,
		"thread_id":  p.ThreadID,
		"session_id": p.SessionID,
		"turn_id":    p.TurnID,
		"cwd":        p.CWD,
		"model":      p.Model,
		"client":     p.Client,
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if value := strings.TrimSpace(v); value != "" {
			out[k] = value
		}
	}
	return out
}

func (p claudeHookPayload) claudeMetadata() map[string]string {
	metadata := map[string]string{
		"agent":             aiModeClaude,
		"event":             p.EventName,
		"session_id":        p.SessionID,
		"cwd":               p.CWD,
		"transcript_path":   p.TranscriptPath,
		"notification_type": p.NotificationType,
		"prompt":            truncateRunes(p.Prompt, 60),
		"tool_name":         p.ToolName,
		"tool_use_id":       p.ToolUseID,
		"error_type":        p.ErrorType,
		"error_message":     truncateRunes(p.ErrorMessage, 160),
		"subagent_type":     p.SubagentType,
		"subagent_id":       p.SubagentID,
		"teammate_name":     p.TeammateName,
		"teammate_id":       p.TeammateID,
		"teammate_context":  truncateRunes(p.TeammateContext, 160),
	}
	for key, value := range p.ToolInput {
		if text := stringFromAny(value); text != "" {
			metadata["tool_input."+key] = truncateRunes(text, 160)
		}
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if value := strings.TrimSpace(v); value != "" {
			out[k] = value
		}
	}
	return out
}

func (c *aiCommand) markAIHookPane(paneID, agent, cwd, threadID, sessionID, topic string) {
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneHookActiveOption, "1")
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneManagedOption, "1")
	if agent != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneAgentOption, agent)
	}
	if cwd != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneContextOption, cwd)
	}
	if threadID != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneThreadIDOption, threadID)
	}
	if sessionID != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneSessionIDOption, sessionID)
	}
	if topic != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, topic)
	}
}

func (c *aiCommand) matchAIPane(in aiPaneMatchInput) string {
	if envPane := strings.TrimSpace(c.env("TMUX_PANE")); envPane != "" {
		return envPane
	}
	rows := c.listAIPaneMatchRows()
	if cwd := cleanMatchPath(in.CWD); cwd != "" {
		for _, row := range rows {
			if cleanMatchPath(row.CWD) == cwd {
				return row.PaneID
			}
		}
	}
	threadID := strings.TrimSpace(in.ThreadID)
	sessionID := strings.TrimSpace(in.SessionID)
	if threadID == "" && sessionID == "" {
		return ""
	}
	for _, row := range rows {
		if threadID != "" && strings.TrimSpace(row.ThreadID) == threadID {
			return row.PaneID
		}
		if sessionID != "" && strings.TrimSpace(row.SessionID) == sessionID {
			return row.PaneID
		}
	}
	return ""
}

func (c *aiCommand) listAIPaneMatchRows() []aiPaneMatchRow {
	out, err := c.read("tmux", "list-panes", "-a", "-F", aiIngestListPanesFormat)
	if err != nil {
		return nil
	}
	rows := strings.Split(strings.TrimSpace(string(out)), "\n")
	matches := make([]aiPaneMatchRow, 0, len(rows))
	for _, raw := range rows {
		fields := strings.Split(raw, "\x1f")
		if len(fields) != 4 {
			continue
		}
		paneID := strings.TrimSpace(fields[0])
		if paneID == "" {
			continue
		}
		matches = append(matches, aiPaneMatchRow{
			PaneID:    paneID,
			CWD:       strings.TrimSpace(fields[1]),
			ThreadID:  strings.TrimSpace(fields[2]),
			SessionID: strings.TrimSpace(fields[3]),
		})
	}
	return matches
}

func cleanMatchPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func codexNotifyID(p codexNotifyPayload) string {
	threadID := strings.TrimSpace(p.ThreadID)
	turnID := strings.TrimSpace(p.TurnID)
	switch {
	case threadID != "" && turnID != "":
		return "ai:codex:" + threadID + ":" + turnID
	case threadID != "":
		return "ai:codex:" + threadID
	default:
		return ""
	}
}

func claudeNotifyID(p claudeHookPayload) string {
	parts := []string{"ai", "claude", "notification"}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.NotificationType); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.Message); value != "" {
		parts = append(parts, truncateRunes(value, 40))
	}
	return strings.Join(parts, ":")
}

func claudePermissionNotifyID(p claudeHookPayload) string {
	sessionID := strings.TrimSpace(p.SessionID)
	toolUseID := strings.TrimSpace(p.ToolUseID)
	switch {
	case sessionID != "" && toolUseID != "":
		return "ai:claude:permission:" + sessionID + ":" + toolUseID
	case toolUseID != "":
		return "ai:claude:permission:" + toolUseID
	default:
		return ""
	}
}

func claudeStopNotifyID(p claudeHookPayload) string {
	if sessionID := strings.TrimSpace(p.SessionID); sessionID != "" {
		return "ai:claude:stop:" + sessionID
	}
	return ""
}

func claudeExtraNotifyID(p claudeHookPayload, kind string, values ...string) string {
	parts := []string{"ai", "claude", kind}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	for _, value := range values {
		if trimmed := truncateRunes(value, 40); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, ":")
}

func readClaudeTranscriptLastAssistantText(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size <= 0 {
		return ""
	}
	start := int64(0)
	if size > claudeTranscriptTailLimit {
		start = size - claudeTranscriptTailLimit
	}
	buf := make([]byte, size-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(buf)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if text := claudeAssistantTextFromJSONLine(lines[i]); text != "" {
			return text
		}
	}
	return ""
}

func claudeAssistantTextFromJSONLine(line string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return ""
	}
	if strings.EqualFold(stringFromAny(raw["role"]), "assistant") {
		return claudeContentText(raw["content"])
	}
	message := mapFromAny(raw["message"])
	if strings.EqualFold(stringFromAny(message["role"]), "assistant") {
		return claudeContentText(message["content"])
	}
	return ""
}

func claudeContentText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		for _, item := range v {
			if text := firstNestedString(item, "text"); text != "" {
				return text
			}
			if text := stringFromAny(item); text != "" {
				return text
			}
		}
	case map[string]any:
		return firstString(v, "text", "content")
	}
	return ""
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringFromAny(raw[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNestedString(value any, keys ...string) string {
	nested, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return firstString(nested, keys...)
}

func mapFromAny(value any) map[string]any {
	if raw, ok := value.(map[string]any); ok {
		out := make(map[string]any, len(raw))
		maps.Copy(out, raw)
		return out
	}
	return map[string]any{}
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func truncateRunes(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
