package selector

import (
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// UIDPrefix marks a selector value as an opaque Projmux uid. Without it the
// value is a metadata.name. There is no bare-uid form: uids and names share a
// character set, so an unprefixed value would be ambiguous.
const UIDPrefix = "uid:"

// Ref is one parsed name-or-uid selector occurrence. Exactly one of UID and
// Name is set. Raw preserves the operator's spelling for error text.
type Ref struct {
	Kind metadata.Kind
	UID  string
	Name string
	Raw  string
}

// IsUID reports whether the occurrence used the uid: form.
func (r Ref) IsUID() bool { return r.UID != "" }

// ParseRef parses one --project/--window/--pane occurrence.
//
// The value is taken as a single literal token. It is never split on commas,
// never interpreted as a path, and never matched against a displayName or a
// tmux id; see the package documentation for why each of those is excluded.
func ParseRef(kind metadata.Kind, raw string) (Ref, error) {
	flag := flagNameFor(kind)
	if strings.TrimSpace(raw) == "" {
		return Ref{}, inputErr("parse selector", "--%s requires a value", flag)
	}
	if uid, ok := strings.CutPrefix(raw, UIDPrefix); ok {
		if strings.TrimSpace(uid) == "" {
			return Ref{}, inputErr("parse selector", "--%s %q requires a uid after %q", flag, raw, UIDPrefix)
		}
		return Ref{Kind: kind, UID: uid, Raw: raw}, nil
	}
	return Ref{Kind: kind, Name: raw, Raw: raw}, nil
}

// Label is one parsed --selector key=value condition.
type Label struct {
	Key   string
	Value string
}

// String renders the condition in its input spelling.
func (l Label) String() string { return l.Key + "=" + l.Value }

// ParseLabel parses one --selector occurrence. The value may be empty, which
// matches a label explicitly set to the empty string; the key may not be.
func ParseLabel(raw string) (Label, error) {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		return Label{}, inputErr("parse selector", "--selector %q must be key=value", raw)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return Label{}, inputErr("parse selector", "--selector %q has an empty key", raw)
	}
	return Label{Key: key, Value: strings.TrimSpace(value)}, nil
}

func flagNameFor(kind metadata.Kind) string {
	return strings.ToLower(string(kind))
}

// Query is one route's selector input, already parsed.
//
// Project is the at-most-once exact-one scope. Windows and Panes hold every
// repeated occurrence in argv order. Labels apply to the target kind only, not
// to the scoping refs.
type Query struct {
	Project *Ref
	Windows []Ref
	Panes   []Ref
	Labels  []Label
}

// Stage names one recorded resolution step.
type Stage string

// The three resolution stages, in their fixed contract order.
const (
	// StageNameUIDUnion unions the resolved name/uid occurrences. With no
	// occurrence the whole enclosing scope enters the pipeline.
	StageNameUIDUnion Stage = "name/uid union"
	// StageLabelFilter ANDs the --selector label conditions.
	StageLabelFilter Stage = "label filter"
	// StageUIDDedupe collapses repeated uids, keeping the first occurrence.
	StageUIDDedupe Stage = "uid dedupe"
)

// stageOrder is the canonical stage sequence. Resolution.Trace always reports
// exactly this sequence, so a reordering is a test failure rather than a silent
// behavior change.
var stageOrder = []Stage{StageNameUIDUnion, StageLabelFilter, StageUIDDedupe}

// StageOrder returns the fixed resolution stage order. Callers receive a copy.
func StageOrder() []Stage {
	out := make([]Stage, len(stageOrder))
	copy(out, stageOrder)
	return out
}

// TraceStep records how many resources survived one stage.
type TraceStep struct {
	Stage Stage
	Count int
}

// Status is the interpreted live/offline/missing-root state of a resource.
//
// Window, Pane, and Agent resources have no runtime projection of their own:
// their status is the status of the Project that transitively owns them, which
// is why offline metadata stays queryable after tmux goes away.
type Status string

// The closed status set.
const (
	// StatusLive means the owning Project has a live tmux session projection.
	StatusLive Status = "live"
	// StatusOffline means the resource exists in metadata with no live session.
	StatusOffline Status = "offline"
	// StatusMissingRoot means the owning Project carries a MissingRoot
	// condition: spec.root disappeared. The resource is preserved, never
	// deleted or re-identified, so it stays selectable.
	StatusMissingRoot Status = "missing-root"
)

// ProjectStatus interprets one Project's runtime and reconciliation state.
//
// MissingRoot outranks the session projection because a Project whose root has
// disappeared needs explicit rebind or prune regardless of whether a stale tmux
// session is still up.
func ProjectStatus(project metadata.Project) Status {
	if condition, ok := project.HasCondition(metadata.ConditionMissingRoot); ok && condition.Status == metadata.ConditionTrue {
		return StatusMissingRoot
	}
	if project.Status.Session != nil && project.Status.Session.Live {
		return StatusLive
	}
	return StatusOffline
}

