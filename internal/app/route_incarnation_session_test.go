package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Readers accept exactly two incarnations for the current route: the full
// digest writers emit today and the session-scoped value writers switch to
// later. Every reader below is driven by the same four values.

const arbitraryRouteIncarnation = "route-000000000000000000000000000000000000"

type incarnationCase struct {
	name   string
	value  string
	accept bool
}

// incarnationCases lists, for the fixture's current route, both accepted
// values and two values every reader must refuse.
func incarnationCases(t *testing.T, f *claudeCoordinationTestFixture) []incarnationCase {
	t.Helper()
	other := changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.SessionID = "other-session" })
	if other.SessionIncarnation() == "" || other.SessionIncarnation() == f.route.SessionIncarnation() {
		t.Fatal("other-session fixture has no distinct session value")
	}
	return []incarnationCase{
		{"full digest", f.route.Incarnation(), true},
		{"session value", f.route.SessionIncarnation(), true},
		{"session value of another SessionID", other.SessionIncarnation(), false},
		{"arbitrary value", arbitraryRouteIncarnation, false},
	}
}

// changedClaudeRoute resolves the fixture Agent after mutate replaced part of
// its registered Claude authority. With persist the fixture Registry file is
// rewritten, so live readers observe the replacement too.
func changedClaudeRoute(t *testing.T, f *claudeCoordinationTestFixture, persist bool,
	mutate func(*coremetadata.ClaudeAuthorityRef),
) coremetadata.AgentRouteRef {
	t.Helper()
	change := func(reg *coremetadata.Registry) error {
		pane, ok := reg.Pane(f.route.PaneUID)
		if !ok || pane.Status.Activation.Claude == nil || pane.Status.Activation.Claude.Registration == nil {
			t.Fatal("fixture Claude registration unavailable")
		}
		binding := pane.Status.Activation.Claude
		registration := *binding.Registration
		mutate(&registration.Authority)
		binding.Registration = &registration
		binding.Process = registration.Authority.Process
		binding.RegistrationGeneration = registration.Authority.RegistrationGeneration
		binding.RegistrationSessionID = registration.Authority.SessionID
		return nil
	}
	store := intmetadata.NewStore(f.registryPath)
	var reg coremetadata.Registry
	var err error
	if persist {
		reg, err = store.Update(change)
	} else {
		reg, err = store.LoadReadOnly()
		if err == nil {
			err = change(&reg)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	route, reason := coremetadata.ResolveAgentRoute(reg, f.route.AgentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	return route
}

func routeWithIncarnation(route coremetadata.AgentRouteRef, incarnation string) coremessage.Route {
	public := publicMessageRoute(route)
	public.Incarnation = incarnation
	return public
}

// R1: the private envelope's broker target incarnation.
func TestClaudeCoordinationEnvelopeReadsSessionIncarnation(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	for _, test := range incarnationCases(t, fixture) {
		t.Run(test.name, func(t *testing.T) {
			envelope := dialogueForRoute("message-r1", fixture.route, now)
			envelope.BrokerEnvelope.Target.Incarnation = test.value
			if got := envelope.valid(now, fixture.route); got != test.accept {
				t.Fatalf("envelope.valid = %t, want %t", got, test.accept)
			}
		})
	}
}

// R2: the live broker's proof that a stored route is still current.
func TestLiveClaudeBrokerCurrentReadsSessionIncarnation(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	broker, err := newLiveClaudeDialogueBroker(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, test := range incarnationCases(t, fixture) {
		t.Run(test.name, func(t *testing.T) {
			envelope := *dialogueForRoute("message-r2", fixture.route, now).BrokerEnvelope
			envelope.Target = routeWithIncarnation(fixture.route, test.value)
			envelope.Source = envelope.Target
			if got := broker.Current(envelope); got != test.accept {
				t.Fatalf("Current = %t, want %t", got, test.accept)
			}
		})
	}
}

// R3: the reply tool gate's check of the original's target.
func TestClaudeReplyToolGateReadsSessionIncarnation(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	for _, test := range incarnationCases(t, fixture) {
		t.Run(test.name, func(t *testing.T) {
			hub := qualifiedPushHub(now)
			broker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			original := dialogueForRoute("message-r3", fixture.route, now)
			original.BrokerEnvelope.Target.Incarnation = test.value
			if got := hub.submitPush(original, broker, poster); got.State != agentdelivery.StateDelivered {
				t.Fatalf("original not delivered: %+v", got)
			}
			argv := []string{"projmux", "agent", "message", "send", "uid:" + original.BrokerEnvelope.Source.AgentUID,
				"--reply-to", original.MessageRef, "--", "answer"}
			if got := hub.permitsExplicitTool(argv, fixture.route, broker); got != test.accept {
				t.Fatalf("permitsExplicitTool = %t, want %t", got, test.accept)
			}
		})
	}
}

// R4: the helper's check of an explicit reply's source.
func TestClaudeExplicitReplyCommitReadsSessionIncarnation(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	for _, test := range incarnationCases(t, fixture) {
		t.Run(test.name, func(t *testing.T) {
			hub := qualifiedPushHub(now)
			broker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			original := dialogueForRoute("message-r4", fixture.route, now)
			original.BrokerEnvelope.Target.Incarnation = test.value
			hub.submitPush(original, broker, poster)
			reply := explicitTestReply(*original.BrokerEnvelope, "answer")
			got := hub.commitExplicitReply(reply, fixture.route, broker)
			if test.accept {
				if got.Kind != "reply-accepted" || !got.ReplyCreated || broker.replies != 1 {
					t.Fatalf("current reply refused: %+v replies=%d", got, broker.replies)
				}
				return
			}
			if got.Kind != "reply-refused" || got.Reason != "invalid-explicit-reply-correlation" || broker.replies != 0 {
				t.Fatalf("foreign reply source accepted: %+v replies=%d", got, broker.replies)
			}
		})
	}
}

type statusOnlyClaudeAdapter struct{ statuses int }

func (a *statusOnlyClaudeAdapter) Submit(context.Context, string, coremetadata.AgentRouteRef, coremessage.Envelope) (agentdelivery.Delivery, error) {
	return agentdelivery.Delivery{}, nil
}

func (a *statusOnlyClaudeAdapter) Status(context.Context, string, coremetadata.AgentRouteRef, string) (agentdelivery.Delivery, error) {
	a.statuses++
	return agentdelivery.Delivery{}, nil
}

// R5: `agent message status` projects a non-terminal record stale only when
// its target is not the current route.
func TestAgentMessageStatusReadsSessionIncarnation(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	for _, test := range incarnationCases(t, fixture) {
		t.Run(test.name, func(t *testing.T) {
			store := messagestore.NewStore(t.TempDir())
			adapter := &statusOnlyClaudeAdapter{}
			command := &agentCommand{messageStore: store, messageClaude: adapter,
				messagePaths: agentMessagePaths{registryPath: fixture.registryPath, loadRegistry: intmetadata.NewStore(fixture.registryPath).LoadReadOnly},
				messageRoute: &traceMessageRouteResolver{route: fixture.route}}
			envelope := *dialogueForRoute("message-r5", fixture.route, now).BrokerEnvelope
			envelope.Target.Incarnation = test.value
			if _, _, err := store.PutAccepted(envelope, "claude-coordination"); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			if err := command.runMessageStatus([]string{envelope.MessageRef, "-o", "json"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			var receipt agentMessageReceipt
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			stale := receipt.Delivery.State == coremessage.StateStale && receipt.Delivery.Reason == "target-activation-stale"
			if stale == test.accept || (adapter.statuses == 1) != test.accept {
				t.Fatalf("status delivery=%+v adapter statuses=%d, want current=%t", receipt.Delivery, adapter.statuses, test.accept)
			}
		})
	}
}

// R6: qualification evidence's route incarnation, through valid and
// validExplicit.
func TestClaudeQualificationEvidenceReadsSessionIncarnation(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	for _, test := range incarnationCases(t, fixture) {
		t.Run(test.name, func(t *testing.T) {
			explicit := exactQualificationEvidence(fixture.route, now)
			explicit.RouteIncarnation = test.value
			if got := explicit.validExplicit(now, fixture.route); got != test.accept {
				t.Fatalf("validExplicit = %t, want %t", got, test.accept)
			}
			plain := explicit
			plain.Tools, plain.ReplyExecutionGate = []string{}, false
			if got := plain.valid(now, fixture.route); got != test.accept {
				t.Fatalf("valid = %t, want %t", got, test.accept)
			}
		})
	}
}

// sessionReplyCommandFixture is explicitReplyCommandFixture with the
// original's routes carrying incarnation. A current original is delivered
// through the live helper; a foreign one is only accepted durably, since the
// CLI refuses its reply before any helper call.
func sessionReplyCommandFixture(t *testing.T, incarnation func(*claudeCoordinationTestFixture) string, deliver bool) (*claudeCoordinationTestFixture, *agentCommand, *durableReplyTestBroker, *replyFrameWriter, coremessage.Envelope) {
	t.Helper()
	f := newClaudeCoordinationTestFixture(t)
	store := messagestore.NewStore(t.TempDir())
	broker := &durableReplyTestBroker{store: store, current: true}
	writer := &replyFrameWriter{}
	f.server.broker, f.server.poster = broker, replyTestPoster{writer}
	original := *dialogueForRoute("original-request", f.route, time.Now().UTC()).BrokerEnvelope
	original.Target.Incarnation = incarnation(f)
	original.Source = original.Target
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	adapter := liveAgentMessageClaudeAdapter{}
	if deliver {
		if got, err := adapter.Submit(context.Background(), f.registryPath, f.route, original); err != nil || got.State != agentdelivery.StateDelivered {
			t.Fatalf("original not delivered: %+v %v", got, err)
		}
	}
	command := &agentCommand{messageStore: store, messageClaude: adapter,
		messagePaths: agentMessagePaths{registryPath: f.registryPath, loadRegistry: intmetadata.NewStore(f.registryPath).LoadReadOnly},
		messageRoute: &traceMessageRouteResolver{route: f.route}}
	return f, command, broker, writer, original
}

// R7: a reply built by `agent message send --reply-to` answers in the shape
// of its original, so the store's exact reply comparison holds for either
// accepted incarnation, and a foreign original is still refused.
func TestAgentMessageReplyMirrorsSessionIncarnation(t *testing.T) {
	for _, test := range []struct {
		name        string
		incarnation func(*claudeCoordinationTestFixture) string
		accept      bool
	}{
		{"full digest", func(f *claudeCoordinationTestFixture) string { return f.route.Incarnation() }, true},
		{"session value", func(f *claudeCoordinationTestFixture) string { return f.route.SessionIncarnation() }, true},
		{"session value of another SessionID", func(f *claudeCoordinationTestFixture) string {
			return changedClaudeRoute(t, f, false, func(a *coremetadata.ClaudeAuthorityRef) { a.SessionID = "other-session" }).SessionIncarnation()
		}, false},
		{"arbitrary value", func(*claudeCoordinationTestFixture) string { return arbitraryRouteIncarnation }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, command, broker, writer, original := sessionReplyCommandFixture(t, test.incarnation, test.accept)
			writes := writer.writes
			output, err := runExplicitFixtureCommand(t, command, original, "reply-r7", "answer")
			reply, found, getErr := broker.store.Get("reply-r7")
			if getErr != nil {
				t.Fatal(getErr)
			}
			if !test.accept {
				if err == nil || !strings.Contains(err.Error(), "invalid-explicit-reply-correlation") || found || writer.writes != writes {
					t.Fatalf("foreign original answered: %s %v found=%t writes=%d", output, err, found, writer.writes-writes)
				}
				return
			}
			if err != nil || strings.Contains(output, "invalid-explicit-reply-correlation") || !found || writer.writes != writes+1 {
				t.Fatalf("reply refused: %s %v found=%t writes=%d", output, err, found, writer.writes-writes)
			}
			if reply.Envelope.Source != original.Target || reply.Envelope.Target != original.Source ||
				reply.Envelope.ReplyTo != original.MessageRef || reply.Delivery.State != coremessage.StateDelivered {
				t.Fatalf("reply did not mirror original routes: %+v", reply.Envelope)
			}
		})
	}
}

func callFixtureSocketDirect(t *testing.T, f *claudeCoordinationTestFixture, request claudeCoordinationRequest) claudeCoordinationResponse {
	t.Helper()
	connection, err := net.DialTimeout("unix", claudeCoordinationSocket(f.registryPath, f.target), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if err := localipc.WriteJSON(connection, request); err != nil {
		t.Fatal(err)
	}
	_ = connection.(*net.UnixConn).CloseWrite()
	var response claudeCoordinationResponse
	if err := localipc.ReadJSON(connection, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

// C-2: a session value does not name a helper. With the SessionID unchanged
// the incarnation predicate accepts it, so each refusal below comes from the
// existing authority fences (the helper's exact claudeCoordinationTarget, the
// evidence's explicit process and lease fields, and the live broker's /proc
// proof), and nothing is written, replied, or qualified.
func TestReplacedClaudeHelperWritesNothingEvenWithSessionIncarnation(t *testing.T) {
	replacements := []struct {
		name   string
		mutate func(*coremetadata.ClaudeAuthorityRef)
		// The live broker proves the Registry route, not a helper: a route
		// whose processes are all live is current to it, so only a replaced
		// process is refused by its /proc proof. A new RegistrationGeneration
		// with live processes is fenced by the helper-bound target above.
		brokerProcessFence bool
	}{
		{"replaced lease process", func(a *coremetadata.ClaudeAuthorityRef) { a.LeaseProcess.Start = "test:replaced-helper" }, true},
		{"different registration generation", func(a *coremetadata.ClaudeAuthorityRef) { a.RegistrationGeneration = "replaced-registration" }, false},
		{"different provider process", func(a *coremetadata.ClaudeAuthorityRef) { a.Process.Start = "test:replaced-provider" }, true},
	}
	for _, replacement := range replacements {
		t.Run(replacement.name, func(t *testing.T) {
			f, _, broker, writer, original := sessionReplyCommandFixture(t,
				func(f *claudeCoordinationTestFixture) string { return f.route.SessionIncarnation() }, true)
			now := time.Now().UTC()
			replaced := changedClaudeRoute(t, f, false, replacement.mutate)
			session := f.route.SessionIncarnation()
			if replaced.SessionIncarnation() != session || !replaced.AcceptsIncarnation(session) || !f.route.AcceptsIncarnation(replaced.SessionIncarnation()) ||
				replaced.Incarnation() == f.route.Incarnation() {
				t.Fatal("fixture does not isolate the authority fence from the incarnation predicate")
			}
			replacedTarget, ok := claudeTargetForRoute(replaced)
			if !ok {
				t.Fatal("replaced target unavailable")
			}

			// Private envelope: built for one helper, read by the other.
			forReplaced := dialogueForRoute("message-c2", replaced, now)
			forReplaced.BrokerEnvelope.Target.Incarnation = session
			forCurrent := dialogueForRoute("message-c2", f.route, now)
			forCurrent.BrokerEnvelope.Target.Incarnation = session
			if forReplaced.valid(now, f.route) || forCurrent.valid(now, replaced) {
				t.Fatal("session incarnation carried an envelope across helpers")
			}

			// Qualification: evidence from the replaced helper, carrying the
			// session value, is refused before any challenge is written.
			hub := newClaudeCoordinationHub()
			hub.now = func() time.Time { return now }
			evidence := exactQualificationEvidence(replaced, now)
			evidence.RouteIncarnation = session
			challenge := qualificationTestEnvelope(f.route, now)
			challenge.BrokerEnvelope.Target.Incarnation = session
			qualificationBroker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			if evidence.validExplicit(now, f.route) {
				t.Fatal("replaced helper evidence valid for current route")
			}
			if got := hub.beginExplicitQualification(evidence, f.route, &challenge, qualificationBroker, poster); got.Kind != "qualification-refused" ||
				poster.calls != 0 || qualificationBroker.handoffs != 0 || hub.qualifiedVersion != "" {
				t.Fatalf("replaced helper qualified: %+v posts=%d handoffs=%d", got, poster.calls, qualificationBroker.handoffs)
			}

			// Explicit reply: a caller holding the replaced route reaches the
			// running helper, which refuses the target before any commit.
			reply := explicitTestReply(original, "answer")
			if reply.Source.Incarnation != session {
				t.Fatal("reply does not carry the session value")
			}
			writes := writer.writes
			refused := callFixtureSocketDirect(t, f, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "explicit-reply",
				Target: replacedTarget, SessionID: replacedTarget.Authority.SessionID, ReplyEnvelope: &reply})
			if refused.Kind != "stale" || writer.writes != writes {
				t.Fatalf("replaced helper route replied: %+v", refused)
			}
			if _, found, err := broker.store.Reply(original.MessageRef); err != nil || found {
				t.Fatalf("replaced helper route stored a reply: found=%t err=%v", found, err)
			}
			// A replaced helper is a new process with a new hub; it has no
			// delivered original to answer, whatever the reply incarnation.
			if got := newClaudeCoordinationHub().commitExplicitReply(reply, replaced, broker); got.Kind != "reply-refused" {
				t.Fatalf("fresh replaced hub committed: %+v", got)
			}
			if _, found, _ := broker.store.Reply(original.MessageRef); found {
				t.Fatal("fresh replaced hub stored a reply")
			}
			// Control: the same session-valued reply on the exact helper target
			// is accepted, so the refusals above are the authority fences.
			if accepted := f.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "explicit-reply",
				Target: f.target, SessionID: f.sessionID, ReplyEnvelope: &reply}); accepted.Kind != "reply-accepted" {
				t.Fatalf("exact helper refused the session-valued reply: %+v", accepted)
			}

			// Live broker: the Registry now names the replacement.
			live, err := newLiveClaudeDialogueBroker(f.registryPath)
			if err != nil {
				t.Fatal(err)
			}
			if !live.Current(original) {
				t.Fatal("live broker refused the current session-valued original")
			}
			changedClaudeRoute(t, f, true, replacement.mutate)
			if replacement.brokerProcessFence && live.Current(original) {
				t.Fatalf("Current accepted a session-valued envelope after %s", replacement.name)
			}
		})
	}
}
