package core

import (
	"encoding/json"
	"time"
)

// ApprovalState is the existing permission request state.
type ApprovalState string

// OfferedDecision is a provider-supplied decision. Review accepts only an offered value.
type OfferedDecision string
type ApprovalList struct {
	// Provider is claude or codex; it tells which fields a record carries.
	Provider  string            `json:"provider"`
	AgentUID  string            `json:"agentUID"`
	AgentName string            `json:"agentName"`
	Answering string            `json:"answering"`
	Requests  []ApprovalRequest `json:"requests"`
}

type ApprovalRequest struct {
	ID        string          `json:"id"`
	State     ApprovalState   `json:"state"`
	ToolName  string          `json:"toolName"`
	AgentType string          `json:"agentType,omitempty"`
	ToolInput json.RawMessage `json:"toolInput"`
	CreatedAt time.Time       `json:"createdAt,omitzero"`
	Deadline  time.Time       `json:"deadline,omitzero"`
	// Answers are the `agent approval answer` flags that can settle a Codex
	// request: allow, deny, both, or none.
	Answers []string `json:"answers,omitempty"`
	// DenyDecision is the Codex decision --deny would send: decline (deny,
	// the turn continues) or cancel (deny and interrupt the turn). It is
	// empty when --deny cannot answer the request.
	DenyDecision string `json:"denyDecision,omitempty"`
	// Answerable is set on every Codex record: false when its id is
	// ambiguous across raw request ids or no answer flag applies, and
	// `agent approval review` must answer it instead.
	Answerable *bool `json:"answerable,omitempty"`
}

// ApprovalReceipt records a single accepted answer or explicit review.
type ApprovalReceipt struct {
	RequestID, State, AgentUID, AgentName string
	Decision                              OfferedDecision
	Effect, Warning                       string
}

// PendingApproval is a native review display projection; it contains no raw provider scalar identity.
type PendingApproval struct {
	RequestID       string            `json:"requestId"`
	Kind            string            `json:"kind"`
	ThreadID        string            `json:"threadId"`
	TurnID          string            `json:"turnId"`
	ItemID          string            `json:"itemId"`
	ApprovalID      *string           `json:"approvalId,omitempty"`
	Command         string            `json:"command,omitempty"`
	CWD             string            `json:"cwd,omitempty"`
	NetworkHost     string            `json:"networkHost,omitempty"`
	NetworkProtocol string            `json:"networkProtocol,omitempty"`
	RequestCWD      string            `json:"requestCwd,omitempty"`
	Reason          string            `json:"reason,omitempty"`
	GrantRoot       *string           `json:"grantRoot,omitempty"`
	Permissions     json.RawMessage   `json:"permissions,omitempty"`
	Decisions       []OfferedDecision `json:"decisions"`
}

// OfferedDecisions returns the native review decisions in this request's
// toolInput, in provider order. Claude store requests offer no native review
// decisions. Returned values are a copy; they do not widen permission policy.
func (r ApprovalRequest) OfferedDecisions() []OfferedDecision {
	var input struct {
		Decisions []OfferedDecision `json:"decisions"`
	}
	if json.Unmarshal(r.ToolInput, &input) != nil {
		return nil
	}
	return input.Decisions
}
