package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
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
	pane, ambiguous := processResumePane(registry, uid)
	return !ambiguous && record.Agent == uid && pane != nil && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding == record.Binding && pane.Status.ProcessSession.SessionID == record.Session && pane.Status.ProcessSession.ThreadID == record.Thread
}

// handled=false preserves the original route refusal bytes and exit when no
// eligible live claim exists. The guard excludes close and resume reservation
// until both acceptance and hold have been committed.
func (c *agentCommand) sendDeferredPeer(registry coremetadata.Registry, source, target coremetadata.Agent, sourceRoute coremetadata.AgentRouteRef, ref, reply, payload string, ttl time.Duration, stdout, stderr io.Writer) (bool, error) {
	if processResumeCandidateToken(registry, target.Metadata.UID) != "" {
		return false, nil
	}
	path := c.deferredClaimPath(target.Metadata.UID)
	if path == "" {
		return false, nil
	}
	unlock, err := lockDeferredClaim(path)
	if err != nil {
		return true, err
	}
	defer unlock()
	claim, err := readDeferredClaim(path)
	if err != nil {
		return true, err
	}
	if !deferredClaimLive(claim) {
		return false, nil
	}
	registry, err = c.readMessageRegistry()
	if err != nil {
		return true, err
	}
	if processResumeCandidateToken(registry, target.Metadata.UID) != "" || !deferredClaimMatches(claim, registry, target.Metadata.UID) {
		return false, nil
	}
	if ref == "" {
		ref = c.newMessageRef("message")
	}
	now := c.messageClock()
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: conversationRefFor(ref), ReplyTo: reply, Source: publicMessageRoute(sourceRoute), Target: deferredMessageRoute(claim), Authority: coremessage.PeerAuthority(), Payload: payload, AcceptedAt: now, Deadline: now.Add(ttl)}
	if reply != "" {
		original, found, e := c.messageStore.Get(reply)
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
	if existing, found, e := c.messageStore.Get(ref); e != nil {
		return true, e
	} else if found {
		if existing.Envelope.Target.AgentUID == envelope.Target.AgentUID && existing.Envelope.Target.Provider == envelope.Target.Provider && existing.Envelope.Target.Incarnation == envelope.Target.Incarnation {
			envelope.Target = existing.Envelope.Target
		}
	}
	if err = envelope.Validate(); err != nil {
		return true, err
	}
	adapter := "codex-inbox"
	if target.Spec.Provider == aiModeClaude {
		adapter = "claude-coordination"
	}
	var record messagestore.Record
	var created bool
	if source.Spec.Provider == aiModeClaude && reply != "" {
		explicit, ok := c.messageClaude.(interface {
			ExplicitReply(context.Context, string, coremetadata.AgentRouteRef, coremessage.Envelope) (string, bool, error)
		})
		if !ok {
			return true, fmt.Errorf("agent message send: exact explicit reply adapter is unavailable")
		}
		var replyRef string
		replyRef, created, err = explicit.ExplicitReply(context.Background(), c.messagePaths.registryPath, sourceRoute, envelope)
		if err == nil {
			var found bool
			record, found, err = c.messageStore.Get(replyRef)
			if !found && err == nil {
				err = messagestore.ErrNotFound
			}
		}
	} else {
		store, ok := c.messageStore.(interface {
			PutDeferred(coremessage.Envelope, string) (messagestore.Record, bool, error)
		})
		if !ok {
			return true, fmt.Errorf("process-host-unavailable: deferred acceptance store unavailable")
		}
		record, created, err = store.PutDeferred(envelope, adapter)
	}
	if err != nil {
		return true, err
	}
	if created {
		record, _, err = c.messageStore.Apply(ref, c.publicMessageEvent(record, coremessage.EventHold, deferredHoldReason, false))
		if err != nil {
			return true, err
		}
	}
	c.warnForeignClaudeSource(stderr, &registry, source, sourceRoute)
	if err = writeAgentMessageReceiptText(stdout, receiptFor(record), 0); err != nil {
		return true, err
	}
	if agentMessageUndelivered(record.Delivery) {
		return true, fmt.Errorf("agent message send: state=%s reason=%s", record.Delivery.State, record.Delivery.Reason)
	}
	return true, nil
}
