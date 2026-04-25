package app

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	intfzf "github.com/es5h/projmux/internal/ui/fzf"
)

const (
	aiModeSelective = "selective"
	aiModeClaude    = "claude"
	aiModeCodex     = "codex"
	aiModeShell     = "shell"
)

type aiCommandRunner interface {
	Run(options intfzf.Options) (intfzf.Result, error)
}

type aiCommand struct {
	runner      aiCommandRunner
	executable  func() (string, error)
	lookupEnv   func(string) string
	homeDir     func() (string, error)
	runCommand  func(ctx context.Context, name string, args ...string) error
	readCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	now         func() time.Time
	sleep       func(time.Duration)
}

func newAICommand() *aiCommand {
	return &aiCommand{
		runner:      intfzf.NewRunner(),
		executable:  os.Executable,
		lookupEnv:   os.Getenv,
		homeDir:     os.UserHomeDir,
		runCommand:  runExternalCommand,
		readCommand: readExternalCommand,
		now:         time.Now,
		sleep:       time.Sleep,
	}
}

func (c *aiCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printAIUsage(stderr)
		return errors.New("ai requires a subcommand")
	}

	switch args[0] {
	case "split":
		return c.runSplit(args[1:], stderr)
	case "picker":
		return c.runPicker(args[1:], stderr)
	case "settings":
		return c.runSettings(args[1:], stdout, stderr)
	case "status":
		return c.runStatus(args[1:], stderr)
	case "notify":
		return c.runNotify(args[1:], stderr)
	case "watch-title":
		return c.runWatchTitle(args[1:], stderr)
	case "help", "--help", "-h":
		printAIUsage(stdout)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai subcommand: %s", args[0])
	}
}

func (c *aiCommand) runStatus(args []string, stderr io.Writer) error {
	if len(args) == 0 {
		printAIUsage(stderr)
		return errors.New("ai status requires a subcommand")
	}
	switch args[0] {
	case "set":
		if len(args) < 2 || len(args) > 3 {
			printAIUsage(stderr)
			return errors.New("ai status set requires <thinking|waiting|idle> [pane]")
		}
		paneID := strings.TrimSpace(c.env("TMUX_PANE"))
		if len(args) == 3 {
			paneID = strings.TrimSpace(args[2])
		}
		return c.applyAIStatus(args[1], paneID)
	case "help", "--help", "-h":
		printAIUsage(stderr)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai status subcommand: %s", args[0])
	}
}

func (c *aiCommand) applyAIStatus(state, paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return nil
	}

	currentTitle := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{pane_title}")
	baseTitle := trimAIStatePrefix(currentTitle)
	switch strings.TrimSpace(state) {
	case "thinking":
		_ = c.resetAINotification(paneID)
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, attentionStateOption, attentionStateBusy)
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionAckOption)
		_ = c.run("tmux", "select-pane", "-T", "⠹ "+baseTitle, "-t", paneID)
	case "waiting":
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, attentionStateOption, attentionStateReply)
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionAckOption)
		_ = c.run("tmux", "select-pane", "-T", "✳ "+baseTitle, "-t", paneID)
		_ = c.notifyAI(paneID)
	case "idle", "":
		_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, attentionStateOption)
		_ = c.run("tmux", "select-pane", "-T", baseTitle, "-t", paneID)
	default:
		return fmt.Errorf("unknown ai status state: %s", state)
	}
	return nil
}

func (c *aiCommand) runNotify(args []string, stderr io.Writer) error {
	action := "notify"
	paneID := strings.TrimSpace(c.env("TMUX_PANE"))
	switch len(args) {
	case 0:
	case 1:
		if args[0] == "notify" || args[0] == "reset" {
			action = args[0]
		} else {
			paneID = strings.TrimSpace(args[0])
		}
	case 2:
		action = args[0]
		paneID = strings.TrimSpace(args[1])
	default:
		printAIUsage(stderr)
		return errors.New("ai notify accepts [notify|reset] [pane]")
	}

	switch action {
	case "reset":
		return c.resetAINotification(paneID)
	case "notify":
		return c.notifyAI(paneID)
	case "help", "--help", "-h":
		printAIUsage(stderr)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai notify action: %s", action)
	}
}

func (c *aiCommand) resetAINotification(paneID string) error {
	if strings.TrimSpace(paneID) == "" {
		return nil
	}
	_ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, "@dotfiles_desktop_notified")
	return nil
}

