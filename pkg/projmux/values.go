package projmux

import (
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// ErrCodexThreadPolicyMismatch is the sentinel of a Codex thread whose
// effective sandbox or approval policy is not the one requested. A Refusal
// that wraps such a failure reaches it through Unwrap, so errors.Is finds it
// without matching error text. It is never a safe fallback: retrying without
// the requested policy would silently drop the capability the profile asked
// for.
var ErrCodexThreadPolicyMismatch = codexappserver.ErrPolicyMismatch

// CodexThreadPolicyMismatchReason is the stable reason token that starts the
// error text of such a thread.
const CodexThreadPolicyMismatchReason = codexappserver.ReasonPolicyMismatch

// IsUsageError reports whether err, or an error it wraps, was caused by
// invalid user input. The CLI exits with code 2 for exactly these errors.
func IsUsageError(err error) bool { return metadata.IsUsageError(err) }
