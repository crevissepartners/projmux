package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
	"github.com/crevissepartners/projmux/internal/services/questions"
)

// Refusal reason tokens of `agent question`. Like the persona tokens they are
// stable strings, carried verbatim in the error text of a usage error.
const (
	// questionReasonNotFound: no question with that id belongs to the Agent.
	questionReasonNotFound = "question-not-found"
	// questionReasonNotPending: the question was already answered.
	questionReasonNotPending = "question-not-pending"
	// questionReasonExpired: the answer window ended.
	questionReasonExpired = "question-expired"
	// questionReasonClosed: the record closed before an answer arrived; its
	// detail says why and whether the provider still asks it.
	questionReasonClosed = "question-closed"
	// questionReasonAnsweredElsewhere: Codex's own input surface resolved first.
	questionReasonAnsweredElsewhere = "question-answered-elsewhere"
	// questionReasonInvalidAnswer: the answer does not fit the question set.
	questionReasonInvalidAnswer = "question-invalid-answer"
	// questionReasonProviderUnsupported: the Agent is not a supported Agent.
	questionReasonProviderUnsupported = questions.ReasonProviderUnsupported
	// questionReasonSecretNativeOnly: a secret answer cannot travel in argv.
	questionReasonSecretNativeOnly = "question-secret-native-only"
)

// agentQuestionActions are the `agent question` subcommands in help order.
var agentQuestionActions = []string{"enable", "disable", "list", "answer"}

// agentQuestionRequest is one parsed `agent question` argv.
type agentQuestionRequest struct {
	action     string
	spelling   string
	flags      resourceQueryFlags
	questionID string
	json       bool
	options    repeatedFlag
	indexes    repeatedFlag
	texts      repeatedFlag
}

// runQuestion validates legacy controls and answers current or already waiting
// questions. Global changes affect admission, never an existing answer path.
func (c *agentCommand) runQuestion(args []string, stdout, stderr io.Writer) error {
	request, err := parseAgentQuestionArgs(args, stderr)
	if err != nil {
		return err
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	query, err := request.flags.query()
	if err != nil {
		return MapMetadataError(err)
	}
	agent, err := questions.SelectAgent(registry, query, request.spelling)
	if err != nil {
		return MapMetadataError(err)
	}
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", request.spelling, agent.Metadata.Name, detail, reason))
	}
	if err := questions.ValidateProvider(request.spelling, agent); err != nil {
		var refusal *questions.Failure
		if errors.As(err, &refusal) && refusal.Code == questionReasonProviderUnsupported {
			return usageError(refusal.Error())
		}
		return MapMetadataError(err)
	}
	switch request.action {
	case "enable":
		return c.setQuestionChannel(request, agent, true, stdout)
	case "disable":
		return c.setQuestionChannel(request, agent, false, stdout)
	case "list":
		return c.listQuestions(request, agent, stdout)
	default:
		return c.answerQuestion(request, agent, refuse, stdout)
	}
}

// parseAgentQuestionArgs parses one `agent question <action>` argv. The Agent
// reference is required: the command addresses another Agent's prompt, so it
// never falls back to the active Pane.
func parseAgentQuestionArgs(args []string, stderr io.Writer) (agentQuestionRequest, error) {
	if len(args) == 0 || !slices.Contains(agentQuestionActions, args[0]) {
		return agentQuestionRequest{}, usageError("agent question requires " + strings.Join(agentQuestionActions, ", "))
	}
	request := agentQuestionRequest{action: args[0], spelling: "agent question " + args[0]}
	fs := flag.NewFlagSet(request.spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	request.flags = resourceQueryFlags{kind: coremetadata.KindAgent}
	request.flags.register(fs)
	var output string
	if request.action == "list" {
		fs.StringVar(&output, "output", "", "result projection: json")
		fs.StringVar(&output, "o", "", "result projection: json (alias of --output)")
	}
	if request.action == "answer" {
		fs.Var(&request.options, "option", "repeatable <question-number>=<option label>")
		fs.Var(&request.indexes, "index", "repeatable <question-number>=<1-based option number>")
		fs.Var(&request.texts, "text", "<question-number>=<free text>, used as given instead of an option")
	}
	positionals, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return agentQuestionRequest{}, err
		}
		return agentQuestionRequest{}, flagParseError(err)
	}
	want, shape := 1, "<agent-ref>"
	if request.action == "answer" {
		want, shape = 2, "<agent-ref> <question-id>"
	}
	if len(positionals) != want {
		return agentQuestionRequest{}, usageError(fmt.Sprintf("%s requires %s", request.spelling, shape))
	}
	if output != "" && output != "json" {
		return agentQuestionRequest{}, usageError(fmt.Sprintf("%s: unsupported output %q; want json", request.spelling, output))
	}
	request.json = output == "json"
	request.flags.addPositionalRef(positionals[0])
	if request.action == "answer" {
		request.questionID = positionals[1]
		if len(request.options)+len(request.indexes)+len(request.texts) == 0 {
			return agentQuestionRequest{}, usageError(request.spelling + " requires at least one --option, --index, or --text")
		}
	}
	return request, nil
}

