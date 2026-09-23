package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const (
	// claudeQuestionHookRoute is the hidden `internal` route Claude Code runs
	// as a PreToolUse hook for AskUserQuestion.
	claudeQuestionHookRoute = "claude-question-hook"
	// claudeQuestionPoll is how often the waiting hook rereads its record.
	claudeQuestionPoll = 250 * time.Millisecond
	// claudeQuestionGiveUp is how long past the deadline a hook that cannot
	// read its record keeps trying before it gives the question back.
	claudeQuestionGiveUp = 5 * time.Second
	// claudeQuestionPayloadLimit bounds the PreToolUse payload the hook reads.
	claudeQuestionPayloadLimit = 1 << 20
	// claudeQuestionClientPoll is how often a way-2 hook with no popup open
	// looks for a tmux client viewing the Agent's Pane.
	claudeQuestionClientPoll = time.Second
)

// claudeQuestionWindow is how long the hook holds one question open for a
// command-line answer before it gives the question back to Claude Code's own
// prompt. It is the central agent-question-window-seconds setting (60..3600,
// default 900); an out-of-range or broken value, and a config directory that
// cannot be resolved, read as the default.
//
// The installed hook timeout is derived from it when `projmux agent integrate
// claude` runs, not when the hook runs. A window raised without re-running
// integrate outlasts the installed timeout: Claude Code's SIGTERM at that
// timeout cancels the wait, which prints nothing, so the question goes on to
// the ordinary prompt.
func claudeQuestionWindow() time.Duration {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return config.DefaultAgentQuestionWindowSeconds * time.Second
	}
	return claudeQuestionWindowFromPaths(paths)
}

// claudeQuestionWindowFromPaths reads the question window under paths.
func claudeQuestionWindowFromPaths(paths config.Paths) time.Duration {
	seconds, _ := config.LoadAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile())
	return time.Duration(seconds) * time.Second
}

// claudeQuestionAnswering is the central agent-question-answering setting:
// way 1 (Claude Code's own prompt) unless the file names way 2. A config
// directory that cannot be resolved, like any unreadable or unknown value,
// reads as way 1.
func claudeQuestionAnswering() config.AgentQuestionAnswering {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return config.AgentQuestionAnsweringClaude
	}
	return claudeQuestionAnsweringFromPaths(paths)
}

// claudeQuestionAnsweringFromPaths reads the answering way under paths.
func claudeQuestionAnsweringFromPaths(paths config.Paths) config.AgentQuestionAnswering {
	answering, _ := config.LoadAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile())
	return answering
}

// claudeQuestionAnsweredByProjmux resolves which way answers a confirmed
// projmux Claude Agent's question: the Agent's own question channel annotation
// is way 2 without reading anything else; otherwise the central setting
// decides, read once. A nil resolver is way 1.
func claudeQuestionAnsweredByProjmux(agent coremetadata.Agent, answering func() config.AgentQuestionAnswering) bool {
	if coremetadata.QuestionChannelEnabled(agent) {
		return true
	}
	return answering != nil && answering() == config.AgentQuestionAnsweringProjmux
}

// claudeQuestionHook holds one AskUserQuestion tool call open until its
// question set is answered, and then answers the tool call with Claude Code's
// documented PreToolUse decision: permissionDecision "allow" with updatedInput
// carrying the original questions and the answers.
//
// It holds a question only in way 2: the Agent is opted in with `projmux agent
// question enable`, or the central agent-question-answering setting is
// `projmux`. Then it records the question, opens a projmux picker in a tmux
// popup on the client viewing the Agent's Pane (when one is, or as soon as one
// is), and waits for that picker or `projmux agent question answer`, whichever
// answers the record first.
//
// Whatever else happens, it prints nothing and succeeds, which Claude Code
// reads as "no decision": the question goes on to the ordinary prompt. That is
// way 1, and the outcome for every event it does not own, every error, an
// answer window that runs out, a popup that is canceled, fails to open, or
// ends without answering, and a cancellation. It never exits with the blocking
// status.
//
// Way 1 runs for every question of every Claude session with the hook
// installed, so it reads one payload, the Registry file, and, only once the
// Registry confirms a projmux Claude Agent that is not opted in, the one
// answering setting: no tmux, no store, no migration. The window is resolved
// only in way 2.
type claudeQuestionHook struct {
	loadRegistry func() (coremetadata.Registry, error)
	store        func() (*agentquestion.Store, error)
	answering    func() config.AgentQuestionAnswering
	window       func() time.Duration
	// popup opens the way-2 picker; nil never opens one, and the question is
	// then answered from the command line only.
	popup      claudeQuestionPopup
	poll       time.Duration
	clientPoll time.Duration
	newID      func() (string, error)
	now        func() time.Time
}

