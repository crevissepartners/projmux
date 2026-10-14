package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type asyncSDKDispatcher struct {
	views  []processquestions.ExactQuestion
	writes int
}

func (d *asyncSDKDispatcher) ReadExactProcessQuestions(context.Context, string) ([]processquestions.ExactQuestion, error) {
	return d.views, nil
}
func (d *asyncSDKDispatcher) AnswerExactProcessQuestion(context.Context, string, string, map[int]agentquestion.Selection) error {
	d.writes++
	return nil
}

func TestSDKAsyncQuestionsReadAndAnswerRefusal(t *testing.T) {
	c, reg, uid, puid := sdkStopFixture(t)
	p, _ := reg.Pane(puid)
	st := agentquestion.NewStore(c.paths.StateDir)
	r, err := st.Create(agentquestion.Record{ID: "question-0000000000000001", AgentUID: uid, PaneUID: puid, Provider: "codex", Delivery: "async", SessionID: p.Status.ProcessSession.ThreadID, Generation: p.Status.ProcessSession.Binding.Generation, Questions: json.RawMessage(`[{"id":"q1","question":"Choose a color","isOther":true,"options":[{"label":"cobalt"},{"label":"amber"}]}]`), Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	prompts, _ := r.ParsedQuestions()
	d := &asyncSDKDispatcher{views: []processquestions.ExactQuestion{{QuestionID: r.ID, Delivery: "async", CanAnswer: true, State: agentquestion.StateWaiting, Prompts: prompts}}}
	c.questionDispatcher = d
	for _, phase := range []m.AgentPhase{m.PhaseOffline, m.PhaseRunning} {
		_, err = store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(reg *m.Registry) error { a, _ := reg.Agent(uid); a.Status.Phase = phase; return nil })
		if err != nil {
			t.Fatal(err)
		}
		list, err := c.ListQuestions(context.Background(), "uid:"+uid)
		if err != nil || len(list.Questions) != 1 || list.Questions[0].Delivery != "async" || list.Questions[0].CanAnswer || list.Questions[0].Prompts[0].Options[0].Label != "cobalt" || !list.Questions[0].Prompts[0].IsOther {
			t.Fatal(list, err)
		}
		before, _ := os.ReadFile(st.Path())
		text := "explicit answer"
		_, err = c.AnswerQuestion(context.Background(), AnswerQuestionRequest{Agent: "uid:" + uid, QuestionID: r.ID, Selections: []core.QuestionSelection{{Question: 1, Text: &text}}})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeQuestionNotPending {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(st.Path())
		if d.writes != 0 || !bytes.Equal(before, after) {
			t.Fatal("SDK answer effects")
		}
	}
}
