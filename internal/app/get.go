package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// canonicalGetPane is the canonical route spelling whose output catalog owns
// the Pane-read `cwd` field projection.
const canonicalGetPane = "get pane"

// getKinds lists the kind spellings `get` implements, in help order, each
// canonical token followed by its accepted aliases.
//
// It is derived from the command manifest rather than restated here, so a kind
// the manifest accepts can never be missing from the refusal that lists them --
// which was exactly the failure mode when the four resource verbs each kept
// their own hand-written list and disagreed about singular and plural.
var getKinds = cli.ChildSpellings("get")

// getCommand implements the read-only `get` verb.
//
// Every path through this command is read-only: it loads the resource registry
// without creating it, reads the active tmux pane path with a display-message
// query, and writes to stdout only after a successful resolution. A selector or
// cardinality failure therefore leaves zero bytes on stdout and zero mutations.
//
// `notifications` and `snapshots` are parity aliases: they forward raw argv to
// the notify queue and session snapshot handlers so stdout, stderr, and the exit
// code stay identical to the current public spellings.
type getCommand struct {
	loadRegistry func() (coremetadata.Registry, error)
	// runtime is the live-tmux observation Window and Pane status is derived
	// from; see runtime_observation.go.
	runtime     runtimeLookup
	currentPath currentPathResolver
	notify      rawArgvCommand
	snapshots   rawArgvCommand
	// runtimeDiag is the Runtime diagnostics escape hatch's read seam. It is a
	// separate field from `runtime` above, which observes mirrored uids to
	// derive Registry row status; this one resolves the whole server, including
	// everything projmux does not own.
	runtimeDiag *runtimeDiagnosticsReader
	// activeTarget is the active-derived seam shared by the singular Pane
	// fallback and the plural Window/Pane/Agent Project default; see
	// active_target.go. It is deliberately unrelated to `--current`: see
	// runPane.
	activeTarget activeTargetLookup
	// now is the clock the plural read's AGE column is measured against.
	//
	// It exists so a golden can pin the column: a renderer that called time.Now
	// itself would produce output no test could freeze. A nil value is the real
	// clock, which keeps every existing construction of this struct -- including
	// the literals the tests build -- valid without a clock they do not care
	// about.
	now func() time.Time
}

// clock answers the invocation's current time, defaulting to the wall clock.
func (c *getCommand) clock() time.Time {
	if c == nil || c.now == nil {
		return time.Now()
	}
	return c.now()
}

func newGetCommand() *getCommand {
	return &getCommand{
		loadRegistry: loadResourceRegistry,
		runtime:      defaultRuntimeLookup(),
		// No lifecycle recorder: the registry-backed reads perform no lifecycle
		// operation, so they must not open an operations journal entry. The two
		// delegating kinds keep whatever their own handler already records.
		currentPath:  defaultTmuxClient(),
		activeTarget: defaultActiveTargetLookup(),
	}
}

// loadResourceRegistry reads the resource registry without creating any state.
func loadResourceRegistry() (coremetadata.Registry, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return coremetadata.Registry{}, fmt.Errorf("resolve projmux state paths: %w", err)
	}
	return intmetadata.NewDefaultStore(paths).LoadReadOnly()
}

// snapshotResourceRegistry is the stricter read required by a reconciliation
// preview: even an existing Registry is read without a lock file or permission
// repair, relying on the store's atomic-replace writer boundary.
func snapshotResourceRegistry() (coremetadata.Registry, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return coremetadata.Registry{}, fmt.Errorf("resolve projmux state paths: %w", err)
	}
	return intmetadata.NewDefaultStore(paths).LoadSnapshot()
}

