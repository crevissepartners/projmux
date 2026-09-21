package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// errClaudeTurnEndChanged refuses the turn-end commit when the Agent or its
// blocked observation changed after the transcript was judged.
var errClaudeTurnEndChanged = errors.New("claude agent interaction changed before the turn-end commit")

// claudeTranscriptTurnLine is the only part of one transcript line the
// held-message release reads. Message stays raw and is only asked whether an
// assistant line carries a tool_use item.
type claudeTranscriptTurnLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

// claudeTurnEndedAfter reports whether a Claude transcript tail shows that the
// turn which raised the blocked observation at blockedAt has ended: its last
// `turn_duration` system line is stamped strictly after blockedAt and no
// assistant tool_use follows it. A denied permission sends no hook, so this
// line is the only trace that the dialog is gone. Every doubt answers false:
// a tail with a line that is not JSON, no turn_duration line, a timestamp that
// does not parse or is not after blockedAt, or a later tool_use. When the tail
// was read from after offset 0, its first line is partial and is dropped.
func claudeTurnEndedAfter(tail []byte, truncatedHead bool, blockedAt time.Time) bool {
	if truncatedHead {
		newline := bytes.IndexByte(tail, '\n')
		if newline < 0 {
			return false
		}
		tail = tail[newline+1:]
	}
	var turnEnd string
	found, toolUseAfter := false, false
	for raw := range bytes.SplitSeq(tail, []byte("\n")) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var line claudeTranscriptTurnLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return false
		}
		switch {
		case line.Type == "system" && line.Subtype == "turn_duration":
			turnEnd, found, toolUseAfter = line.Timestamp, true, false
		case line.Type == "assistant" && found:
			if claudeTranscriptMessageMayUseTool(line.Message) {
				toolUseAfter = true
			}
		}
	}
	if !found || toolUseAfter {
		return false
	}
	ended, err := time.Parse(time.RFC3339Nano, turnEnd)
	return err == nil && ended.After(blockedAt)
}

// claudeTranscriptMessageMayUseTool reports whether an assistant message has a
// tool_use content item. A message whose shape cannot be read counts as one.
func claudeTranscriptMessageMayUseTool(message json.RawMessage) bool {
	if len(message) == 0 {
		return false
	}
	var body struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &body); err != nil {
		return true
	}
	content := bytes.TrimSpace(body.Content)
	if len(content) == 0 || content[0] != '[' {
		return false
	}
	var items []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &items); err != nil {
		return true
	}
	for _, item := range items {
		if item.Type == "tool_use" {
			return true
		}
	}
	return false
}

// claudeAgentTranscriptPath is the transcript the target Agent's own Registry
// binding recorded. It is never derived from a working directory.
func claudeAgentTranscriptPath(agent coremetadata.Agent) string {
	ref := agent.Status.SessionRef
	if ref == nil || ref.Provider != string(aiprovider.Claude) || ref.Claude == nil {
		return ""
	}
	return strings.TrimSpace(ref.Claude.TranscriptPath)
}

// readClaudeTranscriptTail reads at most the last claudeTranscriptTailLimit
// bytes of a transcript and reports whether the read started after offset 0.
func readClaudeTranscriptTail(path string) ([]byte, bool, error) {
	file, err := os.Open(path) // #nosec G304 -- the path is the transcript the target Agent's own Registry binding recorded from its hook; the read is bounded and read-only.
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	size := info.Size()
	if size <= 0 {
		return nil, false, nil
	}
	start := max(size-claudeTranscriptTailLimit, 0)
	buf := make([]byte, size-start)
	n, err := file.ReadAt(buf, start)
	if n != len(buf) {
		if err == nil || errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, false, err
	}
	return buf, start > 0, nil
}

func (c *agentCommand) readMessageTranscriptTail(path string) ([]byte, bool, error) {
	if c != nil && c.messageTranscriptTail != nil {
		return c.messageTranscriptTail(path)
	}
	return readClaudeTranscriptTail(path)
}

// endBlockedClaudeTurn asks the transcript of a target found awaiting its
// operator whether that turn has already ended. When it has, it records the
// interaction the Stop hook would have written and reports true, so the held
// record is judged again at once. Any read failure or doubt reports false and
// the target stays blocked.
func (c *agentCommand) endBlockedClaudeTurn(target coremetadata.Agent) bool {
	path := claudeAgentTranscriptPath(target)
	if path == "" {
		return false
	}
	tail, truncated, err := c.readMessageTranscriptTail(path)
	if err != nil {
		return false
	}
	blocked := target.Status.Interaction
	if !claudeTurnEndedAfter(tail, truncated, blocked.ObservedAt) {
		return false
	}
	return c.commitClaudeTurnEnd(target, blocked) == nil
}

// commitClaudeTurnEnd writes response_complete from the provider hook source as
// one compare-and-set: only while the Agent is still Running on the same Pane
// and still carries the very blocked observation the transcript was judged
// against. A committed turn end is then projected onto the managed Pane, so the
// badge the operator reads does not stay on the answered dialog until the next
// hook writes it.
func (c *agentCommand) commitClaudeTurnEnd(target coremetadata.Agent, blocked coremetadata.AgentInteraction) error {
	if c == nil || c.store == nil || c.store.update == nil {
		return errors.New("agent registry mutation is not configured")
	}
	mutator := intmetadata.DefaultMutator()
	if c.store.mutator != nil {
		mutator = c.store.mutator()
	}
	mutator.Now = c.messageClock
	uid := target.Metadata.UID
	var committed coremetadata.Agent
	_, err := c.store.update(func(working *coremetadata.Registry) error {
		current, ok := working.Agent(uid)
		if !ok || current.Status.Phase != coremetadata.PhaseRunning || current.Status.PaneRef != target.Status.PaneRef ||
			current.Status.Interaction.Kind != blocked.Kind || !current.Status.Interaction.ObservedAt.Equal(blocked.ObservedAt) {
			return errClaudeTurnEndChanged
		}
		updated, err := mutator.SetAgentInteraction(working, uid, coremetadata.InteractionResponseComplete,
			string(coremetadata.InteractionSourceProviderHook))
		if err != nil {
			return err
		}
		committed = updated.Clone()
		return nil
	})
	if err != nil {
		return err
	}
	// The Registry commit is the result; the badge is its projection. A failed
	// projection is dropped rather than returned: the release is detached with
	// no standard stream, so nothing would read the error, and returning it
	// would tell endBlockedClaudeTurn the turn had not ended although it was
	// committed, which would keep held messages waiting on a dialog that is
	// already gone. A Pane the mirror cannot find, and a release that inherited
	// no TMUX, leave the badge to the next interaction writer.
	_ = c.mirrorAgentInteraction(committed, coremetadata.InteractionResponseComplete)
	return nil
}
