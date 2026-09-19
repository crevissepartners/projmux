package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// holdClaudeAdapter delivers every Submit and counts it. Status must never be
// asked about a held record, so it only counts and fails.
type holdClaudeAdapter struct {
	store    *messagestore.Store
	submits  []string
	statuses int
	onSubmit func(string)
}

func (a *holdClaudeAdapter) Submit(_ context.Context, _ string, _ coremetadata.AgentRouteRef, envelope coremessage.Envelope) (agentdelivery.Delivery, error) {
	a.submits = append(a.submits, envelope.MessageRef)
	if a.onSubmit != nil {
		a.onSubmit(envelope.MessageRef)
	}
	return agentdelivery.Delivery{MessageRef: envelope.MessageRef, State: agentdelivery.StateDelivered,
		Reason: "provider-pipe-full-frame", WaiterRef: "push-" + envelope.MessageRef}, nil
}

func (a *holdClaudeAdapter) Status(context.Context, string, coremetadata.AgentRouteRef, string) (agentdelivery.Delivery, error) {
	a.statuses++
	return agentdelivery.Delivery{}, errors.New("a held record must not reach the helper")
}

// ExplicitReply creates the reply receipt the way the source helper's broker
// does, so the Claude-source --reply-to path reaches the delivery step.
func (a *holdClaudeAdapter) ExplicitReply(_ context.Context, _ string, _ coremetadata.AgentRouteRef, reply coremessage.Envelope) (string, bool, error) {
	record, created, err := a.store.PutReply(reply.ReplyTo, reply.MessageRef, reply.Payload, reply.Source, reply.Target, reply.AcceptedAt, reply.Deadline)
	if err != nil {
		return "", false, err
	}
	return record.Envelope.MessageRef, created, nil
}

type holdRouteResolver struct{ f *holdFixture }

func (r holdRouteResolver) Resolve(coremetadata.Registry, coremetadata.Agent) (coremetadata.AgentRouteRef, error) {
	r.f.resolves++
	if len(r.f.routeErrs) > 0 {
		err := r.f.routeErrs[0]
		r.f.routeErrs = r.f.routeErrs[1:]
		if err != nil {
			return coremetadata.AgentRouteRef{}, err
		}
		return r.f.route, nil
	}
	if r.f.routeErr != nil {
		return coremetadata.AgentRouteRef{}, r.f.routeErr
	}
	return r.f.route, nil
}

type holdFixture struct {
	cmd      *agentCommand
	registry *coremetadata.Registry
	store    *messagestore.Store
	adapter  *holdClaudeAdapter
	route    coremetadata.AgentRouteRef
	routeErr error
	// routeErrs is consumed one per Resolve before routeErr applies; a nil
	// entry answers with the route.
	routeErrs []error
	resolves  int
	// registryFailures fails that many Registry reads before they succeed.
	registryFailures int
	claudeUID        string
	original         coremessage.Envelope
	now              time.Time
	launches         []string
	// waits records each release wait once installFakeSleep is used; onWait
	// runs after the clock advanced for that wait.
	waits  []time.Duration
	onWait func()
	// transcript is what the target's transcript tail read returns;
	// transcriptTruncated and transcriptErr complete that answer.
	transcript          []byte
	transcriptTruncated bool
	transcriptErr       error
	transcriptReads     int
	// registryWrites counts committed Registry updates; beforeUpdate runs on
	// the working copy before the update function sees it.
	registryWrites int
	beforeUpdate   func(*coremetadata.Registry)
}

// newHoldFixture wires one Claude Agent as both source and target, with one
// delivered original request a reply can answer. The clock stays near real
// time because the store prunes and judges reply deadlines by its own clock.
func newHoldFixture(t *testing.T) *holdFixture {
	t.Helper()
	coordination := newClaudeCoordinationTestFixture(t)
	h := newSessionRefHarness(t, aiModeClaude)
	store := messagestore.NewStore(t.TempDir())
	now := time.Now().UTC()
	public := publicMessageRoute(coordination.route)
	original := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-hold-original",
		ConversationRef: "conversation-hold-original", Source: public, Target: public,
		Authority: coremessage.PeerAuthority(), Payload: "request", AcceptedAt: now.Add(-time.Second), Deadline: now.Add(5 * time.Minute)}
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: original.MessageRef,
		ConversationRef: original.ConversationRef, Target: original.Target, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	f := &holdFixture{registry: h.registry, store: store, adapter: &holdClaudeAdapter{store: store},
		route: coordination.route, claudeUID: h.agentUID, original: original, now: now}
	f.cmd = &agentCommand{
		activeTarget: insideTmux(h.paneUID, "").lookup,
		messagePaths: agentMessagePaths{registryPath: coordination.registryPath, loadRegistry: func() (coremetadata.Registry, error) {
			if f.registryFailures > 0 {
				f.registryFailures--
				return coremetadata.Registry{}, errors.New("registry read failed")
			}
			return f.registry.Clone(), nil
		}},
		messageStore:  store,
		messageRoute:  holdRouteResolver{f: f},
		messageClaude: f.adapter,
		messageNow:    func() time.Time { return f.now },
		messageNewRef: func(prefix string) string {
			t.Fatalf("explicit references must not mint %s", prefix)
			return ""
		},
		messageRelease: func(agentUID string) error {
			f.launches = append(f.launches, agentUID)
			return nil
		},
		messageTranscriptTail: func(string) ([]byte, bool, error) {
			f.transcriptReads++
			return f.transcript, f.transcriptTruncated, f.transcriptErr
		},
		store: &resourceStore{
			update: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
				working := f.registry.Clone()
				if f.beforeUpdate != nil {
					f.beforeUpdate(&working)
				}
				if err := fn(&working); err != nil {
					return coremetadata.Registry{}, err
				}
				*f.registry = working
				f.registryWrites++
				return working.Clone(), nil
			},
			mutator: func() coremetadata.Mutator { return coremetadata.Mutator{} },
		},
	}
	return f
}

