package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// canonicalGetPane is the canonical route spelling whose output catalog owns
// the Pane-read `cwd` field projection.
const canonicalGetPane = "get pane"

// getPaneKinds lists the resource kinds `get` implements today. The remaining
// kinds of the verb-to-kind family arrive with the public route relocation
// Phase; naming them here would promise a route that does not exist.
var getKinds = []string{"pane"}

// getCommand implements the read-only `get` verb.
//
// Every path through this command is read-only: it loads the resource registry
// without creating it, reads the active tmux pane path with a display-message
// query, and writes to stdout only after a successful resolution. A selector or
// cardinality failure therefore leaves zero bytes on stdout and zero mutations.
type getCommand struct {
	loadRegistry func() (coremetadata.Registry, error)
	currentPath  currentPathResolver
}

func newGetCommand() *getCommand {
	return &getCommand{
		loadRegistry: loadResourceRegistry,
		// No lifecycle recorder: this route performs no lifecycle operation, so
		// it must not open an operations journal entry.
		currentPath: defaultTmuxClient(),
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

// Run dispatches one `get <kind>` invocation.
func (c *getCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("get requires a resource kind: %s", strings.Join(getKinds, ", ")))
	}
	switch args[0] {
	case "pane":
		return c.runPane(args[1:], stdout, stderr)
	default:
		return usageError(fmt.Sprintf("get %s is not available; this release implements: %s",
			args[0], strings.Join(getKinds, ", ")))
	}
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
	fs.Var(&windows, "window", "repeatable Window selector: <name> or uid:<uid>")
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
	resolution, err := selector.New(registry).ResolvePanes(query)
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
// document rather than a list envelope; the list envelopes belong to the
// fan-out create routes.
func writePaneProjection(stdout io.Writer, mode cli.OutputMode, match selector.Match, registry coremetadata.Registry) error {
	switch mode {
	case cli.OutputModeNone:
		return nil
	case cli.OutputModeUID:
		_, err := fmt.Fprintln(stdout, match.UID)
		return err
	case cli.OutputModeName:
		_, err := fmt.Fprintln(stdout, match.Name)
		return err
	case cli.OutputModeRef:
		_, err := fmt.Fprintln(stdout, "pane/"+match.Name)
		return err
	case cli.OutputModeMetadata:
		pane, ok := registry.Pane(match.UID)
		if !ok {
			return fmt.Errorf("get pane: resolved uid %q is no longer in the registry", match.UID)
		}
		return writeJSON(stdout, pane.Metadata)
	case cli.OutputModeJSON:
		pane, ok := registry.Pane(match.UID)
		if !ok {
			return fmt.Errorf("get pane: resolved uid %q is no longer in the registry", match.UID)
		}
		return writeJSON(stdout, pane)
	case cli.OutputModePaneID:
		// A raw `%N` handle is a live transport binding, not stored metadata.
		// It becomes available when the runtime materialization Phase wires the
		// tmux mirror into the read path.
		return errors.New("get pane -o pane-id needs a live transport binding, which is not wired yet")
	default:
		summary := "pane/" + match.Name + " status=" + string(match.Status)
		if owner := match.Owner.String(); owner != "" {
			summary += " owner=" + owner
		}
		_, err := fmt.Fprintln(stdout, summary)
		return err
	}
}
