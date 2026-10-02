package app

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/app/initcmd"
	"github.com/crevissepartners/projmux/internal/app/keybinding"
	"github.com/crevissepartners/projmux/internal/app/setupcmd"
)

// newInitCommand wires terminal remediation with the bundled terminal
// adapters, injecting the desired bindings derived from the keybinding catalog.
func newInitCommand() *initcmd.Command {
	return initcmd.New(
		initcmd.NewGhosttyAdapter(ghosttyBindingsFromCatalog()),
		initcmd.NewWindowsTerminalAdapter(windowsTerminalBindingsFromCatalog()),
	)
}

func ghosttyBindingsFromCatalog() []initcmd.GhosttyBinding {
	var actions []keybinding.KeyBindingAction
	for _, action := range keybinding.DefaultKeyBindingCatalog() {
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
	var actions []keybinding.KeyBindingAction
	for _, action := range keybinding.DefaultKeyBindingCatalog() {
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

// newSetupCommand wires the setup probe with the probe keys derived from the
// keybinding catalog and with terminal as its `setup terminal` remediation.
func newSetupCommand(terminal *initcmd.Command) *setupcmd.Command {
	var remediation setupcmd.TerminalRemediation
	if terminal != nil {
		remediation = terminal
	}
	return setupcmd.New(probeKeysFromCatalog(), remediation, os.Getenv)
}

// probeKeysFromCatalog returns the keys the setup probe checks. Sequences are
// derived from the same keybinding catalog as the tmux and terminal remediation
// renderers.
func probeKeysFromCatalog() []setupcmd.ProbeKey {
	return probeKeysFromActions(keybinding.DefaultKeyBindingCatalog())
}

func probeKeysFromActions(catalog []keybinding.KeyBindingAction) []setupcmd.ProbeKey {
	var actions []keybinding.KeyBindingAction
	for _, action := range catalog {
		if action.ProbeLabel != "" {
			actions = append(actions, action)
		}
	}
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i].ProbeOrder < actions[j].ProbeOrder
	})

	keys := make([]setupcmd.ProbeKey, 0, len(actions))
	for _, action := range actions {
		keys = append(keys, setupcmd.ProbeKey{
			ActionID:   action.ID,
			Label:      action.ProbeLabel,
			Action:     action.ProbeAction,
			Plain:      action.ProbePlain,
			PlainChord: keybinding.FirstNonEmptyString(keybinding.KeyBindingEffectivePlainChords(action)),
		})
	}
	return keys
}

func suggestedPlainChordForSequence(seq []byte) (string, bool) {
	if len(seq) == 0 || setupcmd.IsAmbiguousEnterSequence(seq) {
		return "", false
	}
	got := string(seq)
	for _, action := range keybinding.DefaultKeyBindingCatalog() {
		if action.ProbePlain != "" && action.ProbePlain == got && !setupcmd.IsAmbiguousEnterSequence([]byte(action.ProbePlain)) {
			if chord := keybinding.FirstNonEmptyString(keybinding.KeyBindingEffectivePlainChords(action)); chord != "" {
				return chord, true
			}
			if chord := probeLabelToTmuxChord(action.ProbeLabel); chord != "" {
				return chord, true
			}
		}
	}
	if len(seq) == 2 && seq[0] == 0x1b && seq[1] >= 0x21 && seq[1] <= 0x7e {
		return "M-" + string(seq[1]), true
	}
	if len(seq) == 1 && seq[0] >= 0x01 && seq[0] <= 0x1a {
		return fmt.Sprintf("C-%c", 'a'+seq[0]-1), true
	}
	if len(seq) == 1 && seq[0] >= 0x21 && seq[0] <= 0x7e {
		chord := string(seq)
		if err := keybinding.ValidateKeymapChord(chord); err == nil {
			return chord, true
		}
	}
	return "", false
}

func probeLabelToTmuxChord(label string) string {
	label = strings.TrimSpace(label)
	label = strings.ReplaceAll(label, "Alt-Shift-", "M-S-")
	label = strings.ReplaceAll(label, "Alt-", "M-")
	label = strings.ReplaceAll(label, "Ctrl-", "C-")
	return label
}
