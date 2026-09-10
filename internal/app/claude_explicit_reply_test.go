package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Keep the durable reply implementation real while replacing only the
// fixture's provider liveness observation. This does not bypass the helper's
// exact source route or process-descendant gate.
type durableReplyTestBroker struct {
	store   *messagestore.Store
	current bool
}

func (b *durableReplyTestBroker) Current(coremessage.Envelope) bool { return b.current }
func (b *durableReplyTestBroker) MarkHandoff(e coremessage.Envelope) error {
	_, _, err := b.store.MarkHandoff(e.MessageRef)
	return err
}
func (b *durableReplyTestBroker) MarkDelivered(e coremessage.Envelope, now time.Time) error {
	_, _, err := b.store.Apply(e.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: e.MessageRef,
		ConversationRef: e.ConversationRef, Target: e.Target, ObservedAt: now, Reason: "provider-pipe-full-frame"})
	return err
}
func (b *durableReplyTestBroker) CommitReply(original, reply coremessage.Envelope) (bool, error) {
	_, created, err := b.store.PutReply(original.MessageRef, reply.MessageRef, reply.Payload, reply.Source, reply.Target,
		reply.AcceptedAt, reply.Deadline)
	return created, err
}
func (b *durableReplyTestBroker) ReplyStatus(ref string) (messagestore.Record, bool, error) {
	return b.store.Reply(ref)
}

type replyFrameWriter struct {
	mode   string
	writes int
	frames [][]byte
}

func (w *replyFrameWriter) Write(frame []byte) (int, error) {
	w.writes++
	switch w.mode {
	case "zero":
		return 0, errors.New("private write cause")
	case "partial":
		return 2, errors.New("private write cause")
	case "unknown":
		return -1, errors.New("private write cause")
	}
	w.frames = append(w.frames, append([]byte(nil), frame...))
	return len(frame), nil
}

type replyTestPoster struct{ writer *replyFrameWriter }

func (p replyTestPoster) Post(content string, current func() bool) (claudeProviderPostOutcome, error) {
	if !current() {
		return claudeProviderPostOutcome{Reason: "provider-prewrite-refused"}, errors.New("stale")
	}
	frame, err := buildClaudeProviderPushFrame("private-fixture-token", content)
	if err != nil {
		return claudeProviderPostOutcome{Reason: err.Error()}, err
	}
	return writeClaudeProviderPushFrame(p.writer, frame), nil
}

func explicitReplyCommandFixture(t *testing.T) (*claudeCoordinationTestFixture, *agentCommand, *durableReplyTestBroker, *replyFrameWriter, coremessage.Envelope) {
	t.Helper()
	f := newClaudeCoordinationTestFixture(t)
	store := messagestore.NewStore(t.TempDir())
	broker := &durableReplyTestBroker{store: store, current: true}
	writer := &replyFrameWriter{}
	f.server.broker, f.server.poster = broker, replyTestPoster{writer}
	original := *dialogueForRoute("original-request", f.route, time.Now().UTC()).BrokerEnvelope
	original.Source = original.Target // A homogeneous reply exercises the provider-write boundary.
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	adapter := liveAgentMessageClaudeAdapter{}
	if got, err := adapter.Submit(context.Background(), f.registryPath, f.route, original); err != nil || got.State != agentdelivery.StateDelivered {
		t.Fatalf("original not delivered: %+v %v", got, err)
	}
	command := &agentCommand{messageStore: store, messageClaude: adapter,
		messagePaths: agentMessagePaths{registryPath: f.registryPath, loadRegistry: intmetadata.NewStore(f.registryPath).LoadReadOnly},
		messageRoute: &traceMessageRouteResolver{route: f.route}}
	return f, command, broker, writer, original
}

func runExplicitFixtureCommand(t *testing.T, command *agentCommand, original coremessage.Envelope, ref, payload string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := command.runMessageSend([]string{"uid:" + original.Source.AgentUID, "--source", "uid:" + original.Target.AgentUID,
		"--reply-to", original.MessageRef, "--message-ref", ref, "--", payload}, &stdout, &stderr)
	return stdout.String() + stderr.String(), err
}

