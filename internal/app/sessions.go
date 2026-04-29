package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intfzf "github.com/crevissepartners/projmux/internal/ui/fzf"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

const sessionsKillExpectKey = "ctrl-x"

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
	Run(options intfzf.Options) (intfzf.Result, error)
}

type sessionsCommand struct {
	recent     sessionsRecentResolver
	store      sessionsSelectionStore
	opener     sessionsOpener
	killer     sessionsKiller
	runner     sessionsRunner
	executable func() (string, error)
}

func newSessionsCommand() *sessionsCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})
	return &sessionsCommand{
		recent:     client,
		store:      newSessionPopupCommand().store,
		opener:     client,
		killer:     client,
		runner:     intfzf.NewRunner(),
		executable: os.Executable,
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

	if c.runner == nil {
		return fmt.Errorf("sessions runner is not configured")
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
		result, err := c.runner.Run(intfzf.Options{
			UI:             *ui,
			Entries:        rowsToEntries(rows),
			Prompt:         "› ",
			Footer:         sessionsPickerFooter(),
			ExpectKeys:     []string{sessionsKillExpectKey},
			PreviewCommand: previewCommand,
			PreviewWindow:  sessionsPreviewWindow(*ui),
			Bindings: append(pickerCloseBindings(),
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
		if result.Key == sessionsKillExpectKey {
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
		"Enter: switch to previewed target",
		"Ctrl-X: kill focused session",
		"Left/Right: preview window",
		"Alt-Up/Alt-Down: preview pane",
	}, "\n"))
}

func rowsToEntries(rows []intrender.SessionRow) []intfzf.Entry {
	entries := make([]intfzf.Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, intfzf.Entry{
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
