package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

const sessionsKillExpectKey = "ctrl-x"
const sessionsStateExpectKey = "ctrl-s"

type sessionsRecentResolver interface {
	RecentSessionSummaries(ctx context.Context) ([]inttmux.RecentSessionSummary, error)
}

type sessionsSelectionStore interface {
	ReadSelection(sessionName string) (selection corepreview.Selection, found bool, err error)
}

type sessionsOpener interface {
	OpenSessionTarget(ctx context.Context, sessionName, windowIndex, paneIndex string) error
}

type sessionsKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type sessionsRunner interface {
	Run(options intpickercompat.Options) (intpickercompat.Result, error)
}

type sessionsCommand struct {
	recent               sessionsRecentResolver
	store                sessionsSelectionStore
	opener               sessionsOpener
	killer               sessionsKiller
	runner               sessionsRunner
	native               intpicker.Runner
	executable           func() (string, error)
	lookupEnv            func(string) string
	homeDir              func() (string, error)
	stateStore           func() (sessionstate.Store, error)
	cleanupKilledSession func(string)
}

func newSessionsCommand() *sessionsCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})
	if usePSMuxBackend(os.Getenv, nil) {
		client := newDefaultPSMuxClient()
		return &sessionsCommand{
			recent:     client,
			store:      newSessionPopupCommand().store,
			opener:     client,
			killer:     client,
			native:     intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
			executable: os.Executable,
			lookupEnv:  os.Getenv,
			homeDir:    os.UserHomeDir,
			stateStore: sessionstate.NewDefaultStoreFromEnv,
		}
	}
	return &sessionsCommand{
		recent:     client,
		store:      newSessionPopupCommand().store,
		opener:     client,
		killer:     client,
		native:     intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		executable: os.Executable,
		lookupEnv:  os.Getenv,
		homeDir:    os.UserHomeDir,
		stateStore: sessionstate.NewDefaultStoreFromEnv,
	}
}

