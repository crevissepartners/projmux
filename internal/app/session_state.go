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

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

type sessionStateCommand struct {
	diagnostics  *diagnostics.SessionStateRecorder
	runner       tmuxRunner
	nativePicker intpicker.Runner
	lookupEnv    func(string) string
	homeDir      func() (string, error)
	now          func() time.Time
	sessionStore func() (sessionstate.Store, error)
}

func newSessionStateCommand() *sessionStateCommand {
	return &sessionStateCommand{
		runner:       inttmux.ExecRunner{},
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		lookupEnv:    os.Getenv,
		homeDir:      os.UserHomeDir,
		now:          time.Now,
		sessionStore: sessionstate.NewDefaultStoreFromEnv,
	}
}

// Run manages user-facing session snapshot actions.
func (c *sessionStateCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("session-state", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printSessionStateUsage(stderr)
		return errors.New("session-state requires a subcommand")
	}

	switch fs.Arg(0) {
	case "status":
		return c.runStatus(fs.Args()[1:], stdout, stderr)
	case "save":
		return c.runSave(fs.Args()[1:], stdout, stderr)
	case "delete":
		return c.runDelete(fs.Args()[1:], stdout, stderr)
	case "restore":
		return c.runRestore(fs.Args()[1:], stdout, stderr)
	case "preview":
		return c.runPreview(fs.Args()[1:], stdout, stderr)
	case "popup":
		return c.runPopup(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printSessionStateUsage(stdout)
		return nil
	default:
		printSessionStateUsage(stderr)
		return fmt.Errorf("unknown session-state subcommand: %s", fs.Arg(0))
	}
}

func (c *sessionStateCommand) runStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("session-state status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "target session name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printSessionStateUsage(stderr)
		return fmt.Errorf("session-state status does not accept positional arguments")
	}

	state := c.loadView(context.Background(), *session)
	for _, line := range sessionStateStatusLines(state, c.nowTime(), 100) {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func (c *sessionStateCommand) runSave(args []string, stdout, stderr io.Writer) (err error) {
	fs := flag.NewFlagSet("session-state save", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printSessionStateUsage(stderr)
		return fmt.Errorf("session-state save does not accept positional arguments")
	}
	started := c.nowTime()
	var counts diagnostics.SessionStateCounts
	defer func() {
		c.diagnostics.Record(diagnostics.OperationSessionStateSave, diagnostics.SessionStateSourceManual, started, counts, err)
	}()
	if !c.insideTmux() {
		return errors.New("session-state save requires a current tmux session")
	}
	if c.runner == nil {
		return errors.New("configure tmux runner: tmux runner is not configured")
	}
	store, err := c.store()
	if err != nil {
		return err
	}

	ctx := context.Background()
	sessionName, err := c.currentSessionName(ctx)
	if err != nil {
		return err
	}
	now := c.nowTime()
	client := inttmux.NewClient(c.runner)
	snap, err := client.SaveSessionSnapshot(ctx, store, sessionName, now)
	if err != nil {
		return fmt.Errorf("save session snapshot %q: %w", sessionName, err)
	}
	counts = sessionStateDiagnosticCounts(snap)
	_, err = fmt.Fprintf(stdout, "saved session snapshot: %s (%s, %s)\n", snap.Session, sessionStateCount(len(snap.Windows), "window"), sessionStateCount(statusbarSessionStatePaneCount(snap), "pane"))
	return err
}

func (c *sessionStateCommand) runDelete(args []string, stdout, stderr io.Writer) (err error) {
	fs := flag.NewFlagSet("session-state delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "target session name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printSessionStateUsage(stderr)
		return fmt.Errorf("session-state delete does not accept positional arguments")
	}
	started := c.nowTime()
	counts := diagnostics.SessionStateCounts{ItemCount: 1}
	defer func() {
		c.diagnostics.Record(diagnostics.OperationSessionStateDelete, diagnostics.SessionStateSourceManual, started, counts, err)
	}()
	sessionName, err := c.resolveSessionName(context.Background(), *session)
	if err != nil {
		return err
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	if err := store.Delete(sessionName); err != nil {
		return fmt.Errorf("delete session snapshot %q: %w", sessionName, err)
	}
	_, err = fmt.Fprintf(stdout, "deleted session snapshot: %s\n", sessionName)
	return err
}

func sessionStateDiagnosticCounts(snap sessionstate.Snapshot) diagnostics.SessionStateCounts {
	counts := diagnostics.SessionStateCounts{WindowCount: len(snap.Windows)}
	for _, window := range snap.Windows {
		counts.PaneCount += len(window.Panes)
		for _, pane := range window.Panes {
			switch pane.Recipe.Kind {
			case sessionstate.RecipeKindShell:
				counts.ShellRecipeCount++
			case sessionstate.RecipeKindAgent:
				counts.AgentRecipeCount++
			case sessionstate.RecipeKindStartup:
				counts.StartupRecipeCount++
			}
		}
	}
	return counts
}

func (c *sessionStateCommand) runRestore(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("session-state restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "target session name")
	dryRun := fs.Bool("dry-run", false, "preview restore actions without running tmux commands")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printSessionStateUsage(stderr)
		return fmt.Errorf("session-state restore does not accept positional arguments")
	}
	if !*dryRun {
		printSessionStateUsage(stderr)
		return errors.New("session-state restore only supports --dry-run in this release")
	}
	return c.printRestorePreview(context.Background(), *session, stdout)
}

func (c *sessionStateCommand) runPreview(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("session-state preview", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "target session name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printSessionStateUsage(stderr)
		return fmt.Errorf("session-state preview does not accept positional arguments")
	}
	return c.printRestorePreview(context.Background(), *session, stdout)
}

const (
	sessionStatePopupClose          = "sessionstate-popup:close"
	sessionStatePopupSave           = "sessionstate-popup:save"
	sessionStatePopupPreviewRestore = "sessionstate-popup:preview-restore"
)

func (c *sessionStateCommand) runPopup(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("session-state popup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "target session name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printSessionStateUsage(stderr)
		return fmt.Errorf("session-state popup does not accept positional arguments")
	}
	for {
		state := c.loadView(context.Background(), *session)
		result, err := c.runPopupPicker(sessionStatePopupOptions(state))
		if err != nil {
			if errors.Is(err, errSettingsClosed) {
				return nil
			}
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" || action == sessionStatePopupClose {
			return nil
		}
		if err := c.executePopupAction(action, *session, stdout, stderr); err != nil {
			return err
		}
		if action == sessionStatePopupPreviewRestore {
			return nil
		}
	}
}

func (c *sessionStateCommand) runPopupPicker(options intpickercompat.Options) (intpickercompat.Result, error) {
	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, options)
	if err != nil {
		if isNoSelectionExit(err) {
			return intpickercompat.Result{}, errSettingsClosed
		}
		return intpickercompat.Result{}, fmt.Errorf("run session state popup: %w", err)
	}
	return result, nil
}

func (c *sessionStateCommand) executePopupAction(action, explicitSession string, stdout, stderr io.Writer) error {
	switch action {
	case sessionStatePopupSave:
		return c.runSave(nil, stdout, stderr)
	case sessionStatePopupPreviewRestore:
		return c.printRestorePreview(context.Background(), explicitSession, stdout)
	default:
		return fmt.Errorf("unknown session state popup action: %s", action)
	}
}

func (c *sessionStateCommand) printRestorePreview(ctx context.Context, explicitSession string, stdout io.Writer) error {
	sessionName, err := c.resolveSessionName(ctx, explicitSession)
	if err != nil {
		return err
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	snap, err := store.Load(sessionName)
	if err != nil {
		return fmt.Errorf("load session snapshot %q: %w", sessionName, err)
	}

	for _, line := range sessionStateRestorePreviewLines(snap, c.nowTime(), 100) {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func (c *sessionStateCommand) loadView(ctx context.Context, explicitSession string) statusbarSessionStateView {
	state := statusbarSessionStateView{
		Autosave: sessionStateToggleStateDefault(c.homeDir, c.lookupEnv, sessionStateAutosaveEnv, config.SessionStateToggleOff, func(paths config.Paths) string { return paths.SessionStateAutosaveFile() }),
	}
	sessionName, err := c.resolveSessionName(ctx, explicitSession)
	if err != nil {
		state.SessionErr = err
		return state
	}
	state.Session = sessionName
	state.Source = c.liveSessionStateSource(ctx, sessionName)
	store, err := c.store()
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

func (c *sessionStateCommand) liveSessionStateSource(ctx context.Context, sessionName string) string {
	if c.runner == nil || strings.TrimSpace(sessionName) == "" {
		return ""
	}
	return inttmux.NewClient(c.runner).SessionStateSource(ctx, sessionName)
}

func (c *sessionStateCommand) resolveSessionName(ctx context.Context, explicit string) (string, error) {
	if sessionName := strings.TrimSpace(explicit); sessionName != "" {
		return sessionName, nil
	}
	if c.lookupEnv != nil {
		if sessionName := strings.TrimSpace(c.lookupEnv("PROJMUX_SESSION")); sessionName != "" {
			return sessionName, nil
		}
	}
	if !c.insideTmux() {
		return "", errors.New("session-state requires --session or a current tmux session")
	}
	return c.currentSessionName(ctx)
}

func (c *sessionStateCommand) currentSessionName(ctx context.Context) (string, error) {
	if c.runner == nil {
		return "", errors.New("configure tmux runner: tmux runner is not configured")
	}
	client := inttmux.NewClient(c.runner)
	sessionName, err := client.CurrentSessionName(ctx)
	if err != nil {
		return "", err
	}
	return sessionName, nil
}

func (c *sessionStateCommand) store() (sessionstate.Store, error) {
	if c.sessionStore == nil {
		return sessionstate.Store{}, errors.New("configure sessionstate store: sessionstate store is not configured")
	}
	store, err := c.sessionStore()
	if err != nil {
		return sessionstate.Store{}, fmt.Errorf("resolve sessionstate store: %w", err)
	}
	return store, nil
}

func (c *sessionStateCommand) insideTmux() bool {
	if c.lookupEnv == nil {
		return strings.TrimSpace(os.Getenv("TMUX")) != ""
	}
	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

func (c *sessionStateCommand) nowTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func sessionStateStatusLines(state statusbarSessionStateView, now time.Time, cols int) []string {
	lines := []string{"Session State"}
	session := state.Session
	if session == "" {
		session = "-"
	}
	lines = append(lines, sessionStateField("session", session))
	lines = append(lines, sessionStateField("source", statusbarSessionStateSourceText(state)))
	lines = append(lines, sessionStateField("auto-save", statusbarSessionStateToggleText(state.Autosave)))

	switch {
	case state.SessionErr != nil:
		lines = append(lines, sessionStateField("snapshot", "unavailable - "+statusbarSessionStateErrorSummary(state.SessionErr)))
	case state.StoreErr != nil:
		lines = append(lines, sessionStateField("snapshot", "store unavailable - "+statusbarSessionStateErrorSummary(state.StoreErr)))
	case state.LoadErr != nil:
		status := "invalid - " + statusbarSessionStateErrorSummary(state.LoadErr)
		if errors.Is(state.LoadErr, sessionstate.ErrNotFound) {
			status = "missing"
		}
		lines = append(lines, sessionStateField("snapshot", status))
	default:
		snap := state.Snapshot
		lines = append(lines, sessionStateField("snapshot", "saved"))
		lines = append(lines, sessionStateField("saved", statusbarSessionStateSavedText(snap.SavedAt, now)))
		lines = append(lines, sessionStateField("windows", fmt.Sprintf("%d", len(snap.Windows))))
		lines = append(lines, sessionStateField("panes", fmt.Sprintf("%d", statusbarSessionStatePaneCount(snap))))
		if strings.TrimSpace(snap.DefaultCWD) != "" {
			lines = append(lines, sessionStateField("default cwd", snap.DefaultCWD))
		}
		lines = append(lines, "")
		lines = append(lines, "Preview")
		lines = append(lines, sessionStatePlainPreviewLines(snap, cols)...)
	}
	return lines
}

func sessionStatePopupOptions(state statusbarSessionStateView) intpickercompat.Options {
	return intpickercompat.Options{
		UI:            "sessionstate-popup",
		Entries:       sessionStatePopupEntries(state),
		Title:         "Session State",
		Prompt:        "Session State > ",
		Footer:        projmuxFooter("Enter: action  |  Esc/Ctrl-C: close"),
		ExpectKeys:    []string{"enter"},
		Bindings:      []string{"esc:abort", "ctrl-c:abort"},
		DisableSearch: true,
	}
}

func sessionStatePopupEntries(state statusbarSessionStateView) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{
		{
			Label: settingsLabelInfo("Session", nonEmpty(state.Session, "-"), ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Source", statusbarSessionStateSourceText(state), ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Auto-save", statusbarSessionStateToggleText(state.Autosave), ""),
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, sessionStatePopupSnapshotEntries(state)...)
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Save snapshot", "capture current tmux session"),
		Value: sessionStatePopupSave,
	})
	if state.LoadErr == nil && state.StoreErr == nil && state.SessionErr == nil {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Preview restore", "dry-run only"),
			Value: sessionStatePopupPreviewRestore,
		})
	} else {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Preview restore", "unavailable without a valid snapshot"),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphBack, settingsColorBack, "Close", "dismiss popup"),
			Value: sessionStatePopupClose,
		},
	)
	return entries
}

func sessionStatePopupSnapshotEntries(state statusbarSessionStateView) []intpickercompat.Entry {
	switch {
	case state.SessionErr != nil:
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Snapshot", "unavailable", statusbarSessionStateErrorSummary(state.SessionErr)),
			Value: settingsNoopValue,
		}}
	case state.StoreErr != nil:
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Snapshot", "store unavailable", statusbarSessionStateErrorSummary(state.StoreErr)),
			Value: settingsNoopValue,
		}}
	case state.LoadErr != nil:
		status := "invalid"
		if errors.Is(state.LoadErr, sessionstate.ErrNotFound) {
			status = "missing"
		}
		return []intpickercompat.Entry{{
			Label: settingsLabelInfo("Snapshot", status, statusbarSessionStateErrorSummary(state.LoadErr)),
			Value: settingsNoopValue,
		}}
	default:
		snap := state.Snapshot
		return []intpickercompat.Entry{
			{
				Label: settingsLabelInfo("Snapshot", "saved", statusbarSessionStateSavedText(snap.SavedAt, time.Time{})),
				Value: settingsNoopValue,
			},
			{
				Label: settingsLabelInfo("Windows", fmt.Sprintf("%d", len(snap.Windows)), ""),
				Value: settingsNoopValue,
			},
			{
				Label: settingsLabelInfo("Panes", fmt.Sprintf("%d", statusbarSessionStatePaneCount(snap)), ""),
				Value: settingsNoopValue,
			},
		}
	}
}

