package questions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
)

type sourceDispatcher struct {
	read  func(context.Context) ([]processquestions.ExactQuestion, error)
	calls int
}

func (d *sourceDispatcher) ReadExactProcessQuestions(ctx context.Context, _ string) ([]processquestions.ExactQuestion, error) {
	d.calls++
	return d.read(ctx)
}
func (d *sourceDispatcher) AnswerExactProcessQuestion(context.Context, string, string, map[int]agentquestion.Selection) error {
	panic("read attempted an answer")
}

func sourceFixture(t *testing.T) (Service, *m.Registry, *agentquestion.Store, *sourceDispatcher) {
	t.Helper()
	meta := func(uid string, kind m.Kind, owner string) m.ObjectMeta {
		return m.ObjectMeta{UID: uid, Name: uid, OwnerRef: &m.OwnerRef{Kind: kind, UID: owner}}
	}
	b := m.ProcessBinding{HostInstanceID: "host", ProjectUID: "project", WindowUID: "window", AgentUID: "agent", PaneUID: "pane", Generation: "generation", OperationID: "operation"}
	activation := m.ProcessActivation{Binding: b, HostProcess: m.ProcessIdentity{PID: 123, OwnerUID: 1, Start: "host"}, Child: m.ProcessIdentity{PID: 124, OwnerUID: 1, Start: "child"}}
	reg := &m.Registry{Projects: []m.Project{{Metadata: m.ObjectMeta{UID: "project"}}}, Windows: []m.Window{{Metadata: meta("window", m.KindProject, "project")}}, Agents: []m.Agent{{Metadata: meta("agent", m.KindWindow, "window"), Spec: m.AgentSpec{Provider: "codex"}, Status: m.AgentStatus{PaneRef: "pane", Phase: m.PhaseRunning, SessionRef: &m.AgentSessionRef{Provider: "codex", Codex: &m.CodexSessionRef{ThreadID: "thread"}}}}}, Panes: []m.Pane{{Metadata: meta("pane", m.KindAgent, "agent"), Spec: m.PaneSpec{Role: m.PaneRoleAgent, Runtime: m.PaneRuntimeSpec{Kind: m.RuntimeProcess}}, Status: m.PaneStatus{Activation: m.PaneActivation{Kind: m.RuntimeProcess, AgentUID: "agent", Generation: b.Generation, OperationID: b.OperationID, Process: &activation}, ProcessSession: &m.ProcessSessionRecord{Provider: "codex", Binding: b, ThreadID: "thread", ConnectionID: "operation"}}}}}
	st := agentquestion.NewStore(t.TempDir())
	d := &sourceDispatcher{read: func(context.Context) ([]processquestions.ExactQuestion, error) {
		return []processquestions.ExactQuestion{{QuestionID: "question-0000000000000001", Delivery: "async", State: agentquestion.StateWaiting, CanAnswer: true}}, nil
	}}
	service := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, LoadRegistry: func() (m.Registry, error) { return reg.Clone(), nil }, Answering: func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux }, Dispatcher: d}
	return service, reg, st, d
}
func sourceRecord(t *testing.T, st *agentquestion.Store, id, delivery, uid, requestID string) agentquestion.Record {
	t.Helper()
	r, e := st.Create(agentquestion.Record{ID: id, Provider: "codex", AgentUID: uid, Delivery: delivery, RequestID: requestID, Generation: "old-history-generation", SessionID: "old-thread", Questions: json.RawMessage(`[{"id":"q","question":"Choose","isOther":true,"options":[{"label":"yes"}]}]`), Deadline: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestSourceReadOpaqueIDsAndIndependentCLI(t *testing.T) {
	s, r, st, d := sourceFixture(t)
	sourceRecord(t, st.AsyncStore(), "question-0000000000000001", "async", "agent", "opaque/item:01")
	sourceRecord(t, st, "question-0000000000000002", "", "agent", "must-not-copy-legacy")
	sourceRecord(t, st.AsyncStore(), "question-0000000000000003", "async", "other-agent", "must-not-copy-other")
	cli, e := st.CLIAsyncStore().Create("agent", json.RawMessage(`[{"question":"Independent","options":[{"label":"yes"}]}]`), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	for _, phase := range []m.AgentPhase{m.PhaseRunning, m.PhaseOffline} {
		r.Agents[0].Status.Phase = phase
		list, e := s.ListWithSource(context.Background(), r.Agents[0])
		if e != nil {
			t.Fatal(e)
		}
		if list.Provider != "codex" || list.Runtime != m.RuntimeProcess || list.Phase != phase || len(list.Questions) != 3 {
			t.Fatalf("source metadata/history %+v", list)
		}
		for _, q := range list.Questions {
			switch q.ID {
			case "question-0000000000000001":
				if q.ItemID != "opaque/item:01" || q.CanAnswer {
					t.Fatal(q)
				}
			case cli.ID:
				if q.ItemID != "" || !q.CanAnswer || !q.CanDecline {
					t.Fatal(q)
				}
			default:
				if q.ItemID != "" || q.CanAnswer {
					t.Fatal(q)
				}
			}
		}
		if phase == m.PhaseRunning {
			if list.Session != "thread" || len(list.ExactQuestions) != 1 || list.ExactQuestions[0].ItemID != "opaque/item:01" {
				t.Fatal(list)
			}
		} else if list.Session != "" || len(list.ExactQuestions) != 0 {
			t.Fatal(list)
		}
	}
	if d.calls != 1 {
		t.Fatal("stopped process queried host", d.calls)
	}
}
func TestSourceReadFenceAndProgress(t *testing.T) {
	changes := map[string]func(*m.Registry){
		"provider":              func(r *m.Registry) { r.Agents[0].Spec.Provider = "claude" },
		"phase":                 func(r *m.Registry) { r.Agents[0].Status.Phase = m.PhaseOffline },
		"agent-owner":           func(r *m.Registry) { r.Agents[0].Metadata.OwnerRef.UID = "replacement" },
		"pane":                  func(r *m.Registry) { r.Agents[0].Status.PaneRef = "replacement" },
		"pane-owner":            func(r *m.Registry) { r.Panes[0].Metadata.OwnerRef.UID = "replacement" },
		"runtime":               func(r *m.Registry) { r.Panes[0].Spec.Runtime.Kind = m.RuntimeTmux },
		"generation":            func(r *m.Registry) { r.Panes[0].Status.ProcessSession.Binding.Generation = "replacement" },
		"activation-generation": func(r *m.Registry) { r.Panes[0].Status.Activation.Generation = "replacement" },
		"operation":             func(r *m.Registry) { r.Panes[0].Status.Activation.OperationID = "replacement" },
		"host":                  func(r *m.Registry) { r.Panes[0].Status.Activation.Process.HostProcess.Start = "replacement" },
		"session":               func(r *m.Registry) { r.Agents[0].Status.SessionRef.Codex.ThreadID = "replacement" },
		"thread":                func(r *m.Registry) { r.Panes[0].Status.ProcessSession.ThreadID = "replacement" },
		"connection":            func(r *m.Registry) { r.Panes[0].Status.ProcessSession.ConnectionID = "replacement" },
		"removed":               func(r *m.Registry) { r.Agents = nil },
		"other-session-slot":    func(r *m.Registry) { r.Panes[0].Status.ProcessSession.SessionID = "changed-unused-slot" },
		"consistent-session": func(r *m.Registry) {
			r.Panes[0].Status.ProcessSession.ThreadID = "replacement"
			r.Agents[0].Status.SessionRef.Codex.ThreadID = "replacement"
		},
		"consistent-generation": func(r *m.Registry) {
			p := &r.Panes[0]
			p.Status.Activation.Generation = "next"
			p.Status.Activation.Process.Binding.Generation = "next"
			p.Status.ProcessSession.Binding.Generation = "next"
		},
		"consistent-operation": func(r *m.Registry) {
			p := &r.Panes[0]
			p.Status.Activation.OperationID = "next"
			p.Status.Activation.Process.Binding.OperationID = "next"
			p.Status.ProcessSession.Binding.OperationID = "next"
			p.Status.ProcessSession.ConnectionID = "next"
		},
		"progress": func(r *m.Registry) {
			r.Agents[0].Status.Progress.ObservedAt = time.Now()
			r.Agents[0].Status.SessionRef.ObservedAt = time.Now()
			r.Panes[0].Status.ProcessSession.TurnID = "new-turn"
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, r, _, d := sourceFixture(t)
			agent := r.Agents[0].Clone()
			d.read = func(context.Context) ([]processquestions.ExactQuestion, error) {
				change(r)
				return []processquestions.ExactQuestion{{QuestionID: "native", Delivery: "native"}}, nil
			}
			list, e := s.ListWithSource(context.Background(), agent)
			if name == "progress" {
				if e != nil || list.Session != "thread" {
					t.Fatal(list, e)
				}
				return
			}
			var f *Failure
			if !errors.As(e, &f) || f.Code != "question-stale" || list.AgentUID != "" {
				t.Fatalf("stale result leaked %+v %v", list, e)
			}
		})
	}
	for _, missing := range []string{"pane", "session", "activation", "owner", "connection-operation"} {
		t.Run("before-"+missing, func(t *testing.T) {
			s, r, _, d := sourceFixture(t)
			switch missing {
			case "connection-operation":
				r.Panes[0].Status.ProcessSession.ConnectionID = "unrelated-operation"
			case "pane":
				r.Panes = nil
			case "session":
				r.Agents[0].Status.SessionRef = nil
			case "activation":
				r.Panes[0].Status.Activation.Process = nil
			case "owner":
				r.Panes[0].Metadata.OwnerRef = nil
			}
			_, e := s.ListWithSource(context.Background(), r.Agents[0])
			var f *Failure
			if !errors.As(e, &f) || f.Code != "question-stale" || d.calls != 0 {
				t.Fatal(e, d.calls)
			}
		})
	}
}
func TestSourceReadPartialStoreErrorsAndContext(t *testing.T) {
	for _, phase := range []m.AgentPhase{m.PhaseRunning, m.PhaseOffline} {
		for _, broken := range []string{"open", "legacy", "async", "cli"} {
			t.Run(string(phase)+"/"+broken, func(t *testing.T) {
				s, r, st, _ := sourceFixture(t)
				r.Agents[0].Status.Phase = phase
				sourceRecord(t, st, "question-0000000000000002", "", "agent", "")
				sourceRecord(t, st.AsyncStore(), "question-0000000000000001", "async", "agent", "opaque")
				cli, e := st.CLIAsyncStore().Create("agent", json.RawMessage(`[{"question":"Independent","options":[{"label":"yes"}]}]`), time.Hour)
				if e != nil {
					t.Fatal(e)
				}
				switch broken {
				case "open":
					s.OpenStore = func() (*agentquestion.Store, error) { return nil, errors.New("open failed") }
				case "legacy":
					if e := os.WriteFile(st.Path(), []byte("malformed"), 0600); e != nil {
						t.Fatal(e)
					}
				case "async":
					if e := os.WriteFile(st.AsyncStore().Path(), []byte("malformed"), 0600); e != nil {
						t.Fatal(e)
					}
				case "cli":
					if e := os.WriteFile(st.CLIAsyncStore().Path(), []byte("malformed"), 0600); e != nil {
						t.Fatal(e)
					}
				}
				list, e := s.ListWithSource(context.Background(), r.Agents[0])
				if broken == "cli" {
					if !errors.Is(e, agentquestion.ErrMalformedStore) {
						t.Fatal(e)
					}
					return
				}
				if e != nil {
					t.Fatal(e)
				}
				want := 1
				if broken == "open" {
					want = 0
				}
				if broken == "async" {
					want = 2
				}
				if len(list.Questions) != want {
					t.Fatalf("partial history count %+v", list)
				}
				if broken != "open" {
					last := list.Questions[len(list.Questions)-1]
					if last.ID != cli.ID || !last.CanAnswer {
						t.Fatal(last)
					}
				}
				if phase == m.PhaseRunning && len(list.ExactQuestions) != 1 {
					t.Fatal("exact hidden", list)
				}
			})
		}
	}
	for _, deadline := range []bool{false, true} {
		t.Run("cancel", func(t *testing.T) {
			s, r, _, d := sourceFixture(t)
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}
			defer cancel()
			_, e := s.ListWithSource(ctx, r.Agents[0])
			if e == nil || d.calls != 0 {
				t.Fatal(e, d.calls)
			}
		})
	}
	s, r, _, d := sourceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	d.read = func(got context.Context) ([]processquestions.ExactQuestion, error) {
		if got != ctx {
			t.Fatal("context replaced")
		}
		cancel()
		return nil, ctx.Err()
	}
	if _, e := s.ListWithSource(ctx, r.Agents[0]); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestSourceReadNeverTrustsDispatcherItemIDs(t *testing.T) {
	s, r, st, d := sourceFixture(t)
	const own = "question-0000000000000001"
	const other = "question-0000000000000002"
	const absent = "question-0000000000000003"
	const native = "question-0000000000000004"
	sourceRecord(t, st.AsyncStore(), own, "async", "agent", "authoritative/opaque")
	sourceRecord(t, st.AsyncStore(), other, "async", "other-agent", "foreign-item")
	sourceRecord(t, st, "question-0000000000000005", "", "agent", "legacy-item")
	sourceRecord(t, st.AsyncStore(), "question-0000000000000006", "async", "agent", "")
	d.read = func(context.Context) ([]processquestions.ExactQuestion, error) {
		return []processquestions.ExactQuestion{
			{QuestionID: own, Delivery: "async", ItemID: "host-injected"},
			{QuestionID: other, Delivery: "async", ItemID: "host-injected"},
			{QuestionID: absent, Delivery: "async", ItemID: "host-injected"},
			{QuestionID: native, Delivery: "native", ItemID: "host-injected"},
			{QuestionID: "question-0000000000000006", Delivery: "async", ItemID: "host-injected"},
		}, nil
	}
	plain, e := s.List(context.Background(), r.Agents[0])
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range plain.ExactQuestions {
		if q.ItemID != "" {
			t.Fatal("default trusted host ItemID", q)
		}
	}
	list, e := s.ListWithSource(context.Background(), r.Agents[0])
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range list.ExactQuestions {
		want := ""
		if q.QuestionID == own {
			want = "authoritative/opaque"
		}
		if q.ItemID != want {
			t.Fatal("source trusted host/foreign ItemID", q)
		}
	}
}

func TestSourceReadHostFailureDeadlineAndRegistryFailure(t *testing.T) {
	for _, kind := range []string{"host-error", "deadline", "registry-after"} {
		t.Run(kind, func(t *testing.T) {
			s, r, _, d := sourceFixture(t)
			ctx := context.Background()
			if kind == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			reads := 0
			s.LoadRegistry = func() (m.Registry, error) {
				reads++
				if kind == "registry-after" && reads > 1 {
					return m.Registry{}, errors.New("registry unreadable")
				}
				return r.Clone(), nil
			}
			d.read = func(got context.Context) ([]processquestions.ExactQuestion, error) {
				switch kind {
				case "host-error":
					return nil, errors.New("host unavailable")
				case "deadline":
					if got != ctx {
						t.Fatal("context replaced")
					}
					<-got.Done()
					return nil, got.Err()
				}
				return []processquestions.ExactQuestion{{QuestionID: "must-discard"}}, nil
			}
			list, e := s.ListWithSource(ctx, r.Agents[0])
			if kind == "deadline" {
				if !errors.Is(e, context.DeadlineExceeded) {
					t.Fatal(e)
				}
			} else {
				var f *Failure
				if !errors.As(e, &f) || f.Code != "question-stale" {
					t.Fatal(e)
				}
			}
			if list.AgentUID != "" || len(list.ExactQuestions) != 0 || d.calls != 1 {
				t.Fatal("failed read returned a result", list, d.calls)
			}
		})
	}
}

func TestSourceReadPreservesSavedHistoryState(t *testing.T) {
	for _, phase := range []m.AgentPhase{m.PhaseOffline, m.PhaseRunning} {
		t.Run(string(phase), func(t *testing.T) {
			s, reg, st, d := sourceFixture(t)
			reg.Agents[0].Status.Phase = phase
			record := sourceRecord(t, st.AsyncStore(), "question-0000000000000001", "async", "agent", "opaque-history")
			d.read = func(context.Context) ([]processquestions.ExactQuestion, error) { return nil, nil }
			before, e := os.ReadFile(st.AsyncStore().Path())
			if e != nil {
				t.Fatal(e)
			}
			source, e := s.ListWithSource(context.Background(), reg.Agents[0])
			if e != nil {
				t.Fatal(e)
			}
			if len(source.Questions) != 1 {
				t.Fatal(source)
			}
			q := source.Questions[0]
			if q.State != record.State || q.Disposition != record.Disposition || q.State != agentquestion.StateWaiting || q.CanAnswer || q.CanDecline || q.ItemID != "opaque-history" {
				t.Fatalf("unmatched history changed stored state: %+v", q)
			}
			plain, e := s.List(context.Background(), reg.Agents[0])
			if e != nil {
				t.Fatal(e)
			}
			if len(plain.Questions) != 1 || plain.Questions[0].State != agentquestion.StateClosed || plain.Questions[0].Disposition != "process-stale" {
				t.Fatalf("default stale projection changed: %+v", plain)
			}
			after, e := os.ReadFile(st.AsyncStore().Path())
			if e != nil {
				t.Fatal(e)
			}
			if string(before) != string(after) {
				t.Fatal("projection changed stored bytes")
			}
			closed, e := st.AsyncStore().Close(record.ID, agentquestion.CloseReasonTurnEnded)
			if e != nil {
				t.Fatal(e)
			}
			source, e = s.ListWithSource(context.Background(), reg.Agents[0])
			if e != nil {
				t.Fatal(e)
			}
			if source.Questions[0].State != closed.State || source.Questions[0].Disposition != closed.Disposition || closed.State != agentquestion.StateClosed {
				t.Fatal("actual stored transition was hidden", source)
			}
			if phase == m.PhaseOffline && d.calls != 0 {
				t.Fatal("stopped history queried host", d.calls)
			}
		})
	}
}

func TestSourceReadKeepsMatchedExactProjection(t *testing.T) {
	s, reg, st, d := sourceFixture(t)
	record := sourceRecord(t, st.AsyncStore(), "question-0000000000000001", "async", "agent", "opaque-history")
	for _, state := range []agentquestion.State{agentquestion.StateClosed, agentquestion.StateWaiting} {
		d.read = func(context.Context) ([]processquestions.ExactQuestion, error) {
			return []processquestions.ExactQuestion{{QuestionID: record.ID, Delivery: "async", State: state, CanAnswer: state == agentquestion.StateWaiting}}, nil
		}
		plain, e := s.List(context.Background(), reg.Agents[0])
		if e != nil {
			t.Fatal(e)
		}
		source, e := s.ListWithSource(context.Background(), reg.Agents[0])
		if e != nil {
			t.Fatal(e)
		}
		if len(source.ExactQuestions) != 1 || source.ExactQuestions[0].State != state || source.ExactQuestions[0].CanAnswer != (state == agentquestion.StateWaiting) {
			t.Fatal("exact refresh was changed", source)
		}
		source.Provider, source.Runtime, source.Phase, source.Session = "", "", "", ""
		for i := range source.Questions {
			source.Questions[i].ItemID = ""
		}
		for i := range source.ExactQuestions {
			source.ExactQuestions[i].ItemID = ""
		}
		a, e := json.Marshal(plain)
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(source)
		if e != nil {
			t.Fatal(e)
		}
		if string(a) != string(b) {
			t.Fatalf("matched projection differs beyond source metadata: %s / %s", a, b)
		}
	}
}
