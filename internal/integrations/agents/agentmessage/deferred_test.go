package agentmessage

import (
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

func TestDeferredReaddressIsNarrowAndAcceptanceOrdered(t *testing.T) {
	s := NewStore(t.TempDir())
	now := time.Now().UTC()
	var envelopes []coremessage.Envelope
	for i := range 3 {
		e := storeEnvelope(i)
		e.AcceptedAt = now
		e.Deadline = now.Add(time.Minute)
		envelopes = append(envelopes, e)
		if _, _, err := s.PutAccepted(e, "codex-inbox"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Apply(e.MessageRef, coremessage.Event{Kind: coremessage.EventHold, MessageRef: e.MessageRef, ConversationRef: e.ConversationRef, Target: e.Target, Reason: "target-awaiting-resume", ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	held, err := s.DeferredFor(envelopes[0].Target.AgentUID, "target-awaiting-resume")
	if err != nil || len(held) != 3 {
		t.Fatal("held", err, len(held))
	}
	for i, r := range held {
		if r.Envelope.MessageRef != envelopes[i].MessageRef {
			t.Fatal("acceptance order changed")
		}
	}
	old := envelopes[0].Target
	for _, change := range []func(*coremessage.Route){func(r *coremessage.Route) { r.AgentUID += "-other" }, func(r *coremessage.Route) { r.PaneUID += "-other" }, func(r *coremessage.Route) { r.Provider = "claude" }, func(r *coremessage.Route) { r.Incarnation += "-other" }} {
		next := old
		change(&next)
		if _, err = s.ReaddressDeferred(envelopes[0].MessageRef, old, next, "target-awaiting-resume", now); err == nil {
			t.Fatal("foreign readdress accepted")
		}
	}
	next := old
	next.ActivationGeneration = "resumed-generation"
	got, err := s.ReaddressDeferred(envelopes[0].MessageRef, old, next, "target-awaiting-resume", now)
	if err != nil {
		t.Fatal(err)
	}
	expected := envelopes[0]
	expected.Target = next
	if got.Envelope != expected {
		t.Fatal("envelope bytes changed beyond generation")
	}
	if _, _, err = s.Apply(got.Envelope.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: got.Envelope.MessageRef, ConversationRef: got.Envelope.ConversationRef, Target: next, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReaddressDeferred(got.Envelope.MessageRef, next, old, "target-awaiting-resume", now); err == nil {
		t.Fatal("terminal record moved")
	}
}
