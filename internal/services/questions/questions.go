// Package questions shares Agent question selection, validation and execution between fronts.
package questions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

type Dispatcher interface {
	ReadExactProcessQuestions(context.Context, string) ([]processquestions.ExactQuestion, error)
	AnswerExactProcessQuestion(context.Context, string, string, map[int]agentquestion.Selection) error
}
type Service struct {
	OpenStore    func() (*agentquestion.Store, error)
	LoadRegistry func() (coremetadata.Registry, error)
	Answering    func() config.AgentQuestionAnswering
	Dispatcher   Dispatcher
}
type Request struct {
	Spelling, QuestionID    string
	Options, Indexes, Texts []string
}
type DeclineResult struct {
	QuestionID, AgentUID, AgentName string
	Delivery, Outcome               string
}
type AnswerResult struct {
	QuestionID, AgentUID, AgentName string
	Delivery, Outcome               string
}
type Failure struct {
	Code, Message string
	Cause         error
}

func (e *Failure) Unwrap() error { return e.Cause }

func (e *Failure) Error() string            { return e.Message }
func (e *Failure) MetadataUsageError() bool { return true }
func Refuse(spelling string, agent coremetadata.Agent, reason, detail string) error {
	return &Failure{Code: reason, Message: fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", spelling, agent.Metadata.Name, detail, reason)}
}
func ValidateProvider(spelling string, agent coremetadata.Agent) error {
	provider := coremetadata.NormalizeProvider(agent.Spec.Provider)
	if provider != "claude" && provider != "codex" {
		return Refuse(spelling, agent, ReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; the question channel applies only to Claude and Codex", agent.Spec.Provider))
	}
	return nil
}

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
	// ReasonProviderUnsupported: the Agent is not a supported Agent.
	ReasonProviderUnsupported = "question-provider-unsupported"
	// questionReasonSecretNativeOnly: a secret answer cannot travel in argv.
	questionReasonSecretNativeOnly = "question-secret-native-only"
)

// List is the `agent question list -o json` projection.
type List struct {
	Provider       string                           `json:"provider,omitempty"`
	Runtime        coremetadata.RuntimeKind         `json:"runtime,omitempty"`
	Phase          coremetadata.AgentPhase          `json:"phase,omitempty"`
	Session        string                           `json:"session,omitempty"`
	AgentUID       string                           `json:"agentUID"`
	AgentName      string                           `json:"agentName"`
	Channel        string                           `json:"channel"`
	Questions      []View                           `json:"questions"`
	Policy         string                           `json:"policy"`
	ExactQuestions []processquestions.ExactQuestion `json:"exactQuestions,omitempty"`
}

// View is one question record.
type View struct {
	ItemID      string              `json:"itemId,omitempty"`
	ID          string              `json:"id"`
	State       agentquestion.State `json:"state"`
	Disposition string              `json:"disposition,omitempty"`
	CreatedAt   time.Time           `json:"createdAt"`
	Deadline    time.Time           `json:"deadline"`
	UpdatedAt   time.Time           `json:"updatedAt"`
	Prompts     []Prompt            `json:"prompts"`
	Answers     map[string]string   `json:"answers,omitempty"`
	// asking is the text form's word on whether the provider still asks a
	// closed question; JSON readers take it from the disposition.
	OutcomeUnknown bool      `json:"outcomeUnknown,omitempty"`
	Outcome        string    `json:"outcome,omitempty"`
	ResultDeadline time.Time `json:"resultDeadline,omitzero"`
	ResultReason   string    `json:"resultReason,omitempty"`
	Delivery       string    `json:"delivery,omitempty"`
	CanAnswer      bool      `json:"canAnswer"`
	CanDecline     bool      `json:"canDecline"`
	Asking         string    `json:"-"`
}

// Prompt is one question of a record, numbered the way `answer`
// addresses it.
type Prompt struct {
	Number      int      `json:"number"`
	ID          string   `json:"id,omitempty"`
	Header      string   `json:"header,omitempty"`
	Question    string   `json:"question"`
	MultiSelect bool     `json:"multiSelect"`
	IsOther     bool     `json:"isOther"`
	IsSecret    bool     `json:"isSecret,omitempty"`
	Options     []Option `json:"options"`
}

// Option is one option, numbered the way `--index` addresses it.
type Option struct {
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func (c Service) List(ctx context.Context, agent coremetadata.Agent) (List, error) {
	if err := ctx.Err(); err != nil {
		return List{}, err
	}
	request := Request{Spelling: "agent question list"}
	store, err := c.OpenStore()
	if err != nil {
		return List{}, fmt.Errorf("%s: %w", request.Spelling, err)
	}
	cliRecords := map[string]agentquestion.CLIAsyncRecord{}
	readRecords := func() ([]agentquestion.Record, error) {
		legacy, err := store.List(agent.Metadata.UID)
		if err != nil {
			return nil, err
		}
		async, err := store.AsyncStore().List(agent.Metadata.UID)
		if err != nil {
			return nil, err
		}
		cli, err := store.CLIAsyncStore().List(agent.Metadata.UID)
		if err != nil {
			return nil, err
		}
		for _, r := range cli {
			cliRecords[r.ID] = r
			legacy = append(legacy, r.Record)
		}
		return append(legacy, async...), nil
	}
	records, err := readRecords()
	if err != nil {
		return List{}, fmt.Errorf("%s: %w", request.Spelling, err)
	}
	result := List{AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Channel: "off", Questions: []View{}}
	result.Policy = "native"
	if c.Answering != nil && c.Answering() == config.AgentQuestionAnsweringProjmux {
		result.Channel = "on"
		result.Policy = "projmux"
	}
	reg, err := c.LoadRegistry()
	if err != nil {
		return List{}, err
	}
	process := false
	if pane, ok := reg.Pane(agent.Status.PaneRef); ok {
		process = pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess
	}
	if process && agent.Status.Phase == coremetadata.PhaseRunning {
		result.ExactQuestions, err = c.Dispatcher.ReadExactProcessQuestions(ctx, agent.Metadata.UID)
		result.ExactQuestions = slices.Clone(result.ExactQuestions)
		for i := range result.ExactQuestions {
			result.ExactQuestions[i].ItemID = ""
		}
		if err != nil {
			return List{}, err
		}
		records, err = readRecords()
		if err != nil {
			return List{}, err
		}
	}
	c.projectRecords(&result, agent, process, records, cliRecords)

	return result, nil
}

// projectRecords is the shared display projection. Retrieval and error order
// stay with each caller; the default List path retains its existing policy.
func (c Service) projectRecords(result *List, agent coremetadata.Agent, process bool, records []agentquestion.Record, cliRecords map[string]agentquestion.CLIAsyncRecord) {
	for _, record := range records {
		questions, err := record.ParsedQuestions()
		if err != nil {
			continue
		}
		view := View{ID: record.ID, State: record.State, Disposition: record.Disposition, CreatedAt: record.CreatedAt, Deadline: record.Deadline, UpdatedAt: record.UpdatedAt, Answers: record.Answers, Delivery: "held-question", CanAnswer: record.State == agentquestion.StateWaiting, Asking: ClosedAsking(record)}
		if record.Delivery == "async" {
			view.Delivery, view.CanAnswer = "async", false
			current := false
			for _, exact := range result.ExactQuestions {
				if exact.QuestionID == record.ID && exact.Delivery == "async" {
					current = true
					capability, supported := c.Dispatcher.(processquestions.ReceiptDispatcher)
					view.CanAnswer = supported && capability.SupportsAsyncQuestionAnswers() && exact.CanAnswer && exact.State == agentquestion.StateWaiting && record.State == agentquestion.StateWaiting
					decline, supportsDecline := c.Dispatcher.(processquestions.DeclineReceiptDispatcher)
					view.CanDecline = supportsDecline && decline.SupportsAsyncQuestionDeclines() && exact.CanDecline && exact.State == agentquestion.StateWaiting && record.State == agentquestion.StateWaiting
					for _, prompt := range questions {
						if prompt.IsSecret {
							view.CanDecline = false
						}
					}
					break
				}
			}
			if !current && view.State == agentquestion.StateWaiting {
				view.State, view.Disposition = agentquestion.StateClosed, "process-stale"
			}
		}
		if record.Delivery == "async-message" {
			view.Outcome = cliRecords[record.ID].Outcome
			for _, part := range cliRecords[record.ID].Parts {
				if part.State == "unknown" {
					view.OutcomeUnknown = true
					break
				}
			}
			view.ResultReason = cliRecords[record.ID].ResultReason
			view.ResultDeadline = cliRecords[record.ID].ResultDeadline
			view.Delivery = "async-message"
			view.CanAnswer = record.State == agentquestion.StateWaiting
			view.CanDecline = view.CanAnswer
		}
		if record.Delivery != "async-message" && process && agent.Status.Phase != coremetadata.PhaseRunning {
			view.CanAnswer = false
		}
		for i, question := range questions {
			// Claude always takes free text, as its popup and BuildAnswers do;
			// Codex takes it only when the question sets isOther.
			prompt := Prompt{Number: i + 1, ID: question.ID, Header: question.Header, Question: question.Question, MultiSelect: question.MultiSelect, IsOther: record.Provider != "codex" || question.IsOther, IsSecret: question.IsSecret}
			for j, option := range question.Options {
				prompt.Options = append(prompt.Options, Option{Number: j + 1, Label: option.Label, Description: option.Description})
			}
			if question.IsSecret {
				delete(view.Answers, question.ID)
				if record.Delivery == "async-message" {
					delete(view.Answers, question.Question)
					view.CanAnswer, view.CanDecline = false, false
					view.Disposition = "secret-native-only"
				}
			}
			view.Prompts = append(view.Prompts, prompt)
		}
		result.Questions = append(result.Questions, view)
	}
}

// Decline submits an explicit operator non-answer notice for a complete async
// set. It shares the host's answer claim, never a blank selection or cancel RPC.
func (c Service) Decline(ctx context.Context, request Request, agent coremetadata.Agent) (DeclineResult, error) {
	if err := ctx.Err(); err != nil {
		return DeclineResult{}, err
	}
	result := DeclineResult{QuestionID: request.QuestionID, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name}
	failure := func(code string, cause error) (DeclineResult, error) {
		message := code
		if cause != nil {
			message += ": " + cause.Error()
		}
		return result, &Failure{Code: code, Message: message, Cause: cause}
	}
	if !agentquestion.ValidID(request.QuestionID) {
		return failure(questionReasonNotFound, agentquestion.ErrNotFound)
	}
	if len(request.Options)+len(request.Indexes)+len(request.Texts) != 0 {
		return failure(questionReasonInvalidAnswer, agentquestion.ErrInvalidAnswer)
	}
	if handled, cliResult, cliErr := c.resolveCLI(ctx, request, agent, true); handled {
		result.Delivery = "async-message"
		result.Outcome = cliResult.Outcome
		return result, cliErr
	}
	reg, err := c.LoadRegistry()
	if err != nil {
		return result, err
	}
	pane, process := reg.Pane(agent.Status.PaneRef)
	process = process && pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess
	capability, supported := c.Dispatcher.(processquestions.DeclineReceiptDispatcher)
	if process && agent.Status.Phase == coremetadata.PhaseRunning {
		views, readErr := c.Dispatcher.ReadExactProcessQuestions(ctx, agent.Metadata.UID)
		if readErr != nil {
			return result, readErr
		}
		for _, view := range views {
			if view.QuestionID != request.QuestionID {
				continue
			}
			if view.Delivery != "async" || agent.Spec.Provider != "codex" {
				return failure("question-decline-unsupported", nil)
			}
			result.Delivery = "async"
			if !supported || !capability.SupportsAsyncQuestionDeclines() {
				return failure("question-decline-unsupported", nil)
			}
			if view.State == agentquestion.StateWaiting {
				fresh, readErr := c.LoadRegistry()
				if readErr != nil {
					return result, readErr
				}
				current, found := fresh.Agent(agent.Metadata.UID)
				if !found {
					return failure("question-stale", nil)
				}
				if agent.Status.Phase != coremetadata.PhaseRunning || current.Status.Phase != coremetadata.PhaseRunning {
					return failure(questionReasonClosed, nil)
				}
				currentPane, present := fresh.Pane(current.Status.PaneRef)
				original, originalPresent := reg.Agent(agent.Metadata.UID)
				if !present || !originalPresent || original.Status.PaneRef != agent.Status.PaneRef || current.Status.PaneRef != agent.Status.PaneRef || current.Spec.Provider != agent.Spec.Provider || current.Metadata.OwnerUID() != original.Metadata.OwnerUID() || currentPane.Metadata.UID != pane.Metadata.UID || currentPane.Metadata.OwnerUID() != pane.Metadata.OwnerUID() || currentPane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
					return failure("question-stale", nil)
				}
				before, after := pane.Status.ProcessSession, currentPane.Status.ProcessSession
				if (before == nil) != (after == nil) {
					return failure("question-stale", nil)
				}
				if before != nil && (before.Binding != after.Binding || before.Provider != after.Provider || before.ThreadID != after.ThreadID || before.SessionID != after.SessionID || before.ConnectionID != after.ConnectionID) {
					return failure("question-stale", nil)
				}
				oldActivation, newActivation := pane.Status.Activation.Process, currentPane.Status.Activation.Process
				if (oldActivation == nil) != (newActivation == nil) {
					return failure("question-stale", nil)
				}
				if oldActivation != nil && *oldActivation != *newActivation {
					return failure("question-stale", nil)
				}
				for _, prompt := range view.Prompts {
					if prompt.IsSecret {
						return failure(questionReasonSecretNativeOnly, errors.New("secret question sets require the native surface"))
					}
				}
				if !view.CanDecline {
					return failure("question-decline-unsupported", nil)
				}
				if view.Deadline != nil && !time.Now().Before(*view.Deadline) {
					return failure(questionReasonExpired, agentquestion.ErrExpired)
				}
				receipt, deliveryErr := capability.DeclineExactProcessQuestionReceipt(ctx, agent.Metadata.UID, view.QuestionID)
				result.Outcome = receipt.Outcome
				if deliveryErr != nil {
					var typed *processquestions.DeliveryError
					if errors.As(deliveryErr, &typed) {
						if typed.Receipt.Code == "question-delivery-unknown" {
							result.Outcome = "unknown"
						}
						return failure(typed.Receipt.Code, deliveryErr)
					}
					code := "question-stale"
					if errors.Is(deliveryErr, processhost.ErrBusy) || errors.Is(deliveryErr, agentquestion.ErrBusy) {
						code = "question-busy"
					}
					if errors.Is(deliveryErr, processhost.ErrClosed) {
						code = questionReasonClosed
					}
					return failure(code, deliveryErr)
				}
				if receipt.QuestionID != view.QuestionID || receipt.Delivery != "async" || receipt.Outcome != "submitted" {
					result.Outcome = "unknown"
					return failure("question-delivery-unknown", nil)
				}
				return result, nil
			}
			if view.State == agentquestion.StateExpired {
				return failure(questionReasonExpired, agentquestion.ErrExpired)
			}
			// For terminal views the owned store distinguishes answer from notice.
			break
		}
	}
	st, err := c.OpenStore()
	if err != nil {
		return result, err
	}
	history, found, err := st.AsyncStore().Get(request.QuestionID)
	if err != nil {
		return result, err
	}
	if found && history.AgentUID == agent.Metadata.UID && history.Delivery == "async" {
		result.Delivery = "async"
		if history.State == agentquestion.StateExpired {
			return failure(questionReasonExpired, agentquestion.ErrExpired)
		}
		switch history.Disposition {
		case "async-declined":
			result.Outcome = "submitted"
			return failure(questionReasonNotPending, agentquestion.ErrNotPending)
		case "async-submitted":
			return failure(questionReasonNotPending, agentquestion.ErrNotPending)
		case "async-unknown":
			result.Outcome = "unknown"
			return failure(questionReasonNotPending, agentquestion.ErrNotPending)
		case "async-not-submitted":
			result.Outcome = "not-submitted"
			return failure(questionReasonNotPending, agentquestion.ErrNotPending)
		}
		if history.State != agentquestion.StateWaiting || agent.Status.Phase != coremetadata.PhaseRunning {
			return failure(questionReasonClosed, agentquestion.ErrClosed)
		}
		if !supported || !capability.SupportsAsyncQuestionDeclines() {
			return failure("question-decline-unsupported", nil)
		}
		return failure("question-stale", processhost.ErrStale)
	}
	legacy, found, err := st.Get(request.QuestionID)
	if err != nil {
		return result, err
	}
	if found && legacy.AgentUID == agent.Metadata.UID {
		return failure("question-decline-unsupported", nil)
	}
	return failure(questionReasonNotFound, agentquestion.ErrNotFound)
}

func (c Service) Answer(ctx context.Context, request Request, agent coremetadata.Agent) (AnswerResult, error) {
	if err := ctx.Err(); err != nil {
		return AnswerResult{}, err
	}
	refuse := func(reason, detail string) error { return Refuse(request.Spelling, agent, reason, detail) }
	if !agentquestion.ValidID(request.QuestionID) {
		return AnswerResult{}, refuse(questionReasonNotFound, fmt.Sprintf("has no question %q", request.QuestionID))
	}
	if handled, cliResult, cliErr := c.resolveCLI(ctx, request, agent, false); handled {
		return cliResult, cliErr
	}
	reg, err := c.LoadRegistry()
	if err != nil {
		return AnswerResult{}, err
	}
	if pane, ok := reg.Pane(agent.Status.PaneRef); ok && pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
		dispatcher := c.Dispatcher
		views, err := dispatcher.ReadExactProcessQuestions(ctx, agent.Metadata.UID)
		if err != nil {
			return AnswerResult{}, err
		}
		for _, view := range views {
			if view.QuestionID != request.QuestionID {
				continue
			}
			if view.Delivery == "async" {
				result := AnswerResult{QuestionID: view.QuestionID, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Delivery: "async"}
				// Refuse without the generic no-effects assertion: an earlier attempt
				// may already have reached the provider, even when this one is rejected.
				failure := func(code string, cause error) (AnswerResult, error) {
					message := code
					if cause != nil {
						message += ": " + cause.Error()
					}
					return result, &Failure{Code: code, Message: message, Cause: cause}
				}
				capability, supported := dispatcher.(processquestions.ReceiptDispatcher)
				if !supported || !capability.SupportsAsyncQuestionAnswers() {
					return failure("question-host-unsupported", nil)
				}
				if view.State == agentquestion.StateWaiting {
					// A pluggable receipt dispatcher is still subject to the service's
					// current Agent/lifetime fence. Original-turn completion is not a fence.
					fresh, readErr := c.LoadRegistry()
					if readErr != nil {
						return result, readErr
					}
					current, found := fresh.Agent(agent.Metadata.UID)
					if !found {
						return failure("question-stale", nil)
					}
					if agent.Status.Phase != coremetadata.PhaseRunning || current.Status.Phase != coremetadata.PhaseRunning {
						return failure(questionReasonClosed, nil)
					}
					currentPane, present := fresh.Pane(current.Status.PaneRef)
					original, originalPresent := reg.Agent(agent.Metadata.UID)
					if !present || !originalPresent || original.Status.PaneRef != agent.Status.PaneRef || current.Status.PaneRef != agent.Status.PaneRef || current.Spec.Provider != agent.Spec.Provider || current.Metadata.OwnerUID() != original.Metadata.OwnerUID() || currentPane.Metadata.UID != pane.Metadata.UID || currentPane.Metadata.OwnerUID() != pane.Metadata.OwnerUID() || currentPane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
						return failure("question-stale", nil)
					}
					before, after := pane.Status.ProcessSession, currentPane.Status.ProcessSession
					if (before == nil) != (after == nil) {
						return failure("question-stale", nil)
					}
					if before != nil && (before.Binding != after.Binding || before.Provider != after.Provider || before.ThreadID != after.ThreadID || before.SessionID != after.SessionID || before.ConnectionID != after.ConnectionID) {
						return failure("question-stale", nil)
					}
					oldActivation, newActivation := pane.Status.Activation.Process, currentPane.Status.Activation.Process
					if (oldActivation == nil) != (newActivation == nil) {
						return failure("question-stale", nil)
					}
					if oldActivation != nil && *oldActivation != *newActivation {
						return failure("question-stale", nil)
					}
				}
				if view.State != agentquestion.StateWaiting {
					reason, _ := StateRefusal(view.State)
					if view.State == agentquestion.StateClosed {
						reason = questionReasonClosed
						st, openErr := c.OpenStore()
						if openErr != nil {
							return result, openErr
						}
						stored, found, readErr := st.AsyncStore().Get(view.QuestionID)
						if readErr != nil {
							return result, readErr
						}
						if found && stored.AgentUID == agent.Metadata.UID {
							switch stored.Disposition {
							case "async-declined":
								reason = questionReasonNotPending
							case "async-submitted":
								reason, result.Outcome = questionReasonNotPending, "submitted"
							case "async-unknown":
								reason, result.Outcome = questionReasonNotPending, "unknown"
							case "async-not-submitted":
								reason, result.Outcome = questionReasonNotPending, "not-submitted"
							}
						}
					}
					return failure(reason, nil)
				}
				if !view.CanAnswer {
					return failure("question-host-unsupported", nil)
				}
				for _, prompt := range view.Prompts {
					if prompt.IsSecret {
						return failure(questionReasonSecretNativeOnly, errors.New("secret question sets require the native surface"))
					}
				}
				selections, selectionErr := Selections(request, view.Prompts)
				if selectionErr != nil {
					return failure(questionReasonInvalidAnswer, selectionErr)
				}
				if code, validateErr := agentquestion.ValidateAsyncSelections(view.Prompts, selections); validateErr != nil {
					return failure(code, validateErr)
				}
				receipt, deliveryErr := capability.AnswerExactProcessQuestionReceipt(ctx, agent.Metadata.UID, view.QuestionID, selections)
				result.Outcome = receipt.Outcome
				if deliveryErr != nil {
					var typed *processquestions.DeliveryError
					if errors.As(deliveryErr, &typed) {
						if typed.Receipt.Code == "question-delivery-unknown" {
							result.Outcome = "unknown"
						}
						return failure(typed.Receipt.Code, deliveryErr)
					}
					code := "question-stale"
					if errors.Is(deliveryErr, processhost.ErrBusy) || errors.Is(deliveryErr, agentquestion.ErrBusy) {
						code = "question-busy"
					}
					if errors.Is(deliveryErr, processhost.ErrClosed) {
						code = questionReasonClosed
					}
					return failure(code, deliveryErr)
				}
				if receipt.QuestionID != view.QuestionID || receipt.Delivery != "async" || receipt.Outcome != "submitted" {
					result.Outcome = "unknown"
					return failure("question-delivery-unknown", nil)
				}
				return result, nil
			}
			if !view.CanAnswer {
				return AnswerResult{}, refuse(questionReasonNotPending, "question is no longer waiting")
			}
			for _, q := range view.Prompts {
				if q.IsSecret {
					return AnswerResult{}, refuse(questionReasonSecretNativeOnly, "secret answers require masked typed delivery")
				}
			}
			selections, err := Selections(request, view.Prompts)
			if err != nil {
				failure := Refuse(request.Spelling, agent, questionReasonInvalidAnswer, err.Error()).(*Failure)
				failure.Cause = err
				return AnswerResult{}, failure
			}
			if err = dispatcher.AnswerExactProcessQuestion(ctx, agent.Metadata.UID, view.QuestionID, selections); err != nil {
				return AnswerResult{}, err
			}
			return AnswerResult{QuestionID: view.QuestionID, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name}, nil
		}
		// A missing live view is not a missing opaque key when this Agent has
		// retained async history. This is read-only and never dispatches input.
		if c.OpenStore != nil {
			st, openErr := c.OpenStore()
			if openErr != nil {
				return AnswerResult{}, openErr
			}
			history, found, readErr := st.AsyncStore().Get(request.QuestionID)
			if readErr != nil {
				return AnswerResult{}, readErr
			}
			if found && history.AgentUID == agent.Metadata.UID && history.Delivery == "async" {
				result := AnswerResult{QuestionID: history.ID, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Delivery: "async"}
				code := "question-stale"
				switch history.State {
				case agentquestion.StateExpired:
					code = questionReasonExpired
				case agentquestion.StateClosed:
					code = questionReasonClosed
				}
				switch history.Disposition {
				case "async-submitted":
					result.Outcome = "submitted"
				case "async-unknown":
					result.Outcome = "unknown"
				case "async-not-submitted":
					result.Outcome = "not-submitted"
				}
				if result.Outcome != "" || history.Disposition == "async-declined" {
					code = questionReasonNotPending
				}
				return result, &Failure{Code: code, Message: code + ": async form is unavailable; do not resend"}
			}
		}
		return AnswerResult{}, refuse(questionReasonNotFound, "has no current exact question")
	}
	store, err := c.OpenStore()
	if err != nil {
		return AnswerResult{}, fmt.Errorf("%s: %w", request.Spelling, err)
	}
	record, found, err := store.Get(request.QuestionID)
	if err != nil {
		return AnswerResult{}, fmt.Errorf("%s: %w", request.Spelling, err)
	}
	asyncRecord, asyncFound, err := store.AsyncStore().Get(request.QuestionID)
	if err != nil {
		return AnswerResult{}, fmt.Errorf("%s: %w", request.Spelling, err)
	}
	if asyncFound {
		record, found = asyncRecord, true
	}
	if !found || record.AgentUID != agent.Metadata.UID {
		return AnswerResult{}, refuse(questionReasonNotFound, fmt.Sprintf("has no question %q", request.QuestionID))
	}
	if record.Delivery == "async" && record.State == agentquestion.StateClosed {
		outcome := ""
		switch record.Disposition {
		case "async-submitted":
			outcome = "submitted"
		case "async-unknown":
			outcome = "unknown"
		case "async-not-submitted":
			outcome = "not-submitted"
		}
		if outcome != "" || record.Disposition == "async-declined" {
			return AnswerResult{QuestionID: record.ID, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, Delivery: "async", Outcome: outcome}, &Failure{Code: questionReasonNotPending, Message: "question-not-pending: async form is locked; do not resend"}
		}
	}
	if record.Delivery == "async" && record.State == agentquestion.StateWaiting {
		return AnswerResult{}, refuse("question-host-unsupported", "async question requires a current receipt-capable host")
	}
	if reason, detail := RecordRefusal(record); reason != "" {
		return AnswerResult{}, refuse(reason, fmt.Sprintf("question %s %s", record.ID, detail))
	}
	questions, err := record.ParsedQuestions()
	if err != nil {
		return AnswerResult{}, fmt.Errorf("%s: %w", request.Spelling, err)
	}
	if record.Provider == "codex" {
		for _, question := range questions {
			if question.IsSecret {
				return AnswerResult{}, refuse(questionReasonSecretNativeOnly, "has a secret answer; answer it in the Codex window instead of passing it on the command line")
			}
		}
	}
	selections, err := Selections(request, questions)
	if err != nil {
		failure := Refuse(request.Spelling, agent, questionReasonInvalidAnswer, err.Error()).(*Failure)
		failure.Cause = err
		return AnswerResult{}, failure
	}
	var answers map[string]string
	if record.Provider == "codex" {
		answers, err = agentquestion.BuildCodexAnswers(questions, selections)
	} else {
		answers, err = agentquestion.BuildAnswers(questions, selections)
	}
	if err != nil {
		return AnswerResult{}, refuse(questionReasonInvalidAnswer, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return AnswerResult{}, err
	}
	answered, err := store.Answer(record.ID, agent.Metadata.UID, answers)
	if errors.Is(err, agentquestion.ErrClosed) {
		// The record closed after the read above; describe it as it closed.
		if current, found, getErr := store.Get(record.ID); getErr == nil && found {
			if reason, detail := RecordRefusal(current); reason != "" {
				return AnswerResult{}, refuse(reason, fmt.Sprintf("question %s %s", record.ID, detail))
			}
		}
	}
	if err != nil {
		if reason, detail := StoreRefusal(err); reason != "" {
			return AnswerResult{}, refuse(reason, fmt.Sprintf("question %s %s", record.ID, detail))
		}
		return AnswerResult{}, fmt.Errorf("%s: %w", request.Spelling, err)
	}
	return AnswerResult{QuestionID: answered.ID, AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name}, nil
}

