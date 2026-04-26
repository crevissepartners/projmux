package app

import (
	"errors"
	"strings"
)

type aiNotification struct {
	Summary string
	Body    string
	Urgency string
	AppName string
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
	if n.command.readTrimmed("command", "-v", "notify-send") == "" {
		return errors.New("notify-send is unavailable")
	}
	return n.command.run("notify-send",
		"--app-name="+notification.AppName,
		"--icon=dialog-information",
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
