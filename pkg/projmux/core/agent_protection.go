package core

// SetAgentProtectionRequest names one Agent by uid: ref or a name in explicit
// Project/Window scope. No active Pane, cwd or environment supplies a target.
type SetAgentProtectionRequest struct {
	Ref       string
	Project   string
	Window    string
	Protected bool
}

// SetAgentProtectionResult owns the Agent projection from the label commit.
// Changed is false when the requested label map was already present.
type SetAgentProtectionResult struct {
	Agent   Agent
	Changed bool
}
