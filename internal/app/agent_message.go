package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const defaultAgentMessageTimeout = 30 * time.Second

type agentMessageStore interface {
	Get(string) (messagestore.Record, bool, error)
	PutAccepted(coremessage.Envelope, string) (messagestore.Record, bool, error)
	Apply(string, coremessage.Event) (messagestore.Record, bool, error)
	MarkHandoff(string) (messagestore.Record, bool, error)
	Status(string, time.Time) (messagestore.Record, bool, error)
	Claim(coremessage.Route, time.Time) (messagestore.Record, bool, error)
}

type agentMessagePaths struct {
	registryPath string
	loadRegistry func() (coremetadata.Registry, error)
}

func defaultAgentMessagePaths(paths config.Paths) agentMessagePaths {
	store := intmetadata.NewDefaultStore(paths)
	return agentMessagePaths{registryPath: store.Path(), loadRegistry: store.LoadReadOnly}
}

func newAgentMessageStore(stateDir string) agentMessageStore { return messagestore.NewStore(stateDir) }

type agentMessageRouteResolver interface {
	Resolve(coremetadata.Registry, coremetadata.Agent) (coremetadata.AgentRouteRef, error)
}

type liveAgentMessageRouteResolver struct{ registryPath string }

func (r liveAgentMessageRouteResolver) Resolve(registry coremetadata.Registry, agent coremetadata.Agent) (coremetadata.AgentRouteRef, error) {
	route, reason := coremetadata.ResolveAgentRoute(registry, agent.Metadata.UID)
	if reason != "" {
		return coremetadata.AgentRouteRef{}, errors.New(reason)
	}
	if route.Authority().Provider() == string(aiprovider.Claude) && !probeClaudeRegistrationLease(r.registryPath, route) {
		return coremetadata.AgentRouteRef{}, errors.New("claude registration lease is stale or unavailable")
	}
	return route, nil
}

type agentMessageClaudeAdapter interface {
	Submit(context.Context, string, coremetadata.AgentRouteRef, coremessage.Envelope) (agentdelivery.Delivery, error)
	Status(context.Context, string, coremetadata.AgentRouteRef, string) (agentdelivery.Delivery, error)
}

type liveAgentMessageClaudeAdapter struct{}

func (liveAgentMessageClaudeAdapter) Submit(ctx context.Context, registryPath string, route coremetadata.AgentRouteRef, envelope coremessage.Envelope) (agentdelivery.Delivery, error) {
	target, ok := claudeTargetForRoute(route)
	if !ok {
		return agentdelivery.Delivery{}, errors.New("claude target authority is unavailable")
	}
	private := claudeCoordinationEnvelope{Version: claudeCoordinationVersion, MessageRef: envelope.MessageRef, Target: target,
		Source:   claudeCoordinationSource{Kind: "peer", Trust: "untrusted", Authority: "coordination-only"},
		Deadline: envelope.Deadline, BrokerEnvelope: &envelope}
	now := time.Now()
	if !private.valid(now, route) {
		return agentdelivery.Delivery{MessageRef: envelope.MessageRef, State: agentdelivery.StateRefused,
			Reason: "claude-private-frame-unsupported"}, nil
	}
	deadline := now.Add(localipc.Deadline)
	if envelope.Deadline.Before(deadline) {
		deadline = envelope.Deadline
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response, err := callClaudeCoordination(callCtx, registryPath, route, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "submit", Target: target, Envelope: &private,
	})
	return claudeResponseDelivery(envelope.MessageRef, response), err
}

