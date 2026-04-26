package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/es5h/projmux/internal/core/lifecycle"
)

var (
	errCurrentPanePathUnavailable = errors.New("tmux current pane path is unavailable")
	errCurrentSessionUnavailable  = errors.New("tmux current session is unavailable")
	errSessionNameRequired        = errors.New("tmux session name is required")
	errSessionCWDRequired         = errors.New("tmux session cwd is required")
	errPopupCommandRequired       = errors.New("tmux popup command is required")
	errPopupCloseBehaviorInvalid  = errors.New("tmux popup close behavior is invalid")
	errWindowIndexRequired        = errors.New("tmux window index is required when pane index is set")
	errSessionActivityInvalid     = errors.New("tmux session activity is invalid")
	errSessionAttachedInvalid     = errors.New("tmux session attached flag is invalid")
	errSessionEphemeralInvalid    = errors.New("tmux session ephemeral flag is invalid")
	errWindowIndexInvalid         = errors.New("tmux window index is invalid")
	errWindowPaneCountInvalid     = errors.New("tmux window pane count is invalid")
	errPaneIndexInvalid           = errors.New("tmux pane index is invalid")
	errActiveFlagInvalid          = errors.New("tmux active flag is invalid")
)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner shells out to external commands.
type ExecRunner struct{}

// Run executes a command and returns its combined output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "tmux" && len(args) > 0 && (args[0] == "attach-session" || args[0] == "switch-client") {
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

// Client exposes typed tmux queries used by CLI commands.
type Client struct {
	runner    commandRunner
	lookupEnv func(string) string
}

// Window describes a tmux window inventory row for a session.
type Window struct {
	Index     int
	Name      string
	PaneCount int
	Path      string
	Active    bool
}

// Pane describes a tmux pane inventory row.
type Pane struct {
	ID                  string
	SessionName         string
	WindowIndex         int
	PaneIndex           int
	Title               string
	AttentionState      string
	AIState             string
	AIAgent             string
	AITopic             string
	AttentionAck        string
	AttentionFocusArmed string
	Command             string
	Path                string
	Active              bool
}

// WindowPane describes a tmux pane inventory row scoped to a single window.
type WindowPane struct {
	Index  int
	Active bool
}

type PopupCloseBehavior string

const (
	PopupCloseOnExit PopupCloseBehavior = "close-on-exit"
	PopupKeepOpen    PopupCloseBehavior = "keep-open"
)

type PopupOptions struct {
	Target        string
	Cwd           string
	Env           map[string]string
	X             string
	Y             string
	Width         string
	Height        string
	Title         string
	CloseBehavior PopupCloseBehavior
}

// RecentSessionSummary describes one recent tmux session with lightweight row
// metadata for session pickers.
type RecentSessionSummary struct {
	Name        string
	Attached    bool
	WindowCount int
	PaneCount   int
	Path        string
	Activity    int64
}

// NewClient builds a tmux client over the provided runner.
func NewClient(runner commandRunner) *Client {
	return newClientWithEnv(runner, os.Getenv)
}

func newClientWithEnv(runner commandRunner, lookupEnv func(string) string) *Client {
	return &Client{
		runner:    runner,
		lookupEnv: lookupEnv,
	}
}

// CurrentPanePath returns the current tmux pane path for the active client.
func (c *Client) CurrentPanePath(ctx context.Context) (string, error) {
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pane_current_path}")
	if err != nil {
		return "", fmt.Errorf("resolve current tmux pane path: %w", err)
	}

	path := strings.TrimSpace(string(output))
	if path == "" {
		return "", errCurrentPanePathUnavailable
	}

	return path, nil
}

// CurrentSessionName returns the current tmux session name for the active client.
func (c *Client) CurrentSessionName(ctx context.Context) (string, error) {
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{session_name}")
	if err != nil {
		return "", fmt.Errorf("resolve current tmux session: %w", err)
	}

	sessionName := strings.TrimSpace(string(output))
	if sessionName == "" {
		return "", errCurrentSessionUnavailable
	}

	return sessionName, nil
}

