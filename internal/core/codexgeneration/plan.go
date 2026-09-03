package codexgeneration

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type PlanDecision string

const (
	PlanReady   PlanDecision = "ready"
	PlanBlocked PlanDecision = "blocked"
)

type BlockerCode string

const (
	BlockerInvalidTopology      BlockerCode = "invalid-topology"
	BlockerQualificationMissing BlockerCode = "qualification-missing"
	BlockerQualificationFailed  BlockerCode = "qualification-failed"
	BlockerPoolFull             BlockerCode = "pool-full"
	BlockerAgentObligation      BlockerCode = "agent-obligation"
	BlockerOfficialLifecycle    BlockerCode = "official-managed-lifecycle"
	BlockerUnmanagedLifecycle   BlockerCode = "unmanaged-lifecycle"
	BlockerUnknownLifecycle     BlockerCode = "unknown-lifecycle"
)

type Blocker struct {
	Code                 BlockerCode `json:"code"`
	AgentUID             string      `json:"agentUID,omitempty"`
	EndpointGenerationID string      `json:"endpointGenerationID,omitempty"`
	Reason               string      `json:"reason"`
}

// MutationCount is present in every plan so repeated/read-only behavior is
// machine-verifiable rather than an implementation comment.
type MutationCount struct {
	Registry       int `json:"registry"`
	Provider       int `json:"provider"`
	Tmux           int `json:"tmux"`
	Process        int `json:"process"`
	CurrentPointer int `json:"currentPointer"`
}

type UpgradePlan struct {
	ModelVersion      int           `json:"modelVersion"`
	StateDomainID     string        `json:"stateDomainID"`
	CurrentGeneration string        `json:"currentGeneration,omitempty"`
	TargetGeneration  string        `json:"targetGeneration"`
	Decision          PlanDecision  `json:"decision"`
	Blockers          []Blocker     `json:"blockers,omitempty"`
	Mutations         MutationCount `json:"mutations"`
}

// PlanUpgrade is a pure reduction over values. It neither accepts nor returns
// a callback or adapter, making process/provider/Registry/tmux mutation
// unreachable from the Phase 0 product path.
func PlanUpgrade(pool Pool, targetGenerationID string, qualification *QualificationResult) UpgradePlan {
	plan := UpgradePlan{ModelVersion: ModelVersion, Decision: PlanReady}
	poolValid := true
	if err := pool.Validate(); err != nil {
		poolValid = false
		plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerInvalidTopology, Reason: RefusalOf(err).String()})
	}
	if !validIdentityToken(targetGenerationID) {
		plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerInvalidTopology, Reason: "target-generation-required"})
	} else {
		plan.TargetGeneration = targetGenerationID
	}
	if qualification == nil {
		plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerQualificationMissing, Reason: "fixed-version-pair qualification is required"})
	} else if err := qualification.Validate(); err != nil {
		plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerQualificationFailed, Reason: "qualification-invalid"})
	} else if gate := GateQualification(*qualification); !gate.Phase2Ready {
		plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerQualificationFailed, Reason: string(qualification.Reason)})
	}
	if poolValid {
		plan.StateDomainID = pool.StateDomainID
		if current, ok := pool.Current(); ok {
			plan.CurrentGeneration = current.Endpoint.EndpointGenerationID
			switch current.Owner {
			case OwnerOfficialManaged:
				plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerOfficialLifecycle,
					EndpointGenerationID: current.Endpoint.EndpointGenerationID,
					Reason:               "official manager owns endpoint lifecycle; an external exact stop receipt is required"})
			case OwnerUnmanaged:
				plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerUnmanagedLifecycle,
					EndpointGenerationID: current.Endpoint.EndpointGenerationID,
					Reason:               "exact-current unmanaged endpoint is attach-only; operator-owned stop is required"})
			case OwnerUnknown:
				plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerUnknownLifecycle,
					EndpointGenerationID: current.Endpoint.EndpointGenerationID,
					Reason:               "endpoint lifecycle owner is unknown"})
			}
		}
		for _, generation := range pool.Generations {
			if generation.State != StateRetired && generation.State != StateCurrent {
				plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerPoolFull,
					EndpointGenerationID: generation.Endpoint.EndpointGenerationID,
					Reason:               "bounded current-plus-draining pool has no free slot"})
			}
		}
		for _, obligation := range pool.Obligations {
			if obligation.State == ObligationClosed {
				continue
			}
			plan.Blockers = append(plan.Blockers, Blocker{Code: BlockerAgentObligation,
				AgentUID: obligation.AgentUID, EndpointGenerationID: obligation.EndpointGenerationID,
				Reason: string(obligation.State)})
		}
	}
	slices.SortFunc(plan.Blockers, func(a, b Blocker) int {
		return strings.Compare(string(a.Code)+"\x00"+a.EndpointGenerationID+"\x00"+a.AgentUID+"\x00"+a.Reason,
			string(b.Code)+"\x00"+b.EndpointGenerationID+"\x00"+b.AgentUID+"\x00"+b.Reason)
	})
	if len(plan.Blockers) != 0 {
		plan.Decision = PlanBlocked
	}
	return plan
}

func (p UpgradePlan) JSON() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }

func (p UpgradePlan) Text() string {
	var out strings.Builder
	fmt.Fprintf(&out, "codex app-server upgrade plan: %s\nstate-domain: %s\ncurrent-generation: %s\ntarget-generation: %s",
		p.Decision, valueOrNone(p.StateDomainID), valueOrNone(p.CurrentGeneration), valueOrNone(p.TargetGeneration))
	for _, blocker := range p.Blockers {
		fmt.Fprintf(&out, "\nblocker: code=%s agent=%s generation=%s reason=%s", blocker.Code,
			valueOrNone(blocker.AgentUID), valueOrNone(blocker.EndpointGenerationID), blocker.Reason)
	}
	fmt.Fprintf(&out, "\nmutations: registry=%d provider=%d tmux=%d process=%d current-pointer=%d",
		p.Mutations.Registry, p.Mutations.Provider, p.Mutations.Tmux, p.Mutations.Process, p.Mutations.CurrentPointer)
	return out.String()
}

func valueOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}
