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
)

var (
	errCurrentPanePathUnavailable = errors.New("tmux current pane path is unavailable")
	errCurrentSessionUnavailable  = errors.New("tmux current session is unavailable")
	errSessionNameRequired        = errors.New("tmux session name is required")
	errSessionCWDRequired         = errors.New("tmux session cwd is required")
	errSessionActivityInvalid     = errors.New("tmux session activity is invalid")
)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner shells out to external commands.
type ExecRunner struct{}

// Run executes a command and returns its combined output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
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
	output, err := c.runner.Run(ctx, "tmux", "list-sessions", "-F", "#{session_activity}\t#{session_name}")
	if err != nil {
		return nil, fmt.Errorf("list recent tmux sessions: %w", err)
	}

	return parseRecentSessions(output)
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

type recentSession struct {
	name     string
	activity int64
	order    int
}

func parseRecentSessions(output []byte) ([]string, error) {
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}

	lines := strings.Split(string(output), "\n")
	sessions := make([]recentSession, 0, len(lines))
	for index, rawLine := range lines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}

		fields := strings.SplitN(rawLine, "\t", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("parse recent tmux sessions: malformed row %q", rawLine)
		}

		activity, err := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse recent tmux sessions for %q: %w", strings.TrimSpace(fields[1]), errSessionActivityInvalid)
		}

		sessionName := strings.TrimSpace(fields[1])
		if sessionName == "" {
			return nil, fmt.Errorf("parse recent tmux sessions: %w", errSessionNameRequired)
		}

		sessions = append(sessions, recentSession{
			name:     sessionName,
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

	names := make([]string, 0, len(sessions))
	for _, session := range sessions {
		names = append(names, session.name)
	}

	return names, nil
}
