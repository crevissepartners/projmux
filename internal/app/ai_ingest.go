package app

import (
	"bytes"
	"context"
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
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
)

const (
	aiBellDedupeOption = "@projmux_ai_bell_notified_at"
	aiBellDedupeWindow = 5 * time.Second
	aiIngestLogName    = "ai-ingest.log"
	aiIngestLogMaxSize = 1024 * 1024
	aiIngestLogRetain  = 512 * 1024
)

var aiIngestListPanesFormats = []string{
	intmux.TmuxFormat("pane_id"),
	intmux.TmuxFormat("pane_current_path"),
	intmux.PaneOptionFormat(aiPaneThreadIDOption),
	intmux.PaneOptionFormat(aiPaneSessionIDOption),
}

var aiIngestListPanesFormat = intmux.JoinFormats(intmux.FieldDelimiter, aiIngestListPanesFormats...)

var aiBellPaneFormats = []string{
	intmux.TmuxFormat("session_name"),
	intmux.TmuxFormat("window_id"),
	intmux.TmuxFormat("window_name"),
	intmux.TmuxFormat("pane_id"),
	intmux.TmuxFormat("pane_title"),
	intmux.TmuxFormat("pane_current_command"),
	intmux.TmuxFormat("socket_path"),
}

var aiBellPaneFormat = intmux.JoinFormats(intmux.FieldDelimiter, aiBellPaneFormats...)

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
	case "antigravity-hook":
		if len(args) != 1 {
			printAIUsage(stderr)
			return errors.New("ai ingest antigravity-hook reads JSON from stdin and accepts no payload arguments")
		}
		reader := c.stdin
		if reader == nil {
			reader = os.Stdin
		}
		data, err := io.ReadAll(io.LimitReader(reader, 1024*1024+1))
		if err != nil {
			return fmt.Errorf("read antigravity hook payload: %w", err)
		}
		if len(data) > 1024*1024 {
			return errors.New("antigravity hook payload exceeds 1 MiB")
		}
		return c.ingestAntigravityHook(data)
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
		notify.MetaAgent: "bell",
		notify.MetaEvent: "bell",
		"pane":           info.Pane,
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
	c.publishNotifyQueueRefreshBestEffort()
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
	fields, err := c.muxRunner().DisplayPaneFields(context.Background(), paneID, aiBellPaneFormats...)
	if err != nil || len(fields) < 7 {
		return bellPaneInfo{}, false
	}
	info := bellPaneInfo{
		Session: fields[0],
		Window:  fields[1],
		WinName: fields[2],
		Pane:    fields[3],
		Title:   fields[4],
		Command: fields[5],
		Socket:  fields[6],
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
	rows, err := c.muxRunner().ListPanes(context.Background(), intmux.ListPanesOptions{
		All:     true,
		Formats: aiIngestListPanesFormats,
	})
	if err != nil {
		return nil
	}
	matches := make([]aiPaneMatchRow, 0, len(rows))
	for _, fields := range rows {
		paneID := fields[0]
		if paneID == "" {
			continue
		}
		matches = append(matches, aiPaneMatchRow{
			PaneID:    paneID,
			CWD:       fields[1],
			ThreadID:  fields[2],
			SessionID: fields[3],
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

func firstAny(raw map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := raw[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func firstNestedAny(value any, keys ...string) any {
	nested, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return firstAny(nested, keys...)
}

func firstBool(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		if value, ok := boolFromAny(raw[key]); ok {
			return value, true
		}
	}
	return false, false
}

func firstNestedBool(value any, keys ...string) (bool, bool) {
	nested, ok := value.(map[string]any)
	if !ok {
		return false, false
	}
	return firstBool(nested, keys...)
}

func mapFromAny(value any) map[string]any {
	if raw, ok := value.(map[string]any); ok {
		out := make(map[string]any, len(raw))
		maps.Copy(out, raw)
		return out
	}
	return map[string]any{}
}

func boolFromAny(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}

func boolMetadataValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
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
