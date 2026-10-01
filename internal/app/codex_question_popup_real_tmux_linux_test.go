package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// realTmuxCodexQuestionPrompt is the question text the Codex popup draws.
const realTmuxCodexQuestionPrompt = "Choose a build tool"

// startRealTmuxCodexQuestion asks one Codex question whose popup talks to the
// isolated server and runs the popup route from the test binary. It returns
// the fixture, the question's record id, and the replies Codex receives.
func (s realTmuxQuestionServer) startRealTmuxCodexQuestion(t *testing.T) (*questionFixture, string, <-chan codexQuestionReply) {
	t.Helper()
	fixture := newQuestionFixture(t, false)
	agent, _ := fixture.resources.registry.Agent(questionTestAgent)
	agent.Spec.Provider = aiModeCodex
	pane, _ := fixture.resources.registry.Pane(questionTestPane)
	pane.Status.Activation.RuntimeID = s.paneID
	if err := fixture.resources.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	wrapper := s.pickerWrapper(t)
	channel := &codexQuestionChannel{
		loadRegistry: fixture.resources.store().load,
		store:        func() (*agentquestion.Store, error) { return fixture.store, nil },
		popup: tmuxClaudeQuestionPopup{
			runner:     s,
			executable: func() (string, error) { return wrapper, nil },
			routed:     true,
		},
		answering:  func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux },
		window:     func() time.Duration { return time.Minute },
		newID:      agentquestion.NewID,
		poll:       10 * time.Millisecond,
		clientPoll: 100 * time.Millisecond,
	}
	// Cleanups run before t.TempDir removes the store, and after
	// t.Context is canceled, so the waiter is joined first.
	t.Cleanup(channel.Wait)
	responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
	channel.Handle(t.Context(), codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: s.paneID, Generation: "gen-1", ThreadID: "thread-1"}, codexappserver.Notification{
		Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"choice","question":"` + realTmuxCodexQuestionPrompt + `","options":[{"label":"make"},{"label":"task"}]}]}`),
	}, responder)
	records, err := fixture.store.List(questionTestAgent)
	if err != nil || len(records) != 1 || records[0].State != agentquestion.StateWaiting {
		t.Fatalf("waiting record = %+v, err = %v", records, err)
	}
	return fixture, records[0].ID, responder.replies
}

// requireRealTmuxCodexQuestionWaiting holds that Codex was sent nothing and
// the record still waits.
func requireRealTmuxCodexQuestionWaiting(t *testing.T, fixture *questionFixture, id string, replies <-chan codexQuestionReply) {
	t.Helper()
	select {
	case reply := <-replies:
		t.Fatalf("Codex was answered %+v after the popup's client left", reply)
	default:
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateWaiting {
		t.Fatalf("record = %s/%q, want waiting after the popup's client left", record.State, record.Disposition)
	}
}

// requireRealTmuxCodexAnswer waits for the popup's first option to reach
// Codex through the existing responder, and for the record to be answered.
func requireRealTmuxCodexAnswer(t *testing.T, fixture *questionFixture, id string, replies <-chan codexQuestionReply) {
	t.Helper()
	select {
	case reply := <-replies:
		if reply.id != "17" || len(reply.result.Answers) != 1 || len(reply.result.Answers["choice"].Answers) != 1 || reply.result.Answers["choice"].Answers[0] != "make" {
			t.Fatalf("Codex popup answer = %+v", reply)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the answer from the popup that opened again did not reach the Codex binding")
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateAnswered {
		t.Fatalf("record = %s/%q, want answered", record.State, record.Disposition)
	}
}

// TestCodexQuestionPopupRealTmuxReopensOnTheClientLeftAfterADetach is two
// clients with the Codex popup on the one used last: that client detaches, the
// record keeps waiting, the popup opens again on the other client, and the
// answer picked there reaches Codex.
func TestCodexQuestionPopupRealTmuxReopensOnTheClientLeftAfterADetach(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	first := server.attach(t)
	second := server.attach(t)
	rows, err := server.tmux("list-clients", "-F", "#{client_name}\t#{pane_id}\t#{client_control_mode}\t#{client_activity}")
	if err != nil {
		t.Fatalf("list clients: %v: %s", err, rows)
	}
	viewing, other := first, second
	if claudeQuestionViewingClient(rows, server.paneID) == second.name {
		viewing, other = second, first
	}
	otherMark := other.mark()
	fixture, id, replies := server.startRealTmuxCodexQuestion(t)

	viewing.waitFor(t, 0, realTmuxCodexQuestionPrompt)
	if other.drewSince(otherMark, realTmuxCodexQuestionPrompt) {
		t.Fatal("the popup opened on the client not used last")
	}
	otherMark = other.mark()
	server.detachRealTmuxQuestionClient(t, viewing)
	requireRealTmuxCodexQuestionWaiting(t, fixture, id, replies)
	other.waitFor(t, otherMark, realTmuxCodexQuestionPrompt)
	other.keys(t, "\r")
	requireRealTmuxCodexAnswer(t, fixture, id, replies)
}

// TestCodexQuestionPopupRealTmuxWaitsForAClientAfterTheOnlyOneDetached is the
// only client detaching from the Codex popup: the record keeps waiting with no
// client, and the popup opens again once a client attaches.
func TestCodexQuestionPopupRealTmuxWaitsForAClientAfterTheOnlyOneDetached(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	client := server.attach(t)
	mark := client.mark()
	fixture, id, replies := server.startRealTmuxCodexQuestion(t)

	client.waitFor(t, mark, realTmuxCodexQuestionPrompt)
	server.detachRealTmuxQuestionClient(t, client)
	// Several looks at 100ms each find no client.
	time.Sleep(time.Second)
	requireRealTmuxCodexQuestionWaiting(t, fixture, id, replies)

	again := server.attach(t)
	again.waitFor(t, 0, realTmuxCodexQuestionPrompt)
	again.keys(t, "\r")
	requireRealTmuxCodexAnswer(t, fixture, id, replies)
}

// TestCodexQuestionPopupRealTmuxEscGivesTheQuestionBack is Esc in the real
// Codex popup: Codex is sent nothing, the record closes as popup-dismissed,
// and the popup does not open again.
func TestCodexQuestionPopupRealTmuxEscGivesTheQuestionBack(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	client := server.attach(t)
	mark := client.mark()
	fixture, id, replies := server.startRealTmuxCodexQuestion(t)

	client.waitFor(t, mark, realTmuxCodexQuestionPrompt)
	client.keys(t, "\x1b")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		record, _, _ := fixture.store.Get(id)
		if record.State != agentquestion.StateWaiting {
			if record.State != agentquestion.StateClosed || record.Disposition != string(agentquestion.CloseReasonPopupDismissed) {
				t.Fatalf("record = %s/%q, want closed/%s", record.State, record.Disposition, agentquestion.CloseReasonPopupDismissed)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the record still waits after Esc")
		}
	}
	after := client.mark()
	// Several looks at 100ms each would have opened it again.
	time.Sleep(time.Second)
	if client.drewSince(after, realTmuxCodexQuestionPrompt) {
		t.Fatal("the popup came back after Esc")
	}
	select {
	case reply := <-replies:
		t.Fatalf("Codex was answered %+v after Esc", reply)
	default:
	}
}
