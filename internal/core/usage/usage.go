// Package usage models AI token usage tracking for projmux.
//
// Schema v2 — authoritative usage:
//
// Adapters now emit Snapshots directly, sourced from authoritative,
// server-side data (Anthropic OAuth usage API for Claude; native Codex
// account/rateLimits with a newest-rollout fallback). The previous bucketed token
// counting and per-model placeholder limits are gone — adapters return the
// real used_percent and resets_at, which the HUD renders verbatim.
//
// The package exposes:
//
//   - Adapter / Snapshot: a pluggable interface for collecting Snapshots
//     from external sources.
//   - Registry: a process-level registry of named adapters.
//   - Store: a single-file snapshot cache persisted at
//     <StateDir>/usage/snapshots.json. The store REPLACES on Collect; it
//     does not merge.
//   - Manager: orchestrates Collect / MaybeCollect (throttled) / LoadAll.
//
// All adapters remain best-effort: when authoritative data is not reachable
// (no network, no credentials, no rollout file) they return zero snapshots
// and a non-fatal error so the status segment degrades gracefully.
package usage

import "time"

// Window enumerates the rolling windows the aggregator understands.
type Window string

const (
	Window5h     Window = "5h"
	WindowWeekly Window = "weekly"
	// WindowContext is a non-time-bounded window describing how full the
	// active context window is (0-100%). Unlike 5h/weekly it has no reset
	// cadence — it rises and falls with the conversation — so Duration()
	// returns 0 and adapters leave ResetsAt zero. Used by adapters (e.g.
	// Antigravity) that expose a context-fullness metric but no
	// server-side quota contract.
	WindowContext Window = "context"
	// WindowQuota identifies an account-quota bucket whose upstream identity
	// is carried separately in Snapshot.Bucket. Keeping the discriminator
	// separate prevents an upstream bucket named "context", "5h", or
	// "weekly" from colliding with the fixed canonical windows.
	WindowQuota Window = "quota"
)

// Duration is the rolling window length used when describing windows. The
// weekly window's duration is approximate — actual rollover follows the
// vendor's published reset cadence (Anthropic returns an explicit
// resets_at; Codex's rate_limits payload includes resets_at as a unix
// timestamp). WindowContext and opaque WindowQuota buckets have no inferred
// duration and return 0.
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

// Snapshot is the canonical, CLI-facing view of usage for one
// (model, window, bucket) identity. Bucket is empty for fixed/context windows.
// Pct is the authoritative percentage from the upstream
// API. Tokens and Limit are OPTIONAL — they are zero when the adapter is
// percent-only (e.g. Claude OAuth usage API, Codex rate_limits payload).
type Snapshot struct {
	Model  string  `json:"model"`
	Window Window  `json:"window"`
	Bucket string  `json:"bucket,omitempty"`
	Tokens int64   `json:"tokens,omitempty"`
	Limit  int64   `json:"limit,omitempty"`
	Pct    float64 `json:"pct"`
	// ResetInSeconds preserves an upstream relative reset value independently
	// from ResetsAt. A pointer distinguishes an absent value from an explicit
	// zero without deriving one representation from the other.
	ResetInSeconds *int64    `json:"reset_in_seconds,omitempty"`
	ResetsAt       time.Time `json:"resets_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	// NamedQuota preserves the typed upstream identity and scope attached to
	// a named account-quota row. It is nil for canonical 5h/weekly rows and
	// for adapters whose opaque quota source does not expose this metadata.
	NamedQuota *NamedQuota `json:"named_quota,omitempty"`
	// Source is the bounded collector lane that produced this row. It is
	// optional so snapshots written before source-aware adapters remain valid.
	Source SnapshotSource `json:"source,omitempty"`
	// FallbackReason explains why a fresh fallback row was selected instead of
	// the preferred source. StaleReason explains why an older row was retained
	// as last-known-good. Both are closed, content-free values.
	FallbackReason SnapshotReason `json:"fallback_reason,omitempty"`
	StaleReason    SnapshotReason `json:"stale_reason,omitempty"`
	// RateLimit preserves native Codex bucket identity, label, slot, and
	// nullable cadence without changing the established Snapshot fields.
	RateLimit *RateLimitMetadata `json:"rate_limit,omitempty"`
}

// SnapshotSource is the closed usage source vocabulary exposed by the cache,
// JSON CLI, HUD, and diagnostics.
type SnapshotSource string

const (
	SourceAppServer SnapshotSource = "app-server"
	SourceRollout   SnapshotSource = "rollout"
)

// SnapshotReason is a secret-free reason for fallback or last-known-good.
// Values deliberately carry no endpoint, account, credential, prompt, token,
// path, or upstream error text.
type SnapshotReason string

const (
	ReasonAppServerUnavailable  SnapshotReason = "app-server-unavailable"
	ReasonAppServerUnsupported  SnapshotReason = "app-server-unsupported"
	ReasonAccountUnsupported    SnapshotReason = "account-unsupported"
	ReasonAppServerTimeout      SnapshotReason = "app-server-timeout"
	ReasonAppServerProtocol     SnapshotReason = "app-server-protocol-error"
	ReasonAppServerDisconnected SnapshotReason = "app-server-disconnected"
)

// RateLimitMetadata is the lossless native account-rate-limit identity for a
// projected window. BucketKey is the authoritative map key when the response
// used rateLimitsByLimitId. LimitID and Label remain pointers so JSON null is
// distinguishable from an explicitly empty upstream string. CadenceMinutes
// similarly preserves nullable windowDurationMins; unknown cadences remain
// valid quota rows instead of being dropped.
type RateLimitMetadata struct {
	BucketKey      string  `json:"bucket_key,omitempty"`
	LimitID        *string `json:"limit_id"`
	Label          *string `json:"label"`
	Slot           string  `json:"slot"`
	CadenceMinutes *int64  `json:"cadence_minutes"`
}

// NamedQuota is the lossless metadata attached to a typed upstream quota
// row. Group is also copied to Snapshot.Bucket so the established
// (model, window, bucket) identity and sort contract remain intact.
// Strings stay opaque: consumers must not infer model families, aliases,
// cadence, or aggregate counts from them.
type NamedQuota struct {
	Kind     string           `json:"kind"`
	Group    string           `json:"group"`
	Severity string           `json:"severity"`
	IsActive bool             `json:"is_active"`
	Scope    *NamedQuotaScope `json:"scope"`
}

// NamedQuotaScope mirrors the nullable scope object returned by an
// upstream quota row. Nil pointers encode JSON null rather than inventing
// a discriminator when the upstream did not provide one.
type NamedQuotaScope struct {
	Model   *NamedQuotaModel `json:"model"`
	Surface *string          `json:"surface"`
}

// NamedQuotaModel preserves the upstream model identity exactly. ID is
// nullable; DisplayName is the opaque display identity supplied upstream.
type NamedQuotaModel struct {
	ID          *string `json:"id"`
	DisplayName string  `json:"display_name"`
}
