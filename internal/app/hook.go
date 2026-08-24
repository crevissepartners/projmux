package app

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

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// hookCommand implements the `projmux hook` CLI surface. The CLI shares the
// declarative engine with the Settings popup so the two surfaces produce
// equivalent results — list/edit/validate/trust/untrust all route through
// the same hooks package APIs (LoadGlobalConfig / LoadProjectConfigFile /
// UpdateProjectConfig / UpdateGlobalConfig / MergeEffective /
// TrustProjectConfig / UntrustProjectConfig).
//
// Phase 2.6 dropped the script branch, so `edit` always operates on
// [hooks.<event>] run = "..." declarative entries.
type hookCommand struct {
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
	editorRunner func(command string, args []string, stdout, stderr io.Writer) error
}

func newHookCommand() *hookCommand {
	return &hookCommand{
		homeDir:      os.UserHomeDir,
		lookupEnv:    os.Getenv,
		getwd:        os.Getwd,
		stdin:        os.Stdin,
		editorRunner: defaultEditorRunner,
	}
}

func defaultEditorRunner(command string, args []string, stdout, stderr io.Writer) error {
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
func (c *hookCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printHookUsage(stderr)
		return usageError("hook requires a subcommand")
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
	case "help", "--help", "-h":
		printHookUsage(stdout)
		return nil
	default:
		printHookUsage(stderr)
		return usageError("unknown hook subcommand: " + fs.Arg(0))
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

func (c *hookCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	globalOnly := fs.Bool("global", false, "only show global config entries")
	projectOnly := fs.Bool("project", false, "only show project config entries")
	effective := fs.Bool("effective", false, "show merged effective view with source labels")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printHookUsage(stderr)
		return usageError("hook list does not accept positional arguments")
	}
	flags := 0
	for _, b := range []bool{*globalOnly, *projectOnly, *effective} {
		if b {
			flags++
		}
	}
	if flags > 1 {
		printHookUsage(stderr)
		return usageError("hook list: --global, --project, and --effective are mutually exclusive")
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

func (c *hookCommand) printList(scope hookListScope, stdout, stderr io.Writer) error {
	globalPath, globalCfg, globalErr := c.loadGlobal()
	if globalErr != nil {
		fmt.Fprintf(stderr, "projmux hook: global config %q parse error: %v\n", globalPath, globalErr)
	}
	projectPath, projectCfg, projectErr, projectCtx := c.loadProject()
	if projectErr != nil {
		fmt.Fprintf(stderr, "projmux hook: project config %q parse error: %v\n", projectPath, projectErr)
	}

	switch scope {
	case hookListScopeGlobal:
		return c.writeScopeTable(stdout, "global", globalPath, globalCfg)
	case hookListScopeProject:
		if projectCtx == "" {
			fmt.Fprintln(stdout, "no project context (run from inside a project tree or set PROJMUX_CWD)")
			return nil
		}
		return c.writeScopeTable(stdout, "project", projectPath, projectCfg)
	case hookListScopeEffective:
		return c.writeEffectiveTable(stdout, globalPath, projectPath, projectCtx, globalCfg, projectCfg)
	default:
		// Default view: render both global and project tables so the user
		// sees the full active set in one shot.
		if err := c.writeScopeTable(stdout, "global", globalPath, globalCfg); err != nil {
			return err
		}
		fmt.Fprintln(stdout)
		if projectCtx == "" {
			fmt.Fprintln(stdout, "project: no project context (run from inside a project tree or set PROJMUX_CWD)")
			return nil
		}
		return c.writeScopeTable(stdout, "project", projectPath, projectCfg)
	}
}

func (c *hookCommand) writeScopeTable(stdout io.Writer, scope, path string, cfg hooks.ProjectConfig) error {
	fmt.Fprintf(stdout, "%s config: %s\n", scope, path)
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

func (c *hookCommand) writeEffectiveTable(stdout io.Writer, globalPath, projectPath, projectCtx string, globalCfg, projectCfg hooks.ProjectConfig) error {
	fmt.Fprintf(stdout, "global config:  %s\n", globalPath)
	if projectCtx == "" {
		fmt.Fprintln(stdout, "project config: (no project context)")
	} else {
		fmt.Fprintf(stdout, "project config: %s\n", projectPath)
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

func (c *hookCommand) runEdit(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook edit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	global := fs.Bool("global", false, "edit the global config.toml entry")
	project := fs.Bool("project", false, "force a project-local override in .projmux/config.toml")
	useEditor := fs.Bool("editor", false, "open the config.toml file in $EDITOR instead of the inline prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *global && *project {
		printHookUsage(stderr)
		return usageError("hook edit: --global and --project are mutually exclusive")
	}
	if fs.NArg() != 1 {
		printHookUsage(stderr)
		return usageError("hook edit requires exactly one <event> argument")
	}
	event := strings.TrimSpace(fs.Arg(0))
	if !isSupportedHookEvent(event) {
		return usageError(fmt.Sprintf("unsupported hook event %q (supported: %s)", event, supportedHookEventList()))
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
			return fmt.Errorf("hook %q is defined at %s; edit that file directly or run 'projmux hook edit %s --project' to create a project override", event, sourcePath, event)
		}
	}
	path := filepath.Join(repo, ".projmux", "config.toml")
	if *useEditor {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create project config dir: %w", err)
		}
		return c.openInEditor(path, stdout, stderr)
	}
	return c.editProjectInline(repo, path, event, stdout, stderr)
}

func (c *hookCommand) effectiveHookSource(event string) (hooks.EffectiveSource, string, error) {
	globalPath, globalCfg, globalErr := c.loadGlobal()
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

func (c *hookCommand) editGlobalInline(path, event string, stdout, stderr io.Writer) error {
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

func (c *hookCommand) editProjectInline(repo, path, event string, stdout, stderr io.Writer) error {
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
func (c *hookCommand) readInlineLine(event, current string, stdout io.Writer) (string, bool, error) {
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

func (c *hookCommand) openInEditor(path string, stdout, stderr io.Writer) error {
	editor := strings.TrimSpace(c.lookupEnv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(c.lookupEnv("VISUAL"))
	}
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
		c.editorRunner = defaultEditorRunner
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

func (c *hookCommand) runValidate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hook validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printHookUsage(stderr)
		return usageError("hook validate does not accept positional arguments")
	}
	globalPath, globalCfg, globalErr := c.loadGlobal()
	projectPath, projectCfg, projectErr, projectCtx := c.loadProject()

	ok := true
	if globalErr != nil {
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

func (c *hookCommand) runTrust(args []string, stdout, stderr io.Writer) error {
	repo, err := c.resolveTrustTarget(args, stderr)
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
	_, err = fmt.Fprintf(stdout, "trusted %s\n  .projmux/config.toml sha256=%s\n", repo, sum)
	return err
}

func (c *hookCommand) runUntrust(args []string, stdout, stderr io.Writer) error {
	repo, err := c.resolveTrustTarget(args, stderr)
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
	return err
}

func (c *hookCommand) resolveTrustTarget(args []string, stderr io.Writer) (string, error) {
	switch len(args) {
	case 0:
		repo, err := c.resolveProjectContext()
		if err != nil {
			return "", err
		}
		if repo == "" {
			printHookUsage(stderr)
			return "", usageError("trust/untrust requires <project> or a project context")
		}
		return repo, nil
	case 1:
		raw := strings.TrimSpace(args[0])
		if raw == "" {
			printHookUsage(stderr)
			return "", usageError("trust/untrust <project> must not be empty")
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", raw, err)
		}
		return filepath.Clean(abs), nil
	default:
		printHookUsage(stderr)
		return "", usageError("trust/untrust takes at most one <project> argument")
	}
}

// --- shared helpers ------------------------------------------------------

func (c *hookCommand) globalConfigPath() (string, error) {
	return hooks.GlobalConfigPath(c.lookupEnv, c.homeDir)
}

func (c *hookCommand) trustStorePath() (string, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, "trusted-projects.json"), nil
}

func (c *hookCommand) loadGlobal() (string, hooks.ProjectConfig, error) {
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
func (c *hookCommand) loadProject() (string, hooks.ProjectConfig, error, string) {
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
// `os.Getwd()` and walk upward to the nearest `.projmux` or `.git` marker.
// The implicit walk stops before considering the system temp root itself so
// temp fixtures and other scratch parents do not become project contexts.
// Returning an empty string is not an error; downstream commands decide
// whether the context is required.
func (c *hookCommand) resolveProjectContext() (string, error) {
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

// nearestProjectMarker walks parent directories looking for a `.projmux` or
// `.git` marker. Boundary paths are not considered candidates. Returns "" when
// the walk reaches a boundary or the filesystem root with nothing found.
func nearestProjectMarker(path string, boundaries ...string) string {
	path = filepath.Clean(path)
	for {
		for _, boundary := range boundaries {
			boundary = filepath.Clean(strings.TrimSpace(boundary))
			if boundary != "" && boundary != "." && path == boundary {
				return ""
			}
		}
		if hookMarkerExists(filepath.Join(path, ".projmux")) || gitWorktreeMarkerExists(filepath.Join(path, ".git")) {
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

// gitWorktreeMarkerExists rejects placeholder .git paths while preserving both
// normal repositories (.git directory) and linked/separate worktrees (.git
// file containing "gitdir: ..."). A credible admin directory needs a valid
// HEAD plus the common repository's config, object store, and refs inventory;
// mere filesystem existence is not project identity.
func gitWorktreeMarkerExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return gitAdminDirCredible(path)
	}
	if !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		return false
	}
	line := strings.TrimSpace(string(data))
	gitDir, ok := strings.CutPrefix(line, "gitdir:")
	gitDir = strings.TrimSpace(gitDir)
	if !ok || gitDir == "" || strings.ContainsAny(gitDir, "\r\n") {
		return false
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(path), gitDir)
	}
	return gitAdminDirCredible(filepath.Clean(gitDir))
}

func gitAdminDirCredible(path string) bool {
	head, err := os.ReadFile(filepath.Join(path, "HEAD"))
	if err != nil || len(head) > 4096 || !validGitHEAD(strings.TrimSpace(string(head))) {
		return false
	}
	commonDir := path
	if data, err := os.ReadFile(filepath.Join(path, "commondir")); err == nil {
		if len(data) > 4096 {
			return false
		}
		common := strings.TrimSpace(string(data))
		if common == "" || strings.ContainsAny(common, "\r\n") {
			return false
		}
		if !filepath.IsAbs(common) {
			common = filepath.Join(path, common)
		}
		commonDir = filepath.Clean(common)
	}
	return regularFileExists(filepath.Join(commonDir, "config")) &&
		directoryExists(filepath.Join(commonDir, "objects")) &&
		(directoryExists(filepath.Join(commonDir, "refs")) || regularFileExists(filepath.Join(commonDir, "packed-refs")))
}

func validGitHEAD(value string) bool {
	if ref, ok := strings.CutPrefix(value, "ref: "); ok {
		return strings.HasPrefix(ref, "refs/") && !strings.ContainsAny(ref, " \t\r\n")
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isSupportedHookEvent(event string) bool {
	for _, e := range hooks.SupportedEvents {
		if string(e) == event {
			return true
		}
	}
	return false
}

func supportedHookEventList() string {
	names := make([]string, 0, len(hooks.SupportedEvents))
	for _, e := range hooks.SupportedEvents {
		names = append(names, string(e))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func printHookUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux hook list [--global|--project|--effective]")
	fmt.Fprintln(w, "  projmux hook edit <event> [--global|--project] [--editor]")
	fmt.Fprintln(w, "  projmux hook validate")
	fmt.Fprintln(w, "  projmux hook trust [<project>]")
	fmt.Fprintln(w, "  projmux hook untrust [<project>]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Events:")
	fmt.Fprintln(w, "  "+supportedHookEventList())
}
