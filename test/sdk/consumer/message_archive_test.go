package consumer_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// This separate GOWORK=off module uses only public imports and the same fixture
// as the SDK/authority parity test. No live messages, provider or tmux is used.
func TestExternalMessageArchiveConsumer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent-messages")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"messages.json", "history.jsonl", "history.jsonl.1"} {
		b, err := os.ReadFile(filepath.Join("testdata", "message-archive", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := projmux.NewClient(projmux.Options{StateDir: root, ConfigDir: filepath.Join(root, "absent-config"), SocketPath: filepath.Join(root, "absent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	var archive core.MessageArchive
	archive, err = client.ReadMessageArchive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	terminal := at.Add(time.Minute)
	source := core.MessageRoute{AgentUID: "agent-source", PaneUID: "pane-source", ActivationGeneration: "generation-source", Provider: "codex", Incarnation: "incarnation-source"}
	target := core.MessageRoute{AgentUID: "agent-target", PaneUID: "pane-target", ActivationGeneration: "generation-target", Provider: "claude", Incarnation: "incarnation-target"}
	origin := core.MessageOrigin{Kind: "operator", Client: "fixture-client"}
	peer := core.MessageArchiveRecord{MessageRef: "message-peer", ConversationRef: "conversation-one", ReplyTo: "message-original", Source: source, Target: target, Payload: "안녕 🌿", PayloadBytes: 11, Delivery: core.MessageDelivery{MessageRef: "message-peer", ConversationRef: "conversation-one", State: core.MessageFailed, Reason: "fixture-reason", OutcomeUnknown: true, AcceptedAt: at, TerminalAt: terminal}}
	operator := core.MessageArchiveRecord{MessageRef: "message-operator", ConversationRef: "conversation-two", Origin: origin, Target: target, Payload: "operator body", PayloadBytes: 13, Operator: true, Delivery: core.MessageDelivery{MessageRef: "message-operator", ConversationRef: "conversation-two", State: core.MessageFailed, Reason: "fixture-reason", OutcomeUnknown: true, AcceptedAt: at, TerminalAt: terminal}}
	full := core.MessageHistoryEntry{SchemaVersion: 9, MessageRef: peer.MessageRef, ConversationRef: peer.ConversationRef, ReplyTo: peer.ReplyTo, State: core.MessageFailed, AcceptedAt: at, PayloadBytes: 11, PayloadSHA256: "fixture-digest", Source: source, Target: target}
	narrow := core.MessageHistoryEntry{SchemaVersion: 1, MessageRef: operator.MessageRef, ConversationRef: operator.ConversationRef, State: core.MessageDelivered, AcceptedAt: at, PayloadBytes: 13, Origin: origin, Target: core.MessageRoute{AgentUID: "agent-target", Provider: "claude"}, Operator: true}
	legacy := full
	legacy.SchemaVersion = 1
	legacy.PayloadSHA256 = ""
	want := core.MessageArchive{Records: []core.MessageArchiveRecord{peer, operator}, History: []core.MessageHistoryEntry{full, narrow, legacy, narrow}, Skipped: 2}
	if !reflect.DeepEqual(archive, want) {
		t.Fatalf("archive %+v, want %+v", archive, want)
	}
	var paths []string
	paths, err = client.MessageArchiveFiles(context.Background())
	if err != nil || !reflect.DeepEqual(paths, []string{filepath.Join(dir, "messages.json"), filepath.Join(dir, "history.jsonl"), filepath.Join(dir, "history.jsonl.1")}) {
		t.Fatalf("paths %v %v", paths, err)
	}
}
