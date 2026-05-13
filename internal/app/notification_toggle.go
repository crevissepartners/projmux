package app

import (
	"os/exec"
	"strings"
)

// notification_toggle.go owns the OS-desktop-notification mode selector.
// The selector is intentionally orthogonal to the in-app notify queue,
// statusbar segment, and attention badge — those keep working regardless
// of which desktop dispatch mode is active (loud-environment / screen-share
// use case).
//
// The setting carries three values:
//
//	none    — no toast, no auto-raise
//	notify  — toast only (no click-to-focus)
//	raise   — toast + click-to-focus + auto-raise host terminal via osfocus chain
//
// Click handling is only enabled in raise mode, where the URI handler is
// registered on first dispatch. The mode gates whether to fire a toast,
// whether the toast is clickable, and whether to auto-raise on push.
//
// Resolution priority (highest first):
//  1. env `PROJMUX_DESKTOP_NOTIFY_MODE=none|notify|raise` (case-insensitive)
//  2. env `PROJMUX_DESKTOP_NOTIFY` (legacy on/off, mapped: off→none, on→notify)
//  3. tmux global option `@projmux_desktop_notify_mode`
//  4. tmux global option `@projmux_desktop_notify` (legacy `1`/`0`, same mapping)
//  5. default = `raise` if (isWSL && $WT_SESSION present), else `notify`
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

	// desktopNotifyModeTmuxOption is the 3-way mode user-option that
	// Settings writes. Values: `none` / `notify` / `raise`.
	desktopNotifyModeTmuxOption = "@projmux_desktop_notify_mode"

	// desktopNotifyModeEnv is the 3-way env override. Same values as the
	// tmux option, case insensitive.
	desktopNotifyModeEnv = "PROJMUX_DESKTOP_NOTIFY_MODE"
)

// desktopNotifyMode is the effective dispatch behavior.
type desktopNotifyMode string

const (
	// desktopNotifyModeNone disables the OS-level dispatch entirely. The
	// in-app notify queue / statusbar / attention badge still fire — only
	// the toast / notify-send / osfocus raise are suppressed.
	desktopNotifyModeNone desktopNotifyMode = "none"

	// desktopNotifyModeNotify dispatches the OS notification (toast on
	// WSL, notify-send on Linux). Click-to-focus and on-push auto-raise
	// are OFF.
	desktopNotifyModeNotify desktopNotifyMode = "notify"

	// desktopNotifyModeRaise dispatches the OS notification with
	// click-to-focus and auto-raises the host terminal via the osfocus
	// chain after a successful dispatch.
	desktopNotifyModeRaise desktopNotifyMode = "raise"
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

// desktopNotifyResolver resolves the effective 3-way mode. The lookups
// and the WSL/WT-host signals are injected as function pointers / fields
// so the resolution path stays testable without spawning real tmux
// processes or touching `/proc`.
type desktopNotifyResolver struct {
	lookupEnv func(string) string
	// readTmuxOption reads a global user-option (`tmux show-option -gqv …`).
	// Must return the trimmed string, or empty when tmux is unavailable or
	// the option is unset.
	readTmuxOption func(name string) string
	// isWSL tells the default branch whether we are inside WSL. Combined
	// with `wtPresent`, this decides whether the unset-everything default
	// is `raise` or `notify`.
	isWSL bool
	// wtPresent indicates `$WT_SESSION` is set — the only signal that
	// reliably survives the tmux env rewrite. The two-pronged default
	// matches the WSL+Windows-Terminal cell where osfocus raise actually
	// works today; everything else falls back to `notify` (toast only).
	wtPresent bool
}

// parseDesktopNotifyMode maps a raw 3-way value to a desktopNotifyMode.
// Returns (mode, true) on a known value; (_, false) when the input is
// unknown or empty so the caller can fall through to the next rung.
func parseDesktopNotifyMode(raw string) (desktopNotifyMode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "none", "off", "disabled":
		return desktopNotifyModeNone, true
	case "notify", "toast":
		return desktopNotifyModeNotify, true
	case "raise", "auto-raise", "autoraise":
		return desktopNotifyModeRaise, true
	}
	return "", false
}

// parseLegacyDesktopNotify maps the legacy boolean values to a 3-way
// mode. Migration policy: legacy off → none, legacy on → notify. Users
// who previously had auto-raise behavior didn't have it on the legacy
// toggle (it didn't exist), so notify is the closest faithful migration.
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
//	new env → legacy env → new tmux option → legacy tmux option → default
//
// The default rung uses `isWSL && wtPresent` to decide between `raise`
// and `notify`.
func (r desktopNotifyResolver) resolveMode() (desktopNotifyMode, desktopNotifySource) {
	if r.lookupEnv != nil {
		if mode, ok := parseDesktopNotifyMode(r.lookupEnv(desktopNotifyModeEnv)); ok {
			return mode, desktopNotifySourceEnv
		}
		if mode, ok := parseLegacyDesktopNotify(r.lookupEnv(desktopNotifyEnv)); ok {
			return mode, desktopNotifySourceEnvLegacy
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
	if r.isWSL && r.wtPresent {
		return desktopNotifyModeRaise, desktopNotifySourceDefault
	}
	return desktopNotifyModeNotify, desktopNotifySourceDefault
}

// desktopNotifyMode is the convenience accessor used by the
// `aiDesktopNotifier.Notify` gate. The gate uses the mode directly to
// decide whether to dispatch the toast and whether to follow it up with
// an auto-raise; the source label is reserved for the Settings UI render
// path.
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
		readTmuxOption: func(name string) string {
			// tmux show-option only makes sense inside a tmux client
			// (we follow the same TMUX gate used elsewhere in the
			// codebase — see tmuxProjdirOption in switch.go).
			if strings.TrimSpace(c.env("TMUX")) == "" {
				return ""
			}
			return c.readTrimmed("tmux", "show-option", "-gqv", name)
		},
		isWSL:     c.isWSL(),
		wtPresent: strings.TrimSpace(c.env("WT_SESSION")) != "",
	}
	return resolver.resolveMode()
}

// settingsDesktopNotifyResolver builds the same resolver from a
// `settingsCommand`. settingsCommand uses raw `runCommand` / direct
// `exec.Command` for its tmux reads, so we wire the lookup through
// `exec.Command` directly (mirroring `tmuxProjdirOption`).
//
// isWSL and wtPresent are derived from the env lookup so the Settings
// render path computes the same default as the runtime gate.
func settingsDesktopNotifyResolver(lookupEnv func(string) string) desktopNotifyResolver {
	wsl := false
	wt := false
	if lookupEnv != nil {
		wsl = strings.TrimSpace(lookupEnv("WSL_DISTRO_NAME")) != ""
		wt = strings.TrimSpace(lookupEnv("WT_SESSION")) != ""
	}
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
		isWSL:     wsl,
		wtPresent: wt,
	}
}
