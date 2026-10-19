package tmuxconfig

type ControllerTriggerReason string

const (
	// controllerTriggerConfigApply is the generated-config reload boundary. It
	// is the one producer with no hook session, because the reload is what
	// installs the hooks.
	ControllerTriggerConfigApply ControllerTriggerReason = "config-apply"
	// controllerTriggerRuntimeCreated is tmux's after-new-window and
	// after-split-window.
	ControllerTriggerRuntimeCreated ControllerTriggerReason = "runtime-created"
	// controllerTriggerPaneExited is tmux's pane-exited. It carries exact
	// #{hook_pane}; its owner Window's exact live $N/@N binding is persisted by
	// observation because current-context hook formats can name a survivor.
	ControllerTriggerPaneExited ControllerTriggerReason = "pane-exited"
	// controllerTriggerPaneKilled is tmux's after-kill-pane. That hook cannot
	// name the dead Pane, so it retains the whole-host reobservation.
	ControllerTriggerPaneKilled ControllerTriggerReason = "pane-killed"
	// controllerTriggerWindowUnlinked is tmux's window-unlinked. A standalone
	// firing with exact #{hook_window} is explicit Window termination evidence;
	// the absence pass remains whole-host because the dead window no longer has
	// a queryable Registry mirror.
	ControllerTriggerWindowUnlinked ControllerTriggerReason = "window-unlinked"
)

// controllerTriggerReasons returns the closed reason set in declaration order.
// The generated hook bodies and the route parser share it, so a reason cannot be
// spelled in a config that the binary refuses to parse.
func ControllerTriggerReasons() []ControllerTriggerReason {
	return []ControllerTriggerReason{
		ControllerTriggerConfigApply,
		ControllerTriggerRuntimeCreated,
		ControllerTriggerPaneExited,
		ControllerTriggerPaneKilled,
		ControllerTriggerWindowUnlinked,
	}
}
