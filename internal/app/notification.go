package app

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type aiNotification struct {
	Summary string
	Body    string
	Urgency string
	AppName string
	Icon    string
	Tag     string
	Group   string
}

type aiNotifier interface {
	Notify(aiNotification) error
}

func (c *aiCommand) notificationNotifier() aiNotifier {
	if hook := strings.TrimSpace(c.env("PROJMUX_NOTIFY_HOOK")); hook != "" {
		return aiHookNotifier{command: c, hook: hook}
	}
	return aiDesktopNotifier{command: c}
}

type aiHookNotifier struct {
	command *aiCommand
	hook    string
}

func (n aiHookNotifier) Notify(notification aiNotification) error {
	return n.command.run(n.hook,
		notification.Summary,
		notification.Body,
		notification.Urgency,
		notification.AppName,
		notification.Tag,
		notification.Group,
		notification.Icon,
	)
}

type aiDesktopNotifier struct {
	command *aiCommand
}

func (n aiDesktopNotifier) Notify(notification aiNotification) error {
	// Phase 1 desktop-notification gate. The in-app notify queue, the
	// statusbar segment, and the attention badge are intentionally
	// untouched — this only suppresses the OS-level dispatch so users
	// can silence the popup/Toast/notify-send fan-out without losing
	// the in-app surfaces.
	if !n.command.desktopNotifyEnabled() {
		return nil
	}
	if n.command.isWSL() {
		n.command.ensureWSLLegacyAppIDCleaned(notification)
		n.command.ensureWSLURIProtocol()
		_ = n.ensureWSLToastAppID(notification)
		if err := n.dispatchWSLToast(notification); err == nil {
			return nil
		}
		if n.command.readTrimmed("command", "-v", "wsl-notify-send.exe") != "" {
			message := notification.Summary
			if notification.Body != "" {
				message += "\n" + notification.Body
			}
			if err := n.command.run("wsl-notify-send.exe", "--category", notification.AppName, message); err == nil {
				return nil
			}
		}
	}
	icon := strings.TrimSpace(notification.Icon)
	if icon == "" {
		icon = "dialog-information"
	}
	if n.command.readTrimmed("command", "-v", "notify-send") == "" {
		return errors.New("notify-send is unavailable")
	}
	return n.command.run("notify-send",
		"--app-name="+notification.AppName,
		"--icon="+icon,
		"--urgency="+notification.Urgency,
		notification.Summary,
		notification.Body,
	)
}

func (n aiDesktopNotifier) dispatchWSLToast(notification aiNotification) error {
	powerShell := n.command.resolvePowerShell()
	if powerShell == "" {
		return errors.New("powershell.exe is unavailable")
	}
	// `notification.Tag` carries the originating pane id (e.g. `%8`); the
	// Toast click handler needs that plus the live tmux socket path so it
	// can route back to the right server. Reading `#{socket_path}` here
	// (rather than threading it through the producer) keeps the existing
	// notify call sites unchanged and naturally tracks the user's
	// current tmux server, which is what we want for click round-trip.
	socket := ""
	if strings.TrimSpace(notification.Tag) != "" {
		socket = n.command.readTrimmed("tmux", "display-message", "-p", "#{socket_path}")
	}
	launchURI := buildFocusURI(notification.Tag, socket)
	script := buildToastPowerShell(
		notification.Summary,
		notification.Body,
		notification.AppName,
		notification.Tag,
		notification.Group,
		n.command.wslToastIconPath(notification.Icon),
		launchURI,
	)
	return n.command.run(powerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16LEBase64(script))
}

func (n aiDesktopNotifier) ensureWSLToastAppID(notification aiNotification) error {
	powerShell := n.command.resolvePowerShell()
	if powerShell == "" {
		return errors.New("powershell.exe is unavailable")
	}
	appID := strings.TrimSpace(notification.AppName)
	if appID == "" {
		return errors.New("toast app id is empty")
	}
	// During the AppName migration we accept both the new reverse-domain
	// id and the legacy `projmux.TmuxCodex` so users mid-migration keep
	// getting toasts. New installs pick up `desktopDisplayName`; the
	// legacy id still resolves to the old "Tmux Codex" label so we don't
	// surprise users who somehow re-register the old AppID.
	var displayName string
	switch appID {
	case desktopAppID:
		displayName = desktopDisplayName
	case legacyDesktopAppID:
		displayName = "Tmux Codex"
	default:
		displayName = appID
	}
	script := buildRegisterToastAppIDPowerShell(appID, displayName, n.command.wslToastIconPath(notification.Icon))
	return n.command.run(powerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16LEBase64(script))
}

