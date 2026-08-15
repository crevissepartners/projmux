package app

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// resourceStore is the shared registry seam of the canonical verb-to-kind
// routes. Reads go through LoadReadOnly so a route that resolves nothing never
// materializes <state>/projmux/metadata/; writes go through the store's locked
// read -> mutate -> validate -> atomic replace transaction, so a mutation that
// fails validation leaves the file byte-identical.
type resourceStore struct {
	load    func() (coremetadata.Registry, error)
	update  func(func(*coremetadata.Registry) error) (coremetadata.Registry, error)
	mutator func() coremetadata.Mutator
}

func newResourceStore() *resourceStore {
	return &resourceStore{
		load:    loadResourceRegistry,
		update:  updateResourceRegistry,
		mutator: intmetadata.DefaultMutator,
	}
}

// updateResourceRegistry applies one transaction to the resource registry.
func updateResourceRegistry(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return coremetadata.Registry{}, fmt.Errorf("resolve projmux state paths: %w", err)
	}
	return intmetadata.NewDefaultStore(paths).Update(fn)
}

// mutate applies one registry transaction over an already-preflighted uid set.
//
// The preflight resolves selectors against a read-only snapshot, so a selector
// or cardinality failure never opens the store and never creates
// <state>/projmux/metadata/. Execution then re-checks every approved uid inside
// the lock: a resource that disappeared between the two reads aborts the whole
// transaction, and an aborted transaction performs no write at all.
func (s *resourceStore) mutate(kind coremetadata.Kind, uids []string, apply func(*coremetadata.Registry, coremetadata.Mutator) error) error {
	if s == nil || s.update == nil {
		return errors.New("resource registry store is not configured")
	}
	_, err := s.update(func(working *coremetadata.Registry) error {
		for _, uid := range uids {
			if _, _, ok := resourceFor(*working, kind, uid); !ok {
				return fmt.Errorf("%s %q disappeared before the mutation ran", strings.ToLower(string(kind)), uid)
			}
		}
		return apply(working, s.mutator())
	})
	return MapMetadataError(err)
}

// resourceKindTokens maps the public kind token of a verb-to-kind route onto the
// resource kind it addresses. Singular tokens address one resource; the plural
// tokens are the list spellings of the read verbs.
var resourceKindTokens = map[string]coremetadata.Kind{
	"project": coremetadata.KindProject,
	"window":  coremetadata.KindWindow,
	"pane":    coremetadata.KindPane,
	"agent":   coremetadata.KindAgent,
}

var resourceListKindTokens = map[string]coremetadata.Kind{
	"projects": coremetadata.KindProject,
	"windows":  coremetadata.KindWindow,
	"panes":    coremetadata.KindPane,
	"agents":   coremetadata.KindAgent,
}

// resourceQueryFlags is the shared selector surface of the canonical routes.
//
// The scope flags a route registers follow its target kind: a Window route is
// scoped by --project, a Pane route by --project and --window. There is
// deliberately no --agent flag: the selector grammar fixes the repeatable scope
// flags at --project/--window/--pane, so an Agent ref only ever arrives as the
// positional resource reference.
type resourceQueryFlags struct {
	kind     coremetadata.Kind
	projects repeatedFlag
	windows  repeatedFlag
	panes    repeatedFlag
	agents   []string
	labels   repeatedFlag
	output   string
}

func (f *resourceQueryFlags) register(fs *flag.FlagSet) {
	fs.Var(&f.projects, "project", "exact-one Project selector: <name> or uid:<uid>")
	switch f.kind {
	case coremetadata.KindWindow, coremetadata.KindPane, coremetadata.KindAgent:
		fs.Var(&f.windows, "window", "repeatable Window selector: <name> or uid:<uid>")
	}
	if f.kind == coremetadata.KindPane {
		fs.Var(&f.panes, "pane", "repeatable Pane selector: <name> or uid:<uid>")
	}
	fs.Var(&f.labels, "selector", "repeatable label filter: key=value (AND)")
}