func (liveAgentMessageClaudeAdapter) Status(ctx context.Context, registryPath string, route coremetadata.AgentRouteRef, messageRef string) (agentdelivery.Delivery, error) {
	target, ok := claudeTargetForRoute(route)
	if !ok {
		return agentdelivery.Delivery{}, errors.New("claude target authority is unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, localipc.Deadline)
	defer cancel()
	response, err := callClaudeCoordination(callCtx, registryPath, route, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "status", Target: target, MessageRef: messageRef,
	})
	return claudeResponseDelivery(messageRef, response), err
}

func claudeResponseDelivery(messageRef string, response claudeCoordinationResponse) agentdelivery.Delivery {
	if response.Delivery.MessageRef != "" || response.Delivery.State != "" {
		return response.Delivery
	}
	switch response.Kind {
	case "refused":
		return agentdelivery.Delivery{MessageRef: messageRef, State: agentdelivery.StateRefused, Reason: "provider-refused"}
	case "stale":
		return agentdelivery.Delivery{MessageRef: messageRef, State: agentdelivery.StateStale, Reason: "target-activation-stale"}
	default:
		return agentdelivery.Delivery{}
	}
}

type agentMessageReceipt struct {
	Version         int                  `json:"version"`
	MessageRef      string               `json:"messageRef"`
	ConversationRef string               `json:"conversationRef"`
	ReplyTo         string               `json:"replyTo,omitempty"`
	Source          coremessage.Route    `json:"source"`
	Target          coremessage.Route    `json:"target"`
	Delivery        coremessage.Delivery `json:"delivery"`
	Deadline        time.Time            `json:"deadline"`
}

func receiptFor(record messagestore.Record) agentMessageReceipt {
	return agentMessageReceipt{Version: record.Envelope.Version, MessageRef: record.Envelope.MessageRef,
		ConversationRef: record.Envelope.ConversationRef, ReplyTo: record.Envelope.ReplyTo,
		Source: record.Envelope.Source, Target: record.Envelope.Target, Delivery: record.Delivery,
		Deadline: record.Envelope.Deadline}
}

func (c *agentCommand) runMessage(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("agent message requires send, wait, or status")
	}
	switch args[0] {
	case "send":
		return c.runMessageSend(args[1:], stdout, stderr)
	case "wait":
		return c.runMessageClaim(args[1:], stdout, stderr)
	case "status":
		return c.runMessageStatus(args[1:], stdout, stderr)
	default:
		return usageError("agent message requires send, wait, or status")
	}
}

func (c *agentCommand) runMessageSend(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent message send"
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+2 != len(args) {
		return usageError(spelling + " requires <target-agent-ref> -- <text>; quote text as one argument")
	}
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var messageRef, replyTo string
	var ttl time.Duration
	fs.StringVar(&messageRef, "message-ref", "", "idempotency reference")
	fs.StringVar(&replyTo, "reply-to", "", "message reference being replied to")
	fs.DurationVar(&ttl, "ttl", 10*time.Minute, "delivery deadline")
	positionals, err := parseWithPositionals(fs, args[:separator])
	if err != nil {
		return err
	}
	if len(positionals) != 1 || strings.TrimSpace(args[separator+1]) == "" {
		return usageError(spelling + " requires one <target-agent-ref> and non-empty text")
	}
	if ttl <= 0 || ttl > coremessage.MaxTTL {
		return usageError(fmt.Sprintf("%s: --ttl must be greater than zero and at most %s", spelling, coremessage.MaxTTL))
	}
	authorityAction := coremessage.ActionCoordinationSend
	if replyTo != "" {
		authorityAction = coremessage.ActionCoordinationReply
	}
	if !coremessage.Authorize(coremessage.PrincipalPeer, authorityAction) {
		return fmt.Errorf("%s: peer authority does not permit %s", spelling, authorityAction)
	}
	registry, err := c.readMessageRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	source, err := c.currentMessageAgent(registry, spelling)
	if err != nil {
		return err
	}
	target, err := c.resolveMessageAgent(registry, positionals[0], spelling)
	if err != nil {
		return err
	}
	if err := requireAgentMessageCapability("message.send", source); err != nil {
		return err
	}
	if err := requireAgentMessageCapability("message.send", target); err != nil {
		return err
	}
	// Both static cells and both exact activation authorities are proved before
	// the broker store or provider adapter is touched.
	sourceRoute, err := c.resolveMessageRoute(registry, source)
	if err != nil {
		return fmt.Errorf("%s: source Agent is not eligible: %w", spelling, err)
	}
	targetRoute, err := c.resolveMessageRoute(registry, target)
	if err != nil {
		return fmt.Errorf("%s: target Agent is not eligible: %w", spelling, err)
	}
	if messageRef == "" {
		messageRef = c.newMessageRef("message")
	}
	now := c.messageClock()
	conversationRef := conversationRefFor(messageRef)
	if replyTo != "" {
		conversationRef = ""
	}
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: messageRef,
		ConversationRef: conversationRef, ReplyTo: replyTo,
		Source: publicMessageRoute(sourceRoute), Target: publicMessageRoute(targetRoute), Authority: coremessage.PeerAuthority(),
		Payload: args[separator+1], AcceptedAt: now, Deadline: now.Add(ttl)}
	if replyTo != "" {
		original, found, getErr := c.messageStore.Get(replyTo)
		if getErr != nil || !found {
			if getErr == nil {
				getErr = messagestore.ErrNotFound
			}
			return fmt.Errorf("%s: reply correlation failed: %w", spelling, getErr)
		}
		envelope.ConversationRef = original.Envelope.ConversationRef
		if err := coremessage.ValidateReply(original.Envelope, envelope); err != nil {
			return fmt.Errorf("%s: %w", spelling, err)
		}
	}
	adapter := "codex-inbox"
	if target.Spec.Provider == string(aiprovider.Claude) {
		adapter = "claude-coordination"
	}
	record, created, err := c.messageStore.PutAccepted(envelope, adapter)
	if err != nil {
		return fmt.Errorf("%s: %w", spelling, err)
	}
	if created && adapter == "claude-coordination" {
		private, submitErr := c.messageClaude.Submit(context.Background(), c.messagePaths.registryPath, targetRoute, envelope)
		record, err = c.projectClaudeDelivery(record, private, submitErr)
		if err != nil {
			return fmt.Errorf("%s: persist provider projection: %w", spelling, err)
		}
	}
	return writeAgentMessageReceipt(stdout, receiptFor(record), false)
}

