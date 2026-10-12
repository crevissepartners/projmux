package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

type agentActivationBindingReason = mutationruntime.AgentActivationBindingReason

const (
	agentBindingPaneMissing                 = mutationruntime.AgentBindingPaneMissing
	agentBindingPaneActivationCleared       = mutationruntime.AgentBindingPaneActivationCleared
	agentBindingRuntimeIDChanged            = mutationruntime.AgentBindingRuntimeIDChanged
	agentBindingActivationAgentChanged      = mutationruntime.AgentBindingActivationAgentChanged
	agentBindingActivationGenerationChanged = mutationruntime.AgentBindingActivationGenerationChanged
	agentBindingAgentMissing                = mutationruntime.AgentBindingAgentMissing
	agentBindingAgentNotRunning             = mutationruntime.AgentBindingAgentNotRunning
	agentBindingAgentPaneRefChanged         = mutationruntime.AgentBindingAgentPaneRefChanged
)

var agentActivationBindingReasons = mutationruntime.AgentActivationBindingReasons

type agentActivationBindingExpectation = mutationruntime.AgentActivationBindingExpectation
type agentActivationBindingCheck mutationruntime.AgentActivationBindingCheck

func (c agentActivationBindingCheck) bound() bool {
	return mutationruntime.AgentActivationBindingCheck(c).Bound()
}
func checkAgentActivationBinding(registry coremetadata.Registry, paneUID, runtimeID string, expect *agentActivationBindingExpectation) agentActivationBindingCheck {
	return agentActivationBindingCheck(mutationruntime.CheckAgentActivationBinding(registry, paneUID, runtimeID, expect))
}

const (
	agentBindingStageBeforeAwaiting  = mutationruntime.AgentBindingStageBeforeAwaiting
	agentBindingStageWhileAwaiting   = mutationruntime.AgentBindingStageWhileAwaiting
	agentBindingStageBeforeRecording = mutationruntime.AgentBindingStageBeforeRecording
)

type agentActivationBindingError = mutationruntime.AgentActivationBindingError

func agentActivationBindingRefusal(command, stage, paneUID, paneID string, check agentActivationBindingCheck) string {
	return mutationruntime.AgentActivationBindingRefusal(command, stage, paneUID, paneID, mutationruntime.AgentActivationBindingCheck(check))
}
