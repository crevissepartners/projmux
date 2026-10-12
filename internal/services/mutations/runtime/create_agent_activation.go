package runtime

import (
"context"
"errors"
"fmt"
"github.com/crevissepartners/projmux/internal/aiprovider"
"github.com/crevissepartners/projmux/internal/cli"
"github.com/crevissepartners/projmux/internal/core/selector"
"github.com/crevissepartners/projmux/internal/diagnostics"
"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
"io"
"strings"
"time"
coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Provider startup and initial-task acknowledgement are separate bounded
// stages. Exact SessionStart evidence opens the acknowledgement window; it does
// not itself acknowledge the task. Keeping them separate lets a provider spend
// startup time before a delayed hook without turning one larger timeout into
// the contract.
//
// Both bounds were five seconds, which is inside the cost of starting a real
// provider on a busy machine, so a create that had worked would still exit
// nonzero and offer to delete the live Agent. Measured on a 14-core machine
// under `while :; do :; done` at 1x and 3x nproc (observed loadavg 37-72, real
// `claude`, n=20, the product's own stage timestamps):
//
//	stage           p50     p90     observed max
//	startup         9.5s    13.9s   24.4s  (loadavg 69.8)
//	acknowledgement 4.2s     5.2s    8.6s  (loadavg 63.3)
//
// Each bound covers its own peak plus one more step of the observed tail slope,
// max + (max - p90). Twenty samples make a max a weak tail estimate, and this
// tail is heavy: startup jumps 1.75x from p90 to max. Sizing to the sample max
// alone would leave the bound one unlucky run from the behavior this change
// exists to remove.
//
// The asymmetry is the reason to spend the headroom rather than save it. A
// bound that is too low deletes live work; a bound that is too high only delays
// reporting a provider that really is dead, once, on the failure path -- worst
// case 47s for one target and 47s x targets for a fan-out create, which waits
// per target in confirmAgentActivations. The same load that produced these
// peaks also put 19-61s of Registry and tmux work *before* this wait: those
// creates took 26-90s end to end.
//
// The headroom is also what makes the exit code honest. Exceeding a bound is
// reported as a failure only because it is meant to mean "the task never
// arrived" rather than "this run was slower than the sample" -- so the claim
// depends on the margin, not just on the timeout existing.
//
// These are not a sufficiency proof. They cover the measured envelope up to
// loadavg ~72 on this machine class; heavier load is explicitly outside it.
const (
	agentActivationStartupDeadline         = 35 * time.Second
	agentActivationAcknowledgementDeadline = 12 * time.Second
)

// activationUnconfirmedDiagnosticSteps is the ordered remediation an
// unconfirmed activation prints. The order is the contract, not the prose: a
// nonzero exit used to lead with `delete agent`, and operators -- including
// this repository's own launcher automation, which propagates the exit code --
// deleted Agents that were alive and already working, because a provider hook
// delayed past the bound still refines the activation to acknowledged
// afterwards. Reading committed authority is therefore always cheaper and safer
// than deleting, so deletion is last and conditional on the two reads above it
// finding no evidence.
func activationUnconfirmedDiagnosticSteps(target agentActivationTarget) []string {
	return []string{
		fmt.Sprintf("the Agent and its managed Pane %s were created and are still live; nothing was rolled back", target.paneID),
		fmt.Sprintf("re-read the committed activation with `projmux describe agent uid:%s` first: a provider hook that arrived after the bound refines it to acknowledged", target.agentUID),
		fmt.Sprintf("if it is still unconfirmed, look at Pane %s (`tmux capture-pane -p -t %s`) or the provider transcript for the initial task, and retry it through the provider when it never arrived", target.paneID, target.paneID),
		fmt.Sprintf("only when neither read shows activation evidence, clean up with `projmux delete agent uid:%s --yes`", target.agentUID),
	}
}

// activationUnconfirmedDiagnostic names the exact live resources first and then
// prints activationUnconfirmedDiagnosticSteps in order.
func activationUnconfirmedDiagnostic(target agentActivationTarget, reason string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "create agent: agent/%s uid:%s has live managed Pane %s but initial task activation was not confirmed: %s",
		target.agentName, target.agentUID, target.paneID, reason)
	for _, step := range activationUnconfirmedDiagnosticSteps(target) {
		builder.WriteString("; ")
		builder.WriteString(step)
	}
	return builder.String()
}
