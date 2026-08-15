package app

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/app/initcmd"
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

const (
	paneRenameActionID        = "rename-pane-label"
	retiredPaneRenameActionID = "rename-pane-topic"
)

// keyBindingAction is the in-app source of truth for built-in key actions.
// Terminal init adapters and tmux config rendering derive their concrete
// trigger/action tables from these entries.
type keyBindingAction struct {
	ID          string
	Aliases     []string
	Description string
	Kind        keyBindingActionKind
	Tier        keyBindingTier
	Surface     string
	Scope       keyBindingScope

	PlainChord  string
	PlainChords []string
	PrefixChord string

	TmuxKind        tmuxBindingKind
	TmuxBody        string
	TmuxBodyAliases []string
	TmuxPromptArgs  string
	Toggleable      bool

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
			ID:          "SessionPopupToggle",
			Description: "Existing session popup",
			Kind:        keyBindingActionTogglePopup,
			Tier:        keyBindingTierUserConfigurableDirect,
			Scope:       keyBindingScopeStandalone,
			TmuxKind:    tmuxBindingPopupToggle,
			TmuxBody:    "session-popup",
			Toggleable:  true,
		},
		{
			ID:          "Resources:Open",
			Description: "Open the read-only Project, Window, and Pane resource inspector",
			Kind:        keyBindingActionTogglePopup,
			Tier:        keyBindingTierUserConfigurableDirect,
			Scope:       keyBindingScopeStandalone,
			TmuxKind:    tmuxBindingPopupToggle,
			TmuxBody:    resourceInspectorPopupMode,
			Toggleable:  true,
		},
		{
			ID:             "RecentWindows:Open",
			Description:    "Recent windows queue across projects; switches to a live window without restoring a historical pane, distinct from last-pane and the existing-session popup.",
			Kind:           keyBindingActionTogglePopup,
			Tier:           keyBindingTierGuaranteedLaunchDefault,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "M-3",
			TmuxKind:       tmuxBindingPopupToggle,
			TmuxBody:       "recent-windows",
			Toggleable:     true,
			PlainBindOrder: 30,
			GhosttyTrigger: "alt+3",
			GhosttyAction:  `text:\x1b3`,
			GhosttyOrder:   30,
			WTID:           "User.projmuxRecentWindows",
			WTKeys:         "alt+3",
			WTInput:        "\x1b3",
			WTOrder:        30,
			ProbeOrder:     30,
			ProbeLabel:     "Alt-3",
			ProbeAction:    "Recent windows",
			ProbePlain:     "\x1b3",
		},
		{
			ID:              "AISplitPickerToggle",
			Description:     "Toggle the popup picker for choosing an AI split mode",
			Kind:            keyBindingActionTogglePopup,
			Tier:            keyBindingTierUserConfigurableDirect,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "M-7",
			TmuxKind:        tmuxBindingPopupToggle,
			TmuxBody:        "ai-split-picker-right",
			TmuxBodyAliases: []string{"ai-split-picker-down"},
			Toggleable:      true,
			PlainBindOrder:  55,
			GhosttyTrigger:  "alt+7",
			GhosttyAction:   `text:\x1b7`,
			GhosttyOrder:    55,
			WTID:            "User.projmuxAIPicker",
			WTKeys:          "alt+7",
			WTInput:         "\x1b7",
			WTOrder:         55,
			ProbeOrder:      55,
			ProbeLabel:      "Alt-7",
			ProbeAction:     "AI split picker right",
			ProbePlain:      "\x1b7",
		},
		{
			ID:             "SettingsToggle",
			Description:    "Settings",
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
			ID:              "AIResumePickerToggle",
			Description:     "Toggle the popup picker for resuming AI sessions",
			Kind:            keyBindingActionTogglePopup,
			Tier:            keyBindingTierGuaranteedLaunchDefault,
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "M-4",
			TmuxKind:        tmuxBindingPopupToggle,
			TmuxBody:        "ai-split-resume-right",
			TmuxBodyAliases: []string{"ai-split-resume-down"},
			Toggleable:      true,
			PlainBindOrder:  40,
			GhosttyTrigger:  "alt+4",
			GhosttyAction:   `text:\x1b4`,
			GhosttyOrder:    40,
			WTID:            "User.projmuxAIResumePicker",
			WTKeys:          "alt+4",
			WTInput:         "\x1b4",
			WTOrder:         40,
			ProbeOrder:      40,
			ProbeLabel:      "Alt-4",
			ProbeAction:     "AI resume picker right",
			ProbePlain:      "\x1b4",
		},
		{
			ID:             "ProjectSwitcherToggle",
			Description:    "Project switcher popup",
			Kind:           keyBindingActionTogglePopup,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "",
			TmuxKind:       tmuxBindingPopupToggle,
			TmuxBody:       "sessionizer",
			Toggleable:     true,
			PlainBindOrder: 60,
			ProbeOrder:     60,
			ProbeLabel:     "Alt-6",
			ProbeAction:    "Open sessionizer",
			ProbePlain:     "\x1b6",
		},
		{
			ID:             "rename-window",
			Description:    "Rename the current tmux window",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeStandalone,
			PlainChord:     "",
			TmuxKind:       tmuxBindingCommandPrompt,
			TmuxBody:       "rename-window -- '%%'",
			TmuxPromptArgs: "-I \"#{window_name}\"",
			PlainBindOrder: 70,
			ProbeOrder:     100,
			ProbeLabel:     "Ctrl-M",
			ProbeAction:    "Rename window",
			ProbePlain:     "\r",
		},
		{
			ID:             paneRenameActionID,
			Description:    "Set or clear the current tmux pane's user label",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierTransportDependent,
			Scope:          keyBindingScopeStandalone,
			TmuxKind:       tmuxBindingCommandPrompt,
			TmuxBody:       "if-shell -F '#{==:%1,}' 'set-option -p -u " + paneLabelOption + "' 'set-option -p " + paneLabelOption + " \"%1\"'",
			TmuxPromptArgs: "-p \"pane label:\" -I \"#{" + paneLabelOption + "}\"",
			ProbeOrder:     110,
			ProbeLabel:     "Ctrl-Shift-M",
			ProbeAction:    "Rename pane",
		},
		{
			ID:          "ai-split-right",
			Description: "Open a new AI split to the right",
			Kind:        keyBindingActionCommand,
			Tier:        keyBindingTierUserConfigurableDirect,
			Scope:       keyBindingScopeStandalone,
			TmuxKind:    tmuxBindingRunProjmux,
			TmuxBody:    "ai split right",
			WTID:        "User.projmuxAISplitRight",
			WTKeys:      "ctrl+shift+r",
			WTInput:     "\x02r",
			WTOrder:     70,
			ProbeOrder:  80,
			ProbeLabel:  "Ctrl-Shift-R",
			ProbeAction: "No projmux binding by default",
		},
		{
			ID:          "ai-split-down",
			Description: "Open a new AI split below",
			Kind:        keyBindingActionCommand,
			Tier:        keyBindingTierUserConfigurableDirect,
			Scope:       keyBindingScopeStandalone,
			TmuxKind:    tmuxBindingRunProjmux,
			TmuxBody:    "ai split down",
			WTID:        "User.projmuxAISplitDown",
			WTKeys:      "ctrl+shift+l",
			WTInput:     "\x02l",
			WTOrder:     80,
			ProbeOrder:  90,
			ProbeLabel:  "Ctrl-Shift-L",
			ProbeAction: "No projmux binding by default",
		},
		{
			ID:             "ai-split-codex-right",
			Description:    "Open a Codex split to the right without the picker",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeStandalone,
			TmuxKind:       tmuxBindingRunProjmux,
			TmuxBody:       "ai split --agent codex right",
			PlainBindOrder: 82,
		},
		{
			ID:             "ai-split-codex-down",
			Description:    "Open a Codex split below without the picker",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeStandalone,
			TmuxKind:       tmuxBindingRunProjmux,
			TmuxBody:       "ai split --agent codex down",
			PlainBindOrder: 83,
		},
		{
			ID:             "ai-split-claude-right",
			Description:    "Open a Claude split to the right without the picker",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeStandalone,
			TmuxKind:       tmuxBindingRunProjmux,
			TmuxBody:       "ai split --agent claude right",
			PlainBindOrder: 84,
		},
		{
			ID:             "ai-split-claude-down",
			Description:    "Open a Claude split below without the picker",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeStandalone,
			TmuxKind:       tmuxBindingRunProjmux,
			TmuxBody:       "ai split --agent claude down",
			PlainBindOrder: 85,
		},
		{
			ID:             "ai-split-shell-right",
			Description:    "Open a shell split to the right without the picker",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeStandalone,
			TmuxKind:       tmuxBindingRunProjmux,
			TmuxBody:       "ai split --agent shell right",
			PlainBindOrder: 86,
		},
		{
			ID:             "ai-split-shell-down",
			Description:    "Open a shell split below without the picker",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeStandalone,
			TmuxKind:       tmuxBindingRunProjmux,
			TmuxBody:       "ai split --agent shell down",
			PlainBindOrder: 87,
		},
		{
			ID:          "current-project-session",
			Description: "Jump to current pane project session",
			Kind:        keyBindingActionCommand,
			Tier:        keyBindingTierUserConfigurableDirect,
			Scope:       keyBindingScopeStandalone,
			TmuxKind:    tmuxBindingRunProjmux,
			TmuxBody:    "current",
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
			ID:             "last-pane",
			Description:    "Return to the previously active pane",
			Kind:           keyBindingActionCommand,
			Tier:           keyBindingTierUserConfigurableDirect,
			Scope:          keyBindingScopeApp,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "last-pane",
			PlainBindOrder: 45,
		},
		{
			ID:          "toggle-mouse",
			Description: "Toggle tmux mouse mode",
			Kind:        keyBindingActionCommand,
			Tier:        keyBindingTierUserConfigurableDirect,
			Scope:       keyBindingScopeApp,
			TmuxKind:    tmuxBindingCommand,
			TmuxBody:    "if -F \"#{mouse}\" \"set -g mouse off \\; display-message 'tmux mouse: off'\" \"set -g mouse on \\; display-message 'tmux mouse: on'\"",
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
			ID:          "NotifySidebar:AckGroup",
			Description: "Acknowledge every visible notification in the selected group",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "A",
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
			ID:          "NotifySidebar:ClearGone",
			Description: "Clear gone notifications",
			Kind:        keyBindingActionPickerInternal,
			Tier:        keyBindingTierNativePickerInternal,
			Surface:     "NotifySidebar",
			PlainChord:  "g",
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

// Settings-owned keybinding navigation categories. Every catalog action
// belongs to exactly one category, and the assignment is explicit metadata in
// keyBindingCategoryByActionID: it is never inferred from the action ID prefix,
// because the IDs are a compatibility surface and their spelling is not a
// product taxonomy.
const (
	keyBindingCategoryLaunch     = "launch-and-popups"
	keyBindingCategoryAgentPane  = "agent-and-pane-launch"
	keyBindingCategoryNavigation = "pane-and-window-navigation"
	keyBindingCategorySurfaces   = "sidebar-and-picker-actions"
	keyBindingCategoryInput      = "input-delivery"

	keyBindingCategoryLaunchLabel     = "Launch & popups"
	keyBindingCategoryAgentPaneLabel  = "Agent & Pane launch"
	keyBindingCategoryNavigationLabel = "Pane & Window navigation"
	keyBindingCategorySurfacesLabel   = "Sidebar & picker actions"
	keyBindingCategoryInputLabel      = "Input delivery"
)

// keyBindingCategoryOrder is the render order of the Keybindings categories.
// Input delivery holds no keymap action; it owns the native-key toggle only.
var keyBindingCategoryOrder = []struct {
	ID    string
	Label string
}{
	{keyBindingCategoryLaunch, keyBindingCategoryLaunchLabel},
	{keyBindingCategoryAgentPane, keyBindingCategoryAgentPaneLabel},
	{keyBindingCategoryNavigation, keyBindingCategoryNavigationLabel},
	{keyBindingCategorySurfaces, keyBindingCategorySurfacesLabel},
	{keyBindingCategoryInput, keyBindingCategoryInputLabel},
}

// keyBindingSurfaceOrder is the second navigation level inside the
// sidebar/picker category. The surface keys are the existing catalog `Surface`
// values; only the display labels follow the canonical resource vocabulary.
var keyBindingSurfaceOrder = []struct {
	ID    string
	Label string
}{
	{"Sidebar", "Project Sidebar"},
	{"SessionPopup", "Session Picker"},
	{"NotifySidebar", "Notification Sidebar"},
	{"Settings", "Settings"},
}

// keyBindingCategoryByActionID assigns every catalog action to exactly one
// category. Exhaustiveness in both directions is a test.
var keyBindingCategoryByActionID = map[string]string{
	"ProjectSidebarToggle":  keyBindingCategoryLaunch,
	"NotifySidebarToggle":   keyBindingCategoryLaunch,
	"SessionPopupToggle":    keyBindingCategoryLaunch,
	"Resources:Open":        keyBindingCategoryLaunch,
	"RecentWindows:Open":    keyBindingCategoryLaunch,
	"AISplitPickerToggle":   keyBindingCategoryLaunch,
	"SettingsToggle":        keyBindingCategoryLaunch,
	"AIResumePickerToggle":  keyBindingCategoryLaunch,
	"ProjectSwitcherToggle": keyBindingCategoryLaunch,

	"ai-split-right":        keyBindingCategoryAgentPane,
	"ai-split-down":         keyBindingCategoryAgentPane,
	"ai-split-codex-right":  keyBindingCategoryAgentPane,
	"ai-split-codex-down":   keyBindingCategoryAgentPane,
	"ai-split-claude-right": keyBindingCategoryAgentPane,
	"ai-split-claude-down":  keyBindingCategoryAgentPane,
	"ai-split-shell-right":  keyBindingCategoryAgentPane,
	"ai-split-shell-down":   keyBindingCategoryAgentPane,

	"current-project-session": keyBindingCategoryNavigation,
	"new-window":              keyBindingCategoryNavigation,
	"rename-window":           keyBindingCategoryNavigation,
	paneRenameActionID:        keyBindingCategoryNavigation,
	"toggle-mouse":            keyBindingCategoryNavigation,
	"previous-window":         keyBindingCategoryNavigation,
	"next-window":             keyBindingCategoryNavigation,
	"last-pane":               keyBindingCategoryNavigation,
	"select-pane-left":        keyBindingCategoryNavigation,
	"select-pane-right":       keyBindingCategoryNavigation,
	"select-pane-up":          keyBindingCategoryNavigation,
	"select-pane-down":        keyBindingCategoryNavigation,

	"Sidebar:PinProject":                  keyBindingCategorySurfaces,
	"Sidebar:KillSession":                 keyBindingCategorySurfaces,
	"SessionPopup:KillSession":            keyBindingCategorySurfaces,
	"SessionPopup:OpenState":              keyBindingCategorySurfaces,
	"SessionPopup:CyclePreviewWindowPrev": keyBindingCategorySurfaces,
	"SessionPopup:CyclePreviewWindowNext": keyBindingCategorySurfaces,
	"SessionPopup:CyclePreviewPanePrev":   keyBindingCategorySurfaces,
	"SessionPopup:CyclePreviewPaneNext":   keyBindingCategorySurfaces,
	"NotifySidebar:FocusAndAck":           keyBindingCategorySurfaces,
	"NotifySidebar:Ack":                   keyBindingCategorySurfaces,
	"NotifySidebar:AckGroup":              keyBindingCategorySurfaces,
	"NotifySidebar:ClearNonCritical":      keyBindingCategorySurfaces,
	"NotifySidebar:ClearAll":              keyBindingCategorySurfaces,
	"NotifySidebar:ClearGone":             keyBindingCategorySurfaces,
	"Settings:SwitchTabPrev":              keyBindingCategorySurfaces,
	"Settings:SwitchTabNext":              keyBindingCategorySurfaces,
}

// keyBindingDisplayNames is the canonical display label for every catalog
// action. The labels follow the shared resource vocabulary (Project, Window,
// Pane, Agent, Provider, Notification, Snapshot); the keymap action IDs above
// keep their current spelling, so a label change never rewrites a saved
// `keymap.toml` table or a runtime route.
var keyBindingDisplayNames = map[string]string{
	"ProjectSidebarToggle":  "Open / close Project Sidebar",
	"NotifySidebarToggle":   "Open / close Notification Sidebar",
	"SessionPopupToggle":    "Open / close Session Picker",
	"Resources:Open":        "Open Resource Inspector",
	"RecentWindows:Open":    "Open Recent Windows",
	"AISplitPickerToggle":   "Open Agent / Pane Launcher",
	"SettingsToggle":        "Open / close Settings",
	"AIResumePickerToggle":  "Open Agent Resume Picker",
	"ProjectSwitcherToggle": "Open Project Picker",

	"ai-split-right":        "Launch default target right",
	"ai-split-down":         "Launch default target down",
	"ai-split-codex-right":  "Create Codex Agent right",
	"ai-split-codex-down":   "Create Codex Agent down",
	"ai-split-claude-right": "Create Claude Agent right",
	"ai-split-claude-down":  "Create Claude Agent down",
	"ai-split-shell-right":  "Create Shell Pane right",
	"ai-split-shell-down":   "Create Shell Pane down",

	"current-project-session": "Open Project for Current Directory",
	"new-window":              "Create Window",
	"rename-window":           "Rename Window",
	paneRenameActionID:        "Rename Pane",
	"toggle-mouse":            "Toggle mouse",
	"previous-window":         "Focus previous Window",
	"next-window":             "Focus next Window",
	"last-pane":               "Focus last Pane",
	"select-pane-left":        "Focus Pane left",
	"select-pane-right":       "Focus Pane right",
	"select-pane-up":          "Focus Pane up",
	"select-pane-down":        "Focus Pane down",

	"Sidebar:PinProject":                  "Pin / unpin Project",
	"Sidebar:KillSession":                 "Stop Project Runtime",
	"SessionPopup:KillSession":            "Stop Runtime Session",
	"SessionPopup:OpenState":              "Open Snapshots",
	"SessionPopup:CyclePreviewWindowPrev": "Preview previous Window",
	"SessionPopup:CyclePreviewWindowNext": "Preview next Window",
	"SessionPopup:CyclePreviewPanePrev":   "Preview previous Pane",
	"SessionPopup:CyclePreviewPaneNext":   "Preview next Pane",
	"NotifySidebar:FocusAndAck":           "Focus source and acknowledge Notification",
	"NotifySidebar:Ack":                   "Acknowledge Notification",
	"NotifySidebar:AckGroup":              "Acknowledge Notification group",
	"NotifySidebar:ClearNonCritical":      "Clear non-critical Notifications",
	"NotifySidebar:ClearAll":              "Clear all Notifications",
	"NotifySidebar:ClearGone":             "Clear gone Notifications",
	"Settings:SwitchTabPrev":              "Previous Settings tab",
	"Settings:SwitchTabNext":              "Next Settings tab",
}

// keyBindingActionSemantics is the product meaning of a key action, projected
// into the Settings action detail. It answers the four questions the target
// action detail asks — what resource the action targets, what it produces,
// where the result is placed, and which anchor it is placed against — without
// touching the action ID, the tmux body, or the keymap file.
//
// The anchor field is the contract that keeps interactive splits adjacent to
// the pane the user pressed the key in: an interactive right/down action
// passes the current Pane as an explicit anchor, and never falls back to a
// Window's persisted primary Pane or to whatever pane happens to be focused.
type keyBindingActionSemantics struct {
	TargetKind string
	ResultKind string
	Placement  string
	Anchor     string
}

const (
	keyBindingAnchorCurrentPane = "current Pane (explicit)"
	keyBindingPlacementRight    = "right"
	keyBindingPlacementDown     = "down"
)

// keyBindingActionSemanticsByID declares the semantics of the actions whose
// target/result is a product decision rather than a self-evident one. Actions
// with no entry render no semantic rows.
var keyBindingActionSemanticsByID = map[string]keyBindingActionSemantics{
	"ai-split-right": {TargetKind: "Pane", ResultKind: "default launch target", Placement: keyBindingPlacementRight, Anchor: keyBindingAnchorCurrentPane},
	"ai-split-down":  {TargetKind: "Pane", ResultKind: "default launch target", Placement: keyBindingPlacementDown, Anchor: keyBindingAnchorCurrentPane},

	"ai-split-codex-right":  {TargetKind: "Agent", ResultKind: "always a new Agent", Placement: keyBindingPlacementRight, Anchor: keyBindingAnchorCurrentPane},
	"ai-split-codex-down":   {TargetKind: "Agent", ResultKind: "always a new Agent", Placement: keyBindingPlacementDown, Anchor: keyBindingAnchorCurrentPane},
	"ai-split-claude-right": {TargetKind: "Agent", ResultKind: "always a new Agent", Placement: keyBindingPlacementRight, Anchor: keyBindingAnchorCurrentPane},
	"ai-split-claude-down":  {TargetKind: "Agent", ResultKind: "always a new Agent", Placement: keyBindingPlacementDown, Anchor: keyBindingAnchorCurrentPane},

	"ai-split-shell-right": {TargetKind: "Pane", ResultKind: "Shell Pane", Placement: keyBindingPlacementRight, Anchor: keyBindingAnchorCurrentPane},
	"ai-split-shell-down":  {TargetKind: "Pane", ResultKind: "Shell Pane", Placement: keyBindingPlacementDown, Anchor: keyBindingAnchorCurrentPane},

	"AIResumePickerToggle": {TargetKind: "Agent", ResultKind: "resume an existing Offline or Failed Agent"},
	"AISplitPickerToggle":  {TargetKind: "Agent", ResultKind: "choose a launch target"},

	"new-window":               {TargetKind: "Window", ResultKind: "new Window with its initial Pane"},
	"current-project-session":  {TargetKind: "Project", ResultKind: "open the Project for the current directory"},
	"Sidebar:KillSession":      {TargetKind: "Project", ResultKind: "stop the Project runtime; Project metadata is kept"},
	"SessionPopup:KillSession": {TargetKind: "Session", ResultKind: "stop a runtime Session"},
	"SessionPopup:OpenState":   {TargetKind: "Snapshot", ResultKind: "open Snapshots for the focused Session"},
}

func keyBindingActionSemanticsFor(action keyBindingAction) (keyBindingActionSemantics, bool) {
	semantics, ok := keyBindingActionSemanticsByID[action.ID]
	return semantics, ok
}

func keyBindingDisplayName(action keyBindingAction) string {
	if name := strings.TrimSpace(keyBindingDisplayNames[action.ID]); name != "" {
		return name
	}
	return humanizeKeyBindingActionID(action.ID)
}

// keyBindingActionCategory returns the navigation category an action belongs
// to. An unassigned action returns false so the Settings loop can fail loudly
// instead of hiding the action from every category.
func keyBindingActionCategory(action keyBindingAction) (string, bool) {
	category, ok := keyBindingCategoryByActionID[action.ID]
	return category, ok
}

// keyBindingSurfaceLabel maps a catalog `Surface` value to its canonical
// display label.
func keyBindingSurfaceLabel(surface string) (string, bool) {
	for _, entry := range keyBindingSurfaceOrder {
		if entry.ID == surface {
			return entry.Label, true
		}
	}
	return "", false
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
	return uniqueNonEmptyStrings(append([]string{action.ID}, action.Aliases...))
}

func keyBindingActionIsPopupToggle(action keyBindingAction) bool {
	return action.Kind == keyBindingActionTogglePopup &&
		action.TmuxKind == tmuxBindingPopupToggle &&
		action.Toggleable
}

func popupToggleModesForAction(action keyBindingAction) []string {
	if !keyBindingActionIsPopupToggle(action) {
		return nil
	}
	modes := append([]string{action.TmuxBody}, action.TmuxBodyAliases...)
	return uniqueNonEmptyStrings(modes)
}

func popupToggleActionIDByModeFromCatalog(actions []keyBindingAction, mode string) (string, bool) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "", false
	}
	for _, action := range actions {
		if slices.Contains(popupToggleModesForAction(action), mode) {
			return action.ID, true
		}
	}
	return "", false
}

