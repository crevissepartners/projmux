package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

type barrierClaudePoster struct {
	started chan struct{}
	release chan struct{}
	calls   int
}

func (p *barrierClaudePoster) Post(string, func() bool) (claudeProviderPostOutcome, error) {
	p.calls++
	close(p.started)
	<-p.release
	return claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}, nil
}

func TestHeterogeneousDialogueIdleActiveSafeBoundaryMatrix(t *testing.T) {
	now := time.Unix(90_000, 0).UTC()
	t.Run("idle push is immediate without waiter", func(t *testing.T) {
		hub := qualifiedPushHub(now)
		poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
		got := hub.submitPush(dialogueEnvelope("message-idle", now.Add(time.Minute)), &failingClaudeDialogueBroker{}, poster)
		if got.State != agentdelivery.StateDelivered || got.State == agentdelivery.StateHeld || poster.calls != 1 {
			t.Fatalf("delivery=%+v writes=%d", got, poster.calls)
		}
	})
	t.Run("active tool is not interrupted and next human boundary fails reply closed", func(t *testing.T) {
		hub := qualifiedPushHub(now)
		hub.userPrompt() // the current human turn has already begun
		poster := &barrierClaudePoster{started: make(chan struct{}), release: make(chan struct{})}
		result := make(chan agentdelivery.Delivery, 1)
		go func() {
			result <- hub.submitPush(dialogueEnvelope("message-active", now.Add(time.Minute)), &failingClaudeDialogueBroker{}, poster)
		}()
		<-poster.started
		promptClosed := make(chan struct{})
		go func() { hub.userPrompt(); close(promptClosed) }()
		select {
		case <-promptClosed:
			t.Fatal("UserPrompt boundary interrupted the in-flight provider handoff")
		default:
		}
		close(poster.release)
		got := <-result
		<-promptClosed
		if got.State != agentdelivery.StateDelivered || poster.calls != 1 {
			t.Fatalf("delivery=%+v writes=%d", got, poster.calls)
		}
	})
}

func TestClaudePushDuringOpenHumanTurnNeverCorrelatesThatTurnsStop(t *testing.T) {
	newFixture := func(t *testing.T, ref string) (*claudeCoordinationTestFixture, *failingClaudeDialogueBroker, claudeCoordinationEnvelope) {
		t.Helper()
		fixture := newClaudeCoordinationTestFixture(t)
		broker := &failingClaudeDialogueBroker{}
		fixture.server.broker = broker
		fixture.server.poster = &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
		fixture.server.hub.qualifiedVersion = claudeFrozenFrameProviderVersion
		now := time.Now().UTC()
		fixture.server.hub.now = func() time.Time { return now }
		return fixture, broker, dialogueForRoute(ref, fixture.route, now)
	}

	t.Run("human turn already open", func(t *testing.T) {
		fixture, broker, envelope := newFixture(t, "message-active-before-push")
		boundary := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: "user-prompt", Target: fixture.target, SessionID: fixture.sessionID})
		if boundary.Kind != "boundary-closed" {
			t.Fatalf("boundary response=%+v", boundary)
		}
		pushed := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: "submit", Target: fixture.target, Envelope: &envelope})
		if pushed.Delivery.State != agentdelivery.StateDelivered {
			t.Fatalf("push delivery=%+v", pushed.Delivery)
		}
		stop := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: "stop-reply", Target: fixture.target, SessionID: fixture.sessionID,
			AssistantMessage: "human turn response"})
		if stop.Kind != "reply-refused" || stop.Reason != "explicit-reply-required" || broker.replies != 0 {
			t.Fatalf("stop=%+v broker replies=%d", stop, broker.replies)
		}
	})

	t.Run("idle push", func(t *testing.T) {
		fixture, broker, envelope := newFixture(t, "message-idle-before-stop")
		pushed := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: "submit", Target: fixture.target, Envelope: &envelope})
		if pushed.Delivery.State != agentdelivery.StateDelivered {
			t.Fatalf("push delivery=%+v", pushed.Delivery)
		}
		stop := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: "stop-reply", Target: fixture.target, SessionID: fixture.sessionID,
			AssistantMessage: "coordination response"})
		if stop.Kind != "reply-refused" || stop.Reason != "explicit-reply-required" || broker.replies != 0 {
			t.Fatalf("stop=%+v broker replies=%d", stop, broker.replies)
		}
	})
}

