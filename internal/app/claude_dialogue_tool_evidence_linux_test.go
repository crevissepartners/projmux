package app

import (
	"bytes"
	"encoding/json"
	"net"
	"os"

	"errors"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"strings"
	"testing"
	"time"
)

func TestClaudeDialogueToolEvidenceIsReadOnlyAndIndependentOfArrivalOrder(t *testing.T) {
	fixture, gate, _ := currentDialogueProfileFixture(t)
	fixture.server.hub.qualifiedVersion = claudeFrozenFrameProviderVersion
	broker := &failingClaudeDialogueBroker{}
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	fixture.server.broker, fixture.server.poster = broker, poster
	call := func(request claudeCoordinationRequest) claudeCoordinationResponse {
		request.Version = claudeCoordinationVersion
		request.Target = fixture.target
		request.SessionID = fixture.sessionID
		return fixture.call(t, request)
	}
	observe := func(kind, id, ref, reply string) {
		action := claudeDialogueObservedTool{ToolUseID: id, MessageRef: ref, TargetAgentUID: "codex-agent", ReplyRef: reply, ResultObserved: kind == "tool-result"}
		got := call(claudeCoordinationRequest{Operation: "profile-observe", Observation: &claudeDialogueObservation{Kind: kind, SessionID: fixture.sessionID, ToolActions: []claudeDialogueObservedTool{action}}})
		if got.Kind != "profile-observed" {
			t.Fatalf("observation %s failed: %s", kind, got.Kind)
		}
	}
	originals := map[string]claudeCoordinationEnvelope{}
	permits := map[string]string{}
	for _, ref := range []string{"message-a", "message-b"} {
		envelope := dialogueForRoute(ref, fixture.route, time.Now())
		originals[ref] = envelope
		if got := call(claudeCoordinationRequest{Operation: "submit", Envelope: &envelope}); got.Kind != "delivered" {
			t.Fatal("fixture submit failed")
		}
		input := toolTestInput(gate, "tool-"+ref, ref, "UNRETAINED_REPLY_BODY")
		got := call(claudeCoordinationRequest{Operation: "tool-prepare", ToolInput: &input})
		if got.Kind != "tool-permitted" || got.ToolResult == nil {
			t.Fatal("fixture prepare failed")
		}
		permits[ref] = got.ToolResult.Marker
	}
	// A observation precedes commit, B observation follows it. Human activity
	// does not supply a reply and does not veto the later valid explicit action.
	observe("tool", "tool-message-a", "message-a", "")
	boundary := call(claudeCoordinationRequest{Operation: "user-prompt"})
	if boundary.Kind != "boundary-closed" || broker.replies != 0 {
		t.Fatal("human boundary changed reply authority")
	}
	for _, ref := range []string{"message-b", "message-a"} {
		if got := call(claudeCoordinationRequest{Operation: "tool-consume", ToolMarker: permits[ref]}); got.Kind != "tool-permitted" {
			t.Fatal("consume failed")
		}
		if ref == "message-a" {
			broker.replyErr = errors.New("fixture durable commit failed")
		}
		original := originals[ref]
		reply := explicitTestReply(*original.BrokerEnvelope, "UNRETAINED_REPLY_BODY")
		got := call(claudeCoordinationRequest{Operation: "explicit-reply", ReplyEnvelope: &reply})
		if ref == "message-b" && got.Kind != "reply-accepted" {
			t.Fatal("B explicit commit failed")
		}
		if ref == "message-a" && got.Kind != "reply-refused" {
			t.Fatal("failed durable commit was promoted")
		}
	}
	observe("tool", "tool-message-b", "message-b", "")
	observe("tool-result", "tool-message-b", "message-b", "reply-message-b")
	observe("tool-result", "tool-message-a", "message-a", "reply-message-a")
	writes, commits := poster.calls, broker.replies
	snapshot := call(claudeCoordinationRequest{Operation: "profile-evidence"})
	if snapshot.Kind != "profile-evidence" || len(snapshot.ToolEvidence) != 2 {
		t.Fatal("bounded tool evidence unavailable")
	}
	for _, item := range snapshot.ToolEvidence {
		if !item.GuardSelectionMatched || !item.ResultObserved {
			t.Fatal("valid original selection was lost")
		}
		if item.GuardedCommitMatched != (item.MessageRef == "message-b") {
			t.Fatal("observation/guard witness substituted for durable commit")
		}
	}
	if poster.calls != writes || broker.replies != commits {
		t.Fatal("read-only proof caused effects")
	}
	fixture.server.profile.mu.Lock()
	wrong := fixture.server.profile.tools["tool-message-b"]
	wrong.ReplyRef = "reply-message-a"
	fixture.server.profile.tools["tool-message-b"] = wrong
	fixture.server.profile.mu.Unlock()
	for _, item := range fixture.server.dialogueToolEvidence() {
		if item.MessageRef == "message-b" && (!item.GuardSelectionMatched || item.GuardedCommitMatched) {
			t.Fatal("wrong observed result matched an otherwise valid committed original")
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"UNRETAINED_REPLY_BODY", "pmx-reply-ticket-", "thinking", "signature"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("raw model/tool data retained")
		}
	}
}