// Run dispatches one `get <kind>` invocation.
func (c *getCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("get requires a resource kind: %s", strings.Join(getKinds, ", ")))
	}
	// The alias is normalized away before anything else looks at the token, so
	// the switch below, the flag-set name, the `-o` catalog key, and every
	// message built from the spelling all see the canonical kind. That is what
	// makes `get project` byte-identical to `get projects` instead of a parallel
	// route that has to be kept in step by hand.
	kind, ok := cli.CanonicalChildToken("get", args[0])
	if !ok {
		return usageError(fmt.Sprintf("get %s is not available; this release implements: %s",
			args[0], strings.Join(getKinds, ", ")))
	}
	switch kind {
	case "pane":
		return c.runPane(args[1:], stdout, stderr)
	case "projects", "windows", "panes", "agents":
		return c.runList(kind, args[1:], stdout, stderr)
	case "runtime":
		return c.runRuntime(args[1:], stdout, stderr)
	case "notifications":
		return forwardRawArgv(c.notify, "get notifications", "notify", []string{"list"}, args[1:], stdout, stderr)
	case "snapshots":
		return forwardRawArgv(c.snapshots, "get snapshots", "session-state", []string{"status"}, args[1:], stdout, stderr)
	default:
		return usageError(fmt.Sprintf("get %s is not available; this release implements: %s",
			args[0], strings.Join(getKinds, ", ")))
	}
}

// runList answers one plural read. The list cardinality is 0..N, so an empty
// result is a success with empty stdout rather than a not-resolved error.
func (c *getCommand) runList(token string, args []string, stdout, stderr io.Writer) error {
	kind := resourceListKindTokens[token]
	spelling := "get " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := resourceQueryFlags{kind: kind, runtime: c.runtime}
	flags.register(fs)
	if kind == coremetadata.KindWindow || kind == coremetadata.KindPane || kind == coremetadata.KindAgent {
		flags.active = c.activeTarget
		flags.defaultProjectScope = true
		fs.BoolVar(&flags.allProjects, "all-projects", false,
			"list resources across every Project instead of the active Project")
		fs.BoolVar(&flags.allProjects, "A", false,
			"list resources across every Project instead of the active Project (alias of --all-projects)")
	}
	flags.registerOutput(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("%s does not accept positional arguments; got %q", spelling, fs.Arg(0)))
	}
	if flags.allProjects && len(flags.projects) > 0 {
		return usageError(fmt.Sprintf("%s: --all-projects cannot be combined with --project", spelling))
	}

	mode, field, err := resolveOutputMode(spelling, flags.output)
	if err != nil {
		return err
	}
	if field != "" {
		return usageError(fmt.Sprintf("-o %s is not a %s projection", field, spelling))
	}

	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbGet, true, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	return writeResourceProjection(stdout, spelling, mode, kind, resolution.Matches, registry, true, c.clock())
}

// repeatedFlag collects every occurrence of a repeatable singular selector
// flag. Values are never split: the selector grammar forbids implicit comma
// splitting, so one occurrence is exactly one value.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, " ") }

