package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

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
	promptUpdate := hasStatus && shouldPromptShellUpdate(status) && !skipped
	if err := writeShellWelcome(stdout, current, status, hasStatus, promptUpdate, skipped, c.welcomeWidth()); err != nil {
		return hasStatus, err
	}
	if !promptUpdate {
		return hasStatus, nil
	}

	action, err := c.readWelcomeUpdateAction(stdout)
	if err != nil {
		return true, nil
	}
	switch action {
	case "", "y", "yes":
		if err := c.update.Run([]string{"apply"}, stdout, stderr); err != nil {
			return true, fmt.Errorf("run shell welcome update: %w", err)
		}
	case "s", "skip":
		if err := c.writeUpdateSkip(status); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintf(stdout, "Skipped %s for daily update prompts.\n", strings.TrimSpace(status.LatestVersion))
	default:
		_, _ = fmt.Fprintf(stdout, "Run `%s` to upgrade.\n", shellWelcomeApplyCommand)
	}
	return true, nil
}

func (c *shellCommand) welcomeUpdateStatus() (updateStatus, bool) {
	return resolveWelcomeUpdateStatus(c.update)
}

func (c *shellCommand) readWelcomeUpdateAction(stdout io.Writer) (string, error) {
	if _, err := fmt.Fprint(stdout, "Update now? [Y/n, s=skip] "); err != nil {
		return "", err
	}
	input := c.welcomeInput
	if input == nil {
		return "n", nil
	}
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && strings.TrimSpace(line) != "") {
		return "n", err
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

func writeShellWelcome(w io.Writer, current string, status updateStatus, hasStatus, promptUpdate, skipped bool, width int) error {
	if w == nil {
		return nil
	}
	if width < 24 {
		width = 24
	}
	lines := []string{
		"Welcome to projmux shell " + current + ".",
		"Detach: Ctrl-b d keeps sessions running; re-enter with projmux shell.",
		"Exit: run exit in every window, or tmux -L projmux kill-server.",
		"Keys: Alt-1 projects, Alt-3 sessions, Alt-5 Settings.",
	}
	if hasStatus {
		lines = append(lines, "")
		lines = append(lines, shellWelcomeUpdateLines(status, promptUpdate, skipped)...)
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

func shellWelcomeUpdateLines(status updateStatus, promptUpdate, skipped bool) []string {
	latest := strings.TrimSpace(status.LatestVersion)
	current := strings.TrimSpace(status.CurrentVersion)
	switch status.UpdateState {
	case "current":
		return []string{"Update: you're on the latest release (" + latest + ")."}
	case "update_available":
		if skipped {
			return []string{"Update: " + latest + " is available; daily prompts are skipped for this version."}
		}
		if promptUpdate {
			return []string{
				"Update: " + latest + " is available (current " + current + ").",
				"Run `" + shellWelcomeApplyCommand + "` to upgrade manually.",
				"Press Enter/Y to update now, n to print the command, or s to skip this version.",
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
