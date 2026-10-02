package app

import (
	"sort"

	"github.com/crevissepartners/projmux/internal/app/initcmd"
	"github.com/crevissepartners/projmux/internal/app/keybinding"
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

func probeKeysFromCatalog() []probeKey {
	return probeKeysFromActions(keybinding.DefaultKeyBindingCatalog())
}

func probeKeysFromActions(catalog []keybinding.KeyBindingAction) []probeKey {
	var actions []keybinding.KeyBindingAction
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
			PlainChord: keybinding.FirstNonEmptyString(keybinding.KeyBindingEffectivePlainChords(action)),
		})
	}
	return keys
}
