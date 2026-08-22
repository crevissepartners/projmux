package tmux

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	"github.com/crevissepartners/projmux/internal/version"
)

const SwitchTargetClientEnv = "PROJMUX_SWITCH_TARGET_CLIENT"

var (
	errCurrentPanePathUnavailable = errors.New("tmux current pane path is unavailable")
	errCurrentSessionUnavailable  = errors.New("tmux current session is unavailable")
	errSessionNameRequired        = errors.New("tmux session name is required")
	errSessionCWDRequired         = errors.New("tmux session cwd is required")
	errPopupCommandRequired       = intmux.ErrPopupCommandRequired
	errPopupCloseBehaviorInvalid  = intmux.ErrPopupCloseBehaviorInvalid
	errWindowIndexRequired        = errors.New("tmux window index is required when pane index is set")
	errSessionActivityInvalid     = errors.New("tmux session activity is invalid")
	errSessionAttachedInvalid     = errors.New("tmux session attached flag is invalid")
	errSessionEphemeralInvalid    = errors.New("tmux session ephemeral flag is invalid")
	errWindowIndexInvalid         = errors.New("tmux window index is invalid")
	errWindowPaneCountInvalid     = errors.New("tmux window pane count is invalid")
	errPaneIndexInvalid           = errors.New("tmux pane index is invalid")
	errActiveFlagInvalid          = errors.New("tmux active flag is invalid")
)

const (
	tmuxFieldSep        = "\x1f"
	tmuxEscapedFieldSep = "\\037"
)

// ProjectPathSessionOption stores the project cwd a session was created with.
// It is written once at session creation and never drifts, so AI split/resume
// can anchor to the project root even after a pane wanders via `cd`. Read back
// by the app layer's resolveSessionProjectPath.
const ProjectPathSessionOption = "@projmux_project_path"

// createOperationEnvironment is a private, session-scoped ownership marker
// supplied only by the resource create transaction. It is intentionally not a
// public PROJMUX_* hook variable.
const createOperationEnvironment = "__projmux_create_operation"

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner shells out to external commands.
type ExecRunner struct{}

// CommandFailureKind is a closed subprocess failure classification. Stderr is
// carried separately from argv so safe diagnostics never classify composed
// user-controlled error strings.
type CommandFailureKind string

const (
	CommandFailureExit       CommandFailureKind = "exit"
	CommandFailureNotFound   CommandFailureKind = "not-found"
	CommandFailurePermission CommandFailureKind = "permission"
	CommandFailureRunner     CommandFailureKind = "runner"
)

// CommandFailure is the narrow typed projection used by safe classifiers.
type CommandFailure struct {
	Kind   CommandFailureKind
	Stderr string
}

type commandFailureCarrier interface {
	CommandFailure() CommandFailure
}

type commandError struct {
	name    string
	args    []string
	cause   error
	failure CommandFailure
}

func (e *commandError) Error() string {
	base := fmt.Sprintf("%s %s: %v", e.name, strings.Join(e.args, " "), e.cause)
	if stderr := strings.TrimSpace(e.failure.Stderr); stderr != "" {
		return base + ": " + stderr
	}
	return base
}

func (e *commandError) Unwrap() error { return e.cause }

func (e *commandError) CommandFailure() CommandFailure { return e.failure }

// Run executes a command and returns its combined output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "tmux" && tmuxInteractiveHandoff(args) {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, newCommandError(name, args, nil, err)
		}
		return nil, nil
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, newCommandError(name, args, output, err)
	}
	return output, nil
}

func tmuxInteractiveHandoff(args []string) bool {
	for len(args) >= 2 && (args[0] == "-L" || args[0] == "-S" || args[0] == "-f") {
		args = args[2:]
	}
	return len(args) > 0 && (args[0] == "attach-session" || args[0] == "switch-client")
}

func newCommandError(name string, args []string, stderr []byte, cause error) error {
	kind := CommandFailureRunner
	var exitErr *exec.ExitError
	var execErr *exec.Error
	switch {
	case errors.As(cause, &exitErr):
		kind = CommandFailureExit
	case errors.As(cause, &execErr):
		kind = CommandFailureNotFound
	case errors.Is(cause, os.ErrPermission):
		kind = CommandFailurePermission
	}
	return &commandError{
		name:  name,
		args:  append([]string(nil), args...),
		cause: cause,
		failure: CommandFailure{
			Kind:   kind,
			Stderr: strings.TrimSpace(string(stderr)),
		},
	}
}

