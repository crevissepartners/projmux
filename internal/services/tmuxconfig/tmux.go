package tmuxconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/services/keybinding"
	"github.com/crevissepartners/projmux/internal/theme"
	"strconv"
	"strings"
)

func TmuxStandaloneConfig(binaryPath string, decoration config.StatusbarDecoration) string {
	return TmuxStandaloneConfigWithKeymap(binaryPath, StatusbarDecorationSetFromGlobal(decoration), keybinding.DefaultKeyBindingCatalog(), false)
}

func StatusbarSettingsButton(label string, roles theme.RenderRoles) string {
	return "#[bold,fg=" + roles.ActionFg + ",bg=" + roles.ActionBg + "]#[range=user|settings]" + StatusbarSettingsButtonBody(label) + "#[norange]#[default]"
}

func StatusbarSettingsButtonBody(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = StatusbarSettingsIcon
	}
	if label == StatusbarSettingsIcon {
		return " " + label + "  "
	}
	return " " + label + " "
}

func StatusbarSessionLeftFormat(bin string, roles theme.RenderRoles) string {
	return "#[range=user|session]#[bold,fg=" + roles.IdentityFg + ",bg=" + roles.IdentityBg + "] #(" + bin + " internal status project) #[default]#[norange] "
}

func StatusbarClientWidthLessThan(n int, then, otherwise string) string {
	return "#{?#{e|<:" + StatusbarClientWidthFormat + "," + strconv.Itoa(n) + "}," + then + "," + otherwise + "}"
}

func StatusbarNotifyBudgetFormat() string {
	half := "#{e|/:" + StatusbarClientWidthFormat + ",2}"
	surplus := "#{e|-:" + StatusbarClientWidthFormat + "," + strconv.Itoa(StatusbarNotifySurplusFromWidth) + "}"
	return StatusbarClientWidthLessThan(StatusbarEvenSplitMaxWidth, half,
		StatusbarClientWidthLessThan(StatusbarNotifySurplusFromWidth+StatusbarNotifyMinWidth, strconv.Itoa(StatusbarNotifyMinWidth),
			StatusbarClientWidthLessThan(StatusbarNotifySurplusCapWidth, surplus, strconv.Itoa(StatusbarNotifyMaxWidth))))
}

func StatusbarUsageBudgetFormat() string {
	return "#{e|-:" + StatusbarClientWidthFormat + "," + StatusbarNotifyBudgetFormat() + "}"
}

func StatusbarAuxLineFormat(bin string) string {
	return "#[align=left range=user|notify]#(" + bin + " internal status notify --max-width " + StatusbarNotifyBudgetFormat() + ")#[norange]" +
		"#[align=right range=user|usage]#(" + bin + " internal status usage --max-width " + StatusbarUsageBudgetFormat() + ")#[norange]"
}

func StatusbarAuxLineFormatWithVisibility(bin string, visibility StatusbarHUDVisibilitySet) string {
	switch {
	case visibility.Visible(StatusbarHUDNotifications) && visibility.Visible(StatusbarHUDAgentUsage):
		return StatusbarAuxLineFormat(bin)
	case visibility.Visible(StatusbarHUDNotifications):
		return "#[align=left range=user|notify]#(" + bin + " internal status notify --max-width " + StatusbarClientWidthFormat + ")#[norange]"
	case visibility.Visible(StatusbarHUDAgentUsage):
		return "#[align=right range=user|usage]#(" + bin + " internal status usage --max-width " + StatusbarClientWidthFormat + ")#[norange]"
	}
	return ""
}

func StatusbarRowCount(visibility StatusbarHUDVisibilitySet) int {
	if visibility.AnyVisible() {
		return 2
	}
	return 1
}

func StatusbarRowCountOption(visibility StatusbarHUDVisibilitySet) string {
	if StatusbarRowCount(visibility) == 1 {
		// tmux spells its one-line status value "on"; numeric values start at 2.
		return "on"
	}
	return strconv.Itoa(StatusbarRowCount(visibility))
}

func StatusbarRowFormatLines(bin string, visibility StatusbarHUDVisibilitySet) []string {
	if visibility.AnyVisible() {
		return []string{
			"set -g status-format[0] " + keybinding.TmuxConfigQuote(StatusbarAuxLineFormatWithVisibility(bin, visibility)),
			"set -g status-format[1] " + keybinding.TmuxConfigQuote(StatusbarWindowLineFormat()),
			"set -gu status-format[2]",
		}
	}
	return []string{
		"set -g status-format[0] " + keybinding.TmuxConfigQuote(StatusbarWindowLineFormat()),
		"set -gu status-format[1]",
		"set -gu status-format[2]",
	}
}

func StatusbarWindowLineFormat() string {
	return "#[align=left range=left #{E:status-left-style}]#[push-default]#{T;=/#{status-left-length}:status-left}#[pop-default]#[norange default]" +
		"#[list=on align=#{status-justify}]#[list=left-marker]<#[list=right-marker]>#[list=on]" +
		"#{W:#[range=window|#{window_index} #{E:window-status-style}#{?#{&&:#{window_last_flag},#{!=:#{E:window-status-last-style},default}}, #{E:window-status-last-style},}#{?#{&&:#{window_bell_flag},#{!=:#{E:window-status-bell-style},default}}, #{E:window-status-bell-style},#{?#{&&:#{||:#{window_activity_flag},#{window_silence_flag}},#{!=:#{E:window-status-activity-style},default}}, #{E:window-status-activity-style},}}]#[push-default]#{T:window-status-format}#[pop-default]#[norange default]#{?window_end_flag,,#{window-status-separator}}," +
		"#[range=window|#{window_index} list=focus #{?#{!=:#{E:window-status-current-style},default},#{E:window-status-current-style},#{E:window-status-style}}#{?#{&&:#{window_last_flag},#{!=:#{E:window-status-last-style},default}}, #{E:window-status-last-style},}#{?#{&&:#{window_bell_flag},#{!=:#{E:window-status-bell-style},default}}, #{E:window-status-bell-style},#{?#{&&:#{||:#{window_activity_flag},#{window_silence_flag}},#{!=:#{E:window-status-activity-style},default}}, #{E:window-status-activity-style},}}]#[push-default]#{T:window-status-current-format}#[pop-default]#[norange list=on default]#{?window_end_flag,,#{window-status-separator}}}" +
		"#[nolist align=right range=right #{E:status-right-style}]#[push-default]#{T;=/#{status-right-length}:status-right}#[pop-default]#[norange default]"
}

