package codexgeneration

import (
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

// LifecycleOperationRef is the explicit durable discriminator for a planned
// generation transition. It is intentionally content-free: the operation ID
// and endpoint identity are sufficient to distinguish planned drain/recovery
// from an ordinary provider or process failure.
//
// This package consumes the ref but never creates it. Upgrade, drain, and
// handover producers belong to later phases.
type LifecycleOperationRef = metadata.CodexGenerationOperationRef

// LifecycleProjectionInput is the complete semantic input to the canonical
// Agent/Pane presentation mapper. Empty GenerationState is the
// provider-neutral compatibility lane; it does not claim that a legacy Codex
// Agent belongs to the current endpoint generation.
type LifecycleProjectionInput struct {
	Interaction     metadata.AgentInteractionKind
	Endpoint        *metadata.CodexEndpointRef
	GenerationState GenerationState
	Operation       *LifecycleOperationRef
}

// Authoritative reports whether an explicit generation projection carries the
// complete durable evidence for that state. The provider-neutral compatibility
// lane is intentionally not authority and is accepted only by legacy callers.
func (i LifecycleProjectionInput) Authoritative() bool {
	if i.GenerationState == "" || i.Endpoint == nil || !i.Endpoint.Valid() ||
		!slices.Contains(GenerationStates(), i.GenerationState) {
		return false
	}
	switch i.GenerationState {
	case StateDraining, StateHandoverPending, StateRecovering, StateBlocked:
		return i.Operation != nil && i.Operation.ValidFor(i.Endpoint)
	case StatePreparing, StateCurrent, StateRetired:
		return i.Operation == nil
	default:
		return false
	}
}

// LifecycleProjection is the exact live Pane tuple derived from one effective
// Agent interaction and generation state. Empty fields mean the corresponding
// option must be absent.
type LifecycleProjection struct {
	State     string
	Badge     string
	Attention string
}

const (
	LifecycleStateIdle       = "idle"
	LifecycleStateThinking   = "thinking"
	LifecycleStateWaiting    = "waiting"
	LifecycleStateDraining   = "draining"
	LifecycleStateRecovering = "recovering"
	LifecycleStateBlocked    = "blocked"

	LifecycleBadgeInProgress       = "in_progress"
	LifecycleBadgeApprovalRequired = "approval_required"
	LifecycleBadgeInputRequired    = "input_required"
	LifecycleBadgeResponseComplete = "response_complete"
	LifecycleBadgeDraining         = "draining"
	LifecycleBadgeHandoverPending  = "handover_pending"
	LifecycleBadgeRecovering       = "recovering"
	LifecycleBadgeBlocked          = "blocked"

	LifecycleAttentionBusy  = "busy"
	LifecycleAttentionReply = "reply"
)

// ProjectLifecycle is the single canonical interaction+generation mapper.
//
// Current and provider-neutral inputs preserve the established interaction
// semantics. Draining keeps already-live work actionable, while making a quiet
// Agent's planned drain explicit. Handover-pending and recovery states are
// non-replyable operation states. Preparing and retired generations cannot
// own a live Agent presentation and therefore clear the tuple.
//
// Planned states require a valid operation ref. A state token alone is not a
// maintenance marker: malformed, missing, or foreign refs clear the tuple
// instead of turning an ordinary crash/version drift into maintenance.
func ProjectLifecycle(input LifecycleProjectionInput) LifecycleProjection {
	interaction := input.Interaction
	if !metadata.ValidAgentInteractionKind(interaction) {
		interaction = metadata.InteractionUnknown
	}
	base := projectInteraction(interaction)
	state := input.GenerationState
	if state == "" {
		return base
	}
	if !input.Authoritative() {
		return LifecycleProjection{}
	}
	switch state {
	case StateCurrent:
		return base
	case StateDraining:
		if !validPlannedProjection(input) {
			return LifecycleProjection{}
		}
		if base.Badge == "" {
			return LifecycleProjection{State: LifecycleStateDraining, Badge: LifecycleBadgeDraining}
		}
		base.State = LifecycleStateDraining
		return base
	case StateHandoverPending:
		if !validPlannedProjection(input) {
			return LifecycleProjection{}
		}
		return LifecycleProjection{State: LifecycleStateDraining, Badge: LifecycleBadgeHandoverPending}
	case StateRecovering:
		if !validPlannedProjection(input) {
			return LifecycleProjection{}
		}
		return LifecycleProjection{State: LifecycleStateRecovering, Badge: LifecycleBadgeRecovering}
	case StateBlocked:
		if !validPlannedProjection(input) {
			return LifecycleProjection{}
		}
		return LifecycleProjection{State: LifecycleStateBlocked, Badge: LifecycleBadgeBlocked}
	case StatePreparing, StateRetired:
		return LifecycleProjection{}
	default:
		return LifecycleProjection{}
	}
}

func projectInteraction(kind metadata.AgentInteractionKind) LifecycleProjection {
	switch kind {
	case metadata.InteractionInProgress:
		return LifecycleProjection{State: LifecycleStateThinking, Badge: LifecycleBadgeInProgress, Attention: LifecycleAttentionBusy}
	case metadata.InteractionApprovalRequired:
		return LifecycleProjection{State: LifecycleStateWaiting, Badge: LifecycleBadgeApprovalRequired, Attention: LifecycleAttentionReply}
	case metadata.InteractionInputRequired:
		return LifecycleProjection{State: LifecycleStateWaiting, Badge: LifecycleBadgeInputRequired, Attention: LifecycleAttentionReply}
	case metadata.InteractionResponseComplete:
		return LifecycleProjection{State: LifecycleStateWaiting, Badge: LifecycleBadgeResponseComplete, Attention: LifecycleAttentionReply}
	case metadata.InteractionIdle:
		return LifecycleProjection{State: LifecycleStateIdle}
	default:
		return LifecycleProjection{}
	}
}

func validPlannedProjection(input LifecycleProjectionInput) bool {
	return input.Authoritative()
}

// GenerationStates returns the complete v1 state vocabulary in transition
// order. Projection properties and the pool validator share this owner.
func GenerationStates() []GenerationState {
	return []GenerationState{
		StatePreparing,
		StateCurrent,
		StateDraining,
		StateHandoverPending,
		StateRetired,
		StateRecovering,
		StateBlocked,
	}
}

// MutationOwner classifies whether an event belongs to the durable Agent
// endpoint. It is independent of whether its broker epoch remains current.
type MutationOwner string

const (
	MutationOwnerExact   MutationOwner = "owner"
	MutationOwnerForeign MutationOwner = "foreign"
)

// MutationFreshness classifies the complete stored/presented composite fence.
type MutationFreshness string

const (
	MutationCurrent MutationFreshness = "current"
	MutationStale   MutationFreshness = "stale"
)

// MutationTarget classifies whether the event names the exact runtime being
// projected or a sibling binding.
type MutationTarget string

const (
	MutationTargetExact   MutationTarget = "target"
	MutationTargetSibling MutationTarget = "sibling"
)

// MutationEffect is the only runtime presentation outcome.
type MutationEffect string

const (
	MutationSemanticEffect MutationEffect = "semantic-effect"
	MutationZeroWrite      MutationEffect = "zero-write"
)

// RuntimeMutationClass is one row of the owner/foreign x current/stale x
// target/sibling equivalence table.
type RuntimeMutationClass struct {
	Owner     MutationOwner
	Freshness MutationFreshness
	Target    MutationTarget
	Effect    MutationEffect
}

// RuntimeMutationClasses returns the complete equivalence table. Only the
// exact owner with a current five-dimensional fence targeting its own runtime is
// allowed to produce a semantic effect.
func RuntimeMutationClasses() []RuntimeMutationClass {
	rows := make([]RuntimeMutationClass, 0, 8)
	for _, owner := range []MutationOwner{MutationOwnerExact, MutationOwnerForeign} {
		for _, freshness := range []MutationFreshness{MutationCurrent, MutationStale} {
			for _, target := range []MutationTarget{MutationTargetExact, MutationTargetSibling} {
				effect := MutationZeroWrite
				if owner == MutationOwnerExact && freshness == MutationCurrent && target == MutationTargetExact {
					effect = MutationSemanticEffect
				}
				rows = append(rows, RuntimeMutationClass{Owner: owner, Freshness: freshness, Target: target, Effect: effect})
			}
		}
	}
	return rows
}

// RuntimeMutationInput is the content-free evidence for one provider event.
type RuntimeMutationInput struct {
	DurableEndpoint    *metadata.CodexEndpointRef
	StoredAuthority    *metadata.CodexAuthorityRef
	PresentedAuthority *metadata.CodexAuthorityRef
	TargetRuntimeID    string
	EventRuntimeID     string
}

// RuntimeMutationDecision names the equivalence class and composite fence
// result so a refusal diagnostic does not need provider content.
type RuntimeMutationDecision struct {
	Class     RuntimeMutationClass
	Authority AuthorityDecision
}

// DecideRuntimeMutation classifies one event without writing.
func DecideRuntimeMutation(input RuntimeMutationInput) RuntimeMutationDecision {
	owner := MutationOwnerForeign
	if input.DurableEndpoint != nil && input.PresentedAuthority != nil &&
		input.DurableEndpoint.Same(input.PresentedAuthority.Endpoint()) {
		owner = MutationOwnerExact
	}
	authority := DecideAuthority(input.DurableEndpoint, input.StoredAuthority, input.PresentedAuthority)
	freshness := MutationStale
	if input.StoredAuthority != nil && input.PresentedAuthority != nil &&
		input.StoredAuthority.Authorizes(*input.PresentedAuthority) {
		freshness = MutationCurrent
	}
	target := MutationTargetSibling
	if strings.TrimSpace(input.TargetRuntimeID) != "" && input.TargetRuntimeID == input.EventRuntimeID {
		target = MutationTargetExact
	}
	var class RuntimeMutationClass
	for _, candidate := range RuntimeMutationClasses() {
		if candidate.Owner == owner && candidate.Freshness == freshness && candidate.Target == target {
			class = candidate
			break
		}
	}
	if authority != AuthorityAllowed {
		class.Effect = MutationZeroWrite
	}
	return RuntimeMutationDecision{Class: class, Authority: authority}
}

// ApplyRuntimeMutation invokes both bounded presentation writers only for the
// one allowed equivalence class. Refused events cannot partially touch either
// Registry or tmux because the complete decision precedes both callbacks.
func ApplyRuntimeMutation(input RuntimeMutationInput, registryWrite, tmuxWrite func()) RuntimeMutationDecision {
	decision := DecideRuntimeMutation(input)
	if decision.Class.Effect == MutationSemanticEffect {
		if registryWrite != nil {
			registryWrite()
		}
		if tmuxWrite != nil {
			tmuxWrite()
		}
	}
	return decision
}
