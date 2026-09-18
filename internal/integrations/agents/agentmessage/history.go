package agentmessage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	historyFileName = "history.jsonl"
	// historyRotatedSuffix names the single retained previous generation. Two
	// generations is the whole retention policy: the log has no reader yet, so
	// an unbounded archive would only grow the private state directory.
	historyRotatedSuffix = ".1"
	// historySchemaVersion follows the coordination frame rule: an absent or
	// zero value reads as 1, and a reader that meets a higher version reads the
	// fields it knows instead of discarding the line. It is independent of the
	// durable envelope coremessage.Version and of storeVersion.
	historySchemaVersion = 1
	maxHistoryBytes      = 8 << 20

	// reclaimRetention marks a record reclaimed because it went terminal more
	// than terminalRetention ago; reclaimCapacity marks one pushed out by
	// maxRecords.
	reclaimRetention = "retention"
	reclaimCapacity  = "capacity"
)

// reclaimedRecord is one record pruneRecords removed from the store, with the
// rule that removed it. Reclaiming is not deleting: the caller hands these to
// writeLocked, which makes the log durable before the store commits.
type reclaimedRecord struct {
	Record Record
	Reason string
}

// historyRecord is one line of history.jsonl. Keys reuse the envelope's own
// names, and payload is deliberately absent: the declared consumer needs the
// edges (source, target, conversationRef, replyTo), not the free text, which is
// the one part of an unbounded log a user wrote by hand.
type historyRecord struct {
	SchemaVersion   int               `json:"schemaVersion"`
	EvictedAt       time.Time         `json:"evictedAt"`
	Reason          string            `json:"reason"`
	Adapter         string            `json:"adapter"`
	MessageRef      string            `json:"messageRef"`
	ConversationRef string            `json:"conversationRef"`
	ReplyTo         string            `json:"replyTo,omitempty"`
	State           coremessage.State `json:"state"`
	DeliveryReason  string            `json:"deliveryReason"`
	OutcomeUnknown  bool              `json:"outcomeUnknown"`
	HandoffObserved bool              `json:"handoffObserved"`
	AcceptedAt      time.Time         `json:"acceptedAt"`
	Deadline        time.Time         `json:"deadline"`
	TerminalAt      time.Time         `json:"terminalAt"`
	PayloadBytes    int               `json:"payloadBytes"`
	// Origin and Source follow the envelope: an Agent line has a source and
	// no origin, byte for byte as before; an operator line has an origin and
	// no source.
	Origin coremessage.Origin `json:"origin,omitzero"`
	Source coremessage.Route  `json:"source,omitzero"`
	Target coremessage.Route  `json:"target"`
}

func newHistoryRecords(reclaimed []reclaimedRecord, evictedAt time.Time) []historyRecord {
	if len(reclaimed) == 0 {
		return nil
	}
	out := make([]historyRecord, 0, len(reclaimed))
	for _, item := range reclaimed {
		out = append(out, historyRecord{
			SchemaVersion:   historySchemaVersion,
			EvictedAt:       evictedAt.UTC(),
			Reason:          item.Reason,
			Adapter:         item.Record.Adapter,
			MessageRef:      item.Record.Envelope.MessageRef,
			ConversationRef: item.Record.Envelope.ConversationRef,
			ReplyTo:         item.Record.Envelope.ReplyTo,
			State:           item.Record.Delivery.State,
			DeliveryReason:  item.Record.Delivery.Reason,
			OutcomeUnknown:  item.Record.Delivery.OutcomeUnknown,
			HandoffObserved: item.Record.HandoffObserved,
			AcceptedAt:      item.Record.Envelope.AcceptedAt,
			Deadline:        item.Record.Envelope.Deadline,
			TerminalAt:      item.Record.Delivery.TerminalAt,
			PayloadBytes:    len(item.Record.Envelope.Payload),
			Origin:          item.Record.Envelope.Origin,
			Source:          item.Record.Envelope.Source,
			Target:          item.Record.Envelope.Target,
		})
	}
	return out
}

func (s *Store) historyPath() string {
	return filepath.Join(filepath.Dir(s.path), historyFileName)
}

func (s *Store) historyLimit() int {
	if s.historyMaxBytes > 0 {
		return s.historyMaxBytes
	}
	return maxHistoryBytes
}

// appendHistoryLocked writes the reclaimed lines and fsyncs them. It runs under
// the store flock and before the store's own commit, so a crash can leave a
// record in both places but never in neither.
func (s *Store) appendHistoryLocked(records []historyRecord) error {
	if len(records) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if s.hooks.beforeHistoryAppend != nil {
		if err := s.hooks.beforeHistoryAppend(); err != nil {
			return err
		}
	}
	path := s.historyPath()
	if err := rotateHistory(path, buf.Len(), s.historyLimit()); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, localstate.PrivateFileMode) // #nosec G304 -- private store sibling.
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if _, err := file.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	localstate.RepairPrivateFile(path)
	return syncDir(filepath.Dir(path))
}

// rotateHistory keeps the active log under the limit by moving it to the single
// retained generation. os.Rename replaces an older generation, so the log never
// grows past two files.
func rotateHistory(path string, incoming, limit int) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size()+int64(incoming) <= int64(limit) {
		return nil
	}
	return os.Rename(path, path+historyRotatedSuffix)
}
