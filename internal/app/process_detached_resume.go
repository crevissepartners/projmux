package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	sdkcore "github.com/crevissepartners/projmux/pkg/projmux/core"
)

// No private planner/claim pointers cross the launch boundary. Explicit resume
// retains its host-lost admission; deferred resume retains its retired proof.
type processDetachedResumeRequest mutationruntime.ProcessDetachedResumeRequest

func (r processDetachedResumeRequest) options() processAgentResumeOptions {
	return processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: r.Agent}, Model: r.Model, Effort: r.Effort, Prompt: r.Prompt, allowHostLost: !r.WaitForPeer, preparation: r.Preparation}
}

func (c *agentCommand) runDetachedProcessResume(agent coremetadata.Agent, flags resourceQueryFlags, model, effort string, dialogueOnly, peer bool, prompt []string, stdout, stderr io.Writer) error {
	if dialogueOnly {
		return usageError("agent resume: --detach does not support --dialogue-reply-only")
	}
	if peer && len(prompt) > 0 {
		return usageError("agent resume: --wait-for-peer cannot be combined with a prompt or --dialogue-reply-only")
	}
	mode, err := resolveLifecycleProjection("agent resume", flags.output)
	if err != nil {
		return usageError(err.Error())
	}
	if mode == cli.OutputModePaneID {
		return usageError("agent resume: process agents do not have a pane-id output")
	}
	wire := processDetachedResumeRequest{Agent: agent.Metadata.UID, Model: model, Effort: effort, WaitForPeer: peer}
	if len(prompt) > 0 {
		wire.Prompt = processResumeFirstFrame{Kind: "user", Text: strings.Join(prompt, " ")}
	}
	request, err := newProcessAgentResumeRequest(wire.options())
	if err != nil {
		return err
	}
	candidate, err := c.processResumeCandidate(request)
	if err != nil {
		return err
	}
	if !peer && candidate.Record.Provider == aiModeClaude && strings.TrimSpace(wire.Prompt.Text) == "" {
		return usageError("agent resume: process Claude requires -- <prompt>; stream-json emits init only after the first user frame")
	}
	wire.Previous = candidate.Record.Binding
	ctx, cancel := processForegroundLifetime()
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, processDetachedStartupTimeout)
	defer timeout()
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	shared := mutationruntime.ProcessDetachedResumeRequest(wire)
	_, err = mutationruntime.LaunchDetachedProcessResume(ctx, executable, mutationruntime.ProcessDetachedResumeEnvelope{Resume: &shared, Output: string(mode), Caller: caller, RegistryPath: c.messagePaths.registryPath}, c.loadRegistry, stdout, stderr)
	return err
}

func detachedClaimCurrent(command *createCommand, reg coremetadata.Registry, claim deferredClaimRecord, owner coremetadata.ProcessIdentity) bool {
	c := &agentCommand{messagePaths: agentMessagePaths{registryPath: claimRegistryPath(command)}}
	current, err := readDeferredClaim(c.deferredClaimPath(claim.Agent))
	return err == nil && current.Detached && !current.StopRequested && reflect.DeepEqual(current, claim) && claim.Process == owner && deferredClaimLive(current) && deferredClaimMatches(current, reg, claim.Agent)
}

func claimRegistryPath(command *createCommand) string {
	state, err := command.store.stateDir()
	if err != nil {
		return ""
	}
	return intmetadata.PathFor(state)
}

