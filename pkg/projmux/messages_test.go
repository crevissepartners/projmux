package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	registry "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
)

func TestPeerMessageExplicitInputsAndCancellationReadNothing(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "not-created")
	client, err := NewClient(Options{HomeDir: root, ConfigDir: filepath.Join(root, "cfg"), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "poison")
	t.Setenv("TMUX_PANE", "%999")
	t.Setenv("__PROJMUX_RUNTIME_ANCHOR_PANE", "pane-poison")
	for _, req := range []SendMessageRequest{{Target: "uid:agent-target", Text: "peer"}, {Source: "uid:agent-source", Text: "peer"}, {Source: "uid:agent-source", Target: "uid:agent-target", Text: "peer", TTL: -1}} {
		_, err := client.SendMessage(context.Background(), req)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeMessageInvalidInput {
			t.Fatalf("%+v %v", req, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.SendMessage(ctx, SendMessageRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := client.MessageStatus(ctx, "message-fixed"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-admission state effects: %v", err)
	}
}
func TestPeerMessageTerminalReceiptsPreserveStateUncertaintyAndStore(t *testing.T) {
	for _, row := range []struct {
		kind    message.EventKind
		unknown bool
	}{{message.EventDeliver, false}, {message.EventRefuse, false}, {message.EventExpire, false}, {message.EventStale, false}, {message.EventFail, false}, {message.EventFail, true}} {
		t.Run(string(row.kind)+map[bool]string{true: "-unknown"}[row.unknown], func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			client, err := NewClient(Options{HomeDir: root, ConfigDir: filepath.Join(root, "cfg"), StateDir: state})
			if err != nil {
				t.Fatal(err)
			}
			disk := store.NewStore(state)
			now := time.Now().UTC()
			route := message.Route{AgentUID: "agent-one", PaneUID: "pane-one", ActivationGeneration: "gen-one", Provider: "codex", Incarnation: "route-one"}
			e := message.Envelope{Version: message.Version, MessageRef: "message-fixed", ConversationRef: "conversation-fixed", Source: route, Target: route, Authority: message.PeerAuthority(), Payload: "private-text", AcceptedAt: now, Deadline: now.Add(time.Minute)}
			if _, _, err := disk.PutAccepted(e, "codex-inbox"); err != nil {
				t.Fatal(err)
			}
			record, _, err := disk.Apply(e.MessageRef, message.Event{Kind: row.kind, MessageRef: e.MessageRef, ConversationRef: e.ConversationRef, Target: e.Target, ObservedAt: now, Reason: "fixture-reason", OutcomeUnknown: row.unknown})
			if err != nil {
				t.Fatal(err)
			}
			// Poison the process defaults after construction; the Client must stay on its root.
			t.Setenv("XDG_STATE_HOME", filepath.Join(root, "poison"))
			got, err := client.MessageStatus(context.Background(), e.MessageRef)
			if err != nil {
				t.Fatal(err)
			}
			want := publicMessageReceipt(agentmessages.ReceiptFor(record))
			if got != want {
				t.Fatalf("receipt differs: %+v %+v", got, want)
			}
			got.Delivery.Reason = "caller-mutated"
			again, err := client.MessageStatus(context.Background(), e.MessageRef)
			if err != nil || again != want {
				t.Fatalf("result aliased or changed: %+v %v", again, err)
			}
			after, found, err := disk.Get(e.MessageRef)
			if err != nil || !found || after.Delivery != record.Delivery {
				t.Fatalf("terminal read changed store: %+v %v", after, err)
			}
		})
	}
}

func TestPeerMessageClaudeExecutableRequiredBeforeRouteOrAcceptance(t *testing.T) {
	client, _, uid, paneUID := sdkStopFixture(t)
	storage := registry.NewStore(registry.PathFor(client.paths.StateDir))
	if _, err := storage.Update(func(reg *metadata.Registry) error {
		agent, _ := reg.Agent(uid)
		agent.Spec.Provider = "claude"
		pane, _ := reg.Pane(paneUID)
		pane.Status.ProcessSession.Provider = "claude"
		pane.Status.ProcessSession.SessionID = pane.Status.ProcessSession.ThreadID
		pane.Status.ProcessSession.ThreadID = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(storage.Path())
	if err != nil {
		t.Fatal(err)
	}
	notExecutable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(notExecutable, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, executable := range []string{"", "relative", filepath.Join(t.TempDir(), "missing"), t.TempDir(), notExecutable} {
		client.projmuxExecutable = executable
		receipt, err := client.SendMessage(context.Background(), SendMessageRequest{Source: "uid:" + uid, Target: "uid:" + uid, Text: "peer"})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeMessageInvalidInput || receipt.MessageRef != "" {
			t.Fatalf("executable=%q receipt=%+v err=%v", executable, receipt, err)
		}
	}
	// A valid executable proceeds to the existing route refusal: this retired
	// fixture has no live Claude authority, and must never reach acceptance.
	client.projmuxExecutable, err = os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SendMessage(context.Background(), SendMessageRequest{Source: "uid:" + uid, Target: "uid:" + uid, Text: "peer"})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeMessageRouteUnavailable {
		t.Fatalf("valid executable did not reach route admission: %v", err)
	}
	after, err := os.ReadFile(storage.Path())
	if err != nil || string(before) != string(after) {
		t.Fatalf("Registry changed before admission: %v", err)
	}
	if _, err := os.Stat(filepath.Join(client.paths.StateDir, "agent-messages")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("message acceptance effect: %v", err)
	}
}

func TestPeerMessagePostAcceptanceNotFoundRemainsDeliveryFailure(t *testing.T) {
	receipt := publicMessageReceipt(agentmessages.Receipt{MessageRef: "message-known"})
	err := messageRefusal(store.ErrNotFound, receipt)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeMessageDeliveryFailed || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-acceptance not-found=%v", err)
	}
	err = messageRefusal(store.ErrNotFound, publicMessageReceipt(agentmessages.Receipt{}))
	if !errors.As(err, &refusal) || refusal.Code != CodeMessageNotFound {
		t.Fatalf("pre-acceptance not-found=%v", err)
	}
}
