package app

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type qualificationBarrierPoster struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	outcome claudeProviderPostOutcome
}

func (p *qualificationBarrierPoster) Post(string, func() bool) (claudeProviderPostOutcome, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	close(p.started)
	<-p.release
	return p.outcome, nil
}

func (p *qualificationBarrierPoster) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type qualificationPosterRecorder struct {
	calls   int
	content string
	outcome claudeProviderPostOutcome
	err     error
}

func (p *qualificationPosterRecorder) Post(content string, fence func() bool) (claudeProviderPostOutcome, error) {
	if fence != nil && !fence() {
		return claudeProviderPostOutcome{}, errors.New("route stale")
	}
	p.calls++
	p.content = content
	return p.outcome, p.err
}

func exactQualificationEvidence(route coremetadata.AgentRouteRef, now time.Time) claudeQualificationEvidence {
	authority := route.Authority().(coremetadata.ClaudeAuthorityRef)
	return claudeQualificationEvidence{
		Version: claudeQualificationEvidenceVersion, ClaudeCodeVersion: claudeFrozenFrameProviderVersion,
		SessionID: authority.SessionID, AgentUID: route.AgentUID, PaneUID: route.PaneUID,
		ActivationGeneration: route.Generation, RouteIncarnation: route.Incarnation(), ProviderProcess: authority.Process,
		RegistrationGeneration: authority.RegistrationGeneration, HelperProcess: authority.LeaseProcess,
		Tools: []string{"Bash"}, ReplyExecutionGate: true, MCPServers: []string{}, Plugins: []string{}, InboundPolicy: "accept",
		PublicInitObserved: true, StreamFrozen: true, ObservedAt: now,
	}
}

func qualificationTestEnvelope(route coremetadata.AgentRouteRef, now time.Time) claudeCoordinationEnvelope {
	envelope := dialogueForRoute("qualification-owned", route, now)
	envelope.BrokerEnvelope.Payload = "Explicit reply challenge"
	return envelope
}

func explicitTestReply(original coremessage.Envelope, text string) coremessage.Envelope {
	return coremessage.Envelope{Version: coremessage.Version, MessageRef: "reply-" + original.MessageRef,
		ConversationRef: original.ConversationRef, ReplyTo: original.MessageRef, Source: original.Target, Target: original.Source,
		Authority: coremessage.PeerAuthority(), Payload: text, AcceptedAt: original.AcceptedAt, Deadline: original.Deadline}
}

func TestClaudeQualificationRequiresBrokerChallengeAndExplicitReply(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	hub := fixture.server.hub
	hub.now = func() time.Time { return now }
	broker := &failingClaudeDialogueBroker{}
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	fixture.server.broker, fixture.server.poster = broker, poster
	challenge := qualificationTestEnvelope(fixture.route, now)
	evidence := exactQualificationEvidence(fixture.route, now)
	hub.userPrompt()
	started := hub.beginExplicitQualification(evidence, fixture.route, &challenge, broker, poster)
	if started.Kind != "qualification-pending" || hub.qualificationResponse(challenge.MessageRef).Kind != "qualification-pending" || poster.calls != 1 || broker.handoffs != 1 || broker.deliveries != 1 {
		t.Fatalf("started=%+v", started)
	}
	stop := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "stop-reply", Target: fixture.target,
		SessionID: fixture.sessionID, AssistantMessage: claudeQualificationMarkerPrefix + challenge.MessageRef})
	if stop.Kind != "reply-refused" || hub.qualificationResponse(challenge.MessageRef).Kind != "qualification-pending" || broker.replies != 0 {
		t.Fatalf("Stop gained authority: %+v", stop)
	}
	// General admission no longer waits for qualification. Delivery and the
	// challenge are independent: an ordinary message is pushed on its own while
	// the challenge stays pending.
	general := dialogueForRoute("message-general-before-qualified", fixture.route, now)
	if got := hub.submitPush(general, broker, poster); got.State != agentdelivery.StateDelivered || poster.calls != 2 {
		t.Fatalf("general admission changed: %+v calls=%d", got, poster.calls)
	}
	if got := hub.qualificationResponse(challenge.MessageRef); got.Kind != "qualification-pending" {
		t.Fatalf("general delivery must not qualify the activation")
	}
	generalReply := []string{"projmux", "agent", "message", "send", "uid:" + general.BrokerEnvelope.Source.AgentUID,
		"--reply-to", general.MessageRef, "--", "general reply"}
	if hub.permitsExplicitTool(generalReply, fixture.route, broker) {
		t.Fatal("pending qualification opened the general reply execution gate")
	}
	hub.userPrompt() // Human presence has no authority to revoke an explicit action.
	reply := explicitTestReply(*challenge.BrokerEnvelope, claudeQualificationMarkerPrefix+challenge.MessageRef)
	completed := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "explicit-reply", Target: fixture.target, SessionID: fixture.sessionID, ReplyEnvelope: &reply})
	if completed.Kind != "reply-accepted" || broker.replies != 1 {
		t.Fatalf("explicit=%+v", completed)
	}
	if got := hub.qualificationResponse(challenge.MessageRef); got.Kind != "qualification-qualified" || got.Reason != "exact-public-init-and-explicit-reply" {
		t.Fatalf("qualification=%+v", got)
	}
	if !hub.permitsExplicitTool(generalReply, fixture.route, broker) {
		t.Fatal("completed qualification did not open the general reply execution gate")
	}
	hub.close()
	if got := hub.qualificationResponse(challenge.MessageRef); got.Kind != "qualification-failed" || got.Reason != "helper-restart" || !got.Ambiguous || got.AutoResend {
		t.Fatalf("qualification survived helper close: %+v", got)
	}
	if hub.permitsExplicitTool(generalReply, fixture.route, broker) {
		t.Fatal("reply execution gate survived helper close")
	}
}