// RecentSessions lists tmux session names ordered by most-recent activity first.
func (c *Client) RecentSessions(ctx context.Context) ([]string, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-sessions", "-F", "#{session_activity}\t#{session_name}\t#{session_attached}\t#{session_windows}")
	if err != nil {
		return nil, fmt.Errorf("list recent tmux sessions: %w", err)
	}

	return parseRecentSessions(output)
}

// RecentSessionSummaries lists tmux session rows ordered by most-recent
// activity first, enriched with attachment and pane metadata.
func (c *Client) RecentSessionSummaries(ctx context.Context) ([]RecentSessionSummary, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-sessions", "-F", "#{session_activity}\t#{session_name}\t#{session_attached}\t#{session_windows}")
	if err != nil {
		return nil, fmt.Errorf("list recent tmux sessions: %w", err)
	}

	rows, err := parseRecentSessionRows(output)
	if err != nil {
		return nil, fmt.Errorf("list recent tmux sessions: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	panes, err := c.ListAllPanes(ctx)
	if err != nil {
		return nil, err
	}

	bySession := summarizePanesBySession(panes)
	summaries := make([]RecentSessionSummary, 0, len(rows))
	for _, row := range rows {
		summary := RecentSessionSummary{
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

// ListEphemeralSessions lists tmux sessions with the lifecycle metadata needed
// for auto-attach reuse and stale-session pruning decisions.
func (c *Client) ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-sessions", "-F", "#{session_name}\t#{session_attached}\t#{session_last_attached}\t#{@projmux_ephemeral}")
	if err != nil {
		if isNoServerError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list ephemeral tmux sessions: %w", err)
	}

	sessions, err := parseEphemeralSessions(output)
	if err != nil {
		return nil, fmt.Errorf("list ephemeral tmux sessions: %w", err)
	}

	return sessions, nil
}

func isNoServerError(err error) bool {
	if err == nil {
		return false
	}

	message := err.Error()
	return strings.Contains(message, "no server running on") ||
		strings.Contains(message, "failed to connect to server") ||
		strings.Contains(message, "error connecting to ") && strings.Contains(message, "(No such file or directory)")
}

// ListSessionWindows lists the windows in a tmux session with active hints.
func (c *Client) ListSessionWindows(ctx context.Context, sessionName string) ([]Window, error) {
	if strings.TrimSpace(sessionName) == "" {
		return nil, errSessionNameRequired
	}

	output, err := c.runner.Run(ctx, "tmux", "list-windows", "-t", sessionName, "-F", "#{window_index}\t#{?window_active,1,0}\t#{window_name}\t#{window_panes}\t#{pane_current_path}")
	if err != nil {
		return nil, fmt.Errorf("list tmux windows for session %q: %w", sessionName, err)
	}

	windows, err := parseSessionWindows(output)
	if err != nil {
		return nil, fmt.Errorf("list tmux windows for session %q: %w", sessionName, err)
	}

	return windows, nil
}

// ListAllPanes lists tmux panes across all sessions with active hints.
func (c *Client) ListAllPanes(ctx context.Context) ([]Pane, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-a", "-F", "#{session_name}\t#{pane_id}\t#{window_index}\t#{pane_index}\t#{?pane_active,1,0}\t#{pane_title}\t#{@projmux_attention_state}\t#{@projmux_ai_state}\t#{@projmux_ai_agent}\t#{@projmux_ai_topic}\t#{@projmux_attention_ack}\t#{@projmux_attention_focus_armed}\t#{pane_current_command}\t#{pane_current_path}")
	if err != nil {
		return nil, fmt.Errorf("list tmux panes: %w", err)
	}

	panes, err := parseAllPanes(output)
	if err != nil {
		return nil, fmt.Errorf("list tmux panes: %w", err)
	}

	return panes, nil
}

// CapturePane returns visible text from a tmux pane starting at the requested
// history offset.
func (c *Client) CapturePane(ctx context.Context, paneTarget string, startLine int) (string, error) {
	paneTarget = strings.TrimSpace(paneTarget)
	if paneTarget == "" {
		return "", errPaneIndexInvalid
	}

	output, err := c.runner.Run(ctx, "tmux", "capture-pane", "-p", "-t", paneTarget, "-S", strconv.Itoa(startLine))
	if err != nil {
		return "", fmt.Errorf("capture tmux pane %q: %w", paneTarget, err)
	}
	return strings.TrimRight(string(output), "\r\n"), nil
}

// ListWindowPanes lists panes for a tmux session window with active hints.
func (c *Client) ListWindowPanes(ctx context.Context, sessionName string, windowIndex int) ([]WindowPane, error) {
	if strings.TrimSpace(sessionName) == "" {
		return nil, errSessionNameRequired
	}
	if windowIndex < 0 {
		return nil, errWindowIndexInvalid
	}

	target := fmt.Sprintf("%s:%d", sessionName, windowIndex)
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-t", target, "-F", "#{pane_index}\t#{?pane_active,1,0}")
	if err != nil {
		return nil, fmt.Errorf("list tmux panes for session %q window %d: %w", sessionName, windowIndex, err)
	}

	panes, err := parseWindowPanes(output)
	if err != nil {
		return nil, fmt.Errorf("list tmux panes for session %q window %d: %w", sessionName, windowIndex, err)
	}

	return panes, nil
}

// EnsureSession creates the target session when it is missing.
func (c *Client) EnsureSession(ctx context.Context, sessionName, cwd string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}
	if strings.TrimSpace(cwd) == "" {
		return errSessionCWDRequired
	}

	exists, err := c.sessionExists(ctx, sessionName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if _, err := c.runner.Run(ctx, "tmux", "new-session", "-d", "-s", sessionName, "-c", cwd); err != nil {
		return fmt.Errorf("create tmux session %q: %w", sessionName, err)
	}

	return nil
}

// CreateEphemeralSession creates a detached tmux session and marks it as a
// CreateEphemeralSession creates a projmux-managed ephemeral session.
func (c *Client) CreateEphemeralSession(ctx context.Context, sessionName, cwd string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}
	if strings.TrimSpace(cwd) == "" {
		return errSessionCWDRequired
	}

	if _, err := c.runner.Run(ctx, "tmux", "new-session", "-d", "-s", sessionName, "-c", cwd); err != nil {
		return fmt.Errorf("create tmux ephemeral session %q: %w", sessionName, err)
	}
	if _, err := c.runner.Run(ctx, "tmux", "set-option", "-t", sessionName, "-q", "@projmux_ephemeral", "1"); err != nil {
		return nil
	}

	return nil
}

// SessionExists reports whether the named tmux session already exists.
func (c *Client) SessionExists(ctx context.Context, sessionName string) (bool, error) {
	if strings.TrimSpace(sessionName) == "" {
		return false, errSessionNameRequired
	}

	return c.sessionExists(ctx, sessionName)
}

// OpenSession switches the current client when already inside tmux and attaches otherwise.
func (c *Client) OpenSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}

	command := []string{"attach-session", "-t", sessionName}
	action := "attach"
	if c.InsideSession() {
		command = []string{"switch-client", "-t", sessionName}
		action = "switch"
	}

	if _, err := c.runner.Run(ctx, "tmux", command...); err != nil {
		return fmt.Errorf("%s tmux session %q: %w", action, sessionName, err)
	}

	return nil
}

// OpenSessionTarget opens a stored preview target. Outside tmux, pane targeting
// degrades to session/window because attach-session cannot target a pane.
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

	target := sessionName
	action := "attach"
	command := []string{"attach-session", "-t", target}

	if c.InsideSession() {
		target = sessionPaneTarget(sessionName, windowIndex, paneIndex)
		action = "switch"
		command = []string{"switch-client", "-t", target}
	} else if windowIndex != "" {
		target = sessionWindowTarget(sessionName, windowIndex)
		command = []string{"attach-session", "-t", target}
	}

	if _, err := c.runner.Run(ctx, "tmux", command...); err != nil {
		return fmt.Errorf("%s tmux target %q: %w", action, target, err)
	}

	return nil
}

