package sessionstate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	antigravityagent "github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
	claudeagent "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	codexagent "github.com/crevissepartners/projmux/internal/integrations/agents/codex"
)

// Runner is the command execution surface used by replay. It intentionally
// matches the tmux command runner shape used elsewhere without importing tmux.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ReplayOptions controls cwd fallback behavior while restoring a snapshot.
type ReplayOptions struct {
	// FallbackCWD is used when a stored cwd no longer exists. When empty or
	// unusable, panes fall back to the snapshot default cwd and then $HOME.
	FallbackCWD string

	// AgentBinaryResolver overrides agent executable discovery for tests.
	AgentBinaryResolver func(agent string) string

	// CommandShell overrides the POSIX shell used for direct-start wrappers.
	CommandShell string
}

// ReplayResult reports non-fatal restore decisions.
type ReplayResult struct {
	Warnings []ReplayWarning
}

// ReplayWarning describes a cwd fallback used during restore.
type ReplayWarning struct {
	Scope       string
	WindowIndex int
	PaneIndex   int
	CWD         string
	FallbackCWD string
	Reason      string
}

// Replay creates a detached tmux session and replays its saved window, pane,
// and layout metadata. It does not restore live shell processes or dump/replay
// environment variables, including secrets. Agent recipe replay is limited to
// adapters with explicit safe command generation.
func Replay(ctx context.Context, runner Runner, snap Snapshot, opts ReplayOptions) (ReplayResult, error) {
	return replay(ctx, runner, snap, opts, replayOptions{replayPaneRecipes: true, directStartAgents: true})
}

type replayOptions struct {
	replayPaneRecipes bool
	directStartAgents bool
}