func TestClaudeExplicitReplyRejectsForeignStaleAndAlteredCorrelationBeforeCommit(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	challenge := qualificationTestEnvelope(fixture.route, now)
	for _, tc := range []struct {
		name   string
		mutate func(*coremessage.Envelope)
	}{
		{"foreign ref", func(e *coremessage.Envelope) { e.ReplyTo = "message-foreign" }},
		{"foreign original source", func(e *coremessage.Envelope) { e.Target.AgentUID = "agent-other" }},
		{"stale source generation", func(e *coremessage.Envelope) { e.Source.ActivationGeneration = "old" }},
		{"stale target incarnation", func(e *coremessage.Envelope) { e.Target.Incarnation = "old" }},
		{"altered conversation", func(e *coremessage.Envelope) { e.ConversationRef = "conversation-foreign" }},
		{"challenge text mismatch", func(e *coremessage.Envelope) { e.Payload = "echo without correct challenge" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newClaudeCoordinationHub()
			hub.now = func() time.Time { return now }
			broker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			hub.beginExplicitQualification(exactQualificationEvidence(fixture.route, now), fixture.route, &challenge, broker, poster)
			reply := explicitTestReply(*challenge.BrokerEnvelope, claudeQualificationMarkerPrefix+challenge.MessageRef)
			tc.mutate(&reply)
			if got := hub.commitExplicitReply(reply, fixture.route, broker); got.Kind != "reply-refused" || broker.replies != 0 {
				t.Fatalf("reply=%+v commits=%d", got, broker.replies)
			}
			if got := hub.qualificationResponse(challenge.MessageRef); got.Kind != "qualification-pending" {
				t.Fatalf("invalid reply changed qualification: %+v", got)
			}
		})
	}
}

func TestClaudeExplicitQualificationMissingToolProofOrOriginalWritesZero(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	base := exactQualificationEvidence(fixture.route, now)
	for _, tc := range []struct {
		name   string
		mutate func(*claudeQualificationEvidence)
	}{
		{"old version", func(e *claudeQualificationEvidence) { e.ClaudeCodeVersion = "2.1.261" }},
		{"no tool", func(e *claudeQualificationEvidence) { e.Tools = []string{} }},
		{"other tool", func(e *claudeQualificationEvidence) { e.Tools = []string{"Bash", "Read"} }},
		{"no gate", func(e *claudeQualificationEvidence) { e.ReplyExecutionGate = false }},
		{"stale process", func(e *claudeQualificationEvidence) { e.ProviderProcess.Start += "old" }},
		{"stale helper", func(e *claudeQualificationEvidence) { e.HelperProcess.Start += "old" }},
		{"stale evidence", func(e *claudeQualificationEvidence) { e.ObservedAt = now.Add(-claudeQualificationEvidenceMaxAge) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newClaudeCoordinationHub()
			hub.now = func() time.Time { return now }
			e := base
			tc.mutate(&e)
			broker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{}
			challenge := qualificationTestEnvelope(fixture.route, now)
			if got := hub.beginExplicitQualification(e, fixture.route, &challenge, broker, poster); got.Kind != "qualification-refused" || poster.calls != 0 || broker.handoffs != 0 {
				t.Fatalf("got=%+v", got)
			}
		})
	}
	hub := newClaudeCoordinationHub()
	poster := &qualificationPosterRecorder{}
	broker := &failingClaudeDialogueBroker{}
	if got := hub.beginExplicitQualification(base, fixture.route, nil, broker, poster); got.Kind != "qualification-refused" || poster.calls != 0 {
		t.Fatalf("missing original=%+v", got)
	}
}