// SwitchClient switches the active tmux client to the target session.
func (c *Client) SwitchClient(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}

	if _, err := c.runner.Run(ctx, "tmux", "switch-client", "-t", sessionName); err != nil {
		return fmt.Errorf("switch tmux client to session %q: %w", sessionName, err)
	}

	return nil
}

// KillSession terminates the named tmux session.
func (c *Client) KillSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}

	if _, err := c.runner.Run(ctx, "tmux", "kill-session", "-t", sessionName); err != nil {
		return fmt.Errorf("kill tmux session %q: %w", sessionName, err)
	}

	return nil
}

// DisplayPopup opens a tmux popup and executes the provided shell command.
func (c *Client) DisplayPopup(ctx context.Context, command string) error {
	return c.DisplayPopupWithOptions(ctx, command, PopupOptions{})
}

// DisplayPopupWithOptions opens a tmux popup and executes the provided shell command.
func (c *Client) DisplayPopupWithOptions(ctx context.Context, command string, options PopupOptions) error {
	args, err := BuildDisplayPopupArgs(command, options)
	if err != nil {
		return err
	}

	if _, err := c.runner.Run(ctx, "tmux", args...); err != nil {
		return fmt.Errorf("display tmux popup: %w", err)
	}

	return nil
}

// BuildDisplayPopupArgs maps structured popup options to tmux display-popup args.
func BuildDisplayPopupArgs(command string, options PopupOptions) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errPopupCommandRequired
	}

	resolved, err := resolvePopupOptions(options)
	if err != nil {
		return nil, err
	}

	args := []string{"display-popup"}
	if resolved.Target != "" {
		args = append(args, "-t", resolved.Target)
	}
	if resolved.CloseBehavior == PopupCloseOnExit {
		args = append(args, "-E")
	}
	if resolved.Cwd != "" {
		args = append(args, "-d", resolved.Cwd)
	}
	for _, key := range sortedEnvKeys(resolved.Env) {
		args = append(args, "-e", key+"="+resolved.Env[key])
	}
	if resolved.X != "" {
		args = append(args, "-x", resolved.X)
	}
	if resolved.Y != "" {
		args = append(args, "-y", resolved.Y)
	}
	if resolved.Width != "" {
		args = append(args, "-w", resolved.Width)
	}
	if resolved.Height != "" {
		args = append(args, "-h", resolved.Height)
	}
	if resolved.Title != "" {
		args = append(args, "-T", resolved.Title)
	}
	args = append(args, command)
	return args, nil
}