// installFakeSleep makes every release wait return at once after advancing the
// fixture clock by the requested duration, and records the duration.
func (f *holdFixture) installFakeSleep() {
	f.cmd.messageSleep = func(_ context.Context, duration time.Duration) error {
		f.waits = append(f.waits, duration)
		f.now = f.now.Add(duration)
		if f.onWait != nil {
			f.onWait()
		}
		return nil
	}
}

func (f *holdFixture) setInteraction(t *testing.T, kind coremetadata.AgentInteractionKind) {
	t.Helper()
	agent, ok := f.registry.Agent(f.claudeUID)
	if !ok {
		t.Fatal("claude agent fixture missing")
	}
	agent.Status.Interaction = coremetadata.AgentInteraction{Kind: kind, ObservedAt: f.now,
		Source: string(coremetadata.InteractionSourceProviderHook)}
}

func (f *holdFixture) send(t *testing.T, ref string, reply bool) (string, error) {
	t.Helper()
	args := []string{"message", "send", "uid:" + f.claudeUID, "--source", "uid:" + f.claudeUID, "--message-ref", ref}
	if reply {
		args = append(args, "--reply-to", f.original.MessageRef)
	}
	stdout, _, err := runRoute(t, f.cmd, append(args, "--", "coordinate "+ref)...)
	return stdout, err
}

func (f *holdFixture) delivery(t *testing.T, ref string) coremessage.Delivery {
	t.Helper()
	return persistedDelivery(t, f.store, ref).Delivery
}

// putHeld stores one held record addressed to the fixture Agent directly, so a
// test controls its acceptance time and deadline.
func (f *holdFixture) putHeld(t *testing.T, ref string, acceptedAt, deadline time.Time) {
	t.Helper()
	public := publicMessageRoute(f.route)
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: conversationRefFor(ref),
		Source: public, Target: public, Authority: coremessage.PeerAuthority(), Payload: "coordinate " + ref,
		AcceptedAt: acceptedAt, Deadline: deadline}
	if _, _, err := f.store.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if record, _, err := f.store.Apply(ref, coremessage.Event{Kind: coremessage.EventHold, MessageRef: ref,
		ConversationRef: envelope.ConversationRef, Target: envelope.Target, Reason: claudeHoldReasonAwaitingOperator,
		ObservedAt: acceptedAt}); err != nil || record.Delivery.State != coremessage.StateHeld {
		t.Fatalf("hold %s = %+v err=%v", ref, record.Delivery, err)
	}
}

func wantHeldReceipt(t *testing.T, stdout string, err error, ref string) {
	t.Helper()
	want := ref + "\theld\t" + claudeHoldReasonAwaitingOperator + "\t" + agentMessageHeldAction(ref) + "\n"
	if err != nil || stdout != want {
		t.Fatalf("stdout=%q err=%v, want %q and exit 0", stdout, err, want)
	}
	for _, phrase := range []string{"dialog closes", "projmux agent message status " + ref, "do not resend"} {
		if !strings.Contains(stdout, phrase) {
			t.Fatalf("held action %q does not say %q", stdout, phrase)
		}
	}
}