func TestClaudeExplicitQualificationPartialAndTimeoutNeverResend(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "partial"}[partial], func(t *testing.T) {
			hub := newClaudeCoordinationHub()
			clock := now
			hub.now = func() time.Time { return clock }
			challenge := qualificationTestEnvelope(fixture.route, now)
			broker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{WroteAny: true, FullFrameWritten: !partial}}
			hub.beginExplicitQualification(exactQualificationEvidence(fixture.route, now), fixture.route, &challenge, broker, poster)
			clock = challenge.Deadline.Add(time.Second)
			expired := hub.qualificationResponse(challenge.MessageRef)
			if expired.Kind != "qualification-failed" || !expired.Ambiguous || expired.AutoResend {
				t.Fatalf("expired=%+v", expired)
			}
			hub.beginExplicitQualification(exactQualificationEvidence(fixture.route, clock), fixture.route, &challenge, broker, poster)
			if poster.calls != 1 || hub.qualificationResponse(challenge.MessageRef).Kind != "qualification-failed" {
				t.Fatal("failed qualification resent or opened")
			}
		})
	}
}

func TestClaudeExplicitMultipleRequestsAndHumanOverlapSelectOnlyNamedOriginal(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	hub := qualifiedPushHub(now)
	broker := &failingClaudeDialogueBroker{}
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	a := dialogueForRoute("message-a", fixture.route, now)
	b := dialogueForRoute("message-b", fixture.route, now)
	hub.submitPush(a, broker, poster)
	hub.userPrompt()
	hub.submitPush(b, broker, poster)
	hub.userPrompt()
	for _, original := range []coremessage.Envelope{*b.BrokerEnvelope, *a.BrokerEnvelope} {
		reply := explicitTestReply(original, "chosen reply")
		if got := hub.commitExplicitReply(reply, fixture.route, broker); got.Kind != "reply-accepted" {
			t.Fatalf("reply=%+v", got)
		}
		reply.MessageRef += "-second"
		if got := hub.commitExplicitReply(reply, fixture.route, broker); got.Kind != "reply-refused" {
			t.Fatalf("duplicate=%+v", got)
		}
	}
	if broker.replies != 2 {
		t.Fatalf("commits=%d", broker.replies)
	}
}

func TestClaudeQualificationPublishesOriginalBeforeConcurrentExplicitReply(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	hub := newClaudeCoordinationHub()
	hub.now = func() time.Time { return now }
	challenge := qualificationTestEnvelope(fixture.route, now)
	broker := &failingClaudeDialogueBroker{}
	poster := &qualificationBarrierPoster{started: make(chan struct{}), release: make(chan struct{}), outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	qualificationDone := make(chan claudeCoordinationResponse, 1)
	go func() {
		qualificationDone <- hub.beginExplicitQualification(exactQualificationEvidence(fixture.route, now), fixture.route, &challenge, broker, poster)
	}()
	<-poster.started
	// A reply observing the full frame may arrive before Post returns. The
	// publication boundary must still be held until the durable record exists.
	if hub.mu.TryLock() {
		hub.mu.Unlock()
		close(poster.release)
		<-qualificationDone
		t.Fatal("reply could observe unpublished challenge after provider write")
	}
	attempted := make(chan struct{})
	replyDone := make(chan claudeCoordinationResponse, 1)
	go func() {
		close(attempted)
		replyDone <- hub.commitExplicitReply(explicitTestReply(*challenge.BrokerEnvelope, claudeQualificationMarkerPrefix+challenge.MessageRef), fixture.route, broker)
	}()
	<-attempted
	select {
	case result := <-replyDone:
		close(poster.release)
		<-qualificationDone
		t.Fatalf("reply completed before publication: %+v", result)
	default:
	}
	close(poster.release)
	if got := <-qualificationDone; got.Kind != "qualification-pending" {
		t.Fatalf("qualification=%+v", got)
	}
	if got := <-replyDone; got.Kind != "reply-accepted" || broker.replies != 1 || poster.callCount() != 1 {
		t.Fatalf("reply=%+v commits=%d", got, broker.replies)
	}
	if got := hub.qualificationResponse(challenge.MessageRef); got.Kind != "qualification-qualified" || got.Reason != "exact-public-init-and-explicit-reply" {
		t.Fatalf("qualification=%+v", got)
	}
}