func defaultClaudeQuestionHook() claudeQuestionHook {
	return claudeQuestionHook{
		loadRegistry: func() (coremetadata.Registry, error) {
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				return coremetadata.Registry{}, err
			}
			return intmetadata.NewDefaultStore(paths).LoadReadOnly()
		},
		store:      defaultAgentQuestionStore,
		answering:  claudeQuestionAnswering,
		window:     claudeQuestionWindow,
		popup:      defaultClaudeQuestionPopup(),
		poll:       claudeQuestionPoll,
		clientPoll: claudeQuestionClientPoll,
		newID:      agentquestion.NewID,
		now:        time.Now,
	}
}

// defaultAgentQuestionStore opens the question store under the same state
// directory the Agent message store uses.
func defaultAgentQuestionStore() (*agentquestion.Store, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, err
	}
	return agentquestion.NewStore(paths.StateDir), nil
}

// runClaudeQuestionHook is the process entry of the route. SIGTERM, which
// Claude Code sends when the operator cancels the prompt or the hook timeout
// fires, and SIGINT/SIGHUP cancel the wait instead of killing the process, so
// the record is closed before it exits.
func runClaudeQuestionHook(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return runClaudeQuestionHookWith(defaultClaudeQuestionHook, args, stdin, stdout, stderr)
}