func (c *aiCommand) notifyAI(paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return nil
	}
	if c.readTmuxPaneOption(paneID, "@dotfiles_desktop_notified") == "1" {
		return nil
	}

	paneTitle := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{pane_title}")
	sessionName := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#S")
	windowName := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#W")
	panePath := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{pane_current_path}")
	cleanTitle := displayAITopic(paneTitle)
	replyKind := aiReplyKindForTitle(paneTitle)
	key := aiNotificationKey(replyKind, paneTitle)
	if c.duplicateAINotificationRecent(paneID, key) {
		c.recordAINotification(paneID, key)
		return nil
	}

	summary := aiSummaryForKind(replyKind, cleanTitle)
	body := aiNotificationBody(filepath.Base(defaultString(panePath, "/")), c.gitBranchForPath(panePath), sessionName, windowName)
	if err := c.dispatchAINotification(summary, body, aiUrgencyForKind(replyKind), "dotfiles.TmuxCodex", paneID, sessionName); err != nil {
		return nil
	}
	c.recordAINotification(paneID, key)
	return nil
}

func (c *aiCommand) runWatchTitle(args []string, stderr io.Writer) error {
	if len(args) > 1 {
		printAIUsage(stderr)
		return errors.New("ai watch-title accepts at most 1 [pane] argument")
	}
	paneID := strings.TrimSpace(c.env("TMUX_PANE"))
	if len(args) == 1 {
		paneID = strings.TrimSpace(args[0])
	}
	if paneID == "" {
		return nil
	}

	interval := c.watchInterval()
	settleLimit := c.watchSettleLoops()
	phase := "idle"
	lastState := ""
	settleCount := 0
	for {
		if _, err := c.read("tmux", "display-message", "-p", "-t", paneID, "#{pane_id}"); err != nil {
			return nil
		}
		title, state, ack := c.readAIWatchSnapshot(paneID)
		nextState := "idle"
		switch {
		case isAIBusyTitle(title):
			phase = "busy"
			settleCount = 0
			nextState = "thinking"
		case ack != "1" && isAIReplyTitle(title):
			phase = "replied"
			settleCount = 0
			nextState = "waiting"
		case phase == "busy":
			settleCount++
			if settleCount >= settleLimit {
				phase = "replied"
				nextState = "waiting"
			} else {
				nextState = "thinking"
			}
		case phase == "replied" && ack != "1":
			settleCount = 0
			nextState = "waiting"
		default:
			settleCount = 0
		}

		if nextState != lastState || aiAttentionMismatch(nextState, state) {
			_ = c.applyAIStatus(nextState, paneID)
			lastState = nextState
		}
		c.sleepFor(interval)
	}
}

func (c *aiCommand) runSplit(args []string, stderr io.Writer) error {
	direction, err := parseAISplitDirection(args, "ai split", stderr)
	if err != nil {
		return err
	}

	mode := c.getMode()
	switch mode {
	case aiModeClaude:
		return c.runAgentSplit(aiModeClaude, direction)
	case aiModeCodex:
		return c.runAgentSplit(aiModeCodex, direction)
	case aiModeShell:
		return c.runShellSplit(direction)
	case aiModeSelective:
		return c.openPickerToggle(direction)
	default:
		return c.openPickerToggle(direction)
	}
}

func (c *aiCommand) runPicker(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai picker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inside := fs.Bool("inside", false, "run inside an already-open popup")
	shellOnly := fs.Bool("shell", false, "open a plain shell split")
	if err := fs.Parse(args); err != nil {
		return err
	}
	direction, err := parseAISplitDirection(fs.Args(), "ai picker", stderr)
	if err != nil {
		return err
	}
	if *shellOnly {
		return c.runShellSplit(direction)
	}
	if !*inside && c.env("TMUX") != "" {
		return c.openPicker(direction)
	}

	result, err := c.runAgentPicker(direction)
	if err != nil {
		if isNoSelectionExit(err) {
			return nil
		}
		return err
	}
	if result.Value == "" || result.Key != "enter" {
		return nil
	}

	switch normalizeAIMode(result.Value) {
	case aiModeClaude:
		return c.runAgentSplit(aiModeClaude, direction)
	case aiModeCodex:
		return c.runAgentSplit(aiModeCodex, direction)
	case aiModeShell:
		return c.runShellSplit(direction)
	default:
		return nil
	}
}

