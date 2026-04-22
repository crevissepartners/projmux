package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	errCurrentPanePathUnavailable = errors.New("tmux current pane path is unavailable")
	errSessionNameRequired        = errors.New("tmux session name is required")
	errSessionCWDRequired         = errors.New("tmux session cwd is required")
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