// Test group 1: a target awaiting its operator holds the message on both send
// paths and never calls the helper; any other interaction pushes at once.
func TestAgentMessageSendHoldsWhileClaudeTargetAwaitsOperator(t *testing.T) {
	for _, kind := range []coremetadata.AgentInteractionKind{coremetadata.InteractionApprovalRequired, coremetadata.InteractionInputRequired} {
		for _, reply := range []bool{false, true} {
			name := string(kind) + "/send"
			if reply {
				name = string(kind) + "/claude-source reply"
			}
			t.Run(name, func(t *testing.T) {
				f := newHoldFixture(t)
				f.setInteraction(t, kind)
				const ref = "message-hold-blocked"
				stdout, err := f.send(t, ref, reply)
				wantHeldReceipt(t, stdout, err, ref)
				if len(f.adapter.submits) != 0 {
					t.Fatalf("submits = %v, want none while the target awaits its operator", f.adapter.submits)
				}
				if got := f.delivery(t, ref); got.State != coremessage.StateHeld || got.Reason != claudeHoldReasonAwaitingOperator {
					t.Fatalf("persisted = %+v, want held/%s", got, claudeHoldReasonAwaitingOperator)
				}
				// A blocked target may never send the hook that ends the
				// hold, so the hold launches the release that watches it.
				if !slices.Equal(f.launches, []string{f.claudeUID}) {
					t.Fatalf("launches = %v, want one release watching the blocked target", f.launches)
				}
			})
		}
	}
	for _, kind := range []coremetadata.AgentInteractionKind{coremetadata.InteractionResponseComplete, coremetadata.InteractionInProgress,
		coremetadata.InteractionIdle, coremetadata.InteractionUnknown} {
		t.Run(string(kind)+"/pushes", func(t *testing.T) {
			f := newHoldFixture(t)
			f.setInteraction(t, kind)
			const ref = "message-hold-open"
			stdout, err := f.send(t, ref, false)
			if err != nil || stdout != ref+"\tdelivered\n" || !slices.Equal(f.adapter.submits, []string{ref}) {
				t.Fatalf("stdout=%q err=%v submits=%v, want one delivered push", stdout, err, f.adapter.submits)
			}
		})
	}
	t.Run("stale approval does not hold", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionApprovalRequired)
		f.now = f.now.Add(coremetadata.AgentInteractionFreshFor + time.Minute)
		stdout, err := f.send(t, "message-hold-stale-observation", false)
		if err != nil || !strings.HasSuffix(stdout, "\tdelivered\n") || len(f.adapter.submits) != 1 {
			t.Fatalf("stdout=%q err=%v submits=%v, want an expired observation to push", stdout, err, f.adapter.submits)
		}
	})
	t.Run("hold races a closing dialog", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionApprovalRequired)
		// The dialog closes between the send's read and its hold write: the
		// registry the re-read sees no longer blocks, so the send launches
		// the release itself.
		reads := 0
		f.cmd.messagePaths.loadRegistry = func() (coremetadata.Registry, error) {
			reads++
			registry := f.registry.Clone()
			if reads > 1 {
				agent, _ := registry.Agent(f.claudeUID)
				agent.Status.Interaction.Kind = coremetadata.InteractionInProgress
			}
			return registry, nil
		}
		stdout, err := f.send(t, "message-hold-race", false)
		wantHeldReceipt(t, stdout, err, "message-hold-race")
		if !slices.Equal(f.launches, []string{f.claudeUID}) || len(f.adapter.submits) != 0 {
			t.Fatalf("launches=%v submits=%v, want one release launch and no push", f.launches, f.adapter.submits)
		}
	})
}

// Test group 2: a message sent while earlier messages are held waits behind
// them, and the release delivers the earlier one first.
func TestAgentMessageSendQueuesBehindEarlierHeldMessages(t *testing.T) {
	f := newHoldFixture(t)
	f.setInteraction(t, coremetadata.InteractionApprovalRequired)
	stdout, err := f.send(t, "message-hold-first", false)
	wantHeldReceipt(t, stdout, err, "message-hold-first")

	f.now = f.now.Add(time.Second)
	f.setInteraction(t, coremetadata.InteractionInProgress)
	stdout, err = f.send(t, "message-hold-second", false)
	wantHeldReceipt(t, stdout, err, "message-hold-second")
	if len(f.adapter.submits) != 0 {
		t.Fatalf("the later message overtook the held one: submits=%v", f.adapter.submits)
	}
	if !slices.Equal(f.launches, []string{f.claudeUID, f.claudeUID}) {
		t.Fatalf("launches = %v, want one release per hold", f.launches)
	}

	if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
		t.Fatal(err)
	}
	if want := []string{"message-hold-first", "message-hold-second"}; !slices.Equal(f.adapter.submits, want) {
		t.Fatalf("release order = %v, want %v", f.adapter.submits, want)
	}
	for _, ref := range []string{"message-hold-first", "message-hold-second"} {
		if got := f.delivery(t, ref); got.State != coremessage.StateDelivered {
			t.Fatalf("%s = %+v, want delivered", ref, got)
		}
	}
}