func (c *aiCommand) runSettings(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai settings", flag.ContinueOnError)
	fs.SetOutput(stderr)
	get := fs.Bool("get", false, "print the configured AI split mode")
	set := fs.String("set", "", "set the configured AI split mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai settings does not accept positional arguments")
	}

	if *get {
		_, err := fmt.Fprintln(stdout, c.getMode())
		return err
	}
	if strings.TrimSpace(*set) != "" {
		return c.setMode(*set)
	}

	if c.runner == nil {
		return errors.New("ai settings runner is not configured")
	}
	result, err := c.runner.Run(intfzf.Options{
		UI:         "ai-settings",
		Entries:    c.settingsRows(),
		Prompt:     "AI Setting > ",
		Header:     "Set Ctrl+Shift+R/L default mode",
		Footer:     projmuxFooter("Enter: set default  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys: []string{"enter"},
		Bindings: []string{
			"esc:abort",
			"ctrl-c:abort",
			"alt-5:abort",
			"ctrl-alt-s:abort",
			"alt-4:abort",
		},
	})
	if err != nil {
		if isNoSelectionExit(err) {
			return nil
		}
		return fmt.Errorf("run ai settings picker: %w", err)
	}
	if result.Key != "enter" || result.Value == "" {
		return nil
	}
	return c.setMode(result.Value)
}

func (c *aiCommand) runAgentPicker(direction string) (intfzf.Result, error) {
	if c.runner == nil {
		return intfzf.Result{}, errors.New("ai picker runner is not configured")
	}
	return c.runner.Run(intfzf.Options{
		UI:         "ai-picker",
		Entries:    c.agentRows(),
		Prompt:     "AI Launch > ",
		Header:     "Split Direction: " + direction + "  |  Choose runtime",
		Footer:     projmuxFooter("Enter: launch  |  Esc/Alt+4/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys: []string{"enter"},
		Bindings: []string{
			"esc:abort",
			"ctrl-c:abort",
			"alt-4:abort",
			"alt-5:abort",
			"ctrl-alt-s:abort",
		},
	})
}

func (c *aiCommand) settingsRows() []intfzf.Entry {
	current := c.getMode()
	modes := []struct {
		mode string
		desc string
	}{
		{aiModeSelective, "show picker each time"},
		{aiModeClaude, "always run Claude split"},
		{aiModeCodex, "always run Codex split"},
		{aiModeShell, "always open zsh split"},
	}
	rows := make([]intfzf.Entry, 0, len(modes))
	for _, item := range modes {
		tag := ansiDim("[ ]")
		if item.mode == current {
			tag = "\x1b[32m[ACTIVE]\x1b[0m"
		}
		rows = append(rows, intfzf.Entry{
			Label: fmt.Sprintf("%s \x1b[36m%-9s\x1b[0m  \x1b[90m%s\x1b[0m", tag, item.mode, item.desc),
			Value: item.mode,
		})
	}
	return rows
}

func (c *aiCommand) agentRows() []intfzf.Entry {
	rows := []intfzf.Entry{
		c.agentRow(aiModeClaude, "Anthropic CLI split"),
		c.agentRow(aiModeCodex, "OpenAI Codex split"),
		{
			Label: fmt.Sprintf("%-8s \x1b[34m[READY]\x1b[0m Plain zsh split (\x1b[90mno agent\x1b[0m)", aiModeShell),
			Value: aiModeShell,
		},
	}
	return rows
}

func (c *aiCommand) agentRow(mode, desc string) intfzf.Entry {
	status := "\x1b[33m[MISSING]\x1b[0m"
	if c.agentAvailable(mode) {
		status = "\x1b[32m[READY]\x1b[0m"
	}
	return intfzf.Entry{
		Label: fmt.Sprintf("%-8s %s %s", mode, status, desc),
		Value: mode,
	}
}

func (c *aiCommand) getMode() string {
	content, err := os.ReadFile(c.configFile())
	if err != nil {
		return aiModeSelective
	}
	return normalizeAIMode(strings.TrimSpace(string(content)))
}

func (c *aiCommand) setMode(mode string) error {
	mode = normalizeAIMode(mode)
	path := c.configFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(mode+"\n"), 0o644); err != nil {
		return err
	}
	_ = c.displayMessage("ai split default: " + mode)
	return nil
}

func (c *aiCommand) configFile() string {
	configHome := strings.TrimSpace(c.env("XDG_CONFIG_HOME"))
	if configHome == "" {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			return filepath.Join(".config", "dotfiles", "tmux-ai-split-mode")
		}
		configHome = filepath.Join(homeDir, ".config")
	}
	return filepath.Join(configHome, "dotfiles", "tmux-ai-split-mode")
}

func (c *aiCommand) openPicker(direction string) error {
	binaryPath, err := c.binaryPath()
	if err != nil {
		return err
	}
	targetPane := c.resolveTargetPane()
	command := shellEnv("TMUX_SPLIT_TARGET_PANE", targetPane) + shellQuote(binaryPath) + " ai picker --inside " + shellQuote(direction)
	width, height := c.popupSize(40, 64, 30, 12)
	err = c.run("tmux", "display-popup", "-E", "-w", width, "-h", height, command)
	if isNoSelectionExit(err) {
		return nil
	}
	return err
}

