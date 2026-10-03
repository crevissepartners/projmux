package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// claudeProcessControl projects stream tokens into the existing answer domains.
// Hooks never enter these stores for process launches. The caller supplies the
// existing configured windows; this seam neither selects policy nor opens UI.
type claudeProcessControl struct {
	mu                             sync.Mutex
	handle                         *processhost.Handle
	binding                        processhost.Binding
	questions                      *agentquestion.Store
	approvals                      *agentapproval.Store
	questionWindow, approvalWindow time.Duration
	now                            func() time.Time
	records                        map[string]processhost.Request
}

func processClaudeControlID(b processhost.Binding, r processhost.Request) string {
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

// sync reconciles one bounded snapshot. All response writes go through the
// Handle's exact pending token, including concurrent callers and stale answers.
func (c *claudeProcessControl) sync(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handle == nil || c.questions == nil || c.approvals == nil || c.now == nil || c.questionWindow <= 0 || c.approvalWindow <= 0 {
		return errors.New("incomplete Claude process control")
	}
	snap, err := c.handle.Observe(c.binding)
	if err != nil {
		return err
	}
	authority := processhost.Authority{Binding: c.binding, Connection: snap.Connection, Session: snap.Session}
	if c.records == nil {
		c.records = make(map[string]processhost.Request)
	}
	if err = c.handle.ValidateAuthority(ctx, authority); err != nil {
		for id, request := range c.records {
			c.closeRecord(id, request)
			_ = c.handle.Expire(authority, request)
			delete(c.records, id)
		}
		return err
	}
	pending := make(map[string]bool, len(snap.Pending))
	for _, request := range snap.Pending {
		id := processClaudeControlID(c.binding, request)
		pending[id] = true
		if _, known := c.records[id]; !known {
			created := c.now().UTC()
			if request.Kind == "question" {
				var input struct {
					Questions json.RawMessage `json:"questions"`
				}
				if json.Unmarshal(request.Input, &input) != nil {
					return errors.New("invalid Claude question input")
				}
				_, err = c.questions.Create(agentquestion.Record{ID: id, AgentUID: c.binding.Agent, PaneUID: c.binding.Pane, SessionID: request.Session,
					Generation: c.binding.Generation, RequestID: request.ID, Questions: input.Questions, CreatedAt: created, Deadline: created.Add(c.questionWindow)})
			} else {
				_, err = c.approvals.Create(agentapproval.Record{ID: id, AgentUID: c.binding.Agent, PaneUID: c.binding.Pane, SessionID: request.Session,
					ToolName: request.Tool, ToolInput: request.Input, CreatedAt: created, Deadline: created.Add(c.approvalWindow)})
				if errors.Is(err, agentapproval.ErrCommittedNotSynced) {
					err = nil
				}
			}
			if err != nil {
				return err
			}
			c.records[id] = request
		}
		var response *processhost.Response
		terminal := false
		if request.Kind == "question" {
			record, found, readErr := c.questions.Get(id)
			if readErr != nil {
				return readErr
			}
			if !found {
				return agentquestion.ErrNotFound
			}
			terminal = record.State.Terminal()
			if record.State == agentquestion.StateAnswered {
				response = &processhost.Response{Answers: record.Answers}
			}
		} else {
			record, found, readErr := c.approvals.Get(id)
			if readErr != nil {
				return readErr
			}
			if !found {
				return agentapproval.ErrNotFound
			}
			terminal = record.State != agentapproval.StateWaiting
			switch record.State {
			case agentapproval.StateAllowed:
				response = &processhost.Response{Allow: true}
			case agentapproval.StateDenied:
				response = &processhost.Response{Deny: claudePermissionDenyMessage}
			}
		}
		if response != nil {
			if err = c.handle.Respond(ctx, authority, request, *response); err != nil && !errors.Is(err, processhost.ErrStale) {
				return err
			}
		} else if terminal {
			_ = c.handle.Expire(authority, request)
		}
	}
	for id, request := range c.records {
		if !pending[id] || snap.State != "ready" {
			c.closeRecord(id, request)
			delete(c.records, id)
		}
	}
	return nil
}

func (c *claudeProcessControl) closeRecord(id string, request processhost.Request) {
	if request.Kind == "question" {
		_, _ = c.questions.Close(id, agentquestion.CloseReasonHookCanceled)
	} else {
		_, _ = c.approvals.Close(id, agentapproval.CloseReasonCanceled)
	}
}
