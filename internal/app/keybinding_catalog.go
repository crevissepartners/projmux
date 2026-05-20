package app

import (
	"fmt"
	"sort"
	"strings"
)

const noUserSlot = -1

type keyBindingScope string

const (
	keyBindingScopeStandalone keyBindingScope = "standalone"
	keyBindingScopeApp        keyBindingScope = "app"
)

type tmuxBindingKind string

const (
	tmuxBindingPopupToggle   tmuxBindingKind = "popup-toggle"
	tmuxBindingRunProjmux    tmuxBindingKind = "run-projmux"
	tmuxBindingCommand       tmuxBindingKind = "command"
	tmuxBindingCommandPrompt tmuxBindingKind = "command-prompt"
)

type keyBindingTier string

const (
	keyBindingTierGuaranteedLaunchDefault keyBindingTier = "guaranteed-launch-default"
	keyBindingTierUserConfigurableDirect  keyBindingTier = "user-configurable-direct-binding"
	keyBindingTierTransportDependent      keyBindingTier = "transport-dependent-special-chord"
	keyBindingTierCSIuFallback            keyBindingTier = "csi-u-user-fallback-transport"
	keyBindingTierAmbiguousTerminalChord  keyBindingTier = "ambiguous-terminal-chord"
	keyBindingTierNativePickerInternal    keyBindingTier = "native-picker-internal-command"
	keyBindingTierPopupLaunchCloseAlias   keyBindingTier = "popup-launch-close-alias"
)

type keyBindingActionKind string

const (
	keyBindingActionTogglePopup    keyBindingActionKind = "toggle-popup"
	keyBindingActionCommand        keyBindingActionKind = "command"
	keyBindingActionPickerInternal keyBindingActionKind = "picker-internal"
)

// keyBindingAction is the in-app source of truth for built-in key actions.
// Terminal init adapters and tmux config rendering derive their concrete
// trigger/action tables from these entries.
type keyBindingAction struct {
	ID          string
	Description string
	DisplayName string
	LegacyIDs   []string
	Kind        keyBindingActionKind
	Tier        keyBindingTier
	Surface     string
	Scope       keyBindingScope

	PlainChord  string
	PlainChords []string
	PrefixChord string
	UserSlot    int
	CSIu        string

	TmuxKind       tmuxBindingKind
	TmuxBody       string
	TmuxPromptArgs string
	Toggleable     bool

	PlainBindOrder  int
	UserBindOrder   int
	PrefixBindOrder int

	GhosttyTrigger string
	GhosttyOrder   int
	WTID           string
	WTKeys         string
	WTInput        string
	WTOrder        int

	ProbeOrder  int
	ProbeLabel  string
	ProbeAction string
	ProbePlain  string
	ProbeCSIu   string
}

