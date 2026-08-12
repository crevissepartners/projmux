// Package diagnostics provides the private, bounded operational event journal.
// Its schema is deliberately closed: callers cannot attach arbitrary metadata.
package diagnostics

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
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
}

// NewRunID creates one opaque correlation ID for a process invocation.
func NewRunID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), os.Getpid())
}

// MuxBackend returns only the supported backend enum. Arbitrary environment
// values are ignored rather than copied into an event.
func MuxBackend(lookupEnv func(string) string, goos string) string {
	if lookupEnv != nil {
		switch strings.ToLower(strings.TrimSpace(lookupEnv("PROJMUX_MUX_BACKEND"))) {
		case "tmux":
			return "tmux"
		case "psmux":
			return "psmux"
		}
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if strings.EqualFold(goos, "windows") {
		return "psmux"
	}
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
	allowedEvents     = stringSet("command.outcome")
	allowedResults    = stringSet("success", "error")
	allowedKinds      = stringSet("usage", "exit", "runtime")
	allowedBackends   = stringSet("tmux", "psmux")
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
