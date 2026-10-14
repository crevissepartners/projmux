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

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Carry public argv and the exact read-only source proof, never planner pointers.
type processDetachedRelaunchRequest struct {
	Args          []string
	Source        coremetadata.Agent
	Pane          *coremetadata.Pane
	AllowHostLost bool
}

type detachedRelaunchSessionKey struct{}
type processDetachedRelaunchSession struct {
	conn         net.Conn
	encoder      *json.Encoder
	decoder      *json.Decoder
	identity     coremetadata.ProcessIdentity
	registryPath string
	timer        *time.Timer
}

func detachedRelaunchSession(ctx context.Context) *processDetachedRelaunchSession {
	if ctx == nil {
		return nil
	}
	session, _ := ctx.Value(detachedRelaunchSessionKey{}).(*processDetachedRelaunchSession)
	return session
}

func (c *agentCommand) runDetachedProcessRelaunch(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, stdout, stderr io.Writer) error {
	// The public planner rejects unsupported destinations and validates the source
	// before a launcher exists. The owner repeats admission with the original caller.
	preview := request
	preview.dryRun = true
	var planned agentHostTransferResult
	planner := *c
	planner.hostTransferResult = &planned
	if err := planner.dispatchRelaunch(reg, target, preview, io.Discard, io.Discard); err != nil {
		return err
	}
	if planned.Result.PromptRequired {
		return usageError(fmt.Sprintf("agent relaunch: lost-host Claude requires an explicit first prompt; nothing was changed; recover with: %s", planned.Result.RelaunchCommand))
	}
	if planned.Result.ConfirmationRequired && !request.yes {
		return usageError(fmt.Sprintf("agent relaunch: agent/%s is %s with interaction %s; restarting would cut that turn; re-run with --yes (%s); nothing was changed", target.Metadata.Name, target.Status.Phase, planned.Result.Interaction, relaunchReasonAgentBusy))
	}
	if c.rebind == nil || c.rebind.create == nil {
		return errors.New("agent relaunch: process launcher unavailable")
	}
	args := []string{"uid:" + target.Metadata.UID, "--detach"}
	// Reparse the public request in the owner; no payload travels through argv.
	args = append(args, detachedRelaunchArgs(request)...)
	wire := processDetachedRelaunchRequest{Args: args, Source: target.Clone(), AllowHostLost: request.allowHostLost}
	if pane, found := reg.Pane(target.Status.PaneRef); found {
		clone := pane.Clone()
		wire.Pane = &clone
	}
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
	err = launchProcessDetached(ctx, executable, processDetachedRequest{Relaunch: &wire, Caller: caller, RegistryPath: c.messagePaths.registryPath}, c.rebind.create, stdout, stderr)
	return c.detachedRelaunchFailure(reg, target, request, planned.processRecovery, err)
}

func detachedRelaunchArgs(r agentRelaunchRequest) []string {
	var args []string
	add := func(flag, value string) { args = append(args, flag, value) }
	if r.host != "" {
		add("--host", r.host)
	}
	if r.model != "" {
		add("--model", r.model)
	}
	if r.effort != "" {
		add("--effort", r.effort)
	}
	if r.profile != nil {
		value := *r.profile
		if value == "" {
			value = relaunchNone
		}
		add("--profile", value)
	}
	if r.instructions != nil {
		value := *r.instructions
		if value == "" {
			value = relaunchNone
		}
		add("--instructions", value)
	}
	if len(r.reset) > 0 {
		var value strings.Builder
		for i, item := range r.reset {
			if i > 0 {
				value.WriteString(",")
			}
			value.WriteString(item)
		}
		add("--reset", value.String())
	}
	if r.yes {
		args = append(args, "--yes")
	}
	if r.json {
		add("-o", "json")
	}
	if r.socket.socket != "" {
		add("--socket", r.socket.socket)
	}
	if r.socket.socketPath != "" {
		add("--socket-path", r.socket.socketPath)
	}
	if len(r.prompt) > 0 {
		args = append(args, "--")
		args = append(args, r.prompt...)
	}
	return args
}

