package questions

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
)

type asyncReadDispatcher struct {
	views   []processquestions.ExactQuestion
	answers int
}

func (d *asyncReadDispatcher) ReadExactProcessQuestions(context.Context, string) ([]processquestions.ExactQuestion, error) {
	return d.views, nil
}
func (d *asyncReadDispatcher) AnswerExactProcessQuestion(context.Context, string, string, map[int]agentquestion.Selection) error {
	d.answers++
	return nil
}

func TestAsyncServiceListAndAnswerOfflineAndExact(t *testing.T) {
	st := agentquestion.NewStore(t.TempDir())
	r, err := st.Create(agentquestion.Record{ID: "question-0000000000000001", Provider: "codex", Delivery: "async", AgentUID: "agent", PaneUID: "pane", SessionID: "thread", Generation: "generation", Questions: json.RawMessage(`[{"id":"q1","question":"Choose","isOther":true,"options":[{"label":"cobalt"},{"label":"amber"}]}]`), Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	a := m.Agent{Metadata: m.ObjectMeta{UID: "agent", Name: "fixture"}, Spec: m.AgentSpec{Provider: "codex"}, Status: m.AgentStatus{PaneRef: "pane", Phase: m.PhaseOffline}}
	p := m.Pane{Metadata: m.ObjectMeta{UID: "pane"}, Spec: m.PaneSpec{Runtime: m.PaneRuntimeSpec{Kind: m.RuntimeProcess}}}
	reg := m.Registry{Agents: []m.Agent{a}, Panes: []m.Pane{p}}
	prompts, _ := r.ParsedQuestions()
	dispatcher := &asyncReadDispatcher{views: []processquestions.ExactQuestion{{QuestionID: r.ID, Delivery: "async", State: agentquestion.StateWaiting, Prompts: prompts, CanAnswer: true}}}
	service := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, LoadRegistry: func() (m.Registry, error) { return reg, nil }, Dispatcher: dispatcher}
	for _, running := range []bool{false, true} {
		if running {
			a.Status.Phase = m.PhaseRunning
			reg.Agents[0] = a
			reg.Panes[0].Status.ProcessSession = &m.ProcessSessionRecord{ThreadID: "thread", Binding: m.ProcessBinding{Generation: "generation"}}
		}
		list, err := service.List(context.Background(), a)
		if err != nil || len(list.Questions) != 1 || list.Questions[0].CanAnswer || list.Questions[0].Delivery != "async" || !list.Questions[0].Prompts[0].IsOther {
			t.Fatal(list, err)
		}
		before, _ := os.ReadFile(st.Path())
		if _, err = service.Answer(context.Background(), Request{Spelling: "agent question answer", QuestionID: r.ID, Texts: []string{"1=answer"}}, a); err == nil {
			t.Fatal("answer accepted")
		}
		after, _ := os.ReadFile(st.Path())
		if !bytes.Equal(before, after) || dispatcher.answers != 0 {
			t.Fatal("answer effects")
		}
	}
}
