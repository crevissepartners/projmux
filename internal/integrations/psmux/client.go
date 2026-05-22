package psmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

const (
	defaultSocket = "projmux"
	fieldSep      = "\x1f"
	escapedSep    = "\\037"
)

var (
	errSessionNameRequired     = errors.New("psmux session name is required")
	errSessionCWDRequired      = errors.New("psmux session cwd is required")
	errSessionActivityInvalid  = errors.New("psmux session activity is invalid")
	errSessionAttachedInvalid  = errors.New("psmux session attached flag is invalid")
	errSessionEphemeralInvalid = errors.New("psmux session ephemeral flag is invalid")
	errWindowIndexInvalid      = errors.New("psmux window index is invalid")
	errWindowPaneCountInvalid  = errors.New("psmux window pane count is invalid")
	errPaneIndexInvalid        = errors.New("psmux pane index is invalid")
	errActiveFlagInvalid       = errors.New("psmux active flag is invalid")
	errWindowIndexRequired     = errors.New("psmux window index is required when pane index is set")
)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner shells out to psmux. Attach and switch commands inherit stdio so
// they can take over the current terminal like the tmux backend does.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "psmux" && psmuxInteractiveCommand(args) {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return nil, nil
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
		}
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output, nil
}

func psmuxInteractiveCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-L", "-S", "-f":
			i += 2
		default:
			goto command
		}
	}
command:
	if i >= len(args) {
		return false
	}
	return args[i] == "attach-session" || args[i] == "switch-client"
}

// Client exposes the psmux subset used by the Phase 4A app/session foundation.
// Pane-scoped custom options, sidecar metadata, session restore, and hook
// parity are intentionally absent.
type Client struct {
	runner    commandRunner
	socket    string
	lookupEnv func(string) string
}

type ClientOption func(*Client)

func WithSocketName(socket string) ClientOption {
	return func(c *Client) {
		c.socket = strings.TrimSpace(socket)
	}
}

func WithEnv(lookup func(string) string) ClientOption {
	return func(c *Client) {
		if lookup == nil {
			c.lookupEnv = os.Getenv
			return
		}
		c.lookupEnv = lookup
	}
}

func NewClient(runner commandRunner, opts ...ClientOption) *Client {
	if runner == nil {
		runner = ExecRunner{}
	}
	c := &Client{
		runner:    runner,
		socket:    defaultSocket,
		lookupEnv: os.Getenv,
	}
	for _, opt := range opts {
		opt(c)
	}
	if strings.TrimSpace(c.socket) == "" {
		c.socket = defaultSocket
	}
	return c
}

func (c *Client) RecentSessions(ctx context.Context) ([]string, error) {
	rows, err := c.recentSessionRows(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.name)
	}
	return names, nil
}

