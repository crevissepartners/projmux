package app

import (
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)



const (
	// agentBindingPaneMissing: the Pane row is gone from the Registry.
	agentBindingPaneMissing agentActivationBindingReason = "pane-missing"
	// agentBindingPaneActivationCleared: the Pane row exists, but its activation
	// carries no generation, Agent, or runtime id.
	agentBindingPaneActivationCleared agentActivationBindingReason = "pane-activation-cleared"
	// agentBindingRuntimeIDChanged: the activation names a different %N than
	// the one being observed.
	agentBindingRuntimeIDChanged agentActivationBindingReason = "runtime-id-changed"
	// agentBindingActivationAgentChanged: the activation now names another
	// Agent than the expected one.
	agentBindingActivationAgentChanged agentActivationBindingReason = "activation-agent-changed"
	// agentBindingActivationGenerationChanged: the same Agent was re-activated
	// on this Pane, by a resume or a replacement.
	agentBindingActivationGenerationChanged agentActivationBindingReason = "activation-generation-changed"
	// agentBindingAgentMissing: the activation names an Agent row that is gone.
	agentBindingAgentMissing agentActivationBindingReason = "agent-missing"
	// agentBindingAgentNotRunning: the Agent row left the Running phase.
	agentBindingAgentNotRunning agentActivationBindingReason = "agent-not-running"
	// agentBindingAgentPaneRefChanged: the Agent row now points at another Pane.
	agentBindingAgentPaneRefChanged agentActivationBindingReason = "agent-pane-ref-changed"
)






