// Run manages the recent-session picker surface.
func (c *sessionsCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printSessionsUsage(stderr)
	}

	ui := fs.String(switchUIFlag, switchUIPopup, "recent-session surface to prepare")
	if err := fs.Parse(args); err != nil {
		printSessionsUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printSessionsUsage(stderr)
		return fmt.Errorf("sessions does not accept positional arguments")
	}
	if err := validateSwitchUI(*ui); err != nil {
		printSessionsUsage(stderr)
		return err
	}

	if c.recent == nil {
		return fmt.Errorf("recent tmux session resolver is not configured")
	}
	summaries, err := c.recent.RecentSessionSummaries(context.Background())
	if err != nil {
		return fmt.Errorf("resolve recent tmux sessions: %w", err)
	}
	if len(summaries) == 0 {
		return nil
	}

	if c.native == nil {
		return fmt.Errorf("native picker is not configured")
	}
	if c.executable == nil {
		return fmt.Errorf("sessions executable resolver is not configured")
	}

	binaryPath, err := c.executable()
	if err != nil {
		return fmt.Errorf("resolve sessions executable: %w", err)
	}
	previewCommand, err := inttmux.BuildSessionPopupPreviewCommand(binaryPath)
	if err != nil {
		return fmt.Errorf("build sessions preview command: %w", err)
	}

	cycleWindowPrev, err := inttmux.BuildSessionPopupCycleCommand(binaryPath, "cycle-window", "prev")
	if err != nil {
		return fmt.Errorf("build sessions cycle-window prev command: %w", err)
	}
	cycleWindowNext, err := inttmux.BuildSessionPopupCycleCommand(binaryPath, "cycle-window", "next")
	if err != nil {
		return fmt.Errorf("build sessions cycle-window next command: %w", err)
	}
	cyclePanePrev, err := inttmux.BuildSessionPopupCycleCommand(binaryPath, "cycle-pane", "prev")
	if err != nil {
		return fmt.Errorf("build sessions cycle-pane prev command: %w", err)
	}
	cyclePaneNext, err := inttmux.BuildSessionPopupCycleCommand(binaryPath, "cycle-pane", "next")
	if err != nil {
		return fmt.Errorf("build sessions cycle-pane next command: %w", err)
	}

	for {
		rows, err := c.buildRows(summaries)
		if err != nil {
			return err
		}
		result, err := runPickerOptionBackend(c.homeDir, c.lookupEnv, c.native, c.runner, intpickercompat.Options{
			UI:      *ui,
			Entries: rowsToEntries(rows),
			Prompt:  "› ",
			Footer:  sessionsPickerFooter(),
			ExpectKeys: append(
				effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"SessionPopup:KillSession"}, []string{sessionsKillExpectKey}),
				effectivePickerKeysForActions(c.homeDir, c.lookupEnv, []string{"SessionPopup:OpenState"}, []string{sessionsStateExpectKey})...,
			),
			PreviewCommand: previewCommand,
			PreviewWindow:  sessionsPreviewWindow(*ui),
			Bindings: append(pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, "session-popup", "esc", "ctrl-n"),
				"left:execute-silent("+cycleWindowPrev+")+refresh-preview",
				"right:execute-silent("+cycleWindowNext+")+refresh-preview",
				"alt-up:execute-silent("+cyclePanePrev+")+refresh-preview",
				"alt-down:execute-silent("+cyclePaneNext+")+refresh-preview",
			),
		})
		if err != nil {
			return fmt.Errorf("run sessions picker: %w", err)
		}
		if result.Value == "" {
			return nil
		}
		if pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "SessionPopup:OpenState", sessionsStateExpectKey) {
			if err := c.runSessionStateOverview(result.Value, summaries); err != nil {
				return err
			}
			continue
		}
		if pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "SessionPopup:KillSession", sessionsKillExpectKey) {
			nextSummaries, err := c.killFocusedSession(context.Background(), summaries, result.Value)
			if err != nil {
				return err
			}
			if len(nextSummaries) == 0 {
				return nil
			}
			summaries = nextSummaries
			continue
		}

		if c.opener == nil {
			return fmt.Errorf("sessions opener is not configured")
		}
		windowIndex, paneIndex, err := c.resolveSelection(result.Value)
		if err != nil {
			return err
		}
		if err := c.opener.OpenSessionTarget(context.Background(), result.Value, windowIndex, paneIndex); err != nil {
			return fmt.Errorf("open tmux session %q: %w", result.Value, err)
		}

		return nil
	}
}

func (c *sessionsCommand) runSessionStateOverview(sessionName string, summaries []inttmux.RecentSessionSummary) error {
	entries := c.sessionStateOverviewEntries(sessionName, summaries)
	result, err := runPickerOptionBackend(c.homeDir, c.lookupEnv, c.native, c.runner, intpickercompat.Options{
		UI:            "projects-sessions-state",
		Entries:       entries,
		Title:         "Projects > Sessions > State",
		Prompt:        "Projects > Sessions > State > ",
		Footer:        projmuxFooter("Session state overview is read-only here."),
		ExpectKeys:    []string{"enter"},
		Bindings:      pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, "session-popup", "esc"),
		DisableSearch: true,
	})
	if err != nil {
		return fmt.Errorf("run sessions state overview: %w", err)
	}
	if strings.TrimSpace(result.Value) == settingsBackValue || strings.TrimSpace(result.Value) == "" {
		return nil
	}
	return nil
}

func (c *sessionsCommand) sessionStateOverviewEntries(sessionName string, summaries []inttmux.RecentSessionSummary) []intpickercompat.Entry {
	sessionName = strings.TrimSpace(sessionName)
	entries := []intpickercompat.Entry{settingsBackEntry()}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfo("Session", nonEmpty(sessionName, "-"), "read-only overview"),
		Value: settingsNoopValue,
	})
	summary := sessionsSummaryByName(summaries, sessionName)
	if strings.TrimSpace(summary.Path) != "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Project path", summary.Path, ""),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, c.latestSessionStateOverviewEntries(sessionName)...)
	entries = append(entries, namedSessionStateOverviewEntries(summary.Path)...)
	return entries
}

