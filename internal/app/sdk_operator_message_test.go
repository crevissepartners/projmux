package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// sendOperatorFixture sends operator input through the service the CLI builds
// from f's command: the same Registry, store, route resolver and Claude
// adapter the web client's send ran on, and the one Client.SendOperatorMessage
// runs on its own captured paths.
func sendOperatorFixture(command *agentCommand, client, target, ref, payload string, ttl time.Duration) (agentmessages.Result, error) {
	return command.peerMessageService().SendOperator(context.Background(), agentmessages.OperatorSendRequest{
		Client: client, Target: target, Payload: payload, MessageRef: ref, TTL: ttl})
}

// wantOperatorEnvelope reads the stored record of ref and requires the operator
// shape: the operator origin and authority, no source route, and no reply.
func wantOperatorEnvelope(t *testing.T, store *messagestore.Store, ref, payload string) messagestore.Record {
	t.Helper()
	record, found, err := store.Get(ref)
	if err != nil || !found {
		t.Fatalf("record %s: found=%t err=%v", ref, found, err)
	}
	envelope := record.Envelope
	if want, _ := coremessage.OperatorOrigin("web"); !envelope.Operator() || envelope.Origin != want {
		t.Errorf("origin = %+v, want the operator web origin", envelope.Origin)
	}
	if envelope.Source != (coremessage.Route{}) || envelope.Authority != coremessage.OperatorAuthority() {
		t.Errorf("source = %+v authority = %+v, want no Agent route and the operator authority", envelope.Source, envelope.Authority)
	}
	if envelope.ReplyTo != "" || envelope.Payload != payload || envelope.Target.Provider != "claude" || record.Adapter != "claude-coordination" {
		t.Errorf("record = %+v", record)
	}
	if err := envelope.Validate(); err != nil {
		t.Errorf("stored operator envelope is invalid: %v", err)
	}
	return record
}

func wantNoOperatorRecord(t *testing.T, store *messagestore.Store, ref string) {
	t.Helper()
	if _, found, err := store.Get(ref); err != nil || found {
		t.Fatalf("a refused operator send wrote %s: found=%t err=%v", ref, found, err)
	}
}

