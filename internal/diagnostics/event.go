// Package diagnostics provides the private, bounded operational event journal.
// Its schema is deliberately closed: callers cannot attach arbitrary metadata.
package diagnostics

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxMessageRunes = 512

// Event is the complete on-disk operational event schema. It intentionally has
// no argv, environment, routing, payload, or generic metadata field.
type Event struct {
	At         string `json:"at"`
	Level      string `json:"level"`
	Component  string `json:"component"`
	Event      string `json:"event"`
	Result     string `json:"result"`
	DurationMS int64  `json:"duration_ms"`
	RunID      string `json:"run_id"`
	Version    string `json:"version"`
	MuxBackend string `json:"mux_backend"`
	Command    string `json:"command,omitempty"`
	Subcommand string `json:"subcommand,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Message    string `json:"message,omitempty"`
	Operation  string `json:"operation,omitempty"`
	Code       string `json:"code,omitempty"`
}

// NewRunID creates one opaque correlation ID for a process invocation.
func NewRunID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), os.Getpid())
}

// MuxBackend returns the supported backend enum for operational events.
func MuxBackend() string {
	return "tmux"
}

// SanitizeMessage removes terminal controls, abbreviates the user's home
// directory, and bounds the result. Error kind remains a separate field.
func SanitizeMessage(message, home string) string {
	if home != "" {
		home = strings.TrimRight(home, `/\`)
		if home != "" {
			message = strings.ReplaceAll(message, home, "~")
		}
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.ToValidUTF8(message, "�") {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		if unicode.IsSpace(r) {
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	clean := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(clean) <= maxMessageRunes {
		return clean
	}
	runes := []rune(clean)
	return string(runes[:maxMessageRunes-1]) + "…"
}

var (
	allowedLevels     = stringSet("info", "error")
	allowedComponents = stringSet("cli", "runtime", "session-state", "notify", "focus", "ai", "resource")
	allowedEvents     = stringSet("command.outcome", "lifecycle.start", "lifecycle.outcome")
	allowedResults    = stringSet("started", "success", "error")
	allowedKinds      = stringSet("usage", "exit", "runtime")
	allowedBackends   = stringSet("tmux")
	allowedOperations = stringSet(
		string(OperationSessionCreate),
		string(OperationSessionAttach),
		string(OperationSessionSwitch),
		string(OperationSessionKill),
		string(OperationTmuxApply),
	)
	allowedCodes = stringSet(
		string(CodeSessionCreateFailed),
		string(CodeSessionAttachFailed),
		string(CodeSessionSwitchFailed),
		string(CodeSessionKillFailed),
		string(CodeTmuxApplyFailed),
		string(CodeTmuxApplySocketUnreachable),
		string(CodeTmuxApplyReloadFailed),
		string(CodeTmuxApplyReloadSkipped),
	)
)

func sanitizeEvent(in Event, home string) (Event, error) {
	out := in
	if _, ok := allowedLevels[out.Level]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics level")
	}
	if _, ok := allowedComponents[out.Component]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics component")
	}
	if _, ok := allowedEvents[out.Event]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics event")
	}
	if _, ok := allowedResults[out.Result]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics result")
	}
	if err := validateEventShape(out); err != nil {
		return Event{}, err
	}
	if _, ok := allowedBackends[out.MuxBackend]; !ok {
		return Event{}, fmt.Errorf("invalid diagnostics mux backend")
	}
	if out.Kind != "" {
		if _, ok := allowedKinds[out.Kind]; !ok {
			return Event{}, fmt.Errorf("invalid diagnostics error kind")
		}
	}
	if out.At == "" {
		return Event{}, fmt.Errorf("missing diagnostics timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, out.At); err != nil {
		return Event{}, fmt.Errorf("invalid diagnostics timestamp")
	}
	if out.RunID == "" || len(out.RunID) > 96 || !safeIdentifier(out.RunID) {
		return Event{}, fmt.Errorf("invalid diagnostics run id")
	}
	if out.DurationMS < 0 {
		out.DurationMS = 0
	}
	if len(out.Version) > 64 || !safeVersion(out.Version) {
		return Event{}, fmt.Errorf("invalid diagnostics version")
	}
	class := Classify([]string{out.Command, out.Subcommand})
	if out.Command != "" && class.Command != out.Command {
		return Event{}, fmt.Errorf("unsafe diagnostics command")
	}
	if out.Subcommand != "" && class.Subcommand != out.Subcommand {
		return Event{}, fmt.Errorf("unsafe diagnostics subcommand")
	}
	out.Message = SanitizeMessage(out.Message, home)
	return out, nil
}

func validateEventShape(event Event) error {
	switch event.Event {
	case "command.outcome":
		if event.Result == "started" || event.Operation != "" || event.Code != "" {
			return fmt.Errorf("invalid command outcome shape")
		}
	case "lifecycle.start":
		if event.Result != "started" || event.Level != "info" || event.Operation == "" || event.Code != "" || event.Kind != "" || event.Message != "" || event.Command != "" || event.Subcommand != "" {
			return fmt.Errorf("invalid lifecycle start shape")
		}
	case "lifecycle.outcome":
		if event.Result == "started" || event.Operation == "" || event.Command != "" || event.Subcommand != "" || event.Message != "" {
			return fmt.Errorf("invalid lifecycle outcome shape")
		}
		if event.Result == "error" && (event.Level != "error" || event.Kind != "runtime" || event.Code == "") {
			return fmt.Errorf("invalid lifecycle error shape")
		}
		if event.Result == "success" && (event.Level != "info" || event.Kind != "") {
			return fmt.Errorf("invalid lifecycle success shape")
		}
		if event.Result == "success" && event.Code != "" && event.Code != string(CodeTmuxApplyReloadSkipped) {
			return fmt.Errorf("invalid lifecycle success code")
		}
		if event.Result == "error" && event.Code == string(CodeTmuxApplyReloadSkipped) {
			return fmt.Errorf("invalid lifecycle error code")
		}
		if !operationAcceptsCode(Operation(event.Operation), Code(event.Code)) {
			return fmt.Errorf("invalid lifecycle operation code")
		}
	}
	if event.Operation != "" {
		if _, ok := allowedOperations[event.Operation]; !ok {
			return fmt.Errorf("invalid diagnostics operation")
		}
	}
	if event.Code != "" {
		if _, ok := allowedCodes[event.Code]; !ok {
			return fmt.Errorf("invalid diagnostics code")
		}
	}
	return nil
}

func operationAcceptsCode(operation Operation, code Code) bool {
	if code == "" {
		return true
	}
	switch operation {
	case OperationSessionCreate:
		return code == CodeSessionCreateFailed
	case OperationSessionAttach:
		return code == CodeSessionAttachFailed
	case OperationSessionSwitch:
		return code == CodeSessionSwitchFailed
	case OperationSessionKill:
		return code == CodeSessionKillFailed
	case OperationTmuxApply:
		return code == CodeTmuxApplyFailed || code == CodeTmuxApplySocketUnreachable || code == CodeTmuxApplyReloadFailed || code == CodeTmuxApplyReloadSkipped
	default:
		return false
	}
}

// ValidLevel reports whether value is in the stable severity allowlist.
func ValidLevel(value string) bool {
	_, ok := allowedLevels[value]
	return ok
}

func safeIdentifier(value string) bool {
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func safeVersion(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func stringSet(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}
