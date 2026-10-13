package runtime

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// CodexInstallCapabilityGuidanceURL is the single product authority for the
// official Codex CLI capability reference rendered by native create, Doctor,
// and Settings.
const CodexInstallCapabilityGuidanceURL = "https://learn.chatgpt.com/docs/codex/cli"

// CodexInstallGuidance is the bounded, observed-facts projection of one Codex
// install capability. It deliberately carries no installer provenance: the
// topology observer proves only whether the ordinary CLI and canonical managed
// standalone payload were observed.
type CodexInstallGuidance struct {
	Capability  codexappserver.InstallCapability
	Observation string
	Reference   string
}

func CodexInstallCapabilityGuidance(capability codexappserver.InstallCapability) CodexInstallGuidance {
	guidance := CodexInstallGuidance{
		Capability: capability,
		Reference:  "Official Codex CLI capability guidance: " + CodexInstallCapabilityGuidanceURL,
	}
	switch capability {
	case codexappserver.InstallCapabilityManagedReady:
		guidance.Observation = "The managed standalone Codex payload was observed."
	case codexappserver.InstallCapabilityExternalCLIOnly:
		guidance.Observation = "The ordinary Codex CLI exists; the managed standalone payload was not observed."
	case codexappserver.InstallCapabilityCLIMissing:
		guidance.Observation = "The ordinary Codex CLI was not observed on PATH."
	case codexappserver.InstallCapabilityUnknown:
		guidance.Observation = "Codex install capability could not be determined from read-only observation."
	default:
		guidance.Capability = codexappserver.InstallCapabilityUnknown
		guidance.Observation = "Codex install capability could not be determined from read-only observation."
	}
	return guidance
}

func (g CodexInstallGuidance) Text() string {
	return strings.TrimSpace(g.Observation + " " + g.Reference)
}
