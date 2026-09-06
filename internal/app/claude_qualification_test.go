package app

import (
	"errors"
	"sync"
	"testing"
	"time"

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
		Tools: []string{}, MCPServers: []string{}, Plugins: []string{}, InboundPolicy: "accept",
		PublicInitObserved: true, StreamFrozen: true, ObservedAt: now,
	}
}

func TestClaudeQualificationRequiresExactPublicInitAndStopMarker(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Unix(10_000, 0).UTC()
	hub := newClaudeCoordinationHub()
	hub.now = func() time.Time { return now }
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}

	started := hub.beginQualification(exactQualificationEvidence(fixture.route, now), fixture.route, poster)
	if started.Kind != "qualification-pending" || started.QualificationRef == "" || poster.calls != 1 || hub.coordinationEligible() {
		t.Fatalf("started=%+v calls=%d eligible=%t", started, poster.calls, hub.coordinationEligible())
	}
	wantMarker := claudeQualificationMarkerPrefix + started.QualificationRef
	if poster.content != claudeQualificationPrompt(wantMarker) {
		t.Fatalf("qualification content = %q", poster.content)
	}
	completed, handled := hub.consumeQualificationStop(wantMarker, false)
	if !handled || completed.Kind != "qualification-qualified" || completed.Ambiguous || completed.AutoResend || !hub.coordinationEligible() {
		t.Fatalf("handled=%t completed=%+v eligible=%t", handled, completed, hub.coordinationEligible())
	}
	hub.close()
	if hub.coordinationEligible() {
		t.Fatal("helper close inherited qualification")
	}
}

func TestClaudeQualificationForgedMissingOldAndStaleEvidenceWritesZero(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Unix(20_000, 0).UTC()
	base := exactQualificationEvidence(fixture.route, now)
	for _, test := range []struct {
		name   string
		mutate func(*claudeQualificationEvidence)
	}{
		{name: "old version", mutate: func(e *claudeQualificationEvidence) { e.ClaudeCodeVersion = "2.1.261" }},
		{name: "missing public init", mutate: func(e *claudeQualificationEvidence) { e.PublicInitObserved = false }},
		{name: "missing tools", mutate: func(e *claudeQualificationEvidence) { e.Tools = nil }},
		{name: "tool enabled", mutate: func(e *claudeQualificationEvidence) { e.Tools = []string{"SendMessage"} }},
		{name: "foreign session", mutate: func(e *claudeQualificationEvidence) { e.SessionID = "foreign" }},
		{name: "foreign process", mutate: func(e *claudeQualificationEvidence) { e.ProviderProcess.PID++ }},
		{name: "foreign pane", mutate: func(e *claudeQualificationEvidence) { e.PaneUID = "uid:pane-foreign" }},
		{name: "stale generation", mutate: func(e *claudeQualificationEvidence) { e.ActivationGeneration = "generation-old" }},
		{name: "stale helper", mutate: func(e *claudeQualificationEvidence) { e.HelperProcess.PID++ }},
		{name: "stale observation", mutate: func(e *claudeQualificationEvidence) { e.ObservedAt = now.Add(-claudeQualificationEvidenceMaxAge) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := base
			test.mutate(&evidence)
			hub := newClaudeCoordinationHub()
			hub.now = func() time.Time { return now }
			poster := &qualificationPosterRecorder{}
			response := hub.beginQualification(evidence, fixture.route, poster)
			if response.Kind != "qualification-refused" || poster.calls != 0 || hub.coordinationEligible() {
				t.Fatalf("response=%+v calls=%d eligible=%t", response, poster.calls, hub.coordinationEligible())
			}
		})
	}
	t.Run("human turn open", func(t *testing.T) {
		hub := newClaudeCoordinationHub()
		hub.now = func() time.Time { return now }
		hub.userPrompt()
		poster := &qualificationPosterRecorder{}
		response := hub.beginQualification(base, fixture.route, poster)
		if response.Kind != "qualification-refused" || poster.calls != 0 || hub.coordinationEligible() {
			t.Fatalf("response=%+v calls=%d eligible=%t", response, poster.calls, hub.coordinationEligible())
		}
	})
}

