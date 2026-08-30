package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

const (
	shellWelcomeApplyCommand = "projmux update apply"
	shellWelcomeExitFallback = "Exit: run projmux quit; it opens an interactive action picker by default."
	shellUpdateCheckTimeout  = 1500 * time.Millisecond

	welcomeReset = "\x1b[0m"
	welcomeBox   = "\x1b[38;5;45m"
	welcomeBadge = "\x1b[1;38;5;16;48;5;45m"
	welcomeDim   = "\x1b[38;5;245m"
)

func (c *shellCommand) promptWelcome(stdout, stderr io.Writer) (bool, error) {
	current, show := c.prepareWelcomeState()
	if !show {
		return false, nil
	}

	status, hasStatus := c.welcomeUpdateStatus()
	skipped := hasStatus && c.updatePromptSkipped(status)
	updateAvailable := hasStatus && shouldPromptShellUpdate(status) && !skipped
	upgradeEnabled := hasStatus && shellUpdateCanUpgrade(status)
	locale := appLocale(c.homeDir, c.env)
	if err := writeShellWelcome(stdout, current, status, hasStatus, updateAvailable, skipped, upgradeEnabled, c.welcomeWidth(), locale); err != nil {
		return hasStatus, err
	}

	action, err := c.readWelcomeAction(stdout, updateAvailable, upgradeEnabled, locale)
	if err != nil {
		return true, nil
	}
	switch action {
	case "s", "skip", "skip until next":
		if !updateAvailable {
			return true, nil
		}
		if err := c.writeUpdateSkip(status); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintf(stdout, "Skipped %s until the next release.\n", strings.TrimSpace(status.LatestVersion))
	case "u", "upgrade", "update":
		if !updateAvailable {
			return true, nil
		}
		if !upgradeEnabled {
			_, _ = fmt.Fprintf(stdout, "Upgrade is not available for installer source %q. %s\n", status.Installer.Source, status.Installer.Note)
			_, _ = fmt.Fprintln(stdout, "Continue shell entry, then run `projmux update status` for details.")
			return true, nil
		}
		if err := c.update.Run([]string{"apply"}, stdout, stderr); err != nil {
			// Surface the failure but never block shell entry on it: the user
			// asked to enter the shell, and a failed upgrade should be visible,
			// not fatal.
			_, _ = fmt.Fprintf(stdout, "Update failed: %v\n", err)
			_, _ = fmt.Fprintln(stdout, "Continuing shell entry; retry later with `projmux update apply`.")
			return true, nil
		}
		_, _ = fmt.Fprintf(stdout, "Updated projmux from %s to %s. Restart projmux shell to run the new version.\n",
			strings.TrimSpace(status.CurrentVersion), strings.TrimSpace(status.LatestVersion))
	default:
	}
	return true, nil
}

func (c *shellCommand) welcomeUpdateStatus() (updateStatus, bool) {
	c.refreshWelcomeUpdateCache()
	return resolveWelcomeUpdateStatus(c.update)
}

func (c *shellCommand) refreshWelcomeUpdateCache() {
	if c.update == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.welcomeUpdateCheckTimeout())
	defer cancel()
	_ = c.update.refreshCacheIfNeeded(ctx)
}

