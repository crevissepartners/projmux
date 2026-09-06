package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

type traceMessageRouteResolver struct {
	trace  *[]string
	route  coremetadata.AgentRouteRef
	failAt int
	calls  int
}

func (r *traceMessageRouteResolver) Resolve(coremetadata.Registry, coremetadata.Agent) (coremetadata.AgentRouteRef, error) {
	r.calls++
	if r.trace != nil {
		*r.trace = append(*r.trace, "route")
	}
	if r.calls == r.failAt {
		return coremetadata.AgentRouteRef{}, errors.New("stale activation")
	}
	return r.route, nil
}

type traceMessageStore struct {
	trace *[]string
	base  *messagestore.Store
}

func (s *traceMessageStore) Get(ref string) (messagestore.Record, bool, error) {
	*s.trace = append(*s.trace, "store-get")
	return s.base.Get(ref)
}
func (s *traceMessageStore) PutAccepted(envelope coremessage.Envelope, adapter string) (messagestore.Record, bool, error) {
	*s.trace = append(*s.trace, "store-put")
	return s.base.PutAccepted(envelope, adapter)
}
func (s *traceMessageStore) Apply(ref string, event coremessage.Event) (messagestore.Record, bool, error) {
	*s.trace = append(*s.trace, "store-apply")
	return s.base.Apply(ref, event)
}
func (s *traceMessageStore) MarkHandoff(ref string) (messagestore.Record, bool, error) {
	*s.trace = append(*s.trace, "store-handoff")
	return s.base.MarkHandoff(ref)
}
func (s *traceMessageStore) Status(ref string, now time.Time) (messagestore.Record, bool, error) {
	*s.trace = append(*s.trace, "store-status")
	return s.base.Status(ref, now)
}
func (s *traceMessageStore) Claim(route coremessage.Route, now time.Time) (messagestore.Record, bool, error) {
	*s.trace = append(*s.trace, "store-claim")
	return s.base.Claim(route, now)
}

type traceClaudeMessageAdapter struct {
	trace          *[]string
	statusDelivery agentdelivery.Delivery
}

func (a *traceClaudeMessageAdapter) Submit(context.Context, string, coremetadata.AgentRouteRef, coremessage.Envelope) (agentdelivery.Delivery, error) {
	*a.trace = append(*a.trace, "adapter")
	return agentdelivery.Delivery{MessageRef: "message-fixed", State: agentdelivery.StateQueued}, nil
}
func (a *traceClaudeMessageAdapter) Status(context.Context, string, coremetadata.AgentRouteRef, string) (agentdelivery.Delivery, error) {
	*a.trace = append(*a.trace, "adapter-status")
	return a.statusDelivery, nil
}

func replaceClaudeServerWithResponse(t *testing.T, fixture *claudeCoordinationTestFixture, kind string) <-chan error {
	t.Helper()
	fixture.server.Close()
	listener, err := localipc.Listen(claudeCoordinationSocket(fixture.registryPath, fixture.target))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Unix.AcceptUnix()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer conn.Close()
		var request claudeCoordinationRequest
		if readErr := localipc.ReadJSON(conn, &request); readErr != nil {
			done <- readErr
			return
		}
		done <- localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: kind})
	}()
	return done
}

