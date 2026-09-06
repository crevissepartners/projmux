package agentmessage

import "slices"

// Principal and Action form the exhaustive peer-authority matrix. The broker
// accepts only PrincipalPeer, but keeping the other authority domains explicit
// prevents coordination from inheriting their permissions by omission.
type Principal string
type Action string

const (
	PrincipalHuman            Principal = "human"
	PrincipalPeer             Principal = "peer-agent"
	PrincipalProviderRuntime  Principal = "provider-runtime"
	PrincipalApprovalReviewer Principal = "approval-reviewer"
)

const (
	ActionCoordinationSend  Action = "coordination-send"
	ActionCoordinationRead  Action = "coordination-read"
	ActionCoordinationReply Action = "coordination-reply"
	ActionTurnStart         Action = "turn-start"
	ActionTurnSteer         Action = "turn-steer"
	ActionTurnInterrupt     Action = "turn-interrupt"
	ActionApprovalReview    Action = "approval-review"
	ActionConfigWrite       Action = "config-write"
	ActionToolOrConnector   Action = "tool-or-connector"
	ActionModelHistoryWrite Action = "model-history-write"
)

func Principals() []Principal {
	return []Principal{PrincipalHuman, PrincipalPeer, PrincipalProviderRuntime, PrincipalApprovalReviewer}
}

func Actions() []Action {
	return []Action{ActionCoordinationSend, ActionCoordinationRead, ActionCoordinationReply, ActionTurnStart,
		ActionTurnSteer, ActionTurnInterrupt, ActionApprovalReview, ActionConfigWrite, ActionToolOrConnector,
		ActionModelHistoryWrite}
}

func Authorize(principal Principal, action Action) bool {
	if !slices.Contains(Principals(), principal) || !slices.Contains(Actions(), action) {
		return false
	}
	switch principal {
	case PrincipalHuman:
		return action == ActionCoordinationSend || action == ActionCoordinationRead || action == ActionCoordinationReply ||
			action == ActionTurnStart || action == ActionTurnSteer || action == ActionTurnInterrupt || action == ActionConfigWrite
	case PrincipalPeer:
		return action == ActionCoordinationSend || action == ActionCoordinationRead || action == ActionCoordinationReply
	case PrincipalProviderRuntime:
		return action == ActionToolOrConnector || action == ActionModelHistoryWrite
	case PrincipalApprovalReviewer:
		return action == ActionApprovalReview
	default:
		return false
	}
}