// Test group 4: the release delivers in acceptance order, stops when the
// target awaits its operator again, and finishes expired and stale records
// without a push.
func TestAgentMessageReleaseOrderReblockExpiryAndStale(t *testing.T) {
	putThree := func(t *testing.T, f *holdFixture) {
		t.Helper()
		base := f.now.Add(-time.Minute)
		// Stored out of acceptance order on purpose.
		f.putHeld(t, "message-release-c", base.Add(2*time.Second), f.now.Add(5*time.Minute))
		f.putHeld(t, "message-release-a", base, f.now.Add(5*time.Minute))
		f.putHeld(t, "message-release-b", base.Add(time.Second), f.now.Add(5*time.Minute))
	}
	t.Run("accepted order", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		putThree(t, f)
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if want := []string{"message-release-a", "message-release-b", "message-release-c"}; !slices.Equal(f.adapter.submits, want) {
			t.Fatalf("release order = %v, want %v", f.adapter.submits, want)
		}
	})
	t.Run("re-block watches and leaves the rest held", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		base := f.now.Add(-time.Minute)
		// Deadlines past the watch window, so the window ends before any
		// record expires.
		f.putHeld(t, "message-release-c", base.Add(2*time.Second), f.now.Add(time.Hour))
		f.putHeld(t, "message-release-a", base, f.now.Add(time.Hour))
		f.putHeld(t, "message-release-b", base.Add(time.Second), f.now.Add(time.Hour))
		f.adapter.onSubmit = func(string) { f.setInteraction(t, coremetadata.InteractionInputRequired) }
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if want := []string{"message-release-a"}; !slices.Equal(f.adapter.submits, want) {
			t.Fatalf("release pushed %v, want only %v before the target blocked again", f.adapter.submits, want)
		}
		if got := sumHoldWaits(f.waits); got != agentMessageReleaseRetryWindow {
			t.Fatalf("watched %v, want the whole %v window", got, agentMessageReleaseRetryWindow)
		}
		for _, ref := range []string{"message-release-b", "message-release-c"} {
			if got := f.delivery(t, ref); got.State != coremessage.StateHeld {
				t.Fatalf("%s = %+v, want still held", ref, got)
			}
		}
	})
	t.Run("expired deadline", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.putHeld(t, "message-release-expired", f.now.Add(-time.Minute), f.now.Add(time.Minute))
		f.now = f.now.Add(2 * time.Minute)
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, "message-release-expired"); got.State != coremessage.StateExpired || got.Reason != "deadline-expired" ||
			len(f.adapter.submits) != 0 {
			t.Fatalf("expired = %+v submits=%v, want expired/deadline-expired and no push", got, f.adapter.submits)
		}
	})
	t.Run("route stale", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.putHeld(t, "message-release-stale", f.now.Add(-time.Minute), f.now.Add(5*time.Minute))
		f.routeErr = errors.New("claude registration lease is stale or unavailable")
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, "message-release-stale"); got.State != coremessage.StateStale || got.Reason != "target-activation-stale" ||
			len(f.adapter.submits) != 0 {
			t.Fatalf("stale = %+v submits=%v, want stale/target-activation-stale and no push", got, f.adapter.submits)
		}
	})
	t.Run("target removed", func(t *testing.T) {
		f := newHoldFixture(t)
		f.putHeld(t, "message-release-removed", f.now.Add(-time.Minute), f.now.Add(5*time.Minute))
		f.registry.Agents = slices.DeleteFunc(f.registry.Agents, func(agent coremetadata.Agent) bool {
			return agent.Metadata.UID == f.claudeUID
		})
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, "message-release-removed"); got.State != coremessage.StateStale || got.Reason != "target-removed" {
			t.Fatalf("removed = %+v, want stale/target-removed", got)
		}
	})
	t.Run("handoff observed is never pushed again", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.putHeld(t, "message-release-handoff", f.now.Add(-2*time.Minute), f.now.Add(5*time.Minute))
		if _, _, err := f.store.MarkHandoff("message-release-handoff"); err != nil {
			t.Fatal(err)
		}
		f.putHeld(t, "message-release-after-handoff", f.now.Add(-time.Minute), f.now.Add(5*time.Minute))
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, "message-release-handoff"); got.State != coremessage.StateFailed ||
			got.Reason != "provider-handoff-outcome-unknown" || !got.OutcomeUnknown {
			t.Fatalf("handoff-observed = %+v, want failed/provider-handoff-outcome-unknown with outcomeUnknown", got)
		}
		if want := []string{"message-release-after-handoff"}; !slices.Equal(f.adapter.submits, want) {
			t.Fatalf("release pushed %v, want only %v", f.adapter.submits, want)
		}
	})
	t.Run("busy probe leaves held", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		// The deadline lies past the retry window, so the window ends first
		// and the record is left held rather than expired.
		f.putHeld(t, "message-release-busy", f.now.Add(-time.Minute), f.now.Add(time.Hour))
		f.routeErr = claudeProbeUnansweredError{probe: "registration lease probe"}
		f.installFakeSleep()
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, "message-release-busy"); got.State != coremessage.StateHeld || len(f.adapter.submits) != 0 {
			t.Fatalf("busy = %+v submits=%v, want still held", got, f.adapter.submits)
		}
	})
	t.Run("a second release waits and finds nothing lost", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.putHeld(t, "message-release-first", f.now.Add(-time.Minute), f.now.Add(5*time.Minute))
		// A message held while the first release is pushing is picked up by
		// the same release's final listing under its lock.
		f.adapter.onSubmit = func(ref string) {
			if ref == "message-release-first" {
				f.putHeld(t, "message-release-late", f.now, f.now.Add(5*time.Minute))
			}
		}
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if want := []string{"message-release-first", "message-release-late"}; !slices.Equal(f.adapter.submits, want) {
			t.Fatalf("release pushed %v, want %v", f.adapter.submits, want)
		}
	})
}

