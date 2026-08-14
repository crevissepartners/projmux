package app

import (
	"context"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/mux"
)

// notification_toggle.go owns the OS-desktop-notification mode selector.
// The selector is intentionally orthogonal to the in-app notify queue,
// statusbar segment, and attention badge — those keep working regardless
// of which desktop dispatch mode is active (loud-environment / screen-share
// use case).
//
// The setting carries two values:
//
//	none    — no toast, no notify-send
//	notify  — OS notification only
//
// Delivery never focuses or raises the host terminal window, the Toast never
// carries a click URI, and no URI protocol handler is registered. The mode
// only gates whether to fire the OS notification at all.
//
// Resolution priority (highest first):
//  1. env `PROJMUX_DESKTOP_NOTIFY_MODE=off|none|notify` (case-insensitive)
//  2. env `PROJMUX_DESKTOP_NOTIFY` (legacy on/off, mapped: off→none, on→notify)
//  3. saved config `~/.config/projmux/desktop-notify-mode`
//  4. tmux global option `@projmux_desktop_notify_mode`
//  5. tmux global option `@projmux_desktop_notify` (legacy `1`/`0`, same mapping)
//  6. default = `notify` on every platform (WSL + Windows Terminal included)
//
// The retired `raise` / `auto-raise` / `autoraise` literals are still accepted
// wherever a mode literal is read and resolve to `notify` *within that same
// source*, so an old value keeps pinning the resolution instead of falling
// through to a lower-precedence rung. They are never offered in Settings and
// are never written back.
//
// The Settings popup exposes a row whose info line surfaces the source
// (`env` / `env (legacy)` / `setting` / `setting (legacy)` / `default`)
// so users see immediately which rung of the resolution cascade pinned
// the value.
//
// Legacy migration is intentionally read-time only. Users who set
// `@projmux_desktop_notify=0` on the previous toggle keep getting their
// "no desktop dispatch" behavior (mapped to mode=none) without any eager
// rewrite of their tmux state. The first Settings toggle through the new
// row writes the new key and orphans the legacy one (legacy reads still
// hit on machines that never touched Settings).

const (
	// desktopNotifyTmuxOption is the legacy on/off toggle. Kept for the
	// migration read path — users with `0`/`1` set here see their old
	// preference honored until they touch the new selector. Settings no
	// longer writes here.
	desktopNotifyTmuxOption = "@projmux_desktop_notify"

	// desktopNotifyEnv is the legacy on/off env override. Same migration
	// shape as the tmux option above.
	desktopNotifyEnv = "PROJMUX_DESKTOP_NOTIFY"

	// desktopNotifyModeTmuxOption is the mode user-option that Settings
	// writes. Written values are only `off` / `notify`.
	desktopNotifyModeTmuxOption = "@projmux_desktop_notify_mode"

	// desktopNotifyModeEnv is the mode env override. Same values as the
	// tmux option, case insensitive.
	desktopNotifyModeEnv = "PROJMUX_DESKTOP_NOTIFY_MODE"
)

// desktopNotifyMode is the effective dispatch behavior.
type desktopNotifyMode string

const (
	// desktopNotifyModeNone disables the OS-level dispatch entirely. The
	// in-app notify queue / statusbar / attention badge still fire — only
	// the toast / notify-send are suppressed.
	desktopNotifyModeNone desktopNotifyMode = "none"

	// desktopNotifyModeNotify dispatches the OS notification (toast on
	// WSL, notify-send on Linux) and nothing else. It never focuses or
	// raises the host terminal window.
	desktopNotifyModeNotify desktopNotifyMode = "notify"
)

// desktopNotifySource describes where the effective mode came from. The
// Settings info row renders this so the user can tell which rung of the
// resolution cascade pinned the value.
type desktopNotifySource string

const (
	desktopNotifySourceEnv           desktopNotifySource = "env"
	desktopNotifySourceEnvLegacy     desktopNotifySource = "env (legacy)"
	desktopNotifySourceSetting       desktopNotifySource = "setting"
	desktopNotifySourceSettingLegacy desktopNotifySource = "setting (legacy)"
	desktopNotifySourceDefault       desktopNotifySource = "default"
)

// desktopNotifyResolver resolves the effective mode. The lookups are
// injected as function pointers so the resolution path stays testable
// without spawning real tmux processes or touching `/proc`.
//
// The resolver deliberately carries no host/platform signal: the
// unset-everything default is `notify` on every platform.
type desktopNotifyResolver struct {
	lookupEnv func(string) string
	// readConfigMode reads the durable Settings value. The bool is false
	// when no saved value exists so defaults can stay default-only.
	readConfigMode func() (desktopNotifyMode, bool)
	// readTmuxOption reads a global user-option (`tmux show-options -gqv …`).
	// Must return the trimmed string, or empty when tmux is unavailable or
	// the option is unset.
	readTmuxOption func(name string) string
}

