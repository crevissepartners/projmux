package app

import (
	"context"
	"errors"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// A peer selection is persisted before planning/birth. Stop must fence birth
// without fencing the same claimant's cancelled selection cleanup and Close.
func TestDetachedProcessStoppedClaimClearsPreBirthSelection(t *testing.T) {
	c, options := deferredClaimFixture(t)
	claim, err := c.claimDeferredProcessAgent((processOwnerLifetime{}).withContext(context.Background()), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Close() })
	before, err := c.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err = claim.markInflight("selected-peer"); err != nil {
		t.Fatal(err)
	}
	record, err := readDeferredClaim(claim.path)
	if err != nil {
		t.Fatal(err)
	}
	record.StopRequested = true
	if err = writeDeferredClaim(claim.path, record); err != nil {
		t.Fatal(err)
	}
	if err = c.checkDeferredClaim(claim.record.Agent, claim); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("stopped claim admitted birth", err)
	}
	if err = claim.markInflight("new-peer"); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("stopped claim accepted new selection", err)
	}
	if err = claim.markInflight(""); err != nil {
		t.Fatal("stopped exact claimant cannot clear pre-birth selection", err)
	}
	cleaned, err := readDeferredClaim(claim.path)
	if err != nil || cleaned.Inflight != "" || !cleaned.StopRequested {
		t.Fatal("cleanup changed stop fence or retained selection", cleaned, err)
	}
	if err = claim.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(claim.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stopped claim not released", err)
	}
	after, err := c.loadRegistry()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("pre-birth cleanup fabricated child/Wait or changed Registry", err)
	}
}

func TestDetachedProcessClaimCleanupRejectsReplacementIdentity(t *testing.T) {
	c, options := deferredClaimFixture(t)
	claim, err := c.claimDeferredProcessAgent((processOwnerLifetime{}).withContext(context.Background()), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Close() })
	record := claim.record
	record.Inflight = "selected-peer"
	record.StopRequested = true
	record.Nonce += "-replacement"
	if err = writeDeferredClaim(claim.path, record); err != nil {
		t.Fatal(err)
	}
	if err = claim.markInflight(""); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("cleanup borrowed replacement identity", err)
	}
	current, err := readDeferredClaim(claim.path)
	if err != nil || current != record {
		t.Fatal("replacement witness changed", current, err)
	}
	if err = claim.Close(); err != nil {
		t.Fatal(err)
	}
	current, err = readDeferredClaim(claim.path)
	if err != nil || current != record {
		t.Fatal("old Close erased replacement", current, err)
	}
}

// Stop wins after the real first envelope is selected and its write witness is
// durable, but before candidate admission/provider birth. The callback repeats
// Stop's guarded marker + owner cancellation without signaling this test's PID.
func TestDetachedProcessFirstPeerStopBeforeBirthPreservesQueue(t *testing.T) {
	testDetachedPreBirthStopQueue(t, true, false)
}

func TestDetachedProcessStopFenceBeforeSignalDeliveryPreservesPeer(t *testing.T) {
	testDetachedPreBirthStopQueue(t, false, false)
}

func TestDetachedProcessStopFencePreservesUnknownHandoffEvidence(t *testing.T) {
	testDetachedPreBirthStopQueue(t, false, true)
}