func defaultKeyBindingCatalog() []keyBindingAction {
	return []keyBindingAction{
		{
			ID:              "ProjectSidebarToggle",
			Description:     "Project sidebar",
			DisplayName:     "ProjectSidebarToggle",
			LegacyIDs:       []string{"sessionizer-sidebar"},
			Kind:            keyBindingActionTogglePopup,
			Tier:            keyBindingTierGuaranteedLaunchDefault,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "M-1",
			PrefixChord:     "F",
			UserSlot:        4,
			CSIu:            "9005",
			TmuxKind:        tmuxBindingPopupToggle,
			TmuxBody:        "sessionizer-sidebar",
			Toggleable:      true,
			PlainBindOrder:  10,
			UserBindOrder:   50,
			PrefixBindOrder: 30,
			GhosttyTrigger:  "alt+1",
			GhosttyOrder:    10,
			WTID:            "User.projmuxSidebar",
			WTKeys:          "alt+1",
			WTInput:         "\x1b1",
			WTOrder:         10,
			ProbeOrder:      10,
			ProbeLabel:      "Alt-1",
			ProbeAction:     "Open sidebar (User4)",
			ProbePlain:      "\x1b1",
		},
		{
			ID:             "NotifySidebarToggle",
			Description:    "Notify sidebar",
			DisplayName:    "NotifySidebarToggle",
			LegacyIDs:      []string{"notify-sidebar"},
			Kind:           keyBindingActionTogglePopup,
			Tier:           keyBindingTierGuaranteedLaunchDefault,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "M-2",
			UserSlot:       2,
			CSIu:           "9003",
			TmuxKind:       tmuxBindingPopupToggle,
			TmuxBody:       "notify-sidebar",
			Toggleable:     true,
			PlainBindOrder: 20,
			UserBindOrder:  30,
			GhosttyTrigger: "alt+2",
			GhosttyOrder:   20,
			WTID:           "User.projmuxNotifySidebar",
			WTKeys:         "alt+2",
			WTInput:        "\x1b2",
			WTOrder:        20,
			ProbeOrder:     20,
			ProbeLabel:     "Alt-2",
			ProbeAction:    "Notify sidebar (User2)",
			ProbePlain:     "\x1b2",
		},
		{
			ID:              "SessionPopupToggle",
			Description:     "Existing session popup",
			DisplayName:     "SessionPopupToggle",
			LegacyIDs:       []string{"session-popup"},
			Kind:            keyBindingActionTogglePopup,
			Tier:            keyBindingTierGuaranteedLaunchDefault,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "M-3",
			PrefixChord:     "b",
			UserSlot:        3,
			CSIu:            "9004",
			TmuxKind:        tmuxBindingPopupToggle,
			TmuxBody:        "session-popup",
			Toggleable:      true,
			PlainBindOrder:  30,
			UserBindOrder:   40,
			PrefixBindOrder: 10,
			GhosttyTrigger:  "alt+3",
			GhosttyOrder:    30,
			WTID:            "User.projmuxSessions",
			WTKeys:          "alt+3",
			WTInput:         "\x1b3",
			WTOrder:         30,
			ProbeOrder:      30,
			ProbeLabel:      "Alt-3",
			ProbeAction:     "Open session popup (User3)",
			ProbePlain:      "\x1b3",
		},
		{
			ID:             "AISplitPickerToggle",
			Description:    "AI split picker",
			DisplayName:    "AISplitPickerToggle",
			LegacyIDs:      []string{"ai-split-picker-right"},
			Kind:           keyBindingActionTogglePopup,
			Tier:           keyBindingTierGuaranteedLaunchDefault,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "M-4",
			UserSlot:       5,
			CSIu:           "9006",
			TmuxKind:       tmuxBindingPopupToggle,
			TmuxBody:       "ai-split-picker-right",
			Toggleable:     true,
			PlainBindOrder: 40,
			UserBindOrder:  60,
			GhosttyTrigger: "alt+4",
			GhosttyOrder:   40,
			WTID:           "User.projmuxAIPicker",
			WTKeys:         "alt+4",
			WTInput:        "\x1b4",
			WTOrder:        40,
			ProbeOrder:     40,
			ProbeLabel:     "Alt-4",
			ProbeAction:    "AI split picker right (User5)",
			ProbePlain:     "\x1b4",
		},
		{
			ID:             "SettingsToggle",
			Description:    "Settings",
			DisplayName:    "SettingsToggle",
			LegacyIDs:      []string{"ai-split-settings"},
			Kind:           keyBindingActionTogglePopup,
			Tier:           keyBindingTierGuaranteedLaunchDefault,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "M-5",
			UserSlot:       6,
			CSIu:           "9007",
			TmuxKind:       tmuxBindingPopupToggle,
			TmuxBody:       "ai-split-settings",
			Toggleable:     true,
			PlainBindOrder: 50,
			UserBindOrder:  70,
			GhosttyTrigger: "alt+5",
			GhosttyOrder:   50,
			WTID:           "User.projmuxSettings",
			WTKeys:         "alt+5",
			WTInput:        "\x1b5",
			WTOrder:        50,
			ProbeOrder:     50,
			ProbeLabel:     "Alt-5",
			ProbeAction:    "Settings (User6)",
			ProbePlain:     "\x1b5",
		},
		{
			ID:              "ProjectSwitcherToggle",
			Description:     "Project switcher popup",
			DisplayName:     "ProjectSwitcherToggle",
			LegacyIDs:       []string{"sessionizer"},
			Kind:            keyBindingActionTogglePopup,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "",
			PrefixChord:     "f",
			UserSlot:        12,
			CSIu:            "9013",
			TmuxKind:        tmuxBindingPopupToggle,
			TmuxBody:        "sessionizer",
			Toggleable:      true,
			PlainBindOrder:  60,
			UserBindOrder:   90,
			PrefixBindOrder: 20,
			GhosttyTrigger:  "alt+6",
			GhosttyOrder:    60,
			WTID:            "User.projmuxSwitch",
			WTKeys:          "alt+6",
			WTInput:         "\x1b6",
			WTOrder:         60,
			ProbeOrder:      60,
			ProbeLabel:      "Alt-6",
			ProbeAction:     "Open sessionizer (User12)",
			ProbePlain:      "\x1b6",
		},
		{
			ID:              "rename-window",
			Description:     "Rename the current tmux window",
			Kind:            keyBindingActionCommand,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "",
			PrefixChord:     "R",
			UserSlot:        10,
			CSIu:            "9011",
			TmuxKind:        tmuxBindingCommandPrompt,
			TmuxBody:        "rename-window -- '%%'",
			TmuxPromptArgs:  "-I \"#{window_name}\"",
			PlainBindOrder:  70,
			UserBindOrder:   80,
			PrefixBindOrder: 70,
			GhosttyTrigger:  "ctrl+m",
			GhosttyOrder:    100,
			WTID:            "User.projmuxRenameWindow",
			WTKeys:          "ctrl+m",
			WTInput:         "\x1b[9011u",
			WTOrder:         120,
			ProbeOrder:      100,
			ProbeLabel:      "Ctrl-M",
			ProbeAction:     "Rename window (User10)",
			ProbePlain:      "\r",
		},
		{
			ID:             "rename-pane-topic",
			Description:    "Rename the current tmux pane label",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierCSIuFallback,
			Scope:          keyBindingScopeStandalone,
			UserSlot:       11,
			CSIu:           "9012",
			TmuxKind:       tmuxBindingCommandPrompt,
			TmuxBody:       "select-pane -T '%1' \\; set-option -p " + aiPaneTopicOption + " '%1' \\; if-shell -F '#{==:#{" + aiPaneTopicOption + "},}' 'set-option -p -u " + aiPaneTopicManualOption + "' 'set-option -p " + aiPaneTopicManualOption + " 1'",
			TmuxPromptArgs: "-p \"ai topic:\" -I \"#{pane_title}\"",
			UserBindOrder:  1,
			GhosttyTrigger: "ctrl+shift+m",
			GhosttyOrder:   110,
			WTID:           "User.projmuxRenamePane",
			WTKeys:         "ctrl+shift+m",
			WTInput:        "\x1b[9012u",
			WTOrder:        130,
			ProbeOrder:     110,
			ProbeLabel:     "Ctrl-Shift-M",
			ProbeAction:    "AI topic prompt (User11)",
		},
		{
			ID:              "ai-split-right",
			Description:     "Open AI split to the right",
			Kind:            keyBindingActionCommand,
			Tier:            keyBindingTierCSIuFallback,
			Scope:           keyBindingScopeStandalone,
			PrefixChord:     "r",
			UserSlot:        0,
			CSIu:            "9001",
			TmuxKind:        tmuxBindingRunProjmux,
			TmuxBody:        "ai split right",
			UserBindOrder:   10,
			PrefixBindOrder: 50,
			GhosttyTrigger:  "ctrl+shift+r",
			GhosttyOrder:    70,
			WTID:            "User.projmuxAISplitRight",
			WTKeys:          "ctrl+shift+r",
			WTInput:         "\x02r",
			WTOrder:         70,
			ProbeOrder:      80,
			ProbeLabel:      "Ctrl-Shift-R",
			ProbeAction:     "No projmux binding by default",
			ProbeCSIu:       "-",
		},
		{
			ID:              "ai-split-down",
			Description:     "Open AI split below",
			Kind:            keyBindingActionCommand,
			Tier:            keyBindingTierCSIuFallback,
			Scope:           keyBindingScopeStandalone,
			PrefixChord:     "l",
			UserSlot:        1,
			CSIu:            "9002",
			TmuxKind:        tmuxBindingRunProjmux,
			TmuxBody:        "ai split down",
			UserBindOrder:   20,
			PrefixBindOrder: 60,
			GhosttyTrigger:  "ctrl+shift+l",
			GhosttyOrder:    80,
			WTID:            "User.projmuxAISplitDown",
			WTKeys:          "ctrl+shift+l",
			WTInput:         "\x02l",
			WTOrder:         80,
			ProbeOrder:      90,
			ProbeLabel:      "Ctrl-Shift-L",
			ProbeAction:     "No projmux binding by default",
			ProbeCSIu:       "-",
		},
		{
			ID:              "current-project-session",
			Description:     "Jump to current pane project session",
			Kind:            keyBindingActionCommand,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeStandalone,
			PrefixChord:     "g",
			UserSlot:        noUserSlot,
			TmuxKind:        tmuxBindingRunProjmux,
			TmuxBody:        "current",
			PrefixBindOrder: 40,
		},
		{
			ID:             "new-window",
			Description:    "New tmux window in the current pane directory",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeApp,
			PlainChord:     "",
			UserSlot:       7,
			CSIu:           "9008",
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "new-window -c \"#{pane_current_path}\"",
			PlainBindOrder: 50,
			UserBindOrder:  10,
			GhosttyTrigger: "ctrl+shift+n",
			GhosttyOrder:   90,
			WTID:           "User.projmuxNewWindow",
			WTKeys:         "ctrl+n",
			WTInput:        "\x0e",
			WTOrder:        90,
			ProbeOrder:     70,
			ProbeLabel:     "Ctrl-N",
			ProbeAction:    "New window (User7)",
			ProbePlain:     "\x0e",
		},
		{
			// Phase 2.8: dropped UserSlot/CSIu (9009u) detour — tmux now
			// listens directly for the xterm-standard `M-S-Left` chord that
			// Ghostty and Windows Terminal emit out of the box. Keeping
			// `WTInput` ensures Windows Terminal explicitly sends the same
			// `\x1b[1;4D` sequence (it does not always forward modifier-arrow
			// chords unless mapped). Ghostty drops the explicit keybind
			// entirely because its default behaviour already emits the
			// xterm sequence.
			ID:             "previous-window",
			Description:    "Previous tmux window",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeApp,
			PlainChord:     "",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "previous-window",
			PlainBindOrder: 60,
			WTID:           "User.projmuxPrevWindow",
			WTKeys:         "alt+shift+left",
			WTInput:        "\x1b[1;4D",
			WTOrder:        100,
			ProbeOrder:     120,
			ProbeLabel:     "Alt-Shift-Left",
			ProbeAction:    "Previous window (M-S-Left)",
			ProbePlain:     "\x1b[1;4D",
			ProbeCSIu:      "-",
		},
		{
			// See `previous-window` above — the same reasoning applies to the
			// next-window chord (xterm sequence `\x1b[1;4C`).
			ID:             "next-window",
			Description:    "Next tmux window",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeApp,
			PlainChord:     "",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "next-window",
			PlainBindOrder: 70,
			WTID:           "User.projmuxNextWindow",
			WTKeys:         "alt+shift+right",
			WTInput:        "\x1b[1;4C",
			WTOrder:        110,
			ProbeOrder:     130,
			ProbeLabel:     "Alt-Shift-Right",
			ProbeAction:    "Next window (M-S-Right)",
			ProbePlain:     "\x1b[1;4C",
			ProbeCSIu:      "-",
		},
		{
			ID:             "select-pane-left",
			Description:    "Move focus to the left pane",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeApp,
			PlainChord:     "",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "select-pane -L",
			PlainBindOrder: 10,
		},
		{
			ID:             "select-pane-right",
			Description:    "Move focus to the right pane",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeApp,
			PlainChord:     "",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "select-pane -R",
			PlainBindOrder: 20,
		},
		{
			ID:             "select-pane-up",
			Description:    "Move focus to the pane above",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeApp,
			PlainChord:     "",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "select-pane -U",
			PlainBindOrder: 30,
		},
		{
			ID:             "select-pane-down",
			Description:    "Move focus to the pane below",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeApp,
			PlainChord:     "",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "select-pane -D",
			PlainBindOrder: 40,
		},
		{
			ID:              "toggle-mouse",
			Description:     "Toggle tmux mouse mode",
			Kind:            keyBindingActionCommand,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeApp,
			PrefixChord:     "M",
			UserSlot:        noUserSlot,
			TmuxKind:        tmuxBindingCommand,
			TmuxBody:        "if -F \"#{mouse}\" \"set -g mouse off \\; display-message 'tmux mouse: off'\" \"set -g mouse on \\; display-message 'tmux mouse: on'\"",
			PrefixBindOrder: 10,
		},
		{
			ID:          "Sidebar:PinProject",
			Description: "Pin or unpin the focused project",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "Sidebar",
			PlainChord:  "M-p",
		},
		{
			ID:          "Sidebar:KillSession",
			Description: "Kill the focused existing session",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "Sidebar",
			PlainChord:  "C-x",
		},
		{
			ID:          "SessionPopup:KillSession",
			Description: "Kill the focused existing session",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "C-x",
		},
		{
			ID:          "SessionPopup:OpenState",
			Description: "Open session state for the focused session",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "C-s",
		},
		{
			ID:          "SessionPopup:CyclePreviewWindowPrev",
			Description: "Preview previous window",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "Left",
		},
		{
			ID:          "SessionPopup:CyclePreviewWindowNext",
			Description: "Preview next window",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "Right",
		},
		{
			ID:          "SessionPopup:CyclePreviewPanePrev",
			Description: "Preview previous pane",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "M-Up",
		},
		{
			ID:          "SessionPopup:CyclePreviewPaneNext",
			Description: "Preview next pane",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "M-Down",
		},
		{
			ID:          "NotifySidebar:FocusAndAck",
			Description: "Focus and acknowledge the selected notification",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "Enter",
		},
		{
			ID:          "NotifySidebar:Ack",
			Description: "Acknowledge the selected notification",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "a",
		},
		{
			ID:          "NotifySidebar:ClearNonCritical",
			Description: "Clear non-critical notifications",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "x",
		},
		{
			ID:          "NotifySidebar:ClearAll",
			Description: "Clear all notifications",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "C-x",
		},
		{
			ID:          "Settings:SwitchTabPrev",
			Description: "Switch Settings tab left",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "Settings",
			PlainChord:  "M-S-Left",
		},
		{
			ID:          "Settings:SwitchTabNext",
			Description: "Switch Settings tab right",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "Settings",
			PlainChord:  "M-S-Right",
		},
	}
}

