package questions

import (
	"context"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
)

// resolveCLI precedes provider dispatch: an offline Agent can still receive an
// operator answer into its durable outbox without contacting a stopped host.
func (c Service) resolveCLI(ctx context.Context, req Request, agent metadata.Agent, decline bool) (bool, AnswerResult, error) {
	result := AnswerResult{QuestionID: req.QuestionID, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Delivery: "async-message"}
	if c.OpenStore == nil {
		return false, result, nil
	}
	st, e := c.OpenStore()
	if e != nil {
		return true, result, e
	}
	cli := st.CLIAsyncStore()
	r, found, e := cli.Get(req.QuestionID)
	if e != nil {
		return true, result, e
	}
	if !found || r.AgentUID != agent.Metadata.UID {
		return false, result, nil
	}
	fail := func(code string, err error) (bool, AnswerResult, error) {
		return true, result, &Failure{Code: code, Message: code, Cause: err}
	}
	if _, err := agentquestion.ParseCLIAsyncQuestions(r.Questions); err != nil {
		return fail(questionReasonSecretNativeOnly, err)
	}
	if r.State != agentquestion.StateWaiting {
		code := questionReasonNotPending
		if r.State == agentquestion.StateExpired {
			code = questionReasonExpired
		}
		return fail(code, agentquestion.ErrNotPending)
	}
	reason := ""
	var answers map[string]string
	if decline {
		reason = "declined"
	} else {
		qs, e := agentquestion.ParseQuestions(r.Questions)
		if e != nil {
			return true, result, e
		}
		selections, e := Selections(req, qs)
		if e != nil {
			return fail(questionReasonInvalidAnswer, e)
		}
		answers, e = agentquestion.BuildAnswers(qs, selections)
		if e != nil {
			return fail(questionReasonInvalidAnswer, e)
		}
	}
	if e = ctx.Err(); e != nil {
		return true, result, e
	}
	r, e = cli.Resolve(req.QuestionID, agent.Metadata.UID, answers, reason)
	if e != nil {
		if code, _ := StoreRefusal(e); code != "" {
			return fail(code, e)
		}
		return true, result, e
	}
	result.Outcome = r.Outcome
	if r.ResultReason == "expired" {
		return fail(questionReasonExpired, agentquestion.ErrExpired)
	}
	return true, result, nil
}