func StatusbarWindowTitleFormat() string {
	return TmuxCenteredWindowNameFormat(tmuxWindowTitleWidth)
}

func TmuxVisiblePaneLabelFormat() string {
	label := "#{" + paneLabelOption + "}"
	return "#{?#{!=:" + label + ",}," + label + ",#{?#{&&:#{!=:#{@projmux_ai_agent},},#{!=:#{@projmux_ai_topic},}},#{@projmux_ai_topic}," + TmuxShellPaneLabelFormat() + "}}"
}

func TmuxStyledVisiblePaneLabelFormat() string {
	return TmuxStyledVisiblePaneLabelFormatFor(theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{})).StateProgress)
}

func TmuxStyledVisiblePaneLabelFormatFor(styleFg string) string {
	label := "#{" + paneLabelOption + "}"
	topic := "#{@projmux_ai_topic}"
	return "#{?#{!=:" + label + ",}," + label + ",#{?#{&&:#{!=:#{@projmux_ai_agent},},#{!=:" + topic + ",}}," + TmuxStyledAITopicFormat(topic, styleFg) + "," + TmuxShellPaneLabelFormat() + "}}"
}

func TmuxStyledAITopicFormat(topic, styleFg string) string {
	return "#{?" + TmuxLeadModeTopicMatchFormat(topic) + ",#[push-default]#[bold#,fg=" + styleFg + "]" + topic + "#[pop-default]," + topic + "}"
}

func TmuxLeadModeTopicMatchFormat(topic string) string {
	qa := "#{m/r:^\\\\[[Ll]ead:[Qq][Aa]\\\\]," + topic + "}"
	poc := "#{m/r:^\\\\[[Ll]ead:[Pp][Oo][Cc]\\\\]," + topic + "}"
	ship := "#{m/r:^\\\\[[Ll]ead:[Ss]hip\\\\]," + topic + "}"
	roadmap := "#{m/r:^\\\\[[Ll]ead:[Rr]oadmap\\\\]," + topic + "}"
	return "#{||:#{||:" + qa + "," + poc + "},#{||:" + ship + "," + roadmap + "}}"
}

func TmuxShellPaneLabelFormat() string {
	return "#{?#{||:#{||:#{||:#{==:#{pane_current_command},zsh},#{==:#{pane_current_command},bash}},#{||:#{==:#{pane_current_command},fish},#{==:#{pane_current_command},sh}}},#{||:#{==:#{pane_current_command},nu},#{==:#{pane_current_command},xonsh}}},#{pane_current_command},#{pane_title}}"
}

func TmuxPaneBorderFormat() string {
	return TmuxPaneBorderFormatWithAIBadgeStyle(config.AIBadgeStyleDot, theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{})))
}

func TmuxPaneBorderFormatWithAIBadgeStyle(badgeStyle config.AIBadgeStyle, roles theme.RenderRoles) string {
	badgeStyle = config.NormalizeAIBadgeStyle(string(badgeStyle))
	activePaneLabelFormat := TmuxVisiblePaneLabelFormat()
	promptPaneLabelFormat := TmuxStyledVisiblePaneLabelFormatFor(roles.AIActionRequired)
	busyPaneLabelFormat := TmuxStyledVisiblePaneLabelFormatFor(roles.AIProgress)
	replyPaneLabelFormat := TmuxStyledVisiblePaneLabelFormatFor(roles.AISuccess)
	mutedPaneLabelFormat := TmuxStyledVisiblePaneLabelFormatFor(roles.PaneBorderMutedFg)
	panePromptFormat := "#{||:#{==:#{@projmux_ai_badge_kind}," + aiBadgeKindApprovalRequired + "},#{==:#{@projmux_ai_badge_kind}," + aiBadgeKindInputRequired + "}}"
	paneCompleteFormat := "#{==:#{@projmux_ai_badge_kind}," + aiBadgeKindResponseComplete + "}"
	paneProgressFormat := "#{==:#{@projmux_ai_badge_kind}," + aiBadgeKindInProgress + "}"
	paneBusyFormat := "#{==:#{@projmux_attention_state},busy}"
	paneReplyFormat := "#{==:#{@projmux_attention_state},reply}"
	inactivePaneBorderFormat := "#{?" + panePromptFormat + ",#[bold#,fg=" + roles.AIActionRequired + "]" + TmuxAIBadgeMarker(aiBadgeKindApprovalRequired, badgeStyle) + promptPaneLabelFormat + " #[default],#{?" + paneCompleteFormat + ",#[bold#,fg=" + roles.AISuccess + "]" + TmuxAIBadgeMarker(aiBadgeKindResponseComplete, badgeStyle) + replyPaneLabelFormat + " #[default],#{?#{||:" + paneProgressFormat + "," + paneBusyFormat + "},#[bold#,fg=" + roles.AIProgress + "]" + TmuxAIBadgeMarker(aiBadgeKindInProgress, badgeStyle) + busyPaneLabelFormat + " #[default],#{?" + paneReplyFormat + ",#[bold#,fg=" + roles.AISuccess + "]" + TmuxAIBadgeMarker(aiBadgeKindResponseComplete, badgeStyle) + replyPaneLabelFormat + " #[default],#[fg=" + roles.PaneBorderMutedFg + "] " + mutedPaneLabelFormat + " #[default]}}}}"
	return "#{?pane_active,#[bold#,fg=" + roles.PaneTopicChipFg + "#,bg=" + roles.PaneTopicChipBg + "] > " + activePaneLabelFormat + " #[default]," + inactivePaneBorderFormat + "}"
}