// IsNoServerFailure reports only typed tmux exit failures whose isolated
// stderr has a recognized no-server/socket signature. Plain composed errors,
// exec/permission failures, and runner errors are always generic.
func IsNoServerFailure(err error) bool {
	var carrier commandFailureCarrier
	if !errors.As(err, &carrier) {
		return false
	}
	failure := carrier.CommandFailure()
	if failure.Kind != CommandFailureExit {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(failure.Stderr))
	return strings.HasPrefix(message, "no server running on ") && len(message) > len("no server running on ") ||
		message == "failed to connect to server: connection refused" ||
		strings.HasPrefix(message, "error connecting to ") && strings.HasSuffix(message, " (no such file or directory)")
}

// postCreateRunner is the narrow post-create hook surface that the tmux
// client invokes after creating a new session. The interface keeps the
// dependency narrow and lets tests stub it without spinning up real exec.
type postCreateRunner interface {
	Run(ctx context.Context, c hooks.PostCreateContext)
}

// lifecycleHookRunner is the event-oriented hook surface the tmux client uses
// for lifecycle events beyond the legacy post-create callback.
type lifecycleHookRunner interface {
	Run(ctx context.Context, event hooks.Event, c hooks.Context) (hooks.RunResult, error)
}

type lifecycleHookInspector interface {
	HasHooks(event hooks.Event, cwd string) bool
}

type lifecycleSessionEnvProvider interface {
	ProjectSessionEnv(cwd string) map[string]string
}

type startupCommandProvider interface {
	StartupCommand(cwd string) (string, bool)
}

// Client exposes typed tmux queries used by CLI commands.
type Client struct {
	runner          commandRunner
	lookupEnv       func(string) string
	readFile        func(string) ([]byte, error)
	readCodexThread func(context.Context, string) (codexappserver.CatalogThread, error)
	postCreate      postCreateRunner
	lifecycle       lifecycleHookRunner
	socket          string
	diagnostics     *diagnostics.LifecycleRecorder
}

// WithLifecycleDiagnostics attaches the process-scoped, coalescing runtime
// recorder. The client only marks typed operations; the app command boundary
// owns the single terminal outcome.
func WithLifecycleDiagnostics(recorder *diagnostics.LifecycleRecorder) ClientOption {
	return func(c *Client) { c.diagnostics = recorder }
}

// ClientOption configures optional Client behavior.
type ClientOption func(*Client)

// WithLookupEnv supplies the environment evidence used for client handoff.
// It lets an explicit --client remain authoritative in background commands
// whose inherited TMUX variable is intentionally absent.
func WithLookupEnv(lookup func(string) string) ClientOption {
	return func(c *Client) { c.lookupEnv = lookup }
}

// WithLifecycleHookRunner attaches the generic lifecycle hook runner.
func WithLifecycleHookRunner(r *hooks.Runner) ClientOption {
	return func(c *Client) {
		if r == nil {
			c.lifecycle = nil
			return
		}
		c.lifecycle = r
	}
}

// WithSocketName records the tmux -L socket name (if any) the caller is using
// so it can be propagated to hook scripts via PROJMUX_SOCKET. The Client
// itself does not currently shell out with -L; this is metadata only.
func WithSocketName(socket string) ClientOption {
	return func(c *Client) {
		c.socket = strings.TrimSpace(socket)
	}
}

// WithFileReader replaces file reads used by the client. It is primarily for
// tests that need deterministic local file metadata without touching tmux.
func WithFileReader(readFile func(string) ([]byte, error)) ClientOption {
	return func(c *Client) {
		if readFile == nil {
			c.readFile = os.ReadFile
			return
		}
		c.readFile = readFile
	}
}