func TestClaudeQualificationMismatchConcurrentPromptAndPartialWriteNeverOpen(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Unix(30_000, 0).UTC()
	for _, test := range []struct {
		name    string
		outcome claudeProviderPostOutcome
		err     error
		after   func(*claudeCoordinationHub, claudeCoordinationResponse)
	}{
		{name: "partial write", outcome: claudeProviderPostOutcome{WroteAny: true}, err: errors.New("short")},
		{name: "marker mismatch", outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}, after: func(h *claudeCoordinationHub, _ claudeCoordinationResponse) {
			h.consumeQualificationStop("wrong", false)
		}},
		{name: "recursive stop", outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}, after: func(h *claudeCoordinationHub, started claudeCoordinationResponse) {
			h.consumeQualificationStop(claudeQualificationMarkerPrefix+started.QualificationRef, true)
		}},
		{name: "concurrent prompt", outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}, after: func(h *claudeCoordinationHub, started claudeCoordinationResponse) {
			h.userPrompt()
			h.consumeQualificationStop(claudeQualificationMarkerPrefix+started.QualificationRef, false)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newClaudeCoordinationHub()
			hub.now = func() time.Time { return now }
			poster := &qualificationPosterRecorder{outcome: test.outcome, err: test.err}
			started := hub.beginQualification(exactQualificationEvidence(fixture.route, now), fixture.route, poster)
			if test.after != nil {
				test.after(hub, started)
			}
			if hub.coordinationEligible() {
				t.Fatalf("started=%+v unexpectedly opened eligibility", started)
			}
		})
	}
}

func TestClaudeQualificationDuplicateTimeoutAndHelperExitAreBounded(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Unix(70_000, 0).UTC()
	clock := now
	hub := newClaudeCoordinationHub()
	hub.now = func() time.Time { return clock }
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	evidence := exactQualificationEvidence(fixture.route, now)
	first := hub.beginQualification(evidence, fixture.route, poster)
	duplicate := hub.beginQualification(evidence, fixture.route, poster)
	if first.QualificationRef == "" || duplicate.QualificationRef != first.QualificationRef || poster.calls != 1 {
		t.Fatalf("first=%+v duplicate=%+v writes=%d", first, duplicate, poster.calls)
	}
	clock = now.Add(claudeQualificationStopWindow + time.Second)
	expired := hub.qualificationResponse(first.QualificationRef)
	if expired.Kind != "qualification-failed" || expired.Reason != "qualification-stop-timeout" || !expired.Ambiguous || expired.AutoResend || hub.coordinationEligible() {
		t.Fatalf("expired=%+v eligible=%t", expired, hub.coordinationEligible())
	}
	if response, handled := hub.consumeQualificationStop(claudeQualificationMarkerPrefix+first.QualificationRef, false); handled || response.Kind != "" || hub.coordinationEligible() {
		t.Fatalf("late Stop changed expired qualification: handled=%t response=%+v", handled, response)
	}
	evidence.ObservedAt = clock
	retry := hub.beginQualification(evidence, fixture.route, poster)
	if retry.Kind != "qualification-pending" || retry.QualificationRef == first.QualificationRef || poster.calls != 2 {
		t.Fatalf("retry=%+v writes=%d", retry, poster.calls)
	}
	hub.close()
	closed := hub.qualificationResponse(retry.QualificationRef)
	if closed.Kind != "qualification-failed" || closed.Reason != "helper-restart" || hub.coordinationEligible() {
		t.Fatalf("closed=%+v eligible=%t", closed, hub.coordinationEligible())
	}
}

