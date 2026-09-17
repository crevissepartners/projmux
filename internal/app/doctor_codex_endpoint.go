package app

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const doctorCodexEndpointRisk = "replacement-interruption-observed"

// This is a snapshot diagnostic, not a prediction or a lifecycle authority.
// IDs remain exact in Doctor; support reports retain their normal ID hashing.
type doctorCodexEndpointMismatch struct {
	Status              string                          `json:"status"`
	Reason              string                          `json:"reason,omitempty"`
	Agents              int                             `json:"agents"`
	RunningVersion      string                          `json:"running_version,omitempty"`
	RunningGenerationID string                          `json:"running_generation_id,omitempty"`
	Mismatches          []doctorCodexEndpointAgent      `json:"mismatches,omitempty"`
	UnobservedAgents    []doctorCodexEndpointUnobserved `json:"unobserved_agents,omitempty"`
	Risk                string                          `json:"risk,omitempty"`
	ObservedOn          string                          `json:"observed_on,omitempty"`
	Observations        int                             `json:"observations,omitempty"`
	Causality           string                          `json:"causality,omitempty"`
	RetiredRefs         []doctorCodexRetiredRef         `json:"retired_refs,omitempty"`
}

// doctorCodexRetiredRef is one resumable Codex Agent whose stored endpoint is
// not the default daemon endpoint, or still carries a draining /
// handover-pending marker of a retired private generation. It states what
// `projmux agent resume` will do and the next command. It is judged from the
// Registry and the default endpoint only.
type doctorCodexRetiredRef struct {
	AgentUID             string `json:"agent_uid"`
	EndpointGenerationID string `json:"endpoint_generation_id"`
	GenerationState      string `json:"generation_state"`
	Resume               string `json:"resume"`
	Reason               string `json:"reason,omitempty"`
	Next                 string `json:"next"`
}

const (
	doctorCodexRetiredResumeSwitches = "switches-to-default-endpoint"
	doctorCodexRetiredResumeRefused  = "refused"
)

type doctorCodexEndpointAgent struct {
	AgentUID             string `json:"agent_uid"`
	EndpointGenerationID string `json:"endpoint_generation_id"`
}

type doctorCodexEndpointUnobserved struct {
	AgentUID string `json:"agent_uid,omitempty"`
	Reason   string `json:"reason"`
}

// All current Codex activations in the Registry are considered, across Projects
// and Windows. A Running Agent with missing activation evidence is not silently
// treated as matching. Durable offline conversations are not current activations.
// Only the existing default endpoint's codex-<running-version> identity can be
// compared: a broker's published key does not establish which executable is
// now serving an activation after replacement.
func diagnoseCodexEndpointMismatch(registry coremetadata.Registry, registryErr error, domain string, domainErr error, health *codexappserver.Health) *doctorCodexEndpointMismatch {
	out := &doctorCodexEndpointMismatch{Status: "complete"}
	if registryErr != nil {
		out.Status, out.Reason = "unavailable", "registry-unavailable"
		return out
	}
	if domainErr != nil || domain == "" {
		out.Reason = "state-domain-unavailable"
	} else if health == nil || health.EndpointReadiness != codexappserver.EndpointReady ||
		!plainCodexEndpointVersion(health.RunningVersion) {
		out.Reason = "running-endpoint-unavailable"
	} else {
		out.RunningVersion = health.RunningVersion
		out.RunningGenerationID = "codex-" + health.RunningVersion
	}
	seenPanes := map[string]bool{}
	for _, agent := range registry.Agents {
		if agent.Spec.Provider != aiModeCodex || (agent.Status.Phase != coremetadata.PhaseRunning && agent.Status.PaneRef == "") {
			continue
		}
		out.Agents++
		pane, ok := registry.Pane(agent.Status.PaneRef)
		if !ok {
			out.unobserved(agent.Metadata.UID, "activation-missing")
			continue
		}
		seenPanes[pane.Metadata.UID] = true
		endpoint, reason := doctorCodexActivationEndpoint(agent, *pane)
		if reason == "" {
			reason = out.compare(agent.Metadata.UID, endpoint, domain)
		}
		if reason != "" {
			out.unobserved(agent.Metadata.UID, reason)
		}
	}
	// Orphaned or misattributed Codex activations cannot disappear just because
	// their Agent no longer supplies the expected forward reference.
	for _, pane := range registry.Panes {
		if pane.Status.Activation.Codex != nil && !seenPanes[pane.Metadata.UID] {
			out.unobserved(pane.Status.Activation.AgentUID, "activation-agent-unresolved")
		}
	}
	out.RetiredRefs = diagnoseCodexRetiredRefs(registry, domain, domainErr, health)
	if len(out.Mismatches) == 0 && len(out.UnobservedAgents) == 0 && len(out.RetiredRefs) == 0 {
		return nil // zero mismatches and no enumeration gap: no diagnostic row
	}
	if len(out.UnobservedAgents) > 0 {
		out.Status = "incomplete"
	}
	if len(out.Mismatches) > 0 {
		out.Risk, out.ObservedOn, out.Observations, out.Causality = doctorCodexEndpointRisk, "2026-09-09", 1, "undetermined"
	}
	slices.SortFunc(out.Mismatches, func(a, b doctorCodexEndpointAgent) int { return strings.Compare(a.AgentUID, b.AgentUID) })
	slices.SortFunc(out.UnobservedAgents, func(a, b doctorCodexEndpointUnobserved) int {
		return strings.Compare(a.AgentUID+":"+a.Reason, b.AgentUID+":"+b.Reason)
	})
	return out
}