// WithCodexCatalogThreadReader replaces the exact thread/read validator used
// only when Session State has a thread candidate but no authoritative bound
// session id or persisted resume id.
func WithCodexCatalogThreadReader(read func(context.Context, string) (codexappserver.CatalogThread, error)) ClientOption {
	return func(c *Client) { c.readCodexThread = read }
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
	Label               string
	AttentionState      string
	AIState             string
	AIBadgeKind         string
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

type PopupCloseBehavior = intmux.PopupCloseBehavior

const (
	PopupCloseOnExit = intmux.PopupCloseOnExit
	PopupKeepOpen    = intmux.PopupKeepOpen
)

type PopupOptions = intmux.PopupOptions

// RecentSessionSummary describes one recent tmux session with lightweight row
// metadata for session pickers.
type RecentSessionSummary struct {
	// ID is the stable `$N` tmux session id. It is the exact handle a
	// Registry-first surface pairs this row with a resolved graph node by: a
	// session name can be renamed or reused between two reads, an id cannot.
	ID          string
	Name        string
	Attached    bool
	WindowCount int
	PaneCount   int
	Path        string
	Activity    int64
}

// NewClient builds a tmux client over the provided runner with optional
// configuration.
func NewClient(runner commandRunner, opts ...ClientOption) *Client {
	c := &Client{
		runner:    runner,
		lookupEnv: os.Getenv,
		readFile:  os.ReadFile,
		readCodexThread: func(ctx context.Context, threadID string) (codexappserver.CatalogThread, error) {
			return codexappserver.ReadDefaultCatalogThread(ctx, version.String(), threadID)
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func newClientWithEnv(runner commandRunner, lookupEnv func(string) string, opts ...ClientOption) *Client {
	c := &Client{
		runner:    runner,
		lookupEnv: lookupEnv,
		readFile:  os.ReadFile,
		readCodexThread: func(ctx context.Context, threadID string) (codexappserver.CatalogThread, error) {
			return codexappserver.ReadDefaultCatalogThread(ctx, version.String(), threadID)
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
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
	output, err := c.runner.Run(ctx, "tmux", "list-sessions", "-F", tmuxFormat("#{session_id}", "#{session_activity}", "#{session_name}", "#{session_attached}", "#{session_windows}"))
	if err != nil {
		return nil, fmt.Errorf("list recent tmux sessions: %w", err)
	}

	return parseRecentSessions(output)
}

// ExistingSessions returns the current tmux session-name inventory as a set.
func (c *Client) ExistingSessions(ctx context.Context) (map[string]bool, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if isNoServerError(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("list tmux sessions: %w", err)
	}
	return parseExistingSessionSet(output), nil
}

// RecentSessionSummaries lists tmux session rows ordered by most-recent
// activity first, enriched with attachment and pane metadata.
func (c *Client) RecentSessionSummaries(ctx context.Context) ([]RecentSessionSummary, error) {
	output, err := c.runner.Run(ctx, "tmux", "list-sessions", "-F", tmuxFormat("#{session_id}", "#{session_activity}", "#{session_name}", "#{session_attached}", "#{session_windows}"))
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
			ID:          row.id,
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
	// Existing client query semantics accept legacy runner errors. Safe apply
	// diagnostics deliberately bypass this compatibility helper and use the
	// typed IsNoServerFailure classifier instead.
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

	output, err := c.runner.Run(ctx, "tmux", "list-windows", "-t", sessionName, "-F", tmuxFormat("#{window_index}", "#{?window_active,1,0}", "#{window_name}", "#{window_panes}", "#{pane_current_path}"))
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
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-a", "-F", tmuxFormat(
		"#{session_name}",
		"#{pane_id}",
		"#{window_index}",
		"#{pane_index}",
		"#{?pane_active,1,0}",
		"#{pane_title}",
		"#{@projmux_pane_label}",
		"#{@projmux_attention_state}",
		"#{@projmux_ai_state}",
		"#{@projmux_ai_badge_kind}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_attention_ack}",
		"#{@projmux_attention_focus_armed}",
		"#{pane_current_command}",
		"#{pane_current_path}",
	))
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
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-t", target, "-F", tmuxFormat("#{pane_index}", "#{?pane_active,1,0}"))
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
	return c.EnsureSessionWithEnvironment(ctx, sessionName, cwd, nil)
}

// EnsureSessionWithEnvironment is EnsureSession with additional session
// environment installed atomically by new-session -e. Canonical create uses
// this narrow extension so a synchronous after-new-window hook can recognize
// the transaction that is already holding the metadata lock.
func (c *Client) EnsureSessionWithEnvironment(ctx context.Context, sessionName, cwd string, additionalEnv map[string]string) error {
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
	c.markLifecycle(diagnostics.OperationSessionCreate)

	if err := c.runPreCreate(ctx, sessionName, cwd, "persistent"); err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return err
	}

	sessionEnv := c.projectSessionEnv(cwd)
	if len(additionalEnv) > 0 {
		merged := make(map[string]string, len(sessionEnv)+len(additionalEnv))
		maps.Copy(merged, sessionEnv)
		maps.Copy(merged, additionalEnv)
		sessionEnv = merged
	}
	paneID, err := c.createDetachedSession(ctx, sessionName, cwd, sessionEnv)
	if err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return fmt.Errorf("create tmux session %q: %w", sessionName, err)
	}

	c.applyProjectSessionEnv(ctx, sessionName, sessionEnv)
	c.setProjectPathAnchor(ctx, sessionName, cwd)
	c.runPostCreate(ctx, sessionName, cwd, "persistent", paneID)
	c.runStartupCommand(ctx, sessionName, cwd, "persistent", paneID)
	return nil
}

// EnsureSessionWithEnvironmentResult is the atomic identity-bearing variant
// used by resource create. An inner same-name hit is returned as Created=false
// instead of being indistinguishable from the session this call created.
func (c *Client) EnsureSessionWithEnvironmentResult(ctx context.Context, sessionName, cwd string, additionalEnv map[string]string) (intmux.NewSessionResult, error) {
	return c.EnsureSessionWithEnvironmentResultAt(ctx, sessionName, cwd, cwd, additionalEnv)
}

// EnsureSessionWithEnvironmentResultAt separates the initial shell Pane cwd from
// the Project hook and routing cwd. Registry topology materialization needs this
// when a stored primary Pane starts below the Project root, while the public
// PROJMUX_CWD, the pre/post-create hook contract, and the Project path anchor
// must all stay on the canonical Project root.
func (c *Client) EnsureSessionWithEnvironmentResultAt(ctx context.Context, sessionName, runtimeCWD, projectCWD string, additionalEnv map[string]string) (intmux.NewSessionResult, error) {
	if strings.TrimSpace(sessionName) == "" {
		return intmux.NewSessionResult{}, errSessionNameRequired
	}
	if strings.TrimSpace(runtimeCWD) == "" || strings.TrimSpace(projectCWD) == "" {
		return intmux.NewSessionResult{}, errSessionCWDRequired
	}

	exists, err := c.sessionExists(ctx, sessionName)
	if err != nil {
		return intmux.NewSessionResult{}, err
	}
	if exists {
		return intmux.NewSessionResult{Created: false}, nil
	}
	operationMarker := strings.TrimSpace(additionalEnv[createOperationEnvironment])
	if operationMarker == "" {
		return intmux.NewSessionResult{}, errors.New("create tmux session with result: private operation marker is required")
	}
	c.markLifecycle(diagnostics.OperationSessionCreate)

	if err := c.runPreCreate(ctx, sessionName, projectCWD, "persistent"); err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return intmux.NewSessionResult{}, err
	}

	sessionEnv := c.projectSessionEnv(projectCWD)
	if len(additionalEnv) > 0 {
		merged := make(map[string]string, len(sessionEnv)+len(additionalEnv))
		maps.Copy(merged, sessionEnv)
		maps.Copy(merged, additionalEnv)
		sessionEnv = merged
	}
	result, err := c.createDetachedSessionResult(ctx, sessionName, runtimeCWD, sessionEnv)
	if err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return result, fmt.Errorf("create tmux session %q: %w", sessionName, err)
	}

	c.applyProjectSessionEnv(ctx, result.SessionID, sessionEnv)
	c.setProjectPathAnchor(ctx, result.SessionID, projectCWD)
	c.runPostCreate(ctx, sessionName, projectCWD, "persistent", result.PaneID)
	if err := c.verifyCreatedSessionOwnership(ctx, result, operationMarker); err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return result, err
	}
	return result, nil
}

