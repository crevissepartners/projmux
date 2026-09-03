package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

type doctorCodexGenerationPool struct {
	Status                  string                                     `json:"status"`
	Reason                  string                                     `json:"reason"`
	StateDomainID           string                                     `json:"state_domain_id,omitempty"`
	CurrentGenerationID     string                                     `json:"current_generation_id,omitempty"`
	ExpectedMultiGeneration bool                                       `json:"expected_multi_generation"`
	DoctorMutations         int                                        `json:"doctor_mutations"`
	Qualification           *codexgeneration.QualificationResult       `json:"qualification,omitempty"`
	Generations             []doctorCodexGeneration                    `json:"generations"`
	PinnedAgents            []doctorCodexPinnedAgent                   `json:"pinned_agents"`
	Operation               *doctorCodexGenerationOperation            `json:"operation,omitempty"`
	RollingMutations        *codexgeneration.RollingOperationMutations `json:"rolling_mutations,omitempty"`
	HandoverMutations       *codexgeneration.HandoverMutations         `json:"handover_mutations,omitempty"`
}

type doctorCodexGeneration struct {
	GenerationID string                          `json:"generation_id"`
	State        codexgeneration.GenerationState `json:"state"`
	Owner        codexgeneration.OwnerClass      `json:"owner"`
	BundleID     string                          `json:"bundle_id"`
	BundleStatus string                          `json:"bundle_status"`
	Version      string                          `json:"version,omitempty"`
	PinnedAgents int                             `json:"pinned_agents"`
	Status       string                          `json:"status"`
	Action       string                          `json:"action"`
	Reason       string                          `json:"reason"`
}

type doctorCodexPinnedAgent struct {
	AgentUID     string                          `json:"agent_uid"`
	GenerationID string                          `json:"generation_id"`
	State        codexgeneration.ObligationState `json:"state"`
	Status       string                          `json:"status"`
	Action       string                          `json:"action"`
	Reason       string                          `json:"reason"`
}

type doctorCodexGenerationOperation struct {
	OperationRef string                     `json:"operation_ref"`
	Kind         string                     `json:"kind"`
	Phase        string                     `json:"phase"`
	NextAction   string                     `json:"next_action"`
	TargetAgent  string                     `json:"target_agent_uid,omitempty"`
	Status       string                     `json:"status"`
	Reason       string                     `json:"reason"`
	Timeline     []doctorCodexOperationStep `json:"timeline"`
}

type doctorCodexOperationStep struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Count int    `json:"count"`
}

type doctorCodexBundleVerifier func(codexgenerationhost.PrivateGenerationConfig) (codexgenerationhost.VerifiedBundleIdentity, error)

