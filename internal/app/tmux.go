package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// tmux 256-color tone tokens shared with the projmux picker chip primitives.
// Both surfaces source from the same palette so the popup tab chip strip
// stays visually congruent with the tmux window-status row.
const (
	tmuxWindowInactiveBg = projmuxpicker.TmuxWindowInactiveBg
	tmuxWindowInactiveFg = projmuxpicker.TmuxWindowInactiveFg
	tmuxWindowActiveBg   = projmuxpicker.TmuxWindowActiveBg
	tmuxWindowActiveFg   = projmuxpicker.TmuxWindowActiveFg
	tmuxWindowTitleWidth = 10
)

type tmuxPopupClient interface {
	CurrentPanePath(ctx context.Context) (string, error)
	DisplayPopupWithOptions(ctx context.Context, command string, options inttmux.PopupOptions) error
}

type tmuxRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type tmuxCommand struct {
	popup         tmuxPopupClient
	executable    func() (string, error)
	runner        tmuxRunner
	lookupEnv     func(string) string
	homeDir       func() (string, error)
	writeFile     func(string, []byte, os.FileMode) error
	readFile      func(string) ([]byte, error)
	now           func() time.Time
	sessionStore  func() (sessionstate.Store, error)
	popupOptions  func(sessionName string, ctx tmuxPopupContext) inttmux.PopupOptions
	switchPopup   func(ctx tmuxPopupContext) inttmux.PopupOptions
	sessionsPopup func(ctx tmuxPopupContext) inttmux.PopupOptions
}

func newTmuxCommand() *tmuxCommand {
	runner := inttmux.ExecRunner{}
	return &tmuxCommand{
		popup:         inttmux.NewClient(inttmux.ExecRunner{}),
		executable:    os.Executable,
		runner:        runner,
		lookupEnv:     os.Getenv,
		homeDir:       os.UserHomeDir,
		writeFile:     os.WriteFile,
		readFile:      os.ReadFile,
		now:           time.Now,
		sessionStore:  sessionstate.NewDefaultStoreFromEnv,
		popupOptions:  defaultPopupPreviewOptions,
		switchPopup:   defaultPopupSwitchOptions,
		sessionsPopup: defaultPopupSessionsOptions,
	}
}

// Run manages tmux-specific helper subcommands.
func (c *tmuxCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tmux", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printTmuxUsage(stderr)
		return errors.New("tmux requires a subcommand")
	}

	switch fs.Arg(0) {
	case "hook-trust-prompt":
		return c.runHookTrustPrompt(fs.Args()[1:], stdout, stderr)
	case "popup-preview":
		return c.runPopupPreview(fs.Args()[1:], stderr)
	case "popup-switch":
		return c.runPopupSwitch(fs.Args()[1:], stderr)
	case "popup-sessions":
		return c.runPopupSessions(fs.Args()[1:], stderr)
	case "popup-toggle":
		return c.runPopupToggle(fs.Args()[1:], stderr)
	case "rebalance-panes":
		return c.runRebalancePanes(fs.Args()[1:], stderr)
	case "rename-pane":
		return c.runRenamePane(fs.Args()[1:], stderr)
	case "print-config":
		return c.runPrintConfig(fs.Args()[1:], stdout, stderr)
	case "print-app-config":
		return c.runPrintAppConfig(fs.Args()[1:], stdout, stderr)
	case "install":
		return c.runInstall(fs.Args()[1:], stdout, stderr)
	case "install-app":
		return c.runInstallApp(fs.Args()[1:], stdout, stderr)
	case "apply":
		return c.runApply(fs.Args()[1:], stdout, stderr)
	case "autosave-session-state":
		return c.runAutosaveSessionState(fs.Args()[1:], stderr)
	case "help", "--help", "-h":
		printTmuxUsage(stdout)
		return nil
	default:
		printTmuxUsage(stderr)
		return fmt.Errorf("unknown tmux subcommand: %s", fs.Arg(0))
	}
}

const (
	sessionStateAutosaveOption          = "@projmux_sessionstate_autosave_at"
	defaultSessionStateAutosaveInterval = 60 * time.Second
)

func (c *tmuxCommand) runAutosaveSessionState(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("tmux autosave-session-state", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("quiet", false, "suppress autosave errors")
	force := fs.Bool("force", false, "bypass the autosave debounce gate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		err := errors.New("tmux autosave-session-state does not accept positional arguments")
		return c.finishAutosaveSessionState(err, *quiet, stderr)
	}
	if c.runner == nil {
		return c.finishAutosaveSessionState(errors.New("configure tmux runner: tmux runner is not configured"), *quiet, stderr)
	}
	if c.sessionStore == nil {
		return c.finishAutosaveSessionState(errors.New("configure sessionstate store: sessionstate store is not configured"), *quiet, stderr)
	}
	ctx := context.Background()
	now := c.nowTime()
	sessionName, err := c.currentTmuxSessionName(ctx)
	if err != nil {
		return c.finishAutosaveSessionState(err, *quiet, stderr)
	}
	autosave, err := c.sessionStateAutosaveEnabledForSession(ctx, sessionName)
	if err != nil {
		return c.finishAutosaveSessionState(err, *quiet, stderr)
	}
	if !autosave {
		return nil
	}
	client := inttmux.NewClient(c.runner)
	if sessionstate.SourceLabel(client.SessionStateSource(ctx, sessionName)) == sessionstate.SourceFresh {
		return nil
	}
	if !*force {
		interval := (&settingsCommand{homeDir: c.homeDir, lookupEnv: c.lookupEnv}).currentSessionStateAutosaveInterval().Duration
		ok, err := c.sessionStateAutosaveDue(ctx, sessionName, now, interval)
		if err != nil || !ok {
			return c.finishAutosaveSessionState(err, *quiet, stderr)
		}
	}

	store, err := c.sessionStore()
	if err != nil {
		return c.finishAutosaveSessionState(fmt.Errorf("resolve sessionstate store: %w", err), *quiet, stderr)
	}
	if _, err := client.SaveSessionSnapshot(ctx, store, sessionName, now); err != nil {
		return c.finishAutosaveSessionState(err, *quiet, stderr)
	}
	_, err = c.runner.Run(ctx, "tmux", "set-option", "-t", sessionName, "-q", sessionStateAutosaveOption, strconv.FormatInt(now.Unix(), 10))
	return c.finishAutosaveSessionState(err, *quiet, stderr)
}

func (c *tmuxCommand) sessionStateAutosaveEnabledForSession(ctx context.Context, sessionName string) (bool, error) {
	global := sessionStateAutosaveEnabled(c.homeDir, c.lookupEnv)
	mode, found, err := c.projectSessionStateAutosaveModeForSession(ctx, sessionName)
	if err != nil || !found {
		return global, err
	}
	switch mode {
	case config.SessionStateProjectOn:
		return true, nil
	case config.SessionStateProjectOff:
		return false, nil
	default:
		return global, nil
	}
}

func (c *tmuxCommand) projectSessionStateAutosaveModeForSession(ctx context.Context, sessionName string) (config.SessionStateProjectToggle, bool, error) {
	_ = ctx
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return config.SessionStateProjectInherit, false, nil
	}
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.SessionStateProjectInherit, false, err
	}
	path := paths.ProjectSessionStateAutosaveFile(sessionName)
	if _, err := osStat(path); err != nil {
		return config.SessionStateProjectInherit, false, nil
	}
	mode, err := config.LoadSessionStateProjectToggleFile(path)
	if err != nil {
		return config.SessionStateProjectInherit, false, err
	}
	return mode, true, nil
}