func TestClaudeQualificationInFlightIsSingleWriteAndBoundaryRaceIsAmbiguous(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Unix(75_000, 0).UTC()
	evidence := exactQualificationEvidence(fixture.route, now)

	t.Run("duplicate while writing", func(t *testing.T) {
		hub := newClaudeCoordinationHub()
		hub.now = func() time.Time { return now }
		poster := &qualificationBarrierPoster{started: make(chan struct{}), release: make(chan struct{}),
			outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
		firstDone := make(chan claudeCoordinationResponse, 1)
		go func() { firstDone <- hub.beginQualification(evidence, fixture.route, poster) }()
		<-poster.started
		duplicate := hub.beginQualification(evidence, fixture.route, poster)
		if duplicate.Kind != "qualification-writing" || duplicate.QualificationRef == "" || poster.callCount() != 1 {
			t.Fatalf("duplicate=%+v writes=%d", duplicate, poster.callCount())
		}
		close(poster.release)
		first := <-firstDone
		if first.Kind != "qualification-pending" || first.QualificationRef != duplicate.QualificationRef || poster.callCount() != 1 {
			t.Fatalf("first=%+v duplicate=%+v writes=%d", first, duplicate, poster.callCount())
		}
	})

	for _, test := range []struct {
		name    string
		outcome claudeProviderPostOutcome
		close   func(*claudeCoordinationHub)
	}{
		{name: "human boundary during full write", outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}, close: func(h *claudeCoordinationHub) { h.userPrompt() }},
		{name: "helper exit during full write", outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}, close: func(h *claudeCoordinationHub) { h.close() }},
		{name: "helper exit during partial write", outcome: claudeProviderPostOutcome{WroteAny: true}, close: func(h *claudeCoordinationHub) { h.close() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newClaudeCoordinationHub()
			hub.now = func() time.Time { return now }
			poster := &qualificationBarrierPoster{started: make(chan struct{}), release: make(chan struct{}),
				outcome: test.outcome}
			done := make(chan claudeCoordinationResponse, 1)
			go func() { done <- hub.beginQualification(evidence, fixture.route, poster) }()
			<-poster.started
			test.close(hub)
			close(poster.release)
			response := <-done
			if response.Kind != "qualification-failed" || !response.Ambiguous || response.AutoResend || hub.coordinationEligible() {
				t.Fatalf("response=%+v eligible=%t", response, hub.coordinationEligible())
			}
		})
	}
}

func TestClaudeQualificationLateOldWriteCannotMutateFreshRetry(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Unix(76_000, 0).UTC()
	hub := newClaudeCoordinationHub()
	hub.now = func() time.Time { return now }
	evidence := exactQualificationEvidence(fixture.route, now)
	oldPoster := &qualificationBarrierPoster{started: make(chan struct{}), release: make(chan struct{}),
		outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	oldDone := make(chan claudeCoordinationResponse, 1)
	go func() { oldDone <- hub.beginQualification(evidence, fixture.route, oldPoster) }()
	<-oldPoster.started
	hub.userPrompt()
	// The ordinary Stop for that human turn closes the open human boundary;
	// it cannot revive the already-failed qualification.
	_, _ = hub.consumeQualificationStop("human-turn-response", false)
	freshPoster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	fresh := hub.beginQualification(evidence, fixture.route, freshPoster)
	if fresh.Kind != "qualification-pending" || fresh.QualificationRef == "" {
		t.Fatalf("fresh=%+v", fresh)
	}
	completed, handled := hub.consumeQualificationStop(claudeQualificationMarkerPrefix+fresh.QualificationRef, false)
	if !handled || completed.Kind != "qualification-qualified" {
		t.Fatalf("completed=%+v handled=%t", completed, handled)
	}
	close(oldPoster.release)
	old := <-oldDone
	if old.Kind != "qualification-failed" || old.QualificationRef == fresh.QualificationRef || !old.Ambiguous ||
		!hub.coordinationEligible() {
		t.Fatalf("old=%+v fresh=%+v eligible=%t", old, fresh, hub.coordinationEligible())
	}
}