func popupToggleActionIDForMode(mode string) (string, bool) {
	return popupToggleActionIDByModeFromCatalog(defaultKeyBindingCatalog(), mode)
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
		// Legacy: retained for cleaning stale tmux keybindings from live tmux
		// after reload; sunset when retired bindings from the 0.7/keybinding
		// cleanup era have aged out and live stale-binding cleanup is
		// intentionally stopped.
		"unbind-key -q -n M-6",
		"unbind-key -q -n C-n",
		"unbind-key -q -n M-r",
		// rename-pane-topic used C-t before the canonical pane-label action
		// replaced it. A generated config reload must retire that live root
		// binding even after the old action has disappeared from keymap.toml.
		"unbind-key -q -n C-t",
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
		return "run-shell " + tmuxConfigQuote(bin+" internal tmux popup-toggle --client #{client_tty} "+action.TmuxBody)
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

// newInitCommand wires terminal remediation with the bundled terminal
// adapters, injecting the desired bindings derived from the keybinding catalog.
func newInitCommand() *initcmd.Command {
	return initcmd.New(
		initcmd.NewGhosttyAdapter(ghosttyBindingsFromCatalog()),
		initcmd.NewWindowsTerminalAdapter(windowsTerminalBindingsFromCatalog()),
	)
}

func ghosttyBindingsFromCatalog() []initcmd.GhosttyBinding {
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

	out := make([]initcmd.GhosttyBinding, 0, len(actions))
	for _, action := range actions {
		out = append(out, initcmd.GhosttyBinding{
			Trigger: action.GhosttyTrigger,
			Action:  action.GhosttyAction,
		})
	}
	return out
}

func windowsTerminalBindingsFromCatalog() []initcmd.WTBinding {
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

	out := make([]initcmd.WTBinding, 0, len(actions))
	for _, action := range actions {
		out = append(out, initcmd.WTBinding{
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
