package app

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
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
	script := buildToastPowerShell(
		notification.Summary,
		notification.Body,
		notification.AppName,
		notification.Tag,
		notification.Group,
		n.command.wslToastIconPath(notification.Icon),
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
	displayName := "Tmux Codex"
	if appID != "projmux.TmuxCodex" {
		displayName = appID
	}
	script := buildRegisterToastAppIDPowerShell(appID, displayName, n.command.wslToastIconPath(notification.Icon))
	return n.command.run(powerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeUTF16LEBase64(script))
}

func (c *aiCommand) notificationIcon(agentName string) string {
	switch strings.ToLower(strings.TrimSpace(agentName)) {
	case "codex":
		if c.isWSL() {
			return c.ensureNotificationPNG("codex.png", codexNotificationIconPNG)
		}
		return c.ensureNotificationIcon("codex.svg", codexNotificationIconSVG)
	case "claude":
		if c.isWSL() {
			return c.ensureNotificationPNG("claude.png", claudeNotificationIconPNG)
		}
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

func (c *aiCommand) ensureNotificationPNG(name string, content []byte) string {
	dir := c.notificationIconDir()
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

var codexNotificationIconPNG = mustBuildNotificationPNG(func(img *image.RGBA) {
	fillRect(img, 0, 0, 64, 64, color.RGBA{0x11, 0x18, 0x27, 0xff})
	drawRing(img, 32, 32, 20, 3, color.RGBA{0x10, 0xb9, 0x81, 0xff})
	drawArcRing(img, 31, 32, 13, 3, 45, 315, color.RGBA{0xf9, 0xfa, 0xfb, 0xff})
})

var claudeNotificationIconPNG = mustBuildNotificationPNG(func(img *image.RGBA) {
	fillRect(img, 0, 0, 64, 64, color.RGBA{0x3b, 0x24, 0x18, 0xff})
	drawFilledCircle(img, 32, 32, 22, color.RGBA{0xd9, 0x77, 0x06, 0xff})
	drawArcRing(img, 29, 32, 13, 3, 45, 315, color.RGBA{0xff, 0xf7, 0xed, 0xff})
	fillRect(img, 40, 22, 5, 20, color.RGBA{0xff, 0xf7, 0xed, 0xff})
})

func mustBuildNotificationPNG(drawFn func(*image.RGBA)) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	drawFn(img)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func fillRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			img.SetRGBA(xx, yy, c)
		}
	}
}

func drawFilledCircle(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	rr := r * r
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy <= rr {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func drawRing(img *image.RGBA, cx, cy, r, thickness int, c color.RGBA) {
	outer := r * r
	inner := (r - thickness) * (r - thickness)
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx := x - cx
			dy := y - cy
			dist := dx*dx + dy*dy
			if dist <= outer && dist >= inner {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func drawArcRing(img *image.RGBA, cx, cy, r, thickness, minAngle, maxAngle int, c color.RGBA) {
	outer := r * r
	inner := (r - thickness) * (r - thickness)
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx := x - cx
			dy := y - cy
			dist := dx*dx + dy*dy
			if dist > outer || dist < inner {
				continue
			}
			angle := pointAngleDegrees(dx, dy)
			if angle >= minAngle && angle <= maxAngle {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func pointAngleDegrees(dx, dy int) int {
	switch {
	case dx >= 0 && dy < 0:
		return 45
	case dx < 0 && dy < 0:
		return 135
	case dx < 0 && dy >= 0:
		return 225
	default:
		return 315
	}
}