func conversationRefFor(messageRef string) string {
	digest := sha256.Sum256([]byte(messageRef))
	return fmt.Sprintf("conversation-%x", digest[:18])
}

func (c *agentCommand) runMessageClaim(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent message wait"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var timeout time.Duration
	var output string
	fs.DurationVar(&timeout, "timeout", defaultAgentMessageTimeout, "maximum wait duration")
	fs.StringVar(&output, "o", "", "output mode: json")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		return err
	}
	if len(refs) > 1 || (output != "" && output != "json") || timeout < 0 || timeout > coremessage.MaxTTL {
		return usageError(spelling + " accepts [<self-agent-ref>] [--timeout <duration>] [-o json]")
	}
	if !coremessage.Authorize(coremessage.PrincipalPeer, coremessage.ActionCoordinationRead) {
		return fmt.Errorf("%s: peer authority does not permit inbox reads", spelling)
	}
	registry, err := c.readMessageRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	self, err := c.currentMessageAgent(registry, spelling)
	if err != nil {
		return err
	}
	if len(refs) == 1 {
		explicit, resolveErr := c.resolveMessageAgent(registry, refs[0], spelling)
		if resolveErr != nil {
			return resolveErr
		}
		if explicit.Metadata.UID != self.Metadata.UID {
			return fmt.Errorf("%s: explicit Agent is not the current managed Pane owner", spelling)
		}
	}
	if err := requireAgentMessageCapability("message.wait", self); err != nil {
		return err
	}
	route, err := c.resolveMessageRoute(registry, self)
	if err != nil {
		return fmt.Errorf("%s: current Agent is not eligible: %w", spelling, err)
	}
	deadline := c.messageClock().Add(timeout)
	expectedRoute := publicMessageRoute(route)
	for {
		record, claimed, claimErr := c.messageStore.Claim(expectedRoute, c.messageClock())
		if claimErr != nil {
			return claimErr
		}
		if claimed {
			return writeAgentMessageClaim(stdout, record, output == "json")
		}
		if !c.messageClock().Before(deadline) {
			return fmt.Errorf("%s: timed out with no compatible message", spelling)
		}
		if err := c.sleepMessage(context.Background(), 50*time.Millisecond); err != nil {
			return err
		}
		latest, loadErr := c.readMessageRegistry()
		if loadErr != nil {
			return MapMetadataError(loadErr)
		}
		current, ok := latest.Agent(self.Metadata.UID)
		if !ok {
			return fmt.Errorf("%s: current Agent activation is stale", spelling)
		}
		if err := requireAgentMessageCapability("message.wait", *current); err != nil {
			return err
		}
		currentRoute, routeErr := c.resolveMessageRoute(latest, *current)
		if routeErr != nil || publicMessageRoute(currentRoute) != expectedRoute {
			return fmt.Errorf("%s: current Agent activation is stale", spelling)
		}
	}
}

