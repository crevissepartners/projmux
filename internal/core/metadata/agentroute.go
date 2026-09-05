package metadata

import "strings"

// ProviderAuthorityRef is a sealed union. Each adapter owns its authority
// namespace; no common epoch or nullable bag of provider fields exists.
type ProviderAuthorityRef interface {
	Provider() string
	providerAuthority()
	sameAuthority(ProviderAuthorityRef) bool
}

// AgentRouteRef addresses one stable Agent activation. It contains identity
// only, never a provider address or credential. Construct it from Registry
// evidence, then let the adapter revalidate its live authority before use.
type AgentRouteRef struct {
	AgentUID   string
	PaneUID    string
	Generation string
	authority  ProviderAuthorityRef
}

func (r AgentRouteRef) Authority() ProviderAuthorityRef { return r.authority }

func (r AgentRouteRef) Same(other AgentRouteRef) bool {
	return r.AgentUID != "" && r.PaneUID != "" && r.Generation != "" && r.authority != nil &&
		r.AgentUID == other.AgentUID && r.PaneUID == other.PaneUID && r.Generation == other.Generation &&
		r.authority.sameAuthority(other.authority)
}

// CodexRouteAuthority consumes the existing composite authority unchanged.
// It has no ownership of app-server lifecycle, upgrades, or epoch allocation.
type CodexRouteAuthority struct {
	ThreadID  string
	Authority CodexAuthorityRef
}

func (CodexRouteAuthority) Provider() string   { return "codex" }
func (CodexRouteAuthority) providerAuthority() {}
func (a CodexRouteAuthority) sameAuthority(other ProviderAuthorityRef) bool {
	b, ok := other.(CodexRouteAuthority)
	return ok && a.ThreadID != "" && a.ThreadID == b.ThreadID && a.Authority.Authorizes(b.Authority)
}

// ProcessIdentity uses the kernel's process birth identity, not a PID alone.
// Start is OS-qualified and includes the boot identity where birth is relative.
type ProcessIdentity struct {
	PID      int    `json:"pid"`
	OwnerUID uint32 `json:"ownerUID"`
	Start    string `json:"start"`
}

func (p ProcessIdentity) Valid() bool {
	return p.PID > 0 && p.Start != "" && len(p.Start) <= 160 && !strings.ContainsAny(p.Start, "\r\n\x00")
}

// ClaudeAuthorityRef has a session/process/registration lease namespace, not
// Codex connection or binding epochs. LeaseProcess is the private helper which
// retains the actual endpoint and credential only in its memory.
type ClaudeAuthorityRef struct {
	SessionID              string          `json:"sessionId"`
	Process                ProcessIdentity `json:"process"`
	RegistrationGeneration string          `json:"registrationGeneration"`
	LeaseProcess           ProcessIdentity `json:"leaseProcess"`
}

func (ClaudeAuthorityRef) Provider() string   { return "claude" }
func (ClaudeAuthorityRef) providerAuthority() {}
func (a ClaudeAuthorityRef) Valid() bool {
	return validCodexIdentityToken(a.SessionID) && a.Process.Valid() &&
		validCodexIdentityToken(a.RegistrationGeneration) && a.LeaseProcess.Valid()
}
func (a ClaudeAuthorityRef) sameAuthority(other ProviderAuthorityRef) bool {
	b, ok := other.(ClaudeAuthorityRef)
	return ok && a.Valid() && b.Valid() && a == b
}

// ResolveAgentRoute uses only exact managed ownership. Runtime liveness remains
// adapter evidence; a Registry snapshot alone never proves a live endpoint.
func ResolveAgentRoute(reg Registry, agentUID string) (AgentRouteRef, string) {
	agent, ok := reg.Agent(agentUID)
	if !ok || agent.Status.Phase != PhaseRunning || agent.Status.PaneRef == "" {
		return AgentRouteRef{}, "no current Running Agent activation"
	}
	pane, ok := reg.Pane(agent.Status.PaneRef)
	if !ok || pane.Spec.Role != PaneRoleAgent || pane.Metadata.OwnerRef == nil ||
		pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerRef.UID != agentUID ||
		pane.Status.Activation.AgentUID != agentUID {
		return AgentRouteRef{}, "managed activation ownership mismatch"
	}
	activation := pane.Status.Activation
	if activation.Generation == "" || activation.RuntimeID == "" {
		return AgentRouteRef{}, "managed activation identity is incomplete"
	}
	ref := AgentRouteRef{AgentUID: agentUID, PaneUID: pane.Metadata.UID, Generation: activation.Generation}
	switch agent.Spec.Provider {
	case "codex":
		binding := activation.Codex
		durable := agent.Status.SessionRef
		if binding == nil || binding.Authority == nil || !binding.Authority.Valid() || binding.ThreadID == "" ||
			durable == nil || durable.Provider != "codex" || durable.Codex == nil || durable.Codex.ThreadID != binding.ThreadID ||
			durable.Codex.Endpoint == nil || !durable.Codex.Endpoint.Same(binding.Authority.Endpoint()) {
			return AgentRouteRef{}, "Codex composite authority is unavailable"
		}
		ref.authority = CodexRouteAuthority{ThreadID: binding.ThreadID, Authority: *binding.Authority}
	case "claude":
		binding := activation.Claude
		if binding == nil || binding.Registration == nil || !binding.Registration.Authority.Valid() ||
			binding.RegistrationGeneration != binding.Registration.Authority.RegistrationGeneration ||
			binding.RegistrationSessionID != binding.Registration.Authority.SessionID ||
			binding.Process != binding.Registration.Authority.Process || !binding.Registration.Ready {
			return AgentRouteRef{}, "Claude registration lease is unavailable"
		}
		ref.authority = binding.Registration.Authority
	default:
		return AgentRouteRef{}, "provider has no endpoint adapter"
	}
	return ref, ""
}
