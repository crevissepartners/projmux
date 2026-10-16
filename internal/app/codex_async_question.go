package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func codexAsyncQuestionID(b processhost.Binding, r processhost.Request) string {
	raw, _ := json.Marshal(struct {
		Binding                         processhost.Binding
		Connection, Session, Turn, Item string
	}{b, r.Connection, r.Session, r.Turn, r.ID})
	sum := sha256.Sum256(raw)
	return "question-" + hex.EncodeToString(sum[:8])
}

// Snapshot retention is independent of the output ring and blocking Pending.
// Durable records capture the deadline once and terminal records never reopen.
func captureAsyncQuestions(b processhost.Binding, snap processhost.Snapshot, records *map[string]processhost.Request, store *agentquestion.Store, window time.Duration) error {
	if store == nil || window <= 0 || snap.Binding != b {
		return processhost.ErrStale
	}
	if *records == nil {
		*records = map[string]processhost.Request{}
	}
	if len(snap.AsyncQuestions) == 0 {
		known := false
		for _, r := range *records {
			if r.Kind == "async-question" {
				known = true
				break
			}
		}
		if !known {
			return nil
		}
	}
	store = store.AsyncStore()
	retained := map[string]bool{}
	for i := range snap.AsyncQuestions {
		q := &snap.AsyncQuestions[i]
		r := q.Request
		if r.Kind != "async-question" || r.Session != snap.Session || r.Connection != snap.Connection || r.Session == "" || r.Connection == "" || r.Turn == "" || q.CreatedAt.IsZero() || (!q.Deadline.IsZero() && !q.Deadline.After(q.CreatedAt)) {
			return processhost.ErrStale
		}
		if _, err := agentquestion.ParseCodexQuestions(r.Input); err != nil {
			return err
		}
		id := codexAsyncQuestionID(b, r)
		retained[id] = true
		existing, found, err := store.Get(id)
		if err != nil {
			return err
		}
		if !found {
			if q.Closed || (!q.Deadline.IsZero() && !time.Now().Before(q.Deadline)) {
				delete(*records, id)
				continue
			}
			if _, known := (*records)[id]; known {
				return agentquestion.ErrNotFound
			}
			deadline := q.Deadline
			if deadline.IsZero() {
				deadline = q.CreatedAt.Add(window)
			}
			existing, err = store.Create(agentquestion.Record{ID: id, Provider: aiModeCodex, Delivery: "async", AgentUID: b.Agent, PaneUID: b.Pane, SessionID: r.Session, Generation: b.Generation, RequestID: r.ID, ToolUseID: r.Turn, Questions: r.Input, CreatedAt: q.CreatedAt, Deadline: deadline})
			if err != nil {
				return err
			}
		} else if existing.Delivery != "async" || existing.AgentUID != b.Agent || existing.PaneUID != b.Pane || existing.SessionID != r.Session || existing.Generation != b.Generation || existing.RequestID != r.ID || existing.ToolUseID != r.Turn || !existing.CreatedAt.Equal(q.CreatedAt) || !reflect.DeepEqual(existing.Questions, r.Input) {
			return processhost.ErrStale
		}
		if !existing.Deadline.After(q.CreatedAt) || (!q.Deadline.IsZero() && !q.Deadline.Equal(existing.Deadline)) {
			return processhost.ErrStale
		}
		// Observe returns a deep clone. Keep the first captured deadline and
		// observed terminal state in this caller's copy for subsequent Gets.
		q.Deadline = existing.Deadline
		observedTerminal := existing.State.Terminal()
		if q.Closed || snap.State != "ready" || snap.Exit != nil {
			q.Closed = true
			reason := agentquestion.CloseReason("process-ended")
			if q.Closed && snap.State == "ready" && snap.Exit == nil && time.Now().Before(existing.Deadline) {
				reason = "async-not-submitted"
			}
			if _, err = store.Close(id, reason); errors.Is(err, agentquestion.ErrNotFound) {
				delete(*records, id)
				continue
			} else if err != nil {
				return err
			}
		}
		q.Closed = q.Closed || observedTerminal
		(*records)[id] = r
	}
	all, err := store.List(b.Agent)
	if err != nil {
		return err
	}
	for _, record := range all {
		// A retiring controller owns only its generation and endpoint. A new
		// controller may already have registered questions for the same Agent.
		if record.Generation != b.Generation || record.PaneUID != b.Pane || record.SessionID != snap.Session {
			continue
		}
		if record.Delivery == "async" && (!retained[record.ID] || snap.State != "ready" || snap.Exit != nil) {
			if _, err = store.Close(record.ID, agentquestion.CloseReason("process-stale")); err != nil {
				return err
			}
			delete(*records, record.ID)
		}
	}
	return nil
}

