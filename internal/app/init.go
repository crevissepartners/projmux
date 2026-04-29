package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// initCommand auto-merges projmux keybindings into a terminal emulator's
// config file. The framework dispatches by terminal name and delegates the
// actual merge to a TerminalAdapter implementation.
type initCommand struct {
	registry *terminalRegistry
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	stat     func(string) (os.FileInfo, error)
}

func newInitCommand() *initCommand {
	return &initCommand{
		registry: defaultTerminalRegistry,
		getenv:   os.Getenv,
		readFile: os.ReadFile,
		stat:     os.Stat,
	}
}

// Run implements the `projmux init [terminal]` flow. The default mode is a
// dry-run that prints the planned changes; --apply commits them with a
// timestamped backup. The terminal name may appear before or after flags,
// e.g. `projmux init ghostty --apply` or `projmux init --apply ghostty`.
func (c *initCommand) Run(args []string, stdout, stderr io.Writer) error {
	terminalName, flagArgs := splitInitArgs(args)

	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "write the merged config (default: dry-run preview)")
	dryRun := fs.Bool("dry-run", false, "force dry-run preview even when no other flag is set")
	configOverride := fs.String("config", "", "override the terminal config path (mostly for tests)")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if *apply && *dryRun {
		return errors.New("init: --apply and --dry-run are mutually exclusive")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("init: unexpected positional argument %q", fs.Arg(0))
	}

	registry := c.registry
	if registry == nil {
		registry = defaultTerminalRegistry
	}

	var (
		adapter TerminalAdapter
		ok      bool
	)
	if terminalName != "" {
		name := strings.ToLower(strings.TrimSpace(terminalName))
		adapter, ok = registry.lookup(name)
		if !ok {
			return fmt.Errorf("init: unknown terminal %q (known: %s)", name, strings.Join(registry.names(), ", "))
		}
	} else {
		adapter, ok = registry.detect(c.env())
		if !ok {
			return fmt.Errorf("init: could not auto-detect terminal; pass one explicitly (known: %s)", strings.Join(registry.names(), ", "))
		}
	}

	configPath := strings.TrimSpace(*configOverride)
	if configPath == "" {
		path, err := adapter.ConfigPath(c.env())
		if err != nil {
			return fmt.Errorf("init: resolve %s config path: %w", adapter.Name(), err)
		}
		configPath = path
	}

	current, exists, err := c.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("init: read %s: %w", configPath, err)
	}

	plan, err := adapter.PlanMerge(current, exists)
	if err != nil {
		return fmt.Errorf("init: plan %s merge: %w", adapter.Name(), err)
	}
	plan.ConfigPath = configPath

	if !*apply {
		return c.printPlan(adapter.Name(), plan, stdout)
	}

	if err := adapter.ApplyMerge(plan); err != nil {
		return fmt.Errorf("init: apply %s merge: %w", adapter.Name(), err)
	}

	return c.printApplyResult(adapter.Name(), plan, stdout)
}

func (c *initCommand) env() func(string) string {
	if c.getenv != nil {
		return c.getenv
	}
	return os.Getenv
}

// loadConfig reads the terminal config and reports whether it already exists.
// A missing file is not an error; the merge will create it.
func (c *initCommand) loadConfig(path string) (string, bool, error) {
	statFn := c.stat
	if statFn == nil {
		statFn = os.Stat
	}
	if _, err := statFn(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	readFn := c.readFile
	if readFn == nil {
		readFn = os.ReadFile
	}
	data, err := readFn(path)
	if err != nil {
		return "", true, err
	}
	return string(data), true, nil
}

func (c *initCommand) printPlan(terminal string, plan MergePlan, stdout io.Writer) error {
	if _, err := fmt.Fprintf(stdout, "projmux init %s (dry-run)\n", terminal); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "config: %s\n", plan.ConfigPath); err != nil {
		return err
	}
	if plan.CreateNew {
		if _, err := fmt.Fprintln(stdout, "note:   config file does not exist; would be created"); err != nil {
			return err
		}
	}
	for _, ch := range plan.Changes {
		switch ch.Kind {
		case "add":
			if _, err := fmt.Fprintf(stdout, "  +  %s = %s\n", ch.Trigger, ch.Action); err != nil {
				return err
			}
		case "noop":
			if _, err := fmt.Fprintf(stdout, "  =  %s = %s (already set)\n", ch.Trigger, ch.Action); err != nil {
				return err
			}
		case "skip-conflict":
			if _, err := fmt.Fprintf(stdout, "  !  %s already mapped to %s; skipping (want %s)\n", ch.Trigger, ch.Existing, ch.Action); err != nil {
				return err
			}
		}
	}
	if !plan.HasEffect() {
		_, err := fmt.Fprintln(stdout, "no changes; already configured")
		return err
	}
	_, err := fmt.Fprintln(stdout, "run with --apply to write changes (a timestamped backup will be created)")
	return err
}

func (c *initCommand) printApplyResult(terminal string, plan MergePlan, stdout io.Writer) error {
	if _, err := fmt.Fprintf(stdout, "projmux init %s --apply\n", terminal); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "config: %s\n", plan.ConfigPath); err != nil {
		return err
	}
	added := 0
	skipped := 0
	for _, ch := range plan.Changes {
		switch ch.Kind {
		case "add":
			added++
		case "skip-conflict":
			skipped++
			if _, err := fmt.Fprintf(stdout, "warning: %s already mapped to %s; skipped (want %s)\n", ch.Trigger, ch.Existing, ch.Action); err != nil {
				return err
			}
		}
	}
	if !plan.HasEffect() {
		_, err := fmt.Fprintln(stdout, "no changes; already configured")
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote %d new keybindings (%d skipped due to user conflict)\n", added, skipped); err != nil {
		return err
	}
	if !plan.CreateNew {
		_, err := fmt.Fprintln(stdout, "previous config saved as <path>.bak.<timestamp>")
		return err
	}
	_, err := fmt.Fprintln(stdout, "created new config")
	return err
}

// splitInitArgs separates the (optional) terminal name from the remaining
// flag-style arguments. The terminal name is the first non-flag token, no
// matter where it appears in the slice. Subsequent non-flag tokens are left
// in flagArgs so the flag parser can complain about them.
func splitInitArgs(args []string) (terminal string, flagArgs []string) {
	flagArgs = make([]string, 0, len(args))
	consumed := false
	for _, a := range args {
		if !consumed && a != "" && !strings.HasPrefix(a, "-") {
			terminal = a
			consumed = true
			continue
		}
		flagArgs = append(flagArgs, a)
	}
	return terminal, flagArgs
}

// init registers the Ghostty adapter with the package-level registry. Future
// terminals add a sibling file with their own init() block.
func init() {
	RegisterTerminalAdapter(NewGhosttyAdapter())
}
