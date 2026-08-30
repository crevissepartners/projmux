package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// codexInstallCapabilityGuidanceURL is the single product authority for the
// official Codex CLI capability reference rendered by native create, Doctor,
// and Settings.
const codexInstallCapabilityGuidanceURL = "https://learn.chatgpt.com/docs/codex/cli"

// codexInstallGuidance is the bounded, observed-facts projection of one Codex
// install capability. It deliberately carries no installer provenance: the
// topology observer proves only whether the ordinary CLI and canonical managed
// standalone payload were observed.
type codexInstallGuidance struct {
	Capability  codexappserver.InstallCapability
	Observation string
	Reference   string
}

func codexInstallCapabilityGuidance(capability codexappserver.InstallCapability) codexInstallGuidance {
	guidance := codexInstallGuidance{
		Capability: capability,
		Reference:  "Official Codex CLI capability guidance: " + codexInstallCapabilityGuidanceURL,
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

func (g codexInstallGuidance) Text() string {
	return strings.TrimSpace(g.Observation + " " + g.Reference)
}