func (c *sessionsCommand) latestSessionStateOverviewEntries(sessionName string) []intpickercompat.Entry {
	if c.stateStore == nil {
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Latest snapshot", "unavailable", "session state store unavailable"),
			Value: settingsNoopValue,
		}}
	}
	store, err := c.stateStore()
	if err != nil {
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Latest snapshot", "unavailable", err.Error()),
			Value: settingsNoopValue,
		}}
	}
	snap, err := store.Load(sessionName)
	if err != nil {
		status := "invalid"
		if errors.Is(err, sessionstate.ErrNotFound) {
			status = "missing"
		}
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Latest snapshot", status, statusbarSessionStateErrorSummary(err)),
			Value: settingsNoopValue,
		}}
	}
	entries := []intpickercompat.Entry{
		{
			Label: settingsLabelInfo("Latest snapshot", "saved", statusbarSessionStateSavedText(snap.SavedAt, time.Time{})),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Snapshot source", snap.SourceLabel(), ""),
			Value: settingsNoopValue,
		},
	}
	for _, window := range snap.Windows {
		windowName := statusbarSessionStateClean(window.Name)
		if windowName == "" {
			windowName = "window"
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Window", fmt.Sprintf("%d %s", window.Index, windowName), sessionStateCount(len(window.Panes), "pane")),
			Value: settingsNoopValue,
		})
		for _, pane := range window.Panes {
			entries = append(entries,
				intpickercompat.Entry{
					Label: settingsLabelInfo("Pane", fmt.Sprintf("%d.%d %s", window.Index, pane.Index, projectSessionStatePaneTitle(pane)), ""),
					Value: settingsNoopValue,
				},
				intpickercompat.Entry{
					Label: settingsLabelInfo("Pane cwd", nonEmpty(strings.TrimSpace(pane.CWD), "-"), ""),
					Value: settingsNoopValue,
				},
				intpickercompat.Entry{
					Label: settingsLabelInfo("Pane recipe", projectSessionStateRecipeText(pane.Recipe, snap.SavedAt), ""),
					Value: settingsNoopValue,
				},
			)
		}
	}
	return entries
}

func namedSessionStateOverviewEntries(projectPath string) []intpickercompat.Entry {
	projectPath = strings.TrimSpace(projectPath)
	if projectPath == "" {
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Named snapshots", "unavailable", "project path unavailable"),
			Value: settingsNoopValue,
		}}
	}
	named, _, err := corelayout.NewStore(projectPath).List()
	if err != nil {
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Named snapshots", "unavailable", err.Error()),
			Value: settingsNoopValue,
		}}
	}
	if len(named) == 0 {
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Named snapshots", "missing", projectPath),
			Value: settingsNoopValue,
		}}
	}
	entries := []intpickercompat.Entry{{
		Label: settingsLabelInfo("Named snapshots", fmt.Sprintf("%d", len(named)), "manual snapshots"),
		Value: settingsNoopValue,
	}}
	for _, entry := range named {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Named snapshot", entry.Name, sessionStateCount(entry.Windows, "window")+", "+sessionStateCount(entry.Panes, "pane")),
			Value: settingsNoopValue,
		})
	}
	return entries
}

func sessionsSummaryByName(summaries []inttmux.RecentSessionSummary, sessionName string) inttmux.RecentSessionSummary {
	for _, summary := range summaries {
		if strings.TrimSpace(summary.Name) == sessionName {
			return summary
		}
	}
	return inttmux.RecentSessionSummary{}
}

