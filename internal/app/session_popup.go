package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

type sessionPopupStore interface {
	ReadSelection(sessionName string) (corepreview.Selection, bool, error)
	CyclePaneSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)
	CycleWindowSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)
}

type sessionPopupOpener interface {
	OpenSessionTarget(ctx context.Context, sessionName, windowIndex, paneIndex string) error
}

type sessionPopupCommand struct {
	store        sessionPopupStore
	storeErr     error
	inventory    previewInventory
	inventoryErr error
	opener       sessionPopupOpener
	openerErr    error
}

func newSessionPopupCommand() *sessionPopupCommand {
	paths, err := config.DefaultPathsFromEnv()
	client := inttmux.NewClient(inttmux.ExecRunner{})

	cmd := &sessionPopupCommand{
		inventory: tmuxPreviewInventory{client: client},
		opener:    client,
	}
	if err != nil {
		cmd.storeErr = fmt.Errorf("resolve default config paths: %w", err)
		return cmd
	}

	cmd.store = corepreview.NewDefaultStore(paths)
	return cmd
}

// Run manages session-popup subcommands.
func (c *sessionPopupCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("session-popup", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printSessionPopupUsage(stderr)
		return errors.New("session-popup requires a subcommand")
	}

	switch fs.Arg(0) {
	case "preview":
		return c.runPreview(fs.Args()[1:], stdout, stderr)
	case "open":
		return c.runOpen(fs.Args()[1:], stderr)
	case "cycle-pane":
		return c.runCyclePane(fs.Args()[1:], stdout, stderr)
	case "cycle-window":
		return c.runCycleWindow(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printSessionPopupUsage(stdout)
		return nil
	default:
		printSessionPopupUsage(stderr)
		return fmt.Errorf("unknown session-popup subcommand: %s", fs.Arg(0))
	}
}

func (c *sessionPopupCommand) runPreview(args []string, stdout, stderr io.Writer) error {
	sessionName, err := parseSessionPopupSessionArg(args, "preview", stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	inventory, err := c.requireInventory()
	if err != nil {
		return err
	}

	selection, hasSelection, err := store.ReadSelection(sessionName)
	if err != nil {
		return fmt.Errorf("load popup preview selection for %q: %w", sessionName, err)
	}

	windows, err := inventory.SessionWindows(context.Background(), sessionName)
	if err != nil {
		return fmt.Errorf("load popup preview windows for %q: %w", sessionName, err)
	}

	panes, err := inventory.SessionPanes(context.Background(), sessionName)
	if err != nil {
		return fmt.Errorf("load popup preview panes for %q: %w", sessionName, err)
	}

	return writeSessionPopupPreview(context.Background(), inventory, stdout, sessionName, selection, hasSelection, windows, panes)
}

func (c *sessionPopupCommand) runOpen(args []string, stderr io.Writer) error {
	sessionName, err := parseSessionPopupSessionArg(args, "open", stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	opener, err := c.requireOpener()
	if err != nil {
		return err
	}

	selection, hasSelection, err := store.ReadSelection(sessionName)
	if err != nil {
		return fmt.Errorf("load popup open selection for %q: %w", sessionName, err)
	}

	windowIndex := ""
	paneIndex := ""
	if hasSelection {
		windowIndex = strings.TrimSpace(selection.WindowIndex)
		paneIndex = strings.TrimSpace(selection.PaneIndex)
	}

	if err := opener.OpenSessionTarget(context.Background(), sessionName, windowIndex, paneIndex); err != nil {
		return fmt.Errorf("open popup target for %q: %w", sessionName, err)
	}

	return nil
}

func (c *sessionPopupCommand) runCyclePane(args []string, stdout, stderr io.Writer) error {
	sessionName, direction, err := parseSessionPopupCycleArgs("session-popup cycle-pane", args, stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	inventory, err := c.requireInventory()
	if err != nil {
		return err
	}

	windows, err := inventory.SessionWindows(context.Background(), sessionName)
	if err != nil {
		return fmt.Errorf("load popup cycle windows for %q: %w", sessionName, err)
	}
	panes, err := inventory.SessionPanes(context.Background(), sessionName)
	if err != nil {
		return fmt.Errorf("load popup cycle panes for %q: %w", sessionName, err)
	}

	result, err := store.CyclePaneSelection(sessionName, nil, panes, direction)
	if err != nil {
		return fmt.Errorf("cycle popup pane for %q: %w", sessionName, err)
	}
	if !result.Selected {
		return fmt.Errorf("session-popup cycle-pane found no panes for session %q", sessionName)
	}

	return writeSessionPopupPreview(context.Background(), inventory, stdout, sessionName, selectionFromCursor(sessionName, result.Cursor), true, windows, panes)
}

func (c *sessionPopupCommand) runCycleWindow(args []string, stdout, stderr io.Writer) error {
	sessionName, direction, err := parseSessionPopupCycleArgs("session-popup cycle-window", args, stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	inventory, err := c.requireInventory()
	if err != nil {
		return err
	}

	windows, err := inventory.SessionWindows(context.Background(), sessionName)
	if err != nil {
		return fmt.Errorf("load popup cycle windows for %q: %w", sessionName, err)
	}
	panes, err := inventory.SessionPanes(context.Background(), sessionName)
	if err != nil {
		return fmt.Errorf("load popup cycle panes for %q: %w", sessionName, err)
	}

	result, err := store.CycleWindowSelection(sessionName, windows, panes, direction)
	if err != nil {
		return fmt.Errorf("cycle popup window for %q: %w", sessionName, err)
	}
	if !result.Selected {
		return fmt.Errorf("session-popup cycle-window found no windows for session %q", sessionName)
	}

	return writeSessionPopupPreview(context.Background(), inventory, stdout, sessionName, selectionFromCursor(sessionName, result.Cursor), true, windows, panes)
}

func selectionFromCursor(sessionName string, cursor corepreview.Cursor) corepreview.Selection {
	return corepreview.Selection{
		SessionName: sessionName,
		WindowIndex: cursor.WindowIndex,
		PaneIndex:   cursor.PaneIndex,
	}
}

func writeSessionPopupPreview(ctx context.Context, inventory previewInventory, stdout io.Writer, sessionName string, selection corepreview.Selection, hasSelection bool, windows []corepreview.Window, panes []corepreview.Pane) error {
	model := corepreview.BuildPopupReadModel(corepreview.PopupReadModelInputs{
		SessionName:        sessionName,
		StoredSelection:    selection,
		HasStoredSelection: hasSelection,
		Windows:            windows,
		Panes:              panes,
	})
	model.PaneSnapshot = capturePaneSnapshot(ctx, inventory, model, -80)

	_, err := io.WriteString(stdout, intrender.RenderPopupPreview(model))
	return err
}

func capturePaneSnapshot(ctx context.Context, inventory previewInventory, model corepreview.PopupReadModel, startLine int) string {
	capturer, ok := inventory.(previewPaneCapturer)
	if !ok {
		return ""
	}
	target := selectedPaneTarget(model)
	if target == "" {
		return ""
	}
	snapshot, err := capturer.CapturePane(ctx, target, startLine)
	if err != nil {
		return ""
	}
	return snapshot
}

func selectedPaneTarget(model corepreview.PopupReadModel) string {
	selectedPaneIndex := strings.TrimSpace(model.SelectedPaneIndex)
	if selectedPaneIndex == "" {
		return ""
	}
	for _, pane := range model.Panes {
		if pane.Index != selectedPaneIndex {
			continue
		}
		if id := strings.TrimSpace(pane.ID); id != "" {
			return id
		}
		windowIndex := strings.TrimSpace(model.SelectedWindowIndex)
		sessionName := strings.TrimSpace(model.SessionName)
		if sessionName == "" || windowIndex == "" {
			return ""
		}
		return sessionName + ":" + windowIndex + "." + selectedPaneIndex
	}
	return ""
}

func (c *sessionPopupCommand) requireStore() (sessionPopupStore, error) {
	if c.storeErr != nil {
		return nil, fmt.Errorf("configure session-popup store: %w", c.storeErr)
	}
	if c.store == nil {
		return nil, errors.New("configure session-popup store: session-popup store is not configured")
	}
	return c.store, nil
}

func (c *sessionPopupCommand) requireInventory() (previewInventory, error) {
	if c.inventoryErr != nil {
		return nil, fmt.Errorf("configure session-popup inventory: %w", c.inventoryErr)
	}
	if c.inventory == nil {
		return nil, errors.New("configure session-popup inventory: session-popup inventory is not configured")
	}
	return c.inventory, nil
}

func (c *sessionPopupCommand) requireOpener() (sessionPopupOpener, error) {
	if c.openerErr != nil {
		return nil, fmt.Errorf("configure session-popup opener: %w", c.openerErr)
	}
	if c.opener == nil {
		return nil, errors.New("configure session-popup opener: session-popup opener is not configured")
	}
	return c.opener, nil
}

func parseSessionPopupSessionArg(args []string, subcommand string, stderr io.Writer) (string, error) {
	if len(args) != 1 {
		printSessionPopupUsage(stderr)
		return "", fmt.Errorf("session-popup %s requires exactly 1 argument: <session>", subcommand)
	}

	sessionName := strings.TrimSpace(args[0])
	if sessionName == "" {
		printSessionPopupUsage(stderr)
		return "", fmt.Errorf("session-popup %s requires a non-empty <session> argument", subcommand)
	}

	return sessionName, nil
}

func parseSessionPopupCycleArgs(command string, args []string, stderr io.Writer) (string, corepreview.Direction, error) {
	if len(args) != 2 {
		printSessionPopupUsage(stderr)
		return "", "", fmt.Errorf("%s requires exactly 2 arguments: <session> <next|prev>", command)
	}

	sessionName := strings.TrimSpace(args[0])
	if sessionName == "" {
		printSessionPopupUsage(stderr)
		return "", "", fmt.Errorf("%s requires a non-empty <session> argument", command)
	}

	direction, err := parsePreviewDirection(args[1])
	if err != nil {
		printSessionPopupUsage(stderr)
		return "", "", fmt.Errorf("%s: %w", command, err)
	}

	return sessionName, direction, nil
}

func printSessionPopupUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux session-popup preview <session>")
	fmt.Fprintln(w, "  projmux session-popup open <session>")
	fmt.Fprintln(w, "  projmux session-popup cycle-pane <session> <next|prev>")
	fmt.Fprintln(w, "  projmux session-popup cycle-window <session> <next|prev>")
}
