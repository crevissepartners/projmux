// Package codexgeneration owns the pure, content-free model for a bounded
// Codex app-server generation pool. It has no provider, process, filesystem,
// Registry-writer, or tmux dependency; Phase 0 can therefore plan and qualify
// topology without accidentally implementing lifecycle mutation.
package codexgeneration

import (
	"errors"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

const (
	// ModelVersion identifies the first bounded-pool decision model.
	ModelVersion = 1
	// QualificationSchemaVersion is the strict, content-free receipt schema.
	QualificationSchemaVersion = 1
)

type GenerationState = metadata.CodexGenerationState

const (
	StatePreparing       = metadata.CodexGenerationPreparing
	StateCurrent         = metadata.CodexGenerationCurrent
	StateDraining        = metadata.CodexGenerationDraining
	StateHandoverPending = metadata.CodexGenerationHandoverPending
	StateRetired         = metadata.CodexGenerationRetired
	StateRecovering      = metadata.CodexGenerationRecovering
	StateBlocked         = metadata.CodexGenerationBlocked
)

func validGenerationState(state GenerationState) bool {
	return slices.Contains(GenerationStates(), state)
}

type OwnerClass string

const (
	OwnerProjmuxPrivate OwnerClass = "projmux-private"
	OwnerUnmanaged      OwnerClass = "unmanaged"
	OwnerUnknown        OwnerClass = "unknown"
)

func validOwnerClass(owner OwnerClass) bool {
	return slices.Contains([]OwnerClass{OwnerProjmuxPrivate, OwnerUnmanaged, OwnerUnknown}, owner)
}

// Generation is the durable, path-free inventory of one pool slot.
type Generation struct {
	Endpoint metadata.CodexEndpointRef `json:"endpoint"`
	State    GenerationState           `json:"state"`
	Owner    OwnerClass                `json:"owner"`
	BundleID string                    `json:"bundleID"`
}

type ObligationState string

const (
	ObligationActive             ObligationState = "active"
	ObligationApprovalPending    ObligationState = "approval-pending"
	ObligationNoTurn             ObligationState = "no-turn"
	ObligationUnknown            ObligationState = "unknown"
	ObligationCompletedPersisted ObligationState = "completed-persisted"
	ObligationClosed             ObligationState = "closed"
)

func validObligationState(state ObligationState) bool {
	return slices.Contains([]ObligationState{
		ObligationActive, ObligationApprovalPending, ObligationNoTurn,
		ObligationUnknown, ObligationCompletedPersisted, ObligationClosed,
	}, state)
}

// AgentObligation is the minimum identity/state tuple an upgrade plan may
// retain. It intentionally cannot carry prompt, message, transcript, socket,
// or credential material.
type AgentObligation struct {
	AgentUID             string          `json:"agentUID"`
	EndpointGenerationID string          `json:"endpointGenerationID"`
	State                ObligationState `json:"state"`
}

// ProjectAgentObligation derives one generation-retirement obligation from
// durable Agent state. In particular, a native thread with no observed turn is
// ObligationNoTurn regardless of whether its Pane is still Running. Only an
// explicit close decision changes that row to ObligationClosed.
func ProjectAgentObligation(agent metadata.Agent, explicitlyClosed bool) (AgentObligation, bool) {
	ref := agent.Status.SessionRef
	if ref == nil || ref.Provider != "codex" || ref.Codex == nil || ref.Codex.Endpoint == nil ||
		!ref.Codex.Endpoint.Valid() || strings.TrimSpace(ref.Codex.ThreadID) == "" {
		return AgentObligation{}, false
	}
	state := ObligationUnknown
	switch {
	case explicitlyClosed:
		state = ObligationClosed
	case !ref.Codex.HasStartedTurn:
		state = ObligationNoTurn
	case agent.Status.Interaction.Kind == metadata.InteractionApprovalRequired:
		state = ObligationApprovalPending
	case agent.Status.Phase == metadata.PhaseRunning:
		state = ObligationActive
	case agent.Status.Interaction.Kind == metadata.InteractionResponseComplete || agent.Status.Phase == metadata.PhaseOffline:
		state = ObligationCompletedPersisted
	}
	return AgentObligation{
		AgentUID: agent.Metadata.UID, EndpointGenerationID: ref.Codex.Endpoint.EndpointGenerationID, State: state,
	}, true
}

// Pool is the complete input to the Phase 0 topology and plan model.
type Pool struct {
	StateDomainID string            `json:"stateDomainID"`
	Generations   []Generation      `json:"generations"`
	Obligations   []AgentObligation `json:"obligations,omitempty"`
}

type TopologyRefusal string

const (
	RefusalNone                        TopologyRefusal = "none"
	RefusalStateDomainRequired         TopologyRefusal = "state-domain-required"
	RefusalGenerationInvalid           TopologyRefusal = "generation-invalid"
	RefusalGenerationDuplicate         TopologyRefusal = "generation-duplicate"
	RefusalGenerationDomainMismatch    TopologyRefusal = "generation-domain-mismatch"
	RefusalPoolCapacityExceeded        TopologyRefusal = "pool-capacity-exceeded"
	RefusalMultipleCurrent             TopologyRefusal = "multiple-current"
	RefusalMultipleDraining            TopologyRefusal = "multiple-draining"
	RefusalCurrentMissingWithLiveWork  TopologyRefusal = "current-missing-with-live-obligations"
	RefusalObligationInvalid           TopologyRefusal = "obligation-invalid"
	RefusalObligationGenerationUnknown TopologyRefusal = "obligation-generation-unknown"
)

// TopologyError renders only one closed refusal token.
type TopologyError struct{ Refusal TopologyRefusal }

func (e *TopologyError) Error() string {
	return "Codex generation topology refused: " + string(e.Refusal)
}

func RefusalOf(err error) TopologyRefusal {
	var refusal *TopologyError
	if errors.As(err, &refusal) {
		return refusal.Refusal
	}
	return RefusalNone
}

// Validate enforces the v1 current-one plus draining-one topology. Preparing,
// handover-pending, recovering, and blocked generations also occupy one of the
// two live slots; retired receipt rows do not.
func (p Pool) Validate() error {
	if !validIdentityToken(p.StateDomainID) {
		return &TopologyError{Refusal: RefusalStateDomainRequired}
	}
	seen := make(map[string]Generation, len(p.Generations))
	current, draining, liveSlots := 0, 0, 0
	for _, generation := range p.Generations {
		id := strings.TrimSpace(generation.Endpoint.EndpointGenerationID)
		if !generation.Endpoint.Valid() || !validGenerationState(generation.State) ||
			!validOwnerClass(generation.Owner) || !validIdentityToken(generation.BundleID) {
			return &TopologyError{Refusal: RefusalGenerationInvalid}
		}
		if generation.Endpoint.StateDomainID != p.StateDomainID {
			return &TopologyError{Refusal: RefusalGenerationDomainMismatch}
		}
		if _, exists := seen[id]; exists {
			return &TopologyError{Refusal: RefusalGenerationDuplicate}
		}
		seen[id] = generation
		if generation.State != StateRetired {
			liveSlots++
		}
		if generation.State == StateCurrent {
			current++
		}
		if generation.State == StateDraining || generation.State == StateHandoverPending {
			draining++
		}
	}
	if current > 1 {
		return &TopologyError{Refusal: RefusalMultipleCurrent}
	}
	if draining > 1 {
		return &TopologyError{Refusal: RefusalMultipleDraining}
	}
	if liveSlots > 2 {
		return &TopologyError{Refusal: RefusalPoolCapacityExceeded}
	}
	liveObligations := 0
	for _, obligation := range p.Obligations {
		if !validIdentityToken(obligation.AgentUID) ||
			!validIdentityToken(obligation.EndpointGenerationID) ||
			!validObligationState(obligation.State) {
			return &TopologyError{Refusal: RefusalObligationInvalid}
		}
		generation, ok := seen[obligation.EndpointGenerationID]
		if !ok || generation.State == StateRetired {
			return &TopologyError{Refusal: RefusalObligationGenerationUnknown}
		}
		if obligation.State != ObligationClosed {
			liveObligations++
		}
	}
	if liveObligations != 0 && current == 0 {
		return &TopologyError{Refusal: RefusalCurrentMissingWithLiveWork}
	}
	return nil
}

// Current returns the sole admission-current generation after validation.
func (p Pool) Current() (Generation, bool) {
	for _, generation := range p.Generations {
		if generation.State == StateCurrent {
			return generation, true
		}
	}
	return Generation{}, false
}

// String keeps TopologyError values from gaining contextual or sensitive
// material through fmt wrapping in tests and diagnostics.
func (r TopologyRefusal) String() string { return string(r) }

func validIdentityToken(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}
