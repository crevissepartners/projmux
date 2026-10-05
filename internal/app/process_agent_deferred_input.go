package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const deferredUserDeadline = 30 * time.Second

// A user input is not a coordination envelope. This single private slot is
// fenced by operation, retired conversation, claimant birth and recipe digest.
// Terminal records retain only content-free evidence for the submitting CLI.
type deferredUserInput struct {
	Version                                                      int
	Operation, Nonce, Session, Recipe, Phase, Text, Turn, Reason string
	Binding                                                      coremetadata.ProcessBinding
	Process                                                      coremetadata.ProcessIdentity
	Deadline                                                     time.Time
	Success, Unknown                                             bool
}

func (c *agentCommand) readDeferredInput(uid string) (*deferredUserInput, error) {
	var input deferredUserInput
	found, err := readDeferredState(c.deferredStatePath("deferred-inputs", uid), &input)
	if err != nil || !found {
		return nil, err
	}
	if input.Version != 1 || input.Operation == "" || input.Nonce == "" || input.Binding.AgentUID != uid || input.Session == "" || !input.Process.Valid() || input.Deadline.IsZero() || (input.Phase != "pending" && input.Phase != "inflight" && input.Phase != "settled") {
		return nil, deferredRefused("damaged deferred user input")
	}
	return &input, nil
}

func (c *agentCommand) writeDeferredInput(input *deferredUserInput) error {
	return writeDeferredState(c.deferredStatePath("deferred-inputs", input.Binding.AgentUID), input)
}

func (input *deferredUserInput) settle(success, unknown bool, turn, reason string) {
	input.Phase, input.Text, input.Success, input.Unknown, input.Turn, input.Reason = "settled", "", success, unknown, turn, reason
}

func (input *deferredUserInput) matches(claim deferredClaimRecord, recipe string) bool {
	return input.Nonce == claim.Nonce && input.Process == claim.Process && input.Binding == claim.Binding && input.Session == claim.Session && input.Recipe == recipe
}

// Guard is held by acquire/Close. A dead claimant's unsubmitted slot can move
// only within the same exact retired conversation and prepared configuration.
func (c *agentCommand) reclaimDeferredInput(claim deferredClaimRecord) error {
	input, err := c.readDeferredInput(claim.Agent)
	if err != nil || input == nil || input.Phase == "settled" {
		return err
	}
	launch, err := c.readDeferredLaunch(claim.Agent)
	if err != nil {
		return err
	}
	if input.Phase == "inflight" {
		input.settle(false, true, "", "provider-handoff-outcome-unknown")
	} else if !input.Deadline.After(c.messageClock()) {
		input.settle(false, false, "", "deadline-expired")
	} else if input.Binding != claim.Binding || input.Session != claim.Session || input.Recipe != deferredLaunchDigest(launch) {
		input.settle(false, false, "", "stale-binding")
	} else {
		input.Nonce, input.Process = claim.Nonce, claim.Process
	}
	return c.writeDeferredInput(input)
}

func (c *agentCommand) closeDeferredInput(claim deferredClaimRecord) error {
	input, err := c.readDeferredInput(claim.Agent)
	if err != nil || input == nil || input.Phase != "pending" || input.Nonce != claim.Nonce {
		return err
	}
	input.settle(false, false, "", "owner-ended")
	return c.writeDeferredInput(input)
}

// Reservation calls this under the ordinary claim guard. Go Resume keeps its
// user-first semantics; it defeats an unsubmitted CLI slot without submitting
// that slot too. An active mailbox handoff can only be consumed by its owner.
func (c *agentCommand) admitDeferredInput(claim *deferredProcessClaim) error {
	if claim == nil || claim.record.Provider != aiModeClaude {
		return nil
	}
	input, err := c.readDeferredInput(claim.record.Agent)
	if err != nil || input == nil || input.Phase == "settled" {
		return err
	}
	if claim.inputOperation != "" {
		if input.Operation != claim.inputOperation || input.Phase != "inflight" || input.Nonce != claim.record.Nonce || input.Process != claim.record.Process || input.Binding != claim.record.Binding || input.Session != claim.record.Session || !input.Deadline.After(c.messageClock()) {
			return deferredRefused("first user input authority expired or changed")
		}
		return nil
	}
	if input.Phase == "inflight" {
		return processhost.ErrBusy
	}
	input.settle(false, false, "", "busy")
	return c.writeDeferredInput(input)
}