func (c *tmuxCommand) finishAutosaveSessionState(err error, quiet bool, stderr io.Writer) error {
	if err == nil {
		return nil
	}
	if quiet {
		if envValue(c.lookupEnv, "PROJMUX_SESSIONSTATE_DEBUG") != "" && stderr != nil {
			_, _ = fmt.Fprintf(stderr, "projmux sessionstate autosave: %v\n", err)
		}
		return nil
	}
	return err
}

func (c *tmuxCommand) sessionStateAutosaveDue(ctx context.Context, sessionName string, now time.Time, interval time.Duration) (bool, error) {
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-t", sessionName, "#{"+sessionStateAutosaveOption+"}")
	if err != nil {
		return false, fmt.Errorf("read sessionstate autosave gate: %w", err)
	}
	last := parsePositiveInt(strings.TrimSpace(string(output)))
	if last <= 0 {
		return true, nil
	}
	if interval <= 0 {
		interval = defaultSessionStateAutosaveInterval
	}
	return now.Sub(time.Unix(int64(last), 0)) >= interval, nil
}

func (c *tmuxCommand) currentTmuxSessionName(ctx context.Context) (string, error) {
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "#{session_name}")
	if err != nil {
		return "", fmt.Errorf("resolve current tmux session: %w", err)
	}
	sessionName := strings.TrimSpace(string(output))
	if sessionName == "" {
		return "", errors.New("current tmux session is unavailable")
	}
	return sessionName, nil
}

func (c *tmuxCommand) nowTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func (c *tmuxCommand) runRebalancePanes(args []string, stderr io.Writer) error {
	if len(args) != 0 {
		printTmuxUsage(stderr)
		return fmt.Errorf("tmux rebalance-panes accepts no arguments")
	}
	if c.runner == nil {
		return errors.New("configure tmux runner: tmux runner is not configured")
	}
	out, err := c.runner.Run(context.Background(), "tmux", "list-windows", "-a", "-F", "#{window_id}\t#{window_panes}")
	if err != nil {
		return nil
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		windowID, paneCountText, ok := strings.Cut(line, "\t")
		if !ok || strings.TrimSpace(windowID) == "" || parsePositiveInt(paneCountText) < 2 {
			continue
		}
		_, _ = c.runner.Run(context.Background(), "tmux", "select-layout", "-t", strings.TrimSpace(windowID), "-E")
	}
	return nil
}

func (c *tmuxCommand) runRenamePane(args []string, stderr io.Writer) error {
	if len(args) != 2 || strings.TrimSpace(args[0]) == "" {
		printTmuxUsage(stderr)
		return fmt.Errorf("tmux rename-pane requires <pane> <title>")
	}
	if c.runner == nil {
		return errors.New("configure tmux runner: tmux runner is not configured")
	}
	paneID := strings.TrimSpace(args[0])
	title := strings.TrimSpace(args[1])
	if _, err := c.runner.Run(context.Background(), "tmux", "select-pane", "-T", title, "-t", paneID); err != nil {
		return fmt.Errorf("rename tmux pane title: %w", err)
	}
	if _, err := c.runner.Run(context.Background(), "tmux", "set-option", "-p", "-t", paneID, aiPaneTopicOption, title); err != nil {
		return fmt.Errorf("rename tmux pane topic: %w", err)
	}
	if title == "" {
		if _, err := c.runner.Run(context.Background(), "tmux", "set-option", "-p", "-u", "-t", paneID, aiPaneTopicManualOption); err != nil {
			return fmt.Errorf("clear manual tmux pane topic flag: %w", err)
		}
		return nil
	}
	if _, err := c.runner.Run(context.Background(), "tmux", "set-option", "-p", "-t", paneID, aiPaneTopicManualOption, "1"); err != nil {
		return fmt.Errorf("mark manual tmux pane topic: %w", err)
	}
	return nil
}

func (c *tmuxCommand) runPopupPreview(args []string, stderr io.Writer) error {
	sessionName, err := parseTmuxPopupPreviewArgs(args, stderr)
	if err != nil {
		return err
	}
	if c.popup == nil {
		return errors.New("configure tmux popup client: tmux popup client is not configured")
	}
	if c.executable == nil {
		return errors.New("configure tmux popup executable: tmux popup executable resolver is not configured")
	}

	binaryPath, err := c.executable()
	if err != nil {
		return fmt.Errorf("resolve tmux popup executable: %w", err)
	}

	command, err := inttmux.BuildPopupPreviewCommand(binaryPath, sessionName)
	if err != nil {
		return fmt.Errorf("build tmux popup preview command for %q: %w", sessionName, err)
	}

	popupCtx := c.popupContext(context.Background())
	options := defaultPopupPreviewOptions(sessionName, popupCtx)
	if c.popupOptions != nil {
		options = c.popupOptions(sessionName, popupCtx)
	}

	if err := c.popup.DisplayPopupWithOptions(context.Background(), command, options); err != nil {
		return fmt.Errorf("display tmux popup preview for %q: %w", sessionName, err)
	}

	return nil
}

func parseTmuxPopupPreviewArgs(args []string, stderr io.Writer) (string, error) {
	if len(args) != 1 {
		printTmuxUsage(stderr)
		return "", fmt.Errorf("tmux popup-preview requires exactly 1 argument: <session>")
	}

	sessionName := strings.TrimSpace(args[0])
	if sessionName == "" {
		printTmuxUsage(stderr)
		return "", fmt.Errorf("tmux popup-preview requires a non-empty <session> argument")
	}

	return sessionName, nil
}

