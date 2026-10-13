package runtime

import (
	"maps"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// agentResumePlan is one preflighted rebind: everything `agent resume` fixed
// from the read-only registry before it opened the store.
type AgentResumePlan struct {
	DialogueReplyOnly bool
	AgentUID          string
	AgentName         string
	// provider is the union discriminator of the stored ref, which is the
	// provider whose resume argv will be built.
	Provider string
	// conversationID is the identifier the provider's resume argv addresses.
	ConversationID string
	// ref is the stored pointer, kept whole so the transaction can prove the
	// conversation did not change between the preflight read and the lock.
	Ref *coremetadata.AgentSessionRef
	// The owner chain the new managed Pane is materialized into.
	ProjectUID  string
	ProjectRoot string
	// project is the owner Project as the plan read it, whose name and labels
	// are the variables of its label link rules.
	Project   coremetadata.Project
	Workspace coremetadata.AgentWorkspace
	Topic     string
	WindowUID string
	AnchorUID string
	// shared names the other Agents that record the same conversation, in uid
	// order. It is disclosed, never decisive: see planAgentResume.
	Shared []string
	// annotations are the Agent's own, handed to the resume seam unread.
	Annotations map[string]string
	// modelOverride and effortOverride are `agent resume --model/--effort`,
	// both empty on every other rebind. Both are recorded on the Agent by the
	// rebind transaction and launched with. Later plain resumes re-pass the
	// effort but not the model, which the conversation owns.
	ModelOverride  string
	EffortOverride string
	// overrideSource is where the overrides came from, recorded beside them:
	// coremetadata.SettingSourceRelaunch for `agent relaunch`, and
	// coremetadata.SettingSourceResume (the empty value) for `agent resume`.
	OverrideSource string
	// layerChanges are the changes `agent relaunch` makes to the layers
	// themselves: --profile, --instructions and --reset. Empty on every other
	// rebind.
	LayerChanges AgentSettingsRequest
	// stoppedPane is the managed Pane a restart closed right before this
	// rebind. Empty on every other rebind.
	StoppedPane StoppedAgentPane
	// retiredProcess is supplied only by host relaunch after exact supervisor Wait.

	VirtualWindow bool
}

// settingsRequest is what this rebind asks of the Agent's layers.
func (p AgentResumePlan) SettingsRequest() AgentSettingsRequest {
	request := p.LayerChanges
	request.ProjectUID = p.ProjectUID
	request.Model, request.Effort, request.Source = p.ModelOverride, p.EffortOverride, p.SettingSource()
	return request
}

// settingSource is the source the rebind transaction records beside the
// model and effort overrides.
func (p AgentResumePlan) SettingSource() string {
	if p.OverrideSource == "" {
		return coremetadata.SettingSourceResume
	}
	return p.OverrideSource
}

// launchAnnotations are the annotations the resume seam reads: the Agent's
// own, with the effort override in place of the recorded effort. That is the
// value the rebind transaction records, so the launch reads what the Agent
// will record. Without an override they are the Agent's own map itself.
func (p AgentResumePlan) LaunchAnnotations() map[string]string {
	if p.EffortOverride == "" {
		return p.Annotations
	}
	out := maps.Clone(p.Annotations)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentEffort] = p.EffortOverride
	return out
}

// stoppedAgentPane is the managed Pane a restart closed: its uid and its
// non-automatic name, "" when it was named by its own UID.
type StoppedAgentPane struct {
	UID  string
	Name string
}
