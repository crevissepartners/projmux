package codexgeneration

import "github.com/crevissepartners/projmux/internal/core/metadata"

type AuthorityDecision string

const (
	AuthorityAllowed               AuthorityDecision = "allowed"
	AuthorityLegacyUnavailable     AuthorityDecision = "legacy-generation-unavailable"
	AuthorityEndpointMismatch      AuthorityDecision = "endpoint-mismatch"
	AuthorityBrokerRuntimeMismatch AuthorityDecision = "broker-runtime-mismatch"
	AuthorityConnectionStale       AuthorityDecision = "connection-epoch-stale"
	AuthorityBindingStale          AuthorityDecision = "binding-epoch-stale"
)

// DecideAuthority is the only Phase 0 comparison for a live Codex authority.
// Legacy fields do not inherit the current endpoint. Ordering is deliberate so
// diagnostics identify the first namespace that differs without exposing
// provider content.
func DecideAuthority(durable *metadata.CodexEndpointRef, stored, presented *metadata.CodexAuthorityRef) AuthorityDecision {
	if durable == nil || stored == nil || presented == nil || !durable.Valid() || !stored.Valid() || !presented.Valid() {
		return AuthorityLegacyUnavailable
	}
	if !durable.Same(stored.Endpoint()) || !durable.Same(presented.Endpoint()) {
		return AuthorityEndpointMismatch
	}
	if stored.BrokerRuntimeID != presented.BrokerRuntimeID {
		return AuthorityBrokerRuntimeMismatch
	}
	if stored.ConnectionEpoch != presented.ConnectionEpoch {
		return AuthorityConnectionStale
	}
	if stored.BindingEpoch != presented.BindingEpoch {
		return AuthorityBindingStale
	}
	return AuthorityAllowed
}
