package app

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

const (
	aiNotifyExpireMSEnv     = "PROJMUX_NOTIFY_EXPIRE_MS"
	defaultAINotifyExpireMS = 5000
)

type aiNotification struct {
	Summary            string
	Body               string
	Urgency            string
	ExpireMS           int
	AppName            string
	Icon               string
	Tag                string
	Group              string
	diagnosticProvider diagnostics.Provider
	diagnosticCategory diagnostics.Category
}

type aiNotifier interface {
	Notify(aiNotification) error
}

func (c *aiCommand) notificationNotifier() aiNotifier {
	deliveryDiagnostics := notifyDeliveryDiagnostics{
		recorder: c.notifyDiagnostics, ownsTopLevel: c.notifyDeliveryOwnsTopLevel,
	}
	if hook := strings.TrimSpace(c.env("PROJMUX_NOTIFY_HOOK")); hook != "" {
		return aiHookNotifier{command: c, hook: hook, deliveryDiagnostics: deliveryDiagnostics}
	}
	return aiDesktopNotifier{command: c, deliveryDiagnostics: deliveryDiagnostics}
}

type aiHookNotifier struct {
	command             *aiCommand
	hook                string
	deliveryDiagnostics notifyDeliveryDiagnostics
}

func (n aiHookNotifier) Notify(notification aiNotification) error {
	started := time.Now()
	deliveryDiagnostics := n.deliveryDiagnostics
	deliveryDiagnostics.provider, deliveryDiagnostics.category = notification.diagnosticProvider, notification.diagnosticCategory
	err := n.command.run(n.hook,
		notification.Summary,
		notification.Body,
		notification.Urgency,
		notification.AppName,
		notification.Tag,
		notification.Group,
		notification.Icon,
	)
	if err != nil {
		deliveryDiagnostics.record(diagnostics.DispositionFailed, diagnostics.RouteHook, diagnostics.CodeNotifyDeliveryFailed, started)
		return err
	}
	deliveryDiagnostics.record(diagnostics.DispositionDelivered, diagnostics.RouteHook, "", started)
	return nil
}

type aiDesktopNotifier struct {
	command             *aiCommand
	deliveryDiagnostics notifyDeliveryDiagnostics
}

func (n aiDesktopNotifier) Notify(notification aiNotification) error {
	started := time.Now()
	deliveryDiagnostics := n.deliveryDiagnostics
	deliveryDiagnostics.provider, deliveryDiagnostics.category = notification.diagnosticProvider, notification.diagnosticCategory
	// Two-state mode gate. The in-app notify queue, the statusbar segment,
	// and the attention badge are intentionally untouched — this only
	// suppresses the OS-level dispatch so users can silence the
	// popup/Toast/notify-send fan-out without losing the in-app
	// surfaces.
	//
	// `mode=none` skips dispatch entirely. `mode=notify` dispatches the
	// toast / notify-send and nothing else: the Toast carries no click URI,
	// no URI protocol handler is registered, and the host terminal window
	// is never focused or raised.
	mode := n.command.desktopNotifyMode()
	if mode == desktopNotifyModeNone {
		deliveryDiagnostics.record(diagnostics.DispositionSuppressed, diagnostics.RouteDisabled, "", started)
		return nil
	}
	expireMS := notification.ExpireMS
	if n.command.isWSL() {
		n.command.ensureWSLLegacyAppIDCleaned(notification)
		_ = n.ensureWSLToastAppID(notification)
		if err := n.dispatchWSLToast(notification, expireMS); err == nil {
			deliveryDiagnostics.record(diagnostics.DispositionDelivered, diagnostics.RouteWSLToast, "", started)
			return nil
		}
		if n.command.readTrimmed("command", "-v", "wsl-notify-send.exe") != "" {
			message := notification.Summary
			if notification.Body != "" {
				message += "\n" + notification.Body
			}
			if err := n.command.run("wsl-notify-send.exe", "--category", notification.AppName, message); err == nil {
				deliveryDiagnostics.record(diagnostics.DispositionDelivered, diagnostics.RouteWSLNotifySend, "", started)
				return nil
			}
		}
	}
	icon := strings.TrimSpace(notification.Icon)
	if icon == "" {
		icon = "dialog-information"
	}
	if n.command.readTrimmed("command", "-v", "notify-send") == "" {
		deliveryDiagnostics.record(diagnostics.DispositionFailed, diagnostics.RouteNotifySend, diagnostics.CodeNotifyDeliveryUnavailable, started)
		return errors.New("notify-send is unavailable")
	}
	args := []string{
		"--app-name=" + notification.AppName,
		"--icon=" + icon,
		"--urgency=" + notification.Urgency,
	}
	if expireMS > 0 {
		args = append(args, "--expire-time="+strconv.Itoa(expireMS))
	}
	args = append(args,
		notification.Summary,
		notification.Body,
	)
	if err := n.command.run("notify-send", args...); err != nil {
		deliveryDiagnostics.record(diagnostics.DispositionFailed, diagnostics.RouteNotifySend, diagnostics.CodeNotifyDeliveryFailed, started)
		return err
	}
	deliveryDiagnostics.record(diagnostics.DispositionDelivered, diagnostics.RouteNotifySend, "", started)
	return nil
}

// dispatchWSLToast shows a passive Toast. The Toast intentionally carries no
// `launch` payload and no `activationType="protocol"` pair: desktop delivery
// must never be able to steal the host terminal window focus, so there is no
// click target and no URI protocol handler to register.
func (n aiDesktopNotifier) dispatchWSLToast(notification aiNotification, expireMS int) error {
	powerShell := n.command.resolvePowerShell()
	if powerShell == "" {
		return errors.New("powershell.exe is unavailable")
	}
	script := buildToastPowerShell(
		notification.Summary,
		notification.Body,
		notification.AppName,
		notification.Tag,
		notification.Group,
		n.command.wslToastIconPath(notification.Icon),
		expireMS,
	)
	return n.command.run(powerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16LEBase64(script))
}

func (c *aiCommand) notificationExpireMS() int {
	if ms := parsePositiveInt(c.env(aiNotifyExpireMSEnv)); ms > 0 {
		return ms
	}
	return defaultAINotifyExpireMS
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
	if strings.TrimSpace(c.readTrimmed("tmux", "show-options", "-gqv", legacyAppIDCleanedTmuxOption)) == "1" {
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
	// best-effort-write: a one-shot global marker recording that the legacy
	// toast cleanup ran. Leaving it unset is the safe direction -- the next
	// Notify simply retries the cleanup -- so a dropped failure here costs a
	// repeated no-op, not a false report.
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
