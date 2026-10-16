package agentmessages

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/aiprovider"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"strings"
	"time"
)

const FrameMaxBytes = 8 << 10

func FrameSizeReason(n int) string {
	return fmt.Sprintf("provider-frame-too-large: frameBytes=%d limitBytes=%d", n, FrameMaxBytes)
}

type SendRequest struct {
	Source, Target, Text, MessageRef, ReplyTo string
	TTL                                       time.Duration
}
type Render struct{ FrameBytes, ContentBytes int }
type Result struct {
	Receipt      Receipt
	ContentBytes int
}

// Send returns the receipt even when delivery or later persistence failed.
func (s Service) Send(ctx context.Context, req SendRequest) (Result, error) {
	var out Result
	err := s.ExecuteSend(ctx, req, func(r Receipt, n int) error { out = Result{r, n}; return nil })
	var partial *PartialError
	if errors.As(err, &partial) {
		out.Receipt = partial.Receipt
	}
	if err != nil && ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return out, err
}

// ExecuteSend calls report before returning a post-acceptance failure. The CLI
// preserves its output-writer failure ordering; typed callers use Send.
func (s Service) ExecuteSend(ctx context.Context, req SendRequest, report func(Receipt, int) error) error {
	const spelling = "agent message send"
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.AllowImplicitSource && (strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.Target) == "" || strings.TrimSpace(req.Text) == "") {
		return errors.New("agent message send requires explicit source, target and non-empty text")
	}
	messageRef, replyTo, sourceRef, ttl := req.MessageRef, req.ReplyTo, req.Source, req.TTL
	if ttl <= 0 || ttl > message.MaxTTL {
		return fmt.Errorf("%s: --ttl must be greater than zero and at most %s", spelling, message.MaxTTL)
	}
	authorityAction := message.ActionCoordinationSend
	if replyTo != "" {
		authorityAction = message.ActionCoordinationReply
	}
	if !message.Authorize(message.PrincipalPeer, authorityAction) {
		return fmt.Errorf("%s: peer authority does not permit %s", spelling, authorityAction)
	}
	// Operator input has no Agent route to answer on. The refusal comes before
	// route resolution so its reason is the one reported, whatever the target.
	if replyTo != "" {
		if original, found, err := s.Store.Get(replyTo); err == nil && found && original.Envelope.Operator() {
			return fmt.Errorf("%s: %w", spelling, s.ReplyCorrelationRefusal(replyTo, message.ReasonExplicitReplyOperatorOrigin))
		}
	}
	registry, err := s.LoadRegistry()
	if err != nil {
		return s.registryError(err)
	}
	source, err := s.ResolveSource(ctx, registry, sourceRef, spelling)
	if err != nil {
		return err
	}
	target, err := s.ResolveAgent(ctx, registry, req.Target, spelling)
	if err != nil {
		return err
	}
	if err := s.RequireCapability("message.send", source); err != nil {
		return err
	}
	if err := s.RequireCapability("message.send", target); err != nil {
		return err
	}
	// Both static cells and both exact activation authorities are proved before
	// accepting or dispatching a message. Refusals may read earlier reply receipts.
	sourceRoute, err := s.ResolveRoute(ctx, registry, source)
	if err != nil {
		if replyTo != "" {
			return fmt.Errorf("%s: %s: %w; %w", spelling, s.RouteRefusalPrefix("source", err), err,
				s.ReplyCorrelationRefusal(replyTo, "explicit-reply-source-route-stale"))
		}
		return fmt.Errorf("%s: %s: %w", spelling, s.RouteRefusalPrefix("source", err), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if handled, deferredErr := s.Deferred(ctx, registry, source, target, sourceRoute, messageRef, replyTo, req.Text, ttl, report); handled {
		return deferredErr
	}
	targetRoute, err := s.ResolveTarget(ctx, registry, target)
	if err != nil {
		if replyTo != "" {
			err = fmt.Errorf("%s: %s: %w; %w", spelling, s.RouteRefusalPrefix("target", err), err,
				s.ReplyCorrelationRefusal(replyTo, "explicit-reply-target-route-stale"))
		} else {
			err = fmt.Errorf("%s: %s: %w", spelling, s.RouteRefusalPrefix("target", err), err)
		}
		// An Offline/Failed Codex target is recovered by resuming it (on the
		// default daemon endpoint, or with a typed refusal naming its own next
		// command); other providers keep their existing refusal text.
		if target.Spec.Provider == "codex" && (target.Status.Phase == metadata.PhaseOffline || target.Status.Phase == metadata.PhaseFailed) {
			return s.ResumeRecovery(err, target)
		}
		return err
	}
	if messageRef == "" {
		messageRef = s.NewRef("message")
	}
	now := s.clock()
	conversationRef := conversationRefFor(messageRef)
	if replyTo != "" {
		conversationRef = ""
	}
	envelope := message.Envelope{Version: message.Version, MessageRef: messageRef,
		ConversationRef: conversationRef, ReplyTo: replyTo,
		Source: PublicRoute(sourceRoute), Target: PublicRoute(targetRoute), Authority: message.PeerAuthority(),
		Payload: req.Text, AcceptedAt: now, Deadline: now.Add(ttl)}
	// A first-frame deferred delivery keeps its retired address as receipt
	// evidence. Replays use that stored address and never dispatch again.
	if pane, found := registry.Pane(target.Status.PaneRef); found && pane.Spec.Runtime.Kind == metadata.RuntimeProcess {
		if existing, found, getErr := s.Store.Get(messageRef); getErr != nil {
			return getErr
		} else if found && existing.Delivery.Reason == "provider-resume-first-frame" && existing.Envelope.Target.AgentUID == target.Metadata.UID && targetRoute.AcceptsIncarnation(existing.Envelope.Target.Incarnation) {
			envelope.Target = existing.Envelope.Target
		}
	}
	if replyTo != "" {
		original, found, getErr := s.Store.Get(replyTo)
		if getErr != nil || !found {
			if getErr == nil {
				getErr = store.ErrNotFound
			}
			return fmt.Errorf("%s: reply correlation failed: %w", spelling, getErr)
		}
		envelope.ConversationRef = original.Envelope.ConversationRef
		// The reply predicate (message.ReplyRoutes) compares incarnations
		// exactly and has no authority to read either value. Answer in the shape
		// the original used whenever the current route accepts it; otherwise
		// keep the current value so the changed conversation is refused below.
		// The Pane and activation stay the current ones: a relaunch into the
		// same conversation still correlates, and the reply goes to the Agent
		// as it runs now.
		if sourceRoute.AcceptsIncarnation(original.Envelope.Target.Incarnation) {
			envelope.Source.Incarnation = original.Envelope.Target.Incarnation
		}
		if targetRoute.AcceptsIncarnation(original.Envelope.Source.Incarnation) {
			envelope.Target.Incarnation = original.Envelope.Source.Incarnation
		}
		if envelope.Deadline.After(original.Envelope.Deadline) {
			envelope.Deadline = original.Envelope.Deadline
		}
		// A same-ref call is a receipt replay, never a new dispatch. Keep the
		// durable times so reducing the remaining original TTL cannot change
		// its immutable-envelope comparison.
		if existing, exists, err := s.Store.Get(messageRef); err != nil {
			return fmt.Errorf("%s: reply receipt lookup failed: %w", spelling, err)
		} else if exists && existing.Envelope.ReplyTo == replyTo {
			envelope.AcceptedAt, envelope.Deadline = existing.Envelope.AcceptedAt, existing.Envelope.Deadline
		}
		if !original.Envelope.Deadline.After(now) {
			return fmt.Errorf("%s: %w", spelling, s.ReplyCorrelationRefusal(replyTo, "explicit-reply-deadline-expired"))
		}
		if err := message.ValidateReply(original.Envelope, envelope); err != nil {
			return fmt.Errorf("%s: %w; %w", spelling, err, s.ReplyCorrelationRefusal(replyTo, explicitReplyRefusalReason(envelope, err)))
		}
	}
	// The envelope is judged before any provider work. The store refuses the
	// same shapes on its own first line, so returning here changes no durable
	// state; it keeps the cause next to the send that caused it instead of
	// letting a downstream reader restate it.
	if err := envelope.Validate(); err != nil {
		return fmt.Errorf("%s: refused before acceptance: %w", spelling, err)
	}
	// A Claude target's push frame is judged here, before any receipt exists.
	claudeContentBytes := 0
	if target.Spec.Provider == string(aiprovider.Claude) {
		render, renderErr := s.Render(ctx, targetRoute, envelope)
		if renderErr != nil {
			return fmt.Errorf("%s: claude push frame unavailable before acceptance: %w", spelling, renderErr)
		}
		if render.FrameBytes > FrameMaxBytes {
			return fmt.Errorf("%s: refused before acceptance: %s; reduce payload before retrying; frame bytes include auth and serialized content",
				spelling, FrameSizeReason(render.FrameBytes))
		}
		claudeContentBytes = render.ContentBytes
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if source.Spec.Provider == string(aiprovider.Claude) && replyTo != "" {
		if adapter, ok := s.Claude.(interface {
			ExplicitReply(context.Context, string, metadata.AgentRouteRef, message.Envelope) (string, bool, error)
		}); ok {
			ref, created, replyErr := adapter.ExplicitReply(ctx, s.RegistryPath, sourceRoute, envelope)
			if replyErr != nil {
				wrapped := fmt.Errorf("%s: explicit reply refused: %w", spelling, replyErr)
				var uncertain *ReplyUncertainError
				if errors.As(replyErr, &uncertain) {
					return partialUnknown(envelope, "explicit-reply-outcome-unknown", wrapped)
				}
				return wrapped
			}
			record, found, getErr := s.Store.Get(ref)
			if getErr != nil || !found || !record.Envelope.SameRetry(envelope) {
				if getErr == nil {
					if !found {
						getErr = store.ErrNotFound
					} else {
						getErr = message.ErrRetryMismatch
					}
				}
				partial := partialUnknown(envelope, "explicit-reply-receipt-unavailable", fmt.Errorf("%s: explicit reply receipt unavailable", spelling))
				partial.Cause = getErr // preserve the CLI text while retaining the SDK cause
				return partial
			}
			// A reply is delivered the same way any other message is. Without
			// this it only reached the store, and with the self-claim inbox gone
			// nothing would ever hand it to the target.
			var pushErr error
			if created {
				record, pushErr = s.Deliver(ctx, record, target, targetRoute, record.Envelope)
			}
			if err := report(ReceiptFor(record), claudeContentBytes); err != nil {
				return err
			}
			if pushErr != nil {
				return fmt.Errorf("%s: %w", spelling, pushErr)
			}
			if agentMessageUndelivered(record.Delivery) {
				return fmt.Errorf("%s: explicit reply not delivered: previousRef=%s state=%s reason=%s outcomeUnknown=%t; %s",
					spelling, ref, record.Delivery.State, record.Delivery.Reason, record.Delivery.OutcomeUnknown,
					s.FailureAction(ReceiptFor(record), claudeContentBytes))
			}
			return nil
		}
		return fmt.Errorf("%s: exact explicit reply adapter is unavailable", spelling)
	}
	adapter := "codex-inbox"
	if target.Spec.Provider == string(aiprovider.Claude) {
		adapter = "claude-coordination"
	}
	record, created, err := s.Store.PutAccepted(envelope, adapter)
	if err != nil {
		return fmt.Errorf("%s: %w", spelling, err)
	}
	// Only an accepted send is judged. The reply above needs no warning: its
	// coordination server already refuses a caller outside the provider.
	s.accepted(registry, source, sourceRoute)
	var pushErr error
	if created {
		record, pushErr = s.Deliver(ctx, record, target, targetRoute, envelope)
	}
	// The receipt is written before the failure is returned, so the sender sees
	// the terminal state, reason, and action on stdout and still exits nonzero.
	receipt := ReceiptFor(record)
	if err := report(receipt, claudeContentBytes); err != nil {
		return err
	}
	if pushErr != nil {
		return fmt.Errorf("%s: %w", spelling, pushErr)
	}
	// A push that returned no cause, and a same-ref replay that pushed nothing,
	// still exit by the receipt they printed.
	if agentMessageUndelivered(record.Delivery) {
		return fmt.Errorf("%s: message not delivered: messageRef=%s state=%s reason=%s outcomeUnknown=%t; %s",
			spelling, record.Envelope.MessageRef, record.Delivery.State, record.Delivery.Reason, record.Delivery.OutcomeUnknown,
			s.FailureAction(receipt, claudeContentBytes))
	}
	return nil
}

func explicitReplyRefusalReason(reply message.Envelope, correlation error) string {
	if reply.Validate() != nil {
		return "invalid-explicit-reply-envelope"
	}
	if errors.Is(correlation, message.ErrReplyConversationChanged) {
		return message.ReasonExplicitReplyConversationChanged
	}
	return message.ReasonExplicitReplyCorrelation
}

func conversationRefFor(messageRef string) string {
	digest := sha256.Sum256([]byte(messageRef))
	return fmt.Sprintf("conversation-%x", digest[:18])
}

func agentMessageUndelivered(delivery message.Delivery) bool {
	return delivery.State.Terminal() && delivery.State != message.StateDelivered
}

func (s Service) ReplyCorrelationRefusal(originalRef, reason string) error {
	if s.CorrelationRefusal != nil {
		return s.CorrelationRefusal(originalRef, reason)
	}
	previousRef := ""
	var delivery *message.Delivery
	if lookup, ok := s.Store.(interface {
		Reply(string) (store.Record, bool, error)
	}); ok {
		if previous, found, err := lookup.Reply(originalRef); err == nil && found {
			previousRef = previous.Envelope.MessageRef
			delivery = &previous.Delivery
		}
	}
	action := "inspect original and previous reply status; do not resend"
	if reason == message.ReasonExplicitReplyConversationChanged {
		action = "the original belongs to another conversation of that Agent; send a new message without --reply-to"
	}
	if delivery != nil {
		return fmt.Errorf("replyTo=%s: %s; previousRef=%s state=%s reason=%s outcomeUnknown=%t; %s", originalRef, reason, previousRef, delivery.State, delivery.Reason, delivery.OutcomeUnknown, action)
	}
	return fmt.Errorf("replyTo=%s: %s; previousRef=%s; %s", originalRef, reason, previousRef, action)
}

// PartialError preserves an exact uncertain receipt without changing CLI error
// text or emitting a receipt the CLI previously did not print.
type PartialError struct {
	Receipt Receipt
	Err     error
	Cause   error // optional underlying failure omitted from legacy CLI text
}

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() []error {
	if e.Cause != nil {
		return []error{e.Err, e.Cause}
	}
	return []error{e.Err}
}

type ReplyUncertainError struct{ Err error }

func (e *ReplyUncertainError) Error() string { return e.Err.Error() }
func (e *ReplyUncertainError) Unwrap() error { return e.Err }
func partialUnknown(e message.Envelope, reason string, err error) *PartialError {
	r := ReceiptFor(store.Record{Envelope: e, Delivery: message.Delivery{MessageRef: e.MessageRef, ConversationRef: e.ConversationRef, State: message.StateFailed, Reason: reason, OutcomeUnknown: true, AcceptedAt: e.AcceptedAt}})
	return &PartialError{Receipt: r, Err: err}
}
