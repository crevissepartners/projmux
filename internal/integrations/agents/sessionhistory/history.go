// Package sessionhistory keeps the append-only record of which provider
// conversations a Claude Agent has moved through.
//
// The Registry holds one conversation per Agent: `status.sessionRef` is
// overwritten when the Agent moves to another conversation (a `/clear`, a
// resume of another session). This package is the memory of the refs it
// replaced. It lives outside the Registry on purpose: the Registry schema and
// its version stay exactly as they are, and a history that only grows has no
// business inside a file every transaction rewrites.
//
// The file is one JSON object per line. Writers only ever append a complete
// line; readers skip a line they cannot parse and count it. Nothing here reads
// a provider transcript: the transcript path is the one the provider hook
// reported, recorded as a path.
package sessionhistory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// FileName is the history file inside the projmux state directory, next to
// the Registry directory, `termination-receipts.jsonl`, and
// `deletion-records.jsonl`.
const FileName = "agent-session-history.jsonl"

// ProviderClaude is the only provider this history records. Codex and
// Antigravity conversation changes are not recorded.
const ProviderClaude = "claude"

// Source says where one row came from.
type Source string

const (
	// SourceObserved is a line a projmux writer appended when it committed a
	// Claude Agent's move to a different conversation.
	SourceObserved Source = "observed"
	// SourceCurrent is added by the read side for the conversation the
	// Registry currently records for the Agent. It is never written to the
	// file.
	SourceCurrent Source = "current"
	// SourceEstimated is reserved for rows reconstructed after the fact from
	// transcripts that predate this history. Nothing produces it yet.
	SourceEstimated Source = "estimated"
)

// Record is one row. The JSON keys are a stable contract: the file stores
// them and `agent sessions list -o json` prints them.
type Record struct {
	AgentUID       string    `json:"agentUID"`
	Provider       string    `json:"provider"`
	SessionID      string    `json:"sessionId"`
	TranscriptPath string    `json:"transcriptPath"`
	ObservedAt     time.Time `json:"observedAt"`
	Source         Source    `json:"source"`
}

// Path is the history file of one state directory.
func Path(stateDir string) string {
	return filepath.Join(stateDir, FileName)
}

// RecordFor projects a Claude session ref onto a row of the given source. It
// reports false for a nil ref, another provider, or a ref with no session id,
// which is how the Claude-only scope is enforced at both ends.
func RecordFor(agentUID string, ref *coremetadata.AgentSessionRef, source Source) (Record, bool) {
	agentUID = strings.TrimSpace(agentUID)
	if agentUID == "" || ref == nil || ref.Provider != ProviderClaude || ref.Claude == nil {
		return Record{}, false
	}
	sessionID := strings.TrimSpace(ref.Claude.SessionID)
	if sessionID == "" {
		return Record{}, false
	}
	return Record{
		AgentUID:       agentUID,
		Provider:       ProviderClaude,
		SessionID:      sessionID,
		TranscriptPath: ref.Claude.TranscriptPath,
		ObservedAt:     ref.ObservedAt.UTC(),
		Source:         source,
	}, true
}

// Append adds one complete line to the history file of stateDir.
//
// The line is framed with a leading and a trailing newline and written with
// one O_APPEND write, like the termination journal: the leading delimiter
// seals a partial tail another process left behind, so a torn write costs at
// most its own line. The write also holds an exclusive flock on the file, so
// concurrent appenders cannot interleave even where a single write is not
// atomic. The directory and file are created private.
func Append(stateDir string, record Record) error {
	if strings.TrimSpace(stateDir) == "" {
		return errors.New("agent session history: no state directory")
	}
	if record.Provider != ProviderClaude || strings.TrimSpace(record.AgentUID) == "" || strings.TrimSpace(record.SessionID) == "" {
		return fmt.Errorf("agent session history: refusing an incomplete record for agent %q", record.AgentUID)
	}
	record.ObservedAt = record.ObservedAt.UTC()
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("agent session history: marshal record: %w", err)
	}
	framed := make([]byte, 0, len(body)+2)
	framed = append(framed, '\n')
	framed = append(framed, body...)
	framed = append(framed, '\n')
	path := Path(stateDir)
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("agent session history: create state dir: %w", err)
	}
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, localstate.PrivateFileMode)
	if err != nil {
		return fmt.Errorf("agent session history: open: %w", err)
	}
	defer file.Close()
	if err := lockAppend(file); err != nil {
		return fmt.Errorf("agent session history: lock: %w", err)
	}
	if _, err := file.Write(framed); err != nil {
		return fmt.Errorf("agent session history: append: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("agent session history: sync: %w", err)
	}
	return nil
}