// registerOutput adds the shared `-o` flag. Only the read verbs register it:
// the canonical manifest pins an output catalog for the result-producing routes
// and deliberately leaves the mutation routes without one, so offering `-o`
// there would advertise a projection the contract does not define.
func (f *resourceQueryFlags) registerOutput(fs *flag.FlagSet) {
	fs.StringVar(&f.output, "output", "", "result projection")
	fs.StringVar(&f.output, "o", "", "result projection (alias of --output)")
}

// addPositionalRef folds the positional `<ref>` of a singular route into the
// selector occurrence list of its own kind, so one resolution pipeline answers
// both spellings.
func (f *resourceQueryFlags) addPositionalRef(ref string) {
	switch f.kind {
	case coremetadata.KindProject:
		f.projects = append(f.projects, ref)
	case coremetadata.KindWindow:
		f.windows = append(f.windows, ref)
	case coremetadata.KindPane:
		f.panes = append(f.panes, ref)
	case coremetadata.KindAgent:
		f.agents = append(f.agents, ref)
	}
}

// parseWithPositionals parses argv while allowing flags and positional resource
// references to interleave.
//
// The standard flag package stops at the first non-flag token, which would make
// `describe pane log --project alpha` a parse error even though it is the exact
// shape the grammar documents. Parsing in rounds, taking one positional between
// them, keeps flag semantics unchanged while accepting either order.
func parseWithPositionals(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}

// query parses the collected occurrences into a resolver query.
func (f *resourceQueryFlags) query() (selector.Query, error) {
	query, err := buildPaneQuery(f.projects, f.windows, f.panes, f.labels)
	if err != nil {
		return selector.Query{}, err
	}
	for _, raw := range f.agents {
		ref, err := selector.ParseRef(coremetadata.KindAgent, raw)
		if err != nil {
			return selector.Query{}, err
		}
		query.Agents = append(query.Agents, ref)
	}
	return query, nil
}

// targetRefs returns the occurrences that address the target kind itself, as
// opposed to the enclosing --project/--window scope.
func (f *resourceQueryFlags) targetRefs() []string {
	switch f.kind {
	case coremetadata.KindProject:
		return f.projects
	case coremetadata.KindWindow:
		return f.windows
	case coremetadata.KindPane:
		return f.panes
	case coremetadata.KindAgent:
		return f.agents
	default:
		return nil
	}
}

// withTargetRefs returns a copy of the query narrowed to one target occurrence,
// keeping the enclosing scope intact.
func (f *resourceQueryFlags) withTargetRefs(refs []string) resourceQueryFlags {
	probe := *f
	switch f.kind {
	case coremetadata.KindProject:
		probe.projects = refs
	case coremetadata.KindWindow:
		probe.windows = refs
	case coremetadata.KindPane:
		probe.panes = refs
	case coremetadata.KindAgent:
		probe.agents = refs
	}
	return probe
}

// unmatchedTargetRefs returns the explicit target occurrences that addressed no
// resource at all.
//
// A 1..N route is satisfied by any single match, so a fan-out over several
// explicit references would otherwise silently drop the ones that resolve to
// nothing. For a destructive fan-out that is exactly the partial outcome the
// preflight contract forbids, so the caller turns a non-empty result into a
// usage error before anything is removed.
func (f *resourceQueryFlags) unmatchedTargetRefs(registry coremetadata.Registry) ([]string, error) {
	var unmatched []string
	for _, raw := range f.targetRefs() {
		probe := f.withTargetRefs([]string{raw})
		query, err := probe.query()
		if err != nil {
			return nil, err
		}
		resolution, err := probe.resolveQuery(registry, query)
		if err != nil {
			return nil, err
		}
		if len(resolution.Matches) == 0 {
			unmatched = append(unmatched, raw)
		}
	}
	return unmatched, nil
}

