package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
	"github.com/crevissepartners/projmux/internal/services/questions"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// QuestionDispatcher overrides the existing host transport, principally for
// isolated fixtures. Implementations must honor context and exact Agent identity.
type QuestionDispatcher interface {
	ListQuestions(context.Context, string) ([]core.ExactQuestion, error)
	AnswerQuestion(context.Context, string, string, []core.QuestionSelection) error
}

const (
	CodeQuestionNotFound            Code = "question-not-found"
	CodeQuestionNotPending          Code = "question-not-pending"
	CodeQuestionExpired             Code = "question-expired"
	CodeQuestionClosed              Code = "question-closed"
	CodeQuestionAnsweredElsewhere   Code = "question-answered-elsewhere"
	CodeQuestionInvalidAnswer       Code = "question-invalid-answer"
	CodeQuestionProviderUnsupported Code = "question-provider-unsupported"
	CodeQuestionSecretNativeOnly    Code = "question-secret-native-only"
)

// AnswerQuestionRequest addresses one explicit Agent and question. No active
// Pane is inferred. Selections are repeated option, index or text occurrences.
type AnswerQuestionRequest struct {
	Agent      string
	QuestionID string
	Selections []core.QuestionSelection
}

func (c *Client) questionService() questions.Service {
	dispatcher := c.questionDispatcher
	if dispatcher == nil {
		dispatcher = processquestions.Dispatcher{RegistryPath: store.PathFor(c.paths.StateDir)}
	}
	return questions.Service{
		OpenStore:    func() (*agentquestion.Store, error) { return agentquestion.NewStore(c.paths.StateDir), nil },
		LoadRegistry: func() (metadata.Registry, error) { return resources.Load(c.paths) },
		Answering: func() config.AgentQuestionAnswering {
			v, _ := config.LoadAgentQuestionAnsweringFile(c.paths.AgentQuestionAnsweringFile())
			return v
		},
		Dispatcher: dispatcher,
	}
}
func (c *Client) questionAgent(ctx context.Context, ref, spelling string) (metadata.Agent, error) {
	if err := ctx.Err(); err != nil {
		return metadata.Agent{}, err
	}
	if c.pathsErr != nil {
		return metadata.Agent{}, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return metadata.Agent{}, resourceRefusal(err)
	}
	agent, err := questions.ResolveAgent(reg, ref)
	if err != nil {
		return metadata.Agent{}, resourceRefusal(err)
	}
	if err = questions.ValidateProvider(spelling, agent); err != nil {
		return metadata.Agent{}, questionRefusal(err)
	}
	return agent, nil
}

// ListQuestions returns the same redacted display projection as agent question
// list -o json. A running process Agent queries its existing host; it never
// starts a provider. Reading may reconcile questions in the existing store.
func (c *Client) ListQuestions(ctx context.Context, agentRef string) (core.QuestionList, error) {
	agent, err := c.questionAgent(ctx, agentRef, "agent question list")
	if err != nil {
		return core.QuestionList{}, err
	}
	result, err := c.questionService().List(ctx, agent)
	if err != nil {
		return core.QuestionList{}, questionRefusal(err)
	}
	var out core.QuestionList
	b, err := json.Marshal(result)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}

// AnswerQuestion follows the existing store or exact process dispatcher path.
// Secret questions require the provider's native surface and are refused here.
// A canceled context before admission changes nothing. An accepted write is
// never retried, including when its delivery outcome is uncertain.
func (c *Client) AnswerQuestion(ctx context.Context, input AnswerQuestionRequest) (core.QuestionAnswerResult, error) {
	agent, err := c.questionAgent(ctx, input.Agent, "agent question answer")
	if err != nil {
		return core.QuestionAnswerResult{}, err
	}
	request := questions.Request{Spelling: "agent question answer", QuestionID: input.QuestionID}
	if len(input.Selections) == 0 {
		return core.QuestionAnswerResult{}, &Refusal{Code: CodeQuestionInvalidAnswer, message: "AnswerQuestionRequest.Selections requires at least one selection with Option, Index, or Text"}
	}
	for _, v := range input.Selections {
		n := 0
		if v.Option != nil {
			n++
		}
		if v.Index != nil {
			n++
		}
		if v.Text != nil {
			n++
		}
		if n != 1 {
			return core.QuestionAnswerResult{}, &Refusal{Code: CodeQuestionInvalidAnswer, message: "each question selection requires exactly one Option, Index, or Text"}
		}
		prefix := strconv.Itoa(v.Question) + "="
		if v.Option != nil {
			request.Options = append(request.Options, prefix+*v.Option)
		}
		if v.Index != nil {
			request.Indexes = append(request.Indexes, prefix+strconv.Itoa(*v.Index))
		}
		if v.Text != nil {
			request.Texts = append(request.Texts, prefix+*v.Text)
		}
	}
	result, err := c.questionService().Answer(ctx, request, agent)
	return core.QuestionAnswerResult{QuestionID: result.QuestionID, AgentUID: result.AgentUID, AgentName: result.AgentName}, questionRefusal(err)
}
func questionRefusal(err error) error {
	// The host can reject a validated choice when its live question changes.
	// Preserve the CLI transport error text while exposing its refusal code.
	if errors.Is(err, agentquestion.ErrInvalidAnswer) {
		return &Refusal{Code: CodeQuestionInvalidAnswer, message: err.Error()}
	}
	var failure *questions.Failure
	if errors.As(err, &failure) {
		message := failure.Error()
		var selection *questions.SelectionError
		if errors.As(failure, &selection) {
			message = "AnswerQuestionRequest.Selections: " + selection.InputMessage
		}
		return &Refusal{Code: Code(failure.Code), message: message}
	}
	return err
}

type publicQuestionDispatcher struct{ QuestionDispatcher }

func (d publicQuestionDispatcher) ReadExactProcessQuestions(ctx context.Context, uid string) ([]processquestions.ExactQuestion, error) {
	result, err := d.ListQuestions(ctx, uid)
	if err != nil {
		return nil, err
	}
	var out []processquestions.ExactQuestion
	b, err := json.Marshal(result)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}
func (d publicQuestionDispatcher) AnswerExactProcessQuestion(ctx context.Context, uid, id string, selections map[int]agentquestion.Selection) error {
	out := []core.QuestionSelection{}
	keys := make([]int, 0, len(selections))
	for i := range selections {
		keys = append(keys, i)
	}
	slices.Sort(keys)
	for _, i := range keys {
		v := selections[i]
		for _, label := range v.Labels {
			value := label
			out = append(out, core.QuestionSelection{Question: i + 1, Option: &value})
		}
		if v.HasText {
			value := v.Text
			out = append(out, core.QuestionSelection{Question: i + 1, Text: &value})
		}
	}
	return d.AnswerQuestion(ctx, uid, id, out)
}
