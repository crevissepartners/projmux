package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// This bridge projects only the host's exact stream requests. Hooks never
// register requests; existing stores own answer windows and terminal states.
type codexProcessControl struct {
	mu                             sync.Mutex
	endpoint                       *codexProcessEndpoint
	questions                      *agentquestion.Store
	approvals                      *agentapproval.Store
	questionWindow, approvalWindow time.Duration
	now                            func() time.Time
	records                        map[string]processhost.Request
}

func processCodexControlID(b processhost.Binding, r processhost.Request) string {
	raw, _ := json.Marshal(struct {
		Binding             processhost.Binding
		Connection, Request string
	}{b, r.Connection, r.ID})
	sum := sha256.Sum256(raw)
	prefix := "permission-"
	if r.Kind == "question" {
		prefix = "question-"
	}
	return prefix + hex.EncodeToString(sum[:8])
}
func (c *codexProcessControl) closeRecord(id string, r processhost.Request) {
	if r.Kind == "question" {
		_, _ = c.questions.Close(id, agentquestion.CloseReasonTurnEnded)
	} else {
		_, _ = c.approvals.Close(id, agentapproval.CloseReasonCanceled)
	}
}
func (c *codexProcessControl) sync(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.endpoint == nil || c.questions == nil || c.approvals == nil || c.now == nil || c.questionWindow <= 0 || c.approvalWindow <= 0 {
		return errors.New("incomplete codex process control")
	}
	if c.records == nil {
		c.records = make(map[string]processhost.Request)
	}
	e := c.endpoint
	if _, err := e.route(ctx); err != nil {
		for id, r := range c.records {
			c.closeRecord(id, r)
			_ = e.handle.Expire(e.authority(), r)
			delete(c.records, id)
		}
		return err
	}
	snap, err := e.handle.Observe(e.binding)
	if err != nil {
		return err
	}
	pending := make(map[string]bool, len(snap.Pending))
	for _, r := range snap.Pending {
		id := processCodexControlID(e.binding, r)
		pending[id] = true
		if _, known := c.records[id]; !known {
			if err = c.create(id, r); err != nil {
				return err
			}
			c.records[id] = r
		}
		if err = c.answer(ctx, id, r); err != nil && !errors.Is(err, processhost.ErrStale) {
			return err
		}
	}
	for id, r := range c.records {
		if !pending[id] {
			c.closeRecord(id, r)
			delete(c.records, id)
		}
	}
	return nil
}
func (c *codexProcessControl) create(id string, r processhost.Request) error {
	var n codexappserver.Notification
	if json.Unmarshal(r.Input, &n) != nil {
		return errors.New("invalid codex process request")
	}
	created := c.now().UTC()
	e := c.endpoint
	if r.Kind == "question" {
		var params struct {
			Questions json.RawMessage `json:"questions"`
		}
		if json.Unmarshal(n.Params, &params) != nil {
			return errors.New("invalid codex question input")
		}
		_, err := c.questions.Create(agentquestion.Record{ID: id, Provider: "codex", AgentUID: e.binding.Agent, PaneUID: e.binding.Pane, SessionID: r.Session, Generation: e.binding.Generation, RequestID: r.ID, Questions: params.Questions, CreatedAt: created, Deadline: created.Add(c.questionWindow)})
		return err
	}
	_, err := c.approvals.Create(agentapproval.Record{ID: id, AgentUID: e.binding.Agent, PaneUID: e.binding.Pane, SessionID: r.Session, ToolName: r.Tool, ToolInput: n.Params, CreatedAt: created, Deadline: created.Add(c.approvalWindow)})
	if errors.Is(err, agentapproval.ErrCommittedNotSynced) {
		return nil
	}
	return err
}
func (c *codexProcessControl) answer(ctx context.Context, id string, r processhost.Request) error {
	e := c.endpoint
	if r.Kind == "question" {
		record, found, err := c.questions.Get(id)
		if err != nil {
			return err
		}
		if !found {
			return agentquestion.ErrNotFound
		}
		if record.State == agentquestion.StateAnswered {
			selections, err := processCodexSelections(record)
			if err != nil {
				return err
			}
			return e.handle.RespondQuestion(ctx, e.authority(), r, selections)
		}
		if record.State.Terminal() {
			return e.handle.Expire(e.authority(), r)
		}
		return nil
	}
	record, found, err := c.approvals.Get(id)
	if err != nil {
		return err
	}
	if !found {
		return agentapproval.ErrNotFound
	}
	if record.State == agentapproval.StateAllowed || record.State == agentapproval.StateDenied {
		var n codexappserver.Notification
		if err = json.Unmarshal(r.Input, &n); err != nil {
			return err
		}
		envelope, ok, err := codexappserver.DecodeApprovalEnvelope(n)
		if err != nil {
			return err
		}
		if !ok {
			return processhost.ErrStale
		}
		decision, ok := codexPermissionDecision(record.State == agentapproval.StateAllowed, envelope.Decisions)
		if !ok {
			return processhost.ErrStale
		}
		return e.handle.RespondApproval(ctx, e.authority(), r, decision)
	}
	if record.State != agentapproval.StateWaiting {
		return e.handle.Expire(e.authority(), r)
	}
	return nil
}
func processCodexSelections(record agentquestion.Record) (map[int]agentquestion.Selection, error) {
	questions, err := record.ParsedQuestions()
	if err != nil {
		return nil, err
	}
	selections := make(map[int]agentquestion.Selection, len(questions))
	for i, q := range questions {
		var values []string
		if json.Unmarshal([]byte(record.Answers[q.ID]), &values) != nil || len(values) == 0 {
			return nil, agentquestion.ErrInvalidAnswer
		}
		labels := len(q.Options) > 0 && slices.ContainsFunc(q.Options, func(o agentquestion.Option) bool { return o.Label == values[0] })
		if labels {
			selections[i] = agentquestion.Selection{Labels: values}
		} else if len(values) == 1 {
			selections[i] = agentquestion.Selection{Text: values[0], HasText: true}
		} else {
			return nil, agentquestion.ErrInvalidAnswer
		}
	}
	return selections, nil
}
