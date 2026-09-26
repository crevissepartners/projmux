package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestCodexQuestionChannelCloseAndExpiryLeaveNativePromptAnswerable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window time.Duration
		close  bool
		want   agentquestion.State
	}{
		{name: "disable", window: time.Minute, close: true, want: agentquestion.StateClosed},
		{name: "expiry", window: 20 * time.Millisecond, want: agentquestion.StateExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newQuestionFixture(t, true)
			agent, _ := fixture.resources.registry.Agent(questionTestAgent)
			agent.Spec.Provider = aiModeCodex
			channel := codexQuestionChannel{
				loadRegistry: fixture.resources.store().load,
				store:        func() (*agentquestion.Store, error) { return fixture.store, nil },
				answering:    func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringClaude },
				window:       func() time.Duration { return tc.window },
				newID:        agentquestion.NewID,
				poll:         time.Millisecond,
			}
			identity := codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "gen-1", ThreadID: "thread-1"}
			responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			channel.Handle(ctx, identity, codexappserver.Notification{
				Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`),
				Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"q1","question":"Pick","options":[{"label":"A"}]}]}`),
			}, responder)
			records, err := fixture.store.List(questionTestAgent)
			if err != nil || len(records) != 1 {
				t.Fatalf("waiting record count = %d, err = %v", len(records), err)
			}
			if tc.close {
				if _, _, err := runRoute(t, fixture.command, "question", "disable", "uid:"+questionTestAgent); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.After(time.Second)
			for {
				record, found, err := fixture.store.Get(records[0].ID)
				if err != nil || !found {
					t.Fatalf("record missing after %s: %v", tc.name, err)
				}
				if record.State == tc.want {
					break
				}
				select {
				case <-deadline:
					t.Fatalf("record state = %s, want %s", record.State, tc.want)
				case <-time.After(time.Millisecond):
				}
			}
			select {
			case <-responder.replies:
				t.Fatal("close or expiry answered the native Codex prompt")
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}

func TestCodexQuestionNativeAnswerFirstRefusesLateCLIAnswer(t *testing.T) {
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
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	channel.Handle(ctx, identity, codexappserver.Notification{
		Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"q1","question":"Pick","options":[{"label":"A"}]}]}`),
	}, responder)
	records, err := fixture.store.List(questionTestAgent)
	if err != nil || len(records) != 1 {
		t.Fatalf("waiting record count = %d, err = %v", len(records), err)
	}
	channel.HandleResolved(identity, codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "other"})
	channel.HandleResolved(codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "other", ThreadID: "thread-1"}, codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "17"})
	record, _, err := fixture.store.Get(records[0].ID)
	if err != nil || record.State != agentquestion.StateWaiting {
		t.Fatalf("unrelated resolution changed record state to %s: %v", record.State, err)
	}
	channel.HandleResolved(identity, codexappserver.LifecycleEvent{Kind: codexappserver.LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "17"})
	record, _, err = fixture.store.Get(records[0].ID)
	if err != nil || record.State != agentquestion.StateClosed || record.Disposition != "answered-elsewhere" {
		t.Fatalf("native resolution state/disposition = %s/%s: %v", record.State, record.Disposition, err)
	}
	if _, err := fixture.store.Answer(record.ID, questionTestAgent, map[string]string{"q1": `["A"]`}); !errors.Is(err, agentquestion.ErrAnsweredElsewhere) {
		t.Fatalf("store race refusal = %v", err)
	}
	_, _, err = runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, record.ID, "--index", "1=1")
	if err == nil || !strings.Contains(err.Error(), questionReasonAnsweredElsewhere) {
		t.Fatalf("late CLI answer refusal = %v", err)
	}
	stdout, _, err := runRoute(t, fixture.command, "question", "list", "uid:"+questionTestAgent, "-o", "json")
	if err != nil || !strings.Contains(stdout, `"disposition":"answered-elsewhere"`) {
		t.Fatalf("list missing disposition: %v", err)
	}
	select {
	case <-responder.replies:
		t.Fatal("late CLI answer responded to an already resolved Codex request")
	case <-time.After(20 * time.Millisecond):
	}
}