func TestAgentMessageSendGatesBothStaticCellsAndRoutesBeforeStoreOrAdapter(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	h := newSessionRefHarness(t, aiModeClaude)
	active := insideTmux(h.paneUID, "")
	clock := sessionRefObservedAt.Add(time.Minute)
	newCommand := func(trace *[]string, failAt int) *agentCommand {
		return &agentCommand{
			activeTarget: active.lookup,
			messagePaths: agentMessagePaths{registryPath: fixture.registryPath, loadRegistry: func() (coremetadata.Registry, error) {
				return h.registry.Clone(), nil
			}},
			messageStore:  &traceMessageStore{trace: trace, base: messagestore.NewStore(t.TempDir())},
			messageRoute:  &traceMessageRouteResolver{trace: trace, route: fixture.route, failAt: failAt},
			messageClaude: &traceClaudeMessageAdapter{trace: trace},
			messageNow:    func() time.Time { return clock },
			messageNewRef: func(prefix string) string {
				if prefix == "message" {
					return "message-fixed"
				}
				return "conversation-fixed"
			},
		}
	}

	t.Run("second route refusal is mutation zero", func(t *testing.T) {
		var trace []string
		cmd := newCommand(&trace, 2)
		_, _, err := runRoute(t, cmd, "message", "send", "uid:"+h.agentUID, "--", "hello")
		if err == nil || !strings.Contains(err.Error(), "target Agent is not eligible") {
			t.Fatalf("error = %v", err)
		}
		if got := strings.Join(trace, ","); got != "route,route" {
			t.Fatalf("call order = %s", got)
		}
	})

	t.Run("success touches provider after accepted store record", func(t *testing.T) {
		var trace []string
		cmd := newCommand(&trace, 0)
		if _, _, err := runRoute(t, cmd, "message", "send", "uid:"+h.agentUID, "--", "hello"); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(trace, ","); got != "route,route,store-put,adapter" {
			t.Fatalf("call order = %s", got)
		}
	})
}

func TestAgentMessageUnsupportedProviderStopsBeforeRouteAndStore(t *testing.T) {
	h := newSessionRefHarness(t, aiModeAntigravity)
	active := insideTmux(h.paneUID, "")
	var trace []string
	cmd := &agentCommand{
		activeTarget: active.lookup,
		messagePaths: agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return h.registry.Clone(), nil }},
		messageStore: &traceMessageStore{trace: &trace, base: messagestore.NewStore(t.TempDir())},
		messageRoute: &traceMessageRouteResolver{trace: &trace},
		messageNow:   func() time.Time { return sessionRefObservedAt },
		messageNewRef: func(string) string {
			return "must-not-be-called"
		},
	}
	_, _, err := runRoute(t, cmd, "message", "send", "uid:"+h.agentUID, "--", "hello")
	if err == nil || !strings.Contains(err.Error(), "capability message.send unsupported") {
		t.Fatalf("error = %v", err)
	}
	if len(trace) != 0 {
		t.Fatalf("unsupported route touched side effects: %v", trace)
	}
}

func TestAgentMessageSendSameReferenceRetryKeepsBrokerCorrelationAndDoesNotResend(t *testing.T) {
	cmd, store, _ := exactControlCLICommand(t)
	active := insideTmux("pan-alpha-codex", "win-alpha-main")
	var trace []string
	private := &traceMessageStore{trace: &trace, base: messagestore.NewStore(t.TempDir())}
	cmd.activeTarget = active.lookup
	cmd.messagePaths = agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return store.registry.Clone(), nil }}
	cmd.messageStore = private
	cmd.messageRoute = &traceMessageRouteResolver{trace: &trace, route: mustMessageRoute(t, store.registry, "agt-alpha-codex")}
	cmd.messageNow = func() time.Time { return resourceFixtureClock.Add(time.Hour) }
	cmd.messageNewRef = func(prefix string) string {
		t.Fatalf("explicit message ref must not mint %s", prefix)
		return "unused"
	}
	args := []string{"message", "send", "uid:agt-alpha-codex", "--message-ref", "message-retry", "--ttl", "1m", "--", "same payload"}
	first, _, err := runRoute(t, cmd, args...)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := runRoute(t, cmd, args...)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("retry output changed: first=%q second=%q", first, second)
	}
	if strings.Count(strings.Join(trace, ","), "store-put") != 2 {
		t.Fatalf("retry trace = %v", trace)
	}
}

