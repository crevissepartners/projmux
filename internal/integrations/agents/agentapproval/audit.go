package agentapproval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	auditFileName = "audit.jsonl"
	// auditRotatedSuffix names the single retained older generation.
	auditRotatedSuffix = ".1"
	// auditMaxBytes rotates the active log before a line would take it past
	// this size, so the log never holds more than two generations of it.
	auditMaxBytes = 1 << 20
	// MaxInputSummaryRunes bounds the one-line input summary of an audit line.
	MaxInputSummaryRunes = 200
)

// Audit events, one line each.
const (
	AuditRequested = "requested"
	AuditAllowed   = "allowed"
	AuditDenied    = "denied"
	AuditExpired   = "expired"
	AuditClosed    = "closed"
	// AuditRefused is an answer that changed nothing: a late or second
	// answer, or one naming a request that does not exist.
	AuditRefused = "refused"
	// AuditUncommitted follows the allowed or denied line of the same request
	// id when that answer is confirmed not to have taken effect. It is written
	// only on that confirmation: an answer whose outcome is unknown keeps its
	// line alone, and one that took effect never gets this line.
	AuditUncommitted = "uncommitted"
)

// Reasons an uncommitted line carries.
const (
	// UncommittedRecordWriteFailed: the allowed or denied line of a Claude
	// answer was written, and the record write then failed before it
	// committed, so the request is still waiting.
	UncommittedRecordWriteFailed = "record-write-failed"
	// UncommittedSendFailed: the allowed or denied line of a Codex answer was
	// written, and the decision was then confirmed not to have reached Codex.
	UncommittedSendFailed = "send-failed"
)

// AuditLine is one line of the append-only audit log. It never carries the
// full tool input: Input is the bounded summary InputSummary spells.
type AuditLine struct {
	Event     string `json:"event"`
	RequestID string `json:"requestID"`
	AgentUID  string `json:"agentUID,omitempty"`
	PaneUID   string `json:"paneUID,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	// AgentType is set when a subagent raised the request.
	AgentType   string    `json:"agentType,omitempty"`
	ToolName    string    `json:"toolName,omitempty"`
	Input       string    `json:"input,omitempty"`
	RequestedAt time.Time `json:"requestedAt,omitzero"`
	// DecidedAt is when an allowed, denied, expired, or closed record settled.
	DecidedAt time.Time `json:"decidedAt,omitzero"`
	// Via is the answer's self-reported channel; it is never verified.
	Via    string    `json:"via,omitempty"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// auditLine spells event for record at now.
func auditLine(event string, record Record, now time.Time) AuditLine {
	line := AuditLine{
		Event: event, RequestID: record.ID, AgentUID: record.AgentUID, PaneUID: record.PaneUID,
		SessionID: record.SessionID, AgentType: boundedLine(record.AgentType, 64), ToolName: boundedLine(record.ToolName, 128),
		Input: InputSummary(record.ToolName, record.ToolInput), RequestedAt: record.CreatedAt.UTC(), Via: record.Via, At: now.UTC(),
	}
	if event != AuditRequested && event != AuditRefused {
		line.DecidedAt = record.UpdatedAt.UTC()
	}
	return line
}

// uncommittedLine is the uncommitted line that follows answer: the same
// request, Agent, Pane, session, tool, input, and via, with reason and the
// time at. DecidedAt stays zero because nothing was decided.
func uncommittedLine(answer AuditLine, reason string, at time.Time) AuditLine {
	line := answer
	line.Event = AuditUncommitted
	line.Reason = reason
	line.DecidedAt = time.Time{}
	line.At = at.UTC()
	return line
}

// auditLocked appends one line while the caller holds the store lock. It is
// best effort: the requested, expired, closed, and refused lines describe a
// transition that already happened or none at all, so a log that cannot be
// appended never undoes or fails it. Allowed and denied lines go through
// appendAuditLocked before the answer is written instead.
func (s *Store) auditLocked(line AuditLine) {
	_ = s.appendAuditLocked(line)
}

// appendAuditLocked appends one line while the caller holds the store lock and
// reports every failure: a line that is not on disk, synced, returns an error.
func (s *Store) appendAuditLocked(line AuditLine) error {
	if s == nil || s.auditPath == "" {
		return errors.New("agent approval audit log path is empty")
	}
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	limit := s.auditLimit
	if limit <= 0 {
		limit = auditMaxBytes
	}
	info, err := os.Stat(s.auditPath)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", s.auditPath)
	case err == nil && info.Size()+int64(len(data)) > limit:
		if err := os.Rename(s.auditPath, s.auditPath+auditRotatedSuffix); err != nil {
			return err
		}
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	file, err := os.OpenFile(s.auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, localstate.PrivateFileMode) // #nosec G304 -- private store sibling.
	if err != nil {
		return err
	}
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	localstate.RepairPrivateFile(s.auditPath)
	return syncDir(filepath.Dir(s.auditPath))
}

// InputSummary is the one line an audit entry shows of a tool input: Bash's
// command, a file tool's file path, and otherwise only the input's key names,
// never their values. It is at most MaxInputSummaryRunes runes with every
// control character replaced by a space.
func InputSummary(toolName string, raw json.RawMessage) string {
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	field := ""
	switch toolName {
	case "Bash":
		field = "command"
	case "Read", "Write", "Edit", "MultiEdit":
		field = "file_path"
	case "NotebookEdit":
		field = "notebook_path"
	}
	if field != "" {
		var value string
		if json.Unmarshal(input[field], &value) == nil && value != "" {
			return boundedLine(value, MaxInputSummaryRunes)
		}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return boundedLine("keys: "+strings.Join(keys, ","), MaxInputSummaryRunes)
}

// boundedLine folds text onto one line and cuts it to limit runes, marking a
// cut with "…".
func boundedLine(text string, limit int) string {
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "?")
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit-1]) + "…"
}