func (c *Client) ExistingSessions(ctx context.Context) (map[string]bool, error) {
	output, err := c.run(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if isNoServerError(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("list psmux sessions: %w", err)
	}
	return parseExistingSessionSet(output), nil
}

func (c *Client) RecentSessionSummaries(ctx context.Context) ([]inttmux.RecentSessionSummary, error) {
	rows, err := c.recentSessionRows(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	panes, err := c.ListAllPanes(ctx)
	if err != nil {
		return nil, err
	}
	bySession := summarizePanesBySession(panes)
	summaries := make([]inttmux.RecentSessionSummary, 0, len(rows))
	for _, row := range rows {
		summary := inttmux.RecentSessionSummary{
			Name:        row.name,
			Attached:    row.attached,
			WindowCount: row.windows,
			Activity:    row.activity,
		}
		if paneSummary, ok := bySession[row.name]; ok {
			summary.PaneCount = paneSummary.paneCount
			summary.Path = paneSummary.path
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (c *Client) ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error) {
	output, err := c.run(ctx, "list-sessions", "-F", "#{session_name}\t#{session_attached}\t#{session_last_attached}\t#{@projmux_ephemeral}")
	if err != nil {
		if isNoServerError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list ephemeral psmux sessions: %w", err)
	}
	return parseEphemeralSessions(output)
}

func (c *Client) ListSessionWindows(ctx context.Context, sessionName string) ([]inttmux.Window, error) {
	if strings.TrimSpace(sessionName) == "" {
		return nil, errSessionNameRequired
	}
	output, err := c.run(ctx, "list-windows", "-t", sessionName, "-F", joinFormats("#{window_index}", "#{?window_active,1,0}", "#{window_name}", "#{window_panes}", "#{pane_current_path}"))
	if err != nil {
		return nil, fmt.Errorf("list psmux windows for session %q: %w", sessionName, err)
	}
	return parseSessionWindows(output)
}

func (c *Client) ListAllPanes(ctx context.Context) ([]inttmux.Pane, error) {
	output, err := c.run(ctx, "list-panes", "-a", "-F", joinFormats(
		"#{session_name}",
		"#{pane_id}",
		"#{window_index}",
		"#{pane_index}",
		"#{?pane_active,1,0}",
		"#{pane_title}",
		"#{pane_current_command}",
		"#{pane_current_path}",
	))
	if err != nil {
		if isNoServerError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list psmux panes: %w", err)
	}
	return parseAllPanes(output)
}

func (c *Client) EnsureSession(ctx context.Context, sessionName, cwd string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}
	if strings.TrimSpace(cwd) == "" {
		return errSessionCWDRequired
	}
	exists, err := c.SessionExists(ctx, sessionName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := c.run(ctx, "new-session", "-d", "-s", sessionName, "-c", cwd); err != nil {
		return fmt.Errorf("create psmux session %q: %w", sessionName, err)
	}
	return nil
}

func (c *Client) CreateEphemeralSession(ctx context.Context, sessionName, cwd string) error {
	if err := c.EnsureSession(ctx, sessionName, cwd); err != nil {
		return err
	}
	_, _ = c.run(ctx, "set-option", "-t", sessionName, "-q", "@projmux_ephemeral", "1")
	return nil
}

func (c *Client) SessionExists(ctx context.Context, sessionName string) (bool, error) {
	if strings.TrimSpace(sessionName) == "" {
		return false, errSessionNameRequired
	}
	if _, err := c.run(ctx, "has-session", "-t", exactSessionTarget(sessionName)); err != nil {
		if isMissingSessionError(err) || isNoServerError(err) {
			return false, nil
		}
		return false, fmt.Errorf("check psmux session %q: %w", sessionName, err)
	}
	return true, nil
}

func (c *Client) OpenSession(ctx context.Context, sessionName string) error {
	return c.OpenSessionTarget(ctx, sessionName, "", "")
}

func (c *Client) OpenSessionTarget(ctx context.Context, sessionName, windowIndex, paneIndex string) error {
	sessionName = strings.TrimSpace(sessionName)
	windowIndex = strings.TrimSpace(windowIndex)
	paneIndex = strings.TrimSpace(paneIndex)
	if sessionName == "" {
		return errSessionNameRequired
	}
	if paneIndex != "" && windowIndex == "" {
		return errWindowIndexRequired
	}
	target := exactSessionTarget(sessionName)
	args := []string{"attach-session", "-t", target}
	action := "attach"
	if c.InsideSession() {
		target = sessionPaneTarget(sessionName, windowIndex, paneIndex)
		args = []string{"switch-client", "-t", target}
		action = "switch"
	} else if windowIndex != "" {
		target = sessionWindowTarget(sessionName, windowIndex)
		args = []string{"attach-session", "-t", target}
	}
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("%s psmux target %q: %w", action, target, err)
	}
	return nil
}

func (c *Client) KillSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}
	if _, err := c.run(ctx, "kill-session", "-t", exactSessionTarget(sessionName)); err != nil {
		return fmt.Errorf("kill psmux session %q: %w", sessionName, err)
	}
	return nil
}

func (c *Client) InsideSession() bool {
	if c.lookupEnv == nil {
		return false
	}
	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	wrapped := append([]string{"-L", c.socket}, args...)
	return c.runner.Run(ctx, "psmux", wrapped...)
}

func isNoServerError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "can't find server") ||
		strings.Contains(msg, "failed to connect") ||
		strings.Contains(msg, "error connecting to ")
}

func isMissingSessionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "no such session") ||
		strings.Contains(msg, "session not found")
}

type recentSessionRow struct {
	name     string
	attached bool
	windows  int
	activity int64
	order    int
}

func (c *Client) recentSessionRows(ctx context.Context) ([]recentSessionRow, error) {
	output, err := c.run(ctx, "list-sessions", "-F", joinFormats("#{session_activity}", "#{session_name}", "#{session_attached}", "#{session_windows}"))
	if err != nil {
		if isNoServerError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list recent psmux sessions: %w", err)
	}
	return parseRecentSessionRows(output)
}

func parseRecentSessionRows(output []byte) ([]recentSessionRow, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}
	lines := strings.Split(string(output), "\n")
	rows := make([]recentSessionRow, 0, len(lines))
	for index, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields := splitFields(raw, 4)
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse recent psmux sessions: malformed row %q", raw)
		}
		activity, err := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64)
		if err != nil {
			return nil, errSessionActivityInvalid
		}
		attached, err := parseAttachedFlag(fields[2])
		if err != nil {
			return nil, err
		}
		windows, err := strconv.Atoi(strings.TrimSpace(fields[3]))
		if err != nil {
			return nil, errWindowPaneCountInvalid
		}
		name := strings.TrimSpace(fields[1])
		if name == "" {
			return nil, errSessionNameRequired
		}
		rows = append(rows, recentSessionRow{name: name, attached: attached, windows: windows, activity: activity, order: index})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].activity == rows[j].activity {
			return rows[i].order < rows[j].order
		}
		return rows[i].activity > rows[j].activity
	})
	return rows, nil
}

func parseExistingSessionSet(output []byte) map[string]bool {
	sessions := map[string]bool{}
	for line := range strings.SplitSeq(string(output), "\n") {
		sessionName := strings.TrimSpace(line)
		if sessionName == "" {
			continue
		}
		sessions[sessionName] = true
	}
	return sessions
}

func parseEphemeralSessions(output []byte) ([]lifecycle.SessionInventory, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}
	lines := strings.Split(string(output), "\n")
	sessions := make([]lifecycle.SessionInventory, 0, len(lines))
	for _, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields := strings.Split(raw, "\t")
		if len(fields) == 3 {
			fields = append(fields, "")
		}
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse ephemeral psmux sessions: malformed row %q", raw)
		}
		attached, err := parseAttachedFlag(fields[1])
		if err != nil {
			return nil, err
		}
		lastAttached, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		if err != nil {
			return nil, errSessionActivityInvalid
		}
		ephemeral, err := parseOptionalBinaryFlag(fields[3], errSessionEphemeralInvalid)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(fields[0])
		if name == "" {
			return nil, errSessionNameRequired
		}
		sessions = append(sessions, lifecycle.SessionInventory{Name: name, Attached: attached, LastAttached: lastAttached, Ephemeral: ephemeral})
	}
	return sessions, nil
}

func parseSessionWindows(output []byte) ([]inttmux.Window, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}
	lines := strings.Split(string(output), "\n")
	windows := make([]inttmux.Window, 0, len(lines))
	for _, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields := splitFields(raw, 5)
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse psmux windows: malformed row %q", raw)
		}
		index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			return nil, errWindowIndexInvalid
		}
		active, err := parseActiveFlag(fields[1])
		if err != nil {
			return nil, err
		}
		paneCount, err := strconv.Atoi(strings.TrimSpace(fields[3]))
		if err != nil {
			return nil, errWindowPaneCountInvalid
		}
		windows = append(windows, inttmux.Window{
			Index:     index,
			Name:      strings.TrimSpace(fields[2]),
			PaneCount: paneCount,
			Path:      strings.TrimSpace(fields[4]),
			Active:    active,
		})
	}
	return windows, nil
}

