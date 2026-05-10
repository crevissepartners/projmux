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

// keyBindingAction is the in-app source of truth for built-in key actions.
// Terminal init adapters and tmux config rendering derive their concrete
// trigger/action tables from these entries.
type keyBindingAction struct {
	ID          string
	Description string
	Scope       keyBindingScope

	PlainChord  string
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
			ID:              "sessionizer-sidebar",
			Description:     "Project sidebar",
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
			ID:             "notify-sidebar",
			Description:    "Notify sidebar",
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
			ID:              "session-popup",
			Description:     "Existing session popup",
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
			ID:             "ai-split-picker-right",
			Description:    "AI split picker",
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
			ID:             "ai-split-settings",
			Description:    "Settings",
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
			ID:              "sessionizer",
			Description:     "Project switcher popup",
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "M-6",
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
			Scope:           keyBindingScopeStandalone,
			PlainChord:      "M-r",
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
			Scope:          keyBindingScopeApp,
			PlainChord:     "C-n",
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
			ID:             "previous-window",
			Description:    "Previous tmux window",
			Scope:          keyBindingScopeApp,
			PlainChord:     "M-S-Left",
			UserSlot:       8,
			CSIu:           "9009",
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "previous-window",
			PlainBindOrder: 60,
			UserBindOrder:  20,
			GhosttyTrigger: "alt+shift+left",
			GhosttyOrder:   120,
			WTID:           "User.projmuxPrevWindow",
			WTKeys:         "alt+shift+left",
			WTInput:        "\x1b[1;4D",
			WTOrder:        100,
			ProbeOrder:     120,
			ProbeLabel:     "Alt-Shift-Left",
			ProbeAction:    "Previous window (User8)",
			ProbePlain:     "\x1b[1;4D",
		},
		{
			ID:             "next-window",
			Description:    "Next tmux window",
			Scope:          keyBindingScopeApp,
			PlainChord:     "M-S-Right",
			UserSlot:       9,
			CSIu:           "9010",
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "next-window",
			PlainBindOrder: 70,
			UserBindOrder:  30,
			GhosttyTrigger: "alt+shift+right",
			GhosttyOrder:   130,
			WTID:           "User.projmuxNextWindow",
			WTKeys:         "alt+shift+right",
			WTInput:        "\x1b[1;4C",
			WTOrder:        110,
			ProbeOrder:     130,
			ProbeLabel:     "Alt-Shift-Right",
			ProbeAction:    "Next window (User9)",
			ProbePlain:     "\x1b[1;4C",
		},
		{
			ID:             "select-pane-left",
			Description:    "Move focus to the left pane",
			Scope:          keyBindingScopeApp,
			PlainChord:     "M-Left",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "select-pane -L",
			PlainBindOrder: 10,
		},
		{
			ID:             "select-pane-right",
			Description:    "Move focus to the right pane",
			Scope:          keyBindingScopeApp,
			PlainChord:     "M-Right",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "select-pane -R",
			PlainBindOrder: 20,
		},
		{
			ID:             "select-pane-up",
			Description:    "Move focus to the pane above",
			Scope:          keyBindingScopeApp,
			PlainChord:     "M-Up",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "select-pane -U",
			PlainBindOrder: 30,
		},
		{
			ID:             "select-pane-down",
			Description:    "Move focus to the pane below",
			Scope:          keyBindingScopeApp,
			PlainChord:     "M-Down",
			UserSlot:       noUserSlot,
			TmuxKind:       tmuxBindingCommand,
			TmuxBody:       "select-pane -D",
			PlainBindOrder: 40,
		},
		{
			ID:              "toggle-mouse",
			Description:     "Toggle tmux mouse mode",
			Scope:           keyBindingScopeApp,
			PrefixChord:     "M",
			UserSlot:        noUserSlot,
			TmuxKind:        tmuxBindingCommand,
			TmuxBody:        "if -F \"#{mouse}\" \"set -g mouse off \\; display-message 'tmux mouse: off'\" \"set -g mouse on \\; display-message 'tmux mouse: on'\"",
			PrefixBindOrder: 10,
		},
	}
}

func keyBindingCatalogForScope(scope keyBindingScope) []keyBindingAction {
	var out []keyBindingAction
	for _, action := range defaultKeyBindingCatalog() {
		if action.Scope == scope {
			out = append(out, action)
		}
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
		return action.PlainChord != ""
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

	lines := make([]string, 0, len(plain)+len(user))
	for _, action := range plain {
		lines = append(lines, "unbind-key -q -n "+action.PlainChord)
	}
	for _, action := range user {
		lines = append(lines, "unbind-key -q -n "+keyBindingUserKey(action))
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
		if action.PlainChord != "" {
			bindings = append(bindings, struct {
				chord string
				line  string
				order int
			}{
				chord: action.PlainChord,
				line:  renderTmuxBindLine(binaryPath, action.PlainChord, false, action),
				order: action.PlainBindOrder,
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

	bindings = bindings[:0]
	for _, action := range actions {
		if action.PrefixChord != "" {
			bindings = append(bindings, struct {
				chord string
				line  string
				order int
			}{
				chord: action.PrefixChord,
				line:  renderTmuxBindLine(binaryPath, action.PrefixChord, false, action),
				order: action.PrefixBindOrder,
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
	var actions []keyBindingAction
	for _, action := range defaultKeyBindingCatalog() {
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
			Label:   action.ProbeLabel,
			Action:  action.ProbeAction,
			Plain:   action.ProbePlain,
			CSIu:    csiu,
			UserKey: userKey,
		})
	}
	return keys
}