func (c *tmuxCommand) runPopupToggle(args []string, stderr io.Writer) error {
	mode, err := parseTmuxPopupToggleArgs(args, stderr)
	if err != nil {
		return err
	}
	if c.runner == nil {
		return errors.New("configure tmux runner: tmux runner is not configured")
	}
	if c.executable == nil {
		return errors.New("configure tmux popup executable: tmux popup executable resolver is not configured")
	}

	binaryPath, err := c.executable()
	if err != nil {
		return fmt.Errorf("resolve tmux popup executable: %w", err)
	}

	ctx := context.Background()
	popupCtx := c.popupContext(ctx)
	if mode.ClientKey != "" {
		popupCtx.ClientKey = sanitizePopupKey(mode.ClientKey)
		popupCtx.TargetClient = strings.TrimSpace(mode.ClientKey)
	}
	marker := popupMarkerPath(popupCtx.ClientKey, mode.Canonical)
	if _, err := os.Stat(marker); err == nil {
		targetPane := strings.TrimSpace(popupCtx.OriginPane)
		targetClient := ""
		if content, readErr := os.ReadFile(marker); readErr == nil && strings.TrimSpace(string(content)) != "" {
			targetPane = strings.TrimSpace(string(content))
		}
		if mode.Canonical == "notify-sidebar" {
			targetPane = ""
			targetClient = popupCtx.TargetClient
		}
		if err := c.closePopup(ctx, targetPane, targetClient); err != nil {
			if !isRecoverablePopupCloseError(err) {
				return err
			}
			if removeErr := os.Remove(marker); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return fmt.Errorf("remove stale tmux popup marker: %w", removeErr)
			}
		} else {
			_ = os.Remove(marker)
			return nil
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat tmux popup marker: %w", err)
	}

	command, options, err := buildPopupToggleWithPickerBackend(mode, binaryPath, marker, popupCtx, c.pickerBackend(), c.lookupEnv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(marker, []byte(popupCtx.OriginPane+"\n"), 0o644); err != nil {
		return fmt.Errorf("write tmux popup marker: %w", err)
	}
	if err := intmux.NewRunner(c.runner).DisplayPopup(ctx, command, intmux.PopupOptions(options)); err != nil {
		_ = os.Remove(marker)
		if isNoSelectionExit(err) {
			return nil
		}
		return fmt.Errorf("display tmux popup-toggle %q: %w", mode.Raw, err)
	}
	return nil
}

func parseTmuxPopupToggleArgs(args []string, stderr io.Writer) (tmuxPopupToggleMode, error) {
	fs := flag.NewFlagSet("tmux popup-toggle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	clientKey := fs.String("client", "", "tmux client key used to scope the popup marker")
	if err := fs.Parse(args); err != nil {
		return tmuxPopupToggleMode{}, err
	}
	if fs.NArg() != 1 {
		printTmuxUsage(stderr)
		return tmuxPopupToggleMode{}, fmt.Errorf("tmux popup-toggle requires exactly 1 argument: <mode>")
	}

	raw := strings.TrimSpace(fs.Arg(0))
	client := strings.TrimSpace(*clientKey)
	switch raw {
	case "session-popup", "sessionizer", "sessionizer-sidebar", "notify-sidebar", "ai-split-settings":
		return tmuxPopupToggleMode{Raw: raw, Canonical: raw, ClientKey: client}, nil
	case "ai-split-picker-right":
		return tmuxPopupToggleMode{Raw: raw, Canonical: "ai-split-picker", Direction: "right", ClientKey: client}, nil
	case "ai-split-picker-down":
		return tmuxPopupToggleMode{Raw: raw, Canonical: "ai-split-picker", Direction: "down", ClientKey: client}, nil
	default:
		printTmuxUsage(stderr)
		return tmuxPopupToggleMode{}, fmt.Errorf("unknown tmux popup-toggle mode: %s", raw)
	}
}

func (c *tmuxCommand) runPopupSwitch(args []string, stderr io.Writer) error {
	if len(args) != 0 {
		printTmuxUsage(stderr)
		return fmt.Errorf("tmux popup-switch accepts no arguments")
	}
	if c.popup == nil {
		return errors.New("configure tmux popup client: tmux popup client is not configured")
	}
	if c.executable == nil {
		return errors.New("configure tmux popup executable: tmux popup executable resolver is not configured")
	}

	cwd, err := c.popup.CurrentPanePath(context.Background())
	if err != nil {
		return fmt.Errorf("resolve tmux popup switch cwd: %w", err)
	}

	binaryPath, err := c.executable()
	if err != nil {
		return fmt.Errorf("resolve tmux popup executable: %w", err)
	}

	command, err := inttmux.BuildPopupSwitchCommand(binaryPath, cwd)
	if err != nil {
		return fmt.Errorf("build tmux popup switch command: %w", err)
	}

	popupCtx := c.popupContext(context.Background())
	options := defaultPopupSwitchOptions(popupCtx)
	if c.switchPopup != nil {
		options = c.switchPopup(popupCtx)
	}
	options = withHookTrustInlineEnv(options)
	options = c.applyPickerPopupBackend(options)

	if err := c.popup.DisplayPopupWithOptions(context.Background(), command, options); err != nil {
		return fmt.Errorf("display tmux popup switch: %w", err)
	}

	return nil
}

func (c *tmuxCommand) runPopupSessions(args []string, stderr io.Writer) error {
	if len(args) != 0 {
		printTmuxUsage(stderr)
		return fmt.Errorf("tmux popup-sessions accepts no arguments")
	}
	if c.popup == nil {
		return errors.New("configure tmux popup client: tmux popup client is not configured")
	}
	if c.executable == nil {
		return errors.New("configure tmux popup executable: tmux popup executable resolver is not configured")
	}

	binaryPath, err := c.executable()
	if err != nil {
		return fmt.Errorf("resolve tmux popup executable: %w", err)
	}

	command, err := inttmux.BuildPopupSessionsCommand(binaryPath)
	if err != nil {
		return fmt.Errorf("build tmux popup sessions command: %w", err)
	}

	popupCtx := c.popupContext(context.Background())
	options := defaultPopupSessionsOptions(popupCtx)
	if c.sessionsPopup != nil {
		options = c.sessionsPopup(popupCtx)
	}
	options = c.applyPickerPopupBackend(options)

	if err := c.popup.DisplayPopupWithOptions(context.Background(), command, options); err != nil {
		return fmt.Errorf("display tmux popup sessions: %w", err)
	}

	return nil
}

func (c *tmuxCommand) runPrintConfig(args []string, stdout, stderr io.Writer) error {
	binaryPath, err := c.parseConfigBinary(args, "tmux print-config", stderr)
	if err != nil {
		return err
	}
	keyBindings, keymapPresent, err := c.loadKeyBindingCatalog()
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, tmuxStandaloneConfigWithKeymap(binaryPath, loadStatusbarDecorationSet(c.homeDir, c.lookupEnv), keyBindings, keymapPresent))
	return err
}

func (c *tmuxCommand) runPrintAppConfig(args []string, stdout, stderr io.Writer) error {
	binaryPath, err := c.parseConfigBinary(args, "tmux print-app-config", stderr)
	if err != nil {
		return err
	}
	keyBindings, keymapPresent, err := c.loadKeyBindingCatalog()
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, tmuxAppConfigWithKeymap(binaryPath, c.defaultShell(), loadStatusbarDecorationSet(c.homeDir, c.lookupEnv), keyBindings, keymapPresent))
	return err
}

func (c *tmuxCommand) runInstall(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tmux install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	binaryOverride := fs.String("bin", "", "projmux binary path to write into the tmux snippet")
	configPath := fs.String("config", "", "tmux config file to update")
	includePath := fs.String("include", "", "standalone snippet path to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printTmuxUsage(stderr)
		return errors.New("tmux install does not accept positional arguments")
	}

	binaryPath, err := c.resolveConfigBinary(*binaryOverride)
	if err != nil {
		return err
	}
	include := c.expandHome(strings.TrimSpace(*includePath))
	if include == "" {
		include = c.expandHome("~/.config/tmux/projmux.conf")
	}
	config := c.expandHome(strings.TrimSpace(*configPath))
	if config == "" {
		config = c.expandHome("~/.tmux.conf")
	}

	if c.writeFile == nil {
		return errors.New("configure tmux install writer: file writer is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(include), 0o755); err != nil {
		return fmt.Errorf("create tmux include directory: %w", err)
	}
	keyBindings, keymapPresent, err := c.loadKeyBindingCatalog()
	if err != nil {
		return err
	}
	if err := c.writeFile(include, []byte(tmuxStandaloneConfigWithKeymap(binaryPath, loadStatusbarDecorationSet(c.homeDir, c.lookupEnv), keyBindings, keymapPresent)), 0o644); err != nil {
		return fmt.Errorf("write tmux standalone config: %w", err)
	}

	sourceLine := "source-file " + tmuxConfigQuote(include)
	if err := c.ensureConfigIncludes(config, sourceLine); err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "wrote %s\nincluded from %s\n", include, config)
	return err
}

func (c *tmuxCommand) runInstallApp(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tmux install-app", flag.ContinueOnError)
	fs.SetOutput(stderr)
	binaryOverride := fs.String("bin", "", "projmux binary path to write into the app config")
	configPath := fs.String("config", "", "app tmux config path to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printTmuxUsage(stderr)
		return errors.New("tmux install-app does not accept positional arguments")
	}

	resolved, err := c.writeAppConfig(*binaryOverride, *configPath)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %s\n", resolved)
	return err
}

