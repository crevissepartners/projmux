package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type layoutCommand struct {
	runner    tmuxRunner
	lookupEnv func(string) string
	getwd     func() (string, error)
	now       func() time.Time
}

func newLayoutCommand() *layoutCommand {
	return &layoutCommand{
		runner:    inttmux.ExecRunner{},
		lookupEnv: os.Getenv,
		getwd:     os.Getwd,
		now:       time.Now,
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
	case "save":
		return c.runSave(fs.Args()[1:], stdout, stderr)
	case "remove":
		return c.runRemove(fs.Args()[1:], stdout, stderr)
	case "apply":
		return c.runApply(fs.Args()[1:], stdout, stderr)
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

func (c *layoutCommand) runSave(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("layout save", flag.ContinueOnError)
	fs.SetOutput(stderr)
	description := fs.String("description", "", "layout preset description")
	fresh := fs.Bool("fresh", false, "start from the preset each time instead of autosave state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		printLayoutUsage(stderr)
		return fmt.Errorf("layout save requires exactly 1 argument: <name>")
	}
	if !c.insideTmux() {
		return errors.New("layout save requires a current tmux session")
	}
	if c.runner == nil {
		return errors.New("configure tmux runner: tmux runner is not configured")
	}
	store, err := c.store()
	if err != nil {
		return err
	}

	ctx := context.Background()
	client := inttmux.NewClient(c.runner)
	sessionName, err := client.CurrentSessionName(ctx)
	if err != nil {
		return err
	}
	snap, err := client.CaptureSessionSnapshot(ctx, sessionName, c.nowTime())
	if err != nil {
		return fmt.Errorf("capture layout preset %q from session %q: %w", fs.Arg(0), sessionName, err)
	}
	mode := corelayout.ModeInheritAutosave
	if *fresh {
		mode = corelayout.ModeFreshEachTime
	}
	preset := corelayout.FromSnapshot(snap, store.ProjectRoot, *description, mode)
	if err := store.Save(fs.Arg(0), preset); err != nil {
		return err
	}
	path, err := store.Path(fs.Arg(0))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "saved layout preset: %s (%s, %s) -> %s\n", fs.Arg(0), sessionStateCount(len(preset.Windows), "window"), sessionStateCount(layoutPresetPaneCount(preset), "pane"), path)
	return err
}

func (c *layoutCommand) runRemove(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("layout remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "delete without an interactive confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		printLayoutUsage(stderr)
		return fmt.Errorf("layout remove requires exactly 1 argument: <name>")
	}
	if !*force {
		return fmt.Errorf("layout remove %q requires --force for non-interactive deletion", fs.Arg(0))
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	if err := store.Remove(fs.Arg(0)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "removed layout preset: %s\n", fs.Arg(0))
	return err
}

type layoutApplyOptions struct {
	name   string
	dryRun bool
	force  bool
}

func (c *layoutCommand) runApply(args []string, stdout, stderr io.Writer) error {
	opts, err := parseLayoutApplyArgs(args)
	if err != nil {
		printLayoutUsage(stderr)
		return err
	}
	if !opts.dryRun && !opts.force {
		return fmt.Errorf("layout apply %q requires --force for destructive apply; use --dry-run to preview", opts.name)
	}
	if !c.insideTmux() {
		return errors.New("layout apply requires a current tmux session")
	}
	if c.runner == nil {
		return errors.New("configure tmux runner: tmux runner is not configured")
	}

	ctx := context.Background()
	client := inttmux.NewClient(c.runner)
	sessionName, err := client.CurrentSessionName(ctx)
	if err != nil {
		return err
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	preset, err := store.Load(opts.name)
	if err != nil {
		return err
	}
	snap, err := corelayout.ToSnapshot(preset, sessionName, store.ProjectRoot, c.nowTime())
	if err != nil {
		return fmt.Errorf("convert layout preset %q for session %q: %w", opts.name, sessionName, err)
	}
	if opts.dryRun {
		for _, line := range sessionStateRestorePreviewLines(snap, c.nowTime(), 100) {
			if _, err := fmt.Fprintln(stdout, line); err != nil {
				return err
			}
		}
		return nil
	}

	result, err := sessionstate.ApplyToExistingSession(ctx, c.runner, snap, sessionstate.ApplyToExistingSessionOptions{
		ReplayOptions: sessionstate.ReplayOptions{FallbackCWD: store.ProjectRoot},
	})
	if err != nil {
		return fmt.Errorf("apply layout preset %q to session %q: %w", opts.name, sessionName, err)
	}
	printSessionStateReplayWarnings(stderr, result.Warnings)
	_, err = fmt.Fprintf(stdout, "applied layout preset: %s (%s, %s) -> %s\n", opts.name, sessionStateCount(len(snap.Windows), "window"), sessionStateCount(statusbarSessionStatePaneCount(snap), "pane"), sessionName)
	return err
}

func parseLayoutApplyArgs(args []string) (layoutApplyOptions, error) {
	var opts layoutApplyOptions
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--force":
			opts.force = true
		case "--help", "-h":
			return opts, errors.New("layout apply help is available from `projmux layout help`")
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown layout apply flag: %s", arg)
			}
			if opts.name != "" {
				return opts, errors.New("layout apply requires exactly 1 argument: <name>")
			}
			opts.name = arg
		}
	}
	if opts.name == "" {
		return opts, errors.New("layout apply requires exactly 1 argument: <name>")
	}
	return opts, nil
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

func (c *layoutCommand) insideTmux() bool {
	if c.lookupEnv == nil {
		return strings.TrimSpace(os.Getenv("TMUX")) != ""
	}
	return strings.TrimSpace(c.lookupEnv("TMUX")) != ""
}

func (c *layoutCommand) nowTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func layoutPresetPaneCount(preset corelayout.Preset) int {
	count := 0
	for _, window := range preset.Windows {
		count += len(window.Panes)
	}
	return count
}

func printLayoutWarnings(w io.Writer, warnings []corelayout.Warning) {
	for _, warning := range warnings {
		fmt.Fprintf(w, "warning: skip layout preset %s: %v\n", warning.Path, warning.Err)
	}
}

func printSessionStateReplayWarnings(w io.Writer, warnings []sessionstate.ReplayWarning) {
	for _, warning := range warnings {
		switch warning.Scope {
		case "pane":
			fmt.Fprintf(w, "warning: window %d pane %d cwd %s unavailable; using %s\n", warning.WindowIndex, warning.PaneIndex, warning.CWD, warning.FallbackCWD)
		case "agent":
			fmt.Fprintf(w, "warning: window %d pane %d agent replay skipped: %s\n", warning.WindowIndex, warning.PaneIndex, warning.Reason)
		default:
			fmt.Fprintf(w, "warning: session-state replay %s: %s\n", warning.Scope, warning.Reason)
		}
	}
}

func printLayoutUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux layout list [--json]")
	fmt.Fprintln(w, "  projmux layout show <name>")
	fmt.Fprintln(w, "  projmux layout save [--description <text>] [--fresh] <name>")
	fmt.Fprintln(w, "  projmux layout remove --force <name>")
	fmt.Fprintln(w, "  projmux layout apply <name> --dry-run")
	fmt.Fprintln(w, "  projmux layout apply <name> --force")
}