// runClaudeQuestionHookWith is runClaudeQuestionHook with the hook it builds
// injected. Everything from building the hook to writing the decision runs
// under one recover: an unrecovered Go panic exits with status 2, and Claude
// Code reads a PreToolUse exit 2 as blocking the tool. Way 1 runs for every
// Claude question, so a panic there would block questions of sessions that
// never opted into anything. A recovered panic prints nothing and succeeds,
// which is no decision; the run itself closes a record it already created.
func runClaudeQuestionHookWith(newHook func() claudeQuestionHook, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	defer func() {
		if recover() != nil {
			err = nil
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	newHook().run(ctx, args, stdin, stdout, stderr)
	return nil
}

// claudeQuestionPayload is the part of a PreToolUse payload the hook reads.
type claudeQuestionPayload struct {
	HookEventName string                     `json:"hook_event_name"`
	SessionID     string                     `json:"session_id"`
	ToolName      string                     `json:"tool_name"`
	ToolUseID     string                     `json:"tool_use_id"`
	ToolInput     map[string]json.RawMessage `json:"tool_input"`
	AgentID       string                     `json:"agent_id"`
	AgentType     string                     `json:"agent_type"`
}

// run is the whole hook. It reports nothing to its caller: every outcome is
// either the decision on stdout or silence.
func (h claudeQuestionHook) run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) {
	fs := flag.NewFlagSet("internal "+claudeQuestionHookRoute, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	paneRef := fs.String("pane", "", aiHookPaneArgumentUsage)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return
	}
	data, err := io.ReadAll(io.LimitReader(stdin, claudeQuestionPayloadLimit+1))
	if err != nil || len(data) > claudeQuestionPayloadLimit {
		return
	}
	var payload claudeQuestionPayload
	if json.Unmarshal(data, &payload) != nil {
		return
	}
	// A subagent's question belongs to a conversation the operator does not
	// see in the Pane, so it is left to Claude Code.
	if payload.HookEventName != "PreToolUse" || payload.ToolName != "AskUserQuestion" ||
		strings.TrimSpace(payload.AgentID) != "" || strings.TrimSpace(payload.AgentType) != "" {
		return
	}
	rawQuestions := bytes.TrimSpace(payload.ToolInput["questions"])
	if _, err := agentquestion.ParseQuestions(rawQuestions); err != nil {
		return
	}
	if h.loadRegistry == nil {
		return
	}
	registry, err := h.loadRegistry()
	if err != nil {
		return
	}
	agent, paneUID, ok := claudeQuestionAgent(registry, strings.TrimSpace(*paneRef), strings.TrimSpace(payload.SessionID))
	if !ok || !claudeQuestionAnsweredByProjmux(agent, h.answering) {
		return
	}
	// The popup is placed by the Agent Pane's live tmux handle.
	var paneID string
	if pane, found := registry.Pane(paneUID); found {
		paneID = strings.TrimSpace(pane.Status.Activation.RuntimeID)
	}

	store, err := h.openStore()
	if err != nil {
		fmt.Fprintf(stderr, "projmux: question store unavailable: %v\n", err)
		return
	}
	id, err := h.newID()
	if err != nil {
		return
	}
	window := config.DefaultAgentQuestionWindowSeconds * time.Second
	if h.window != nil {
		window = h.window()
	}
	created := h.now().UTC()
	record, err := store.Create(agentquestion.Record{
		ID: id, AgentUID: agent.Metadata.UID, PaneUID: paneUID,
		SessionID: payload.SessionID, ToolUseID: payload.ToolUseID,
		Questions: json.RawMessage(rawQuestions),
		CreatedAt: created, Deadline: created.Add(window),
	})
	if err != nil {
		fmt.Fprintf(stderr, "projmux: question not recorded: %v\n", err)
		return
	}
	// A panic past this point still hands the question back: the record is
	// closed before the panic goes on to the process entry's recover.
	defer func() {
		if recovered := recover(); recovered != nil {
			_, _ = store.Close(record.ID)
			panic(recovered)
		}
	}()
	answered, ok := h.wait(ctx, store, record, paneID)
	if !ok {
		return
	}
	decision, err := claudeQuestionDecision(rawQuestions, payload.ToolInput, answered.Answers)
	if err != nil {
		return
	}
	_, _ = stdout.Write(decision)
}

func (h claudeQuestionHook) openStore() (*agentquestion.Store, error) {
	if h.store == nil {
		return nil, fmt.Errorf("question store is not configured")
	}
	return h.store()
}

// wait polls the record until it is answered, closed, or its deadline passes,
// or ctx is canceled. Every terminal step that is this hook's to take runs
// under the store lock, so an answer racing the deadline or a cancellation
// resolves one way only: the returned record is answered exactly when the
// store recorded the answer.
//
// While it waits it keeps one popup picker open on the client viewing paneID.
// No such client is not a failure: the wait goes on for a command-line answer
// and looks again every clientPoll. A popup that ends while the record still
// waits (Esc, a failed open, a crashed picker) closes the record, which gives
// the question back; whatever ends the wait first closes a popup still open.
func (h claudeQuestionHook) wait(ctx context.Context, store *agentquestion.Store, record agentquestion.Record, paneID string) (agentquestion.Record, bool) {
	poll := h.poll
	if poll <= 0 {
		poll = claudeQuestionPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(record.Deadline))
	defer deadline.Stop()
	popup := newClaudeQuestionPopupDriver(h.popup, h.clientPoll, paneID, store, record)
	defer popup.stop()
	popup.maybeOpen(ctx)
	for {
		var step func(string) (agentquestion.Record, error)
		select {
		case <-ctx.Done():
			// Claude Code discards a canceled hook's output, so an answer that
			// landed just before the cancellation is not printed either.
			_, _ = store.Close(record.ID)
			return agentquestion.Record{}, false
		case <-deadline.C:
			step = store.Settle
		case <-popup.ended:
			// The picker answers or closes the record itself; one that ended
			// with the record still waiting is given back here. Close returns
			// the record as it stands, so a picker answer is kept.
			popup.markEnded()
			step = store.Close
		case <-ticker.C:
			current, found, err := store.Get(record.ID)
			switch {
			case err != nil && time.Now().After(record.Deadline.Add(claudeQuestionGiveUp)):
				// A store that stays unreadable past the window gives the
				// question back rather than holding it until the hook timeout.
				return agentquestion.Record{}, false
			case err != nil:
				continue
			case !found:
				return agentquestion.Record{}, false
			case current.State == agentquestion.StateWaiting && popup.finished:
				// The popup ended but closing the record failed; try again.
				step = store.Close
			case current.State == agentquestion.StateWaiting:
				popup.maybeOpen(ctx)
				continue
			case current.State == agentquestion.StateExpired:
				step = store.Settle
			default:
				return current, current.State == agentquestion.StateAnswered
			}
		}
		settled, err := step(record.ID)
		if errors.Is(err, agentquestion.ErrNotFound) || (err != nil && time.Now().After(record.Deadline.Add(claudeQuestionGiveUp))) {
			return agentquestion.Record{}, false
		}
		if err != nil {
			continue
		}
		// A timer that fires a hair before the store clock reaches the
		// deadline leaves the record waiting; the next tick settles it.
		if settled.State != agentquestion.StateWaiting {
			return settled, settled.State == agentquestion.StateAnswered
		}
	}
}

// claudeQuestionAgent resolves the Agent a question hook belongs to from the
// Registry alone. The Pane handed to the hook is the answer when there is one,
// through the same Pane lookup the ingest hook uses and the same round trip:
// the Pane's Agent must point back at that Pane. Without one, the session id
// names the Agent when exactly one Running Claude Agent records it.
func claudeQuestionAgent(registry coremetadata.Registry, paneRef, sessionID string) (coremetadata.Agent, string, bool) {
	var agent *coremetadata.Agent
	if paneRef != "" {
		pane, ok := explicitAIPaneResource(registry, paneRef)
		if !ok {
			return coremetadata.Agent{}, "", false
		}
		agentUID := strings.TrimSpace(pane.Status.Activation.AgentUID)
		if agentUID == "" && pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
			agentUID = pane.Metadata.OwnerRef.UID
		}
		found, ok := registry.Agent(agentUID)
		if agentUID == "" || !ok || found.Status.PaneRef != pane.Metadata.UID {
			return coremetadata.Agent{}, "", false
		}
		agent = found
	} else if sessionID != "" {
		for i := range registry.Agents {
			candidate := &registry.Agents[i]
			if candidate.Status.Phase != coremetadata.PhaseRunning || candidate.Status.SessionRef.ConversationID() != sessionID {
				continue
			}
			if agent != nil {
				return coremetadata.Agent{}, "", false
			}
			agent = candidate
		}
	}
	if agent == nil || coremetadata.NormalizeProvider(agent.Spec.Provider) != aiModeClaude {
		return coremetadata.Agent{}, "", false
	}
	return agent.Clone(), agent.Status.PaneRef, true
}

// claudeQuestionDecision spells the PreToolUse decision that answers an
// AskUserQuestion call. updatedInput replaces the whole tool input, so it
// carries the questions exactly as Claude Code sent them, every other input
// field verbatim, and the answers.
func claudeQuestionDecision(rawQuestions json.RawMessage, toolInput map[string]json.RawMessage, answers map[string]string) ([]byte, error) {
	encodedAnswers, err := marshalJSONNoEscape(answers)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"questions":`)
	out.Write(rawQuestions)
	keys := make([]string, 0, len(toolInput))
	for key := range toolInput {
		if key != "questions" && key != "answers" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		encodedKey, err := marshalJSONNoEscape(key)
		if err != nil {
			return nil, err
		}
		out.WriteByte(',')
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(bytes.TrimSpace(toolInput[key]))
	}
	out.WriteString(`,"answers":`)
	out.Write(encodedAnswers)
	out.WriteString("}}}\n")
	return out.Bytes(), nil
}

func marshalJSONNoEscape(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
