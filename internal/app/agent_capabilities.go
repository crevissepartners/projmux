package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

type agentCapabilityProjection struct {
	Provider     aiprovider.ID                    `json:"provider"`
	Agent        *agentCapabilityAgent            `json:"agent,omitempty"`
	Runtime      *agentCapabilityRuntime          `json:"runtimeEligibility,omitempty"`
	Capabilities []agentCapabilityProjectionEntry `json:"capabilities"`
}

type agentCapabilityAgent struct {
	UID   string                  `json:"uid"`
	Name  string                  `json:"name"`
	Phase coremetadata.AgentPhase `json:"phase"`
}

type agentCapabilityRuntime struct {
	Ready                bool                         `json:"registryReady"`
	Evidence             string                       `json:"evidence"`
	LiveVerified         bool                         `json:"liveVerified"`
	Reason               string                       `json:"reason"`
	PaneUID              string                       `json:"paneUID,omitempty"`
	PaneRuntimeID        string                       `json:"paneRuntimeID,omitempty"`
	ActivationGeneration string                       `json:"activationGeneration,omitempty"`
	RouteIncarnation     string                       `json:"routeIncarnation,omitempty"`
	StateDomainID        string                       `json:"stateDomainID,omitempty"`
	EndpointGenerationID string                       `json:"endpointGenerationID,omitempty"`
	BrokerRuntimeID      string                       `json:"brokerRuntimeID,omitempty"`
	ConnectionEpoch      uint64                       `json:"connectionEpoch,omitempty"`
	BindingEpoch         uint64                       `json:"bindingEpoch,omitempty"`
	Coordination         *agentCapabilityCoordination `json:"coordination,omitempty"`
}

type agentCapabilityCoordination struct {
	Eligible bool   `json:"eligible"`
	Evidence string `json:"evidence"`
	Reason   string `json:"reason"`
	Recovery string `json:"recovery,omitempty"`
}

type agentCapabilityProjectionEntry struct {
	Action              string                         `json:"action"`
	Group               string                         `json:"group"`
	Callable            bool                           `json:"callable"`
	Mode                aiprovider.AgentSupportMode    `json:"mode"`
	CompletionPrecision aiprovider.CompletionPrecision `json:"completionPrecision"`
	Available           *bool                          `json:"available,omitempty"`
	Evidence            string                         `json:"evidence,omitempty"`
	Reason              string                         `json:"reason,omitempty"`
}

func (c *agentCommand) runCapabilities(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent capabilities"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var provider, output string
	var jsonOutput bool
	fs.StringVar(&provider, "provider", "", "provider id: codex, claude, or antigravity")
	fs.StringVar(&output, "o", "", "output mode: json")
	fs.BoolVar(&jsonOutput, "json", false, "emit JSON")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if len(refs) > 1 {
		return usageError(spelling + " accepts at most one Agent reference")
	}
	if len(refs) == 1 && strings.TrimSpace(provider) != "" {
		return usageError(spelling + " accepts an Agent reference or --provider, not both")
	}
	if output != "" && output != "json" {
		return usageError(fmt.Sprintf("%s: unsupported output mode %q", spelling, output))
	}
	jsonOutput = jsonOutput || output == "json"

	var projection agentCapabilityProjection
	if strings.TrimSpace(provider) != "" {
		metadata, ok := aiprovider.Lookup(provider)
		if !ok {
			return usageError(fmt.Sprintf("%s: unsupported provider %q", spelling, provider))
		}
		projection = projectStaticAgentCapabilities(metadata.ID)
	} else {
		ref := ""
		if len(refs) == 1 {
			ref = refs[0]
		}
		registry, agent, resolveErr := c.resolveOneAgent(spelling, ref, selector.VerbStatus)
		if resolveErr != nil {
			return resolveErr
		}
		metadata, ok := aiprovider.Lookup(agent.Spec.Provider)
		if !ok {
			return fmt.Errorf("%s: agent/%s has unsupported provider %q", spelling, agent.Metadata.Name, agent.Spec.Provider)
		}
		projection = projectExactAgentCapabilities(registry, agent, metadata.ID)
		if metadata.ID == aiprovider.Claude {
			projection.Runtime.Coordination = projectClaudeCoordinationEligibility(registry, agent)
			sourceReady := false
			if route, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID); reason == "" {
				if paths, pathErr := config.DefaultPathsFromEnv(); pathErr == nil {
					sourceReady = probeClaudeRegistrationLease(intmetadata.PathFor(paths.StateDir), route)
				}
			}
			for i := range projection.Capabilities {
				if projection.Capabilities[i].Action != "message.send" && projection.Capabilities[i].Action != "message.status" {
					continue
				}
				projection.Capabilities[i].Available = &sourceReady
				projection.Capabilities[i].Evidence = "local-registration-lease"
				if sourceReady {
					projection.Capabilities[i].Reason = "exact Claude source registration lease is ready"
				} else {
					projection.Capabilities[i].Reason = "exact Claude source registration lease is stale or unavailable"
				}
			}
		}
	}
	return writeAgentCapabilityProjection(stdout, projection, jsonOutput)
}

