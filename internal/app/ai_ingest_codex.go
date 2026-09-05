package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type codexHookPayload struct {
	EventName      string
	ThreadID       string
	SessionID      string
	TurnID         string
	CWD            string
	TranscriptPath string
	Model          string
	ToolName       string
	ToolInput      map[string]any
}

func (c *aiCommand) ingestCodexHook(data []byte, explicitPane string) error {
	payload, err := parseCodexHookPayload(data)
	if err != nil {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Result: "error", Reason: aiIngestFailureReason(aiIngestReasonHookPayloadInvalid, err)})
		return err
	}

	paneID, nativeRouted, nativeAllowed, nativeReason := c.routeNativeCodexHook(payload.matchThreadID())
	if nativeRouted && !nativeAllowed {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: aiIngestRecordReason(nativeReason), ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
		return nil
	}
	matchReason := ""
	if !nativeRouted {
		paneID, matchReason = c.matchAIPane(aiPaneMatchInput{
			ExplicitPane: explicitPane,
			Provider:     aiModeCodex,
			CWD:          payload.CWD,
			ThreadID:     payload.matchThreadID(),
			SessionID:    payload.SessionID,
		})
	}
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: aiIngestRecordReason(matchReason), CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	}
	authority, authorityErr := c.muxRunner().ShowPaneOption(context.Background(), paneID, aiPaneCodexAuthorityOption)
	if nativeRouted && (authorityErr != nil || authority != codexAuthorityHook) {
		reason := aiIngestReasonNativeAuthorityFailed
		if authorityErr == nil {
			reason = aiIngestAuthorityReason(authority)
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: reason, Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
		return nil
	}
	if !nativeRouted && codexAuthoritySuppressesHooks(authority) {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: aiIngestAuthorityReason(authority), Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
		return nil
	}
	if nativeRouted {
		release, err := c.acquireCodexAuthorityFence(c.env(internalActivationPaneUIDEnv))
		if err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonAuthorityFenceFailed, err), Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
			return err
		}
		defer release()
		// SetAuthority owns the same fence. Revalidate after waiting so a hook
		// that observed provider-hook before an invalidation cannot commit any
		// Registry, tmux, queue, or desktop write after that invalidation.
		authority, err = c.muxRunner().ShowPaneOption(context.Background(), paneID, aiPaneCodexAuthorityOption)
		if err != nil || authority != codexAuthorityHook {
			reason := aiIngestReasonNativeAuthorityFailed
			if err == nil {
				reason = aiIngestAuthorityReason(authority)
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: reason, Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
			return nil
		}
	}
	if allowed, reason := c.nativeCodexHookAllowed(paneID, payload.matchThreadID()); !allowed {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: aiIngestRecordReason(reason), Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
		return nil
	}
	c.stageCodexBinding(paneID, payload.matchThreadID(), payload.TurnID)
	defer c.flushPendingAgentSessionRef(paneID)

	metadata := payload.codexHookMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderCodex, payload.EventName)
	switch payload.EventName {
	case "UserPromptSubmit":
		if action.Action == aiHookActionQuiet {
			c.quietCodexHook(paneID, payload, aiIngestRecordReason(aiHookQuietReason(action)))
			return nil
		}
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		if err := c.applyAIStatusWithNotify("thinking", paneID, attentionNotifyInput{
			Metadata:  metadata,
			BadgeKind: aiBadgeKindInProgress,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonStatusApplyFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "state", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "Stop":
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		body := formatCodexHookStopNotifyBody(payload)
		policy, rawOverride := c.codexHookSemanticPolicy(payload.EventName, coremetadata.InteractionResponseComplete)
		if err := c.applyCodexHookSemanticDelivery(paneID, coremetadata.InteractionResponseComplete, policy, attentionNotifyInput{
			ID:        codexHookNotifyID(payload, "stop"),
			Text:      body.Text,
			Severity:  body.Severity,
			Metadata:  mergeAINotifyBodyMetadata(metadata, body),
			Force:     true,
			BadgeKind: aiBadgeKindResponseComplete,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonSemanticDeliverFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		result, reason := codexHookSemanticLogResult(policy, rawOverride)
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: result, Reason: aiIngestRecordReason(reason), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "PermissionRequest":
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		body := formatCodexHookPermissionNotifyBody(payload)
		policy, rawOverride := c.codexHookSemanticPolicy(payload.EventName, coremetadata.InteractionApprovalRequired)
		if err := c.applyCodexHookSemanticDelivery(paneID, coremetadata.InteractionApprovalRequired, policy, attentionNotifyInput{
			ID:        codexHookNotifyID(payload, "permission"),
			Text:      body.Text,
			Severity:  body.Severity,
			Metadata:  mergeAINotifyBodyMetadata(metadata, body),
			Force:     true,
			BadgeKind: aiBadgeKindApprovalRequired,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonSemanticDeliverFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		result, reason := codexHookSemanticLogResult(policy, rawOverride)
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: result, Reason: aiIngestRecordReason(reason), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "SessionStart":
		if _, _, err := c.persistManagedAgentStartupReadiness(paneID, aiModeCodex); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonReadinessWriteFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		if c.shouldPushGenericCodexHookNotify(action) {
			if err := c.pushGenericCodexHookNotifyWithoutActivation(paneID, payload); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonNotifyPushFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		c.quietCodexHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	case "PreToolUse", "PostToolUse", "PreCompact", "PostCompact":
		if c.shouldPushGenericCodexHookNotify(action) {
			if err := c.pushGenericCodexHookNotify(paneID, payload); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: aiIngestFailureReason(aiIngestReasonNotifyPushFailed, err), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		c.quietCodexHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	default:
		c.quietCodexHook(paneID, payload, aiIngestRecordReason(aiHookNoHandlerReason(action)))
		return nil
	}
}

// codexHookSemanticPolicy makes the semantic event policy source-independent
// while preserving an existing raw hook override as a fallback-only override.
// Catalog defaults are installation/compatibility metadata, not a second
// semantic policy store, so they deliberately do not participate here.
func (c *aiCommand) codexHookSemanticPolicy(event string, interaction coremetadata.AgentInteractionKind) (config.AISemanticPolicy, bool) {
	semantic := c.codexSemanticPolicyForInteraction(interaction)
	action, overridden := c.aiHookRuntimeAction(aiHookProviderCodex, event)
	if !overridden {
		return semantic, false
	}
	return codexSemanticPolicyWithFallbackOverride(semantic, action, true), true
}

func codexSemanticPolicyWithFallbackOverride(semantic config.AISemanticPolicy, action string, overridden bool) config.AISemanticPolicy {
	if !overridden {
		return semantic
	}
	switch action {
	case aiHookActionNotify:
		return config.AISemanticNotify
	case aiHookActionState:
		return config.AISemanticStateOnly
	case aiHookActionQuiet:
		return config.AISemanticQuiet
	default:
		// Runtime loading admits only closed actions. Keep this helper closed as
		// well so a malformed value can never silently become a new policy.
		return semantic
	}
}

func codexHookSemanticLogResult(policy config.AISemanticPolicy, rawOverride bool) (result, reason string) {
	source := "semantic policy"
	if rawOverride {
		source = "runtime fallback override"
	}
	switch policy {
	case config.AISemanticQuiet:
		return "quiet", source + " quiet"
	case config.AISemanticStateOnly:
		return "state", source + " state only"
	default:
		return "notify", ""
	}
}

// A codex hook launched by a pane client inherits $TMUX, and that receipt names
// its server without a search. A hook launched by the shared app-server that
// several Panes talk to inherits nothing at all, so an unprefixed `tmux
// set-option` there is a default-socket probe: it looks for the operator's
// `default` server, does not find one, and dies. That is why an attributed
// Stop reached its Pane and then failed with a bare `exit status 1`.
//
// These are the closed reasons a reflection refuses to write. Each names the
// exact authority that was missing, so `ai-ingest.log` never carries a bare
// process exit status where a cause belongs.
const (
	// codexHookRouteUnavailableReason is raised when the hook has no inherited
	// receipt and projmux's own app-owned runtime could not be observed.
	codexHookRouteUnavailableReason = "pane runtime route unavailable"
	// codexHookRouteForeignPaneReason is raised when a runtime answered but the
	// attributed pane is not a projmux Pane living on it.
	codexHookRouteForeignPaneReason = "pane runtime route does not hold this pane"
	// codexHookWriteRejectedReason is raised when the routed set-option itself
	// failed. It always carries the exact option and a re-read classification
	// of the route, never the write's process exit status.
	codexHookWriteRejectedReason = "pane option write rejected"
)

// codexHookDeliveryError is the self-describing refusal a reflection returns.
// Its Error() is what `ai-ingest.log` records as the reason, so every value it
// can render is a sentence about authority, never an exit status alone.
type codexHookDeliveryError struct {
	Reason string
	Detail string
}

func (e *codexHookDeliveryError) Error() string {
	if e == nil {
		return codexHookRouteUnavailableReason
	}
	if strings.TrimSpace(e.Detail) == "" {
		return e.Reason
	}
	return e.Reason + ": " + e.Detail
}

// The route a reflection took, named by kind. The socket behind it is an
// absolute path, and this track's change boundary keeps paths out of durable
// records, so a reason says which lane answered and never which file it is.
const (
	codexHookInheritedRoute = "inherited route"
	codexHookAppOwnedRoute  = "app-owned route"
)

// What tmux refused, as a closed set. tmux explains itself on stderr and that
// text carries socket paths, so the explanation is read, classified, and
// dropped -- the classification is what a record may keep.
const (
	codexHookCauseNoServer     = "no server on this route"
	codexHookCauseNoPane       = "pane not found on this route"
	codexHookCauseDenied       = "permission denied on this route"
	codexHookCauseUnclassified = "unclassified tmux failure"
)

// codexHookDeliveryCause folds one tmux failure into that closed set. It is the
// only reader of tmux's own words on this path, and it returns none of them:
// a bare exit status says nothing, and the message that would say something
// names the socket.
func codexHookDeliveryCause(output []byte, err error) string {
	message := strings.ToLower(strings.TrimSpace(string(output)))
	switch {
	case message == "" && err == nil:
		return codexHookCauseUnclassified
	case strings.Contains(message, "no server running"),
		strings.Contains(message, "error connecting"),
		strings.Contains(message, "failed to connect"),
		strings.Contains(message, "no such file or directory"):
		return codexHookCauseNoServer
	case strings.Contains(message, "can't find pane"),
		strings.Contains(message, "cant find pane"),
		strings.Contains(message, "no such pane"),
		strings.Contains(message, "pane not found"):
		return codexHookCauseNoPane
	case strings.Contains(message, "permission denied"):
		return codexHookCauseDenied
	default:
		return codexHookCauseUnclassified
	}
}

// codexHookDeliveryRouteFormat reads the three facts that decide whether a
// candidate runtime may receive this Pane's reflection, in one call.
var codexHookDeliveryRouteFormat = tmuxRowFormat(
	intmux.TmuxFormat(tmuxopts.AppGlobal),
	"#{pane_id}",
	intmux.TmuxFormat(tmuxopts.PaneUID),
)

// codexHookDeliveryTarget is the one tmux server a reflection may write to.
// Every write carries its routing argv, so no reflection call is ever the
// unprefixed default-server probe that killed an attributed Stop.
type codexHookDeliveryTarget struct {
	command   *aiCommand
	transport tmuxTransport
	// kind names the lane that answered. It is what a refusal may record; the
	// transport's own value is a socket path and stays out of every record.
	kind string
}

func (t codexHookDeliveryTarget) args(args ...string) []string {
	routed := make([]string, 0, len(args)+2)
	routed = append(routed, t.transport.Args()...)
	return append(routed, args...)
}

// contained re-reads whether this route still holds paneID as a projmux Pane.
// It is the pure-read classifier a rejected write is explained by: no write is
// repeated, and the answer names a cause instead of a process exit status.
func (t codexHookDeliveryTarget) contained(paneID string) (bool, string) {
	runner := explicitTmuxRunner{
		runner: aiCommandMuxBackend{runCommand: t.command.runCommand, readCommand: t.command.readCommand},
		target: t.transport,
	}
	output, err := runner.Run(context.Background(), "tmux",
		"display-message", "-p", "-t", paneID, "-F", codexHookDeliveryRouteFormat)
	if err != nil {
		return false, "runtime is no longer reachable: " + codexHookDeliveryCause(output, err)
	}
	rows := splitTmuxRows(string(output), 3)
	if len(rows) != 1 || rows[0][1] != paneID || strings.TrimSpace(rows[0][2]) == "" {
		return false, "pane is gone from this runtime"
	}
	return true, "runtime still holds this pane and rejected the option write"
}

// codexHookDeliveryRoute pins every reflection write to one exact tmux server.
//
// An inherited receipt is taken as-is: an absolute socket path is the only
// shape tmux writes, and it names the client's own server without a search.
// Without one, the route is projmux's own app-owned runtime -- the same route
// every detached projmux invocation already takes -- and it is never trusted on
// its name alone. The Pane the hook was already attributed to is the
// discriminator: that server must be app-owned and must actually hold the
// attributed pane as a projmux Pane. A candidate that cannot prove the
// containment receives no write at all, because answering one server's question
// with another server's objects is worse than refusing.
func (c *aiCommand) codexHookDeliveryRoute(paneID string) (codexHookDeliveryTarget, error) {
	inherited, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{InheritedTMUX: c.env("TMUX")})
	if err == nil && inherited.Present() {
		return codexHookDeliveryTarget{command: c, transport: inherited, kind: codexHookInheritedRoute}, nil
	}
	target := codexHookDeliveryTarget{command: c, transport: defaultRuntimeMutationRoute().target, kind: codexHookAppOwnedRoute}
	runner := explicitTmuxRunner{
		runner: aiCommandMuxBackend{runCommand: c.runCommand, readCommand: c.readCommand},
		target: target.transport,
	}
	output, runErr := runner.Run(context.Background(), "tmux",
		"display-message", "-p", "-t", paneID, "-F", codexHookDeliveryRouteFormat)
	if runErr != nil {
		return codexHookDeliveryTarget{}, &codexHookDeliveryError{
			Reason: codexHookRouteUnavailableReason,
			Detail: target.kind + ": " + codexHookDeliveryCause(output, runErr),
		}
	}
	rows := splitTmuxRows(string(output), 3)
	if len(rows) != 1 {
		return codexHookDeliveryTarget{}, &codexHookDeliveryError{
			Reason: codexHookRouteUnavailableReason,
			Detail: target.kind + ": containment probe returned no single row",
		}
	}
	if resourcegraph.HostModeFromAppMarker(rows[0][0]) != resourcegraph.HostModeAppOwned {
		return codexHookDeliveryTarget{}, &codexHookDeliveryError{
			Reason: codexHookRouteUnavailableReason,
			Detail: target.kind + ": server is not app-owned",
		}
	}
	if rows[0][1] != paneID || strings.TrimSpace(rows[0][2]) == "" {
		return codexHookDeliveryTarget{}, &codexHookDeliveryError{
			Reason: codexHookRouteForeignPaneReason,
			Detail: target.kind + ": " + paneID,
		}
	}
	return target, nil
}

// applyCodexHookSemanticDelivery mirrors the native lifecycle delivery intent
// for the hook authority without routing through the legacy waiting reducer,
// whose state-only branch intentionally retains reply attention. The hook was
// already checked against exact activation and authority before reaching here.
func (c *aiCommand) applyCodexHookSemanticDelivery(paneID string, interaction coremetadata.AgentInteractionKind, policy config.AISemanticPolicy, input attentionNotifyInput) error {
	delivery := codexSemanticDeliveryFor(policy, interaction)
	if _, _, err := c.persistManagedAgentInteractionWithActivationPolicy(paneID, delivery.RegistryInteraction, string(coremetadata.InteractionSourceProviderHook), true); err != nil {
		if errors.Is(err, errManagedAgentObservationIgnored) {
			return nil
		}
		return err
	}

	input.PaneID = paneID
	if input.Lookup == nil {
		input.Lookup = c.notifyLookup()
	}
	if !delivery.Notify {
		store, err := c.aiNotifyStore()
		if err != nil {
			return err
		}
		if err := store.Ack(input.ID); err != nil && !errors.Is(err, notify.ErrNotFound) {
			return err
		}
		c.publishNotifyQueueRefreshBestEffort()
	}

	route, err := c.codexHookDeliveryRoute(paneID)
	if err != nil {
		return err
	}
	for _, field := range []struct{ option, value string }{
		{aiPaneStateOption, delivery.State},
		{aiPaneBadgeKindOption, delivery.Badge},
		{attentionStateOption, delivery.Attention},
	} {
		args := []string{"set-option", "-p", "-t", paneID, field.option, field.value}
		if field.value == "" {
			args = []string{"set-option", "-p", "-u", "-t", paneID, field.option}
		}
		if runErr := c.run("tmux", route.args(args...)...); runErr != nil {
			_, cause := route.contained(paneID)
			return &codexHookDeliveryError{
				Reason: codexHookWriteRejectedReason,
				Detail: field.option + " on " + route.kind + ": " + cause,
			}
		}
	}
	if !delivery.Notify {
		return nil
	}
	_ = c.notifyAIWithInput(paneID, input)
	c.notifyProducer().PushReplyReady(input)
	return nil
}

func (c *aiCommand) quietCodexHook(paneID string, payload codexHookPayload, reason aiIngestReason) {
	c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
	c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "quiet", Reason: reason, Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
}

func (c *aiCommand) shouldPushGenericCodexHookNotify(action aiHookActionResolution) bool {
	return action.Action == aiHookActionNotify && action.Source == aiHookActionSourceRuntime
}

func (c *aiCommand) pushGenericCodexHookNotify(paneID string, payload codexHookPayload) error {
	return c.pushGenericCodexHookNotifyWithActivationPolicy(paneID, payload, true)
}

func (c *aiCommand) pushGenericCodexHookNotifyWithoutActivation(paneID string, payload codexHookPayload) error {
	return c.pushGenericCodexHookNotifyWithActivationPolicy(paneID, payload, false)
}

func (c *aiCommand) pushGenericCodexHookNotifyWithActivationPolicy(paneID string, payload codexHookPayload, activationEligible bool) error {
	c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
	body := formatCodexGenericHookNotifyBody(payload)
	notifyIn := attentionNotifyInput{
		ID:            codexHookNotifyID(payload, "generic"),
		Text:          body.Text,
		Severity:      body.Severity,
		Metadata:      mergeAINotifyBodyMetadata(payload.codexGenericHookMetadata(), body),
		Force:         true,
		SuppressHooks: true,
	}
	if !activationEligible {
		return c.applyAIStatusQueueOnlyWithoutActivation("waiting", paneID, notifyIn)
	}
	return c.applyAIStatusQueueOnly("waiting", paneID, notifyIn)
}

func parseCodexHookPayload(data []byte) (codexHookPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return codexHookPayload{}, fmt.Errorf("parse codex hook payload: %w", err)
	}
	payload := codexHookPayload{
		EventName:      firstString(raw, "hook_event_name", "event_name"),
		ThreadID:       firstString(raw, "thread_id", "thread-id"),
		SessionID:      firstString(raw, "session_id", "session-id"),
		TurnID:         firstString(raw, "turn_id", "turn-id"),
		CWD:            firstString(raw, "cwd", "workspace", "project_dir"),
		TranscriptPath: firstString(raw, "transcript_path", "transcriptPath"),
		Model:          firstString(raw, "model"),
		ToolName:       firstString(raw, "tool_name", "toolName"),
	}
	if payload.CWD == "" {
		payload.CWD = firstNestedString(raw["workspace"], "cwd", "path")
	}
	if payload.ToolName == "" {
		payload.ToolName = firstNestedString(raw["tool"], "name", "tool_name")
	}
	payload.ToolInput = mapFromAny(raw["tool_input"])
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["input"])
	}
	if len(payload.ToolInput) == 0 {
		payload.ToolInput = mapFromAny(raw["tool"])
		delete(payload.ToolInput, "name")
		delete(payload.ToolInput, "tool_name")
	}
	return payload, nil
}

func (p codexHookPayload) codexHookMetadata() map[string]string {
	metadata := map[string]string{
		notify.MetaAgent:  aiModeCodex,
		notify.MetaEvent:  p.EventName,
		"session_id":      p.SessionID,
		"thread_id":       p.matchThreadID(),
		"turn_id":         p.TurnID,
		"cwd":             p.CWD,
		"transcript_path": p.TranscriptPath,
		"model":           p.Model,
		"tool_name":       p.ToolName,
	}
	for key, value := range p.ToolInput {
		if text := stringFromAny(value); text != "" {
			metadata["tool_input."+key] = truncateRunes(text, 160)
		}
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if value := strings.TrimSpace(v); value != "" {
			out[k] = value
		}
	}
	return out
}

func (p codexHookPayload) codexGenericHookMetadata() map[string]string {
	metadata := map[string]string{
		"provider":       aiHookProviderCodex,
		notify.MetaAgent: aiModeCodex,
		notify.MetaEvent: p.EventName,
		"tool":           p.ToolName,
		"tool_name":      p.ToolName,
		"cwd":            p.CWD,
		"thread_id":      p.matchThreadID(),
		"session_id":     p.SessionID,
		"turn_id":        p.TurnID,
		"model":          p.Model,
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if value := strings.TrimSpace(v); value != "" {
			out[k] = value
		}
	}
	return out
}

func (p codexHookPayload) matchThreadID() string {
	if threadID := strings.TrimSpace(p.ThreadID); threadID != "" {
		return threadID
	}
	return p.SessionID
}

func codexHookNotifyID(p codexHookPayload, kind string) string {
	parts := []string{"ai", "codex", kind}
	if value := strings.TrimSpace(p.SessionID); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(p.TurnID); value != "" {
		parts = append(parts, value)
	}
	if kind == "permission" {
		if value := strings.TrimSpace(p.ToolName); value != "" {
			parts = append(parts, value)
		}
		if summary := formatCodexToolInputSummary(p.ToolName, p.ToolInput); summary != "" {
			parts = append(parts, truncateRunes(summary, 40))
		}
	}
	return strings.Join(parts, ":")
}
