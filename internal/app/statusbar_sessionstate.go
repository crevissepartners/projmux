package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

type statusbarSessionStateView struct {
	Session     string
	Source      string
	Autosave    sessionStateEffectiveToggle
	Autorestore sessionStateEffectiveToggle
	Snapshot    sessionstate.Snapshot
	SessionErr  error
	StoreErr    error
	LoadErr     error
}

type statusbarSessionStatePopupView struct {
	Title   string
	Toast   string
	Command string
	Width   int
	Height  int
}

func (c *statusbarCommand) handleSessionState(opts statusbarClickOptions, _ io.Writer, stderr io.Writer) error {
	ctx := context.Background()
	state := c.loadSessionStateView(ctx)
	binaryPath, binErr := c.resolveBinary()
	if binErr != nil {
		fmt.Fprintf(stderr, "statusbar sessionstate: resolve projmux binary for popup actions: %v\n", binErr)
		return c.runTmux(stderr, "display-message", statusbarSessionStateToast(state))
	}
	popup := statusbarSessionStateActionPopup(state, binaryPath)
	args := []string{
		"display-popup",
		"-E",
		"-B",
		"-w", strconv.Itoa(popup.Width),
		"-h", strconv.Itoa(popup.Height),
	}
	if client := strings.TrimSpace(opts.ClientTTY); client != "" {
		args = append(args, "-c", client)
	}
	args = append(args, popup.Command)
	if err := c.runTmuxNoFallback(stderr,
		args...,
	); err == nil {
		return nil
	}
	return c.runTmux(stderr, "display-message", popup.Toast)
}

func (c *statusbarCommand) loadSessionStateView(ctx context.Context) statusbarSessionStateView {
	state := statusbarSessionStateView{
		Autosave:    sessionStateToggleState(c.homeDir, c.lookupEnv, sessionStateAutosaveEnv, func(paths config.Paths) string { return paths.SessionStateAutosaveFile() }),
		Autorestore: sessionStateToggleState(c.homeDir, c.lookupEnv, sessionStateAutorestoreEnv, func(paths config.Paths) string { return paths.SessionStateAutorestoreFile() }),
	}
	sessionName, err := c.currentStatusbarSessionName(ctx)
	if err != nil {
		state.SessionErr = err
		return state
	}
	state.Session = sessionName
	state.Source = c.liveSessionStateSource(ctx, sessionName)
	store, err := c.statusbarSessionStateStore()
	if err != nil {
		state.StoreErr = err
		return state
	}
	snap, err := store.Load(sessionName)
	if err != nil {
		state.LoadErr = err
		return state
	}
	state.Snapshot = snap
	if strings.TrimSpace(state.Source) == "" {
		state.Source = snap.SourceLabel()
	}
	return state
}

func (c *statusbarCommand) liveSessionStateSource(ctx context.Context, sessionName string) string {
	if c.runner == nil || strings.TrimSpace(sessionName) == "" {
		return ""
	}
	return inttmux.NewClient(c.runner).SessionStateSource(ctx, sessionName)
}

func (c *statusbarCommand) currentStatusbarSessionName(ctx context.Context) (string, error) {
	if c.runner == nil {
		return "", errors.New("runner unavailable")
	}
	out, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "#{session_name}")
	if err != nil {
		return "", fmt.Errorf("resolve current tmux session: %w", err)
	}
	sessionName := strings.TrimSpace(string(out))
	if sessionName == "" {
		return "", errors.New("current tmux session unavailable")
	}
	return sessionName, nil
}

func (c *statusbarCommand) statusbarSessionStateStore() (sessionstate.Store, error) {
	if c.sessionStoreFn != nil {
		return c.sessionStoreFn()
	}
	paths, err := statusbarConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return sessionstate.Store{}, err
	}
	return sessionstate.NewStore(paths.SessionStateDir()), nil
}

func statusbarSessionStatePopup(state statusbarSessionStateView, now time.Time, binaryPath string) statusbarSessionStatePopupView {
	const width = 104
	title := "Session State"
	innerLayout := projmuxpicker.DefaultRenderer().ContentLayoutWithTitle(
		projmuxpicker.Layout{Rows: 0, Cols: width}, title,
	)
	lines := statusbarSessionStatePopupLines(state, now, innerLayout.Cols)
	bodyContent := strings.Join(lines, "\n")
	height := max(len(lines), 1) + 4
	outerLayout := projmuxpicker.Layout{Rows: height, Cols: width}

	var framed strings.Builder
	projmuxpicker.DefaultRenderer().RenderFrameWithTitle(&framed, bodyContent, title, outerLayout)
	payload := strings.TrimRight(framed.String(), "\n") + "\n"
	return statusbarSessionStatePopupView{
		Title:   title,
		Toast:   statusbarSessionStateToast(state),
		Command: statusbarPopupCommand(payload, binaryPath),
		Width:   width,
		Height:  height,
	}
}