func (c *aiCommand) openPickerToggle(direction string) error {
	binaryPath, err := c.binaryPath()
	if err != nil {
		return err
	}
	mode := "ai-split-picker-right"
	if direction == "down" {
		mode = "ai-split-picker-down"
	}
	args := []string{"tmux", "popup-toggle"}
	if clientKey := c.readTrimmed("tmux", "display-message", "-p", "-F", "#{client_tty}"); clientKey != "" {
		args = append(args, "--client", clientKey)
	}
	args = append(args, mode)
	err = c.run(binaryPath, args...)
	if isNoSelectionExit(err) {
		return nil
	}
	return err
}

func (c *aiCommand) runAgentSplit(mode, direction string) error {
	if mode == aiModeShell {
		return c.runShellSplit(direction)
	}
	agentBin := c.findAgentBinary(mode)
	if agentBin == "" {
		_ = c.displayMessage("selected runner is not installed: " + mode)
		return fmt.Errorf("selected runner is not installed: %s", mode)
	}

	targetPane := c.resolveTargetPane()
	contextDir := c.resolveAgentContextDir(mode)
	title := c.buildAgentTitle(mode, contextDir)
	command := c.agentLaunchCommand(mode, agentBin, contextDir, title)
	if targetPane == "" {
		return c.run("zsh", "-lc", command)
	}

	splitFlag := "-h"
	if direction == "down" {
		splitFlag = "-v"
	}
	args := []string{"split-window", "-P", "-F", "#{pane_id}", splitFlag, "-t", targetPane}
	if contextDir != "" {
		args = append(args, "-c", contextDir)
	}
	args = append(args, "zsh", "-lc", command)
	out, err := c.read("tmux", args...)
	if err != nil {
		return err
	}
	if mode == aiModeCodex {
		c.startAIWatchTitle(strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *aiCommand) runShellSplit(direction string) error {
	targetPane := c.resolveTargetPane()
	contextDir := c.resolveContextDir()
	splitFlag := "-h"
	if direction == "down" {
		splitFlag = "-v"
	}

	args := []string{"split-window", splitFlag}
	if targetPane != "" {
		args = append(args, "-t", targetPane)
	}
	if contextDir != "" {
		args = append(args, "-c", contextDir)
	}
	args = append(args, "zsh", "-l")
	return c.run("tmux", args...)
}

func (c *aiCommand) resolveContextDir() string {
	if dir := c.env("TMUX_SPLIT_CONTEXT_DIR"); isDir(dir) {
		return dir
	}
	if c.env("TMUX") != "" {
		if path := c.readTrimmed("tmux", "display-message", "-p", "-F", "#{pane_current_path}"); isDir(path) {
			return path
		}
	}
	if target := c.resolveRecentTmuxTarget(); target != "" {
		if path := c.readTrimmed("tmux", "display-message", "-p", "-t", target, "-F", "#{pane_current_path}"); isDir(path) {
			return path
		}
	}
	return c.resolveIDEContextDir()
}

func (c *aiCommand) resolveAgentContextDir(mode string) string {
	switch mode {
	case aiModeClaude:
		if dir := c.env("CLAUDE_CONTEXT_DIR"); isDir(dir) {
			return dir
		}
	case aiModeCodex:
		if dir := c.env("CODEX_CONTEXT_DIR"); isDir(dir) {
			return dir
		}
	}
	return c.resolveContextDir()
}

func (c *aiCommand) resolveTargetPane() string {
	if pane := strings.TrimSpace(c.env("TMUX_SPLIT_TARGET_PANE")); pane != "" {
		if resolved := c.readTrimmed("tmux", "display-message", "-p", "-t", pane, "-F", "#{pane_id}"); resolved != "" {
			return resolved
		}
		return pane
	}
	if c.env("TMUX") != "" {
		return c.readTrimmed("tmux", "display-message", "-p", "-F", "#{pane_id}")
	}
	if target := c.resolveRecentTmuxTarget(); target != "" {
		return c.readTrimmed("tmux", "display-message", "-p", "-t", target, "-F", "#{pane_id}")
	}
	return ""
}

func (c *aiCommand) resolveRecentTmuxTarget() string {
	out, err := c.read("tmux", "list-clients", "-F", "#{client_activity}\t#{session_id}")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	bestActivity := ""
	bestTarget := ""
	for _, line := range lines {
		activity, target, ok := strings.Cut(line, "\t")
		if !ok || strings.TrimSpace(target) == "" {
			continue
		}
		if bestActivity == "" || strings.TrimSpace(activity) > bestActivity {
			bestActivity = strings.TrimSpace(activity)
			bestTarget = strings.TrimSpace(target)
		}
	}
	return bestTarget
}

func (c *aiCommand) resolveIDEContextDir() string {
	homeDir, err := c.home()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return ""
	}
	cacheDir := filepath.Join(homeDir, ".cache", "ide")
	for _, name := range []string{"vscode", "code", "intellij-idea", "pycharm", "goland", "webstorm", "clion", "rider", "idea", "android-studio"} {
		content, err := os.ReadFile(filepath.Join(cacheDir, name))
		if err == nil {
			if path := strings.TrimSpace(string(content)); isDir(path) {
				return path
			}
		}
	}
	return ""
}

func (c *aiCommand) popupSize(widthPercent, widthMin, heightPercent, heightMin int) (string, string) {
	return c.popupAxisSize("width", widthPercent, widthMin), c.popupAxisSize("height", heightPercent, heightMin)
}

func (c *aiCommand) buildAgentTitle(mode, contextDir string) string {
	switch mode {
	case aiModeClaude:
		if title := strings.TrimSpace(c.env("CLAUDE_THREAD_TITLE")); title != "" {
			return "claude:" + title
		}
		if title := strings.TrimSpace(c.env("AI_THREAD_TITLE")); title != "" {
			return "claude:" + title
		}
		if contextDir != "" {
			return "claude:" + filepath.Base(contextDir)
		}
		return "claude"
	case aiModeCodex:
		if title := strings.TrimSpace(c.env("CODEX_THREAD_TITLE")); title != "" {
			return "codex:" + title
		}
		if title := strings.TrimSpace(c.env("AI_THREAD_TITLE")); title != "" {
			return "codex:" + title
		}
		if contextDir != "" {
			return "codex:" + filepath.Base(contextDir)
		}
		return "codexcli"
	default:
		return mode
	}
}

func (c *aiCommand) agentLaunchCommand(mode, agentBin, contextDir, title string) string {
	titleVar := "__" + mode + "_title"
	parts := []string{}
	if contextDir != "" {
		parts = append(parts, "cd "+shellQuote(contextDir))
	}
	parts = append(parts,
		titleVar+"="+shellQuote(title),
		`printf '\033]0;%s\007' "$`+titleVar+`"`,
		`if [[ -n "${TMUX:-}" ]]; then tmux select-pane -T "$`+titleVar+`" >/dev/null 2>&1 || true; fi`,
		"exec "+shellQuote(agentBin),
	)
	return strings.Join(parts, " && ")
}

func (c *aiCommand) startAIWatchTitle(paneID string) {
	if strings.TrimSpace(paneID) == "" {
		return
	}
	binaryPath, err := c.binaryPath()
	if err != nil || strings.TrimSpace(binaryPath) == "" {
		return
	}
	_ = c.run("tmux", "run-shell", "-b", shellQuote(binaryPath)+" ai watch-title "+shellQuote(paneID))
}

func (c *aiCommand) popupAxisSize(axis string, percent, minimum int) string {
	format := "#{client_width}"
	if axis == "height" {
		format = "#{client_height}"
	}
	total := parsePositiveInt(c.readTrimmed("tmux", "display-message", "-p", "-F", format))
	if total <= 0 {
		return fmt.Sprintf("%d%%", percent)
	}
	value := min(max(total*percent/100, minimum), total)
	return fmt.Sprintf("%d", value)
}

func (c *aiCommand) agentAvailable(mode string) bool {
	if mode == aiModeShell {
		return true
	}
	return c.findAgentBinary(mode) != ""
}

func (c *aiCommand) findAgentBinary(mode string) string {
	switch mode {
	case aiModeClaude:
		return firstExecutable(c.readTrimmed("command", "-v", "claude"), filepath.Join(c.homeOrEmpty(), ".local", "bin", "claude"))
	case aiModeCodex:
		if path := firstExecutable(c.readTrimmed("command", "-v", "codex"), filepath.Join(c.homeOrEmpty(), ".npm-global", "bin", "codex"), filepath.Join(c.homeOrEmpty(), ".local", "bin", "codex")); path != "" {
			return path
		}
		matches, _ := filepath.Glob(filepath.Join(c.homeOrEmpty(), ".vscode", "extensions", "openai.chatgpt-*", "bin", "*", "codex"))
		return newestExecutable(matches)
	default:
		return ""
	}
}

func (c *aiCommand) displayMessage(message string) error {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	return c.run("tmux", "display-message", message)
}

func (c *aiCommand) binaryPath() (string, error) {
	if c.executable == nil {
		return "", errors.New("ai executable resolver is not configured")
	}
	return c.executable()
}

func (c *aiCommand) home() (string, error) {
	if c.homeDir == nil {
		return "", errors.New("ai home directory resolver is not configured")
	}
	return c.homeDir()
}

func (c *aiCommand) homeOrEmpty() string {
	homeDir, err := c.home()
	if err != nil {
		return ""
	}
	return homeDir
}

func (c *aiCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

func (c *aiCommand) run(name string, args ...string) error {
	if c.runCommand == nil {
		return errors.New("ai command runner is not configured")
	}
	return c.runCommand(context.Background(), name, args...)
}

func (c *aiCommand) read(name string, args ...string) ([]byte, error) {
	if c.readCommand == nil {
		return nil, errors.New("ai command reader is not configured")
	}
	return c.readCommand(context.Background(), name, args...)
}

func (c *aiCommand) readTrimmed(name string, args ...string) string {
	out, err := c.read(name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (c *aiCommand) readTmuxPaneOption(paneID, option string) string {
	return c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{"+option+"}")
}

func (c *aiCommand) duplicateAINotificationRecent(paneID, key string) bool {
	if key == "" {
		return false
	}
	dedupeSeconds := parsePositiveInt(c.env("DOTFILES_TMUX_NOTIFY_DEDUPE_SECONDS"))
	if dedupeSeconds <= 0 {
		dedupeSeconds = 120
	}
	if c.readTmuxPaneOption(paneID, "@dotfiles_desktop_notification_key") != key {
		return false
	}
	lastAt := parsePositiveInt(c.readTmuxPaneOption(paneID, "@dotfiles_desktop_notification_at"))
	if lastAt <= 0 {
		return false
	}
	return c.now().Unix()-int64(lastAt) < int64(dedupeSeconds)
}

func (c *aiCommand) recordAINotification(paneID, key string) {
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, "@dotfiles_desktop_notified", "1")
	if key != "" {
		_ = c.run("tmux", "set-option", "-p", "-t", paneID, "@dotfiles_desktop_notification_key", key)
	}
	_ = c.run("tmux", "set-option", "-p", "-t", paneID, "@dotfiles_desktop_notification_at", fmt.Sprintf("%d", c.now().Unix()))
}

func (c *aiCommand) dispatchAINotification(summary, body, urgency, appName, tag, group string) error {
	if c.isWSL() {
		if err := c.dispatchWSLToast(summary, body, appName, tag, group); err == nil {
			return nil
		}
		if c.readTrimmed("command", "-v", "wsl-notify-send.exe") != "" {
			message := summary
			if body != "" {
				message += "\n" + body
			}
			if err := c.run("wsl-notify-send.exe", "--category", appName, message); err == nil {
				return nil
			}
		}
	}
	if c.readTrimmed("command", "-v", "notify-send") == "" {
		return errors.New("notify-send is unavailable")
	}
	return c.run("notify-send", "--app-name="+appName, "--icon=dialog-information", "--urgency="+urgency, summary, body)
}

func (c *aiCommand) dispatchWSLToast(summary, body, appName, tag, group string) error {
	powerShell := c.resolvePowerShell()
	if powerShell == "" {
		return errors.New("powershell.exe is unavailable")
	}
	script := buildToastPowerShell(summary, body, appName, tag, group)
	return c.run(powerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16LEBase64(script))
}

func (c *aiCommand) resolvePowerShell() string {
	if path := c.readTrimmed("command", "-v", "powershell.exe"); path != "" {
		return path
	}
	for _, candidate := range []string{
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe",
		"/mnt/c/Windows/system32/WindowsPowerShell/v1.0/powershell.exe",
	} {
		if isExecutable(candidate) {
			return candidate
		}
	}
	return ""
}

func (c *aiCommand) isWSL() bool {
	if c.env("WSL_DISTRO_NAME") != "" {
		return true
	}
	content, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(content)), "microsoft")
}

func (c *aiCommand) gitBranchForPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if _, err := c.read("git", "-C", path, "rev-parse", "--is-inside-work-tree"); err != nil {
		return ""
	}
	if branch := c.readTrimmed("git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD"); branch != "" {
		return branch
	}
	return c.readTrimmed("git", "-C", path, "rev-parse", "--short", "HEAD")
}

