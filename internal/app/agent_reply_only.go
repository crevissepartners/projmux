package app

import (
	"fmt"
	"slices"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// replyOnlyReasonLaunchFixed refuses a model, an effort, or a reset of the
// settings layers on an Agent that records the reply-only activation: that
// launch is fixed and passes none of them. A profile and instructions keep
// the reasons create gives them next to --dialogue-reply-only
// (profileReasonLaneUnsupported, persona.ReasonProviderUnsupported).
const replyOnlyReasonLaunchFixed = "reply-only-launch-fixed"

// replyOnlyRefusal is why a request cannot be launched on a reply-only Agent,
// the zero value when it can.
type replyOnlyRefusal struct {
	reason string
	// carry names what the request asks the launch to carry.
	carry string
}

// detail is the refusal text between the Agent and the reason token.
func (r replyOnlyRefusal) detail() string {
	return fmt.Sprintf("records the reply-only activation (--%s), whose fixed launch cannot carry %s; create a new Agent to run with it",
		claudeDialogueReplyOnlyFlag, r.carry)
}

// replyOnlyRefusalOf refuses what an Agent recording the reply-only activation
// (coremetadata.AnnotationAgentDialogueReplyOnly) is asked to launch with
// beyond it: a profile (`none` included), instructions (a detach and a reset
// of them included), a model, an effort, or any other reset. Every resume of
// such an Agent is the reply-only launch again, so none of them would reach
// the provider, and removing the mode would widen what the Agent may do. An
// Agent that does not record it is never refused here.
func replyOnlyRefusalOf(annotations map[string]string, request agentSettingsRequest) replyOnlyRefusal {
	if !coremetadata.RecordsDialogueReplyOnly(annotations) {
		return replyOnlyRefusal{}
	}
	switch {
	case request.profile != nil:
		return replyOnlyRefusal{reason: profileReasonLaneUnsupported, carry: "a profile"}
	case request.instructions != nil || slices.Contains(request.reset, agentsettings.ItemInstructions):
		return replyOnlyRefusal{reason: persona.ReasonProviderUnsupported, carry: "instructions"}
	case request.model != "" || request.effort != "" || len(request.reset) > 0:
		return replyOnlyRefusal{reason: replyOnlyReasonLaunchFixed, carry: "a model, an effort, or a reset"}
	}
	return replyOnlyRefusal{}
}
