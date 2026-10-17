package projmux

import (
	"context"
	"encoding/json"
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
