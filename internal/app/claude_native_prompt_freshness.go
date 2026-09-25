package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const (
	// claudeNativePromptRefreshInterval is how old a Claude native prompt's
	// provider-hook raise must be before the pane supervisor recommits it. Its
	// contract is the way-2 question refresh's: interval <
	// coremetadata.AgentInteractionFreshFor, since every read of the effective
	// interaction reads an older observation as unknown, and a third of the
	// window leaves two missed refreshes of slack.
	claudeNativePromptRefreshInterval = coremetadata.AgentInteractionFreshFor / 3
	// claudeNativePromptCheckInterval is how often the pane supervisor's lease
	// watcher asks whether a native prompt is due. A check that is not due reads
	// only the Registry; a failed recommit is tried again on the next check.
	claudeNativePromptCheckInterval = time.Minute
)

// errClaudeNativePromptBindingChanged aborts a native-prompt recommit whose
// Agent is no longer the running Claude Agent of this supervisor's Pane and
// activation. It is recorded, since a dialog the transcript shows open is then
// left to decay.
var errClaudeNativePromptBindingChanged = errors.New("claude native prompt binding changed before refresh commit")

// errClaudeNativePromptInteractionChanged aborts a native-prompt recommit whose
// Agent no longer carries the exact observation that was judged: another writer
// owns the interaction now, so the recommit writes nothing and records nothing.
var errClaudeNativePromptInteractionChanged = errors.New("claude native prompt interaction changed before refresh commit")

// claudeNativePromptLine is the only part of one transcript line the native
// prompt judge reads: its type and subtype, and a raw message it asks only for
// tool_use ids and tool_result tool_use_ids.
type claudeNativePromptLine struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Message json.RawMessage `json:"message"`
}

// claudeNativePromptOpen reports whether a Claude transcript tail positively
// shows that the native prompt raised at observedAt is still open: the turn
// has not ended after observedAt by claudeTurnEndedAfter, and after the tail's
// last `turn_duration` system line some assistant tool_use id has no later
// user tool_result naming it. Claude writes parallel tool uses as separate
// assistant lines and each result as its own user line; a tool_result for a
// tool_use outside the tail is ignored. Every doubt answers false: a line that
// is not JSON, an empty tail, a truncated head with no newline, a tool_use with
// no id or a tool_result with no tool_use_id, a message whose content shape
// cannot be read, or no unanswered tool_use at all. When the tail was read from
// after offset 0, its first line is partial and is dropped.
func claudeNativePromptOpen(tail []byte, truncatedHead bool, observedAt time.Time) bool {
	if claudeTurnEndedAfter(tail, truncatedHead, observedAt) {
		return false
	}
	if truncatedHead {
		newline := bytes.IndexByte(tail, '\n')
		if newline < 0 {
			return false
		}
		tail = tail[newline+1:]
	}
	open := map[string]bool{}
	for raw := range bytes.SplitSeq(tail, []byte("\n")) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var line claudeNativePromptLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return false
		}
		switch {
		case line.Type == "system" && line.Subtype == "turn_duration":
			clear(open)
		case line.Type == "assistant" || line.Type == "user":
			items, ok := claudeNativePromptItems(line.Message)
			if !ok {
				return false
			}
			for _, item := range items {
				switch {
				case line.Type == "assistant" && item.Type == "tool_use":
					if item.ID == "" {
						return false
					}
					open[item.ID] = true
				case line.Type == "user" && item.Type == "tool_result":
					if item.ToolUseID == "" {
						return false
					}
					delete(open, item.ToolUseID)
				}
			}
		}
	}
	return len(open) > 0
}

// claudeNativePromptItem is the only part of one message content item the
// judge reads.
type claudeNativePromptItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
}

// claudeNativePromptItems reads a message's content items. A message with no
// content, or with string content, has none; a missing message, one that is not
// an object, or content that is neither a string nor an array of objects
// cannot be read.
func claudeNativePromptItems(message json.RawMessage) ([]claudeNativePromptItem, bool) {
	message = bytes.TrimSpace(message)
	if len(message) == 0 || message[0] != '{' {
		return nil, false
	}
	var body struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &body); err != nil {
		return nil, false
	}
	content := bytes.TrimSpace(body.Content)
	switch {
	case len(content) == 0:
		return nil, true
	case content[0] == '"':
		return nil, true
	case content[0] != '[':
		return nil, false
	}
	var items []claudeNativePromptItem
	if err := json.Unmarshal(content, &items); err != nil {
		return nil, false
	}
	return items, true
}

// claudeNativePromptRefresh is the pane supervisor's native-prompt producer.
// Every seam is injected so a test drives it one step at a time; production
// wires the supervisor's exact Registry, the bounded transcript tail read, the
// wall clock, and the operations diagnostics journal.
type claudeNativePromptRefresh struct {
	spec     superviseSpec
	now      func() time.Time
	load     func() (coremetadata.Registry, error)
	update   func(func(*coremetadata.Registry) error) error
	readTail func(string) ([]byte, bool, error)
	// record is told the diagnostics kind of a dialog the transcript showed
	// open whose recommit failed for a reason other than another writer.
	record func(diagnostics.AIKind, time.Time)
}

