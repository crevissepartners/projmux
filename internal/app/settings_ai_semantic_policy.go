package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func validAISemanticEvent(event config.AISemanticEvent) bool {
	return event == config.AISemanticApprovalRequired || event == config.AISemanticResponseComplete
}

func (c *settingsCommand) aiSemanticPoliciesPath() (string, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", err
	}
	return paths.AISemanticPoliciesFile(), nil
}

func (c *settingsCommand) currentAISemanticPolicies() config.AISemanticPolicies {
	path, err := c.aiSemanticPoliciesPath()
	if err != nil {
		return config.DefaultAISemanticPolicies()
	}
	policies, err := config.LoadAISemanticPoliciesFile(path)
	if err != nil {
		return config.DefaultAISemanticPolicies()
	}
	return policies
}

func (c *settingsCommand) setAISemanticPolicy(event config.AISemanticEvent, policy config.AISemanticPolicy, stdout io.Writer) error {
	if !validAISemanticEvent(event) || !config.ValidAISemanticPolicy(policy) {
		return fmt.Errorf("invalid Codex native semantic policy: %s=%s", event, policy)
	}
	path, err := c.aiSemanticPoliciesPath()
	if err != nil {
		return err
	}
	policies, err := config.LoadAISemanticPoliciesFile(path)
	if err != nil {
		return err
	}
	policies.Events[event] = policy
	if err := config.SaveAISemanticPoliciesFile(path, policies); err != nil {
		return err
	}
	if stdout != nil {
		format := localizeText(c.locale(), i18n.Key("settings.result.codex_native_semantic_policy"), "Codex native semantic policy: %s = %s\n")
		_, _ = fmt.Fprintf(stdout, format, semanticEventName(c.locale(), event), settingsCatalogTextLocale(c.locale(), semanticPolicyName(policy)))
	}
	return nil
}

func (c *settingsCommand) aiSemanticPolicyChoiceEntries(event config.AISemanticEvent) []intpickercompat.Entry {
	current := c.currentAISemanticPolicies().Events[event]
	entries := []intpickercompat.Entry{c.backEntry(), {
		Label: c.rowLabelInfo("Current", semanticPolicyName(current), "applies to native lifecycle and hook fallback"), Value: settingsNoopValue,
	}}
	for _, policy := range []config.AISemanticPolicy{config.AISemanticNotify, config.AISemanticStateOnly, config.AISemanticQuiet} {
		glyph, color := settingsGlyphInactive, settingsColorDim
		if policy == current {
			glyph, color = settingsGlyphToggle, settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(glyph, color, semanticPolicyName(policy), semanticPolicyDescription(policy)),
			Value:     settingsActionPrefixAISemanticSet + string(event) + ":" + string(policy),
			SearchKey: strings.Join([]string{"codex native", string(event), semanticPolicyName(policy), semanticPolicyDescription(policy)}, " "),
		})
	}
	return entries
}

func semanticPolicyName(policy config.AISemanticPolicy) string {
	switch policy {
	case config.AISemanticStateOnly:
		return "State only"
	case config.AISemanticQuiet:
		return "Quiet"
	default:
		return "Notify"
	}
}

func semanticPolicyDescription(policy config.AISemanticPolicy) string {
	switch policy {
	case config.AISemanticStateOnly:
		return "State only - badge only; queue and desktop off"
	case config.AISemanticQuiet:
		return "Quiet - badge, queue, and desktop off"
	default:
		return "Notify - badge, queue, and desktop on"
	}
}

func hookFallbackDescriptionLocale(locale i18n.Locale, provider, description string) string {
	if provider == aiHookProviderCodex {
		return settingsCatalogTextLocale(locale, "fallback only") + " - " + description
	}
	return description
}

func (c *settingsCommand) codexLifecycleAuthoritySummary() string {
	aggregate := c.codexLifecycleAuthorityAggregate()
	locale := c.locale()
	if aggregate.unavailable != "" {
		unavailable := aggregate.unavailable
		switch unavailable {
		case "no runtime observation":
			unavailable = localizeText(locale, i18n.Key("settings.text.codex_no_runtime_observation"), unavailable)
		case "tmux observation failed":
			unavailable = localizeText(locale, i18n.Key("settings.text.codex_tmux_observation_failed"), unavailable)
		}
		format := localizeText(locale, i18n.Key("settings.text.codex_lifecycle_unavailable"), "unavailable - %s - epoch unknown")
		return fmt.Sprintf(format, unavailable)
	}
	if aggregate.total == 0 {
		return localizeText(locale, i18n.Key("settings.text.codex_lifecycle_no_live_pane"), "provider-hook - no live Codex pane - epoch inactive")
	}
	sources := make([]string, 0, len(aggregate.sources))
	for _, source := range []string{codexAuthorityControlPlane, codexAuthorityInvalidating, codexAuthorityPending, codexAuthorityHook} {
		if count := aggregate.sources[source]; count > 0 {
			sources = append(sources, fmt.Sprintf("%s %d", source, count))
		}
	}
	sourceSummary := strings.Join(sources, ", ")
	if len(sources) > 1 {
		format := localizeText(locale, i18n.Key("settings.text.codex_lifecycle_mixed_sources"), "mixed (%s)")
		sourceSummary = fmt.Sprintf(format, sourceSummary)
	}
	reasonSummary := localizeText(locale, i18n.Key("settings.text.codex_lifecycle_bounded_reasons"), "bounded runtime reasons")
	if len(aggregate.reasons) == 1 {
		for reason := range aggregate.reasons {
			reasonSummary = reason
			if reason == "bounded reason unavailable" {
				reasonSummary = localizeText(locale, i18n.Key("settings.text.codex_bounded_reason_unavailable"), reason)
			}
		}
	}
	format := localizeText(locale, i18n.Key("settings.text.codex_lifecycle_epochs"), "%s - %s - epochs active %d, pending %d, inactive %d")
	return fmt.Sprintf(format, sourceSummary, reasonSummary, aggregate.active, aggregate.pending, aggregate.inactive)
}