// Test group 7: a record the release cannot judge yet, because the target's
// helper did not answer its probe or the Registry could not be read, is judged
// again in place after a backoff wait, inside one bounded retry window.
func TestAgentMessageReleaseRejudgesAfterAnUndecidableRead(t *testing.T) {
	unanswered := claudeProbeUnansweredError{probe: "registration lease probe"}
	sumWaits := func(waits []time.Duration) time.Duration {
		var sum time.Duration
		for _, wait := range waits {
			sum += wait
		}
		return sum
	}
	t.Run("unanswered probe then ready delivers", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		const ref = "message-retry-probe"
		f.putHeld(t, ref, f.now.Add(-time.Minute), f.now.Add(5*time.Minute))
		f.routeErrs = []error{unanswered, unanswered}
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, ref); got.State != coremessage.StateDelivered || !slices.Equal(f.adapter.submits, []string{ref}) {
			t.Fatalf("record = %+v submits=%v, want delivered by one push", got, f.adapter.submits)
		}
		if want := []time.Duration{2 * time.Second, 4 * time.Second}; !slices.Equal(f.waits, want) {
			t.Fatalf("waits = %v, want %v", f.waits, want)
		}
	})
	t.Run("registry read failure then success delivers", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		const ref = "message-retry-registry"
		f.putHeld(t, ref, f.now.Add(-time.Minute), f.now.Add(5*time.Minute))
		f.registryFailures = 1
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, ref); got.State != coremessage.StateDelivered || !slices.Equal(f.adapter.submits, []string{ref}) {
			t.Fatalf("record = %+v submits=%v, want delivered by one push", got, f.adapter.submits)
		}
		if want := []time.Duration{2 * time.Second}; !slices.Equal(f.waits, want) {
			t.Fatalf("waits = %v, want %v", f.waits, want)
		}
	})
	t.Run("registry never readable ends with its error", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		const ref = "message-retry-registry-down"
		f.putHeld(t, ref, f.now.Add(-time.Minute), f.now.Add(time.Hour))
		f.registryFailures = 1 << 20
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err == nil || !strings.Contains(err.Error(), "registry read failed") {
			t.Fatalf("release err = %v, want the last Registry read failure", err)
		}
		if got := f.delivery(t, ref); got.State != coremessage.StateHeld || len(f.adapter.submits) != 0 {
			t.Fatalf("record = %+v submits=%v, want still held", got, f.adapter.submits)
		}
		if got := sumWaits(f.waits); got != agentMessageReleaseRetryWindow {
			t.Fatalf("waited %v, want the whole %v window", got, agentMessageReleaseRetryWindow)
		}
	})
	t.Run("target blocked again during a wait is watched", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		const ref = "message-retry-reblock"
		f.putHeld(t, ref, f.now.Add(-time.Minute), f.now.Add(time.Hour))
		f.routeErr = unanswered
		f.onWait = func() { f.setInteraction(t, coremetadata.InteractionInputRequired) }
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, ref); got.State != coremessage.StateHeld || len(f.adapter.submits) != 0 {
			t.Fatalf("record = %+v submits=%v, want still held and no push", got, f.adapter.submits)
		}
		// The blocked target is watched at the fixed interval, not at the
		// doubling backoff, until the window ends.
		for _, wait := range f.waits {
			if wait != agentMessageReleaseWatchInterval {
				t.Fatalf("waits = %v, want every wait %v", f.waits, agentMessageReleaseWatchInterval)
			}
		}
		if got := sumWaits(f.waits); got != agentMessageReleaseRetryWindow {
			t.Fatalf("watched %v, want the whole %v window", got, agentMessageReleaseRetryWindow)
		}
	})
	t.Run("deadline during the retry expires the record", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		const ref = "message-retry-deadline"
		f.putHeld(t, ref, f.now.Add(-time.Minute), f.now.Add(5*time.Second))
		f.routeErr = unanswered
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, ref); got.State != coremessage.StateExpired || got.Reason != "deadline-expired" || len(f.adapter.submits) != 0 {
			t.Fatalf("record = %+v submits=%v, want expired/deadline-expired and no push", got, f.adapter.submits)
		}
		if want := []time.Duration{2 * time.Second, 3 * time.Second}; !slices.Equal(f.waits, want) {
			t.Fatalf("waits = %v, want %v", f.waits, want)
		}
	})
	t.Run("window ends after ten minutes", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		const ref = "message-retry-window"
		f.putHeld(t, ref, f.now.Add(-time.Minute), f.now.Add(time.Hour))
		f.routeErr = unanswered
		start := f.now
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, ref); got.State != coremessage.StateHeld || len(f.adapter.submits) != 0 {
			t.Fatalf("record = %+v submits=%v, want still held and no push", got, f.adapter.submits)
		}
		for _, wait := range f.waits {
			if wait > agentMessageReleaseRetryMaxWait {
				t.Fatalf("wait %v exceeds %v: %v", wait, agentMessageReleaseRetryMaxWait, f.waits)
			}
		}
		if got := sumWaits(f.waits); got != 10*time.Minute || f.now.Sub(start) != 10*time.Minute {
			t.Fatalf("waited %v (clock +%v), want exactly 10m", got, f.now.Sub(start))
		}
	})
	t.Run("window ends at the earliest held deadline", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		f.putHeld(t, "message-retry-first", f.now.Add(-time.Minute), f.now.Add(time.Hour))
		f.putHeld(t, "message-retry-later", f.now.Add(-time.Second), f.now.Add(3*time.Minute))
		f.routeErr = unanswered
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, "message-retry-first"); got.State != coremessage.StateHeld || len(f.adapter.submits) != 0 {
			t.Fatalf("first = %+v submits=%v, want still held and no push", got, f.adapter.submits)
		}
		if got := sumWaits(f.waits); got != 3*time.Minute {
			t.Fatalf("waited %v, want the window to end at the 3m deadline", got)
		}
	})
	t.Run("a passed deadline of a later record does not close the window", func(t *testing.T) {
		f := newHoldFixture(t)
		f.installFakeSleep()
		f.putHeld(t, "message-retry-live", f.now.Add(-2*time.Minute), f.now.Add(time.Hour))
		f.putHeld(t, "message-retry-overdue", f.now.Add(-time.Minute), f.now.Add(time.Minute))
		// The later record's deadline passes before the release starts; the
		// store still lists it as held.
		f.now = f.now.Add(2 * time.Minute)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.routeErrs = []error{unanswered, unanswered}
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, "message-retry-live"); got.State != coremessage.StateDelivered ||
			!slices.Equal(f.adapter.submits, []string{"message-retry-live"}) {
			t.Fatalf("live = %+v submits=%v, want delivered by one push", got, f.adapter.submits)
		}
		if want := []time.Duration{2 * time.Second, 4 * time.Second}; !slices.Equal(f.waits, want) {
			t.Fatalf("waits = %v, want %v", f.waits, want)
		}
		if got := f.delivery(t, "message-retry-overdue"); got.State != coremessage.StateExpired || got.Reason != "deadline-expired" {
			t.Fatalf("overdue = %+v, want expired/deadline-expired", got)
		}
	})
	t.Run("undecidable record past its own deadline expires without a wait", func(t *testing.T) {
		f := newHoldFixture(t)
		f.installFakeSleep()
		const ref = "message-retry-overdue-first"
		f.putHeld(t, ref, f.now.Add(-time.Minute), f.now.Add(time.Minute))
		f.now = f.now.Add(2 * time.Minute)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.routeErr = unanswered
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, ref); got.State != coremessage.StateExpired || got.Reason != "deadline-expired" ||
			len(f.adapter.submits) != 0 || len(f.waits) != 0 {
			t.Fatalf("record = %+v submits=%v waits=%v, want expired/deadline-expired with no push and no wait",
				got, f.adapter.submits, f.waits)
		}
	})
	t.Run("retry keeps acceptance order", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		base := f.now.Add(-time.Minute)
		// Stored out of acceptance order on purpose.
		f.putHeld(t, "message-retry-c", base.Add(2*time.Second), f.now.Add(5*time.Minute))
		f.putHeld(t, "message-retry-a", base, f.now.Add(5*time.Minute))
		f.putHeld(t, "message-retry-b", base.Add(time.Second), f.now.Add(5*time.Minute))
		f.routeErrs = []error{unanswered, unanswered}
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if want := []string{"message-retry-a", "message-retry-b", "message-retry-c"}; !slices.Equal(f.adapter.submits, want) {
			t.Fatalf("release pushed %v, want %v each once", f.adapter.submits, want)
		}
	})
	t.Run("handoff observed after an unanswered probe is never pushed", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionInProgress)
		f.installFakeSleep()
		f.putHeld(t, "message-retry-handoff", f.now.Add(-2*time.Minute), f.now.Add(5*time.Minute))
		if _, _, err := f.store.MarkHandoff("message-retry-handoff"); err != nil {
			t.Fatal(err)
		}
		f.putHeld(t, "message-retry-after-handoff", f.now.Add(-time.Minute), f.now.Add(5*time.Minute))
		f.routeErrs = []error{unanswered}
		if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
			t.Fatal(err)
		}
		if got := f.delivery(t, "message-retry-handoff"); got.State != coremessage.StateFailed ||
			got.Reason != "provider-handoff-outcome-unknown" || !got.OutcomeUnknown {
			t.Fatalf("handoff-observed = %+v, want failed/provider-handoff-outcome-unknown with outcomeUnknown", got)
		}
		if want := []string{"message-retry-after-handoff"}; !slices.Equal(f.adapter.submits, want) {
			t.Fatalf("release pushed %v, want only %v", f.adapter.submits, want)
		}
	})
}

