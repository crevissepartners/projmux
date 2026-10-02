// Package hookcmd implements `projmux hook`: list, edit, validate, trust, and
// untrust lifecycle hook config, and the trust prompt the hook-trust popup
// renders.
package hookcmd

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/terminaltext"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/theme"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// Command implements the `projmux hook` CLI surface. The CLI shares the
// declarative engine with the Settings popup so the two surfaces produce
// equivalent results — list/edit/validate/trust/untrust all route through
// the same hooks package APIs (LoadGlobalConfig / LoadProjectConfigFile /
// UpdateProjectConfig / UpdateGlobalConfig / MergeEffective /
// TrustProjectConfig / UntrustProjectConfig).
//
// Phase 2.6 dropped the script branch, so `edit` always operates on
// [hooks.<event>] run = "..." declarative entries.
type Command struct {
	// homeDir and lookupEnv are seams used by tests to redirect XDG paths.
	homeDir   func() (string, error)
	lookupEnv func(string) string
	// getwd returns the current working directory. Defaults to os.Getwd so
	// callers can override in tests without setting PROJMUX_CWD.
	getwd func() (string, error)
	// stdin is the inline editor's source of typed input. Defaults to
	// os.Stdin.
	stdin io.Reader
	// editorRunner runs $EDITOR-style commands for `edit --editor`. Tests
	// stub it to a no-op so the CLI can be exercised headlessly.
	editorRunner EditorRunner
	deps         Deps
}

// EditorRunner runs an editor command with args, writing to stdout and stderr.
type EditorRunner func(command string, args []string, stdout, stderr io.Writer) error

// Deps are the app-wide helpers the hook command resolves through, so it
// reads paths, locale, and operands the way every other route does.
type Deps struct {
	// ConfigPaths resolves the config and state paths; the trust store lives
	// in the state directory.
	ConfigPaths func(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error)
	// Locale and LocalizeText render the project scope note.
	Locale       func(homeDir func() (string, error), lookupEnv func(string) string) i18n.Locale
	LocalizeText func(locale i18n.Locale, key i18n.Key, fallback string) string
	// IsMissingHome reports the missing-HOME reason, and PathOrReason is what
	// a path display shows in its place.
	IsMissingHome func(err error) bool
	PathOrReason  func(path string, err error) string
	// SplitOperands refuses an unknown flag of a route without a FlagSet, and
	// PrintRouteNotes prints the catalog notes of a route under its usage.
	SplitOperands   func(command string, args []string, stderr io.Writer) ([]string, error)
	PrintRouteNotes func(w io.Writer, route string)
}

// New builds `projmux hook` over homeDir, lookupEnv, and getwd. Inline edits
// read stdin, and `edit --editor` opens the editor through editorRunner.
func New(homeDir func() (string, error), lookupEnv func(string) string, getwd func() (string, error), stdin io.Reader, editorRunner EditorRunner, deps Deps) *Command {
	return &Command{
		homeDir:      homeDir,
		lookupEnv:    lookupEnv,
		getwd:        getwd,
		stdin:        stdin,
		editorRunner: editorRunner,
		deps:         deps,
	}
}

// DefaultEditorRunner runs the editor command attached to the process's
// stdin.
func DefaultEditorRunner(command string, args []string, stdout, stderr io.Writer) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("editor command is empty")
	}
	cmd := exec.Command(command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Run is the top-level dispatcher for `projmux hook <verb>`.
func (c *Command) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cli.SetRouteUsage(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return cli.FlagParseError(err)
	}
	if fs.NArg() == 0 {
		cli.WriteRouteUsage(stderr, "hook")
		c.printHookEvents(stderr)
		return &coremetadata.InputError{Detail: "hook requires a subcommand"}
	}
	switch fs.Arg(0) {
	case "list":
		return c.runList(fs.Args()[1:], stdout, stderr)
	case "edit":
		return c.runEdit(fs.Args()[1:], stdout, stderr)
	case "validate":
		return c.runValidate(fs.Args()[1:], stdout, stderr)
	case "trust":
		return c.runTrust(fs.Args()[1:], stdout, stderr)
	case "untrust":
		return c.runUntrust(fs.Args()[1:], stdout, stderr)
	default:
		cli.WriteRouteUsage(stderr, "hook")
		c.printHookEvents(stderr)
		return &coremetadata.InputError{Detail: "unknown hook subcommand: " + fs.Arg(0)}
	}
}

// --- list -----------------------------------------------------------------

