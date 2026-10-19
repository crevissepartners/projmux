package keybinding

import (
	"github.com/crevissepartners/projmux/internal/cli"
	"strings"
)

// keyBindingActionHandler pins the exact shipped handler one key action
// dispatches to. It remains internal correctness/search metadata: Invocation
// is what the generated tmux config actually runs, and
// Manifest/Disposition/Canonical are projected from the shipped CLI command
// manifest in internal/cli rather than from a second hand-kept table.
type keyBindingActionHandler struct {
	// Invocation is the exact shipped command the binding runs.
	Invocation string
	// Manifest is the resolved manifest route path. It is empty when the
	// handler is not a projmux route at all (a direct tmux command, or a
	// command handled inside the running picker).
	Manifest string
	// Disposition is the manifest classification of the resolved route.
	Disposition string
	// Canonical is the manifest's canonical spelling list for the route.
	Canonical []string
	// Note pins a handler boundary the invocation alone does not state.
	Note string
}

// KeyBindingActionHandlerFor projects one action's shipped handler.
func KeyBindingActionHandlerFor(action KeyBindingAction) (keyBindingActionHandler, bool) {
	handler, ok := keyBindingActionShippedInvocation(action)
	if !ok {
		return keyBindingActionHandler{}, false
	}
	handler.Note = action.HandlerBoundaryNote
	return handler, true
}

func keyBindingActionShippedInvocation(action KeyBindingAction) (keyBindingActionHandler, bool) {
	if action.Kind == KeyBindingActionPickerInternal {
		label, ok := KeyBindingSurfaceLabel(action.Surface)
		if !ok {
			label = action.Surface
		}
		return keyBindingActionHandler{Invocation: "handled inside the open " + label + " picker"}, true
	}
	switch action.TmuxKind {
	case TmuxBindingPopupToggle:
		return keyBindingHandlerFromManifest(
			[]string{"internal", "tmux", "popup-toggle"},
			"projmux internal tmux popup-toggle "+strings.TrimSpace(action.TmuxBody),
		), true
	case TmuxBindingRunProjmux:
		body := strings.TrimSpace(action.TmuxBody)
		return keyBindingHandlerFromManifest(strings.Fields(body), "projmux "+body), true
	case TmuxBindingCommand:
		return keyBindingActionHandler{Invocation: "tmux " + condenseTmuxHandlerBody(action.TmuxBody)}, true
	case TmuxBindingCommandPrompt:
		return keyBindingActionHandler{Invocation: "tmux command-prompt then " + condenseTmuxHandlerBody(action.TmuxBody)}, true
	case TmuxBindingPromptRunProjmux:
		body := strings.TrimSpace(action.TmuxBody)
		return keyBindingHandlerFromManifest(strings.Fields(body), "tmux command-prompt then projmux "+body), true
	case TmuxBindingManagedDelete:
		body := strings.TrimSpace(action.TmuxBody)
		return keyBindingHandlerFromManifest(strings.Fields(body),
			"tmux if-shell "+action.TmuxManagedGuard+" then projmux "+body+", else tmux's stock binding"), true
	}
	return keyBindingActionHandler{}, false
}

func keyBindingHandlerFromManifest(tokens []string, invocation string) keyBindingActionHandler {
	handler := keyBindingActionHandler{Invocation: invocation}
	path, route, ok := cli.Resolve(tokens)
	if !ok {
		return handler
	}
	handler.Manifest = strings.Join(path, " ")
	top, found := cli.LookupRoute(path[0])
	if found {
		handler.Disposition = string(top.Disposition)
	}
	handler.Canonical = append([]string{}, route.Canonical...)
	if len(handler.Canonical) == 0 && found {
		handler.Canonical = append([]string{}, top.Canonical...)
	}
	return handler
}

// condenseTmuxHandlerBody keeps a direct tmux body bounded in non-visible
// search metadata without hiding which tmux command actually runs.
func condenseTmuxHandlerBody(body string) string {
	condensed := strings.Join(strings.Fields(body), " ")
	const max = 44
	if len(condensed) <= max {
		return condensed
	}
	return strings.TrimSpace(condensed[:max]) + " ..."
}
