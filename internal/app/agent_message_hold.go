package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// claudeHoldReasonAwaitingOperator is the one reason a coordination message is
// held: its Claude target is showing its operator a question, a permission
// dialog, or an MCP elicitation, and a push frame would render as pending input
// over that widget.
const claudeHoldReasonAwaitingOperator = "target-awaiting-operator"

// agentMessageReleaseRoute is the hidden route of the detached held-message
// release: `projmux internal agent-message-release --agent uid:<agent>`.
const agentMessageReleaseRoute = "agent-message-release"

// agentMessageReleaseLockWait bounds how long one release queues behind
// another for the same target. The holder re-lists held records before it
// unlocks, so a waiter that gives up cannot strand a record held before that.
const agentMessageReleaseLockWait = 30 * time.Second

// agentMessageReleaseRetryWindow bounds how long one release keeps its lock
// re-judging a record it cannot judge yet or watching a target that awaits its
// operator, so a helper that stays busy or a dialog that stays open cannot hold
// the release forever.
const agentMessageReleaseRetryWindow = 10 * time.Minute

// agentMessageReleaseRetryFirstWait is the first wait before judging again: a
// helper too busy for one 200ms probe is usually free again within seconds.
const agentMessageReleaseRetryFirstWait = 2 * time.Second

// agentMessageReleaseRetryMaxWait caps the doubling wait, so a target that frees
// up late in the window is still reached within half a minute.
const agentMessageReleaseRetryMaxWait = 30 * time.Second

// agentMessageReleaseWatchInterval is the fixed wait between two reads of a
// target that awaits its operator. A denied permission sends no hook, so the
// release reads the Registry and the target's transcript again this often.
const agentMessageReleaseWatchInterval = 2 * time.Second

// agentInteractionAwaitsOperator is the one closed set of interaction kinds
// that block a coordination push: the Agent waits on its operator's answer.
func agentInteractionAwaitsOperator(kind coremetadata.AgentInteractionKind) bool {
	return kind == coremetadata.InteractionApprovalRequired || kind == coremetadata.InteractionInputRequired
}

// claudeAgentAwaitsOperator is the blocking predicate the send and the release
// both ask. It reads the effective interaction, so an observation older than
// the freshness window is unknown and does not block.
func claudeAgentAwaitsOperator(agent coremetadata.Agent, now time.Time) bool {
	return agentInteractionAwaitsOperator(agent.EffectiveInteraction(now).Kind)
}

// agentMessageHeldAction is the action a held receipt names.
func agentMessageHeldAction(messageRef string) string {
	return "delivery resumes automatically when the target Agent's dialog closes; check projmux agent message status " +
		messageRef + "; do not resend"
}

type agentMessageHeldLister interface {
	HeldFor(string) ([]messagestore.Record, error)
}

type agentMessageReleaseLocker interface {
	LockTargetRelease(string, time.Duration) (func(), error)
}

// heldMessageRelease launches the detached release for an Agent that has at
// least one held message. The zero value launches nothing.
type heldMessageRelease struct {
	store  func() (agentMessageHeldLister, error)
	launch func(string) error
}

// releaseIfHeld never fails its caller: a hook or a send must not fail because
// the store could not be read or the release could not be spawned.
func (r heldMessageRelease) releaseIfHeld(agentUID string) {
	if r.store == nil || r.launch == nil || strings.TrimSpace(agentUID) == "" {
		return
	}
	store, err := r.store()
	if err != nil || store == nil {
		return
	}
	if held, err := store.HeldFor(agentUID); err == nil && len(held) > 0 {
		_ = r.launch(agentUID)
	}
}

func defaultHeldMessageRelease() heldMessageRelease {
	return heldMessageRelease{
		store: func() (agentMessageHeldLister, error) {
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				return nil, err
			}
			return messagestore.NewStore(paths.StateDir), nil
		},
		launch: launchAgentMessageRelease,
	}
}