func (c *aiCommand) readAIWatchSnapshot(paneID string) (title, state, ack string) {
	const delim = "__DOTFILES_TMUX_AI_SEP__"
	snapshot := c.readTrimmed("tmux", "display-message", "-p", "-t", paneID, "#{pane_title}"+delim+"#{"+attentionStateOption+"}"+delim+"#{"+attentionAckOption+"}")
	title, rest, ok := strings.Cut(snapshot, delim)
	if !ok {
		return snapshot, "", ""
	}
	state, ack, _ = strings.Cut(rest, delim)
	return title, state, ack
}

func (c *aiCommand) watchInterval() time.Duration {
	value := strings.TrimSpace(c.env("DOTFILES_CODEX_TITLE_WATCH_INTERVAL"))
	if value == "" {
		return 400 * time.Millisecond
	}
	if strings.ContainsAny(value, "hmsuµns") {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	parts := strings.SplitN(value, ".", 2)
	seconds := parsePositiveInt(parts[0])
	millis := 0
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		for len(frac) < 3 {
			frac += "0"
		}
		millis = parsePositiveInt(frac)
	}
	d := time.Duration(seconds)*time.Second + time.Duration(millis)*time.Millisecond
	if d <= 0 {
		return 400 * time.Millisecond
	}
	return d
}