func (c *shellCommand) welcomeUpdateCheckTimeout() time.Duration {
	raw := strings.TrimSpace(c.env("PROJMUX_SHELL_UPDATE_CHECK_TIMEOUT_MS"))
	if raw == "" {
		return shellUpdateCheckTimeout
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return shellUpdateCheckTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

func (c *shellCommand) readWelcomeAction(stdout io.Writer, updateAvailable, upgradeEnabled bool, locale i18n.Locale) (string, error) {
	prompt := localizeText(locale, i18n.KeyWelcomeShellPromptDefault, "Continue? [Enter=Continue] ")
	if updateAvailable {
		prompt = localizeText(locale, i18n.KeyWelcomeShellPromptUpdate, "Continue? [Enter=Continue, u=Upgrade, s=Skip until next] ")
		if !upgradeEnabled {
			prompt = localizeText(locale, i18n.KeyWelcomeShellPromptUpdateSkip, "Continue? [Enter=Continue, u=Upgrade guidance, s=Skip until next] ")
		}
	}
	if _, err := fmt.Fprint(stdout, prompt); err != nil {
		return "", err
	}
	input := c.welcomeInput
	if input == nil {
		return "", nil
	}
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && strings.TrimSpace(line) != "") {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(line)), nil
}

func (c *shellCommand) welcomeWidth() int {
	return welcomeWidthFromEnv(c.env)
}

func welcomeWidthFromEnv(lookupEnv func(string) string) int {
	width := 80
	colsRaw := ""
	if lookupEnv != nil {
		colsRaw = lookupEnv("COLUMNS")
	}
	cols, err := strconv.Atoi(strings.TrimSpace(colsRaw))
	if err == nil && cols > 0 && cols < width {
		width = cols
	}
	if width < 24 {
		width = 24
	}
	return width
}

func resolveWelcomeUpdateStatus(update *updateCommand) (updateStatus, bool) {
	// Concrete-typed wrapper: keep the nil *updateCommand check here so the
	// shell/welcome callers never wrap a typed nil pointer in the interface.
	if update == nil {
		return updateStatus{}, false
	}
	return resolveWelcomeUpdateStatusFrom(update)
}

// resolveWelcomeUpdateStatusFrom is the interface-typed variant used by the
// Settings About > Welcome viewer, which holds its update dependency behind
// the updateRunner seam.
func resolveWelcomeUpdateStatusFrom(update updateRunner) (updateStatus, bool) {
	if update == nil {
		return updateStatus{}, false
	}
	status, err := update.status()
	if err != nil {
		return updateStatus{}, false
	}
	if status.CacheState != "fresh" || strings.TrimSpace(status.LatestVersion) == "" {
		return updateStatus{}, false
	}
	switch status.UpdateState {
	case "current", "update_available":
		return status, true
	default:
		return updateStatus{}, false
	}
}

func writeShellWelcome(w io.Writer, current string, status updateStatus, hasStatus, updateAvailable, skipped, upgradeEnabled bool, width int, locale i18n.Locale) error {
	if w == nil {
		return nil
	}
	if width < 24 {
		width = 24
	}
	lines := []string{
		localizeText(locale, i18n.KeyWelcomeShellTitle, "Welcome to projmux") + " shell " + current + ".",
		localizeText(locale, i18n.KeyWelcomeShellDetach, "Detach: Ctrl-b d keeps sessions running; re-enter with projmux shell."),
		localizeText(locale, i18n.KeyWelcomeShellExit, shellWelcomeExitFallback),
		localizeText(locale, i18n.KeyWelcomeShellSurfaces, "Bootstrap: generated tmux config and Settings stay available after entry."),
	}
	if hasStatus {
		lines = append(lines, "")
		lines = append(lines, shellWelcomeUpdateLines(status, updateAvailable, skipped)...)
	}
	lines = append(lines, "")
	lines = append(lines, localizeText(locale, i18n.KeyWelcomeShellContinue, "Enter continues into the shell."))
	if updateAvailable {
		if upgradeEnabled {
			lines = append(lines, localizeText(locale, i18n.KeyWelcomeShellUpdateNow, "Press u to upgrade with projmux update apply, or s to skip until the next release."))
		} else {
			lines = append(lines, localizeText(locale, i18n.KeyWelcomeShellUpdateGuidance, "Press u for upgrade guidance, or s to skip until the next release."))
		}
	} else if hasStatus && skipped {
		lines = append(lines, localizeText(locale, i18n.KeyWelcomeShellUpdateSkipped, "This release is skipped until the next latest tag appears."))
	}

	inner := width - 4
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, welcomeBox+"+"+strings.Repeat("-", width-2)+"+"+welcomeReset); err != nil {
		return err
	}
	if err := writeWelcomeBoxLine(w, welcomeBadge+" projmux "+welcomeReset+" shell bootstrap", inner); err != nil {
		return err
	}
	if err := writeWelcomeBoxLine(w, welcomeDim+strings.Repeat("-", inner)+welcomeReset, inner); err != nil {
		return err
	}
	for _, line := range lines {
		if line == "" {
			if err := writeWelcomeBoxLine(w, "", inner); err != nil {
				return err
			}
			continue
		}
		for _, wrapped := range wrapWelcomeLine(line, inner) {
			if err := writeWelcomeBoxLine(w, wrapped, inner); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(w, welcomeBox+"+"+strings.Repeat("-", width-2)+"+"+welcomeReset); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func shellWelcomeUpdateLines(status updateStatus, updateAvailable, skipped bool) []string {
	latest := strings.TrimSpace(status.LatestVersion)
	current := strings.TrimSpace(status.CurrentVersion)
	switch status.UpdateState {
	case "current":
		return []string{"Update: you're on the latest release (" + latest + ")."}
	case "update_available":
		if skipped {
			return []string{"Update: " + latest + " is available; skipped until the next release."}
		}
		if updateAvailable {
			if !shellUpdateCanUpgrade(status) {
				return []string{
					"Update: " + latest + " is available (current " + current + ").",
					"Upgrade guidance: " + status.Installer.Note,
				}
			}
			return []string{
				"Update: " + latest + " is available (current " + current + ").",
				"Upgrade runs `" + shellWelcomeApplyCommand + "`.",
			}
		}
		return []string{
			"Update: " + latest + " is available.",
			"Run `projmux update status` for installer-specific guidance.",
		}
	default:
		return nil
	}
}

func writeWelcomeBoxLine(w io.Writer, line string, inner int) error {
	line = projmuxpicker.TruncateANSI(line, inner)
	line = projmuxpicker.PadRight(line, inner)
	_, err := fmt.Fprintln(w, welcomeBox+"| "+welcomeReset+line+welcomeBox+" |"+welcomeReset)
	return err
}

func wrapWelcomeLine(line string, width int) []string {
	if width <= 0 || projmuxpicker.VisibleLen(line) <= width {
		return []string{line}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	current := words[0]
	for _, word := range words[1:] {
		next := current + " " + word
		if projmuxpicker.VisibleLen(next) <= width {
			current = next
			continue
		}
		lines = append(lines, current)
		current = word
	}
	lines = append(lines, current)
	return lines
}