// resolveQuery runs the kind's resolution pipeline without enforcing a
// cardinality.
func (f *resourceQueryFlags) resolveQuery(registry coremetadata.Registry, query selector.Query) (selector.Resolution, error) {
	resolver := selector.New(registry)
	switch f.kind {
	case coremetadata.KindProject:
		return resolver.ResolveProjects(query)
	case coremetadata.KindWindow:
		return resolver.ResolveWindows(query)
	case coremetadata.KindPane:
		return resolver.ResolvePanes(query)
	case coremetadata.KindAgent:
		return resolver.ResolveAgents(query)
	default:
		return selector.Resolution{}, fmt.Errorf("no resolution pipeline for kind %q", f.kind)
	}
}

// resolve runs the kind's resolution pipeline and enforces the declared
// <verb, kind> cardinality. Nothing is written before Enforce returns, so a
// cardinality failure leaves zero bytes on stdout and zero mutations.
func (f *resourceQueryFlags) resolve(verb selector.Verb, list bool, registry coremetadata.Registry) (selector.Resolution, error) {
	query, err := f.query()
	if err != nil {
		return selector.Resolution{}, err
	}
	resolution, err := f.resolveQuery(registry, query)
	if err != nil {
		return selector.Resolution{}, err
	}
	target := selector.Target{Verb: verb, Kind: f.kind, List: list}
	if err := selector.Enforce(target, selector.DescribeSelector(query), resolution); err != nil {
		return selector.Resolution{}, err
	}
	return resolution, nil
}

// resolveOutputMode maps the raw `-o` token onto the projection the canonical
// route accepts. An absent token is the implicit human summary.
func resolveOutputMode(spelling, token string) (cli.OutputMode, cli.FieldProjection, error) {
	if token == "" {
		return cli.OutputModeDefault, "", nil
	}
	mode, field, err := cli.ResolveOutputToken(spelling, token)
	if err != nil {
		return "", "", usageError(err.Error())
	}
	return mode, field, nil
}

// resourceListKind renders the JSON envelope kind of a fan-out result.
func resourceListKind(kind coremetadata.Kind, metadataOnly bool) string {
	if metadataOnly {
		return string(kind) + "MetadataList"
	}
	return string(kind) + "List"
}

// resourceList is the structured fan-out envelope. Items is never null so a
// consumer can iterate an empty list without a nil check.
type resourceList struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Items      []any  `json:"items"`
}

// resourceFor returns the stored resource and its metadata block for one uid.
func resourceFor(registry coremetadata.Registry, kind coremetadata.Kind, uid string) (any, coremetadata.ObjectMeta, bool) {
	switch kind {
	case coremetadata.KindProject:
		if project, ok := registry.Project(uid); ok {
			return *project, project.Metadata, true
		}
	case coremetadata.KindWindow:
		if window, ok := registry.Window(uid); ok {
			return *window, window.Metadata, true
		}
	case coremetadata.KindPane:
		if pane, ok := registry.Pane(uid); ok {
			return *pane, pane.Metadata, true
		}
	case coremetadata.KindAgent:
		if agent, ok := registry.Agent(uid); ok {
			return *agent, agent.Metadata, true
		}
	}
	return nil, coremetadata.ObjectMeta{}, false
}

// resourceRef renders the `kind/<metadata.name>` reference of one match.
func resourceRef(match selector.Match) string {
	return strings.ToLower(string(match.Kind)) + "/" + match.Name
}

// resourceSummary is the default human projection of one match.
//
// The registry is consulted only for the fields no selector.Match carries. An
// Agent gains a `session=<provider>:<conversation-id>` suffix when it has a
// stored provider session ref, which is what makes the durable conversation
// pointer visible from the plural read rather than only from `describe` or the
// structured `-o json` shape. An Agent that has never had a hook report a
// conversation renders exactly as before.
func resourceSummary(match selector.Match, kind coremetadata.Kind, registry coremetadata.Registry) string {
	summary := resourceRef(match) + " status=" + string(match.Status)
	if owner := match.Owner.String(); owner != "" {
		summary += " owner=" + owner
	}
	if kind != coremetadata.KindAgent {
		return summary
	}
	agent, ok := registry.Agent(match.UID)
	if !ok || agent.Status.SessionRef.Empty() {
		return summary
	}
	return summary + " session=" + agent.Status.SessionRef.Summary()
}