// InsideSession reports whether the caller is already running inside tmux.
func (c *Client) InsideSession() bool {
	if c.lookupEnv == nil {
		return false
	}

	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

func (c *Client) sessionExists(ctx context.Context, sessionName string) (bool, error) {
	if _, err := c.runner.Run(ctx, "tmux", "has-session", "-t", sessionName); err != nil {
		if isExitCode(err, 1) {
			return false, nil
		}
		return false, fmt.Errorf("check tmux session %q: %w", sessionName, err)
	}

	return true, nil
}

func isExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}

	return exitErr.ExitCode() == code
}

func parseEphemeralSessions(output []byte) ([]lifecycle.SessionInventory, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil, nil
	}

	lines := strings.Split(trimmed, "\n")
	sessions := make([]lifecycle.SessionInventory, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) == 3 {
			fields = append(fields, "")
		}
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse ephemeral tmux sessions: malformed row %q", line)
		}

		name := strings.TrimSpace(fields[0])
		if name == "" {
			return nil, errSessionNameRequired
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

		sessions = append(sessions, lifecycle.SessionInventory{
			Name:         name,
			Attached:     attached,
			LastAttached: lastAttached,
			Ephemeral:    ephemeral,
		})
	}

	return sessions, nil
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

func parseAttachedFlag(value string) (bool, error) {
	trimmed := strings.TrimSpace(value)
	count, err := strconv.Atoi(trimmed)
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

// BuildPopupPreviewCommand builds the shell command used inside a tmux popup
// for the existing `projmux session-popup preview <session>` flow.
func BuildPopupPreviewCommand(binaryPath, sessionName string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("popup preview binary path is required")
	}

	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", errSessionNameRequired
	}

	return buildExecCommand(binaryPath, "session-popup", "preview", sessionName), nil
}

