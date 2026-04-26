package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
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
	writeFile     func(string, []byte, os.FileMode) error
	readFile      func(string) ([]byte, error)
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
		writeFile:     os.WriteFile,
		readFile:      os.ReadFile,
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
	case "help", "--help", "-h":
		printTmuxUsage(stdout)
		return nil
	default:
		printTmuxUsage(stderr)
		return fmt.Errorf("unknown tmux subcommand: %s", fs.Arg(0))
	}
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
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
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
	}
	marker := popupMarkerPath(popupCtx.ClientKey, mode.Canonical)
	if _, err := os.Stat(marker); err == nil {
		targetPane := strings.TrimSpace(popupCtx.OriginPane)
		if content, readErr := os.ReadFile(marker); readErr == nil && strings.TrimSpace(string(content)) != "" {
			targetPane = strings.TrimSpace(string(content))
		}
		if err := c.closePopup(ctx, targetPane); err != nil {
			return err
		}
		_ = os.Remove(marker)
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat tmux popup marker: %w", err)
	}

	command, options, err := buildPopupToggle(mode, binaryPath, marker, popupCtx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(marker, []byte(popupCtx.OriginPane+"\n"), 0o644); err != nil {
		return fmt.Errorf("write tmux popup marker: %w", err)
	}
	displayArgs, err := inttmux.BuildDisplayPopupArgs(command, options)
	if err != nil {
		_ = os.Remove(marker)
		return err
	}
	if _, err := c.runner.Run(ctx, "tmux", displayArgs...); err != nil {
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
	case "session-popup", "sessionizer", "sessionizer-sidebar", "ai-split-settings":
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
	_, err = io.WriteString(stdout, tmuxStandaloneConfig(binaryPath))
	return err
}

func (c *tmuxCommand) runPrintAppConfig(args []string, stdout, stderr io.Writer) error {
	binaryPath, err := c.parseConfigBinary(args, "tmux print-app-config", stderr)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, tmuxAppConfig(binaryPath))
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
	if err := c.writeFile(include, []byte(tmuxStandaloneConfig(binaryPath)), 0o644); err != nil {
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

	binaryPath, err := c.resolveConfigBinary(*binaryOverride)
	if err != nil {
		return err
	}
	config := c.expandHome(strings.TrimSpace(*configPath))
	if config == "" {
		config = c.expandHome("~/.config/projmux/tmux.conf")
	}
	if c.writeFile == nil {
		return errors.New("configure tmux install-app writer: file writer is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		return fmt.Errorf("create tmux app config directory: %w", err)
	}
	if err := c.writeFile(config, []byte(tmuxAppConfig(binaryPath)), 0o644); err != nil {
		return fmt.Errorf("write tmux app config: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "wrote %s\n", config)
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
	fmt.Fprintln(w, "  projmux tmux popup-toggle [--client <key>] <session-popup|sessionizer|sessionizer-sidebar|ai-split-picker-right|ai-split-picker-down|ai-split-settings>")
	fmt.Fprintln(w, "  projmux tmux rebalance-panes")
	fmt.Fprintln(w, "  projmux tmux rename-pane <pane> <title>")
	fmt.Fprintln(w, "  projmux tmux print-config [--bin <path>]")
	fmt.Fprintln(w, "  projmux tmux print-app-config [--bin <path>]")
	fmt.Fprintln(w, "  projmux tmux install [--bin <path>] [--config <path>] [--include <path>]")
	fmt.Fprintln(w, "  projmux tmux install-app [--bin <path>] [--config <path>]")
}

type tmuxPopupToggleMode struct {
	Raw       string
	Canonical string
	Direction string
	ClientKey string
}

type tmuxPopupContext struct {
	ClientKey     string
	OriginPane    string
	OriginSession string
	ContextDir    string
	ClientWidth   int
	ClientHeight  int
}

func (c *tmuxCommand) popupContext(ctx context.Context) tmuxPopupContext {
	clientKey := c.tmuxFormat(ctx, "#{client_tty}")
	if clientKey == "" {
		clientKey = c.tmuxFormat(ctx, "#{client_pid}")
	}
	if clientKey == "" {
		clientKey = "unknown"
	}

	return tmuxPopupContext{
		ClientKey:     sanitizePopupKey(clientKey),
		OriginPane:    c.tmuxFormat(ctx, "#{pane_id}"),
		OriginSession: c.tmuxFormat(ctx, "#S"),
		ContextDir:    c.tmuxFormat(ctx, "#{pane_current_path}"),
		ClientWidth:   parseTmuxPositiveInt(c.tmuxFormat(ctx, "#{client_width}")),
		ClientHeight:  parseTmuxPositiveInt(c.tmuxFormat(ctx, "#{client_height}")),
	}
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

func (c *tmuxCommand) closePopup(ctx context.Context, targetPane string) error {
	args := []string{"display-popup"}
	if strings.TrimSpace(targetPane) != "" {
		args = append(args, "-t", targetPane)
	}
	args = append(args, "-C")
	if _, err := c.runner.Run(ctx, "tmux", args...); err != nil {
		return fmt.Errorf("close tmux popup: %w", err)
	}
	return nil
}

func buildPopupToggle(mode tmuxPopupToggleMode, binaryPath, marker string, ctx tmuxPopupContext) (string, inttmux.PopupOptions, error) {
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
		commandArgs = []string{"sessions", "--ui=popup"}
	case "sessionizer":
		options.Width = popupSize(ctx.ClientWidth, 80, 120)
		options.Height = popupSize(ctx.ClientHeight, 70, 28)
		cwd = ctx.ContextDir
		env["TMUX_SESSIONIZER_CONTEXT_DIR"] = ctx.ContextDir
		env["TMUX_SESSIONIZER_CONTEXT_SESSION"] = ctx.OriginSession
		env["TMUX_SESSIONIZER_CONTEXT_PANE"] = ctx.OriginPane
		commandArgs = []string{"switch", "--ui=popup"}
	case "sessionizer-sidebar":
		options.Width = popupSize(ctx.ClientWidth, 20, 56)
		options.Height = popupSize(ctx.ClientHeight, 100, 20)
		options.X = "0"
		options.Y = "0"
		cwd = ctx.ContextDir
		env["TMUX_SESSIONIZER_CONTEXT_DIR"] = ctx.ContextDir
		env["TMUX_SESSIONIZER_CONTEXT_SESSION"] = ctx.OriginSession
		env["TMUX_SESSIONIZER_CONTEXT_PANE"] = ctx.OriginPane
		commandArgs = []string{"switch", "--ui=sidebar"}
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

	options.Cwd = cwd
	options.Env = env
	return buildMarkedPopupCommand(binaryPath, commandArgs, marker, cwd, env), options, nil
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

func tmuxStandaloneConfig(binaryPath string) string {
	bin := tmuxShellQuote(binaryPath)
	lines := []string{
		"# Generated by projmux. Safe to source from ~/.tmux.conf.",
		"set -s user-keys[0] \"\\033[9001u\"",
		"set -s user-keys[1] \"\\033[9002u\"",
		"set -s user-keys[2] \"\\033[9003u\"",
		"set -s user-keys[3] \"\\033[9004u\"",
		"set -s user-keys[4] \"\\033[9005u\"",
		"set -s user-keys[5] \"\\033[9006u\"",
		"set -s user-keys[6] \"\\033[9007u\"",
		"set -s user-keys[10] \"\\033[9011u\"",
		"set -s user-keys[11] \"\\033[9012u\"",
		"set-hook -g pane-focus-out " + tmuxConfigQuote("run-shell -b "+tmuxConfigQuote(bin+" attention arm #{hook_pane}")),
		"set-hook -g pane-focus-in " + tmuxConfigQuote("run-shell -b "+tmuxConfigQuote(bin+" attention clear #{hook_pane}")),
		"set-hook -g after-select-pane " + tmuxConfigQuote("run-shell -b "+tmuxConfigQuote(bin+" attention clear #{pane_id}")),
		"set-hook -g pane-exited " + tmuxConfigQuote("run-shell -b "+tmuxConfigQuote("sleep 0.05; "+bin+" tmux rebalance-panes")),
		"set-hook -g after-kill-pane " + tmuxConfigQuote("run-shell -b "+tmuxConfigQuote("sleep 0.05; "+bin+" tmux rebalance-panes")),
		"set -g window-status-format " + tmuxConfigQuote("#[fg=colour245,bg=colour235] #("+bin+" attention window #{window_id})#[fg=colour245] #I #W #[default]"),
		"set -g window-status-current-format " + tmuxConfigQuote("#[bold,fg=colour231,bg=colour238] #("+bin+" attention window #{window_id})#[fg=colour231] #I #W #[default]"),
		"set -g status-right-length 140",
		"set -g status-right " + tmuxConfigQuote("#[fg=colour242]#{=/28/...:pane_current_path}#[fg=colour239]  #("+bin+" status kube)#("+bin+" status git)  %Y-%m-%d %H:%M #[bold,fg=colour16,bg=colour45] projmux #[default]"),
		"unbind-key -q -n M-1",
		"unbind-key -q -n M-2",
		"unbind-key -q -n M-3",
		"unbind-key -q -n M-4",
		"unbind-key -q -n M-5",
		"unbind-key -q -n M-r",
		"unbind-key -q -n User0",
		"unbind-key -q -n User1",
		"unbind-key -q -n User2",
		"unbind-key -q -n User3",
		"unbind-key -q -n User4",
		"unbind-key -q -n User5",
		"unbind-key -q -n User6",
		"unbind-key -q -n User10",
		"unbind-key -q -n User11",
		"bind-key -n M-1 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} sessionizer-sidebar"),
		"bind-key -n M-2 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} session-popup"),
		"bind-key -n M-3 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} sessionizer"),
		"bind-key -n M-4 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} ai-split-picker-right"),
		"bind-key -n M-5 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} ai-split-settings"),
		"bind-key -n M-r command-prompt -I \"#{window_name}\" " + tmuxConfigQuote("rename-window -- '%%'"),
		"bind-key -n User11 command-prompt -I \"#{?#{!=:#{@projmux_ai_topic},},#{@projmux_ai_topic},#{pane_title}}\" " + tmuxConfigQuote("select-pane -T '%1' \\; set-option -p "+aiPaneTopicOption+" '%1' \\; if-shell -F '#{==:#{"+aiPaneTopicOption+"},}' 'set-option -p -u "+aiPaneTopicManualOption+"' 'set-option -p "+aiPaneTopicManualOption+" 1'"),
		"bind-key -n User0 run-shell " + tmuxConfigQuote(bin+" ai split right"),
		"bind-key -n User1 run-shell " + tmuxConfigQuote(bin+" ai split down"),
		"bind-key -n User2 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} session-popup"),
		"bind-key -n User3 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} sessionizer"),
		"bind-key -n User4 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} sessionizer-sidebar"),
		"bind-key -n User5 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} ai-split-picker-right"),
		"bind-key -n User6 run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} ai-split-settings"),
		"bind-key -n User10 command-prompt -I \"#{window_name}\" " + tmuxConfigQuote("rename-window -- '%%'"),
		"bind-key b run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} session-popup"),
		"bind-key f run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} sessionizer"),
		"bind-key F run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} sessionizer-sidebar"),
		"bind-key g run-shell " + tmuxConfigQuote(bin+" current"),
		"bind-key r run-shell " + tmuxConfigQuote(bin+" ai split right"),
		"bind-key l run-shell " + tmuxConfigQuote(bin+" ai split down"),
		"bind-key R command-prompt -I \"#{window_name}\" " + tmuxConfigQuote("rename-window -- '%%'"),
	}
	return strings.Join(lines, "\n") + "\n"
}