func parseAllPanes(output []byte) ([]inttmux.Pane, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}
	lines := strings.Split(string(output), "\n")
	panes := make([]inttmux.Pane, 0, len(lines))
	for _, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields := splitFields(raw, 8)
		if len(fields) != 8 {
			return nil, fmt.Errorf("parse psmux panes: malformed row %q", raw)
		}
		windowIndex, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			return nil, errWindowIndexInvalid
		}
		paneIndex, err := strconv.Atoi(strings.TrimSpace(fields[3]))
		if err != nil {
			return nil, errPaneIndexInvalid
		}
		active, err := parseActiveFlag(fields[4])
		if err != nil {
			return nil, err
		}
		sessionName := strings.TrimSpace(fields[0])
		if sessionName == "" {
			return nil, errSessionNameRequired
		}
		panes = append(panes, inttmux.Pane{
			ID:          strings.TrimSpace(fields[1]),
			SessionName: sessionName,
			WindowIndex: windowIndex,
			PaneIndex:   paneIndex,
			Title:       strings.TrimSpace(fields[5]),
			Command:     strings.TrimSpace(fields[6]),
			Path:        strings.TrimSpace(fields[7]),
			Active:      active,
		})
	}
	return panes, nil
}

type paneSessionSummary struct {
	paneCount int
	path      string
}

func summarizePanesBySession(panes []inttmux.Pane) map[string]paneSessionSummary {
	bySession := make(map[string]paneSessionSummary, len(panes))
	for _, pane := range panes {
		name := strings.TrimSpace(pane.SessionName)
		if name == "" {
			continue
		}
		summary := bySession[name]
		summary.paneCount++
		if path := strings.TrimSpace(pane.Path); pane.Active && path != "" {
			summary.path = path
		} else if summary.path == "" && path != "" {
			summary.path = path
		}
		bySession[name] = summary
	}
	return bySession
}

func parseAttachedFlag(value string) (bool, error) {
	count, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || count < 0 {
		return false, errSessionAttachedInvalid
	}
	return count > 0, nil
}

func parseOptionalBinaryFlag(value string, invalid error) (bool, error) {
	if strings.TrimSpace(value) == "" {
		return false, nil
	}
	return parseBinaryFlag(value, invalid)
}

func parseBinaryFlag(value string, invalid error) (bool, error) {
	switch strings.TrimSpace(value) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, invalid
	}
}

func parseActiveFlag(value string) (bool, error) {
	return parseBinaryFlag(value, errActiveFlagInvalid)
}

func joinFormats(fields ...string) string {
	return strings.Join(fields, fieldSep)
}

func splitFields(raw string, expected int) []string {
	for _, sep := range []string{fieldSep, escapedSep, "\t"} {
		fields := strings.SplitN(raw, sep, expected)
		if len(fields) == expected {
			row := make([]string, len(fields))
			for i, field := range fields {
				row[i] = strings.TrimSpace(field)
			}
			return row
		}
	}
	return strings.Split(raw, "\t")
}

func exactSessionTarget(sessionName string) string {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" || strings.HasPrefix(sessionName, "=") {
		return sessionName
	}
	return "=" + sessionName
}

func sessionWindowTarget(sessionName, windowIndex string) string {
	if strings.TrimSpace(windowIndex) == "" {
		return exactSessionTarget(sessionName)
	}
	return fmt.Sprintf("%s:%s", exactSessionTarget(sessionName), strings.TrimSpace(windowIndex))
}

func sessionPaneTarget(sessionName, windowIndex, paneIndex string) string {
	target := sessionWindowTarget(sessionName, windowIndex)
	if strings.TrimSpace(paneIndex) == "" {
		return target
	}
	return fmt.Sprintf("%s.%s", target, strings.TrimSpace(paneIndex))
}