// writeResourceProjection renders a resolution through the shared output
// catalog.
//
// A singular route emits one document; a list route emits one line per match in
// resolution order for the scalar modes and a single List envelope for the
// structured modes, which is the fan-out contract for read results.
func writeResourceProjection(
	stdout io.Writer,
	spelling string,
	mode cli.OutputMode,
	kind coremetadata.Kind,
	matches []selector.Match,
	registry coremetadata.Registry,
	list bool,
) error {
	switch mode {
	case cli.OutputModeNone:
		return nil
	case cli.OutputModeUID, cli.OutputModeName, cli.OutputModeRef, cli.OutputModeDefault:
		for _, match := range matches {
			var line string
			switch mode {
			case cli.OutputModeUID:
				line = match.UID
			case cli.OutputModeName:
				line = match.Name
			case cli.OutputModeRef:
				line = resourceRef(match)
			default:
				line = resourceSummary(match, kind, registry)
			}
			if _, err := fmt.Fprintln(stdout, line); err != nil {
				return err
			}
		}
		return nil
	case cli.OutputModeMetadata, cli.OutputModeJSON:
		metadataOnly := mode == cli.OutputModeMetadata
		items := make([]any, 0, len(matches))
		for _, match := range matches {
			resource, meta, ok := resourceFor(registry, kind, match.UID)
			if !ok {
				return fmt.Errorf("%s: resolved uid %q is no longer in the registry", spelling, match.UID)
			}
			if metadataOnly {
				items = append(items, meta)
			} else {
				items = append(items, resource)
			}
		}
		if !list {
			return writeJSON(stdout, items[0])
		}
		return writeJSON(stdout, resourceList{
			APIVersion: coremetadata.APIVersion,
			Kind:       resourceListKind(kind, metadataOnly),
			Items:      items,
		})
	case cli.OutputModePaneID:
		// A raw `%N` handle is a live transport binding, not stored metadata. It
		// becomes available when the runtime materialization Phase wires the tmux
		// mirror into the read path.
		return fmt.Errorf("%s -o pane-id needs a live transport binding, which is not wired yet", spelling)
	default:
		return fmt.Errorf("%s: unsupported output mode %q", spelling, mode)
	}
}

// confirmer answers the interactive confirmation of a destructive route.
type confirmer struct {
	// interactive reports whether stdin is a terminal. A non-interactive caller
	// never sees a prompt; it must pass --yes instead.
	interactive func() bool
	// ask reads one confirmation answer.
	ask func(prompt string, stdout io.Writer) (bool, error)
}

func newConfirmer() *confirmer {
	return &confirmer{interactive: stdinIsTerminal, ask: askOnStdin}
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func askOnStdin(prompt string, stdout io.Writer) (bool, error) {
	if _, err := fmt.Fprintf(stdout, "%s [y/N]: ", prompt); err != nil {
		return false, err
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// confirm gates one destructive execution.
//
// The contract is deliberately asymmetric: on a TTY the operator is asked, and
// without a TTY the absence of --yes is a usage error (exit 2) rather than a
// silent proceed. Either way the answer is resolved before any mutation runs.
func (c *confirmer) confirm(yes bool, prompt, refusal string, stdout io.Writer) error {
	if yes {
		return nil
	}
	if c == nil || c.interactive == nil || !c.interactive() {
		return usageError(refusal)
	}
	ok, err := c.ask(prompt, stdout)
	if err != nil {
		return err
	}
	if !ok {
		return usageError("aborted: confirmation declined")
	}
	return nil
}
