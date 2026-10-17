// Package agentcatalog shares read-only provider and Agent projections between CLI and SDK.
package agentcatalog

import (
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claude "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

type Probes struct {
	Lease         func(string, coremetadata.AgentRouteRef) bool
	Qualification func(string, coremetadata.AgentRouteRef) bool
	ProcessAlive  func(coremetadata.ProcessIdentity) bool
}

func DefaultProbes() Probes {
	return Probes{Lease: claude.RegistrationLease, Qualification: claude.CoordinationEligible, ProcessAlive: ProcessAlive}
}
func ProcessAlive(process coremetadata.ProcessIdentity) bool {
	if !process.Valid() {
		return false
	}
	observed, _, err := localipc.Process(process.PID)
	return err == nil && observed == process
}
func (p Probes) Project(registry coremetadata.Registry, agent coremetadata.Agent, provider aiprovider.ID, registryPath string) Capabilities {
	projection := Exact(registry, agent, provider)
	if provider == aiprovider.Claude {
		projection.Runtime.Coordination = p.CoordinationAt(registry, agent, registryPath)
		sourceReady := false
		if route, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID); reason == "" && registryPath != "" {
			sourceReady = p.Lease(registryPath, route)
		}
		for i := range projection.Capabilities {
			e := &projection.Capabilities[i]
			if e.Action != "message.send" && e.Action != "message.status" {
				continue
			}
			e.Available = &sourceReady
			e.Evidence = "local-registration-lease"
			if sourceReady {
				e.Reason = "exact Claude source registration lease is ready"
			} else {
				e.Reason = "exact Claude source registration lease is stale or unavailable"
			}
		}
	}
	return projection
}

type Capabilities struct {
	Provider     aiprovider.ID `json:"provider"`
	Agent        *Agent        `json:"agent,omitempty"`
	Runtime      *Runtime      `json:"runtimeEligibility,omitempty"`
	Capabilities []Capability  `json:"capabilities"`
}

type Agent struct {
	UID   string                  `json:"uid"`
	Name  string                  `json:"name"`
	Phase coremetadata.AgentPhase `json:"phase"`
}

type Runtime struct {
	Ready                bool          `json:"registryReady"`
	Evidence             string        `json:"evidence"`
	LiveVerified         bool          `json:"liveVerified"`
	Reason               string        `json:"reason"`
	PaneUID              string        `json:"paneUID,omitempty"`
	PaneRuntimeID        string        `json:"paneRuntimeID,omitempty"`
	ActivationGeneration string        `json:"activationGeneration,omitempty"`
	RouteIncarnation     string        `json:"routeIncarnation,omitempty"`
	StateDomainID        string        `json:"stateDomainID,omitempty"`
	EndpointGenerationID string        `json:"endpointGenerationID,omitempty"`
	BrokerRuntimeID      string        `json:"brokerRuntimeID,omitempty"`
	ConnectionEpoch      uint64        `json:"connectionEpoch,omitempty"`
	BindingEpoch         uint64        `json:"bindingEpoch,omitempty"`
	Coordination         *Coordination `json:"coordination,omitempty"`
}

type Coordination struct {
	Eligible bool   `json:"eligible"`
	Evidence string `json:"evidence"`
	Reason   string `json:"reason"`
	Recovery string `json:"recovery,omitempty"`
}

type Capability struct {
	Action              string                         `json:"action"`
	Group               string                         `json:"group"`
	Callable            bool                           `json:"callable"`
	Mode                aiprovider.AgentSupportMode    `json:"mode"`
	CompletionPrecision aiprovider.CompletionPrecision `json:"completionPrecision"`
	Available           *bool                          `json:"available,omitempty"`
	Evidence            string                         `json:"evidence,omitempty"`
	Reason              string                         `json:"reason,omitempty"`
}