func (c *sessionsCommand) buildRows(summaries []inttmux.RecentSessionSummary) ([]intrender.SessionRow, error) {
	renderSummaries := make([]intrender.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		renderSummary := intrender.SessionSummary{
			Name:        summary.Name,
			Attached:    summary.Attached,
			WindowCount: summary.WindowCount,
			PaneCount:   summary.PaneCount,
			Path:        summary.Path,
			Activity:    summary.Activity,
		}

		windowIndex, paneIndex, err := c.resolveSelection(summary.Name)
		if err != nil {
			return nil, err
		}
		renderSummary.StoredTarget = formatStoredTarget(windowIndex, paneIndex)
		renderSummaries = append(renderSummaries, renderSummary)
	}

	return intrender.BuildSessionRows(renderSummaries), nil
}

func (c *sessionsCommand) resolveSelection(sessionName string) (string, string, error) {
	if c.store == nil {
		return "", "", nil
	}

	selection, found, err := c.store.ReadSelection(strings.TrimSpace(sessionName))
	if err != nil {
		return "", "", fmt.Errorf("load sessions preview selection for %q: %w", sessionName, err)
	}
	if !found {
		return "", "", nil
	}

	return strings.TrimSpace(selection.WindowIndex), strings.TrimSpace(selection.PaneIndex), nil
}

func (c *sessionsCommand) killFocusedSession(ctx context.Context, summaries []inttmux.RecentSessionSummary, sessionName string) ([]inttmux.RecentSessionSummary, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return summaries, nil
	}
	if c.killer == nil {
		return nil, fmt.Errorf("sessions killer is not configured")
	}

	targetAttached := false
	targetFound := false
	fallbackSession := ""
	nextSummaries := make([]inttmux.RecentSessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		name := strings.TrimSpace(summary.Name)
		if name == "" {
			continue
		}
		if name == sessionName {
			targetFound = true
			targetAttached = summary.Attached
			continue
		}
		if fallbackSession == "" {
			fallbackSession = name
		}
		nextSummaries = append(nextSummaries, summary)
	}
	if !targetFound {
		return summaries, nil
	}
	if targetAttached {
		if fallbackSession == "" {
			return summaries, nil
		}
		if c.opener == nil {
			return nil, fmt.Errorf("sessions opener is not configured")
		}
		if err := c.opener.OpenSessionTarget(ctx, fallbackSession, "", ""); err != nil {
			return nil, fmt.Errorf("open fallback tmux session %q before kill: %w", fallbackSession, err)
		}
	}
	if err := c.killer.KillSession(ctx, sessionName); err != nil {
		return nil, fmt.Errorf("kill tmux session %q: %w", sessionName, err)
	}
	if c.cleanupKilledSession != nil {
		c.cleanupKilledSession(sessionName)
	}

	refreshed, err := c.recent.RecentSessionSummaries(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve recent tmux sessions after kill: %w", err)
	}
	return refreshed, nil
}

func formatStoredTarget(windowIndex, paneIndex string) string {
	windowIndex = strings.TrimSpace(windowIndex)
	paneIndex = strings.TrimSpace(paneIndex)
	if windowIndex == "" {
		return ""
	}
	if paneIndex == "" {
		return "w" + windowIndex
	}
	return "w" + windowIndex + ".p" + paneIndex
}

func sessionsPreviewWindow(ui string) string {
	if ui == switchUISidebar {
		return "right,60%,border-left"
	}
	return "right,60%,border-left"
}

func sessionsPickerFooter() string {
	return projmuxFooter(strings.Join([]string{
		"Preview follows the focused target.",
		"Session state opens read-only; destructive actions keep the current confirmation policy.",
	}, "\n"))
}

func rowsToEntries(rows []intrender.SessionRow) []intpickercompat.Entry {
	entries := make([]intpickercompat.Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, intpickercompat.Entry{
			Label: row.Label,
			Value: row.Value,
		})
	}
	return entries
}

func printSessionsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux sessions [--ui=popup|sidebar]")
}
