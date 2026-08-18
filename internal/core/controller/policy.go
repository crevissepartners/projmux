package controller

import (
	"slices"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// Intent is the closed set of mutations this kernel can be asked to authorize.
//
// The two repair intents are separated from the three lifecycle intents on
// purpose. Repair writes a value the Registry already owns onto an object the
// Registry already owns; start, import, and delete change what exists. The
// first is convergence, the last three are activation, adoption, and
// destruction -- and this kernel performs none of them, which is only a
// checkable property if the vocabulary can say them out loud.
type Intent string

const (
	// IntentRepairBinding writes a Registry-owned uid onto an exact tmux handle
	// whose mirror is missing or stale.
	IntentRepairBinding Intent = "repair-binding"
	// IntentRepairMirror writes a Registry-owned name, root, or projection
	// value onto an exact tmux handle.
	IntentRepairMirror Intent = "repair-mirror"
	// IntentStart creates runtime for a resource that has none.
	IntentStart Intent = "start"
	// IntentImport adopts a runtime object into the Registry.
	IntentImport Intent = "import"
	// IntentDelete removes a runtime object.
	IntentDelete Intent = "delete"
)

// Intents returns the closed intent set in declaration order.
func Intents() []Intent {
	return []Intent{IntentRepairBinding, IntentRepairMirror, IntentStart, IntentImport, IntentDelete}
}

// Repairs reports whether intent is one of the two convergence intents.
func (i Intent) Repairs() bool {
	return i == IntentRepairBinding || i == IntentRepairMirror
}

func intentRank(intent Intent) int {
	return slices.Index(Intents(), intent)
}

// Authority is the closed verdict set.
//
// Observe and Refuse both end in zero writes, and they are still different
// answers. Observe is "this object is not mine to converge and nothing is
// wrong"; Refuse is "this object contradicts the Registry, and an operator
// should see it". Collapsing them would bury every conflict in the same silence
// a control session gets.
type Authority string

const (
	// AuthorityAllow permits the write, subject to its guards still holding.
	AuthorityAllow Authority = "allow"
	// AuthorityObserve reports the object and performs no write. It is not a
	// drift finding.
	AuthorityObserve Authority = "observe-only"
	// AuthorityRefuse performs no write and reports the object as refused
	// drift.
	AuthorityRefuse Authority = "refuse"
)

// Grant is the authority the invocation itself carries, independent of any
// object's attribution.
//
// It exists for exactly one distinction. `ClassForeign` means "unmarked, and on
// a tmux server projmux does not own" -- which is the correct default refusal
// for anything that discovered the object on its own, and the wrong answer for a
// repair the operator explicitly aimed at that exact server. Naming the grant
// keeps the default a refusal and makes the exception a stated precondition
// rather than a hole in the table.
type Grant struct {
	// OperatorTargeted reports that this invocation names one exact tmux server
	// chosen by the operator -- an explicit socket flag or the socket the
	// operator's own client is attached to. A background trigger, a UI refresh,
	// or any producer that found the server by looking around does not have it.
	OperatorTargeted bool
}

// Subject is what a mutation would land on: the attribution the resolved graph
// gave the object, plus the grant the invocation carries.
//
// Deliberately nothing else. Every other field that could be added here -- a
// session name, a working directory, a running command -- is a field the graph
// already refuses to attribute identity with, and a policy that consulted one
// would reintroduce the heuristic merge at the authorization layer instead of
// the resolution layer.
type Subject struct {
	Class resourcegraph.Class
	Grant Grant
}

// Verdict is one decided cell of the policy table.
type Verdict struct {
	Intent    Intent              `json:"intent"`
	Class     resourcegraph.Class `json:"class"`
	Authority Authority           `json:"authority"`
	Reason    string              `json:"reason"`
}

// Allowed reports whether this verdict permits a write.
func (v Verdict) Allowed() bool { return v.Authority == AuthorityAllow }

const (
	reasonNeverStart  = "this kernel never starts an offline resource; runtime activation belongs to an explicit create, open, or materialize"
	reasonNeverImport = "this kernel never adopts a runtime object into the Registry; managed identity comes from the Registry, never from the machine"
	reasonNeverDelete = "this kernel never deletes a runtime object; a missing runtime object is not a desired deletion"

	reasonManaged = "Registry resource bound to this handle by exact uid evidence"
	// An unmarked object inside projmux's own runtime world carries no
	// competing identity, so restoring a mirror the Registry already owns
	// cannot overwrite anyone. It is still never imported: the Registry row it
	// mirrors has to already exist for a producer to have a value to write.
	reasonUnattributed = "no mirrored identity inside projmux's own runtime world: restoring a Registry-owned mirror overwrites no other identity, and the object is still never imported, started, or deleted"
	// Overwriting a legible foreign uid is worse than leaving it: the uid is
	// the whole evidence an operator-driven recovery would work from, and
	// adopting it is out of this kernel's scope by design.
	reasonRecoverable = "mirrors a uid this Registry does not contain: adoption is out of scope for this kernel, and overwriting the uid would destroy the recovery evidence"
	reasonControl     = "app-owned control session, deliberately not a Registry resource"
	reasonEphemeral   = "auto-attach ephemeral session, never part of the Project hierarchy"
	reasonForeign     = "no mirrored identity and no managed enclosure on a host projmux does not own, and this invocation did not name that host"
	// The operator pointed the command at this exact server. That is the
	// consent the host marker would otherwise stand in for, and the object
	// still carries no identity to overwrite.
	reasonForeignTargeted = "no mirrored identity on a host projmux does not own, repaired only because the operator named this exact server; the object is still never imported, started, or deleted"
	reasonConflict        = "exact evidence contradicts itself; picking a claimant would authorize a mutation on a coin flip"
	reasonUnknown         = "unrecognized attribution: an unknown class is refused rather than assumed safe"
)

// Decide returns the authority of intent over subject.
//
// The lifecycle intents are answered before the class is even read. That order
// is the point: no attribution, however well evidenced, makes this kernel start,
// adopt, or destroy anything, so a future caller cannot reach those behaviors by
// arriving with a better-classified object.
func Decide(intent Intent, subject Subject) Verdict {
	verdict := Verdict{Intent: intent, Class: subject.Class, Authority: AuthorityRefuse}
	switch intent {
	case IntentStart:
		verdict.Reason = reasonNeverStart
		return verdict
	case IntentImport:
		verdict.Reason = reasonNeverImport
		return verdict
	case IntentDelete:
		verdict.Reason = reasonNeverDelete
		return verdict
	}
	switch subject.Class {
	case resourcegraph.ClassManaged:
		verdict.Authority, verdict.Reason = AuthorityAllow, reasonManaged
	case resourcegraph.ClassUnattributed:
		verdict.Authority, verdict.Reason = AuthorityAllow, reasonUnattributed
	case resourcegraph.ClassRecoverable:
		verdict.Authority, verdict.Reason = AuthorityRefuse, reasonRecoverable
	case resourcegraph.ClassControl:
		verdict.Authority, verdict.Reason = AuthorityObserve, reasonControl
	case resourcegraph.ClassEphemeral:
		verdict.Authority, verdict.Reason = AuthorityObserve, reasonEphemeral
	case resourcegraph.ClassForeign:
		if subject.Grant.OperatorTargeted {
			verdict.Authority, verdict.Reason = AuthorityAllow, reasonForeignTargeted
			break
		}
		verdict.Authority, verdict.Reason = AuthorityRefuse, reasonForeign
	case resourcegraph.ClassConflict:
		verdict.Authority, verdict.Reason = AuthorityRefuse, reasonConflict
	default:
		verdict.Authority, verdict.Reason = AuthorityRefuse, reasonUnknown
	}
	return verdict
}

// SortVerdicts orders verdicts the way Table emits them, so a projection
// assembled from a live graph reads in the same order as the reference table.
func SortVerdicts(verdicts []Verdict) {
	slices.SortStableFunc(verdicts, func(a, b Verdict) int {
		if rank := intentRank(a.Intent) - intentRank(b.Intent); rank != 0 {
			return rank
		}
		return slices.Index(resourcegraph.Classes(), a.Class) - slices.Index(resourcegraph.Classes(), b.Class)
	})
}