type hookListScope int

const (
	hookListScopeContext hookListScope = iota
	hookListScopeGlobal
	hookListScopeProject
	hookListScopeEffective
)

func (c *Command) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cli.SetRouteUsage(fs)
	globalOnly := fs.Bool("global", false, "only show global config entries")
	projectOnly := fs.Bool("project", false, "only show project config entries")
	effective := fs.Bool("effective", false, "show merged effective view with source labels")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return cli.FlagParseError(err)
	}
	if fs.NArg() != 0 {
		cli.WriteRouteUsage(stderr, "hook list")
		c.printHookEvents(stderr)
		return &coremetadata.InputError{Detail: "hook list does not accept positional arguments"}
	}
	flags := 0
	for _, b := range []bool{*globalOnly, *projectOnly, *effective} {
		if b {
			flags++
		}
	}
	if flags > 1 {
		cli.WriteRouteUsage(stderr, "hook list")
		c.printHookEvents(stderr)
		return &coremetadata.InputError{Detail: "hook list: --global, --project, and --effective are mutually exclusive"}
	}
	scope := hookListScopeContext
	switch {
	case *globalOnly:
		scope = hookListScopeGlobal
	case *projectOnly:
		scope = hookListScopeProject
	case *effective:
		scope = hookListScopeEffective
	}
	return c.printList(scope, stdout, stderr)
}

func (c *Command) printList(scope hookListScope, stdout, stderr io.Writer) error {
	globalPath, globalCfg, globalErr := c.loadGlobal()
	if c.deps.IsMissingHome(globalErr) {
		// No config home: there is no global file to read, so the list shows
		// the reason where the path goes instead of an empty path.
		globalPath, globalErr = c.deps.PathOrReason("", globalErr), nil
	}
	if globalErr != nil {
		fmt.Fprintf(stderr, "projmux hook: global config %q parse error: %v\n", globalPath, globalErr)
	}
	projectPath, projectCfg, projectErr, projectCtx := c.loadProject()
	if projectErr != nil {
		fmt.Fprintf(stderr, "projmux hook: project config %q parse error: %v\n", projectPath, projectErr)
	}
	projectNote := ""
	if projectCtx != "" {
		projectNote = c.projectScopeNote()
	}

	switch scope {
	case hookListScopeGlobal:
		return c.writeScopeTable(stdout, "global", globalPath, globalCfg, "")
	case hookListScopeProject:
		if projectCtx == "" {
			fmt.Fprintln(stdout, "no project context (run from inside a project tree or set PROJMUX_CWD)")
			return nil
		}
		return c.writeScopeTable(stdout, "project", projectPath, projectCfg, projectNote)
	case hookListScopeEffective:
		return c.writeEffectiveTable(stdout, globalPath, projectPath, projectCtx, projectNote, globalCfg, projectCfg)
	default:
		// Default view: render both global and project tables so the user
		// sees the full active set in one shot.
		if err := c.writeScopeTable(stdout, "global", globalPath, globalCfg, ""); err != nil {
			return err
		}
		fmt.Fprintln(stdout)
		if projectCtx == "" {
			fmt.Fprintln(stdout, "project: no project context (run from inside a project tree or set PROJMUX_CWD)")
			return nil
		}
		return c.writeScopeTable(stdout, "project", projectPath, projectCfg, projectNote)
	}
}