// Test group 5: status answers a held record from the store, never from the
// helper, and follows the release to delivered or the TTL to expired.
func TestAgentMessageStatusAnswersHeldFromTheStore(t *testing.T) {
	f := newHoldFixture(t)
	f.setInteraction(t, coremetadata.InteractionApprovalRequired)
	const ref = "message-status-held"
	if _, err := f.send(t, ref, false); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runRoute(t, f.cmd, "message", "status", ref)
	wantHeldReceipt(t, stdout, err, ref)
	if f.adapter.statuses != 0 {
		t.Fatalf("status asked the helper %d times about a held record", f.adapter.statuses)
	}

	f.setInteraction(t, coremetadata.InteractionInProgress)
	if err := f.cmd.releaseHeldMessages(f.claudeUID); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runRoute(t, f.cmd, "message", "status", ref)
	if err != nil || stdout != ref+"\tdelivered\n" || f.adapter.statuses != 0 {
		t.Fatalf("status after release = %q err=%v statuses=%d, want delivered", stdout, err, f.adapter.statuses)
	}

	t.Run("expired held", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionApprovalRequired)
		const ref = "message-status-expired"
		if _, _, err := runRoute(t, f.cmd, "message", "send", "uid:"+f.claudeUID, "--source", "uid:"+f.claudeUID,
			"--message-ref", ref, "--ttl", "1m", "--", "coordinate"); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(2 * time.Minute)
		stdout, _, err := runRoute(t, f.cmd, "message", "status", ref)
		if err != nil || !strings.HasPrefix(stdout, ref+"\texpired\tdeadline-expired\t") || f.adapter.statuses != 0 {
			t.Fatalf("expired status = %q err=%v statuses=%d", stdout, err, f.adapter.statuses)
		}
	})
}