func (c *aiCommand) watchSettleLoops() int {
	loops := parsePositiveInt(c.env("DOTFILES_CODEX_REPLY_SETTLE_LOOPS"))
	if loops <= 0 {
		return 3
	}
	return loops
}

func (c *aiCommand) sleepFor(d time.Duration) {
	if c.sleep == nil {
		time.Sleep(d)
		return
	}
	c.sleep(d)
}

func parseAISplitDirection(args []string, command string, stderr io.Writer) (string, error) {
	direction := "right"
	switch len(args) {
	case 0:
	case 1:
		direction = strings.TrimSpace(args[0])
	default:
		printAIUsage(stderr)
		return "", fmt.Errorf("%s accepts at most 1 [right|down] argument", command)
	}
	switch direction {
	case "right", "down":
		return direction, nil
	default:
		printAIUsage(stderr)
		return "", fmt.Errorf("%s direction must be right or down", command)
	}
}

func trimAIStatePrefix(title string) string {
	title = strings.TrimLeft(title, " \t")
	if title == "" {
		return ""
	}
	r, size := utf8DecodeRune(title)
	if (r >= 0x2800 && r <= 0x28ff) || r == '✳' || r == '✔' {
		return strings.TrimLeft(title[size:], " \t")
	}
	return title
}