// writeScopeTable renders one config file's hooks. note, when non-empty, is
// printed under the file path (see projectScopeNote).
func (c *Command) writeScopeTable(stdout io.Writer, scope, path string, cfg hooks.ProjectConfig, note string) error {
	fmt.Fprintf(stdout, "%s config: %s\n", scope, path)
	if note != "" {
		fmt.Fprintln(stdout, note)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EVENT\tSTATE\tRUN")
	for _, event := range hooks.SupportedEvents {
		run := ""
		if cfg.Hooks != nil {
			run = strings.TrimSpace(cfg.Hooks[event])
		}
		state := "missing"
		if run != "" {
			state = "active"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", hooks.DisplayEventName(event), state, run)
	}
	return tw.Flush()
}

func (c *Command) writeEffectiveTable(stdout io.Writer, globalPath, projectPath, projectCtx, projectNote string, globalCfg, projectCfg hooks.ProjectConfig) error {
	fmt.Fprintf(stdout, "global config:  %s\n", globalPath)
	if projectCtx == "" {
		fmt.Fprintln(stdout, "project config: (no project context)")
	} else {
		fmt.Fprintf(stdout, "project config: %s\n", projectPath)
		if projectNote != "" {
			fmt.Fprintln(stdout, projectNote)
		}
	}

	// All effective sections come straight from the shared MergeEffective
	// engine so the CLI's --effective view stays wire-compatible with the
	// Settings popup. Hook 4 (#165) added the Hooks field to EffectiveConfig
	// so we no longer need a parallel hook-resolution helper here.
	merged := hooks.MergeEffective(globalCfg, projectCfg)

	// Render the hooks section first with the dedicated EVENT/SOURCE/RUN
	// header. Listing every supported event — even ones with no resolution
	// on either axis — keeps the CLI's surface predictable for CI scripts
	// (the Settings popup is allowed to hide unset rows for brevity, but a
	// CLI reader benefits from the explicit "no entry" line).
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "[hooks]  source=%s\n", merged.Hooks.Source)
	hookTW := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(hookTW, "EVENT\tSOURCE\tRUN")
	resolved := map[string]hooks.EffectiveEntry{}
	for _, entry := range merged.Hooks.Entries {
		resolved[entry.Key] = entry
	}
	for _, event := range hooks.SupportedEvents {
		name := hooks.DisplayEventName(event)
		if entry, ok := resolved[string(event)]; ok {
			fmt.Fprintf(hookTW, "%s\t%s\t%s\n", name, entry.Source, entry.Value)
			continue
		}
		fmt.Fprintf(hookTW, "%s\t%s\t%s\n", name, hooks.EffectiveSourceDefault, "(unset)")
	}
	if err := hookTW.Flush(); err != nil {
		return err
	}

	// Data-only effective sections (env / startup) — we skip Hooks
	// in this loop because it was already rendered above with the EVENT
	// header. Sensitive-value redaction is scoped to [env] keys; applying
	// it to [hooks] or [startup] would mangle legitimate command lines
	// that happen to contain "TOKEN" / "SECRET" / "KEY" / "PASSWORD".
	for _, section := range merged.Sections() {
		if section.Name == merged.Hooks.Name {
			continue
		}
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "[%s]  source=%s\n", section.Name, section.Source)
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KEY\tSOURCE\tVALUE")
		if len(section.Entries) == 0 {
			fmt.Fprintln(tw, "(none)\t\t")
		}
		for _, entry := range section.Entries {
			value := entry.Value
			if section.Name == "env" && hooks.IsSensitiveEnvKey(entry.Key) && value != "" {
				value = hooks.SensitiveRedaction
			}
			if value == "" {
				value = "(unset)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", entry.Key, entry.Source, value)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// --- edit ----------------------------------------------------------------

func (c *Command) runEdit(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook edit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cli.SetRouteUsage(fs)
	global := fs.Bool("global", false, "edit the global config.toml entry")
	project := fs.Bool("project", false, "force a project-local override in .projmux/config.toml")
	useEditor := fs.Bool("editor", false, "open the config.toml file in $EDITOR instead of the inline prompt")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return cli.FlagParseError(err)
	}
	if *global && *project {
		cli.WriteRouteUsage(stderr, "hook edit")
		c.printHookEvents(stderr)
		return &coremetadata.InputError{Detail: "hook edit: --global and --project are mutually exclusive"}
	}
	if fs.NArg() != 1 {
		cli.WriteRouteUsage(stderr, "hook edit")
		c.printHookEvents(stderr)
		return &coremetadata.InputError{Detail: "hook edit requires exactly one <event> argument"}
	}
	event := strings.TrimSpace(fs.Arg(0))
	if !isSupportedHookEvent(event) {
		return &coremetadata.InputError{Detail: fmt.Sprintf("unsupported hook event %q (supported: %s)", event, supportedHookEventList())}
	}

	if *global {
		path, err := c.globalConfigPath()
		if err != nil {
			return err
		}
		if *useEditor {
			return c.openInEditor(path, stdout, stderr)
		}
		return c.editGlobalInline(path, event, stdout, stderr)
	}

	repo, err := c.resolveProjectContext()
	if err != nil {
		return err
	}
	if repo == "" {
		return errors.New("hook edit requires a project context; run inside a project tree or set PROJMUX_CWD")
	}
	if !*project {
		source, sourcePath, err := c.effectiveHookSource(event)
		if err != nil {
			return err
		}
		if source == hooks.EffectiveSourceGlobal {
			return fmt.Errorf("hook %q is defined at %s; edit that file directly or run 'projmux hook edit --project %s' to create a project override", event, sourcePath, event)
		}
	}
	path := filepath.Join(repo, ".projmux", "config.toml")
	if *useEditor {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create project config dir: %w", err)
		}
		if err := c.openInEditor(path, stdout, stderr); err != nil {
			return err
		}
		return c.printProjectScopeNote(stdout)
	}
	if err := c.editProjectInline(repo, path, event, stdout, stderr); err != nil {
		return err
	}
	return c.printProjectScopeNote(stdout)
}

func (c *Command) effectiveHookSource(event string) (hooks.EffectiveSource, string, error) {
	globalPath, globalCfg, globalErr := c.loadGlobal()
	if c.deps.IsMissingHome(globalErr) {
		// No config home: no global entry can be the source.
		globalErr = nil
	}
	if globalErr != nil {
		return "", "", globalErr
	}
	projectPath, projectCfg, projectErr, _ := c.loadProject()
	if projectErr != nil {
		return "", "", projectErr
	}
	merged := hooks.MergeEffective(globalCfg, projectCfg)
	for _, entry := range merged.Hooks.Entries {
		if entry.Key != event || strings.TrimSpace(entry.Value) == "" {
			continue
		}
		switch entry.Source {
		case hooks.EffectiveSourceProject:
			return entry.Source, projectPath, nil
		case hooks.EffectiveSourceGlobal:
			return entry.Source, globalPath, nil
		default:
			return entry.Source, "", nil
		}
	}
	return hooks.EffectiveSourceDefault, "", nil
}

func (c *Command) editGlobalInline(path, event string, stdout, stderr io.Writer) error {
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return err
	}
	current := ""
	if cfg.Hooks != nil {
		current = cfg.Hooks[hooks.Event(event)]
	}
	value, ok, err := c.readInlineLine(event, current, stdout)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(stdout, "no change")
		return nil
	}
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		if cfg.Hooks == nil {
			cfg.Hooks = map[hooks.Event]string{}
		}
		if value == "" {
			delete(cfg.Hooks, hooks.Event(event))
		} else {
			cfg.Hooks[hooks.Event(event)] = value
		}
		return nil
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %s\n", path)
	return err
}