// writeAppConfig resolves the binary + config target and writes the projmux
// app tmux config. Returns the resolved on-disk config path so callers can
// log it or pass it to tmux source-file.
func (c *tmuxCommand) writeAppConfig(binaryOverride, configOverride string) (string, error) {
	binaryPath, err := c.resolveConfigBinary(binaryOverride)
	if err != nil {
		return "", err
	}
	config := c.expandHome(strings.TrimSpace(configOverride))
	if config == "" {
		config = c.expandHome("~/.config/projmux/tmux.conf")
	}
	if c.writeFile == nil {
		return "", errors.New("configure tmux install-app writer: file writer is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		return "", fmt.Errorf("create tmux app config directory: %w", err)
	}
	keyBindings, keymapPresent, err := c.loadKeyBindingCatalog()
	if err != nil {
		return "", err
	}
	if err := c.writeFile(config, []byte(tmuxAppConfigWithKeymap(binaryPath, c.defaultShell(), loadStatusbarDecorationSet(c.homeDir, c.lookupEnv), keyBindings, keymapPresent)), 0o644); err != nil {
		return "", fmt.Errorf("write tmux app config: %w", err)
	}
	return config, nil
}

func (c *tmuxCommand) loadKeyBindingCatalog() ([]keyBindingAction, bool, error) {
	return loadMergedKeyBindingCatalog(keymapLoader{
		homeDir:   c.homeDir,
		lookupEnv: c.lookupEnv,
		readFile:  c.readFile,
	})
}

func (c *tmuxCommand) runApply(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tmux apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	binaryOverride := fs.String("bin", "", "projmux binary path to write into the app config")
	configPath := fs.String("config", "", "app tmux config path to write")
	socket := fs.String("socket", "projmux", "tmux -L socket name to reload")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printTmuxUsage(stderr)
		return errors.New("tmux apply does not accept positional arguments")
	}

	resolved, err := c.writeAppConfig(*binaryOverride, *configPath)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote %s\n", resolved); err != nil {
		return err
	}

	socketName := strings.TrimSpace(*socket)
	if socketName == "" {
		socketName = "projmux"
	}
	if c.runner == nil {
		_, err = fmt.Fprintf(stdout, "skipped reload: no tmux runner configured\n")
		return err
	}

	ctx := context.Background()
	// Probe the socket. tmux exits non-zero (and writes "no server running"
	// to stderr) when the socket is dead — treat that as a non-fatal skip.
	sessionsOut, listErr := c.runner.Run(ctx, "tmux", "-L", socketName, "list-sessions", "-F", "#{session_id}")
	if listErr != nil {
		_, err = fmt.Fprintf(stdout, "skipped reload: no live tmux server -L %s\n", socketName)
		return err
	}

	if _, err := c.runner.Run(ctx, "tmux", "-L", socketName, "source-file", resolved); err != nil {
		fmt.Fprintf(stderr, "tmux source-file failed on -L %s: %v\n", socketName, err)
		_, err2 := fmt.Fprintf(stdout, "skipped reload: source-file failed on -L %s\n", socketName)
		return err2
	}

	count := 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(sessionsOut)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	_, err = fmt.Fprintf(stdout, "reloaded tmux server -L %s: %d sessions\n", socketName, count)
	return err
}

func defaultPopupPreviewOptions(_ string, ctx tmuxPopupContext) inttmux.PopupOptions {
	return inttmux.PopupOptions{
		Width:         popupSize(ctx.ClientWidth, 80, 120),
		Height:        popupSize(ctx.ClientHeight, 80, 30),
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
}

func defaultPopupSwitchOptions(ctx tmuxPopupContext) inttmux.PopupOptions {
	return inttmux.PopupOptions{
		Width:         popupSize(ctx.ClientWidth, 80, 120),
		Height:        popupSize(ctx.ClientHeight, 70, 28),
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
}

func defaultPopupSessionsOptions(ctx tmuxPopupContext) inttmux.PopupOptions {
	return inttmux.PopupOptions{
		Width:         popupSize(ctx.ClientWidth, 80, 120),
		Height:        popupSize(ctx.ClientHeight, 75, 28),
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
}

func printTmuxUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux tmux popup-preview <session>")
	fmt.Fprintln(w, "  projmux tmux popup-switch")
	fmt.Fprintln(w, "  projmux tmux popup-sessions")
	fmt.Fprintln(w, "  projmux tmux popup-toggle [--client <key>] <session-popup|sessionizer|sessionizer-sidebar|notify-sidebar|ai-split-picker-right|ai-split-picker-down|ai-split-settings>")
	fmt.Fprintln(w, "  projmux tmux rebalance-panes")
	fmt.Fprintln(w, "  projmux tmux rename-pane <pane> <title>")
	fmt.Fprintln(w, "  projmux tmux print-config [--bin <path>]")
	fmt.Fprintln(w, "  projmux tmux print-app-config [--bin <path>]")
	fmt.Fprintln(w, "  projmux tmux install [--bin <path>] [--config <path>] [--include <path>]")
	fmt.Fprintln(w, "  projmux tmux install-app [--bin <path>] [--config <path>]")
	fmt.Fprintln(w, "  projmux tmux apply [--bin <path>] [--config <path>] [--socket <name>]")
}

type tmuxPopupToggleMode struct {
	Raw       string
	Canonical string
	Direction string
	ClientKey string
}

type tmuxPopupContext struct {
	ClientKey     string
	TargetClient  string
	OriginPane    string
	OriginSession string
	ContextDir    string
	ClientWidth   int
	ClientHeight  int
	Decoration    config.StatusbarDecoration
}

func (c *tmuxCommand) popupContext(ctx context.Context) tmuxPopupContext {
	clientKey := c.tmuxFormat(ctx, "#{client_tty}")
	if clientKey == "" {
		clientKey = c.tmuxFormat(ctx, "#{client_pid}")
	}
	if clientKey == "" {
		clientKey = "unknown"
	}
	rawDecoration := c.tmuxFormat(ctx, "#{"+statusbarDecorationTmuxOption+"}")
	decoration := config.NormalizeStatusbarDecoration(rawDecoration)
	if strings.TrimSpace(rawDecoration) == "" {
		decoration = loadStatusbarDecoration(c.homeDir, c.lookupEnv)
	}

	return tmuxPopupContext{
		ClientKey:     sanitizePopupKey(clientKey),
		TargetClient:  clientKey,
		OriginPane:    c.tmuxFormat(ctx, "#{pane_id}"),
		OriginSession: c.tmuxFormat(ctx, "#S"),
		ContextDir:    c.tmuxFormat(ctx, "#{pane_current_path}"),
		ClientWidth:   parseTmuxPositiveInt(c.tmuxFormat(ctx, "#{client_width}")),
		ClientHeight:  parseTmuxPositiveInt(c.tmuxFormat(ctx, "#{client_height}")),
		Decoration:    decoration,
	}
}

func (c *tmuxCommand) pickerBackend() intpicker.Backend {
	return resolvePickerBackendWithConfig(c.homeDir, c.lookupEnv)
}

func (c *tmuxCommand) applyPickerPopupBackend(options inttmux.PopupOptions) inttmux.PopupOptions {
	env := options.Env
	if env == nil {
		env = map[string]string{}
	} else {
		env = maps.Clone(env)
	}
	inheritPopupPickerEnv(env, c.lookupEnv)
	if c.pickerBackend() == intpicker.BackendNative {
		options.NoBorder = true
		env[intpicker.BackendEnv] = string(intpicker.BackendNative)
	}
	if len(env) == 0 {
		options.Env = nil
	} else {
		options.Env = env
	}
	return options
}

func (c *tmuxCommand) tmuxFormat(ctx context.Context, format string) string {
	if c.runner == nil {
		return ""
	}
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-F", format)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (c *tmuxCommand) closePopup(ctx context.Context, targetPane, targetClient string) error {
	if c.runner == nil {
		return errors.New("configure tmux runner: tmux runner is not configured")
	}
	if err := intmux.NewRunner(c.runner).ClosePopup(ctx, intmux.ClosePopupOptions{
		Client: targetClient,
		Target: targetPane,
	}); err != nil {
		return fmt.Errorf("close tmux popup: %w", err)
	}
	return nil
}

func isRecoverablePopupCloseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "display-popup") {
		return false
	}
	if strings.Contains(msg, "exit status 1") {
		return true
	}
	for _, fragment := range []string{
		"can't find pane",
		"can't find client",
		"can't find session",
		"can't find window",
		"target not found",
	} {
		if strings.Contains(msg, fragment) {
			return true
		}
	}
	return false
}