func Static(provider aiprovider.ID) Capabilities {
	projection := Capabilities{Provider: provider}
	for _, action := range aiprovider.AgentActions() {
		_, cell, ok := aiprovider.LookupAgentCapability(action.ID, provider)
		if !ok {
			continue
		}
		projection.Capabilities = append(projection.Capabilities, Capability{
			Action: action.ID, Group: action.Group, Callable: action.Callable,
			Mode: cell.Mode, CompletionPrecision: cell.CompletionPrecision,
		})
	}
	return projection
}

func Exact(registry coremetadata.Registry, agent coremetadata.Agent, provider aiprovider.ID) Capabilities {
	projection := Static(provider)
	projection.Agent = &Agent{UID: agent.Metadata.UID, Name: agent.Metadata.Name, Phase: agent.Status.Phase}
	runtime := RuntimeEligibility(registry, agent)
	projection.Runtime = &runtime
	for i := range projection.Capabilities {
		available, reason := ActionEligibility(registry, agent, projection.Capabilities[i].Action, projection.Capabilities[i].Mode)
		projection.Capabilities[i].Available = &available
		projection.Capabilities[i].Evidence = "registry"
		projection.Capabilities[i].Reason = reason
	}
	return projection
}

func RuntimeEligibility(registry coremetadata.Registry, agent coremetadata.Agent) Runtime {
	runtime := Runtime{Evidence: "registry", Reason: "agent has no current Running Pane binding"}
	if agent.Status.Phase != coremetadata.PhaseRunning || strings.TrimSpace(agent.Status.PaneRef) == "" {
		return runtime
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != agent.Metadata.UID {
		runtime.Reason = "current Pane ownership is not exact"
		return runtime
	}
	runtime.PaneUID = pane.Metadata.UID
	runtime.PaneRuntimeID = pane.Status.Activation.RuntimeID
	runtime.ActivationGeneration = pane.Status.Activation.Generation
	if pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
		if !currentProcessActivation(registry, agent, *pane) {
			runtime.Reason = "current Pane activation identity is incomplete"
			return runtime
		}
		runtime.Ready = true
		runtime.Reason = "current Registry activation is ready; mutating commands revalidate live provider authority"
		return runtime
	}
	if runtime.PaneRuntimeID == "" || runtime.ActivationGeneration == "" || pane.Status.Activation.AgentUID != agent.Metadata.UID {
		runtime.Reason = "current Pane activation identity is incomplete"
		return runtime
	}
	runtime.Ready = true
	runtime.Reason = "current Registry activation is ready; mutating commands revalidate live provider authority"
	if route, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID); reason == "" {
		runtime.RouteIncarnation = route.Incarnation()
	}
	if pane.Status.Activation.Codex != nil && pane.Status.Activation.Codex.Authority != nil {
		authority := pane.Status.Activation.Codex.Authority
		runtime.StateDomainID = authority.StateDomainID
		runtime.EndpointGenerationID = authority.EndpointGenerationID
		runtime.BrokerRuntimeID = authority.BrokerRuntimeID
		runtime.ConnectionEpoch = authority.ConnectionEpoch
		runtime.BindingEpoch = authority.BindingEpoch
	}
	return runtime
}