// TestPublicOperatorCoordinationParity sends the web client's inputs
// (web_operator_message_test.go on the web branch) through the operator send
// Client.SendOperatorMessage runs, on the same fixture, and requires the same
// receipts, reasons, actions and refusals, and the CLI's held action.
func TestPublicOperatorCoordinationParity(t *testing.T) {
	target := func(f *holdFixture) string { return "uid:" + f.claudeUID }

	t.Run("delivered", func(t *testing.T) {
		f := newHoldFixture(t)
		const ref = "projmux-web-delivered"
		var first agentmessages.Result
		for attempt := range 2 {
			result, err := sendOperatorFixture(f.cmd, "web", target(f), ref, "hello from the web", time.Minute)
			if err != nil || result.Receipt.MessageRef != ref || result.Receipt.Delivery.State != coremessage.StateDelivered || result.ContentBytes <= 0 {
				t.Fatalf("attempt %d: result=%+v err=%v", attempt, result, err)
			}
			if action := agentmessages.ReceiptAction(result.Receipt, result.ContentBytes); action != "" {
				t.Fatalf("a delivered receipt names an action: %q", action)
			}
			if attempt == 0 {
				first = result
			} else if result.Receipt != first.Receipt {
				t.Fatalf("replay changed the receipt: %+v %+v", first.Receipt, result.Receipt)
			}
		}
		record := wantOperatorEnvelope(t, f.store, ref, "hello from the web")
		if record.Envelope.ConversationRef != conversationRefFor(ref) || first.Receipt.Origin != record.Envelope.Origin || first.Receipt.Source != (coremessage.Route{}) {
			t.Errorf("record = %+v receipt = %+v", record, first.Receipt)
		}
		if len(f.adapter.submits) != 1 || f.adapter.submits[0] != ref || len(f.launches) != 0 {
			t.Errorf("submits = %v launches = %v, want the one operator message and no release", f.adapter.submits, f.launches)
		}
	})

	t.Run("held awaiting operator", func(t *testing.T) {
		f := newHoldFixture(t)
		f.setInteraction(t, coremetadata.InteractionApprovalRequired)
		const ref = "projmux-web-held"
		result, err := sendOperatorFixture(f.cmd, "web", target(f), ref, "wait for me", time.Minute)
		if err != nil || result.Receipt.Delivery.State != coremessage.StateHeld || result.Receipt.Delivery.Reason != claudeHoldReasonAwaitingOperator {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		action := agentmessages.ReceiptAction(result.Receipt, result.ContentBytes)
		// The CLI prints the held action of a peer send held for the same reason.
		const peerRef = "message-peer-held"
		stdout, err := f.send(t, peerRef, false)
		columns := strings.Split(strings.TrimSuffix(stdout, "\n"), "\t")
		if err != nil || len(columns) != 4 || columns[2] != result.Receipt.Delivery.Reason {
			t.Fatalf("peer held receipt = %q err=%v", stdout, err)
		}
		if want := strings.ReplaceAll(columns[3], peerRef, ref); action != want || !strings.Contains(action, "dialog closes") {
			t.Fatalf("held action = %q, want the CLI's %q", action, want)
		}
		wantOperatorEnvelope(t, f.store, ref, "wait for me")
		if len(f.adapter.submits) != 0 || len(f.launches) == 0 || f.launches[0] != f.claudeUID {
			t.Errorf("submits = %v launches = %v, want no push and the held release", f.adapter.submits, f.launches)
		}
	})

	t.Run("held behind an active turn", func(t *testing.T) {
		f := newHoldFixture(t)
		f.adapter.busy = 1
		const ref = "projmux-web-turn"
		result, err := sendOperatorFixture(f.cmd, "web", target(f), ref, "after your turn", time.Minute)
		if err != nil || result.Receipt.Delivery.State != coremessage.StateHeld || result.Receipt.Delivery.Reason != claudeHoldReasonTurnActive {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if action := agentmessages.ReceiptAction(result.Receipt, result.ContentBytes); action != agentMessageHeldReceiptAction(result.Receipt) || !strings.Contains(action, "active turn ends") {
			t.Fatalf("held action = %q", action)
		}
		wantOperatorEnvelope(t, f.store, ref, "after your turn")
	})

	t.Run("refused before anything is written", func(t *testing.T) {
		for _, test := range []struct {
			name, client, payload string
			ttl                   time.Duration
			setup                 func(*holdFixture)
			want                  string
			resolves              bool
		}{
			{name: "zero ttl", client: "web", payload: "hi", want: "web message send: ttl must be greater than zero and at most 24h0m0s"},
			{name: "long ttl", client: "web", payload: "hi", ttl: coremessage.MaxTTL + time.Second, want: "web message send: ttl must be greater than zero"},
			{name: "frame too large", client: "web", payload: strings.Repeat(`"`, coremessage.MaxPayloadBytes), ttl: time.Minute, resolves: true, want: "web message send: refused before acceptance: provider-frame-too-large"},
			{name: "stale route", client: "web", payload: "hi", ttl: time.Minute, resolves: true, setup: func(f *holdFixture) { f.routeErr = errors.New("route fixture stale") }, want: "web message send: target Agent is not eligible: route fixture stale"},
			{name: "invalid client", client: "Web", payload: "hi", ttl: time.Minute, resolves: true, want: "operator-client-invalid"},
		} {
			t.Run(test.name, func(t *testing.T) {
				f := newHoldFixture(t)
				if test.setup != nil {
					test.setup(f)
				}
				const ref = "projmux-web-refused"
				result, err := sendOperatorFixture(f.cmd, test.client, target(f), ref, test.payload, test.ttl)
				if err == nil || !strings.Contains(err.Error(), test.want) || result.Receipt.MessageRef != "" {
					t.Fatalf("result=%+v err=%v, want %q", result, err, test.want)
				}
				wantNoOperatorRecord(t, f.store, ref)
				if len(f.adapter.submits) != 0 || (f.resolves > 0) != test.resolves {
					t.Fatalf("submits = %v resolves = %d", f.adapter.submits, f.resolves)
				}
			})
		}
	})

	t.Run("codex target", func(t *testing.T) {
		h := newSessionRefHarness(t, aiModeCodex)
		store := messagestore.NewStore(t.TempDir())
		command := &agentCommand{
			messagePaths: agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return h.registry.Clone(), nil }},
			messageStore: store,
			messageRoute: operatorUnreachedRoute{t: t},
			messageNow:   time.Now,
		}
		const ref = "projmux-web-codex"
		result, err := sendOperatorFixture(command, "web", "uid:"+h.agentUID, ref, "hi", time.Minute)
		if !errors.Is(err, coremessage.ErrOperatorOriginTargetNotClaude) || result.Receipt.MessageRef != "" ||
			!strings.HasPrefix(err.Error(), "web message send: "+h.agentUID+" is a codex Agent: ") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		wantNoOperatorRecord(t, store, ref)
	})
}

