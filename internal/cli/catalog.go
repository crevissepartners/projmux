package cli

import (
	"fmt"
	"slices"
	"strings"
)

// Disposition is the primary Phase 0 classification of a current public or
// hidden route. Every current route carries exactly one disposition; the
// compatibility contract keeps orphan routes at zero for every later Phase.
type Disposition string

// The seven effect enums separate the independent resource-graph consequences
// of one executable route. They deliberately describe allowed outcomes rather
// than promises that every invocation mutates: preflight refusal is always a
// zero-effect outcome, and conditional routes such as create project may
// create or reuse the same schema-v3 graph.
type IdentityEffect string
type AddressEffect string
type TopologyEffect string
type DesiredStateEffect string
type RuntimeEffect string
type FocusEffect string
type CardinalityEffect string

const (
	IdentityUnchanged IdentityEffect = "unchanged"
	IdentityCreated   IdentityEffect = "created"
	IdentityReused    IdentityEffect = "reused"
	IdentityRemoved   IdentityEffect = "removed"
	IdentityReplaced  IdentityEffect = "replaced"
)

const (
	AddressUnchanged AddressEffect = "unchanged"
	AddressAllocated AddressEffect = "allocated"
	AddressRenamed   AddressEffect = "renamed"
	AddressReleased  AddressEffect = "released"
)

const (
	TopologyUnchanged   TopologyEffect = "unchanged"
	TopologyEstablished TopologyEffect = "established"
	TopologyReparented  TopologyEffect = "reparented"
	TopologyRemoved     TopologyEffect = "removed"
	TopologyReplaced    TopologyEffect = "replaced"
)

const (
	DesiredStateUnchanged DesiredStateEffect = "unchanged"
	DesiredStateCreated   DesiredStateEffect = "created"
	DesiredStateReused    DesiredStateEffect = "reused"
	DesiredStateRemoved   DesiredStateEffect = "removed"
	DesiredStateReplaced  DesiredStateEffect = "replaced"
)

const (
	RuntimeUnchanged    RuntimeEffect = "unchanged"
	RuntimeMaterialized RuntimeEffect = "materialized"
	RuntimeAlreadyLive  RuntimeEffect = "already-live"
	RuntimeReparented   RuntimeEffect = "reparented"
	RuntimeStopped      RuntimeEffect = "stopped"
	RuntimePreserved    RuntimeEffect = "preserved"
)

const (
	FocusUnchanged          FocusEffect = "unchanged"
	FocusMovedCurrentClient FocusEffect = "moved-current-client"
	FocusAttachedCaller     FocusEffect = "attached-caller"
)

const (
	CardinalityUnchanged  CardinalityEffect = "unchanged"
	CardinalityExactOne   CardinalityEffect = "exact-one"
	CardinalityOneOrMore  CardinalityEffect = "one-or-more"
	CardinalityZeroOrMore CardinalityEffect = "zero-or-more"
)

var (
	identityEffects = []IdentityEffect{IdentityUnchanged, IdentityCreated, IdentityReused, IdentityRemoved, IdentityReplaced}
	addressEffects  = []AddressEffect{AddressUnchanged, AddressAllocated, AddressRenamed, AddressReleased}
	topologyEffects = []TopologyEffect{TopologyUnchanged, TopologyEstablished, TopologyReparented, TopologyRemoved, TopologyReplaced}
	desiredEffects  = []DesiredStateEffect{DesiredStateUnchanged, DesiredStateCreated, DesiredStateReused, DesiredStateRemoved, DesiredStateReplaced}
	runtimeEffects  = []RuntimeEffect{RuntimeUnchanged, RuntimeMaterialized, RuntimeAlreadyLive, RuntimeReparented, RuntimeStopped, RuntimePreserved}
	focusEffects    = []FocusEffect{FocusUnchanged, FocusMovedCurrentClient, FocusAttachedCaller}
	cardinalities   = []CardinalityEffect{CardinalityUnchanged, CardinalityExactOne, CardinalityOneOrMore, CardinalityZeroOrMore}
)

// DomainEffectKind is the closed extension discriminant for effects outside
// the Projmux resource graph. Phase 0 introduces only the downstream delivery
// seam; it does not add a send route, reducer, receipt, or provider adapter.
type DomainEffectKind string

const DomainEffectAgentDelivery DomainEffectKind = "agent-delivery"

var domainEffectKinds = []DomainEffectKind{DomainEffectAgentDelivery}

// DomainEffect declares one optional non-resource effect. Nil is the explicit
// null form used by current routes whose contract is fully described by the
// resource tuple in this Phase.
type DomainEffect struct {
	Kind DomainEffectKind
}

// AllowedEffects is the one seven-axis effect record carried by a Route. Each
// field is a non-empty closed set because one route can have conditional
// success outcomes (for example create versus reuse) without becoming two
// command rows.
type AllowedEffects struct {
	Identity     []IdentityEffect
	Address      []AddressEffect
	Topology     []TopologyEffect
	DesiredState []DesiredStateEffect
	Runtime      []RuntimeEffect
	Focus        []FocusEffect
	Cardinality  []CardinalityEffect
	DomainEffect *DomainEffect
}

func allowedEffects(
	identity []IdentityEffect,
	address []AddressEffect,
	topology []TopologyEffect,
	desired []DesiredStateEffect,
	runtime []RuntimeEffect,
	focus []FocusEffect,
	cardinality []CardinalityEffect,
) *AllowedEffects {
	return &AllowedEffects{
		Identity: identity, Address: address, Topology: topology,
		DesiredState: desired, Runtime: runtime, Focus: focus,
		Cardinality: cardinality,
	}
}

func unchangedEffects(cardinality CardinalityEffect) *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateUnchanged},
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{cardinality},
	)
}

func createProjectEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityCreated, IdentityReused},
		[]AddressEffect{AddressAllocated, AddressUnchanged},
		[]TopologyEffect{TopologyEstablished, TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateCreated, DesiredStateReused},
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityExactOne},
	)
}

func createResourceEffects(cardinality CardinalityEffect) *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityCreated},
		[]AddressEffect{AddressAllocated},
		[]TopologyEffect{TopologyEstablished},
		[]DesiredStateEffect{DesiredStateCreated},
		[]RuntimeEffect{RuntimeMaterialized},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{cardinality},
	)
}

func resumeAgentEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityReused, IdentityCreated},
		[]AddressEffect{AddressUnchanged, AddressAllocated},
		[]TopologyEffect{TopologyUnchanged, TopologyEstablished},
		[]DesiredStateEffect{DesiredStateUnchanged, DesiredStateCreated},
		[]RuntimeEffect{RuntimeMaterialized},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityExactOne},
	)
}

func renameResourceEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressRenamed},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateUnchanged},
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityExactOne},
	)
}

func deleteProjectEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityRemoved},
		[]AddressEffect{AddressReleased},
		[]TopologyEffect{TopologyRemoved},
		[]DesiredStateEffect{DesiredStateRemoved},
		[]RuntimeEffect{RuntimePreserved},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityOneOrMore},
	)
}

func deleteChildEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityRemoved},
		[]AddressEffect{AddressReleased},
		[]TopologyEffect{TopologyRemoved},
		[]DesiredStateEffect{DesiredStateRemoved},
		[]RuntimeEffect{RuntimeUnchanged, RuntimeStopped},
		[]FocusEffect{FocusUnchanged, FocusMovedCurrentClient},
		[]CardinalityEffect{CardinalityOneOrMore},
	)
}

func attachProjectEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateUnchanged},
		[]RuntimeEffect{RuntimeMaterialized, RuntimeAlreadyLive},
		[]FocusEffect{FocusAttachedCaller},
		[]CardinalityEffect{CardinalityExactOne},
	)
}

func focusResourceEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateUnchanged},
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusMovedCurrentClient},
		[]CardinalityEffect{CardinalityExactOne},
	)
}

func switchProjectEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged, IdentityCreated, IdentityReused, IdentityReplaced},
		[]AddressEffect{AddressUnchanged, AddressAllocated, AddressReleased},
		[]TopologyEffect{TopologyUnchanged, TopologyEstablished, TopologyReplaced},
		[]DesiredStateEffect{DesiredStateUnchanged, DesiredStateCreated, DesiredStateReused, DesiredStateReplaced},
		[]RuntimeEffect{RuntimeUnchanged, RuntimeMaterialized, RuntimeAlreadyLive, RuntimeStopped},
		[]FocusEffect{FocusUnchanged, FocusMovedCurrentClient, FocusAttachedCaller},
		[]CardinalityEffect{CardinalityUnchanged, CardinalityExactOne},
	)
}

func rebindProjectEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateReplaced},
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityExactOne},
	)
}

func pruneProjectEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityRemoved},
		[]AddressEffect{AddressReleased},
		[]TopologyEffect{TopologyRemoved},
		[]DesiredStateEffect{DesiredStateRemoved},
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityZeroOrMore},
	)
}

func restoreSnapshotEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged, IdentityCreated, IdentityReused, IdentityRemoved, IdentityReplaced},
		[]AddressEffect{AddressUnchanged, AddressAllocated, AddressReleased},
		[]TopologyEffect{TopologyUnchanged, TopologyEstablished, TopologyRemoved, TopologyReplaced},
		[]DesiredStateEffect{DesiredStateUnchanged, DesiredStateCreated, DesiredStateRemoved, DesiredStateReplaced},
		[]RuntimeEffect{RuntimeUnchanged, RuntimeMaterialized},
		[]FocusEffect{FocusUnchanged, FocusMovedCurrentClient, FocusAttachedCaller},
		[]CardinalityEffect{CardinalityUnchanged, CardinalityOneOrMore},
	)
}

func shellEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged, IdentityCreated, IdentityReused},
		[]AddressEffect{AddressUnchanged, AddressAllocated},
		[]TopologyEffect{TopologyUnchanged, TopologyEstablished},
		[]DesiredStateEffect{DesiredStateUnchanged, DesiredStateCreated, DesiredStateReused},
		[]RuntimeEffect{RuntimeMaterialized, RuntimeAlreadyLive},
		[]FocusEffect{FocusAttachedCaller},
		[]CardinalityEffect{CardinalityExactOne, CardinalityOneOrMore},
	)
}

func reconcileResourcesEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged, IdentityCreated},
		[]AddressEffect{AddressUnchanged, AddressAllocated},
		[]TopologyEffect{TopologyUnchanged, TopologyEstablished, TopologyReparented},
		[]DesiredStateEffect{DesiredStateUnchanged, DesiredStateCreated, DesiredStateReplaced},
		[]RuntimeEffect{RuntimeUnchanged, RuntimeMaterialized},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityExactOne, CardinalityZeroOrMore},
	)
}

func reconcileRegistryEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged, IdentityCreated, IdentityRemoved, IdentityReplaced},
		[]AddressEffect{AddressUnchanged, AddressAllocated, AddressRenamed, AddressReleased},
		[]TopologyEffect{TopologyUnchanged, TopologyEstablished, TopologyReparented, TopologyRemoved, TopologyReplaced},
		[]DesiredStateEffect{DesiredStateUnchanged, DesiredStateCreated, DesiredStateRemoved, DesiredStateReplaced},
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityZeroOrMore},
	)
}

func focusIngressEffects() *AllowedEffects {
	return runtimeEffectsOnly(
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusUnchanged, FocusMovedCurrentClient},
		CardinalityExactOne,
	)
}

func openRuntimeTargetEffects() *AllowedEffects {
	return runtimeEffectsOnly(
		[]RuntimeEffect{RuntimeAlreadyLive},
		[]FocusEffect{FocusMovedCurrentClient, FocusAttachedCaller},
		CardinalityExactOne,
	)
}

func runtimeSessionsEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateUnchanged},
		[]RuntimeEffect{RuntimeUnchanged, RuntimeMaterialized, RuntimeAlreadyLive, RuntimeStopped},
		[]FocusEffect{FocusUnchanged, FocusMovedCurrentClient, FocusAttachedCaller},
		[]CardinalityEffect{CardinalityUnchanged, CardinalityExactOne},
	)
}