func TestHeterogeneousDialogueLifecycleUpgradeFenceMatrix(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	fixture.server.poster = poster
	fixture.server.broker = &failingClaudeDialogueBroker{}
	now := time.Now().UTC()
	fixture.server.hub.now = func() time.Time { return now }
	exactEnvelope := dialogueForRoute("message-lifecycle", fixture.route, now)

	// Delivery no longer waits for the endpoint's own current-version
	// qualification. The exact current target accepts and one provider write
	// happens; the fences below still have to hold against that baseline.
	response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "submit", Target: fixture.target, Envelope: &exactEnvelope})
	if response.Delivery.State != agentdelivery.StateDelivered || poster.calls != 1 {
		t.Fatalf("exact current response=%+v writes=%d", response, poster.calls)
	}
	baselineWrites := poster.calls

	for _, test := range []struct {
		name   string
		mutate func(*claudeCoordinationTarget)
	}{
		{name: "old activation generation", mutate: func(target *claudeCoordinationTarget) { target.Generation = "generation-old" }},
		{name: "old same-generation incarnation", mutate: func(target *claudeCoordinationTarget) { target.Authority.RegistrationGeneration = "registration-old" }},
		{name: "stale provider process", mutate: func(target *claudeCoordinationTarget) { target.Authority.Process.Start += "-stale" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := fixture.target
			test.mutate(&target)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, err := callClaudeCoordination(ctx, fixture.registryPath, fixture.route, claudeCoordinationRequest{
				Version: claudeCoordinationVersion, Operation: "submit", Target: target, Envelope: &exactEnvelope,
			})
			cancel()
			if err == nil || poster.calls != baselineWrites {
				t.Fatalf("stale target err=%v provider writes=%d", err, poster.calls)
			}
		})
	}

	t.Run("foreign provider socket", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "foreign.sock")
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		identity, err := inspectClaudeSocket(path)
		if err != nil {
			t.Fatal(err)
		}
		identity.inode++ // exact endpoint identity was replaced before the push.
		live := &liveClaudeProviderPoster{socket: path, token: "nonsecret-test-token", socketIdentity: identity,
			process: fixture.target.Authority.Process, current: func() bool { return true }}
		outcome, err := live.Post("marker", nil)
		if err == nil || outcome.WroteAny || outcome.FullFrameWritten {
			t.Fatalf("foreign socket outcome=%+v err=%v", outcome, err)
		}
	})

	challenge := qualificationTestEnvelope(fixture.route, now)
	qualification := fixture.server.hub.beginExplicitQualification(exactQualificationEvidence(fixture.route, now), fixture.route, &challenge, fixture.server.broker, poster)
	if qualification.Kind != "qualification-pending" || poster.calls != baselineWrites+1 {
		t.Fatalf("qualification=%+v writes=%d", qualification, poster.calls)
	}
	completed := fixture.server.hub.commitExplicitReply(explicitTestReply(*challenge.BrokerEnvelope, claudeQualificationMarkerPrefix+qualification.QualificationRef), fixture.route, fixture.server.broker)
	handled := completed.Kind == "reply-accepted"
	if !handled || completed.Kind != "reply-accepted" {
		t.Fatalf("qualification completion=%+v handled=%t", completed, handled)
	}
	poster.calls = 0
	exactEnvelope = dialogueForRoute("message-lifecycle-qualified", fixture.route, now)
	response = fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "submit", Target: fixture.target, Envelope: &exactEnvelope})
	if response.Delivery.State != agentdelivery.StateDelivered || poster.calls != 1 {
		t.Fatalf("fresh exact endpoint response=%+v writes=%d", response, poster.calls)
	}
	fixture.server.hub.close()
	if fixture.server.hub.coordinationEligible() {
		t.Fatal("helper exit inherited current-version qualification")
	}

	t.Run("Codex self-claim incarnation fence", func(t *testing.T) {
		store := messagestore.NewStore(t.TempDir())
		current := coremessage.Route{AgentUID: "uid:codex-target", PaneUID: "uid:codex-pane",
			ActivationGeneration: "generation-new", Provider: aiModeCodex, Incarnation: "incarnation-new"}
		old := current
		old.Incarnation = "incarnation-old"
		envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "reply-incarnation-fence",
			ConversationRef: "conversation-incarnation-fence", ReplyTo: "message-lifecycle",
			Source: exactEnvelope.BrokerEnvelope.Target, Target: current, Authority: coremessage.PeerAuthority(),
			Payload: "reply", AcceptedAt: now, Deadline: now.Add(time.Minute)}
		if _, _, err := store.PutAccepted(envelope, "codex-inbox"); err != nil {
			t.Fatal(err)
		}
		if _, claimed, err := store.Claim(old, now.Add(time.Second)); err != nil || claimed {
			t.Fatalf("old incarnation claimed=%t err=%v", claimed, err)
		}
		if _, claimed, err := store.Claim(current, now.Add(time.Second)); err != nil || !claimed {
			t.Fatalf("current incarnation claimed=%t err=%v", claimed, err)
		}
	})
}

