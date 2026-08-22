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
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
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
	diagnostics          *diagnostics.LifecycleRecorder
	recent               sessionsRecentResolver
	store                sessionsSelectionStore
	opener               sessionsOpener
	killer               sessionsKiller
	mutationRunner       tmuxCommandRunner
	runner               sessionsRunner
	native               intpicker.Runner
	executable           func() (string, error)
	lookupEnv            func(string) string
	homeDir              func() (string, error)
	stateStore           func() (sessionstate.Store, error)
	cleanupKilledSession func(string)
	// navigation is the shared zero-write Registry read seam. It supplies the
	// attribution that decides which observed sessions are managed rows; it
	// never writes and never materializes.
	navigation *registryNavigationReader
	// runtime is the Runtime diagnostics route the withheld sessions are
	// reached through. It is a field so this surface forwards to the shipped
	// escape hatch rather than growing a second one.
	runtime rawArgvCommand
}

func newSessionsCommand(recorders ...*diagnostics.LifecycleRecorder) *sessionsCommand {
	opts := []inttmux.ClientOption{}
	if len(recorders) > 0 && recorders[0] != nil {
		opts = append(opts, inttmux.WithLifecycleDiagnostics(recorders[0]))
	}
	client := inttmux.NewClient(inttmux.ExecRunner{}, opts...)
	return &sessionsCommand{
		diagnostics:    recorderFrom(recorders),
		recent:         client,
		store:          newSessionPopupCommand().store,
		opener:         client,
		killer:         client,
		mutationRunner: inttmux.ExecRunner{},
		native:         intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		executable:     resolveExecutablePath,
		lookupEnv:      os.Getenv,
		homeDir:        os.UserHomeDir,
		stateStore:     sessionstate.NewDefaultStoreFromEnv,
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
	// Bright Phase 2 (B3): the sessions picker rows render with the resolved
	// effective theme instead of the fallback literals.
	defer applyNativeUIThemeFromConfig(c.homeDir, c.lookupEnv, "")()
	locale := appLocale(c.homeDir, c.lookupEnv)

	if c.recent == nil {
		return fmt.Errorf("recent tmux session resolver is not configured")
	}
	summaries, err := c.recent.RecentSessionSummaries(context.Background())
	if err != nil {
		return fmt.Errorf("resolve recent tmux sessions: %w", err)
	}
	// Managed rows only. A session projmux does not own is not a Project, and a
	// Main surface that listed it beside one would be offering the same actions
	// on two objects with different owners.
	attribution := c.attributeSessions(context.Background(), summaries)
	summaries = attribution.managed
	runtimeLink, hasRuntimeLink := attribution.runtimeLinkEntry()
	if len(summaries) == 0 && !hasRuntimeLink {
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
		rows, err := c.buildRows(summaries, attribution, locale)
		if err != nil {
			return err
		}
		entries := rowsToEntries(rows)
		if hasRuntimeLink {
			entries = append(entries, runtimeLink)
		}
		result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.native, intpickercompat.Options{
			UI:      *ui,
			Entries: entries,
			Prompt:  "› ",
			Footer:  sessionsPickerFooter(locale),
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
		if result.Value == sessionsRuntimeSentinel {
			if c.runtime == nil {
				return fmt.Errorf("sessions runtime diagnostics handler is not configured")
			}
			return c.runtime.Run([]string{"diagnostics"}, stdout, stderr)
		}
		if pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "SessionPopup:OpenState", sessionsStateExpectKey) {
			if err := c.runSessionStateOverview(result.Value, summaries); err != nil {
				return err
			}
			continue
		}
		if pickerKeyMatchesAction(c.homeDir, c.lookupEnv, result.Key, "SessionPopup:KillSession", sessionsKillExpectKey) {
			nextSummaries, err := c.killFocusedSession(context.Background(), summaries, attribution, result.Value)
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
	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.native, intpickercompat.Options{
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

func (c *sessionsCommand) buildRows(summaries []inttmux.RecentSessionSummary, attribution sessionsAttribution, locale i18n.Locale) ([]intrender.SessionRow, error) {
	renderSummaries := make([]intrender.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		renderSummary := intrender.SessionSummary{
			Name:         summary.Name,
			ResourceName: attribution.byID[strings.TrimSpace(summary.ID)].ResourceName,
			Attached:     summary.Attached,
			WindowCount:  summary.WindowCount,
			PaneCount:    summary.PaneCount,
			Path:         summary.Path,
			Activity:     summary.Activity,
		}

		windowIndex, paneIndex, err := c.resolveSelection(summary.Name)
		if err != nil {
			return nil, err
		}
		renderSummary.StoredTarget = formatStoredTarget(windowIndex, paneIndex)
		renderSummaries = append(renderSummaries, renderSummary)
	}

	if locale == i18n.FallbackLocale {
		return intrender.BuildSessionRows(renderSummaries), nil
	}
	return intrender.BuildSessionRowsWithText(renderSummaries, intrender.SessionRowText{
		Attached: localizeText(locale, "picker.sessions.attached", "Attached"),
		Detached: localizeText(locale, "picker.sessions.detached", "Detached"),
		Windows:  localizeText(locale, "picker.sessions.windows", "Windows"),
	}), nil
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

func (c *sessionsCommand) killFocusedSession(ctx context.Context, summaries []inttmux.RecentSessionSummary, attribution sessionsAttribution, sessionName string) ([]inttmux.RecentSessionSummary, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return summaries, nil
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
	var selectedSummary inttmux.RecentSessionSummary
	for _, summary := range summaries {
		if strings.TrimSpace(summary.Name) == sessionName {
			selectedSummary = summary
			break
		}
	}
	managed, ok := attribution.byID[strings.TrimSpace(selectedSummary.ID)]
	if !attribution.resolved || !ok || exactTmuxHandle(selectedSummary.ID, "$") == "" || strings.TrimSpace(managed.ResourceUID) == "" {
		return nil, fmt.Errorf("kill tmux session %q: managed UID attribution is unknown; nothing was changed", sessionName)
	}
	if c.mutationRunner == nil {
		return nil, fmt.Errorf("sessions managed mutation runner is not configured")
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
	route, err := resolveInvocationRuntimeMutationRoute(ctx, c.mutationRunner, c.lookupEnv)
	if err != nil {
		return nil, err
	}
	target := managedRuntimeStopTarget{SessionID: selectedSummary.ID, SessionName: sessionName,
		RootKind: coremetadata.Kind(managed.ResourceKind), RootUID: managed.ResourceUID, Route: route}
	if c.navigation == nil || c.navigation.reader == nil {
		return nil, errors.New("sessions managed runtime Registry reader is not configured")
	}
	authoritative := managedRuntimeStopRegistryAuthority(c.navigation.reader.loadRegistry)
	if err := executeManagedRuntimeStop(ctx, c.mutationRunner, target, authoritative); err != nil {
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
	if ui == switchUIPopup {
		return "down,60%,border-top"
	}
	return "right,60%,border-left"
}

func sessionsPickerFooter(locale i18n.Locale) string {
	return localizeText(locale, "picker.sessions.footer", "Enter: open | Esc: close")
}

func sessionsPopupPreviewText(locale i18n.Locale) intrender.PopupPreviewText {
	return intrender.PopupPreviewText{
		Session:        localizeText(locale, "picker.sessions.preview.session", "Session"),
		Name:           localizeText(locale, "picker.sessions.preview.name", "name"),
		Windows:        localizeText(locale, "picker.sessions.preview.windows", "windows"),
		Pane:           localizeText(locale, "picker.sessions.preview.pane", "pane"),
		Command:        localizeText(locale, "picker.sessions.preview.command", "cmd"),
		Title:          localizeText(locale, "picker.sessions.preview.title", "title"),
		Status:         localizeText(locale, "picker.sessions.preview.status", "status"),
		Path:           localizeText(locale, "picker.sessions.preview.path", "path"),
		WindowsSection: localizeText(locale, "picker.sessions.preview.windows_section", "Windows"),
		PanesSection:   localizeText(locale, "picker.sessions.preview.panes_section", "Panes"),
		PaneSnapshot:   localizeText(locale, "picker.sessions.preview.pane_snapshot", "Pane Snapshot"),
		Window:         localizeText(locale, "picker.sessions.preview.window", "window"),
		None:           localizeText(locale, "picker.sessions.preview.none", "(none)"),
		Unknown:        localizeText(locale, "picker.sessions.preview.unknown", "unknown"),
	}
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
	fmt.Fprintln(w, "  projmux runtime sessions [--ui=popup|sidebar]")
}