// FinalizeSessionStartup runs startup only after the app has claimed and
// mirrored the exact Session, initial Window, and primary Pane. Both boundary
// checks use the same one-observation ownership proof as the post-create path,
// so startup never targets a tuple lost before it began and a synchronous
// startup mutation cannot escape the app transaction unnoticed.
func (c *Client) FinalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd, operationMarker string) error {
	if !result.Created {
		return nil
	}
	if err := c.verifyCreatedSessionOwnership(ctx, result, operationMarker); err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return err
	}
	c.runStartupCommand(ctx, sessionName, cwd, "persistent", result.PaneID)
	if err := c.verifyCreatedSessionOwnership(ctx, result, operationMarker); err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return err
	}
	return nil
}

// verifyCreatedSessionOwnership observes the private marker and the complete
// exact tuple in one session-scoped tmux command. A former two-command marker
// then Pane probe would permit a replacement between reads; one row cannot mix
// ownership evidence from two different runtime states.
func (c *Client) verifyCreatedSessionOwnership(ctx context.Context, result intmux.NewSessionResult, operationMarker string) error {
	format := tmuxFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}")
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-s", "-t", result.SessionID, "-F", format)
	if err != nil {
		return fmt.Errorf("verify created tmux session tuple %s/%s/%s: %w", result.SessionID, result.WindowID, result.PaneID, err)
	}
	matches := 0
	for rawLine := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		fields := splitTmuxFields(strings.TrimRight(rawLine, "\r"), 4)
		if len(fields) != 4 {
			return fmt.Errorf("verify created tmux session tuple %s/%s/%s: malformed owner row %q", result.SessionID, result.WindowID, result.PaneID, rawLine)
		}
		if fields[0] == result.SessionID && fields[1] == result.WindowID && fields[2] == result.PaneID && fields[3] == operationMarker {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("verify created tmux session tuple %s/%s/%s: exact owner matches=%d", result.SessionID, result.WindowID, result.PaneID, matches)
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
	c.markLifecycle(diagnostics.OperationSessionCreate)

	if err := c.runPreCreate(ctx, sessionName, cwd, "ephemeral"); err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return err
	}

	sessionEnv := c.projectSessionEnv(cwd)
	paneID, err := c.createDetachedSession(ctx, sessionName, cwd, sessionEnv)
	if err != nil {
		c.failLifecycle(diagnostics.OperationSessionCreate)
		return fmt.Errorf("create tmux ephemeral session %q: %w", sessionName, err)
	}
	c.applyProjectSessionEnv(ctx, sessionName, sessionEnv)
	c.setProjectPathAnchor(ctx, sessionName, cwd)
	if _, err := c.runner.Run(ctx, "tmux", "set-option", "-t", sessionName, "-q", "@projmux_ephemeral", "1"); err != nil {
		// set-option failure is intentionally swallowed; the session is still
		// usable. The post-create hook still runs so the session gets the same
		// lifecycle treatment as one whose marker stuck.
	}
	c.runPostCreate(ctx, sessionName, cwd, "ephemeral", paneID)
	c.runStartupCommand(ctx, sessionName, cwd, "ephemeral", paneID)

	return nil
}

