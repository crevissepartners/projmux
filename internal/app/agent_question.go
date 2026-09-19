package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
)

// Refusal reason tokens of `agent question`. Like the persona tokens they are
// stable strings, carried verbatim in the error text of a usage error.
const (
	// questionReasonNotFound: no question with that id belongs to the Agent.
	questionReasonNotFound = "question-not-found"
	// questionReasonNotPending: the question was already answered.
	questionReasonNotPending = "question-not-pending"
	// questionReasonExpired: the answer window ended; Claude Code asked the
	// question in its own prompt.
	questionReasonExpired = "question-expired"
	// questionReasonClosed: the prompt was canceled, or the channel was turned
	// off, before an answer arrived.
	questionReasonClosed = "question-closed"
	// questionReasonInvalidAnswer: the answer does not fit the question set.
	questionReasonInvalidAnswer = "question-invalid-answer"
	// questionReasonChannelOff: the Agent is not opted in.
	questionReasonChannelOff = "question-channel-off"
	// questionReasonProviderUnsupported: the Agent is not a Claude Agent.
	questionReasonProviderUnsupported = "question-provider-unsupported"
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

// runQuestion lets the operator answer an opted-in Claude Agent's
// AskUserQuestion prompts from the command line.
//
// `enable` and `disable` set and clear the Agent's question channel
// annotation. While it is set, the PreToolUse hook `agent integrate claude`
// installs holds each question open for a bounded window and records it;
// `list` shows those records and `answer` settles one. `disable` also closes
// every question the Agent still holds open, which hands each back to Claude
// Code's own prompt at once.
func (c *agentCommand) runQuestion(args []string, stdout, stderr io.Writer) error {
	request, err := parseAgentQuestionArgs(args, stderr)
	if err != nil {
		return err
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := request.flags.resolve(selector.VerbTopic, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	found, ok := registry.Agent(resolution.Matches[0].UID)
	if !ok {
		return fmt.Errorf("%s: resolved uid %q is no longer in the registry", request.spelling, resolution.Matches[0].UID)
	}
	agent := found.Clone()
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", request.spelling, agent.Metadata.Name, detail, reason))
	}
	if coremetadata.NormalizeProvider(agent.Spec.Provider) != aiModeClaude {
		return refuse(questionReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; the question channel applies only to --provider %s", agent.Spec.Provider, aiModeClaude))
	}
	switch request.action {
	case "enable":
		return c.setQuestionChannel(request, agent, true, stdout)
	case "disable":
		return c.setQuestionChannel(request, agent, false, stdout)
	case "list":
		return c.listQuestions(request, agent, stdout)
	default:
		if !coremetadata.QuestionChannelEnabled(agent) {
			return refuse(questionReasonChannelOff, "is not opted in; run `projmux agent question enable` first")
		}
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
		return agentQuestionRequest{}, usageError(err.Error())
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

// setQuestionChannel sets or clears the annotation in one Registry mutation.
// Turning it off then closes the questions the Agent still holds open.
func (c *agentCommand) setQuestionChannel(request agentQuestionRequest, agent coremetadata.Agent, on bool, stdout io.Writer) error {
	was := coremetadata.QuestionChannelEnabled(agent)
	if was != on {
		if err := c.mutateAgent(agent.Metadata.UID, func(reg *coremetadata.Registry, mut coremetadata.Mutator) error {
			_, err := mut.SetAgentQuestionChannel(reg, agent.Metadata.UID, on)
			return err
		}); err != nil {
			return err
		}
	}
	state := "on"
	if !on {
		state = "off"
	}
	if on {
		if was {
			_, err := fmt.Fprintf(stdout, "agent/%s question channel is already on\n", agent.Metadata.Name)
			return err
		}
		_, err := fmt.Fprintf(stdout, "agent/%s question channel %s\n", agent.Metadata.Name, state)
		return err
	}
	store, err := c.openQuestionStore()
	if err != nil {
		return fmt.Errorf("%s: agent/%s question channel is off, but its waiting questions were not closed: %w", request.spelling, agent.Metadata.Name, err)
	}
	closed, err := store.CloseAgent(agent.Metadata.UID)
	if err != nil {
		return fmt.Errorf("%s: agent/%s question channel is off, but its waiting questions were not closed: %w", request.spelling, agent.Metadata.Name, err)
	}
	prefix := fmt.Sprintf("agent/%s question channel %s", agent.Metadata.Name, state)
	if !was {
		prefix = fmt.Sprintf("agent/%s question channel is already off", agent.Metadata.Name)
	}
	_, err = fmt.Fprintf(stdout, "%s; closed %d waiting question(s)\n", prefix, closed)
	return err
}

// agentQuestionList is the `agent question list -o json` projection.
type agentQuestionList struct {
	AgentUID  string              `json:"agentUID"`
	AgentName string              `json:"agentName"`
	Channel   string              `json:"channel"`
	Questions []agentQuestionView `json:"questions"`
}

// agentQuestionView is one question record.
type agentQuestionView struct {
	ID        string                `json:"id"`
	State     agentquestion.State   `json:"state"`
	CreatedAt time.Time             `json:"createdAt"`
	Deadline  time.Time             `json:"deadline"`
	UpdatedAt time.Time             `json:"updatedAt"`
	Prompts   []agentQuestionPrompt `json:"prompts"`
	Answers   map[string]string     `json:"answers,omitempty"`
}

// agentQuestionPrompt is one question of a record, numbered the way `answer`
// addresses it.
type agentQuestionPrompt struct {
	Number      int                   `json:"number"`
	Header      string                `json:"header,omitempty"`
	Question    string                `json:"question"`
	MultiSelect bool                  `json:"multiSelect"`
	Options     []agentQuestionOption `json:"options"`
}

// agentQuestionOption is one option, numbered the way `--index` addresses it.
type agentQuestionOption struct {
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func (c *agentCommand) listQuestions(request agentQuestionRequest, agent coremetadata.Agent, stdout io.Writer) error {
	store, err := c.openQuestionStore()
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	records, err := store.List(agent.Metadata.UID)
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	result := agentQuestionList{AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Channel: "off", Questions: []agentQuestionView{}}
	if coremetadata.QuestionChannelEnabled(agent) {
		result.Channel = "on"
	}
	for _, record := range records {
		questions, err := record.ParsedQuestions()
		if err != nil {
			continue
		}
		view := agentQuestionView{ID: record.ID, State: record.State, CreatedAt: record.CreatedAt, Deadline: record.Deadline, UpdatedAt: record.UpdatedAt, Answers: record.Answers}
		for i, question := range questions {
			prompt := agentQuestionPrompt{Number: i + 1, Header: question.Header, Question: question.Question, MultiSelect: question.MultiSelect}
			for j, option := range question.Options {
				prompt.Options = append(prompt.Options, agentQuestionOption{Number: j + 1, Label: option.Label, Description: option.Description})
			}
			view.Prompts = append(view.Prompts, prompt)
		}
		result.Questions = append(result.Questions, view)
	}
	if request.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
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
		if view.State == agentquestion.StateWaiting {
			fmt.Fprintf(&b, "\tdeadline %s (%s left)", view.Deadline.UTC().Format(time.RFC3339), view.Deadline.Sub(now).Round(time.Second))
		}
		b.WriteString("\n")
		for _, prompt := range view.Prompts {
			fmt.Fprintf(&b, "  %d.", prompt.Number)
			if prompt.Header != "" {
				fmt.Fprintf(&b, " [%s]", prompt.Header)
			}
			fmt.Fprintf(&b, " %s", prompt.Question)
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
			if answer, ok := view.Answers[prompt.Question]; ok {
				fmt.Fprintf(&b, "     answer: %s\n", answer)
			}
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// answerQuestion validates one answer against the recorded question set and
// settles the record under the store lock.
func (c *agentCommand) answerQuestion(request agentQuestionRequest, agent coremetadata.Agent, refuse func(string, string) error, stdout io.Writer) error {
	if !agentquestion.ValidID(request.questionID) {
		return refuse(questionReasonNotFound, fmt.Sprintf("has no question %q", request.questionID))
	}
	store, err := c.openQuestionStore()
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	record, found, err := store.Get(request.questionID)
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	if !found || record.AgentUID != agent.Metadata.UID {
		return refuse(questionReasonNotFound, fmt.Sprintf("has no question %q", request.questionID))
	}
	if reason, detail := questionStateRefusal(record.State); reason != "" {
		return refuse(reason, fmt.Sprintf("question %s %s", record.ID, detail))
	}
	questions, err := record.ParsedQuestions()
	if err != nil {
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	selections, err := agentQuestionSelections(request, questions)
	if err != nil {
		return refuse(questionReasonInvalidAnswer, err.Error())
	}
	answers, err := agentquestion.BuildAnswers(questions, selections)
	if err != nil {
		return refuse(questionReasonInvalidAnswer, err.Error())
	}
	answered, err := store.Answer(record.ID, agent.Metadata.UID, answers)
	if err != nil {
		if reason, detail := questionStoreRefusal(err); reason != "" {
			return refuse(reason, fmt.Sprintf("question %s %s", record.ID, detail))
		}
		return fmt.Errorf("%s: %w", request.spelling, err)
	}
	_, err = fmt.Fprintf(stdout, "%s answered for agent/%s\n", answered.ID, agent.Metadata.Name)
	return err
}

// questionStateRefusal is the refusal for a record that cannot take an answer.
func questionStateRefusal(state agentquestion.State) (string, string) {
	switch state {
	case agentquestion.StateWaiting:
		return "", ""
	case agentquestion.StateAnswered:
		return questionReasonNotPending, "is already answered"
	case agentquestion.StateExpired:
		return questionReasonExpired, "expired; Claude Code asked it in its own prompt"
	default:
		return questionReasonClosed, "was closed before an answer arrived"
	}
}

// questionStoreRefusal maps a store refusal onto its reason token.
func questionStoreRefusal(err error) (string, string) {
	switch {
	case errors.Is(err, agentquestion.ErrNotFound):
		return questionReasonNotFound, "does not exist"
	case errors.Is(err, agentquestion.ErrNotPending):
		return questionStateRefusal(agentquestion.StateAnswered)
	case errors.Is(err, agentquestion.ErrExpired):
		return questionStateRefusal(agentquestion.StateExpired)
	case errors.Is(err, agentquestion.ErrClosed):
		return questionStateRefusal(agentquestion.StateClosed)
	case errors.Is(err, agentquestion.ErrInvalidAnswer):
		return questionReasonInvalidAnswer, err.Error()
	default:
		return "", ""
	}
}

// agentQuestionSelections folds the --option, --index, and --text occurrences
// into one Selection per question. Each occurrence is <question-number>=<value>
// with a 1-based question number.
func agentQuestionSelections(request agentQuestionRequest, questions []agentquestion.Question) (map[int]agentquestion.Selection, error) {
	selections := map[int]agentquestion.Selection{}
	split := func(flagName, raw string) (int, string, error) {
		number, value, ok := strings.Cut(raw, "=")
		index, err := strconv.Atoi(strings.TrimSpace(number))
		if !ok || err != nil || index < 1 {
			return 0, "", fmt.Errorf("--%s %q is not <question-number>=<value>", flagName, raw)
		}
		if index > len(questions) {
			return 0, "", fmt.Errorf("--%s %q names question %d; this question set has %d", flagName, raw, index, len(questions))
		}
		return index - 1, value, nil
	}
	for _, raw := range request.options {
		index, label, err := split("option", raw)
		if err != nil {
			return nil, err
		}
		selection := selections[index]
		selection.Labels = append(selection.Labels, label)
		selections[index] = selection
	}
	for _, raw := range request.indexes {
		index, value, err := split("index", raw)
		if err != nil {
			return nil, err
		}
		position, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || position < 1 || position > len(questions[index].Options) {
			return nil, fmt.Errorf("--index %q: question %d has options 1-%d", raw, index+1, len(questions[index].Options))
		}
		selection := selections[index]
		selection.Labels = append(selection.Labels, questions[index].Options[position-1].Label)
		selections[index] = selection
	}
	for _, raw := range request.texts {
		index, text, err := split("text", raw)
		if err != nil {
			return nil, err
		}
		selection := selections[index]
		if selection.HasText {
			return nil, fmt.Errorf("question %d takes one --text", index+1)
		}
		selection.Text, selection.HasText = text, true
		selections[index] = selection
	}
	return selections, nil
}