func (c *Command) editProjectInline(repo, path, event string, stdout, stderr io.Writer) error {
	cfg, err := hooks.LoadProjectConfigFile(path)
	if err != nil {
		return err
	}
	current := ""
	if cfg.Hooks != nil {
		current = cfg.Hooks[hooks.Event(event)]
	}
	value, ok, err := c.readInlineLine(event, current, stdout)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(stdout, "no change")
		return nil
	}
	if _, err := hooks.UpdateProjectConfig(path, func(cfg *hooks.ProjectConfig) error {
		if cfg.Hooks == nil {
			cfg.Hooks = map[hooks.Event]string{}
		}
		if value == "" {
			delete(cfg.Hooks, hooks.Event(event))
		} else {
			cfg.Hooks[hooks.Event(event)] = value
		}
		return nil
	}); err != nil {
		return err
	}
	trustPath, err := c.trustStorePath()
	if err != nil {
		return err
	}
	if _, err := hooks.TrustProjectConfig(repo, trustPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote %s\n", path); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "trusted %s\n", path)
	return err
}

// readInlineLine prompts for one line of input. Returning ok=false means the
// caller pressed Ctrl-D / EOF without typing anything, which is treated as a
// no-op so the existing entry is preserved. An empty (whitespace-only) line
// IS a change — it deletes the entry, matching the Settings popup behaviour.
func (c *Command) readInlineLine(event, current string, stdout io.Writer) (string, bool, error) {
	if c.stdin == nil {
		return "", false, errors.New("inline edit requires stdin")
	}
	if current == "" {
		fmt.Fprintf(stdout, "current [hooks.%s] run = (unset)\n", event)
	} else {
		fmt.Fprintf(stdout, "current [hooks.%s] run = %s\n", event, current)
	}
	fmt.Fprintf(stdout, "new run (empty to clear, Ctrl-D to abort) > ")
	reader := bufio.NewReader(c.stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", false, err
	}
	if err == io.EOF && len(line) == 0 {
		fmt.Fprintln(stdout)
		return "", false, nil
	}
	value := strings.TrimRight(line, "\r\n")
	value = strings.TrimSpace(value)
	return value, true, nil
}