func ActionEligibility(registry coremetadata.Registry, agent coremetadata.Agent, action string, mode aiprovider.AgentSupportMode) (bool, string) {
	if mode == aiprovider.SupportUnsupported {
		return false, "unsupported by provider"
	}
	if strings.HasPrefix(action, "integrate.") || strings.HasPrefix(action, "app-server.") || action == "usage" {
		return true, "action is not bound to one Agent runtime"
	}
	switch action {
	case "status.set":
		if agent.Status.Phase != coremetadata.PhaseRunning || strings.TrimSpace(agent.Status.PaneRef) == "" {
			return false, "requires a Running Agent with a current managed Pane"
		}
	case "stop":
		pane, ok := registry.Pane(agent.Status.PaneRef)
		if !ok || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || pane.Status.ProcessSession == nil {
			return false, "requires a process Agent"
		}
		if pane.Status.Activation.IsZero() && agent.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) && coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
			return true, "exact process Wait is already recorded"
		}
		if pane.Status.Activation.Process == nil || agent.Status.Phase != coremetadata.PhaseRunning {
			return false, "requires a live process owner or exact stopped Wait evidence"
		}
		return true, "exact process generation requires durable Wait confirmation"
	case "resume":
		if !slices.Contains([]coremetadata.AgentPhase{coremetadata.PhaseOffline, coremetadata.PhaseFailed}, agent.Status.Phase) {
			return false, "requires an Offline or Failed Agent"
		}
		if agent.Status.SessionRef == nil || agent.Status.SessionRef.Provider != agent.Spec.Provider || agent.Status.SessionRef.ConversationID() == "" {
			return false, "provider conversation identity is unavailable"
		}
	case "persona.attach", "persona.detach", "relaunch":
		if agent.Status.Phase != coremetadata.PhaseRunning && !slices.Contains([]coremetadata.AgentPhase{coremetadata.PhaseOffline, coremetadata.PhaseFailed}, agent.Status.Phase) {
			return false, "requires a Running, Offline, or Failed Agent"
		}
		if agent.Status.SessionRef == nil || agent.Status.SessionRef.ConversationID() == "" {
			return false, "provider conversation identity is unavailable"
		}
	case "review":
		if reason := ReviewReason(registry, agent); reason != "" {
			return false, reason
		}
	case "message.send", "message.status":
		if _, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID); reason != "" {
			return false, reason
		}
	case "wait.idle":
		if reason := ActivationReason(registry, agent); reason != "" {
			return false, reason
		}
	default:
		if mode == aiprovider.SupportNativeExact {
			if reason := ControlReason(registry, agent); reason != "" {
				return false, reason
			}
		}
	}
	return true, "eligible from current Registry evidence"
}

func ActivationReason(registry coremetadata.Registry, agent coremetadata.Agent) string {
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
		return "requires a Running Agent with a current managed Pane"
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if ok && pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
		if currentProcessActivation(registry, agent, *pane) {
			return ""
		}
		return "managed Agent activation identity is incomplete"
	}
	if !ok || pane.Spec.Role != coremetadata.PaneRoleAgent || pane.Metadata.OwnerRef == nil ||
		pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != agent.Metadata.UID ||
		pane.Status.Activation.AgentUID != agent.Metadata.UID || pane.Status.Activation.Generation == "" || pane.Status.Activation.RuntimeID == "" {
		return "managed Agent activation identity is incomplete"
	}
	return ""
}

func currentProcessActivation(registry coremetadata.Registry, agent coremetadata.Agent, pane coremetadata.Pane) bool {
	if pane.Status.ProcessSession == nil {
		return false
	}
	binding := pane.Status.ProcessSession.Binding
	if binding.AgentUID != agent.Metadata.UID || binding.PaneUID != agent.Status.PaneRef {
		return false
	}
	_, provider, ok := registry.CurrentProcessActivation(binding)
	return ok && provider == agent.Spec.Provider && provider == pane.Status.ProcessSession.Provider
}

func ReviewReason(registry coremetadata.Registry, agent coremetadata.Agent) string {
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
		return "requires a Running Agent with a current managed Pane"
	}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Provider != string(aiprovider.Codex) || agent.Status.SessionRef.Codex == nil || strings.TrimSpace(agent.Status.SessionRef.Codex.ThreadID) == "" {
		return "exact Codex thread identity is unavailable"
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != agent.Metadata.UID || pane.Status.Activation.Generation == "" {
		return "exact managed Pane generation is unavailable"
	}
	return ""
}

