package agentapproval

import (
	"encoding/json"
	"errors"
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

// auditLocked appends one line while the caller holds the store lock. It is
// best effort: the transition it describes has already been written, so a log
// that cannot be appended never undoes or fails it.
func (s *Store) auditLocked(line AuditLine) {
	if s == nil || s.auditPath == "" {
		return
	}
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	data = append(data, '\n')
	limit := s.auditLimit
	if limit <= 0 {
		limit = auditMaxBytes
	}
	if info, err := os.Stat(s.auditPath); err == nil && info.Size()+int64(len(data)) > limit {
		if os.Rename(s.auditPath, s.auditPath+auditRotatedSuffix) != nil {
			return
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	file, err := os.OpenFile(s.auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, localstate.PrivateFileMode) // #nosec G304 -- private store sibling.
	if err != nil {
		return
	}
	_, _ = file.Write(data)
	_ = file.Sync()
	_ = file.Close()
	localstate.RepairPrivateFile(s.auditPath)
	_ = syncDir(filepath.Dir(s.auditPath))
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