func RecordRefusal(record agentquestion.Record) (string, string) {
	if record.State == agentquestion.StateClosed && record.Disposition == "answered-elsewhere" {
		return questionReasonAnsweredElsewhere, "was already answered in Codex's own input surface"
	}
	reason, detail := StateRefusal(record.State)
	if asking := ClosedAsking(record); reason == questionReasonClosed && asking != "" {
		detail = fmt.Sprintf("%s (%s); %s", detail, record.Disposition, asking)
	}
	return reason, detail
}

// ClosedAsking says whether the provider still asks a closed record's
// question in its own prompt. It is empty for any other record, and for a
// closed one without a reason this release knows.
func ClosedAsking(record agentquestion.Record) string {
	if record.State != agentquestion.StateClosed {
		return ""
	}
	asks, known := agentquestion.ProviderStillAsks(record.Disposition)
	if !known {
		return ""
	}
	provider, surface := "Claude Code", "its own prompt"
	if record.Provider == "codex" {
		provider, surface = "Codex", "its own input surface"
	}
	if asks {
		return provider + " still asks it in " + surface
	}
	return provider + " no longer asks it"
}

// StateRefusal is the refusal for a record that cannot take an answer.
func StateRefusal(state agentquestion.State) (string, string) {
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

// StoreRefusal maps a store refusal onto its reason token.
func StoreRefusal(err error) (string, string) {
	switch {
	case errors.Is(err, agentquestion.ErrNotFound):
		return questionReasonNotFound, "does not exist"
	case errors.Is(err, agentquestion.ErrNotPending):
		return StateRefusal(agentquestion.StateAnswered)
	case errors.Is(err, agentquestion.ErrExpired):
		return StateRefusal(agentquestion.StateExpired)
	case errors.Is(err, agentquestion.ErrClosed):
		return StateRefusal(agentquestion.StateClosed)
	case errors.Is(err, agentquestion.ErrAnsweredElsewhere):
		return questionReasonAnsweredElsewhere, "was already answered in Codex's own input surface"
	case errors.Is(err, agentquestion.ErrInvalidAnswer):
		return questionReasonInvalidAnswer, err.Error()
	default:
		return "", ""
	}
}

// SelectionError preserves CLI syntax while naming typed selection fields for SDK callers.
type SelectionError struct{ Message, InputMessage string }

func (e *SelectionError) Error() string { return e.Message }

// agentQuestionSelections folds the --option, --index, and --text occurrences
// into one Selection per question. Each occurrence is <question-number>=<value>
// with a 1-based question number.
func Selections(request Request, questions []agentquestion.Question) (map[int]agentquestion.Selection, error) {
	selections := map[int]agentquestion.Selection{}
	split := func(flagName, raw string) (int, string, error) {
		number, value, ok := strings.Cut(raw, "=")
		index, err := strconv.Atoi(strings.TrimSpace(number))
		if !ok || err != nil || index < 1 {
			if flagName == "text" {
				return 0, "", &SelectionError{Message: "--text is not <question-number>=<value>", InputMessage: "Text requires a positive Question number"}
			}
			return 0, "", &SelectionError{Message: fmt.Sprintf("--%s %q is not <question-number>=<value>", flagName, raw), InputMessage: "Option or Index requires a positive Question number"}
		}
		if index > len(questions) {
			if flagName == "text" {
				return 0, "", &SelectionError{Message: fmt.Sprintf("--text names question %d; this question set has %d", index, len(questions)), InputMessage: fmt.Sprintf("Text names Question %d; this question set has %d", index, len(questions))}
			}
			return 0, "", &SelectionError{Message: fmt.Sprintf("--%s %q names question %d; this question set has %d", flagName, raw, index, len(questions)), InputMessage: fmt.Sprintf("Option or Index names Question %d; this question set has %d", index, len(questions))}
		}
		return index - 1, value, nil
	}
	for _, raw := range request.Options {
		index, label, err := split("option", raw)
		if err != nil {
			return nil, err
		}
		selection := selections[index]
		selection.Labels = append(selection.Labels, label)
		selections[index] = selection
	}
	for _, raw := range request.Indexes {
		index, value, err := split("index", raw)
		if err != nil {
			return nil, err
		}
		position, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || position < 1 || position > len(questions[index].Options) {
			return nil, &SelectionError{Message: fmt.Sprintf("--index %q: question %d has options 1-%d", raw, index+1, len(questions[index].Options)), InputMessage: fmt.Sprintf("Index for Question %d must be between 1 and %d", index+1, len(questions[index].Options))}
		}
		selection := selections[index]
		selection.Labels = append(selection.Labels, questions[index].Options[position-1].Label)
		selections[index] = selection
	}
	for _, raw := range request.Texts {
		index, text, err := split("text", raw)
		if err != nil {
			return nil, err
		}
		selection := selections[index]
		if selection.HasText {
			return nil, &SelectionError{Message: fmt.Sprintf("question %d takes one --text", index+1), InputMessage: fmt.Sprintf("Question %d accepts one Text selection", index+1)}
		}
		selection.Text, selection.HasText = text, true
		selections[index] = selection
	}
	return selections, nil
}

func ResolveAgent(reg coremetadata.Registry, raw string) (coremetadata.Agent, error) {
	ref, err := selector.ParseRef(coremetadata.KindAgent, raw)
	if err != nil {
		return coremetadata.Agent{}, resources.Classify(err)
	}
	return SelectAgent(reg, selector.Query{Agents: []selector.Ref{ref}}, "agent question")
}

// SelectAgent shares exact selection and cardinality validation with the CLI.
func SelectAgent(reg coremetadata.Registry, query selector.Query, spelling string) (coremetadata.Agent, error) {
	result, err := resources.Resolve(reg, coremetadata.RuntimeObservation{}, nil, coremetadata.KindAgent, query)
	if err != nil {
		return coremetadata.Agent{}, resources.Classify(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbTopic, Kind: coremetadata.KindAgent}, query, result); err != nil {
		return coremetadata.Agent{}, err
	}
	agent, ok := reg.Agent(result.Matches[0].UID)
	if !ok {
		return coremetadata.Agent{}, fmt.Errorf("%s: resolved uid %q is no longer in the registry", spelling, result.Matches[0].UID)
	}
	return agent.Clone(), nil
}