func diagnoseCodexGenerationPool(journal codexupgrade.Journal, registry coremetadata.Registry, verify doctorCodexBundleVerifier) doctorCodexGenerationPool {
	report := doctorCodexGenerationPool{
		Status: "ready", Reason: "qualified", StateDomainID: journal.StateDomainID,
		CurrentGenerationID: journal.CurrentGenerationID, Generations: []doctorCodexGeneration{},
		PinnedAgents: []doctorCodexPinnedAgent{}, Qualification: journal.Qualification,
	}
	block := func(reason string) {
		if report.Status != "blocked" {
			report.Status, report.Reason = "blocked", reason
		}
	}
	if err := journal.Validate(); err != nil {
		reason := string(codexgeneration.RefusalOf(err))
		if reason == string(codexgeneration.RefusalNone) {
			reason = "invalid-admission-tuple"
		}
		block(reason)
		return report
	}
	liveGenerations := 0
	for _, route := range journal.Routes {
		if route.Generation.State != codexgeneration.StateRetired {
			liveGenerations++
		}
	}
	report.ExpectedMultiGeneration = liveGenerations == 2
	if journal.Qualification == nil {
		block("qualification-missing")
	} else if journal.Qualification.Verdict != codexgeneration.VerdictYes {
		block("version-pair-no")
	}

	obligations := make(map[string]codexgeneration.AgentObligation, len(journal.Obligations))
	for _, obligation := range journal.Obligations {
		if _, duplicate := obligations[obligation.AgentUID]; duplicate {
			block("duplicate-pinned-agent")
		}
		obligations[obligation.AgentUID] = obligation
	}
	for _, agent := range registry.Agents {
		if agent.Spec.Provider != aiModeCodex || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
			agent.Status.SessionRef.Codex.Endpoint == nil || agent.Status.SessionRef.Codex.Endpoint.StateDomainID != journal.StateDomainID {
			continue
		}
		projected, ok := codexgeneration.ProjectAgentObligation(agent, false)
		if !ok {
			continue
		}
		if stored, exists := obligations[agent.Metadata.UID]; exists {
			if stored.EndpointGenerationID != projected.EndpointGenerationID {
				block("pinned-agent-endpoint-skew")
			}
		} else {
			obligations[agent.Metadata.UID] = projected
		}
	}

	for _, obligation := range obligations {
		pinned := doctorPinnedAgentAction(obligation)
		if _, ok := journal.Route(coremetadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: obligation.EndpointGenerationID}); !ok {
			pinned.Status, pinned.Action, pinned.Reason = "blocked", "repair-admission-tuple", "pinned-generation-missing"
			block("pinned-agent-endpoint-skew")
		}
		if pinned.Status == "blocked" {
			block(pinned.Reason)
		}
		report.PinnedAgents = append(report.PinnedAgents, pinned)
	}
	slices.SortFunc(report.PinnedAgents, func(a, b doctorCodexPinnedAgent) int { return strings.Compare(a.AgentUID, b.AgentUID) })

	for _, route := range journal.Routes {
		generation := doctorCodexGeneration{
			GenerationID: route.Generation.Endpoint.EndpointGenerationID, State: route.Generation.State,
			Owner: route.Generation.Owner, BundleID: route.Generation.BundleID,
			BundleStatus: "not-managed", Status: "ready", Action: "none", Reason: "ready",
		}
		for _, pinned := range report.PinnedAgents {
			if pinned.GenerationID == generation.GenerationID && pinned.State != codexgeneration.ObligationClosed {
				generation.PinnedAgents++
			}
		}
		if route.Generation.Owner != codexgeneration.OwnerProjmuxPrivate && route.Generation.State != codexgeneration.StateRetired {
			generation.Status, generation.Action, generation.Reason = "blocked", "await-owner-stop", "foreign-owner"
			block("foreign-owner")
		} else if route.Generation.Owner == codexgeneration.OwnerProjmuxPrivate && route.Generation.State != codexgeneration.StateRetired {
			generation.BundleStatus = "ready"
			if verify == nil {
				generation.BundleStatus, generation.Status, generation.Action, generation.Reason = "unavailable", "blocked", "restore-bundle", "bundle-verifier-unavailable"
				block(generation.Reason)
			} else if identity, err := verify(route.Config.HostConfig()); err != nil {
				reason := string(codexgenerationhost.HostRefusalOf(err))
				if errors.Is(err, os.ErrNotExist) {
					reason = "bundle-missing"
				} else if reason == string(codexgenerationhost.HostRefusalNone) {
					reason = "bundle-unavailable"
				}
				generation.BundleStatus, generation.Status, generation.Action, generation.Reason = reason, "blocked", "restore-bundle", reason
				block(reason)
			} else if identity.ID != route.Generation.BundleID || identity.TUIPath != route.TUIPath {
				generation.BundleStatus, generation.Status, generation.Action, generation.Reason = "bundle-drift", "blocked", "restore-bundle", "bundle-drift"
				block("bundle-drift")
			} else {
				generation.Version = identity.Version
			}
		} else {
			generation.BundleStatus = "retired"
		}
		if generation.State == codexgeneration.StateDraining || generation.State == codexgeneration.StateHandoverPending {
			if generation.Status != "blocked" {
				generation.Status, generation.Action, generation.Reason = "action-required", "handover-required", "draining-resume-requires-handover"
				if report.Status == "ready" {
					report.Status, report.Reason = "action-required", "handover-required"
				}
			}
		}
		report.Generations = append(report.Generations, generation)
	}
	slices.SortFunc(report.Generations, func(a, b doctorCodexGeneration) int { return strings.Compare(a.GenerationID, b.GenerationID) })

	if journal.Handover != nil {
		action, index := journal.Handover.NextAction()
		op := &doctorCodexGenerationOperation{
			OperationRef: journal.Handover.OperationRef, Kind: "handover", Phase: string(journal.Handover.Phase),
			NextAction: string(action), Status: "action-required", Reason: "resume-operation",
			Timeline: doctorHandoverTimeline(*journal.Handover),
		}
		if index >= 0 {
			if action == codexgeneration.HandoverActionNoTurnChoice && index < len(journal.Handover.Choices) {
				op.TargetAgent = journal.Handover.Choices[index].AgentUID
				op.NextAction, op.Status, op.Reason = "close-or-replace", "blocked", "no-turn-choice-required"
				block("no-turn-choice-required")
			} else if index < len(journal.Handover.Targets) {
				op.TargetAgent = journal.Handover.Targets[index].AgentUID
			}
		}
		if action == codexgeneration.HandoverActionAwaitOwnerStop {
			op.Status, op.Reason = "blocked", "foreign-owner-stop-required"
			block("foreign-owner")
		} else if action == codexgeneration.HandoverActionNone {
			op.Status, op.Reason = "ready", "complete"
		}
		report.Operation = op
		mutations := journal.Handover.Mutations
		report.HandoverMutations = &mutations
	} else if journal.Operation != nil {
		next := journal.Operation.NextAction()
		op := &doctorCodexGenerationOperation{
			OperationRef: journal.Operation.OperationRef, Kind: "rolling-upgrade", Phase: string(journal.Operation.Phase),
			NextAction: string(next), Status: "ready", Reason: "complete", Timeline: []doctorCodexOperationStep{},
		}
		if next != codexgeneration.RollingActionNone || journal.Operation.Phase == codexgeneration.RollingDraining || journal.Operation.HandoverRequested {
			op.Status, op.Reason = "action-required", "resume-operation"
		}
		if journal.Operation.HandoverRequested {
			op.NextAction, op.Status, op.Reason = "handover-required", "blocked", "handover-journal-missing"
			block("handover-journal-missing")
		}
		report.Operation = op
		mutations := journal.Operation.Mutations
		report.RollingMutations = &mutations
	}
	return report
}

