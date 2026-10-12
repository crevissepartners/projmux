package runtime

import (
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// AgentActivationBindingReason names the one Registry fact that stopped a Pane
// from carrying the exact Agent activation binding an observer or writer
// expected.
//
// The set is closed on purpose. The refusal used to say only that "the binding
// changed", which under load left an operator with a nonzero exit, a live Pane,
// and no way to tell a replaced activation from a deleted Agent short of
// reading the Registry by hand -- and the Registry may have moved again by
// then. Naming the fact from the same snapshot the verdict read is what makes
// the refusal actionable. AgentActivationBindingReasons declares the whole set,
// and a test holds every constant of this type to exactly one entry there.
type AgentActivationBindingReason string

const (
	// AgentBindingPaneMissing: the Pane row is gone from the Registry.
	AgentBindingPaneMissing AgentActivationBindingReason = "pane-missing"
	// AgentBindingPaneActivationCleared: the Pane row exists, but its activation
	// carries no generation, Agent, or runtime id.
	AgentBindingPaneActivationCleared AgentActivationBindingReason = "pane-activation-cleared"
	// AgentBindingRuntimeIDChanged: the activation names a different %N than
	// the one being observed.
	AgentBindingRuntimeIDChanged AgentActivationBindingReason = "runtime-id-changed"
	// AgentBindingActivationAgentChanged: the activation now names another
	// Agent than the expected one.
	AgentBindingActivationAgentChanged AgentActivationBindingReason = "activation-agent-changed"
	// AgentBindingActivationGenerationChanged: the same Agent was re-activated
	// on this Pane, by a resume or a replacement.
	AgentBindingActivationGenerationChanged AgentActivationBindingReason = "activation-generation-changed"
	// AgentBindingAgentMissing: the activation names an Agent row that is gone.
	AgentBindingAgentMissing AgentActivationBindingReason = "agent-missing"
	// AgentBindingAgentNotRunning: the Agent row left the Running phase.
	AgentBindingAgentNotRunning AgentActivationBindingReason = "agent-not-running"
	// AgentBindingAgentPaneRefChanged: the Agent row now points at another Pane.
	AgentBindingAgentPaneRefChanged AgentActivationBindingReason = "agent-pane-ref-changed"
)

// AgentActivationBindingReasons is the whole closed set, in the order
// CheckAgentActivationBinding evaluates it.
var AgentActivationBindingReasons = []AgentActivationBindingReason{
	AgentBindingPaneMissing,
	AgentBindingPaneActivationCleared,
	AgentBindingRuntimeIDChanged,
	AgentBindingActivationAgentChanged,
	AgentBindingActivationGenerationChanged,
	AgentBindingAgentMissing,
	AgentBindingAgentNotRunning,
	AgentBindingAgentPaneRefChanged,
}

// AgentActivationBindingExpectation is the exact binding a caller already
// holds. A nil expectation accepts whichever Agent and generation the Pane
// currently carries, which is what the first read of an observer needs.
type AgentActivationBindingExpectation struct {
	AgentUID   string
	Generation string
}

// AgentActivationBindingCheck is one verdict and the facts behind it, all read
// from a single Registry snapshot.
type AgentActivationBindingCheck struct {
	// Reason is empty exactly when the binding holds.
	Reason AgentActivationBindingReason
	// Expected and Observed are the two sides of Reason.
	Expected string
	Observed string
	// AgentUID is the Agent the verdict is about: the expected one when there
	// is an expectation, otherwise whichever Agent the Pane activation names
	// (empty when it names none).
	AgentUID string
	// Generation is the Bound activation generation; set only when Bound.
	Generation string
	// AgentPresent and PanePresent say whether the AgentUID row and the Pane
	// row exist in the snapshot the verdict read.
	AgentPresent bool
	PanePresent  bool
}

func (c AgentActivationBindingCheck) Bound() bool { return c.Reason == "" }

// CheckAgentActivationBinding judges the one Agent→Pane materialization a
// provider acknowledgement may refine. Pane uid is durable and therefore not
// enough by itself; the generation changes on resume/replacement.
//
// The conditions are exactly the ones the Bound/not-Bound verdict has always
// used -- including the caller's own Agent and generation comparison, which
// moved here so the reason is named once -- and only their order decides which
// reason a multiply broken binding reports.
func CheckAgentActivationBinding(registry coremetadata.Registry, paneUID, runtimeID string, expect *AgentActivationBindingExpectation) AgentActivationBindingCheck {
	var check AgentActivationBindingCheck
	pane, panePresent := registry.Pane(paneUID)
	check.PanePresent = panePresent
	var activation coremetadata.PaneActivation
	if panePresent {
		activation = pane.Status.Activation
	}
	check.AgentUID = activation.AgentUID
	if expect != nil {
		check.AgentUID = expect.AgentUID
	}
	if check.AgentUID != "" {
		_, check.AgentPresent = registry.Agent(check.AgentUID)
	}
	refuse := func(reason AgentActivationBindingReason, expected, observed string) AgentActivationBindingCheck {
		check.Reason, check.Expected, check.Observed = reason, expected, observed
		return check
	}
	runtimeID = strings.TrimSpace(runtimeID)
	switch {
	case !panePresent:
		return refuse(AgentBindingPaneMissing, "Pane row uid:"+paneUID, "absent")
	case strings.TrimSpace(activation.Generation) == "" ||
		strings.TrimSpace(activation.AgentUID) == "" ||
		strings.TrimSpace(activation.RuntimeID) == "":
		return refuse(AgentBindingPaneActivationCleared, "generation, agentUID, and runtimeID all set",
			fmt.Sprintf("generation=%q agentUID=%q runtimeID=%q", activation.Generation, activation.AgentUID, activation.RuntimeID))
	case activation.RuntimeID != runtimeID:
		return refuse(AgentBindingRuntimeIDChanged, runtimeID, activation.RuntimeID)
	case expect != nil && activation.AgentUID != expect.AgentUID:
		return refuse(AgentBindingActivationAgentChanged, "uid:"+expect.AgentUID, "uid:"+activation.AgentUID)
	case expect != nil && activation.Generation != expect.Generation:
		return refuse(AgentBindingActivationGenerationChanged, expect.Generation, activation.Generation)
	}
	agent, agentPresent := registry.Agent(activation.AgentUID)
	switch {
	case !agentPresent:
		return refuse(AgentBindingAgentMissing, "Agent row uid:"+activation.AgentUID, "absent")
	case agent.Status.Phase != coremetadata.PhaseRunning:
		return refuse(AgentBindingAgentNotRunning, string(coremetadata.PhaseRunning), string(agent.Status.Phase))
	case agent.Status.PaneRef != paneUID:
		return refuse(AgentBindingAgentPaneRefChanged, "uid:"+paneUID, "uid:"+agent.Status.PaneRef)
	}
	check.Generation = activation.Generation
	return check
}

// Refusal stages. The stage is the only thing that differs between the create
// writer's recheck and the observer's two reads; everything else in the text
// comes from AgentActivationBindingRefusal.
const (
	AgentBindingStageBeforeAwaiting  = "before awaiting acknowledgement"
	AgentBindingStageWhileAwaiting   = "while awaiting acknowledgement"
	AgentBindingStageBeforeRecording = "before recording activation"
)

// AgentActivationBindingError is a refused binding. Error() is
// AgentActivationBindingRefusal, so every consumer prints the same text for
// the same facts.
type AgentActivationBindingError struct {
	// command prefixes the text ("create agent"); empty for the observer, whose
	// caller owns the prefix.
	Command string
	Stage   string
	PaneUID string
	PaneID  string
	Check   AgentActivationBindingCheck
}

func (e *AgentActivationBindingError) Error() string {
	return AgentActivationBindingRefusal(e.Command, e.Stage, e.PaneUID, e.PaneID, e.Check)
}

// AgentActivationBindingRefusalSteps is the ordered remediation a refused
// binding prints, on the same principle as
// activationUnconfirmedDiagnosticSteps: a refused binding is usually a
// concurrent writer that moved the Registry, not a dead Agent, so the two cheap
// reads come first and deletion is last and conditional on both finding no
// evidence.
func AgentActivationBindingRefusalSteps(agentUID, paneUID, paneID string) []string {
	read := fmt.Sprintf("`projmux describe pane uid:%s`", paneUID)
	remove := fmt.Sprintf("`projmux delete pane uid:%s --yes`", paneUID)
	if agentUID != "" {
		read = fmt.Sprintf("`projmux describe agent uid:%s` and %s", agentUID, read)
		remove = fmt.Sprintf("`projmux delete agent uid:%s --yes`", agentUID)
	}
	return []string{
		"nothing was rolled back",
		"re-read the committed binding with " + read + " first: another writer may have moved it on purpose",
		fmt.Sprintf("then look at Pane %s (`tmux capture-pane -p -t %s`) or the provider transcript for evidence the Agent is still working", paneID, paneID),
		"only when neither read shows activation evidence, clean up with " + remove,
	}
}

// AgentActivationBindingRefusal is the one generator of refused-binding text.
// It names the target identity, the reason with both sides of it, and whether
// the Agent and Pane rows were still present in the snapshot the verdict read,
// then prints AgentActivationBindingRefusalSteps in order.
func AgentActivationBindingRefusal(command, stage, paneUID, paneID string, check AgentActivationBindingCheck) string {
	var builder strings.Builder
	if command != "" {
		builder.WriteString(command + ": ")
	}
	agent := "(none bound)"
	if check.AgentUID != "" {
		agent = "uid:" + check.AgentUID
	}
	fmt.Fprintf(&builder, "activation binding changed %s for Agent %s Pane uid:%s %s: %s (expected %s, observed %s)",
		stage, agent, paneUID, paneID, check.Reason, check.Expected, check.Observed)
	fmt.Fprintf(&builder, "; in that Registry read the Agent row was %s and the Pane row was %s",
		AgentBindingRegistryPresence(check.AgentUID != "" && check.AgentPresent), AgentBindingRegistryPresence(check.PanePresent))
	for _, step := range AgentActivationBindingRefusalSteps(check.AgentUID, paneUID, paneID) {
		builder.WriteString("; ")
		builder.WriteString(step)
	}
	return builder.String()
}

func AgentBindingRegistryPresence(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}