// newClaudeNativePromptRefresh wires the producer for one supervised
// activation. A nil recorder records nothing.
func newClaudeNativePromptRefresh(spec superviseSpec, recorder *diagnostics.AIRecorder) claudeNativePromptRefresh {
	store := intmetadata.NewStore(spec.RegistryPath)
	return claudeNativePromptRefresh{
		spec: spec, now: time.Now, load: store.Load, readTail: readClaudeTranscriptTail,
		update: func(fn func(*coremetadata.Registry) error) error {
			_, _, err := store.UpdateConvergent(fn)
			return err
		},
		record: func(kind diagnostics.AIKind, started time.Time) {
			recorder.RecordIngest(diagnostics.ProviderClaude, kind, diagnostics.AIResultFailed, diagnostics.AIFailureRoute, started, false)
		},
	}
}

// claudeNativePromptKind is the diagnostics kind of a native prompt's raw
// interaction, and false for any interaction this producer never refreshes.
func claudeNativePromptKind(interaction coremetadata.AgentInteraction) (diagnostics.AIKind, bool) {
	if interaction.Source != string(coremetadata.InteractionSourceProviderHook) {
		return "", false
	}
	switch interaction.Kind {
	case coremetadata.InteractionApprovalRequired:
		return diagnostics.AIKindPermission, true
	case coremetadata.InteractionInputRequired:
		return diagnostics.AIKindNotification, true
	default:
		return "", false
	}
}

// step keeps an open Claude native prompt from decaying. A permission dialog or
// an elicitation raises its Agent once from the provider hook, and nothing
// else writes while the dialog is up, so past the interaction freshness window
// the hold would release into the open dialog. Once the Agent's raw
// interaction is a provider-hook approval_required or input_required at least
// claudeNativePromptRefreshInterval old, step reads the Agent's own recorded
// transcript tail and, only when claudeNativePromptOpen judges the dialog still
// open, recommits the same interaction in one Registry transaction. That
// transaction is fenced on this supervisor's activation -- the Agent is the
// running Claude Agent bound to spec's Pane, whose activation is spec's
// Generation for spec's Agent -- and compares-and-sets on the exact judged
// observation. The recommit is stamped with the time taken before the tail
// read, so a turn_duration written after that read stays after the new
// observation and the held-message release still sees the turn end. It projects
// nothing: the Pane already shows the kind. It never writes to a stream: the
// supervisor's stderr is the provider's terminal. A binding that changed or a
// failed transaction is recorded once the transcript showed the dialog open;
// an observation another writer replaced is expected and records nothing.
//
// It reports whether later steps can still apply to this activation: false
// once the Agent is gone or is not a Claude Agent, since neither changes
// within one activation, so the watcher stops asking and stops loading the
// Registry. A failed load, an interaction that is not due, or any other answer
// keeps it asking.
func (r claudeNativePromptRefresh) step() (keep bool) {
	if r.now == nil || r.load == nil || r.update == nil || r.readTail == nil {
		return false
	}
	registry, err := r.load()
	if err != nil {
		return true
	}
	agent, ok := registry.Agent(r.spec.AgentUID)
	if !ok || agent.Spec.Provider != string(aiprovider.Claude) {
		return false
	}
	judged := agent.Status.Interaction
	kind, ok := claudeNativePromptKind(judged)
	judgedAt := r.now()
	if !ok || judged.ObservedAt.IsZero() || judgedAt.Sub(judged.ObservedAt) < claudeNativePromptRefreshInterval {
		return true
	}
	path := claudeAgentTranscriptPath(*agent)
	if path == "" {
		return true
	}
	tail, truncated, err := r.readTail(path)
	if err != nil || !claudeNativePromptOpen(tail, truncated, judged.ObservedAt) {
		return true
	}
	mutator := intmetadata.DefaultMutator()
	mutator.Now = func() time.Time { return judgedAt }
	spec := r.spec
	err = r.update(func(working *coremetadata.Registry) error {
		current, ok := working.Agent(spec.AgentUID)
		if !ok || current.Spec.Provider != string(aiprovider.Claude) || current.Status.Phase != coremetadata.PhaseRunning ||
			current.Status.PaneRef != spec.PaneUID {
			return errClaudeNativePromptBindingChanged
		}
		pane, ok := working.Pane(spec.PaneUID)
		if !ok || pane.Status.Activation.Generation != spec.Generation || pane.Status.Activation.AgentUID != spec.AgentUID {
			return errClaudeNativePromptBindingChanged
		}
		interaction := current.Status.Interaction
		if interaction.Kind != judged.Kind || interaction.Source != judged.Source || !interaction.ObservedAt.Equal(judged.ObservedAt) {
			return errClaudeNativePromptInteractionChanged
		}
		_, err := mutator.SetAgentInteraction(working, spec.AgentUID, judged.Kind, string(coremetadata.InteractionSourceProviderHook))
		return err
	})
	if err != nil && !errors.Is(err, errClaudeNativePromptInteractionChanged) && r.record != nil {
		r.record(kind, judgedAt)
	}
	return true
}