func buildPopupToggle(mode tmuxPopupToggleMode, binaryPath, marker string, ctx tmuxPopupContext) (string, inttmux.PopupOptions, error) {
	return buildPopupToggleWithPickerBackend(mode, binaryPath, marker, ctx, resolvePickerBackend(os.Getenv), os.Getenv)
}

func addHookTrustPopupTargetEnv(env map[string]string, ctx tmuxPopupContext) {
	if strings.TrimSpace(ctx.TargetClient) != "" {
		env[hookTrustPopupTargetClientEnv] = ctx.TargetClient
	}
}

func addHookTrustInlineEnv(env map[string]string) {
	env[hookTrustInlineEnv] = "1"
}

func withHookTrustInlineEnv(options inttmux.PopupOptions) inttmux.PopupOptions {
	env := options.Env
	if env == nil {
		env = map[string]string{}
	} else {
		env = maps.Clone(env)
	}
	addHookTrustInlineEnv(env)
	options.Env = env
	return options
}

func addSwitchTargetClientEnv(env map[string]string, ctx tmuxPopupContext) {
	if strings.TrimSpace(ctx.TargetClient) != "" {
		env[inttmux.SwitchTargetClientEnv] = ctx.TargetClient
	}
}

func buildPopupToggleWithPickerBackend(mode tmuxPopupToggleMode, binaryPath, marker string, ctx tmuxPopupContext, backend intpicker.Backend, lookupEnv func(string) string) (string, inttmux.PopupOptions, error) {
	options := inttmux.PopupOptions{
		Target:        ctx.OriginPane,
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	commandArgs := []string{}
	env := map[string]string{}
	cwd := ""

	switch mode.Raw {
	case "session-popup":
		options.Width = popupSize(ctx.ClientWidth, 80, 120)
		options.Height = popupSize(ctx.ClientHeight, 70, 28)
		addSwitchTargetClientEnv(env, ctx)
		commandArgs = []string{"sessions", "--ui=popup"}
	case "sessionizer":
		options.Width = popupSize(ctx.ClientWidth, 80, 120)
		options.Height = popupSize(ctx.ClientHeight, 70, 28)
		cwd = ctx.ContextDir
		addHookTrustInlineEnv(env)
		addHookTrustPopupTargetEnv(env, ctx)
		addSwitchTargetClientEnv(env, ctx)
		env["TMUX_SESSIONIZER_CONTEXT_DIR"] = ctx.ContextDir
		env["TMUX_SESSIONIZER_CONTEXT_SESSION"] = ctx.OriginSession
		env["TMUX_SESSIONIZER_CONTEXT_PANE"] = ctx.OriginPane
		commandArgs = []string{"switch", "--ui=popup"}
	case "sessionizer-sidebar":
		options.Width = sessionizerSidebarWidth(ctx.ClientWidth, backend)
		options.Height = sidebarPopupHeight(ctx.ClientHeight)
		options.X = "0"
		options.Y = "0"
		cwd = ctx.ContextDir
		addHookTrustPopupTargetEnv(env, ctx)
		addSwitchTargetClientEnv(env, ctx)
		env["TMUX_SESSIONIZER_CONTEXT_DIR"] = ctx.ContextDir
		env["TMUX_SESSIONIZER_CONTEXT_SESSION"] = ctx.OriginSession
		env["TMUX_SESSIONIZER_CONTEXT_PANE"] = ctx.OriginPane
		commandArgs = []string{"switch", "--ui=sidebar"}
	case "notify-sidebar":
		options.Client = ctx.TargetClient
		options.Target = ""
		options.Width = notifySidebarWidth(ctx.ClientWidth)
		options.Height = sidebarPopupHeight(ctx.ClientHeight)
		options.X = popupRightX(ctx.ClientWidth, options.Width)
		options.Y = "0"
		commandArgs = []string{"notify", "list", "--ui=sidebar"}
		if client := strings.TrimSpace(ctx.TargetClient); client != "" {
			commandArgs = append(commandArgs, "--client", client)
		}
	case "ai-split-picker-right", "ai-split-picker-down":
		options.Width = popupSize(ctx.ClientWidth, 40, 96)
		options.Height = popupSize(ctx.ClientHeight, 45, 20)
		cwd = ctx.ContextDir
		env["TMUX_SPLIT_TARGET_PANE"] = ctx.OriginPane
		env["TMUX_SPLIT_CONTEXT_DIR"] = ctx.ContextDir
		commandArgs = []string{"ai", "picker", "--inside", mode.Direction}
	case "ai-split-settings":
		options.Width = popupSize(ctx.ClientWidth, 55, 80)
		options.Height = popupSize(ctx.ClientHeight, 40, 14)
		commandArgs = []string{"settings"}
	default:
		return "", inttmux.PopupOptions{}, fmt.Errorf("unknown tmux popup-toggle mode: %s", mode.Raw)
	}
	if launchKey := nativeLaunchKeyForPopupMode(mode.Raw); launchKey != "" {
		env[intpicker.NativeLaunchKeyEnv] = launchKey
	}
	inheritPopupPickerEnv(env, lookupEnv)
	if backend == intpicker.BackendNative {
		options.NoBorder = true
		env[intpicker.BackendEnv] = string(intpicker.BackendNative)
	}

	options.Cwd = cwd
	options.Env = env
	return buildMarkedPopupCommand(binaryPath, commandArgs, marker, cwd, env), options, nil
}

func sessionizerSidebarWidth(clientWidth int, backend intpicker.Backend) string {
	_ = backend
	return popupSize(clientWidth, 20, 40)
}

func notifySidebarWidth(clientWidth int) string {
	return popupSize(clientWidth, 24, 64)
}

func sidebarPopupHeight(clientHeight int) string {
	if clientHeight <= 0 {
		return "100%"
	}
	return fmt.Sprintf("%d", max(clientHeight-2, 1))
}

func inheritPopupPickerEnv(env map[string]string, lookupEnv func(string) string) {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	for _, key := range []string{
		intpicker.BackendEnv,
		intpicker.NativeDebugLogEnv,
		intpicker.NativeTTYFallbackEnv,
		"PROJMUX_PROJDIR",
		"PROJMUX_MANAGED_ROOTS",
		"TMUX_SESSIONIZER_ROOTS",
		switchInitialQueryEnv,
		switchInitialSelectionEnv,
		switchStatusMessageEnv,
	} {
		if value := strings.TrimSpace(lookupEnv(key)); value != "" {
			env[key] = value
		}
	}
}

func nativeLaunchKeyForPopupMode(mode string) string {
	switch mode {
	case "sessionizer-sidebar":
		return "alt-1"
	case "notify-sidebar":
		return "alt-2"
	case "session-popup":
		return "alt-3"
	case "ai-split-picker-right", "ai-split-picker-down":
		return "alt-4"
	case "ai-split-settings":
		return "alt-5"
	case "sessionizer":
		return "alt-6"
	default:
		return ""
	}
}

func (c *tmuxCommand) parseConfigBinary(args []string, name string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	binaryOverride := fs.String("bin", "", "projmux binary path to write into the tmux snippet")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		printTmuxUsage(stderr)
		return "", fmt.Errorf("%s does not accept positional arguments", name)
	}
	return c.resolveConfigBinary(*binaryOverride)
}