func keyBindingCatalogForScope(scope keyBindingScope) []keyBindingAction {
	return keyBindingCatalogForScopeFrom(defaultKeyBindingCatalog(), scope)
}

func keyBindingCatalogForScopeFrom(catalog []keyBindingAction, scope keyBindingScope) []keyBindingAction {
	var out []keyBindingAction
	for _, action := range catalog {
		if action.Scope == scope && action.Kind != keyBindingActionPickerInternal {
			out = append(out, action)
		}
	}
	return out
}

func keyBindingDisplayName(action keyBindingAction) string {
	if name := strings.TrimSpace(action.DisplayName); name != "" {
		return name
	}
	return action.ID
}

func keyBindingActionAliases(action keyBindingAction) []string {
	aliases := []string{action.ID}
	aliases = append(aliases, action.LegacyIDs...)
	return aliases
}

func keyBindingEditable(action keyBindingAction) bool {
	switch action.Tier {
	case keyBindingTierGuaranteedLaunchDefault, keyBindingTierUserConfigurableDirect:
		return true
	default:
		return false
	}
}

func keyBindingEffectivePlainChords(action keyBindingAction) []string {
	if action.PlainChords != nil {
		return uniqueNonEmptyStrings(action.PlainChords)
	}
	if strings.TrimSpace(action.PlainChord) == "" {
		return nil
	}
	return []string{strings.TrimSpace(action.PlainChord)}
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func tmuxUserKeyLines(actions []keyBindingAction) []string {
	withUserSlot := filterKeyBindingActions(actions, func(action keyBindingAction) bool {
		return action.UserSlot != noUserSlot && action.CSIu != ""
	})
	sort.SliceStable(withUserSlot, func(i, j int) bool {
		return withUserSlot[i].UserSlot < withUserSlot[j].UserSlot
	})

	lines := make([]string, 0, len(withUserSlot))
	for _, action := range withUserSlot {
		lines = append(lines, fmt.Sprintf("set -s user-keys[%d] \"\\033[%su\"", action.UserSlot, action.CSIu))
	}
	return lines
}

func tmuxUnbindLines(actions []keyBindingAction) []string {
	plain := filterKeyBindingActions(actions, func(action keyBindingAction) bool {
		return len(keyBindingEffectivePlainChords(action)) != 0
	})
	sort.SliceStable(plain, func(i, j int) bool {
		return plain[i].PlainBindOrder < plain[j].PlainBindOrder
	})

	user := filterKeyBindingActions(actions, func(action keyBindingAction) bool {
		return action.UserSlot != noUserSlot
	})
	sort.SliceStable(user, func(i, j int) bool {
		return user[i].UserSlot < user[j].UserSlot
	})

	prefix := filterKeyBindingActions(actions, func(action keyBindingAction) bool {
		return action.PrefixChord != ""
	})
	sort.SliceStable(prefix, func(i, j int) bool {
		return prefix[i].PrefixBindOrder < prefix[j].PrefixBindOrder
	})

	lines := make([]string, 0, len(plain)+len(user)+len(prefix))
	for _, action := range plain {
		for _, chord := range keyBindingEffectivePlainChords(action) {
			lines = append(lines, "unbind-key -q -n "+chord)
		}
	}
	for _, action := range user {
		lines = append(lines, "unbind-key -q -n "+keyBindingUserKey(action))
	}
	for _, action := range prefix {
		lines = append(lines, "unbind-key -q "+action.PrefixChord)
	}
	return lines
}

func tmuxMergedUnbindLines(defaults, merged []keyBindingAction) []string {
	type binding struct {
		chord string
		order int
	}
	seenNoPrefix := map[string]bool{}
	seenPrefix := map[string]bool{}
	var plain, user, prefix []binding
	add := func(action keyBindingAction) {
		for _, chord := range keyBindingEffectivePlainChords(action) {
			if !seenNoPrefix[chord] {
				seenNoPrefix[chord] = true
				plain = append(plain, binding{chord: chord, order: action.PlainBindOrder})
			}
		}
		if action.UserSlot != noUserSlot {
			chord := keyBindingUserKey(action)
			if !seenNoPrefix[chord] {
				seenNoPrefix[chord] = true
				user = append(user, binding{chord: chord, order: action.UserSlot})
			}
		}
		if action.PrefixChord != "" && !seenPrefix[action.PrefixChord] {
			seenPrefix[action.PrefixChord] = true
			prefix = append(prefix, binding{chord: action.PrefixChord, order: action.PrefixBindOrder})
		}
	}
	for _, action := range defaults {
		add(action)
	}
	for _, action := range merged {
		add(action)
	}

	sort.SliceStable(plain, func(i, j int) bool { return plain[i].order < plain[j].order })
	sort.SliceStable(user, func(i, j int) bool { return user[i].order < user[j].order })
	sort.SliceStable(prefix, func(i, j int) bool { return prefix[i].order < prefix[j].order })

	lines := make([]string, 0, len(plain)+len(user)+len(prefix))
	for _, binding := range plain {
		lines = append(lines, "unbind-key -q -n "+binding.chord)
	}
	for _, binding := range user {
		lines = append(lines, "unbind-key -q -n "+binding.chord)
	}
	for _, binding := range prefix {
		lines = append(lines, "unbind-key -q "+binding.chord)
	}
	return lines
}

func tmuxBindLines(binaryPath string, actions []keyBindingAction) []string {
	var bindings []struct {
		chord string
		line  string
		order int
	}

	for _, action := range actions {
		for idx, chord := range keyBindingEffectivePlainChords(action) {
			bindings = append(bindings, struct {
				chord string
				line  string
				order int
			}{
				chord: chord,
				line:  renderTmuxBindLine(binaryPath, chord, false, action),
				order: action.PlainBindOrder + idx,
			})
		}
	}
	sort.SliceStable(bindings, func(i, j int) bool { return bindings[i].order < bindings[j].order })

	lines := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		lines = append(lines, binding.line)
	}

	bindings = bindings[:0]
	for _, action := range actions {
		if action.UserSlot != noUserSlot {
			bindings = append(bindings, struct {
				chord string
				line  string
				order int
			}{
				chord: keyBindingUserKey(action),
				line:  renderTmuxBindLine(binaryPath, keyBindingUserKey(action), true, action),
				order: action.UserBindOrder,
			})
		}
	}
	sort.SliceStable(bindings, func(i, j int) bool { return bindings[i].order < bindings[j].order })
	for _, binding := range bindings {
		lines = append(lines, binding.line)
	}

	return lines
}