// EditorFromEnv is the editor command a file-editing route opens: $EDITOR,
// then $VISUAL. Empty means neither is set.
func EditorFromEnv(lookupEnv func(string) string) string {
	editor := strings.TrimSpace(lookupEnv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(lookupEnv("VISUAL"))
	}
	return editor
}

func (c *Command) openInEditor(path string, stdout, stderr io.Writer) error {
	editor := EditorFromEnv(c.lookupEnv)
	if editor == "" {
		return errors.New("$EDITOR and $VISUAL are unset; cannot open editor")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	parts := strings.Fields(editor)
	cmd := parts[0]
	args := append(parts[1:], path)
	if c.editorRunner == nil {
		c.editorRunner = DefaultEditorRunner
	}
	if err := c.editorRunner(cmd, args, stdout, stderr); err != nil {
		return fmt.Errorf("editor %q exited: %w", editor, err)
	}
	// Validate after the editor closes so a malformed save is reported
	// straight away. A missing file is allowed (the user may have aborted).
	if _, err := os.Stat(path); err == nil {
		if _, err := hooks.LoadProjectConfigFile(path); err != nil {
			fmt.Fprintf(stderr, "projmux hook: %s did not parse: %v\n", path, err)
			return &editorParseError{path: path, err: err}
		}
	}
	_, err := fmt.Fprintf(stdout, "edited %s\n", path)
	return err
}

type editorParseError struct {
	path string
	err  error
}

func (e *editorParseError) Error() string {
	return fmt.Sprintf("editor saved invalid config %q: %v", e.path, e.err)
}

func (e *editorParseError) Unwrap() error { return e.err }

func (e *editorParseError) ExitCode() int { return 1 }

// --- validate ------------------------------------------------------------

func (c *Command) runValidate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cli.SetRouteUsage(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return cli.FlagParseError(err)
	}
	if fs.NArg() != 0 {
		cli.WriteRouteUsage(stderr, "hook validate")
		c.printHookEvents(stderr)
		return &coremetadata.InputError{Detail: "hook validate does not accept positional arguments"}
	}
	globalPath, globalCfg, globalErr := c.loadGlobal()
	projectPath, projectCfg, projectErr, projectCtx := c.loadProject()

	ok := true
	if c.deps.IsMissingHome(globalErr) {
		// No config home: there is no global file to validate.
		fmt.Fprintf(stdout, "global   %s   skipped\n", c.deps.PathOrReason("", globalErr))
	} else if globalErr != nil {
		fmt.Fprintf(stdout, "global   %s   PARSE ERROR: %v\n", globalPath, globalErr)
		ok = false
	} else {
		if err := validateHookEvents(globalCfg); err != nil {
			fmt.Fprintf(stdout, "global   %s   INVALID: %v\n", globalPath, err)
			ok = false
		} else {
			fmt.Fprintf(stdout, "global   %s   OK\n", globalPath)
		}
	}
	if projectCtx == "" {
		fmt.Fprintln(stdout, "project  (no project context — skipping)")
	} else if projectErr != nil {
		fmt.Fprintf(stdout, "project  %s   PARSE ERROR: %v\n", projectPath, projectErr)
		ok = false
	} else {
		if err := validateHookEvents(projectCfg); err != nil {
			fmt.Fprintf(stdout, "project  %s   INVALID: %v\n", projectPath, err)
			ok = false
		} else {
			fmt.Fprintf(stdout, "project  %s   OK\n", projectPath)
		}
	}
	if projectCtx != "" {
		if note := c.projectScopeNote(); note != "" {
			fmt.Fprintln(stdout, note)
		}
	}
	if !ok {
		return &hookValidateError{}
	}
	return nil
}

// hookValidateError flags validation failure with a non-default exit code so
// CI scripts can branch on `projmux hook validate`. The user-facing diagnostic
// is already written to stdout, so main suppresses the error string.
type hookValidateError struct{}

func (e *hookValidateError) Error() string   { return "hook validate failed" }
func (e *hookValidateError) ExitCode() int   { return 1 }
func (e *hookValidateError) IsHookValidate() {}

func validateHookEvents(cfg hooks.ProjectConfig) error {
	for event := range cfg.Hooks {
		if !isSupportedHookEvent(string(event)) {
			return fmt.Errorf("unsupported hook event %q", event)
		}
	}
	return nil
}

// --- trust / untrust -----------------------------------------------------

