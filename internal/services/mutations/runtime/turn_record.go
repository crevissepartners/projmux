package runtime

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"strings"
)

func RecordStartedTurn(binding agentcontrol.Binding, response agentcontrol.Response, mutate func(func(*coremetadata.Registry, coremetadata.Mutator) error) error) error {
	if strings.TrimSpace(response.ThreadID) != binding.Identity.ThreadID || strings.TrimSpace(response.TurnID) == "" {
		return agentcontrol.Recovery(&agentcontrol.BindingError{Reason: "turn response did not preserve the exact thread identity"}, binding)
	}
	return mutate(func(registry *coremetadata.Registry, mutator coremetadata.Mutator) error {
		changed, err := mutator.RefineCodexActivation(registry, coremetadata.CodexActivationObservation{
			AgentUID: binding.Identity.AgentUID, PaneUID: binding.Identity.PaneUID,
			Generation: binding.Identity.Generation, ThreadID: binding.Identity.ThreadID,
			TurnID: response.TurnID, Endpoint: binding.Endpoint,
		})
		if err != nil {
			return err
		}
		if !changed {
			agent, ok := registry.Agent(binding.Identity.AgentUID)
			if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || !agent.Status.SessionRef.Codex.HasStartedTurn {
				return &agentcontrol.BindingError{Reason: "first-turn evidence no longer matches the exact native binding"}
			}
		}
		return nil
	})
}
