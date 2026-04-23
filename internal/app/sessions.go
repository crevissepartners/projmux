package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	corepreview "github.com/es5h/projmux/internal/core/preview"
	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
	intfzf "github.com/es5h/projmux/internal/ui/fzf"
	intrender "github.com/es5h/projmux/internal/ui/render"
)

type sessionsRecentResolver interface {
	RecentSessionSummaries(ctx context.Context) ([]inttmux.RecentSessionSummary, error)
}

type sessionsSelectionStore interface {
	ReadSelection(sessionName string) (selection corepreview.Selection, found bool, err error)
}

type sessionsOpener interface {
	OpenSessionTarget(ctx context.Context, sessionName, windowIndex, paneIndex string) error
}

type sessionsRunner interface {
	Run(options intfzf.Options) (intfzf.Result, error)
}

type sessionsCommand struct {
	recent     sessionsRecentResolver
	store      sessionsSelectionStore
	opener     sessionsOpener
	runner     sessionsRunner
	executable func() (string, error)
}

func newSessionsCommand() *sessionsCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})
	return &sessionsCommand{
		recent:     client,
		store:      newSessionPopupCommand().store,
		opener:     client,
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

	rows, err := c.buildRows(summaries)
	if err != nil {
		return err
	}
	result, err := c.runner.Run(intfzf.Options{
		UI:             *ui,
		Entries:        rowsToEntries(rows),
		Prompt:         "› ",
		Footer:         sessionsPickerFooter(),
		PreviewCommand: previewCommand,
		PreviewWindow:  sessionsPreviewWindow(*ui),
		Bindings: []string{
			"left:execute-silent(" + cycleWindowPrev + ")+refresh-preview",
			"right:execute-silent(" + cycleWindowNext + ")+refresh-preview",
			"alt-up:execute-silent(" + cyclePanePrev + ")+refresh-preview",
			"alt-down:execute-silent(" + cyclePaneNext + ")+refresh-preview",
		},
	})
	if err != nil {
		return fmt.Errorf("run sessions picker: %w", err)
	}
	if result.Value == "" {
		return nil
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

func (c *sessionsCommand) buildRows(summaries []inttmux.RecentSessionSummary) ([]intrender.SessionRow, error) {
	renderSummaries := make([]intrender.SessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		renderSummary := intrender.SessionSummary{
			Name:        summary.Name,
			Attached:    summary.Attached,
			WindowCount: summary.WindowCount,
			PaneCount:   summary.PaneCount,
			Path:        summary.Path,
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
	return "down,45%,border-top"
}

func sessionsPickerFooter() string {
	return strings.Join([]string{
		"Enter: switch to previewed target",
		"Left/Right: preview window",
		"Alt-Up/Alt-Down: preview pane",
	}, "\n")
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