func statusbarSessionStateActionPopup(state statusbarSessionStateView, binaryPath string) statusbarSessionStatePopupView {
	return statusbarSessionStatePopupView{
		Title:   "Session State",
		Toast:   statusbarSessionStateToast(state),
		Command: tmuxShellQuote(binaryPath) + " session-state popup",
		Width:   104,
		Height:  30,
	}
}

func statusbarSessionStatePopupLines(state statusbarSessionStateView, now time.Time, cols int) []string {
	lines := []string{
		dimANSI("Saved tmux layout and replay recipe preview."),
		"",
	}
	session := state.Session
	if session == "" {
		session = "-"
	}
	lines = append(lines, statusbarFieldLines("session", session, cols)...)
	lines = append(lines, statusbarFieldLines("source", statusbarSessionStateSourceText(state), cols)...)
	lines = append(lines, statusbarFieldLines("auto-save", statusbarSessionStateToggleText(state.Autosave), cols)...)
	lines = append(lines, statusbarFieldLines("startup picker", statusbarSessionStateToggleText(state.Autorestore), cols)...)

	switch {
	case state.SessionErr != nil:
		lines = append(lines, "")
		lines = append(lines, statusbarFieldLines("snapshot", "unavailable - "+statusbarSessionStateErrorSummary(state.SessionErr), cols)...)
	case state.StoreErr != nil:
		lines = append(lines, "")
		lines = append(lines, statusbarFieldLines("snapshot", "store unavailable - "+statusbarSessionStateErrorSummary(state.StoreErr), cols)...)
	case state.LoadErr != nil:
		status := "invalid - " + statusbarSessionStateErrorSummary(state.LoadErr)
		if errors.Is(state.LoadErr, sessionstate.ErrNotFound) {
			status = "missing"
		}
		lines = append(lines, "")
		lines = append(lines, statusbarFieldLines("snapshot", status, cols)...)
	default:
		snap := state.Snapshot
		lines = append(lines, "")
		lines = append(lines, statusbarFieldLines("saved", statusbarSessionStateSavedText(snap.SavedAt, now), cols)...)
		lines = append(lines, statusbarFieldLines("windows", fmt.Sprintf("%d", len(snap.Windows)), cols)...)
		lines = append(lines, statusbarFieldLines("panes", fmt.Sprintf("%d", statusbarSessionStatePaneCount(snap)), cols)...)
		if strings.TrimSpace(snap.DefaultCWD) != "" {
			lines = append(lines, statusbarFieldLines("default cwd", snap.DefaultCWD, cols)...)
		}
		lines = append(lines, "", projmuxpicker.SeparatorLine(cols), dimANSI("Preview"))
		lines = append(lines, statusbarSessionStatePreviewLines(snap, cols)...)
	}
	lines = append(lines, statusbarPopupFooterLines(cols)...)
	return lines
}

func statusbarSessionStateToggleText(toggle sessionStateEffectiveToggle) string {
	mode := strings.TrimSpace(string(toggle.Mode))
	if mode == "" {
		mode = string(config.SessionStateToggleOn)
	}
	source := strings.TrimSpace(toggle.Source)
	if source == "" {
		return mode
	}
	return mode + " (" + source + ")"
}

func statusbarSessionStateSourceText(state statusbarSessionStateView) string {
	if source := strings.TrimSpace(state.Source); source != "" {
		return source
	}
	if state.LoadErr == nil {
		return state.Snapshot.SourceLabel()
	}
	return sessionstate.SourceAutosave
}

func statusbarSessionStateSavedText(savedAt, now time.Time) string {
	if savedAt.IsZero() {
		return "-"
	}
	text := savedAt.Local().Format("2006-01-02 15:04:05 MST")
	age := statusbarSessionStateAge(savedAt, now)
	if age >= time.Second {
		text += " (" + formatBackoffDuration(age.Round(time.Second)) + " ago)"
	}
	return text
}