func TmuxAIBadgeMarker(kind string, badgeStyle config.AIBadgeStyle) string {
	glyph := aibadge.Glyph(kind, string(badgeStyle))
	return " " + glyph + " "
}

func TmuxCenteredWindowNameFormat(width int) string {
	if width <= 0 {
		return ""
	}
	trimWidth := max(width-3, 1)
	fallback := "#{=/" + strconv.Itoa(trimWidth) + "/...:window_name}"
	for n := width; n >= 0; n-- {
		left := (width - n) / 2
		right := width - n - left
		body := strings.Repeat(" ", left) + "#{=/" + strconv.Itoa(width) + "/...:window_name}" + strings.Repeat(" ", right)
		fallback = "#{?#{==:#{n:window_name}," + strconv.Itoa(n) + "}," + body + "," + fallback + "}"
	}
	return fallback
}

func TmuxWindowStatusFormats(binaryPath string, effective theme.EffectiveTheme) (string, string) {
	bin := keybinding.TmuxShellQuote(binaryPath)
	tokens := theme.TmuxRenderTokensFromEffective(effective)
	return "#[fg=" + tokens.WindowInactiveFg + ",bg=" + tokens.WindowInactiveBg + "] #(" + bin + " attention window #{window_id} #{@projmux_ai_badge_style})#[fg=" + tokens.WindowInactiveFg + "] #I " + StatusbarWindowTitleFormat() + " #[default]",
		"#[bold,fg=" + tokens.WindowActiveFg + ",bg=" + tokens.WindowActiveBg + "] #(" + bin + " attention window #{window_id} #{@projmux_ai_badge_style})#[fg=" + tokens.WindowActiveFg + "] #I " + StatusbarWindowTitleFormat() + " #[default]"
}

func TmuxStandaloneConfigWithKeymap(binaryPath string, decorations StatusbarDecorationSet, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	return FallbackRenderThemeSource().TmuxStandaloneConfig(binaryPath, decorations, catalog, keymapPresent)
}

func TmuxStandaloneConfigWithKeymapTheme(binaryPath string, decorations StatusbarDecorationSet, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	return TmuxStandaloneConfigWithKeymapThemeAndAIBadgeStyle(binaryPath, decorations, config.AIBadgeStyleDot, catalog, keymapPresent, effective)
}

func TmuxStandaloneConfigWithKeymapThemeAndAIBadgeStyle(binaryPath string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, decorations, badgeStyle, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, catalog, keymapPresent, effective)
}

func TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibilityImpl(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, DefaultStatusbarHUDVisibilitySet(), DefaultStatusbarRowOneVisibilitySet(), catalog, keymapPresent, effective)
}

func TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	if hudVisibility.IsDefault() {
		return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, catalog, keymapPresent, effective)
	}
	return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibilityImpl(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, DefaultStatusbarRowOneVisibilitySet(), catalog, keymapPresent, effective)
}

func TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, rowOneVisibility StatusbarRowOneVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	if rowOneVisibility.IsDefault() {
		return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, catalog, keymapPresent, effective)
	}
	return TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibilityImpl(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, rowOneVisibility, catalog, keymapPresent, effective)
}

func TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibilityImpl(binaryPath string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, rowOneVisibility StatusbarRowOneVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	bin := keybinding.TmuxShellQuote(binaryPath)
	roles := theme.RenderRolesFromEffective(effective)
	windowStatusFormat, windowStatusCurrentFormat := TmuxWindowStatusFormats(binaryPath, effective)
	defaultStandaloneKeyBindings := keybinding.KeyBindingCatalogForScope(keybinding.KeyBindingScopeStandalone)
	standaloneKeyBindings := keybinding.KeyBindingCatalogForScopeFrom(catalog, keybinding.KeyBindingScopeStandalone)
	badgeStyle = config.NormalizeAIBadgeStyle(string(badgeStyle))
	desktopNotifyMode = config.NormalizeDesktopNotifyMode(string(desktopNotifyMode))
	liveResourcesMode = config.NormalizeLiveResourcesMode(string(liveResourcesMode))
	hudVisibility.Notifications = config.NormalizeStatusbarVisibility(string(hudVisibility.Notifications))
	hudVisibility.AgentUsage = config.NormalizeStatusbarVisibility(string(hudVisibility.AgentUsage))
	rowOneVisibility.Project = config.NormalizeStatusbarVisibility(string(rowOneVisibility.Project))
	rowOneVisibility.WorkingDirectory = config.NormalizeStatusbarVisibility(string(rowOneVisibility.WorkingDirectory))
	rowOneVisibility.Git = config.NormalizeStatusbarVisibility(string(rowOneVisibility.Git))
	rowOneVisibility.Clock = config.NormalizeStatusbarVisibility(string(rowOneVisibility.Clock))
	rowOneVisibility.SettingsLauncher = config.NormalizeStatusbarVisibility(string(rowOneVisibility.SettingsLauncher))
	lines := []string{
		"# Generated by projmux. Safe to source from ~/.tmux.conf.",
		"set -as terminal-features \",xterm*:RGB\"",
		"set -g " + StatusbarDecorationTmuxOption + " " + string(config.NormalizeStatusbarDecoration(string(decorations.Cwd))),
		"set -g " + StatusbarDecorationCwdTmuxOption + " " + string(config.NormalizeStatusbarDecoration(string(decorations.Cwd))),
		"set -g " + StatusbarDecorationGitTmuxOption + " " + string(config.NormalizeStatusbarDecoration(string(decorations.Git))),
		"set -g " + StatusbarDecorationNotifyTmuxOption + " " + string(config.NormalizeStatusbarDecoration(string(decorations.Notify))),
		"set -g " + AiBadgeStyleTmuxOption + " " + string(badgeStyle),
		"set -g " + DesktopNotifyModeTmuxOption + " " + string(desktopNotifyMode),
		"set -g " + LiveResourcesTmuxOption + " " + string(liveResourcesMode),
	}
	lines = append(lines,
		"set-hook -g pane-focus-out "+keybinding.TmuxConfigQuote("run-shell -b "+keybinding.TmuxConfigQuote(bin+" attention arm #{hook_pane} >/dev/null 2>&1 || true")),
		"set-hook -g pane-focus-in "+keybinding.TmuxConfigQuote("run-shell -b "+keybinding.TmuxConfigQuote(bin+" attention clear #{hook_pane} >/dev/null 2>&1 || true")),
		"set-hook -g after-select-pane "+keybinding.TmuxConfigQuote("run-shell -b "+keybinding.TmuxConfigQuote(bin+" attention clear #{pane_id} >/dev/null 2>&1 || true")),
		"set-hook -g pane-exited "+keybinding.TmuxConfigQuote(TmuxPaneExitHookBody(bin, ControllerTriggerPaneExited)),
		"set-hook -g pane-died "+keybinding.TmuxConfigQuote(TmuxPaneExitHookBody(bin, ControllerTriggerPaneExited)),
		"set-hook -g after-kill-pane "+keybinding.TmuxConfigQuote(TmuxPaneExitHookBody(bin, ControllerTriggerPaneKilled)),
		"set-hook -g window-unlinked "+keybinding.TmuxConfigQuote(TmuxWindowUnlinkedHookBody(bin)),
	)
	lines = append(lines, TmuxRecentWindowRecordHookLines(bin)...)
	lines = append(lines,
		"set -g window-status-format "+keybinding.TmuxConfigQuote(windowStatusFormat),
		"set -g window-status-current-format "+keybinding.TmuxConfigQuote(windowStatusCurrentFormat),
		"set -g status "+StatusbarRowCountOption(hudVisibility),
		"set -g status-left-length 20",
		"set -g status-right-length 140",
		"set -g status-left "+keybinding.TmuxConfigQuote(StatusbarRowOneProjectFormat(bin, roles, rowOneVisibility)),
		"set -g status-right "+keybinding.TmuxConfigQuote(StatusbarRowOneRightFormat(bin, roles, liveResourcesMode, rowOneVisibility, StatusbarSettingsIcon+" projmux")),
	)
	lines = append(lines, StatusbarRowFormatLines(bin, hudVisibility)...)
	// Always record generated sequence state. If keymap.toml was removed
	// entirely, the empty current trie must still overwrite the roots/tables
	// recorded by the last successful source.
	lines = append(lines, TmuxSequenceStateLines(standaloneKeyBindings)...)
	if keymapPresent {
		lines = append(lines, keybinding.TmuxMergedUnbindLines(defaultStandaloneKeyBindings, standaloneKeyBindings)...)
	} else {
		lines = append(lines, keybinding.TmuxUnbindLines(standaloneKeyBindings)...)
	}
	lines = append(lines, keybinding.TmuxRetiredKeyUnbindLines()...)
	lines = append(lines, keybinding.TmuxBindLines(binaryPath, standaloneKeyBindings)...)
	lines = append(lines, TmuxSequenceBindLines(binaryPath, standaloneKeyBindings)...)
	lines = append(lines, TmuxStatusbarKeyBindings(binaryPath)...)
	lines = append(lines, TmuxPaneContextMenuBindings(binaryPath)...)
	return strings.Join(lines, "\n") + "\n"
}

func TmuxPaneExitHookBody(bin string, reason ControllerTriggerReason) string {
	return "run-shell -b " + keybinding.TmuxConfigQuote(
		"sleep 0.05; "+bin+" internal tmux rebalance-panes >/dev/null 2>&1 || true; "+
			ControllerTriggerHookCommand(bin, reason)+" >/dev/null 2>&1 || true")
}

func TmuxWindowUnlinkedHookBody(bin string) string {
	return "run-shell -b " + keybinding.TmuxConfigQuote(
		"sleep 0.05; "+ControllerTriggerHookCommand(bin, ControllerTriggerWindowUnlinked)+" >/dev/null 2>&1 || true")
}

func ControllerTriggerHookCommand(bin string, reason ControllerTriggerReason) string {
	// Hook children inherit TMUX from tmux itself, but not a stable TMUX_PANE.
	// This route already carries tmux-expanded physical socket and event handles;
	// strip the ambient client variables so the controller cannot mistake an
	// incomplete inherited receipt for authority or fall back to another Pane.
	command := "env -u TMUX -u TMUX_PANE " + bin + " internal tmux converge --socket-path '#{socket_path}'"
	switch reason {
	case ControllerTriggerPaneExited:
		command += " --reason " + string(reason) + " --hook-pane '#{hook_pane}'"
	case ControllerTriggerWindowUnlinked:
		command += " --session '#{hook_session}' --reason " + string(reason) + " --hook-window '#{hook_window}'"
	default:
		command += " --session '#{session_id}' --reason " + string(reason)
	}
	return command
}

