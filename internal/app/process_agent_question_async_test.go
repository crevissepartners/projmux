package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestCodexAsyncQuestionCLIAndSDKNativeSourceReadOnly(t *testing.T) {
	for i := 0; i < len(nativeAppAsyncFrames); i += 2 {
		f := newAsyncProcessFixture(t, nativeAppAsyncFrames[i:i+2], true)
		b := f.endpoint.binding
		state := filepath.Dir(filepath.Dir(f.path))
		f.control.questions = agentquestion.NewStore(state)
		processQuestionCallbacks.Store(b, processQuestionCallback(f.control.exactQuestions))
		t.Cleanup(func() { processQuestionCallbacks.Delete(b) })
		f.turn(t, "first", "fixture")
		snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.AsyncQuestions) == 1 && s.Turn == "" })
		result := processAgentCreateResult{Binding: b, Provider: aiModeCodex, Handle: f.endpoint.handle, codexEndpoint: f.endpoint, registryPath: f.path}
		if err := result.recordProcessSnapshot(snap); err != nil {
			t.Fatal(err)
		}
		if err := f.control.syncControls(context.Background()); err != nil {
			t.Fatal(err)
		}
		reg, err := f.store.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		agent, _ := reg.Agent(b.Agent)
		command := &agentCommand{loadRegistry: f.store.LoadDegradedReadOnly, messagePaths: agentMessagePaths{registryPath: f.path}, questionStore: func() (*agentquestion.Store, error) { return f.control.questions, nil }}
		var out bytes.Buffer
		if err = command.listQuestions(agentQuestionRequest{spelling: "agent question list", json: true}, *agent, &out); err != nil {
			t.Fatal(err)
		}
		var listed agentQuestionList
		if err = json.Unmarshal(out.Bytes(), &listed); err != nil || len(listed.Questions) != 1 || len(listed.ExactQuestions) != 1 {
			t.Fatal(out.String(), err)
		}
		q := listed.Questions[0]
		if q.State != agentquestion.StateWaiting || q.Delivery != "async" || q.CanAnswer || len(q.Prompts[0].Options) != 2 || !q.Prompts[0].IsOther {
			t.Fatal(q)
		}
		client, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: filepath.Join(f.root, "config"), HomeDir: f.root})
		if err != nil {
			t.Fatal(err)
		}
		sdk, err := client.ListQuestions(context.Background(), "uid:"+b.Agent)
		if err != nil || questionJSON(t, listed) != questionJSON(t, sdk) {
			t.Fatal("SDK parity", err)
		}
		beforeWire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
		beforeStore, _ := os.ReadFile(f.control.questions.Path())
		err = command.answerQuestion(agentQuestionRequest{spelling: "agent question answer", questionID: q.ID, texts: []string{"1=explicit answer"}}, *agent, nil, &out)
		if err == nil {
			t.Fatal("CLI async answer accepted")
		}
		_, err = client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "uid:" + b.Agent, QuestionID: q.ID, Selections: []core.QuestionSelection{{Question: 1, Text: ptr("explicit answer")}}})
		if err == nil {
			t.Fatal("SDK async answer accepted")
		}
		if _, err = f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b, QuestionID: q.ID, Selections: map[int]agentquestion.Selection{0: {Text: "answer", HasText: true}}}); !errors.Is(err, processhost.ErrStale) {
			t.Fatal("direct callback", err)
		}
		afterWire, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
		afterStore, _ := os.ReadFile(f.control.questions.Path())
		if !bytes.Equal(beforeWire, afterWire) || !bytes.Equal(beforeStore, afterStore) {
			t.Fatal("answer refusal had effects")
		}
		if err = f.endpoint.handle.Turn(context.Background(), f.endpoint.authority(), "next", "fixture"); err != nil {
			last, _ := f.endpoint.handle.Observe(b)
			t.Fatalf("next turn: %v state=%s failure=%s", err, last.State, last.Failure)
		}
		snap = f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" && s.Sequence > snap.Sequence })
		events, _, eventsErr := f.endpoint.handle.Events(b, 0)
		gap := false
		for _, event := range events {
			if event.Kind == "stream-gap" {
				gap = true
			}
		}
		if eventsErr != nil || !gap {
			t.Fatal("fixture did not exercise host ring gap", eventsErr)
		}
		if err = f.control.syncControls(context.Background()); err != nil {
			t.Fatal(err)
		}
		same, err := client.ListQuestions(context.Background(), "uid:"+b.Agent)
		if err != nil || len(same.Questions) != 1 || same.Questions[0].ID != q.ID || !same.Questions[0].Deadline.Equal(q.Deadline) || same.Questions[0].State != core.QuestionWaiting {
			t.Fatal("next turn lost async", err)
		}
		if err = f.endpoint.handle.Stop(b); err != nil {
			t.Fatal(err)
		}
		_ = f.control.syncControls(context.Background())
		closed, _, err := f.control.questions.Get(q.ID)
		if err != nil || closed.State != agentquestion.StateClosed {
			t.Fatal("exit did not fence", err)
		}
	}
}