func runProcessDetachedRelaunchOwner(conn net.Conn, encoder *json.Encoder, decoder *json.Decoder, req processDetachedRequest, identity coremetadata.ProcessIdentity, creator *createCommand, command *agentCommand) error {
	wire := req.Relaunch
	request, err := parseAgentRelaunchArgs(wire.Args, io.Discard)
	if err != nil {
		return err
	}
	if !request.detach || request.dryRun || request.agentRef != "uid:"+wire.Source.Metadata.UID {
		return errors.New("process-detached-relaunch-request-invalid")
	}
	request.allowHostLost = wire.AllowHostLost
	reg, err := command.loadRegistry()
	if err != nil {
		return err
	}
	target, found := reg.Agent(wire.Source.Metadata.UID)
	if !found || !detachedRelaunchSourceEqual(target, &wire.Source) {
		return encodeDetachedResumeError(encoder, identity, processhost.Binding{}, processhost.ErrStale)
	}
	if wire.Pane != nil {
		pane, present := reg.Pane(wire.Pane.Metadata.UID)
		if !present || !reflect.DeepEqual(pane.Status.Activation, wire.Pane.Status.Activation) || !processResumeRecordEqual(pane.Status.ProcessSession, wire.Pane.Status.ProcessSession) {
			return encodeDetachedResumeError(encoder, identity, processhost.Binding{}, processhost.ErrStale)
		}
	}
	life, cancel := processForegroundLifetime()
	defer cancel()
	life = (processOwnerLifetime{}).withContext(life)
	session := &processDetachedRelaunchSession{conn: conn, encoder: encoder, decoder: decoder, identity: identity, registryPath: req.RegistryPath}
	session.timer = time.AfterFunc(processDetachedStartupTimeout, cancel)
	defer session.timer.Stop()
	life = context.WithValue(life, detachedRelaunchSessionKey{}, session)
	call := *command
	// The source cannot evade self-target admission by the detached session hop.
	call.processAncestors = creator.processAncestors
	call.hostTransferContext = life
	call.hostTransferResult = &agentHostTransferResult{}
	err = call.dispatchRelaunch(reg, target.Clone(), request, io.Discard, io.Discard)
	if err != nil {
		_ = encoder.Encode(processDetachedReply{Owner: identity, Error: err.Error()})
	}
	return err
}

func detachedRelaunchSourceEqual(a, b *coremetadata.Agent) bool {
	return reflect.DeepEqual(a.Spec, b.Spec) && reflect.DeepEqual(a.Metadata, b.Metadata) && a.Status.Phase == b.Status.Phase && a.Status.PaneRef == b.Status.PaneRef && a.Status.SessionRef.SameConversation(b.Status.SessionRef)
}

func (s *processDetachedRelaunchSession) finishOwned(ctx context.Context, cancel context.CancelFunc, owned *processAgentResumeResult, sync processRelaunchSynchronization, result agentRelaunchResult, request agentRelaunchRequest, fail func(error) error, beforeOwned ...func() error) error {
	rollback := func(cause error) error {
		cancel()
		return encodeDetachedResumeError(s.encoder, s.identity, owned.Binding, fail(cause))
	}
	if err := s.encoder.Encode(processDetachedReply{Owner: s.identity, Binding: owned.Binding, Phase: "starting"}); err != nil {
		return rollback(err)
	}
	var output bytes.Buffer
	if err := writeAgentRelaunchResult(&output, request, result); err != nil {
		return rollback(err)
	}
	reply := processDetachedReply{Owner: s.identity, Binding: owned.Binding, Stdout: output.String(), Stderr: fmt.Sprintf("agent uid:%s pane uid:%s runtime=process foreground=detached\n", owned.Binding.Agent, owned.Binding.Pane)}
	if owned.Previous.MayBeTruncated {
		reply.Stderr += processRelaunchHostLostNotice + "\n"
	}
	if err := s.encoder.Encode(reply); err != nil {
		return rollback(err)
	}
	if err := detachedResumeCommit(ctx, s.decoder, owned); err != nil {
		return rollback(err)
	}
	log, err := newProcessDetachedLog(s.registryPath, owned.Binding)
	if err != nil {
		return rollback(err)
	}
	defer log.file.Close()
	for _, finish := range beforeOwned {
		if err := finish(); err != nil {
			return rollback(err)
		}
	}
	if err := s.encoder.Encode("owned"); err != nil {
		return rollback(err)
	}
	s.timer.Stop()
	_ = s.conn.Close()
	return processRelaunchOwnedWait(owned, sync).run(ctx, cancel, nil, log)
}

