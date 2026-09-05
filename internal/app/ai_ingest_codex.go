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
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Result: "error", Reason: err.Error()})
		return err
	}

	paneID, nativeRouted, nativeAllowed, nativeReason := c.routeNativeCodexHook(payload.matchThreadID())
	if nativeRouted && !nativeAllowed {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: nativeReason, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
		return nil
	}
	matchReason := ""
	if !nativeRouted {
		paneID, matchReason = c.matchAIPane(aiPaneMatchInput{
			ExplicitPane: explicitPane,
			CWD:          payload.CWD,
			ThreadID:     payload.matchThreadID(),
			SessionID:    payload.SessionID,
		})
	}
	if paneID == "" {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: matchReason, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	}
	authority, authorityErr := c.muxRunner().ShowPaneOption(context.Background(), paneID, aiPaneCodexAuthorityOption)
	if nativeRouted && (authorityErr != nil || authority != codexAuthorityHook) {
		reason := "native authority unavailable"
		if authorityErr == nil {
			reason = "provider-control-plane authority: " + authority
		}
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: reason, Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
		return nil
	}
	if !nativeRouted && codexAuthoritySuppressesHooks(authority) {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: "provider-control-plane authority: " + authority, Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
		return nil
	}
	if nativeRouted {
		release, err := c.acquireCodexAuthorityFence(c.env(internalActivationPaneUIDEnv))
		if err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
			return err
		}
		defer release()
		// SetAuthority owns the same fence. Revalidate after waiting so a hook
		// that observed provider-hook before an invalidation cannot commit any
		// Registry, tmux, queue, or desktop write after that invalidation.
		authority, err = c.muxRunner().ShowPaneOption(context.Background(), paneID, aiPaneCodexAuthorityOption)
		if err != nil || authority != codexAuthorityHook {
			reason := "native authority unavailable"
			if err == nil {
				reason = "provider-control-plane authority: " + authority
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: reason, Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
			return nil
		}
	}
	if allowed, reason := c.nativeCodexHookAllowed(paneID, payload.matchThreadID()); !allowed {
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "ignored", Reason: reason, Pane: paneID, ThreadID: payload.matchThreadID(), TurnID: payload.TurnID})
		return nil
	}
	c.stageCodexBinding(paneID, payload.matchThreadID(), payload.TurnID)
	defer c.flushPendingAgentSessionRef(paneID)

	metadata := payload.codexHookMetadata()
	action := c.aiHookEffectiveAction(aiHookProviderCodex, payload.EventName)
	switch payload.EventName {
	case "UserPromptSubmit":
		if action.Action == aiHookActionQuiet {
			c.quietCodexHook(paneID, payload, aiHookQuietReason(action))
			return nil
		}
		c.markAIHookPane(paneID, aiModeCodex, payload.CWD, payload.matchThreadID(), payload.SessionID, "")
		if err := c.applyAIStatusWithNotify("thinking", paneID, attentionNotifyInput{
			Metadata:  metadata,
			BadgeKind: aiBadgeKindInProgress,
		}); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
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
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		result, reason := codexHookSemanticLogResult(policy, rawOverride)
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: result, Reason: reason, Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
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
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		result, reason := codexHookSemanticLogResult(policy, rawOverride)
		c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: result, Reason: reason, Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
		return nil
	case "SessionStart":
		if _, _, err := c.persistManagedAgentStartupReadiness(paneID, aiModeCodex); err != nil {
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return err
		}
		if c.shouldPushGenericCodexHookNotify(action) {
			if err := c.pushGenericCodexHookNotifyWithoutActivation(paneID, payload); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		c.quietCodexHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	case "PreToolUse", "PostToolUse", "PreCompact", "PostCompact":
		if c.shouldPushGenericCodexHookNotify(action) {
			if err := c.pushGenericCodexHookNotify(paneID, payload); err != nil {
				c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "error", Reason: err.Error(), Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
				return err
			}
			c.appendAIIngestLog(aiIngestLogEntry{Source: "codex-hook", Event: payload.EventName, Result: "notify", Pane: paneID, CWD: payload.CWD, ThreadID: payload.matchThreadID(), SessionID: payload.SessionID, TurnID: payload.TurnID})
			return nil
		}
		c.quietCodexHook(paneID, payload, aiHookNoHandlerReason(action))
		return nil
	default:
		c.quietCodexHook(paneID, payload, aiHookNoHandlerReason(action))
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

	for _, field := range []struct{ option, value string }{
		{aiPaneStateOption, delivery.State},
		{aiPaneBadgeKindOption, delivery.Badge},
		{attentionStateOption, delivery.Attention},
	} {
		args := []string{"set-option", "-p", "-t", paneID, field.option, field.value}
		if field.value == "" {
			args = []string{"set-option", "-p", "-u", "-t", paneID, field.option}
		}
		if err := c.run("tmux", args...); err != nil {
			return err
		}
	}
	if !delivery.Notify {
		return nil
	}
	_ = c.notifyAIWithInput(paneID, input)
	c.notifyProducer().PushReplyReady(input)
	return nil
}

func (c *aiCommand) quietCodexHook(paneID string, payload codexHookPayload, reason string) {
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
