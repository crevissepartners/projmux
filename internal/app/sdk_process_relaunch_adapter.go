package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// The SDK uses the existing relaunch planner/owner. A read-only plan and an
// explicit caller acknowledgment precede the first Stop or provider reservation.
func (c *agentCommand) runSDKProcessRelaunchOwner(life context.Context, cancel context.CancelFunc, session *processDetachedRelaunchSession, reg m.Registry, target m.Agent, request agentRelaunchRequest) error {
	preview := request
	preview.dryRun = true
	var planned agentHostTransferResult
	planner := *c
	planner.hostTransferResult = &planned
	fail := func(err error) error {
		return encodeDetachedRelaunchError(session.encoder, session.identity, processhost.Binding{}, &planned.Result, err)
	}
	if err := planner.dispatchRelaunch(reg, target, preview, io.Discard, io.Discard); err != nil {
		return fail(err)
	}
	if request.dryRun {
		return finishSDKRelaunch(session, planned.Result)
	}
	if planned.Result.PromptRequired {
		return fail(usageError("agent relaunch: lost-host Claude requires an explicit first prompt; nothing was changed; recover with: " + planned.Result.RelaunchCommand))
	}
	if planned.Result.ConfirmationRequired && !request.yes {
		return fail(usageError(fmt.Sprintf("agent relaunch: agent/%s is %s with interaction %s; restarting would cut that turn; re-run with --yes (%s); nothing was changed", target.Metadata.Name, target.Status.Phase, planned.Result.Interaction, relaunchReasonAgentBusy)))
	}
	raw, _ := json.Marshal(planned.Result)
	if err := session.encoder.Encode(processDetachedReply{Owner: session.identity, Phase: "planned", Relaunch: raw}); err != nil {
		return err
	}
	var admit string
	if err := session.decoder.Decode(&admit); err != nil {
		return err
	}
	if admit != "admit" || life.Err() != nil {
		return fail(context.Canceled)
	}
	// A tmux destination has no independent process lifetime; after admission
	// its existing Stop/rebind recovery runs to completion.
	if request.host == "tmux" {
		session.timer.Stop()
	}
	planned.Result.DryRun = request.dryRun
	c.hostTransferResult = &agentHostTransferResult{Result: planned.Result}
	if err := c.dispatchRelaunch(reg, target, request, io.Discard, io.Discard); err != nil {
		return encodeDetachedRelaunchError(session.encoder, session.identity, processhost.Binding{}, &c.hostTransferResult.Result, err)
	}
	// Process destinations return through finishOwned/finishClaim. A tmux
	// destination completes synchronously and acknowledges its exact rebind.
	return finishSDKRelaunch(session, c.hostTransferResult.Result)
}

func finishSDKRelaunch(s *processDetachedRelaunchSession, result agentRelaunchResult) error {
	raw, _ := json.Marshal(result)
	if err := s.encoder.Encode(processDetachedReply{Owner: s.identity, Binding: processhost.Binding{Agent: result.AgentUID}, Phase: "complete", Relaunch: raw}); err != nil {
		return err
	}
	var commit string
	if err := s.decoder.Decode(&commit); err != nil {
		return err
	}
	if commit != "commit" {
		return errors.New("process-detached-commit-invalid")
	}
	return s.encoder.Encode("owned")
}

func encodeDetachedRelaunchError(encoder interface{ Encode(any) error }, owner m.ProcessIdentity, binding processhost.Binding, result *agentRelaunchResult, err error) error {
	code := "relaunch-execution-failed"
	if errors.Is(err, processhost.ErrStale) {
		code = "relaunch-source-stale"
	}
	for _, token := range []string{relaunchReasonAgentBusy, relaunchReasonSelfTarget, relaunchReasonNoConversation, relaunchReasonProviderUnsupported, "profile-resume-unavailable", "relaunch-profile-not-found", "relaunch-profile-invalid"} {
		if strings.Contains(err.Error(), token) {
			code = token
			break
		}
	}
	var raw json.RawMessage
	if result != nil {
		raw, _ = json.Marshal(result)
	}
	_ = encoder.Encode(processDetachedReply{Owner: owner, Binding: binding, Relaunch: raw, Error: err.Error(), ErrorCode: code})
	return err
}

func (c *agentCommand) bindProcessRelaunchContext(creator *createCommand, frozen mutationruntime.ProcessCreateContext, wire processDetachedRelaunchRequest) {
	c.bindProcessResumeContext(creator, frozen)
	// bindProcessCreateContext has verified the shared AI adapter. Native
	// observers created by a process-to-tmux move need the same frozen domain.
	creator.agents.(*aiCommand).nativeObserverPaths = &mutationruntime.NativeObserverPaths{
		Paths: frozen.Paths, RegistryPath: c.messagePaths.registryPath, Executable: frozen.Executable,
	}
	deletion := newDeleteCommand()
	deletion.store = creator.store
	deletion.lookupEnv = creator.lookupEnv
	c.paneDelete = deletion
	// Reuse the SDK's explicit tmux materializer as well as the owned process
	// launcher. The ordinary CLI binders intentionally retain their ambient route.
	request, err := parseAgentRelaunchArgs(wire.Args, io.Discard)
	if err != nil {
		return
	} // The entrypoint validates the same request before effects.
	execution, _ := mutationruntime.NewSDKCreate(frozen.Paths, request.socket.socketPath, creator.shell, frozen.Executable, creator.sessionNameFor, tmux.ExecRunner{}, func() ([]string, error) { return nil, nil })
	creator.runtime = execution.Runtime
	creator.runtimeBound = false
	bind := func(ctx context.Context) error {
		if err := execution.EnsureRuntimeRoute(ctx); err != nil {
			return err
		}
		creator.reconciler = execution.Reconciler
		return nil
	}
	creator.bindRuntime, creator.bindExplicitRuntime = bind, bind
}
