package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/pins"
)

type pinStore interface {
	List() ([]string, error)
	Add(pin string) error
	Remove(pin string) error
	Toggle(pin string) (bool, error)
	Clear() error
}

type pinCommand struct {
	store    pinStore
	storeErr error
}

func newPinCommand() *pinCommand {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return &pinCommand{
			storeErr: fmt.Errorf("resolve default config paths: %w", err),
		}
	}

	return &pinCommand{
		store: pins.NewDefaultStore(paths),
	}
}

// Run manages the configured pin subcommands.
func (c *pinCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pin", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printPinUsage(stderr)
		return errors.New("pin requires a subcommand")
	}

	switch fs.Arg(0) {
	case "list":
		return c.runList(fs.Args()[1:], stdout, stderr)
	case "add":
		return c.runAdd(fs.Args()[1:], stdout, stderr)
	case "remove":
		return c.runRemove(fs.Args()[1:], stdout, stderr)
	case "toggle":
		return c.runToggle(fs.Args()[1:], stdout, stderr)
	case "clear":
		return c.runClear(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printPinUsage(stdout)
		return nil
	default:
		printPinUsage(stderr)
		return fmt.Errorf("unknown pin subcommand: %s", fs.Arg(0))
	}
}

func (c *pinCommand) runList(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		printPinUsage(stderr)
		return fmt.Errorf("pin list does not accept positional arguments")
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	items, err := store.List()
	if err != nil {
		return fmt.Errorf("list pins: %w", err)
	}

	for _, item := range items {
		if _, err := fmt.Fprintln(stdout, item); err != nil {
			return err
		}
	}

	return nil
}

func (c *pinCommand) runAdd(args []string, stdout, stderr io.Writer) error {
	pin, err := requireSingleDirArg("pin add", args, stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}
	if err := store.Add(pin); err != nil {
		return fmt.Errorf("add pin: %w", err)
	}

	_, err = fmt.Fprintf(stdout, "pinned: %s\n", pin)
	return err
}

func (c *pinCommand) runRemove(args []string, stdout, stderr io.Writer) error {
	pin, err := requireSingleDirArg("pin remove", args, stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}
	if err := store.Remove(pin); err != nil {
		return fmt.Errorf("remove pin: %w", err)
	}

	_, err = fmt.Fprintf(stdout, "unpinned: %s\n", pin)
	return err
}

func (c *pinCommand) runToggle(args []string, stdout, stderr io.Writer) error {
	pin, err := requireSingleDirArg("pin toggle", args, stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	pinned, err := store.Toggle(pin)
	if err != nil {
		return fmt.Errorf("toggle pin: %w", err)
	}

	if pinned {
		_, err = fmt.Fprintf(stdout, "pinned: %s\n", pin)
		return err
	}

	_, err = fmt.Fprintf(stdout, "unpinned: %s\n", pin)
	return err
}

func (c *pinCommand) runClear(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		printPinUsage(stderr)
		return fmt.Errorf("pin clear does not accept positional arguments")
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}
	if err := store.Clear(); err != nil {
		return fmt.Errorf("clear pins: %w", err)
	}

	_, err = fmt.Fprintln(stdout, "cleared pins")
	return err
}

func (c *pinCommand) requireStore() (pinStore, error) {
	if c.storeErr != nil {
		return nil, fmt.Errorf("configure pin store: %w", c.storeErr)
	}
	if c.store == nil {
		return nil, errors.New("configure pin store: pin store is not configured")
	}
	return c.store, nil
}

func requireSingleDirArg(command string, args []string, stderr io.Writer) (string, error) {
	if len(args) != 1 {
		printPinUsage(stderr)
		return "", fmt.Errorf("%s requires exactly 1 <dir> argument", command)
	}
	return filepath.Clean(args[0]), nil
}

func printPinUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux pin list")
	fmt.Fprintln(w, "  projmux pin add <dir>")
	fmt.Fprintln(w, "  projmux pin remove <dir>")
	fmt.Fprintln(w, "  projmux pin toggle <dir>")
	fmt.Fprintln(w, "  projmux pin clear")
}