func TmuxRuntimeCreatedHookBody(bin string) string {
	return "run-shell " + keybinding.TmuxConfigQuote(
		ControllerTriggerHookCommand(bin, ControllerTriggerRuntimeCreated)+" >/dev/null 2>&1 || true")
}

func TmuxRecentWindowRecordHookLines(bin string) []string {
	command := bin + " window record >/dev/null 2>&1 || true"
	body := "run-shell -b " + keybinding.TmuxConfigQuote(command)
	return []string{
		"set-hook -g after-select-window " + keybinding.TmuxConfigQuote(body),
		"set-hook -g client-session-changed " + keybinding.TmuxConfigQuote(body),
		"run-shell -b " + keybinding.TmuxConfigQuote(command),
	}
}

func TmuxAppConfig(binaryPath, defaultShell string, decoration config.StatusbarDecoration) string {
	return TmuxAppConfigWithKeymap(binaryPath, defaultShell, StatusbarDecorationSetFromGlobal(decoration), keybinding.DefaultKeyBindingCatalog(), false)
}

func TmuxAppConfigWithKeymap(binaryPath, defaultShell string, decorations StatusbarDecorationSet, catalog []keybinding.KeyBindingAction, keymapPresent bool) string {
	return FallbackRenderThemeSource().TmuxAppConfig(binaryPath, defaultShell, decorations, catalog, keymapPresent)
}

func TmuxAppConfigWithKeymapTheme(binaryPath, defaultShell string, decorations StatusbarDecorationSet, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	return TmuxAppConfigWithKeymapThemeAndAIBadgeStyle(binaryPath, defaultShell, decorations, config.AIBadgeStyleDot, catalog, keymapPresent, effective)
}

func TmuxAppConfigWithKeymapThemeAndAIBadgeStyle(binaryPath, defaultShell string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, defaultShell, decorations, badgeStyle, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, catalog, keymapPresent, effective)
}

func TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, defaultShell string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibilityImpl(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, DefaultStatusbarHUDVisibilitySet(), DefaultStatusbarRowOneVisibilitySet(), catalog, keymapPresent, effective)
}

func TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, defaultShell string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	if hudVisibility.IsDefault() {
		return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, catalog, keymapPresent, effective)
	}
	return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibilityImpl(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, DefaultStatusbarRowOneVisibilitySet(), catalog, keymapPresent, effective)
}

func TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath, defaultShell string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, rowOneVisibility StatusbarRowOneVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	if rowOneVisibility.IsDefault() {
		return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, catalog, keymapPresent, effective)
	}
	return TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibilityImpl(binaryPath, defaultShell, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, rowOneVisibility, catalog, keymapPresent, effective)
}

func TmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibilityImpl(binaryPath, defaultShell string, decorations StatusbarDecorationSet, badgeStyle config.AIBadgeStyle, desktopNotifyMode config.DesktopNotifyMode, liveResourcesMode config.LiveResourcesMode, hudVisibility StatusbarHUDVisibilitySet, rowOneVisibility StatusbarRowOneVisibilitySet, catalog []keybinding.KeyBindingAction, keymapPresent bool, effective theme.EffectiveTheme) string {
	bin := keybinding.TmuxShellQuote(binaryPath)
	tokens := theme.TmuxRenderTokensFromEffective(effective)
	roles := theme.RenderRolesFromEffective(effective)
	shell := keybinding.TmuxConfigQuote(NonEmpty(strings.TrimSpace(defaultShell), fallbackInteractiveShell))
	paneLabelFormat := TmuxVisiblePaneLabelFormat()
	paneBorderFormat := TmuxPaneBorderFormatWithAIBadgeStyle(badgeStyle, roles)
	lines := []string{
		"# Generated by projmux. Used by `projmux shell`.",
		"set -g @projmux_app 1",
		"set -g default-terminal \"tmux-256color\"",
		"set -as terminal-features \",xterm*:RGB\"",
		"set -g mouse on",
		"set -g history-limit 10000",
		"set -g set-clipboard on",
		"set -g default-shell " + shell,
		"set -g default-command \"\"",
		"set -ga update-environment \"WSL_DISTRO_NAME\"",
		"set -ga update-environment \"WSL_INTEROP\"",
		"set -ga update-environment \"WSLENV\"",
		"set -ga update-environment \"VSCODE_IPC_HOOK_CLI\"",
		"set -ga update-environment \"VSCODE_GIT_IPC_HANDLE\"",
		"set -ga update-environment \"VSCODE_GIT_ASKPASS_NODE\"",
		"set -ga update-environment \"VSCODE_GIT_ASKPASS_MAIN\"",
		"set -ga update-environment \"VSCODE_GIT_ASKPASS_EXTRA_ARGS\"",
		"set -ga update-environment \"VSCODE_INJECTION\"",
		"set -ga update-environment \"TERM_PROGRAM\"",
		"set -ga update-environment \"TERM_PROGRAM_VERSION\"",
		"set -ga update-environment \"PROJMUX_WELCOME\"",
		"set -g status on",
		"set -g status-position bottom",
		"set -g status-interval 5",
		"set -g status-keys vi",
		"set -g status-left-length 20",
		"set -g status-right-length 140",
		"set -g window-status-separator \" \"",
		"set -g allow-rename off",
		"set -g automatic-rename on",
		"set -g automatic-rename-format " + keybinding.TmuxConfigQuote(paneLabelFormat),
		"set -g mode-keys vi",
		"set -sg escape-time 100",
		"set -g status-style \"bg=" + tokens.StatusBg + ",fg=" + tokens.StatusFg + "\"",
		"set -g message-style \"bg=" + theme.TmuxMessageBg + ",fg=" + theme.TmuxMessageFg + ",bold\"",
		"set -g message-command-style \"bg=" + theme.TmuxMessageBg + ",fg=" + theme.TmuxMessageFg + ",bold\"",
		"set -g pane-border-style \"fg=" + roles.PaneBorder + "\"",
		"set -g pane-active-border-style \"fg=" + roles.FocusBorder + ",bold\"",
		"set -g pane-border-status top",
		// Active-pane tint (Phase 2 spike decision: window-active-style tint,
		// not a label chip). tmux only tints the active window's panes via
		// window-active-style; window-style sets the inactive-pane body bg.
		// Phase 6b+: window-style follows the `background` token via
		// PaneInactiveBg (fallback "default"), separating the general pane body
		// from popup/native frame and status backgrounds.
		"set -g window-style \"bg=" + roles.PaneInactiveBg + "\"",
		"set -g window-active-style \"bg=" + roles.FocusPaneActiveBg + "\"",
		"set -g pane-border-format " + keybinding.TmuxConfigQuote(paneBorderFormat),
	}
	lines = append(lines, strings.Split(strings.TrimSpace(TmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath, decorations, badgeStyle, desktopNotifyMode, liveResourcesMode, hudVisibility, rowOneVisibility, catalog, keymapPresent, effective)), "\n")[1:]...)
	// The creation hooks are app-config only, and the asymmetry with the two
	// exit hooks above is deliberate. A convergence caused by a *new* runtime
	// object mints and rebinds -- it adopts an unmarked window inside a managed
	// enclosure -- and on the app-owned server every session is projmux's own, so
	// there is nothing there to adopt by accident. The standalone snippet is
	// sourced from the operator's `~/.tmux.conf` and therefore runs on every
	// server they start, where a raw `new-window` in a session projmux does not
	// own must stay an unmanaged runtime object that only the Runtime diagnostics
	// surface shows.
	lines = append(lines,
		"set-hook -g after-new-window "+keybinding.TmuxConfigQuote(TmuxRuntimeCreatedHookBody(bin)),
		"set-hook -g after-split-window "+keybinding.TmuxConfigQuote(TmuxRuntimeCreatedHookBody(bin)),
	)
	lines = append(lines,
		"set-hook -g client-attached "+keybinding.TmuxConfigQuote("run-shell -b "+keybinding.TmuxConfigQuote(bin+" welcome --popup >/dev/null 2>&1")),
	)
	// The managed stock menus render before the catalog key bindings, so a
	// keymap that deliberately puts a managed action on `prefix <` or `>` still
	// owns that key.
	lines = append(lines, TmuxManagedStockMenuBindings(binaryPath)...)
	lines = append(lines, TmuxAppKeyBindings(binaryPath, catalog, keymapPresent)...)
	// Two-line status bar:
	//   [0] notify HUD (left) + usage HUD (right)
	//   [1] existing session/window/path/git/clock row
	// We re-assert `status 2` here because `tmuxStandaloneConfig` already
	// sets it, but a tmux server that previously ran an older projmux build
	// may have stuck a leftover `status-format[1]` showing pane debug info —
	// we explicitly overwrite the line so it always reflects this build's
	// intent. `set -gu status-format[2]` clears any stale third row inherited
	// from the previous three-line layout. Both segments' width budgets are
	// derived from `#{client_width}` by statusbarAuxLineFormat rather than
	// hardcoded, so a wide terminal is no longer rendered against a narrow
	// terminal's cap.
	lines = append(lines,
		"set -g status "+StatusbarRowCountOption(hudVisibility),
		"set -g status-left "+keybinding.TmuxConfigQuote(StatusbarRowOneProjectFormat(bin, roles, rowOneVisibility)),
		"set -g status-right "+keybinding.TmuxConfigQuote(StatusbarRowOneRightFormat(bin, roles, liveResourcesMode, rowOneVisibility, StatusbarSettingsIcon)),
	)
	lines = append(lines, StatusbarRowFormatLines(bin, hudVisibility)...)
	return WithTmuxConfigDigest(strings.Join(lines, "\n") + "\n")
}

func WithTmuxConfigDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	return body + "set -g " + TmuxConfigDigestOption + " " + digest + "\n"
}

func TmuxAppKeyBindings(binaryPath string, catalog []keybinding.KeyBindingAction, keymapPresent bool) []string {
	defaultAppKeyBindings := keybinding.KeyBindingCatalogForScope(keybinding.KeyBindingScopeApp)
	appKeyBindings := keybinding.KeyBindingCatalogForScopeFrom(catalog, keybinding.KeyBindingScopeApp)
	allKeyBindings := keybinding.FilterKeyBindingActions(catalog, func(action keybinding.KeyBindingAction) bool {
		return action.Kind != keybinding.KeyBindingActionPickerInternal
	})
	var lines []string
	// The app config embeds the standalone config first. Record sequence state
	// once more with the combined scope so shared prefixes spanning a
	// standalone and app action compile into one trie rather than whichever
	// scope happened to render last. This is unconditional so deleting the
	// keymap also overwrites the previously recorded generated state.
	lines = append(lines, TmuxSequenceStateLines(allKeyBindings)...)
	if keymapPresent {
		lines = append(lines, keybinding.TmuxMergedUnbindLines(defaultAppKeyBindings, appKeyBindings)...)
	} else {
		lines = append(lines, keybinding.TmuxUnbindLines(appKeyBindings)...)
	}
	lines = append(lines, keybinding.TmuxRetiredKeyUnbindLines()...)
	lines = append(lines, keybinding.TmuxStockPrefixRestoreLines(defaultAppKeyBindings, appKeyBindings)...)
	lines = append(lines, keybinding.TmuxBindLines(binaryPath, appKeyBindings)...)
	lines = append(lines, TmuxSequenceBindLines(binaryPath, allKeyBindings)...)
	return lines
}

