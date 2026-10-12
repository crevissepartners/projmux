package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

type processResumeSynchronization struct {
	changed   func(processhost.Snapshot) error
	controls  func(context.Context) error
	attention func() error
}

func agentMessageHeldReceiptAction(receipt agentMessageReceipt) string {
	if receipt.Delivery.Reason == deferredHoldReason {
		return "delivery resumes when the claimant wakes this Agent; check projmux agent message status " + receipt.MessageRef + "; do not resend"
	}
	if receipt.Delivery.Reason == claudeHoldReasonTurnActive {
		return "delivery resumes automatically when the target Agent's active turn ends; check projmux agent message status " + receipt.MessageRef + "; do not resend"
	}
	return agentMessageHeldAction(receipt.MessageRef)
}

func (claim *deferredProcessClaim) held() ([]messagestore.Record, error) {
	store, ok := claim.command.messageStore.(interface {
		DeferredFor(string, string) ([]messagestore.Record, error)
	})
	if !ok {
		return nil, errors.New("process-host-unavailable: deferred message store unavailable")
	}
	return store.DeferredFor(claim.record.Agent, deferredHoldReason)
}

// WaitPeer checks once immediately, then sleeps on a ticker. It owns no
// detached worker, provider process or broker while waiting.
func (claim *deferredProcessClaim) WaitPeer(ctx context.Context) (processAgentResumeResult, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return processAgentResumeResult{}, err
		}
		if err := claim.command.checkDeferredClaim(claim.record.Agent, claim); err != nil {
			return processAgentResumeResult{}, err
		}
		if result, handled, err := claim.resumeDeferredInput(ctx); handled || err != nil {
			return result, err
		}
		held, err := claim.held()
		if err != nil {
			return processAgentResumeResult{}, err
		}
		for _, record := range held {
			if !record.Envelope.Deadline.After(claim.command.messageClock()) {
				if _, _, err = claim.command.messageStore.Status(record.Envelope.MessageRef, claim.command.messageClock()); err != nil {
					return processAgentResumeResult{}, err
				}
				continue
			}
			if record.Envelope.Target != deferredMessageRoute(claim.record) {
				if _, err = claim.command.terminalCoordination(record, coremessage.EventStale, "stale-binding", false, nil); err != nil {
					return processAgentResumeResult{}, err
				}
				continue
			}
			text, err := deferredPeerText(record)
			if err != nil {
				return processAgentResumeResult{}, err
			}
			return claim.Resume(ctx, processResumeFirstFrame{Kind: "peer", Text: text})
		}
		select {
		case <-ctx.Done():
			return processAgentResumeResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Resume is the in-process operator path. Its first frame stays a user frame;
// queued peers follow through their ordinary provider delivery adapter.
func (claim *deferredProcessClaim) Resume(ctx context.Context, frame processResumeFirstFrame) (processAgentResumeResult, error) {
	if !claim.mu.TryLock() {
		return processAgentResumeResult{}, deferredClaimOwned()
	}
	defer claim.mu.Unlock()
	if frame.Kind == "peer" {
		held, err := claim.held()
		if err != nil {
			return processAgentResumeResult{}, err
		}
		for _, record := range held {
			if record.Envelope.Target != deferredMessageRoute(claim.record) || !record.Envelope.Deadline.After(claim.command.messageClock()) {
				continue
			}
			text, err := deferredPeerText(record)
			if err != nil {
				return processAgentResumeResult{}, err
			}
			if text != frame.Text {
				break
			}
			return claim.resume(ctx, frame, &record)
		}
		return processAgentResumeResult{}, fmt.Errorf("%s: %w: peer frame is not the oldest claimed deferred envelope", processResumeRefused, processhost.ErrResumeRefused)
	}
	return claim.resume(ctx, frame, nil)
}

func (claim *deferredProcessClaim) markInflight(ref string) error {
	unlock, err := lockDeferredClaim(claim.path)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := readDeferredClaim(claim.path)
	if err != nil {
		return err
	}
	if ref == "" {
		// Stop prevents birth, but the same exact owner must still clear a
		// cancelled pre-birth selection or a settled delivery witness. This
		// cannot admit input, clear StopRequested, or touch a replacement claim.
		if !deferredClaimIdentityEqual(record, claim.record) || !deferredClaimLive(record) {
			return deferredRefused("deferred claim changed before inflight cleanup")
		}
	} else if err = claim.command.checkDeferredClaim(claim.record.Agent, claim); err != nil {
		return err
	}
	if ref != "" && record.Inflight != "" {
		return deferredClaimOwned()
	}
	record.Inflight = ref
	return writeDeferredClaim(claim.path, record)
}

func deferredPeerText(record messagestore.Record) (string, error) {
	if record.Envelope.Target.Provider == aiModeCodex {
		return codexCoordinationContent(record.Envelope)
	}
	// The renderer needs only the durable broker envelope, not live authority.
	return providerCoordinationContent(claudeCoordinationEnvelope{BrokerEnvelope: &record.Envelope})
}

func (claim *deferredProcessClaim) resume(ctx context.Context, frame processResumeFirstFrame, first *messagestore.Record) (processAgentResumeResult, error) {
	if err := ctx.Err(); err != nil {
		return processAgentResumeResult{}, err
	}
	c := claim.command
	if first != nil {
		text, err := deferredPeerText(*first)
		if err != nil {
			return processAgentResumeResult{}, err
		}
		frame = processResumeFirstFrame{Kind: "peer", Text: text}
		if err = claim.markInflight(first.Envelope.MessageRef); err != nil {
			return processAgentResumeResult{}, err
		}
	}
	resumeCtx := ctx
	cancelResume := func() {}
	if first != nil {
		resumeCtx, cancelResume = context.WithDeadline(ctx, first.Envelope.Deadline)
	}
	defer cancelResume()
	options := claim.options
	options.claim, options.Prompt = claim, frame
	request, err := newProcessAgentResumeRequest(options)
	var result processAgentResumeResult
	if err == nil {
		result, err = c.resumeProcessAgent(resumeCtx, request)
	}
	if err != nil {
		noChild := result.hasNoChild()
		err = result.fail(err)
		// A durable exact Stop fence can precede signal/context observation.
		// Only an undispatched peer and a pre-birth, unmodified conversation
		// may be released as cancellation; handoff evidence remains unknown.
		cancelled := ctx.Err()
		if noChild && result.previousRecord == nil && cancelled == nil {
			stopped, stopErr := claim.preBirthStopRequested()
			if stopErr != nil {
				return result, errors.Join(err, stopErr)
			}
			if stopped {
				cancelled = context.Canceled
			}
		}
		if first != nil {
			current, found, readErr := c.messageStore.Get(first.Envelope.MessageRef)
			if readErr != nil || !found {
				return result, errors.Join(err, readErr, messagestore.ErrNotFound)
			}
			first.HandoffObserved = current.HandoffObserved
			first.Delivery = current.Delivery
		}
		undispatched := first == nil || (!first.HandoffObserved && !first.Delivery.OutcomeUnknown && !first.Delivery.State.Terminal())
		if noChild && cancelled != nil && result.previousRecord == nil && undispatched {
			if first != nil {
				if clearErr := claim.markInflight(""); clearErr != nil {
					return result, clearErr
				}
			}
			result.Handle = nil
			return result, cancelled
		}
		if first != nil {
			kind, reason := coremessage.EventFail, processResumeRefused
			unknown := first.HandoffObserved || first.Delivery.OutcomeUnknown
			if unknown {
				reason = "provider-handoff-outcome-unknown"
			} else if resumeCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				kind, reason = coremessage.EventExpire, "deadline-expired"
			}
			_, applyErr := c.terminalCoordination(*first, kind, reason, unknown, nil)
			err = errors.Join(err, applyErr)
			if applyErr == nil {
				err = errors.Join(err, claim.markInflight(""))
			}
		}
		return result, err
	}
	// Provider init/resume proved the original conversation. Persist delivery
	// before clearing its crash witness, so no new claimant can duplicate it.
	if first != nil {
		_, _, err = c.messageStore.Apply(first.Envelope.MessageRef, c.publicMessageEvent(*first, coremessage.EventDeliver, "provider-resume-first-frame", false))
		if err != nil {
			return result, result.fail(err)
		}
		if err = claim.markInflight(""); err != nil {
			return result, result.fail(err)
		}
	}
	changed, controls, attention, syncErr := result.resumeSynchronization(c.rebind.create)
	if syncErr != nil {
		return result, result.fail(syncErr)
	}
	result.deferredSynchronization = &processResumeSynchronization{changed, controls, attention}
	if err = c.settleDeferredInput(claim, true, false, result.Binding.Operation+"-resume", ""); err != nil {
		return result, result.fail(err)
	}
	if err = claim.drain(ctx, result); err != nil && err != ctx.Err() {
		return result, result.fail(err)
	}
	if err = claim.Close(); err != nil {
		return result, result.fail(err)
	}
	return result, nil
}

