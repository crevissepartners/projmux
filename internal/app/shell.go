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
	"runtime"
	"strings"
	"time"

	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	"github.com/crevissepartners/projmux/internal/platformkeys"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

const (
	defaultAppSocket  = "projmux"
	defaultAppSession = "home"
)

type shellCommand struct {
	diagnostics  *diagnostics.LifecycleRecorder
	executable   func() (string, error)
	lookupEnv    func(string) string
	homeDir      func() (string, error)
	welcomeInput io.Reader
	writeFile    func(string, []byte, os.FileMode) error
	readFile     func(string) ([]byte, error)
	runCommand   func(ctx context.Context, env []string, name string, args ...string) error
	startCommand func(ctx context.Context, env []string, name string, args ...string) error
	tmuxRunner   tmuxRunner
	sessionStore func() (sessionstate.Store, error)
	update       *updateCommand
	nativePicker intpicker.Runner
	getwd        func() (string, error)
	goos         func() string
	nativeKeys   func() bool
	now          func() time.Time
}

type shellUpdateSkipState struct {
	Version   int       `json:"version"`
	TagName   string    `json:"tag_name"`
	SkippedAt time.Time `json:"skipped_at"`
}

func newShellCommand(update *updateCommand, recorders ...*diagnostics.LifecycleRecorder) *shellCommand {
	var recorder *diagnostics.LifecycleRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &shellCommand{
		diagnostics:  recorder,
		executable:   resolveExecutablePath,
		lookupEnv:    os.Getenv,
		homeDir:      os.UserHomeDir,
		welcomeInput: os.Stdin,
		writeFile:    os.WriteFile,
		readFile:     os.ReadFile,
		runCommand:   runForegroundCommand,
		startCommand: startBackgroundCommand,
		tmuxRunner:   shellTmuxExecRunner{},
		update:       update,
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		getwd:        os.Getwd,
		goos:         func() string { return runtime.GOOS },
		nativeKeys:   platformkeys.Available,
		now:          time.Now,
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
	sessionExplicit := flagSetExplicitly(fs, "session")

	socketName := nonEmpty(strings.TrimSpace(*socket), defaultAppSocket)
	if c.insideAppSocket(socketName) {
		return fmt.Errorf("projmux shell cannot run inside the %q projmux tmux server", socketName)
	}

	if _, err := c.promptWelcome(stdout, stderr); err != nil {
		return err
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
	target, err := c.resolveShellTarget(*session, sessionExplicit)
	if err != nil {
		return err
	}
	command := "tmux"
	runArgs := []string{"-L", socketName, "-f", config, "new-session", "-A", "-s", target.SessionName}
	if target.CWD != "" {
		runArgs = append(runArgs, "-c", target.CWD)
	}
	if c.shouldStartNativeKeyBroker() {
		if err := c.start(context.Background(), binaryPath, "internal", "key-broker", "--socket", socketName); err != nil {
			_, _ = fmt.Fprintf(stderr, "warning: start native macOS keybindings: %v\n", err)
		}
	}
	return c.executeShellSession(context.Background(), socketName, target.SessionName, command, runArgs...)
}

func (c *shellCommand) executeShellSession(ctx context.Context, socketName, sessionName, command string, args ...string) error {
	_ = socketName
	_ = sessionName
	if c.diagnostics != nil {
		// `shell` explicitly opens the app session. tmux's atomic `new-session
		// -A` may provision internally, but preflighting that race would make
		// ownership less truthful. Keep the closed outer lifecycle as attach.
		c.diagnostics.Mark(diagnostics.OperationSessionAttach)
	}
	return c.run(ctx, command, args...)
}

type shellTarget struct {
	SessionName    string
	CWD            string
	ProjectDefault bool
}

func (c *shellCommand) resolveShellTarget(rawSession string, sessionExplicit bool) (shellTarget, error) {
	home, err := c.home()
	if err != nil {
		return shellTarget{}, fmt.Errorf("resolve shell home directory: %w", err)
	}
	home = filepath.Clean(home)

	sessionName := nonEmpty(strings.TrimSpace(rawSession), defaultAppSession)
	if sessionExplicit {
		return shellTarget{SessionName: sessionName, CWD: home}, nil
	}

	projectRoot, err := c.resolveShellProjectContext()
	if err != nil {
		return shellTarget{}, fmt.Errorf("resolve shell project context: %w", err)
	}
	if projectRoot == "" {
		return shellTarget{SessionName: sessionName, CWD: home}, nil
	}
	projectRoot = filepath.Clean(projectRoot)
	return shellTarget{
		SessionName:    coresessions.NewNamer(home).SessionName(projectRoot),
		CWD:            projectRoot,
		ProjectDefault: true,
	}, nil
}

func flagSetExplicitly(fs *flag.FlagSet, name string) bool {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			explicit = true
		}
	})
	return explicit
}

