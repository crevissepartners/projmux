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
	st := agentquestion.NewStore(c.paths.StateDir).AsyncStore()
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
		if !errors.As(err, &refusal) || refusal.Code != CodeQuestionHostUnsupported {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(st.Path())
		if d.writes != 0 || !bytes.Equal(before, after) {
			t.Fatal("SDK answer effects")
		}
	}
}

type asyncSDKReceiptDispatcher struct {
	asyncSDKDispatcher
	receipt processquestions.AnswerReceipt
	err     error
}

func (d *asyncSDKReceiptDispatcher) SupportsAsyncQuestionAnswers() bool { return true }
func (d *asyncSDKReceiptDispatcher) AnswerExactProcessQuestionReceipt(context.Context, string, string, map[int]agentquestion.Selection) (processquestions.AnswerReceipt, error) {
	d.writes++
	return d.receipt, d.err
}

func TestSDKAsyncReceiptUnknownPreservesOpaqueIDAndConstants(t *testing.T) {
	if MaxAsyncQuestionTextBytes != 4096 || MaxAsyncQuestionSelectionsBytes != 16384 || MaxAsyncQuestionTextBytes != agentquestion.MaxAsyncQuestionTextBytes || MaxAsyncQuestionSelectionsBytes != agentquestion.MaxAsyncQuestionSelectionsBytes {
		t.Fatal("SDK bounds drift")
	}
	c, _, uid, _ := sdkStopFixture(t)
	if _, err := store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(reg *m.Registry) error {
		agent, _ := reg.Agent(uid)
		agent.Status.Phase = m.PhaseRunning
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	id := "question-0000000000000001"
	prompts := []agentquestion.Question{{ID: "q1", Question: "Explicit", IsOther: true}}
	d := &asyncSDKReceiptDispatcher{asyncSDKDispatcher: asyncSDKDispatcher{views: []processquestions.ExactQuestion{{QuestionID: id, Delivery: "async", State: agentquestion.StateWaiting, CanAnswer: true, Prompts: prompts}}}}
	c.questionDispatcher = d
	text := "explicit"
	request := AnswerQuestionRequest{Agent: "uid:" + uid, QuestionID: id, Selections: []core.QuestionSelection{{Question: 1, Text: &text}}}
	for _, outcome := range []string{"submitted", "unknown"} {
		d.receipt = processquestions.AnswerReceipt{QuestionID: id, Delivery: "async", Outcome: outcome}
		d.err = nil
		if outcome == "unknown" {
			d.receipt.Code = "question-delivery-unknown"
			d.err = &processquestions.DeliveryError{Receipt: d.receipt}
		}
		result, err := c.AnswerQuestion(context.Background(), request)
		if result.QuestionID != id || result.Delivery != "async" || result.Outcome != outcome {
			t.Fatal(result, err)
		}
		if outcome == "submitted" && err != nil {
			t.Fatal(err)
		}
		if outcome == "unknown" {
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeQuestionDeliveryUnknown {
				t.Fatal(err)
			}
		}
	}
}

type asyncPublicLegacyDispatcher struct {
	views []core.ExactQuestion
	calls int
}

func (d *asyncPublicLegacyDispatcher) ListQuestions(context.Context, string) ([]core.ExactQuestion, error) {
	return d.views, nil
}
func (d *asyncPublicLegacyDispatcher) AnswerQuestion(context.Context, string, string, []core.QuestionSelection) error {
	d.calls++
	return nil
}

type asyncPublicReceiptDispatcher struct {
	*asyncPublicLegacyDispatcher
	result     core.QuestionAnswerResult
	err        error
	selections []core.QuestionSelection
}

func (d *asyncPublicReceiptDispatcher) AnswerQuestionReceipt(_ context.Context, _ string, _ string, selections []core.QuestionSelection) (core.QuestionAnswerResult, error) {
	d.calls++
	d.selections = selections
	return d.result, d.err
}

func TestSDKOptionalPublicQuestionReceiptDispatcher(t *testing.T) {
	c, _, uid, _ := sdkStopFixture(t)
	if _, err := store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(reg *m.Registry) error { a, _ := reg.Agent(uid); a.Status.Phase = m.PhaseRunning; return nil }); err != nil {
		t.Fatal(err)
	}
	id := "question-0000000000000001"
	legacy := &asyncPublicLegacyDispatcher{views: []core.ExactQuestion{{QuestionID: id, Delivery: "async", CanAnswer: true, State: core.QuestionWaiting, Prompts: []core.ProviderQuestion{{ID: "q1", Question: "First", IsOther: true}, {ID: "q2", Question: "Second", Options: []core.ProviderQuestionOption{{Label: "cobalt"}}}}}}}
	opts := Options{ConfigDir: c.paths.ConfigDir, StateDir: c.paths.StateDir, HomeDir: c.paths.StateDir, QuestionDispatcher: legacy}
	var err error
	c, err = NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	text, label := " exact text ", "cobalt"
	request := AnswerQuestionRequest{Agent: "uid:" + uid, QuestionID: id, Selections: []core.QuestionSelection{{Question: 2, Option: &label}, {Question: 1, Text: &text}}}
	_, err = c.AnswerQuestion(context.Background(), request)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeQuestionHostUnsupported || legacy.calls != 0 {
		t.Fatal("legacy dispatcher admitted async", err)
	}
	receipt := &asyncPublicReceiptDispatcher{asyncPublicLegacyDispatcher: legacy, result: core.QuestionAnswerResult{QuestionID: id, Delivery: "async", Outcome: "submitted"}}
	opts.QuestionDispatcher = receipt
	c, err = NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.AnswerQuestion(context.Background(), request)
	if err != nil || result.QuestionID != id || result.Outcome != "submitted" || legacy.calls != 1 {
		t.Fatal(result, err)
	}
	if len(receipt.selections) != 2 || receipt.selections[0].Question != 1 || receipt.selections[0].Text == nil || *receipt.selections[0].Text != text || receipt.selections[1].Question != 2 || receipt.selections[1].Option == nil || *receipt.selections[1].Option != label {
		t.Fatal("normalization changed explicit answers", receipt.selections)
	}
	receipt.result.Outcome = "unknown"
	receipt.err = &Refusal{Code: CodeQuestionDeliveryUnknown, message: "uncertain delivery; do not resend"}
	result, err = c.AnswerQuestion(context.Background(), request)
	if !errors.As(err, &refusal) || refusal.Code != CodeQuestionDeliveryUnknown || result.QuestionID != id || result.Delivery != "async" || result.Outcome != "unknown" {
		t.Fatal(result, err)
	}
}