func (c *Client) createDetachedSession(ctx context.Context, sessionName, cwd string, env map[string]string) (string, error) {
	return intmux.NewRunner(c.runner).NewSession(ctx, intmux.NewSessionOptions{
		Detached:     true,
		Session:      sessionName,
		Cwd:          cwd,
		Env:          env,
		ReturnPaneID: c.lifecycle != nil || c.postCreate != nil,
	})
}

func (c *Client) createDetachedSessionResult(ctx context.Context, sessionName, cwd string, env map[string]string) (intmux.NewSessionResult, error) {
	return intmux.NewRunner(c.runner).NewSessionWithResult(ctx, intmux.NewSessionOptions{
		Detached: true,
		Session:  sessionName,
		Cwd:      cwd,
		Env:      env,
	})
}

func (c *Client) projectSessionEnv(cwd string) map[string]string {
	if c.lifecycle == nil {
		return nil
	}
	provider, ok := c.lifecycle.(lifecycleSessionEnvProvider)
	if !ok {
		return nil
	}
	return provider.ProjectSessionEnv(cwd)
}

func (c *Client) applyProjectSessionEnv(ctx context.Context, sessionName string, env map[string]string) {
	for _, key := range sortedMapKeys(env) {
		if key == createOperationEnvironment {
			continue
		}
		_, _ = c.runner.Run(ctx, "tmux", "set-environment", "-t", sessionName, key, env[key])
	}
}

// setProjectPathAnchor records the project cwd on the freshly created session
// so AI split/resume can anchor to the project root even after a pane drifts
// via `cd`. Failures are swallowed (matching the ephemeral marker): the session
// is still usable and simply falls back to the live pane cwd when the anchor is
// missing.
func (c *Client) setProjectPathAnchor(ctx context.Context, sessionName, cwd string) {
	if strings.TrimSpace(cwd) == "" {
		return
	}
	_, _ = c.runner.Run(ctx, "tmux", "set-option", "-t", sessionName, "-q", ProjectPathSessionOption, cwd)
}

