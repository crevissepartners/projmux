package agentmessages

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	claude "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

// Options contains already captured paths and transports. New reads no environment.
type Options struct {
	RegistryPath, StateDir string
	LoadRegistry           func() (metadata.Registry, error)
	Control                agentcontrol.Client
	Executable             func() (string, error)
	Release                func(string) error
}

func New(o Options) Service {
	s := Service{RegistryPath: o.RegistryPath, Store: store.NewStore(o.StateDir), LoadRegistry: o.LoadRegistry, Now: time.Now, DeferredHoldReason: deferredHoldReason}
	s.NewRef = func(prefix string) string {
		var b [18]byte
		if _, err := rand.Read(b[:]); err != nil {
			sum := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
			copy(b[:], sum[:])
		}
		return prefix + "-" + hex.EncodeToString(b[:])
	}
	resolve := func(ctx context.Context, reg metadata.Registry, ref, spelling string) (metadata.Agent, error) {
		if err := ctx.Err(); err != nil {
			return metadata.Agent{}, err
		}
		parsed, err := selector.ParseRef(metadata.KindAgent, ref)
		if err != nil {
			return metadata.Agent{}, err
		}
		query := selector.Query{Agents: []selector.Ref{parsed}}
		found, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
		if err == nil {
			err = resources.Enforce(selector.Target{Verb: selector.VerbStatus, Kind: metadata.KindAgent}, query, found)
		}
		if err != nil {
			return metadata.Agent{}, err
		}
		a, ok := reg.Agent(found.Matches[0].UID)
		if !ok {
			return metadata.Agent{}, errors.New(spelling + ": resolved Agent disappeared")
		}
		return a.Clone(), nil
	}
	s.ResolveSource, s.ResolveAgent = resolve, resolve
	s.RequireCapability = RequireCapability
	s.ResolveRoute = func(ctx context.Context, reg metadata.Registry, a metadata.Agent) (metadata.AgentRouteRef, error) {
		return resolveLiveRoute(ctx, o.RegistryPath, reg, a, false)
	}
	s.ResolveTarget = func(ctx context.Context, reg metadata.Registry, a metadata.Agent) (metadata.AgentRouteRef, error) {
		return resolveLiveRoute(ctx, o.RegistryPath, reg, a, true)
	}
	s.RouteRefusalPrefix = func(role string, err error) string {
		var unanswered probeUnanswered
		if errors.As(err, &unanswered) {
			return role + " Agent readiness is unconfirmed"
		}
		return role + " Agent is not eligible"
	}
	s.ResumeRecovery = func(err error, a metadata.Agent) error {
		return fmt.Errorf("%w; next: `projmux agent resume uid:%s`", err, a.Metadata.UID)
	}
	s.FailureAction = ReceiptFailureAction
	adapter := ClaudeAdapter{Executable: o.Executable}
	s.Claude = adapter
	s.Render = func(ctx context.Context, route metadata.AgentRouteRef, e message.Envelope) (Render, error) {
		if err := ctx.Err(); err != nil {
			return Render{}, err
		}
		return adapter.Render(route, e)
	}
	s.ClaudeStatus = func(ctx context.Context, route metadata.AgentRouteRef, ref string) (delivery agentdelivery.Delivery, err error) {
		return adapter.Status(ctx, o.RegistryPath, route, ref)
	}
	s.Deferred = func(ctx context.Context, reg metadata.Registry, source, target metadata.Agent, route metadata.AgentRouteRef, ref, reply, text string, ttl time.Duration, report func(Receipt, int) error) (bool, error) {
		return s.SendDeferred(ctx, reg, source, target, route, ref, reply, text, ttl, report, s.Accepted)
	}
	s.Deliver = func(ctx context.Context, r store.Record, a metadata.Agent, route metadata.AgentRouteRef, e message.Envelope) (store.Record, error) {
		return s.DeliverOrHold(ctx, r, a, route, e, func(ctx context.Context, r store.Record, a metadata.Agent, route metadata.AgentRouteRef, e message.Envelope) (store.Record, error) {
			if a.Spec.Provider != "claude" {
				return s.pushNative(ctx, r, a, route, e, o.Control)
			}
			private, submitErr := adapter.Submit(ctx, s.RegistryPath, route, e)
			return s.projectClaudeSend(r, private, submitErr)
		}, func() {
			if o.Release != nil {
				_ = o.Release(a.Metadata.UID)
			}
		})
	}
	return s
}

// projectClaudeSend preserves SDK uncertainty when a provider has answered but
// its projection cannot be stored. The CLI keeps its existing adapter policy;
// no durable state is fabricated and no second submission is attempted.
func (s Service) projectClaudeSend(record store.Record, private agentdelivery.Delivery, submitErr error) (store.Record, error) {
	updated, err := s.ProjectClaudeDelivery(record, private, submitErr)
	if err != nil {
		record.Delivery.OutcomeUnknown = true
		record.Delivery.Reason = "provider-handoff-outcome-unknown"
		return record, err
	}
	return updated, nil
}

func RequireCapability(action string, a metadata.Agent) error {
	provider, ok := aiprovider.Lookup(a.Spec.Provider)
	if !ok {
		return fmt.Errorf("capability %s unsupported for provider %q", action, a.Spec.Provider)
	}
	_, cell, ok := aiprovider.LookupAgentCapability(action, provider.ID)
	if !ok || cell.Mode == aiprovider.SupportUnsupported {
		return fmt.Errorf("capability %s unsupported for provider %q", action, a.Spec.Provider)
	}
	return nil
}

type probeUnanswered struct{ probe string }