func (c *agentCommand) openQuestionStore() (*agentquestion.Store, error) {
	if c.questionStore == nil {
		return nil, errors.New("the agent question store is not configured")
	}
	return c.questionStore()
}

// setQuestionChannel preserves the legacy spelling and validation without mutation.
func (c *agentCommand) setQuestionChannel(request agentQuestionRequest, agent coremetadata.Agent, _ bool, stdout io.Writer) error {
	legacy := ""
	if coremetadata.QuestionChannelEnabled(agent) {
		legacy = "; legacy annotation ignored"
	}
	_, err := fmt.Fprintf(stdout, "agent/%s %s deprecated; no effect; questions follow the global setting%s\n", agent.Metadata.Name, request.spelling, legacy)
	return err
}

// agentQuestionList is the `agent question list -o json` projection.
type agentQuestionList struct {
	AgentUID       string                 `json:"agentUID"`
	AgentName      string                 `json:"agentName"`
	Channel        string                 `json:"channel"`
	Questions      []agentQuestionView    `json:"questions"`
	Policy         string                 `json:"policy"`
	ExactQuestions []ExactProcessQuestion `json:"exactQuestions,omitempty"`
}

// agentQuestionView is one question record.
type agentQuestionView struct {
	ID          string                `json:"id"`
	State       agentquestion.State   `json:"state"`
	Disposition string                `json:"disposition,omitempty"`
	CreatedAt   time.Time             `json:"createdAt"`
	Deadline    time.Time             `json:"deadline"`
	UpdatedAt   time.Time             `json:"updatedAt"`
	Prompts     []agentQuestionPrompt `json:"prompts"`
	Answers     map[string]string     `json:"answers,omitempty"`
	// asking is the text form's word on whether the provider still asks a
	// closed question; JSON readers take it from the disposition.
	Delivery  string `json:"delivery,omitempty"`
	CanAnswer bool   `json:"canAnswer"`
	asking    string
}

// agentQuestionPrompt is one question of a record, numbered the way `answer`
// addresses it.
type agentQuestionPrompt struct {
	Number      int                   `json:"number"`
	ID          string                `json:"id,omitempty"`
	Header      string                `json:"header,omitempty"`
	Question    string                `json:"question"`
	MultiSelect bool                  `json:"multiSelect"`
	IsOther     bool                  `json:"isOther"`
	IsSecret    bool                  `json:"isSecret,omitempty"`
	Options     []agentQuestionOption `json:"options"`
}

