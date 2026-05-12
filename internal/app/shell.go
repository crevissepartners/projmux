package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	defaultAppSocket  = "projmux"
	defaultAppSession = "home"

	shellUpdateApply = "update:apply"
	shellUpdateLater = "update:later"
	shellUpdateSkip  = "update:skip"
)

type shellCommand struct {
	executable         func() (string, error)
	lookupEnv          func(string) string
	homeDir            func() (string, error)
	welcomeInput       io.Reader
	writeFile          func(string, []byte, os.FileMode) error
	readFile           func(string) ([]byte, error)
	runCommand         func(ctx context.Context, env []string, name string, args ...string) error
	tmuxRunner         tmuxRunner
	sessionStore       func() (sessionstate.Store, error)
	update             *updateCommand
	updatePromptRunner intpickercompat.Runner
	nativePicker       intpicker.Runner
}

type shellUpdateSkipState struct {
	Version   int       `json:"version"`
	TagName   string    `json:"tag_name"`
	SkippedAt time.Time `json:"skipped_at"`
}

func newShellCommand(update *updateCommand) *shellCommand {
	return &shellCommand{
		executable:   os.Executable,
		lookupEnv:    os.Getenv,
		homeDir:      os.UserHomeDir,
		welcomeInput: os.Stdin,
		writeFile:    os.WriteFile,
		readFile:     os.ReadFile,
		runCommand:   runForegroundCommand,
		tmuxRunner:   shellTmuxExecRunner{},
		update:       update,
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
	}
}

func (c *shellCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", defaultAppSocket, "tmux socket name for the projmux app")
	session := fs.String("session", defaultAppSession, "tmux session name for the projmux app")
	configPath := fs.String("config", "", "tmux config path for the projmux app")
	binaryOverride := fs.String("bin", "", "projmux binary path to write into the app config")
	noInstall := fs.Bool("no-install", false, "run without writing the app tmux config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		printShellUsage(stderr)
		return errors.New("shell does not accept positional arguments")
	}

	socketName := nonEmpty(strings.TrimSpace(*socket), defaultAppSocket)
	if c.insideAppSocket(socketName) {
		return fmt.Errorf("projmux shell cannot run inside the %q projmux tmux server", socketName)
	}

	welcomeHandledUpdate, err := c.promptWelcome(stdout, stderr)
	if err != nil {
		return err
	}
	if !welcomeHandledUpdate {
		if err := c.promptForUpdate(stdout, stderr); err != nil {
			return err
		}
	}

	binaryPath, err := c.resolveBinary(*binaryOverride)
	if err != nil {
		return err
	}
	config := c.expandHome(strings.TrimSpace(*configPath))
	if config == "" {
		config = c.defaultConfigPath()
	}
	if !*noInstall {
		if err := c.writeAppConfig(config, binaryPath); err != nil {
			return err
		}
	}
	cwd, err := c.home()
	if err != nil {
		return fmt.Errorf("resolve shell home directory: %w", err)
	}
	cwd = filepath.Clean(cwd)
	runArgs := []string{"-L", socketName, "-f", config, "new-session", "-A", "-s", nonEmpty(strings.TrimSpace(*session), defaultAppSession)}
	if cwd != "" {
		runArgs = append(runArgs, "-c", cwd)
	}
	c.autorestoreSessionState(context.Background(), socketName, config, nonEmpty(strings.TrimSpace(*session), defaultAppSession), cwd, stderr)
	return c.run(context.Background(), "tmux", runArgs...)
}

func (c *shellCommand) autorestoreSessionState(ctx context.Context, socketName, configPath, sessionName, cwd string, stderr io.Writer) {
	if !sessionStateAutorestoreEnabled(c.homeDir, c.lookupEnv) {
		return
	}
	if c.tmuxRunner == nil {
		c.reportSessionStateAutorestore(stderr, "tmux runner is not configured")
		return
	}
	store, err := c.shellSessionStateStore()
	if err != nil {
		c.reportSessionStateAutorestore(stderr, fmt.Sprintf("resolve store: %v", err))
		return
	}
	snap, err := store.Load(sessionName)
	if err != nil {
		if errors.Is(err, sessionstate.ErrNotFound) {
			return
		}
		c.reportSessionStateAutorestore(stderr, err.Error())
		return
	}

	runner := shellAppTmuxRunner{runner: c.tmuxRunner, socketName: socketName, configPath: configPath}
	exists, err := tmuxSessionExists(ctx, runner, sessionName)
	if err != nil {
		c.reportSessionStateAutorestore(stderr, fmt.Sprintf("check existing session: %v", err))
		return
	}
	if exists {
		return
	}
	if _, err := sessionstate.Replay(ctx, runner, snap, sessionstate.ReplayOptions{FallbackCWD: cwd}); err != nil {
		c.reportSessionStateAutorestore(stderr, err.Error())
	}
}