func TestClaudeDialogueObservedToolCannotGrantOrForgeCommitEvidence(t *testing.T) {
	fixture, gate, _ := currentDialogueProfileFixture(t)
	fixture.server.hub.qualifiedVersion = claudeFrozenFrameProviderVersion
	action := claudeDialogueObservedTool{ToolUseID: "forged-tool", MessageRef: "unknown-original", TargetAgentUID: "codex-agent"}
	peer := gate.profileObserver
	authority := fixture.target.Authority
	if !fixture.server.recordDialogueObservation(peer, authority.Process.PID, &claudeDialogueObservation{Kind: "tool", SessionID: fixture.sessionID, ToolActions: []claudeDialogueObservedTool{action}}) {
		t.Fatal("well-shaped observation refused")
	}
	input := toolTestInput(gate, action.ToolUseID, action.MessageRef, "text")
	if _, err := gate.prepare(input, peer, fixture.route, fixture.server.hub, &failingClaudeDialogueBroker{}); err == nil {
		t.Fatal("observation granted a permit")
	}
	action.ResultObserved = true
	action.ReplyRef = "forged-reply"
	if !fixture.server.recordDialogueObservation(peer, authority.Process.PID, &claudeDialogueObservation{Kind: "tool-result", SessionID: fixture.sessionID, ToolActions: []claudeDialogueObservedTool{action}}) {
		t.Fatal("paired result shape refused")
	}
	snapshot := fixture.server.dialogueToolEvidence()
	if len(snapshot) != 1 || snapshot[0].GuardSelectionMatched || snapshot[0].GuardedCommitMatched {
		t.Fatal("forged observation became execution authority")
	}
}

func TestAgentCapabilitiesClaudeUnqualifiedRecoveryUsesCurrentPublicProfile(t *testing.T) {
	fixture, _, _ := currentDialogueProfileFixture(t)
	path := claudeLeaseSocket(fixture.registryPath, fixture.route.PaneUID, fixture.route.Generation, fixture.target.Authority.RegistrationGeneration)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			_, _ = connection.Write([]byte{1})
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	before, err := os.ReadFile(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := intmetadata.NewStore(fixture.registryPath).LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := registry.Agent(fixture.route.AgentUID)
	if !ok {
		t.Fatal("fixture Agent missing")
	}
	if !probeClaudeRegistrationLease(fixture.registryPath, fixture.route) {
		t.Fatal("live registration baseline is missing")
	}
	projection := projectClaudeCoordinationEligibilityAt(registry, *agent, fixture.registryPath)
	if projection.Eligible || !strings.Contains(projection.Reason, "unqualified") || !strings.Contains(projection.Recovery, "reply-only profile ready") ||
		!strings.Contains(projection.Recovery, "agent resume uid:"+agent.Metadata.UID+" --dialogue-reply-only") ||
		!strings.Contains(projection.Recovery, "agent message qualify uid:"+agent.Metadata.UID+" --confirm-isolated-provider-push") || strings.Contains(projection.Recovery, "--evidence") {
		t.Fatal("unqualified activation recovery does not use the current public guard/evidence path")
	}
	after, err := os.ReadFile(fixture.registryPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("projection mutated Registry or identity")
	}
}
