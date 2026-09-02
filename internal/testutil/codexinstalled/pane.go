package codexinstalled

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const capabilityTmuxSocket = "projmux-installed-capability"

// IsolatedPane is the exact tmux Pane used as carrier evidence for one
// installed capability probe. It owns its server below the fixture root and
// never addresses the caller's tmux socket.
type IsolatedPane struct {
	tmpdir      string
	socketPath  string
	paneID      string
	environment []string
	closed      bool
}

func (fixture *Fixture) StartTurnFreeAttachPane(ctx context.Context, threadID string) (*IsolatedPane, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, fmt.Errorf("turn-free attach Pane requires thread id")
	}
	tmpdir := filepath.Join(fixture.Root, "tmux")
	if err := os.MkdirAll(tmpdir, 0o700); err != nil {
		return nil, fmt.Errorf("create capability tmux root: %w", err)
	}
	environment := tmuxCapabilityEnvironment(os.Environ(), fixture.CodexHome, tmpdir)
	pane := &IsolatedPane{tmpdir: tmpdir, environment: environment}
	keeper, err := pane.run(ctx, "-L", capabilityTmuxSocket, "-f", "/dev/null", "new-session", "-d",
		"-s", "installed-capability-keeper", "-P", "-F", "#{pane_id}", "tail", "-f", "/dev/null")
	if err != nil {
		_ = os.RemoveAll(tmpdir)
		return nil, fmt.Errorf("start capability tmux keeper: %w", err)
	}
	if strings.TrimSpace(string(keeper)) == "" {
		_ = pane.Close(context.Background())
		return nil, fmt.Errorf("capability tmux keeper returned no Pane")
	}
	socket, err := pane.run(ctx, "-L", capabilityTmuxSocket, "display-message", "-p", "#{socket_path}")
	if err != nil {
		_ = pane.Close(context.Background())
		return nil, fmt.Errorf("observe capability tmux socket: %w", err)
	}
	pane.socketPath = filepath.Clean(strings.TrimSpace(string(socket)))
	if !pathWithin(pane.tmpdir, pane.socketPath) {
		_ = pane.Close(context.Background())
		return nil, fmt.Errorf("capability tmux socket escaped its fixture root")
	}
	if _, err := pane.run(ctx, "-L", capabilityTmuxSocket, "set-option", "-g", "remain-on-exit", "on"); err != nil {
		_ = pane.Close(context.Background())
		return nil, fmt.Errorf("enable capability Pane exit evidence: %w", err)
	}
	created, err := pane.run(ctx, "-L", capabilityTmuxSocket, "new-session", "-d",
		"-s", "installed-capability-attach", "-c", fixture.Workspace,
		"-P", "-F", "#{pane_id}", "codex", "resume", "--remote", "unix://", threadID)
	if err != nil {
		_ = pane.Close(context.Background())
		return nil, fmt.Errorf("start turn-free attach Pane: %w", err)
	}
	pane.paneID = strings.TrimSpace(string(created))
	if !strings.HasPrefix(pane.paneID, "%") {
		_ = pane.Close(context.Background())
		return nil, fmt.Errorf("turn-free attach returned an invalid Pane id")
	}
	return pane, nil
}

func (pane *IsolatedPane) ID() string { return pane.paneID }

// Alive observes only the exact Pane returned by new-session. pane_dead is the
// carrier liveness authority; pane order, screen contents, and send-keys are
// intentionally absent.
func (pane *IsolatedPane) Alive(ctx context.Context) (bool, error) {
	if pane == nil || pane.closed || pane.paneID == "" {
		return false, fmt.Errorf("capability Pane is not open")
	}
	output, err := pane.run(ctx, "-L", capabilityTmuxSocket, "display-message", "-p", "-t", pane.paneID,
		"#{pane_dead}\t#{pane_pid}\t#{socket_path}")
	if err != nil {
		return false, err
	}
	fields := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(fields) != 3 || filepath.Clean(fields[2]) != pane.socketPath {
		return false, fmt.Errorf("capability Pane observation is malformed")
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil || pid <= 0 {
		return false, fmt.Errorf("capability Pane has no process identity")
	}
	switch fields[0] {
	case "0":
		return true, nil
	case "1":
		return false, nil
	default:
		return false, fmt.Errorf("capability Pane returned unknown dead state")
	}
}

func (pane *IsolatedPane) Close(ctx context.Context) error {
	if pane == nil || pane.closed {
		return nil
	}
	pane.closed = true
	if pane.socketPath == "" || !pathWithin(pane.tmpdir, pane.socketPath) {
		return fmt.Errorf("refuse capability tmux cleanup without contained socket proof")
	}
	observed, err := pane.run(ctx, "-L", capabilityTmuxSocket, "display-message", "-p", "#{socket_path}")
	if err != nil {
		return fmt.Errorf("observe capability tmux socket before cleanup: %w", err)
	}
	if filepath.Clean(strings.TrimSpace(string(observed))) != pane.socketPath {
		return fmt.Errorf("capability tmux socket identity changed before cleanup")
	}
	_, killErr := pane.run(ctx, "-L", capabilityTmuxSocket, "kill-server")
	removeErr := os.RemoveAll(pane.tmpdir)
	return errors.Join(killErr, removeErr)
}

func (pane *IsolatedPane) run(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "tmux", args...) // #nosec G204 -- executable is fixed tmux; argv is internal structured arguments and never enters a shell.
	command.Env = pane.environment
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func tmuxCapabilityEnvironment(environment []string, codexHome, tmpdir string) []string {
	filtered := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "CODEX_HOME", "TMUX", "TMUX_PANE", "TMUX_TMPDIR",
			"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_TOKEN":
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "CODEX_HOME="+codexHome, "TMUX_TMPDIR="+tmpdir)
}

func pathWithin(root, path string) bool {
	root, path = filepath.Clean(root), filepath.Clean(path)
	return filepath.IsAbs(root) && filepath.IsAbs(path) && path != root && strings.HasPrefix(path, root+string(filepath.Separator))
}
