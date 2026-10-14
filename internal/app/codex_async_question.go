package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	retained := map[string]bool{}
	for _, q := range snap.AsyncQuestions {
		r := q.Request
		if r.Kind != "async-question" || r.Session != snap.Session || r.Connection != snap.Connection || r.Session == "" || r.Connection == "" || r.Turn == "" || q.CreatedAt.IsZero() {
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
			if _, known := (*records)[id]; known || q.Closed {
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
		} else if existing.Delivery != "async" || existing.AgentUID != b.Agent || existing.PaneUID != b.Pane || existing.SessionID != r.Session || existing.Generation != b.Generation || existing.RequestID != r.ID || existing.ToolUseID != r.Turn || !reflect.DeepEqual(existing.Questions, r.Input) {
			return processhost.ErrStale
		}
		if q.Closed || snap.State != "ready" || snap.Exit != nil {
			if _, err = store.Close(id, agentquestion.CloseReason("process-ended")); err != nil {
				return err
			}
		}
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

func asyncQuestionView(id string, r processhost.Request, store *agentquestion.Store) (ExactProcessQuestion, error) {
	record, found, err := store.Get(id)
	if err != nil {
		return ExactProcessQuestion{}, err
	}
	if !found {
		return ExactProcessQuestion{}, agentquestion.ErrNotFound
	}
	if record.Delivery != "async" {
		return ExactProcessQuestion{}, processhost.ErrStale
	}
	prompts, err := agentquestion.ParseCodexQuestions(r.Input)
	deadline := record.Deadline
	return ExactProcessQuestion{QuestionID: id, Policy: "projmux", Delivery: "async", CanAnswer: false, State: record.State, Deadline: &deadline, Prompts: prompts}, err
}

func (c *codexProcessControl) captureAsyncSnapshot(snap processhost.Snapshot) error {
	if err := captureAsyncQuestions(c.endpoint.binding, snap, &c.records, c.questions, c.questionWindow); err != nil {
		return err
	}
	for _, q := range snap.AsyncQuestions {
		record, found, err := c.questions.Get(codexAsyncQuestionID(c.endpoint.binding, q.Request))
		if err != nil {
			return err
		}
		if !found {
			return agentquestion.ErrNotFound
		}
		if err = c.endpoint.handle.RetainAsyncQuestion(c.endpoint.binding, q.Request, record.Deadline, record.State.Terminal()); err != nil {
			return err
		}
	}
	return nil
}
