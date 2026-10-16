package agentmessages

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"time"
)

const deferredHoldReason = "target-awaiting-resume"

func deferredCandidate(reg coremetadata.Registry, uid string) bool {
	agent, found := reg.Agent(uid)
	pane, ambiguous := runtime.ProcessAgentPane(reg, uid)
	return !ambiguous && found && pane != nil && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.ResumeState == coremetadata.ProcessResumable && agent.Status.Phase == coremetadata.PhaseOffline && pane.Status.Activation.IsZero() && pane.Status.ProcessSession.Binding.AgentUID == uid && (agent.Status.PaneRef == "" || agent.Status.PaneRef == pane.Metadata.UID)
}

// A retired route identifies the recorded conversation, never live authority.
// It can only enter the deferred store behind an exact live claim.
func deferredMessageRoute(record runtime.DeferredClaimRecord) coremessage.Route {
	material := "codex-session"
	if record.Provider == "claude" {
		material = "claude-session\x00" + record.Session
	}
	digest := sha256.Sum256([]byte(material))
	return coremessage.Route{AgentUID: record.Agent, PaneUID: record.Binding.PaneUID, ActivationGeneration: record.Binding.Generation, Provider: record.Provider, Incarnation: fmt.Sprintf("route-%x", digest[:18])}
}

func deferredClaimMatches(record runtime.DeferredClaimRecord, registry coremetadata.Registry, uid string) bool {
	return runtime.DeferredClaimMatches(record, registry, uid)
}

// handled=false preserves the original route refusal bytes and exit when no
// eligible live claim exists. The guard excludes close and resume reservation
// until both acceptance and hold have been committed.
func (s Service) SendDeferred(ctx context.Context, registry coremetadata.Registry, source, target coremetadata.Agent, sourceRoute coremetadata.AgentRouteRef, ref, reply, payload string, ttl time.Duration, report func(Receipt, int) error, accepted func(coremetadata.Registry, coremetadata.Agent, coremetadata.AgentRouteRef)) (bool, error) {
	if !deferredCandidate(registry, target.Metadata.UID) {
		return false, nil
	}
	path := runtime.DeferredClaimPath(s.RegistryPath, target.Metadata.UID)
	if path == "" {
		return false, nil
	}
	unlock, err := runtime.LockDeferredClaimContext(ctx, path)
	if err != nil {
		return true, err
	}
	defer unlock()
	claim, err := runtime.ReadDeferredClaim(path)
	if err != nil {
		return true, err
	}
	if claim.StopRequested || !runtime.DeferredClaimLive(claim) {
		return false, nil
	}
	registry, err = s.LoadRegistry()
	if err != nil {
		return true, err
	}
	if !deferredCandidate(registry, target.Metadata.UID) || !deferredClaimMatches(claim, registry, target.Metadata.UID) {
		return false, nil
	}
	if ref == "" {
		ref = s.NewRef("message")
	}
	now := s.clock()
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: conversationRefFor(ref), ReplyTo: reply, Source: PublicRoute(sourceRoute), Target: deferredMessageRoute(claim), Authority: coremessage.PeerAuthority(), Payload: payload, AcceptedAt: now, Deadline: now.Add(ttl)}
	if reply != "" {
		original, found, e := s.Store.Get(reply)
		if e != nil {
			return true, e
		}
		if !found {
			return true, messagestore.ErrNotFound
		}
		envelope.ConversationRef = original.Envelope.ConversationRef
		if sourceRoute.AcceptsIncarnation(original.Envelope.Target.Incarnation) {
			envelope.Source.Incarnation = original.Envelope.Target.Incarnation
		}
		if envelope.Deadline.After(original.Envelope.Deadline) {
			envelope.Deadline = original.Envelope.Deadline
		}
		if e = coremessage.ValidateReply(original.Envelope, envelope); e != nil {
			return true, e
		}
	}
	// A receipt replay never dispatches or extends its original lifetime.
	if existing, found, e := s.Store.Get(ref); e != nil {
		return true, e
	} else if found {
		if existing.Envelope.Target.AgentUID == envelope.Target.AgentUID && existing.Envelope.Target.Provider == envelope.Target.Provider && existing.Envelope.Target.Incarnation == envelope.Target.Incarnation {
			envelope.Target = existing.Envelope.Target
		}
	}
	if err = envelope.Validate(); err != nil {
		return true, err
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	adapter := "codex-inbox"
	if target.Spec.Provider == "claude" {
		adapter = "claude-coordination"
	}
	var record messagestore.Record
	var created bool
	if source.Spec.Provider == "claude" && reply != "" {
		explicit, ok := s.Claude.(interface {
			ExplicitReply(context.Context, string, coremetadata.AgentRouteRef, coremessage.Envelope) (string, bool, error)
		})
		if !ok {
			return true, fmt.Errorf("agent message send: exact explicit reply adapter is unavailable")
		}
		var replyRef string
		replyRef, created, err = explicit.ExplicitReply(ctx, s.RegistryPath, sourceRoute, envelope)
		if err == nil {
			var found bool
			record, found, err = s.Store.Get(replyRef)
			if !found && err == nil {
				err = messagestore.ErrNotFound
			}
		}
	} else {
		store, ok := s.Store.(interface {
			PutDeferred(coremessage.Envelope, string) (messagestore.Record, bool, error)
		})
		if !ok {
			return true, fmt.Errorf("process-host-unavailable: deferred acceptance store unavailable")
		}
		record, created, err = store.PutDeferred(envelope, adapter)
	}
	if err != nil {
		var uncertain *ReplyUncertainError
		if errors.As(err, &uncertain) {
			return true, partialUnknown(envelope, "explicit-reply-outcome-unknown", err)
		}
		return true, err
	}
	if created {
		record, _, err = s.Store.Apply(ref, s.event(record, coremessage.EventHold, deferredHoldReason, false))
		if err != nil {
			return true, err
		}
	}
	// CLI lineage diagnostics run through its accepted callback.
	if accepted != nil {
		accepted(registry, source, sourceRoute)
	}
	if err = report(ReceiptFor(record), 0); err != nil {
		return true, err
	}
	if agentMessageUndelivered(record.Delivery) {
		return true, fmt.Errorf("agent message send: state=%s reason=%s", record.Delivery.State, record.Delivery.Reason)
	}
	return true, nil
}
