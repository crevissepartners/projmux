// Package notify implements a small persistent notification queue used by the
// projmux status bar. Entries are stored until explicit ack or the bounded
// reconcile policy evicts expired-gone or hard-cap overflow rows.
//
// Note: the click handler in the status bar will call
// `projmux focus --target=... --source=os-notification` — but this package
// does NOT call focus itself. It is pure storage.
package notify

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Severity values accepted on push.
const (
	SeverityInfo     = "info"
	SeverityWarn     = "warn"
	SeverityCritical = "critical"
)

// Source values accepted on push.
const (
	SourceAI       = "ai"
	SourceK8s      = "k8s"
	SourceGit      = "git"
	SourceExternal = "external"
)

// Metadata keys shared by the notify produce/consume/reconcile sites.
const (
	MetaAgent    = "agent"
	MetaTopic    = "topic"
	MetaEvent    = "event"
	MetaCategory = "category"
	MetaState    = "state"
)

// DefaultTTL is the default freshness window applied when the caller does not
// supply --ttl. Expiration alone does not remove a pending entry; reconcile
// requires both expiration and a gone target unless the hard cap is exceeded.
const DefaultTTL = 600 * time.Second

// MaxTextLength caps the stored Text on push. Longer text is truncated
// (preserving leading content). v0 keeps no extension field; the truncation
// is destructive.
const MaxTextLength = 80

// Notification is a single queued status-bar entry.
type Notification struct {
	ID        string            `json:"id"`
	Text      string            `json:"text"`
	Severity  string            `json:"severity"`
	Socket    string            `json:"socket,omitempty"`
	Session   string            `json:"session"`
	Window    string            `json:"window,omitempty"`
	Pane      string            `json:"pane,omitempty"`
	Source    string            `json:"source"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// Target groups the routing fields of a notification.
type Target struct {
	Socket  string
	Session string
	Window  string
	Pane    string
}

// PushInput captures the user-provided fields for a push call. CreatedAt is
// supplied by the store at write time so callers can leave it zero.
type PushInput struct {
	ID       string
	Text     string
	Severity string
	Source   string
	Metadata map[string]string
	TTL      time.Duration
	Target   Target
}

// ErrInvalidSeverity indicates a severity value outside the allowed set.
var ErrInvalidSeverity = errors.New("invalid severity")

// ErrInvalidSource indicates a source value outside the allowed set.
var ErrInvalidSource = errors.New("invalid source")

// ErrInvalidTarget indicates the target string could not be parsed or the
// session field was empty.
var ErrInvalidTarget = errors.New("invalid target")

// ErrInvalidText indicates the supplied text was empty after trimming.
var ErrInvalidText = errors.New("invalid text")

// ErrInvalidTTL indicates a non-positive TTL was supplied.
var ErrInvalidTTL = errors.New("invalid ttl")

// ErrNotFound indicates an ack was requested for an unknown id.
var ErrNotFound = errors.New("notification not found")

// ValidateSeverity returns nil if value is one of the accepted severities.
func ValidateSeverity(value string) error {
	switch value {
	case SeverityInfo, SeverityWarn, SeverityCritical:
		return nil
	}
	return fmt.Errorf("%w: %q", ErrInvalidSeverity, value)
}

// ValidateSource returns nil if value is one of the accepted sources.
func ValidateSource(value string) error {
	switch value {
	case SourceAI, SourceK8s, SourceGit, SourceExternal:
		return nil
	}
	return fmt.Errorf("%w: %q", ErrInvalidSource, value)
}

// ParseTarget parses a SESSION[:WINDOW[.PANE]] string. The SESSION segment is
// required and must be non-empty after trimming.
func ParseTarget(value string) (Target, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Target{}, fmt.Errorf("%w: empty target", ErrInvalidTarget)
	}

	var t Target

	rest := value
	hasSep := false
	if idx := strings.Index(rest, ":"); idx >= 0 {
		hasSep = true
		t.Session = rest[:idx]
		rest = rest[idx+1:]
	} else {
		t.Session = rest
		rest = ""
	}

	hasDot := false
	if hasSep {
		if before, after, ok := strings.Cut(rest, "."); ok {
			hasDot = true
			t.Window = before
			t.Pane = after
		} else {
			t.Window = rest
		}
	}

	if strings.TrimSpace(t.Session) == "" {
		return Target{}, fmt.Errorf("%w: missing session", ErrInvalidTarget)
	}
	if hasSep && strings.TrimSpace(t.Window) == "" {
		return Target{}, fmt.Errorf("%w: empty window", ErrInvalidTarget)
	}
	if hasDot && strings.TrimSpace(t.Pane) == "" {
		return Target{}, fmt.Errorf("%w: empty pane", ErrInvalidTarget)
	}

	return t, nil
}

// FormatTarget renders a Target as SESSION[:WINDOW[.PANE]].
func FormatTarget(t Target) string {
	if t.Session == "" {
		return ""
	}
	out := t.Session
	if t.Window != "" {
		out += ":" + t.Window
		if t.Pane != "" {
			out += "." + t.Pane
		}
	}
	return out
}

// truncateText trims runes beyond MaxTextLength. The slice is treated as
// runes so multi-byte characters are not split.
func truncateText(s string) string {
	runes := []rune(s)
	if len(runes) <= MaxTextLength {
		return s
	}
	return string(runes[:MaxTextLength])
}