func setClaudeQuietInteraction(t *testing.T, f *claudeQuietHookFixture, kind coremetadata.AgentInteractionKind) {
	t.Helper()
	agent, ok := f.registry.Agent(f.agentUID)
	if !ok {
		t.Fatal("claude agent fixture missing")
	}
	agent.Status.Interaction = coremetadata.AgentInteraction{Kind: kind, ObservedAt: claudeQuietHookClock,
		Source: string(coremetadata.InteractionSourceProviderHook)}
}

// countRegistryWrites wraps the fixture's write seam.
func countRegistryWrites(f *claudeQuietHookFixture) *int {
	writes := 0
	update := f.cmd.updateRegistry
	f.cmd.updateRegistry = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		writes++
		return update(fn)
	}
	return &writes
}

// Test group 3: each event that follows an operator answer moves only a
// blocked Agent to in_progress, state only; every other Agent stays quiet and
// the event writes nothing.
func TestClaudeOperatorDialogCloseEventsMoveOnlyABlockedAgentToInProgress(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"PostToolUse", "PostToolUseFailure", "PermissionDenied", "ElicitationResult"} {
		for _, kind := range []coremetadata.AgentInteractionKind{coremetadata.InteractionApprovalRequired, coremetadata.InteractionInputRequired,
			coremetadata.InteractionResponseComplete, coremetadata.InteractionInProgress, coremetadata.InteractionIdle} {
			t.Run(event+"/"+string(kind), func(t *testing.T) {
				t.Parallel()
				f := newClaudeQuietHookFixture(t, true)
				// Settle the session ref first, so the event under test is
				// judged on its own writes.
				f.ingest(t, "PreToolUse")
				setClaudeQuietInteraction(t, f, kind)
				before := f.registry.Clone()
				writes := countRegistryWrites(f)
				notified := len(f.cmd.notifyStore.(*stubNotifyStore).pushed)

				f.ingest(t, event)

				agent, _ := f.registry.Agent(f.agentUID)
				if agentInteractionAwaitsOperator(kind) {
					if agent.Status.Interaction.Kind != coremetadata.InteractionInProgress ||
						agent.Status.Interaction.Source != string(coremetadata.InteractionSourceProviderHook) {
						t.Fatalf("interaction = %+v, want provider-hook in_progress", agent.Status.Interaction)
					}
					if pushed := len(f.cmd.notifyStore.(*stubNotifyStore).pushed); pushed != notified {
						t.Fatalf("a state-only close pushed %d notifications", pushed-notified)
					}
					return
				}
				if *writes != 0 || !reflect.DeepEqual(before, *f.registry) {
					t.Fatalf("%s on %s opened %d Registry writes or changed the Registry", event, kind, *writes)
				}
				records := claudeQuietHookLogRecords(t, f.cmd)
				if last := records[len(records)-1]; last.Event != event || last.Result != "quiet" {
					t.Fatalf("last ingest record = %+v, want a quiet %s", last, event)
				}
			})
		}
	}
}