func (c *tmuxCommand) resolveConfigBinary(override string) (string, error) {
	if binaryPath := strings.TrimSpace(override); binaryPath != "" {
		return binaryPath, nil
	}
	if c.executable == nil {
		return "", errors.New("configure tmux executable: tmux executable resolver is not configured")
	}
	binaryPath, err := c.executable()
	if err != nil {
		return "", fmt.Errorf("resolve tmux executable: %w", err)
	}
	return binaryPath, nil
}

func (c *tmuxCommand) ensureConfigIncludes(config, sourceLine string) error {
	if c.writeFile == nil {
		return errors.New("configure tmux install writer: file writer is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		return fmt.Errorf("create tmux config directory: %w", err)
	}

	var existing []byte
	if c.readFile != nil {
		content, err := c.readFile(config)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read tmux config: %w", err)
		}
		if err == nil {
			existing = content
		}
	}

	if strings.Contains(string(existing), sourceLine) {
		return nil
	}

	next := string(existing)
	if strings.TrimSpace(next) == "" {
		next = "# projmux standalone tmux bindings\n" + sourceLine + "\n"
	} else {
		if !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
		next += "\n# projmux standalone tmux bindings\n" + sourceLine + "\n"
	}
	if err := c.writeFile(config, []byte(next), 0o644); err != nil {
		return fmt.Errorf("write tmux config include: %w", err)
	}
	return nil
}

func (c *tmuxCommand) expandHome(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home := envValue(c.lookupEnv, "HOME")
		if home == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}

func (c *tmuxCommand) defaultShell() string {
	return defaultInteractiveShell(c.lookupEnv)
}

func tmuxStandaloneConfig(binaryPath string, decoration config.StatusbarDecoration) string {
	return tmuxStandaloneConfigWithKeymap(binaryPath, statusbarDecorationSetFromGlobal(decoration), defaultKeyBindingCatalog(), false)
}

const statusbarSettingsIcon = ""

func statusbarSettingsButton(label string) string {
	return "#[bold,fg=colour230,bg=colour31]#[range=user|settings] " + label + " #[norange]#[default]"
}

func statusbarStandaloneSessionLeftFormat() string {
	return "#[range=user|session][#S] #[norange] "
}

func statusbarAppSessionLeftFormat() string {
	return "#[range=user|session]#[bold,fg=colour231,bg=colour90] #{s|^[^-]*-||:session_name} #[default]#[norange] "
}

func statusbarAuxLineFormat(bin string, autosave bool) string {
	line := "#[align=left range=user|notify]#(" + bin + " status notify --max-width 80)#[norange]" +
		"#[align=right range=user|usage]#(" + bin + " status usage --max-width 120)#[norange]"
	if autosave {
		line += "#(" + bin + " tmux autosave-session-state --quiet)"
	}
	return line
}

func statusbarWindowLineFormat() string {
	return "#[align=left range=left #{E:status-left-style}]#[push-default]#{T;=/#{status-left-length}:status-left}#[pop-default]#[norange default]" +
		"#[list=on align=#{status-justify}]#[list=left-marker]<#[list=right-marker]>#[list=on]" +
		"#{W:#[range=window|#{window_index} #{E:window-status-style}#{?#{&&:#{window_last_flag},#{!=:#{E:window-status-last-style},default}}, #{E:window-status-last-style},}#{?#{&&:#{window_bell_flag},#{!=:#{E:window-status-bell-style},default}}, #{E:window-status-bell-style},#{?#{&&:#{||:#{window_activity_flag},#{window_silence_flag}},#{!=:#{E:window-status-activity-style},default}}, #{E:window-status-activity-style},}}]#[push-default]#{T:window-status-format}#[pop-default]#[norange default]#{?window_end_flag,,#{window-status-separator}}," +
		"#[range=window|#{window_index} list=focus #{?#{!=:#{E:window-status-current-style},default},#{E:window-status-current-style},#{E:window-status-style}}#{?#{&&:#{window_last_flag},#{!=:#{E:window-status-last-style},default}}, #{E:window-status-last-style},}#{?#{&&:#{window_bell_flag},#{!=:#{E:window-status-bell-style},default}}, #{E:window-status-bell-style},#{?#{&&:#{||:#{window_activity_flag},#{window_silence_flag}},#{!=:#{E:window-status-activity-style},default}}, #{E:window-status-activity-style},}}]#[push-default]#{T:window-status-current-format}#[pop-default]#[norange list=on default]#{?window_end_flag,,#{window-status-separator}}}" +
		"#[nolist align=right range=right #{E:status-right-style}]#[push-default]#{T;=/#{status-right-length}:status-right}#[pop-default]#[norange default]"
}

func statusbarWindowTitleFormat() string {
	return tmuxCenteredWindowNameFormat(tmuxWindowTitleWidth)
}

// tmuxVisiblePaneLabelFormat is the canonical Projmux visible naming rule for
// panes and app window tabs. It intentionally keeps the pane border priority:
// AI topic first, known interactive shell command second, raw pane title last.
// Raw terminal/pane titles remain metadata for apps and snapshots, but shell
// branch OSC titles are not the primary Projmux window naming source.
func tmuxVisiblePaneLabelFormat() string {
	return "#{?#{&&:#{!=:#{@projmux_ai_agent},},#{!=:#{@projmux_ai_topic},}},#{@projmux_ai_topic}," + tmuxShellPaneLabelFormat() + "}"
}

func tmuxShellPaneLabelFormat() string {
	return "#{?#{||:#{||:#{||:#{==:#{pane_current_command},zsh},#{==:#{pane_current_command},bash}},#{||:#{==:#{pane_current_command},fish},#{==:#{pane_current_command},sh}}},#{||:#{==:#{pane_current_command},nu},#{==:#{pane_current_command},xonsh}}},#{pane_current_command},#{pane_title}}"
}

func tmuxPaneBorderFormat() string {
	paneLabelFormat := tmuxVisiblePaneLabelFormat()
	paneBusyFormat := "#{==:#{@projmux_attention_state},busy}"
	paneReplyFormat := "#{==:#{@projmux_attention_state},reply}"
	inactivePaneBorderFormat := "#{?" + paneBusyFormat + ",#[bold#,fg=colour220] ● " + paneLabelFormat + " #[default],#{?" + paneReplyFormat + ",#[bold#,fg=colour46] ● " + paneLabelFormat + " #[default],#[fg=colour244] " + paneLabelFormat + " #[default]}}"
	return "#{?pane_active,#[bold#,fg=colour16#,bg=colour45] > " + paneLabelFormat + " #[default]," + inactivePaneBorderFormat + "}"
}

func tmuxCenteredWindowNameFormat(width int) string {
	if width <= 0 {
		return ""
	}
	trimWidth := max(width-3, 1)
	fallback := "#{=/" + strconv.Itoa(trimWidth) + "/...:window_name}"
	for n := width; n >= 0; n-- {
		left := (width - n) / 2
		right := width - n - left
		body := strings.Repeat(" ", left) + "#{=/" + strconv.Itoa(width) + "/...:window_name}" + strings.Repeat(" ", right)
		fallback = "#{?#{==:#{n:window_name}," + strconv.Itoa(n) + "}," + body + "," + fallback + "}"
	}
	return fallback
}

