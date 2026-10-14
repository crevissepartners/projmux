package processhost

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// AsyncQuestion is display metadata, never a native server request or answer token.
// Entries and their tombstones live for this handle only, bounded by Requests.
type AsyncQuestion struct {
	Request             Request
	CreatedAt, Deadline time.Time
	Sequence            uint64
	Closed              bool
}

func cloneAsyncQuestion(q AsyncQuestion) AsyncQuestion {
	q.Request = cloneRequest(q.Request)
	return q
}

func (c *codexAdapter) collectAsyncQuestionLocked(n codexappserver.Notification) (bool, error) {
	var event struct {
		Thread string `json:"threadId"`
		Turn   string `json:"turnId"`
		Item   struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Delivery  string          `json:"delivery"`
			Questions json.RawMessage `json:"questions"`
		} `json:"item"`
	}
	if json.Unmarshal(n.Params, &event) != nil {
		return false, agentquestion.ErrInvalidQuestions
	}
	if event.Item.Type != "agentMessage" || event.Item.Delivery != "async" {
		return false, nil
	}
	p := c.p
	if n.RequestID != "" || event.Thread != p.session || event.Turn == "" || len(event.Turn) > 256 || !strings.HasPrefix(event.Item.ID, "call_") || len(event.Item.ID) <= 5 || len(event.Item.ID) > 256 {
		return true, ErrStale
	}
	if len(n.Params) > p.host.limits.FrameBytes || len(event.Item.Questions) > agentquestion.MaxQuestionsBytes {
		return true, ErrBusy
	}
	var source []struct {
		Title   string   `json:"title"`
		Options []string `json:"options"`
	}
	if json.Unmarshal(event.Item.Questions, &source) != nil || len(source) == 0 {
		return true, agentquestion.ErrInvalidQuestions
	}
	prompts := make([]agentquestion.Question, 0, len(source))
	for i, q := range source {
		prompt := agentquestion.Question{ID: fmt.Sprintf("q%d", i+1), Question: q.Title, IsOther: true, Options: []agentquestion.Option{}}
		for _, label := range q.Options {
			prompt.Options = append(prompt.Options, agentquestion.Option{Label: label})
		}
		prompts = append(prompts, prompt)
	}
	raw, _ := json.Marshal(prompts)
	if _, err := agentquestion.ParseCodexQuestions(raw); err != nil {
		return true, err
	}
	request := Request{ID: event.Item.ID, Connection: p.connection, Session: event.Thread, Turn: event.Turn, Kind: "async-question", Tool: "agentMessage", Input: raw}
	// Deduplicate before current-turn admission: a completed item may be replayed
	// after its root turn ended, but it may never reopen or replace metadata.
	for _, q := range p.asyncQuestions {
		if q.Request.ID == request.ID && q.Request.Turn == request.Turn {
			if !reflect.DeepEqual(q.Request, request) {
				return true, ErrStale
			}
			return true, nil
		}
	}
	if p.state != "ready" || event.Turn != p.turn || p.connection == "" {
		return true, ErrStale
	}
	if len(p.asyncQuestions) >= p.host.limits.Requests {
		return true, ErrBusy
	}
	p.emitTurnLocked("async-question", nil, nil, "")
	p.asyncQuestions = append(p.asyncQuestions, AsyncQuestion{Request: request, CreatedAt: time.Now().UTC(), Sequence: p.seq})
	return true, nil
}

// RetainAsyncQuestion captures the app's deadline once. A retry cannot renew it.
// No input writer, request admission, or native response is involved.
func (c *CodexHandle) RetainAsyncQuestion(b Binding, request Request, deadline time.Time, terminal bool) error {
	p := c.handle
	if b != p.launch.Binding {
		return ErrStale
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.asyncQuestions {
		q := &p.asyncQuestions[i]
		if reflect.DeepEqual(q.Request, request) {
			if deadline.IsZero() || !deadline.After(q.CreatedAt) {
				return ErrStale
			}
			if !q.Deadline.IsZero() && !q.Deadline.Equal(deadline) {
				return ErrStale
			}
			if terminal && !q.Closed {
				q.Closed = true
				p.scheduleAsyncQuestionTimerLocked()
				p.changedLocked()
			}
			if q.Deadline.IsZero() {
				q.Deadline = deadline
				p.scheduleAsyncQuestionTimerLocked()
				p.changedLocked()
			}
			return nil
		}
	}
	return ErrStale
}

func (p *Handle) asyncSnapshotLocked() []AsyncQuestion {
	out := make([]AsyncQuestion, 0, len(p.asyncQuestions))
	for i := range p.asyncQuestions {
		q := &p.asyncQuestions[i]
		if p.state != "ready" || p.exit != nil || (!q.Deadline.IsZero() && !time.Now().Before(q.Deadline)) {
			q.Closed = true
		}
		out = append(out, cloneAsyncQuestion(*q))
	}
	return out
}

// One timer wakes the existing owner at the nearest captured deadline even
// when an idle provider has no blocking requests or further output. Timer
// generations fence callbacks already queued when stop/retention cancels one.
func (p *Handle) stopAsyncQuestionTimerLocked() {
	p.asyncTimerGeneration++
	if p.asyncQuestionTimer != nil {
		p.asyncQuestionTimer.Stop()
		p.asyncQuestionTimer = nil
	}
}

func (p *Handle) scheduleAsyncQuestionTimerLocked() {
	p.stopAsyncQuestionTimerLocked()
	if p.state != "ready" {
		return
	}
	var nearest time.Time
	for _, q := range p.asyncQuestions {
		if !q.Closed && !q.Deadline.IsZero() && (nearest.IsZero() || q.Deadline.Before(nearest)) {
			nearest = q.Deadline
		}
	}
	if nearest.IsZero() {
		return
	}
	generation := p.asyncTimerGeneration
	p.asyncQuestionTimer = time.AfterFunc(max(time.Until(nearest), 0), func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if generation != p.asyncTimerGeneration {
			return
		}
		p.asyncQuestionTimer = nil
		now := time.Now()
		for i := range p.asyncQuestions {
			q := &p.asyncQuestions[i]
			if !q.Deadline.IsZero() && !now.Before(q.Deadline) {
				q.Closed = true
			}
		}
		p.changedLocked()
		p.scheduleAsyncQuestionTimerLocked()
	})
}