func runProcessDetachedResumeOwner(conn net.Conn, encoder *json.Encoder, decoder *json.Decoder, req processDetachedRequest, identity coremetadata.ProcessIdentity, c *agentCommand) error {
	wire := *req.Resume
	if wire.PreparationAction != "" || wire.Preparation != nil {
		if err := validateSDKProcessResumeWire(wire); err != nil {
			return encodeDetachedResumeError(encoder, identity, processSchemaBinding(wire.Previous), err)
		}
	}
	request, err := newProcessAgentResumeRequest(wire.options())
	if err != nil {
		return err
	}
	candidate, err := c.processResumeCandidate(request)
	if err != nil {
		return encodeDetachedResumeError(encoder, identity, processSchemaBinding(wire.Previous), err)
	}
	if candidate.Record.Binding != wire.Previous {
		return encodeDetachedResumeError(encoder, identity, processSchemaBinding(wire.Previous), processhost.ErrStale)
	}
	if wire.PreparationAction != "" {
		proof, prepareErr := runResumePreparation(context.Background(), c, wire)
		if prepareErr != nil {
			return encodeDetachedResumeError(encoder, identity, processSchemaBinding(wire.Previous), prepareErr)
		}
		if err = encoder.Encode(processDetachedReply{Owner: identity, Binding: processSchemaBinding(wire.Previous), Phase: "prepared", Preparation: &proof}); err != nil {
			return err
		}
		var commit string
		if err = decoder.Decode(&commit); err != nil {
			return err
		}
		if commit != "commit" {
			return errors.New("process-preparation-commit-invalid")
		}
		return encoder.Encode("completed")
	}
	var acknowledged *deferredLaunchRecord
	if wire.Preparation != nil {
		_, acknowledged, err = c.readResumePreparation(context.Background(), preparationOptions(*wire.Preparation), wire.Preparation)
		if err != nil {
			return encodeDetachedResumeError(encoder, identity, processSchemaBinding(wire.Previous), err)
		}
	}
	life, cancel := processForegroundLifetime()
	defer cancel()
	life = (processOwnerLifetime{}).withContext(life)
	timer := time.AfterFunc(processDetachedStartupTimeout, cancel)
	defer timer.Stop()
	if wire.WaitForPeer {
		return runDetachedClaimOwner(life, cancel, timer, conn, encoder, decoder, req, identity, c)
	}
	result, err := c.resumeProcessAgent(life, request)
	fail := func(cause error) error {
		failure := result.fail(cause)
		if wire.Preparation != nil {
			reply := processDetachedReply{Owner: identity, Binding: result.Binding, Error: failure.Error(), ErrorCode: processResumeErrorCode(failure), Preparation: c.failedResumePreparation(wire.Preparation, acknowledged, result)}
			_ = encoder.Encode(reply)
			return failure
		}
		return encodeDetachedResumeError(encoder, identity, result.Binding, failure)
	}
	if err != nil {
		return fail(err)
	}
	if err = encoder.Encode(processDetachedReply{Owner: identity, Binding: result.Binding, Phase: "starting"}); err != nil {
		return fail(err)
	}
	changed, controls, attention, err := result.resumeSynchronization(c.rebind.create)
	if err != nil {
		return fail(err)
	}
	var output, notices bytes.Buffer
	if err = result.writeResumeResult(c.rebind.create, &output, &notices, req.Output, true); err != nil {
		return fail(err)
	}
	if err = writeResumePrevious(&notices, req.Output, result.Previous); err != nil {
		return fail(err)
	}
	reply := processDetachedReply{Owner: identity, Binding: result.Binding, Stdout: output.String(), Stderr: notices.String()}
	if req.ResumeContext != nil {
		reply.Result, err = processResumeCommitted(c, metadataProcessBinding(result.Binding), result.owner.Notices)
		if err != nil {
			return fail(err)
		}
	}
	if err = encoder.Encode(reply); err != nil {
		return fail(err)
	}
	if err = detachedResumeCommit(life, decoder, &result); err != nil {
		return fail(err)
	}
	log, err := newProcessDetachedLog(req.RegistryPath, result.Binding)
	if err != nil {
		return fail(err)
	}
	defer log.file.Close()
	if err = encoder.Encode("owned"); err != nil {
		return fail(err)
	}
	timer.Stop()
	_ = conn.Close()
	return result.ownedWait(changed, controls, attention).run(life, cancel, nil, log)
}

func encodeDetachedResumeError(encoder *json.Encoder, owner coremetadata.ProcessIdentity, binding processhost.Binding, err error) error {
	reply := processDetachedReply{Owner: owner, Binding: binding, Error: err.Error(), ErrorCode: processResumeErrorCode(err)}
	var recipe *sdkcore.ResumeRecipeChangedError
	if errors.As(err, &recipe) {
		reply.ErrorDetail, _ = json.Marshal(recipe)
	}
	_ = encoder.Encode(reply)
	return err
}

func detachedResumeCommit(ctx context.Context, decoder *json.Decoder, result *processAgentResumeResult) error {
	var commit string
	if err := decoder.Decode(&commit); err != nil {
		return err
	}
	if commit != "commit" {
		return errors.New("process-detached-commit-invalid")
	}
	snapshot, err := result.Handle.Observe(result.Binding)
	if err != nil {
		return err
	}
	if snapshot.State == "exited" || snapshot.State == "unknown" || snapshot.State == "stopping" || ctx.Err() != nil {
		return errors.New("process-detached-owner-ended-before-commit")
	}
	if h, ok := result.Handle.(processGenerationHandle); ok {
		return h.CheckGeneration(ctx, result.Binding)
	}
	return nil
}