func tmuxAppConfig(binaryPath string) string {
	bin := tmuxShellQuote(binaryPath)
	shellPaneLabelFormat := "#{?#{||:#{||:#{||:#{==:#{pane_current_command},zsh},#{==:#{pane_current_command},bash}},#{||:#{==:#{pane_current_command},fish},#{==:#{pane_current_command},sh}}},#{||:#{==:#{pane_current_command},nu},#{==:#{pane_current_command},xonsh}}},#{pane_current_command},#{pane_title}}"
	paneLabelFormat := "#{?#{&&:#{!=:#{@projmux_ai_agent},},#{!=:#{@projmux_ai_topic},}},#{@projmux_ai_topic}," + shellPaneLabelFormat + "}"
	paneBusyFormat := "#{||:#{==:#{@projmux_attention_state},busy},#{==:#{@projmux_ai_state},thinking}}"
	paneReplyFormat := "#{||:#{==:#{@projmux_attention_state},reply},#{==:#{@projmux_ai_state},waiting}}"
	inactivePaneBorderFormat := "#{?" + paneBusyFormat + ",#[bold#,fg=colour220] ● " + paneLabelFormat + " #[default],#{?" + paneReplyFormat + ",#[bold#,fg=colour46] ● " + paneLabelFormat + " #[default],#[fg=colour244] " + paneLabelFormat + " #[default]}}"
	paneBorderFormat := "#{?pane_active,#[bold#,fg=colour16#,bg=colour45] > " + paneLabelFormat + " #[default]," + inactivePaneBorderFormat + "}"
	lines := []string{
		"# Generated by projmux. Used by `projmux shell`.",
		"set -g @projmux_app 1",
		"set -g default-terminal \"tmux-256color\"",
		"set -g mouse on",
		"set -g history-limit 10000",
		"set -g set-clipboard on",
		"set -g default-shell /usr/bin/zsh",
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
		"set -g status on",
		"set -g status-position bottom",
		"set -g status-interval 5",
		"set -g status-keys vi",
		"set -g status-left-length 20",
		"set -g status-right-length 140",
		"set -g window-status-separator \" \"",
		"set -g automatic-rename on",
		"set -g automatic-rename-format \"#{pane_title}\"",
		"set -g mode-keys vi",
		"set -sg escape-time 100",
		"set -g status-style \"bg=colour235,fg=colour245\"",
		"set -g message-style \"bg=colour45,fg=colour16,bold\"",
		"set -g pane-border-style \"fg=colour236\"",
		"set -g pane-active-border-style \"fg=colour51,bold\"",
		"set -g pane-border-status top",
		"set -g pane-border-format " + tmuxConfigQuote(paneBorderFormat),
	}
	lines = append(lines, strings.Split(strings.TrimSpace(tmuxStandaloneConfig(binaryPath)), "\n")[1:]...)
	lines = append(lines, tmuxAppKeyBindings()...)
	lines = append(lines,
		"set -g status-left \"#[bold,fg=colour16,bg=colour45] #{b:pane_current_path} #[default]\"",
		"set -g status-right "+tmuxConfigQuote("#[fg=colour242]#{=/28/...:pane_current_path}#[fg=colour239]  #("+bin+" status kube)#("+bin+" status git)  %Y-%m-%d %H:%M#[default]"),
	)
	return strings.Join(lines, "\n") + "\n"
}

