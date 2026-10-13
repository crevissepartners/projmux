package runtime

import (
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"strings"
)

func NativePrompt(payload []string) (string, bool) {
	switch len(payload) {
	case 0:
		return "", true
	case 1:
		return payload[0], true
	default:
		// The legacy CLI owns provider parsing for a multi-operand payload. Native
		// turn/start accepts one text item, so joining operands here would silently
		// invent prompt semantics.
		return "", false
	}
}

func NativeLaunchError(spelling string, err error) error {
	if err == nil {
		return nil
	}
	// Provider identity is already indeterminate here, so this refusal must not
	// offer any second lane -- `--interactive-only` included. Starting another
	// Codex process now could submit the same prompt twice.
	return fmt.Errorf("%s: native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: %w", spelling, err)
}

func NativeThreadReason(err error) string {
	var route *NativeCodexRouteError
	if errors.As(err, &route) && strings.TrimSpace(route.Reason) != "" {
		return route.Reason
	}
	var action *codexappserver.ThreadActionError
	if errors.As(err, &action) {
		if reason := strings.TrimSpace(action.Reason); reason != "" {
			return reason
		}
	}
	return "unclassified"
}

func NativeRootsUnsupported(err error) bool {
	return NativeThreadReason(err) == codexappserver.ReasonAdditionalRootsUnsupported
}

func NativeCreatePreparationRefusal(spelling string, err error, capability codexappserver.InstallCapability) error {
	if err == nil {
		return nil
	}
	guidance := CodexInstallCapabilityGuidance(capability)
	return errors.New(spelling + ": native Codex thread preparation is unavailable (" + NativeThreadReason(err) +
		") and no provider conversation was mutated; refusing to create a managed Agent with no native thread binding. " +
		"Re-run with " + "--interactive-only" + " for a plain interactive Codex Agent with no native turn control, " +
		"or make the shared Codex app server attachable through `codex app-server daemon` as the operator action below describes. Install capability " + string(guidance.Capability) + ": " +
		guidance.Text() + ". Native error: " + err.Error())
}

func NativeFanOutRefusal(spelling string, targets int) error {
	return createUsage(fmt.Sprintf(
		"%s creates exactly one native Codex thread, but this selector resolved %d Windows; "+
			"narrow the selector to one Window, or pass %s to keep the plain-CLI fan-out of one Agent per resolved Window",
		spelling, targets, "--interactive-only"))
}