func TestAgentMessageConcurrentSameReferenceUsesOneConversation(t *testing.T) {
	_, registryStore, _ := exactControlCLICommand(t)
	registry := registryStore.registry.Clone()
	route := mustMessageRoute(t, registry, "agt-alpha-codex")
	private := messagestore.NewStore(t.TempDir())
	newCommand := func() *agentCommand {
		return &agentCommand{
			activeTarget: func() (activeTargetObserver, bool) {
				return activeTargetObserver{paneID: "%7", paneUID: func() string { return "pan-alpha-codex" }}, true
			},
			messagePaths: agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return registry.Clone(), nil }},
			messageStore: private,
			messageRoute: &traceMessageRouteResolver{route: route},
			messageNow:   func() time.Time { return resourceFixtureClock.Add(time.Hour) },
			messageNewRef: func(prefix string) string {
				t.Fatalf("explicit message ref must not mint %s", prefix)
				return ""
			},
		}
	}
	const workers = 16
	errs := make(chan error, workers)
	outputs := make(chan string, workers)
	for range workers {
		go func() {
			cmd := newCommand()
			stdout, _, err := runRoute(t, cmd, "message", "send", "uid:agt-alpha-codex", "--message-ref", "message-race", "--ttl", "1m", "--", "same")
			errs <- err
			outputs <- stdout
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if output := <-outputs; output != "message-race\taccepted\n" {
			t.Fatalf("output = %q", output)
		}
	}
	record, found, err := private.Get("message-race")
	if err != nil || !found || record.Envelope.ConversationRef != conversationRefFor("message-race") {
		t.Fatalf("record=%#v found=%t err=%v", record, found, err)
	}
}

func TestAgentMessageReplyReturnsOnlyToOriginalSourceConversation(t *testing.T) {
	_, registryStore, _ := exactControlCLICommand(t)
	registry := registryStore.registry.Clone()
	sourceAgent, _ := registry.Agent("agt-alpha-codex")
	sourcePane, _ := registry.Pane("pan-alpha-codex")
	targetAgent := sourceAgent.Clone()
	targetAgent.Metadata.UID = "agt-target-codex"
	targetAgent.Metadata.Name = "target-codex"
	targetAgent.Status.PaneRef = "pan-target-codex"
	targetAgent.Status.SessionRef.Codex.ThreadID = "thread-target"
	targetPane := sourcePane.Clone()
	targetPane.Metadata.UID = "pan-target-codex"
	targetPane.Metadata.Name = "target-codex-pane"
	targetPane.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: targetAgent.Metadata.UID}
	targetPane.Status.Activation.AgentUID = targetAgent.Metadata.UID
	targetPane.Status.Activation.Generation = "generation-target"
	targetPane.Status.Activation.RuntimeID = "%8"
	targetAuthority := *sourcePane.Status.Activation.Codex.Authority
	targetPane.Status.Activation.Codex = &coremetadata.CodexActivationBinding{ThreadID: "thread-target", TurnID: "turn-target", Authority: &targetAuthority}
	registry.Agents = append(registry.Agents, targetAgent)
	registry.Panes = append(registry.Panes, targetPane)

	private := messagestore.NewStore(t.TempDir())
	activePane := sourcePane.Metadata.UID
	active := func() (activeTargetObserver, bool) {
		return activeTargetObserver{paneID: "%7", paneUID: func() string { return activePane }}, true
	}
	cmd := &agentCommand{
		activeTarget: active,
		messagePaths: agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return registry.Clone(), nil }},
		messageStore: private,
		messageRoute: liveAgentMessageRouteResolver{},
		messageNow:   func() time.Time { return resourceFixtureClock.Add(time.Hour) },
		messageNewRef: func(prefix string) string {
			t.Fatalf("explicit references must not mint %s", prefix)
			return ""
		},
	}
	if _, _, err := runRoute(t, cmd, "message", "send", "uid:"+targetAgent.Metadata.UID,
		"--message-ref", "message-original", "--", "request"); err != nil {
		t.Fatal(err)
	}
	activePane = targetPane.Metadata.UID
	if _, _, err := runRoute(t, cmd, "message", "send", "uid:"+sourceAgent.Metadata.UID,
		"--message-ref", "message-reply", "--reply-to", "message-original", "--", "response"); err != nil {
		t.Fatal(err)
	}
	original, _, _ := private.Get("message-original")
	reply, _, _ := private.Get("message-reply")
	if err := coremessage.ValidateReply(original.Envelope, reply.Envelope); err != nil ||
		reply.Envelope.ConversationRef != original.Envelope.ConversationRef {
		t.Fatalf("reply correlation original=%#v reply=%#v err=%v", original.Envelope, reply.Envelope, err)
	}
	if _, _, err := runRoute(t, cmd, "message", "send", "uid:"+targetAgent.Metadata.UID,
		"--message-ref", "message-foreign-reply", "--reply-to", "message-original", "--", "misrouted"); err == nil ||
		!strings.Contains(err.Error(), "reply route or conversation mismatch") {
		t.Fatalf("misrouted reply error = %v", err)
	}
	if _, found, err := private.Get("message-foreign-reply"); err != nil || found {
		t.Fatalf("misrouted reply stored = %t err=%v", found, err)
	}
}