func testDetachedPreBirthStopQueue(t *testing.T, signalDelivered, handoff bool) {
	t.Helper()
	c, options := deferredClaimFixture(t)
	ctx, cancel := context.WithCancel((processOwnerLifetime{}).withContext(context.Background()))
	defer cancel()
	claim, err := c.claimDeferredProcessAgent(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Close() })
	load := c.loadRegistry
	before, err := load()
	if err != nil {
		t.Fatal(err)
	}
	store := c.messageStore.(*messagestore.Store)
	now := time.Now().UTC()
	route := deferredMessageRoute(claim.record)
	var first messagestore.Record
	for i, ref := range []string{"race-first", "race-second"} {
		envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: "race-conversation", Source: route, Target: route, Authority: coremessage.PeerAuthority(), Payload: ref, AcceptedAt: now.Add(time.Duration(i) * time.Millisecond), Deadline: now.Add(time.Minute)}
		record, _, err := store.PutDeferred(envelope, "claude-coordination")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = record
		}
	}
	if handoff {
		if _, _, err = store.MarkHandoff(first.Envelope.MessageRef); err != nil {
			t.Fatal(err)
		}
	}
	stopped := false
	c.loadRegistry = func() (coremetadata.Registry, error) {
		reg, err := load()
		if err != nil {
			return reg, err
		}
		record, err := readDeferredClaim(claim.path)
		if err != nil {
			return reg, err
		}
		if !stopped && record.Inflight == first.Envelope.MessageRef {
			unlock, err := lockDeferredClaim(claim.path)
			if err != nil {
				return reg, err
			}
			defer unlock()
			record, err = readDeferredClaim(claim.path)
			if err != nil {
				return reg, err
			}
			pane, _ := reg.Pane(record.Binding.PaneUID)
			agent, _ := reg.Agent(record.Agent)
			if !deferredClaimIdentityEqual(record, claim.record) || !deferredClaimLive(record) || pane == nil || agent == nil || !pane.Status.Activation.IsZero() || pane.Status.ProcessSession.Binding != record.Binding || agent.Status.Phase != coremetadata.PhaseOffline {
				t.Fatal("Stop did not win before birth with exact proof")
			}
			record.StopRequested = true
			if err = writeDeferredClaim(claim.path, record); err != nil {
				return reg, err
			}
			stopped = true
			if signalDelivered {
				cancel()
			}
		}
		return reg, nil
	}
	text, err := deferredPeerText(first)
	if err != nil {
		t.Fatal(err)
	}
	result, err := claim.Resume(ctx, processResumeFirstFrame{Kind: "peer", Text: text})
	resumeErr := err
	if !stopped || result.Handle != nil || result.previousRecord != nil {
		t.Fatalf("Stop did not retire pre-birth selection: stopped=%v err=%v handle=%v", stopped, err, result.Handle)
	}
	if !signalDelivered && ctx.Err() != nil {
		t.Fatal("test delivered signal before durable fence was judged")
	}
	if err = claim.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(claim.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pre-birth Stop retained claim", err)
	}
	after, err := load()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("pre-birth Stop invented child/Wait or changed Registry", err)
	}
	c.loadRegistry = load
	replacement, err := c.claimDeferredProcessAgent((processOwnerLifetime{}).withContext(context.Background()), options)
	if err != nil {
		t.Fatal("stopped claim cannot be reclaimed", err)
	}
	defer replacement.Close()
	held, err := replacement.held()
	refs := []string{"race-first", "race-second"}
	if handoff {
		record, _, readErr := store.Get("race-first")
		if readErr != nil || record.Delivery.State != coremessage.StateFailed || !record.HandoffObserved || !record.Delivery.OutcomeUnknown {
			t.Fatal("Stop erased handoff/unknown evidence", record, readErr)
		}
		refs = refs[1:]
	}
	if err != nil || len(held) != len(refs) {
		record, _, readErr := store.Get("race-first")
		t.Fatalf("Stop lost undispatched peer queue: held=%d want=%d first=%s reason=%s unknown=%v handoff=%v err=%v readErr=%v", len(held), len(refs), record.Delivery.State, record.Delivery.Reason, record.Delivery.OutcomeUnknown, record.HandoffObserved, err, readErr)
	}
	for i, ref := range refs {
		if held[i].Envelope.MessageRef != ref || held[i].Delivery.State != coremessage.StateHeld || held[i].Delivery.OutcomeUnknown {
			t.Fatalf("Stop reordered/promoted/failed an undispatched frame: %+v", held[i])
		}
	}
	if !handoff && resumeErr != context.Canceled {
		t.Fatal("exact pre-birth Stop was not cancellation", resumeErr)
	}
}

func TestDetachedProcessDeferredReleaseOldestAndConcurrentRelease(t *testing.T) {
	f := newHoldFixture(t)
	refs := []string{"drain-first", "drain-second", "drain-third"}
	for i, ref := range refs {
		f.putHeld(t, ref, f.now.Add(time.Duration(i)*time.Millisecond), f.now.Add(time.Minute), deferredHoldReason)
	}
	unlock, err := f.cmd.lockDeferredRelease(context.Background(), f.claudeUID)
	if err != nil {
		t.Fatal(err)
	}
	held, err := f.store.DeferredFor(f.claudeUID, deferredHoldReason)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	f.adapter.busy = 1
	f.installFakeSleep()
	// The ordinary release starts while the deferred owner holds the same lock.
	started, done := make(chan struct{}), make(chan error, 1)
	go func() { close(started); done <- f.cmd.releaseHeldMessages(f.claudeUID) }()
	<-started
	var witnesses []string
	for _, record := range held {
		err = f.cmd.releaseDeferredPeer(context.Background(), record, func(ref string) error { witnesses = append(witnesses, ref); return nil }, nil)
		if err != nil {
			unlock()
			<-done
			t.Fatal(err)
		}
	}
	unlock()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.adapter.submits, []string{refs[0], refs[0], refs[1], refs[2]}) {
		t.Fatal("concurrent release duplicated or reordered writes", f.adapter.submits)
	}
	if !reflect.DeepEqual(witnesses, []string{refs[0], "", refs[0], "", refs[1], "", refs[2], ""}) {
		t.Fatal("crash witness did not bracket writes", witnesses)
	}
	for _, ref := range refs {
		if f.delivery(t, ref).State != coremessage.StateDelivered {
			t.Fatal("oldest held was stranded", ref)
		}
	}
	again, err := f.cmd.lockDeferredRelease(context.Background(), f.claudeUID)
	if err != nil {
		t.Fatal("release lock leaked", err)
	}
	again()
}

