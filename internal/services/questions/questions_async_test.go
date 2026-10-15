package questions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	r, err := st.AsyncStore().Create(agentquestion.Record{ID: "question-0000000000000001", Provider: "codex", Delivery: "async", AgentUID: "agent", PaneUID: "pane", SessionID: "thread", Generation: "generation", Questions: json.RawMessage(`[{"id":"q1","question":"Choose","isOther":true,"options":[{"label":"cobalt"},{"label":"amber"}]}]`), Deadline: time.Now().Add(time.Minute)})
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
		before, _ := os.ReadFile(st.AsyncStore().Path())
		if _, err = service.Answer(context.Background(), Request{Spelling: "agent question answer", QuestionID: r.ID, Texts: []string{"1=answer"}}, a); err == nil {
			t.Fatal("answer accepted")
		}
		after, _ := os.ReadFile(st.AsyncStore().Path())
		if !bytes.Equal(before, after) || dispatcher.answers != 0 {
			t.Fatal("answer effects")
		}
	}
}

func TestAsyncServiceMergedNamespacesAndReadFailures(t *testing.T) {
	st := agentquestion.NewStore(t.TempDir())
	now := time.Now().UTC()
	legacy, err := st.Create(agentquestion.Record{ID: "question-0000000000000001", Provider: "codex", AgentUID: "agent", Questions: json.RawMessage(`[{"id":"q1","question":"Legacy","options":[]}]`), Deadline: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	async, err := st.AsyncStore().Create(agentquestion.Record{ID: "question-0000000000000002", Provider: "codex", Delivery: "async", AgentUID: "agent", Questions: json.RawMessage(`[{"id":"q1","question":"Async","isOther":true,"options":[]}]`), Deadline: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	a := m.Agent{Metadata: m.ObjectMeta{UID: "agent"}, Status: m.AgentStatus{Phase: m.PhaseOffline}}
	service := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, LoadRegistry: func() (m.Registry, error) { return m.Registry{Agents: []m.Agent{a}}, nil }}
	list, err := service.List(context.Background(), a)
	if err != nil || len(list.Questions) != 2 || list.Questions[0].ID != legacy.ID || list.Questions[1].ID != async.ID || list.Questions[1].CanAnswer {
		t.Fatal("namespace merge", list, err)
	}
	after, _ := os.ReadFile(st.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("async touched legacy")
	}
	for _, bad := range []*agentquestion.Store{st, st.AsyncStore()} {
		original, err := os.ReadFile(bad.Path())
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(bad.Path(), []byte("malformed"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = service.List(context.Background(), a); !errors.Is(err, agentquestion.ErrMalformedStore) {
			t.Fatal("malformed namespace hidden", err)
		}
		if _, err = service.Answer(context.Background(), Request{Spelling: "agent question answer", QuestionID: async.ID, Texts: []string{"1=answer"}}, a); !errors.Is(err, agentquestion.ErrMalformedStore) {
			t.Fatal("answer ignored namespace error", err)
		}
		if err = os.WriteFile(bad.Path(), original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Reading a missing async namespace must not create its directory or lock.
	empty := agentquestion.NewStore(t.TempDir())
	service.OpenStore = func() (*agentquestion.Store, error) { return empty, nil }
	if _, err = service.List(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(empty.AsyncStore().Path())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created async namespace", err)
	}
}

type asyncReceiptServiceDispatcher struct {
	asyncReadDispatcher
	receipt processquestions.AnswerReceipt
	err     error
}

func (d *asyncReceiptServiceDispatcher) SupportsAsyncQuestionAnswers() bool { return true }
func (d *asyncReceiptServiceDispatcher) AnswerExactProcessQuestionReceipt(context.Context, string, string, map[int]agentquestion.Selection) (processquestions.AnswerReceipt, error) {
	d.answers++
	return d.receipt, d.err
}

func TestAsyncServiceReceiptAndUnknownResult(t *testing.T) {
	st := agentquestion.NewStore(t.TempDir())
	r, err := st.AsyncStore().Create(agentquestion.Record{ID: "question-0000000000000001", Provider: "codex", Delivery: "async", AgentUID: "agent", Questions: json.RawMessage(`[{"id":"q1","question":"Choose","isOther":true,"options":[]}]`), Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	prompts, _ := r.ParsedQuestions()
	a := m.Agent{Metadata: m.ObjectMeta{UID: "agent", Name: "fixture"}, Spec: m.AgentSpec{Provider: "codex"}, Status: m.AgentStatus{PaneRef: "pane", Phase: m.PhaseRunning}}
	p := m.Pane{Metadata: m.ObjectMeta{UID: "pane"}, Spec: m.PaneSpec{Runtime: m.PaneRuntimeSpec{Kind: m.RuntimeProcess}}}
	d := &asyncReceiptServiceDispatcher{asyncReadDispatcher: asyncReadDispatcher{views: []processquestions.ExactQuestion{{QuestionID: r.ID, Delivery: "async", CanAnswer: true, State: agentquestion.StateWaiting, Prompts: prompts}}}}
	service := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, LoadRegistry: func() (m.Registry, error) { return m.Registry{Agents: []m.Agent{a}, Panes: []m.Pane{p}}, nil }, Dispatcher: d}
	list, err := service.List(context.Background(), a)
	if err != nil || !list.Questions[0].CanAnswer {
		t.Fatal(list, err)
	}
	for _, outcome := range []string{"submitted", "unknown"} {
		d.receipt = processquestions.AnswerReceipt{QuestionID: r.ID, Delivery: "async", Outcome: outcome}
		d.err = nil
		if outcome == "unknown" {
			d.receipt.Code = "question-delivery-unknown"
			d.err = &processquestions.DeliveryError{Receipt: d.receipt}
		}
		result, err := service.Answer(context.Background(), Request{Spelling: "agent question answer", QuestionID: r.ID, Texts: []string{"1=explicit"}}, a)
		if result.QuestionID != r.ID || result.Delivery != "async" || result.Outcome != outcome {
			t.Fatal(result, err)
		}
		if outcome == "submitted" && err != nil {
			t.Fatal(err)
		}
		if outcome == "unknown" {
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != "question-delivery-unknown" || strings.Contains(err.Error(), "nothing was changed") {
				t.Fatal(err)
			}
		}
	}
}

func TestAsyncServiceFreshRunningAndLifetimeFence(t *testing.T) {
	for _, change := range []string{"already-offline", "offline", "removed", "pane", "generation", "thread", "connection", "owner", "registry-error"} {
		t.Run(change, func(t *testing.T) {
			st := agentquestion.NewStore(t.TempDir())
			a := m.Agent{Metadata: m.ObjectMeta{UID: "agent"}, Spec: m.AgentSpec{Provider: "codex"}, Status: m.AgentStatus{PaneRef: "pane", Phase: m.PhaseRunning}}
			p := m.Pane{Metadata: m.ObjectMeta{UID: "pane"}, Spec: m.PaneSpec{Runtime: m.PaneRuntimeSpec{Kind: m.RuntimeProcess}}, Status: m.PaneStatus{ProcessSession: &m.ProcessSessionRecord{Provider: "codex", Binding: m.ProcessBinding{Generation: "first"}, ThreadID: "thread", ConnectionID: "connection"}}}
			if change == "already-offline" {
				a.Status.Phase = m.PhaseOffline
			}
			initial := m.Registry{Agents: []m.Agent{a}, Panes: []m.Pane{p}}
			reads := 0
			d := &asyncReceiptServiceDispatcher{asyncReadDispatcher: asyncReadDispatcher{views: []processquestions.ExactQuestion{{QuestionID: "question-0000000000000001", Delivery: "async", CanAnswer: true, State: agentquestion.StateWaiting, Prompts: []agentquestion.Question{{ID: "q1", Question: "Explicit", IsOther: true}}}}}}
			service := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, Dispatcher: d, LoadRegistry: func() (m.Registry, error) {
				reads++
				reg := initial.Clone()
				if reads > 1 && change == "registry-error" {
					return m.Registry{}, os.ErrPermission
				}
				if reads > 1 {
					switch change {
					case "offline":
						reg.Agents[0].Status.Phase = m.PhaseOffline
					case "removed":
						reg.Agents = nil
					case "pane":
						reg.Agents[0].Status.PaneRef = "replacement"
					case "generation":
						reg.Panes[0].Status.ProcessSession.Binding.Generation = "second"
					case "thread":
						reg.Panes[0].Status.ProcessSession.ThreadID = "replacement"
					case "connection":
						reg.Panes[0].Status.ProcessSession.ConnectionID = "replacement"
					case "owner":
						reg.Panes[0].Status.Activation.Process = &m.ProcessActivation{Binding: m.ProcessBinding{Generation: "replacement"}}
					}
				}
				return reg, nil
			}}
			_, err := service.Answer(context.Background(), Request{Spelling: "agent question answer", QuestionID: "question-0000000000000001", Texts: []string{"1=explicit"}}, a)
			var failure *Failure
			want := "question-stale"
			if change == "offline" || change == "already-offline" {
				want = questionReasonClosed
			}
			if change == "registry-error" {
				if !errors.Is(err, os.ErrPermission) || d.answers != 0 {
					t.Fatal("registry read error hidden", err)
				}
			} else if !errors.As(err, &failure) || failure.Code != want || d.answers != 0 {
				t.Fatal("service admitted replaced lifetime", failure, d.answers, err)
			}
			if _, err = os.Stat(st.AsyncStore().Path()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("guard wrote store", err)
			}
		})
	}
}

func TestAsyncServiceMissingViewUsesOnlyOwnedHistory(t *testing.T) {
	for _, outcome := range []string{"waiting", "expired", "closed", "submitted", "unknown", "not-submitted", "other-agent", "missing", "malformed"} {
		t.Run(outcome, func(t *testing.T) {
			st := agentquestion.NewStore(t.TempDir())
			sidecar := st.AsyncStore()
			now := time.Now().UTC()
			a := m.Agent{Metadata: m.ObjectMeta{UID: "agent", Name: "fixture"}, Spec: m.AgentSpec{Provider: "codex"}, Status: m.AgentStatus{PaneRef: "pane", Phase: m.PhaseRunning}}
			p := m.Pane{Metadata: m.ObjectMeta{UID: "pane"}, Spec: m.PaneSpec{Runtime: m.PaneRuntimeSpec{Kind: m.RuntimeProcess}}}
			id := "question-0000000000000001"
			owner := "agent"
			if outcome == "other-agent" {
				owner = "other"
			}
			if outcome != "missing" {
				r, err := sidecar.Create(agentquestion.Record{ID: id, Provider: "codex", Delivery: "async", AgentUID: owner, Questions: json.RawMessage(`[{"id":"q1","question":"Explicit","isOther":true,"options":[]}]`), CreatedAt: now, Deadline: now.Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				switch outcome {
				case "expired":
					sidecar = sidecar.WithClock(func() time.Time { return now.Add(2 * time.Hour) })
					st = st.WithClock(func() time.Time { return now.Add(2 * time.Hour) })
				case "closed":
					if _, err = sidecar.Close(id, agentquestion.CloseReason("process-ended")); err != nil {
						t.Fatal(err)
					}
				case "submitted", "unknown", "not-submitted":
					claim, err := sidecar.BeginAsync(r)
					if err != nil {
						t.Fatal(err)
					}
					if err = sidecar.FinishAsync(claim, outcome); err != nil {
						t.Fatal(err)
					}
				case "malformed":
					if err = os.WriteFile(sidecar.Path(), []byte("malformed fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, _ := os.ReadFile(sidecar.Path())
			d := &asyncReceiptServiceDispatcher{}
			service := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, LoadRegistry: func() (m.Registry, error) { return m.Registry{Agents: []m.Agent{a}, Panes: []m.Pane{p}}, nil }, Dispatcher: d}
			result, err := service.Answer(context.Background(), Request{Spelling: "agent question answer", QuestionID: id, Texts: []string{"1=explicit"}}, a)
			if outcome == "malformed" {
				if !errors.Is(err, agentquestion.ErrMalformedStore) {
					t.Fatal("store error hidden", err)
				}
			} else {
				want := "question-stale"
				switch outcome {
				case "expired":
					want = questionReasonExpired
				case "closed":
					want = questionReasonClosed
				case "submitted", "unknown", "not-submitted":
					want = questionReasonNotPending
				case "other-agent", "missing":
					want = questionReasonNotFound
				}
				var failure *Failure
				if !errors.As(err, &failure) || failure.Code != want {
					t.Fatal(outcome, result, err)
				}
				if outcome == "submitted" || outcome == "unknown" || outcome == "not-submitted" {
					if result.QuestionID != id || result.Delivery != "async" || result.Outcome != outcome || strings.Contains(err.Error(), "nothing was changed") {
						t.Fatal("lost prior receipt", result, err)
					}
				}
			}
			after, _ := os.ReadFile(sidecar.Path())
			if d.answers != 0 || !bytes.Equal(before, after) {
				t.Fatal("history refusal had effects")
			}
		})
	}
}