// BuildPopupSwitchCommand builds the shell command used inside a tmux popup
// for the existing `projmux switch --ui=popup` flow.
func BuildPopupSwitchCommand(binaryPath, cwd string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("popup switch binary path is required")
	}

	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", errors.New("popup switch working directory is required")
	}

	return "cd -- " + shellQuote(cwd) + " && " + buildExecCommand(binaryPath, "switch", "--ui=popup"), nil
}

// BuildPopupSessionsCommand builds the shell command used inside a tmux popup
// for the existing `projmux sessions --ui=popup` flow.
func BuildPopupSessionsCommand(binaryPath string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("popup sessions binary path is required")
	}

	return buildExecCommand(binaryPath, "sessions", "--ui=popup"), nil
}

// BuildSessionPopupPreviewCommand builds the shell command used by fzf preview
// panes for the existing `projmux session-popup preview {2}` flow.
func BuildSessionPopupPreviewCommand(binaryPath string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("session popup preview binary path is required")
	}

	return buildExecCommand(binaryPath, "session-popup", "preview") + " {2}", nil
}

// BuildSessionPopupCycleCommand builds the shell command used by fzf bindings
// to move popup preview selection for the focused tmux session.
func BuildSessionPopupCycleCommand(binaryPath, subcommand, direction string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("session popup cycle binary path is required")
	}

	subcommand = strings.TrimSpace(subcommand)
	if subcommand == "" {
		return "", errors.New("session popup cycle subcommand is required")
	}

	direction = strings.TrimSpace(direction)
	if direction == "" {
		return "", errors.New("session popup cycle direction is required")
	}

	return buildExecCommand(binaryPath, "session-popup", subcommand) + " {2} " + shellQuote(direction), nil
}

// BuildSwitchPreviewCommand builds the shell command used by fzf preview panes
// for the existing `projmux switch preview {2}` flow.
func BuildSwitchPreviewCommand(binaryPath, ui string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("switch preview binary path is required")
	}

	ui = strings.TrimSpace(ui)
	if ui == "" {
		ui = "popup"
	}

	return buildExecCommand(binaryPath, "switch", "preview", "--ui="+ui) + " {2}", nil
}

// BuildSwitchCycleWindowCommand builds the shell command used by fzf bindings
// to move switch preview window selection for the focused candidate.
func BuildSwitchCycleWindowCommand(binaryPath, direction string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("switch cycle window binary path is required")
	}

	direction = strings.TrimSpace(direction)
	if direction == "" {
		return "", errors.New("switch cycle window direction is required")
	}

	return buildExecCommand(binaryPath, "switch", "cycle-window") + " {2} " + shellQuote(direction), nil
}

// BuildSwitchCyclePaneCommand builds the shell command used by fzf bindings to
// move switch preview pane selection for the focused candidate.
func BuildSwitchCyclePaneCommand(binaryPath, direction string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("switch cycle pane binary path is required")
	}

	direction = strings.TrimSpace(direction)
	if direction == "" {
		return "", errors.New("switch cycle pane direction is required")
	}

	return buildExecCommand(binaryPath, "switch", "cycle-pane") + " {2} " + shellQuote(direction), nil
}

