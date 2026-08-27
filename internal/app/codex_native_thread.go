package app

import (
	"context"
	"errors"
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

var codexNativeLaunchOutcomeTable = []codexNativeLaunchOutcomeRow{
	{Action: "create", NativeResult: "thread+turn", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread/turn"},
	{Action: "resume", NativeResult: "same thread", Launch: "remote resume without prompt", Binding: "exact Agent/Pane/generation/thread"},
	{Action: "create/resume", NativeResult: "empty prompt, unavailable, or unsupported before provider mutation", Launch: "current CLI", Binding: "current hook late-ack/refinement"},
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
	return errors.New(spelling + ": native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: " + err.Error())
}
