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
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const (
	// claudePermissionHookRoute is the hidden `internal` route Claude Code runs
	// as a PermissionRequest hook.
	claudePermissionHookRoute = "claude-permission-hook"
	// claudePermissionPoll is how often the waiting hook rereads its record.
	claudePermissionPoll = 250 * time.Millisecond
	// claudePermissionGiveUp is how long past the deadline a hook that cannot
	// read its record keeps trying before it gives the request back.
	claudePermissionGiveUp = 5 * time.Second
	// claudePermissionPayloadLimit bounds the PermissionRequest payload the
	// hook reads.
	claudePermissionPayloadLimit = 1 << 20
	// claudePermissionDenyMessage is what Claude Code tells the model when the
	// operator denied the request in projmux.
	claudePermissionDenyMessage = "denied by the operator in projmux"
)

// claudePermissionAllowDecision and claudePermissionDenyDecision are the only
// two outputs the hook ever prints. The allow decision is exactly the behavior:
// it never carries updatedInput or updatedPermissions, so an allow runs the
// tool call as Claude Code asked for it, once, and never adds a permission
// rule; the request's permission_suggestions are never applied.
var (
	claudePermissionAllowDecision = []byte(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` + "\n")
	claudePermissionDenyDecision  = []byte(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"` +
		claudePermissionDenyMessage + `","interrupt":false}}}` + "\n")
)

// claudePermissionSkippedModes are the permission modes whose requests the hook
// never captures: the operator already chose not to be asked.
var claudePermissionSkippedModes = map[string]bool{"bypassPermissions": true, "dontAsk": true}

// claudePermissionWindow is how long the hook holds one permission request
// open for a command-line answer: the central agent-approval-window-seconds
// setting (60..3600, default 900). An out-of-range or broken value, and a
// config directory that cannot be resolved, read as the default. It is read
// for every request; the installed timeout is the fixed ceiling
// config.AgentApprovalHookTimeoutSeconds.
func claudePermissionWindow() time.Duration {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return config.DefaultAgentApprovalWindowSeconds * time.Second
	}
	return claudePermissionWindowFromPaths(paths)
}

// claudePermissionWindowFromPaths reads the permission window under paths.
func claudePermissionWindowFromPaths(paths config.Paths) time.Duration {
	seconds, _ := config.LoadAgentApprovalWindowSecondsFile(paths.AgentApprovalWindowSecondsFile())
	return time.Duration(seconds) * time.Second
}

// claudePermissionAnswering is the central agent-approval-answering setting:
// way 1 (Claude Code's own prompt) unless the file names projmux. A config
// directory that cannot be resolved, like any unreadable or unknown value,
// reads as way 1.
func claudePermissionAnswering() config.AgentApprovalAnswering {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return config.AgentApprovalAnsweringClaude
	}
	return claudePermissionAnsweringFromPaths(paths)
}

// claudePermissionAnsweringFromPaths reads the answering way under paths.
func claudePermissionAnsweringFromPaths(paths config.Paths) config.AgentApprovalAnswering {
	answering, _ := config.LoadAgentApprovalAnsweringFile(paths.AgentApprovalAnsweringFile())
	return answering
}

// defaultAgentApprovalStore opens the permission request store under the same
// state directory the question store uses.
func defaultAgentApprovalStore() (*agentapproval.Store, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, err
	}
	return agentapproval.NewStore(paths.StateDir), nil
}