func (c *shellCommand) resolveShellProjectContext() (string, error) {
	if c.lookupEnv != nil {
		if raw := strings.TrimSpace(c.lookupEnv("PROJMUX_CWD")); raw != "" {
			return filepath.Clean(raw), nil
		}
	}
	if c.getwd == nil {
		return "", nil
	}
	wd, err := c.getwd()
	if err != nil {
		return "", err
	}
	wd = filepath.Clean(wd)
	if root := nearestProjectMarker(wd, os.TempDir()); root != "" {
		return root, nil
	}
	return "", nil
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

func shouldPromptShellUpdate(status updateStatus) bool {
	if status.UpdateState != "update_available" {
		return false
	}
	if status.CacheState != "fresh" {
		return false
	}
	return strings.TrimSpace(status.LatestVersion) != ""
}

func shellUpdateCanUpgrade(status updateStatus) bool {
	switch status.Installer.Source {
	case "npm", "go", "github-release":
		return true
	default:
		return false
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
	// Canonicalize here because this path outlives the process: it is written
	// into a tmux config file and live hooks that keep running long after an
	// npm update has deleted the retired staging directory a resolved path
	// may point into.
	return canonicalNpmBinaryPath(binaryPath), nil
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
	if err := c.writeFile(path, []byte(c.appConfigThemeSource().tmuxAppConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, c.defaultShell(), loadStatusbarDecorationSet(c.homeDir, c.lookupEnv), loadAIBadgeStyle(c.homeDir, c.lookupEnv), loadDesktopNotifyModeForTmuxConfig(c.homeDir, c.lookupEnv), loadLiveResourcesMode(c.homeDir, c.lookupEnv), loadStatusbarHUDVisibilitySet(c.homeDir, c.lookupEnv), keyBindings, keymapPresent)), 0o644); err != nil {
		return fmt.Errorf("write shell app config: %w", err)
	}
	return nil
}

// appConfigThemeSource resolves the global user theme for the shell-start writer,
// mirroring tmuxCommand.appConfigThemeSource: an explicit global `[theme]`
// repaints the generated tmux chrome on `projmux shell` start instead of
// clobbering a themed config with the built-in fallback. It degrades to the
// fallback when the global config cannot be read so shell start never fails on a
// missing or malformed user config. Theme is global-only, so no project path
// participates.
func (c *shellCommand) appConfigThemeSource() renderThemeSource {
	source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, "")
	if err != nil {
		return fallbackRenderThemeSource()
	}
	return source
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

func (c *shellCommand) start(ctx context.Context, name string, args ...string) error {
	if c.startCommand == nil {
		return errors.New("shell background command runner is not configured")
	}
	return c.startCommand(ctx, withoutEnv(os.Environ(), "TMUX"), name, args...)
}

func (c *shellCommand) shouldStartNativeKeyBroker() bool {
	return c.goos != nil &&
		c.goos() == "darwin" &&
		c.nativeKeys != nil &&
		c.nativeKeys() &&
		nativeKeysEnabled(c.lookupEnv, c.homeDir)
}

func runForegroundCommand(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startBackgroundCommand(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
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
