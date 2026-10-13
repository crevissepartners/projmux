package app

import (
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const codexInstallCapabilityGuidanceURL = runtime.CodexInstallCapabilityGuidanceURL

type codexInstallGuidance = runtime.CodexInstallGuidance

func codexInstallCapabilityGuidance(c codexappserver.InstallCapability) codexInstallGuidance {
	return runtime.CodexInstallCapabilityGuidance(c)
}
