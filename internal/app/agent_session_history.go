package app

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

// The Claude and Codex session history write.
//
// The Registry keeps one conversation per Agent in `status.sessionRef` and
// overwrites it when the Agent moves on. Every committed write that moves a
// Claude or Codex Agent to a different conversation also appends one line to
// <state>/agent-session-history.jsonl, so the conversations it left stay
// listable (`agent sessions list`, `agent sessions project`).
//
// The writers of observed rows are a closed set of four functions:
//
//   - agent_session_ref.go persistAgentSessionRef (hook ingest);
//   - agent_session_ref.go persistManagedAgentInteractionWithActivationPolicy
//     (managed-Agent interaction commit);
//   - create_intent.go openIntentAgent (resume-picker create, and native
//     Codex create through the intent path);
//   - create_agent.go createAgent (native Codex fresh create).
//
// Each follows the same two steps:
//
//  1. inside the Registry transaction, it builds the line with
//     sessionhistory.ObservedRecordFor -- directly, or through
//     claudeSessionHistoryRecord, which first turns RecordAgentSessionRef's
//     changed verdict into the line or none -- passing the transaction's own
//     working Registry, so the row carries the Agent's projectUID, windowUID,
//     and agentName as that transaction committed them (each empty when its
//     link of the chain did not resolve; never guessed);
//  2. after the transaction commits, an append helper writes it.
//
// TestClaudeSessionRefWritersRecordHistory pins the callers of
// RecordAgentSessionRef, and
// TestSessionHistoryObservedRowWritersCarryAffiliation pins the set of four
// and that no other code builds an observed row with RecordFor, so a new
// writer cannot land without the history or without its affiliation.
//
// The append never fails its caller, and neither does a chain that does not
// resolve. The Registry commit and the append are not atomic: a crash
// between them loses that one line, which is the documented cost of keeping
// the history out of the Registry.

// sessionHistoryNotRecordedFmt is the one stderr line a create prints when
// its history line could not be written.
const sessionHistoryNotRecordedFmt = "agent session history not recorded: %s\n"

// claudeSessionHistoryRecord is step 1. changed is RecordAgentSessionRef's
// verdict, so "a different conversation" means exactly what the Registry
// write means by it; committed is the ref the mutator stored, and working is
// the Registry of the same transaction, which the row's affiliation is
// resolved from. The name is retained for the established Claude writer
// guard; Codex uses the same row.
func claudeSessionHistoryRecord(working *coremetadata.Registry, agentUID string, changed bool, committed *coremetadata.AgentSessionRef) (sessionhistory.Record, bool) {
	if !changed {
		return sessionhistory.Record{}, false
	}
	return sessionhistory.ObservedRecordFor(working, agentUID, committed)
}

// historyConversationChanged narrows Codex writes to thread identity changes.
// RecordAgentSessionRef can also change optional session metadata or endpoint
// without changing the thread; those updates must not append a duplicate row.
func historyConversationChanged(changed bool, previous, committed *coremetadata.AgentSessionRef) bool {
	if !changed || committed == nil || committed.Provider != sessionhistory.ProviderCodex || committed.Codex == nil {
		return changed
	}
	if previous == nil || previous.Provider != sessionhistory.ProviderCodex || previous.Codex == nil {
		return true
	}
	return strings.TrimSpace(previous.Codex.ThreadID) != strings.TrimSpace(committed.Codex.ThreadID)
}

// recordClaudeSessionHistory is step 2 on the hook ingest path. A failure is
// one ai-ingest.log line; the hook carries on.
func (c *aiCommand) recordClaudeSessionHistory(record sessionhistory.Record, ok bool) {
	if c == nil || !ok {
		return
	}
	stateDir, err := c.aiStateDir()
	if err == nil {
		err = sessionhistory.Append(stateDir, record)
	}
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{
			Source: "session-history", Result: "error",
			Reason:    aiIngestFailureReason(aiIngestReasonSessionHistoryFailed, err),
			SessionID: record.SessionID,
		})
	}
}

// recordIntentAgentSessionHistory is step 2 of a resume-picker create, run
// after the create transaction committed. A failure is one stderr line; the
// create still succeeds.
func (c *createCommand) recordIntentAgentSessionHistory(opened intentAgentOpened, stderr io.Writer) {
	c.recordCreateAgentSessionHistory(opened.sessionHistory, opened.sessionHistoryOK, stderr)
}

// recordCreateAgentSessionHistory runs after a successful Registry commit.
func (c *createCommand) recordCreateAgentSessionHistory(record sessionhistory.Record, ok bool, stderr io.Writer) {
	// A store with no state root (an in-memory Registry) has nowhere the line
	// belongs, exactly as for a deletion record, so nil records nothing.
	if !ok || c == nil || c.store == nil || c.store.stateDir == nil {
		return
	}
	stateDir, err := c.store.stateDir()
	if err == nil {
		err = sessionhistory.Append(stateDir, record)
	}
	if err != nil && stderr != nil {
		_, _ = fmt.Fprintf(stderr, sessionHistoryNotRecordedFmt, "append-failed")
	}
}

// aiStateDir is the projmux state directory the hook ingest path writes its
// own files under, resolved from the same seams as ai-ingest.log.
func (c *aiCommand) aiStateDir() (string, error) {
	if c == nil || c.homeDir == nil {
		return "", errors.New("resolve home directory: not configured")
	}
	homeDir, err := c.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	paths, err := config.Homes{
		HomeDir:    homeDir,
		ConfigHome: c.env("XDG_CONFIG_HOME"),
		StateHome:  c.env("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(paths.StateDir) == "" {
		return "", errors.New("resolve projmux state directory: empty")
	}
	return paths.StateDir, nil
}
