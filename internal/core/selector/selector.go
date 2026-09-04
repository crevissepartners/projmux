package selector

import (
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
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
// never interpreted as a path, and never matched against presentation context
// or a tmux id; see the package documentation for why each is excluded.
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
// Project is the at-most-once exact-one scope. Windows, Panes, and Agents hold
// every repeated occurrence in argv order. Labels apply to the target kind
// only, not to the scoping refs.
//
// Agents carries the Agent name/uid occurrences of an Agent-kind route. There is
// no public `--agent` scope selector: the grammar contract fixes the repeatable
// scope flags at --project/--window/--pane, so this field is only ever filled
// from the positional resource ref of an Agent route.
type Query struct {
	Project *Ref
	// DefaultRoot is an invocation-derived managed-root scope used only when
	// the operator did not pass --project. It is kept separate from Project so
	// explicit selector rendering and compatibility stay truthful. The shared
	// windowScope seam is the only place that chooses an explicit Project, an
	// invocation-derived Project or ControlSession root, or the whole registry.
	//
	// The root is always an exact uid observed through a Registry owner chain;
	// there is intentionally no public ControlSession selector grammar.
	DefaultRoot *RootScope
	Windows     []Ref
	Panes       []Ref
	Agents      []Ref
	Labels      []Label
}

// RootScope is one exact, invocation-derived Registry root.
//
// It is not a selector occurrence: users cannot spell a ControlSession scope,
// and a root name is never inferred. Keeping the kind beside the uid prevents
// a ControlSession from being flattened into the Project-only public grammar.
type RootScope struct {
	Kind metadata.Kind
	UID  string
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
//
// There is deliberately no accessor. Every consumer of the order is one of this
// package's own tests, which read this table directly; production consumes the
// pipeline through Resolution.Trace. An exported copy-returning accessor would
// be a test-only symbol on the production surface, and an unexported one would
// still be a test-only function. Neither is worth carrying, so callers that need
// a mutable list copy this slice themselves.
var stageOrder = []Stage{StageNameUIDUnion, StageLabelFilter, StageUIDDedupe}

// TraceStep records how many resources survived one stage.
type TraceStep struct {
	Stage Stage
	Count int
}

// Status is the interpreted live/offline/missing-root state of a resource.
//
// Status is an *observation*, never a stored field a read path trusts. Spec is
// the opposite: stored spec is authoritative. A Window or Pane is live because
// a live tmux object still mirrors its uid right now, not because something
// once wrote a bool saying so.
//
// Offline metadata stays fully queryable. Nothing here deletes, prunes, or
// re-identifies a resource whose runtime went away.
type Status string

// The closed status set.
const (
	// StatusLive means the resource's own runtime object was observed live:
	// a mirrored uid on a live tmux window or pane for a Window or Pane, and a
	// live session projection for a Project.
	StatusLive Status = "live"
	// StatusOffline means the resource exists in metadata and its runtime
	// object was not observed. Its metadata is untouched and still selectable.
	StatusOffline Status = "offline"
	// StatusMissingRoot means the owning Project carries a MissingRoot
	// condition: spec.root disappeared. The resource is preserved, never
	// deleted or re-identified, so it stays selectable.
	StatusMissingRoot Status = "missing-root"
)

// ObservedStatus is the single status-derivation rule in the codebase.
//
// Every kind goes through it -- Project, Window, Pane, and Agent -- so the
// precedence can never drift per kind. It takes exactly two facts and no
// resource, which is the point: there is no third input a caller could smuggle
// a stored liveness bool in through.
//
//   - missingRoot outranks everything. A resource whose owning Project lost its
//     spec.root needs an explicit rebind or prune regardless of what tmux is
//     doing, so a stale live session must not hide it. This is the preservation
//     contract StatusMissingRoot established and it is unchanged.
//   - bound is the live observation of the resource's own runtime object. It is
//     supplied by the caller from a live-tmux snapshot; an absent observation is
//     false, which can only downgrade a resource to offline and can never
//     invent a live one.
func ObservedStatus(missingRoot, bound bool) Status {
	switch {
	case missingRoot:
		return StatusMissingRoot
	case bound:
		return StatusLive
	default:
		return StatusOffline
	}
}

// ProjectStatus interprets one Project's runtime and reconciliation state.
//
// A Project is the one kind whose runtime object is a tmux *session*, which the
// reconciler projects onto status.session. It is deliberately not part of the
// Window/Pane uid observation: a session has no @projmux uid of its own, and
// the two observed sets are the whole tmux-query budget of one invocation.
func ProjectStatus(project metadata.Project) Status {
	return ObservedStatus(hasMissingRoot(project), project.Status.Session != nil && project.Status.Session.Live)
}

// hasMissingRoot reports whether a Project carries an active MissingRoot
// condition.
func hasMissingRoot(project metadata.Project) bool {
	condition, ok := project.HasCondition(metadata.ConditionMissingRoot)
	return ok && condition.Status == metadata.ConditionTrue
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
// Context is reported for operator context only. It never participates in the
// resolution that produced this match.
type Match struct {
	Kind    metadata.Kind
	UID     string
	Name    string
	Context registryview.Context
	Owner   OwnerContext
	Status  Status
	CWD     string
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

// Resolver answers selector queries against one registry snapshot plus one
// live-tmux observation.
//
// The two are separate inputs on purpose. The registry answers "which resources
// exist and what are they called"; the observation answers "which of them still
// have a runtime object". Identity resolution never consults the observation,
// so an offline resource resolves exactly like a live one -- only its reported
// Status differs.
type Resolver struct {
	registry metadata.Registry
	observed metadata.RuntimeObservation
	contexts registryview.Projector
}

// New builds a Resolver with no live-tmux observation.
//
// With no observation every Window and Pane reports offline. That is the
// fail-closed reading of "nothing was observed" and it is correct for the
// callers that use it: create's Project scope, target-Window, and anchor-Pane
// lookups need identity resolution only and never render Status, so paying for
// two tmux queries to fill a field nobody prints would be waste.
//
// A route that *renders* Status must use NewObserved. There is no third option
// and there is deliberately no fallback to a stored value: reporting live from
// something the registry once wrote down is the exact defect this constructor
// pair exists to make impossible.
func New(registry metadata.Registry) *Resolver {
	return NewObserved(registry, metadata.RuntimeObservation{})
}

// NewObserved builds a Resolver over a private copy of registry and observed,
// so a caller can never observe the resolver mutating shared state.
//
// observed is one snapshot taken once per process invocation and thrown away
// with it. It is not a cache and it is never persisted: the next invocation
// takes a fresh one, which is what makes closing a pane visible to the very
// next query with no hook involved.
func NewObserved(registry metadata.Registry, observed metadata.RuntimeObservation) *Resolver {
	return NewObservedWithContext(registry, observed, registryview.NewContextProjector(registry))
}

// NewObservedWithContext builds a Resolver whose human ambiguity projection
// comes from the caller's one invocation-scoped projector. Matching and
// cardinality still read only registry identity/address/classification fields.
func NewObservedWithContext(registry metadata.Registry, observed metadata.RuntimeObservation, contexts registryview.Projector) *Resolver {
	registry = registry.Clone()
	return &Resolver{
		registry: registry,
		observed: observed.Clone(),
		contexts: contexts,
	}
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

// ResolveProjects resolves the Project target set for q.
//
// --project is the at-most-once Project occurrence, so the union stage sees at
// most one ref; with none the whole registry enters the pipeline, which is what
// makes a bare `get projects` a list. It returns no error because there is no
// enclosing scope to fail to resolve: cardinality is enforced by the caller
// against the declared <verb, kind> cell.
func (r *Resolver) ResolveProjects(q Query) (Resolution, error) {
	var refs []Ref
	if q.Project != nil {
		refs = []Ref{*q.Project}
	}
	return runPipeline(metadata.KindProject, r.registry.Projects, refs, q.Labels,
		func(project metadata.Project) metadata.ObjectMeta { return project.Metadata },
		func(project metadata.Project) Match { return r.projectMatch(project) }), nil
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
// The Pane universe follows the selected Window set: for every Window in scope
// it collects the Window's own shell Panes plus the managed Panes owned by that
// Window's Agents. A Pane name is unique root-wide, so it may repeat only when
// those Windows belong to different Project or ControlSession roots.
func (r *Resolver) ResolvePanes(q Query) (Resolution, error) {
	windows, err := r.selectedWindows(q)
	if err != nil {
		return Resolution{}, err
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

// ResolveAgents resolves the Agent target set for q.
//
// Agents remain Window-owned, so the enclosing target scope is the same
// --project / --window scope the Pane resolution uses. Agent names are unique
// root-wide and may repeat only across distinct Project or ControlSession roots.
func (r *Resolver) ResolveAgents(q Query) (Resolution, error) {
	windows, err := r.selectedWindows(q)
	if err != nil {
		return Resolution{}, err
	}
	var scope []metadata.Agent
	for _, window := range windows {
		scope = append(scope, r.registry.AgentsOf(window.Metadata.UID)...)
	}
	return runPipeline(metadata.KindAgent, scope, q.Agents, q.Labels,
		func(agent metadata.Agent) metadata.ObjectMeta { return agent.Metadata },
		func(agent metadata.Agent) Match { return r.agentMatch(agent) }), nil
}

// selectedWindows returns the Windows the query addresses: the --project scope
// narrowed by the repeated --window occurrences when there are any. It is the
// shared enclosing scope of the Pane and Agent universes.
func (r *Resolver) selectedWindows(q Query) ([]metadata.Window, error) {
	windows, err := r.windowScope(q)
	if err != nil {
		return nil, err
	}
	if len(q.Windows) == 0 {
		return windows, nil
	}
	windows = filterByRefs(windows, q.Windows, func(window metadata.Window) metadata.ObjectMeta { return window.Metadata })
	return dedupe(windows, func(window metadata.Window) string { return window.Metadata.UID }), nil
}

// windowScope is the single root-scope decision for Window, Pane, and Agent
// queries. An explicit --project wins; otherwise an invocation-derived Project
// or ControlSession root narrows the read; with neither, the whole registry
// remains visible.
func (r *Resolver) windowScope(q Query) ([]metadata.Window, error) {
	if q.Project != nil {
		project, err := r.ResolveProject(*q.Project)
		if err != nil {
			return nil, err
		}
		return r.windowsOwnedBy(metadata.KindProject, project.UID), nil
	}
	if q.DefaultRoot == nil {
		return r.registry.Windows, nil
	}

	root := *q.DefaultRoot
	switch root.Kind {
	case metadata.KindProject:
		if _, ok := r.registry.Project(root.UID); !ok {
			return nil, inputErr("resolve window scope", "default Project uid %q is not in the registry", root.UID)
		}
	case metadata.KindControlSession:
		if _, ok := r.registry.ControlSession(root.UID); !ok {
			return nil, inputErr("resolve window scope", "default ControlSession uid %q is not in the registry", root.UID)
		}
	default:
		return nil, inputErr("resolve window scope", "default root kind %q is not Project or ControlSession", root.Kind)
	}
	return r.windowsOwnedBy(root.Kind, root.UID), nil
}

// windowsOwnedBy keeps root-kind equality explicit instead of relying on uid
// uniqueness alone. That makes the owner chain itself the scope authority.
func (r *Resolver) windowsOwnedBy(kind metadata.Kind, uid string) []metadata.Window {
	windows := make([]metadata.Window, 0)
	for _, window := range r.registry.Windows {
		owner := window.Metadata.OwnerRef
		if owner != nil && owner.Kind == kind && owner.UID == uid {
			windows = append(windows, window)
		}
	}
	return windows
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
// equality. Presentation context, spec.root, and any tmux transport handle are
// never consulted.
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
		Kind:    metadata.KindProject,
		UID:     project.Metadata.UID,
		Name:    project.Metadata.Name,
		Context: r.contexts.For(metadata.KindProject, project.Metadata.UID),
		Status:  ProjectStatus(project),
		CWD:     project.Spec.Root,
	}
}

// windowMatch renders one Window with its observed status.
//
// The Window's own runtime object is a tmux window carrying @projmux_window_uid.
// The owning Project contributes exactly one thing to the answer -- whether it
// carries MissingRoot -- and specifically not its session projection: a Window
// under a Project whose session bool says live is still offline when no live
// tmux window mirrors its uid, which is the whole point of the change.
func (r *Resolver) windowMatch(window metadata.Window) Match {
	owner := OwnerContext{}
	missingRoot := false
	if project, ok := r.registry.Project(window.Metadata.OwnerUID()); ok {
		owner.Project = project.Metadata.Name
		missingRoot = hasMissingRoot(*project)
	}
	return Match{
		Kind:    metadata.KindWindow,
		UID:     window.Metadata.UID,
		Name:    window.Metadata.Name,
		Context: r.contexts.For(metadata.KindWindow, window.Metadata.UID),
		Owner:   owner,
		Status:  ObservedStatus(missingRoot, r.observed.BoundWindow(window.Metadata.UID)),
	}
}

// agentMatch renders one Agent with its Window/Project owner chain and its
// observed status.
//
// An Agent owns no tmux object of its own: there is no @projmux_agent_uid to
// observe, and there must not be one, because an Agent outlives the managed
// Pane it is currently bound to. Its runtime object is that managed Pane, named
// by status.paneRef, and that is what is observed here.
//
// It used to report the status of the owning Window instead -- the nearest
// enclosing thing that was observable at the time. That was the last surviving
// piece of status inheritance, and it produced exactly the contradiction the
// observation contract exists to prevent: once one Window was adopted and went
// live, every Agent under it read live whether or not it had a pane, so
// `get agents` said live for the same resource `describe agent` said was
// Offline with no managed pane.
//
// The Window still contributes one thing and only one: whether the owning
// Project carries MissingRoot. Its liveness is deliberately not read. A Window
// that is live says nothing about whether a particular Agent inside it is
// running, which is the whole defect.
//
// status.phase is deliberately not an input either. Phase is lifecycle -- a
// stored value the Agent liveness track owns and this file must not infer,
// duplicate, or transition -- while Status is an observation, and mixing a
// stored value back into the observation is the thing the product contract
// forbids. They cannot contradict anyway: the registry's own invariant is that
// every non-Running phase clears paneRef, so a non-Running Agent has no runtime
// object to observe and reports offline on the observation alone.
func (r *Resolver) agentMatch(agent metadata.Agent) Match {
	owner := OwnerContext{}
	missingRoot := false
	if window, ok := r.registry.Window(agent.Metadata.OwnerUID()); ok {
		owner.Window = window.Metadata.Name
		if project, ok := r.registry.Project(window.Metadata.OwnerUID()); ok {
			owner.Project = project.Metadata.Name
			missingRoot = hasMissingRoot(*project)
		}
	}
	return Match{
		Kind:    metadata.KindAgent,
		UID:     agent.Metadata.UID,
		Name:    agent.Metadata.Name,
		Context: r.contexts.For(metadata.KindAgent, agent.Metadata.UID),
		Owner:   owner,
		Status:  ObservedStatus(missingRoot, r.agentBound(agent)),
	}
}

// agentBound reports whether an Agent's own runtime object was observed live.
//
// An empty paneRef is false, which is the answer for every Agent released by
// the liveness sweep and every Agent that has never been attached: it has no
// runtime object, so nothing about it can be observed live. That is a real
// state, not a missing one, and it is why offline is the right report rather
// than some third value -- the Agent's metadata stays fully queryable and
// nothing prunes it.
//
// The Pane must still exist in the registry as well as in tmux. A paneRef that
// names a Pane the registry no longer holds is not a runtime observation of
// anything, and reporting live from it would make `get agents` disagree with a
// `get panes` that has no such row.
func (r *Resolver) agentBound(agent metadata.Agent) bool {
	paneUID := agent.Status.PaneRef
	if paneUID == "" {
		return false
	}
	if _, ok := r.registry.Pane(paneUID); !ok {
		return false
	}
	return r.observed.BoundPane(paneUID)
}

// paneMatch renders one Pane with its observed status.
//
// The Pane's own runtime object is a tmux pane carrying @projmux_pane_uid, so a
// Pane is judged directly rather than through its Window: closing one pane in a
// live window offlines exactly that Pane.
func (r *Resolver) paneMatch(pane metadata.Pane) Match {
	owner, missingRoot := r.paneOwner(pane)
	return Match{
		Kind:    metadata.KindPane,
		UID:     pane.Metadata.UID,
		Name:    pane.Metadata.Name,
		Context: r.contexts.For(metadata.KindPane, pane.Metadata.UID),
		Owner:   owner,
		Status:  ObservedStatus(missingRoot, r.observed.BoundPane(pane.Metadata.UID)),
		CWD:     pane.Spec.CWD,
	}
}

// paneOwner walks the owner chain up to the Project. A shell Pane is owned by
// its Window; a managed Pane is owned by its Agent, which is owned by a Window.
//
// The second result is the owning Project's MissingRoot flag, which is the only
// thing the ancestry contributes to a Pane's status. A Pane whose owner chain is
// broken reports no MissingRoot: it is judged purely on its own observation, so
// a live tmux pane is never called offline just because its owner row is gone.
func (r *Resolver) paneOwner(pane metadata.Pane) (OwnerContext, bool) {
	owner := OwnerContext{}
	windowUID := pane.Metadata.OwnerUID()
	if agent, ok := r.registry.Agent(windowUID); ok {
		owner.Agent = agent.Metadata.Name
		windowUID = agent.Metadata.OwnerUID()
	}
	window, ok := r.registry.Window(windowUID)
	if !ok {
		return owner, false
	}
	owner.Window = window.Metadata.Name
	project, ok := r.registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return owner, false
	}
	owner.Project = project.Metadata.Name
	return owner, hasMissingRoot(*project)
}