func TmuxStatusbarKeyBindings(binaryPath string) []string {
	bin := keybinding.TmuxShellQuote(binaryPath)
	clickCmd := bin + " internal statusbar click \"#{mouse_status_range}\" --client \"#{client_tty}\""
	// Use tmux's `{...}` block syntax for the if-shell branches so the nested
	// quotes inside `run-shell` don't need to be escaped through another layer
	// of tmux config quoting (which the parser rejects). Block syntax requires
	// tmux 3.0+; projmux already enforces tmux >= 3.4 via the doctor check.
	mouseDownBind := "bind-key -n MouseDown1Status if-shell -F " +
		keybinding.TmuxConfigQuote("#{==:#{mouse_status_range},window}") + " " +
		"{ select-window -t = } " +
		"{ run-shell " + keybinding.TmuxConfigQuote(clickCmd) + " }"
	// The keyboard table carries `#{client_tty}` for the same reason the mouse
	// binding does: a status action's popup and its refusal both belong to the
	// client that pressed the key, and on a session with two clients attached
	// the invoking client is the only handle that stays correct.
	statusKey := func(key, action string) string {
		return "bind-key -T projmux-status " + key + " run-shell " +
			keybinding.TmuxConfigQuote(bin+" internal statusbar "+action+" --client \"#{client_tty}\"")
	}
	return []string{
		"unbind-key -q -n MouseDown1Status",
		mouseDownBind,
		"bind-key s switch-client -T projmux-status",
		statusKey("u", "click usage"),
		statusKey("r", "usage-refresh"),
		statusKey("n", "click notify"),
		statusKey("g", "click git"),
		statusKey("p", "click pwd"),
		statusKey("s", "click session"),
	}
}

func TmuxPaneContextMenuBindings(binaryPath string) []string {
	// Reuse the keybinding catalog's renderer so the menu action stays
	// byte-identical to the C-r binding's proven run-shell line.
	resumeAction := keybinding.RenderTmuxBindingBody(binaryPath, keybinding.KeyBindingAction{
		TmuxKind: keybinding.TmuxBindingPopupToggle,
		TmuxBody: "ai-split-resume-right",
	})
	bin := keybinding.TmuxShellQuote(binaryPath)
	// Guard + title + dim conditions mirror tmux 3.4 key-bindings.c: swap
	// entries are dimmed (`-` prefix) in single-pane windows, Mark/Unmark and
	// Zoom/Unzoom toggle with pane/window state.
	menu := "bind-key -n MouseDown3Pane if-shell -F -t = " +
		keybinding.TmuxConfigQuote("#{||:#{mouse_any_flag},#{&&:#{pane_in_mode},#{?#{m/r:(copy|view)-mode,#{pane_mode}},0,1}}}") + " " +
		"{ select-pane -t = ; send-keys -M } " +
		"{ display-menu -T " + keybinding.TmuxConfigQuote("#[align=centre]#{pane_index} (#{pane_id})") + " -t = -x M -y M " +
		keybinding.TmuxConfigQuote("AI Resume Picker") + " a { select-pane -t = ; " + resumeAction + " } " +
		"'' " +
		keybinding.TmuxConfigQuote("Horizontal Split") + " h { " + TmuxPaneMenuAction(bin, "split-right") + " } " +
		keybinding.TmuxConfigQuote("Vertical Split") + " v { " + TmuxPaneMenuAction(bin, "split-down") + " } " +
		"'' " +
		keybinding.TmuxConfigQuote("#{?#{>:#{window_panes},1},,-}Swap Up") + " u { swap-pane -U } " +
		keybinding.TmuxConfigQuote("#{?#{>:#{window_panes},1},,-}Swap Down") + " d { swap-pane -D } " +
		"'' " +
		keybinding.TmuxConfigQuote("Kill") + " X { " + TmuxPaneMenuKill(bin) + " } " +
		keybinding.TmuxConfigQuote("#{?pane_marked,Unmark,Mark}") + " m { select-pane -m } " +
		keybinding.TmuxConfigQuote("#{?#{>:#{window_panes},1},,-}#{?window_zoomed_flag,Unzoom,Zoom}") + " z { resize-pane -Z } }"
	return []string{
		"unbind-key -q -n MouseDown3Pane",
		menu,
	}
}

func TmuxPaneMenuAction(bin, action string) string {
	return "run-shell " + keybinding.TmuxConfigQuote(bin+" internal tmux pane-menu --client #{client_tty} "+action+" #{pane_id}")
}

func TmuxPaneMenuKill(bin string) string {
	return "if-shell -F " + keybinding.TmuxConfigQuote(keybinding.ManagedDeletePaneGuard) + " { " + TmuxPaneMenuAction(bin, "kill") + " } { " + TmuxStockMenuKillPane + " }"
}