func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func (c *getCommand) runPane(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("get pane", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var projects, windows, panes, labels repeatedFlag
	var current bool
	var output string
	fs.Var(&projects, "project", "exact-one Project selector: <name> or uid:<uid>")
	fs.Var(&projects, "p", "exact-one Project selector: <name> or uid:<uid> (alias of --project)")
	fs.Var(&windows, "window", "repeatable Window selector: <name> or uid:<uid>")
	fs.Var(&windows, "w", "repeatable Window selector: <name> or uid:<uid> (alias of --window)")
	fs.Var(&panes, "pane", "repeatable Pane selector: <name> or uid:<uid>")
	fs.Var(&labels, "selector", "repeatable label filter: key=value (AND)")
	fs.BoolVar(&current, "current", false, "read the active tmux pane instead of resolving a selector")
	fs.StringVar(&output, "output", "", "result projection")
	fs.StringVar(&output, "o", "", "result projection (alias of --output)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("get pane does not accept positional arguments; got %q", fs.Arg(0)))
	}

	mode := cli.OutputModeDefault
	var field cli.FieldProjection
	if output != "" {
		resolvedMode, resolvedField, err := cli.ResolveOutputToken(canonicalGetPane, output)
		if err != nil {
			return usageError(err.Error())
		}
		mode, field = resolvedMode, resolvedField
	}
	// `cwd` is a Pane-read route-local projection, not a shared output mode.
	// It is legal on exactly one scope: the current-Pane read.
	if field == cli.FieldProjectionCWD && !current {
		return usageError("-o cwd is only valid on the Pane current read; use `projmux get pane --current -o cwd`")
	}

	// `--current` and the empty-selector fallback below are two different
	// routes to two different answers, which is why both survive. `--current`
	// never reads the registry at all: it reports the live tmux
	// `#{pane_current_path}` of the focused pane as a bare scalar, and it
	// accepts no projection other than `cwd`. The fallback resolves a registry
	// Pane *resource* and renders the shared resource projection. Different
	// source, different output, different failure surface, so neither is a
	// duplicate of the other and nothing is deprecated here.
	if current {
		if len(projects) > 0 || len(windows) > 0 || len(panes) > 0 || len(labels) > 0 {
			return usageError("get pane --current reads the active tmux pane and does not accept selectors")
		}
		if field != cli.FieldProjectionCWD {
			return usageError("get pane --current supports -o cwd only; the live Pane resource projection arrives with runtime materialization")
		}
		return c.printCurrentCWD(stdout)
	}

	query, err := buildPaneQuery(projects, windows, panes, labels)
	if err != nil {
		return MapMetadataError(err)
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	// `get pane` builds its own flag set rather than sharing resourceQueryFlags,
	// so the fallback is applied here against the same emptiness rule: any
	// positional-free selector occurrence at all keeps the pre-fallback meaning.
	if len(projects) == 0 && len(windows) == 0 && len(panes) == 0 && len(labels) == 0 {
		ref, resolved, err := activeTargetRef(c.activeTarget, coremetadata.KindPane, registry)
		if err != nil {
			return MapMetadataError(err)
		}
		if resolved {
			query = withActiveTargetRef(query, coremetadata.KindPane, ref)
		}
	}
	resolution, err := selector.NewObserved(registry, c.runtime.observation()).ResolvePanes(query)
	if err != nil {
		return MapMetadataError(err)
	}
	target := selector.Target{Verb: selector.VerbGet, Kind: coremetadata.KindPane}
	if err := selector.Enforce(target, selector.DescribeSelector(query), resolution); err != nil {
		return MapMetadataError(err)
	}
	return writePaneProjection(stdout, mode, resolution.Matches[0], registry)
}

// printCurrentCWD writes the active tmux pane path as a scalar with a single
// trailing newline.
func (c *getCommand) printCurrentCWD(stdout io.Writer) error {
	if c.currentPath == nil {
		return errors.New("get pane --current: tmux client is not configured")
	}
	path, err := c.currentPath.CurrentPanePath(context.Background())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, strings.TrimSpace(path))
	return err
}

// buildPaneQuery parses raw selector occurrences into a resolver query.
func buildPaneQuery(projects, windows, panes, labels []string) (selector.Query, error) {
	var query selector.Query
	switch len(projects) {
	case 0:
	case 1:
		ref, err := selector.ParseRef(coremetadata.KindProject, projects[0])
		if err != nil {
			return selector.Query{}, err
		}
		query.Project = &ref
	default:
		return selector.Query{}, usageError("--project accepts at most one occurrence; repeat --window or --pane instead")
	}
	for _, raw := range windows {
		ref, err := selector.ParseRef(coremetadata.KindWindow, raw)
		if err != nil {
			return selector.Query{}, err
		}
		query.Windows = append(query.Windows, ref)
	}
	for _, raw := range panes {
		ref, err := selector.ParseRef(coremetadata.KindPane, raw)
		if err != nil {
			return selector.Query{}, err
		}
		query.Panes = append(query.Panes, ref)
	}
	for _, raw := range labels {
		label, err := selector.ParseLabel(raw)
		if err != nil {
			return selector.Query{}, err
		}
		query.Labels = append(query.Labels, label)
	}
	return query, nil
}

// writePaneProjection renders one resolved Pane.
//
// `get pane` is an exact-one read, so the structured modes emit a single JSON
// document rather than a list envelope; the list envelopes belong to the plural
// read spellings and the fan-out create routes.
func writePaneProjection(stdout io.Writer, mode cli.OutputMode, match selector.Match, registry coremetadata.Registry) error {
	// The singular read renders no elapsed time, so it passes no clock.
	return writeResourceProjection(stdout, canonicalGetPane, mode, coremetadata.KindPane,
		[]selector.Match{match}, registry, false, time.Time{})
}