func (e probeUnanswered) Error() string {
	return "claude " + e.probe + " did not answer in time; the Agent may be alive but busy under load, so retry the send and do not delete the Agent on this refusal"
}
func probeError(outcome claude.ProbeOutcome, probe string) error {
	switch outcome {
	case claude.ProbeReady:
		return nil
	case claude.ProbeUnanswered:
		return probeUnanswered{probe}
	case claude.ProbeUnqualified:
		return errors.New("claude coordination requires exact-version isolated qualification; use agent message qualify")
	default:
		return errors.New("claude registration lease is stale or unavailable")
	}
}

func (s Service) DeliverOrHold(ctx context.Context, r store.Record, a metadata.Agent, route metadata.AgentRouteRef, e message.Envelope, push func(context.Context, store.Record, metadata.Agent, metadata.AgentRouteRef, message.Envelope) (store.Record, error), release func()) (store.Record, error) {
	if a.Spec.Provider != "claude" {
		return push(ctx, r, a, route, e)
	}
	if locker, ok := s.Store.(interface {
		LockTargetDispatch(string, time.Duration) (func(), error)
	}); ok {
		lock := func() (func(), error) { return locker.LockTargetDispatch(a.Metadata.UID, 30*time.Second) }
		if contextual, ok := s.Store.(interface {
			LockTargetDispatchContext(context.Context, string, time.Duration) (func(), error)
		}); ok {
			lock = func() (func(), error) {
				return contextual.LockTargetDispatchContext(ctx, a.Metadata.UID, 30*time.Second)
			}
		}
		unlock, err := lock()
		if err != nil {
			failed, _, applyErr := s.Store.Apply(r.Envelope.MessageRef, s.event(r, message.EventFail, "provider-prewrite-refused", false))
			if applyErr != nil {
				return r, fmt.Errorf("keep send order to the target: %w", errors.Join(err, applyErr))
			}
			if ctx.Err() != nil {
				return failed, ctx.Err()
			}
			return failed, nil
		}
		defer unlock()
	}
	blocked := a.EffectiveInteraction(s.clock()).Kind
	waiting := blocked == metadata.InteractionApprovalRequired || blocked == metadata.InteractionInputRequired
	earlier, reason := false, ""
	if lister, ok := s.Store.(interface {
		HeldFor(string) ([]store.Record, error)
	}); ok {
		rows, err := lister.HeldFor(a.Metadata.UID)
		if err == nil {
			for _, row := range rows {
				if row.Envelope.MessageRef != r.Envelope.MessageRef {
					earlier, reason = true, row.Delivery.Reason
					break
				}
			}
		}
	}
	if !waiting && !earlier {
		updated, err := push(ctx, r, a, route, e)
		// The CLI preserves the last accepted receipt on a projection write failure.
		if err != nil {
			return updated, err
		}
		if updated.Delivery.State == message.StateHeld {
			release()
		}
		return updated, nil
	}
	if waiting || reason == "" {
		reason = "target-awaiting-operator"
	}
	held, _, err := s.Store.Apply(r.Envelope.MessageRef, s.event(r, message.EventHold, reason, false))
	if err != nil {
		return r, fmt.Errorf("hold message while the target awaits its operator: %w", err)
	}
	if held.Delivery.State == message.StateHeld {
		release()
	}
	return held, nil
}

func (s Service) terminal(r store.Record, kind message.EventKind, reason string, unknown bool, cause error) (store.Record, error) {
	updated, _, err := s.Store.Apply(r.Envelope.MessageRef, s.event(r, kind, reason, unknown))
	if err == nil {
		return updated, cause
	}
	state := message.StateFailed
	if kind == message.EventExpire {
		state = message.StateExpired
	}
	r.Delivery.State, r.Delivery.Reason, r.Delivery.OutcomeUnknown, r.Delivery.TerminalAt = state, reason, unknown, s.clock()
	return r, fmt.Errorf("%w; persist coordination outcome: %w", cause, err)
}

func (s Service) pushNative(ctx context.Context, r store.Record, a metadata.Agent, route metadata.AgentRouteRef, e message.Envelope, control agentcontrol.Client) (store.Record, error) {
	if _, ok := route.Authority().(metadata.CodexProcessRouteEvidence); ok {
		return s.pushProcessCodex(ctx, r, route)
	}
	if r.Delivery.State.Terminal() {
		return r, nil
	}
	if !s.clock().Before(r.Envelope.Deadline) {
		return s.terminal(r, message.EventExpire, "deadline-expired", false, errors.New("coordination deadline expired before the native turn push"))
	}
	reg, err := s.LoadRegistry()
	if err != nil {
		return s.terminal(r, message.EventFail, "codex-native-binding-unavailable", false, err)
	}
	binding, err := control.Bind(ctx, reg, a)
	if err != nil {
		return s.terminal(r, message.EventFail, "codex-native-binding-unavailable", false, err)
	}
	response, callErr := control.Call(ctx, binding, agentcontrol.Request{Operation: "peer-context", RequestKey: e.MessageRef})
	// Peer context never falls back to a user turn or steer.
	outcome := classifyPeerPush(response, callErr)
	if !outcome.delivered {
		return s.terminal(r, message.EventFail, outcome.reason, outcome.unknown, outcome.err)
	}
	updated, _, err := s.Store.Apply(e.MessageRef, s.event(r, message.EventDeliver, "provider-peer-context-accepted", false))
	if err != nil {
		r.Delivery.State = message.StateFailed
		r.Delivery.Reason = "broker-delivery-persist-failed"
		r.Delivery.OutcomeUnknown = true
		r.Delivery.TerminalAt = s.clock()
		return r, fmt.Errorf("persist delivered coordination turn: %w", err)
	}
	return updated, nil
}
