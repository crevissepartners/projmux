package aibadge

import "strings"

const (
	InProgress       = "in_progress"
	ApprovalRequired = "approval_required"
	InputRequired    = "input_required"
	ResponseComplete = "response_complete"
)

const (
	StyleDot   = "dot"
	StyleEmoji = "emoji"
	StyleOff   = "off"
)

const (
	RoleProgress       = "progress"
	RoleSuccess        = "success"
	RoleActionRequired = "action_required"
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

func NormalizeStyle(style string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case StyleEmoji:
		return StyleEmoji
	case StyleOff:
		return StyleOff
	default:
		return StyleDot
	}
}

func Glyph(kind, style string) string {
	switch NormalizeStyle(style) {
	case StyleEmoji:
		switch Normalize(kind) {
		case ApprovalRequired, InputRequired:
			return "⏳"
		case ResponseComplete:
			return "✅"
		case InProgress:
			return "🔄"
		default:
			return " "
		}
	case StyleOff:
		return " "
	default:
		switch Normalize(kind) {
		case ApprovalRequired, InputRequired, ResponseComplete, InProgress:
			return "●"
		default:
			return " "
		}
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

func ThemeRole(kind string) string {
	switch Normalize(kind) {
	case ApprovalRequired, InputRequired:
		return RoleActionRequired
	case ResponseComplete:
		return RoleSuccess
	case InProgress:
		return RoleProgress
	default:
		return ""
	}
}
