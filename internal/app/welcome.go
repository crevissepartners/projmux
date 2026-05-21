package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

const (
	shellWelcomeApplyCommand = "projmux update apply"

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
	updateAvailable := hasStatus && shouldPromptShellUpdate(status)
	locale := appLocale(c.env)
	if err := writeShellWelcome(stdout, current, status, hasStatus, updateAvailable, skipped, c.welcomeWidth(), locale); err != nil {
		return hasStatus, err
	}

	action, err := c.readWelcomeAction(stdout, updateAvailable, skipped, locale)
	if err != nil {
		return true, nil
	}
	switch action {
	case "s", "skip":
		if err := c.skipWelcomeVersion(current); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintf(stdout, "Skipped welcome for projmux %s.\n", current)
	case "u", "update":
		if !updateAvailable {
			return true, nil
		}
		if err := c.update.Run([]string{"apply"}, stdout, stderr); err != nil {
			return true, fmt.Errorf("run shell welcome update: %w", err)
		}
	case "d", "daily-skip", "skip-update":
		if !updateAvailable || skipped {
			return true, nil
		}
		if err := c.writeUpdateSkip(status); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintf(stdout, "Skipped %s for daily update prompts.\n", strings.TrimSpace(status.LatestVersion))
	case "n", "no":
		if updateAvailable {
			_, _ = fmt.Fprintf(stdout, "Run `%s` to upgrade.\n", shellWelcomeApplyCommand)
		}
	default:
	}
	return true, nil
}

func (c *shellCommand) welcomeUpdateStatus() (updateStatus, bool) {
	return resolveWelcomeUpdateStatus(c.update)
}

func (c *shellCommand) readWelcomeAction(stdout io.Writer, updateAvailable, updateSkipped bool, locale i18n.Locale) (string, error) {
	prompt := localizeText(locale, i18n.KeyWelcomeShellPromptDefault, "Continue? [Enter, s=skip welcome] ")
	if updateAvailable {
		prompt = localizeText(locale, i18n.KeyWelcomeShellPromptUpdate, "Continue? [Enter, u=update, s=skip welcome, d=skip update prompts] ")
		if updateSkipped {
			prompt = localizeText(locale, i18n.KeyWelcomeShellPromptUpdateSkip, "Continue? [Enter, u=update, s=skip welcome] ")
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

func writeShellWelcome(w io.Writer, current string, status updateStatus, hasStatus, updateAvailable, skipped bool, width int, locale i18n.Locale) error {
	if w == nil {
		return nil
	}
	if width < 24 {
		width = 24
	}
	lines := []string{
		localizeText(locale, i18n.KeyWelcomeShellTitle, "Welcome to projmux") + " shell " + current + ".",
		localizeText(locale, i18n.KeyWelcomeShellDetach, "Detach: Ctrl-b d keeps sessions running; re-enter with projmux shell."),
		localizeText(locale, i18n.KeyWelcomeShellExit, "Exit: run exit in every window, or tmux -L projmux kill-server."),
		localizeText(locale, i18n.KeyWelcomeShellSurfaces, "Launch surfaces are available from the generated tmux config and Settings."),
	}
	if hasStatus {
		lines = append(lines, "")
		lines = append(lines, shellWelcomeUpdateLines(status, updateAvailable, skipped)...)
	}
	lines = append(lines, "")
	lines = append(lines, localizeText(locale, i18n.KeyWelcomeShellContinue, "Enter continues for this run. Press s to skip this welcome for the current version."))
	if updateAvailable && skipped {
		lines = append(lines, localizeText(locale, i18n.KeyWelcomeShellUpdateSkipped, "Press u to update now. Daily update prompts are already skipped for this release."))
	} else if updateAvailable {
		lines = append(lines, localizeText(locale, i18n.KeyWelcomeShellUpdateNow, "Press u to update now. Press d to skip daily update prompts for this release."))
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
			return []string{"Update: " + latest + " is available; daily prompts are skipped for this version."}
		}
		if updateAvailable {
			return []string{
				"Update: " + latest + " is available (current " + current + ").",
				"Run `" + shellWelcomeApplyCommand + "` to upgrade manually.",
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
