package projmux

import "github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"

// IsClaudeApprovalRequestID reports whether id has the shape of a permission
// request id projmux mints for a Claude Agent: "permission-" followed by 16
// lowercase hexadecimal digits, with nothing before or after; id is not
// trimmed. AnswerApproval trims a Claude Agent's RequestID and refuses it with
// CodePermissionNotFound before it is answered when this is false. A true
// result does not mean the request exists or is pending. It reads nothing,
// needs no Client and never fails.
func IsClaudeApprovalRequestID(id string) bool {
	return agentapproval.ValidID(id)
}