// BuildSwitchSidebarFocusCommand builds the shell command used by fzf sidebar
// focus bindings to jump to an already-existing session for the focused path.
func BuildSwitchSidebarFocusCommand(binaryPath string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("switch sidebar focus binary path is required")
	}

	return buildExecCommand(binaryPath, "switch", "sidebar-focus") + " {2}", nil
}

func buildExecCommand(binaryPath string, args ...string) string {
	quoted := make([]string, 0, len(args)+2)
	quoted = append(quoted, "exec", shellQuote(binaryPath))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func sessionWindowTarget(sessionName, windowIndex string) string {
	if strings.TrimSpace(windowIndex) == "" {
		return strings.TrimSpace(sessionName)
	}

	return fmt.Sprintf("%s:%s", strings.TrimSpace(sessionName), strings.TrimSpace(windowIndex))
}

func sessionPaneTarget(sessionName, windowIndex, paneIndex string) string {
	target := sessionWindowTarget(sessionName, windowIndex)
	if strings.TrimSpace(paneIndex) == "" {
		return target
	}

	return fmt.Sprintf("%s.%s", target, strings.TrimSpace(paneIndex))
}

func resolvePopupOptions(options PopupOptions) (PopupOptions, error) {
	resolved := PopupOptions{
		Target:        strings.TrimSpace(options.Target),
		Cwd:           strings.TrimSpace(options.Cwd),
		Env:           cleanPopupEnv(options.Env),
		X:             strings.TrimSpace(options.X),
		Y:             strings.TrimSpace(options.Y),
		Width:         strings.TrimSpace(options.Width),
		Height:        strings.TrimSpace(options.Height),
		Title:         strings.TrimSpace(options.Title),
		CloseBehavior: options.CloseBehavior,
	}

	if resolved.Width == "" {
		resolved.Width = "80%"
	}
	if resolved.Height == "" {
		resolved.Height = "80%"
	}
	if resolved.CloseBehavior == "" {
		resolved.CloseBehavior = PopupCloseOnExit
	}

	switch resolved.CloseBehavior {
	case PopupCloseOnExit, PopupKeepOpen:
		return resolved, nil
	default:
		return PopupOptions{}, errPopupCloseBehaviorInvalid
	}
}

func cleanPopupEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	cleaned := make(map[string]string, len(env))
	for key, value := range env {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		cleaned[key] = value
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type recentSession struct {
	name     string
	attached bool
	windows  int
	activity int64
	order    int
}

func parseRecentSessionRows(output []byte) ([]recentSession, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	lines := strings.Split(string(output), "\n")
	sessions := make([]recentSession, 0, len(lines))
	for index, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}

		fields := strings.SplitN(rawLine, "\t", 4)
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse recent tmux sessions: malformed row %q", rawLine)
		}

		activity, err := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse recent tmux sessions for %q: %w", strings.TrimSpace(fields[1]), errSessionActivityInvalid)
		}
		attached, err := parseBinaryFlag(fields[2], errSessionAttachedInvalid)
		if err != nil {
			return nil, fmt.Errorf("parse recent tmux sessions for %q: %w", strings.TrimSpace(fields[1]), err)
		}
		windows, err := strconv.Atoi(strings.TrimSpace(fields[3]))
		if err != nil {
			return nil, errWindowPaneCountInvalid
		}

		sessionName := strings.TrimSpace(fields[1])
		if sessionName == "" {
			return nil, fmt.Errorf("parse recent tmux sessions: %w", errSessionNameRequired)
		}

		sessions = append(sessions, recentSession{
			name:     sessionName,
			attached: attached,
			windows:  windows,
			activity: activity,
			order:    index,
		})
	}

	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].activity == sessions[j].activity {
			return sessions[i].order < sessions[j].order
		}
		return sessions[i].activity > sessions[j].activity
	})

	return sessions, nil
}

