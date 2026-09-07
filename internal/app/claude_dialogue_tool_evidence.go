package app

import (
	"maps"
	"slices"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Observations are bounded descriptive metadata, never an execution permit.
// No command, body, thinking, signature, credential or ticket is retained here.
type claudeDialogueObservedTool struct {
	ToolUseID      string `json:"toolUseID"`
	MessageRef     string `json:"messageRef"`
	TargetAgentUID string `json:"targetAgentUID"`
	ResultObserved bool   `json:"resultObserved"`
	ReplyRef       string `json:"replyRef,omitempty"`
}

func (a claudeDialogueObservedTool) valid() bool {
	return validCoordinationRef(a.ToolUseID) && validCoordinationRef(a.MessageRef) && validCoordinationRef(a.TargetAgentUID) && (a.ReplyRef == "" || (a.ResultObserved && validCoordinationRef(a.ReplyRef)))
}

type claudeDialogueGuardEvidence struct {
	MessageRef         string
	TargetAgentUID     string
	AuthorizedReplyRef string
	ExecutionProcess   coremetadata.ProcessIdentity
}

type claudeDialogueToolEvidence struct {
	claudeDialogueObservedTool
	GuardSelectionMatched bool                         `json:"guardSelectionMatched"`
	GuardedCommitMatched  bool                         `json:"guardedCommitMatched"`
	ExecutionProcess      coremetadata.ProcessIdentity `json:"executionProcess"`
	ExecutingAtRead       bool                         `json:"executingAtRead"`
}

func (s *claudeCoordinationServer) dialogueToolEvidence() []claudeDialogueToolEvidence {
	if s.profile == nil || s.tool == nil {
		return nil
	}
	s.profile.mu.Lock()
	observed := maps.Clone(s.profile.tools)
	s.profile.mu.Unlock()
	s.tool.mu.Lock()
	guarded := maps.Clone(s.tool.evidence)
	s.tool.mu.Unlock()
	ids := slices.Sorted(maps.Keys(observed))
	out := make([]claudeDialogueToolEvidence, 0, len(ids))
	for _, id := range ids {
		action := observed[id]
		guard, exists := guarded[id]
		item := claudeDialogueToolEvidence{claudeDialogueObservedTool: action}
		item.GuardSelectionMatched = exists && guard.MessageRef == action.MessageRef && guard.TargetAgentUID == action.TargetAgentUID
		if item.GuardSelectionMatched {
			item.ExecutionProcess = guard.ExecutionProcess
			if !action.ResultObserved && guard.ExecutionProcess.Valid() {
				actual, _, err := localipc.Process(guard.ExecutionProcess.PID)
				item.ExecutingAtRead = err == nil && actual == guard.ExecutionProcess && s.tool.currentExecutable(actual)
			}
			// A consumed execution witness alone cannot prove the durable commit.
			// Read its separately correlated hub outcome without nesting gate locks.
			s.hub.mu.Lock()
			message := s.hub.messages[action.MessageRef]
			item.GuardedCommitMatched = action.ResultObserved && action.ReplyRef != "" && guard.AuthorizedReplyRef == action.ReplyRef && message != nil && !message.replyReserved && message.replyRef == action.ReplyRef
			s.hub.mu.Unlock()
		}
		out = append(out, item)
	}
	return out
}
