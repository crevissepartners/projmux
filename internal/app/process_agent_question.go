package app

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ExactProcessQuestion contains only question display data, never a writable
// provider token. Policy is captured at admission; Delivery names its transport.
type ExactProcessQuestion struct {
	QuestionID string                   `json:"questionID"`
	Policy     string                   `json:"policy"`
	Delivery   string                   `json:"delivery"`
	CanAnswer  bool                     `json:"canAnswer"`
	Prompts    []agentquestion.Question `json:"prompts"`
	State      agentquestion.State      `json:"state"`
	Deadline   *time.Time               `json:"deadline,omitempty"`
}

// ProcessQuestionDispatcher uses the same Registry and host sockets as commands.
// An empty path resolves production paths; fixtures supply their isolated path.
type ProcessQuestionDispatcher struct{ RegistryPath string }

func ReadExactProcessQuestions(ctx context.Context, agentUID string) ([]ExactProcessQuestion, error) {
	result, err := (ProcessQuestionDispatcher{}).call(ctx, agentUID, "", nil)
	return result.Questions, err
}
func AnswerExactProcessQuestion(ctx context.Context, agentUID, questionID string, selections map[int]agentquestion.Selection) error {
	if questionID == "" || len(selections) == 0 {
		return agentquestion.ErrInvalidAnswer
	}
	_, err := (ProcessQuestionDispatcher{}).call(ctx, agentUID, questionID, selections)
	return err
}
func (d ProcessQuestionDispatcher) ReadExactProcessQuestions(ctx context.Context, uid string) ([]ExactProcessQuestion, error) {
	if d.RegistryPath == "" {
		return ReadExactProcessQuestions(ctx, uid)
	}
	result, err := d.call(ctx, uid, "", nil)
	return result.Questions, err
}
func (d ProcessQuestionDispatcher) AnswerExactProcessQuestion(ctx context.Context, uid, id string, selections map[int]agentquestion.Selection) error {
	if id == "" || len(selections) == 0 {
		return agentquestion.ErrInvalidAnswer
	}
	if d.RegistryPath == "" {
		return AnswerExactProcessQuestion(ctx, uid, id, selections)
	}
	_, err := d.call(ctx, uid, id, selections)
	return err
}
func (d ProcessQuestionDispatcher) call(ctx context.Context, uid, id string, selections map[int]agentquestion.Selection) (processForegroundResult, error) {
	path := d.RegistryPath
	if path == "" {
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			return processForegroundResult{}, err
		}
		path = intmetadata.PathFor(paths.StateDir)
	}
	reg, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
	if err != nil {
		return processForegroundResult{}, err
	}
	agent, ok := reg.Agent(uid)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning {
		return processForegroundResult{}, processhost.ErrStale
	}
	pane, ok := reg.Pane(agent.Status.PaneRef)
	if !ok || pane.Status.Activation.Process == nil {
		return processForegroundResult{}, processhost.ErrStale
	}
	activation := *pane.Status.Activation.Process
	binding := processSchemaBinding(activation.Binding)
	view := processHostObservation{Binding: binding, Provider: agent.Spec.Provider, Host: activation.HostProcess, Child: activation.Child, State: "ready"}
	record := pane.Status.ProcessSession
	session := ""
	connection := ""
	if record != nil {
		session = record.SessionID
		if agent.Spec.Provider == aiModeCodex {
			session = record.ThreadID
		}
		connection = record.ConnectionID
	}
	if !processObservationMatches(reg, binding, view) || !exactQuestionSessionMatches(reg, binding, agent.Spec.Provider, session, connection) {
		return processForegroundResult{}, processhost.ErrStale
	}
	socket := processHostSocket(agent.Spec.Provider, path, binding.Pane, binding.Generation)
	inode, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return processForegroundResult{}, err
	}
	request := &processQuestionRequest{Binding: binding, QuestionID: id, Selections: selections}
	var envelope any = claudeProcessCheck{Questions: request}
	if agent.Spec.Provider == aiModeCodex {
		envelope = codexProcessExchange{Questions: request}
	} else if agent.Spec.Provider != aiModeClaude {
		return processForegroundResult{}, processhost.ErrStale
	}
	result, err := callProcessForeground(ctx, socket, inode, activation.HostProcess, envelope)
	if err != nil {
		return processForegroundResult{}, err
	} // uncertain writes are never retried
	current, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
	next, exists := current.Pane(binding.Pane)
	afterInode, inodeErr := localipc.InspectOwnedSocket(socket)
	if err != nil || !exists || next.Status.Activation.Process == nil || *next.Status.Activation.Process != activation || !processObservationMatches(current, binding, view) || !exactQuestionSessionMatches(current, binding, agent.Spec.Provider, session, connection) || inodeErr != nil || afterInode != inode {
		return processForegroundResult{}, processhost.ErrStale
	}
	if !result.Accepted {
		if result.InvalidAnswer {
			return processForegroundResult{}, agentquestion.ErrInvalidAnswer
		}
		return processForegroundResult{}, processhost.ErrStale
	}
	return result, nil
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
func exactQuestionViews(provider string, records map[string]processhost.Request, native map[string]bool, store *agentquestion.Store) ([]ExactProcessQuestion, error) {
	views := []ExactProcessQuestion{}
	for id, r := range records {
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
func exactQuestionRequest(provider string, r processQuestionRequest, records map[string]processhost.Request, native map[string]bool, store *agentquestion.Store) (processhost.Request, []agentquestion.Question, error) {
	request, ok := records[r.QuestionID]
	if !ok || request.Kind != "question" || len(r.Selections) == 0 {
		return processhost.Request{}, nil, processhost.ErrStale
	}
	if !native[r.QuestionID] {
		record, found, err := store.Get(r.QuestionID)
		if err != nil {
			return request, nil, err
		}
		if !found || record.State != agentquestion.StateWaiting {
			return request, nil, processhost.ErrStale
		}
	}
	prompts, err := processQuestionPrompts(provider, request)
	return request, prompts, err
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
	request, prompts, err := exactQuestionRequest(aiModeClaude, r, c.records, c.nativeQuestions, c.questions)
	if err != nil {
		return nil, err
	}
	answers, err := agentquestion.BuildAnswers(prompts, r.Selections)
	if err != nil {
		return nil, agentquestion.ErrInvalidAnswer
	}
	err = c.handle.Respond(ctx, authority, request, processhost.Response{Answers: answers})
	if err != nil {
		return nil, err
	}
	if !c.nativeQuestions[r.QuestionID] {
		_, err = c.questions.Close(r.QuestionID, agentquestion.CloseReasonAnsweredDirect)
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
		return exactQuestionViews(aiModeCodex, c.records, c.nativeQuestions, c.questions)
	}
	request, prompts, err := exactQuestionRequest(aiModeCodex, r, c.records, c.nativeQuestions, c.questions)
	if err != nil {
		return nil, err
	}
	if _, err = agentquestion.BuildCodexAnswers(prompts, r.Selections); err != nil {
		return nil, agentquestion.ErrInvalidAnswer
	}
	if err = c.endpoint.handle.RespondQuestion(ctx, c.endpoint.authority(), request, r.Selections); err != nil {
		return nil, err
	}
	if !c.nativeQuestions[r.QuestionID] {
		_, err = c.questions.Close(r.QuestionID, agentquestion.CloseReasonAnsweredDirect)
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
	a, ok := reg.Agent(b.Agent)
	p, found := reg.Pane(b.Pane)
	if !ok || !found || a.Status.Phase != coremetadata.PhaseRunning || a.Status.SessionRef == nil || a.Status.SessionRef.Provider != provider || p.Status.ProcessSession == nil || session == "" || connection == "" {
		return false
	}
	record := p.Status.ProcessSession
	if record.Binding != metadataProcessBinding(b) || record.Provider != provider || record.ConnectionID != connection {
		return false
	}
	if provider == aiModeClaude {
		return a.Status.SessionRef.Claude != nil && a.Status.SessionRef.Claude.SessionID == session && record.SessionID == session
	}
	return provider == aiModeCodex && a.Status.SessionRef.Codex != nil && a.Status.SessionRef.Codex.ThreadID == session && record.ThreadID == session
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