func (c *Command) runTrust(args []string, stdout, stderr io.Writer) error {
	repo, fromContext, err := c.resolveTrustTarget("hook trust", args, stderr, func() { cli.WriteRouteUsage(stderr, "hook trust"); c.printHookEvents(stderr) })
	if err != nil {
		return err
	}
	trustPath, err := c.trustStorePath()
	if err != nil {
		return err
	}
	sum, err := hooks.TrustProjectConfig(repo, trustPath)
	if err != nil {
		return fmt.Errorf("trust %s: %w", repo, err)
	}
	if _, err := fmt.Fprintf(stdout, "trusted %s\n  .projmux/config.toml sha256=%s\n", repo, sum); err != nil {
		return err
	}
	if !fromContext {
		return nil
	}
	return c.printProjectScopeNote(stdout)
}

func (c *Command) runUntrust(args []string, stdout, stderr io.Writer) error {
	repo, fromContext, err := c.resolveTrustTarget("hook untrust", args, stderr, func() { cli.WriteRouteUsage(stderr, "hook untrust"); c.printHookEvents(stderr) })
	if err != nil {
		return err
	}
	trustPath, err := c.trustStorePath()
	if err != nil {
		return err
	}
	removed, err := hooks.UntrustProjectConfig(repo, trustPath)
	if err != nil {
		return fmt.Errorf("untrust %s: %w", repo, err)
	}
	if removed {
		_, err = fmt.Fprintf(stdout, "untrusted %s\n", repo)
	} else {
		_, err = fmt.Fprintf(stdout, "no trust entry for %s\n", repo)
	}
	if err != nil || !fromContext {
		return err
	}
	return c.printProjectScopeNote(stdout)
}

// resolveTrustTarget rejects unknown flags before it reads the project
// context or the trust store; command names the verb in that rejection, which
// prints only the reason and the catalog Usage (splitOperands). printUsage
// prints that verb's usage under every other refusal.
// fromContext reports that the target is the project context rather than an
// explicit <project> argument, so only then can a scope note apply.
func (c *Command) resolveTrustTarget(command string, args []string, stderr io.Writer, printUsage func()) (repo string, fromContext bool, err error) {
	args, err = c.deps.SplitOperands(command, args, stderr)
	if err != nil {
		return "", false, err
	}
	switch len(args) {
	case 0:
		repo, err := c.resolveProjectContext()
		if err != nil {
			return "", false, err
		}
		if repo == "" {
			printUsage()
			return "", false, &coremetadata.InputError{Detail: "trust/untrust requires <project> or a project context"}
		}
		return repo, true, nil
	case 1:
		raw := strings.TrimSpace(args[0])
		if raw == "" {
			printUsage()
			return "", false, &coremetadata.InputError{Detail: "trust/untrust <project> must not be empty"}
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", false, fmt.Errorf("resolve %q: %w", raw, err)
		}
		return filepath.Clean(abs), false, nil
	default:
		printUsage()
		return "", false, &coremetadata.InputError{Detail: "trust/untrust takes at most one <project> argument"}
	}
}

// --- shared helpers ------------------------------------------------------

func (c *Command) globalConfigPath() (string, error) {
	return hooks.GlobalConfigPath(c.lookupEnv, c.homeDir)
}

func (c *Command) trustStorePath() (string, error) {
	paths, err := c.deps.ConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, "trusted-projects.json"), nil
}