func agentPaneLaunchEffects(allowResume bool) *AllowedEffects {
	identity := []IdentityEffect{IdentityUnchanged, IdentityCreated}
	if allowResume {
		identity = append(identity, IdentityReused)
	}
	return allowedEffects(
		identity,
		[]AddressEffect{AddressUnchanged, AddressAllocated},
		[]TopologyEffect{TopologyUnchanged, TopologyEstablished},
		[]DesiredStateEffect{DesiredStateUnchanged, DesiredStateCreated},
		[]RuntimeEffect{RuntimeUnchanged, RuntimeMaterialized},
		[]FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityUnchanged, CardinalityExactOne},
	)
}

func runtimeDiagnosticsEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateUnchanged},
		[]RuntimeEffect{RuntimeUnchanged, RuntimeMaterialized, RuntimeAlreadyLive},
		[]FocusEffect{FocusUnchanged, FocusMovedCurrentClient, FocusAttachedCaller},
		[]CardinalityEffect{CardinalityUnchanged, CardinalityExactOne},
	)
}

func statusbarClickEffects() *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateUnchanged},
		[]RuntimeEffect{RuntimeUnchanged},
		[]FocusEffect{FocusUnchanged, FocusMovedCurrentClient},
		[]CardinalityEffect{CardinalityUnchanged, CardinalityExactOne},
	)
}

func runtimeEffectsOnly(runtime []RuntimeEffect, focus []FocusEffect, cardinality CardinalityEffect) *AllowedEffects {
	return allowedEffects(
		[]IdentityEffect{IdentityUnchanged},
		[]AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyUnchanged},
		[]DesiredStateEffect{DesiredStateUnchanged},
		runtime,
		focus,
		[]CardinalityEffect{cardinality},
	)
}

// InvocationAuthority is the selectorless authority contract of one executable
// command node. It is carried by the unified Route graph beside the parser,
// help, canonical, and output projections; it is not a second command
// manifest.
//
// Every node declares the field directly. Parent inheritance is forbidden: a
// newly added command with no deliberate classification must fail the graph
// completeness gate.
type InvocationAuthority string

const (
	// InvocationNatural selects one predictable current Pane, Window, or owner
	// resource when the operator omits selectors. An explicit selector replaces
	// that natural target rather than blending with it.
	InvocationNatural InvocationAuthority = "natural-omitted"
	// InvocationExplicit requires the operator or generated caller to name the
	// target. Ambient tmux context may prove the runtime route, but cannot choose
	// or narrow the resource target.
	InvocationExplicit InvocationAuthority = "explicit-target"
	// InvocationRefusal has no safe selectorless target. Omission is a typed,
	// pre-write refusal rather than an implicit whole-set operation.
	InvocationRefusal InvocationAuthority = "refusal"
	// InvocationFanOut permits a whole-set or named global operation only through
	// that route's explicit opt-in spelling.
	InvocationFanOut InvocationAuthority = "explicit-fan-out"
)

var invocationAuthorities = []InvocationAuthority{
	InvocationNatural,
	InvocationExplicit,
	InvocationRefusal,
	InvocationFanOut,
}

// InvocationAuthorities returns the closed four-class set in documentation
// order.
func InvocationAuthorities() []InvocationAuthority {
	return slices.Clone(invocationAuthorities)
}

// Summary explains the omission behavior represented by the stable machine
// label. Runtime help pairs both so an explicit selector on a natural-default
// route is not mistaken for a contradiction.
func (a InvocationAuthority) Summary() string {
	switch a {
	case InvocationNatural:
		return "omission resolves one predictable current resource or documented contextual read/scope; any selector replaces it"
	case InvocationExplicit:
		return "the route or caller must name the exact target"
	case InvocationRefusal:
		return "there is no safe selectorless action; refuse before output or mutation"
	case InvocationFanOut:
		return "the route spelling is an intentional global or whole-set opt-in"
	default:
		return "unclassified"
	}
}

// The four primary dispositions from the CLI information architecture v2
// compatibility contract.
const (
	// DispositionCanonical marks a route whose current noun/domain namespace
	// (or standard global command) already fits the v2 model.
	DispositionCanonical Disposition = "canonical"
	// DispositionShortcut marks a high-frequency product entrypoint kept as a
	// top-level alias over a canonical handler.
	DispositionShortcut Disposition = "shortcut"
	// DispositionCompatibility marks a currently public route whose name or
	// responsibility is ambiguous in the v2 model.
	DispositionCompatibility Disposition = "compatibility"
	// DispositionInternal marks plumbing invoked by generated tmux config,
	// hooks, or popups rather than by users.
	DispositionInternal Disposition = "internal"
)

// dispositions is the closed disposition set used by the coverage audit.
var dispositions = []Disposition{
	DispositionCanonical,
	DispositionShortcut,
	DispositionCompatibility,
	DispositionInternal,
}

// Dispositions returns the closed disposition set in contract order.
func Dispositions() []Disposition {
	out := make([]Disposition, len(dispositions))
	copy(out, dispositions)
	return out
}

// Route is one node of the current CLI surface. Top-level routes own the
// primary Disposition and Hidden flag; nested children describe the current
// sub-route tree used by the shared help renderer and carry no disposition of
// their own (the parent's disposition covers the whole node).
type Route struct {
	// Effects is the route's single seven-axis allowed-effect record. It is a
	// pointer so omission cannot be confused with an all-unchanged declaration;
	// Routes fails closed on nil, unknown, empty, or duplicate values.
	Effects *AllowedEffects
	// Name is the exact current argv token for this node. It is the canonical
	// spelling: everything a route prints about itself is built from it, and an
	// alias never replaces it anywhere.
	Name string
	// Aliases are extra argv tokens that reach this exact node.
	//
	// An alias is a spelling and never a second behavior. Dispatch normalizes it
	// to Name before the handler runs, so both spellings share one flag set, one
	// output-catalog lookup, one cardinality cell, and one error vocabulary --
	// which is what makes the two byte-identical rather than merely similar.
	//
	// The resource verbs use this to accept the singular and the plural of every
	// kind they implement, because `get panes` next to `describe pane` made the
	// operator memorize a form per verb for no gain. The one spelling that is
	// deliberately absent is `get pane`: it is not the singular of `get panes`
	// but a separate exact-one read that owns the `--current -o cwd` projection,
	// so it stays its own canonical child rather than becoming an alias of the
	// list.
	Aliases []string
	// Summary is the one-line description. For top-level visible routes this
	// string is byte-identical to the historical `printUsage` column so root
	// help stays stable while its source of truth moves into this manifest.
	Summary string
	// Disposition is the primary classification. Only top-level routes set it.
	Disposition Disposition
	// Hidden keeps a route out of the primary help listing. The internal
	// namespace invoked from generated popup/key payloads is hidden.
	Hidden bool
	// Invocation is this node's selectorless authority class. Every node must
	// set it directly; empty is always a completeness failure.
	Invocation InvocationAuthority
	// ProviderShortcut marks a `create <provider>` node. The contract keeps
	// provider shortcuts out of the resource-kind listing, so the shared help
	// renderer groups these separately and no reference or telemetry surface
	// counts a provider as a resource kind.
	ProviderShortcut bool
	// Namespace marks a child that groups sub-routes instead of naming a
	// resource kind of its parent verb.
	//
	// It exists for `get runtime`, whose children are tmux object kinds rather
	// than Projmux resource kinds. Without it the kind-parity contracts would
	// read `runtime` as a fifth kind of the read family and demand a `runtimes`
	// alias, a singular `runtime` read, and a Registry projection catalog for
	// objects that have no Registry identity at all -- which is exactly the
	// merge between runtime inventory and managed resources this surface exists
	// to keep apart.
	Namespace bool
	// Usage holds representative synopsis lines (not an exhaustive flag list).
	Usage []string
	// Canonical lists the canonical route spellings this executable node reaches.
	// The node whose own path appears in this list owns that canonical command;
	// other entries are explicit source aliases. CanonicalRoutes, help hints, and
	// the source audit are all projections of these edges.
	Canonical []string
	// CanonicalOrder orders canonical command families independently of public
	// root-help order. Only top-level canonical owners set it.
	CanonicalOrder int
	// CanonicalNodeOrder overrides depth-first order inside one family without
	// changing public help order. Zero keeps graph order.
	CanonicalNodeOrder int
	// CanonicalSummary overrides Summary only when the canonical contract and
	// the current help sentence intentionally differ. Empty means Summary.
	CanonicalSummary string
	// AcceptedOutputs overrides Outputs only when parser acceptance is wider
	// than help advertising (notably pane-id on Registry reads). Nil means
	// Outputs. Both values stay on this one command node.
	AcceptedOutputs []OutputMode
	// CanonicalSelfFirst preserves source ordering for the few canonical routes
	// whose own namespace historically precedes a compatibility source.
	CanonicalSelfFirst bool
	// Outputs pins the route-local shared output modes where the contract
	// fixes them for this node today.
	Outputs []OutputMode
	// Fields pins route-local field projections such as the Pane-read `cwd`.
	Fields []FieldProjection
	// Children is the current sub-route tree, in help display order.
	Children []Route
}

// InvocationCensusRow is one mechanically projected command or root-parser
// entrypoint in the selectorless-authority census. Catalog routes use their
// exact graph path; parser bridges use a stable angle-bracketed family name.
type InvocationCensusRow struct {
	Spelling  string
	Authority InvocationAuthority
	Catalog   bool
}

// InvocationCensus projects every graph node and the few public root parser
// bridges that exist outside the graph. The bridge rows come from the same
// token/name lists consumed by the parser, so adding an ad-hoc spelling cannot
// bypass this inventory through a separately maintained semantic table.
func InvocationCensus() []InvocationCensusRow {
	var out []InvocationCensusRow
	walkInvocationGraph(Routes(), nil, func(path []string, route Route) {
		out = append(out, InvocationCensusRow{
			Spelling: strings.Join(path, " "), Authority: route.Invocation, Catalog: true,
		})
	})
	out = append(out, rootInvocationBridgeRows()...)
	return out
}

func walkInvocationGraph(nodes []Route, prefix []string, visit func([]string, Route)) {
	for _, node := range nodes {
		path := append(append([]string{}, prefix...), node.Name)
		visit(path, node)
		walkInvocationGraph(node.Children, path, visit)
	}
}

// readProjectionCatalog is the shared `-o` catalog minus `pane-id`, and it is
// what the registry read routes advertise.
//
// A `%N` pane id is a live transport binding rather than stored metadata, so
// the read path answers `-o pane-id` with "needs a live transport binding,
// which is not wired yet" and exits 1. The parser still accepts the token --
// `ResolveOutputToken` consults the graph's canonical projection, whose Outputs list is
// parser input rather than advertising -- but a route may not advertise a
// projection whose only outcome today is an error. The create routes, where the
// projection does work, keep the full catalog.
//
// Splitting the two is what keeps the error classification stable. Dropping
// `pane-id` from the accepted graph projection would make the token malformed rather than
// unimplemented and move `get panes -o pane-id` from exit 1 to exit 2, which is
// a behavior change; narrowing only what the route lists changes nothing a
// caller can observe except the help text.
var readProjectionCatalog = []OutputMode{
	OutputModeUID,
	OutputModeName,
	OutputModeRef,
	OutputModeMetadata,
	OutputModeJSON,
	OutputModeNone,
}

// runtimeProjectionCatalog is the `-o` catalog of the runtime read route.
//
// It is deliberately two modes wide. `uid`, `name`, and `ref` are Registry
// projections of a Projmux resource, and most of what a runtime read returns is
// not one: an operator's own pane has no uid to print, no `metadata.name`, and
// no `kind/name` reference. Advertising those modes here would promise a
// projection that is empty for the majority of rows, which is worse than not
// offering it. The whole runtime object -- its exact handle, its attribution,
// and the reason for it -- is in the JSON document.
var runtimeProjectionCatalog = []OutputMode{
	OutputModeJSON,
	OutputModeNone,
}

