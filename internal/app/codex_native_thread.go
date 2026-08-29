package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/version"
)

const codexNativeThreadTimeout = 25 * time.Second

type codexNativeThreadController interface {
	Create(context.Context, coremetadata.AgentWorkspace, string, string) (codexappserver.ThreadBinding, error)
	Resume(context.Context, coremetadata.AgentWorkspace, string) (codexappserver.ThreadBinding, error)
	CanFallback(error) bool
}

type defaultCodexNativeThreadController struct{}

func (defaultCodexNativeThreadController) Create(ctx context.Context, workspace coremetadata.AgentWorkspace, prompt, generation string) (codexappserver.ThreadBinding, error) {
	return codexappserver.StartDefaultThread(ctx, version.String(), workspace.CWD, workspace.AdditionalWritableRoots, prompt, generation)
}

func (defaultCodexNativeThreadController) Resume(ctx context.Context, workspace coremetadata.AgentWorkspace, threadID string) (codexappserver.ThreadBinding, error) {
	return codexappserver.ResumeDefaultThread(ctx, version.String(), workspace.CWD, workspace.AdditionalWritableRoots, threadID)
}

func (defaultCodexNativeThreadController) CanFallback(err error) bool {
	return codexappserver.CanFallback(err)
}

type codexNativeAgentLauncher interface {
	PlanNativeCodexResume(coremetadata.AgentWorkspace, string) (title string, argv []string, err error)
	BindNativeCodexPane(paneID, contextDir, title, threadID string)
	BindAgentPaneOnRoute(context.Context, tmuxCommandRunner, agentPaneBinding) error
}

func bindNativeCodexPaneOnRoute(
	ctx context.Context,
	launcher codexNativeAgentLauncher,
	runner tmuxCommandRunner,
	paneID, contextDir, title, threadID string,
) error {
	return launcher.BindAgentPaneOnRoute(ctx, runner, agentPaneBinding{
		PaneID: paneID, Provider: aiModeCodex, ContextDir: contextDir, Title: title,
		ConversationID: threadID, ThreadID: threadID, NativeCodex: true,
	})
}

type codexNativeLaunchOutcomeRow struct {
	Action       string
	NativeResult string
	Launch       string
	Binding      string
}

