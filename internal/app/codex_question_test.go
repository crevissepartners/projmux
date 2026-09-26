package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

type codexQuestionReply struct {
	id     string
	result codexUserInputResponse
}

type recordingCodexQuestionResponder struct {
	replies chan codexQuestionReply
}

func (r recordingCodexQuestionResponder) RespondServerRequest(_ context.Context, id json.RawMessage, result any) error {
	r.replies <- codexQuestionReply{id: string(id), result: result.(codexUserInputResponse)}
	return nil
}

func TestCodexQuestionChannelAnswersBlockingRequestThroughCLI(t *testing.T) {
	fixture := newQuestionFixture(t, true)
	agent, _ := fixture.resources.registry.Agent(questionTestAgent)
	agent.Spec.Provider = aiModeCodex
	identity := codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "gen-1", ThreadID: "thread-1"}
	channel := codexQuestionChannel{
		loadRegistry: fixture.resources.store().load,
		store:        func() (*agentquestion.Store, error) { return fixture.store, nil },
		answering:    func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringClaude },
		window:       func() time.Duration { return time.Minute },
		newID:        agentquestion.NewID,
		poll:         time.Millisecond,
	}
	responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
	params := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"choice","header":"Pick","question":"Pick one","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]},{"id":"text","header":"Reason","question":"Reason","options":null,"isSecret":true}]}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	channel.Handle(ctx, identity, codexappserver.Notification{Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`), Params: params}, responder)
	listed, err := fixture.store.List(questionTestAgent)
	if err != nil || len(listed) != 1 || listed[0].Provider != "codex" || listed[0].State != agentquestion.StateWaiting {
		t.Fatalf("record state/count = %v/%d, err=%v", listed, len(listed), err)
	}
	stdout, _, err := runRoute(t, fixture.command, "question", "list", "uid:"+questionTestAgent, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var view agentQuestionList
	if json.Unmarshal([]byte(stdout), &view) != nil || len(view.Questions) != 1 || len(view.Questions[0].Prompts) != 2 || view.Questions[0].Prompts[0].ID != "choice" {
		t.Fatal("Codex question was not projected into the CLI list")
	}
	if _, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, listed[0].ID, "--index", "1=2", "--text", "2=private answer"); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-responder.replies:
		if reply.id != "17" || len(reply.result.Answers) != 2 || reply.result.Answers["choice"].Answers[0] != "B" || reply.result.Answers["text"].Answers[0] != "private answer" {
			t.Fatal("Codex response did not preserve question IDs and selected answers")
		}
	case <-time.After(time.Second):
		t.Fatal("Codex request was not answered")
	}
	if stdout, _, err = runRoute(t, fixture.command, "question", "list", "uid:"+questionTestAgent, "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(stdout), &view) != nil || view.Questions[0].Answers["text"] != "" {
		t.Fatal("secret answer was disclosed by list")
	}
}

func TestCodexQuestionChannelOffAndNonblockingLeaveProviderRequestAlone(t *testing.T) {
	fixture := newQuestionFixture(t, false)
	agent, _ := fixture.resources.registry.Agent(questionTestAgent)
	agent.Spec.Provider = aiModeCodex
	identity := codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "gen-1", ThreadID: "thread-1"}
	channel := codexQuestionChannel{
		loadRegistry: fixture.resources.store().load,
		store:        func() (*agentquestion.Store, error) { return fixture.store, nil },
		answering:    func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringClaude },
		window:       func() time.Duration { return time.Minute },
		newID:        agentquestion.NewID,
	}
	responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
	notification := codexappserver.Notification{Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`), Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"q1","question":"Pick","header":"Pick","options":[{"label":"A"}]}]}`)}
	channel.Handle(t.Context(), identity, notification, responder)
	if records, _ := fixture.store.List(questionTestAgent); len(records) != 0 {
		t.Fatal("off channel recorded a request")
	}
	if _, _, err := runRoute(t, fixture.command, "question", "enable", "uid:"+questionTestAgent); err != nil {
		t.Fatal(err)
	}
	channel.Handle(t.Context(), codexLifecycleIdentity{
		AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "gen-1", ThreadID: "other-thread",
	}, notification, responder)
	if records, _ := fixture.store.List(questionTestAgent); len(records) != 0 {
		t.Fatal("request from another broker thread entered the CLI channel")
	}
	channel.Handle(t.Context(), codexLifecycleIdentity{
		AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "other-generation", ThreadID: "thread-1",
	}, notification, responder)
	if records, _ := fixture.store.List(questionTestAgent); len(records) != 0 {
		t.Fatal("request from another activation entered the CLI channel")
	}
	notification.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":false,"questions":[{"id":"q1","question":"Pick","header":"Pick","options":[{"label":"A"}]}]}`)
	channel.Handle(t.Context(), identity, notification, responder)
	if records, _ := fixture.store.List(questionTestAgent); len(records) != 0 {
		t.Fatal("nonblocking request entered the CLI channel")
	}
}