func TestDetachedProcessDeferredReleaseZeroWriteCancelAndDeadline(t *testing.T) {
	for _, ending := range []string{"cancel", "deadline"} {
		t.Run(ending, func(t *testing.T) {
			f := newHoldFixture(t)
			f.putHeld(t, "drain-busy", f.now, f.now.Add(time.Second), deferredHoldReason)
			f.putHeld(t, "drain-later", f.now.Add(time.Millisecond), f.now.Add(time.Minute), deferredHoldReason)
			held, err := f.store.DeferredFor(f.claudeUID, deferredHoldReason)
			if err != nil || len(held) != 2 {
				t.Fatal("missing held fixture", held, err)
			}
			f.adapter.busy = 1
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.installFakeSleep()
			f.onWait = func() {
				if ending == "cancel" {
					cancel()
				} else {
					f.now = f.now.Add(time.Second)
				}
			}
			var witness string
			unlock, err := f.cmd.lockDeferredRelease(ctx, f.claudeUID)
			if err != nil {
				t.Fatal(err)
			}
			err = f.cmd.releaseDeferredPeer(ctx, held[0], func(ref string) error { witness = ref; return nil }, nil)
			unlock()
			if ending == "cancel" && err != context.Canceled {
				t.Fatal("cancel ignored", err)
			}
			if ending == "deadline" && err != nil {
				t.Fatal(err)
			}
			if witness != "" || len(f.adapter.submits) != 1 || f.delivery(t, "drain-later").State != coremessage.StateHeld {
				t.Fatal("zero-write hold lost witness/queue", witness, f.adapter.submits)
			}
			record, _, _ := f.store.Get("drain-busy")
			want := coremessage.StateHeld
			if ending == "deadline" {
				want = coremessage.StateExpired
			}
			if record.Delivery.State != want || record.HandoffObserved || record.Delivery.OutcomeUnknown {
				t.Fatal("zero-write outcome changed", record)
			}
			again, err := f.cmd.lockDeferredRelease(context.Background(), f.claudeUID)
			if err != nil {
				t.Fatal("cancel/deadline leaked lock", err)
			}
			again()
		})
	}
}

func TestDetachedProcessDeferredReleaseUnknownIsNeverRewritten(t *testing.T) {
	f := newHoldFixture(t)
	f.putHeld(t, "drain-unknown", f.now, f.now.Add(time.Minute), deferredHoldReason)
	if _, _, err := f.store.MarkHandoff("drain-unknown"); err != nil {
		t.Fatal(err)
	}
	record, _, _ := f.store.Get("drain-unknown")
	var witness string
	unlock, err := f.cmd.lockDeferredRelease(context.Background(), f.claudeUID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = f.cmd.releaseDeferredPeer(context.Background(), record, func(ref string) error { witness = ref; return nil }, nil); err != nil {
		t.Fatal(err)
	}
	settled, _, _ := f.store.Get("drain-unknown")
	if len(f.adapter.submits) != 0 || witness != "" || settled.Delivery.State != coremessage.StateFailed || !settled.Delivery.OutcomeUnknown {
		t.Fatal("unknown frame rewritten or witness retained", settled)
	}
	if err = f.cmd.releaseDeferredPeer(context.Background(), record, func(string) error { t.Fatal("terminal frame obtained a new witness"); return nil }, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDetachedProcessDeferredReleaseLockCancellation(t *testing.T) {
	f := newHoldFixture(t)
	unlock, err := f.store.LockTargetRelease(f.claudeUID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := f.cmd.lockDeferredRelease(ctx, f.claudeUID); err != context.Canceled || release != nil {
		unlock()
		t.Fatal("cancelled owner waited for release", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if release, err := f.cmd.lockDeferredRelease(ctx, f.claudeUID); err != context.DeadlineExceeded || release != nil {
		unlock()
		t.Fatal("waiting owner ignored cancellation", err)
	}
	unlock()
	again, err := f.cmd.lockDeferredRelease(context.Background(), f.claudeUID)
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestDetachedProcessStopFenceRejectsReplacementOrStaleProof(t *testing.T) {
	for _, change := range []string{"nonce", "binding", "kernel"} {
		t.Run(change, func(t *testing.T) {
			c, options := deferredClaimFixture(t)
			claim, err := c.claimDeferredProcessAgent((processOwnerLifetime{}).withContext(context.Background()), options)
			if err != nil {
				t.Fatal(err)
			}
			defer claim.Close()
			record := claim.record
			record.StopRequested = true
			switch change {
			case "nonce":
				record.Nonce += "-replacement"
			case "binding":
				record.Binding.Generation += "-replacement"
			case "kernel":
				record.Process.Start += "-stale"
			}
			if err = writeDeferredClaim(claim.path, record); err != nil {
				t.Fatal(err)
			}
			if stopped, err := claim.preBirthStopRequested(); err != nil || stopped {
				t.Fatal("borrowed replacement/stale Stop fence", stopped, err)
			}
			current, err := readDeferredClaim(claim.path)
			if err != nil || current != record {
				t.Fatal("foreign Stop proof mutated", current, err)
			}
		})
	}
}
