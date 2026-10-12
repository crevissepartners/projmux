package metadata

// LabelProtected is the reserved Agent deletion protection label. Presence,
// including an empty or false value, protects until the label is removed.
const LabelProtected = "projmux.io/protected"

func AgentProtected(agent Agent) bool {
	_, present := agent.Metadata.Labels[LabelProtected]
	return present
}