func (c *agentCommand) runMessageStatus(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent message status"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var output string
	fs.StringVar(&output, "o", "", "output mode: json")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		return err
	}
	if len(refs) != 1 || (output != "" && output != "json") {
		return usageError(spelling + " requires <message-ref> [-o json]")
	}
	record, found, err := c.messageStore.Get(refs[0])
	if err != nil || !found {
		if err == nil {
			err = messagestore.ErrNotFound
		}
		return fmt.Errorf("%s: %w", spelling, err)
	}
	if !record.Delivery.State.Terminal() {
		registry, loadErr := c.readMessageRegistry()
		if loadErr != nil {
			return loadErr
		}
		target, ok := registry.Agent(record.Envelope.Target.AgentUID)
		if !ok {
			record, _, err = c.messageStore.Apply(record.Envelope.MessageRef, c.staleMessageEvent(record, "target-removed"))
		} else if capabilityErr := requireAgentMessageCapability("message.status", *target); capabilityErr != nil {
			return capabilityErr
		} else if route, routeErr := c.resolveMessageRoute(registry, *target); routeErr != nil || publicMessageRoute(route) != record.Envelope.Target {
			record, _, err = c.messageStore.Apply(record.Envelope.MessageRef, c.staleMessageEvent(record, "target-activation-stale"))
		} else if record.Adapter == "claude-coordination" {
			private, statusErr := c.messageClaude.Status(context.Background(), c.messagePaths.registryPath, route, record.Envelope.MessageRef)
			record, err = c.projectClaudeDelivery(record, private, statusErr)
		} else if record.Adapter == "codex-inbox" {
			record, found, err = c.messageStore.Status(refs[0], c.messageClock())
			if err == nil && !found {
				err = messagestore.ErrNotFound
			}
		}
		if err != nil {
			return fmt.Errorf("%s: persist delivery projection: %w", spelling, err)
		}
	}
	return writeAgentMessageReceipt(stdout, receiptFor(record), output == "json")
}

func (c *agentCommand) runWait(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent wait"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var timeout time.Duration
	var until, output string
	fs.DurationVar(&timeout, "timeout", defaultAgentMessageTimeout, "maximum wait duration")
	fs.StringVar(&until, "until", "idle", "condition: idle")
	fs.StringVar(&output, "o", "", "output mode: json")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		return err
	}
	if len(refs) != 1 || until != "idle" || (output != "" && output != "json") || timeout < 0 || timeout > coremessage.MaxTTL {
		return usageError(spelling + " requires <agent-ref> [--until idle] [--timeout <duration>] [-o json]")
	}
	registry, loadErr := c.readMessageRegistry()
	if loadErr != nil {
		return MapMetadataError(loadErr)
	}
	agent, resolveErr := c.resolveMessageAgent(registry, refs[0], spelling)
	if resolveErr != nil {
		return resolveErr
	}
	if err := requireAgentMessageCapability("wait.idle", agent); err != nil {
		return err
	}
	if reason := exactAgentActivationReason(registry, agent); reason != "" {
		return fmt.Errorf("%s: Agent activation is stale: %s", spelling, reason)
	}
	expectedUID, expectedPaneUID := agent.Metadata.UID, agent.Status.PaneRef
	expectedPane, _ := registry.Pane(expectedPaneUID)
	expectedGeneration := expectedPane.Status.Activation.Generation
	deadline := c.messageClock().Add(timeout)
	for {
		current, ok := registry.Agent(expectedUID)
		if !ok {
			return fmt.Errorf("%s: Agent activation is stale", spelling)
		}
		agent = current.Clone()
		if reason := exactAgentActivationReason(registry, agent); reason != "" {
			return fmt.Errorf("%s: Agent activation is stale: %s", spelling, reason)
		}
		pane, _ := registry.Pane(agent.Status.PaneRef)
		if agent.Status.PaneRef != expectedPaneUID || pane.Status.Activation.Generation != expectedGeneration {
			return fmt.Errorf("%s: Agent activation is stale", spelling)
		}
		interaction := agent.EffectiveInteraction(c.messageClock())
		if interaction.Kind == coremetadata.InteractionIdle {
			result := struct {
				AgentUID    string                        `json:"agentUID"`
				Name        string                        `json:"name"`
				Interaction coremetadata.AgentInteraction `json:"interaction"`
			}{agent.Metadata.UID, agent.Metadata.Name, interaction}
			if output == "json" {
				return json.NewEncoder(stdout).Encode(result)
			}
			_, err = fmt.Fprintf(stdout, "%s\tidle\n", agent.Metadata.UID)
			return err
		}
		if !c.messageClock().Before(deadline) {
			return fmt.Errorf("%s: timed out waiting for idle", spelling)
		}
		if err := c.sleepMessage(context.Background(), 50*time.Millisecond); err != nil {
			return err
		}
		registry, loadErr = c.readMessageRegistry()
		if loadErr != nil {
			return MapMetadataError(loadErr)
		}
	}
}

