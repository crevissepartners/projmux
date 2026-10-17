package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestSDKCLIAsyncOfflineAnswersAndDeclines(t *testing.T) {
	for _, decline := range []bool{false, true} {
		t.Run(map[bool]string{true: "decline", false: "answer"}[decline], func(t *testing.T) {
			c, _, uid, _ := sdkStopFixture(t)
			st := agentquestion.NewStore(c.paths.StateDir).CLIAsyncStore()
			r, e := st.Create(uid, json.RawMessage(`[{"question":"Pick?","options":[{"label":"yes (Recommended)"}]}]`), time.Hour)
			if e != nil {
				t.Fatal(e)
			}
			list, e := c.ListQuestions(context.Background(), "uid:"+uid)
			if e != nil || len(list.Questions) != 1 || list.Questions[0].Delivery != "async-message" || !list.Questions[0].CanAnswer || !list.Questions[0].CanDecline {
				t.Fatal(list, e)
			}
			if decline {
				out, e := c.DeclineQuestion(context.Background(), "uid:"+uid, r.ID)
				if e != nil || out.Delivery != "async-message" || out.Outcome != "pending" {
					t.Fatal(out, e)
				}
			} else {
				index := 1
				out, e := c.AnswerQuestion(context.Background(), AnswerQuestionRequest{Agent: "uid:" + uid, QuestionID: r.ID, Selections: []core.QuestionSelection{{Question: 1, Index: &index}}})
				if e != nil || out.Delivery != "async-message" || out.Outcome != "pending" {
					t.Fatal(out, e)
				}
			}
			list, e = c.ListQuestions(context.Background(), "uid:"+uid)
			if e != nil || list.Questions[0].Outcome != "pending" || list.Questions[0].ResultDeadline.IsZero() {
				t.Fatal(list, e)
			}
			if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
				v.Parts[0].State = "unknown"
				v.Outcome = "unknown"
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			list, e = c.ListQuestions(context.Background(), "uid:"+uid)
			if e != nil || !list.Questions[0].OutcomeUnknown {
				t.Fatalf("SDK unknown projection %+v %v", list, e)
			}
		})
	}
}

func TestSDKCLIAsyncSecretRecordsFailClosed(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "existing-answer"}[answered], func(t *testing.T) {
			c, _, uid, _ := sdkStopFixture(t)
			st := agentquestion.NewStore(c.paths.StateDir).CLIAsyncStore()
			r, err := st.Create(uid, json.RawMessage(`[{"id":"native-id-differs","question":"Token?","options":[{"label":"other"}]}]`), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if answered {
				if _, err = st.Resolve(r.ID, uid, map[string]string{"Token?": "private-fixture-value"}, ""); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(st.Path())
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.Replace(raw, []byte(`"question":"Token?"`), []byte(`"question":"Token?","isSecret":true`), 1)
			if err = os.WriteFile(st.Path(), raw, 0600); err != nil {
				t.Fatal(err)
			}
			list, err := c.ListQuestions(context.Background(), "uid:"+uid)
			if err != nil || len(list.Questions) != 1 || list.Questions[0].CanAnswer || list.Questions[0].CanDecline {
				t.Fatalf("secret actions advertised: %+v %v", list, err)
			}
			encoded, _ := json.Marshal(list)
			if bytes.Contains(encoded, []byte("private-fixture-value")) || len(list.Questions[0].Answers) != 0 {
				t.Fatal("SDK JSON leaked secret answer")
			}
			text := "private-fixture-value"
			_, err = c.AnswerQuestion(context.Background(), AnswerQuestionRequest{Agent: "uid:" + uid, QuestionID: r.ID, Selections: []core.QuestionSelection{{Question: 1, Text: &text}}})
			if err == nil || !strings.Contains(err.Error(), "question-secret-native-only") {
				t.Fatal("secret SDK answer admitted", err)
			}
			_, err = c.DeclineQuestion(context.Background(), "uid:"+uid, r.ID)
			if err == nil || !strings.Contains(err.Error(), "question-secret-native-only") {
				t.Fatal("secret SDK decline admitted", err)
			}
			after, _ := os.ReadFile(st.Path())
			if !bytes.Equal(raw, after) {
				t.Fatal("secret refusal changed record")
			}
			stored, _, err := st.Get(r.ID)
			if err != nil || stored.Pending() {
				t.Fatal("secret owns peer delivery", err)
			}
			if _, err = stored.Messages(); err == nil {
				t.Fatal("secret peer result serialized")
			}
		})
	}
}