func (s *processDetachedRelaunchSession) finishClaim(ctx context.Context, cancel context.CancelFunc, c *agentCommand, claim *deferredProcessClaim, record, previous *deferredLaunchRecord, result agentRelaunchResult, request agentRelaunchRequest) error {
	binding := processSchemaBinding(claim.record.Binding)
	fail := func(cause error) error {
		cancel()
		cause = errors.Join(cause, claim.Close())
		cause = errors.Join(cause, c.rollbackDetachedRelaunchClaim(record, previous))
		return encodeDetachedResumeError(s.encoder, s.identity, binding, cause)
	}
	result.Phase = coremetadata.PhaseOffline
	result.NewPaneUID = binding.Pane
	result.Outcome = personaOutcomeResumed
	if result.Restart {
		result.Outcome = personaOutcomeRestarted
	}
	var output bytes.Buffer
	if err := writeAgentRelaunchResult(&output, request, result); err != nil {
		return fail(err)
	}
	reply := processDetachedReply{Owner: s.identity, Binding: binding, Claim: &claim.record, Stdout: output.String(), Stderr: fmt.Sprintf("agent uid:%s pane uid:%s runtime=process foreground=detached claim=waiting\n", binding.Agent, binding.Pane)}
	if err := s.encoder.Encode(reply); err != nil {
		return fail(err)
	}
	var commit string
	if err := s.decoder.Decode(&commit); err != nil {
		return fail(err)
	}
	if commit != "commit" || ctx.Err() != nil {
		return fail(errors.New("process-detached-commit-invalid"))
	}
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
	log, err := newProcessDetachedLog(s.registryPath, logBinding)
	if err != nil {
		return fail(err)
	}
	defer log.file.Close()
	if err := s.encoder.Encode("owned"); err != nil {
		return fail(err)
	}
	s.timer.Stop()
	_ = s.conn.Close()
	owned, err := claim.WaitPeer(ctx)
	if err != nil {
		if owned.Handle == nil && ctx.Err() != nil {
			return nil
		}
		return err
	}
	changed, controls, attention, err := owned.resumeSynchronization(c.rebind.create)
	if err != nil {
		return owned.fail(err)
	}
	return owned.ownedWait(changed, controls, attention).run(ctx, cancel, nil, log)
}

