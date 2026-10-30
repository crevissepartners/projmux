package core

// ProcessAttentionRequest asks for the process attention of every process
// Agent. It has no fields yet.
type ProcessAttentionRequest struct{}

// ProcessAttentionBadge is the attention a process Agent shows: its open
// questions and approvals, a turn in progress, or a completed response nobody
// acknowledged yet.
type ProcessAttentionBadge string

const (
	ProcessAttentionInProgress       ProcessAttentionBadge = "in_progress"
	ProcessAttentionApprovalRequired ProcessAttentionBadge = "approval_required"
	ProcessAttentionInputRequired    ProcessAttentionBadge = "input_required"
	ProcessAttentionResponseComplete ProcessAttentionBadge = "response_complete"
)

// ProcessAttention maps an Agent UID (without the "uid:" prefix) to its badge.
// An Agent with no badge to show is absent. The map belongs to the caller.
//
// It is read from the durable process attention store and the current Registry
// declarations, so it differs in source from AgentProcessSnapshot.Attention,
// which is a projection of the Registry's interactions for one Agent. No owner
// socket is read: a badge can outlive an owner that is already gone, and an
// Agent that has not recorded anything yet has none.
type ProcessAttention struct {
	Agents map[string]ProcessAttentionBadge `json:"agents"`
}
