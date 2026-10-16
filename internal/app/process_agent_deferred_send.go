package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// A retired route identifies the recorded conversation, never live authority.
// It can only enter the deferred store behind an exact live claim.
func deferredMessageRoute(record deferredClaimRecord) coremessage.Route {
	material := "codex-session"
	if record.Provider == "claude" {
		material = "claude-session\x00" + record.Session
	}
	digest := sha256.Sum256([]byte(material))
	return coremessage.Route{AgentUID: record.Agent, PaneUID: record.Binding.PaneUID, ActivationGeneration: record.Binding.Generation, Provider: record.Provider, Incarnation: fmt.Sprintf("route-%x", digest[:18])}
}

func deferredClaimMatches(record deferredClaimRecord, registry coremetadata.Registry, uid string) bool {
	return mutationruntime.DeferredClaimMatches(record, registry, uid)
}

// handled=false preserves the original route refusal bytes and exit when no
// eligible live claim exists. The guard excludes close and resume reservation
// until both acceptance and hold have been committed.
func (c *agentCommand) sendDeferredPeer(ctx context.Context, registry coremetadata.Registry, source, target coremetadata.Agent, sourceRoute coremetadata.AgentRouteRef, ref, reply, payload string, ttl time.Duration, report func(agentmessages.Receipt, int) error, accepted func(coremetadata.Registry, coremetadata.Agent, coremetadata.AgentRouteRef)) (bool, error) {
	return c.peerMessageService().SendDeferred(ctx, registry, source, target, sourceRoute, ref, reply, payload, ttl, report, accepted)
}
