package app

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// codexQuestionResponder is the existing broker epoch's generic single-use
// server-request answer path. Question content never enters lifecycle logs.
type codexQuestionResponder interface {
	RespondServerRequest(context.Context, json.RawMessage, any) error
}

type codexQuestionChannel struct {
	loadRegistry func() (coremetadata.Registry, error)
	store        func() (*agentquestion.Store, error)
	popup        claudeQuestionPopup
	answering    func() config.AgentQuestionAnswering
	window       func() time.Duration
	newID        func() (string, error)
	poll         time.Duration
	// beforeCanceledClose, when set, runs on a waiter whose context ended,
	// just before its store write. Tests use it to hold that write.
	beforeCanceledClose func()
}

type codexUserInputParams struct {
	ThreadID   string          `json:"threadId"`
	TurnID     string          `json:"turnId"`
	ItemID     string          `json:"itemId"`
	Questions  json.RawMessage `json:"questions"`
	IsBlocking bool            `json:"isBlocking"`
}

type codexUserInputAnswer struct {
	Answers []string `json:"answers"`
}

type codexUserInputResponse struct {
	Answers map[string]codexUserInputAnswer `json:"answers"`
}

// Handle records only a blocking request from the exact running Codex Agent.
// An off channel leaves the request entirely to Codex's own input surface.
// Bad or unavailable local state has the same safe fallback.
func (c codexQuestionChannel) Handle(ctx context.Context, identity codexLifecycleIdentity, notification codexappserver.Notification, responder codexQuestionResponder) {
	if notification.Method != "item/tool/requestUserInput" || len(notification.RawRequestID) == 0 || notification.RequestID == "" || responder == nil || c.loadRegistry == nil || c.store == nil || c.newID == nil {
		return
	}
	var params codexUserInputParams
	if json.Unmarshal(notification.Params, &params) != nil || !params.IsBlocking || params.ThreadID != identity.ThreadID || strings.TrimSpace(params.TurnID) == "" || strings.TrimSpace(params.ItemID) == "" {
		return
	}
	questions, err := agentquestion.ParseCodexQuestions(params.Questions)
	if err != nil {
		return
	}
	// The shared picker currently echoes free text. Keep secret sets on
	// Codex's own input surface, which can collect them without echoing.
	popup := c.popup
	for _, question := range questions {
		if question.IsSecret {
			popup = nil
			break
		}
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return
	}
	agent, ok := registry.Agent(identity.AgentUID)
	if !ok || coremetadata.NormalizeProvider(agent.Spec.Provider) != aiModeCodex || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != identity.PaneUID || !claudeQuestionAnsweredByProjmux(*agent, c.answering) {
		return
	}
	pane, ok := registry.Pane(identity.PaneUID)
	if !ok || pane.Status.Activation.AgentUID != identity.AgentUID || pane.Status.Activation.Generation != identity.Generation || pane.Status.Activation.RuntimeID != identity.RuntimeID {
		return
	}
	store, err := c.store()
	if err != nil {
		return
	}
	id, err := c.newID()
	if err != nil {
		return
	}
	window := config.DefaultAgentQuestionWindowSeconds * time.Second
	if c.window != nil {
		window = c.window()
	}
	now := time.Now().UTC()
	record, err := store.Create(agentquestion.Record{
		ID: id, Provider: "codex", AgentUID: identity.AgentUID, PaneUID: identity.PaneUID,
		SessionID: identity.ThreadID, Generation: identity.Generation, RuntimeID: identity.RuntimeID,
		RequestID: notification.RequestID, ToolUseID: params.ItemID, Questions: params.Questions,
		CreatedAt: now, Deadline: now.Add(window),
	})
	if err != nil {
		return
	}
	go c.waitAndAnswer(ctx, store, record, notification.RawRequestID, responder, popup, identity.RuntimeID, claudeQuestionAskerOf(registry, *agent))
}

// Wait is meant to return once every waiter Handle started has returned.
// Not joined yet.
func (c *codexQuestionChannel) Wait() {}

// HandleResolved marks only the matching waiting Codex request as answered in
// Codex's own input surface. An earlier CLI answer remains answered here.
func (c codexQuestionChannel) HandleResolved(identity codexLifecycleIdentity, event codexappserver.LifecycleEvent) {
	if event.Kind != codexappserver.LifecycleRequestResolved || event.ThreadID != identity.ThreadID || event.RequestID == "" || c.store == nil {
		return
	}
	store, err := c.store()
	if err != nil {
		return
	}
	records, err := store.List(identity.AgentUID)
	if err != nil {
		return
	}
	for _, record := range records {
		if record.Provider == "codex" && record.State == agentquestion.StateWaiting && record.PaneUID == identity.PaneUID && record.SessionID == identity.ThreadID && record.Generation == identity.Generation && record.RuntimeID == identity.RuntimeID && record.RequestID == event.RequestID {
			_, _ = store.CloseAnsweredElsewhere(record.ID, identity.AgentUID, identity.ThreadID, event.RequestID)
		}
	}
}

func (c codexQuestionChannel) waitAndAnswer(ctx context.Context, store *agentquestion.Store, record agentquestion.Record, rawID json.RawMessage, responder codexQuestionResponder, questionPopup claudeQuestionPopup, paneID string, asker claudeQuestionAsker) {
	poll := c.poll
	if poll <= 0 {
		poll = claudeQuestionPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(record.Deadline))
	defer deadline.Stop()
	popup := newClaudeQuestionPopupDriver(questionPopup, claudeQuestionClientPoll, paneID, asker, store, record)
	answered := false
	defer func() { popup.stop(answered) }()
	popup.maybeOpen(ctx)
	for {
		select {
		case <-ctx.Done():
			if c.beforeCanceledClose != nil {
				c.beforeCanceledClose()
			}
			_, _ = store.Close(record.ID)
			return
		case <-deadline.C:
			// Codex also presents this request in its native TUI. Leave the
			// provider request open so the operator can answer there.
			_, _ = store.Settle(record.ID)
			// An answer can win the store lock just before the timer. Let the
			// next read deliver it through the existing responder path.
			continue
		case err := <-popup.ended:
			if err == errClaudeQuestionPopupNotShown {
				popup.markNotShown()
				continue
			}
			popup.markEnded()
			_, _ = store.Close(record.ID)
		case <-ticker.C:
			current, found, err := store.Get(record.ID)
			if err != nil || !found {
				continue
			}
			switch current.State {
			case agentquestion.StateWaiting:
				if popup.finished {
					_, _ = store.Close(record.ID)
					continue
				}
				popup.maybeOpen(ctx)
				continue
			case agentquestion.StateAnswered:
				answered = true
				response := codexUserInputResponse{Answers: make(map[string]codexUserInputAnswer, len(current.Answers))}
				for id, encoded := range current.Answers {
					var values []string
					if json.Unmarshal([]byte(encoded), &values) != nil || len(values) == 0 {
						return
					}
					response.Answers[id] = codexUserInputAnswer{Answers: values}
				}
				answerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_ = responder.RespondServerRequest(answerCtx, rawID, response)
				cancel()
				return
			default:
				return
			}
		}
	}
}