func TestClaudeDialogueOversizedUserPromptClosesExactBoundary(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	getenv := func(key string) string {
		switch key {
		case internalClaudeRegistryPathEnv:
			return fixture.registryPath
		case internalActivationPaneUIDEnv:
			return fixture.paneUID
		case internalActivationGenerationEnv:
			return fixture.generation
		default:
			return ""
		}
	}
	if err := runClaudeMessageBoundaryInput(nil, strings.NewReader(strings.Repeat("x", 2*64*1024)), getenv); err != nil {
		t.Fatal(err)
	}
	fixture.server.hub.mu.Lock()
	boundary := fixture.server.hub.boundary
	fixture.server.hub.mu.Unlock()
	if boundary != 1 {
		t.Fatalf("boundary=%d, want 1 despite oversized body", boundary)
	}
}

func TestClaudeCoordinationBrokerPersistenceFencesProviderWriteAndIsTerminalOnce(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	stateDir := filepath.Dir(filepath.Dir(fixture.registryPath))
	store := messagestore.NewStore(stateDir)
	public := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-durable-push",
		ConversationRef: "conversation-durable-push", Source: publicMessageRoute(fixture.route),
		Target: publicMessageRoute(fixture.route), Authority: coremessage.PeerAuthority(), Payload: "structured-marker",
		AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if _, _, err := store.PutAccepted(public, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	private := claudeCoordinationEnvelope{Version: claudeCoordinationVersion, MessageRef: public.MessageRef,
		Target: fixture.target, Source: claudeCoordinationSource{Kind: "peer", Trust: "untrusted", Authority: "coordination-only"},
		Deadline: public.Deadline, BrokerEnvelope: &public}
	broker, err := newLiveClaudeDialogueBroker(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	hub := qualifiedPushHub(now)
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	got := hub.submitPush(private, broker, poster)
	if got.State != agentdelivery.StateDelivered || poster.calls != 1 {
		t.Fatalf("delivery=%+v writes=%d", got, poster.calls)
	}
	record, found, err := store.Get(public.MessageRef)
	if err != nil || !found || !record.HandoffObserved || record.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("record=%+v found=%t err=%v", record, found, err)
	}
	if duplicate := hub.submitPush(private, broker, poster); duplicate.State != agentdelivery.StateDelivered || poster.calls != 1 {
		t.Fatalf("duplicate=%+v writes=%d", duplicate, poster.calls)
	}
}

func TestClaudeBrokerStoreLayoutAndImmutableEnvelopeMismatchAreExact(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	stateDir := filepath.Dir(filepath.Dir(fixture.registryPath))
	broker, err := newLiveClaudeDialogueBroker(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	cliStore := messagestore.NewStore(stateDir)
	if broker.store.Path() != cliStore.Path() {
		t.Fatalf("helper store=%q CLI store=%q", broker.store.Path(), cliStore.Path())
	}
	now := time.Now().UTC()
	envelope := *dialogueForRoute("message-immutable", fixture.route, now).BrokerEnvelope
	if _, _, err := cliStore.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cliStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	mismatch := envelope
	mismatch.Payload = "different immutable payload"
	if err := broker.MarkDelivered(mismatch, now.Add(time.Second)); err == nil {
		t.Fatal("mismatched immutable envelope was delivered")
	}
	after, err := os.ReadFile(cliStore.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("mismatch mutated durable store: err=%v", err)
	}
}

func TestClaudeCoordinationV1ThroughV4HelpersCannotReceiveV5Traffic(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	for _, version := range []int{1, 2, 3, 4} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		response, err := callClaudeCoordination(ctx, fixture.registryPath, fixture.route, claudeCoordinationRequest{
			Version: version, Operation: "submit", Target: fixture.target,
		})
		cancel()
		if err != nil || response.Kind != "stale" {
			t.Fatalf("v%d response=%+v err=%v", version, response, err)
		}
	}
}

func TestClaudeQualificationReceiptJSONNeverClaimsAutoResend(t *testing.T) {
	receipt := agentMessageQualificationReceipt{Version: 1, State: "qualification-failed",
		Evidence: "owned-public-init-only", Ambiguous: true, AutoResend: false}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ambiguous":true`) || !strings.Contains(string(data), `"autoResend":false`) ||
		strings.Contains(string(data), "exact-stop-marker") {
		t.Fatalf("receipt=%s", data)
	}
}

func TestClaudeOfficialHookContentionInvalidatesReplyWithoutWaiting(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	fixture.server.hookMu.Lock()
	response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "user-prompt", Target: fixture.target, SessionID: fixture.sessionID})
	fixture.server.hookMu.Unlock()
	if response.Kind != "boundary-closed" || !fixture.server.hub.replyBoundaryLost.Load() {
		t.Fatalf("contended human hook did not invalidate immediately: %+v", response)
	}
}