func (c *Client) runPreCreate(ctx context.Context, sessionName, cwd, kind string) error {
	if c.lifecycle == nil {
		return nil
	}
	// PaneID is intentionally empty: pre-create runs before tmux creates the
	// first pane, unlike post-create which receives its exact returned id.
	_, err := c.lifecycle.Run(ctx, hooks.EventPreCreate, hooks.Context{
		SessionName: sessionName,
		CWD:         cwd,
		Kind:        kind,
		Socket:      c.socket,
	})
	if err != nil {
		return fmt.Errorf("pre-create hook for tmux session %q: %w", sessionName, err)
	}
	return nil
}

func (c *Client) runPostCreate(ctx context.Context, sessionName, cwd, kind, paneID string) {
	context := hooks.Context{
		SessionName: sessionName,
		CWD:         cwd,
		Kind:        kind,
		Socket:      c.socket,
		PaneID:      paneID,
	}
	if c.lifecycle != nil {
		_, _ = c.lifecycle.Run(ctx, hooks.EventPostCreate, context)
		return
	}
	if c.postCreate == nil {
		return
	}
	c.postCreate.Run(ctx, hooks.PostCreateContext{
		SessionName: sessionName,
		CWD:         cwd,
		Kind:        kind,
		Socket:      c.socket,
		PaneID:      paneID,
	})
}

func (c *Client) runStartupCommand(ctx context.Context, sessionName, cwd, kind, paneID string) {
	if c.lifecycle == nil || strings.TrimSpace(paneID) == "" {
		return
	}
	provider, ok := c.lifecycle.(startupCommandProvider)
	if !ok {
		return
	}
	command, ok := provider.StartupCommand(cwd)
	command = strings.TrimSpace(command)
	if !ok || command == "" {
		return
	}
	if err := c.waitForPaneShellReady(ctx, paneID, 2*time.Second, 50*time.Millisecond); err != nil {
		return
	}
	if _, err := c.runner.Run(ctx, "tmux", "send-keys", "-t", paneID, command, "Enter"); err != nil {
		return
	}
	c.markStartupPane(ctx, paneID, command)
}

func (c *Client) markStartupPane(ctx context.Context, paneID, command string) {
	_, _ = c.runner.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, "@projmux_recipe_kind", "startup")
	_, _ = c.runner.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, "@projmux_startup_command", command)
}

func (c *Client) waitForPaneShellReady(ctx context.Context, paneID string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", "#{pane_current_command}")
		if err != nil {
			return err
		}
		if c.isShellCommand(strings.TrimSpace(string(output))) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pane %s shell did not become ready before %s", paneID, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (c *Client) isShellCommand(command string) bool {
	command = filepathBase(command)
	switch command {
	case "sh", "bash", "zsh", "fish", "nu", "xonsh", "dash", "ksh", "tcsh", "csh", "elvish":
		return true
	}
	if c.lookupEnv == nil {
		return false
	}
	return command != "" && command == filepathBase(c.lookupEnv("SHELL"))
}