// Only an exact child-free preparation can be restored. Another claimant,
// changed recipe, or provider birth fences rollback instead of stopping peers.
func (c *agentCommand) rollbackDetachedRelaunchClaim(record, previous *deferredLaunchRecord) error {
	unlock, err := lockDeferredClaim(c.deferredClaimPath(record.Agent))
	if err != nil {
		return err
	}
	defer unlock()
	if err = c.checkDeferredClaim(record.Agent, nil); err != nil {
		return err
	}
	current, err := c.readDeferredLaunch(record.Agent)
	if err != nil {
		return err
	}
	if deferredLaunchDigest(current) != deferredLaunchDigest(record) {
		return processhost.ErrStale
	}
	_, err = c.rebind.create.store.update(func(reg *coremetadata.Registry) error {
		agent, ok := reg.Agent(record.Agent)
		pane, ambiguous := processResumePane(*reg, record.Agent)
		if !ok || ambiguous || pane == nil || agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || !processResumeRecordEqual(pane.Status.ProcessSession, &record.Retired) || !(sameHostTransferSpec(agent.Spec, record.NewSpec) && reflect.DeepEqual(agent.Metadata.Annotations, record.NewAnnotations) || sameHostTransferSpec(agent.Spec, record.OldSpec) && reflect.DeepEqual(agent.Metadata.Annotations, record.OldAnnotations)) {
			return processhost.ErrStale
		}
		agent.Spec = record.OldSpec
		agent.Metadata.Annotations = record.OldAnnotations
		return nil
	})
	if err != nil {
		return err
	}
	path := c.deferredStatePath("deferred-launches", record.Agent)
	if previous != nil {
		return writeDeferredState(path, previous)
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func awaitDetachedRelaunchCleanup(command *createCommand, req processDetachedRequest, owner coremetadata.ProcessIdentity, ready processDetachedReply) error {
	ctx, cancel := context.WithTimeout(context.Background(), processRelaunchRetirementTimeout)
	defer cancel()
	ticker := time.NewTicker(processRelaunchRetirementPoll)
	defer ticker.Stop()
	source := req.Relaunch.Source
	for {
		reg, err := command.store.snapshot()
		settled := false
		if err == nil {
			agent, exists := reg.Agent(source.Metadata.UID)
			if exists {
				// Admission failures may leave the old owner untouched. Never stop it.
				settled = detachedRelaunchSourceEqual(agent, &source)
				if settled && req.Relaunch.Pane != nil {
					previous := req.Relaunch.Pane
					current, present := reg.Pane(previous.Metadata.UID)
					settled = present && reflect.DeepEqual(current.Status.Activation, previous.Status.Activation) && processResumeRecordEqual(current.Status.ProcessSession, previous.Status.ProcessSession)
				}
				pane, ambiguous := processResumePane(reg, source.Metadata.UID)
				if pane != nil && !ambiguous && pane.Status.ProcessSession != nil && agent.Status.Phase == coremetadata.PhaseOffline && pane.Status.Activation.IsZero() {
					binding := pane.Status.ProcessSession.Binding
					settled = settled || (sameHostTransferSpec(agent.Spec, source.Spec) && reflect.DeepEqual(agent.Metadata.Annotations, source.Metadata.Annotations) && coremetadata.MatchesProcessWait(binding, pane.Status.LastTermination) && coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination))
				}
				if agent.Status.Phase == coremetadata.PhaseOffline && agent.Status.PaneRef == "" && relaunchCurrentHost(reg, *agent) == "tmux" && sameHostTransferSpec(agent.Spec, source.Spec) && reflect.DeepEqual(agent.Metadata.Annotations, source.Metadata.Annotations) {
					settled = true
				}
			}
		}
		c := &agentCommand{messagePaths: agentMessagePaths{registryPath: req.RegistryPath}}
		claim, claimErr := readDeferredClaim(c.deferredClaimPath(source.Metadata.UID))
		if settled && claimErr == nil && (claim.Agent == "" || claim.Process != owner) && processIdentityAbsent(owner, localipc.Process) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("process-detached-cleanup-unconfirmed: agent uid:%s; inspect with: projmux describe agent uid:%s; cleanup: projmux agent stop uid:%s: %w", source.Metadata.UID, source.Metadata.UID, source.Metadata.UID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// The preview carries the exact recipe's command, never permission to recover.
// Recheck current ownership after failure using the existing recovery barrier.
func (c *agentCommand) detachedRelaunchFailure(reg coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, recovery string, cause error) error {
	if cause == nil || !request.allowHostLost || recovery == "" || strings.Contains(cause.Error(), "recover with:") {
		return cause
	}
	call := *c
	call.processResumeHostLost = true
	return fmt.Errorf("agent relaunch: recorded conversation retained; %s: %w", call.processRelaunchFailureRecovery(reg, target, request, recovery), cause)
}