func projectClaudeCoordinationEligibility(registry coremetadata.Registry, agent coremetadata.Agent) *agentCapabilityCoordination {
	projection := &agentCapabilityCoordination{Evidence: "local-registration-lease"}
	recovery := "projmux agent integrate claude --dry-run; projmux agent integrate claude; let the existing Claude session exit normally, then projmux agent resume uid:" + agent.Metadata.UID + " (same Agent UID; no Agent recreation)"
	route, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID)
	if reason != "" {
		projection.Reason = reason
		projection.Recovery = recovery
		return projection
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil || !probeClaudeRegistrationLease(intmetadata.PathFor(paths.StateDir), route) {
		projection.Reason = "Claude registration lease is stale or unavailable"
		projection.Recovery = recovery
		return projection
	}
	if !probeClaudeCoordinationEligibility(intmetadata.PathFor(paths.StateDir), route) {
		projection.Reason = "Claude coordination is unqualified for the exact running provider version"
		projection.Recovery = "run the owned isolation collector, then projmux agent message qualify uid:" + agent.Metadata.UID + " --evidence <private-json> --confirm-isolated-provider-push -o json; if public init is unavailable, let Claude exit normally and projmux agent resume uid:" + agent.Metadata.UID + " (same UID)"
		return projection
	}
	projection.Eligible = true
	projection.Evidence = "helper-memory-exact-version-qualification"
	projection.Reason = "exact local registration and current-version qualification are ready for coordination delivery"
	return projection
}

func projectStaticAgentCapabilities(provider aiprovider.ID) agentCapabilityProjection {
	projection := agentCapabilityProjection{Provider: provider}
	for _, action := range aiprovider.AgentActions() {
		_, cell, ok := aiprovider.LookupAgentCapability(action.ID, provider)
		if !ok {
			continue
		}
		projection.Capabilities = append(projection.Capabilities, agentCapabilityProjectionEntry{
			Action: action.ID, Group: action.Group, Callable: action.Callable,
			Mode: cell.Mode, CompletionPrecision: cell.CompletionPrecision,
		})
	}
	return projection
}

func projectExactAgentCapabilities(registry coremetadata.Registry, agent coremetadata.Agent, provider aiprovider.ID) agentCapabilityProjection {
	projection := projectStaticAgentCapabilities(provider)
	projection.Agent = &agentCapabilityAgent{UID: agent.Metadata.UID, Name: agent.Metadata.Name, Phase: agent.Status.Phase}
	runtime := projectAgentRuntimeEligibility(registry, agent)
	projection.Runtime = &runtime
	for i := range projection.Capabilities {
		available, reason := exactAgentActionEligibility(registry, agent, projection.Capabilities[i].Action, projection.Capabilities[i].Mode)
		projection.Capabilities[i].Available = &available
		projection.Capabilities[i].Evidence = "registry"
		projection.Capabilities[i].Reason = reason
	}
	return projection
}

