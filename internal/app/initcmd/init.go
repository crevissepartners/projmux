// Package initcmd implements terminal key-delivery remediation for both the
// canonical `projmux setup terminal` command and its compatibility
// `projmux init` alias. It auto-merges projmux keybindings into a terminal
// emulator's config file via per-terminal TerminalAdapter implementations.
// The desired bindings are injected by the caller (the app package derives
// them from its keybinding catalog), so this package has no dependency on the
// rest of the app.
package initcmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Command auto-merges projmux keybindings into a terminal emulator's
// config file. The framework dispatches by terminal name and delegates the
// actual merge to a TerminalAdapter implementation.
type Command struct {
	registry *terminalRegistry
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	stat     func(string) (os.FileInfo, error)
	// lstat is used for symlink detection; defaults to os.Lstat. Tests can
	// override it to drive the symlink branch without touching the real fs.
	lstat func(string) (os.FileInfo, error)
	// getwd resolves relative --config paths against the caller's cwd.
	getwd func() (string, error)
}

type initOptions struct {
	TerminalName   string
	Apply          bool
	DryRun         bool
	ConfigOverride string
	AllowSymlink   bool
}

type initResult struct {
	Terminal string
	Plan     MergePlan
	Applied  bool
}

type invocation struct {
	command       string
	acceptDryRun  bool
	displayDryRun bool
}

// New builds the init command with the supplied terminal adapters registered.
// Registration panics on duplicate names so wiring bugs surface immediately.
func New(adapters ...TerminalAdapter) *Command {
	registry := newTerminalRegistry()
	for _, a := range adapters {
		registry.register(a)
	}
	return &Command{
		registry: registry,
		getenv:   os.Getenv,
		readFile: os.ReadFile,
		stat:     os.Stat,
		lstat:    os.Lstat,
		getwd:    os.Getwd,
	}
}

// Run implements the legacy `projmux init [terminal]` command surface. The
// application owns the compatibility warning so direct adapter tests can use
// this method without mixing that warning into the remediation result.
func (c *Command) Run(args []string, stdout, stderr io.Writer) error {
	return c.runInvocation(args, stdout, stderr, invocation{
		command:       "projmux init",
		acceptDryRun:  true,
		displayDryRun: true,
	})
}

// RunCanonical implements `projmux setup terminal [terminal]`. Preview is the
// default; unlike the compatibility alias, this surface does not register the
// redundant --dry-run flag.
func (c *Command) RunCanonical(args []string, stdout, stderr io.Writer) error {
	return c.runInvocation(args, stdout, stderr, invocation{
		command:       "projmux setup terminal",
		acceptDryRun:  false,
		displayDryRun: false,
	})
}

func (c *Command) runInvocation(args []string, stdout, stderr io.Writer, inv invocation) error {
	terminalName, flagArgs := splitInitArgs(args)

	fs := flag.NewFlagSet(inv.command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "write the merged config (default: preview only)")
	var dryRun *bool
	if inv.acceptDryRun {
		dryRun = fs.Bool("dry-run", false, "force preview even when no other flag is set")
	}
	configOverride := fs.String("config", "", "explicit config file path (overrides auto-detected candidates)")
	allowSymlink := fs.Bool("allow-symlink", false, "merge into a symlinked config target (default: refuse to mutate symlink targets such as dotfiles repos)")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	dryRunValue := dryRun != nil && *dryRun
	if *apply && dryRunValue {
		return fmt.Errorf("%s: --apply and --dry-run are mutually exclusive", inv.command)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%s: unexpected positional argument %q", inv.command, fs.Arg(0))
	}

	result, err := c.run(initOptions{
		TerminalName:   terminalName,
		Apply:          *apply,
		DryRun:         dryRunValue,
		ConfigOverride: strings.TrimSpace(*configOverride),
		AllowSymlink:   *allowSymlink,
	}, inv.command)
	if err != nil {
		return err
	}
	if result.Applied {
		return c.printApplyResult(inv.command, result.Terminal, result.Plan, stdout)
	}
	return c.printPlan(inv.command, inv.displayDryRun, result.Terminal, result.Plan, stdout)
}