func (claim *deferredProcessClaim) drain(ctx context.Context, result processAgentResumeResult) error {
	c := claim.command
	// Deferred and ordinary held release share one target queue. Hold this
	// before listing/readdressing so neither can submit a stale held snapshot.
	unlock, err := c.lockDeferredRelease(ctx, claim.record.Agent)
	if err != nil {
		return err
	}
	defer unlock()
	snapshot, err := result.Handle.Observe(result.Binding)
	if err != nil {
		return err
	}
	if err = result.owner.recordProcessSnapshot(snapshot); err != nil {
		return err
	}
	registry, err := c.readMessageRegistry()
	if err != nil {
		return err
	}
	pane, _ := processResumePane(registry, claim.record.Agent)
	if pane == nil || pane.Status.ProcessSession == nil {
		return fmt.Errorf("resume session snapshot unavailable: %w", processhost.ErrStale)
	}
	session := pane.Status.ProcessSession
	if session.Binding != metadataProcessBinding(result.Binding) || session.SessionID != claim.record.Session || session.ThreadID != claim.record.Thread {
		return fmt.Errorf("resume session snapshot differs: %w", processhost.ErrStale)
	}
	target, found := registry.Agent(claim.record.Agent)
	if !found {
		return processhost.ErrStale
	}
	route, err := c.resolveMessageTargetRoute(registry, *target)
	if err != nil {
		return err
	}
	old, next := deferredMessageRoute(claim.record), publicMessageRoute(route)
	if old.AgentUID != next.AgentUID || old.PaneUID != next.PaneUID || old.Provider != next.Provider || old.Incarnation != next.Incarnation {
		return fmt.Errorf("resume route differs from retired conversation: %w", processhost.ErrStale)
	}
	store, ok := c.messageStore.(interface {
		ReaddressDeferred(string, coremessage.Route, coremessage.Route, string, time.Time) (messagestore.Record, error)
	})
	if !ok {
		return errors.New("process-host-unavailable: deferred release store unavailable")
	}
	held, err := claim.held()
	if err != nil {
		return err
	}
	// Address all unsubmitted holds before waiting. If the owner ends this
	// generation, the next claimant can match the new retired binding.
	for i, record := range held {
		if record.Envelope.Target != old || !record.Envelope.Deadline.After(c.messageClock()) {
			continue
		}
		held[i], err = store.ReaddressDeferred(record.Envelope.MessageRef, old, next, deferredHoldReason, c.messageClock())
		if err != nil {
			return err
		}
	}
	synchronize := processSnapshotSynchronizer(result.deferredSynchronization.changed, func(snapshot processhost.Snapshot) error {
		if len(snapshot.Pending) > 0 {
			return result.deferredSynchronization.controls(context.WithoutCancel(ctx))
		}
		return nil
	})
release:
	for _, record := range held {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !record.Envelope.Deadline.After(c.messageClock()) {
			_, _, err = c.messageStore.Status(record.Envelope.MessageRef, c.messageClock())
			if err != nil {
				return err
			}
			continue
		}
		if record.Envelope.Target != next {
			_, err = c.terminalCoordination(record, coremessage.EventStale, "stale-binding", false, nil)
			if err != nil {
				return err
			}
			continue
		}
		for {
			snapshot, observeErr := result.Handle.Observe(result.Binding)
			if observeErr != nil {
				return observeErr
			}
			if !record.Envelope.Deadline.After(c.messageClock()) {
				_, _, err = c.messageStore.Status(record.Envelope.MessageRef, c.messageClock())
				if err != nil {
					return err
				}
				continue release
			}
			if err = synchronize(snapshot); err != nil {
				return err
			}
			if snapshot.Turn == "" {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
		// Persist the crash witness before submitting. A dead
		// claimant is never permission to resend a potentially written frame.
		if err = c.releaseDeferredPeer(ctx, record, claim.markInflight, func() error {
			snapshot, err := result.Handle.Observe(result.Binding)
			if err != nil {
				return err
			}
			return synchronize(snapshot)
		}); err != nil {
			return err
		}
	}
	return nil
}

// A short lock attempt preserves owner cancellation while using the ordinary
// release's existing lock and total wait bound.
func (c *agentCommand) lockDeferredRelease(ctx context.Context, uid string) (func(), error) {
	locker, ok := c.messageStore.(agentMessageReleaseLocker)
	if !ok {
		return nil, errors.New("process-host-unavailable: deferred release lock unavailable")
	}
	end := time.Now().Add(agentMessageReleaseLockWait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, err := locker.LockTargetRelease(uid, 20*time.Millisecond)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, messagestore.ErrBusy) || !time.Now().Before(end) {
			return nil, err
		}
	}
}

// The caller holds the target release lock. Judge the oldest held record with
// the ordinary release path, which does not hold it behind later queued peers.
// Only a proved zero-write hold can be retried; terminal/unknown outcomes retain
// their existing meaning. Persist a crash witness around each possible write.
func (c *agentCommand) releaseDeferredPeer(ctx context.Context, record messagestore.Record, mark func(string) error, syncStep func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if syncStep != nil {
			if err := syncStep(); err != nil {
				return err
			}
		}
		current, found, err := c.messageStore.Get(record.Envelope.MessageRef)
		if err != nil || !found {
			return errors.Join(err, messagestore.ErrNotFound)
		}
		if current.Delivery.State.Terminal() {
			return nil
		}
		if !current.Envelope.Deadline.After(c.messageClock()) {
			_, _, err = c.messageStore.Status(current.Envelope.MessageRef, c.messageClock())
			return err
		}
		if err = mark(current.Envelope.MessageRef); err != nil {
			return err
		}
		outcome, _, err := c.releaseHeldMessage(current)
		if err != nil {
			return err
		}
		settled, found, err := c.messageStore.Get(current.Envelope.MessageRef)
		if err != nil || !found {
			return errors.Join(err, messagestore.ErrNotFound)
		}
		if settled.Delivery.State.Terminal() {
			return mark("")
		}
		if settled.Delivery.State != coremessage.StateHeld || settled.HandoffObserved || (outcome != heldReleaseBlocked && outcome != heldReleaseUndecidable) {
			return fmt.Errorf("%s: deferred release did not settle", processResumeRefused)
		}
		if err = mark(""); err != nil {
			return err
		}
		if err = c.sleepMessage(ctx, 20*time.Millisecond); err != nil {
			return err
		}
	}
}

// The caller has proved no child and no previous-record replacement. Read Stop
// under the reservation guard; a replacement/stale owner cannot lend its fence.
func (claim *deferredProcessClaim) preBirthStopRequested() (bool, error) {
	unlock, err := lockDeferredClaim(claim.path)
	if err != nil {
		return false, err
	}
	defer unlock()
	record, err := readDeferredClaim(claim.path)
	if err != nil {
		return false, err
	}
	return record.StopRequested && deferredClaimIdentityEqual(record, claim.record) && deferredClaimLive(record), nil
}