func renderTmuxBindLine(binaryPath, chord string, noPrefix bool, action keyBindingAction) string {
	parts := []string{"bind-key"}
	if noPrefix || strings.HasPrefix(chord, "M-") || strings.HasPrefix(chord, "C-") || strings.HasPrefix(chord, "User") {
		parts = append(parts, "-n")
	}
	parts = append(parts, chord, renderTmuxBindingBody(binaryPath, action))
	return strings.Join(parts, " ")
}

func renderTmuxBindingBody(binaryPath string, action keyBindingAction) string {
	bin := tmuxShellQuote(binaryPath)
	switch action.TmuxKind {
	case tmuxBindingPopupToggle:
		return "run-shell " + tmuxConfigQuote(bin+" tmux popup-toggle --client #{client_tty} "+action.TmuxBody)
	case tmuxBindingRunProjmux:
		return "run-shell " + tmuxConfigQuote(bin+" "+action.TmuxBody)
	case tmuxBindingCommand:
		return action.TmuxBody
	case tmuxBindingCommandPrompt:
		return strings.TrimSpace("command-prompt " + action.TmuxPromptArgs + " " + tmuxConfigQuote(action.TmuxBody))
	default:
		return action.TmuxBody
	}
}

func keyBindingUserKey(action keyBindingAction) string {
	return fmt.Sprintf("User%d", action.UserSlot)
}