func (c *Command) run(opts initOptions, command ...string) (initResult, error) {
	commandName := "projmux init"
	if len(command) > 0 && command[0] != "" {
		commandName = command[0]
	}
	if opts.Apply && opts.DryRun {
		return initResult{}, fmt.Errorf("%s: --apply and --dry-run are mutually exclusive", commandName)
	}

	registry := c.registry
	if registry == nil {
		registry = newTerminalRegistry()
	}

	var (
		adapter TerminalAdapter
		ok      bool
	)
	if opts.TerminalName != "" {
		name := strings.ToLower(strings.TrimSpace(opts.TerminalName))
		adapter, ok = registry.lookup(name)
		if !ok {
			return initResult{}, fmt.Errorf("%s: unknown terminal %q (known: %s)", commandName, name, strings.Join(registry.names(), ", "))
		}
	} else {
		adapter, ok = registry.detect(c.env())
		if !ok {
			return initResult{}, fmt.Errorf("%s: could not auto-detect terminal; pass one explicitly (known: %s)", commandName, strings.Join(registry.names(), ", "))
		}
	}

	configPath, err := c.resolveConfigPath(adapter, strings.TrimSpace(opts.ConfigOverride), commandName)
	if err != nil {
		return initResult{}, err
	}

	if err := c.guardSymlink(configPath, opts.AllowSymlink, commandName); err != nil {
		return initResult{}, err
	}

	current, exists, err := c.loadConfig(configPath)
	if err != nil {
		return initResult{}, fmt.Errorf("%s: read %s: %w", commandName, configPath, err)
	}

	plan, err := adapter.PlanMerge(current, exists)
	if err != nil {
		return initResult{}, fmt.Errorf("%s: plan %s merge: %w", commandName, adapter.Name(), err)
	}
	plan.ConfigPath = configPath

	if !opts.Apply {
		return initResult{Terminal: adapter.Name(), Plan: plan}, nil
	}

	if err := adapter.ApplyMerge(plan); err != nil {
		return initResult{}, fmt.Errorf("%s: apply %s merge: %w", commandName, adapter.Name(), err)
	}

	return initResult{Terminal: adapter.Name(), Plan: plan, Applied: true}, nil
}

func (c *Command) env() func(string) string {
	if c.getenv != nil {
		return c.getenv
	}
	return os.Getenv
}

// resolveConfigPath chooses the config file the merge should target.
//
// When the caller passes an explicit --config override, that path is used
// verbatim (with relative paths resolved against the cwd). Otherwise the
// adapter is asked for its candidate list (or single ConfigPath, for
// adapters that have not opted into the multi-candidate interface):
//
//   - exactly one candidate exists  -> pick it
//   - both candidates exist         -> ambiguous, require --config <path>
//   - none exist                    -> pick the first (canonical default)
//
// Adapters that only register a single ConfigPath fall through to the same
// logic with a one-element candidate list.
func (c *Command) resolveConfigPath(adapter TerminalAdapter, override string, command ...string) (string, error) {
	commandName := "projmux init"
	if len(command) > 0 && command[0] != "" {
		commandName = command[0]
	}
	if override != "" {
		return c.absConfigPath(override, commandName)
	}

	candidates, err := c.candidatesFor(adapter)
	if err != nil {
		return "", fmt.Errorf("%s: resolve %s config path: %w", commandName, adapter.Name(), err)
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("%s: %s has no config path candidates", commandName, adapter.Name())
	}

	statFn := c.stat
	if statFn == nil {
		statFn = os.Stat
	}
	var existing []string
	for _, cand := range candidates {
		if _, statErr := statFn(cand); statErr == nil {
			existing = append(existing, cand)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("%s: stat %s: %w", commandName, cand, statErr)
		}
	}
	switch len(existing) {
	case 0:
		return candidates[0], nil
	case 1:
		return existing[0], nil
	default:
		return "", fmt.Errorf("%s: multiple %s config files found (%s); pass --config <path> to disambiguate", commandName, adapter.Name(), strings.Join(existing, ", "))
	}
}

