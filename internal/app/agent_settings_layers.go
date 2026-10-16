package app

import (
	"errors"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// agentSettingsResolver is the optional seam that resolves an Agent's layered
// settings from the profile and instructions stores of its home. The Claude
// resume launcher implements it, next to the profile permissions it re-reads
// from the same home; a launcher without it launches the recorded values.
type agentSettingsResolver interface {
	ResolveAgentSettingsRequest(provider string, annotations map[string]string, request agentSettingsRequest) (agentSettingsLaunch, error)
}

var _ agentSettingsResolver = (*aiCommand)(nil)

// ResolveAgentSettings is ResolveAgentSettingsRequest with only the model and
// effort overrides of `agent resume` and `agent relaunch`.
func (c *aiCommand) ResolveAgentSettings(provider string, annotations map[string]string, model, effort, source string) (agentSettingsLaunch, error) {
	return c.ResolveAgentSettingsRequest(provider, annotations, agentSettingsRequest{model: model, effort: effort, source: source})
}

// ResolveAgentSettingsRequest is resolveAgentSettings over the resume seam's
// home.
func (c *aiCommand) ResolveAgentSettingsRequest(provider string, annotations map[string]string, request agentSettingsRequest) (agentSettingsLaunch, error) {
	if c.processCreateContext != nil {
		launch, err := mutationruntime.ResolveAgentSettingsWithPaths(c.processCreatePaths, provider, annotations, request.shared())
		return localAgentSettingsLaunch(launch), localResumeError(err)
	}
	return resolveAgentSettings(c.homeDir, c.lookupEnv, provider, annotations, request)
}

// resolveSettings resolves the layered settings of one rebind: a Codex Agent
// from the create command's home, where its profile policy is re-read too
// (codexResumeProfile), and any other Agent through the resume launcher.
func (r *agentRebinder) resolveSettings(provider string, annotations map[string]string, request agentSettingsRequest) (agentSettingsLaunch, error) {
	// Even a launcher without layered settings must honor the recorded scope.
	if name := annotations[coremetadata.AnnotationAgentProfile]; name != "" {
		_, _, _, err := resolveRecordedProfile(r.create.homeDir, r.create.lookupEnv, name, request.projectUID)
		if profile.ReasonOf(err) == profile.ReasonOutOfScope {
			return agentSettingsLaunch{}, err
		}
	}

	if provider == aiModeCodex {
		return resolveAgentSettings(r.create.homeDir, r.create.lookupEnv, provider, annotations, request)
	}
	if resolver, ok := r.launcher.(agentSettingsResolver); ok {
		return resolver.ResolveAgentSettingsRequest(provider, annotations, request)
	}
	if request.changesLayers() {
		return agentSettingsLaunch{}, errors.New("the resume launcher cannot change an Agent's profile or instructions")
	}
	return agentSettingsLaunch{}, nil
}