func TestClaudeExplicitReplyKnownZeroManualRetryPublicReceiptsAndReload(t *testing.T) {
	f, command, broker, writer, original := explicitReplyCommandFixture(t)
	writer.mode = "zero"
	output, err := runExplicitFixtureCommand(t, command, original, "reply-zero", "initial answer")
	if err == nil || !strings.Contains(err.Error(), "previousRef=reply-zero") || !strings.Contains(err.Error(), "provider-write-zero") ||
		!strings.Contains(output, "new --message-ref") || writer.writes != 2 {
		t.Fatalf("zero receipt: %s %v writes=%d", output, err, writer.writes)
	}
	failed, _, _ := broker.store.Get("reply-zero")
	command.messageStore = messagestore.NewStoreAt(broker.store.Path())
	broker.store = messagestore.NewStoreAt(broker.store.Path())
	writer.mode = "full"
	if _, err := runExplicitFixtureCommand(t, command, original, "reply-zero", "initial answer"); err == nil || writer.writes != 2 {
		t.Fatal("same ref resent the zero-write attempt")
	}
	if _, err := runExplicitFixtureCommand(t, command, original, "reply-zero", "changed answer"); err == nil || !strings.Contains(err.Error(), "provider-write-zero") || writer.writes != 2 {
		t.Fatal("same ref changed immutable payload or lost previous cause")
	}
	if output, err := runExplicitFixtureCommand(t, command, original, "reply-manual", "corrected answer"); err != nil || !strings.Contains(output, "delivered") || writer.writes != 3 {
		t.Fatalf("manual retry: %s %v writes=%d", output, err, writer.writes)
	}
	delivered, _, _ := broker.store.Get("reply-manual")
	if delivered.Envelope.ReplyTo != original.MessageRef || delivered.Envelope.ConversationRef != original.ConversationRef ||
		delivered.Envelope.Source != original.Target || delivered.Envelope.Target != original.Source || delivered.Envelope.Authority != coremessage.PeerAuthority() ||
		delivered.Envelope.Deadline.After(original.Deadline) {
		t.Fatal("retry changed correlation, route, deadline or authority")
	}
	// Forget every in-memory reply ref; the reloaded store still owns admission.
	f.server.hub.mu.Lock()
	f.server.hub.messages[original.MessageRef].replyRef = ""
	f.server.hub.mu.Unlock()
	if _, err := runExplicitFixtureCommand(t, command, original, "reply-manual", "corrected answer"); err != nil || writer.writes != 3 {
		t.Fatal("same delivered ref dispatched after reload")
	}
	if _, err := runExplicitFixtureCommand(t, command, original, "reply-duplicate", "corrected answer"); err == nil ||
		!strings.Contains(err.Error(), "previousRef=reply-manual") || !strings.Contains(err.Error(), "provider-pipe-full-frame") || !strings.Contains(err.Error(), "do not resend") || writer.writes != 3 {
		t.Fatalf("different delivered ref refusal: %v writes=%d", err, writer.writes)
	}
	for _, want := range []messagestore.Record{failed, delivered} {
		got, found, err := broker.store.Get(want.Envelope.MessageRef)
		if err != nil || !found || got != want {
			t.Fatal("retry replaced immutable attempt")
		}
		var status bytes.Buffer
		if err := command.runMessageStatus([]string{want.Envelope.MessageRef, "-o", "json"}, &status, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		var receipt agentMessageReceipt
		if json.Unmarshal(status.Bytes(), &receipt) != nil || receipt.Delivery != want.Delivery {
			t.Fatal("public status lost durable cause")
		}
	}
	if got, _, _ := broker.store.Get(original.MessageRef); got.Envelope != original {
		t.Fatal("retry altered original")
	}
	if len(writer.frames) != 2 || !bytes.Contains(writer.frames[1], []byte("corrected answer")) || !bytes.Contains(writer.frames[1], []byte("coordination-only")) {
		t.Fatal("successful retry did not preserve peer framing")
	}
}

func TestClaudeExplicitReplyPartialUnknownExpiredAndStaleNeverDuplicate(t *testing.T) {
	for _, mode := range []string{"partial", "unknown", "expired", "expired at CLI", "stale", "stale source resolution", "stale target resolution"} {
		t.Run(mode, func(t *testing.T) {
			f, command, broker, writer, original := explicitReplyCommandFixture(t)
			writer.mode = mode
			if mode != "partial" && mode != "unknown" {
				writer.mode = "zero"
			}
			if _, err := runExplicitFixtureCommand(t, command, original, "previous", "answer"); err == nil {
				t.Fatal("expected failed first write")
			}
			previous, _, _ := broker.store.Get("previous")
			broker.store = messagestore.NewStoreAt(broker.store.Path())
			command.messageStore = broker.store
			if mode == "expired" {
				f.server.hub.now = func() time.Time { return original.Deadline }
			}
			if mode == "expired at CLI" {
				command.messageNow = func() time.Time { return original.Deadline }
			}
			if mode == "stale" {
				broker.current = false
			}
			writer.mode = "full"
			for _, ref := range []string{"previous", "manual"} {
				if mode == "stale source resolution" {
					command.messageRoute = &traceMessageRouteResolver{route: f.route, failAt: 1}
				}
				if mode == "stale target resolution" {
					command.messageRoute = &traceMessageRouteResolver{route: f.route, failAt: 2}
				}
				_, err := runExplicitFixtureCommand(t, command, original, ref, "answer")
				if err == nil || !strings.Contains(err.Error(), "previousRef=previous") || !strings.Contains(err.Error(), previous.Delivery.Reason) ||
					!strings.Contains(err.Error(), "do not resend") || writer.writes != 2 {
					t.Fatalf("unsafe %s retry: %v writes=%d", mode, err, writer.writes)
				}
			}
		})
	}
}

func TestClaudeExplicitReplyAcknowledgementRequiresFreshDispatchProof(t *testing.T) {
	for _, response := range []claudeCoordinationResponse{
		{Version: claudeCoordinationVersion, Kind: "reply-accepted", ReplyRef: "reply-test"}, // Older helper, no creation proof.
		{Version: claudeCoordinationVersion, Kind: "reply-replayed", ReplyRef: "reply-test", ReplyCreated: true},
		{Version: claudeCoordinationVersion, Kind: "reply-accepted", ReplyRef: "other", ReplyCreated: true},
		{Version: claudeCoordinationVersion, Kind: "reply-accepted", ReplyRef: "reply-test", ReplyCreated: true, AutoResend: true},
	} {
		t.Run(response.Kind+response.ReplyRef, func(t *testing.T) {
			fixture := newClaudeCoordinationTestFixture(t)
			done := replaceClaudeServerWithCoordinationResponse(t, fixture, response)
			reply := explicitTestReply(*dialogueForRoute("original", fixture.route, time.Now().UTC()).BrokerEnvelope, "answer")
			reply.MessageRef = "reply-test"
			if _, created, err := (liveAgentMessageClaudeAdapter{}).ExplicitReply(context.Background(), fixture.registryPath, fixture.route, reply); err == nil || created || !strings.Contains(err.Error(), "replyRef=reply-test") || !strings.Contains(err.Error(), "do not resend") {
				t.Fatal("invalid or legacy acknowledgement granted a dispatch")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaudeExplicitReplyStoreRefusalsKeepCauseAndUnknownReservation(t *testing.T) {
	for _, tc := range []struct {
		err     error
		reason  string
		unknown bool
	}{
		{messagestore.ErrBusy, "broker-reply-store-busy", false},
		{messagestore.ErrCapacity, "broker-reply-store-capacity", false},
		{messagestore.ErrNotFound, "broker-reply-original-not-found", false},
		{messagestore.ErrMalformedStore, "broker-reply-store-malformed", false},
		{errors.New("private filesystem failure"), "broker-reply-outcome-unknown", true},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			fixture := newClaudeCoordinationTestFixture(t)
			now := time.Now().UTC()
			hub := qualifiedPushHub(now)
			broker := &failingClaudeDialogueBroker{replyErr: tc.err}
			original := dialogueForRoute("original", fixture.route, now)
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			hub.submitPush(original, broker, poster)
			reply := explicitTestReply(*original.BrokerEnvelope, "answer")
			got := hub.commitExplicitReply(reply, fixture.route, broker)
			if got.Kind != "reply-refused" || got.Reason != tc.reason || hub.messages[original.MessageRef].replyReserved != tc.unknown {
				t.Fatalf("refusal lost no-write/unknown cause: %+v", got)
			}
			broker.replyErr = nil
			retried := hub.commitExplicitReply(reply, fixture.route, broker)
			if tc.unknown {
				if retried.Reason != "broker-reply-outcome-unknown" || retried.ReplyRef != reply.MessageRef || broker.replies != 1 {
					t.Fatal("ambiguous persistence retried")
				}
			} else if retried.Kind != "reply-accepted" || !retried.ReplyCreated {
				t.Fatal("pre-write store refusal permanently reserved original")
			}
		})
	}
}

func TestClaudeExplicitReplyGuardRetryRetainsQualificationAndRouteFences(t *testing.T) {
	f, command, broker, writer, original := explicitReplyCommandFixture(t)
	writer.mode = "zero"
	_, _ = runExplicitFixtureCommand(t, command, original, "failed", "answer")
	argv := []string{"/owned/projmux", "agent", "message", "send", "uid:" + original.Source.AgentUID, "--reply-to", original.MessageRef, "--", "corrected"}
	hub := f.server.hub
	if hub.permitsExplicitTool(argv, f.route, broker) {
		t.Fatal("retry relaxed qualification")
	}
	hub.qualifiedVersion = claudeFrozenFrameProviderVersion
	if !hub.permitsExplicitTool(argv, f.route, broker) {
		t.Fatal("qualified known-zero retry permanently consumed guard")
	}
	broker.current = false
	if hub.permitsExplicitTool(argv, f.route, broker) {
		t.Fatal("retry relaxed exact route")
	}
	broker.current = true
	writer.mode = "full"
	if _, err := runExplicitFixtureCommand(t, command, original, "manual", "corrected"); err != nil {
		t.Fatal(err)
	}
	if hub.permitsExplicitTool(argv, f.route, broker) {
		t.Fatal("delivered reply reopened guarded execution")
	}
}