// candidatesFor returns the adapter's well-known config path candidates,
// falling back to a single-element list for adapters that have not opted
// into ConfigPathCandidatesResolver.
func (c *Command) candidatesFor(adapter TerminalAdapter) ([]string, error) {
	if multi, ok := adapter.(ConfigPathCandidatesResolver); ok {
		return multi.ConfigPathCandidates(c.env())
	}
	path, err := adapter.ConfigPath(c.env())
	if err != nil {
		return nil, err
	}
	return []string{path}, nil
}

// absConfigPath turns a (possibly relative) --config override into an
// absolute path so downstream stat/symlink checks behave consistently.
func (c *Command) absConfigPath(p string, command ...string) (string, error) {
	commandName := "projmux init"
	if len(command) > 0 && command[0] != "" {
		commandName = command[0]
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	getwd := c.getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	cwd, err := getwd()
	if err != nil {
		return "", fmt.Errorf("%s: resolve --config %q: %w", commandName, p, err)
	}
	return filepath.Join(cwd, p), nil
}

// guardSymlink refuses to merge into a symlinked target unless the caller
// explicitly opts in via --allow-symlink. The default refusal exists because
// dotfiles users commonly symlink terminal configs into a tracked repo, and
// silently editing through the symlink would mutate that repo without their
// knowledge.
func (c *Command) guardSymlink(path string, allow bool, command ...string) error {
	commandName := "projmux init"
	if len(command) > 0 && command[0] != "" {
		commandName = command[0]
	}
	lstatFn := c.lstat
	if lstatFn == nil {
		lstatFn = os.Lstat
	}
	info, err := lstatFn(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s: lstat %s: %w", commandName, path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if allow {
		return nil
	}
	return fmt.Errorf("%s: %s is a symlink; merging would mutate the symlink target (e.g. a dotfiles repo). Pass --config <path> to point at a different file, or --allow-symlink to proceed anyway", commandName, path)
}

// loadConfig reads the terminal config and reports whether it already exists.
// A missing file is not an error; the merge will create it.
func (c *Command) loadConfig(path string) (string, bool, error) {
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

func (c *Command) printPlan(command string, displayDryRun bool, terminal string, plan MergePlan, stdout io.Writer) error {
	mode := "preview"
	if displayDryRun {
		mode = "dry-run"
	}
	if _, err := fmt.Fprintf(stdout, "%s %s (%s)\n", command, terminal, mode); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "config: %s\n", plan.ConfigPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, "purpose: terminal-specific fallback for keys `projmux setup` reports as swallowed"); err != nil {
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

func (c *Command) printApplyResult(command, terminal string, plan MergePlan, stdout io.Writer) error {
	if _, err := fmt.Fprintf(stdout, "%s %s --apply\n", command, terminal); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "config: %s\n", plan.ConfigPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, "purpose: terminal-specific fallback for keys `projmux setup` reports as swallowed"); err != nil {
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
	if _, err := fmt.Fprintln(stdout, "rerun `projmux setup` outside tmux to verify the failing keys now arrive"); err != nil {
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
// flag-style arguments. The terminal name is the first non-flag token that is
// not the value of --config, no matter where it appears in the slice.
// Subsequent non-flag tokens are left in flagArgs so the flag parser can
// complain about them.
func splitInitArgs(args []string) (terminal string, flagArgs []string) {
	flagArgs = make([]string, 0, len(args))
	consumed := false
	configValue := false
	for _, a := range args {
		if configValue {
			flagArgs = append(flagArgs, a)
			configValue = false
			continue
		}
		if a == "--config" || a == "-config" {
			flagArgs = append(flagArgs, a)
			configValue = true
			continue
		}
		if !consumed && a != "" && !strings.HasPrefix(a, "-") {
			terminal = a
			consumed = true
			continue
		}
		flagArgs = append(flagArgs, a)
	}
	return terminal, flagArgs
}