func asyncQuestionView(id string, r processhost.Request, store *agentquestion.Store, snap *processhost.Snapshot) (ExactProcessQuestion, error) {
	var proof *processhost.AsyncQuestion
	if snap != nil {
		if r.Session != snap.Session || r.Connection != snap.Connection || codexAsyncQuestionID(snap.Binding, r) != id {
			return ExactProcessQuestion{}, processhost.ErrStale
		}
		for i := range snap.AsyncQuestions {
			if reflect.DeepEqual(snap.AsyncQuestions[i].Request, r) {
				proof = &snap.AsyncQuestions[i]
				break
			}
		}
		if proof == nil {
			return ExactProcessQuestion{}, processhost.ErrStale
		}
		if proof.CreatedAt.IsZero() || (!proof.Deadline.IsZero() && !proof.Deadline.After(proof.CreatedAt)) {
			return ExactProcessQuestion{}, processhost.ErrStale
		}
	}
	record, found, err := store.AsyncStore().Get(id)
	if err != nil {
		return ExactProcessQuestion{}, err
	}
	if !found {
		if proof != nil && (proof.Closed || (!proof.Deadline.IsZero() && !time.Now().Before(proof.Deadline))) {
			return ExactProcessQuestion{}, nil
		}
		return ExactProcessQuestion{}, agentquestion.ErrNotFound
	}
	if record.Delivery != "async" || record.SessionID != r.Session || record.RequestID != r.ID || record.ToolUseID != r.Turn || !reflect.DeepEqual(record.Questions, r.Input) {
		return ExactProcessQuestion{}, processhost.ErrStale
	}
	if snap != nil && (record.AgentUID != snap.Binding.Agent || record.PaneUID != snap.Binding.Pane || record.Generation != snap.Binding.Generation || !record.CreatedAt.Equal(proof.CreatedAt) || (!proof.Deadline.IsZero() && !record.Deadline.Equal(proof.Deadline))) {
		return ExactProcessQuestion{}, processhost.ErrStale
	}
	prompts, err := agentquestion.ParseCodexQuestions(r.Input)
	deadline := record.Deadline
	canAnswer := snap != nil && snap.State == "ready" && snap.Exit == nil && proof != nil && !proof.Closed && record.State == agentquestion.StateWaiting
	canDecline := canAnswer && err == nil
	for _, prompt := range prompts {
		if prompt.IsSecret {
			canDecline = false
		}
	}
	return ExactProcessQuestion{QuestionID: id, Policy: "projmux", Delivery: "async", CanAnswer: canAnswer, CanDecline: canDecline, State: record.State, Deadline: &deadline, Prompts: prompts}, err
}

func (c *codexProcessControl) captureAsyncSnapshot(snap processhost.Snapshot) error {
	if err := captureAsyncQuestions(c.endpoint.binding, snap, &c.records, c.questions, c.questionWindow); err != nil {
		return err
	}
	for _, q := range snap.AsyncQuestions {
		id := codexAsyncQuestionID(c.endpoint.binding, q.Request)
		record, found, err := c.questions.AsyncStore().Get(id)
		if err != nil {
			return err
		}
		if !found {
			if q.Closed || (!q.Deadline.IsZero() && !time.Now().Before(q.Deadline)) {
				if !q.Deadline.IsZero() {
					if err = c.endpoint.handle.RetainAsyncQuestion(c.endpoint.binding, q.Request, q.Deadline, true); err != nil {
						return err
					}
				}
				delete(c.records, id)
				continue
			}
			return agentquestion.ErrNotFound
		}
		if record.Delivery != "async" || record.AgentUID != c.endpoint.binding.Agent || record.PaneUID != c.endpoint.binding.Pane || record.Generation != c.endpoint.binding.Generation || record.SessionID != q.Request.Session || record.RequestID != q.Request.ID || record.ToolUseID != q.Request.Turn || !record.CreatedAt.Equal(q.CreatedAt) || !record.Deadline.Equal(q.Deadline) || !reflect.DeepEqual(record.Questions, q.Request.Input) {
			return processhost.ErrStale
		}
		if err = c.endpoint.handle.RetainAsyncQuestion(c.endpoint.binding, q.Request, record.Deadline, record.State.Terminal()); err != nil {
			return err
		}
	}
	return nil
}
