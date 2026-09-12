package app

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

func TestClaudeDialogueCanaryAcceptsProductionStoreClaimAndPublicReceipt(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required by the opt-in canary")
	}
	now := time.Now()
	original := *dialogueEnvelope("message-canary-production", now.Add(time.Minute)).BrokerEnvelope
	original.ConversationRef = conversationRefFor(original.MessageRef)
	store := messagestore.NewStoreAt(filepath.Join(t.TempDir(), "messages.json"))
	if _, _, err := store.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MarkHandoff(original.MessageRef); err != nil {
		t.Fatal(err)
	}
	record, _, err := store.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: original.MessageRef, ConversationRef: original.ConversationRef, Target: original.Target, Reason: "full-frame-handoff-receipt", ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutReply(original.MessageRef, "reply-canary-production", "EXPECTED", original.Target, original.Source, now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(original.Source, now)
	if err != nil || !ok {
		t.Fatal("production claim failed", err)
	}
	var receipt bytes.Buffer
	if err := writeAgentMessageReceipt(&receipt, receiptFor(record), true); err != nil {
		t.Fatal(err)
	}
	// The canary consumes the durable store claim shape; there is no public
	// claim command. Keep its correlation check tied to the production record
	// and the current public send/status receipt serializer.
	fixture := map[string]any{"original": json.RawMessage(receipt.Bytes()), "reply": claimed, "routes": map[string]any{"sender": original.Source, "receiver": original.Target},
		"evidence": []map[string]any{{"toolUseID": "tool-production", "messageRef": original.MessageRef, "targetAgentUID": original.Source.AgentUID, "replyRef": claimed.Envelope.MessageRef, "resultObserved": true, "guardSelectionMatched": true, "guardedCommitMatched": true}}}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "public-receipts.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../scripts/agent-dialogue-canary-evidence.py")
	if err != nil {
		t.Fatal(err)
	}
	code := "import json,runpy,sys; f=json.load(open(sys.argv[2])); n=runpy.run_path(sys.argv[1]); n['validate_reply'](f['original'],f['reply'],f['evidence'],f['routes'],'EXPECTED')"
	cmd := exec.Command(python, "-c", code, script, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("production receipt rejected: %v\n%s", err, output)
	}
}
