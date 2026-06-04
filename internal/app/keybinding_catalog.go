package app

import (
	"fmt"
	"sort"
	"strings"
)

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

	TmuxKind       tmuxBindingKind
	TmuxBody       string
	TmuxPromptArgs string
	Toggleable     bool

	PlainBindOrder  int
	PrefixBindOrder int

	GhosttyTrigger string
	GhosttyAction  string
	GhosttyOrder   int
	WTID           string
	WTKeys         string
	WTInput        string
	WTOrder        int

	ProbeOrder  int
	ProbeLabel  string
	ProbeAction string
	ProbePlain  string
}

func defaultKeyBindingCatalog() []keyBindingAction {
	return []keyBindingAction{
		{
			ID:              "ProjectSidebarToggle",
			Description:     "Project sidebar",
			DisplayName:     "Toggle Project Sidebar",
			LegacyIDs:       []string{"sessionizer-sidebar"},
			Kind:            keyBindingActionTogglePopup,
			Tier:            keyBindingTierGuaranteedLaunchDefault,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "M-1",
			PrefixChord:     "F",
			TmuxKind:        tmuxBindingPopupToggle,
			TmuxBody:        "sessionizer-sidebar",
			Toggleable:      true,
			PlainBindOrder:  10,
			PrefixBindOrder: 30,
			GhosttyTrigger:  "alt+1",
			GhosttyAction:   `text:\x1b1`,
			GhosttyOrder:    10,
			WTID:            "User.projmuxSidebar",
			WTKeys:          "alt+1",
			WTInput:         "\x1b1",
			WTOrder:         10,
			ProbeOrder:      10,
			ProbeLabel:      "Alt-1",
			ProbeAction:     "Open sidebar",
			ProbePlain:      "\x1b1",
		},
		{
			ID:             "NotifySidebarToggle",
			Description:    "Notify sidebar",
			DisplayName:    "Toggle Notify Sidebar",
			LegacyIDs:      []string{"notify-sidebar"},
			Kind:           keyBindingActionTogglePopup,
			Tier:           keyBindingTierGuaranteedLaunchDefault,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "M-2",
			TmuxKind:       tmuxBindingPopupToggle,
			TmuxBody:       "notify-sidebar",
			Toggleable:     true,
			PlainBindOrder: 20,
			GhosttyTrigger: "alt+2",
			GhosttyAction:  `text:\x1b2`,
			GhosttyOrder:   20,
			WTID:           "User.projmuxNotifySidebar",
			WTKeys:         "alt+2",
			WTInput:        "\x1b2",
			WTOrder:        20,
			ProbeOrder:     20,
			ProbeLabel:     "Alt-2",
			ProbeAction:    "Notify sidebar",
			ProbePlain:     "\x1b2",
		},
		{
			ID:              "SessionPopupToggle",
			Description:     "Existing session popup",
			DisplayName:     "Toggle Session Popup",
			LegacyIDs:       []string{"session-popup"},
			Kind:            keyBindingActionTogglePopup,
			Tier:            keyBindingTierGuaranteedLaunchDefault,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "M-3",
			PrefixChord:     "b",
			TmuxKind:        tmuxBindingPopupToggle,
			TmuxBody:        "session-popup",
			Toggleable:      true,
			PlainBindOrder:  30,
			PrefixBindOrder: 10,
			GhosttyTrigger:  "alt+3",
			GhosttyAction:   `text:\x1b3`,
			GhosttyOrder:    30,
			WTID:            "User.projmuxSessions",
			WTKeys:          "alt+3",
			WTInput:         "\x1b3",
			WTOrder:         30,
			ProbeOrder:      30,
			ProbeLabel:      "Alt-3",
			ProbeAction:     "Open session popup",
			ProbePlain:      "\x1b3",
		},
		{
			ID:             "AISplitPickerToggle",
			Description:    "Toggle the popup picker for choosing an AI split mode",
			DisplayName:    "Toggle AI Split Picker Popup",
			LegacyIDs:      []string{"ai-split-picker-right"},
			Kind:           keyBindingActionTogglePopup,
			Tier:           keyBindingTierGuaranteedLaunchDefault,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "M-4",
			TmuxKind:       tmuxBindingPopupToggle,
			TmuxBody:       "ai-split-picker-right",
			Toggleable:     true,
			PlainBindOrder: 40,
			GhosttyTrigger: "alt+4",
			GhosttyAction:  `text:\x1b4`,
			GhosttyOrder:   40,
			WTID:           "User.projmuxAIPicker",
			WTKeys:         "alt+4",
			WTInput:        "\x1b4",
			WTOrder:        40,
			ProbeOrder:     40,
			ProbeLabel:     "Alt-4",
			ProbeAction:    "AI split picker right",
			ProbePlain:     "\x1b4",
		},
		{
			ID:             "SettingsToggle",
			Description:    "Settings",
			DisplayName:    "Toggle Settings",
			LegacyIDs:      []string{"ai-split-settings"},
			Kind:           keyBindingActionTogglePopup,
			Tier:           keyBindingTierGuaranteedLaunchDefault,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "M-5",
			TmuxKind:       tmuxBindingPopupToggle,
			TmuxBody:       "ai-split-settings",
			Toggleable:     true,
			PlainBindOrder: 50,
			GhosttyTrigger: "alt+5",
			GhosttyAction:  `text:\x1b5`,
			GhosttyOrder:   50,
			WTID:           "User.projmuxSettings",
			WTKeys:         "alt+5",
			WTInput:        "\x1b5",
			WTOrder:        50,
			ProbeOrder:     50,
			ProbeLabel:     "Alt-5",
			ProbeAction:    "Settings",
			ProbePlain:     "\x1b5",
		},
		{
			ID:              "ProjectSwitcherToggle",
			Description:     "Project switcher popup",
			DisplayName:     "Toggle Project Switcher",
			LegacyIDs:       []string{"sessionizer"},
			Kind:            keyBindingActionTogglePopup,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "",
			PrefixChord:     "f",
			TmuxKind:        tmuxBindingPopupToggle,
			TmuxBody:        "sessionizer",
			Toggleable:      true,
			PlainBindOrder:  60,
			PrefixBindOrder: 20,
			ProbeOrder:      60,
			ProbeLabel:      "Alt-6",
			ProbeAction:     "Open sessionizer",
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
			TmuxKind:        tmuxBindingCommandPrompt,
			TmuxBody:        "rename-window -- '%%'",
			TmuxPromptArgs:  "-I \"#{window_name}\"",
			PlainBindOrder:  70,
			PrefixBindOrder: 70,
			ProbeOrder:      100,
			ProbeLabel:      "Ctrl-M",
			ProbeAction:     "Rename window",
			ProbePlain:      "\r",
		},
		{
			ID:             "rename-pane-topic",
			Description:    "Rename the current tmux pane label",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeStandalone,
			TmuxKind:       tmuxBindingCommandPrompt,
			TmuxBody:       "select-pane -T '%1' \\; set-option -p " + aiPaneTopicOption + " '%1' \\; if-shell -F '#{==:#{" + aiPaneTopicOption + "},}' 'set-option -p -u " + aiPaneTopicManualOption + "' 'set-option -p " + aiPaneTopicManualOption + " 1'",
			TmuxPromptArgs: "-p \"ai topic:\" -I \"#{pane_title}\"",
			ProbeOrder:     110,
			ProbeLabel:     "Ctrl-Shift-M",
			ProbeAction:    "AI topic prompt",
		},
		{
			ID:              "ai-split-right",
			Description:     "Open a new AI split to the right",
			DisplayName:     "Open AI Split Right",
			Kind:            keyBindingActionCommand,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeStandalone,
			PrefixChord:     "r",
			TmuxKind:        tmuxBindingRunProjmux,
			TmuxBody:        "ai split right",
			PrefixBindOrder: 50,
			WTID:            "User.projmuxAISplitRight",
			WTKeys:          "ctrl+shift+r",
			WTInput:         "\x02r",
			WTOrder:         70,
			ProbeOrder:      80,
			ProbeLabel:      "Ctrl-Shift-R",
			ProbeAction:     "No projmux binding by default",
		},
		{
			ID:              "ai-split-down",
			Description:     "Open a new AI split below",
			DisplayName:     "Open AI Split Down",
			Kind:            keyBindingActionCommand,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeStandalone,
			PrefixChord:     "l",
			TmuxKind:        tmuxBindingRunProjmux,
			TmuxBody:        "ai split down",
			PrefixBindOrder: 60,
			WTID:            "User.projmuxAISplitDown",
			WTKeys:          "ctrl+shift+l",
			WTInput:         "\x02l",
			WTOrder:         80,
			ProbeOrder:      90,
			ProbeLabel:      "Ctrl-Shift-L",
			ProbeAction:     "No projmux binding by default",
		},
		{
			ID:              "current-project-session",
			Description:     "Jump to current pane project session",
			Kind:            keyBindingActionCommand,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeStandalone,
			PrefixChord:     "g",
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
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "new-window -c \"#{pane_current_path}\"",
			PlainBindOrder: 50,
			WTID:           "User.projmuxNewWindow",
			WTKeys:         "ctrl+n",
			WTInput:        "\x0e",
			WTOrder:        90,
			ProbeOrder:     70,
			ProbeLabel:     "Ctrl-N",
			ProbeAction:    "New window",
			ProbePlain:     "\x0e",
		},
		{
			// tmux listens directly for the xterm-standard `M-S-Left` chord that
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
			PlainChord:     "M-S-Left",
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
		},
		{
			// See `previous-window` above — the same reasoning applies to the
			// next-window chord (xterm sequence `\x1b[1;4C`).
			ID:             "next-window",
			Description:    "Next tmux window",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeApp,
			PlainChord:     "M-S-Right",
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
		},
		{
			ID:             "select-pane-left",
			Description:    "Move focus to the left pane",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeApp,
			PlainChord:     "M-Left",
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
			PlainChord:     "M-Right",
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
			PlainChord:     "M-Up",
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
			PlainChord:     "M-Down",
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
			TmuxKind:        tmuxBindingCommand,
			TmuxBody:        "if -F \"#{mouse}\" \"set -g mouse off \\; display-message 'tmux mouse: off'\" \"set -g mouse on \\; display-message 'tmux mouse: on'\"",
			PrefixBindOrder: 10,
		},
		{
			ID:          "Sidebar:PinProject",
			Description: "Pin or unpin the focused project",
			DisplayName: "Project Sidebar: Pin Project",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "Sidebar",
			PlainChord:  "M-p",
		},
		{
			ID:          "Sidebar:KillSession",
			Description: "Kill the focused existing session",
			DisplayName: "Project Sidebar: Kill Session",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "Sidebar",
			PlainChord:  "C-x",
		},
		{
			ID:          "SessionPopup:KillSession",
			Description: "Kill the focused existing session",
			DisplayName: "Session Popup: Kill Session",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "C-x",
		},
		{
			ID:          "SessionPopup:OpenState",
			Description: "Open session state for the focused session",
			DisplayName: "Session Popup: Open Session State",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "C-s",
		},
		{
			ID:          "SessionPopup:CyclePreviewWindowPrev",
			Description: "Preview previous window",
			DisplayName: "Session Popup: Preview Previous Window",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "Left",
		},
		{
			ID:          "SessionPopup:CyclePreviewWindowNext",
			Description: "Preview next window",
			DisplayName: "Session Popup: Preview Next Window",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "Right",
		},
		{
			ID:          "SessionPopup:CyclePreviewPanePrev",
			Description: "Preview previous pane",
			DisplayName: "Session Popup: Preview Previous Pane",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "M-Up",
		},
		{
			ID:          "SessionPopup:CyclePreviewPaneNext",
			Description: "Preview next pane",
			DisplayName: "Session Popup: Preview Next Pane",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "SessionPopup",
			PlainChord:  "M-Down",
		},
		{
			ID:          "NotifySidebar:FocusAndAck",
			Description: "Focus and acknowledge the selected notification",
			DisplayName: "Notify Sidebar: Focus and Acknowledge",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "Enter",
		},
		{
			ID:          "NotifySidebar:Ack",
			Description: "Acknowledge the selected notification",
			DisplayName: "Notify Sidebar: Acknowledge",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "a",
		},
		{
			ID:          "NotifySidebar:ClearNonCritical",
			Description: "Clear non-critical notifications",
			DisplayName: "Notify Sidebar: Clear Non-Critical",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "x",
		},
		{
			ID:          "NotifySidebar:ClearAll",
			Description: "Clear all notifications",
			DisplayName: "Notify Sidebar: Clear All",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "C-x",
		},
		{
			ID:          "Settings:SwitchTabPrev",
			Description: "Switch Settings tab left",
			DisplayName: "Previous Settings Tab",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "Settings",
			PlainChord:  "M-S-Left",
		},
		{
			ID:          "Settings:SwitchTabNext",
			Description: "Switch Settings tab right",
			DisplayName: "Next Settings Tab",
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
	return humanizeKeyBindingActionID(action.ID)
}

func humanizeKeyBindingActionID(id string) string {
	if _, local, ok := strings.Cut(id, ":"); ok {
		id = local
	}
	id = strings.ReplaceAll(id, "-", " ")
	var words []string
	var current strings.Builder
	runes := []rune(id)
	flush := func() {
		if current.Len() == 0 {
			return
		}
		words = append(words, current.String())
		current.Reset()
	}
	for i, r := range runes {
		if r == ' ' || r == '_' {
			flush()
			continue
		}
		if i > 0 && current.Len() > 0 && isUpperASCII(r) {
			prev := runes[i-1]
			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if isLowerASCII(prev) || isDigitASCII(prev) || (isUpperASCII(prev) && isLowerASCII(next)) {
				flush()
			}
		}
		current.WriteRune(r)
	}
	flush()
	for i, word := range words {
		words[i] = titleASCIIWord(word)
	}
	return strings.Join(words, " ")
}

func titleASCIIWord(word string) string {
	if word == "" {
		return ""
	}
	lower := strings.ToLower(word)
	return strings.ToUpper(lower[:1]) + lower[1:]
}

func isUpperASCII(r rune) bool {
	return r >= 'A' && r <= 'Z'
}

func isLowerASCII(r rune) bool {
	return r >= 'a' && r <= 'z'
}

func isDigitASCII(r rune) bool {
	return r >= '0' && r <= '9'
}

func keyBindingActionAliases(action keyBindingAction) []string {
	aliases := []string{action.ID}
	aliases = append(aliases, action.LegacyIDs...)
	return aliases
}

func keyBindingEditable(action keyBindingAction) bool {
	switch action.Tier {
	case keyBindingTierGuaranteedLaunchDefault, keyBindingTierUserConfigurableDirect, keyBindingTierTransportDependent, keyBindingTierNativePickerInternal:
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

func tmuxUnbindLines(actions []keyBindingAction) []string {
	plain := filterKeyBindingActions(actions, func(action keyBindingAction) bool {
		return len(keyBindingEffectivePlainChords(action)) != 0
	})
	sort.SliceStable(plain, func(i, j int) bool {
		return plain[i].PlainBindOrder < plain[j].PlainBindOrder
	})

	prefix := filterKeyBindingActions(actions, func(action keyBindingAction) bool {
		return action.PrefixChord != ""
	})
	sort.SliceStable(prefix, func(i, j int) bool {
		return prefix[i].PrefixBindOrder < prefix[j].PrefixBindOrder
	})

	lines := make([]string, 0, len(plain)+len(prefix))
	for _, action := range plain {
		for _, chord := range keyBindingEffectivePlainChords(action) {
			lines = append(lines, "unbind-key -q -n "+chord)
		}
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
	var plain, prefix []binding
	add := func(action keyBindingAction) {
		for _, chord := range keyBindingEffectivePlainChords(action) {
			if !seenNoPrefix[chord] {
				seenNoPrefix[chord] = true
				plain = append(plain, binding{chord: chord, order: action.PlainBindOrder})
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
	sort.SliceStable(prefix, func(i, j int) bool { return prefix[i].order < prefix[j].order })

	lines := make([]string, 0, len(plain)+len(prefix))
	for _, binding := range plain {
		lines = append(lines, "unbind-key -q -n "+binding.chord)
	}
	for _, binding := range prefix {
		lines = append(lines, "unbind-key -q "+binding.chord)
	}
	return lines
}

func tmuxRetiredKeyUnbindLines() []string {
	lines := []string{
		// Historical direct defaults removed during the keybinding surface
		// cleanup. Keep unbinding them so a live tmux server sourced from an
		// older projmux build does not retain stale behavior after reload.
		"unbind-key -q -n M-6",
		"unbind-key -q -n C-n",
		"unbind-key -q -n M-r",
	}
	for slot := 0; slot <= 12; slot++ {
		lines = append(lines, fmt.Sprintf("unbind-key -q -n User%d", slot))
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

	return lines
}

func renderTmuxBindLine(binaryPath, chord string, noPrefix bool, action keyBindingAction) string {
	parts := []string{"bind-key"}
	if noPrefix || strings.HasPrefix(chord, "M-") || strings.HasPrefix(chord, "C-") {
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
		if action.GhosttyTrigger == "" || action.GhosttyAction == "" {
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
			Action:  action.GhosttyAction,
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
		keys = append(keys, probeKey{
			ActionID:   action.ID,
			Label:      action.ProbeLabel,
			Action:     action.ProbeAction,
			Plain:      action.ProbePlain,
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
