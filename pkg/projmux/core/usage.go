package core

import "time"

type UsageWindow string
type SnapshotSource string
type SnapshotReason string

// UsageSnapshot preserves every saved account usage field.
type UsageSnapshot struct {
	Model  string      `json:"model"`
	Window UsageWindow `json:"window"`
	Bucket string      `json:"bucket,omitempty"`
	Tokens int64       `json:"tokens,omitempty"`
	Limit  int64       `json:"limit,omitempty"`
	Pct    float64     `json:"pct"`
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
type RateLimitMetadata struct {
	BucketKey      string  `json:"bucket_key,omitempty"`
	LimitID        *string `json:"limit_id"`
	Label          *string `json:"label"`
	Slot           string  `json:"slot"`
	CadenceMinutes *int64  `json:"cadence_minutes"`
}
type NamedQuota struct {
	Kind     string           `json:"kind"`
	Group    string           `json:"group"`
	Severity string           `json:"severity"`
	IsActive bool             `json:"is_active"`
	Scope    *NamedQuotaScope `json:"scope"`
}
type NamedQuotaScope struct {
	Model   *NamedQuotaModel `json:"model"`
	Surface *string          `json:"surface"`
}
type NamedQuotaModel struct {
	ID          *string `json:"id"`
	DisplayName string  `json:"display_name"`
}
type BackoffState struct {
	Until       time.Time `json:"until"`
	Consecutive int       `json:"consecutive"`
}
type UsageMetricPoint struct {
	Name       string     `json:"name"`
	Value      float64    `json:"value"`
	ObservedAt time.Time  `json:"observed_at"`
	Provider   string     `json:"provider,omitempty"`
	Window     string     `json:"window,omitempty"`
	Bucket     string     `json:"bucket,omitempty"`
	ResetsAt   *time.Time `json:"resets_at,omitempty"`
}

// UsageHistoryFilter matches the public history CLI filters. Empty fields mean
// all values; Now defaults to the wall clock and controls 30-day retention.
type UsageHistoryFilter struct {
	Provider, Window, Metric string
	Now                      time.Time
}
type UnsupportedUsageProvider struct{ Model, Label, Reason string }

// UsageCache is the stored state visible to the statusbar, scoped to enabled
// usage providers, excluding legacy conversation-context rows. CacheModTime
// is zero for a missing cache. This reader never collects provider usage.
type UsageCache struct {
	Snapshots    []UsageSnapshot
	LastCollect  map[string]time.Time
	Backoff      map[string]BackoffState
	Unsupported  []UnsupportedUsageProvider
	CacheModTime time.Time
}