func filepathBase(command string) string {
	if idx := strings.LastIndex(command, "/"); idx >= 0 {
		return command[idx+1:]
	}
	return command
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

	target := exactSessionTarget(sessionName)
	command := []string{"attach-session", "-t", target}
	action := "attach"
	inside := c.InsideSession()
	explicitClient := ""
	// An explicit client is stronger evidence than the inherited TMUX
	// environment. Sidebar continuations run in the background and may lose
	// TMUX, but they still own this exact client and must switch it as the final
	// handoff rather than trying to attach a new client.
	if c.lookupEnv != nil {
		explicitClient = strings.TrimSpace(c.lookupEnv(SwitchTargetClientEnv))
	}
	if explicitClient != "" {
		inside = true
	}
	if inside {
		command = c.switchClientCommand(target)
		action = "switch"
	}
	if inside {
		c.markLifecycle(diagnostics.OperationSessionSwitch)
	} else {
		c.markLifecycle(diagnostics.OperationSessionAttach)
	}

	if _, err := c.runner.Run(ctx, "tmux", command...); err != nil {
		if inside {
			c.failLifecycle(diagnostics.OperationSessionSwitch)
		} else {
			c.failLifecycle(diagnostics.OperationSessionAttach)
		}
		return fmt.Errorf("%s tmux session %q: %w", action, sessionName, err)
	}

	// A caller that supplies an exact client is performing a final handoff from
	// a detached continuation. No lifecycle query or hook may follow that
	// switch-client command; ordinary interactive opens retain the existing
	// post-attach contract.
	if inside && explicitClient == "" {
		c.runPostAttach(ctx, sessionName, target)
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
	inside := c.InsideSession()

	if inside {
		target = sessionPaneTarget(sessionName, windowIndex, paneIndex)
		action = "switch"
		command = c.switchClientCommand(target)
	} else if windowIndex != "" {
		target = sessionWindowTarget(sessionName, windowIndex)
		command = []string{"attach-session", "-t", target}
	}
	if inside {
		c.markLifecycle(diagnostics.OperationSessionSwitch)
	} else {
		c.markLifecycle(diagnostics.OperationSessionAttach)
	}

	if _, err := c.runner.Run(ctx, "tmux", command...); err != nil {
		if inside {
			c.failLifecycle(diagnostics.OperationSessionSwitch)
		} else {
			c.failLifecycle(diagnostics.OperationSessionAttach)
		}
		return fmt.Errorf("%s tmux target %q: %w", action, target, err)
	}

	if inside {
		c.runPostAttach(ctx, sessionName, target)
	}
	return nil
}

func (c *Client) switchClientCommand(target string) []string {
	command := []string{"switch-client"}
	if c.lookupEnv != nil {
		if client := strings.TrimSpace(c.lookupEnv(SwitchTargetClientEnv)); client != "" {
			command = append(command, "-c", client)
		}
	}
	return append(command, "-t", target)
}

func (c *Client) runPostAttach(ctx context.Context, sessionName, target string) {
	if c.lifecycle == nil {
		return
	}
	cwd := c.resolveTargetCWD(ctx, target)
	_, _ = c.lifecycle.Run(ctx, hooks.EventPostAttach, hooks.Context{
		SessionName: sessionName,
		CWD:         cwd,
		Socket:      c.socket,
	})
}

func (c *Client) resolveTargetCWD(ctx context.Context, target string) string {
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-t", target, "-F", "#{pane_current_path}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// SwitchClient switches the active tmux client to the target session.
func (c *Client) SwitchClient(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}
	c.markLifecycle(diagnostics.OperationSessionSwitch)

	if _, err := c.runner.Run(ctx, "tmux", "switch-client", "-t", exactSessionTarget(sessionName)); err != nil {
		c.failLifecycle(diagnostics.OperationSessionSwitch)
		return fmt.Errorf("switch tmux client to session %q: %w", sessionName, err)
	}

	return nil
}

// KillSession terminates the named tmux session.
func (c *Client) KillSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return errSessionNameRequired
	}
	c.markLifecycle(diagnostics.OperationSessionKill)

	if _, err := c.runner.Run(ctx, "tmux", "kill-session", "-t", exactSessionTarget(sessionName)); err != nil {
		c.failLifecycle(diagnostics.OperationSessionKill)
		return fmt.Errorf("kill tmux session %q: %w", sessionName, err)
	}

	return nil
}

func (c *Client) markLifecycle(operation diagnostics.Operation) {
	if c.diagnostics != nil {
		c.diagnostics.Mark(operation)
	}
}

func (c *Client) failLifecycle(operation diagnostics.Operation) {
	if c.diagnostics != nil {
		c.diagnostics.Fail(operation)
	}
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
	return intmux.BuildDisplayPopupArgs(command, options)
}

// InsideSession reports whether the caller is already running inside tmux.
func (c *Client) InsideSession() bool {
	if c.lookupEnv == nil {
		return false
	}

	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

func (c *Client) sessionExists(ctx context.Context, sessionName string) (bool, error) {
	if _, err := c.runner.Run(ctx, "tmux", "has-session", "-t", exactSessionTarget(sessionName)); err != nil {
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

// internalRouteToken is the hidden CLI namespace that owns machine-invoked
// plumbing. Popup payloads are generated by the running binary and consumed by
// the same binary, so they emit the canonical `internal ...` spelling. Phase 2
// removed the pre-namespace entrypoints.
const internalRouteToken = "internal"

// BuildPopupPreviewCommand builds the shell command used inside a tmux popup
// for the `projmux internal session-popup preview <session>` flow.
func BuildPopupPreviewCommand(binaryPath, sessionName string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("popup preview binary path is required")
	}

	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", errSessionNameRequired
	}

	return buildExecCommand(binaryPath, internalRouteToken, "session-popup", "preview", sessionName), nil
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
// for the canonical `projmux runtime sessions --ui=popup` flow.
func BuildPopupSessionsCommand(binaryPath string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("popup sessions binary path is required")
	}

	return buildExecCommand(binaryPath, "runtime", "sessions", "--ui=popup"), nil
}

// BuildSessionPopupPreviewCommand builds the shell command used by picker preview
// panes for the `projmux internal session-popup preview {2}` flow.
func BuildSessionPopupPreviewCommand(binaryPath string) (string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return "", errors.New("session popup preview binary path is required")
	}

	return buildExecCommand(binaryPath, internalRouteToken, "session-popup", "preview") + " {2}", nil
}