func ControlReason(registry coremetadata.Registry, agent coremetadata.Agent) string {
	if reason := ReviewReason(registry, agent); reason != "" {
		return reason
	}
	pane, _ := registry.Pane(agent.Status.PaneRef)
	if pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.Authority == nil || !pane.Status.Activation.Codex.Authority.Valid() {
		return "exact Codex connection and binding epoch is unavailable"
	}
	if pane.Status.Activation.Codex.ThreadID != agent.Status.SessionRef.Codex.ThreadID {
		return "Codex activation thread does not match the durable Agent thread"
	}
	if agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Same(pane.Status.Activation.Codex.Authority.Endpoint()) {
		return "Codex activation epoch does not match the durable endpoint"
	}
	return ""
}

func (p Probes) CoordinationAt(registry coremetadata.Registry, agent coremetadata.Agent, registryPath string) *Coordination {
	projection := &Coordination{Evidence: "local-registration-lease"}
	// recovery is the original one-size-fits-all sentence. It now reaches only
	// the route reasons that are not a missing Claude registration -- states in
	// which no activation is Running, so the resume it names is callable. Every
	// other branch below says what its own state can actually run.
	recovery := "let the existing Claude session exit normally without interrupting its active tool, then projmux agent resume uid:" + agent.Metadata.UID + " --dialogue-reply-only (same Agent UID and conversation; explicit next-activation tool policy); from the exact current Codex source, projmux agent message qualify uid:" + agent.Metadata.UID + " --confirm-isolated-provider-push -o json (fresh current observer; no Agent recreation)"
	route, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID)
	if reason != "" {
		projection.Reason = reason
		projection.Recovery = recovery
		// A missing registration is three different situations with three
		// different next actions, and the generic recovery above is executable
		// in none of them: `agent resume` refuses a Running Agent and
		// `agent message qualify` needs the lease that is missing. Say which
		// shape this is and what actually re-registers it.
		if shape := p.RegistrationShape(registry, agent); reason == coremetadata.ClaudeRegistrationUnavailableReason && shape.Diagnosis() != "" {
			projection.Reason = reason + "; " + claudeRegistrationDiagnosis(agent, shape)
			projection.Recovery = claudeRegistrationNextAction(shape, agent.Metadata.UID)
		}
		return projection
	}
	if registryPath == "" || !p.Lease(registryPath, route) {
		projection.Reason = "Claude registration lease is stale or unavailable"
		projection.Recovery = claudeLeaseProbeNextAction(agent)
		return projection
	}
	if !p.Qualification(registryPath, route) {
		projection.Reason = "Claude coordination is unqualified for the exact running provider version"
		projection.Recovery = claudeUnqualifiedNextAction(agent)
		return projection
	}
	projection.Eligible = true
	projection.Evidence = "helper-memory-exact-version-qualification"
	projection.Reason = "exact local registration and current-version qualification are ready for coordination delivery"
	return projection
}

// claudeLeaseProbeNextAction answers the branch whose registration is present
// in the Registry and whose live lease probe did not come back.
//
// This is not the missing-registration case wearing different words. The route
// resolved, so classifyAgentClaudeRegistration reports ready and has no next
// action to lend. What failed is a probe with four fixed 200ms deadlines
// (probeClaudeRegistrationLease in claude_endpoint.go), and under load those
// expire while the helper is alive: the measured instance retried a minute
// later and was delivered. Telling that operator to restart or re-register a
// session that never lost its registration destroys a live Agent to fix
// nothing, so this names a retry and a liveness read instead.
func claudeLeaseProbeNextAction(agent coremetadata.Agent) string {
	next := "the Registry still holds this Agent's registration, so the live probe did not answer inside its fixed deadline rather than the session losing its lease: re-run projmux agent capabilities uid:" + agent.Metadata.UID + " -o json after about a minute, because that probe has been measured to answer on a later attempt while the helper stayed alive under load"
	if agent.Status.PaneRef != "" {
		next += "; read the recorded lease process with projmux describe pane uid:" + agent.Status.PaneRef + " -o json (status.activation.claude.registration.authority.leaseProcess) and confirm that pid is still on the host"
	}
	return next + ". Do not re-register or restart this Claude session: the registration this projection read is not the thing that failed, and ending the session throws away the activation the retry needs."
}

