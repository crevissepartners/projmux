// Package usage models AI token usage tracking for projmux.
//
// The package exposes:
//
//   - Adapter / TokenEvent: a pluggable interface for collecting raw token
//     events from external sources (CLI logs, API endpoints, etc).
//   - Registry: a process-level registry of named adapters.
//   - Store: a per-model 1-minute-bucketed cache persisted under
//     <StateDir>/usage/<model>.json. Entries older than the weekly retention
//     window are trimmed on append.
//   - Aggregator: window math (5h rolling, weekly Mon 00:00 local rollover).
//   - Limits: hardcoded placeholder limits with optional JSON override loaded
//     from PROJMUX_USAGE_LIMITS_PATH.
//
// All adapters today are best-effort v0 stubs — if the data is not reachable
// they return zero events (not an error) so the rest of the pipeline (and the
// status-bar segment) degrades gracefully. See the per-adapter package
// comment for the TODO that marks the real-data follow-up.
package usage

import "time"

// Window enumerates the rolling windows the aggregator understands.
type Window string

const (
	Window5h     Window = "5h"
	WindowWeekly Window = "weekly"
)

// AllWindows returns the canonical ordered window list used by the CLI.
func AllWindows() []Window {
	return []Window{Window5h, WindowWeekly}
}

// Duration is the rolling window length used when aggregating events. The
// weekly window's "duration" is approximate — actual rollover follows the
// Monday-00:00-local boundary in NextResetAt.
func (w Window) Duration() time.Duration {
	switch w {
	case Window5h:
		return 5 * time.Hour
	case WindowWeekly:
		return 7 * 24 * time.Hour
	default:
		return 0
	}
}

// TokenEvent is a single point-in-time count of consumed tokens reported by
// an adapter. Adapters emit raw events; aggregation is the Store's job.
type TokenEvent struct {
	At     time.Time
	Tokens int64
}

// Snapshot is the aggregated, CLI-facing view of usage for one (model,
// window) pair.
type Snapshot struct {
	Model     string    `json:"model"`
	Window    Window    `json:"window"`
	Tokens    int64     `json:"tokens"`
	Limit     int64     `json:"limit"`
	Pct       float64   `json:"pct"`
	ResetsAt  time.Time `json:"resets_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