func TestAgentMessageStatusJSONNeverContainsPayload(t *testing.T) {
	cmd, store, _ := exactControlCLICommand(t)
	route := mustMessageRoute(t, store.registry, "agt-alpha-codex")
	now := resourceFixtureClock.Add(time.Hour)
	private := messagestore.NewStore(t.TempDir())
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-secret", ConversationRef: "conversation-secret",
		Source: publicMessageRoute(route), Target: publicMessageRoute(route), Authority: coremessage.PeerAuthority(),
		Payload: "do-not-expose-payload", AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if _, _, err := private.PutAccepted(envelope, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	cmd.messageStore = private
	cmd.messagePaths = agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return store.registry.Clone(), nil }}
	cmd.messageRoute = liveAgentMessageRouteResolver{}
	cmd.messageNow = func() time.Time { return now }
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"message", "status", envelope.MessageRef, "-o", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), envelope.Payload) || strings.Contains(stdout.String(), `"payload"`) {
		t.Fatalf("status leaked payload: %s", stdout.String())
	}
	var receipt agentMessageReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.MessageRef != envelope.MessageRef {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
}

func TestAgentMessageStatusMarksCodexGenerationChangeStale(t *testing.T) {
	cmd, registryStore, _ := exactControlCLICommand(t)
	registry := registryStore.registry.Clone()
	route := mustMessageRoute(t, registry, "agt-alpha-codex")
	now := resourceFixtureClock.Add(time.Hour)
	private := messagestore.NewStore(t.TempDir())
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-codex-stale", ConversationRef: "conversation-codex-stale",
		Source: publicMessageRoute(route), Target: publicMessageRoute(route), Authority: coremessage.PeerAuthority(),
		Payload: "private", AcceptedAt: now.Add(-time.Minute), Deadline: now.Add(-time.Second)}
	if _, _, err := private.PutAccepted(envelope, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	pane, _ := registry.Pane("pan-alpha-codex")
	pane.Status.Activation.Generation = "generation-restarted"
	cmd.messagePaths = agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return registry.Clone(), nil }}
	cmd.messageStore = private
	cmd.messageRoute = liveAgentMessageRouteResolver{}
	cmd.messageNow = func() time.Time { return now }
	stdout, _, err := runRoute(t, cmd, "message", "status", envelope.MessageRef, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"state":"stale"`) || strings.Contains(stdout, envelope.Payload) {
		t.Fatalf("stale status = %s", stdout)
	}
}

func TestAgentMessageStatusProjectsClaudeReceiptBeforeLocalDeadline(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	h := newSessionRefHarness(t, aiModeClaude)
	if h.agentUID != fixture.route.AgentUID || h.paneUID != fixture.route.PaneUID {
		t.Fatalf("deterministic Claude fixtures diverged: harness=%s/%s route=%s/%s", h.agentUID, h.paneUID, fixture.route.AgentUID, fixture.route.PaneUID)
	}
	now := time.Now().UTC()
	private := messagestore.NewStore(t.TempDir())
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-late-claude-receipt", ConversationRef: "conversation-late-claude-receipt",
		Source: publicMessageRoute(fixture.route), Target: publicMessageRoute(fixture.route), Authority: coremessage.PeerAuthority(),
		Payload: "private", AcceptedAt: now.Add(-time.Minute), Deadline: now.Add(-time.Second)}
	if _, _, err := private.PutAccepted(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	var trace []string
	cmd := &agentCommand{
		messagePaths: agentMessagePaths{registryPath: fixture.registryPath, loadRegistry: func() (coremetadata.Registry, error) { return h.registry.Clone(), nil }},
		messageStore: private,
		messageRoute: &traceMessageRouteResolver{route: fixture.route},
		messageClaude: &traceClaudeMessageAdapter{trace: &trace, statusDelivery: agentdelivery.Delivery{
			MessageRef: envelope.MessageRef, State: agentdelivery.StateDelivered,
		}},
		messageNow: func() time.Time { return now },
	}
	stdout, _, err := runRoute(t, cmd, "message", "status", envelope.MessageRef, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"state":"delivered"`) || strings.Contains(stdout, `"state":"expired"`) ||
		strings.Join(trace, ",") != "adapter-status" {
		t.Fatalf("late Claude receipt = %s trace=%v", stdout, trace)
	}
}