// claudePermissionHook holds one Claude Code permission request open until the
// operator allows or denies it with `projmux agent approval answer`, and then
// answers the request with Claude Code's PermissionRequest decision.
//
// It captures a request only when the central agent-approval-answering setting
// is `projmux` and the Registry confirms the request comes from a projmux
// Claude Agent. A subagent's request is captured too and recorded with its
// agent type; a session in bypassPermissions or dontAsk mode is not.
//
// Claude Code shows its own prompt while the hook waits, and that prompt stays
// usable: the first answer wins. A terminal "No" cancels the hook with SIGTERM;
// a terminal "Yes" does not tell the hook at all, so the PostToolUse ingest
// closes the matching record (closeClaudePermissionAnsweredInTerminal).
//
// It is deny-by-default: it prints the allow decision only for a record this
// store settled as allowed under its lock, and the deny decision only for one
// settled as denied. Every other outcome prints nothing and succeeds, which
// Claude Code reads as no decision: way 1, every request it does not own, every
// error, a window that runs out, a cancellation, and a panic. It never exits
// with the blocking status.
//
// Way 1 runs for every permission request of every Claude session with the hook
// installed, so it reads one payload, the Registry file, and, only once the
// Registry confirms a projmux Claude Agent, the one answering setting: no tmux,
// no store, no migration, no Registry write. The window is resolved only when
// the request is captured.
type claudePermissionHook struct {
	loadRegistry func() (coremetadata.Registry, error)
	store        func() (*agentapproval.Store, error)
	answering    func() config.AgentApprovalAnswering
	window       func() time.Duration
	poll         time.Duration
	giveUp       time.Duration
	readRecord   func(*agentapproval.Store, string) (agentapproval.Record, bool, error)
	newID        func() (string, error)
	now          func() time.Time
}

func defaultClaudePermissionHook() claudePermissionHook {
	return claudePermissionHook{
		loadRegistry: func() (coremetadata.Registry, error) {
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				return coremetadata.Registry{}, err
			}
			return intmetadata.NewDefaultStore(paths).LoadReadOnly()
		},
		store:     defaultAgentApprovalStore,
		answering: claudePermissionAnswering,
		window:    claudePermissionWindow,
		poll:      claudePermissionPoll,
		giveUp:    claudePermissionGiveUp,
		newID:     agentapproval.NewID,
		now:       time.Now,
	}
}

// runClaudePermissionHook is the process entry of the route. SIGTERM, which
// Claude Code sends when the operator answers No in its own prompt or the hook
// timeout fires, and SIGINT/SIGHUP cancel the wait instead of killing the
// process, so the record is closed before it exits.
func runClaudePermissionHook(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return runClaudePermissionHookWith(defaultClaudePermissionHook, args, stdin, stdout, stderr)
}