// codexNativeLaunchOutcomeTable is the closed native launch outcome contract.
//
// Only two rows still reach the plain CLI lane, and neither of them is a
// degradation: an empty-prompt create has no native input to attach, and
// `--interactive-only` is the operator asking for a plain interactive Codex
// Agent. Every other unproven native authority refuses.
var codexNativeLaunchOutcomeTable = []codexNativeLaunchOutcomeRow{
	{Action: "create", NativeResult: "thread+turn", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread/turn"},
	{Action: "resume", NativeResult: "same thread", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread"},
	{Action: "create", NativeResult: "empty prompt before provider mutation", Launch: "current CLI", Binding: "current hook late-ack/refinement"},
	{Action: "create", NativeResult: "explicit --interactive-only", Launch: "current CLI", Binding: "no native binding; native turn control unavailable"},
	{Action: "create", NativeResult: "prompted create, unavailable or unsupported before provider mutation", Launch: "none", Binding: "write zero; refuse and name --interactive-only"},
	{Action: "create", NativeResult: "prompted create, selector resolved several Windows", Launch: "none", Binding: "write zero before allocation; refuse and name --interactive-only"},
	{Action: "resume", NativeResult: "unavailable or unsupported before provider mutation", Launch: "none", Binding: "write zero; refuse without a second lane"},
	{Action: "create", NativeResult: "indeterminate after thread creation", Launch: "none", Binding: "write zero; refuse duplicate lane"},
}

func nativePrompt(payload []string) (string, bool) {
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

func prepareNativeContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, codexNativeThreadTimeout)
}

func nativeFallbackAllowed(controller codexNativeThreadController, err error) bool {
	return err != nil && controller != nil && controller.CanFallback(err)
}

func nativeLaunchError(spelling string, err error) error {
	if err == nil {
		return nil
	}
	// Provider identity is already indeterminate here, so this refusal must not
	// offer any second lane -- `--interactive-only` included. Starting another
	// Codex process now could submit the same prompt twice.
	return errors.New(spelling + ": native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: " + err.Error())
}

// interactiveOnlyFlag is the one public spelling that asks for a plain-CLI
// Codex Agent with no native thread binding. It is named in every refusal that
// has an explicit escape hatch, and in none that does not.
const interactiveOnlyFlag = "--interactive-only"

// nativeThreadReason renders the typed classification of a native preparation
// failure. A refusal has to say why native authority could not be proven, not
// only that it could not, or the operator cannot tell an unreachable endpoint
// from an endpoint that is missing a capability.
func nativeThreadReason(err error) string {
	var action *codexappserver.ThreadActionError
	if errors.As(err, &action) {
		if reason := strings.TrimSpace(action.Reason); reason != "" {
			return reason
		}
	}
	return "unclassified"
}

// nativeRootsUnsupported reports whether a native failure is the fail-closed
// additional-writable-roots classification.
//
// That row is not a safe fallback -- launching anyway would narrow the
// operator's writable workspace -- but it is still raised before any provider
// conversation exists, so it belongs with the pre-mutation refusals and not
// with the indeterminate one.
func nativeRootsUnsupported(err error) bool {
	return nativeThreadReason(err) == codexappserver.ReasonAdditionalRootsUnsupported
}

// nativeCreatePreparationRefusal answers a prompted native Codex create whose
// native authority could not be proven before any provider conversation was
// mutated.
//
// Nothing is committed and nothing is launched. The old behaviour -- quietly
// creating a managed Agent on the hook/plain lane -- produced an Agent that
// looked native, carried no thread binding, and answered no native turn
// control, so the degradation is now a refusal that names the explicit opt-out.
func nativeCreatePreparationRefusal(spelling string, err error) error {
	if err == nil {
		return nil
	}
	return errors.New(spelling + ": native Codex thread preparation is unavailable (" + nativeThreadReason(err) +
		") and no provider conversation was mutated; refusing to create a managed Agent with no native thread binding. " +
		"Re-run with " + interactiveOnlyFlag + " for a plain interactive Codex Agent with no native turn control, " +
		"or make the Codex app-server endpoint available: " + err.Error())
}

// nativeResumePreparationRefusal answers a stored-thread resume whose native
// authority could not be proven. A resume names an existing app-server
// conversation, so there is no launch mode to fall back to and no
// `--interactive-only` escape hatch to offer: the only honest answer is that
// the stored thread cannot be resumed natively right now.
func nativeResumePreparationRefusal(spelling string, err error) error {
	if err == nil {
		return nil
	}
	return errors.New(spelling + ": the stored Codex thread cannot be resumed natively right now (" + nativeThreadReason(err) +
		"); refusing to rebind it onto a lane with no native turn control: " + err.Error())
}

// nativeFanOutRefusal answers a default native Codex create whose selector
// resolved several Windows.
//
// One create can own exactly one native thread, and a Registry rollback cannot
// delete app-server threads, so a fan-out has no atomic native shape. It is a
// usage error because the selector is what is wrong: narrowing it, or asking
// for the plain-CLI fan-out on purpose, both fix it.
func nativeFanOutRefusal(spelling string, targets int) error {
	return usageError(fmt.Sprintf(
		"%s with a prompt creates exactly one native Codex thread, but this selector resolved %d Windows; "+
			"narrow the selector to one Window, or pass %s to keep the plain-CLI fan-out of one Agent per resolved Window",
		spelling, targets, interactiveOnlyFlag))
}

// requireInteractiveOnlyProvider refuses `--interactive-only` on a provider
// that has no native lane to opt out of. Accepting it as a silent no-op would
// tell the operator their Claude or Antigravity Agent was launched under a mode
// that never existed.
func requireInteractiveOnlyProvider(spelling, provider string, flags resourceCreateFlags) error {
	if !flags.interactiveOnly || provider == aiModeCodex {
		return nil
	}
	return usageError(fmt.Sprintf(
		"%s %s applies only to --provider %s; %s has no native thread binding to opt out of",
		spelling, interactiveOnlyFlag, aiModeCodex, provider))
}