func sessionStateRestorePreviewLines(snap sessionstate.Snapshot, now time.Time, cols int) []string {
	lines := []string{
		"Session State Restore Preview",
		sessionStateField("session", snap.Session),
		sessionStateField("source", snap.SourceLabel()),
		sessionStateField("saved", statusbarSessionStateSavedText(snap.SavedAt, now)),
		sessionStateField("windows", fmt.Sprintf("%d", len(snap.Windows))),
		sessionStateField("panes", fmt.Sprintf("%d", statusbarSessionStatePaneCount(snap))),
	}
	if strings.TrimSpace(snap.DefaultCWD) != "" {
		lines = append(lines, sessionStateField("default cwd", snap.DefaultCWD))
	}
	lines = append(lines, "", "Dry run only; no tmux commands were executed.", "")
	lines = append(lines, sessionStatePlainPreviewLines(snap, cols)...)
	return lines
}

func sessionStatePlainPreviewLines(snap sessionstate.Snapshot, cols int) []string {
	const (
		maxWindows = 12
		maxPanes   = 30
	)
	windows := snap.Windows
	lines := make([]string, 0, len(windows)+statusbarSessionStatePaneCount(snap))
	panesSeen := 0
	for wi, window := range windows {
		if wi >= maxWindows {
			lines = append(lines, fmt.Sprintf("... %d more windows", len(windows)-wi))
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
					lines = append(lines, fmt.Sprintf("  ... %d more panes", remaining))
				}
				return lines
			}
			panesSeen++
			lines = append(lines, statusbarSessionStateClip("  "+statusbarSessionStatePanePreview(snap.SavedAt, window.Index, pane), cols))
		}
	}
	if len(lines) == 0 {
		return []string{"No windows recorded."}
	}
	return lines
}

func sessionStateField(label, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "-"
	}
	return fmt.Sprintf("%-13s %s", label+":", value)
}

func sessionStateCount(count int, singular string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", count, singular)
}

func printSessionStateUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux get snapshots [--session <name>]")
	fmt.Fprintln(w, "  projmux create snapshot")
	fmt.Fprintln(w, "  projmux delete snapshot [--session <name>]")
	fmt.Fprintln(w, "  projmux restore snapshot --dry-run [--session <name>]")
}
