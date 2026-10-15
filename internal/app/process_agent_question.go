package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
)

type ExactProcessQuestion = processquestions.ExactQuestion
type ProcessQuestionDispatcher struct{ RegistryPath string }

func ReadExactProcessQuestions(ctx context.Context, uid string) ([]ExactProcessQuestion, error) {
	return processquestions.ReadExactProcessQuestions(ctx, uid)
}
func AnswerExactProcessQuestion(ctx context.Context, uid, id string, selections map[int]agentquestion.Selection) error {
	return processquestions.AnswerExactProcessQuestion(ctx, uid, id, selections)
}
func (d ProcessQuestionDispatcher) ReadExactProcessQuestions(ctx context.Context, uid string) ([]ExactProcessQuestion, error) {
	return (processquestions.Dispatcher{RegistryPath: d.RegistryPath}).ReadExactProcessQuestions(ctx, uid)
}
func (d ProcessQuestionDispatcher) AnswerExactProcessQuestion(ctx context.Context, uid, id string, selections map[int]agentquestion.Selection) error {
	return (processquestions.Dispatcher{RegistryPath: d.RegistryPath}).AnswerExactProcessQuestion(ctx, uid, id, selections)
}

// Callbacks are local to the owning host and keyed by the complete immutable
// binding. They carry no frontend authority and are removed with its socket.
var processQuestionCallbacks sync.Map // processhost.Binding -> func(context.Context, processQuestionRequest) ([]ExactProcessQuestion,error)
type processQuestionCallback func(context.Context, processQuestionRequest) ([]ExactProcessQuestion, error)

func captureNativeQuestion(native *map[string]bool, id string, resolver func() config.AgentQuestionAnswering) bool {
	if resolver == nil || resolver() == config.AgentQuestionAnsweringProjmux {
		return false
	}
	if *native == nil {
		*native = map[string]bool{}
	}
	(*native)[id] = true
	return true
}
func processQuestionPrompts(provider string, request processhost.Request) ([]agentquestion.Question, error) {
	var input struct {
		Questions json.RawMessage `json:"questions"`
	}
	raw := request.Input
	if provider == aiModeCodex {
		var n codexappserver.Notification
		if json.Unmarshal(raw, &n) != nil {
			return nil, agentquestion.ErrInvalidQuestions
		}
		raw = n.Params
	}
	if json.Unmarshal(raw, &input) != nil {
		return nil, agentquestion.ErrInvalidQuestions
	}
	if provider == aiModeCodex {
		return agentquestion.ParseCodexQuestions(input.Questions)
	}
	return agentquestion.ParseQuestions(input.Questions)
}
func exactQuestionViews(provider string, records map[string]processhost.Request, native map[string]bool, store *agentquestion.Store, snapshots ...processhost.Snapshot) ([]ExactProcessQuestion, error) {
	views := []ExactProcessQuestion{}
	for id, r := range records {
		if provider == aiModeCodex && r.Kind == "async-question" {
			var snap *processhost.Snapshot
			if len(snapshots) == 1 {
				snap = &snapshots[0]
			} else if len(snapshots) > 1 {
				return nil, processhost.ErrStale
			}
			view, err := asyncQuestionView(id, r, store, snap)
			if err != nil {
				return nil, err
			}
			if view.QuestionID == "" {
				delete(records, id)
				continue
			}
			views = append(views, view)
			continue
		}
		if r.Kind != "question" {
			continue
		}
		prompts, err := processQuestionPrompts(provider, r)
		if err != nil {
			return nil, err
		}
		view := ExactProcessQuestion{QuestionID: id, Policy: "native", Delivery: "process-exact-question", CanAnswer: true, State: agentquestion.StateWaiting, Prompts: prompts}
		if !native[id] {
			record, found, err := store.Get(id)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, agentquestion.ErrNotFound
			}
			view.Policy = "projmux"
			view.Delivery = "held-question"
			view.State = record.State
			view.CanAnswer = record.State == agentquestion.StateWaiting
			deadline := record.Deadline
			view.Deadline = &deadline
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].QuestionID < views[j].QuestionID })
	return views, nil
}
func exactQuestionRequest(provider string, r processQuestionRequest, records map[string]processhost.Request, native map[string]bool, store *agentquestion.Store) (processhost.Request, []agentquestion.Question, agentquestion.Record, error) {
	request, ok := records[r.QuestionID]
	var held agentquestion.Record
	if !ok || request.Kind != "question" || len(r.Selections) == 0 {
		return request, nil, held, processhost.ErrStale
	}
	prompts, err := processQuestionPrompts(provider, request)
	if err != nil {
		return request, nil, held, err
	}
	if !native[r.QuestionID] {
		record, found, err := store.Get(r.QuestionID)
		if err != nil {
			return request, nil, held, err
		}
		if !found || record.State != agentquestion.StateWaiting {
			return request, nil, held, processhost.ErrStale
		}
		stored, err := record.ParsedQuestions()
		if err != nil || !reflect.DeepEqual(stored, prompts) {
			return request, nil, held, processhost.ErrStale
		}
		held = record
	}
	return request, prompts, held, nil
}
func admitHeldDirect(store *agentquestion.Store, provider string, b processhost.Binding, request processhost.Request, held agentquestion.Record, native bool) (agentquestion.Record, error) {
	if native {
		return agentquestion.Record{}, nil
	}
	if provider == aiModeClaude {
		provider = ""
	}
	if held.AgentUID != b.Agent || held.PaneUID != b.Pane || held.SessionID != request.Session || held.Generation != b.Generation || held.RequestID != request.ID || held.Provider != provider || held.ID != processControlID(b, request) {
		return agentquestion.Record{}, processhost.ErrStale
	}
	return store.BeginDirect(held)
}