// runClaudePermissionHookWith is runClaudePermissionHook with the hook it
// builds injected. Everything from building the hook to writing the decision
// runs under one recover: an unrecovered Go panic exits with status 2, which
// Claude Code reads as a blocking hook. A recovered panic prints nothing and
// succeeds, which is no decision; the run itself closes a record it already
// created.
func runClaudePermissionHookWith(newHook func() claudePermissionHook, args []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
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

// claudePermissionPayload is the part of a PermissionRequest payload the hook
// reads. The payload carries no tool_use_id.
type claudePermissionPayload struct {
	HookEventName  string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	PermissionMode string          `json:"permission_mode"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	AgentID        string          `json:"agent_id"`
	AgentType      string          `json:"agent_type"`
}

// run is the whole hook. It reports nothing to its caller: every outcome is
// either the decision on stdout or silence.
func (h claudePermissionHook) run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) {
	fs := flag.NewFlagSet("internal "+claudePermissionHookRoute, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	paneRef := fs.String("pane", "", aiHookPaneArgumentUsage)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return
	}
	data, err := io.ReadAll(io.LimitReader(stdin, claudePermissionPayloadLimit+1))
	if err != nil || len(data) > claudePermissionPayloadLimit {
		return
	}
	var payload claudePermissionPayload
	if json.Unmarshal(data, &payload) != nil {
		return
	}
	toolInput := bytes.TrimSpace(payload.ToolInput)
	if payload.HookEventName != "PermissionRequest" || strings.TrimSpace(payload.ToolName) == "" ||
		len(toolInput) == 0 || toolInput[0] != '{' || len(toolInput) > agentapproval.MaxToolInputBytes ||
		claudePermissionSkippedModes[payload.PermissionMode] {
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
	if !ok || h.answering == nil || h.answering() != config.AgentApprovalAnsweringProjmux {
		return
	}

	store, err := h.openStore()
	if err != nil {
		fmt.Fprintf(stderr, "projmux: approval store unavailable: %v\n", err)
		return
	}
	if h.newID == nil {
		return
	}
	id, err := h.newID()
	if err != nil {
		return
	}
	window := config.DefaultAgentApprovalWindowSeconds * time.Second
	if h.window != nil {
		window = h.window()
	}
	created := h.clock()().UTC()
	record, err := store.Create(agentapproval.Record{
		ID: id, AgentUID: agent.Metadata.UID, PaneUID: paneUID, SessionID: payload.SessionID,
		AgentType: strings.TrimSpace(payload.AgentType), ToolName: payload.ToolName, ToolInput: json.RawMessage(toolInput),
		CreatedAt: created, Deadline: created.Add(window),
	})
	if err != nil {
		fmt.Fprintf(stderr, "projmux: permission request not recorded: %v\n", err)
		return
	}
	// A panic past this point still gives the request back: the record is
	// closed before the panic goes on to the process entry's recover.
	defer func() {
		if recovered := recover(); recovered != nil {
			_, _ = store.Close(record.ID, agentapproval.CloseReasonFailed)
			panic(recovered)
		}
	}()
	settled, ok := h.wait(ctx, store, record)
	if !ok {
		// A wait that ended without a settled record leaves nothing waiting
		// behind it for a later answer to settle; a record that already
		// settled keeps its state.
		_, _ = store.Close(record.ID, agentapproval.CloseReasonFailed)
		return
	}
	// Claude Code discards a canceled hook's output, and a decision the store
	// did not settle for this very record is never printed.
	if ctx.Err() != nil || settled.ID != record.ID {
		return
	}
	switch settled.State {
	case agentapproval.StateAllowed:
		_, _ = stdout.Write(claudePermissionAllowDecision)
	case agentapproval.StateDenied:
		_, _ = stdout.Write(claudePermissionDenyDecision)
	}
}

// clock is the hook's injected clock, or the wall clock when none is set.
func (h claudePermissionHook) clock() func() time.Time {
	if h.now != nil {
		return h.now
	}
	return time.Now
}

func (h claudePermissionHook) openStore() (*agentapproval.Store, error) {
	if h.store == nil {
		return nil, fmt.Errorf("approval store is not configured")
	}
	store, err := h.store()
	if err == nil && store == nil {
		return nil, fmt.Errorf("approval store is not configured")
	}
	return store, err
}

// wait polls the record until it is answered, closed, or its deadline passes,
// or ctx is canceled. It reports settled only with a record read back under the
// store lock: an allowed or denied state an unlocked read saw is confirmed by
// one locked Settle before it counts, so an answer racing the deadline, a
// terminal close, or a cancellation resolves one way only.
func (h claudePermissionHook) wait(ctx context.Context, store *agentapproval.Store, record agentapproval.Record) (agentapproval.Record, bool) {
	read := h.readRecord
	if read == nil {
		read = func(store *agentapproval.Store, id string) (agentapproval.Record, bool, error) { return store.Get(id) }
	}
	poll := h.poll
	if poll <= 0 {
		poll = claudePermissionPoll
	}
	giveUp := h.giveUp
	if giveUp <= 0 {
		giveUp = claudePermissionGiveUp
	}
	pastGiveUp := func() bool { return time.Now().After(record.Deadline.Add(giveUp)) }
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(record.Deadline))
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			_, _ = store.Close(record.ID, agentapproval.CloseReasonCanceled)
			return agentapproval.Record{}, false
		case <-deadline.C:
		case <-ticker.C:
			current, found, err := read(store, record.ID)
			switch {
			case err != nil && pastGiveUp():
				// A store that stays unreadable past the window gives the
				// request back rather than holding it until the hook timeout.
				return agentapproval.Record{}, false
			case err != nil:
				continue
			case !found:
				return agentapproval.Record{}, false
			case current.State == agentapproval.StateWaiting:
				continue
			case current.State == agentapproval.StateClosed:
				return agentapproval.Record{}, false
			}
		}
		settled, err := store.Settle(record.ID)
		if errors.Is(err, agentapproval.ErrNotFound) || (err != nil && pastGiveUp()) {
			return agentapproval.Record{}, false
		}
		if err != nil {
			continue
		}
		// A timer that fires a hair before the store clock reaches the
		// deadline leaves the record waiting; the next tick settles it.
		if settled.State != agentapproval.StateWaiting {
			return settled, true
		}
	}
}
