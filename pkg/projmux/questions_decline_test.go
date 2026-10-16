package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type publicDeclineDispatcher struct {
	*asyncPublicLegacyDispatcher
	result core.QuestionDeclineResult
	err    error
}

func (d *publicDeclineDispatcher) DeclineQuestionReceipt(context.Context, string, string) (core.QuestionDeclineResult, error) {
	d.calls++
	return d.result, d.err
}

func TestSDKOptionalPublicQuestionDeclineReceiptDispatcher(t *testing.T) {
	c, _, uid, _ := sdkStopFixture(t)
	if _, err := store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(reg *m.Registry) error { a, _ := reg.Agent(uid); a.Status.Phase = m.PhaseRunning; return nil }); err != nil {
		t.Fatal(err)
	}
	id := "question-0000000000000001"
	st := agentquestion.NewStore(c.paths.StateDir).AsyncStore()
	if _, err := st.Create(agentquestion.Record{ID: id, AgentUID: uid, Provider: "codex", Delivery: "async", Questions: json.RawMessage(`[{"id":"q1","question":"Choose","isOther":true,"options":[]}]`), Deadline: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	legacy := &asyncPublicLegacyDispatcher{views: []core.ExactQuestion{{QuestionID: id, Delivery: "async", CanAnswer: true, CanDecline: true, State: core.QuestionWaiting, Prompts: []core.ProviderQuestion{{ID: "q1", Question: "Choose", IsOther: true}}}}}
	opts := Options{ConfigDir: c.paths.ConfigDir, StateDir: c.paths.StateDir, HomeDir: c.paths.StateDir, QuestionDispatcher: legacy}
	c, err := NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.DeclineQuestion(context.Background(), "uid:"+uid, id)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeQuestionDeclineUnsupported || legacy.calls != 0 || result.Outcome != "" {
		t.Fatal(result, err)
	}
	list, err := c.ListQuestions(context.Background(), "uid:"+uid)
	if err != nil || list.Questions[0].CanDecline {
		t.Fatal(list, err)
	}
	decline := &publicDeclineDispatcher{asyncPublicLegacyDispatcher: legacy, result: core.QuestionDeclineResult{QuestionID: id, Delivery: "async", Outcome: "submitted"}}
	opts.QuestionDispatcher = decline
	c, err = NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	list, err = c.ListQuestions(context.Background(), "uid:"+uid)
	if err != nil || !list.Questions[0].CanDecline || list.Questions[0].CanAnswer {
		t.Fatal("decline-only optional capability", list, err)
	}
	result, err = c.DeclineQuestion(context.Background(), "uid:"+uid, id)
	if err != nil || result.QuestionID != id || result.AgentUID != uid || result.Delivery != "async" || result.Outcome != "submitted" || legacy.calls != 1 {
		t.Fatal(result, err)
	}
	// An old host still has no decline capability after the dispatcher upgrades.
	legacy.views[0].CanDecline = false
	_, err = c.DeclineQuestion(context.Background(), "uid:"+uid, id)
	if !errors.As(err, &refusal) || refusal.Code != CodeQuestionDeclineUnsupported || legacy.calls != 1 {
		t.Fatal(err, legacy.calls)
	}
}

func TestSDKDeclineReceiptCauseAndCancellation(t *testing.T) {
	c, _, uid, _ := sdkStopFixture(t)
	if _, err := store.NewStore(store.PathFor(c.paths.StateDir)).Update(func(reg *m.Registry) error { a, _ := reg.Agent(uid); a.Status.Phase = m.PhaseRunning; return nil }); err != nil {
		t.Fatal(err)
	}
	id := "question-0000000000000001"
	legacy := &asyncPublicLegacyDispatcher{views: []core.ExactQuestion{{QuestionID: id, Delivery: "async", CanDecline: true, State: core.QuestionWaiting, Prompts: []core.ProviderQuestion{{ID: "q1", Question: "Choose", IsOther: true}}}}}
	d := &publicDeclineDispatcher{asyncPublicLegacyDispatcher: legacy, result: core.QuestionDeclineResult{QuestionID: id, Delivery: "async", Outcome: "unknown"}}
	var err error
	c, err = NewClient(Options{ConfigDir: c.paths.ConfigDir, StateDir: c.paths.StateDir, HomeDir: c.paths.StateDir, QuestionDispatcher: d})
	if err != nil {
		t.Fatal(err)
	}
	pathCause := &os.PathError{Op: "read", Path: "fixture", Err: io.ErrUnexpectedEOF}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.EOF, pathCause} {
		for _, typed := range []bool{false, true} {
			before := d.calls
			d.err = fmt.Errorf("fixture: %w", cause)
			if typed {
				d.err = &Refusal{Code: CodeQuestionDeliveryUnknown, message: "uncertain notice; do not resend", cause: d.err}
			}
			result, err := c.DeclineQuestion(context.Background(), "uid:"+uid, id)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeQuestionDeliveryUnknown || !errors.Is(err, cause) || result.QuestionID != id || result.Delivery != "async" || result.Outcome != "unknown" || d.calls != before+1 {
				t.Fatal(result, err, d.calls)
			}
			if cause == pathCause {
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) || pathErr != pathCause {
					t.Fatal("typed cause lost", err)
				}
			}
		}
	}
	before := d.calls
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := c.DeclineQuestion(ctx, "uid:"+uid, id)
	if err != context.Canceled || result != (core.QuestionDeclineResult{}) || d.calls != before {
		t.Fatal(result, err)
	}
}
