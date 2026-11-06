package projmux

import (
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

// Input size limits, in bytes, of the configuration inputs the SDK validates.
// Each is the limit its validator enforces, not a copy of it.
const (
	// MaxProjectLinkRulesBytes bounds one Project's label link rules: a stored
	// rule file larger than it is a CodeLinkRulesUnreadable read refusal, and
	// rules whose stored encoding would be larger are a CodeInvalidLinkRules
	// write refusal.
	MaxProjectLinkRulesBytes = projectlinks.MaxFileSize
	// MaxAgentGuidanceBytes bounds the host-global and Project agent guidance:
	// a larger candidate is a CodeAgentGuidanceTooLarge refusal.
	MaxAgentGuidanceBytes = agentguidance.MaxSize
	// MaxPersonaBytes bounds one persona file.
	MaxPersonaBytes = persona.MaxSize
	// MaxProfileBytes bounds one profile file: a larger candidate is a
	// CodeProfileTooLarge refusal.
	MaxProfileBytes = profile.MaxSize
)
