package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const aiIngestListPanesFormat = "#{pane_id}\x1f#{pane_current_path}\x1f#{" + aiPaneThreadIDOption + "}\x1f#{" + aiPaneSessionIDOption + "}"

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
	case "help", "--help", "-h":
		printAIUsage(stderr)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai ingest agent-kind: %s", args[0])
	}
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

	notifyInput := attentionNotifyInput{
		ID:       codexNotifyID(payload),
		Text:     codexNotifyText(payload.LastAssistantMessage),
		Metadata: metadata,
		Force:    true,
	}
	return c.applyAIStatusWithNotify("waiting", paneID, notifyInput)
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

func codexNotifyText(message string) string {
	text := "Codex · 응답 완료"
	if msg := truncateRunes(message, 80); msg != "" {
		text += " · " + msg
	}
	return text
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