// OwnerContext is the human-readable owner chain of a match. It carries names
// only; identity stays in Match.UID.
type OwnerContext struct {
	Project string
	Window  string
	Agent   string
}

// String renders the owner chain as "project/<name> window/<name>".
func (o OwnerContext) String() string {
	var parts []string
	if o.Project != "" {
		parts = append(parts, "project/"+o.Project)
	}
	if o.Window != "" {
		parts = append(parts, "window/"+o.Window)
	}
	if o.Agent != "" {
		parts = append(parts, "agent/"+o.Agent)
	}
	return strings.Join(parts, " ")
}

// Match is one resolved resource, pinned to its Projmux uid.
//
// DisplayName is reported for operator context only. It never participated in
// the resolution that produced this match.
type Match struct {
	Kind        metadata.Kind
	UID         string
	Name        string
	DisplayName string
	Owner       OwnerContext
	Status      Status
	CWD         string
}

// Resolution is the result of one selector pipeline run.
type Resolution struct {
	Kind    metadata.Kind
	Matches []Match
	Trace   []TraceStep
}

// UIDs returns the resolved uids in match order.
func (r Resolution) UIDs() []string {
	out := make([]string, 0, len(r.Matches))
	for _, match := range r.Matches {
		out = append(out, match.UID)
	}
	return out
}

// Resolver answers selector queries against one registry snapshot.
type Resolver struct {
	registry metadata.Registry
}

// New builds a Resolver over a private copy of registry, so a caller can never
// observe the resolver mutating shared state.
func New(registry metadata.Registry) *Resolver {
	return &Resolver{registry: registry.Clone()}
}

// ResolveProject resolves the exact-one --project selector.
//
// The grammar fixes --project at exact-one, so this returns a usage error with
// a bounded candidate listing on both no-match and ambiguity rather than
// deferring to a per-route cardinality rule.
func (r *Resolver) ResolveProject(ref Ref) (Match, error) {
	scope := r.registry.Projects
	var matched []metadata.Project
	for _, project := range scope {
		if matchesRef(ref, project.Metadata) {
			matched = append(matched, project)
		}
	}
	if len(matched) == 1 {
		return r.projectMatch(matched[0]), nil
	}
	candidates := make([]Match, 0, len(matched))
	for _, project := range matched {
		candidates = append(candidates, r.projectMatch(project))
	}
	return Match{}, cardinalityErr("resolve project", metadata.KindProject, CardinalityExactOne,
		"--project "+ref.Raw, candidates)
}

// ResolveWindows resolves the Window target set for q.
func (r *Resolver) ResolveWindows(q Query) (Resolution, error) {
	scope, err := r.windowScope(q)
	if err != nil {
		return Resolution{}, err
	}
	return runPipeline(metadata.KindWindow, scope, q.Windows, q.Labels,
		func(window metadata.Window) metadata.ObjectMeta { return window.Metadata },
		func(window metadata.Window) Match { return r.windowMatch(window) }), nil
}

// ResolvePanes resolves the Pane target set for q.
//
// The Pane universe is owner-scoped: for every Window in scope it collects the
// Window's own shell Panes plus the managed Panes owned by that Window's
// Agents. A Pane name is unique inside its owner scope, not globally, so the
// same name legitimately appears under several Windows.
func (r *Resolver) ResolvePanes(q Query) (Resolution, error) {
	windows, err := r.windowScope(q)
	if err != nil {
		return Resolution{}, err
	}
	if len(q.Windows) > 0 {
		windows = filterByRefs(windows, q.Windows, func(window metadata.Window) metadata.ObjectMeta { return window.Metadata })
		windows = dedupe(windows, func(window metadata.Window) string { return window.Metadata.UID })
	}
	var scope []metadata.Pane
	for _, window := range windows {
		scope = append(scope, r.registry.PanesOf(window.Metadata.UID)...)
		for _, agent := range r.registry.AgentsOf(window.Metadata.UID) {
			scope = append(scope, r.registry.PanesOf(agent.Metadata.UID)...)
		}
	}
	return runPipeline(metadata.KindPane, scope, q.Panes, q.Labels,
		func(pane metadata.Pane) metadata.ObjectMeta { return pane.Metadata },
		func(pane metadata.Pane) Match { return r.paneMatch(pane) }), nil
}

// windowScope returns the Windows visible to q: one Project's Windows when
// --project is given, the whole registry otherwise.
func (r *Resolver) windowScope(q Query) ([]metadata.Window, error) {
	if q.Project == nil {
		return r.registry.Windows, nil
	}
	project, err := r.ResolveProject(*q.Project)
	if err != nil {
		return nil, err
	}
	return r.registry.WindowsOf(project.UID), nil
}