func (c *Command) loadGlobal() (string, hooks.ProjectConfig, error) {
	path, err := c.globalConfigPath()
	if err != nil {
		return "", hooks.ProjectConfig{}, err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	return path, cfg, err
}

// loadProject returns the resolved project context's config.toml path, parsed
// config, parse error, and the project context root. An empty projectCtx
// signals "no project context"; callers should branch on it. The returned
// path is always populated when projectCtx != "" so error messages can name
// the file even when the parse itself failed.
func (c *Command) loadProject() (string, hooks.ProjectConfig, error, string) {
	repo, err := c.resolveProjectContext()
	if err != nil || repo == "" {
		return "", hooks.ProjectConfig{}, nil, ""
	}
	path := filepath.Join(repo, ".projmux", "config.toml")
	cfg, err := hooks.LoadProjectConfigFile(path)
	return path, cfg, err, repo
}

// resolveProjectContext mirrors the Settings UI's "what project am I in"
// resolution but trimmed for CLI use: PROJMUX_CWD wins (so tmux-launched
// CLI invocations inherit the pane's project), otherwise we fall back to
// `os.Getwd()`. A working directory with its own `.projmux/config.toml` is
// the context, because that is the file the hook runner reads for sessions
// created there; otherwise we walk upward to the nearest `.projmux` or `.git`
// marker. The implicit walk stops before considering the system temp root
// itself so temp fixtures and other scratch parents do not become project
// contexts. Returning an empty string is not an error; downstream commands
// decide whether the context is required.
func (c *Command) resolveProjectContext() (string, error) {
	root, _, err := c.resolveProjectScope()
	return root, err
}

// resolveProjectScope returns the project context root and the directory the
// command runs from (PROJMUX_CWD, else the working directory). The two differ
// only when the root was found by walking up from the working directory.
func (c *Command) resolveProjectScope() (root, cwd string, err error) {
	if c.lookupEnv != nil {
		if raw := strings.TrimSpace(c.lookupEnv("PROJMUX_CWD")); raw != "" {
			cwd = filepath.Clean(raw)
			return cwd, cwd, nil
		}
	}
	if c.getwd == nil {
		return "", "", nil
	}
	wd, err := c.getwd()
	if err != nil {
		return "", "", err
	}
	wd = filepath.Clean(wd)
	if hooks.SessionProjectConfigPath(wd) != "" {
		return wd, wd, nil
	}
	if root := NearestProjectMarker(wd, os.TempDir()); root != "" {
		return root, wd, nil
	}
	return "", wd, nil
}

// hookProjectScopeNoteFallback is the en-US text of i18n.KeyHookProjectScopeNote.
const hookProjectScopeNoteFallback = "note: sessions created in {cwd} do not run this file's pre-create, post-create, or post-attach hooks, [startup], or [env]; only sessions created in {root} do"

// projectScopeNote says, when the project context was found by walking up,
// that sessions created in the current directory do not read the context's
// config: the hook runner reads only <session dir>/.projmux/config.toml. It
// names the session-scoped surfaces only. send-noti is left out because its
// dispatcher resolves the project root on its own and does run that file's
// send-noti hook.
func (c *Command) projectScopeNote() string {
	root, cwd, err := c.resolveProjectScope()
	if err != nil || root == "" || root == cwd {
		return ""
	}
	template := c.deps.LocalizeText(c.deps.Locale(c.homeDir, c.lookupEnv), i18n.KeyHookProjectScopeNote, hookProjectScopeNoteFallback)
	return strings.NewReplacer("{cwd}", cwd, "{root}", root).Replace(template)
}

// printProjectScopeNote writes projectScopeNote on its own line after a
// command that acted on the project context, when there is a note to show.
func (c *Command) printProjectScopeNote(stdout io.Writer) error {
	note := c.projectScopeNote()
	if note == "" {
		return nil
	}
	_, err := fmt.Fprintln(stdout, note)
	return err
}

// NearestProjectMarker walks parent directories looking for a `.projmux` or
// `.git` marker. Boundary paths are not considered candidates. Returns "" when
// the walk reaches a boundary or the filesystem root with nothing found.
func NearestProjectMarker(path string, boundaries ...string) string {
	path = filepath.Clean(path)
	for {
		for _, boundary := range boundaries {
			boundary = filepath.Clean(strings.TrimSpace(boundary))
			if boundary != "" && boundary != "." && path == boundary {
				return ""
			}
		}
		if hookMarkerExists(filepath.Join(path, ".projmux")) || hookMarkerExists(filepath.Join(path, ".git")) {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}

func hookMarkerExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func isSupportedHookEvent(event string) bool {
	for _, e := range hooks.SupportedEvents {
		if string(e) == event {
			return true
		}
	}
	return false
}

// printHookEvents prints the catalog note of `hook`, the hook events, under
// the hook usage block.
func (c *Command) printHookEvents(w io.Writer) {
	c.deps.PrintRouteNotes(w, "hook")
}

func supportedHookEventList() string {
	names := make([]string, 0, len(hooks.SupportedEvents))
	for _, e := range hooks.SupportedEvents {
		names = append(names, string(e))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// --- trust prompt --------------------------------------------------------

const hookTrustPopupContentWidth = 86

// TrustPrompt renders the trust request req on writer and reads the choice
// from reader. Anything but a recognised choice, three times, or end of input
// is a deny.
func TrustPrompt(reader io.Reader, writer io.Writer, req hooks.ProjectHookPromptRequest) hooks.ProjectHookDecision {
	fmt.Fprintln(writer, hookTrustHeaderStart+" Trust project automation "+projmuxpicker.Reset)
	scope := hookTrustRequestScope(req)
	fmt.Fprintln(writer, hookTrustMuted(scope.description))
	fmt.Fprintln(writer)
	writeHookTrustField(writer, "repo", req.RepoPath)
	writeHookTrustField(writer, scope.label, req.RelativePath)
	if req.PreviousSHA256 != "" {
		writeHookTrustField(writer, "trusted sha", req.PreviousSHA256)
	}
	writeHookTrustField(writer, "current sha", req.SHA256)
	if strings.TrimSpace(req.Preview) != "" {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, projmuxpicker.SeparatorLine(hookTrustPopupContentWidth))
		fmt.Fprintln(writer, hookTrustMuted("preview"))
		for line := range strings.SplitSeq(req.Preview, "\n") {
			safeLine := terminaltext.EscapeControls(line)
			fmt.Fprintln(writer, "  "+projmuxpicker.TruncateANSI(safeLine, hookTrustPopupContentWidth-2))
		}
	}

	fmt.Fprintln(writer)
	fmt.Fprintln(writer, projmuxpicker.SeparatorLine(hookTrustPopupContentWidth))
	fmt.Fprintln(writer, hookTrustActionLine("[o] Allow once", "run this time only"))
	fmt.Fprintln(writer, hookTrustActionLine("[a] Allow always", "trust this exact file hash"))
	fmt.Fprintln(writer, hookTrustActionLine("[d] Deny", scope.denyDetail))

	input := bufio.NewReader(reader)
	for range 3 {
		fmt.Fprint(writer, "\n"+hookTrustMuted("choice")+"  ")
		line, err := input.ReadString('\n')
		if err != nil && len(line) == 0 {
			fmt.Fprintln(writer)
			return hooks.ProjectHookDeny
		}
		decision := ParseTrustDecision(line)
		if decision != "" {
			return decision
		}
		fmt.Fprintln(writer, hookTrustMuted("Enter o, a, or d."))
	}
	return hooks.ProjectHookDeny
}

type hookTrustScopeCopy struct {
	label       string
	description string
	denyDetail  string
}

func hookTrustRequestScope(req hooks.ProjectHookPromptRequest) hookTrustScopeCopy {
	if strings.TrimSpace(req.RelativePath) == ".projmux/config.toml" {
		return hookTrustScopeCopy{
			label:       "config",
			description: "Project-local config is disabled until this file hash is trusted.",
			denyDetail:  "skip project config",
		}
	}
	return hookTrustScopeCopy{
		label:       "hook",
		description: "Project-local automation is disabled until this file hash is trusted.",
		denyDetail:  "skip this hook",
	}
}

func writeHookTrustField(w io.Writer, label, value string) {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(terminaltext.EscapeControls(value))
	if value == "" {
		value = "-"
	}
	fmt.Fprintf(w, "%s  %s\n",
		hookTrustMuted(fmt.Sprintf("%-11s", label)),
		projmuxpicker.TruncateANSI(value, hookTrustPopupContentWidth-13),
	)
}

func hookTrustActionLine(action, detail string) string {
	return fmt.Sprintf("  %-12s %s", action, hookTrustMuted(detail))
}

// hook-trust popup role escapes (bright Phase 2, B3). Defaults are the
// historical fallback literals (byte-identical); ApplyTheme repoints them at
// the resolved effective theme at command entry.
var (
	hookTrustHeaderStart = projmuxpicker.CurrentStart
	hookTrustMutedStart  = projmuxpicker.MutedStart
)

// ApplyTheme repoints the trust prompt role escapes at roles. Applying the
// fallback theme's roles restores byte-identity.
func ApplyTheme(roles theme.ANSIRoles) {
	hookTrustHeaderStart = roles.SurfaceActive
	hookTrustMutedStart = roles.TextMuted
}

func hookTrustMuted(value string) string {
	return hookTrustMutedStart + value + projmuxpicker.Reset
}

// ParseTrustDecision maps a typed choice to its decision; "" is no choice.
func ParseTrustDecision(value string) hooks.ProjectHookDecision {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "o", "once", string(hooks.ProjectHookAllowOnce):
		return hooks.ProjectHookAllowOnce
	case "a", "always", string(hooks.ProjectHookAllowAlways):
		return hooks.ProjectHookAllowAlways
	case "d", "deny", "n", "no":
		return hooks.ProjectHookDeny
	default:
		return ""
	}
}
