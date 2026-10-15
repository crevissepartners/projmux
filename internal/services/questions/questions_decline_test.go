package questions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
)

type declineServiceDispatcher struct {
	asyncReadDispatcher
	receipt  processquestions.AnswerReceipt
	err      error
	declines int
}

func (d *declineServiceDispatcher) SupportsAsyncQuestionDeclines() bool { return true }
func (d *declineServiceDispatcher) DeclineExactProcessQuestionReceipt(context.Context, string, string) (processquestions.AnswerReceipt, error) {
	d.declines++
	return d.receipt, d.err
}

func newDeclineServiceFixture(t *testing.T) (Service, m.Agent, *declineServiceDispatcher, *agentquestion.Store) {
	t.Helper()
	st := agentquestion.NewStore(t.TempDir())
	a := m.Agent{Metadata: m.ObjectMeta{UID: "agent", Name: "fixture"}, Spec: m.AgentSpec{Provider: "codex"}, Status: m.AgentStatus{PaneRef: "pane", Phase: m.PhaseRunning}}
	p := m.Pane{Metadata: m.ObjectMeta{UID: "pane"}, Spec: m.PaneSpec{Runtime: m.PaneRuntimeSpec{Kind: m.RuntimeProcess}}, Status: m.PaneStatus{ProcessSession: &m.ProcessSessionRecord{Provider: "codex", ThreadID: "thread", ConnectionID: "connection", Binding: m.ProcessBinding{Generation: "first"}}}}
	id := "question-0000000000000001"
	r, err := st.AsyncStore().Create(agentquestion.Record{ID: id, AgentUID: "agent", Provider: "codex", Delivery: "async", Questions: json.RawMessage(`[{"id":"q1","question":"Choose","isOther":true,"options":[]}]`), Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	prompts, _ := r.ParsedQuestions()
	d := &declineServiceDispatcher{asyncReadDispatcher: asyncReadDispatcher{views: []processquestions.ExactQuestion{{QuestionID: id, Delivery: "async", CanAnswer: true, CanDecline: true, State: agentquestion.StateWaiting, Prompts: prompts}}}, receipt: processquestions.AnswerReceipt{QuestionID: id, Delivery: "async", Outcome: "submitted"}}
	service := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, LoadRegistry: func() (m.Registry, error) { return m.Registry{Agents: []m.Agent{a}, Panes: []m.Pane{p}}, nil }, Dispatcher: d}
	return service, a, d, st
}

func TestDeclineServiceCapabilitiesAndRefusals(t *testing.T) {
	for _, mode := range []string{"decline-only", "old-dispatcher", "old-host", "secret", "blocking", "claude", "expired", "selectors", "missing"} {
		t.Run(mode, func(t *testing.T) {
			service, a, d, st := newDeclineServiceFixture(t)
			id := d.views[0].QuestionID
			want := "question-decline-unsupported"
			switch mode {
			case "old-dispatcher":
				service.Dispatcher = &d.asyncReadDispatcher
			case "old-host":
				d.views[0].CanDecline = false
			case "secret":
				d.views[0].CanDecline = false
				d.views[0].Prompts[0].IsSecret = true
				want = questionReasonSecretNativeOnly
			case "blocking":
				d.views[0].Delivery = "held-question"
			case "claude":
				a.Spec.Provider = "claude"
			case "expired":
				d.views[0].State = agentquestion.StateExpired
				d.views[0].CanDecline = false
				want = questionReasonExpired
			case "selectors":
				want = questionReasonInvalidAnswer
			case "missing":
				id = "question-ffffffffffffffff"
				want = questionReasonNotFound
			}
			if mode == "decline-only" {
				list, err := service.List(context.Background(), a)
				if err != nil || len(list.Questions) != 1 || !list.Questions[0].CanDecline || list.Questions[0].CanAnswer {
					t.Fatal(list, err)
				}
			}
			before, readErr := os.ReadFile(st.AsyncStore().Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			request := Request{QuestionID: id, Spelling: "agent question decline"}
			if mode == "selectors" {
				request.Texts = []string{"1=bad"}
			}
			r, err := service.Decline(context.Background(), request, a)
			if mode == "decline-only" {
				if err != nil || r.QuestionID != id || r.Delivery != "async" || r.Outcome != "submitted" || d.declines != 1 {
					t.Fatal(r, err, d.declines)
				}
			} else {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Code != want || d.declines != 0 || d.answers != 0 {
					t.Fatal(mode, r, err, d.declines)
				}
			}
			after, readErr := os.ReadFile(st.AsyncStore().Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("service mutated store")
			}
		})
	}
}

func TestDeclineServiceFreshLifetimeFence(t *testing.T) {
	for _, change := range []string{"offline", "removed", "pane", "generation", "thread", "connection", "owner", "registry-error"} {
		t.Run(change, func(t *testing.T) {
			service, a, d, st := newDeclineServiceFixture(t)
			original, _ := service.LoadRegistry()
			reads := 0
			service.LoadRegistry = func() (m.Registry, error) {
				reads++
				reg := original.Clone()
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
					case "registry-error":
						return m.Registry{}, os.ErrPermission
					}
				}
				return reg, nil
			}
			before, readErr := os.ReadFile(st.AsyncStore().Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			_, err := service.Decline(context.Background(), Request{QuestionID: d.views[0].QuestionID}, a)
			if change == "registry-error" {
				if !errors.Is(err, os.ErrPermission) {
					t.Fatal(err)
				}
			} else {
				var failure *Failure
				want := "question-stale"
				if change == "offline" {
					want = questionReasonClosed
				}
				if !errors.As(err, &failure) || failure.Code != want {
					t.Fatal(err)
				}
			}
			after, readErr := os.ReadFile(st.AsyncStore().Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(before, after) || d.declines != 0 {
				t.Fatal("lifetime fence effects")
			}
		})
	}
}

func TestDeclineServiceOwnedHistoryAndAnswerMeaning(t *testing.T) {
	for _, outcome := range []string{"declined", "submitted", "unknown", "not-submitted", "expired", "closed", "foreign", "missing", "malformed", "legacy"} {
		for _, liveView := range []bool{false, true} {
			t.Run(outcome+map[bool]string{true: "-view", false: "-history"}[liveView], func(t *testing.T) {
				service, a, d, st := newDeclineServiceFixture(t)
				id := d.views[0].QuestionID
				sidecar := st.AsyncStore()
				r, _, err := sidecar.Get(id)
				if err != nil {
					t.Fatal(err)
				}
				switch outcome {
				case "declined", "submitted", "unknown", "not-submitted":
					claim, err := sidecar.BeginAsync(r)
					if err != nil {
						t.Fatal(err)
					}
					if err = sidecar.FinishAsync(claim, outcome); err != nil {
						t.Fatal(err)
					}
				case "closed":
					_, err = sidecar.Close(id, "process-ended")
				case "expired":
					st = st.WithClock(func() time.Time { return r.Deadline.Add(time.Second) })
					service.OpenStore = func() (*agentquestion.Store, error) { return st, nil }
				case "foreign", "missing", "legacy":
					raw, readErr := os.ReadFile(sidecar.Path())
					if readErr != nil {
						t.Fatal(readErr)
					}
					var state struct {
						Version int                    `json:"version"`
						Records []agentquestion.Record `json:"records"`
					}
					if err = json.Unmarshal(raw, &state); err != nil {
						t.Fatal(err)
					}
					if outcome == "foreign" {
						state.Records[0].AgentUID = "other"
					} else {
						state.Records = nil
					}
					raw, _ = json.Marshal(state)
					err = os.WriteFile(sidecar.Path(), raw, 0600)
					if outcome == "legacy" {
						r.Delivery = ""
						_, err = st.Create(r)
					}
				case "malformed":
					err = os.WriteFile(sidecar.Path(), []byte("malformed fixture"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if liveView && outcome != "foreign" && outcome != "missing" && outcome != "legacy" {
					d.views[0].State = agentquestion.StateClosed
					d.views[0].CanDecline = false
				} else {
					d.views = nil
				}
				before, readErr := os.ReadFile(sidecar.Path())
				if readErr != nil {
					t.Fatal(readErr)
				}
				result, err := service.Decline(context.Background(), Request{QuestionID: id}, a)
				if outcome == "malformed" {
					if !errors.Is(err, agentquestion.ErrMalformedStore) {
						t.Fatal(err)
					}
				} else {
					want := questionReasonNotPending
					switch outcome {
					case "expired":
						want = questionReasonExpired
					case "closed":
						want = questionReasonClosed
					case "foreign", "missing":
						want = questionReasonNotFound
					case "legacy":
						want = "question-decline-unsupported"
					}
					var failure *Failure
					if !errors.As(err, &failure) || failure.Code != want {
						t.Fatal(outcome, result, err)
					}
					expected := ""
					switch outcome {
					case "declined":
						expected = "submitted"
					case "unknown", "not-submitted":
						expected = outcome
					}
					if result.Outcome != expected {
						t.Fatal("prior intent confused", result, expected)
					}
				}
				if outcome == "declined" {
					service.Dispatcher = &asyncReceiptServiceDispatcher{asyncReadDispatcher: d.asyncReadDispatcher}
					answer, err := service.Answer(context.Background(), Request{QuestionID: id, Texts: []string{"1=must not send"}}, a)
					var failure *Failure
					if !errors.As(err, &failure) || failure.Code != questionReasonNotPending || answer.Outcome == "submitted" {
						t.Fatal("notice became answer", answer, err)
					}
				}
				after, readErr := os.ReadFile(sidecar.Path())
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(before, after) || d.declines != 0 {
					t.Fatal("terminal history effects")
				}
			})
		}
	}
}

func TestDeclineServiceReceiptAndCause(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.EOF, &os.PathError{Op: "read", Path: "fixture", Err: io.ErrUnexpectedEOF}} {
		service, a, d, _ := newDeclineServiceFixture(t)
		d.receipt.Outcome = "unknown"
		d.receipt.Code = "question-delivery-unknown"
		d.err = &processquestions.DeliveryError{Receipt: d.receipt, Cause: cause}
		result, err := service.Decline(context.Background(), Request{QuestionID: d.views[0].QuestionID}, a)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Code != "question-delivery-unknown" || !errors.Is(err, cause) || result.Outcome != "unknown" || d.declines != 1 {
			t.Fatal(result, err)
		}
	}
	service, a, d, _ := newDeclineServiceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.Decline(ctx, Request{QuestionID: d.views[0].QuestionID}, a)
	if err != context.Canceled || result != (DeclineResult{}) || d.declines != 0 {
		t.Fatal(result, err)
	}
}