func (c *shellCommand) shellSessionStateStore() (sessionstate.Store, error) {
	if c.sessionStore != nil {
		return c.sessionStore()
	}
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return sessionstate.Store{}, err
	}
	return sessionstate.NewStore(paths.SessionStateDir()), nil
}

func (c *shellCommand) reportSessionStateAutorestore(stderr io.Writer, message string) {
	if stderr == nil || strings.TrimSpace(message) == "" {
		return
	}
	_, _ = fmt.Fprintf(stderr, "projmux sessionstate autorestore: %s\n", message)
}

type shellAppTmuxRunner struct {
	runner     tmuxRunner
	socketName string
	configPath string
}

func (r shellAppTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" {
		return r.runner.Run(ctx, name, args...)
	}
	wrapped := make([]string, 0, len(args)+4)
	if strings.TrimSpace(r.socketName) != "" {
		wrapped = append(wrapped, "-L", r.socketName)
	}
	if strings.TrimSpace(r.configPath) != "" {
		wrapped = append(wrapped, "-f", r.configPath)
	}
	wrapped = append(wrapped, args...)
	return r.runner.Run(ctx, name, wrapped...)
}

func tmuxSessionExists(ctx context.Context, runner tmuxRunner, sessionName string) (bool, error) {
	if runner == nil {
		return false, errors.New("tmux runner is not configured")
	}
	_, err := runner.Run(ctx, "tmux", "has-session", "-t", sessionName)
	if err == nil {
		return true, nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "can't find session") || strings.Contains(msg, "no server running") || strings.Contains(msg, "can't find server") {
		return false, nil
	}
	return false, err
}

type shellTmuxExecRunner struct {
	env func() []string
}

func (r shellTmuxExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = withoutEnv(r.environ(), "TMUX")
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

func (r shellTmuxExecRunner) environ() []string {
	if r.env != nil {
		return r.env()
	}
	return os.Environ()
}

func (c *shellCommand) promptForUpdate(stdout, stderr io.Writer) error {
	if c.update == nil || c.nativePicker == nil {
		return nil
	}
	status, err := c.update.status()
	if err != nil || !shouldPromptShellUpdate(status) || c.updatePromptSkipped(status) {
		return nil
	}
	result, err := runPickerOptionBackend(c.lookupEnv, c.nativePicker, c.updatePromptRunner, shellUpdatePromptOptions(status))
	if err != nil {
		if stderr != nil {
			_, _ = fmt.Fprintf(stderr, "skipped update prompt: %v\n", err)
		}
		return nil
	}

	switch strings.TrimSpace(result.Value) {
	case shellUpdateApply:
		if err := c.update.Run([]string{"apply"}, stdout, stderr); err != nil {
			return fmt.Errorf("run shell update: %w", err)
		}
	case shellUpdateSkip:
		if err := c.writeUpdateSkip(status); err != nil {
			return err
		}
	case "", shellUpdateLater:
		return nil
	default:
		return fmt.Errorf("unknown shell update action: %s", result.Value)
	}
	return nil
}

func shouldPromptShellUpdate(status updateStatus) bool {
	if status.UpdateState != "update_available" {
		return false
	}
	if status.CacheState != "fresh" {
		return false
	}
	switch status.Installer.Source {
	case "npm", "go", "github-release":
		return strings.TrimSpace(status.LatestVersion) != ""
	default:
		return false
	}
}

func shellUpdatePromptOptions(status updateStatus) intpickercompat.Options {
	latest := strings.TrimSpace(status.LatestVersion)
	current := strings.TrimSpace(status.CurrentVersion)
	return intpickercompat.Options{
		UI:     "shell-update",
		Prompt: "Update > ",
		Header: fmt.Sprintf("projmux %s is available (current %s)", latest, current),
		Footer: "Enter: choose  |  Esc: continue shell",
		Entries: []intpickercompat.Entry{
			{
				Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Update Now", "run projmux update apply"),
				Value: shellUpdateApply,
			},
			{
				Label: settingsLabel(settingsGlyphBack, settingsColorBack, "Later", "continue without updating"),
				Value: shellUpdateLater,
			},
			{
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Skip This Version", latest),
				Value: shellUpdateSkip,
			},
			{
				Label: settingsLabelInfo("Installer", status.Installer.Source, status.Installer.Note),
				Value: shellUpdateLater,
			},
		},
		Bindings: settingsCloseBindings(),
	}
}

func (c *shellCommand) updatePromptSkipped(status updateStatus) bool {
	path, err := c.updateSkipPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var skip shellUpdateSkipState
	if err := json.Unmarshal(data, &skip); err != nil {
		return false
	}
	return strings.TrimSpace(skip.TagName) == strings.TrimSpace(status.LatestVersion)
}

func (c *shellCommand) writeUpdateSkip(status updateStatus) error {
	path, err := c.updateSkipPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create update skip dir: %w", err)
	}
	skip := shellUpdateSkipState{
		Version:   1,
		TagName:   strings.TrimSpace(status.LatestVersion),
		SkippedAt: c.update.clock().UTC(),
	}
	data, err := json.MarshalIndent(skip, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update skip state: %w", err)
	}
	if err := c.writeFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write update skip state: %w", err)
	}
	return nil
}