func tmuxAppKeyBindings() []string {
	return []string{
		"set -s user-keys[7] \"\\033[9008u\"",
		"set -s user-keys[8] \"\\033[9009u\"",
		"set -s user-keys[9] \"\\033[9010u\"",
		"unbind-key -q -n M-Left",
		"unbind-key -q -n M-Right",
		"unbind-key -q -n M-Up",
		"unbind-key -q -n M-Down",
		"unbind-key -q -n C-n",
		"unbind-key -q -n M-S-Left",
		"unbind-key -q -n M-S-Right",
		"unbind-key -q -n User7",
		"unbind-key -q -n User8",
		"unbind-key -q -n User9",
		"bind-key -n M-Left select-pane -L",
		"bind-key -n M-Right select-pane -R",
		"bind-key -n M-Up select-pane -U",
		"bind-key -n M-Down select-pane -D",
		"bind-key -n C-n new-window -c \"#{pane_current_path}\"",
		"bind-key -n M-S-Left previous-window",
		"bind-key -n M-S-Right next-window",
		"bind-key -n User7 new-window -c \"#{pane_current_path}\"",
		"bind-key -n User8 previous-window",
		"bind-key -n User9 next-window",
		"bind-key M if -F \"#{mouse}\" \"set -g mouse off \\; display-message 'tmux mouse: off'\" \"set -g mouse on \\; display-message 'tmux mouse: on'\"",
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