func parseRecentSessions(output []byte) ([]string, error) {
	rows, err := parseRecentSessionRows(output)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(rows))
	for _, session := range rows {
		names = append(names, session.name)
	}

	return names, nil
}

type paneSessionSummary struct {
	paneCount int
	path      string
}

func summarizePanesBySession(panes []Pane) map[string]paneSessionSummary {
	bySession := make(map[string]paneSessionSummary, len(panes))
	for _, pane := range panes {
		name := strings.TrimSpace(pane.SessionName)
		if name == "" {
			continue
		}

		summary := bySession[name]
		summary.paneCount++

		path := strings.TrimSpace(pane.Path)
		if pane.Active && path != "" {
			summary.path = path
		} else if summary.path == "" && path != "" {
			summary.path = path
		}

		bySession[name] = summary
	}

	return bySession
}

func parseSessionWindows(output []byte) ([]Window, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	lines := strings.Split(string(output), "\n")
	windows := make([]Window, 0, len(lines))
	for _, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}

		fields := strings.Split(rawLine, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse tmux windows: malformed row %q", rawLine)
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

		windows = append(windows, Window{
			Index:     index,
			Name:      strings.TrimSpace(fields[2]),
			PaneCount: paneCount,
			Path:      strings.TrimSpace(fields[4]),
			Active:    active,
		})
	}

	return windows, nil
}

func parseAllPanes(output []byte) ([]Pane, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	lines := strings.Split(string(output), "\n")
	panes := make([]Pane, 0, len(lines))
	for _, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}

		fields := normalizeAllPaneFields(strings.Split(rawLine, "\t"))
		if len(fields) != 14 {
			return nil, fmt.Errorf("parse tmux panes: malformed row %q", rawLine)
		}

		sessionName := strings.TrimSpace(fields[0])
		if sessionName == "" {
			return nil, errSessionNameRequired
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

		panes = append(panes, Pane{
			ID:                  strings.TrimSpace(fields[1]),
			SessionName:         sessionName,
			WindowIndex:         windowIndex,
			PaneIndex:           paneIndex,
			Title:               strings.TrimSpace(fields[5]),
			AttentionState:      strings.TrimSpace(fields[6]),
			AIState:             strings.TrimSpace(fields[7]),
			AIAgent:             strings.TrimSpace(fields[8]),
			AITopic:             strings.TrimSpace(fields[9]),
			AttentionAck:        strings.TrimSpace(fields[10]),
			AttentionFocusArmed: strings.TrimSpace(fields[11]),
			Command:             strings.TrimSpace(fields[12]),
			Path:                strings.TrimSpace(fields[13]),
			Active:              active,
		})
	}

	return panes, nil
}

func normalizeAllPaneFields(fields []string) []string {
	switch len(fields) {
	case 8:
		fields = append(fields[:6], append([]string{""}, fields[6:]...)...)
		fallthrough
	case 9:
		return append(fields[:7], append([]string{"", "", "", "", ""}, fields[7:]...)...)
	default:
		return fields
	}
}

func parseWindowPanes(output []byte) ([]WindowPane, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	lines := strings.Split(string(output), "\n")
	panes := make([]WindowPane, 0, len(lines))
	for _, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}

		fields := strings.Split(rawLine, "\t")
		if len(fields) != 2 {
			return nil, fmt.Errorf("parse tmux window panes: malformed row %q", rawLine)
		}

		index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			return nil, errPaneIndexInvalid
		}
		active, err := parseActiveFlag(fields[1])
		if err != nil {
			return nil, err
		}

		panes = append(panes, WindowPane{
			Index:  index,
			Active: active,
		})
	}

	return panes, nil
}

func parseActiveFlag(raw string) (bool, error) {
	switch strings.TrimSpace(raw) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errActiveFlagInvalid
	}
}
