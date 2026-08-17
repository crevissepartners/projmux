package app

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/i18n"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// resourceStore is the shared registry seam of the canonical verb-to-kind
// routes. Reads go through LoadReadOnly so a route that resolves nothing never
// materializes <state>/projmux/metadata/; writes go through the store's locked
// read -> mutate -> validate -> atomic replace transaction, so a mutation that
// fails validation leaves the file byte-identical.
type resourceStore struct {
	load             func() (coremetadata.Registry, error)
	update           func(func(*coremetadata.Registry) error) (coremetadata.Registry, error)
	updateConvergent func(func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error)
	mutator          func() coremetadata.Mutator
}

func newResourceStore() *resourceStore {
	return &resourceStore{
		load:    loadResourceRegistry,
		update:  updateResourceRegistry,
		mutator: intmetadata.DefaultMutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				return coremetadata.Registry{}, false, fmt.Errorf("resolve projmux state paths: %w", err)
			}
			return intmetadata.NewDefaultStore(paths).UpdateConvergent(fn)
		},
	}
}

// converge runs the registry half of runtime binding convergence.
//
// Some existing mutators conservatively refresh Registry.UpdatedAt even when
// the projected value is already equal. That timestamp is useful for a real
// mutation but would turn a repeat reconciliation into a disk write. If it is
// the only difference, preserve the previous timestamp before the store's
// convergent equality check.
func (s *resourceStore) converge(apply func(*coremetadata.Registry, coremetadata.Mutator) error) (bool, error) {
	if s == nil || s.updateConvergent == nil || s.mutator == nil {
		return false, errors.New("resource registry convergence store is not configured")
	}
	_, changed, err := s.updateConvergent(func(working *coremetadata.Registry) error {
		before := working.Clone()
		if err := apply(working, s.mutator()); err != nil {
			return err
		}
		updatedAt := working.UpdatedAt
		working.UpdatedAt = before.UpdatedAt
		if !reflect.DeepEqual(working.Normalize(), before.Normalize()) {
			working.UpdatedAt = updatedAt
		}
		return nil
	})
	return changed, MapMetadataError(err)
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
	// runtime observes live tmux once per invocation so Window and Pane status
	// is derived rather than inherited from a stored bool; see
	// runtime_observation.go.
	//
	// The three routes that render selector.Match.Status set it: `get`,
	// `describe`, and `rename`. It is nil on the routes that resolve identity
	// and print their own result -- delete, rebind, and the Agent routes -- and
	// a nil lookup is the empty observation, so opting in is a per-route
	// decision made where the flags are built. A route that starts rendering
	// Status must set it; the alternative it replaced, inheriting the owning
	// Project's stored liveness, no longer exists anywhere.
	runtime runtimeLookup
	// active observes the tmux target this invocation runs in, for the
	// empty-selector fallback described in active_target.go.
	//
	// It is nil on every route that has not adopted the fallback, and a nil
	// lookup is exactly the pre-fallback behavior, so opting in is a per-route
	// decision made where the flags are built rather than a property of this
	// struct.
	active activeTargetLookup
	// wholeSetFlag is the explicit spelling that opts a route back into the
	// whole-registry fan-out an empty selector used to mean.
	//
	// A non-empty value contains the empty selector: when the active-target
	// fallback resolves nothing -- outside tmux above all -- the invocation is
	// refused instead of quietly addressing every resource of the kind. That is
	// the whole point on a destructive verb, where the historical meaning of
	// `projmux delete pane` was "delete every Pane in the registry".
	//
	// The value is the flag name the refusal tells the operator to pass, so the
	// remedy can never name a flag the route does not register. It stays empty
	// on every route that keeps the historical meaning, and the flag itself
	// clears both this field and `active`, which is what makes the explicit
	// spelling restore the pre-containment code path exactly rather than
	// approximate it.
	wholeSetFlag string
	// defaultProjectScope opts a plural registry read into the active-derived
	// Project default. The active resolver supplies only the Project uid; the
	// selector package's windowScope remains the single place that chooses
	// explicit Project, derived default, or whole-registry scope.
	defaultProjectScope bool
	// allProjects is the explicit escape hatch from defaultProjectScope. It is
	// registered only by get windows|panes|agents.
	allProjects bool
	// scopes records the scope-flag spellings register actually installed, which
	// differ per kind: a Window route has no --pane, an Agent route has neither
	// --pane nor an --agent. Refusal text is built from this rather than from a
	// second copy of register's switch, so a message can never advise a flag the
	// route does not accept.
	scopes []string
}

