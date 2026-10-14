package app

import (
	"slices"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Read only the values used by the existing guard. Never read prompt bodies,
// profile content, environment, credentials, or rendered link rules here.
func recordedResumeRecipeError(target coremetadata.Agent, recipe processRelaunchRecipe) *core.ResumeRecipeChangedError {
	r := recipe.restart.settings.resolution
	a := target.Metadata.Annotations
	e := &core.ResumeRecipeChangedError{Code: processResumeRefused, Reason: relaunchReasonNoConversation, AgentUID: target.Metadata.UID}
	scalar := func(field, reason, old, current string) {
		e.Changes = append(e.Changes, core.ResumeRecipeChange{Field: field, Reason: reason, Recorded: []string{old}, Current: []string{current}})
	}
	// Fixed known-reason order matches agentsettings; unknown reasons follow in
	// sorted order so future resolver reasons remain visible without leaking data.
	reasons := slices.Clone(r.Reasons)
	if r.ProfileSwitched && !slices.Contains(reasons, agentsettings.ReasonProfileChanged) {
		reasons = append(reasons, agentsettings.ReasonProfileChanged)
	}
	known := []string{agentsettings.ReasonProfileChanged, agentsettings.ReasonInstructionsChanged, agentsettings.ReasonInstructionsContentChanged, agentsettings.ReasonModelChanged, agentsettings.ReasonEffortChanged, agentsettings.ReasonGuidanceChanged, agentsettings.ReasonProjectGuidanceChanged, agentsettings.ReasonLinkRulesChanged}
	for _, reason := range known {
		if !slices.Contains(reasons, reason) {
			continue
		}
		switch reason {
		case agentsettings.ReasonProfileChanged:
			scalar("profile", reason, r.Current.Profile.Name, r.New.Profile.Name)
			scalar("profileDigest", reason, r.Current.Profile.Digest, r.New.Profile.Digest)
		case agentsettings.ReasonInstructionsChanged:
			scalar("instructions", reason, r.Current.Instructions.Value, r.New.Instructions.Value)
		case agentsettings.ReasonInstructionsContentChanged:
			scalar("instructionsDigest", reason, a[coremetadata.AnnotationAgentPersonaDigest], r.InstructionsDigest)
		case agentsettings.ReasonModelChanged:
			scalar("model", reason, r.Current.Model.Value, r.New.Model.Value)
		case agentsettings.ReasonEffortChanged:
			scalar("effort", reason, r.Current.Effort.Value, r.New.Effort.Value)
		case agentsettings.ReasonGuidanceChanged:
			scalar("guidanceDigest", reason, a[coremetadata.AnnotationAgentGuidanceDigest], recipe.guidance.digest)
		case agentsettings.ReasonProjectGuidanceChanged:
			scalar("projectGuidanceDigest", reason, a[coremetadata.AnnotationAgentProjectGuidanceDigest], recipe.links.project.digest)
		case agentsettings.ReasonLinkRulesChanged:
			scalar("linkRulesDigest", reason, a[coremetadata.AnnotationAgentProjectLinkRulesDigest], recipe.links.digest)
		}
	}
	slices.Sort(reasons)
	for _, reason := range slices.Compact(reasons) {
		if !slices.Contains(known, reason) {
			scalar("unknown", reason, "<unknown>", "<unknown>")
		}
	}
	if recipe.workspace.CWD != target.Spec.Workspace.CWD {
		scalar("workspace.cwd", "workspace-changed", target.Spec.Workspace.CWD, recipe.workspace.CWD)
	}
	if !slices.Equal(recipe.workspace.AdditionalWritableRoots, target.Spec.Workspace.AdditionalWritableRoots) {
		e.Changes = append(e.Changes, core.ResumeRecipeChange{Field: "workspace.additionalWritableRoots", Reason: "workspace-changed", Recorded: append([]string{}, target.Spec.Workspace.AdditionalWritableRoots...), Current: append([]string{}, recipe.workspace.AdditionalWritableRoots...)})
	}
	e.RelaunchCommand = "projmux agent relaunch uid:" + target.Metadata.UID + " --detach"
	if recipe.restart.provider == aiModeClaude {
		e.RelaunchCommand += " -- <prompt>"
	}
	e.Guidance = "use explicit relaunch with current settings: " + e.RelaunchCommand + "; preserves Agent UID, Pane UID, and the recorded conversation (relaunch-no-conversation describes the resume refusal, not conversation loss)"
	return e
}