func diagnoseCodexRetiredRefs(registry coremetadata.Registry, domain string, domainErr error, health *codexappserver.Health) []doctorCodexRetiredRef {
	if domainErr != nil || domain == "" {
		return nil
	}
	runningGenerationID := ""
	if health != nil && health.EndpointReadiness == codexappserver.EndpointReady && plainCodexEndpointVersion(health.RunningVersion) {
		runningGenerationID = "codex-" + health.RunningVersion
	}
	var out []doctorCodexRetiredRef
	for _, agent := range registry.Agents {
		ref := agent.Status.SessionRef
		if agent.Spec.Provider != aiModeCodex || !slices.Contains(resumableAgentPhases, agent.Status.Phase) ||
			ref == nil || ref.Codex == nil || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Valid() ||
			ref.Codex.Lifecycle == nil || !ref.Codex.Lifecycle.ValidFor(ref.Codex.Endpoint) {
			continue
		}
		endpoint, state := *ref.Codex.Endpoint, ref.Codex.Lifecycle.State
		row := doctorCodexRetiredRef{AgentUID: agent.Metadata.UID, EndpointGenerationID: endpoint.EndpointGenerationID, GenerationState: string(state)}
		resume := "uid:" + agent.Metadata.UID
		switch {
		case endpoint.StateDomainID != domain:
			refusal := codexRetiredGenerationRefusal(codexNativeReasonRetiredStateDomain, "", resume)
			row.Resume, row.Reason, row.Next = doctorCodexRetiredResumeRefused, refusal.Reason, refusal.OperatorAction
		case state == coremetadata.CodexGenerationDraining || state == coremetadata.CodexGenerationHandoverPending ||
			(runningGenerationID != "" && endpoint.EndpointGenerationID != runningGenerationID):
			row.Resume, row.Next = doctorCodexRetiredResumeSwitches, "`projmux agent resume "+resume+"`"
		default:
			continue
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b doctorCodexRetiredRef) int { return strings.Compare(a.AgentUID, b.AgentUID) })
	return out
}

func doctorCodexActivationEndpoint(agent coremetadata.Agent, pane coremetadata.Pane) (coremetadata.CodexEndpointRef, string) {
	activation := pane.Status.Activation
	if pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent ||
		pane.Metadata.OwnerRef.UID != agent.Metadata.UID || activation.AgentUID != agent.Metadata.UID ||
		activation.Generation == "" || activation.RuntimeID == "" {
		return coremetadata.CodexEndpointRef{}, "activation-agent-unresolved"
	}
	if activation.Codex == nil || activation.Codex.Authority == nil {
		return coremetadata.CodexEndpointRef{}, "activation-endpoint-missing"
	}
	endpoint := activation.Codex.Authority.Endpoint()
	if !endpoint.Valid() {
		return coremetadata.CodexEndpointRef{}, "activation-endpoint-invalid"
	}
	if ref := agent.Status.SessionRef; ref != nil && ref.Codex != nil && ref.Codex.Endpoint != nil && !ref.Codex.Endpoint.Same(endpoint) {
		return coremetadata.CodexEndpointRef{}, "activation-endpoint-conflict"
	}
	return endpoint, ""
}