func (c *agentCommand) settleDeferredInput(claim *deferredProcessClaim, success, unknown bool, turn, reason string) error {
	if claim.inputOperation == "" {
		return nil
	}
	unlock, err := lockDeferredClaim(claim.path)
	if err != nil {
		return err
	}
	defer unlock()
	input, err := c.readDeferredInput(claim.record.Agent)
	if err != nil {
		return err
	}
	if input == nil || input.Operation != claim.inputOperation || input.Nonce != claim.record.Nonce || input.Process != claim.record.Process {
		return deferredRefused("first user input witness changed")
	}
	if input.Phase == "settled" {
		return nil
	}
	input.settle(success, unknown, turn, reason)
	return c.writeDeferredInput(input)
}

// No new socket or daemon is involved. WaitPeer also checks this bounded slot;
// only this method enters its inflight state while holding the claim mutex.
func (claim *deferredProcessClaim) resumeDeferredInput(ctx context.Context) (processAgentResumeResult, bool, error) {
	if claim.record.Provider != aiModeClaude {
		return processAgentResumeResult{}, false, nil
	}
	if !claim.mu.TryLock() {
		return processAgentResumeResult{}, true, deferredClaimOwned()
	}
	defer claim.mu.Unlock()
	input, err := claim.takeDeferredInput(ctx)
	if err != nil || input == nil {
		return processAgentResumeResult{}, false, err
	}
	claim.inputOperation = input.Operation
	defer func() { claim.inputOperation = "" }()
	bounded, cancel := context.WithDeadline(ctx, input.Deadline)
	defer cancel()
	result, err := claim.resume(bounded, processResumeFirstFrame{Kind: "user", Text: input.Text}, nil)
	if err != nil {
		err = errors.Join(err, claim.command.settleDeferredInput(claim, false, true, "", processResumeRefused))
	}
	return result, true, err
}

func (claim *deferredProcessClaim) takeDeferredInput(ctx context.Context) (*deferredUserInput, error) {
	c := claim.command
	unlock, err := lockDeferredClaim(claim.path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = c.checkDeferredClaim(claim.record.Agent, claim); err != nil {
		return nil, err
	}
	input, err := c.readDeferredInput(claim.record.Agent)
	if err != nil || input == nil || input.Phase == "settled" {
		return nil, err
	}
	launch, err := c.readDeferredLaunch(claim.record.Agent)
	if err != nil {
		return nil, err
	}
	if input.Phase != "pending" || !input.matches(claim.record, deferredLaunchDigest(launch)) {
		return nil, deferredRefused("deferred input proof changed")
	}
	if !input.Deadline.After(c.messageClock()) {
		input.settle(false, false, "", "deadline-expired")
		return nil, c.writeDeferredInput(input)
	}
	input.Phase = "inflight"
	if err = c.writeDeferredInput(input); err != nil {
		return nil, err
	}
	return input, nil
}

func (c *agentCommand) startDeferredUserTurn(reg coremetadata.Registry, agent coremetadata.Agent, text string, stdout io.Writer) (bool, error) {
	if agent.Spec.Provider != aiModeClaude || processResumeCandidateToken(reg, agent.Metadata.UID) != "" {
		return false, nil
	}
	unlock, err := lockDeferredClaim(c.deferredClaimPath(agent.Metadata.UID))
	if err != nil {
		return true, err
	}
	input, handled, err := c.acceptDeferredUserTurn(agent.Metadata.UID, text)
	unlock()
	if err != nil || !handled {
		return handled, err
	}
	signals, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithDeadline(signals, input.Deadline)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := c.readDeferredInput(agent.Metadata.UID)
		if err != nil {
			return true, err
		}
		if current == nil || current.Operation != input.Operation {
			return true, deferredRefused("deferred input acknowledgement changed; inspect Agent before retrying")
		}
		if current.Phase == "settled" {
			if !current.Success {
				return true, deferredRefused(fmt.Sprintf("first user input %s (outcomeUnknown=%t); inspect Agent before retrying", current.Reason, current.Unknown))
			}
			return true, c.writeProcessTurn(stdout, agentActionSendTurn, agent, current.Turn)
		}
		select {
		case <-ctx.Done():
			reason := "deadline-expired"
			if signals.Err() != nil {
				reason = "caller-cancelled"
			}
			return true, errors.Join(deferredRefused("first user input ended; inspect Agent before retrying"), c.cancelDeferredInput(input, reason))
		case <-ticker.C:
		}
	}
}