func (c *settingsCommand) codexHookFallbackSummary() string {
	aggregate := c.codexLifecycleAuthorityAggregate()
	locale := c.locale()
	if aggregate.unavailable != "" {
		return localizeText(locale, i18n.Key("settings.text.codex_hook_status_unavailable"), "status unavailable; active only in provider-hook fallback")
	}
	if count := aggregate.sources[codexAuthorityHook]; count > 0 {
		format := localizeText(locale, i18n.Key("settings.text.codex_hook_active_counts"), "active on %d live Codex pane(s); inactive on %d")
		return fmt.Sprintf(format, count, aggregate.total-count)
	}
	format := localizeText(locale, i18n.Key("settings.text.codex_hook_inactive_count"), "inactive on %d live Codex pane(s)")
	return fmt.Sprintf(format, aggregate.total)
}

func semanticEventName(locale i18n.Locale, event config.AISemanticEvent) string {
	switch event {
	case config.AISemanticApprovalRequired:
		return localizeText(locale, i18n.KeyNotifyAIApprovalRequired, "Approval required")
	case config.AISemanticResponseComplete:
		return localizeText(locale, i18n.KeyNotifyAIResponseComplete, "Response complete")
	default:
		return string(event)
	}
}

func codexSemanticEventTitle(locale i18n.Locale, event config.AISemanticEvent) string {
	return fmt.Sprintf("%s - Codex - %s", settingsCatalogTextLocale(locale, "Agent event behavior"), semanticEventName(locale, event))
}

func codexSemanticEventPrompt(locale i18n.Locale, event config.AISemanticEvent) string {
	return settingsNotificationsAgentEventsPrompt(locale) + "Codex > " + semanticEventName(locale, event) + " > "
}

type codexAuthorityAggregate struct {
	sources     map[string]int
	reasons     map[string]bool
	total       int
	active      int
	pending     int
	inactive    int
	unavailable string
}

func (c *settingsCommand) codexLifecycleAuthorityAggregate() codexAuthorityAggregate {
	aggregate := codexAuthorityAggregate{sources: map[string]int{}, reasons: map[string]bool{}}
	runner := c.tmuxRunner
	if runner == nil {
		aggregate.unavailable = "no runtime observation"
		return aggregate
	}
	format := intmux.JoinFormats(intmux.FieldDelimiter, []string{
		intmux.PaneOptionFormat(aiPaneAgentOption),
		intmux.PaneOptionFormat(aiPaneCodexAuthorityOption),
		intmux.PaneOptionFormat(aiPaneCodexEpochOption),
		intmux.PaneOptionFormat(aiPaneCodexReasonOption),
	}...)
	out, err := runner.Run(context.Background(), "tmux", "list-panes", "-a", "-F", format)
	if err != nil {
		aggregate.unavailable = "tmux observation failed"
		return aggregate
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, intmux.FieldDelimiter)
		if len(parts) != 4 || strings.TrimSpace(parts[0]) != aiModeCodex {
			continue
		}
		source := safeCodexAuthorityValue(parts[1])
		epoch := strings.TrimSpace(parts[2])
		reason := safeCodexAuthorityReason(parts[3])
		aggregate.sources[source]++
		aggregate.reasons[reason] = true
		aggregate.total++
		if epoch != "" && (source == codexAuthorityControlPlane || source == codexAuthorityInvalidating) {
			aggregate.active++
		} else if source == codexAuthorityPending {
			aggregate.pending++
		} else {
			aggregate.inactive++
		}
	}
	return aggregate
}

func safeCodexAuthorityValue(value string) string {
	switch strings.TrimSpace(value) {
	case codexAuthorityPending, codexAuthorityControlPlane, codexAuthorityInvalidating, codexAuthorityHook:
		return strings.TrimSpace(value)
	default:
		return codexAuthorityHook
	}
}

// safeCodexAuthorityReason renders one stored authority reason. The vocabulary
// is owned by the observer that produces it; this is only the bounded read.
func safeCodexAuthorityReason(value string) string {
	if reason := codexObserverReasonFor(value); reason != "" {
		return string(reason)
	}
	return "bounded reason unavailable"
}