func projectAgentRuntimeEligibility(registry coremetadata.Registry, agent coremetadata.Agent) agentCapabilityRuntime {
	runtime := agentCapabilityRuntime{Evidence: "registry", Reason: "agent has no current Running Pane binding"}
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

func exactAgentActionEligibility(registry coremetadata.Registry, agent coremetadata.Agent, action string, mode aiprovider.AgentSupportMode) (bool, string) {
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
	case "resume":
		if !slicesContainsAgentPhase(resumableAgentPhases, agent.Status.Phase) {
			return false, "requires an Offline or Failed Agent"
		}
		if agent.Status.SessionRef == nil || agent.Status.SessionRef.Provider != agent.Spec.Provider || agent.Status.SessionRef.ConversationID() == "" {
			return false, "provider conversation identity is unavailable"
		}
	case "review":
		if reason := exactCodexReviewRegistryReason(registry, agent); reason != "" {
			return false, reason
		}
	case "message.send", "message.wait", "message.status":
		if _, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID); reason != "" {
			return false, reason
		}
	case "wait.idle":
		if reason := exactAgentActivationReason(registry, agent); reason != "" {
			return false, reason
		}
	default:
		if mode == aiprovider.SupportNativeExact {
			if reason := exactCodexControlRegistryReason(registry, agent); reason != "" {
				return false, reason
			}
		}
	}
	return true, "eligible from current Registry evidence"
}

func exactAgentActivationReason(registry coremetadata.Registry, agent coremetadata.Agent) string {
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
		return "requires a Running Agent with a current managed Pane"
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Spec.Role != coremetadata.PaneRoleAgent || pane.Metadata.OwnerRef == nil ||
		pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != agent.Metadata.UID ||
		pane.Status.Activation.AgentUID != agent.Metadata.UID || pane.Status.Activation.Generation == "" || pane.Status.Activation.RuntimeID == "" {
		return "managed Agent activation identity is incomplete"
	}
	return ""
}

func slicesContainsAgentPhase(phases []coremetadata.AgentPhase, phase coremetadata.AgentPhase) bool {
	return slices.Contains(phases, phase)
}

func exactCodexReviewRegistryReason(registry coremetadata.Registry, agent coremetadata.Agent) string {
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

func exactCodexControlRegistryReason(registry coremetadata.Registry, agent coremetadata.Agent) string {
	if reason := exactCodexReviewRegistryReason(registry, agent); reason != "" {
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

func writeAgentCapabilityProjection(stdout io.Writer, projection agentCapabilityProjection, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(projection)
	}
	if projection.Agent == nil {
		if _, err := fmt.Fprintf(stdout, "provider=%s static=true\n", projection.Provider); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(stdout, "ACTION\tMODE\tCOMPLETION"); err != nil {
			return err
		}
		for _, entry := range projection.Capabilities {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", entry.Action, entry.Mode, entry.CompletionPrecision); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "agent=%s uid=%s provider=%s phase=%s registryReady=%t liveVerified=%t generation=%s connectionEpoch=%d bindingEpoch=%d\n",
		projection.Agent.Name, projection.Agent.UID, projection.Provider, projection.Agent.Phase, projection.Runtime.Ready,
		projection.Runtime.LiveVerified, projection.Runtime.ActivationGeneration, projection.Runtime.ConnectionEpoch, projection.Runtime.BindingEpoch); err != nil {
		return err
	}
	if coordination := projection.Runtime.Coordination; coordination != nil {
		if _, err := fmt.Fprintf(stdout, "coordination eligible=%t evidence=%s reason=%s recovery=%s\n", coordination.Eligible, coordination.Evidence, coordination.Reason, coordination.Recovery); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, "ACTION\tMODE\tCOMPLETION\tAVAILABLE\tEVIDENCE\tREASON"); err != nil {
		return err
	}
	for _, entry := range projection.Capabilities {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%t\t%s\t%s\n", entry.Action, entry.Mode, entry.CompletionPrecision, *entry.Available, entry.Evidence, entry.Reason); err != nil {
			return err
		}
	}
	return nil
}

func requireStaticNativeAgentCapability(spelling string, agent coremetadata.Agent) error {
	action := strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(spelling), "agent "), " ", ".")
	metadata, ok := aiprovider.Lookup(agent.Spec.Provider)
	if !ok {
		return fmt.Errorf("%s unavailable: agent/%s has unsupported provider %q", spelling, agent.Metadata.Name, agent.Spec.Provider)
	}
	_, cell, ok := aiprovider.LookupAgentCapability(action, metadata.ID)
	if !ok || cell.Mode != aiprovider.SupportNativeExact {
		return fmt.Errorf("%s unavailable: agent/%s provider %s does not support native exact control", spelling, agent.Metadata.Name, metadata.ID)
	}
	return nil
}