// Caller holds the claim guard; the OS uid is the existing local host-turn
// permission boundary. A source Agent anchor is never operator authentication.
func (c *agentCommand) acceptDeferredUserTurn(uid, text string) (*deferredUserInput, bool, error) {
	claim, err := readDeferredClaim(c.deferredClaimPath(uid))
	if err != nil {
		return nil, true, err
	}
	if !deferredClaimLive(claim) {
		return nil, false, nil
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return nil, true, err
	}
	if processResumeCandidateToken(registry, uid) != "" || claim.Provider != aiModeClaude || !deferredClaimMatches(claim, registry, uid) {
		return nil, true, deferredRefused("user input target changed")
	}
	caller, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return nil, true, err
	}
	if caller.OwnerUID != claim.Process.OwnerUID {
		return nil, true, deferredRefused("user input OS owner differs")
	}
	if len(text) > 32768 {
		return nil, true, deferredRefused("first user input exceeds its bound")
	}
	previous, err := c.readDeferredInput(uid)
	if err != nil {
		return nil, true, err
	}
	if previous != nil && previous.Phase != "settled" {
		if previous.Deadline.After(c.messageClock()) {
			return nil, true, processhost.ErrBusy
		}
		previous.settle(false, previous.Phase == "inflight", "", "deadline-expired")
		if err = c.writeDeferredInput(previous); err != nil {
			return nil, true, err
		}
	}
	store, ok := c.messageStore.(interface {
		DeferredFor(string, string) ([]messagestore.Record, error)
	})
	if !ok {
		return nil, true, deferredRefused("deferred message store unavailable")
	}
	held, err := store.DeferredFor(uid, deferredHoldReason)
	if err != nil {
		return nil, true, err
	}
	for _, record := range held {
		if record.Envelope.Deadline.After(c.messageClock()) && record.Envelope.Target == deferredMessageRoute(claim) {
			return nil, true, processhost.ErrBusy
		}
	}
	launch, err := c.readDeferredLaunch(uid)
	if err != nil {
		return nil, true, err
	}
	operation, err := newCreateOperationID()
	if err != nil {
		return nil, true, err
	}
	input := &deferredUserInput{Version: 1, Operation: operation, Nonce: claim.Nonce, Binding: claim.Binding, Session: claim.Session, Recipe: deferredLaunchDigest(launch), Process: claim.Process, Phase: "pending", Text: text, Deadline: c.messageClock().Add(deferredUserDeadline)}
	return input, true, c.writeDeferredInput(input)
}

func (c *agentCommand) cancelDeferredInput(want *deferredUserInput, reason string) error {
	unlock, err := lockDeferredClaim(c.deferredClaimPath(want.Binding.AgentUID))
	if err != nil {
		return err
	}
	defer unlock()
	input, err := c.readDeferredInput(want.Binding.AgentUID)
	if err != nil || input == nil || input.Operation != want.Operation || input.Phase == "settled" {
		return err
	}
	input.settle(false, input.Phase == "inflight", "", reason)
	return c.writeDeferredInput(input)
}