func TestClaudePrivateProjectionPreservesPublicBoundaryAndAmbiguity(t *testing.T) {
	now := resourceFixtureClock.Add(time.Hour)
	store := messagestore.NewStore(t.TempDir())
	cmd := &agentCommand{messageStore: store, messageNow: func() time.Time { return now }}
	newRecord := func(ref string) messagestore.Record {
		envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: "conversation-" + ref,
			Source:    coremessage.Route{AgentUID: "source", PaneUID: "source-pane", ActivationGeneration: "source-generation", Provider: "codex"},
			Target:    coremessage.Route{AgentUID: "target", PaneUID: "target-pane", ActivationGeneration: "target-generation", Provider: "claude"},
			Authority: coremessage.PeerAuthority(), Payload: "coordination", AcceptedAt: now, Deadline: now.Add(time.Minute)}
		record, _, err := store.PutAccepted(envelope, "claude-coordination")
		if err != nil {
			t.Fatal(err)
		}
		return record
	}

	queued := newRecord("message-private-queued")
	queued, err := cmd.projectClaudeDelivery(queued, agentdelivery.Delivery{MessageRef: queued.Envelope.MessageRef, State: agentdelivery.StateQueued}, nil)
	if err != nil || queued.Delivery.State != coremessage.StateAccepted {
		t.Fatalf("queued projection = %#v err=%v", queued, err)
	}
	held := newRecord("message-private-held")
	held, err = cmd.projectClaudeDelivery(held, agentdelivery.Delivery{MessageRef: held.Envelope.MessageRef, State: agentdelivery.StateHeld}, nil)
	if err != nil || held.Delivery.State != coremessage.StateHeld {
		t.Fatalf("held projection = %#v err=%v", held, err)
	}
	handoff := newRecord("message-private-handoff")
	handoff, err = cmd.projectClaudeDelivery(handoff, agentdelivery.Delivery{MessageRef: handoff.Envelope.MessageRef, State: agentdelivery.StateHandoff}, nil)
	if err != nil || handoff.Delivery.State != coremessage.StateAccepted || !handoff.HandoffObserved {
		t.Fatalf("handoff projection = %#v err=%v", handoff, err)
	}
	failed, err := cmd.projectClaudeDelivery(handoff, agentdelivery.Delivery{MessageRef: handoff.Envelope.MessageRef,
		State: agentdelivery.StateFailed, Reason: "malformed-known-failure", Ambiguous: false}, nil)
	if err != nil || failed.Delivery.State != coremessage.StateFailed || !failed.Delivery.OutcomeUnknown ||
		failed.Delivery.Reason != "provider-handoff-outcome-unknown" {
		t.Fatalf("ambiguous projection = %#v err=%v", failed, err)
	}
	postHandoffStale := newRecord("message-private-stale-after-handoff")
	postHandoffStale, _, err = store.MarkHandoff(postHandoffStale.Envelope.MessageRef)
	if err != nil {
		t.Fatal(err)
	}
	postHandoffStale, err = cmd.projectClaudeDelivery(postHandoffStale, agentdelivery.Delivery{
		MessageRef: postHandoffStale.Envelope.MessageRef, State: agentdelivery.StateStale, Reason: "helper-replaced",
	}, nil)
	if err != nil || postHandoffStale.Delivery.State != coremessage.StateFailed || !postHandoffStale.Delivery.OutcomeUnknown {
		t.Fatalf("post-handoff stale projection = %#v err=%v", postHandoffStale, err)
	}
	for _, state := range []agentdelivery.State{agentdelivery.StateDelivered, agentdelivery.StateRefused} {
		foreign := newRecord("message-private-foreign-" + string(state))
		unchanged, projectErr := cmd.projectClaudeDelivery(foreign, agentdelivery.Delivery{
			MessageRef: "message-other", State: state, Reason: "foreign-private-event",
		}, nil)
		if projectErr != nil || unchanged.Delivery.State != coremessage.StateAccepted {
			t.Fatalf("foreign %s projection = %#v err=%v", state, unchanged, projectErr)
		}
		persisted, found, getErr := store.Get(foreign.Envelope.MessageRef)
		if getErr != nil || !found || persisted.Delivery.State != coremessage.StateAccepted {
			t.Fatalf("foreign %s mutated store: %#v found=%v err=%v", state, persisted, found, getErr)
		}
	}
}

