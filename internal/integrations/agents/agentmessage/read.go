package agentmessage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// Archive is one lock-free read of every message the state directory still
// retains: the live store with payloads, then the reclaim log without them.
// A message can appear in both, because the writer appends the log before its
// store commit; a reader that wants each message once keys on MessageRef and
// prefers the Records copy.
type Archive struct {
	Records []Record
	History []HistoryEntry
	// Skipped counts log lines that are not a history record: a line that does
	// not decode, including a torn last line, or one without a messageRef.
	Skipped int
}

// HistoryEntry is the part of a reclaim log line a reader needs. A line with a
// higher schemaVersion is still read for these fields.
type HistoryEntry struct {
	SchemaVersion   int
	MessageRef      string
	ConversationRef string
	ReplyTo         string
	State           coremessage.State
	AcceptedAt      time.Time
	PayloadBytes    int
	Source          coremessage.Route
	Target          coremessage.Route
}

// historyReadOrder is the order the log generations are read in, after the
// store. The current generation goes first: a rotation that lands between the
// two reads renames the generation just read to the retained one, which is then
// read again, so nothing still retained is missed and the repeat is removed by
// MessageRef. The other order would miss the lines that rotation moved.
var historyReadOrder = []string{historyFileName, historyFileName + historyRotatedSuffix}

// readHooks let a test run a writer between the reads. Nil means no hook.
type readHooks struct {
	afterStoreRead   func()
	betweenHistories func()
}

// ReadArchive reads the store under stateDir, then the reclaim log. It takes no
// lock and creates nothing: every file is only opened for reading, and a
// missing store or log generation reads as empty. The store is replaced by
// rename, so one read sees one complete version of it.
//
// A store that cannot be read or does not validate is an error. A log line that
// does not decode is skipped and counted; a log file that cannot be read is an
// error.
func ReadArchive(stateDir string) (Archive, error) {
	return readArchive(stateDir, readHooks{})
}

func readArchive(stateDir string, hooks readHooks) (Archive, error) {
	dir := filepath.Join(stateDir, storeDirName)
	var archive Archive
	storePath := filepath.Join(dir, storeFileName)
	data, err := os.ReadFile(storePath) // #nosec G304 -- private store under the caller's state directory.
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return Archive{}, fmt.Errorf("read agent message store %s: %w", storePath, err)
	default:
		state, err := decodeState(data)
		if err != nil {
			return Archive{}, fmt.Errorf("agent message store %s: %w", storePath, err)
		}
		archive.Records = state.Records
	}
	if hooks.afterStoreRead != nil {
		hooks.afterStoreRead()
	}
	for i, name := range historyReadOrder {
		if i > 0 && hooks.betweenHistories != nil {
			hooks.betweenHistories()
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path) // #nosec G304 -- private log beside the store.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Archive{}, fmt.Errorf("read agent message history %s: %w", path, err)
		}
		entries, skipped := decodeHistory(data)
		archive.History = append(archive.History, entries...)
		archive.Skipped += skipped
	}
	return archive, nil
}

// decodeHistory reads one log generation line by line. Unknown keys are
// ignored, so a newer schema still yields the fields this reader knows.
func decodeHistory(data []byte) ([]HistoryEntry, int) {
	var entries []HistoryEntry
	skipped := 0
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record historyRecord
		if json.Unmarshal(line, &record) != nil || record.MessageRef == "" {
			skipped++
			continue
		}
		entries = append(entries, HistoryEntry{
			SchemaVersion:   record.SchemaVersion,
			MessageRef:      record.MessageRef,
			ConversationRef: record.ConversationRef,
			ReplyTo:         record.ReplyTo,
			State:           record.State,
			AcceptedAt:      record.AcceptedAt,
			PayloadBytes:    record.PayloadBytes,
			Source:          record.Source,
			Target:          record.Target,
		})
	}
	return entries, skipped
}
