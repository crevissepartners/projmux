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

// The Claude session history write.
//
// The Registry keeps one conversation per Agent in `status.sessionRef` and
// overwrites it when the Agent moves on. Every committed write that moves a
// Claude Agent to a different conversation also appends one line to
// <state>/agent-session-history.jsonl, so the conversations it left stay
// listable (`agent sessions list`).
//
// The writers of that ref are a closed set -- the callers of
// RecordAgentSessionRef -- and each follows the same two steps:
//
//  1. inside the Registry transaction, claudeSessionHistoryRecord turns the
//     mutator's own changed verdict into the line to append, or none;
//  2. after the transaction commits, an append helper writes it.
//
// TestClaudeSessionRefWritersRecordHistory pins that set, so a new caller of
// RecordAgentSessionRef cannot land without the history.
//
// The append never fails its caller. The Registry commit and the append are
// not atomic: a crash between them loses that one line, which is the
// documented cost of keeping the history out of the Registry.

// sessionHistoryNotRecordedFmt is the one stderr line a create prints when
// its history line could not be written.
const sessionHistoryNotRecordedFmt = "agent session history not recorded: %s\n"

// claudeSessionHistoryRecord is step 1. changed is RecordAgentSessionRef's
// verdict, so "a different conversation" means exactly what the Registry
// write means by it; committed is the ref the mutator stored. Only a Claude
// ref yields a line.
func claudeSessionHistoryRecord(agentUID string, changed bool, committed *coremetadata.AgentSessionRef) (sessionhistory.Record, bool) {
	if !changed {
		return sessionhistory.Record{}, false
	}
	return sessionhistory.RecordFor(agentUID, committed, sessionhistory.SourceObserved)
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
	// A store with no state root (an in-memory Registry) has nowhere the line
	// belongs, exactly as for a deletion record, so nil records nothing.
	if !opened.sessionHistoryOK || c == nil || c.store == nil || c.store.stateDir == nil {
		return
	}
	stateDir, err := c.store.stateDir()
	if err == nil {
		err = sessionhistory.Append(stateDir, opened.sessionHistory)
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
