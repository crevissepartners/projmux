package projmux

import (
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// ErrCodexThreadPolicyMismatch is the sentinel of a Codex thread whose
// effective sandbox or approval policy is not the one requested. A Refusal
// that wraps such a failure reaches it through Unwrap, so errors.Is finds it
// without matching error text. It is never a safe fallback: retrying without
// the requested policy would silently drop the capability the profile asked
// for.
var ErrCodexThreadPolicyMismatch = codexappserver.ErrPolicyMismatch

// ErrResumeNotPrepared is the sentinel of a PrepareAgentResume whose worker
// finished without leaving a prepared plan. The refusal keeps the code
// CodeResumeExecutionFailed and its error text, process-resume-not-prepared;
// errors.Is finds the sentinel through the *Refusal's Unwrap chain without
// matching that text. Other resume execution failures are not matched.
var ErrResumeNotPrepared = mutationruntime.ErrResumeNotPrepared

// CodexThreadPolicyMismatchReason is the stable reason token that starts the
// error text of such a thread.
const CodexThreadPolicyMismatchReason = codexappserver.ReasonPolicyMismatch

// IsUsageError reports whether err, or an error it wraps, was caused by
// invalid user input. It exposes the metadata usage marker only: the CLI maps
// such an error to exit code 2 when nothing in the chain states an explicit
// exit code of its own, and an explicit exit code wins.
func IsUsageError(err error) bool { return metadata.IsUsageError(err) }

// RecordsDialogueReplyOnly reports whether an Agent's annotations record the
// reply-only activation.
func RecordsDialogueReplyOnly(annotations map[string]string) bool {
	return metadata.RecordsDialogueReplyOnly(annotations)
}

// AINewWindowModes returns the modes a new AI window can open with, in the
// order refusals name them. The caller owns the returned slice.
func AINewWindowModes() []string { return append([]string(nil), config.AINewWindowModes...) }