func replay(ctx context.Context, runner Runner, snap Snapshot, opts ReplayOptions, replayOpts replayOptions) (ReplayResult, error) {
	var result ReplayResult
	if runner == nil {
		return result, fmt.Errorf("sessionstate: replay runner is required")
	}
	if err := snap.Validate(); err != nil {
		return result, err
	}

	resolver, err := newCWDResolver(opts, snap.DefaultCWD)
	if err != nil {
		return result, err
	}
	agentLauncher := newAgentLaunchResolver(opts)

	windows := sortedWindows(snap.Windows)
	sessionCWD := resolver.resolveSession(snap.DefaultCWD, &result)
	firstWindowName := ""
	createCWD := sessionCWD
	if len(windows) > 0 {
		firstWindowName = windows[0].Name
		if panes := sortedPanes(windows[0].Panes); len(panes) > 0 {
			createCWD = resolver.resolvePane(panes[0].CWD, windows[0].Index, panes[0].Index, &result)
		}
	}

	args := []string{"new-session", "-d", "-s", snap.Session, "-c", createCWD}
	if len(windows) > 0 {
		if panes := sortedPanes(windows[0].Panes); len(panes) > 0 {
			args = append(args, replayPaneLaunchTail(agentLauncher, windows[0], panes[0], createCWD, &result, replayOpts)...)
		}
	}
	if _, err := runner.Run(ctx, "tmux", args...); err != nil {
		return result, fmt.Errorf("replay tmux session %q: %w", snap.Session, err)
	}
	if len(windows) == 0 {
		return result, nil
	}
	if strings.TrimSpace(firstWindowName) != "" {
		if _, err := runner.Run(ctx, "tmux", "rename-window", "-t", windowTarget(snap.Session, windows[0].Index), firstWindowName); err != nil {
			return result, fmt.Errorf("replay tmux window %d name: %w", windows[0].Index, err)
		}
	}

	for windowOffset, window := range windows {
		panes := sortedPanes(window.Panes)
		windowCWD := sessionCWD
		if len(panes) > 0 {
			if windowOffset == 0 {
				windowCWD = createCWD
			} else {
				windowCWD = resolver.resolvePane(panes[0].CWD, window.Index, panes[0].Index, &result)
			}
		}

		if windowOffset > 0 {
			args := []string{"new-window", "-d", "-t", windowTarget(snap.Session, window.Index), "-c", windowCWD}
			if strings.TrimSpace(window.Name) != "" {
				args = append(args, "-n", window.Name)
			}
			if len(panes) > 0 {
				args = append(args, replayPaneLaunchTail(agentLauncher, window, panes[0], windowCWD, &result, replayOpts)...)
			}
			if _, err := runner.Run(ctx, "tmux", args...); err != nil {
				return result, fmt.Errorf("replay tmux window %d: %w", window.Index, err)
			}
		}

		for paneOffset := 1; paneOffset < len(panes); paneOffset++ {
			pane := panes[paneOffset]
			cwd := resolver.resolvePane(pane.CWD, window.Index, pane.Index, &result)
			targetPane := panes[paneOffset-1].Index
			args := []string{"split-window", "-d", "-t", paneTarget(snap.Session, window.Index, targetPane), "-c", cwd}
			args = append(args, replayPaneLaunchTail(agentLauncher, window, pane, cwd, &result, replayOpts)...)
			if _, err := runner.Run(ctx, "tmux", args...); err != nil {
				return result, fmt.Errorf("replay tmux window %d pane %d: %w", window.Index, pane.Index, err)
			}
		}

		if strings.TrimSpace(window.Layout) != "" {
			if _, err := runner.Run(ctx, "tmux", "select-layout", "-t", windowTarget(snap.Session, window.Index), window.Layout); err != nil {
				return result, fmt.Errorf("replay tmux window %d layout: %w", window.Index, err)
			}
		}
		if len(panes) > 0 {
			if _, err := runner.Run(ctx, "tmux", "select-pane", "-t", paneTarget(snap.Session, window.Index, window.ActivePaneIndex)); err != nil {
				return result, fmt.Errorf("replay tmux window %d active pane %d: %w", window.Index, window.ActivePaneIndex, err)
			}
		}
		if replayOpts.replayPaneRecipes {
			if err := replayPaneRecipes(ctx, runner, snap.Session, window, panes, &result, replayOpts); err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

func replayPaneRecipes(ctx context.Context, runner Runner, session string, window Window, panes []Pane, result *ReplayResult, replayOpts replayOptions) error {
	for _, pane := range panes {
		if replayOpts.directStartAgents && pane.Recipe.Kind == RecipeKindAgent {
			continue
		}
		if err := replayPaneRecipe(ctx, runner, paneTarget(session, window.Index, pane.Index), window.Index, pane, result); err != nil {
			return err
		}
	}
	return nil
}

func replayPaneRecipe(ctx context.Context, runner Runner, target string, windowIndex int, pane Pane, result *ReplayResult) error {
	switch pane.Recipe.Kind {
	case RecipeKindAgent:
		return replayAgentRecipe(ctx, runner, target, windowIndex, pane, result)
	case RecipeKindStartup:
		return replayStartupRecipe(ctx, runner, target, windowIndex, pane)
	default:
		return nil
	}
}

type agentLaunchResolver struct {
	binaryResolver func(agent string) string
	commandShell   string
}

func newAgentLaunchResolver(opts ReplayOptions) agentLaunchResolver {
	resolver := opts.AgentBinaryResolver
	if resolver == nil {
		resolver = findAgentBinary
	}
	shell := strings.TrimSpace(opts.CommandShell)
	if shell == "" {
		shell = commandShell()
	}
	return agentLaunchResolver{binaryResolver: resolver, commandShell: shell}
}

func replayPaneLaunchTail(launcher agentLaunchResolver, window Window, pane Pane, cwd string, result *ReplayResult, replayOpts replayOptions) []string {
	if !replayOpts.directStartAgents || !replayOpts.replayPaneRecipes || pane.Recipe.Kind != RecipeKindAgent {
		return nil
	}
	agent := strings.ToLower(strings.TrimSpace(pane.Recipe.Agent))
	var resumeArgs []string
	var err error
	if !aiprovider.SessionStateSupported(agent) {
		return nil
	}
	switch agent {
	case antigravityagent.AgentName:
		resumeArgs, err = antigravityagent.ResumeArgs(pane.Recipe.ResumeID)
	case claudeagent.AgentName:
		resumeArgs, err = claudeagent.ResumeArgs(pane.Recipe.ResumeID)
	case codexagent.AgentName:
		resumeArgs, err = codexagent.ResumeArgs(pane.Recipe.ResumeID)
	default:
		return nil
	}
	if err != nil {
		appendReplayWarning(result, ReplayWarning{
			Scope:       "agent",
			WindowIndex: window.Index,
			PaneIndex:   pane.Index,
			Reason:      err.Error(),
		})
		return nil
	}
	agentBin := launcher.binaryResolver(agent)
	if agentBin == "" {
		appendReplayWarning(result, ReplayWarning{
			Scope:       "agent",
			WindowIndex: window.Index,
			PaneIndex:   pane.Index,
			Reason:      "missing " + agent + " binary",
		})
		return nil
	}
	title := strings.TrimSpace(pane.Recipe.Topic)
	if title == "" {
		title = agent
	}
	command := agentLaunchCommand(agent, agentBin, cwd, title, resumeArgs[1:])
	return []string{launcher.commandShell, "-lc", command}
}

func agentLaunchCommand(agent, agentBin, cwd, title string, argv []string) string {
	titleVar := "__" + agent + "_title"
	parts := []string{}
	if binDir := filepath.Dir(agentBin); binDir != "" && binDir != "." && binDir != "/" {
		parts = append(parts, "export PATH="+shellQuote(binDir)+`":$PATH"`)
	}
	if cwd != "" {
		parts = append(parts, "cd "+shellQuote(cwd))
	}
	parts = append(parts,
		titleVar+"="+shellQuote(title),
		`printf '\033]0;%s\007' "$`+titleVar+`"`,
		`if [ -n "${TMUX:-}" ]; then tmux select-pane -T "$`+titleVar+`" >/dev/null 2>&1 || true; fi`,
		"exec "+shellJoin(append([]string{agentBin}, argv...)),
	)
	return strings.Join(parts, " && ")
}

func replayAgentRecipe(ctx context.Context, runner Runner, target string, windowIndex int, pane Pane, result *ReplayResult) error {
	agent := strings.ToLower(strings.TrimSpace(pane.Recipe.Agent))
	var command string
	var err error
	if !aiprovider.SessionStateSupported(agent) {
		return nil
	}
	switch agent {
	case antigravityagent.AgentName:
		command, err = antigravityagent.ResumeCommand(pane.Recipe.ResumeID)
	case claudeagent.AgentName:
		command, err = claudeagent.ResumeCommand(pane.Recipe.ResumeID)
	case codexagent.AgentName:
		command, err = codexagent.ResumeCommand(pane.Recipe.ResumeID)
	default:
		return nil
	}
	if err != nil {
		appendReplayWarning(result, ReplayWarning{
			Scope:       "agent",
			WindowIndex: windowIndex,
			PaneIndex:   pane.Index,
			Reason:      err.Error(),
		})
		return nil
	}
	if _, err := runner.Run(ctx, "tmux", "send-keys", "-t", target, command, "Enter"); err != nil {
		return fmt.Errorf("replay tmux window %d pane %d %s resume: %w", windowIndex, pane.Index, agent, err)
	}
	return nil
}

func replayStartupRecipe(ctx context.Context, runner Runner, target string, windowIndex int, pane Pane) error {
	command := strings.TrimSpace(pane.Recipe.Command)
	if command == "" {
		return nil
	}
	if _, err := runner.Run(ctx, "tmux", "send-keys", "-t", target, command, "Enter"); err != nil {
		return fmt.Errorf("replay tmux window %d pane %d startup: %w", windowIndex, pane.Index, err)
	}
	return nil
}

func appendReplayWarning(result *ReplayResult, warning ReplayWarning) {
	if result == nil {
		return
	}
	result.Warnings = append(result.Warnings, warning)
}

func findAgentBinary(agent string) string {
	binName := strings.TrimSpace(agent)
	provider, ok := aiprovider.Lookup(binName)
	if !ok || !provider.SessionState.Supported || strings.TrimSpace(provider.BinaryName) == "" {
		return ""
	}
	binName = provider.BinaryName

	home, _ := os.UserHomeDir()
	if path := firstExecutable(
		lookPath(binName),
		filepath.Join(home, ".npm-global", "bin", binName),
		filepath.Join(home, ".local", "bin", binName),
	); path != "" {
		return path
	}
	if path := newestExecutable(nodeManagerCandidates(home, binName)); path != "" {
		return path
	}
	if provider.ID == aiprovider.Codex {
		matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-*", "bin", "*", "codex"))
		return newestExecutable(matches)
	}
	return ""
}

func commandShell() string {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" || !filepath.IsAbs(shell) || strings.ContainsAny(shell, "\x00\r\n") {
		return "/bin/sh"
	}
	switch filepath.Base(shell) {
	case "bash", "dash", "ksh", "mksh", "sh", "zsh":
		return shell
	default:
		return "/bin/sh"
	}
}

func lookPath(binName string) string {
	path, err := exec.LookPath(binName)
	if err != nil {
		return ""
	}
	return path
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
	var newest string
	var newestModTime int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		modTime := info.ModTime().UnixNano()
		if newest == "" || modTime > newestModTime {
			newest = path
			newestModTime = modTime
		}
	}
	return newest
}

func nodeManagerCandidates(home, binName string) []string {
	if home == "" || binName == "" {
		return nil
	}
	var candidates []string
	globs := []string{
		filepath.Join(home, ".nvm", "versions", "node", "*", "bin", binName),
		filepath.Join(home, ".fnm", "node-versions", "*", "installation", "bin", binName),
		filepath.Join(home, ".asdf", "installs", "nodejs", "*", "bin", binName),
	}
	for _, pattern := range globs {
		matches, _ := filepath.Glob(pattern)
		candidates = append(candidates, matches...)
	}
	candidates = append(candidates, filepath.Join(home, ".volta", "bin", binName))
	return candidates
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type cwdResolver struct {
	fallbackCWD string
	defaultCWD  string
	homeCWD     string
}

func newCWDResolver(opts ReplayOptions, defaultCWD string) (cwdResolver, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return cwdResolver{}, fmt.Errorf("sessionstate: resolve fallback home: %w", err)
	}
	resolver := cwdResolver{homeCWD: home}
	if isExistingDir(opts.FallbackCWD) {
		resolver.fallbackCWD = opts.FallbackCWD
	}
	if isExistingDir(defaultCWD) {
		resolver.defaultCWD = defaultCWD
	}
	return resolver, nil
}

func (r cwdResolver) resolveSession(cwd string, result *ReplayResult) string {
	return r.resolve(cwd, "session", -1, -1, []string{r.fallbackCWD, r.homeCWD}, result)
}

func (r cwdResolver) resolvePane(cwd string, windowIndex, paneIndex int, result *ReplayResult) string {
	return r.resolve(cwd, "pane", windowIndex, paneIndex, []string{r.fallbackCWD, r.defaultCWD, r.homeCWD}, result)
}

func (r cwdResolver) resolve(cwd, scope string, windowIndex, paneIndex int, fallbacks []string, result *ReplayResult) string {
	if isExistingDir(cwd) {
		return cwd
	}

	fallback := r.homeCWD
	for _, candidate := range fallbacks {
		if isExistingDir(candidate) {
			fallback = candidate
			break
		}
	}
	if result != nil && strings.TrimSpace(cwd) != "" {
		result.Warnings = append(result.Warnings, ReplayWarning{
			Scope:       scope,
			WindowIndex: windowIndex,
			PaneIndex:   paneIndex,
			CWD:         cwd,
			FallbackCWD: fallback,
			Reason:      "cwd does not exist",
		})
	}
	return fallback
}

func isExistingDir(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func sortedWindows(windows []Window) []Window {
	out := append([]Window(nil), windows...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Index < out[j].Index
	})
	return out
}

func sortedPanes(panes []Pane) []Pane {
	out := append([]Pane(nil), panes...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Index < out[j].Index
	})
	return out
}

func windowTarget(session string, windowIndex int) string {
	return session + ":" + strconv.Itoa(windowIndex)
}

func paneTarget(session string, windowIndex, paneIndex int) string {
	return windowTarget(session, windowIndex) + "." + strconv.Itoa(paneIndex)
}
