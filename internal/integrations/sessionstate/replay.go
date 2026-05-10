package sessionstate

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	claudeagent "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
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

	if _, err := runner.Run(ctx, "tmux", "new-session", "-d", "-s", snap.Session, "-c", createCWD); err != nil {
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
			if _, err := runner.Run(ctx, "tmux", args...); err != nil {
				return result, fmt.Errorf("replay tmux window %d: %w", window.Index, err)
			}
		}

		for paneOffset := 1; paneOffset < len(panes); paneOffset++ {
			pane := panes[paneOffset]
			cwd := resolver.resolvePane(pane.CWD, window.Index, pane.Index, &result)
			targetPane := panes[paneOffset-1].Index
			if _, err := runner.Run(ctx, "tmux", "split-window", "-d", "-t", paneTarget(snap.Session, window.Index, targetPane), "-c", cwd); err != nil {
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
		for _, pane := range panes {
			if err := replayPaneRecipe(ctx, runner, snap.Session, window.Index, pane, &result); err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

func replayPaneRecipe(ctx context.Context, runner Runner, session string, windowIndex int, pane Pane, result *ReplayResult) error {
	if pane.Recipe.Kind != RecipeKindAgent {
		return nil
	}

	agent := strings.ToLower(strings.TrimSpace(pane.Recipe.Agent))
	if agent != claudeagent.AgentName {
		return nil
	}
	command, err := claudeagent.ResumeCommand(pane.Recipe.ResumeID)
	if err != nil {
		appendReplayWarning(result, ReplayWarning{
			Scope:       "agent",
			WindowIndex: windowIndex,
			PaneIndex:   pane.Index,
			Reason:      err.Error(),
		})
		return nil
	}
	if _, err := runner.Run(ctx, "tmux", "send-keys", "-t", paneTarget(session, windowIndex, pane.Index), command, "Enter"); err != nil {
		return fmt.Errorf("replay tmux window %d pane %d claude resume: %w", windowIndex, pane.Index, err)
	}
	return nil
}

func appendReplayWarning(result *ReplayResult, warning ReplayWarning) {
	if result == nil {
		return
	}
	result.Warnings = append(result.Warnings, warning)
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