func (c *agentCommand) readMessageRegistry() (coremetadata.Registry, error) {
	if c == nil || c.messagePaths.loadRegistry == nil {
		return coremetadata.Registry{}, errors.New("agent message registry path is unavailable")
	}
	return c.messagePaths.loadRegistry()
}

func (c *agentCommand) currentMessageAgent(registry coremetadata.Registry, spelling string) (coremetadata.Agent, error) {
	uid, resolved, detail := activeUID(c.activeTarget, coremetadata.KindAgent, registry)
	if !resolved {
		if detail == "" {
			detail = "not inside a managed Agent Pane"
		}
		return coremetadata.Agent{}, fmt.Errorf("%s: source authority unavailable: %s", spelling, detail)
	}
	agent, ok := registry.Agent(uid)
	if !ok {
		return coremetadata.Agent{}, fmt.Errorf("%s: current Agent disappeared", spelling)
	}
	return agent.Clone(), nil
}

func (c *agentCommand) resolveMessageAgent(registry coremetadata.Registry, ref, spelling string) (coremetadata.Agent, error) {
	flags := resourceQueryFlags{kind: coremetadata.KindAgent, active: c.activeTarget}
	flags.addPositionalRef(ref)
	resolution, err := flags.resolve(selector.VerbStatus, false, registry)
	if err != nil {
		return coremetadata.Agent{}, MapMetadataError(err)
	}
	agent, ok := registry.Agent(resolution.Matches[0].UID)
	if !ok {
		return coremetadata.Agent{}, fmt.Errorf("%s: resolved Agent disappeared", spelling)
	}
	return agent.Clone(), nil
}

func requireAgentMessageCapability(action string, agent coremetadata.Agent) error {
	provider, ok := aiprovider.Lookup(agent.Spec.Provider)
	if !ok {
		return fmt.Errorf("capability %s unsupported for provider %q", action, agent.Spec.Provider)
	}
	_, cell, ok := aiprovider.LookupAgentCapability(action, provider.ID)
	if !ok || cell.Mode == aiprovider.SupportUnsupported {
		return fmt.Errorf("capability %s unsupported for provider %q", action, agent.Spec.Provider)
	}
	return nil
}

func (c *agentCommand) resolveMessageRoute(registry coremetadata.Registry, agent coremetadata.Agent) (coremetadata.AgentRouteRef, error) {
	if c == nil || c.messageRoute == nil {
		return coremetadata.AgentRouteRef{}, errors.New("agent message route resolver is unavailable")
	}
	return c.messageRoute.Resolve(registry, agent)
}

func publicMessageRoute(route coremetadata.AgentRouteRef) coremessage.Route {
	provider := ""
	if route.Authority() != nil {
		provider = route.Authority().Provider()
	}
	return coremessage.Route{AgentUID: route.AgentUID, PaneUID: route.PaneUID,
		ActivationGeneration: route.Generation, Provider: provider}
}

