package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/es5h/projmux/internal/config"
	corepreview "github.com/es5h/projmux/internal/core/preview"
	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
	intrender "github.com/es5h/projmux/internal/ui/render"
)

type sessionPopupStore interface {
	ReadSelection(sessionName string) (corepreview.Selection, bool, error)
}

type sessionPopupCommand struct {
	store        sessionPopupStore
	storeErr     error
	inventory    previewInventory
	inventoryErr error
}

func newSessionPopupCommand() *sessionPopupCommand {
	paths, err := config.DefaultPathsFromEnv()
	client := inttmux.NewClient(inttmux.ExecRunner{})

	cmd := &sessionPopupCommand{
		inventory: tmuxPreviewInventory{client: client},
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
	case "help", "--help", "-h":
		printSessionPopupUsage(stdout)
		return nil
	default:
		printSessionPopupUsage(stderr)
		return fmt.Errorf("unknown session-popup subcommand: %s", fs.Arg(0))
	}
}

func (c *sessionPopupCommand) runPreview(args []string, stdout, stderr io.Writer) error {
	sessionName, err := parseSessionPopupPreviewArgs(args, stderr)
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

	model := corepreview.BuildPopupReadModel(corepreview.PopupReadModelInputs{
		SessionName:        sessionName,
		StoredSelection:    selection,
		HasStoredSelection: hasSelection,
		Windows:            windows,
		Panes:              panes,
	})

	_, err = io.WriteString(stdout, intrender.RenderPopupPreview(model))
	return err
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

func parseSessionPopupPreviewArgs(args []string, stderr io.Writer) (string, error) {
	if len(args) != 1 {
		printSessionPopupUsage(stderr)
		return "", fmt.Errorf("session-popup preview requires exactly 1 argument: <session>")
	}

	sessionName := strings.TrimSpace(args[0])
	if sessionName == "" {
		printSessionPopupUsage(stderr)
		return "", fmt.Errorf("session-popup preview requires a non-empty <session> argument")
	}

	return sessionName, nil
}

func printSessionPopupUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux session-popup preview <session>")
}
