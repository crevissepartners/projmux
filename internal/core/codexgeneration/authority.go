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

// ApplyAuthorized invokes write exactly once only for the complete exact
// authority. Every legacy, same-number cross-generation, and stale row has a
// provider/Registry/tmux write count of zero by construction.
func ApplyAuthorized(durable *metadata.CodexEndpointRef, stored, presented *metadata.CodexAuthorityRef, write func()) AuthorityDecision {
	decision := DecideAuthority(durable, stored, presented)
	if decision == AuthorityAllowed && write != nil {
		write()
	}
	return decision
}

type ResumeDecision string

const (
	ResumeAllowed            ResumeDecision = "allowed"
	ResumeOwnerStillLive     ResumeDecision = "owner-still-live"
	ResumeTargetMismatch     ResumeDecision = "target-mismatch"
	ResumeNotDurable         ResumeDecision = "thread-not-durable"
	ResumeAuthorityUncertain ResumeDecision = "authority-unavailable"
)

// DecideSuccessorResume is the content-free old-stop semantic barrier used by
// qualification. It does not call the provider; the caller may do so only for
// ResumeAllowed.
func DecideSuccessorResume(owner, successor metadata.CodexEndpointRef, oldStopped, completedPersisted bool) ResumeDecision {
	if !owner.Valid() || !successor.Valid() || owner.StateDomainID != successor.StateDomainID {
		return ResumeAuthorityUncertain
	}
	if owner.Same(successor) {
		return ResumeTargetMismatch
	}
	if !oldStopped {
		return ResumeOwnerStillLive
	}
	if !completedPersisted {
		return ResumeNotDurable
	}
	return ResumeAllowed
}

// ApplySuccessorResume runs resume only after the semantic old-stopped and
// durable barriers are both closed.
func ApplySuccessorResume(owner, successor metadata.CodexEndpointRef, oldStopped, completedPersisted bool, resume func()) ResumeDecision {
	decision := DecideSuccessorResume(owner, successor, oldStopped, completedPersisted)
	if decision == ResumeAllowed && resume != nil {
		resume()
	}
	return decision
}