func (c *shellCommand) updateSkipPath() (string, error) {
	if c.update == nil {
		return "", errors.New("shell update prompt is not configured")
	}
	cachePath, err := c.update.cachePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cachePath), "update-skip.json"), nil
}

func (c *shellCommand) insideAppSocket(socketName string) bool {
	tmuxEnv := strings.TrimSpace(c.env("TMUX"))
	if tmuxEnv == "" {
		return false
	}
	socketPath := strings.SplitN(tmuxEnv, ",", 2)[0]
	return filepath.Base(socketPath) == socketName
}

func (c *shellCommand) resolveBinary(override string) (string, error) {
	if binaryPath := strings.TrimSpace(override); binaryPath != "" {
		return binaryPath, nil
	}
	if c.executable == nil {
		return "", errors.New("configure shell executable: executable resolver is not configured")
	}
	binaryPath, err := c.executable()
	if err != nil {
		return "", fmt.Errorf("resolve shell executable: %w", err)
	}
	return binaryPath, nil
}

func (c *shellCommand) writeAppConfig(path, binaryPath string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("shell app config path is required")
	}
	if c.writeFile == nil {
		return errors.New("configure shell app config writer: file writer is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create shell app config directory: %w", err)
	}
	keyBindings, keymapPresent, err := loadMergedKeyBindingCatalog(keymapLoader{
		homeDir:   c.homeDir,
		lookupEnv: c.lookupEnv,
		readFile:  c.readFile,
	})
	if err != nil {
		return err
	}
	if err := c.writeFile(path, []byte(tmuxAppConfigWithKeymap(binaryPath, c.defaultShell(), loadStatusbarDecoration(c.homeDir, c.lookupEnv), keyBindings, keymapPresent)), 0o644); err != nil {
		return fmt.Errorf("write shell app config: %w", err)
	}
	return nil
}

func (c *shellCommand) defaultShell() string {
	return defaultInteractiveShell(c.lookupEnv)
}

func (c *shellCommand) defaultConfigPath() string {
	configHome := strings.TrimRight(c.env("XDG_CONFIG_HOME"), string(os.PathSeparator))
	if configHome == "" {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			configHome = ".config"
		} else {
			configHome = filepath.Join(homeDir, ".config")
		}
	}
	return filepath.Join(configHome, "projmux", "tmux.conf")
}

func (c *shellCommand) expandHome(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			return path
		}
		if path == "~" {
			return homeDir
		}
		return filepath.Join(homeDir, strings.TrimPrefix(path, "~/"))
	}
	return path
}

func (c *shellCommand) home() (string, error) {
	if c.homeDir == nil {
		return "", errors.New("shell home directory resolver is not configured")
	}
	return c.homeDir()
}

func (c *shellCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

func (c *shellCommand) run(ctx context.Context, name string, args ...string) error {
	if c.runCommand == nil {
		return errors.New("shell command runner is not configured")
	}
	return c.runCommand(ctx, withoutEnv(os.Environ(), "TMUX"), name, args...)
}

func runForegroundCommand(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func withoutEnv(env []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

const fallbackInteractiveShell = "/bin/sh"

func defaultInteractiveShell(lookupEnv func(string) string) string {
	if lookupEnv == nil {
		return fallbackInteractiveShell
	}
	shell := strings.TrimSpace(lookupEnv("SHELL"))
	if shell == "" || !filepath.IsAbs(shell) || strings.ContainsAny(shell, "\x00\r\n") {
		return fallbackInteractiveShell
	}
	return shell
}

func posixCommandShell(lookupEnv func(string) string) string {
	shell := defaultInteractiveShell(lookupEnv)
	switch filepath.Base(shell) {
	case "bash", "dash", "ksh", "mksh", "sh", "zsh":
		return shell
	default:
		return fallbackInteractiveShell
	}
}

func loginShellCommand(shell string) []string {
	switch filepath.Base(shell) {
	case "bash", "ksh", "mksh", "zsh":
		return []string{shell, "-l"}
	default:
		return []string{shell}
	}
}

func printShellUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux shell [--socket <name>] [--session <name>] [--config <path>] [--bin <path>] [--no-install]")
}