// launchAgentMessageRelease starts the release detached in its own session. It
// inherits no provider messaging credential and no standard stream, and it is
// never waited for.
func launchAgentMessageRelease(agentUID string) error {
	if !coremessage.ValidRef(agentUID) {
		return coremessage.EnvelopeRefusal(coremessage.ReasonRouteInvalid, "release target Agent uid is not a valid ref")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	// #nosec G204 -- os.Executable above identifies this running Projmux binary;
	// the hidden route is fixed and the Agent uid is a validated ref.
	cmd := exec.Command(binary, "internal", agentMessageReleaseRoute, "--agent", selector.UIDPrefix+agentUID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = claudeHelperEnvironment(os.Environ())
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// runAgentMessageRelease is the detached release process.
func runAgentMessageRelease(args []string) error {
	if len(args) != 2 || args[0] != "--agent" || !strings.HasPrefix(args[1], selector.UIDPrefix) {
		return usageError("internal " + agentMessageReleaseRoute + " requires --agent uid:<agent>")
	}
	return newAgentCommand().releaseHeldMessages(strings.TrimPrefix(args[1], selector.UIDPrefix))
}

// heldRelease is the send side's view of the held-message release.
func (c *agentCommand) heldRelease() heldMessageRelease {
	if c == nil {
		return heldMessageRelease{}
	}
	lister, ok := c.messageStore.(agentMessageHeldLister)
	if !ok {
		return heldMessageRelease{}
	}
	return heldMessageRelease{store: func() (agentMessageHeldLister, error) { return lister, nil }, launch: c.messageRelease}
}

// deliverOrHoldCoordination is the send's delivery step. A Claude target that
// awaits its operator, or that already has held messages this one must not
// overtake, keeps the message held instead of calling its helper.
func (c *agentCommand) deliverOrHoldCoordination(record messagestore.Record, target coremetadata.Agent,
	targetRoute coremetadata.AgentRouteRef, envelope coremessage.Envelope,
) (messagestore.Record, error) {
	if target.Spec.Provider != string(aiprovider.Claude) {
		return c.pushCoordination(record, target, targetRoute, envelope)
	}
	blocked := claudeAgentAwaitsOperator(target, c.messageClock())
	if !blocked && !c.earlierHeldFor(target.Metadata.UID, record.Envelope.MessageRef) {
		return c.pushCoordination(record, target, targetRoute, envelope)
	}
	held, _, err := c.messageStore.Apply(record.Envelope.MessageRef,
		c.publicMessageEvent(record, coremessage.EventHold, claudeHoldReasonAwaitingOperator, false))
	if err != nil {
		return record, fmt.Errorf("hold message while the target awaits its operator: %w", err)
	}
	if held.Delivery.State != coremessage.StateHeld {
		return held, nil
	}
	// Every hold launches the release. A hold caused only by earlier held
	// messages has no dialog to wait for. A blocked target may never send the
	// hook that would release it: a dialog that closed after the send's read,
	// or a denied permission, leaves none, so the release watches the target.
	c.heldRelease().releaseIfHeld(target.Metadata.UID)
	return held, nil
}

func (c *agentCommand) earlierHeldFor(agentUID, messageRef string) bool {
	lister, ok := c.messageStore.(agentMessageHeldLister)
	if !ok {
		return false
	}
	held, err := lister.HeldFor(agentUID)
	if err != nil {
		return false
	}
	for _, record := range held {
		if record.Envelope.MessageRef != messageRef {
			return true
		}
	}
	return false
}

// heldReleaseOutcome is how judging one held record ended.
type heldReleaseOutcome int

const (
	// heldReleaseDone means the record was finished or pushed, and the release
	// goes on to the next record.
	heldReleaseDone heldReleaseOutcome = iota
	// heldReleaseStop means the release window ended, so this record and the
	// rest stay held.
	heldReleaseStop
	// heldReleaseUndecidable means the target's helper did not answer its probe
	// while the target was not blocked, or the Registry could not be read. The
	// same record is judged again after a wait.
	heldReleaseUndecidable
	// heldReleaseBlocked means the target awaits its operator. The release
	// watches it and judges the same record again after a fixed wait.
	heldReleaseBlocked
)

// releaseHeldMessages delivers the messages held for one target Agent, oldest
// first and one at a time, under the per-target release lock. A record it
// cannot push yet, because the target awaits its operator, its helper is too
// busy to answer its probe, or the Registry cannot be read, is judged again in
// place after a wait, inside one window per release; when that window ends the
// release ends with the record still held, and no later record overtakes it.
// After a pass it lists again under the same lock, so a message held while
// this release was running is not left for a release that already gave up.
func (c *agentCommand) releaseHeldMessages(agentUID string) error {
	lister, listOK := c.messageStore.(agentMessageHeldLister)
	locker, lockOK := c.messageStore.(agentMessageReleaseLocker)
	if !listOK || !lockOK {
		return errors.New("held message release is unavailable")
	}
	unlock, err := locker.LockTargetRelease(agentUID, agentMessageReleaseLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	attempted := map[string]bool{}
	var windowEnd time.Time
	for {
		held, err := lister.HeldFor(agentUID)
		if err != nil {
			return err
		}
		progressed := false
		for i, record := range held {
			if attempted[record.Envelope.MessageRef] {
				continue
			}
			attempted[record.Envelope.MessageRef] = true
			progressed = true
			outcome, err := c.judgeHeldMessage(record, held[i:], &windowEnd)
			if err != nil || outcome != heldReleaseDone {
				return err
			}
		}
		if !progressed {
			return nil
		}
	}
}

// judgeHeldMessage judges one held record and, while it cannot be pushed yet,
// waits and judges the same record again. An undecidable judgment waits with a
// doubling backoff. A target that awaits its operator is watched at a fixed
// interval: each time, its transcript is asked whether the blocked turn has
// already ended, which is the only trace a denied permission leaves; when it
// has, the turn end is recorded and the record is judged again at once. The
// release's one window opens at its first wait and ends 10 minutes later or at
// the earliest deadline still ahead among the records still held, whichever is
// first. pending is the current listing from this record on. A record whose
// own deadline has passed is expired instead of waited for. A window that ends
// on a Registry read failure returns that failure.
func (c *agentCommand) judgeHeldMessage(record messagestore.Record, pending []messagestore.Record,
	windowEnd *time.Time,
) (heldReleaseOutcome, error) {
	backoff := agentMessageReleaseRetryFirstWait
	turnEndRecorded := false
	for {
		outcome, target, err := c.releaseHeldMessage(record)
		var pause time.Duration
		switch outcome {
		case heldReleaseBlocked:
			// A turn end just recorded is judged once more without a wait; a
			// target still blocked after that is watched like any other.
			if !turnEndRecorded && c.endBlockedClaudeTurn(target) {
				turnEndRecorded = true
				continue
			}
			pause = agentMessageReleaseWatchInterval
		case heldReleaseUndecidable:
			pause = backoff
			backoff = min(2*backoff, agentMessageReleaseRetryMaxWait)
		default:
			return outcome, err
		}
		turnEndRecorded = false
		now := c.messageClock()
		if !now.Before(record.Envelope.Deadline) {
			_, _, err := c.messageStore.Status(record.Envelope.MessageRef, now)
			return heldReleaseDone, err
		}
		if windowEnd.IsZero() {
			*windowEnd = heldReleaseWindowEnd(now, pending)
		}
		pause = min(pause, windowEnd.Sub(now))
		if pause <= 0 {
			return heldReleaseStop, err
		}
		_ = c.sleepMessage(context.Background(), pause)
		now = c.messageClock()
		if !now.Before(record.Envelope.Deadline) {
			_, _, err := c.messageStore.Status(record.Envelope.MessageRef, now)
			return heldReleaseDone, err
		}
		if !now.Before(*windowEnd) {
			return heldReleaseStop, err
		}
	}
}

// heldReleaseWindowEnd bounds the window opened at now: a release never waits
// past the earliest deadline still ahead among the records still held. A
// deadline already passed does not close the window; that record is expired
// when it is judged.
func heldReleaseWindowEnd(now time.Time, pending []messagestore.Record) time.Time {
	end := now.Add(agentMessageReleaseRetryWindow)
	for _, record := range pending {
		if record.Envelope.Deadline.After(now) && record.Envelope.Deadline.Before(end) {
			end = record.Envelope.Deadline
		}
	}
	return end
}

// releaseHeldMessage judges one held record against a fresh Registry read and
// either finishes it or pushes it through the ordinary Claude path. blocked
// means the target awaits its operator and comes back with the Agent as read;
// undecidable means the record could not be judged now. Either way the record
// stays held for the caller to judge again.
func (c *agentCommand) releaseHeldMessage(record messagestore.Record) (heldReleaseOutcome, coremetadata.Agent, error) {
	ref := record.Envelope.MessageRef
	registry, err := c.readMessageRegistry()
	if err != nil {
		return heldReleaseUndecidable, coremetadata.Agent{}, err
	}
	current, ok := registry.Agent(record.Envelope.Target.AgentUID)
	if !ok {
		_, _, err := c.messageStore.Apply(ref, c.staleMessageEvent(record, "target-removed"))
		return heldReleaseDone, coremetadata.Agent{}, err
	}
	target := current.Clone()
	route, routeErr := c.resolveMessageTargetRoute(registry, target)
	var unanswered claudeProbeUnansweredError
	if errors.As(routeErr, &unanswered) {
		// A live target too busy to answer its probe is not stale. A target
		// that awaits its operator is watched; otherwise the record is judged
		// again after a wait.
		if claudeAgentAwaitsOperator(target, c.messageClock()) {
			return heldReleaseBlocked, target, nil
		}
		return heldReleaseUndecidable, coremetadata.Agent{}, nil
	}
	if routeErr != nil || !messageRouteAccepts(route, record.Envelope.Target) {
		_, _, err := c.messageStore.Apply(ref, c.staleMessageEvent(record, "target-activation-stale"))
		return heldReleaseDone, coremetadata.Agent{}, err
	}
	if record.HandoffObserved {
		// A helper may already have written this frame. A push now could write
		// it twice, and automatic resend is never allowed.
		_, _, err := c.messageStore.Apply(ref,
			c.publicMessageEvent(record, coremessage.EventFail, "provider-handoff-outcome-unknown", true))
		return heldReleaseDone, coremetadata.Agent{}, err
	}
	now := c.messageClock()
	if !now.Before(record.Envelope.Deadline) {
		_, _, err := c.messageStore.Status(ref, now)
		return heldReleaseDone, coremetadata.Agent{}, err
	}
	if claudeAgentAwaitsOperator(target, now) {
		return heldReleaseBlocked, target, nil
	}
	_, err = c.pushCoordination(record, target, route, record.Envelope)
	return heldReleaseDone, coremetadata.Agent{}, err
}