func TestLiveClaudeAdapterMapsTerminalResponseKindsWithoutDelivery(t *testing.T) {
	for _, test := range []struct {
		kind string
		want agentdelivery.State
	}{
		{kind: "refused", want: agentdelivery.StateRefused},
		{kind: "stale", want: agentdelivery.StateStale},
	} {
		for _, operation := range []string{"submit", "status"} {
			t.Run(test.kind+"/"+operation, func(t *testing.T) {
				fixture := newClaudeCoordinationTestFixture(t)
				done := replaceClaudeServerWithResponse(t, fixture, test.kind)
				now := time.Now().UTC()
				envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-kind-" + test.kind + "-" + operation,
					ConversationRef: "conversation-kind-" + test.kind + "-" + operation,
					Source:          publicMessageRoute(fixture.route), Target: publicMessageRoute(fixture.route), Authority: coremessage.PeerAuthority(),
					Payload: "peer text", AcceptedAt: now, Deadline: now.Add(time.Minute)}
				adapter := liveAgentMessageClaudeAdapter{}
				var delivery agentdelivery.Delivery
				var err error
				if operation == "submit" {
					delivery, err = adapter.Submit(context.Background(), fixture.registryPath, fixture.route, envelope)
				} else {
					delivery, err = adapter.Status(context.Background(), fixture.registryPath, fixture.route, envelope.MessageRef)
				}
				if err != nil || delivery.MessageRef != envelope.MessageRef || delivery.State != test.want {
					t.Fatalf("%s %s delivery = %#v err=%v", test.kind, operation, delivery, err)
				}
				if serverErr := <-done; serverErr != nil {
					t.Fatal(serverErr)
				}
			})
		}
	}
}

func TestLiveClaudeAdapterRefusesPublicValidEnvelopeThatExceedsPrivateFrame(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-control-heavy", ConversationRef: "conversation-control-heavy",
		Source: publicMessageRoute(fixture.route), Target: publicMessageRoute(fixture.route), Authority: coremessage.PeerAuthority(),
		Payload: strings.Repeat("\x1b", coremessage.MaxPayloadBytes), AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("control-heavy public envelope should remain valid: %v", err)
	}
	delivery, err := (liveAgentMessageClaudeAdapter{}).Submit(context.Background(), fixture.registryPath+"-missing", fixture.route, envelope)
	if err != nil || delivery.MessageRef != envelope.MessageRef || delivery.State != agentdelivery.StateRefused ||
		delivery.Reason != "claude-private-frame-unsupported" {
		t.Fatalf("private-frame refusal = %#v err=%v", delivery, err)
	}
}

