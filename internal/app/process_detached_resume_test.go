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
			cancel()
		}
		return reg, nil
	}
	text, err := deferredPeerText(first)
	if err != nil {
		t.Fatal(err)
	}
	result, err := claim.Resume(ctx, processResumeFirstFrame{Kind: "peer", Text: text})
	if !stopped || err != context.Canceled || result.Handle != nil || result.previousRecord != nil {
		t.Fatalf("Stop did not retire pre-birth selection: stopped=%v err=%v handle=%v", stopped, err, result.Handle)
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
	if err != nil || len(held) != 2 {
		t.Fatal("Stop lost undispatched peer queue", err)
	}
	for i, ref := range []string{"race-first", "race-second"} {
		if held[i].Envelope.MessageRef != ref || held[i].Delivery.State != coremessage.StateHeld || held[i].Delivery.OutcomeUnknown {
			t.Fatalf("Stop reordered/promoted/failed an undispatched frame: %+v", held[i])
		}
	}
}