// agentQuestionOption is one option, numbered the way `--index` addresses it.
type agentQuestionOption struct {
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func (c *agentCommand) questionService() questions.Service {
	dispatcher := c.questionDispatcher
	if dispatcher == nil {
		dispatcher = processquestions.Dispatcher{RegistryPath: c.messagePaths.registryPath}
	}
	return questions.Service{OpenStore: c.openQuestionStore, LoadRegistry: c.loadRegistry, Answering: c.questionAnswering, Dispatcher: dispatcher}
}
func (c *agentCommand) listQuestions(request agentQuestionRequest, agent coremetadata.Agent, stdout io.Writer) error {
	projection, err := c.questionService().List(context.Background(), agent)
	if err != nil {
		return MapMetadataError(err)
	}
	var result agentQuestionList
	raw, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return err
	}
	for i := range result.Questions {
		result.Questions[i].asking = projection.Questions[i].Asking
	}
	if request.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	for _, v := range result.ExactQuestions {
		if v.Delivery != "process-exact-question" {
			continue
		}
		view := agentQuestionView{ID: v.QuestionID, State: v.State, Delivery: v.Delivery, CanAnswer: v.CanAnswer}
		for i, q := range v.Prompts {
			prompt := agentQuestionPrompt{Number: i + 1, ID: q.ID, Header: q.Header, Question: q.Question, MultiSelect: q.MultiSelect, IsOther: q.IsOther, IsSecret: q.IsSecret}
			for j, o := range q.Options {
				prompt.Options = append(prompt.Options, agentQuestionOption{Number: j + 1, Label: o.Label, Description: o.Description})
			}
			view.Prompts = append(view.Prompts, prompt)
		}
		result.Questions = append(result.Questions, view)
	}
	return writeAgentQuestionList(stdout, result, c.clock())
}
func writeAgentQuestionList(out io.Writer, result agentQuestionList, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "agent/%s question channel %s\n", result.AgentName, result.Channel)
	if len(result.Questions) == 0 {
		b.WriteString("no questions\n")
	}
	for _, view := range result.Questions {
		fmt.Fprintf(&b, "%s\t%s", view.ID, view.State)
		switch {
		case view.asking != "":
			fmt.Fprintf(&b, " (%s; %s)", view.Disposition, view.asking)
		case view.Disposition != "":
			fmt.Fprintf(&b, " (%s)", view.Disposition)
		}
		if view.State == agentquestion.StateWaiting && !view.Deadline.IsZero() {
			fmt.Fprintf(&b, "\tdeadline %s (%s left)", view.Deadline.UTC().Format(time.RFC3339), view.Deadline.Sub(now).Round(time.Second))
		}
		b.WriteString("\n")
		for _, prompt := range view.Prompts {
			fmt.Fprintf(&b, "  %d.", prompt.Number)
			if prompt.Header != "" {
				fmt.Fprintf(&b, " [%s]", prompt.Header)
			}
			fmt.Fprintf(&b, " %s", prompt.Question)
			if prompt.IsSecret {
				if view.Delivery == "process-exact-question" || (len(result.ExactQuestions) > 0 && view.CanAnswer) {
					b.WriteString(" (secret; masked typed answer required)")
				} else {
					b.WriteString(" (secret; answer in the Codex window)")
				}
			}
			if prompt.MultiSelect {
				b.WriteString(" (multi-select)")
			}
			b.WriteString("\n")
			for _, option := range prompt.Options {
				fmt.Fprintf(&b, "     %d) %s", option.Number, option.Label)
				if option.Description != "" {
					fmt.Fprintf(&b, " - %s", option.Description)
				}
				b.WriteString("\n")
			}
			answerKey := prompt.Question
			if prompt.ID != "" {
				answerKey = prompt.ID
			}
			if answer, ok := view.Answers[answerKey]; ok && !prompt.IsSecret {
				if prompt.ID != "" {
					var values []string
					if json.Unmarshal([]byte(answer), &values) == nil {
						answer = strings.Join(values, ", ")
					}
				}
				fmt.Fprintf(&b, "     answer: %s\n", answer)
			}
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func questionRequest(r agentQuestionRequest) questions.Request {
	return questions.Request{Spelling: r.spelling, QuestionID: r.questionID, Options: r.options, Indexes: r.indexes, Texts: r.texts}
}
func (c *agentCommand) answerQuestion(request agentQuestionRequest, agent coremetadata.Agent, _ func(string, string) error, stdout io.Writer) error {
	result, err := c.questionService().Answer(context.Background(), questionRequest(request), agent)
	if err != nil {
		if result.Delivery == "async" && result.Outcome != "" {
			fmt.Fprintf(stdout, "%s %s for agent/%s\n", result.QuestionID, result.Outcome, result.AgentName)
		}
		return MapMetadataError(err)
	}
	verb := "answered"
	if result.Delivery == "async" {
		verb = result.Outcome
	}
	_, err = fmt.Fprintf(stdout, "%s %s for agent/%s\n", result.QuestionID, verb, result.AgentName)
	return err
}
func questionRecordRefusal(record agentquestion.Record) (string, string) {
	return questions.RecordRefusal(record)
}
func questionClosedAsking(record agentquestion.Record) string { return questions.ClosedAsking(record) }
func questionStateRefusal(state agentquestion.State) (string, string) {
	return questions.StateRefusal(state)
}
func questionStoreRefusal(err error) (string, string) {
	switch {
	case errors.Is(err, agentquestion.ErrNotPending):
		return questionStateRefusal(agentquestion.StateAnswered)
	case errors.Is(err, agentquestion.ErrExpired):
		return questionStateRefusal(agentquestion.StateExpired)
	case errors.Is(err, agentquestion.ErrClosed):
		return questionStateRefusal(agentquestion.StateClosed)
	default:
		return questions.StoreRefusal(err)
	}
}