// runPipeline is the single implementation of the fixed three-stage order.
// Every kind goes through it so the order cannot drift per kind.
func runPipeline[T any](
	kind metadata.Kind,
	scope []T,
	refs []Ref,
	labels []Label,
	meta func(T) metadata.ObjectMeta,
	match func(T) Match,
) Resolution {
	trace := make([]TraceStep, 0, len(stageOrder))

	// Stage 1: name/uid union. With no explicit occurrence the enclosing scope
	// is the union, which is what makes a bare `get panes --project X` list.
	union := scope
	if len(refs) > 0 {
		union = filterByRefs(scope, refs, meta)
	}
	trace = append(trace, TraceStep{Stage: StageNameUIDUnion, Count: len(union)})

	// Stage 2: AND label filter over the target kind.
	filtered := union
	for _, label := range labels {
		filtered = filterByLabel(filtered, label, meta)
	}
	trace = append(trace, TraceStep{Stage: StageLabelFilter, Count: len(filtered)})

	// Stage 3: uid dedupe, keeping the first occurrence.
	deduped := dedupe(filtered, func(item T) string { return meta(item).UID })
	trace = append(trace, TraceStep{Stage: StageUIDDedupe, Count: len(deduped)})

	matches := make([]Match, 0, len(deduped))
	for _, item := range deduped {
		matches = append(matches, match(item))
	}
	return Resolution{Kind: kind, Matches: matches, Trace: trace}
}

// matchesRef reports whether meta satisfies one name/uid occurrence.
//
// Exactly two comparisons are possible: an exact uid equality or an exact name
// equality. displayName, spec.root, and any tmux transport handle are never
// consulted.
func matchesRef(ref Ref, meta metadata.ObjectMeta) bool {
	if ref.IsUID() {
		return meta.UID == ref.UID
	}
	return meta.Name == ref.Name
}

// filterByRefs keeps every scope member matched by at least one occurrence,
// walking occurrences in argv order so the union is deterministic.
func filterByRefs[T any](scope []T, refs []Ref, meta func(T) metadata.ObjectMeta) []T {
	var out []T
	for _, ref := range refs {
		for _, item := range scope {
			if matchesRef(ref, meta(item)) {
				out = append(out, item)
			}
		}
	}
	return out
}

func filterByLabel[T any](scope []T, label Label, meta func(T) metadata.ObjectMeta) []T {
	var out []T
	for _, item := range scope {
		labels := meta(item).Labels
		if value, ok := labels[label.Key]; ok && value == label.Value {
			out = append(out, item)
		}
	}
	return out
}

func dedupe[T any](scope []T, key func(T) string) []T {
	seen := make(map[string]bool, len(scope))
	var out []T
	for _, item := range scope {
		id := key(item)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, item)
	}
	return out
}

func (r *Resolver) projectMatch(project metadata.Project) Match {
	return Match{
		Kind:        metadata.KindProject,
		UID:         project.Metadata.UID,
		Name:        project.Metadata.Name,
		DisplayName: project.Metadata.DisplayName,
		Status:      ProjectStatus(project),
		CWD:         project.Spec.Root,
	}
}

func (r *Resolver) windowMatch(window metadata.Window) Match {
	owner := OwnerContext{}
	status := StatusOffline
	if project, ok := r.registry.Project(window.Metadata.OwnerUID()); ok {
		owner.Project = project.Metadata.Name
		status = ProjectStatus(*project)
	}
	return Match{
		Kind:        metadata.KindWindow,
		UID:         window.Metadata.UID,
		Name:        window.Metadata.Name,
		DisplayName: window.Metadata.DisplayName,
		Owner:       owner,
		Status:      status,
	}
}

func (r *Resolver) paneMatch(pane metadata.Pane) Match {
	owner, status := r.paneOwner(pane)
	return Match{
		Kind:        metadata.KindPane,
		UID:         pane.Metadata.UID,
		Name:        pane.Metadata.Name,
		DisplayName: pane.Metadata.DisplayName,
		Owner:       owner,
		Status:      status,
		CWD:         pane.Spec.CWD,
	}
}

// paneOwner walks the owner chain up to the Project. A shell Pane is owned by
// its Window; a managed Pane is owned by its Agent, which is owned by a Window.
func (r *Resolver) paneOwner(pane metadata.Pane) (OwnerContext, Status) {
	owner := OwnerContext{}
	windowUID := pane.Metadata.OwnerUID()
	if agent, ok := r.registry.Agent(windowUID); ok {
		owner.Agent = agent.Metadata.Name
		windowUID = agent.Metadata.OwnerUID()
	}
	window, ok := r.registry.Window(windowUID)
	if !ok {
		return owner, StatusOffline
	}
	owner.Window = window.Metadata.Name
	project, ok := r.registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return owner, StatusOffline
	}
	owner.Project = project.Metadata.Name
	return owner, ProjectStatus(*project)
}