func (c *agentCommand) publicMessageEvent(record messagestore.Record, kind coremessage.EventKind, reason string, unknown bool) coremessage.Event {
	return coremessage.Event{Kind: kind, MessageRef: record.Envelope.MessageRef,
		ConversationRef: record.Envelope.ConversationRef, Target: record.Envelope.Target,
		Reason: reason, ObservedAt: c.messageClock(), OutcomeUnknown: unknown}
}

func (c *agentCommand) projectClaudeDelivery(record messagestore.Record, private agentdelivery.Delivery, adapterErr error) (messagestore.Record, error) {
	kind, reason, unknown := coremessage.EventKind(""), private.Reason, private.Ambiguous
	if private.MessageRef != "" && private.MessageRef != record.Envelope.MessageRef {
		return record, nil
	}
	if adapterErr != nil {
		kind, reason, unknown = coremessage.EventFail, "provider-handoff-outcome-unknown", true
	} else if private.MessageRef != record.Envelope.MessageRef {
		return record, nil
	} else {
		switch private.State {
		case agentdelivery.StateHeld:
			kind = coremessage.EventHold
		case agentdelivery.StateHandoff:
			updated, _, err := c.messageStore.MarkHandoff(record.Envelope.MessageRef)
			return updated, err
		case agentdelivery.StateDelivered:
			kind = coremessage.EventDeliver
		case agentdelivery.StateRefused:
			kind = coremessage.EventRefuse
		case agentdelivery.StateExpired:
			if record.HandoffObserved {
				kind, reason, unknown = coremessage.EventFail, "provider-handoff-outcome-unknown", true
			} else {
				kind = coremessage.EventExpire
			}
		case agentdelivery.StateStale:
			if record.HandoffObserved {
				kind, reason, unknown = coremessage.EventFail, "provider-handoff-outcome-unknown", true
			} else {
				kind = coremessage.EventStale
			}
		case agentdelivery.StateFailed:
			kind = coremessage.EventFail
			if record.HandoffObserved {
				reason, unknown = "provider-handoff-outcome-unknown", true
			}
		}
	}
	if kind == "" {
		return record, nil
	}
	updated, _, err := c.messageStore.Apply(record.Envelope.MessageRef, c.publicMessageEvent(record, kind, reason, unknown))
	if err != nil {
		return record, err
	}
	return updated, nil
}

func (c *agentCommand) staleMessageEvent(record messagestore.Record, reason string) coremessage.Event {
	if record.HandoffObserved {
		return c.publicMessageEvent(record, coremessage.EventFail, "provider-handoff-outcome-unknown", true)
	}
	return c.publicMessageEvent(record, coremessage.EventStale, reason, false)
}

func writeAgentMessageReceipt(stdout io.Writer, receipt agentMessageReceipt, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(stdout).Encode(receipt)
	}
	_, err := fmt.Fprintf(stdout, "%s\t%s\n", receipt.MessageRef, receipt.Delivery.State)
	return err
}

func writeAgentMessageClaim(stdout io.Writer, record messagestore.Record, _ bool) error {
	payload, err := json.Marshal(struct {
		Envelope coremessage.Envelope `json:"envelope"`
		Delivery coremessage.Delivery `json:"delivery"`
	}{record.Envelope, record.Delivery})
	if err != nil {
		return err
	}
	_, err = stdout.Write(payload)
	return err
}

func (c *agentCommand) messageClock() time.Time {
	if c == nil || c.messageNow == nil {
		return time.Now().UTC()
	}
	return c.messageNow().UTC()
}

func (c *agentCommand) newMessageRef(prefix string) string {
	if c == nil || c.messageNewRef == nil {
		return newCoordinationRef(prefix)
	}
	return c.messageNewRef(prefix)
}

func (c *agentCommand) sleepMessage(ctx context.Context, duration time.Duration) error {
	if c == nil || c.messageSleep == nil {
		return waitAgentMessagePoll(ctx, duration)
	}
	return c.messageSleep(ctx, duration)
}

func waitAgentMessagePoll(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
