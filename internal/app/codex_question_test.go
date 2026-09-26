package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
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
	// Cleanups run before t.TempDir removes the store, and after
	// t.Context is canceled, so the waiter is joined first.
	t.Cleanup(channel.Wait)
	responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
	params := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"choice","header":"Pick","question":"Pick one","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]},{"id":"text","header":"Reason","question":"Reason","options":null}]}`)
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
	if json.Unmarshal([]byte(stdout), &view) != nil || view.Questions[0].Answers["text"] == "" {
		t.Fatal("nonsecret answer was absent from list")
	}
}

func TestCodexQuestionPopupAnswersThroughExistingResponder(t *testing.T) {
	fixture := newQuestionFixture(t, false)
	agent, _ := fixture.resources.registry.Agent(questionTestAgent)
	agent.Spec.Provider = aiModeCodex
	popup := newFakeQuestionPopup("client-1")
	fixture.runPickerInPopup(popup, pickRow("B"), pickRow("Other"), typeAnswer("because it is faster"))
	channel := codexQuestionChannel{
		loadRegistry: fixture.resources.store().load,
		store:        func() (*agentquestion.Store, error) { return fixture.store, nil },
		popup:        popup,
		answering:    func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux },
		window:       func() time.Duration { return time.Minute },
		newID:        agentquestion.NewID,
		poll:         time.Millisecond,
	}
	t.Cleanup(channel.Wait)
	responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	channel.Handle(ctx, codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "gen-1", ThreadID: "thread-1"}, codexappserver.Notification{
		Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"choice","question":"Choose","options":[{"label":"A"},{"label":"B"}]},{"id":"reason","question":"Why?","options":null}]}`),
	}, responder)
	target := popup.waitOpened(t)
	if target.PaneID != "%7" || target.AgentUID != questionTestAgent || target.QuestionID == "" || target.StorePath != fixture.store.Path() {
		t.Fatalf("popup target = %+v", target)
	}
	select {
	case reply := <-responder.replies:
		if reply.id != "17" || reply.result.Answers["choice"].Answers[0] != "B" || reply.result.Answers["reason"].Answers[0] != "because it is faster" {
			t.Fatalf("popup answer did not reach Codex binding: %+v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("popup answer did not reach Codex binding")
	}
}

func TestCodexQuestionPickerOffersFreeTextOnlyWhenAllowed(t *testing.T) {
	question := agentquestion.Question{ID: "choice", Question: "Choose", Options: []agentquestion.Option{{Label: "A"}}}
	choose := &scriptedQuestionPicker{steps: steps(pickRow("A"))}
	if _, ok, err := collectClaudeQuestionSelections(choose, claudeQuestionText{}, []agentquestion.Question{question}, true); err != nil || !ok {
		t.Fatalf("Codex option selection: ok=%v err=%v", ok, err)
	}
	for _, item := range choose.runs[0].Items {
		if item.Value == claudeQuestionOtherValue {
			t.Fatal("Codex question without isOther offered free text")
		}
	}
	question.IsOther = true
	other := &scriptedQuestionPicker{steps: steps(pickRow("Other"), typeAnswer("custom"))}
	selections, ok, err := collectClaudeQuestionSelections(other, claudeQuestionText{}, []agentquestion.Question{question}, true)
	if err != nil || !ok || !selections[0].HasText || selections[0].Text != "custom" {
		t.Fatalf("Codex isOther selection = %+v, ok=%v err=%v", selections, ok, err)
	}
}

func TestCodexSecretQuestionKeepsNativePromptAndNeverEchoesCLIAnswer(t *testing.T) {
	fixture := newQuestionFixture(t, false)
	fixture.command.questionAnswering = func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux }
	agent, _ := fixture.resources.registry.Agent(questionTestAgent)
	agent.Spec.Provider = aiModeCodex
	popup := newFakeQuestionPopup("client-1")
	channel := codexQuestionChannel{
		loadRegistry: fixture.resources.store().load,
		store:        func() (*agentquestion.Store, error) { return fixture.store, nil },
		popup:        popup,
		answering:    func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux },
		window:       func() time.Duration { return time.Minute },
		newID:        agentquestion.NewID,
		poll:         time.Millisecond,
	}
	t.Cleanup(channel.Wait)
	responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	channel.Handle(ctx, codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "gen-1", ThreadID: "thread-1"}, codexappserver.Notification{
		Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"secret","question":"Enter a secret","options":null,"isSecret":true}]}`),
	}, responder)
	records, err := fixture.store.List(questionTestAgent)
	if err != nil || len(records) != 1 {
		t.Fatalf("secret record count = %d, err=%v", len(records), err)
	}
	const sampleAnswer = "redaction check sample"
	answerOut, answerErr, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, records[0].ID, "--text", "1="+sampleAnswer)
	if err == nil || !strings.Contains(err.Error(), questionReasonSecretNativeOnly) || strings.Contains(err.Error(), sampleAnswer) || strings.Contains(answerOut, sampleAnswer) || strings.Contains(answerErr, sampleAnswer) {
		t.Fatalf("secret refusal = %v, stdout=%q, stderr=%q", err, answerOut, answerErr)
	}
	jsonList, _, err := runRoute(t, fixture.command, "question", "list", "uid:"+questionTestAgent, "-o", "json")
	if err != nil || strings.Contains(jsonList, sampleAnswer) || !strings.Contains(jsonList, `"isSecret":true`) {
		t.Fatalf("secret list = %q, err=%v", jsonList, err)
	}
	textList, _, err := runRoute(t, fixture.command, "question", "list", "uid:"+questionTestAgent)
	if err != nil || strings.Contains(textList, sampleAnswer) || !strings.Contains(textList, "answer in the Codex window") {
		t.Fatalf("secret text guidance = %q, err=%v", textList, err)
	}
	if _, opens, _ := popup.counts(); opens != 0 {
		t.Fatal("secret question opened an unmasked popup")
	}
	current, _, err := fixture.store.Get(records[0].ID)
	if err != nil || current.State != agentquestion.StateWaiting {
		t.Fatalf("secret request was not left to native Codex: %s, %v", current.State, err)
	}
	if _, err := fixture.store.Answer(records[0].ID, questionTestAgent, map[string]string{"secret": `["` + sampleAnswer + `"]`}); err != nil {
		t.Fatal(err)
	}
	for _, output := range [][]string{{"-o", "json"}, nil} {
		args := append([]string{"question", "list", "uid:" + questionTestAgent}, output...)
		listed, _, err := runRoute(t, fixture.command, args...)
		if err != nil || strings.Contains(listed, sampleAnswer) {
			t.Fatalf("settled secret leaked from list: %q, err=%v", listed, err)
		}
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
	t.Cleanup(channel.Wait)
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
			t.Cleanup(channel.Wait)
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
	t.Cleanup(channel.Wait)
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

// TestCodexQuestionWaitJoinsTheCanceledWaiterBeforeItsStoreWrite holds the
// waiter of a canceled request just before its store write. The owner's Wait
// must not return while that write is still pending: after Wait, the store is
// no longer the waiter's to touch.
func TestCodexQuestionWaitJoinsTheCanceledWaiterBeforeItsStoreWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := newQuestionFixture(t, true)
		agent, _ := fixture.resources.registry.Agent(questionTestAgent)
		agent.Spec.Provider = aiModeCodex
		reached, release := make(chan struct{}), make(chan struct{})
		channel := codexQuestionChannel{
			loadRegistry:        fixture.resources.store().load,
			store:               func() (*agentquestion.Store, error) { return fixture.store, nil },
			answering:           func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringClaude },
			window:              func() time.Duration { return time.Minute },
			newID:               agentquestion.NewID,
			poll:                time.Millisecond,
			beforeCanceledClose: func() { close(reached); <-release },
		}
		responder := recordingCodexQuestionResponder{replies: make(chan codexQuestionReply, 1)}
		ctx, cancel := context.WithCancel(t.Context())
		channel.Handle(ctx, codexLifecycleIdentity{AgentUID: questionTestAgent, PaneUID: questionTestPane, RuntimeID: "%7", Generation: "gen-1", ThreadID: "thread-1"}, codexappserver.Notification{
			Method: "item/tool/requestUserInput", RequestID: "17", RawRequestID: json.RawMessage(`17`),
			Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"q1","question":"Pick","options":[{"label":"A"}]}]}`),
		}, responder)
		records, err := fixture.store.List(questionTestAgent)
		if err != nil || len(records) != 1 {
			t.Fatalf("waiting record count = %d, err = %v", len(records), err)
		}
		state := func() agentquestion.State {
			record, _, err := fixture.store.Get(records[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			return record.State
		}
		cancel()
		<-reached
		joined := make(chan struct{})
		go func() {
			channel.Wait()
			close(joined)
		}()
		// Every goroutine is now blocked: the waiter on its held write, and
		// the owner either in Wait or already past it.
		synctest.Wait()
		select {
		case <-joined:
			before := state()
			close(release)
			synctest.Wait()
			t.Fatalf("Wait returned while the canceled waiter still held its store write: record %s when Wait returned, %s after", before, state())
		default:
		}
		close(release)
		<-joined
		if got := state(); got != agentquestion.StateClosed {
			t.Fatalf("record state after Wait = %s, want %s", got, agentquestion.StateClosed)
		}
	})
}