// selectorIsEmpty reports whether the invocation carried no selector at all:
// no positional reference, no --project/--window/--pane occurrence, and no
// --selector label.
//
// This is the only condition under which the active-target fallback fires. A
// partially specified selector keeps its exact pre-fallback meaning, because
// blending an implicit target into an explicit scope would make the same argv
// mean different things in and out of tmux.
func (f *resourceQueryFlags) selectorIsEmpty() bool {
	return len(f.projects) == 0 && len(f.windows) == 0 && len(f.panes) == 0 &&
		len(f.agents) == 0 && len(f.labels) == 0
}

func (f *resourceQueryFlags) register(fs *flag.FlagSet) {
	scope := func(name string, value *repeatedFlag, usage string) {
		fs.Var(value, name, usage)
		f.scopes = append(f.scopes, "--"+name)
	}
	scope("project", &f.projects, "exact-one Project selector: <name> or uid:<uid>")
	switch f.kind {
	case coremetadata.KindWindow, coremetadata.KindPane, coremetadata.KindAgent:
		scope("window", &f.windows, "repeatable Window selector: <name> or uid:<uid>")
	}
	if f.kind == coremetadata.KindPane {
		scope("pane", &f.panes, "repeatable Pane selector: <name> or uid:<uid>")
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
	resolver := selector.NewObserved(registry, f.runtime.observation())
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
//
// A completely empty selector on a singular route resolves the active tmux
// target first. The declared cardinality is untouched by that: the fallback
// contributes exactly one uid occurrence and the same Enforce call still decides
// whether the cell is satisfied. A plural read never receives an implicit
// target occurrence; its optional defaultProjectScope narrows only the enclosing
// Project universe and keeps the 0..N inventory meaning inside that scope.
//
// A route that also set wholeSetFlag refuses the empty selector outright when
// the fallback resolved nothing. On a 1..N cell that is the only thing standing
// between "no selector" and the whole registry: Enforce is satisfied by any
// match, so an empty query would sail through it with every resource of the kind
// in hand. An exact-one route needs no such guard, because its own cell already
// rejects the wide set.
func (f *resourceQueryFlags) resolve(verb selector.Verb, list bool, registry coremetadata.Registry) (selector.Resolution, error) {
	query, err := f.query()
	if err != nil {
		return selector.Resolution{}, err
	}
	if f.defaultProjectScope {
		if !f.allProjects && query.Project == nil {
			ref, resolved, err := activeTargetRef(f.active, coremetadata.KindProject, registry)
			if err != nil {
				return selector.Resolution{}, err
			}
			if resolved {
				query.DefaultProject = &ref
			}
		}
	}
	if !list && f.selectorIsEmpty() {
		ref, resolved, err := activeTargetRef(f.active, f.kind, registry)
		if err != nil {
			return selector.Resolution{}, err
		}
		switch {
		case resolved:
			query = withActiveTargetRef(query, f.kind, ref)
		case f.wholeSetFlag != "":
			return selector.Resolution{}, f.emptySelectorRefusal()
		}
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

// emptySelectorRefusal is the containment refusal of a route that declared a
// wholeSetFlag.
//
// It is deliberately not the ordinary cardinality failure and not the
// active-target refusal either. There is no cardinality violation to report --
// the empty query matches plenty -- and nothing was inspected on a tmux target,
// so the seam's "the active tmux pane %s carries no ..." wording would name a
// pane that was never read. What actually happened is that the operator asked
// for an unbounded destructive fan-out by omission, and the route requires that
// to be asked for on purpose.
//
// It is a *selector.SelectorError with no Want and no Candidates, so it exits 2
// with zero bytes on stdout like every other selector refusal while staying
// distinguishable from a no-match: SelectorError.IsNoMatch is false, so it does
// not unwrap to metadata.ErrNotFound. Listing candidates here would be actively
// wrong -- they are not ambiguity, they are the blast radius.
func (f *resourceQueryFlags) emptySelectorRefusal() error {
	subject := strings.ToLower(string(f.kind))
	return &selector.SelectorError{
		Op: "resolve " + subject,
		Detail: "no selector was given and no active tmux target resolved, so nothing was selected; " +
			"pass an explicit resource reference, a " + strings.Join(f.scopes, "/") + " scope, --selector, or " +
			f.wholeSetFlag + " to address every " + subject + " in the registry",
	}
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

// resourceTableColumns is the canonical column contract of the columnar plural
// read, keyed by kind and ordered exactly as the columns are printed.
//
// The header set is fixed per kind rather than derived from the rows, so a
// column never disappears because every row happened to leave it empty.
// DISPLAY NAME is the stored, duplicate-allowed `metadata.DisplayName` with a
// read-only fallback to `metadata.Name`; NAME is always that stable name without
// the `kind/` prefix the one-line summary carries. The spelling the operator
// typed already states the kind, exactly as it does for `kubectl get pods`.
//
// AGE is last on every kind, which is where `kubectl get` puts it and the only
// position that costs the columns before it nothing: it is the one column whose
// width changes as time passes, so growing it can never walk an owner-chain
// column sideways. Every kind carries it because every kind stores the field it
// is derived from -- `metadata.createdAt` lives on ObjectMeta, not on any one
// kind's spec -- and a column present on three kinds out of four would make
// "this kind has no age" look like a property of the resource rather than of
// the table.
var resourceTableColumns = map[coremetadata.Kind][]string{
	coremetadata.KindProject: {"DISPLAY NAME", "NAME", "STATUS", "AGE"},
	coremetadata.KindWindow:  {"DISPLAY NAME", "NAME", "STATUS", "PROJECT", "AGE"},
	coremetadata.KindPane:    {"DISPLAY NAME", "NAME", "STATUS", "PROJECT", "WINDOW", "AGENT", "AGE"},
	coremetadata.KindAgent:   {"DISPLAY NAME", "NAME", "STATUS", "PROJECT", "WINDOW", "SESSION", "AGE"},
}

// resourceTableGap is the minimum run of spaces between two columns. It is the
// only separator: the columnar projection draws no rules, no borders, and no
// box characters, so every byte between two cells is a space.
const resourceTableGap = 2

// resourceTableRow projects one match onto its kind's columns.
//
// It carries exactly what the one-line summary carried, split apart: the status
// of `status=`, and the owner chain of `owner=project/X window/Y` as one column
// per leg. A Pane's chain has a third leg -- a managed Pane is owned by its
// Agent, so the summary rendered `owner=project/X window/Y agent/Z` -- and the
// AGENT column is that leg. A shell Pane owns no Agent and leaves the cell
// empty, exactly as an Agent with no conversation leaves SESSION empty. The
// registry is consulted for the one field no selector.Match holds, the Agent's
// provider session ref, which is the same lookup and the same
// `<provider>:<conversation-id>` rendering the summary appended as `session=`.
//
// The AGE cell is the one column that is not carried by the pre-columnar
// one-liner. It is still not new data: it is `metadata.createdAt`, which the
// registry has always stored and `-o json` has always emitted, measured against
// the clock this invocation was handed.
func resourceTableRow(match selector.Match, kind coremetadata.Kind, registry coremetadata.Registry, now time.Time) []string {
	displayName, stableName := resourceTableNames(match, kind, registry)
	row := []string{displayName, stableName, string(match.Status)}
	age := resourceAgeCell(registry, kind, match.UID, now)
	switch kind {
	case coremetadata.KindWindow:
		return append(row, match.Owner.Project, age)
	case coremetadata.KindPane:
		return append(row, match.Owner.Project, match.Owner.Window, match.Owner.Agent, age)
	case coremetadata.KindAgent:
		return append(row, match.Owner.Project, match.Owner.Window, resourceSessionCell(match, registry), age)
	default:
		return append(row, age)
	}
}

// resourceTableNames selects the first two human table cells without changing
// any stored metadata or identity semantics. Whitespace-only display names are
// absent; a non-empty value is returned byte-for-byte, including its original
// surrounding whitespace. A missing registry row is tolerated with the same
// resolved stable name the pre-display table used.
func resourceTableNames(match selector.Match, kind coremetadata.Kind, registry coremetadata.Registry) (string, string) {
	_, meta, ok := resourceFor(registry, kind, match.UID)
	if !ok {
		return match.Name, match.Name
	}
	if strings.TrimSpace(meta.DisplayName) == "" {
		return meta.Name, meta.Name
	}
	return meta.DisplayName, meta.Name
}

// resourceAgeCell renders the AGE column of one row: how long ago the resource
// was created, measured from its stored `metadata.createdAt`.
//
// Nothing here collects a timestamp. The value is read out of the registry the
// route already loaded -- no tmux query, no new field, no write -- so the column
// costs the invocation nothing beyond the map lookup resourceFor already does
// for the structured output modes.
//
// The arithmetic is i18n.FormatDuration's compact form (`3d`, `5h`, `12m`,
// `36s`), which is the same single implementation `notify list` renders its own
// AGE column through, so relative time is computed in exactly one place in the
// binary. It is deliberately measured against a passed-in clock rather than
// time.Now: a renderer that reads the wall clock cannot be pinned by a golden.
//
// Three cases render an empty cell rather than a number:
//
//   - a match whose uid has left the registry, which is the same tolerance the
//     other registry-derived cell (SESSION) already has;
//   - a resource stored before `createdAt` was stamped, whose zero Time would
//     otherwise render as an age counted from year 1;
//   - a caller that passed no clock at all, which is what the singular
//     projections do -- they render no age, so their zero Time can never be
//     mistaken for "created exactly now".
//
// It is deliberately not localized. The headers beside it are fixed English
// tokens, and a cell whose width depended on the operator's locale would make
// the measured column width -- and therefore every golden -- environmental.
func resourceAgeCell(registry coremetadata.Registry, kind coremetadata.Kind, uid string, now time.Time) string {
	if now.IsZero() {
		return ""
	}
	_, meta, ok := resourceFor(registry, kind, uid)
	if !ok || meta.CreatedAt.IsZero() {
		return ""
	}
	return i18n.FormatDuration(now.Sub(meta.CreatedAt), i18n.FallbackLocale, i18n.FormatCompact)
}

// resourceSessionCell renders the SESSION column of one Agent. An Agent that has
// never had a hook report a conversation leaves the cell empty.
func resourceSessionCell(match selector.Match, registry coremetadata.Registry) string {
	agent, ok := registry.Agent(match.UID)
	if !ok || agent.Status.SessionRef.Empty() {
		return ""
	}
	return agent.Status.SessionRef.Summary()
}

// resourceCellWidth is the display width of one cell, in terminal cells.
//
// It is deliberately not a rune count and deliberately not text/tabwriter: a
// Hangul resource name is one rune per two columns, so rune-count padding walks
// every following column left by one position per syllable. VisibleLen is the
// repo's existing East-Asian-aware measurement, already used by the native
// picker for exactly this reason.
func resourceCellWidth(cell string) int {
	return projmuxpicker.VisibleLen(cell)
}

// writeResourceTable renders the columnar default projection of a plural read:
// one uppercase header line followed by one line per match, columns separated
// by padding spaces.
//
// Zero matches emit zero bytes -- no header and no message. The plural read's
// 0..N cardinality already makes an empty result a success with empty stdout
// (see getCommand.runList), the render seam has no stderr to write a kubectl
// style `No resources found` note to, and a header row over no rows would
// announce a table that has nothing in it. Keeping the empty case byte-identical
// to the pre-columnar behavior also keeps its exit code trivially unchanged.
//
// now is the invocation's clock, and it is the only thing the AGE column is
// measured against; see resourceAgeCell for what a zero value renders.
func writeResourceTable(stdout io.Writer, spelling string, kind coremetadata.Kind, matches []selector.Match, registry coremetadata.Registry, now time.Time) error {
	headers, ok := resourceTableColumns[kind]
	if !ok {
		return fmt.Errorf("%s: no column contract is declared for kind %q", spelling, kind)
	}
	if len(matches) == 0 {
		return nil
	}

	rows := make([][]string, 0, len(matches)+1)
	rows = append(rows, headers)
	for _, match := range matches {
		rows = append(rows, resourceTableRow(match, kind, registry, now))
	}

	widths := make([]int, len(headers))
	for _, row := range rows {
		for i, cell := range row {
			if width := resourceCellWidth(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}

	for _, row := range rows {
		if _, err := fmt.Fprintln(stdout, resourceTableLine(row, widths)); err != nil {
			return err
		}
	}
	return nil
}

// resourceTableLine pads one row to the measured column widths.
//
// The last cell is never padded and the line is right-trimmed, so an empty
// trailing cell -- an Agent with no conversation pointer -- ends the line where
// its last populated column ends rather than trailing whitespace onto stdout.
func resourceTableLine(row []string, widths []int) string {
	var line strings.Builder
	for i, cell := range row {
		line.WriteString(cell)
		if i == len(row)-1 {
			break
		}
		line.WriteString(strings.Repeat(" ", widths[i]-resourceCellWidth(cell)+resourceTableGap))
	}
	return strings.TrimRight(line.String(), " ")
}

// writeResourceProjection renders a resolution through the shared output
// catalog.
//
// A singular route emits one document; a list route emits one line per match in
// resolution order for the scalar modes and a single List envelope for the
// structured modes, which is the fan-out contract for read results.
//
// The default projection is the one place the two paths diverge in shape rather
// than in count: a list read renders the columnar table, a singular read the
// one-line `kind/name status=... owner=...` summary. The existing `list` flag is
// the whole discriminator, so nothing about describe, rename, or `get pane`
// moves.
//
// now is the invocation's clock and reaches only the columnar table, which is
// the only projection that renders an elapsed time. Every other projection is
// derived from stored bytes alone, so the call sites that cannot reach the
// table pass the zero Time rather than a clock they would never read.
func writeResourceProjection(
	stdout io.Writer,
	spelling string,
	mode cli.OutputMode,
	kind coremetadata.Kind,
	matches []selector.Match,
	registry coremetadata.Registry,
	list bool,
	now time.Time,
) error {
	switch mode {
	case cli.OutputModeNone:
		return nil
	case cli.OutputModeUID, cli.OutputModeName, cli.OutputModeRef, cli.OutputModeDefault:
		if mode == cli.OutputModeDefault && list {
			return writeResourceTable(stdout, spelling, kind, matches, registry, now)
		}
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