func runDetachedClaimOwner(life context.Context, cancel context.CancelFunc, timer *time.Timer, conn net.Conn, encoder *json.Encoder, decoder *json.Decoder, req processDetachedRequest, identity coremetadata.ProcessIdentity, c *agentCommand) error {
	claim, err := c.claimDeferredProcessAgent(life, req.Resume.options())
	if err != nil {
		return encodeDetachedResumeError(encoder, identity, processSchemaBinding(req.Resume.Previous), err)
	}
	defer func() { _ = claim.Close() }()
	binding := processSchemaBinding(claim.record.Binding)
	fail := func(cause error) error {
		return encodeDetachedResumeError(encoder, identity, binding, errors.Join(cause, claim.Close()))
	}
	var output, notices bytes.Buffer
	if req.Output != cli.OutputModeNone {
		writer := io.Writer(&notices)
		if req.Output == cli.OutputModeDefault {
			writer = &output
		}
		fmt.Fprintf(writer, "agent uid:%s pane uid:%s runtime=process foreground=detached claim=waiting\n", binding.Agent, binding.Pane)
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return fail(err)
	}
	agent, found := reg.Agent(binding.Agent)
	if !found {
		return fail(processhost.ErrStale)
	}
	created := createResult{kind: coremetadata.KindAgent, uid: binding.Agent, name: agent.Metadata.Name, action: cli.ActionReused, windowUID: binding.Window}
	if err = c.rebind.create.writeResultsWithReceipt(&output, "agent resume", req.Output, coremetadata.KindAgent, []createResult{created}, processResumeReceipt(binding, agent.Metadata.Name)); err != nil {
		return fail(err)
	}
	reply := processDetachedReply{Owner: identity, Binding: binding, Claim: &claim.record, Stdout: output.String(), Stderr: notices.String()}
	if req.ResumeContext != nil {
		reply.Result, err = processResumeCommitted(c, claim.record.Binding, nil)
		if err != nil {
			return fail(err)
		}
	}
	if err = encoder.Encode(reply); err != nil {
		return fail(err)
	}
	var commit string
	if err = decoder.Decode(&commit); err != nil {
		return fail(err)
	}
	if commit != "commit" || life.Err() != nil {
		return fail(errors.New("process-detached-commit-invalid"))
	}
	// Provider reservation waits for commit, so the handshake proves a
	// child-free exact claim. Peers may already be held, without starting birth.
	unlock, err := lockDeferredClaim(claim.path)
	if err != nil {
		return fail(err)
	}
	err = c.checkDeferredClaim(binding.Agent, claim)
	unlock()
	if err != nil {
		return fail(err)
	}
	logBinding := binding
	logBinding.Generation += "-claim-" + claim.record.Nonce
	log, err := newProcessDetachedLog(req.RegistryPath, logBinding)
	if err != nil {
		return fail(err)
	}
	defer log.file.Close()
	if err = encoder.Encode("owned"); err != nil {
		return fail(err)
	}
	timer.Stop()
	_ = conn.Close()
	result, err := claim.WaitPeer(life)
	if err != nil {
		if result.Handle == nil && life.Err() != nil {
			return nil
		}
		return err
	}
	changed, controls, attention, err := result.resumeSynchronization(c.rebind.create)
	if err != nil {
		return result.fail(err)
	}
	return result.ownedWait(changed, controls, attention).run(life, cancel, nil, log)
}

func writeResumePrevious(w io.Writer, mode cli.OutputMode, previous processResumePrevious) error {
	if mode == cli.OutputModeNone {
		return nil
	}
	if previous.MayBeTruncated {
		if _, err := fmt.Fprintln(w, "previous generation: reconcile/unknown; last turn may have been truncated"); err != nil {
			return err
		}
	}
	if previous.InterruptedTurn != "" || len(previous.Expired) > 0 {
		_, err := fmt.Fprintf(w, "previous generation: interruptedTurn=%s expiredControls=%d\n", previous.InterruptedTurn, len(previous.Expired))
		return err
	}
	return nil
}

func awaitDetachedRequestCleanup(command *createCommand, req processDetachedRequest, owner coremetadata.ProcessIdentity, ready processDetachedReply) error {
	if req.Relaunch != nil {
		return awaitDetachedRelaunchCleanup(command, req, owner, ready)
	}
	if req.Resume == nil {
		return awaitDetachedStartupCleanup(command, owner, ready.Binding)
	}
	return mutationruntime.AwaitDetachedProcessResumeCleanup(command.store.snapshot, req.RegistryPath, mutationruntime.ProcessDetachedResumeRequest(*req.Resume), owner, ready.Binding)
}

func processResumeErrorCode(err error) string {
	if errors.Is(err, mutationruntime.ErrResumePreparationStale) {
		return mutationruntime.ResumePreparationStale
	}
	var failure *mutations.Failure
	if errors.As(err, &failure) {
		return string(failure.Code)
	}
	var refusal *mutationruntime.AgentCreateRefusal
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	var profile *mutationruntime.ProfileResumeError
	var localProfile *profileResumeError
	if errors.As(err, &profile) || errors.As(err, &localProfile) {
		return "profile-resume-unavailable"
	}
	if coremetadata.IsUsageError(err) {
		return "resume-request-invalid"
	}
	if errors.Is(err, processhost.ErrStale) {
		return "process-resume-refused"
	}
	if errors.Is(err, processhost.ErrResumeRefused) {
		return "process-resume-refused"
	}
	return "resume-execution-failed"
}
