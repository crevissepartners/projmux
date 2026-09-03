package codexgeneration

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

// ConsumerProjection is the one content-free decision shared by every
// generation-aware presentation consumer. Endpoint and Fence identify the
// authority being presented; a refused or stale tuple has no actionable
// consumer surface.
type ConsumerProjection struct {
	Endpoint     metadata.CodexEndpointRef
	Fence        string
	Lifecycle    LifecycleProjection
	Notification bool
	Sidebar      bool
	Statusbar    bool
	Reply        bool
	Effect       MutationEffect
	Reason       AuthorityDecision
}

// ProjectConsumers joins the canonical lifecycle projection to the exact
// endpoint/runtime authority decision. Expected Current+Draining coexistence
// is represented by each Agent's own endpoint and is not itself a refusal.
// Only an invalid lifecycle/admission tuple or a non-current five-dimensional
// authority produces zero actionable surfaces.
func ProjectConsumers(lifecycle LifecycleProjectionInput, mutation RuntimeMutationInput, notify bool) ConsumerProjection {
	out := ConsumerProjection{
		Lifecycle: ProjectLifecycle(lifecycle),
		Effect:    MutationZeroWrite,
		Reason:    AuthorityLegacyUnavailable,
	}
	if lifecycle.Endpoint != nil {
		out.Endpoint = *lifecycle.Endpoint
	}
	if !lifecycle.Authoritative() || mutation.PresentedAuthority == nil {
		return out
	}
	decision := DecideRuntimeMutation(mutation)
	out.Reason = decision.Authority
	out.Effect = decision.Class.Effect
	if decision.Class.Effect != MutationSemanticEffect || !lifecycle.Endpoint.Same(mutation.PresentedAuthority.Endpoint()) {
		return out
	}
	out.Fence = ConsumerFence(*lifecycle.Endpoint, *mutation.PresentedAuthority)
	actionable := out.Lifecycle.Attention == LifecycleAttentionReply
	out.Notification = notify && actionable
	out.Sidebar = actionable
	out.Statusbar = actionable
	out.Reply = actionable && (lifecycle.Interaction == metadata.InteractionApprovalRequired || lifecycle.Interaction == metadata.InteractionInputRequired)
	return out
}

// ConsumerFence is a stable digest of the endpoint plus the complete broker
// authority. It is safe for journals and notification metadata: it carries no
// prompt, turn, approval, path, socket, credential, or provider payload.
func ConsumerFence(endpoint metadata.CodexEndpointRef, authority metadata.CodexAuthorityRef) string {
	if !endpoint.Valid() || !authority.Valid() || !endpoint.Same(authority.Endpoint()) {
		return ""
	}
	parts := []string{
		endpoint.StateDomainID,
		endpoint.EndpointGenerationID,
		authority.BrokerRuntimeID,
		strconv.FormatUint(authority.ConnectionEpoch, 10),
		strconv.FormatUint(authority.BindingEpoch, 10),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}