// routes is the maintained manifest of the current CLI surface: the canonical
// and Shortcut top-level nodes plus the hidden internal plumbing namespace.
// Top-level order is the historical primary help order and is load bearing for
// root help byte-identity.
//
// Hidden is not removal: the `internal` namespace remains live. Retired roots
// are absent entirely so every old spelling follows the ordinary unknown-root
// contract instead of retaining a second dispatch surface.
var routes = []Route{
	{
		// The Agent domain namespace. An Agent is a Window-owned workload with a
		// provider conversation and an Offline/resumable life of its own, so its
		// workflow verbs sit in a noun-first domain; the CRUD half of the Agent
		// surface stays with the shared verbs (`create`, `get`, `describe`,
		// `delete agent`).
		//
		// `integrate` and `usage` remain compatibility aliases. Semantic status
		// and topic are Registry-owned Agent workflows with exact-one resolution;
		// the compatibility `ai` routes forward their managed-Pane mutations into
		// the same authority. `agent usage` forwards to the existing usage
		// handler unchanged: provider account quota is a read-only Agent-domain
		// workflow, not an addressable `usage` resource.
		//
		// The namespace is not read-only as a whole -- `status set`, `topic
		// set|clear`, and `integrate` all write -- so the summary says "manage"
		// rather than "read".
		//
		// `resume` is the one route with logic of its own: it resolves exactly
		// one existing Agent, applies the phase gate, and rebinds it to a new
		// managed Pane launched with the provider's resume argv.
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "agent",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 13,
		Summary:        "Manage Agent state, topic, integrations, and account usage",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux agent status [get [<agent-ref>] | set <unknown|idle|in_progress|approval_required|input_required|response_complete> [<agent-ref>]] [--agent <ref>]",
			"projmux agent topic get|clear [<agent-ref>] [--agent <ref>]",
			"projmux agent topic set <text> [<agent-ref>] [--agent <ref>]",
			"projmux agent resume <ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...",
			"projmux agent turn start|steer <agent-ref> -- <text>",
			"projmux agent turn interrupt <agent-ref>",
			"projmux agent approval review <agent-ref> [--request <normalized-id>]",
			"projmux agent review [<agent-ref>] [--agent <ref>] [--base <branch> | --commit <sha> | --instructions <text>]",
			"projmux agent integrate <provider> [--dry-run]",
			"projmux agent usage [--model <name>] [--window <name>] [--json] [--force]",
			"projmux agent app-server upgrade plan|apply --request <absolute-json>",
			"projmux agent app-server upgrade resume|abort --operation <ref>",
			"projmux agent app-server handover plan|apply --request <absolute-json>",
			"projmux agent app-server handover resume|abort --operation <ref>",
		},
		Canonical: []string{"agent status", "agent topic", "agent resume", "agent turn start", "agent turn steer", "agent turn interrupt", "agent approval review", "agent review", "agent integrate", "agent usage", "agent app-server upgrade plan", "agent app-server upgrade apply", "agent app-server upgrade resume", "agent app-server upgrade abort", "agent app-server handover plan", "agent app-server handover apply", "agent app-server handover resume", "agent app-server handover abort"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityExactOne), Name: "status", Invocation: InvocationNatural, Summary: "Read or set semantic Agent interaction independently of lifecycle", CanonicalSummary: "Read or set Agent status state", Usage: []string{"projmux agent status [get [<agent-ref>] | set <unknown|idle|in_progress|approval_required|input_required|response_complete> [<agent-ref>]] [--agent <ref>]"}, Canonical: []string{"agent status"}},
			{Effects: unchangedEffects(CardinalityExactOne), Name: "topic", Invocation: InvocationNatural, Summary: "Read, set, or clear one exact Agent topic annotation", CanonicalSummary: "Read, set, or clear the Agent topic annotation", Usage: []string{"projmux agent topic get|clear [<agent-ref>] [--agent <ref>]", "projmux agent topic set <text> [<agent-ref>] [--agent <ref>]"}, Canonical: []string{"agent topic"}},
			{
				// This route resolves exactly one existing Agent, refuses a
				// Running one, and rebinds an Offline or Failed one to a new
				// managed Pane built from the conversation its `status.sessionRef`
				// records. The help and canonical graph views now state
				// the same sentence, because the route does what the contract
				// asked for.
				Effects:          resumeAgentEffects(),
				Name:             "resume",
				Invocation:       InvocationExplicit,
				Summary:          "Rebind an Offline or Failed Agent detached on its Window's exact shell or Agent anchor",
				CanonicalSummary: "Rebind an Offline or Failed Agent to a new managed Pane",
				Usage:            []string{"projmux agent resume <ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]..."},
				Canonical:        []string{"agent resume"},
			},
			{
				Effects:    unchangedEffects(CardinalityExactOne),
				Name:       "turn",
				Invocation: InvocationExplicit,
				Summary:    "Send, steer, or interrupt one exact native Codex turn",
				Usage:      []string{"projmux agent turn start|steer <agent-ref> -- <text>", "projmux agent turn interrupt <agent-ref>"},
				Canonical:  []string{"agent turn start", "agent turn steer", "agent turn interrupt"},
				Children: []Route{
					{Effects: unchangedEffects(CardinalityExactOne), Name: "start", Invocation: InvocationExplicit, Summary: "Send a new turn to one exact idle Codex thread", Usage: []string{"projmux agent turn start <agent-ref> -- <text>"}, Canonical: []string{"agent turn start"}},
					{Effects: unchangedEffects(CardinalityExactOne), Name: "steer", Invocation: InvocationExplicit, Summary: "Steer one exact current Codex turn", Usage: []string{"projmux agent turn steer <agent-ref> -- <text>"}, Canonical: []string{"agent turn steer"}},
					{Effects: unchangedEffects(CardinalityExactOne), Name: "interrupt", Invocation: InvocationExplicit, Summary: "Interrupt one exact current Codex turn", Usage: []string{"projmux agent turn interrupt <agent-ref>"}, Canonical: []string{"agent turn interrupt"}},
				},
			},
			{
				Effects:    unchangedEffects(CardinalityUnchanged),
				Name:       "approval",
				Invocation: InvocationExplicit,
				Summary:    "Review one exact pending native Codex approval",
				Usage:      []string{"projmux agent approval review <agent-ref> [--request <normalized-id>]"},
				Canonical:  []string{"agent approval review"},
				Children:   []Route{{Effects: unchangedEffects(CardinalityUnchanged), Name: "review", Invocation: InvocationExplicit, Summary: "Review one exact pending native Codex approval", Usage: []string{"projmux agent approval review <agent-ref> [--request <normalized-id>]"}, Canonical: []string{"agent approval review"}}},
			},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "review", Invocation: InvocationNatural, Summary: "Start a native review on an exact-bound Codex Agent", Usage: []string{"projmux agent review [<agent-ref>] [--agent <ref>] [--base <branch> | --commit <sha> | --instructions <text>]"}, Canonical: []string{"agent review"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "integrate", Invocation: InvocationExplicit, Summary: "Install or remove provider hook integrations", Usage: []string{"projmux agent integrate <provider> [--dry-run]"}, Canonical: []string{"agent integrate"}},
			{
				Effects:    unchangedEffects(CardinalityUnchanged),
				Name:       "usage",
				Invocation: InvocationFanOut,
				Summary:    "Read provider account usage quota snapshots",
				Usage:      []string{"projmux agent usage [--model <name>] [--window <name>] [--json] [--force]"},
				Canonical:  []string{"agent usage"},
			},
			{
				Effects:    unchangedEffects(CardinalityUnchanged),
				Name:       "app-server",
				Invocation: InvocationRefusal,
				Summary:    "Manage explicitly requested private Codex app-server generation operations",
				Canonical:  []string{"agent app-server upgrade plan", "agent app-server upgrade apply", "agent app-server upgrade resume", "agent app-server upgrade abort", "agent app-server handover plan", "agent app-server handover apply", "agent app-server handover resume", "agent app-server handover abort"},
				Children: []Route{{
					Effects:    unchangedEffects(CardinalityUnchanged),
					Name:       "upgrade",
					Invocation: InvocationRefusal,
					Summary:    "Plan, apply, resume, or abort one exact rolling generation operation",
					Canonical:  []string{"agent app-server upgrade plan", "agent app-server upgrade apply", "agent app-server upgrade resume", "agent app-server upgrade abort"},
					Children: []Route{
						{Effects: unchangedEffects(CardinalityUnchanged), Name: "plan", Invocation: InvocationExplicit, Summary: "Read the mutation-zero plan for one exact private generation upgrade", Usage: []string{"projmux agent app-server upgrade plan --request <absolute-json>"}, Canonical: []string{"agent app-server upgrade plan"}},
						{Effects: unchangedEffects(CardinalityUnchanged), Name: "apply", Invocation: InvocationExplicit, Summary: "Apply one exact crash-resumable private generation admission switch", Usage: []string{"projmux agent app-server upgrade apply --request <absolute-json>"}, Canonical: []string{"agent app-server upgrade apply"}},
						{Effects: unchangedEffects(CardinalityUnchanged), Name: "resume", Invocation: InvocationExplicit, Summary: "Resume one exact durable rolling generation operation", Usage: []string{"projmux agent app-server upgrade resume --operation <ref>"}, Canonical: []string{"agent app-server upgrade resume"}},
						{Effects: unchangedEffects(CardinalityUnchanged), Name: "abort", Invocation: InvocationExplicit, Summary: "Abort one pre-admission operation and clean only its exact candidate", Usage: []string{"projmux agent app-server upgrade abort --operation <ref>"}, Canonical: []string{"agent app-server upgrade abort"}},
					},
				}, {
					Effects:    unchangedEffects(CardinalityUnchanged),
					Name:       "handover",
					Invocation: InvocationRefusal,
					Summary:    "Plan, apply, resume, or abort one exact generation-wide handover",
					Canonical:  []string{"agent app-server handover plan", "agent app-server handover apply", "agent app-server handover resume", "agent app-server handover abort"},
					Children: []Route{
						{Effects: unchangedEffects(CardinalityUnchanged), Name: "plan", Invocation: InvocationExplicit, Summary: "Read the exact target-set generation handover plan", Usage: []string{"projmux agent app-server handover plan --request <absolute-json>"}, Canonical: []string{"agent app-server handover plan"}},
						{Effects: unchangedEffects(CardinalityUnchanged), Name: "apply", Invocation: InvocationExplicit, Summary: "Apply one crash-resumable generation-wide handover", Usage: []string{"projmux agent app-server handover apply --request <absolute-json>"}, Canonical: []string{"agent app-server handover apply"}},
						{Effects: unchangedEffects(CardinalityUnchanged), Name: "resume", Invocation: InvocationExplicit, Summary: "Resume one exact durable generation handover", Usage: []string{"projmux agent app-server handover resume --operation <ref>"}, Canonical: []string{"agent app-server handover resume"}},
						{Effects: unchangedEffects(CardinalityUnchanged), Name: "abort", Invocation: InvocationExplicit, Summary: "Abort one exact pre-stop generation handover", Usage: []string{"projmux agent app-server handover abort --operation <ref>"}, Canonical: []string{"agent app-server handover abort"}},
					},
				}},
			},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "attention",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 14,
		Summary:        "View and manage live tmux pane attention state",
		Disposition:    DispositionCanonical,
		Usage:          []string{"projmux attention toggle|clear|arm|list|window"},
		Canonical:      []string{"attention list", "attention toggle", "attention clear", "attention arm", "attention window"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityExactOne), Name: "toggle", Invocation: InvocationNatural, Summary: "Toggle attention state for a pane", CanonicalSummary: "Toggle live Pane attention state", CanonicalNodeOrder: 2, Usage: []string{"projmux attention toggle [pane]"}, Canonical: []string{"attention toggle"}},
			{Effects: unchangedEffects(CardinalityExactOne), Name: "clear", Invocation: InvocationNatural, Summary: "Clear attention state for a pane", CanonicalSummary: "Clear live Pane attention state", CanonicalNodeOrder: 3, Usage: []string{"projmux attention clear [pane]"}, Canonical: []string{"attention clear"}},
			{Effects: unchangedEffects(CardinalityExactOne), Name: "arm", Invocation: InvocationNatural, Summary: "Arm focus-only attention consumption", CanonicalNodeOrder: 4, Usage: []string{"projmux attention arm [pane]"}, Canonical: []string{"attention arm"}},
			{Effects: unchangedEffects(CardinalityZeroOrMore), Name: "list", Invocation: InvocationNatural, Summary: "List live pane attention state", CanonicalSummary: "List live Pane attention state", CanonicalNodeOrder: 1, Canonical: []string{"attention list"}},
			{Effects: unchangedEffects(CardinalityZeroOrMore), Name: "window", Invocation: InvocationNatural, Summary: "Render window-scoped attention badges", CanonicalNodeOrder: 5, Canonical: []string{"attention window"}},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "attach",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 4,
		Summary:        "Enter a Project runtime from outside tmux",
		Disposition:    DispositionCanonical,
		Usage:          []string{"projmux attach project <ref>"},
		Canonical:      []string{"attach project"},
		Children: []Route{
			{
				Effects:          attachProjectEffects(),
				Name:             "project",
				Invocation:       InvocationExplicit,
				Summary:          "Enter a Project runtime from outside tmux, materializing it when offline",
				CanonicalSummary: "Enter a Project runtime from outside tmux",
				Usage:            []string{"projmux attach project <ref>"},
				Canonical:        []string{"attach project"},
			},
		},
	},
	{
		// The public config domain. It closes the gap the CLI information
		// architecture v2 track left open: generated tmux config was reachable
		// only through `tmux`, a route the internal isolation Phase classified as
		// plumbing and took out of the primary listing, so the two operations an
		// operator genuinely runs by hand had no public spelling at all.
		//
		// Every route is a parity alias forwarding raw argv to the tmux handler,
		// so stdout, stderr, the exit code, and the side effects are identical to
		// `tmux print-config`, `tmux print-app-config`, and `tmux apply`.
		//
		// `render` takes the artifact as a positional kind because projmux
		// generates two different tmux configs and the `tmux` route has always
		// had two printers for them. Both `print-config` and `print-app-config`
		// declare the canonical spelling `config render`, so a public `render`
		// that reached only one of them would leave the other with no public door
		// at all -- the same gap this route exists to close. Naming the artifact
		// also keeps the node a pure forwarder: dispatch reads leading tokens and
		// passes the remainder through untouched, so `DisableFlagParsing` stays
		// on everywhere and `--bin` remains the leaf parser's business.
		//
		// `apply` takes no kind because there is one: it writes the app config and
		// reloads the live server. `install` and `install-app` deliberately get no
		// public spelling -- they are installer plumbing, reachable only through
		// the hidden `tmux` / `internal tmux` routes, which are unchanged and
		// undeprecated. The summaries below name the exact artifact each route
		// touches so the public surface cannot be read as covering them.
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "config",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 17,
		Summary:        "Edit AI split-mode settings; render or apply generated tmux configuration",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux config edit [--get|--set <mode>]",
			"projmux config render standalone|app [--bin <path>]",
			"projmux config apply [--bin <path>] [--config <path>] [--socket <name>]",
		},
		Canonical: []string{"config edit", "config render", "config apply"},
		Children: []Route{
			{
				Effects:    unchangedEffects(CardinalityUnchanged),
				Name:       "edit",
				Invocation: InvocationNatural,
				Summary:    "Edit the AI split-mode configuration",
				Usage:      []string{"projmux config edit [--get|--set <mode>]"},
				Canonical:  []string{"config edit"},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "render",
				Invocation:       InvocationExplicit,
				Summary:          "Print a generated tmux config to stdout; writes nothing",
				CanonicalSummary: "Print a generated tmux config to stdout",
				Usage: []string{
					"projmux config render standalone [--bin <path>]",
					"projmux config render app [--bin <path>]",
				},
				Canonical: []string{"config render"},
				Children: []Route{
					{
						Effects:    unchangedEffects(CardinalityUnchanged),
						Name:       "standalone",
						Invocation: InvocationExplicit,
						Summary:    "Print the snippet you source from your own tmux.conf",
						Usage:      []string{"projmux config render standalone [--bin <path>]"},
						Canonical:  []string{"config render"},
					},
					{
						Effects:    unchangedEffects(CardinalityUnchanged),
						Name:       "app",
						Invocation: InvocationExplicit,
						Summary:    "Print the config the app-owned projmux tmux server runs from",
						Usage:      []string{"projmux config render app [--bin <path>]"},
						Canonical:  []string{"config render"},
					},
				},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "apply",
				Invocation:       InvocationFanOut,
				Summary:          "Write the generated app tmux config and reload the live projmux server",
				CanonicalSummary: "Write the generated app tmux config and reload the live server",
				Usage:            []string{"projmux config apply [--bin <path>] [--config <path>] [--socket <name>]"},
				Canonical:        []string{"config apply"},
			},
		},
	},
	{
		// The create verb. An Agent split is `create agent`, and a shell split
		// is `create pane`, because a plain shell surface is a Pane and never
		// an Agent.
		//
		// Every kind is resource-backed and shares one parser. `--project` is a
		// scope flag, not a mode selector: omitted, the scope comes from the
		// active managed runtime, and there is no second product model behind
		// its absence.
		//
		// The kinds and the provider shortcuts share one handler and one schema.
		// A shortcut is a spelling of `create agent --provider <id>`, so it is
		// listed apart from the resource kinds and is never counted as one.
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "create",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 3,
		Summary:        "Create Projmux resources",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux create project --root <absolute-path> [--name <name>] [--label key=value]... [-o <mode>]",
			"projmux create window [--project <ref> | -p <ref>] [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]",
			"projmux create pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]",
			"projmux create agent --provider <provider> [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]",
			"projmux create codex [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--window <ref> | -w <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]",
			"projmux create claude|antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]",
			"projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]",
			"projmux create snapshot",
		},
		Canonical: []string{"create project", "create window", "create pane", "create agent", "create notification", "create snapshot", "create codex", "create claude", "create antigravity"},
		Children: []Route{
			{
				// The explicit Project bootstrap. It is the only route that adds a
				// Project, which is what makes a filesystem scan stop being one:
				// discovery finds candidate directories and this decides that one
				// exact path is a managed resource.
				//
				// Registration is Registry-only -- no session, window or pane is
				// materialized -- so `-o pane-id` is deliberately absent from the
				// projections it advertises.
				Effects:          createProjectEffects(),
				Name:             "project",
				Invocation:       InvocationExplicit,
				Summary:          "Register one exact filesystem path as a Registry Project; no runtime is materialized",
				CanonicalSummary: "Register one exact filesystem path as a Registry Project",
				Usage: []string{
					"projmux create project --root <absolute-path> [--name <name>] [--label key=value]... [-o <mode>]",
				},
				Outputs:   readProjectionCatalog,
				Canonical: []string{"create project"},
			},
			{
				// A Window is always created together with the initial Pane it
				// owns, and that Pane's uid is stored as the Window's
				// compatibility shell ref -- the anchor a later `create pane` splits
				// when no explicit --pane is given.
				Effects:          createResourceEffects(CardinalityExactOne),
				Name:             "window",
				Invocation:       InvocationNatural,
				Summary:          "Create a Window and its initial Pane below one Project; the runtime is materialized detached",
				CanonicalSummary: "Create a Window with its initial Pane",
				Usage: []string{
					"projmux create window [--project <ref> | -p <ref>] [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]",
				},
				Outputs:   sharedOutputModes,
				Canonical: []string{"create window"},
			},
			{
				// One resource-backed spelling. It resolves Windows from the
				// registry, anchors on an explicit Pane or each Window's exact
				// role-agnostic anchorPaneRef, splits
				// detached, and never moves the client. With no scope flag at
				// all the Project, the Window, and the anchor come from the
				// active managed runtime, so the split lands where the operator
				// is looking.
				Effects:          createResourceEffects(CardinalityOneOrMore),
				Name:             "pane",
				Invocation:       InvocationNatural,
				Summary:          "Create a shell Pane detached on an explicit Pane or the Window's exact shell or Agent anchor",
				CanonicalSummary: "Create a shell Pane below a Window",
				Usage: []string{
					"projmux create pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				Outputs:   sharedOutputModes,
				Canonical: []string{"create pane"},
			},
			{
				// One resource-backed spelling, sharing `create pane`'s scope
				// rule. It allocates a Window-owned Agent plus its managed Pane,
				// splits the resolved Windows detached, and never moves the
				// client.
				Effects:          createResourceEffects(CardinalityOneOrMore),
				Name:             "agent",
				Invocation:       InvocationNatural,
				Summary:          "Create an Agent detached on an explicit Pane or the Window's exact shell or Agent anchor; --provider is required",
				CanonicalSummary: "Create an Agent and its managed Pane",
				Usage: []string{
					"projmux create agent --provider <provider> [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				Outputs:   sharedOutputModes,
				Canonical: []string{"create agent"},
			},
			{
				Effects:    unchangedEffects(CardinalityUnchanged),
				Name:       "notification",
				Invocation: InvocationExplicit,
				Summary:    "Create a pending notification row",
				Usage:      []string{"projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]"},
				Canonical:  []string{"create notification"},
			},
			{
				Effects:    unchangedEffects(CardinalityUnchanged),
				Name:       "snapshot",
				Invocation: InvocationNatural,
				Summary:    "Create a session snapshot",
				Usage:      []string{"projmux create snapshot"},
				Canonical:  []string{"create snapshot"},
			},
			{
				Effects:    createResourceEffects(CardinalityOneOrMore),
				Name:       "codex",
				Invocation: InvocationNatural,
				Summary:    "Provider shortcut for create agent --provider codex",
				Usage: []string{
					"projmux create codex [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--interactive-only] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				ProviderShortcut: true,
				Outputs:          sharedOutputModes,
				Canonical:        []string{"create codex"},
			},
			{
				Effects:    createResourceEffects(CardinalityOneOrMore),
				Name:       "claude",
				Invocation: InvocationNatural,
				Summary:    "Provider shortcut for create agent --provider claude",
				Usage: []string{
					"projmux create claude [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				ProviderShortcut: true,
				Outputs:          sharedOutputModes,
				Canonical:        []string{"create claude"},
			},
			{
				Effects:    createResourceEffects(CardinalityOneOrMore),
				Name:       "antigravity",
				Invocation: InvocationNatural,
				Summary:    "Provider shortcut for create agent --provider antigravity",
				Usage: []string{
					"projmux create antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				ProviderShortcut: true,
				Outputs:          sharedOutputModes,
				Canonical:        []string{"create antigravity"},
			},
		},
	},
	{
		// The registry-backed kinds own the cascade planner; `notification` and
		// `snapshot` forward raw argv to the handlers that already own them.
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "delete",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 9,
		Summary:        "Delete Projmux resources with an explicit cascade plan",
		Disposition:    DispositionCanonical,
		// The `<ref>` is bracketed for the same reason it is on `describe`:
		// omitting the whole selector is a meaningful invocation inside tmux,
		// where it addresses the active target. It is spelled `[<ref>...]`
		// rather than `<ref>...` and paired with `[--all]` because the two
		// bracketed forms are not interchangeable here -- an omitted selector
		// deletes exactly one resource, and the whole-registry fan-out has to be
		// asked for by name.
		//
		// Every summary below says "in the registry" rather than a bare "all",
		// deliberately and permanently. `--all` is the all-within-current-scope
		// spelling, and projmux has exactly one scope today: the registry. If a
		// later change ever gives the CLI a narrower default scope, "all" would
		// start reading as "all within that scope" while this route kept
		// deleting registry-wide, and that drift would be silent on a
		// destructive verb. Naming the scope in the string it prints is what
		// makes the drift loud instead.
		Usage: []string{
			"projmux delete project [<ref>...] [--selector key=value]... [--all] [--dry-run] [--yes]",
			"projmux delete window [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]",
			"projmux delete pane [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]",
			"projmux delete agent [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]",
		},
		Canonical: []string{"delete project", "delete window", "delete pane", "delete agent", "delete notification", "delete snapshot"},
		Children: []Route{
			{
				Effects:          deleteProjectEffects(),
				Name:             "project",
				Invocation:       InvocationNatural,
				Summary:          "Explicitly unregister Projects and Registry descendants while preserving roots, Git/worktrees, snapshots, and runtime",
				CanonicalSummary: "Unregister a Project and its Registry graph while preserving runtime and external assets",
				Aliases:          []string{"projects"},
				Usage:            []string{"projmux delete project [<ref>...] [--selector key=value]... [--all] [--dry-run] [--yes]"},
				Canonical:        []string{"delete project"},
			},
			{
				Effects:          deleteChildEffects(),
				Name:             "window",
				Invocation:       InvocationNatural,
				Summary:          "Delete Registry Windows and every descendant Agent and Pane, killing an exact live tmux mirror when present; no selector inside tmux means the active Window, and --all means every Window in the registry",
				CanonicalSummary: "Delete a Window and its descendants",
				Aliases:          []string{"windows"},
				Usage:            []string{"projmux delete window [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]"},
				Canonical:        []string{"delete window"},
			},
			{
				Effects:          deleteChildEffects(),
				Name:             "pane",
				Invocation:       InvocationNatural,
				Summary:          "Delete Panes; an Agent-owned current Pane leaves its Agent Offline; no selector inside tmux means the active Pane, and --all means every Pane in the registry",
				CanonicalSummary: "Delete a Pane resource and its live binding",
				Aliases:          []string{"panes"},
				Usage:            []string{"projmux delete pane [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]"},
				Canonical:        []string{"delete pane"},
			},
			{
				Effects:          deleteChildEffects(),
				Name:             "agent",
				Invocation:       InvocationNatural,
				Summary:          "Delete Agents and their managed Panes; no selector inside tmux means the active Agent, and --all means every Agent in the registry",
				CanonicalSummary: "Delete an Agent and its managed Panes",
				Aliases:          []string{"agents"},
				Usage:            []string{"projmux delete agent [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]"},
				Canonical:        []string{"delete agent"},
			},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "notification", Invocation: InvocationExplicit, Summary: "Delete pending notification rows", Aliases: []string{"notifications"}, Canonical: []string{"delete notification"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "snapshot", Invocation: InvocationExplicit, Summary: "Delete saved session snapshots", Aliases: []string{"snapshots"}, Canonical: []string{"delete snapshot"}},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "describe",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 2,
		Summary:        "Describe one Projmux resource",
		Disposition:    DispositionCanonical,
		// The `<ref>` is bracketed because omitting the whole selector is a
		// meaningful invocation inside tmux, where it addresses the active
		// target. Outside tmux it stays the ambiguity error it always was, so
		// the per-kind summaries say "inside tmux" rather than implying the
		// reference is optional everywhere.
		Usage: []string{
			"projmux describe project [<ref>] [--project <ref> | -p <ref>] [-o <mode>]",
			"projmux describe window [<ref>] [--project <ref> | -p <ref>] [-o <mode>]",
			"projmux describe pane [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]",
			"projmux describe agent [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]",
		},
		Canonical: []string{"describe project", "describe window", "describe pane", "describe agent"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityExactOne), Name: "project", Invocation: InvocationNatural, Summary: "Describe one Project resource; with no selector inside tmux, the active Project", CanonicalSummary: "Describe one Project resource", Aliases: []string{"projects"}, Usage: []string{"projmux describe project [<ref>] [--project <ref> | -p <ref>] [-o <mode>]"}, Canonical: []string{"describe project"}, Outputs: readProjectionCatalog, AcceptedOutputs: sharedOutputModes},
			{Effects: unchangedEffects(CardinalityExactOne), Name: "window", Invocation: InvocationNatural, Summary: "Describe one Window resource; inside tmux a reference resolves within the active Project and no selector means the active Window", CanonicalSummary: "Describe one Window resource", Aliases: []string{"windows"}, Usage: []string{"projmux describe window [<ref>] [--project <ref> | -p <ref>] [-o <mode>]"}, Canonical: []string{"describe window"}, Outputs: readProjectionCatalog, AcceptedOutputs: sharedOutputModes},
			{Effects: unchangedEffects(CardinalityExactOne), Name: "pane", Invocation: InvocationNatural, Summary: "Describe one Pane resource; inside tmux a reference resolves within the active Project and no selector means the active Pane", CanonicalSummary: "Describe one Pane resource", Aliases: []string{"panes"}, Usage: []string{"projmux describe pane [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]"}, Canonical: []string{"describe pane"}, Outputs: readProjectionCatalog, AcceptedOutputs: sharedOutputModes},
			{Effects: unchangedEffects(CardinalityExactOne), Name: "agent", Invocation: InvocationNatural, Summary: "Describe one Agent resource; inside tmux a reference resolves within the active Project and no selector means the Agent owning the active Pane", CanonicalSummary: "Describe one Agent resource", Aliases: []string{"agents"}, Usage: []string{"projmux describe agent [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]"}, Canonical: []string{"describe agent"}, Outputs: readProjectionCatalog, AcceptedOutputs: sharedOutputModes},
		},
	},
	{
		Effects:     unchangedEffects(CardinalityUnchanged),
		Name:        "doctor",
		Invocation:  InvocationFanOut,
		Summary:     "Run read-only runtime and integration diagnostics",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux doctor [--json] [--section <name>] [--verbose]"},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "diagnostics",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 19,
		Summary:        "Read operational events or create an explicit local support report",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux diagnostics log [--json] [--tail <n>]",
			"projmux diagnostics agent-hook [--tail <n>] [--json] [--path]",
			"projmux diagnostics report [--output <path>]",
		},
		Canonical: []string{"diagnostics log", "diagnostics agent-hook", "diagnostics report"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "log", Invocation: InvocationFanOut, Summary: "Read the bounded local operations journal", Canonical: []string{"diagnostics log"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "agent-hook", Invocation: InvocationFanOut, Summary: "Read the bounded Agent hook ingest journal", Usage: []string{"projmux diagnostics agent-hook [--tail <n>] [--json] [--path]"}, Canonical: []string{"diagnostics agent-hook"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "report", Invocation: InvocationFanOut, Summary: "Create an explicit redacted local support report", Canonical: []string{"diagnostics report"}},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "focus",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 5,
		Summary:        "Move the current client to a live resource",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux focus project <ref>",
			"projmux focus window <ref> {--project <ref> | -p <ref>}",
			"projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>}",
		},
		Canonical: []string{"focus project", "focus window", "focus pane"},
		Children: []Route{
			{
				Effects:            focusResourceEffects(),
				Name:               "project",
				Invocation:         InvocationExplicit,
				Summary:            "Move the current client to an already-live Project; never materializes",
				CanonicalSummary:   "Move the current client to a live Project",
				CanonicalSelfFirst: true,
				Usage:              []string{"projmux focus project <ref> [--socket <path>] [--client <tty>] [--json]"},
				Canonical:          []string{"focus project"},
			},
			{
				Effects:            focusResourceEffects(),
				Name:               "window",
				Invocation:         InvocationExplicit,
				Summary:            "Move the current client to an already-live Window in an exact live root session; never materializes",
				CanonicalSummary:   "Move the current client to a live Window",
				CanonicalSelfFirst: true,
				Usage:              []string{"projmux focus window <ref> {--project <ref> | -p <ref>} [--socket <path>] [--client <tty>] [--json]"},
				Canonical:          []string{"focus window"},
			},
			{
				Effects:          focusResourceEffects(),
				Name:             "pane",
				Invocation:       InvocationExplicit,
				Summary:          "Move the current client to an already-live Pane in an exact live root session; never materializes",
				CanonicalSummary: "Move the current client to a live Pane",
				Usage:            []string{"projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>} [--socket <path>] [--json]"},
				Canonical:        []string{"focus pane"},
			},
		},
	},
	{
		// The plural kinds are 0..N reads over the resource registry; the
		// singular `pane` is the exact-one read that also owns the `cwd` field
		// projection. `notifications` and `snapshots` forward raw argv to the
		// handlers that already own them.
		//
		// Every plural kind here accepts its singular as an alias except
		// `panes`. `pane` is already taken by the exact-one read below, and that
		// read is not the singular of this list: it resolves one Pane resource,
		// it owns `--current -o cwd`, and it is what `projmux current` maps onto.
		// Aliasing `pane` onto `panes` would delete a shipped route's meaning, so
		// the two stay separate canonical children and the asymmetry is
		// deliberate rather than an omission.
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "get",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 1,
		Summary:        "Read Projmux resources by selector",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux get projects [--project <ref> | -p <ref>] [--selector key=value]... [-o <mode>]",
			"projmux get windows [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]",
			"projmux get panes [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]",
			"projmux get agents [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]",
			"projmux get pane --current -o cwd",
			"projmux get pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]",
			"projmux get runtime sessions|windows|panes [--socket <name> | --socket-path <absolute>] [-o json|none]",
		},
		Canonical: []string{"get projects", "get windows", "get panes", "get agents",
			"get runtime sessions", "get runtime windows", "get runtime panes",
			"get notifications", "get snapshots", "get pane"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityZeroOrMore), Name: "projects", Invocation: InvocationFanOut, Summary: "List Project resources", Aliases: []string{"project"}, Usage: []string{"projmux get projects [--project <ref> | -p <ref>] [--selector key=value]... [-o <mode>]"}, Canonical: []string{"get projects"}, Outputs: readProjectionCatalog, AcceptedOutputs: sharedOutputModes},
			{
				Effects: unchangedEffects(CardinalityZeroOrMore),
				Name:    "windows", Summary: "List Window resources; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry",
				Invocation:       InvocationNatural,
				CanonicalSummary: "List Window resources",
				Aliases:          []string{"window"}, Usage: []string{"projmux get windows [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]"},
				Canonical: []string{"get windows"}, Outputs: readProjectionCatalog, AcceptedOutputs: sharedOutputModes,
			},
			{
				Effects: unchangedEffects(CardinalityZeroOrMore),
				Name:    "panes", Summary: "List Pane resources; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry",
				Invocation:       InvocationNatural,
				CanonicalSummary: "List Pane resources",
				Usage:            []string{"projmux get panes [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]"},
				Canonical:        []string{"get panes"}, Outputs: readProjectionCatalog, AcceptedOutputs: sharedOutputModes,
			},
			{
				Effects: unchangedEffects(CardinalityZeroOrMore),
				Name:    "agents", Summary: "List Agent resources; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry",
				Invocation:       InvocationNatural,
				CanonicalSummary: "List Agent resources",
				Aliases:          []string{"agent"}, Usage: []string{"projmux get agents [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]"},
				Canonical: []string{"get agents"}, Outputs: readProjectionCatalog, AcceptedOutputs: sharedOutputModes,
			},
			{
				// The Runtime diagnostics escape hatch. It is a child of `get`
				// rather than of the `runtime` domain because it is a read with a
				// projection, and every other `get` child is too; the `runtime`
				// domain owns the surfaces that act on the inventory.
				//
				// Its kinds are tmux object kinds, not resource kinds. A tmux
				// session is not a Projmux resource, a window here is any window
				// on the server rather than a Registry Window, and neither
				// accepts a selector -- most of what this route reports has no
				// name to resolve. The plural spellings stand alone with no
				// singular alias for the same reason `get pane` is not the
				// singular of `get panes`: an exact-one runtime read would have
				// to resolve an identity these objects do not have.
				Effects:    unchangedEffects(CardinalityZeroOrMore),
				Name:       "runtime",
				Invocation: InvocationFanOut,
				Namespace:  true,
				Summary:    "List every tmux Session, Window, and Pane on one exact server with its attribution",
				Usage: []string{
					"projmux get runtime sessions [--socket <name> | --socket-path <absolute>] [-o json|none]",
					"projmux get runtime windows [--socket <name> | --socket-path <absolute>] [-o json|none]",
					"projmux get runtime panes [--socket <name> | --socket-path <absolute>] [-o json|none]",
				},
				Canonical: []string{"get runtime sessions", "get runtime windows", "get runtime panes"},
				Children: []Route{
					{
						Effects: unchangedEffects(CardinalityZeroOrMore),
						Name:    "sessions", Invocation: InvocationFanOut, Summary: "List every tmux session on one exact server with its attribution",
						Usage:     []string{"projmux get runtime sessions [--socket <name> | --socket-path <absolute>] [-o json|none]"},
						Canonical: []string{"get runtime sessions"}, Outputs: runtimeProjectionCatalog,
					},
					{
						Effects: unchangedEffects(CardinalityZeroOrMore),
						Name:    "windows", Invocation: InvocationFanOut, Summary: "List every tmux window on one exact server with its attribution",
						Usage:     []string{"projmux get runtime windows [--socket <name> | --socket-path <absolute>] [-o json|none]"},
						Canonical: []string{"get runtime windows"}, Outputs: runtimeProjectionCatalog,
					},
					{
						Effects: unchangedEffects(CardinalityZeroOrMore),
						Name:    "panes", Invocation: InvocationFanOut, Summary: "List every tmux pane on one exact server with its attribution",
						Usage:     []string{"projmux get runtime panes [--socket <name> | --socket-path <absolute>] [-o json|none]"},
						Canonical: []string{"get runtime panes"}, Outputs: runtimeProjectionCatalog,
					},
				},
			},
			{Effects: unchangedEffects(CardinalityZeroOrMore), Name: "notifications", Invocation: InvocationFanOut, Summary: "List pending notification rows", Aliases: []string{"notification"}, Canonical: []string{"get notifications"}, AcceptedOutputs: sharedOutputModes},
			{Effects: unchangedEffects(CardinalityZeroOrMore), Name: "snapshots", Invocation: InvocationFanOut, Summary: "List saved session snapshots", Aliases: []string{"snapshot"}, Canonical: []string{"get snapshots"}, AcceptedOutputs: sharedOutputModes},
			{
				Effects:          unchangedEffects(CardinalityExactOne),
				Name:             "pane",
				Invocation:       InvocationNatural,
				Summary:          "Read one Pane resource; with no selector inside tmux, the active Pane",
				CanonicalSummary: "Read one Pane resource",
				Usage:            []string{"projmux get pane [--current] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]"},
				Canonical:        []string{"get pane"},
				Outputs:          readProjectionCatalog,
				AcceptedOutputs:  sharedOutputModes,
				Fields:           []FieldProjection{FieldProjectionCWD},
			},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "hook",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 16,
		Summary:        "List, edit, validate, and trust lifecycle hook config",
		Disposition:    DispositionCanonical,
		Usage:          []string{"projmux hook list|edit|validate|trust|untrust"},
		Canonical:      []string{"hook list", "hook edit", "hook validate", "hook trust", "hook untrust"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "list", Invocation: InvocationNatural, Summary: "List global and project lifecycle hooks", CanonicalSummary: "List lifecycle hook config", Canonical: []string{"hook list"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "edit", Invocation: InvocationNatural, Summary: "Edit lifecycle hook config", Canonical: []string{"hook edit"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "validate", Invocation: InvocationNatural, Summary: "Validate lifecycle hook config", Canonical: []string{"hook validate"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "trust", Invocation: InvocationNatural, Summary: "Trust the current project hook config", Canonical: []string{"hook trust"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "untrust", Invocation: InvocationNatural, Summary: "Revoke project hook config trust", Canonical: []string{"hook untrust"}},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "notification",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 15,
		Summary:        "Manage pending notification workflow state",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux notification ack <id> | --all",
			"projmux notification reconcile [--json]",
		},
		Canonical: []string{"notification ack", "notification reconcile"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "ack", Invocation: InvocationExplicit, Summary: "Acknowledge notification rows", Usage: []string{"projmux notification ack <id> | --all"}, Canonical: []string{"notification ack"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "reconcile", Invocation: InvocationFanOut, Summary: "Reconcile the notification queue against live targets", Usage: []string{"projmux notification reconcile [--json]"}, Canonical: []string{"notification reconcile"}},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "pin",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 11,
		Summary:        "Manage pinned project directories",
		Disposition:    DispositionCanonical,
		Usage:          []string{"projmux pin project list|add|remove|toggle|clear"},
		Canonical:      []string{"pin project"},
		Children: []Route{
			// The store behind this route is a lines file of directory paths, not
			// a Project registry: there is no uid, no ownerRef, and no resource
			// document. So the summary says "project directories" like every
			// sibling below it, rather than "Project resources", which would name
			// a resource kind the route never touches.
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "project", Invocation: InvocationExplicit, Summary: "Manage pinned project directories (canonical spelling)", CanonicalSummary: "Manage pinned project directories", Usage: []string{"projmux pin project list|add|remove|toggle|clear"}, Canonical: []string{"pin project"}},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "prune",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 12,
		Summary:        "Prune stale Projects and snapshots",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux prune snapshot [--older-than <duration>]",
			"projmux prune project --missing --older-than <duration> [--yes]",
		},
		Canonical: []string{"prune project", "prune snapshot"},
		Children: []Route{
			{
				Effects:          pruneProjectEffects(),
				Name:             "project",
				Invocation:       InvocationFanOut,
				Summary:          "Delete Projects whose spec.root has been missing for a bounded age",
				CanonicalSummary: "Prune Projects whose spec.root has been missing for a bounded age",
				Usage:            []string{"projmux prune project --missing --older-than <duration> [--yes]"},
				Canonical:        []string{"prune project"},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "snapshot",
				Invocation:       InvocationFanOut,
				Summary:          "Inspect or delete preserved session snapshots (canonical spelling)",
				CanonicalSummary: "Prune preserved session snapshots",
				Usage:            []string{"projmux prune snapshot [--older-than <duration>]", "projmux prune snapshot delete <session>..."},
				Canonical:        []string{"prune snapshot", "delete snapshot"},
			},
		},
	},
	{
		Effects:     runtimeEffectsOnly([]RuntimeEffect{RuntimeStopped}, []FocusEffect{FocusUnchanged}, CardinalityZeroOrMore),
		Name:        "quit",
		Invocation:  InvocationFanOut,
		Summary:     "Quit the app-owned projmux tmux runtime",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux quit [--yes|--force]"},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "reconcile",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 8,
		Summary:        "Preview or repair Registry and exact tmux resource drift",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--socket <name> | --socket-path <absolute>] [-o json]",
			"projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [--expect-source-checksum <sha256:hex>] [--expect-current-checksum <sha256:hex>] [--socket <name> | --socket-path <absolute>] [-o json]",
		},
		Canonical: []string{"reconcile resources", "reconcile registry"},
		Children: []Route{
			{
				Effects:          reconcileResourcesEffects(),
				Name:             "resources",
				Invocation:       InvocationFanOut,
				Summary:          "Preview or repair exact anchor-aware Registry and tmux topology on one exact socket",
				CanonicalSummary: "Preview or repair Registry and exact tmux resource drift",
				Usage:            []string{"projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--socket <name> | --socket-path <absolute>] [-o json]"},
				Canonical:        []string{"reconcile resources"},
			},
			{
				Effects:    reconcileRegistryEffects(),
				Name:       "registry",
				Invocation: InvocationExplicit,
				Summary:    "Plan Registry state-loss recovery with zero writes, then restore one explicitly named verified source",
				Usage:      []string{"projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [--expect-source-checksum <sha256:hex>] [--expect-current-checksum <sha256:hex>] [--socket <name> | --socket-path <absolute>] [-o json]"},
				Canonical:  []string{"reconcile registry"},
			},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "rebind",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 7,
		Summary:        "Rebind a Project to a new absolute root without moving files",
		Disposition:    DispositionCanonical,
		Usage:          []string{"projmux rebind project [<ref>] [--project <ref> | -p <ref>] --root <absolute-path>"},
		Canonical:      []string{"rebind project"},
		Children: []Route{
			{
				Effects:          rebindProjectEffects(),
				Name:             "project",
				Invocation:       InvocationNatural,
				Summary:          "Rewrite one Project spec.root; no filesystem move, no heuristic uid merge",
				CanonicalSummary: "Rebind one Project spec.root to a new absolute directory",
				Usage:            []string{"projmux rebind project [<ref>] [--project <ref> | -p <ref>] --root <absolute-path>"},
				Canonical:        []string{"rebind project"},
			},
		},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "rename",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 6,
		Summary:        "Rename a Projmux resource metadata.name",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux rename project [<ref>] [--project <ref> | -p <ref>] --name <name>",
			"projmux rename window [<ref>] --name <name> [--project <ref> | -p <ref>]",
			"projmux rename pane [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...",
			"projmux rename agent [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...",
		},
		Canonical: []string{"rename project", "rename window", "rename pane", "rename agent"},
		Children: []Route{
			{Effects: renameResourceEffects(), Name: "project", Invocation: InvocationNatural, Summary: "Rename a Projmux Project resource; with no selector inside tmux, the active Project", CanonicalSummary: "Rename a Projmux Project resource", Aliases: []string{"projects"}, Usage: []string{"projmux rename project [<ref>] [--project <ref> | -p <ref>] --name <name>"}, Canonical: []string{"rename project"}},
			{Effects: renameResourceEffects(), Name: "window", Invocation: InvocationNatural, Summary: "Rename a Projmux Window resource; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Window", CanonicalSummary: "Rename a Projmux Window resource", Aliases: []string{"windows"}, Usage: []string{"projmux rename window [<ref>] --name <name> [--project <ref> | -p <ref>]"}, Canonical: []string{"rename window"}},
			{Effects: renameResourceEffects(), Name: "pane", Invocation: InvocationNatural, Summary: "Rename a Projmux Pane resource; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Pane; does not change tmux pane_title", CanonicalSummary: "Rename a Projmux Pane resource; does not change tmux pane_title", Aliases: []string{"panes"}, Usage: []string{"projmux rename pane [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]..."}, Canonical: []string{"rename pane"}},
			{Effects: renameResourceEffects(), Name: "agent", Invocation: InvocationNatural, Summary: "Rename an Agent stable resource name within the active Project or ControlSession without changing its topic, provider, or managed Pane", CanonicalSummary: "Rename an Agent stable resource name only", Aliases: []string{"agents"}, Usage: []string{"projmux rename agent [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]..."}, Canonical: []string{"rename agent"}},
		},
	},
	{
		Effects:     unchangedEffects(CardinalityUnchanged),
		Name:        "resources",
		Invocation:  InvocationNatural,
		Summary:     "Inspect live Project, Window, and Pane CPU/RSS attribution",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux resources"},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "restore",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 10,
		Summary:        "Project a saved snapshot into one exact closed Project desired state",
		Disposition:    DispositionCanonical,
		Usage:          []string{"projmux restore snapshot --session <name> [--project <ref> | -p <ref>] [--dry-run | --yes] [--client <tmux-client>]"},
		Canonical:      []string{"restore snapshot"},
		Children: []Route{
			{
				Effects:    restoreSnapshotEffects(),
				Name:       "snapshot",
				Invocation: InvocationExplicit,
				Summary:    "Project a saved snapshot into one exact closed Project desired state",
				Usage:      []string{"projmux restore snapshot --session <name> [--project <ref> | -p <ref>] [--dry-run | --yes] [--client <tmux-client>]"},
				Canonical:  []string{"restore snapshot"},
			},
		},
	},
	{
		// The runtime domain owns the live and ephemeral tmux inventory, which
		// carries no uid, name reservation, or ownerRef and is therefore not a
		// Projmux resource. Every subcommand forwards raw argv to the handler
		// that already owns the behavior.
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "runtime",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 18,
		Summary:        "Manage the live and ephemeral tmux runtime inventory",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux runtime sessions [--ui=popup|sidebar]",
			"projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]",
			"projmux runtime attach [--keep=N] [--fallback=home|ephemeral]",
			"projmux runtime stop [<session>...]",
			"projmux runtime tag list|toggle|clear",
			"projmux runtime prune [--keep=N]",
		},
		Canonical: []string{"runtime sessions", "runtime diagnostics", "runtime attach", "runtime stop", "runtime tag", "runtime prune"},
		Children: []Route{
			{Effects: runtimeSessionsEffects(), Name: "sessions", Invocation: InvocationNatural, Summary: "Pick a live or ephemeral tmux session", Canonical: []string{"runtime sessions"}},
			{
				// The diagnostics escape hatch, kept separate from `runtime
				// sessions` on purpose. That picker lists recent sessions to open
				// one; this one lists every object on the server -- control,
				// ephemeral, unattributed, foreign, contradictory -- to explain
				// what they are. Merging them would put an operator's own shell
				// into the open-a-session list, which is the adoption this track
				// refuses.
				Effects:    runtimeDiagnosticsEffects(),
				Name:       "diagnostics",
				Invocation: InvocationNatural,
				Summary:    "Inspect every tmux object on one exact server, with attribution and safe actions",
				Usage:      []string{"projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]"},
				Canonical:  []string{"runtime diagnostics"},
			},
			{Effects: runtimeEffectsOnly([]RuntimeEffect{RuntimeAlreadyLive}, []FocusEffect{FocusAttachedCaller}, CardinalityExactOne), Name: "attach", Invocation: InvocationExplicit, Summary: "Attach a live or ephemeral runtime without Project identity", Canonical: []string{"runtime attach"}},
			{Effects: runtimeEffectsOnly([]RuntimeEffect{RuntimeStopped}, []FocusEffect{FocusUnchanged}, CardinalityOneOrMore), Name: "stop", Invocation: InvocationFanOut, Summary: "Terminate live tmux sessions by tagged selection", Canonical: []string{"runtime stop"}},
			{Effects: unchangedEffects(CardinalityZeroOrMore), Name: "tag", Invocation: InvocationFanOut, Summary: "Manage the ephemeral tagged session selection", Canonical: []string{"runtime tag"}},
			{Effects: runtimeEffectsOnly([]RuntimeEffect{RuntimeStopped}, []FocusEffect{FocusUnchanged}, CardinalityZeroOrMore), Name: "prune", Invocation: InvocationFanOut, Summary: "Trim old ephemeral tmux sessions", Canonical: []string{"runtime prune"}},
		},
	},
	{
		Effects:     unchangedEffects(CardinalityUnchanged),
		Name:        "settings",
		Invocation:  InvocationNatural,
		Summary:     "Configure projmux",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux settings"},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "setup",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 20,
		Summary:        "Probe terminal keys or remediate them with setup terminal",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux setup",
			"projmux setup terminal [terminal] [--apply]",
		},
		Canonical: []string{"setup terminal"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "terminal", Invocation: InvocationFanOut, Summary: "Show or apply terminal key remediation", Usage: []string{"projmux setup terminal [terminal] [--apply] [--config <path>] [--allow-symlink]"}, Canonical: []string{"setup terminal"}},
		},
	},
	{
		Effects:     shellEffects(),
		Name:        "shell",
		Invocation:  InvocationNatural,
		Summary:     "Open the isolated projmux tmux app",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux shell [--session <name>]"},
	},
	{
		Effects:     switchProjectEffects(),
		Name:        "switch",
		Invocation:  InvocationNatural,
		Summary:     "Pick and open a project tmux session",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux switch [<project>]"},
		Canonical:   []string{"focus project"},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "update",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 21,
		Summary:        "Check installer-aware release update status",
		Disposition:    DispositionCanonical,
		Usage:          []string{"projmux update status|check|apply"},
		Canonical:      []string{"update status", "update check", "update apply"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "status", Invocation: InvocationFanOut, Summary: "Show read-only update status", Canonical: []string{"update status"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "check", Invocation: InvocationFanOut, Summary: "Check for a newer release and refresh the cache", Canonical: []string{"update check"}},
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "apply", Invocation: InvocationFanOut, Summary: "Apply an available update", Canonical: []string{"update apply"}},
		},
	},
	{
		Effects:     unchangedEffects(CardinalityUnchanged),
		Name:        "welcome",
		Invocation:  InvocationNatural,
		Summary:     "Reprint the shell welcome guide",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux welcome [--popup [--force]]"},
	},
	{
		Effects:     unchangedEffects(CardinalityUnchanged),
		Name:        "window",
		Invocation:  InvocationRefusal,
		Summary:     "Open recent window navigation surfaces",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux window record|recent"},
		Canonical:   []string{"get windows", "describe window", "create window", "focus window", "rename window"},
		Children: []Route{
			{Effects: unchangedEffects(CardinalityUnchanged), Name: "record", Invocation: InvocationNatural, Summary: "Record the current window into the MRU store", Canonical: []string{"get windows"}},
			{Effects: runtimeDiagnosticsEffects(), Name: "recent", Invocation: InvocationNatural, Summary: "Open the recent-window navigation picker", Canonical: []string{"get windows"}},
		},
	},
	{
		Effects:          unchangedEffects(CardinalityUnchanged),
		Name:             "help",
		Invocation:       InvocationFanOut,
		CanonicalOrder:   22,
		Summary:          "Show bootstrap help",
		CanonicalSummary: "Show help for projmux or one route",
		Disposition:      DispositionCanonical,
		Usage: []string{
			"projmux help",
			"projmux --help",
			"projmux <route> --help",
		},
		Canonical: []string{"help"},
	},
	{
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "version",
		Invocation:     InvocationFanOut,
		CanonicalOrder: 23,
		Summary:        "Print the current version",
		Disposition:    DispositionCanonical,
		Usage: []string{
			"projmux version",
			"projmux --version",
		},
		Canonical: []string{"version"},
	},
	{
		// The hidden `internal` namespace. Everything under it is plumbing
		// invoked by generated tmux config, tmux hooks, popup payloads, or
		// provider hook commands -- never by a user typing at a prompt. Keeping
		// it in one hidden namespace is what lets the primary help listing carry
		// zero internal routes.
		//
		// Every subcommand forwards raw argv to the handler that owns the
		// behavior. Old pre-namespace spellings are absent from the public and
		// hidden route graph; generated config uses only this namespace.
		Effects:        unchangedEffects(CardinalityUnchanged),
		Name:           "internal",
		Invocation:     InvocationRefusal,
		CanonicalOrder: 24,
		Summary:        "Internal plumbing invoked by generated tmux config, hooks, and popups",
		Disposition:    DispositionInternal,
		Hidden:         true,
		Usage: []string{
			"projmux internal tmux print-config|apply|popup-toggle|rebalance-panes|autosave-session-state ...",
			"projmux internal status git|project|usage|notify|resources",
			"projmux internal statusbar click|usage-refresh ...",
			"projmux internal preview cycle-pane|cycle-window|select ...",
			"projmux internal session-popup preview|open|cycle-pane|cycle-window ...",
			"projmux internal agent-pane launch-default|picker ...",
			"projmux internal agent-hook ingest|watch-title ...",
			"projmux internal focus --target <target> ...",
			"projmux internal key-broker [--once]",
			"projmux internal popup-wait-key",
			"projmux internal supervise --pane-uid <uid> --generation <gen> [--agent-uid <uid> --operation-id <id> --registry-path <absolute>] -- <command> ...",
			"projmux internal activation-exec --pane-uid <uid> --agent-uid <uid> --generation <gen> --operation-id <id> --registry-path <absolute> -- <command> ...",
			// The Codex endpoint broker runtime is listed here but is
			// deliberately absent from the canonical command projection: it is
			// a per-state-domain service entrypoint, not a command spelling a
			// user reaches for, and the canonical graph is the surface a
			// generated reference and a release boundary are built from.
			"projmux internal codex-broker serve|probe [--state-domain <absolute>] ...",
		},
		Canonical: []string{
			"internal tmux",
			"internal status",
			"internal statusbar",
			"internal preview",
			"internal session-popup",
			"internal agent-pane",
			"internal agent-hook",
			"internal focus",
			"internal key-broker",
			"internal popup-wait-key",
			"internal supervise",
			"internal activation-exec",
		},
		Children: []Route{
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "tmux",
				Invocation:       InvocationExplicit,
				Summary:          "Generated tmux config, popup, and pane plumbing",
				CanonicalSummary: "Generated tmux config and popup plumbing",
				Usage:            []string{"projmux internal tmux print-config|print-app-config|install|install-app|apply", "projmux internal tmux popup-preview|popup-switch|popup-sessions|popup-toggle", "projmux internal tmux hook-trust-prompt|rebalance-panes|rename-pane|autosave-session-state"},
				Canonical:        []string{"internal tmux"},
				Children: []Route{
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "hook-trust-prompt", Invocation: InvocationExplicit, Summary: "Show the project hook trust prompt", Canonical: []string{"internal tmux"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "popup-preview", Invocation: InvocationExplicit, Summary: "Open the preview popup", Canonical: []string{"internal tmux"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "popup-switch", Invocation: InvocationExplicit, Summary: "Open the project switch popup", Canonical: []string{"internal tmux"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "popup-sessions", Invocation: InvocationExplicit, Summary: "Open the sessions popup", Canonical: []string{"internal tmux"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "popup-toggle", Invocation: InvocationExplicit, Summary: "Toggle a client-scoped popup surface", Canonical: []string{"internal tmux"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "rebalance-panes", Invocation: InvocationExplicit, Summary: "Rebalance panes after a pane exit", Canonical: []string{"internal tmux"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "rename-pane", Invocation: InvocationExplicit, Summary: "Run the pane rename prompt helper", Canonical: []string{"rename pane"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "print-config", Invocation: InvocationExplicit, Summary: "Print the generated tmux config", Canonical: []string{"config render"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "print-app-config", Invocation: InvocationExplicit, Summary: "Print the generated app tmux config", Canonical: []string{"config render"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "install", Invocation: InvocationExplicit, Summary: "Install the generated tmux config", Canonical: []string{"internal tmux"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "install-app", Invocation: InvocationExplicit, Summary: "Install the generated app tmux config", Canonical: []string{"internal tmux"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "apply", Invocation: InvocationExplicit, Summary: "Apply the generated tmux config to a live server", Canonical: []string{"config apply"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "autosave-session-state", Invocation: InvocationExplicit, Summary: "Run the debounced snapshot autosave hook", Canonical: []string{"internal tmux"}},
				},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "status",
				Invocation:       InvocationExplicit,
				Summary:          "Render tmux status bar segments",
				CanonicalSummary: "tmux status segment renderer",
				Usage:            []string{"projmux internal status git|project|usage|notify|resources"},
				Canonical:        []string{"internal status", "agent usage"},
				Children: []Route{
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "git", Invocation: InvocationExplicit, Summary: "Render the git status segment", Canonical: []string{"internal status"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "project", Invocation: InvocationExplicit, Summary: "Render the project status segment", Canonical: []string{"internal status"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "usage", Invocation: InvocationExplicit, Summary: "Render the AI usage status segment", Canonical: []string{"agent usage"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "notify", Invocation: InvocationExplicit, Summary: "Render the notify status segment", Canonical: []string{"internal status"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "resources", Invocation: InvocationExplicit, Summary: "Render the live resource status segment", Canonical: []string{"internal status"}},
				},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "statusbar",
				Invocation:       InvocationExplicit,
				Summary:          "Dispatch projmux status bar clicks and shortcuts",
				CanonicalSummary: "tmux status bar click and key dispatcher",
				Usage:            []string{"projmux internal statusbar click <range-id> ...", "projmux internal statusbar usage-refresh"},
				Canonical:        []string{"internal statusbar"},
				Children: []Route{
					{Effects: statusbarClickEffects(), Name: "click", Invocation: InvocationExplicit, Summary: "Dispatch a status bar click range", Canonical: []string{"internal statusbar"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "usage-refresh", Invocation: InvocationExplicit, Summary: "Refresh the AI usage snapshot", Canonical: []string{"internal statusbar"}},
				},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "preview",
				Invocation:       InvocationExplicit,
				Summary:          "Manage persisted tmux preview selection",
				CanonicalSummary: "Persisted preview cursor plumbing",
				Usage:            []string{"projmux internal preview cycle-pane|cycle-window|select"},
				Canonical:        []string{"internal preview"},
				Children: []Route{
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "cycle-pane", Invocation: InvocationExplicit, Summary: "Advance the persisted preview pane cursor", Canonical: []string{"internal preview"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "cycle-window", Invocation: InvocationExplicit, Summary: "Advance the persisted preview window cursor", Canonical: []string{"internal preview"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "select", Invocation: InvocationExplicit, Summary: "Persist the preview selection", Canonical: []string{"internal preview"}},
				},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "session-popup",
				Invocation:       InvocationExplicit,
				Summary:          "Read tmux popup preview state",
				CanonicalSummary: "Generated session popup payload",
				Usage:            []string{"projmux internal session-popup preview|open|cycle-pane|cycle-window"},
				Canonical:        []string{"internal session-popup"},
				Children: []Route{
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "preview", Invocation: InvocationExplicit, Summary: "Render the session popup preview", Canonical: []string{"internal session-popup"}},
					{Effects: openRuntimeTargetEffects(), Name: "open", Invocation: InvocationExplicit, Summary: "Open the previewed session", Canonical: []string{"internal session-popup"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "cycle-pane", Invocation: InvocationExplicit, Summary: "Advance the popup pane cursor", Canonical: []string{"internal session-popup"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "cycle-window", Invocation: InvocationExplicit, Summary: "Advance the popup window cursor", Canonical: []string{"internal session-popup"}},
				},
			},
			{
				Effects:    unchangedEffects(CardinalityUnchanged),
				Name:       "agent-pane",
				Invocation: InvocationExplicit,
				Summary:    "Generated Agent and Pane launch plumbing",
				Usage:      []string{"projmux internal agent-pane launch-default <right|down>", "projmux internal agent-pane picker [--resume] --inside <right|down>"},
				Canonical:  []string{"internal agent-pane"},
				Children: []Route{
					{Effects: agentPaneLaunchEffects(false), Name: "launch-default", Invocation: InvocationExplicit, Summary: "Launch the saved default target in a new Pane", Canonical: []string{"internal agent-pane"}},
					{Effects: agentPaneLaunchEffects(true), Name: "picker", Invocation: InvocationExplicit, Summary: "Run the Agent launch or resume picker inside its popup", Canonical: []string{"internal agent-pane"}},
				},
			},
			{
				// Provider hook plumbing is invoked by provider hook commands
				// and by the pane title watcher, never by a user, so the Agent
				// decomposition parks it here rather than in the public `agent`
				// namespace.
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "agent-hook",
				Invocation:       InvocationExplicit,
				Summary:          "Provider hook ingest and Agent pane title watcher plumbing",
				CanonicalSummary: "Provider hook ingest and title watcher plumbing",
				Usage:            []string{"projmux internal agent-hook ingest <source> ...", "projmux internal agent-hook watch-title [pane]"},
				Canonical:        []string{"internal agent-hook"},
				Children: []Route{
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "ingest", Invocation: InvocationExplicit, Summary: "Ingest provider hook and log events", Canonical: []string{"internal agent-hook"}},
					{Effects: unchangedEffects(CardinalityUnchanged), Name: "watch-title", Invocation: InvocationExplicit, Summary: "Run the Agent pane title watcher", Canonical: []string{"internal agent-hook"}},
				},
			},
			{
				Effects:    focusIngressEffects(),
				Name:       "focus",
				Invocation: InvocationExplicit,
				Summary:    "Machine focus ingress",
				Usage:      []string{"projmux internal focus --target <target> [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>]", "projmux internal focus --uri <uri>"},
				Canonical:  []string{"internal focus"},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "key-broker",
				Invocation:       InvocationExplicit,
				Summary:          "Forward captured physical key chords into the tmux root table",
				CanonicalSummary: "Darwin physical key transport",
				Usage:            []string{"projmux internal key-broker [--once]"},
				Canonical:        []string{"internal key-broker"},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "popup-wait-key",
				Invocation:       InvocationExplicit,
				Summary:          "Read a single key for a display-only tmux popup",
				CanonicalSummary: "Display-only popup single-key reader",
				Usage:            []string{"projmux internal popup-wait-key"},
				Canonical:        []string{"internal popup-wait-key"},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "supervise",
				Invocation:       InvocationExplicit,
				Summary:          "Supervise one managed Pane process and record its exit evidence",
				CanonicalSummary: "Managed Pane process supervisor and termination receipt writer",
				Usage:            []string{"projmux internal supervise --pane-uid <uid> --generation <gen> [--agent-uid <uid> --operation-id <id> --registry-path <absolute>] [--argv0 <name>] -- <command> ..."},
				Canonical:        []string{"internal supervise"},
			},
			{
				Effects:          unchangedEffects(CardinalityUnchanged),
				Name:             "activation-exec",
				Invocation:       InvocationExplicit,
				Summary:          "Admit one exact committed Agent activation before provider exec",
				CanonicalSummary: "Exact committed Agent activation admission",
				Usage:            []string{"projmux internal activation-exec --pane-uid <uid> --agent-uid <uid> --generation <gen> --operation-id <id> --registry-path <absolute> [--failure-fd <fd>] [--argv0 <name>] -- <command> ..."},
				Canonical:        []string{"internal activation-exec"},
			},
		},
	},
}

// Routes returns a deep copy of the command manifest. Callers cannot reorder or
// mutate shared graph storage, and no projection fills a missing authority.
func Routes() []Route {
	if err := validateInvocationGraph(routes, nil); err != nil {
		panic(err)
	}
	if err := validateEffectGraph(routes, nil); err != nil {
		panic(err)
	}
	return cloneRoutes(routes)
}

func validateInvocationGraph(nodes []Route, prefix []string) error {
	for _, node := range nodes {
		path := append(append([]string{}, prefix...), node.Name)
		if !slices.Contains(invocationAuthorities, node.Invocation) {
			return fmt.Errorf("route %q has missing or unknown selectorless authority %q", strings.Join(path, " "), node.Invocation)
		}
		if err := validateInvocationGraph(node.Children, path); err != nil {
			return err
		}
	}
	return nil
}

func validateEffectGraph(nodes []Route, prefix []string) error {
	for _, node := range nodes {
		path := append(append([]string{}, prefix...), node.Name)
		if err := validateAllowedEffects(strings.Join(path, " "), node.Effects); err != nil {
			return err
		}
		if err := validateEffectGraph(node.Children, path); err != nil {
			return err
		}
	}
	return nil
}

func validateAllowedEffects(route string, effects *AllowedEffects) error {
	if effects == nil {
		return fmt.Errorf("route %q has no effect record", route)
	}
	checks := []struct {
		axis string
		err  error
	}{
		{"identity", validateEffectValues(effects.Identity, identityEffects)},
		{"address", validateEffectValues(effects.Address, addressEffects)},
		{"topology", validateEffectValues(effects.Topology, topologyEffects)},
		{"desired-state", validateEffectValues(effects.DesiredState, desiredEffects)},
		{"runtime", validateEffectValues(effects.Runtime, runtimeEffects)},
		{"focus", validateEffectValues(effects.Focus, focusEffects)},
		{"cardinality", validateEffectValues(effects.Cardinality, cardinalities)},
	}
	for _, check := range checks {
		if check.err != nil {
			return fmt.Errorf("route %q has invalid %s effects: %w", route, check.axis, check.err)
		}
	}
	if effects.DomainEffect != nil && !slices.Contains(domainEffectKinds, effects.DomainEffect.Kind) {
		return fmt.Errorf("route %q has unknown domain effect %q", route, effects.DomainEffect.Kind)
	}
	return nil
}

func validateEffectValues[T comparable](values, closed []T) error {
	if len(values) == 0 {
		return fmt.Errorf("missing value")
	}
	seen := make(map[T]bool, len(values))
	for _, value := range values {
		if !slices.Contains(closed, value) {
			return fmt.Errorf("unknown value %v", value)
		}
		if seen[value] {
			return fmt.Errorf("duplicate value %v", value)
		}
		seen[value] = true
	}
	return nil
}

// RouteEffectRow is the flat, machine-readable projection of one graph row.
// Route is the canonical argv path; aliases deliberately do not create a
// second effect record because they reach the same handler.
type RouteEffectRow struct {
	Route   string
	Effects AllowedEffects
}

// EffectManifest projects the unified command graph into one effect row per
// route. It is useful to audits and future receipt consumers without creating
// a separately maintained command/effect table.
func EffectManifest() []RouteEffectRow {
	return effectManifest(Routes())
}

func effectManifest(nodes []Route) []RouteEffectRow {
	var rows []RouteEffectRow
	walkInvocationGraph(nodes, nil, func(path []string, route Route) {
		rows = append(rows, RouteEffectRow{
			Route:   strings.Join(path, " "),
			Effects: cloneAllowedEffects(route.Effects),
		})
	})
	return rows
}

func cloneRoutes(nodes []Route) []Route {
	out := make([]Route, len(nodes))
	for i, node := range nodes {
		if node.Effects != nil {
			effects := cloneAllowedEffects(node.Effects)
			node.Effects = &effects
		}
		node.Children = cloneRoutes(node.Children)
		out[i] = node
	}
	return out
}

func cloneAllowedEffects(effects *AllowedEffects) AllowedEffects {
	if effects == nil {
		return AllowedEffects{}
	}
	out := *effects
	out.Identity = slices.Clone(effects.Identity)
	out.Address = slices.Clone(effects.Address)
	out.Topology = slices.Clone(effects.Topology)
	out.DesiredState = slices.Clone(effects.DesiredState)
	out.Runtime = slices.Clone(effects.Runtime)
	out.Focus = slices.Clone(effects.Focus)
	out.Cardinality = slices.Clone(effects.Cardinality)
	if effects.DomainEffect != nil {
		domain := *effects.DomainEffect
		out.DomainEffect = &domain
	}
	return out
}

// LookupRoute returns the top-level route for token.
func LookupRoute(token string) (Route, bool) {
	for _, route := range Routes() {
		if route.Name == token {
			return route, true
		}
	}
	return Route{}, false
}

// Resolve walks tokens against the manifest and returns the path of the
// deepest matched route plus that route. It stops at the first token that does
// not match a child of the current node, so unknown trailing tokens and flags
// resolve to their nearest documented ancestor. ok is false when the first
// token is not a known top-level route.
func Resolve(tokens []string) (path []string, route Route, ok bool) {
	if len(tokens) == 0 {
		return nil, Route{}, false
	}
	current, found := LookupRoute(tokens[0])
	if !found {
		return nil, Route{}, false
	}
	path = []string{current.Name}
	for _, token := range tokens[1:] {
		child, childFound := findChild(current, token)
		if !childFound {
			break
		}
		current = child
		path = append(path, child.Name)
	}
	return path, current, true
}

// findChild resolves one child token, canonical spelling first.
//
// The two passes are ordered rather than merged so a canonical name always
// wins: `get pane` is a child in its own right and must never be reached as
// some other child's alias, whatever order the manifest happens to list them
// in. TestNoChildAliasShadowsACanonicalSpelling makes that a checked property
// instead of a comment.
func findChild(parent Route, token string) (Route, bool) {
	for _, child := range parent.Children {
		if child.Name == token {
			return child, true
		}
	}
	for _, child := range parent.Children {
		if slices.Contains(child.Aliases, token) {
			return child, true
		}
	}
	return Route{}, false
}

// CanonicalChildToken normalizes one child token of a top-level route onto the
// canonical spelling of the node it addresses.
//
// This is the single normalization point the resource verbs dispatch through.
// Returning the canonical token rather than a bool-plus-original is what makes
// an alias byte-identical to what it aliases: everything downstream -- the flag
// set name, the `-o` catalog lookup keyed by `<verb> <kind>`, every usage and
// selector message that interpolates the spelling -- is built from the returned
// token, so no per-alias string can exist to drift.
func CanonicalChildToken(parent, token string) (string, bool) {
	route, ok := LookupRoute(parent)
	if !ok {
		return "", false
	}
	child, ok := findChild(route, token)
	if !ok {
		return "", false
	}
	return child.Name, true
}

// ChildSpellings renders the accepted child tokens of a top-level route in
// manifest order, each canonical spelling followed by its aliases joined with
// `|`.
//
// The unknown-kind refusals are built from this, so a spelling the manifest
// accepts can never be missing from the list the refusal offers. A child with
// no alias renders as the bare canonical token, which is what keeps `get pane`
// visibly distinct from `panes` in the same line.
func ChildSpellings(parent string) []string {
	route, ok := LookupRoute(parent)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(route.Children))
	for _, child := range route.Children {
		out = append(out, strings.Join(append([]string{child.Name}, child.Aliases...), "|"))
	}
	return out
}

// CanonicalGrandchildToken normalizes one token of a two-level route onto the
// canonical spelling of the node it addresses.
//
// It is the depth-two sibling of CanonicalChildToken and exists for the same
// reason: `get runtime sessions` is one route with three kinds, and the kind
// token has to reach the manifest that owns its spellings rather than a second
// hand-written list inside the handler.
func CanonicalGrandchildToken(parent, child, token string) (string, bool) {
	route, ok := LookupRoute(parent)
	if !ok {
		return "", false
	}
	node, ok := findChild(route, child)
	if !ok {
		return "", false
	}
	grandchild, ok := findChild(node, token)
	if !ok {
		return "", false
	}
	return grandchild.Name, true
}

// GrandchildSpellings renders the accepted tokens of a two-level route in
// manifest order, each canonical spelling followed by its aliases joined with
// `|`.
func GrandchildSpellings(parent, child string) []string {
	route, ok := LookupRoute(parent)
	if !ok {
		return nil
	}
	node, ok := findChild(route, child)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(node.Children))
	for _, grandchild := range node.Children {
		out = append(out, strings.Join(append([]string{grandchild.Name}, grandchild.Aliases...), "|"))
	}
	return out
}