func filterKeyBindingActions(actions []keyBindingAction, keep func(keyBindingAction) bool) []keyBindingAction {
	out := make([]keyBindingAction, 0, len(actions))
	for _, action := range actions {
		if keep(action) {
			out = append(out, action)
		}
	}
	return out
}

func ghosttyBindingsFromCatalog() []ghosttyBinding {
	var actions []keyBindingAction
	for _, action := range defaultKeyBindingCatalog() {
		if action.GhosttyTrigger == "" || action.CSIu == "" {
			continue
		}
		actions = append(actions, action)
	}
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i].GhosttyOrder < actions[j].GhosttyOrder
	})

	out := make([]ghosttyBinding, 0, len(actions))
	for _, action := range actions {
		out = append(out, ghosttyBinding{
			Trigger: action.GhosttyTrigger,
			Action:  "csi:" + action.CSIu + "u",
		})
	}
	return out
}

func windowsTerminalBindingsFromCatalog() []wtBinding {
	var actions []keyBindingAction
	for _, action := range defaultKeyBindingCatalog() {
		if action.WTID == "" {
			continue
		}
		actions = append(actions, action)
	}
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i].WTOrder < actions[j].WTOrder
	})

	out := make([]wtBinding, 0, len(actions))
	for _, action := range actions {
		out = append(out, wtBinding{
			ID:    action.WTID,
			Keys:  action.WTKeys,
			Input: action.WTInput,
		})
	}
	return out
}

func probeKeysFromCatalog() []probeKey {
	return probeKeysFromActions(defaultKeyBindingCatalog())
}

func probeKeysFromActions(catalog []keyBindingAction) []probeKey {
	var actions []keyBindingAction
	for _, action := range catalog {
		if action.ProbeLabel != "" {
			actions = append(actions, action)
		}
	}
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i].ProbeOrder < actions[j].ProbeOrder
	})

	keys := make([]probeKey, 0, len(actions))
	for _, action := range actions {
		csiu := action.ProbeCSIu
		if csiu == "" && action.CSIu != "" {
			csiu = "\x1b[" + action.CSIu + "u"
		}
		if csiu == "-" {
			csiu = ""
		}
		userKey := ""
		if csiu != "" && action.UserSlot != noUserSlot {
			userKey = keyBindingUserKey(action)
		}
		keys = append(keys, probeKey{
			ActionID:   action.ID,
			Label:      action.ProbeLabel,
			Action:     action.ProbeAction,
			Plain:      action.ProbePlain,
			CSIu:       csiu,
			UserKey:    userKey,
			PlainChord: firstNonEmptyString(keyBindingEffectivePlainChords(action)),
		})
	}
	return keys
}

func firstNonEmptyString(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
