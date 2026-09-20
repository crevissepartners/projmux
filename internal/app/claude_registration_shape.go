package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// claudeActivationProcessAlive reports whether the exact process one Claude
// activation bound is still on the host. It is a variable so tests can state
// host truth instead of spawning a process, and so no code path can accidentally
// classify a registration while consulting nothing.
var claudeActivationProcessAlive = liveClaudeActivationProcessAlive

// liveClaudeActivationProcessAlive compares the whole birth identity, not the
// pid. A pid alone comes back to life when the kernel hands the number to an
// unrelated process, and a gone session that reads as live would send the
// operator to re-register a session that no longer exists.
func liveClaudeActivationProcessAlive(process coremetadata.ProcessIdentity) bool {
	if !process.Valid() {
		return false
	}
	observed, _, err := localipc.Process(process.PID)
	return err == nil && observed == process
}

// classifyAgentClaudeRegistration reports the registration shape of one Claude
// Agent's current managed Pane. A non-Claude Agent, or an Agent with no Pane,
// has nothing to explain and reports ready.
func classifyAgentClaudeRegistration(registry coremetadata.Registry, agent coremetadata.Agent) coremetadata.ClaudeRegistrationShape {
	if agent.Spec.Provider != string(aiprovider.Claude) || agent.Status.PaneRef == "" {
		return coremetadata.ClaudeRegistrationReady
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok {
		return coremetadata.ClaudeRegistrationReady
	}
	alive := false
	if binding := pane.Status.Activation.Claude; binding != nil {
		alive = claudeActivationProcessAlive(binding.Process)
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

// claudeRegistrationExplanation names which Agent, why it has no lease, and
// what to do about it. Callers append it to their own refusal, never in place
// of it, so the reason every existing reader matches on stays byte-exact.
func claudeRegistrationExplanation(agent coremetadata.Agent, shape coremetadata.ClaudeRegistrationShape) string {
	diagnosis := claudeRegistrationDiagnosis(agent, shape)
	if diagnosis == "" {
		return ""
	}
	return diagnosis + "; " + claudeRegistrationNextAction(shape, agent.Metadata.UID)
}

// explainClaudeRouteReason appends the missing-registration shape and its next
// action to a route reason. It is purely additive: reason keeps its exact
// leading bytes, and any other reason is returned untouched.
func explainClaudeRouteReason(registry coremetadata.Registry, agent coremetadata.Agent, reason string) string {
	if reason != coremetadata.ClaudeRegistrationUnavailableReason {
		return reason
	}
	explanation := claudeRegistrationExplanation(agent, classifyAgentClaudeRegistration(registry, agent))
	if explanation == "" {
		return reason
	}
	return reason + "; " + explanation
}