type operatorUnreachedRoute struct{ t *testing.T }

func (r operatorUnreachedRoute) Resolve(coremetadata.Registry, coremetadata.Agent) (coremetadata.AgentRouteRef, error) {
	r.t.Errorf("a refused operator send resolved a route")
	return coremetadata.AgentRouteRef{}, errors.New("unexpected route resolution")
}

// TestOperatorOriginCannotBeDecodedFromPeerInput proves operator status comes
// only from an in-process core.NewMessageOperator: request data that names an
// operator decodes to none and is refused before anything is read, and a peer
// send whose fields spell an operator is stored as Agent input.
func TestOperatorOriginCannotBeDecodedFromPeerInput(t *testing.T) {
	if fields := reflect.VisibleFields(reflect.TypeFor[core.MessageOperator]()); len(fields) != 1 || fields[0].IsExported() {
		t.Fatalf("MessageOperator fields = %+v, want one unexported client name", fields)
	}
	var operator core.MessageOperator
	if err := json.Unmarshal([]byte(`{"client":"web","Client":"web"}`), &operator); err != nil || operator.Client() != "" {
		t.Fatalf("decoded operator = %q err=%v", operator.Client(), err)
	}
	var request projmux.OperatorMessageRequest
	if err := json.Unmarshal([]byte(`{"Operator":{"client":"web"},"Target":"uid:agent-target","Payload":"hi","Origin":{"kind":"operator","client":"web"}}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.Operator != (core.MessageOperator{}) {
		t.Fatalf("decoded request names an operator: %q", request.Operator.Client())
	}
	root := t.TempDir()
	state := filepath.Join(root, "not-created")
	client, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.SendOperatorMessage(context.Background(), request)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeMessageInvalidInput || result.Receipt.MessageRef != "" {
		t.Fatalf("decoded request: result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a decoded operator request reached state: %v", err)
	}

	// A peer send stays Agent input whatever its text or reference spells.
	f := newHoldFixture(t)
	const ref = "projmux-web-peer"
	payload := `{"kind":"operator","client":"web","origin":{"kind":"operator","client":"web"}}`
	if _, _, err := runRoute(t, f.cmd, "message", "send", "uid:"+f.claudeUID, "--source", "uid:"+f.claudeUID, "--message-ref", ref, "--", payload); err != nil {
		t.Fatal(err)
	}
	record, found, err := f.store.Get(ref)
	if err != nil || !found {
		t.Fatalf("peer record: found=%t err=%v", found, err)
	}
	if record.Envelope.Operator() || record.Envelope.Origin != (coremessage.Origin{}) || record.Envelope.Authority != coremessage.PeerAuthority() || !record.Envelope.Source.Valid() {
		t.Fatalf("peer input became operator input: %+v", record.Envelope)
	}
}