// claudeUnqualifiedNextAction answers the branch whose lease is ready and whose
// only gap is current-version qualification.
//
// `agent message qualify` is genuinely callable here -- it refuses on a missing
// lease, and this branch is reached only after the lease probe passed -- so the
// sentence keeps it and states the two preconditions it really has: the caller
// must be the exact current Codex source, and the activation must expose a
// reply-only observer for evidence. What the sentence drops is the `otherwise`
// tail, which handed a Running Agent a resume it refuses. When the precondition
// is not met there is no in-place command, and saying so is the honest answer.
func claudeUnqualifiedNextAction(agent coremetadata.Agent) string {
	return "the registration lease is ready and only the exact-version qualification is missing, so nothing here needs re-registering: if this activation has the reply-only profile ready, from the exact current Codex source run projmux agent message qualify uid:" + agent.Metadata.UID + " --confirm-isolated-provider-push -o json (fresh current observer; no Agent recreation). If this activation has no reply-only profile, no command qualifies it in place -- that profile is fixed when the activation starts, so only a replacement Claude activation carrying it can qualify, and this Agent has to stop Running before one can take its place."
}

func (p Probes) RegistrationShape(registry coremetadata.Registry, agent coremetadata.Agent) coremetadata.ClaudeRegistrationShape {
	if agent.Spec.Provider != string(aiprovider.Claude) || agent.Status.PaneRef == "" {
		return coremetadata.ClaudeRegistrationReady
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok {
		return coremetadata.ClaudeRegistrationReady
	}
	alive := false
	if binding := pane.Status.Activation.Claude; binding != nil {
		alive = p.ProcessAlive(binding.Process)
	}
	return coremetadata.ClassifyClaudeRegistration(*pane, alive)
}

// claudeRegistrationNextAction is what actually works for one shape.
//
// Every sentence here was measured rather than guessed. The string this
// replaced named `agent resume --dialogue-reply-only` and `agent message
// qualify` for all three shapes: resume refuses a Running Agent, and qualify
// needs the very lease that is missing, so the product's only recovery advice
// was unexecutable in each case it was printed for.
func claudeRegistrationNextAction(shape coremetadata.ClaudeRegistrationShape, agentUID string) string {
	switch shape {
	case coremetadata.ClaudeRegistrationNeverStarted:
		return "install the registration hook with `projmux agent integrate claude`, then make that session run SessionStart again: start a new Claude session in its Pane, or let this one exit and run `projmux agent resume uid:" + agentUID + "` to rebind the same Agent UID"
	case coremetadata.ClaudeRegistrationLost:
		return "the hook is installed and the provider process is still alive, so one new SessionStart re-registers it: start a new Claude session in its Pane; neither an Agent recreation nor a rebind is needed"
	case coremetadata.ClaudeRegistrationSessionGone:
		return "nothing can re-register this session, because the process that would run SessionStart is gone; run `projmux agent resume uid:" + agentUID + "` once the Agent is no longer Running, or create a replacement Agent"
	default:
		return ""
	}
}

// claudeRegistrationDiagnosis names which Agent has no lease and why, without
// naming a command. It is the half that belongs in a `reason` field.
func claudeRegistrationDiagnosis(agent coremetadata.Agent, shape coremetadata.ClaudeRegistrationShape) string {
	diagnosis := shape.Diagnosis()
	if diagnosis == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("agent/")
	builder.WriteString(agent.Metadata.Name)
	builder.WriteString(" uid:")
	builder.WriteString(agent.Metadata.UID)
	builder.WriteString(" ")
	builder.WriteString(diagnosis)
	return builder.String()
}