func tmuxStandaloneConfigWithKeymap(binaryPath string, decorations statusbarDecorationSet, catalog []keyBindingAction, keymapPresent bool) string {
	bin := tmuxShellQuote(binaryPath)
	defaultStandaloneKeyBindings := keyBindingCatalogForScope(keyBindingScopeStandalone)
	standaloneKeyBindings := keyBindingCatalogForScopeFrom(catalog, keyBindingScopeStandalone)
	lines := []string{
		"# Generated by projmux. Safe to source from ~/.tmux.conf.",
		"set -g " + statusbarDecorationTmuxOption + " " + string(config.NormalizeStatusbarDecoration(string(decorations.Cwd))),
		"set -g " + statusbarDecorationCwdTmuxOption + " " + string(config.NormalizeStatusbarDecoration(string(decorations.Cwd))),
		"set -g " + statusbarDecorationGitTmuxOption + " " + string(config.NormalizeStatusbarDecoration(string(decorations.Git))),
		"set -g " + statusbarDecorationNotifyTmuxOption + " " + string(config.NormalizeStatusbarDecoration(string(decorations.Notify))),
	}
	lines = append(lines,
		"set-hook -g pane-focus-out "+tmuxConfigQuote("run-shell -b "+tmuxConfigQuote(bin+" attention arm #{hook_pane}")),
		"set-hook -g pane-focus-in "+tmuxConfigQuote("run-shell -b "+tmuxConfigQuote(bin+" attention clear #{hook_pane}")),
		"set-hook -g after-select-pane "+tmuxConfigQuote("run-shell -b "+tmuxConfigQuote(bin+" attention clear #{pane_id}")),
		"set-hook -g pane-exited "+tmuxConfigQuote("run-shell -b "+tmuxConfigQuote("sleep 0.05; "+bin+" tmux rebalance-panes")),
		"set-hook -g after-kill-pane "+tmuxConfigQuote("run-shell -b "+tmuxConfigQuote("sleep 0.05; "+bin+" tmux rebalance-panes")),
		"set -g window-status-format "+tmuxConfigQuote("#[fg="+tmuxWindowInactiveFg+",bg="+tmuxWindowInactiveBg+"] #("+bin+" attention window #{window_id})#[fg="+tmuxWindowInactiveFg+"] #I "+statusbarWindowTitleFormat()+" #[default]"),
		"set -g window-status-current-format "+tmuxConfigQuote("#[bold,fg="+tmuxWindowActiveFg+",bg="+tmuxWindowActiveBg+"] #("+bin+" attention window #{window_id})#[fg="+tmuxWindowActiveFg+"] #I "+statusbarWindowTitleFormat()+" #[default]"),
		"set -g status 2",
		"set -g status-left-length 20",
		"set -g status-right-length 140",
		"set -g status-left "+tmuxConfigQuote(statusbarStandaloneSessionLeftFormat()),
		"set -g status-right "+tmuxConfigQuote(statusbarCwdSegmentFormat()+"#[fg=colour239]  #[range=user|kube]#("+bin+" status kube)#[norange]#[range=user|git]#("+bin+" status git)#[norange]   %Y-%m-%d %H:%M "+statusbarSettingsButton(statusbarSettingsIcon+" projmux")),
		// Two-line status bar: line 0 is the notify/HUD control row; line 1 is
		// tmux's native session/window/path row. Setting both rows explicitly is
		// required because tmux's built-in row otherwise stays at index 0.
		"set -g status-format[0] "+tmuxConfigQuote(statusbarAuxLineFormat(bin, false)),
		"set -g status-format[1] "+tmuxConfigQuote(statusbarWindowLineFormat()),
		// Clear any stale index-2 row a previous projmux build may have left on
		// the running tmux server (older config used three lines).
		"set -gu status-format[2]",
	)
	if keymapPresent {
		lines = append(lines, tmuxMergedUnbindLines(defaultStandaloneKeyBindings, standaloneKeyBindings)...)
	} else {
		lines = append(lines, tmuxUnbindLines(standaloneKeyBindings)...)
	}
	lines = append(lines, tmuxRetiredKeyUnbindLines()...)
	lines = append(lines, tmuxBindLines(binaryPath, standaloneKeyBindings)...)
	lines = append(lines, tmuxStatusbarKeyBindings(binaryPath)...)
	return strings.Join(lines, "\n") + "\n"
}

func tmuxAppConfig(binaryPath, defaultShell string, decoration config.StatusbarDecoration) string {
	return tmuxAppConfigWithKeymap(binaryPath, defaultShell, statusbarDecorationSetFromGlobal(decoration), defaultKeyBindingCatalog(), false)
}

func tmuxAppConfigWithKeymap(binaryPath, defaultShell string, decorations statusbarDecorationSet, catalog []keyBindingAction, keymapPresent bool) string {
	bin := tmuxShellQuote(binaryPath)
	shell := tmuxConfigQuote(nonEmpty(strings.TrimSpace(defaultShell), fallbackInteractiveShell))
	paneLabelFormat := tmuxVisiblePaneLabelFormat()
	paneBorderFormat := tmuxPaneBorderFormat()
	lines := []string{
		"# Generated by projmux. Used by `projmux shell`.",
		"set -g @projmux_app 1",
		"set -g default-terminal \"tmux-256color\"",
		"set -g mouse on",
		"set -g history-limit 10000",
		"set -g set-clipboard on",
		"set -g default-shell " + shell,
		"set -g default-command \"\"",
		"set -ga update-environment \"WSL_DISTRO_NAME\"",
		"set -ga update-environment \"WSL_INTEROP\"",
		"set -ga update-environment \"WSLENV\"",
		"set -ga update-environment \"VSCODE_IPC_HOOK_CLI\"",
		"set -ga update-environment \"VSCODE_GIT_IPC_HANDLE\"",
		"set -ga update-environment \"VSCODE_GIT_ASKPASS_NODE\"",
		"set -ga update-environment \"VSCODE_GIT_ASKPASS_MAIN\"",
		"set -ga update-environment \"VSCODE_GIT_ASKPASS_EXTRA_ARGS\"",
		"set -ga update-environment \"VSCODE_INJECTION\"",
		"set -ga update-environment \"TERM_PROGRAM\"",
		"set -ga update-environment \"TERM_PROGRAM_VERSION\"",
		"set -ga update-environment \"PROJMUX_WELCOME\"",
		"set -g status on",
		"set -g status-position bottom",
		"set -g status-interval 5",
		"set -g status-keys vi",
		"set -g status-left-length 20",
		"set -g status-right-length 140",
		"set -g window-status-separator \" \"",
		"set -g allow-rename off",
		"set -g automatic-rename on",
		"set -g automatic-rename-format " + tmuxConfigQuote(paneLabelFormat),
		"set -g mode-keys vi",
		"set -sg escape-time 100",
		"set -g status-style \"bg=" + tmuxWindowInactiveBg + ",fg=" + tmuxWindowInactiveFg + "\"",
		"set -g message-style \"bg=colour208,fg=colour16,bold\"",
		"set -g message-command-style \"bg=colour208,fg=colour16,bold\"",
		"set -g pane-border-style \"fg=colour236\"",
		"set -g pane-active-border-style \"fg=colour51,bold\"",
		"set -g pane-border-status top",
		"set -g pane-border-format " + tmuxConfigQuote(paneBorderFormat),
	}
	lines = append(lines, strings.Split(strings.TrimSpace(tmuxStandaloneConfigWithKeymap(binaryPath, decorations, catalog, keymapPresent)), "\n")[1:]...)
	lines = append(lines,
		"set-hook -g client-attached "+tmuxConfigQuote("run-shell -b "+tmuxConfigQuote(bin+" welcome --popup >/dev/null 2>&1")),
	)
	lines = append(lines, tmuxAppKeyBindings(catalog, keymapPresent)...)
	// Two-line status bar:
	//   [0] notify HUD (left) + usage HUD (right)
	//   [1] existing session/window/path/git/kube/clock row
	// We re-assert `status 2` here because `tmuxStandaloneConfig` already
	// sets it, but a tmux server that previously ran an older projmux build
	// may have stuck a leftover `status-format[1]` showing pane debug info —
	// we explicitly overwrite the line so it always reflects this build's
	// intent. `set -gu status-format[2]` clears any stale third row inherited
	// from the previous three-line layout. Width caps (80/120) target typical
	// 200+ col terminals; the HUD's full form is ~100 cells, so 120 leaves a
	// small buffer while still fitting alongside notify.
	lines = append(lines,
		"set -g status 2",
		"set -g status-left "+tmuxConfigQuote(statusbarAppSessionLeftFormat()),
		"set -g status-right "+tmuxConfigQuote(statusbarCwdSegmentFormat()+"#[fg=colour239]  #[range=user|kube]#("+bin+" status kube)#[norange]#[range=user|git]#("+bin+" status git)#[norange]   %Y-%m-%d %H:%M "+statusbarSettingsButton(statusbarSettingsIcon)),
		"set -g status-format[0] "+tmuxConfigQuote(statusbarAuxLineFormat(bin, true)),
		"set -g status-format[1] "+tmuxConfigQuote(statusbarWindowLineFormat()),
		"set -gu status-format[2]",
	)
	return strings.Join(lines, "\n") + "\n"
}