func (out *doctorCodexEndpointMismatch) compare(uid string, endpoint coremetadata.CodexEndpointRef, domain string) string {
	if out.Reason != "" {
		return out.Reason
	}
	if endpoint.StateDomainID != domain {
		return "endpoint-domain-unobserved"
	}
	version, external := strings.CutPrefix(endpoint.EndpointGenerationID, "codex-")
	if !external || !plainCodexEndpointVersion(version) {
		return "endpoint-generation-unobserved"
	}
	if endpoint.EndpointGenerationID != out.RunningGenerationID {
		out.Mismatches = append(out.Mismatches, doctorCodexEndpointAgent{AgentUID: uid, EndpointGenerationID: endpoint.EndpointGenerationID})
	}
	return ""
}

func plainCodexEndpointVersion(version string) bool {
	return !strings.Contains(version, "/") && codexappserver.IsSafeDiagnosticVersion(version)
}

func (out *doctorCodexEndpointMismatch) unobserved(uid, reason string) {
	out.UnobservedAgents = append(out.UnobservedAgents, doctorCodexEndpointUnobserved{AgentUID: uid, Reason: reason})
}

func writeDoctorCodexEndpointMismatchText(buf *bytes.Buffer, report *doctorCodexEndpointMismatch) {
	if report == nil {
		return
	}
	buf.WriteString("\nCodex endpoint generation comparison\n")
	fmt.Fprintf(buf, "  Enumeration: %s; Codex Agent records: %d", report.Status, report.Agents)
	if report.Reason != "" {
		fmt.Fprintf(buf, "; reason: %s", report.Reason)
	}
	buf.WriteString("\n")
	if len(report.Mismatches) > 0 {
		fmt.Fprintf(buf, "  Observed risk: interruption during endpoint replacement (%s, n=%d); causality undetermined; interruption is not certain. Valid only for this observation, before the next replacement.\n", report.ObservedOn, report.Observations)
		for _, agent := range report.Mismatches {
			fmt.Fprintf(buf, "  Mismatch Agent uid:%s: activation=%s; running=%s (version %s)\n", agent.AgentUID, agent.EndpointGenerationID, report.RunningGenerationID, report.RunningVersion)
		}
	}
	for _, agent := range report.UnobservedAgents {
		fmt.Fprintf(buf, "  Unobserved Agent uid:%s: %s; mismatch enumeration may be incomplete\n", agent.AgentUID, agent.Reason)
	}
	for _, ref := range report.RetiredRefs {
		if ref.Resume == doctorCodexRetiredResumeRefused {
			fmt.Fprintf(buf, "  Retired Codex ref Agent uid:%s: endpoint=%s lifecycle=%s; resume refused (%s); next: %s\n",
				ref.AgentUID, ref.EndpointGenerationID, ref.GenerationState, ref.Reason, ref.Next)
			continue
		}
		fmt.Fprintf(buf, "  Retired Codex ref Agent uid:%s: endpoint=%s lifecycle=%s; resume switches to the default endpoint "+
			"(refused as %s while a retired private Codex app-server still listens on its socket); next: %s\n",
			ref.AgentUID, ref.EndpointGenerationID, ref.GenerationState, codexNativeReasonRetiredRunning, ref.Next)
	}
}

// Scope the support allowlist to this projection. Agent and generation IDs
// still use the existing support hashes, and arbitrary diagnostic text is never
// admitted just because it shares a generic status/reason key.
func safeDoctorCodexEndpointMismatchString(parent, key, value string) bool {
	if parent == "codex_endpoint_mismatch" {
		switch key {
		case "status":
			return value == "complete" || value == "incomplete" || value == "unavailable"
		case "risk":
			return value == doctorCodexEndpointRisk
		case "observed_on":
			return value == "2026-09-09"
		case "causality":
			return value == "undetermined"
		case "running_version":
			return plainCodexEndpointVersion(value)
		}
	}
	if parent == "retired_refs" {
		switch key {
		case "resume":
			return value == doctorCodexRetiredResumeSwitches || value == doctorCodexRetiredResumeRefused
		case "reason":
			return value == codexNativeReasonRetiredStateDomain
		case "generation_state":
			return validAIResumeGenerationState(coremetadata.CodexGenerationState(value))
		}
	}
	if (parent == "codex_endpoint_mismatch" || parent == "unobserved_agents") && key == "reason" {
		switch value {
		case "registry-unavailable", "state-domain-unavailable", "running-endpoint-unavailable",
			"activation-missing", "activation-agent-unresolved", "activation-endpoint-missing",
			"activation-endpoint-invalid", "activation-endpoint-conflict", "endpoint-domain-unobserved",
			"endpoint-generation-unobserved":
			return true
		}
	}
	return false
}