func normalizeAITitle(title string) string {
	return strings.ToLower(trimAIStatePrefix(title))
}

func displayAITopic(title string) string {
	topic := trimAIStatePrefix(title)
	topic = strings.TrimPrefix(topic, "codex:")
	topic = strings.TrimPrefix(topic, "Codex:")
	return topic
}

func aiReplyKindForTitle(title string) string {
	normalized := normalizeAITitle(title)
	switch {
	case strings.Contains(normalized, "approval") || strings.Contains(normalized, "approve") || strings.Contains(normalized, "permission") || strings.Contains(normalized, "allow"):
		return "approval_required"
	case strings.Contains(normalized, "select") || strings.Contains(normalized, "choice") || strings.Contains(normalized, "pick") || strings.Contains(normalized, "which"):
		return "selection_required"
	case strings.Contains(normalized, "confirm") || strings.Contains(normalized, "confirmation"):
		return "confirmation_required"
	case strings.Contains(normalized, "waiting for input") || strings.Contains(normalized, "input") || strings.Contains(normalized, "answer") || (strings.Contains(normalized, "reply") && !strings.Contains(normalized, "response")):
		return "input_required"
	default:
		return "response_ready"
	}
}

func aiSummaryForKind(kind, topic string) string {
	summary := "응답 완료"
	switch kind {
	case "approval_required":
		summary = "승인 필요"
	case "selection_required":
		summary = "선택 필요"
	case "confirmation_required":
		summary = "확인 필요"
	case "input_required":
		summary = "입력 필요"
	}
	if strings.TrimSpace(topic) != "" {
		summary += " · " + topic
	}
	return summary
}

func aiUrgencyForKind(kind string) string {
	switch kind {
	case "approval_required", "selection_required", "confirmation_required", "input_required":
		return "critical"
	default:
		return "normal"
	}
}

func aiNotificationBody(project, branch, sessionName, windowName string) string {
	projectPart := ""
	switch {
	case project != "" && branch != "":
		projectPart = project + "/" + branch
	case project != "":
		projectPart = project
	case branch != "":
		projectPart = branch
	}
	location := ""
	if sessionName != "" || windowName != "" {
		location = "tmux " + sessionName + ":" + windowName
	}
	switch {
	case projectPart != "" && location != "":
		return projectPart + " · " + location
	case projectPart != "":
		return projectPart
	default:
		return location
	}
}

func aiNotificationKey(kind, title string) string {
	return kind + "|" + normalizeAITitle(displayAITopic(title))
}

