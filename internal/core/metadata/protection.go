package metadata

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