func statusbarSessionStateAge(savedAt, now time.Time) time.Duration {
	if savedAt.IsZero() || now.IsZero() || savedAt.After(now) {
		return 0
	}
	return now.Sub(savedAt)
}

func statusbarSessionStatePaneCount(snap sessionstate.Snapshot) int {
	count := 0
	for _, window := range snap.Windows {
		count += len(window.Panes)
	}
	return count
}

func statusbarSessionStatePreviewLines(snap sessionstate.Snapshot, cols int) []string {
	const (
		maxWindows = 8
		maxPanes   = 18
	)
	windows := snap.Windows
	lines := make([]string, 0, len(windows)+statusbarSessionStatePaneCount(snap))
	panesSeen := 0
	for wi, window := range windows {
		if wi >= maxWindows {
			lines = append(lines, dimANSI(fmt.Sprintf("... %d more windows", len(windows)-wi)))
			break
		}
		name := statusbarSessionStateClean(window.Name)
		if name == "" {
			name = "window"
		}
		lines = append(lines, statusbarSessionStateClip(fmt.Sprintf("window %d  %s  (%d panes)", window.Index, name, len(window.Panes)), cols))
		for _, pane := range window.Panes {
			if panesSeen >= maxPanes {
				remaining := statusbarSessionStatePaneCount(snap) - panesSeen
				if remaining > 0 {
					lines = append(lines, dimANSI(fmt.Sprintf("  ... %d more panes", remaining)))
				}
				return lines
			}
			panesSeen++
			lines = append(lines, statusbarSessionStateClip("  "+statusbarSessionStatePanePreview(window.Index, pane), cols))
		}
	}
	if len(lines) == 0 {
		return []string{dimANSI("No windows recorded.")}
	}
	return lines
}

func statusbarSessionStatePanePreview(windowIndex int, pane sessionstate.Pane) string {
	recipe := pane.Recipe
	kind := strings.TrimSpace(recipe.Kind)
	if kind == "" {
		kind = "unknown"
	}
	detail := ""
	switch kind {
	case sessionstate.RecipeKindAgent:
		detail = strings.TrimSpace(recipe.Agent)
		if resumeID := strings.TrimSpace(recipe.ResumeID); resumeID != "" {
			detail += " resume " + resumeID
		}
		if topic := strings.TrimSpace(recipe.Topic); topic != "" {
			detail += " topic " + topic
		}
	case sessionstate.RecipeKindStartup:
		detail = strings.TrimSpace(recipe.Command)
	case sessionstate.RecipeKindShell:
		detail = filepath.Base(strings.TrimSpace(pane.CWD))
	}
	if detail == "." || detail == string(filepath.Separator) {
		detail = strings.TrimSpace(pane.CWD)
	}
	detail = statusbarSessionStateClean(detail)
	if detail == "" {
		return fmt.Sprintf("pane %d.%d  %s", windowIndex, pane.Index, kind)
	}
	return fmt.Sprintf("pane %d.%d  %s  %s", windowIndex, pane.Index, kind, detail)
}

func statusbarSessionStateToast(state statusbarSessionStateView) string {
	switch {
	case state.SessionErr != nil:
		return "session state unavailable"
	case state.StoreErr != nil:
		return "session state store unavailable"
	case state.LoadErr != nil:
		if errors.Is(state.LoadErr, sessionstate.ErrNotFound) {
			return "session state: no snapshot"
		}
		return "session state snapshot invalid"
	default:
		return fmt.Sprintf("session state: %s, %d windows, %d panes", statusbarSessionStateSourceText(state), len(state.Snapshot.Windows), statusbarSessionStatePaneCount(state.Snapshot))
	}
}

func statusbarSessionStateErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "unknown"
	}
	return statusbarSessionStateClip(msg, 88)
}

func statusbarSessionStateClean(value string) string {
	value = strings.TrimSpace(value)
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func statusbarSessionStateClip(value string, cols int) string {
	value = statusbarSessionStateClean(value)
	if cols <= 0 || projmuxpicker.VisibleLen(value) <= cols {
		return value
	}
	const suffix = "..."
	limit := max(cols-projmuxpicker.VisibleLen(suffix), 1)
	var out strings.Builder
	width := 0
	for _, r := range value {
		rw := projmuxpicker.RuneWidth(r)
		if width+rw > limit {
			break
		}
		out.WriteRune(r)
		width += rw
	}
	return strings.TrimSpace(out.String()) + suffix
}
