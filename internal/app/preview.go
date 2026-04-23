package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/es5h/projmux/internal/config"
	corepreview "github.com/es5h/projmux/internal/core/preview"
	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
)

type previewStore interface {
	CyclePaneSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)
	CycleWindowSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error)
	WriteSelection(sessionName, windowIndex, paneIndex string) error
}

type previewInventory interface {
	SessionWindows(ctx context.Context, sessionName string) ([]corepreview.Window, error)
	SessionPanes(ctx context.Context, sessionName string) ([]corepreview.Pane, error)
}

type tmuxPreviewInventoryClient interface {
	ListSessionWindows(ctx context.Context, sessionName string) ([]inttmux.Window, error)
	ListAllPanes(ctx context.Context) ([]inttmux.Pane, error)
}

type previewCommand struct {
	store        previewStore
	storeErr     error
	inventory    previewInventory
	inventoryErr error
}

type tmuxPreviewInventory struct {
	client tmuxPreviewInventoryClient
}

func newPreviewCommand() *previewCommand {
	paths, err := config.DefaultPathsFromEnv()
	client := inttmux.NewClient(inttmux.ExecRunner{})

	cmd := &previewCommand{
		inventory: tmuxPreviewInventory{client: client},
	}
	if err != nil {
		cmd.storeErr = fmt.Errorf("resolve default config paths: %w", err)
		return cmd
	}

	cmd.store = corepreview.NewDefaultStore(paths)
	return cmd
}

// Run manages preview subcommands.
func (c *previewCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printPreviewUsage(stderr)
		return errors.New("preview requires a subcommand")
	}

	switch fs.Arg(0) {
	case "cycle-pane":
		return c.runCyclePane(fs.Args()[1:], stdout, stderr)
	case "cycle-window":
		return c.runCycleWindow(fs.Args()[1:], stdout, stderr)
	case "select":
		return c.runSelect(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printPreviewUsage(stdout)
		return nil
	default:
		printPreviewUsage(stderr)
		return fmt.Errorf("unknown preview subcommand: %s", fs.Arg(0))
	}
}

func (c *previewCommand) runCyclePane(args []string, stdout, stderr io.Writer) error {
	sessionName, direction, err := parsePreviewCycleArgs("preview cycle-pane", args, stderr)
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

	panes, err := inventory.SessionPanes(context.Background(), sessionName)
	if err != nil {
		return fmt.Errorf("load preview panes for %q: %w", sessionName, err)
	}

	result, err := store.CyclePaneSelection(sessionName, nil, panes, direction)
	if err != nil {
		return fmt.Errorf("cycle preview pane for %q: %w", sessionName, err)
	}
	if !result.Selected {
		return fmt.Errorf("preview cycle-pane found no panes for session %q", sessionName)
	}

	_, err = fmt.Fprintf(stdout, "%s\t%s\t%s\n", sessionName, result.Cursor.WindowIndex, result.Cursor.PaneIndex)
	return err
}

func (c *previewCommand) runCycleWindow(args []string, stdout, stderr io.Writer) error {
	sessionName, direction, err := parsePreviewCycleArgs("preview cycle-window", args, stderr)
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
		return fmt.Errorf("load preview windows for %q: %w", sessionName, err)
	}
	panes, err := inventory.SessionPanes(context.Background(), sessionName)
	if err != nil {
		return fmt.Errorf("load preview panes for %q: %w", sessionName, err)
	}

	result, err := store.CycleWindowSelection(sessionName, windows, panes, direction)
	if err != nil {
		return fmt.Errorf("cycle preview window for %q: %w", sessionName, err)
	}
	if !result.Selected {
		return fmt.Errorf("preview cycle-window found no windows for session %q", sessionName)
	}

	_, err = fmt.Fprintf(stdout, "%s\t%s\t%s\n", sessionName, result.Cursor.WindowIndex, result.Cursor.PaneIndex)
	return err
}

func (c *previewCommand) requireStore() (previewStore, error) {
	if c.storeErr != nil {
		return nil, fmt.Errorf("configure preview store: %w", c.storeErr)
	}
	if c.store == nil {
		return nil, errors.New("configure preview store: preview store is not configured")
	}
	return c.store, nil
}

func (c *previewCommand) requireInventory() (previewInventory, error) {
	if c.inventoryErr != nil {
		return nil, fmt.Errorf("configure preview inventory: %w", c.inventoryErr)
	}
	if c.inventory == nil {
		return nil, errors.New("configure preview inventory: preview inventory is not configured")
	}
	return c.inventory, nil
}

func (i tmuxPreviewInventory) SessionWindows(ctx context.Context, sessionName string) ([]corepreview.Window, error) {
	if i.client == nil {
		return nil, errors.New("tmux preview inventory adapter is not configured")
	}

	rows, err := i.client.ListSessionWindows(ctx, sessionName)
	if err != nil {
		return nil, err
	}

	windows := make([]corepreview.Window, 0, len(rows))
	for _, row := range rows {
		windows = append(windows, corepreview.Window{
			Index:     strconv.Itoa(row.Index),
			Name:      row.Name,
			PaneCount: row.PaneCount,
			Path:      row.Path,
			Active:    row.Active,
		})
	}
	return windows, nil
}

func (i tmuxPreviewInventory) SessionPanes(ctx context.Context, sessionName string) ([]corepreview.Pane, error) {
	if i.client == nil {
		return nil, errors.New("tmux preview inventory adapter is not configured")
	}

	rows, err := i.client.ListAllPanes(ctx)
	if err != nil {
		return nil, err
	}

	panes := make([]corepreview.Pane, 0, len(rows))
	for _, row := range rows {
		if row.SessionName != sessionName {
			continue
		}
		panes = append(panes, corepreview.Pane{
			WindowIndex: strconv.Itoa(row.WindowIndex),
			Index:       strconv.Itoa(row.PaneIndex),
			Title:       row.Title,
			Command:     row.Command,
			Path:        row.Path,
			Active:      row.Active,
		})
	}
	return panes, nil
}

func parsePreviewCycleArgs(command string, args []string, stderr io.Writer) (string, corepreview.Direction, error) {
	if len(args) != 2 {
		printPreviewUsage(stderr)
		return "", "", fmt.Errorf("%s requires exactly 2 arguments: <session> <next|prev>", command)
	}

	sessionName := strings.TrimSpace(args[0])
	if sessionName == "" {
		printPreviewUsage(stderr)
		return "", "", fmt.Errorf("%s requires a non-empty <session> argument", command)
	}

	direction, err := parsePreviewDirection(args[1])
	if err != nil {
		printPreviewUsage(stderr)
		return "", "", fmt.Errorf("%s: %w", command, err)
	}

	return sessionName, direction, nil
}

func parsePreviewDirection(raw string) (corepreview.Direction, error) {
	direction := corepreview.Direction(strings.TrimSpace(raw))
	switch direction {
	case corepreview.DirectionNext, corepreview.DirectionPrev:
		return direction, nil
	default:
		return "", fmt.Errorf("direction must be <next|prev>, got %q", strings.TrimSpace(raw))
	}
}

func printPreviewUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux preview cycle-pane <session> <next|prev>")
	fmt.Fprintln(w, "  projmux preview cycle-window <session> <next|prev>")
	fmt.Fprintln(w, "  projmux preview select <session> <window> [pane]")
}