func (c *claudeProcessControl) exactQuestions(ctx context.Context, r processQuestionRequest) ([]ExactProcessQuestion, error) {
	if r.Binding != c.binding {
		return nil, processhost.ErrStale
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snap, err := c.handle.Observe(c.binding)
	if err != nil {
		return nil, err
	}
	authority := processhost.Authority{Binding: c.binding, Connection: snap.Connection, Session: snap.Session}
	if err = c.handle.ValidateAuthority(ctx, authority); err != nil {
		return nil, err
	}
	if err = captureExactQuestions(aiModeClaude, c.binding, snap, &c.records, &c.nativeQuestions, c.questionAnswering, c.questions, c.questionWindow, c.now); err != nil {
		return nil, err
	}
	if r.QuestionID == "" {
		return exactQuestionViews(aiModeClaude, c.records, c.nativeQuestions, c.questions)
	}
	request, prompts, held, err := exactQuestionRequest(aiModeClaude, r, c.records, c.nativeQuestions, c.questions)
	if err != nil {
		return nil, err
	}
	answers, err := agentquestion.BuildAnswers(prompts, r.Selections)
	if err != nil {
		return nil, agentquestion.ErrInvalidAnswer
	}
	receipt, err := admitHeldDirect(c.questions, aiModeClaude, c.binding, request, held, c.nativeQuestions[r.QuestionID])
	if err != nil {
		return nil, err
	}
	err = c.handle.Respond(ctx, authority, request, processhost.Response{Answers: answers})
	if !c.nativeQuestions[r.QuestionID] {
		err = errors.Join(err, c.questions.FinishDirect(receipt, err == nil))
	}
	if err != nil {
		return nil, err
	}
	delete(c.records, r.QuestionID)
	delete(c.nativeQuestions, r.QuestionID)
	return nil, err
}
func (c *codexProcessControl) exactQuestions(ctx context.Context, r processQuestionRequest) ([]ExactProcessQuestion, error) {
	if c.endpoint == nil || r.Binding != c.endpoint.binding {
		return nil, processhost.ErrStale
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.endpoint.route(ctx); err != nil {
		return nil, err
	}
	snap, err := c.endpoint.handle.Observe(c.endpoint.binding)
	if err != nil {
		return nil, err
	}
	if err = captureExactQuestions(aiModeCodex, c.endpoint.binding, snap, &c.records, &c.nativeQuestions, c.questionAnswering, c.questions, c.questionWindow, c.now); err != nil {
		return nil, err
	}
	if r.QuestionID == "" {
		return exactQuestionViews(aiModeCodex, c.records, c.nativeQuestions, c.questions, snap)
	}
	request, prompts, held, err := exactQuestionRequest(aiModeCodex, r, c.records, c.nativeQuestions, c.questions)
	if err != nil {
		return nil, err
	}
	if _, err = agentquestion.BuildCodexAnswers(prompts, r.Selections); err != nil {
		return nil, agentquestion.ErrInvalidAnswer
	}
	receipt, err := admitHeldDirect(c.questions, aiModeCodex, c.endpoint.binding, request, held, c.nativeQuestions[r.QuestionID])
	if err != nil {
		return nil, err
	}
	err = c.endpoint.handle.RespondQuestion(ctx, c.endpoint.authority(), request, r.Selections)
	if !c.nativeQuestions[r.QuestionID] {
		err = errors.Join(err, c.questions.FinishDirect(receipt, err == nil))
	}
	if err != nil {
		return nil, err
	}
	delete(c.records, r.QuestionID)
	delete(c.nativeQuestions, r.QuestionID)
	return nil, err
}
func applyExactProcessQuestions(ctx context.Context, peer coremetadata.ProcessIdentity, r processQuestionRequest, current func(context.Context, processhost.Binding) error) processForegroundResult {
	result := processForegroundResult{Stale: true}
	if (r.QuestionID == "" && len(r.Selections) != 0) || (r.QuestionID != "" && len(r.Selections) == 0) {
		return result
	}
	callback, ok := processQuestionCallbacks.Load(r.Binding)
	if !ok {
		return result
	}
	var views []ExactProcessQuestion
	invalidAnswer := false
	result = controlProcessForeground(ctx, peer, processForegroundRequest{Authority: processhost.Authority{Binding: r.Binding}}, func(ctx context.Context, a processhost.Authority) error { return current(ctx, a.Binding) }, func() error {
		var err error
		views, err = callback.(processQuestionCallback)(ctx, r)
		invalidAnswer = errors.Is(err, agentquestion.ErrInvalidAnswer)
		return err
	})
	// Return only redacted question metadata, never selections or provider response.
	result.InvalidAnswer = invalidAnswer
	result.Questions = views
	return result
}

// Query admission is question-only: it never consumes an approval or message.
func captureExactQuestions(provider string, b processhost.Binding, snap processhost.Snapshot, records *map[string]processhost.Request, native *map[string]bool, resolver func() config.AgentQuestionAnswering, store *agentquestion.Store, window time.Duration, now func() time.Time) error {
	if provider == aiModeCodex {
		if err := captureAsyncQuestions(b, snap, records, store, window); err != nil {
			return err
		}
	}
	if snap.State != "ready" || now == nil || store == nil {
		return processhost.ErrStale
	}
	if *records == nil {
		*records = map[string]processhost.Request{}
	}
	pending := map[string]bool{}
	for _, r := range snap.Pending {
		if r.Kind != "question" {
			continue
		}
		id := processControlID(b, r)
		pending[id] = true
		if _, known := (*records)[id]; known {
			continue
		}
		prompts, err := processQuestionPrompts(provider, r)
		if err != nil {
			return err
		}
		if !captureNativeQuestion(native, id, resolver) {
			questions, _ := json.Marshal(prompts)
			created := now().UTC()
			recordProvider := ""
			if provider == aiModeCodex {
				recordProvider = provider
			}
			if _, err = store.Create(agentquestion.Record{ID: id, Provider: recordProvider, AgentUID: b.Agent, PaneUID: b.Pane, SessionID: r.Session, Generation: b.Generation, RequestID: r.ID, Questions: questions, CreatedAt: created, Deadline: created.Add(window)}); err != nil {
				return err
			}
		}
		(*records)[id] = r
	}
	for id, r := range *records {
		if r.Kind == "question" && !pending[id] {
			if !(*native)[id] {
				reason := agentquestion.CloseReasonHookCanceled
				if provider == aiModeCodex {
					reason = agentquestion.CloseReasonTurnEnded
				}
				_, _ = store.Close(id, reason)
			}
			delete(*records, id)
			delete(*native, id)
		}
	}
	return nil
}

// Conversation identity is checked on the reader and again against the host's
// live snapshot. A Registry observation cannot retarget a provider conversation.
func exactQuestionSessionMatches(reg coremetadata.Registry, b processhost.Binding, provider, session, connection string) bool {
	return processquestions.SessionMatches(reg, b, provider, session, connection)
}
func currentExactQuestionSession(path string, b processhost.Binding, snap processhost.Snapshot) error {
	reg, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
	if err != nil {
		return err
	}
	if !exactQuestionSessionMatches(reg, b, snap.Provider, snap.Session, snap.Connection) {
		return processhost.ErrStale
	}
	return nil
}
