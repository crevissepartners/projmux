package app

import (
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
	if n.command.isWSL() {
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
	script := buildToastPowerShell(notification.Summary, notification.Body, notification.AppName, notification.Tag, notification.Group)
	return n.command.run(powerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16LEBase64(script))
}

func (c *aiCommand) notificationIcon(agentName string) string {
	switch strings.ToLower(strings.TrimSpace(agentName)) {
	case "codex":
		return c.ensureNotificationIcon("codex.svg", codexNotificationIconSVG)
	case "claude":
		return c.ensureNotificationIcon("claude.svg", claudeNotificationIconSVG)
	default:
		return "dialog-information"
	}
}

func (c *aiCommand) ensureNotificationIcon(name, content string) string {
	dir := c.notificationIconDir()
	if strings.TrimSpace(dir) == "" {
		return "dialog-information"
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "dialog-information"
	}
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return path
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "dialog-information"
	}
	return path
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

const codexNotificationIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
<rect width="64" height="64" rx="14" fill="#111827"/>
<circle cx="32" cy="32" r="20" fill="none" stroke="#10b981" stroke-width="5"/>
<path d="M38 22c-3-2-9-2-13 2-4 4-4 12 0 16 4 4 10 4 13 2" fill="none" stroke="#f9fafb" stroke-width="5" stroke-linecap="round"/>
</svg>
`

const claudeNotificationIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
<rect width="64" height="64" rx="14" fill="#3b2418"/>
<circle cx="32" cy="32" r="22" fill="#d97706"/>
<path d="M41 21c-4-3-13-3-18 3-5 5-5 12 0 17 5 6 14 6 18 3" fill="none" stroke="#fff7ed" stroke-width="5" stroke-linecap="round"/>
<path d="M42 24v20" stroke="#fff7ed" stroke-width="5" stroke-linecap="round"/>
</svg>
`
