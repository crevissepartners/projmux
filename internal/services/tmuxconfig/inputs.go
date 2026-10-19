package tmuxconfig

import (
	"github.com/crevissepartners/projmux/internal/core/aibadge"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const (
	tmuxWindowTitleWidth        = 10
	paneLabelOption             = tmuxopts.PaneName
	TmuxConfigDigestOption      = "@projmux_config_digest"
	AiBadgeStyleTmuxOption      = "@projmux_ai_badge_style"
	DesktopNotifyModeTmuxOption = "@projmux_desktop_notify_mode"
	LiveResourcesTmuxOption     = "@projmux_live_resources"
	aiBadgeKindApprovalRequired = aibadge.ApprovalRequired
	aiBadgeKindInputRequired    = aibadge.InputRequired
	aiBadgeKindResponseComplete = aibadge.ResponseComplete
	aiBadgeKindInProgress       = aibadge.InProgress
)
