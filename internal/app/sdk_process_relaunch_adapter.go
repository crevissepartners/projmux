package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
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
		return fail(usageError("agent relaunch: agent/" + target.Metadata.Name + " would interrupt work; re-run with --yes (relaunch-agent-busy); nothing was changed"))
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
	c.hostTransferResult = &agentHostTransferResult{}
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
