package agentmessages

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// OperatorSendRequest is operator input from one in-process operator client to
// one Agent. It names no source: the operator is not an Agent and has no
// route, and there is no reply form, since an operator envelope carries no
// ReplyTo. Client is the operator client name; the caller has already judged
// who the operator is.
type OperatorSendRequest struct {
	Client, Target, Payload, MessageRef string
	TTL                                 time.Duration
}

// SendOperator sends operator input to one Claude Agent. It is the target half
// of a send: the same target resolution, capability and route refusals, frame
// pre-check, acceptance, delivery or hold, and undelivered failure, with the
// operator origin and authority in place of a source Agent's route. Its errors
// are spelled "<client> message send".
//
// A non-Claude target is refused before its route is resolved and before
// anything is written, with ErrOperatorOriginTargetNotClaude: a Codex Agent
// takes the operator's input as native turns instead.
//
// The result's receipt is zero when no receipt exists. When it is not zero and
// the error is not nil either, the message was accepted and the receipt names
// why it was not delivered; it is never resent.
func (s Service) SendOperator(ctx context.Context, req OperatorSendRequest) (Result, error) {
	var out Result
	err := s.executeOperatorSend(ctx, req, func(r Receipt, n int) error { out = Result{r, n}; return nil })
	if err != nil && ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return out, err
}

func (s Service) executeOperatorSend(ctx context.Context, req OperatorSendRequest, report func(Receipt, int) error) error {
	spelling := req.Client + " message send"
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.TTL <= 0 || req.TTL > message.MaxTTL {
		return fmt.Errorf("%s: ttl must be greater than zero and at most %s", spelling, message.MaxTTL)
	}
	if !message.Authorize(message.PrincipalOperator, message.ActionCoordinationSend) {
		return fmt.Errorf("%s: operator authority does not permit %s", spelling, message.ActionCoordinationSend)
	}
	registry, err := s.LoadRegistry()
	if err != nil {
		return s.registryError(err)
	}
	target, err := s.ResolveAgent(ctx, registry, req.Target, spelling)
	if err != nil {
		return err
	}
	if target.Spec.Provider != string(aiprovider.Claude) {
		return fmt.Errorf("%s: %s is a %s Agent: %w", spelling, target.Metadata.UID, target.Spec.Provider,
			message.ErrOperatorOriginTargetNotClaude)
	}
	if err := s.RequireCapability("message.send", target); err != nil {
		return err
	}
	targetRoute, err := s.ResolveTarget(ctx, registry, target)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", spelling, s.RouteRefusalPrefix("target", err), err)
	}
	origin, err := message.OperatorOrigin(req.Client)
	if err != nil {
		return err
	}
	messageRef := req.MessageRef
	if messageRef == "" {
		messageRef = s.NewRef("message")
	}
	now := s.clock()
	envelope := message.Envelope{Version: message.Version, MessageRef: messageRef,
		ConversationRef: conversationRefFor(messageRef), Origin: origin,
		Target: PublicRoute(targetRoute), Authority: message.OperatorAuthority(),
		Payload: req.Payload, AcceptedAt: now, Deadline: now.Add(req.TTL)}
	// An invalid envelope is not judged here: it keeps the store's own refusal.
	contentBytes := 0
	if envelope.Validate() == nil {
		if contentBytes, err = s.claudeSendPrecheck(ctx, spelling, target, targetRoute, envelope); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// An operator send has no source Agent, so there is no source lineage to
	// judge once it is accepted.
	return s.acceptAndDeliver(ctx, spelling, envelope, target, targetRoute, contentBytes, nil, report)
}
