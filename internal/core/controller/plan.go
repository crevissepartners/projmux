package controller

import (
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// Surface is which of the two stores an action writes to.
//
// The split is the commit order made explicit. Registry actions are durable
// desired state and go first, inside one transaction; tmux actions are a
// non-transactional overlay and go second, behind the guards. An action that
// could not say which surface it belongs to could not be ordered against that
// boundary, and that boundary is what the whole failure contract rests on.
type Surface string

const (
	// SurfaceRegistry is a durable desired-state change.
	SurfaceRegistry Surface = "registry"
	// SurfaceTmux is a runtime mirror write on one exact handle.
	SurfaceTmux Surface = "tmux"
)

// Guard is one fact re-proved on the exact handle immediately before the first
// live write.
type Guard struct {
	// Field is the tmux format field the fact is read through.
	Field string `json:"field"`
	// Expect is the value observed when the plan was built. An empty Expect is
	// a real expectation, not an absent one: it asserts the object still
	// carries no value there.
	Expect string `json:"expect"`
}

// Action is one planned unit of convergence, authorized or refused.
//
// A refused action is still an action. It carries its target, its class, and
// the reason it was refused, so the one report an operator reads distinguishes
// "there was nothing to do" from "there was something to do and I was not
// allowed to do it".
type Action struct {
	// Key is the stable total order of the plan. Two plans built from the same
	// graph produce the same keys in the same order.
	Key string `json:"key"`
	// Surface is which store this action writes to.
	Surface Surface `json:"surface"`
	// Intent is what this action would do.
	Intent Intent `json:"intent"`
	// Authority is the policy verdict for this action's subject.
	Authority Authority `json:"authority"`
	// Class is the attribution of the target.
	Class resourcegraph.Class `json:"class,omitempty"`
	// Kind is the resource kind label for display.
	Kind string `json:"kind,omitempty"`
	// Scope is the runtime containment level of the target. It is the primary
	// order of the plan, ahead of the key, because mirror repair has a real
	// containment dependency: a Pane uid written into a Window that does not
	// yet carry its own uid is attributable to nothing, and the next pass reads
	// it as a Pane outside its owner scope. Sorting by key alone is
	// deterministic and wrong.
	Scope resourcegraph.ObjectKind `json:"scope,omitempty"`
	// Target is the exact tmux handle, resolved to its stable id.
	Target string `json:"target,omitempty"`
	// Field is the option or attribute being written.
	Field string `json:"field,omitempty"`
	// Before and After are the observed and desired values.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Reason states the verdict in one clause.
	Reason string `json:"reason,omitempty"`
	// Guards is the evidence re-proved before the first live write. It is empty
	// for a registry action and for any action that will not be executed.
	Guards []Guard `json:"guards,omitempty"`
	// Args is the exact tmux argv, tmux surface only, without the transport
	// prefix. The executor adds the prefix, so no action can name its own
	// server.
	Args          []string                 `json:"-"`
	Divergence    resourcegraph.Divergence `json:"divergence,omitempty"`
	RecoveryLevel RecoveryLevel            `json:"recoveryLevel,omitempty"`
	LossKind      string                   `json:"lossKind,omitempty"`
	LossCount     int                      `json:"lossCount,omitempty"`
}

// Allowed reports whether this action will be executed if its guards hold.
func (a Action) Allowed() bool { return a.Authority == AuthorityAllow }

// Refused reports whether this action is refused drift.
func (a Action) Refused() bool { return a.Authority == AuthorityRefuse }

// Plan is the totally ordered, authorized result of planning one graph.
type Plan struct {
	Transport resourcegraph.Transport `json:"transport"`
	HostMode  resourcegraph.HostMode  `json:"hostMode"`
	Actions   []Action                `json:"actions,omitempty"`
	// Policy is the subset of the policy table this graph actually exercised,
	// so the report explains its own refusals without the reader reconstructing
	// the table.
	Policy []Verdict `json:"policy,omitempty"`
}

// NewPlan sorts actions into the one total order and returns the plan.
//
// The order is registry before tmux, then outermost containment first, then by
// key. It is also the execution order: making the sort produce the execution
// order rather than merely a display order is what makes "dry-run and execute
// used the same plan" a property of one value instead of an agreement between
// two code paths.
func NewPlan(transport resourcegraph.Transport, host resourcegraph.HostMode, actions []Action, policy []Verdict) Plan {
	sorted := slices.Clone(actions)
	slices.SortStableFunc(sorted, compareActions)
	applied := slices.Clone(policy)
	SortVerdicts(applied)
	return Plan{Transport: transport, HostMode: host, Actions: sorted, Policy: applied}
}

func compareActions(a, b Action) int {
	if rank := surfaceRank(a.Surface) - surfaceRank(b.Surface); rank != 0 {
		return rank
	}
	if rank := scopeRank(a.Scope) - scopeRank(b.Scope); rank != 0 {
		return rank
	}
	if cmp := strings.Compare(a.Key, b.Key); cmp != 0 {
		return cmp
	}
	return strings.Compare(a.Field, b.Field)
}

// scopeRank orders sessions before windows before panes. An action with no
// resolved scope -- one whose target the observation never saw -- sorts last,
// where it cannot displace a real containment step.
func scopeRank(scope ObjectScope) int {
	index := slices.Index(resourcegraph.ObjectKinds(), scope)
	if index < 0 {
		return len(resourcegraph.ObjectKinds())
	}
	return index
}

// ObjectScope is the runtime containment level of an action's target.
type ObjectScope = resourcegraph.ObjectKind

func surfaceRank(surface Surface) int {
	if surface == SurfaceRegistry {
		return 0
	}
	return 1
}

// Writes returns the allowed tmux actions in execution order.
func (p Plan) Writes() []Action {
	var out []Action
	for _, action := range p.Actions {
		if action.Surface == SurfaceTmux && action.Allowed() {
			out = append(out, action)
		}
	}
	return out
}

// Refusals returns every refused action in plan order.
func (p Plan) Refusals() []Action {
	var out []Action
	for _, action := range p.Actions {
		if action.Refused() {
			out = append(out, action)
		}
	}
	return out
}

// Pending returns every action that would still do something: an allowed write
// or a refusal an operator has to see. Observe-only actions are excluded, which
// is what lets a machine full of foreign and control objects still report
// convergence.
func (p Plan) Pending() []Action {
	var out []Action
	for _, action := range p.Actions {
		if action.Allowed() || action.Refused() {
			out = append(out, action)
		}
	}
	return out
}

// Converged reports whether this plan has nothing left to do. It is the shape a
// repeat of a successful execute must have.
func (p Plan) Converged() bool { return len(p.Pending()) == 0 }