func tmuxAppKeyBindings(catalog []keyBindingAction, keymapPresent bool) []string {
	defaultAppKeyBindings := keyBindingCatalogForScope(keyBindingScopeApp)
	appKeyBindings := keyBindingCatalogForScopeFrom(catalog, keyBindingScopeApp)
	var lines []string
	if keymapPresent {
		lines = append(lines, tmuxMergedUnbindLines(defaultAppKeyBindings, appKeyBindings)...)
	} else {
		lines = append(lines, tmuxUnbindLines(appKeyBindings)...)
	}
	lines = append(lines, tmuxRetiredKeyUnbindLines()...)
	lines = append(lines, tmuxBindLines("", appKeyBindings)...)
	return lines
}

// tmuxStatusbarKeyBindings emits the bindings that wire the projmux statusbar
// click dispatcher to both mouse clicks and the `prefix s {letter}` chord.
//
// Note: `MouseDown1Status` fires for *any* line of a multi-line status bar,
// and `#{mouse_status_range}` returns whichever user-defined range we wrapped
// under the cursor — so a single bind covers both lines.
//
// We also pass `--mouse-window "#{mouse_window}"` so the dispatcher can fall
// back to tmux's default window-list behavior (`select-window -t @<id>`) when
// the click lands on a window entry rather than one of our user-defined
// ranges. Without this, overriding `MouseDown1Status` would silently disable
// click-to-switch-window in the window list.
//
// IMPORTANT: empirically (tmux 3.4+ on `-L projmux`), a click on the window
// list fires `MouseDown1Status` with `#{mouse_status_range}` set to the bare
// string `"window"` and `#{mouse_window}` empty. The mouse-target idiom
// `select-window -t =` only resolves through tmux's *internal* mouse context,
// which is not preserved across a `run-shell` subprocess — so projmux's Go
// dispatcher cannot recover the clicked window from those inputs alone. We
// therefore short-circuit the `range == "window"` case to a native
// `select-window -t =` inside the same tmux command-chain via `if-shell -F`,
// and only fall through to `run-shell` for projmux-managed ranges. The Go
// fallback (`isWindowListRangeToken`) is left in place as defense-in-depth
// for any future tmux that does populate the index.
//
// The `prefix s` chord uses tmux's `switch-client -T <table>` mechanism so
// keyboard users get the same handlers as mouse clickers without re-defining
// each handler twice.
func tmuxStatusbarKeyBindings(binaryPath string) []string {
	bin := tmuxShellQuote(binaryPath)
	clickCmd := bin + " statusbar click \"#{mouse_status_range}\" --client \"#{client_tty}\" --mouse-window \"#{mouse_window}\""
	// Use tmux's `{...}` block syntax for the if-shell branches so the nested
	// quotes inside `run-shell` don't need to be escaped through another layer
	// of tmux config quoting (which the parser rejects). Block syntax requires
	// tmux 3.0+; projmux already enforces tmux >= 3.4 via the doctor check.
	mouseDownBind := "bind-key -n MouseDown1Status if-shell -F " +
		tmuxConfigQuote("#{==:#{mouse_status_range},window}") + " " +
		"{ select-window -t = } " +
		"{ run-shell " + tmuxConfigQuote(clickCmd) + " }"
	return []string{
		"unbind-key -q -n MouseDown1Status",
		mouseDownBind,
		"bind-key s switch-client -T projmux-status",
		"bind-key -T projmux-status u run-shell " + tmuxConfigQuote(bin+" statusbar click usage"),
		"bind-key -T projmux-status n run-shell " + tmuxConfigQuote(bin+" statusbar click notify"),
		"bind-key -T projmux-status g run-shell " + tmuxConfigQuote(bin+" statusbar click git"),
		"bind-key -T projmux-status k run-shell " + tmuxConfigQuote(bin+" statusbar click kube"),
		"bind-key -T projmux-status p run-shell " + tmuxConfigQuote(bin+" statusbar click pwd"),
		"bind-key -T projmux-status s run-shell " + tmuxConfigQuote(bin+" statusbar click session"),
	}
}

func buildMarkedPopupCommand(binaryPath string, args []string, marker, cwd string, env map[string]string) string {
	parts := []string{}
	if strings.TrimSpace(cwd) != "" {
		parts = append(parts, "cd -- "+tmuxShellQuote(cwd))
	}

	command := []string{}
	for _, key := range sortedStringKeys(env) {
		value := env[key]
		if strings.TrimSpace(value) == "" {
			continue
		}
		command = append(command, key+"="+tmuxShellQuote(value))
	}
	command = append(command, tmuxShellQuote(binaryPath))
	for _, arg := range args {
		command = append(command, tmuxShellQuote(arg))
	}
	parts = append(parts, strings.Join(command, " "))
	parts = append(parts, "code=$?")
	parts = append(parts, "rm -f -- "+tmuxShellQuote(marker))
	parts = append(parts, "exit $code")
	return strings.Join(parts, "; ")
}

func popupMarkerPath(clientKey, mode string) string {
	return filepath.Join(os.TempDir(), "projmux-tmux-popup-"+sanitizePopupKey(clientKey)+"-"+sanitizePopupKey(mode)+".marker")
}

func sanitizePopupKey(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func popupSize(total, percent, minimum int) string {
	if total <= 0 {
		return fmt.Sprintf("%d%%", percent)
	}
	value := min(max(total*percent/100, minimum), total)
	return fmt.Sprintf("%d", value)
}

func popupRightX(total int, width string) string {
	if total <= 0 {
		return "R"
	}
	popupWidth := parseTmuxPositiveInt(width)
	if popupWidth <= 0 {
		return "R"
	}
	return fmt.Sprintf("%d", max(total-popupWidth, 0))
}

func parseTmuxPositiveInt(value string) int {
	var parsed int
	if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func tmuxShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func tmuxConfigQuote(value string) string {
	return "\"" + strings.ReplaceAll(value, "\"", "\\\"") + "\""
}
