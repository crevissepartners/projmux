package questions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"os"
	"testing"
	"time"
)

func TestCLIAsyncOfflineListAnswerDecline(t *testing.T) {
	for _, decline := range []bool{false, true} {
		t.Run(map[bool]string{true: "decline", false: "answer"}[decline], func(t *testing.T) {
			st := agentquestion.NewStore(t.TempDir())
			a := m.Agent{Metadata: m.ObjectMeta{UID: "agent-a", Name: "a"}, Spec: m.AgentSpec{Provider: "claude"}, Status: m.AgentStatus{Phase: m.PhaseOffline, PaneRef: "pane-a"}}
			r, e := st.CLIAsyncStore().Create(a.Metadata.UID, json.RawMessage(`[{"question":"Pick?","options":[{"label":"yes (Recommended)"}]}]`), time.Hour)
			if e != nil {
				t.Fatal(e)
			}
			svc := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, LoadRegistry: func() (m.Registry, error) {
				return m.Registry{Agents: []m.Agent{a}, Panes: []m.Pane{{Metadata: m.ObjectMeta{UID: "pane-a"}, Spec: m.PaneSpec{Runtime: m.PaneRuntimeSpec{Kind: m.RuntimeProcess}}}}}, nil
			}}
			list, e := svc.List(context.Background(), a)
			if e != nil || len(list.Questions) != 1 || list.Questions[0].Delivery != "async-message" || !list.Questions[0].CanAnswer || !list.Questions[0].CanDecline {
				t.Fatalf("list %+v %v", list, e)
			}
			req := Request{Spelling: "agent question answer", QuestionID: r.ID, Indexes: []string{"1=1"}}
			if decline {
				req.Indexes = nil
				out, e := svc.Decline(context.Background(), req, a)
				if e != nil || out.Delivery != "async-message" || out.Outcome != "pending" {
					t.Fatal(out, e)
				}
			} else {
				out, e := svc.Answer(context.Background(), req, a)
				if e != nil || out.Delivery != "async-message" || out.Outcome != "pending" {
					t.Fatal(out, e)
				}
			}
			stored, _, _ := st.CLIAsyncStore().Get(r.ID)
			if len(stored.Parts) != 1 || stored.ResultDeadline.IsZero() {
				t.Fatal(stored)
			}
			if e = st.CLIAsyncStore().MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
				v.Parts[0].State = "unknown"
				v.Outcome = "unknown"
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			list, e = svc.List(context.Background(), a)
			if e != nil || !list.Questions[0].OutcomeUnknown || list.Questions[0].Outcome != "unknown" || list.Questions[0].CanAnswer || list.Questions[0].CanDecline {
				t.Fatalf("unknown projection %+v %v", list, e)
			}
			if _, e = svc.Answer(context.Background(), req, a); e == nil {
				t.Fatal("duplicate answer accepted")
			}
		})
	}
}

func TestCLIAsyncExistingSecretQuestionRefusesPublicAnswer(t *testing.T) {
	st := agentquestion.NewStore(t.TempDir())
	a := m.Agent{Metadata: m.ObjectMeta{UID: "agent-a", Name: "a"}, Spec: m.AgentSpec{Provider: "claude"}, Status: m.AgentStatus{Phase: m.PhaseOffline}}
	r, err := st.CLIAsyncStore().Create(a.Metadata.UID, json.RawMessage(`[{"question":"Token?","options":[{"label":"other"}]}]`), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(st.CLIAsyncStore().Path())
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"question":"Token?"`), []byte(`"question":"Token?","isSecret":true`), 1)
	if err = os.WriteFile(st.CLIAsyncStore().Path(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	svc := Service{OpenStore: func() (*agentquestion.Store, error) { return st, nil }, LoadRegistry: func() (m.Registry, error) { return m.Registry{Agents: []m.Agent{a}}, nil }}
	_, err = svc.Answer(context.Background(), Request{Spelling: "agent question answer", QuestionID: r.ID, Texts: []string{"1=private-fixture-value"}}, a)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != questionReasonSecretNativeOnly {
		t.Fatalf("secret public answer admitted: %v", err)
	}
	_, err = svc.Decline(context.Background(), Request{Spelling: "agent question decline", QuestionID: r.ID}, a)
	if !errors.As(err, &failure) || failure.Code != questionReasonSecretNativeOnly {
		t.Fatalf("secret public decline admitted: %v", err)
	}
	after, _ := os.ReadFile(st.CLIAsyncStore().Path())
	if !bytes.Equal(raw, after) {
		t.Fatal("refused secret answer changed store")
	}
	list, err := svc.List(context.Background(), a)
	if err != nil || len(list.Questions) != 1 || (list.Questions[0].CanAnswer || list.Questions[0].CanDecline) {
		t.Fatalf("secret answer advertised: %+v %v", list, err)
	}
}
