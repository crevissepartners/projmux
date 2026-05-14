package app

import (
	"bytes"
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
	aiIngestLogName    = "ai-ingest.log"
	aiIngestLogMaxSize = 1024 * 1024
	aiIngestLogRetain  = 512 * 1024
)

type codexHookPayload struct {
	EventName      string
	ThreadID       string
	SessionID      string
	TurnID         string
	CWD            string
	TranscriptPath string
	Model          string
	ToolName       string
	ToolInput      map[string]any
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

type aiIngestLogEntry struct {
	At        string `json:"at"`
	Source    string `json:"source"`
	Event     string `json:"event,omitempty"`
	Result    string `json:"result"`
	Reason    string `json:"reason,omitempty"`
	Pane      string `json:"pane,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
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

func (c *aiCommand) runIngest(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		printAIUsage(stderr)
		return errors.New("ai ingest requires <agent-kind>")
	}
	switch args[0] {
	case "codex-hook":
		if len(args) != 1 {
			printAIUsage(stderr)
			return errors.New("ai ingest codex-hook reads JSON from stdin and accepts no payload arguments")
		}
		reader := c.stdin
		if reader == nil {
			reader = os.Stdin
		}
		data, err := io.ReadAll(io.LimitReader(reader, 1024*1024+1))
		if err != nil {
			return fmt.Errorf("read codex hook payload: %w", err)
		}
		if len(data) > 1024*1024 {
			return errors.New("codex hook payload exceeds 1 MiB")
		}
		return c.ingestCodexHook(data)
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
	case "log":
		return c.runIngestLog(args[1:], stdout, stderr)
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

func (c *aiCommand) runIngestLog(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai ingest log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tail := fs.Int("tail", 50, "number of recent log entries to print")
	jsonOut := fs.Bool("json", false, "print raw JSONL entries")
	pathOnly := fs.Bool("path", false, "print the ingest log path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai ingest log does not accept positional arguments")
	}

	path, err := c.aiIngestLogPath()
	if err != nil {
		return err
	}
	if *pathOnly {
		fmt.Fprintln(stdout, path)
		return nil
	}

	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read ai ingest log: %w", err)
	}
	lines := nonEmptyLines(string(data))
	if *tail >= 0 && len(lines) > *tail {
		lines = lines[len(lines)-*tail:]
	}
	for _, line := range lines {
		if *jsonOut {
			fmt.Fprintln(stdout, line)
			continue
		}
		var entry aiIngestLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			fmt.Fprintln(stdout, line)
			continue
		}
		fmt.Fprintln(stdout, formatAIIngestLogEntry(entry))
	}
	return nil
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
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "ignored", Reason: "blank pane"})
		return nil
	}
	info, ok := c.readBellPaneInfo(paneID)
	if !ok {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "ignored", Reason: "pane not found", Pane: paneID})
		return nil
	}
	if c.duplicateBellRecent(info.Pane) {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "deduped", Pane: info.Pane})
		return nil
	}
	store, err := c.aiNotifyStore()
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "error", Reason: err.Error(), Pane: info.Pane})
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
		c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "error", Reason: err.Error(), Pane: info.Pane})
		return err
	}
	c.recordBellNotification(info.Pane)
	c.appendAIIngestLog(aiIngestLogEntry{Source: "tmux-bell", Event: "bell", Result: "notify", Pane: info.Pane})
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
	fields := splitTmuxUnitFields(out)
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

func (c *aiCommand) ingestCodexHook(data []byte) error {
	payload, err := parseCodexHookPayload(data)
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Result: "error", Reason: err.Error()})
		return err
	}

	paneID := c.matchAIPane(aiPaneMatchInput{
		CWD:       payload.CWD,
		ThreadID:  payload.matchThreadID(),
		SessionID: payload.SessionID,
	})
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: "no matching pane", CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	}

	metadata := payload.codexHookMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderCodex, payload.EventName)
	switch payload.EventName {
	case "UserPromptSubmit":
		if action.Action == aiHookActionQuiet {
			c.quietCodexHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		if err := c.applyAIStatusWithNotify("thinking", paneID, attentionNotifyInput{
			Metadata: metadata,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "state", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "Stop":
		if action.Action == aiHookActionQuiet {
			c.quietCodexHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		body := formatCodexHookStopNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:       codexHookNotifyID(payload, "stop"),
				Text:     body.Text,
				Severity: body.Severity,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       codexHookNotifyID(payload, "stop"),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "PermissionRequest":
		if action.Action == aiHookActionQuiet {
			c.quietCodexHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		body := formatCodexHookPermissionNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:       codexHookNotifyID(payload, "permission"),
				Text:     body.Text,
				Severity: body.Severity,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       codexHookNotifyID(payload, "permission"),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "PreToolUse", "PostToolUse", "PreCompact", "PostCompact", "SessionStart":
		c.quietCodexHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	default:
		c.quietCodexHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	}
}

func (c *aiCommand) ingestClaudeHook(data []byte) error {
	payload, err := parseClaudeHookPayload(data)
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Result: "error", Reason: err.Error()})
		return err
	}

	paneID := c.matchAIPane(aiPaneMatchInput{
		CWD:       payload.CWD,
		SessionID: payload.SessionID,
	})
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "ignored", Reason: "no matching pane", CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	}

	c.markAIHookPane(paneID, aiModeClaude, payload.CWD, "", payload.SessionID, payload.TranscriptPath)
	metadata := payload.claudeMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderClaude, payload.EventName)

	switch payload.EventName {
	case "UserPromptSubmit":
		if action.Action == aiHookActionQuiet {
			c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		if err := c.applyAIStatusWithNotify("thinking", paneID, attentionNotifyInput{
			Metadata: metadata,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "state", Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	case "Notification":
		if action.Action == aiHookActionQuiet {
			c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		body := formatClaudeNotificationNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:       claudeNotifyID(payload),
				Text:     body.Text,
				Severity: body.Severity,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeNotifyID(payload),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	case "PermissionRequest":
		if action.Action == aiHookActionQuiet {
			c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		body := formatClaudePermissionNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:       claudePermissionNotifyID(payload),
				Text:     body.Text,
				Severity: body.Severity,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudePermissionNotifyID(payload),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	case "Stop":
		if action.Action == aiHookActionQuiet {
			c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		message := readClaudeTranscriptLastAssistantText(payload.TranscriptPath)
		body := formatClaudeStopNotifyBody(message)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:       claudeStopNotifyID(payload),
				Text:     body.Text,
				Severity: body.Severity,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeStopNotifyID(payload),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	case "StopFailure":
		if action.Action == aiHookActionQuiet {
			c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		body := formatClaudeStopFailureNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:       claudeExtraNotifyID(payload, "stop-failure", payload.ErrorType, payload.ErrorMessage),
				Text:     body.Text,
				Severity: body.Severity,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "stop-failure", payload.ErrorType, payload.ErrorMessage),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	case "SubagentStop":
		if action.Action == aiHookActionNotify {
			body := formatClaudeSubagentStopNotifyBody(payload)
			if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
				ID:       claudeExtraNotifyID(payload, "subagent-stop", payload.SubagentType, payload.SubagentID),
				Text:     body.Text,
				Severity: body.Severity,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return nil
		}
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:       claudeExtraNotifyID(payload, "subagent-stop", payload.SubagentType, payload.SubagentID),
				Text:     formatClaudeSubagentStopNotifyBody(payload).Text,
				Severity: notify.SeverityInfo,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return nil
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "quiet", Reason: "high-volume event", Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "PostToolBatch", "PermissionDenied", "UserPromptExpansion", "SessionStart", "SubagentStart", "PreCompact", "PostCompact", "SessionEnd", "Setup", "TaskCreated", "TaskCompleted", "Elicitation", "ElicitationResult", "ConfigChange", "InstructionsLoaded", "WorktreeCreate", "WorktreeRemove", "CwdChanged", "FileChanged":
		c.quietClaudeHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	case "TeammateIdle":
		if action.Action == aiHookActionQuiet {
			c.quietClaudeHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		body := formatClaudeTeammateIdleNotifyBody(payload)
		if action.Action == aiHookActionState {
			if err := c.applyAIStatusStateOnly("waiting", paneID, attentionNotifyInput{
				ID:       claudeExtraNotifyID(payload, "teammate-idle", payload.TeammateName, payload.TeammateID, payload.TeammateContext),
				Text:     body.Text,
				Severity: body.Severity,
				Metadata: metadata,
				Force:    true,
			}); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "state", Reason: aiHookStateReason(action), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return nil
		}
		if err := c.applyAIStatusWithNotify("waiting", paneID, attentionNotifyInput{
			ID:       claudeExtraNotifyID(payload, "teammate-idle", payload.TeammateName, payload.TeammateID, payload.TeammateContext),
			Text:     body.Text,
			Severity: body.Severity,
			Metadata: metadata,
			Force:    true,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
		return nil
	default:
		c.quietClaudeHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	}
}

func (c *aiCommand) quietCodexHook(paneID string, payload codexHookPayload, reason string) {
	c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
	c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "quiet", Reason: reason, Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
}

func (c *aiCommand) quietClaudeHook(paneID string, payload claudeHookPayload, reason string) {
	c.markAIHookPane(paneID, aiModeClaude, payload.CWD, "", payload.SessionID, payload.TranscriptPath)
	c.appendAIIngestLog(aiIngestLogEntry{Source: "claude-hook", Event: payload.EventName, Result: "quiet", Reason: reason, Pane: paneID, CWD: payload.CWD, SessionID: payload.SessionID})
}

func (c *aiCommand) aiHookFallbackReason(provider, event string) string {
	action, ok, err := c.aiHookCatalogAction(provider, event)
	if err != nil {
		return "catalog unavailable; quiet fallback"
	}
	if !ok {
		return "unknown event"
	}
	if action == aiHookActionQuiet {
		return "catalog quiet event"
	}
	return "catalog " + action + " event has no specialized handler"
}

func (c *aiCommand) aiIngestLogPath() (string, error) {
	homeDir, err := c.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	paths, err := config.Homes{
		HomeDir:    homeDir,
		ConfigHome: c.env("XDG_CONFIG_HOME"),
		StateHome:  c.env("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, aiIngestLogName), nil
}

func (c *aiCommand) appendAIIngestLog(entry aiIngestLogEntry) {
	path, err := c.aiIngestLogPath()
	if err != nil {
		return
	}
	if entry.At == "" {
		entry.At = c.now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	mkdirAll := c.mkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if err := mkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return
	}
	c.trimAIIngestLogFile(path)
}

func (c *aiCommand) trimAIIngestLogFile(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= aiIngestLogMaxSize {
		return
	}
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil || len(data) <= aiIngestLogMaxSize {
		return
	}
	start := max(len(data)-aiIngestLogRetain, 0)
	if start > 0 {
		if offset := bytes.IndexByte(data[start:], '\n'); offset >= 0 {
			start += offset + 1
		}
	}
	writeFile := c.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	_ = writeFile(path, data[start:], 0o600)
}

func nonEmptyLines(text string) []string {
	raw := strings.Split(strings.TrimSpace(text), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func formatAIIngestLogEntry(entry aiIngestLogEntry) string {
	parts := []string{entry.At, entry.Source}
	if entry.Event != "" {
		parts = append(parts, entry.Event)
	}
	parts = append(parts, entry.Result)
	for _, field := range []struct {
		key   string
		value string
	}{
		{"pane", entry.Pane},
		{"cwd", entry.CWD},
		{"thread", entry.ThreadID},
		{"session", entry.SessionID},
		{"turn", entry.TurnID},
		{"reason", entry.Reason},
	} {
		if strings.TrimSpace(field.value) != "" {
			parts = append(parts, field.key+"="+field.value)
		}
	}
	return strings.Join(parts, " ")
}

func parseCodexHookPayload(data []byte) (codexHookPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return codexHookPayload{}, fmt.Errorf("parse codex hook payload: %w", err)
	}
	payload := codexHookPayload{
		EventName:      firstString(raw, "hook_event_name", "event_name"),
		ThreadID:       firstString(raw, "thread_id", "thread-id"),
		SessionID:      firstString(raw, "session_id", "session-id"),
		TurnID:         firstString(raw, "turn_id", "turn-id"),
		CWD:            firstString(raw, "cwd", "workspace", "project_dir"),
		TranscriptPath: firstString(raw, "transcript_path", "transcriptPath"),
		Model:          firstString(raw, "model"),
		ToolName:       firstString(raw, "tool_name", "toolName"),
	}
	if payload.CWD == "" {
		payload.CWD = firstNestedString(raw["workspace"], "cwd", "path")
	}
	if payload.ToolName == "" {
		payload.ToolName = firstNestedString(raw["tool"], "name", "tool_name")
	}
	payload.ToolInput = mapFromAny(raw["tool_input"])
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["input"])
	}
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["tool"])
		delete(payload.ToolInput, "name")
		delete(payload.ToolInput, "tool_name")
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

func (p codexHookPayload) codexHookMetadata() map[string]string {
	metadata := map[string]string{
		"agent":           aiModeCodex,
		"event":           p.EventName,
		"session_id":      p.SessionID,
		"thread_id":       p.matchThreadID(),
		"turn_id":         p.TurnID,
		"cwd":             p.CWD,
		"transcript_path": p.TranscriptPath,
		"model":           p.Model,
		"tool_name":       p.ToolName,
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

func (p codexHookPayload) matchThreadID() string {
	if threadID := strings.TrimSpace(p.ThreadID); threadID != "" {
		return threadID
	}
	return p.SessionID
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

func (c *aiCommand) markAIHookPane(paneID, agent, cwd, threadID, sessionID, transcriptPath string) {
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
		c.writeAIHookResumeMetadata(paneID, sessionID)
	}
	if transcriptPath = strings.TrimSpace(transcriptPath); transcriptPath != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneTranscriptPathOption, transcriptPath)
	}
}

func (c *aiCommand) writeAIHookResumeMetadata(paneID, resumeID string) {
	resumeID = strings.TrimSpace(resumeID)
	if resumeID == "" {
		return
	}
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneResumeIDOption, resumeID)
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneResumeSourceOption, "hook")
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, aiPaneResumeUpdatedAtOption, c.now().UTC().Format(time.RFC3339))
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
		fields := splitTmuxUnitFields(raw)
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

func splitTmuxUnitFields(raw string) []string {
	if strings.Contains(raw, "\x1f") {
		return strings.Split(raw, "\x1f")
	}
	return strings.Split(raw, "\\037")
}

func cleanMatchPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func codexHookNotifyID(p codexHookPayload, kind string) string {
	parts := []string{"ai", "codex", kind}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.TurnID); value != "" {
		parts = append(parts, value)
	}
	if kind == "permission" {
		if value := strings.TrimSpace(p.ToolName); value != "" {
			parts = append(parts, value)
		}
		if summary := formatCodexToolInputSummary(p.ToolName, p.ToolInput); summary != "" {
			parts = append(parts, truncateRunes(summary, 40))
		}
	}
	return strings.Join(parts, ":")
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
