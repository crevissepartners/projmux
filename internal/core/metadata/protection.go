package metadata

import "fmt"

// LabelProtected is the reserved Agent deletion protection label. Presence,
// including an empty or false value, protects until the label is removed.
const LabelProtected = "projmux.io/protected"

func AgentProtected(agent Agent) bool {
	_, present := agent.Metadata.Labels[LabelProtected]
	return present
}

// ValidateAgentProtectionLabels validates a new Agent label write. Existing
// persisted values remain protected by presence and need no Registry migration.
func ValidateAgentProtectionLabels(labels map[string]string) error {
	if value, present := labels[LabelProtected]; present && value != "true" {
		return inputErr("label agent", ErrInvalidLabel, "%s only accepts true; disable protection by removing the label: projmux label agent <ref> %s-", LabelProtected, LabelProtected)
	}
	return nil
}

// AgentProtectionError gives every removal route the same label-removal remedy.
func AgentProtectionError(agent Agent) error {
	return fmt.Errorf("agent-protected: Agent %q uid=%s is protected; remove protection with: projmux label agent uid:%s %s-", agent.Metadata.Name, agent.Metadata.UID, agent.Metadata.UID, LabelProtected)
}

// RemovedAgents compares identities, including decodable degraded graphs.
func RemovedAgents(before, after Registry) []Agent {
	var removed []Agent
	for _, agent := range before.Agents {
		if _, present := after.Agent(agent.Metadata.UID); !present {
			removed = append(removed, agent.Clone())
		}
	}
	return removed
}
func ValidateAgentRemovals(before, after Registry) error {
	for _, agent := range RemovedAgents(before, after) {
		if AgentProtected(agent) {
			return AgentProtectionError(agent)
		}
	}
	return nil
}