// lockWait bounds how long an appender queues behind another one. Holders
// keep the lock for one small write and its fsync.
const (
	lockWait          = time.Second
	lockRetryInterval = 2 * time.Millisecond
)

// lockAppend takes the exclusive advisory lock of the open history file. It is
// released when the file is closed.
func lockAppend(file *os.File) error {
	fd := int(file.Fd())
	deadline := time.Now().Add(lockWait)
	err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	for errors.Is(err, unix.EWOULDBLOCK) && time.Now().Before(deadline) {
		time.Sleep(lockRetryInterval)
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	}
	return err
}

// ReadResult is the parsed history file.
type ReadResult struct {
	// Records are the usable rows, in file order.
	Records []Record
	// Corrupt counts non-empty lines that were skipped: lines that are not
	// JSON, and JSON rows missing the agent uid or the session id. It is
	// counted over the whole file, not per Agent.
	Corrupt int
}

// Read parses the history file of stateDir and keeps the rows of agentUID, or
// every row when agentUID is empty. A missing file is an empty history.
func Read(stateDir, agentUID string) (ReadResult, error) {
	var result ReadResult
	if strings.TrimSpace(stateDir) == "" {
		return result, nil
	}
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.Open(Path(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, fmt.Errorf("agent session history: open: %w", err)
	}
	defer file.Close()
	agentUID = strings.TrimSpace(agentUID)
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			var record Record
			if err := json.Unmarshal([]byte(trimmed), &record); err != nil ||
				strings.TrimSpace(record.AgentUID) == "" || strings.TrimSpace(record.SessionID) == "" {
				result.Corrupt++
			} else if agentUID == "" || record.AgentUID == agentUID {
				record.ObservedAt = record.ObservedAt.UTC()
				result.Records = append(result.Records, record)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return result, fmt.Errorf("agent session history: read: %w", readErr)
		}
	}
	return result, nil
}

// Merge joins history rows with the Registry's current ref into one row per
// (agentUID, sessionId), in time order.
//
// The rule for a conversation seen more than once:
//
//   - observedAt is the latest observation of it, so a conversation the Agent
//     returned to sorts where it was last entered;
//   - transcriptPath is the one of that latest observation that has one;
//   - source is `current` when current names it, and otherwise the source of
//     the rows it came from.
//
// Rows are ordered by observedAt; equal instants keep their input order, with
// current last. current may be nil.
func Merge(history []Record, current *Record) []Record {
	type key struct{ agentUID, sessionID string }
	rows := make([]Record, 0, len(history)+1)
	index := map[key]int{}
	add := func(record Record) {
		k := key{record.AgentUID, record.SessionID}
		at, seen := index[k]
		if !seen {
			index[k] = len(rows)
			rows = append(rows, record)
			return
		}
		merged := rows[at]
		if !record.ObservedAt.Before(merged.ObservedAt) {
			merged.ObservedAt = record.ObservedAt
			if record.TranscriptPath != "" {
				merged.TranscriptPath = record.TranscriptPath
			}
		} else if merged.TranscriptPath == "" {
			merged.TranscriptPath = record.TranscriptPath
		}
		if record.Source == SourceCurrent {
			merged.Source = SourceCurrent
		}
		rows[at] = merged
	}
	for _, record := range history {
		add(record)
	}
	if current != nil {
		row := *current
		row.Source = SourceCurrent
		add(row)
	}
	slices.SortStableFunc(rows, func(a, b Record) int { return a.ObservedAt.Compare(b.ObservedAt) })
	return rows
}

// Result is the session list of one Agent.
type Result struct {
	AgentUID string   `json:"agentUID"`
	Sessions []Record `json:"sessions"`
	// CorruptLines is ReadResult.Corrupt of the file that was read.
	CorruptLines int `json:"corruptLines"`
}

// List is the one read of an Agent's sessions: the history file of stateDir
// joined with the conversation agent's `status.sessionRef` currently records,
// through Merge. `projmux agent sessions list` prints exactly these rows. A
// non-Claude Agent has no history and no current row.
func List(stateDir string, agent coremetadata.Agent) (Result, error) {
	result := Result{AgentUID: agent.Metadata.UID, Sessions: []Record{}}
	read, err := Read(stateDir, agent.Metadata.UID)
	if err != nil {
		return result, err
	}
	result.CorruptLines = read.Corrupt
	var current *Record
	if row, ok := RecordFor(agent.Metadata.UID, agent.Status.SessionRef, SourceCurrent); ok {
		current = &row
	}
	var history []Record
	for _, record := range read.Records {
		if record.Provider == ProviderClaude {
			history = append(history, record)
		}
	}
	result.Sessions = append(result.Sessions, Merge(history, current)...)
	return result, nil
}
