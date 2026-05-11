package app

import (
	"os/exec"
	"strings"
)

// notification_toggle.go owns the OS-desktop-notification on/off switch.
// The toggle is intentionally orthogonal to the in-app notify queue,
// statusbar segment, and attention badge — those keep working when the
// desktop dispatch is silenced (loud-environment / screen-share use case).
//
// Resolution priority (highest first):
//  1. env `PROJMUX_DESKTOP_NOTIFY=on|off` (case-insensitive)
//  2. tmux global option `@projmux_desktop_notify` (`1` / `0`)
//  3. default = on
//
// The Settings popup exposes a row whose info line surfaces the source
// (`env` / `setting` / `default`) so users can see immediately why a
// toggle press would be ineffective when env is set.

const (
	// desktopNotifyTmuxOption controls the OS desktop notification on/off
	// toggle. tmux user-options carry the `@` prefix by convention.
	// Settings writes "1" or "0"; absent means "default".
	desktopNotifyTmuxOption = "@projmux_desktop_notify"

	// desktopNotifyEnv is the env-level override. `on` / `off` (case
	// insensitive). Any other value falls through to the tmux option.
	desktopNotifyEnv = "PROJMUX_DESKTOP_NOTIFY"
)

// desktopNotifySource describes where the effective desktop-notify state
// came from. Settings shows this label so the user understands why a
// toggle press might be ineffective (e.g. an env override pins the value).
type desktopNotifySource string

const (
	desktopNotifySourceEnv     desktopNotifySource = "env"
	desktopNotifySourceSetting desktopNotifySource = "setting"
	desktopNotifySourceDefault desktopNotifySource = "default"
)

// desktopNotifyResolver resolves the effective on/off state and reports the
// source so the Settings row can render it. The lookups are injected as
// function pointers so the resolution path stays testable without spawning
// real tmux processes.
type desktopNotifyResolver struct {
	lookupEnv func(string) string
	// readTmuxOption reads the global @projmux_desktop_notify user-option
	// (`tmux show-option -gqv ...`). It must return the trimmed string,
	// or empty when tmux is unavailable or the option is unset.
	readTmuxOption func(name string) string
}

// resolve returns (enabled, source). enabled = true means desktop
// notifications should fire.
func (r desktopNotifyResolver) resolve() (bool, desktopNotifySource) {
	if r.lookupEnv != nil {
		raw := strings.TrimSpace(r.lookupEnv(desktopNotifyEnv))
		switch strings.ToLower(raw) {
		case "off", "0", "false", "no":
			return false, desktopNotifySourceEnv
		case "on", "1", "true", "yes":
			return true, desktopNotifySourceEnv
		}
	}
	if r.readTmuxOption != nil {
		raw := strings.TrimSpace(r.readTmuxOption(desktopNotifyTmuxOption))
		switch raw {
		case "0":
			return false, desktopNotifySourceSetting
		case "1":
			return true, desktopNotifySourceSetting
		}
	}
	return true, desktopNotifySourceDefault
}

// desktopNotifyEnabled is a convenience wrapper used by the
// `aiDesktopNotifier.Notify` gate. The gate only needs the bool — the
// source is reserved for the Settings UI render path.
func (c *aiCommand) desktopNotifyEnabled() bool {
	enabled, _ := c.desktopNotifyResolution()
	return enabled
}

// desktopNotifyResolution returns the effective on/off state plus the
// source label. Splitting it out keeps the gate cheap (no source string
// formatting) while letting future call sites (e.g. diagnostics) reuse the
// same resolution path.
func (c *aiCommand) desktopNotifyResolution() (bool, desktopNotifySource) {
	resolver := desktopNotifyResolver{
		lookupEnv: c.lookupEnv,
		readTmuxOption: func(name string) string {
			// tmux show-option only makes sense inside a tmux client
			// (we follow the same TMUX gate used elsewhere in the
			// codebase — see tmuxProjdirOption in switch.go).
			if strings.TrimSpace(c.env("TMUX")) == "" {
				return ""
			}
			return c.readTrimmed("tmux", "show-option", "-gqv", name)
		},
	}
	return resolver.resolve()
}

// settingsDesktopNotifyResolver builds the same resolver from a
// `settingsCommand`. settingsCommand uses raw `runCommand` / direct
// `exec.Command` for its tmux reads, so we wire the lookup through
// `exec.Command` directly (mirroring `tmuxProjdirOption`).
func settingsDesktopNotifyResolver(lookupEnv func(string) string) desktopNotifyResolver {
	return desktopNotifyResolver{
		lookupEnv: lookupEnv,
		readTmuxOption: func(name string) string {
			if lookupEnv == nil || strings.TrimSpace(lookupEnv("TMUX")) == "" {
				return ""
			}
			out, err := exec.Command("tmux", "show-option", "-gqv", name).Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		},
	}
}