// parseDesktopNotifyMode maps a raw value to a desktopNotifyMode. Returns
// (mode, true) on a known value; (_, false) when the input is unknown or
// empty so the caller can fall through to the next rung.
//
// The retired `raise` family resolves to `notify` here rather than
// returning false: an existing `raise` value must keep pinning its own
// source instead of leaking the resolution down to a lower rung.
func parseDesktopNotifyMode(raw string) (desktopNotifyMode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "none", "off", "disabled":
		return desktopNotifyModeNone, true
	case "notify", "toast", "raise", "auto-raise", "autoraise":
		return desktopNotifyModeNotify, true
	}
	return "", false
}

// parseLegacyDesktopNotify maps the legacy boolean values to a mode.
// Migration policy: legacy off → none, legacy on → notify.
func parseLegacyDesktopNotify(raw string) (desktopNotifyMode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "off", "0", "false", "no":
		return desktopNotifyModeNone, true
	case "on", "1", "true", "yes":
		return desktopNotifyModeNotify, true
	}
	return "", false
}

// resolveMode returns (mode, source). The cascade is:
//
//	new env → legacy env → saved config → new tmux option → legacy tmux option → default
//
// The default rung is `notify` unconditionally.
func (r desktopNotifyResolver) resolveMode() (desktopNotifyMode, desktopNotifySource) {
	if r.lookupEnv != nil {
		if mode, ok := parseDesktopNotifyMode(r.lookupEnv(desktopNotifyModeEnv)); ok {
			return mode, desktopNotifySourceEnv
		}
		if mode, ok := parseLegacyDesktopNotify(r.lookupEnv(desktopNotifyEnv)); ok {
			return mode, desktopNotifySourceEnvLegacy
		}
	}
	if r.readConfigMode != nil {
		if mode, ok := r.readConfigMode(); ok {
			return mode, desktopNotifySourceSetting
		}
	}
	if r.readTmuxOption != nil {
		if mode, ok := parseDesktopNotifyMode(r.readTmuxOption(desktopNotifyModeTmuxOption)); ok {
			return mode, desktopNotifySourceSetting
		}
		if mode, ok := parseLegacyDesktopNotify(r.readTmuxOption(desktopNotifyTmuxOption)); ok {
			return mode, desktopNotifySourceSettingLegacy
		}
	}
	return desktopNotifyModeNotify, desktopNotifySourceDefault
}

// desktopNotifyMode is the convenience accessor used by the
// `aiDesktopNotifier.Notify` gate. The gate uses the mode directly to
// decide whether to dispatch the OS notification; the source label is
// reserved for the Settings UI render path.
func (c *aiCommand) desktopNotifyMode() desktopNotifyMode {
	mode, _ := c.desktopNotifyModeResolution()
	return mode
}

// desktopNotifyModeResolution returns the effective mode plus the source
// label. Splitting it out keeps the gate cheap (no source string
// formatting) while letting future call sites (e.g. diagnostics) reuse
// the same resolution path.
func (c *aiCommand) desktopNotifyModeResolution() (desktopNotifyMode, desktopNotifySource) {
	resolver := desktopNotifyResolver{
		lookupEnv: c.lookupEnv,
		readConfigMode: func() (desktopNotifyMode, bool) {
			return loadSavedDesktopNotifyMode(c.homeDir, c.lookupEnv)
		},
		readTmuxOption: func(name string) string {
			// tmux show-options only makes sense inside a tmux client
			// (we follow the same TMUX gate used elsewhere in the
			// codebase — see tmuxProjdirOption in switch.go).
			if strings.TrimSpace(c.env("TMUX")) == "" {
				return ""
			}
			return c.readTrimmed("tmux", "show-options", "-gqv", name)
		},
	}
	return resolver.resolveMode()
}

// settingsDesktopNotifyResolver builds the same resolver from a
// `settingsCommand`. settingsCommand does not own an injected tmux reader, so
// production tmux reads go through the central mux runner while the pure
// resolver remains unit-testable.
//
// The Settings render path resolves through exactly the same cascade and
// the same platform-neutral `notify` default as the runtime gate.
func settingsDesktopNotifyResolver(homeDir func() (string, error), lookupEnv func(string) string) desktopNotifyResolver {
	return desktopNotifyResolver{
		lookupEnv: lookupEnv,
		readConfigMode: func() (desktopNotifyMode, bool) {
			return loadSavedDesktopNotifyMode(homeDir, lookupEnv)
		},
		readTmuxOption: func(name string) string {
			if lookupEnv == nil || strings.TrimSpace(lookupEnv("TMUX")) == "" {
				return ""
			}
			out, err := mux.ShowOption(context.Background(), mux.ShowOptionOptions{
				Global:    true,
				Quiet:     true,
				ValueOnly: true,
				Option:    name,
			})
			if err != nil {
				return ""
			}
			return out
		},
	}
}
