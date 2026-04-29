package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coretags "github.com/crevissepartners/projmux/internal/core/tags"
)

type tagStore interface {
	List() ([]string, error)
	Toggle(name string) (bool, error)
	Clear() error
}

type tagCommand struct {
	store    tagStore
	storeErr error
}

func newTagCommand() *tagCommand {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return &tagCommand{
			storeErr: fmt.Errorf("resolve default config paths: %w", err),
		}
	}

	return &tagCommand{
		store: coretags.NewDefaultStore(paths),
	}
}

// Run manages the configured tag subcommands.
func (c *tagCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printTagUsage(stderr)
		return errors.New("tag requires a subcommand")
	}

	switch fs.Arg(0) {
	case "list":
		return c.runList(fs.Args()[1:], stdout, stderr)
	case "toggle":
		return c.runToggle(fs.Args()[1:], stdout, stderr)
	case "clear":
		return c.runClear(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printTagUsage(stdout)
		return nil
	default:
		printTagUsage(stderr)
		return fmt.Errorf("unknown tag subcommand: %s", fs.Arg(0))
	}
}

func (c *tagCommand) runList(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		printTagUsage(stderr)
		return fmt.Errorf("tag list does not accept positional arguments")
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	items, err := store.List()
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}

	for _, item := range items {
		if _, err := fmt.Fprintln(stdout, item); err != nil {
			return err
		}
	}

	return nil
}

func (c *tagCommand) runToggle(args []string, stdout, stderr io.Writer) error {
	name, err := requireSingleTagArg("tag toggle", args, stderr)
	if err != nil {
		return err
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	tagged, err := store.Toggle(name)
	if err != nil {
		return fmt.Errorf("toggle tag: %w", err)
	}

	if tagged {
		_, err = fmt.Fprintf(stdout, "tagged: %s\n", name)
		return err
	}

	_, err = fmt.Fprintf(stdout, "untagged: %s\n", name)
	return err
}

func (c *tagCommand) runClear(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		printTagUsage(stderr)
		return fmt.Errorf("tag clear does not accept positional arguments")
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}
	if err := store.Clear(); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}

	_, err = fmt.Fprintln(stdout, "cleared tags")
	return err
}

func (c *tagCommand) requireStore() (tagStore, error) {
	if c.storeErr != nil {
		return nil, fmt.Errorf("configure tag store: %w", c.storeErr)
	}
	if c.store == nil {
		return nil, errors.New("configure tag store: tag store is not configured")
	}
	return c.store, nil
}

func requireSingleTagArg(command string, args []string, stderr io.Writer) (string, error) {
	if len(args) != 1 {
		printTagUsage(stderr)
		return "", fmt.Errorf("%s requires exactly 1 <name> argument", command)
	}

	name := strings.TrimSpace(args[0])
	if name == "" {
		printTagUsage(stderr)
		return "", fmt.Errorf("%s requires a non-empty <name> argument", command)
	}
	return name, nil
}

func printTagUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux tag list")
	fmt.Fprintln(w, "  projmux tag toggle <name>")
	fmt.Fprintln(w, "  projmux tag clear")
}
