package aibadge

import "strings"

const (
	InProgress       = "in_progress"
	ApprovalRequired = "approval_required"
	InputRequired    = "input_required"
	ResponseComplete = "response_complete"
)

func Normalize(kind string) string {
	switch strings.TrimSpace(kind) {
	case InProgress:
		return InProgress
	case ApprovalRequired:
		return ApprovalRequired
	case InputRequired:
		return InputRequired
	case ResponseComplete:
		return ResponseComplete
	default:
		return ""
	}
}

func Priority(kind string) int {
	switch Normalize(kind) {
	case ApprovalRequired, InputRequired:
		return 3
	case ResponseComplete:
		return 2
	case InProgress:
		return 1
	default:
		return 0
	}
}

func Aggregate(current, next string) string {
	current = Normalize(current)
	next = Normalize(next)
	if Priority(next) > Priority(current) {
		return next
	}
	return current
}