func doctorHandoverTimeline(operation codexgeneration.HandoverOperation) []doctorCodexOperationStep {
	targets := len(operation.Targets)
	steps := []doctorCodexOperationStep{
		{Name: "admission-fence", Count: operation.Mutations.AdmissionFence},
		{Name: "binding-fence", Count: operation.Mutations.BindingFence},
		{Name: "old-stop", Count: operation.Mutations.OldEndpointStop},
		{Name: "successor-resume", Count: operation.Mutations.SuccessorResume},
		{Name: "successor-snapshot", Count: operation.Mutations.SuccessorSnapshot},
		{Name: "endpoint-ref-cas", Count: operation.Mutations.EndpointRefCAS},
		{Name: "pane-relaunch", Count: operation.Mutations.PaneRelaunch},
		{Name: "retirement", Count: operation.Mutations.Retirement},
		{Name: "lease-release", Count: operation.Mutations.LeaseRelease},
	}
	for i := range steps {
		want := 1
		if i >= 3 && i <= 6 {
			want = targets
		}
		steps[i].State = "waiting"
		if steps[i].Count == want {
			steps[i].State = "complete"
		}
	}
	next, _ := operation.NextAction()
	name := map[codexgeneration.HandoverAction]string{
		codexgeneration.HandoverActionAdmissionFence: "admission-fence",
		codexgeneration.HandoverActionBindingFence:   "binding-fence",
		codexgeneration.HandoverActionStopOld:        "old-stop",
		codexgeneration.HandoverActionResumeTarget:   "successor-resume",
		codexgeneration.HandoverActionSnapshotTarget: "successor-snapshot",
		codexgeneration.HandoverActionCASTarget:      "endpoint-ref-cas",
		codexgeneration.HandoverActionRelaunchTarget: "pane-relaunch",
		codexgeneration.HandoverActionRetire:         "retirement",
		codexgeneration.HandoverActionReleaseLease:   "lease-release",
	}[next]
	for i := range steps {
		if steps[i].Name == name && steps[i].State != "complete" {
			steps[i].State = "next"
		}
	}
	return steps
}