// Test group 6: the commit that moves a Claude Agent off awaiting its
// operator launches the release only when that Agent has a held message.
func TestClaudeUnblockCommitLaunchesReleaseOnlyWithHeldMessages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		from   coremetadata.AgentInteractionKind
		event  string
		held   bool
		launch bool
	}{
		{name: "approval answered with held", from: coremetadata.InteractionApprovalRequired, event: "PostToolUse", held: true, launch: true},
		{name: "question answered with held", from: coremetadata.InteractionInputRequired, event: "UserPromptSubmit", held: true, launch: true},
		{name: "stop over approval with held", from: coremetadata.InteractionApprovalRequired, event: "Stop", held: true, launch: true},
		{name: "approval answered without held", from: coremetadata.InteractionApprovalRequired, event: "PostToolUse"},
		{name: "not blocked with held", from: coremetadata.InteractionInProgress, event: "Stop", held: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newClaudeQuietHookFixture(t, true)
			store := messagestore.NewStore(t.TempDir())
			var launches []string
			storeReads := 0
			f.cmd.heldRelease = heldMessageRelease{
				store:  func() (agentMessageHeldLister, error) { storeReads++; return store, nil },
				launch: func(agentUID string) error { launches = append(launches, agentUID); return nil },
			}
			if tc.held {
				now := time.Now().UTC()
				route := coremessage.Route{AgentUID: f.agentUID, PaneUID: "pane-held", ActivationGeneration: "generation-held",
					Provider: "claude", Incarnation: "incarnation-held"}
				envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-trigger-held",
					ConversationRef: "conversation-trigger-held", Source: route, Target: route, Authority: coremessage.PeerAuthority(),
					Payload: "coordinate", AcceptedAt: now, Deadline: now.Add(time.Minute)}
				if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.Apply(envelope.MessageRef, coremessage.Event{Kind: coremessage.EventHold, MessageRef: envelope.MessageRef,
					ConversationRef: envelope.ConversationRef, Target: route, Reason: claudeHoldReasonAwaitingOperator, ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			setClaudeQuietInteraction(t, f, tc.from)
			f.ingest(t, tc.event)
			want := []string(nil)
			if tc.launch {
				want = []string{f.agentUID}
			}
			if !slices.Equal(launches, want) {
				t.Fatalf("launches = %v, want %v", launches, want)
			}
			// Only the blocked-to-unblocked commit reads the message store.
			if wantReads := map[bool]int{true: 1, false: 0}[agentInteractionAwaitsOperator(tc.from)]; storeReads != wantReads {
				t.Fatalf("message store reads = %d, want %d", storeReads, wantReads)
			}
		})
	}
}

func TestAgentMessageReleaseRouteIsPlumbingOnly(t *testing.T) {
	if shouldRunLegacyHookMigrations([]string{"internal", agentMessageReleaseRoute, "--agent", "uid:agent-01"}) {
		t.Fatal("the detached release attempted automatic settings migration")
	}
	for _, args := range [][]string{nil, {"--agent"}, {"--agent", "agent-01"}, {"--pane", "uid:agent-01"}} {
		if err := runAgentMessageRelease(args); err == nil || !IsUsageError(err) {
			t.Fatalf("args %q error = %v, want a usage error", args, err)
		}
	}
}
