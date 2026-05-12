package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
)

type layoutCommand struct {
	lookupEnv func(string) string
	getwd     func() (string, error)
}

func newLayoutCommand() *layoutCommand {
	return &layoutCommand{
		lookupEnv: os.Getenv,
		getwd:     os.Getwd,
	}
}

func (c *layoutCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("layout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printLayoutUsage(stderr)
		return errors.New("layout requires a subcommand")
	}

	switch fs.Arg(0) {
	case "list":
		return c.runList(fs.Args()[1:], stdout, stderr)
	case "show":
		return c.runShow(fs.Args()[1:], stdout, stderr)
	case "save", "remove", "apply":
		return fmt.Errorf("layout %s is not implemented in this release", fs.Arg(0))
	case "help", "--help", "-h":
		printLayoutUsage(stdout)
		return nil
	default:
		printLayoutUsage(stderr)
		return fmt.Errorf("unknown layout subcommand: %s", fs.Arg(0))
	}
}

func (c *layoutCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("layout list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print layout presets as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printLayoutUsage(stderr)
		return fmt.Errorf("layout list does not accept positional arguments")
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	entries, warnings, err := store.List()
	if err != nil {
		return err
	}
	printLayoutWarnings(stderr, warnings)
	if *jsonOut {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fmt.Errorf("encode layout list JSON: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "%s\n", data)
		return err
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(stdout, "no layout presets found")
		return err
	}
	_, _ = fmt.Fprintln(stdout, "NAME\tMODE\tWINDOWS\tPANES\tDESCRIPTION")
	for _, entry := range entries {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%d\t%d\t%s\n", entry.Name, entry.Mode, entry.Windows, entry.Panes, entry.Description); err != nil {
			return err
		}
	}
	return nil
}

func (c *layoutCommand) runShow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("layout show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		printLayoutUsage(stderr)
		return fmt.Errorf("layout show requires exactly 1 argument: <name>")
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	body, err := store.Show(fs.Arg(0))
	if err != nil {
		return err
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	_, err = io.WriteString(stdout, body)
	return err
}

func (c *layoutCommand) store() (corelayout.Store, error) {
	projectRoot, err := c.resolveProjectContext()
	if err != nil {
		return corelayout.Store{}, err
	}
	if projectRoot == "" {
		return corelayout.Store{}, errors.New("layout requires a project context; run inside a project tree or set PROJMUX_CWD")
	}
	return corelayout.NewStore(projectRoot), nil
}

func (c *layoutCommand) resolveProjectContext() (string, error) {
	if c.lookupEnv != nil {
		if raw := strings.TrimSpace(c.lookupEnv("PROJMUX_CWD")); raw != "" {
			return filepath.Clean(raw), nil
		}
	}
	if c.getwd == nil {
		return "", nil
	}
	wd, err := c.getwd()
	if err != nil {
		return "", err
	}
	wd = filepath.Clean(wd)
	if root := nearestProjectMarker(wd, os.TempDir()); root != "" {
		return root, nil
	}
	return "", nil
}

func printLayoutWarnings(w io.Writer, warnings []corelayout.Warning) {
	for _, warning := range warnings {
		fmt.Fprintf(w, "warning: skip layout preset %s: %v\n", warning.Path, warning.Err)
	}
}

func printLayoutUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux layout list [--json]")
	fmt.Fprintln(w, "  projmux layout show <name>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Deferred:")
	fmt.Fprintln(w, "  projmux layout save <name>")
	fmt.Fprintln(w, "  projmux layout remove <name>")
	fmt.Fprintln(w, "  projmux layout apply <name>")
}