func TestAgentMessageWaitClaimsOnlyCurrentExactActivation(t *testing.T) {
	cmd, registryStore, _ := exactControlCLICommand(t)
	registry := registryStore.registry.Clone()
	route := mustMessageRoute(t, registry, "agt-alpha-codex")
	now := resourceFixtureClock.Add(time.Hour)
	private := messagestore.NewStore(t.TempDir())
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-claim", ConversationRef: "conversation-claim",
		Source: publicMessageRoute(route), Target: publicMessageRoute(route), Authority: coremessage.PeerAuthority(),
		Payload: "full envelope for target", AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if _, _, err := private.PutAccepted(envelope, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	active := insideTmux("pan-alpha-codex", "win-alpha-main")
	cmd.activeTarget = active.lookup
	cmd.messagePaths = agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return registry.Clone(), nil }}
	cmd.messageStore = private
	cmd.messageRoute = &traceMessageRouteResolver{route: route}
	cmd.messageNow = func() time.Time { return now }
	stdout, _, err := runRoute(t, cmd, "message", "wait", "uid:agt-alpha-codex", "--timeout", "0", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, envelope.Payload) || !strings.Contains(stdout, `"state":"delivered"`) ||
		!strings.Contains(stdout, `"reason":"target-self-claim"`) {
		t.Fatalf("claim output = %s", stdout)
	}
	if !strings.Contains(stdout, `"kind":"peer"`) || !strings.Contains(stdout, `"trust":"untrusted"`) ||
		!strings.Contains(stdout, `"permission":"coordination-only"`) {
		t.Fatalf("claim lost peer provenance: %s", stdout)
	}
}

