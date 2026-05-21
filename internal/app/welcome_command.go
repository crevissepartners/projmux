package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
	"github.com/crevissepartners/projmux/internal/version"
)

type welcomeCommand struct {
	executable func() (string, error)
	homeDir    func() (string, error)
	lookupEnv  func(string) string
	readFile   func(string) ([]byte, error)
	removeFile func(string) error
	renameFile func(string, string) error
	runner     tmuxRunner
	update     *updateCommand
	writeFile  func(string, []byte, os.FileMode) error
}

func newWelcomeCommand(update *updateCommand) *welcomeCommand {
	return &welcomeCommand{
		executable: os.Executable,
		homeDir:    os.UserHomeDir,
		lookupEnv:  os.Getenv,
		readFile:   os.ReadFile,
		removeFile: os.Remove,
		renameFile: os.Rename,
		runner:     inttmux.ExecRunner{},
		update:     update,
		writeFile:  os.WriteFile,
	}
}

func (c *welcomeCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("welcome", flag.ContinueOnError)
	fs.SetOutput(stderr)
	popup := fs.Bool("popup", false, "show pending attach welcome in a tmux popup")
	force := fs.Bool("force", false, "show popup even when no attach welcome is pending")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printWelcomeUsage(stderr)
		return errors.New("welcome does not accept positional arguments")
	}
	if *popup || *force {
		return c.runPopup(*force)
	}
	status, hasStatus := resolveWelcomeUpdateStatus(c.update)
	return writeShellWelcome(stdout, strings.TrimSpace(version.String()), status, hasStatus, false, false, welcomeWidthFromEnv(c.lookupEnv))
}

func printWelcomeUsage(w io.Writer) {
	if w == nil {
		return
	}
	_, _ = io.WriteString(w, "Usage:\n  projmux welcome [--popup [--force]]\n")
}

func (c *welcomeCommand) runPopup(force bool) error {
	if !force && welcomeAutoPopupDisabled(c.lookupEnv) {
		return nil
	}
	current := welcomeCurrentVersion()
	if !force {
		claimed, err := c.claimPendingAttachWelcome(current)
		if err != nil || !claimed {
			return nil
		}
	}

	status, hasStatus := resolveWelcomeUpdateStatus(c.update)
	var body bytes.Buffer
	if err := writeShellWelcome(&body, current, status, hasStatus, false, false, welcomeWidthFromEnv(c.lookupEnv)); err != nil {
		return nil
	}
	body.WriteString(displayOnlyPopupClosePromptLine() + "\n")

	binaryPath := ""
	if c.executable != nil {
		if resolved, err := c.executable(); err == nil {
			binaryPath = resolved
		}
	}
	payload := body.String()
	command := statusbarPopupCommand(payload, binaryPath)
	args := []string{
		"display-popup",
		"-E",
		"-B",
		"-w",
		welcomePopupWidth(payload),
		"-h",
		welcomePopupHeight(payload),
		command,
	}
	if c.runner == nil {
		return nil
	}
	_, _ = c.runner.Run(context.Background(), "tmux", args...)
	return nil
}

func (c *welcomeCommand) claimPendingAttachWelcome(current string) (bool, error) {
	path, err := c.welcomeStatePath(current)
	if err != nil {
		return false, err
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	_ = lock.Close()
	defer func() {
		if c.removeFile != nil {
			_ = c.removeFile(lockPath)
		} else {
			_ = os.Remove(lockPath)
		}
	}()

	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var state shellWelcomeState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, nil
	}
	if strings.TrimSpace(state.LastWelcomedVersion) != current || !state.PendingAttachWelcome {
		return false, nil
	}
	state.PendingAttachWelcome = false
	return true, c.writeWelcomeStateAtomic(path, state)
}

func (c *welcomeCommand) writeWelcomeStateAtomic(path string, state shellWelcomeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	writeFile := c.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	if err := writeFile(tmp, data, 0o644); err != nil {
		return err
	}
	defer func() {
		if c.removeFile != nil {
			_ = c.removeFile(tmp)
		} else {
			_ = os.Remove(tmp)
		}
	}()
	renameFile := c.renameFile
	if renameFile == nil {
		renameFile = os.Rename
	}
	return renameFile(tmp, path)
}

func (c *welcomeCommand) welcomeStatePath(current string) (string, error) {
	if c.homeDir == nil {
		return "", errors.New("welcome home directory resolver is not configured")
	}
	home, err := c.homeDir()
	if err != nil {
		return "", err
	}
	stateHome := ""
	if c.lookupEnv != nil {
		stateHome = c.lookupEnv("XDG_STATE_HOME")
	}
	return welcomeStatePath(home, stateHome, current)
}

func welcomeAutoPopupDisabled(lookupEnv func(string) string) bool {
	if lookupEnv == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(lookupEnv("PROJMUX_WELCOME"))) {
	case "off", "0", "false", "no":
		return true
	default:
		return false
	}
}

func welcomeCurrentVersion() string {
	current := strings.TrimSpace(version.String())
	if current == "" {
		return "unknown"
	}
	return current
}

func welcomePopupWidth(payload string) string {
	width := 80
	for line := range strings.SplitSeq(payload, "\n") {
		if visible := visibleWelcomeLineLen(line); visible > width {
			width = visible
		}
	}
	return strconv.Itoa(width)
}

func welcomePopupHeight(payload string) string {
	height := max(strings.Count(payload, "\n"), 1)
	return strconv.Itoa(height)
}

func visibleWelcomeLineLen(line string) int {
	return projmuxpicker.VisibleLen(line)
}