// ensureWSLURIProtocol registers the `projmux://` URL scheme in the user's
// HKCU registry so a Toast click is routed back into `projmux focus --uri`
// running inside the same WSL distro that produced the notification. It
// runs at most once per tmux server, gated by
// `@projmux_uri_protocol_registered`.
//
// The handler command binds to the *current* WSL_DISTRO_NAME at
// registration time — users who switch distros mid-server keep the
// originally-registered handler. This is the documented multi-distro
// limitation captured in docs/configuration.md.
//
// The marker is only set on a successful PowerShell launch. Failure cases
// (missing distro env, no PowerShell available, run error) leave the
// marker unset so a later Notify call retries. The registration script
// itself swallows individual errors so even a partial registry write
// doesn't break the notification dispatch.
func (c *aiCommand) ensureWSLURIProtocol() {
	if !c.isWSL() {
		return
	}
	if strings.TrimSpace(c.readTrimmed("tmux", "show-option", "-gqv", uriProtocolRegisteredTmuxOption)) == "1" {
		return
	}
	distro := strings.TrimSpace(c.env("WSL_DISTRO_NAME"))
	if distro == "" {
		// We don't know which distro the click should target — skipping
		// without setting the marker lets a later call (after WSLENV is
		// populated) retry the registration.
		return
	}
	// Resolve the absolute WSL path to this projmux binary so the registry
	// command can use `wsl.exe --exec <abs-path>` (no shell, no PATH
	// dependency). If we can't resolve the path, skip without setting the
	// marker so a later Notify retries with a healthier environment.
	binaryPath, err := c.binaryPath()
	if err != nil {
		return
	}
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return
	}
	powerShell := c.resolvePowerShell()
	if powerShell == "" {
		return
	}
	script := buildRegisterURIProtocolPowerShell(desktopURIScheme, distro, binaryPath)
	if err := c.run(powerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16LEBase64(script)); err != nil {
		return
	}
	_ = c.run("tmux", "set-option", "-g", uriProtocolRegisteredTmuxOption, "1")
}

// ensureWSLLegacyAppIDCleaned removes the Start Menu shortcut and the
// AppUserModelID registry key left over from the `projmux.TmuxCodex` era.
// It runs at most once per tmux server, gated by the
// `@projmux_legacy_appid_cleaned` user-option marker. The marker is set
// even on failure so we don't keep retrying on a broken environment —
// the registration helper still emits the new AppID artifacts each toast
// so the user is never blocked.
//
// _ = notification: the cleanup script is self-contained and intentionally
// does not depend on the inbound notification payload. Keeping the
// parameter symmetric with `ensureWSLToastAppID` lets call sites pair them
// without ceremony.
func (c *aiCommand) ensureWSLLegacyAppIDCleaned(_ aiNotification) {
	if !c.isWSL() {
		return
	}
	if strings.TrimSpace(c.readTrimmed("tmux", "show-option", "-gqv", legacyAppIDCleanedTmuxOption)) == "1" {
		return
	}
	powerShell := c.resolvePowerShell()
	if powerShell == "" {
		return
	}
	script := buildLegacyToastCleanupPowerShell()
	// Best effort — the helper script itself swallows individual errors
	// so a successful return here just means PowerShell launched. Any
	// failure leaves the marker unset so a future Notify retries.
	if err := c.run(powerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16LEBase64(script)); err != nil {
		return
	}
	_ = c.run("tmux", "set-option", "-g", legacyAppIDCleanedTmuxOption, "1")
}

func (c *aiCommand) notificationIcon(string) string {
	if c.isWSL() {
		return c.ensureWSLNotificationPNG("projmux.png", projmuxNotificationIconPNG)
	}
	return c.ensureNotificationPNG("projmux.png", projmuxNotificationIconPNG)
}

func (c *aiCommand) ensureNotificationPNG(name string, content []byte) string {
	dir := c.notificationIconDir()
	return writeNotificationPNG(dir, name, content)
}

func (c *aiCommand) ensureWSLNotificationPNG(name string, content []byte) string {
	if dir := c.wslWindowsNotificationIconDir(); strings.TrimSpace(dir) != "" {
		if path := writeNotificationPNG(dir, name, content); path != "dialog-information" {
			return path
		}
	}
	return c.ensureNotificationPNG(name, content)
}

func writeNotificationPNG(dir, name string, content []byte) string {
	if strings.TrimSpace(dir) == "" {
		return "dialog-information"
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "dialog-information"
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return path
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "dialog-information"
	}
	return path
}

func (c *aiCommand) wslWindowsNotificationIconDir() string {
	if override := strings.TrimSpace(c.env("PROJMUX_WSL_TOAST_ICON_DIR")); override != "" {
		return filepath.Join(override, "projmux", "icons")
	}
	powerShell := c.resolvePowerShell()
	if powerShell == "" {
		return ""
	}
	localAppDataWin := c.readTrimmed(
		powerShell,
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		"[Environment]::GetFolderPath('LocalApplicationData')",
	)
	if localAppDataWin == "" {
		return ""
	}
	localAppDataWSL := c.readTrimmed("wslpath", "-u", localAppDataWin)
	if localAppDataWSL == "" {
		return ""
	}
	return filepath.Join(localAppDataWSL, "projmux", "icons")
}

func (c *aiCommand) wslToastIconPath(iconPath string) string {
	iconPath = strings.TrimSpace(iconPath)
	if iconPath == "" || iconPath == "dialog-information" {
		return ""
	}
	if winPath := c.readTrimmed("wslpath", "-w", iconPath); winPath != "" {
		return winPath
	}
	distro := strings.TrimSpace(c.env("WSL_DISTRO_NAME"))
	if distro == "" || !strings.HasPrefix(iconPath, "/") {
		return ""
	}
	return `\\wsl.localhost\` + distro + strings.ReplaceAll(iconPath, "/", `\`)
}

func (c *aiCommand) notificationIconDir() string {
	if dataHome := strings.TrimSpace(c.env("XDG_DATA_HOME")); dataHome != "" {
		return filepath.Join(dataHome, "projmux", "icons")
	}
	home := ""
	if c.homeDir != nil {
		if dir, err := c.homeDir(); err == nil {
			home = strings.TrimSpace(dir)
		}
	}
	if home == "" {
		home = strings.TrimSpace(c.env("HOME"))
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "projmux", "icons")
}

//go:embed assets/projmux-icon.png
var projmuxNotificationIconPNG []byte