func TmuxManagedStockMenuBindings(binaryPath string) []string {
	bin := keybinding.TmuxShellQuote(binaryPath)
	catalog := keybinding.DefaultKeyBindingCatalog()
	rename, _ := keybinding.KeyBindingActionByID(catalog, "rename-window")
	create, _ := keybinding.KeyBindingActionByID(catalog, "new-window")
	windowKill := keybinding.RenderTmuxBindingBody(binaryPath, keybinding.KeyBindingAction{
		TmuxKind:         keybinding.TmuxBindingManagedDelete,
		TmuxManagedGuard: keybinding.ManagedDeleteWindowGuard,
		TmuxBody:         ManagedMenuWindowDeleteRoute,
		TmuxStockBody:    TmuxStockMenuKillWindow,
	})
	item := func(name, key, command string) string {
		return keybinding.TmuxConfigQuote(name) + " " + key + " { " + command + " }"
	}
	windowItems := strings.Join([]string{
		item("#{?#{>:#{session_windows},1},,-}Swap Left", "l", "swap-window -t :-1"),
		item("#{?#{>:#{session_windows},1},,-}Swap Right", "r", "swap-window -t :+1"),
		item("#{?pane_marked_set,,-}Swap Marked", "s", "swap-window"),
		"''",
		item("Kill", "X", windowKill),
		item("#{?pane_marked,Unmark,Mark}", "m", "select-pane -m"),
		item("Rename", "n", TmuxMenuPromptCommand(binaryPath, rename)),
		"''",
		item("New At End", "W", keybinding.RenderTmuxBindingBody(binaryPath, create)),
	}, " ")
	paneItems := strings.Join([]string{
		item("#{?#{m/r:(copy|view)-mode,#{pane_mode}},Go To Top,}", "<", "send-keys -X history-top"),
		item("#{?#{m/r:(copy|view)-mode,#{pane_mode}},Go To Bottom,}", ">", "send-keys -X history-bottom"),
		"''",
		item("#{?mouse_word,Search For #[underscore]#{=/9/...:mouse_word},}", "C-r", `if-shell -F "#{?#{m/r:(copy|view)-mode,#{pane_mode}},0,1}" "copy-mode -t=" ; send-keys -X -t = search-backward -- "#{q:mouse_word}"`),
		item("#{?mouse_word,Type #[underscore]#{=/9/...:mouse_word},}", "C-y", `copy-mode -q ; send-keys -l "#{q:mouse_word}"`),
		item("#{?mouse_word,Copy #[underscore]#{=/9/...:mouse_word},}", "c", `copy-mode -q ; set-buffer "#{q:mouse_word}"`),
		item("#{?mouse_line,Copy Line,}", "l", `copy-mode -q ; set-buffer "#{q:mouse_line}"`),
		"''",
		item("#{?mouse_hyperlink,Type #[underscore]#{=/9/...:mouse_hyperlink},}", "C-h", `copy-mode -q ; send-keys -l "#{q:mouse_hyperlink}"`),
		item("#{?mouse_hyperlink,Copy #[underscore]#{=/9/...:mouse_hyperlink},}", "h", `copy-mode -q ; set-buffer "#{q:mouse_hyperlink}"`),
		"''",
		item("Horizontal Split", "h", TmuxPaneMenuAction(bin, "split-right")),
		item("Vertical Split", "v", TmuxPaneMenuAction(bin, "split-down")),
		"''",
		item("#{?#{>:#{window_panes},1},,-}Swap Up", "u", "swap-pane -U"),
		item("#{?#{>:#{window_panes},1},,-}Swap Down", "d", "swap-pane -D"),
		item("#{?pane_marked_set,,-}Swap Marked", "s", "swap-pane"),
		"''",
		item("Kill", "X", TmuxPaneMenuKill(bin)),
		item("#{?pane_marked,Unmark,Mark}", "m", "select-pane -m"),
		item("#{?#{>:#{window_panes},1},,-}#{?window_zoomed_flag,Unzoom,Zoom}", "z", "resize-pane -Z"),
	}, " ")
	windowTitle := "-T " + keybinding.TmuxConfigQuote("#[align=centre]#{window_index}:#{window_name}")
	paneTitle := "-T " + keybinding.TmuxConfigQuote("#[align=centre]#{pane_index} (#{pane_id})")
	return []string{
		"unbind-key -q <",
		"bind-key < display-menu " + windowTitle + " -x W -y W " + windowItems,
		"unbind-key -q -n MouseDown3Status",
		"bind-key -n MouseDown3Status display-menu " + windowTitle + " -t = -x W -y W " + windowItems,
		"unbind-key -q -n M-MouseDown3Status",
		"bind-key -n M-MouseDown3Status display-menu " + windowTitle + " -t = -x W -y W " + windowItems,
		"unbind-key -q >",
		"bind-key > display-menu " + paneTitle + " -x P -y P " + paneItems,
		"unbind-key -q -n M-MouseDown3Pane",
		"bind-key -n M-MouseDown3Pane display-menu " + paneTitle + " -t = -x M -y M " + paneItems,
	}
}

func TmuxMenuPromptCommand(binaryPath string, action keybinding.KeyBindingAction) string {
	rendered := keybinding.RenderTmuxBindingBody(binaryPath, action)
	head := strings.TrimSpace("command-prompt "+action.TmuxPromptArgs) + " "
	return head + strings.ReplaceAll(strings.TrimPrefix(rendered, head), "#", "##")
}

const StatusbarSettingsIcon = ""

const StatusbarNotifyMaxWidth = 80

const StatusbarNotifyMinWidth = 20

const StatusbarUsageMinWidth = 120

const StatusbarNotifySurplusFromWidth = StatusbarUsageMinWidth + StatusbarNotifyMinWidth

const StatusbarEvenSplitMaxWidth = 2 * StatusbarNotifyMinWidth

const StatusbarNotifySurplusCapWidth = StatusbarNotifySurplusFromWidth + StatusbarNotifyMaxWidth

const StatusbarClientWidthFormat = "#{client_width}"

const (
	TmuxStockMenuKillPane   = "kill-pane"
	TmuxStockMenuKillWindow = "kill-window"
)

const ManagedMenuWindowDeleteRoute = "internal tmux window-delete --client #{client_tty} --anchor #{pane_id}"