func doctorPinnedAgentAction(obligation codexgeneration.AgentObligation) doctorCodexPinnedAgent {
	out := doctorCodexPinnedAgent{AgentUID: obligation.AgentUID, GenerationID: obligation.EndpointGenerationID, State: obligation.State, Status: "ready", Action: "none", Reason: "settled"}
	switch obligation.State {
	case codexgeneration.ObligationActive:
		out.Status, out.Action, out.Reason = "action-required", "resolve-active-turn", "active-turn"
	case codexgeneration.ObligationApprovalPending:
		out.Status, out.Action, out.Reason = "action-required", "review-approval", "approval-pending"
	case codexgeneration.ObligationNoTurn:
		out.Status, out.Action, out.Reason = "blocked", "close-or-replace", "no-turn-choice-required"
	case codexgeneration.ObligationUnknown:
		out.Status, out.Action, out.Reason = "blocked", "inspect-obligation", "unknown-obligation"
	case codexgeneration.ObligationCompletedPersisted:
		out.Status, out.Action, out.Reason = "action-required", "handover-required", "draining-resume-requires-handover"
	}
	return out
}

func writeDoctorCodexGenerationText(buf *bytes.Buffer, pool *doctorCodexGenerationPool) {
	if pool == nil {
		return
	}
	buf.WriteString("\nCodex generation pool\n")
	fmt.Fprintf(buf, "  Status: %s; reason: %s; state domain: %s; admission current: %s; expected multi-generation: %t; doctor mutations: %d\n",
		pool.Status, pool.Reason, pool.StateDomainID, pool.CurrentGenerationID, pool.ExpectedMultiGeneration, pool.DoctorMutations)
	if pool.Qualification != nil {
		fmt.Fprintf(buf, "  Version pair: %s -> %s; verdict: %s; reason: %s\n",
			pool.Qualification.Versions.Old, pool.Qualification.Versions.New, pool.Qualification.Verdict, pool.Qualification.Reason)
	}
	for _, generation := range pool.Generations {
		fmt.Fprintf(buf, "  Generation %s: state=%s owner=%s version=%s bundle=%s/%s pinned=%d status=%s action=%s reason=%s\n",
			generation.GenerationID, generation.State, generation.Owner, diagnosticVersionOrUnknown(generation.Version),
			generation.BundleID, generation.BundleStatus, generation.PinnedAgents, generation.Status, generation.Action, generation.Reason)
	}
	for _, agent := range pool.PinnedAgents {
		fmt.Fprintf(buf, "  Pinned Agent %s: generation=%s obligation=%s status=%s action=%s reason=%s\n",
			agent.AgentUID, agent.GenerationID, agent.State, agent.Status, agent.Action, agent.Reason)
	}
	if pool.Operation != nil {
		fmt.Fprintf(buf, "  Operation %s: kind=%s phase=%s next=%s target=%s status=%s reason=%s\n",
			pool.Operation.OperationRef, pool.Operation.Kind, pool.Operation.Phase, pool.Operation.NextAction,
			pool.Operation.TargetAgent, pool.Operation.Status, pool.Operation.Reason)
		for _, step := range pool.Operation.Timeline {
			fmt.Fprintf(buf, "    %s: state=%s count=%d\n", step.Name, step.State, step.Count)
		}
	}
}