func TestAgentMessageClaimDefaultOutputEscapesTerminalControlsAndKeepsEnvelope(t *testing.T) {
	now := resourceFixtureClock.Add(time.Hour)
	payload := "peer text\x1b]52;c;Zm9v\a\nnext line"
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-control", ConversationRef: "conversation-control",
		Source:    coremessage.Route{AgentUID: "source", PaneUID: "source-pane", ActivationGeneration: "source-generation", Provider: "claude"},
		Target:    coremessage.Route{AgentUID: "target", PaneUID: "target-pane", ActivationGeneration: "target-generation", Provider: "codex"},
		Authority: coremessage.PeerAuthority(), Payload: payload, AcceptedAt: now, Deadline: now.Add(time.Minute)}
	delivery, _ := coremessage.Reduce(coremessage.Delivery{}, envelope, coremessage.Event{Kind: coremessage.EventAccept,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: now})
	delivery, _ = coremessage.Reduce(delivery, envelope, coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: now})
	var stdout bytes.Buffer
	if err := writeAgentMessageClaim(&stdout, messagestore.Record{Envelope: envelope, Delivery: delivery, Adapter: "codex-inbox"}, false); err != nil {
		t.Fatal(err)
	}
	for _, value := range stdout.Bytes() {
		if value < 0x20 || value == 0x7f {
			t.Fatalf("claim emitted raw terminal control byte %#x: %q", value, stdout.Bytes())
		}
	}
	var got struct {
		Envelope coremessage.Envelope `json:"envelope"`
		Delivery coremessage.Delivery `json:"delivery"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Envelope.Payload != payload || got.Envelope.Authority != coremessage.PeerAuthority() || got.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("safe claim lost envelope: %#v", got)
	}
}

func TestAgentMessageWaitDetectsGenerationChangeBeforeClaim(t *testing.T) {
	cmd, registryStore, _ := exactControlCLICommand(t)
	registry := registryStore.registry.Clone()
	now := resourceFixtureClock.Add(time.Hour)
	active := insideTmux("pan-alpha-codex", "win-alpha-main")
	cmd.activeTarget = active.lookup
	cmd.messagePaths = agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return registry.Clone(), nil }}
	cmd.messageStore = messagestore.NewStore(t.TempDir())
	cmd.messageRoute = liveAgentMessageRouteResolver{}
	cmd.messageNow = func() time.Time { return now }
	cmd.messageSleep = func(context.Context, time.Duration) error {
		pane, _ := registry.Pane("pan-alpha-codex")
		pane.Status.Activation.Generation = "generation-replaced"
		now = now.Add(50 * time.Millisecond)
		return nil
	}
	_, _, err := runRoute(t, cmd, "message", "wait", "--timeout", "1s")
	if err == nil || !strings.Contains(err.Error(), "activation is stale") {
		t.Fatalf("generation change error = %v", err)
	}
}

func TestClaudeBrokerEnvelopeAtPublicPayloadBoundFitsPrivateFrame(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	publicRoute := publicMessageRoute(fixture.route)
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-frame-bound", ConversationRef: "conversation-frame-bound",
		Source: publicRoute, Target: publicRoute, Authority: coremessage.PeerAuthority(),
		Payload: strings.Repeat("x", coremessage.MaxPayloadBytes), AcceptedAt: now, Deadline: now.Add(time.Minute)}
	private := claudeCoordinationEnvelope{Version: claudeCoordinationVersion, MessageRef: envelope.MessageRef, Target: fixture.target,
		Source:   claudeCoordinationSource{Kind: "peer", Trust: "untrusted", Authority: "coordination-only"},
		Deadline: envelope.Deadline, BrokerEnvelope: &envelope}
	if !private.valid(now, fixture.route) {
		t.Fatal("maximum public payload does not fit the Claude private frame")
	}
}

func TestAgentMessageBrokerHasNoCodexOrUserTurnWritePath(t *testing.T) {
	source, err := os.ReadFile("agent_message.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"turn/start", "turn/steer", "thread/inject_items", "send-keys", "sendkeys"} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Errorf("broker source contains forbidden write path %q", forbidden)
		}
	}
}

func TestAgentWaitRegistryIdleReadyTimeoutAndStale(t *testing.T) {
	h := newSessionRefHarness(t, aiModeCodex)
	activeTime := sessionRefObservedAt.Add(time.Minute)
	setInteraction := func(kind coremetadata.AgentInteractionKind) {
		agent, _ := h.registry.Agent(h.agentUID)
		agent.Status.Interaction = coremetadata.AgentInteraction{Kind: kind, ObservedAt: activeTime, Source: string(coremetadata.InteractionSourceLifecycle)}
	}
	newCommand := func(now *time.Time) *agentCommand {
		return &agentCommand{
			messagePaths: agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return h.registry.Clone(), nil }},
			messageNow:   func() time.Time { return *now },
			messageSleep: func(_ context.Context, duration time.Duration) error { *now = now.Add(duration); return nil },
		}
	}

	t.Run("ready", func(t *testing.T) {
		setInteraction(coremetadata.InteractionIdle)
		now := activeTime
		stdout, _, err := runRoute(t, newCommand(&now), "wait", "uid:"+h.agentUID, "--timeout", "0", "-o", "json")
		if err != nil || !strings.Contains(stdout, `"kind":"idle"`) {
			t.Fatalf("ready stdout=%q err=%v", stdout, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		setInteraction(coremetadata.InteractionInProgress)
		now := activeTime
		_, _, err := runRoute(t, newCommand(&now), "wait", "uid:"+h.agentUID, "--timeout", "100ms")
		if err == nil || !strings.Contains(err.Error(), "timed out waiting for idle") {
			t.Fatalf("timeout error=%v", err)
		}
	})

	t.Run("stale generation", func(t *testing.T) {
		setInteraction(coremetadata.InteractionInProgress)
		pane, _ := h.registry.Pane(h.paneUID)
		pane.Status.Activation.Generation = ""
		now := activeTime
		_, _, err := runRoute(t, newCommand(&now), "wait", "uid:"+h.agentUID, "--timeout", "1s")
		if err == nil || !strings.Contains(err.Error(), "activation is stale") {
			t.Fatalf("stale error=%v", err)
		}
	})
}

func mustMessageRoute(t *testing.T, registry coremetadata.Registry, agentUID string) coremetadata.AgentRouteRef {
	t.Helper()
	route, reason := coremetadata.ResolveAgentRoute(registry, agentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	return route
}