// BuildSessionPopupCycleCommand builds the shell command used by picker actions
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

	return buildExecCommand(binaryPath, internalRouteToken, "session-popup", subcommand) + " {2} " + shellQuote(direction), nil
}

// BuildSwitchPreviewCommand builds the shell command used by picker preview panes
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

// BuildSwitchCycleWindowCommand builds the shell command used by picker actions
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

// BuildSwitchCyclePaneCommand builds the shell command used by picker actions to
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

// BuildSwitchSidebarFocusCommand builds the shell command used by picker sidebar
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

func exactSessionTarget(sessionName string) string {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" || strings.HasPrefix(sessionName, "=") {
		return sessionName
	}
	return "=" + sessionName
}

func tmuxFormat(fields ...string) string {
	return strings.Join(fields, tmuxFieldSep)
}

type recentSession struct {
	id       string
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

		fields := recentSessionFields(rawLine)
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse recent tmux sessions: malformed row %q", rawLine)
		}

		activity, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse recent tmux sessions for %q: %w", strings.TrimSpace(fields[2]), errSessionActivityInvalid)
		}
		attached, err := parseAttachedFlag(fields[3])
		if err != nil {
			return nil, fmt.Errorf("parse recent tmux sessions for %q: %w", strings.TrimSpace(fields[2]), err)
		}
		windows, err := strconv.Atoi(strings.TrimSpace(fields[4]))
		if err != nil {
			return nil, errWindowPaneCountInvalid
		}

		sessionName := strings.TrimSpace(fields[2])
		if sessionName == "" {
			return nil, fmt.Errorf("parse recent tmux sessions: %w", errSessionNameRequired)
		}

		sessions = append(sessions, recentSession{
			id:       strings.TrimSpace(fields[0]),
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

func recentSessionFields(rawLine string) []string {
	if fields := splitTmuxFields(rawLine, 5); len(fields) == 5 {
		return fields
	}

	// Some tmux/script combinations render literal tab separators as
	// underscores in `list-sessions -F` output. Session names can also contain
	// underscores, so preserve the leading id and activity fields and the final
	// two fields, then rejoin the middle as the session name. The session id is
	// safe to split on because tmux renders it as `$N`, which carries no
	// underscore.
	parts := strings.Split(rawLine, "_")
	if len(parts) < 5 {
		return parts
	}
	return []string{
		parts[0],
		parts[1],
		strings.Join(parts[2:len(parts)-2], "_"),
		parts[len(parts)-2],
		parts[len(parts)-1],
	}
}

func splitTmuxFields(rawLine string, expected int) []string {
	for _, sep := range []string{tmuxFieldSep, tmuxEscapedFieldSep, "\t"} {
		fields := strings.SplitN(rawLine, sep, expected)
		if len(fields) == expected || len(fields) > 1 {
			return fields
		}
	}
	return strings.Split(rawLine, "\t")
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

		fields := splitTmuxFields(rawLine, 5)
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

		fields := normalizeAllPaneFields(splitTmuxFields(rawLine, 16))
		if len(fields) != 16 {
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
			Label:               strings.TrimSpace(fields[6]),
			AttentionState:      strings.TrimSpace(fields[7]),
			AIState:             strings.TrimSpace(fields[8]),
			AIBadgeKind:         strings.TrimSpace(fields[9]),
			AIAgent:             strings.TrimSpace(fields[10]),
			AITopic:             strings.TrimSpace(fields[11]),
			AttentionAck:        strings.TrimSpace(fields[12]),
			AttentionFocusArmed: strings.TrimSpace(fields[13]),
			Command:             strings.TrimSpace(fields[14]),
			Path:                strings.TrimSpace(fields[15]),
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
		fields = append(fields[:7], append([]string{"", "", "", "", "", ""}, fields[7:]...)...)
	case 14:
		fields = append(fields[:8], append([]string{""}, fields[8:]...)...)
	}
	if len(fields) == 15 {
		fields = append(fields[:6], append([]string{""}, fields[6:]...)...)
	}
	return fields
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

		fields := splitTmuxFields(rawLine, 2)
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