func isAIBusyTitle(title string) bool {
	if title == "" {
		return false
	}
	r, _ := utf8DecodeRune(strings.TrimLeft(title, " \t"))
	if r >= 0x2800 && r <= 0x28ff {
		return true
	}
	normalized := normalizeAITitle(title)
	return strings.Contains(normalized, "thinking") ||
		strings.Contains(normalized, "responding") ||
		strings.Contains(normalized, "running") ||
		strings.Contains(normalized, "working") ||
		strings.Contains(normalized, "streaming") ||
		strings.Contains(normalized, "generating")
}

func isAIReplyTitle(title string) bool {
	if title == "" {
		return false
	}
	trimmed := strings.TrimLeft(title, " \t")
	if strings.HasPrefix(trimmed, "✳") || strings.HasPrefix(trimmed, "✔") {
		return true
	}
	normalized := normalizeAITitle(title)
	return (strings.Contains(normalized, "response") && !strings.Contains(normalized, "responding")) ||
		strings.Contains(normalized, "reply") ||
		strings.Contains(normalized, "response needed") ||
		strings.Contains(normalized, "waiting for input") ||
		strings.Contains(normalized, "waiting") ||
		strings.Contains(normalized, "complete") ||
		strings.Contains(normalized, "completed") ||
		strings.Contains(normalized, "done") ||
		strings.Contains(normalized, "idle")
}

func aiAttentionMismatch(nextState, attentionState string) bool {
	switch nextState {
	case "thinking":
		return attentionState != attentionStateBusy
	case "waiting":
		return attentionState != attentionStateReply
	default:
		return attentionState != ""
	}
}

func buildToastPowerShell(summary, body, appName, tag, group string) string {
	tagLine := ""
	if tag != "" {
		tagLine = "$toast.Tag = '" + psEscape(truncate64(tag)) + "'"
	}
	groupLine := ""
	if group != "" {
		groupLine = "$toast.Group = '" + psEscape(truncate64(group)) + "'"
	}
	return `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null
$tpl = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$nodes = $tpl.GetElementsByTagName('text')
[void]$nodes[0].AppendChild($tpl.CreateTextNode('` + psEscape(summary) + `'))
[void]$nodes[1].AppendChild($tpl.CreateTextNode('` + psEscape(body) + `'))
$toast = [Windows.UI.Notifications.ToastNotification]::new($tpl)
` + tagLine + `
` + groupLine + `
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + psEscape(appName) + `').Show($toast)
`
}

func psEscape(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func truncate64(value string) string {
	runes := []rune(value)
	if len(runes) <= 64 {
		return value
	}
	return string(runes[:64])
}

func encodeUTF16LEBase64(value string) string {
	runes := utf16.Encode([]rune(value))
	bytes := make([]byte, len(runes)*2)
	for i, r := range runes {
		binary.LittleEndian.PutUint16(bytes[i*2:], r)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func utf8DecodeRune(value string) (rune, int) {
	for _, r := range value {
		return r, len(string(r))
	}
	return 0, 0
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func normalizeAIMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case aiModeClaude, aiModeCodex, aiModeSelective, aiModeShell:
		return strings.TrimSpace(mode)
	default:
		return aiModeSelective
	}
}

func runExternalCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isNoSelectionExit(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			switch status.ExitStatus() {
			case 1, 129, 130:
				return true
			}
		}
	}
	message := err.Error()
	return strings.Contains(message, "exit status 1") ||
		strings.Contains(message, "exit status 129") ||
		strings.Contains(message, "exit status 130")
}

func readExternalCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "command" && len(args) >= 2 && args[0] == "-v" {
		path, err := exec.LookPath(args[1])
		if err != nil {
			return nil, err
		}
		return []byte(path + "\n"), nil
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func shellEnv(name, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return name + "=" + shellQuote(value) + " "
}

func ansiDim(value string) string {
	return "\x1b[90m" + value + "\x1b[0m"
}

func isDir(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func firstExecutable(paths ...string) string {
	for _, path := range paths {
		if isExecutable(path) {
			return path
		}
	}
	return ""
}

func newestExecutable(paths []string) string {
	var newestPath string
	var newestMod time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if newestPath == "" || info.ModTime().After(newestMod) {
			newestPath = path
			newestMod = info.ModTime()
		}
	}
	return newestPath
}

func parsePositiveInt(value string) int {
	n := 0
	for _, r := range strings.TrimSpace(value) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func printAIUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux ai split [right|down]")
	fmt.Fprintln(w, "  projmux ai picker [--inside] [--shell] [right|down]")
	fmt.Fprintln(w, "  projmux ai settings [--get|--set <mode>]")
	fmt.Fprintln(w, "  projmux ai status set <thinking|waiting|idle> [pane]")
	fmt.Fprintln(w, "  projmux ai notify [notify|reset] [pane]")
	fmt.Fprintln(w, "  projmux ai watch-title [pane]")
}
