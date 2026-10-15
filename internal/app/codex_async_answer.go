package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
)

// The callback keeps its legacy signature. This local carrier lets its caller
// extract a redacted optional receipt, including a successful async submission.
type asyncAnswerError struct {
	receipt processquestions.AnswerReceipt
	cause   error
}

func (e *asyncAnswerError) Error() string { return e.receipt.Code + ": " + e.receipt.Message }
func (e *asyncAnswerError) Unwrap() error { return e.cause }

// answerAsyncQuestion is called under the controller lock after exact routing.
func (c *codexProcessControl) answerAsyncQuestion(ctx context.Context, r processQuestionRequest, snap processhost.Snapshot) ([]ExactProcessQuestion, error) {
	receipt := processquestions.AnswerReceipt{QuestionID: r.QuestionID, Delivery: "async"}
	refuse := func(code string, err error) ([]ExactProcessQuestion, error) {
		receipt.Code = code
		if err != nil {
			receipt.Message = err.Error()
		}
		return nil, &asyncAnswerError{receipt: receipt, cause: err}
	}
	request, ok := c.records[r.QuestionID]
	if !ok || request.Kind != "async-question" {
		return refuse("question-not-found", agentquestion.ErrNotFound)
	}
	view, err := asyncQuestionView(r.QuestionID, request, c.questions, &snap)
	if err != nil {
		return refuse("question-stale", err)
	}
	store := c.questions.AsyncStore()
	record, found, err := store.Get(r.QuestionID)
	if err != nil {
		return refuse("question-stale", err)
	}
	if !found || record.AgentUID != r.Binding.Agent {
		return refuse("question-not-found", agentquestion.ErrNotFound)
	}
	if record.State == agentquestion.StateExpired {
		return refuse("question-expired", agentquestion.ErrExpired)
	}
	if record.State != agentquestion.StateWaiting {
		if record.Disposition == "async-submitted" {
			receipt.Outcome = "submitted"
		}
		if record.Disposition == "async-unknown" {
			receipt.Outcome = "unknown"
		}
		return refuse("question-not-pending", agentquestion.ErrNotPending)
	}
	if !view.CanAnswer || snap.State != "ready" || snap.Exit != nil {
		return refuse("question-closed", processhost.ErrClosed)
	}
	if !time.Now().Before(record.Deadline) {
		return refuse("question-expired", agentquestion.ErrExpired)
	}
	if code, err := agentquestion.ValidateAsyncSelections(view.Prompts, r.Selections); err != nil {
		return refuse(code, err)
	}
	answers, _ := agentquestion.BuildCodexAnswers(view.Prompts, r.Selections)
	// Structured ordinary user text preserves original item/prompt/answer linkage.
	// It is never persisted as a question answer or sent as a peer envelope.
	raw, err := json.Marshal(struct {
		Item      string                   `json:"itemId"`
		Questions []agentquestion.Question `json:"questions"`
		Answers   map[string]string        `json:"answers"`
	}{request.ID, view.Prompts, answers})
	if err != nil {
		return refuse("question-invalid-answer", err)
	}
	prompt := "Operator answers to the original async question set:\n" + string(raw)
	if _, err = localipc.MarshalJSON(struct{ Questions *processQuestionRequest }{&r}); err != nil {
		return refuse("question-answer-too-large", err)
	}
	authority := c.endpoint.authority()
	operation := "async-" + r.QuestionID
	if err = c.endpoint.handle.ValidateUserTurnFrame(ctx, authority, operation, prompt); err != nil {
		code := "question-stale"
		if errors.Is(err, agentquestion.ErrCapacity) {
			code = "question-answer-too-large"
		}
		if errors.Is(err, processhost.ErrClosed) {
			code = "question-closed"
		}
		return refuse(code, err)
	}
	if err = ctx.Err(); err != nil {
		return refuse("question-stale", err)
	}
	// Sticky in-handle terminal state precedes the durable claim: an old writer
	// can prune terminal history even before a seven-day captured deadline.
	if err = c.endpoint.handle.RetainAsyncQuestion(r.Binding, request, record.Deadline, true); err != nil {
		return refuse("question-stale", err)
	}
	claim, err := store.BeginAsync(record)
	if err != nil {
		// A failed fsync may follow a committed rename. Settle that exact claim
		// when present; otherwise close the original waiting row without input.
		if claim.ID != "" {
			err = errors.Join(err, store.FinishAsync(claim, "not-submitted"))
		}
		_, closeErr := store.Close(record.ID, agentquestion.CloseReason("async-not-submitted"))
		err = errors.Join(err, closeErr, c.attention.sync(c.endpoint.handle, r.Binding))
		receipt.Outcome = "not-submitted"
		code := "question-closed"
		if errors.Is(err, agentquestion.ErrBusy) {
			code = "question-busy"
		}
		return refuse(code, err)
	}
	delivery, deliveryErr := c.endpoint.handle.DeliverUserTurn(ctx, authority, operation, prompt)
	outcome := "submitted"
	if deliveryErr != nil {
		outcome = "unknown"
		if !delivery.WriteAttempted {
			outcome = "not-submitted"
		}
	}
	attentionErr := c.attention.sync(c.endpoint.handle, r.Binding)
	if attentionErr != nil && outcome == "submitted" {
		outcome = "unknown"
	}
	settleErr := store.FinishAsync(claim, outcome)
	if outcome == "not-submitted" {
		receipt.Outcome = outcome
		code := "question-closed"
		if errors.Is(deliveryErr, processhost.ErrBusy) {
			code = "question-busy"
		}
		return refuse(code, errors.Join(deliveryErr, settleErr, attentionErr))
	}
	if deliveryErr != nil || settleErr != nil || attentionErr != nil {
		receipt.Outcome = "unknown"
		// Lost settlement or projection is uncertainty, never permission to resend.
		return refuse("question-delivery-unknown", errors.Join(deliveryErr, settleErr, attentionErr))
	}
	receipt.Outcome = "submitted"
	return nil, &asyncAnswerError{receipt: receipt}
}
