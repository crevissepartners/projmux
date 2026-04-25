package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
)

const (
	attentionStateOption = "@dotfiles_attention_state"
	attentionAckOption   = "@dotfiles_attention_ack"
	attentionStateBusy   = "busy"
	attentionStateReply  = "reply"
)

type attentionCommand struct {
	runner tmuxRunner
}

func newAttentionCommand() *attentionCommand {
	return &attentionCommand{runner: inttmux.ExecRunner{}}
}

func (c *attentionCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printAttentionUsage(stderr)
		return errors.New("attention requires a subcommand")
	}

	switch args[0] {
	case "toggle":
		return c.runToggle(args[1:], stderr)
	case "clear":
		return c.runClear(args[1:], stderr)
	case "window":
		return c.runWindow(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printAttentionUsage(stdout)
		return nil
	default:
		printAttentionUsage(stderr)
		return fmt.Errorf("unknown attention subcommand: %s", args[0])
	}
}

func (c *attentionCommand) runToggle(args []string, stderr io.Writer) error {
	paneID, err := parseOptionalAttentionTarget(args, "attention toggle", stderr)
	if err != nil || paneID == "" {
		return err
	}

	title := c.paneTitle(paneID)
	if strings.HasPrefix(title, "✳") {
		c.unsetPaneOption(paneID, attentionStateOption)
		c.selectPaneTitle(paneID, trimAttentionPrefix(title))
		c.displayPaneMessage(paneID, "attention: cleared")
		return nil
	}

	c.setPaneOption(paneID, attentionStateOption, attentionStateReply)
	c.selectPaneTitle(paneID, "✳ "+title)
	c.displayPaneMessage(paneID, "attention: needs reply")
	return nil
}

func (c *attentionCommand) runClear(args []string, stderr io.Writer) error {
	paneID, err := parseOptionalAttentionTarget(args, "attention clear", stderr)
	if err != nil || paneID == "" {
		return err
	}

	c.unsetPaneOption(paneID, attentionStateOption)
	c.setPaneOption(paneID, attentionAckOption, "1")

	title := c.paneTitle(paneID)
	clean := trimAttentionPrefix(title)
	if clean == title {
		return nil
	}
	c.selectPaneTitle(paneID, clean)
	return nil
}

func (c *attentionCommand) runWindow(args []string, stdout, stderr io.Writer) error {
	windowID, err := parseOptionalAttentionTarget(args, "attention window", stderr)
	if err != nil {
		return err
	}
	if windowID == "" {
		_, err := fmt.Fprint(stdout, " ")
		return err
	}

	rows := c.windowAttentionRows(windowID)
	seenReply := false
	for _, row := range rows {
		if row.State == attentionStateBusy || hasBraillePrefix(row.Title) {
			_, err := fmt.Fprint(stdout, "#[fg=colour220]●")
			return err
		}
		if row.State == attentionStateReply || hasAttentionPrefix(row.Title) {
			seenReply = true
		}
	}

	if seenReply {
		_, err := fmt.Fprint(stdout, "#[fg=colour82]●")
		return err
	}
	_, err = fmt.Fprint(stdout, " ")
	return err
}

func parseOptionalAttentionTarget(args []string, command string, stderr io.Writer) (string, error) {
	if len(args) > 1 {
		printAttentionUsage(stderr)
		return "", fmt.Errorf("%s accepts at most 1 target argument", command)
	}
	if len(args) == 0 {
		return "", nil
	}
	return strings.TrimSpace(args[0]), nil
}

type attentionWindowRow struct {
	Title string
	State string
}

func (c *attentionCommand) paneTitle(paneID string) string {
	output, err := c.run("tmux", "display-message", "-p", "-t", paneID, "#{pane_title}")
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(output), "\r\n")
}

func (c *attentionCommand) windowAttentionRows(windowID string) []attentionWindowRow {
	output, err := c.run("tmux", "list-panes", "-t", windowID, "-F", "#{pane_title}\t#{@dotfiles_attention_state}")
	if err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	rows := make([]attentionWindowRow, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		row := attentionWindowRow{Title: fields[0]}
		if len(fields) == 2 {
			row.State = strings.TrimSpace(fields[1])
		}
		rows = append(rows, row)
	}
	return rows
}

func (c *attentionCommand) setPaneOption(paneID, option, value string) {
	_, _ = c.run("tmux", "set-option", "-p", "-t", paneID, option, value)
}

func (c *attentionCommand) unsetPaneOption(paneID, option string) {
	_, _ = c.run("tmux", "set-option", "-p", "-u", "-t", paneID, option)
}

func (c *attentionCommand) selectPaneTitle(paneID, title string) {
	_, _ = c.run("tmux", "select-pane", "-T", title, "-t", paneID)
}

func (c *attentionCommand) displayPaneMessage(paneID, message string) {
	_, _ = c.run("tmux", "display-message", "-t", paneID, message)
}

func (c *attentionCommand) run(name string, args ...string) ([]byte, error) {
	if c.runner == nil {
		return nil, errors.New("attention tmux runner is not configured")
	}
	return c.runner.Run(context.Background(), name, args...)
}

func trimAttentionPrefix(title string) string {
	switch {
	case strings.HasPrefix(title, "✳ "):
		return strings.TrimPrefix(title, "✳ ")
	case strings.HasPrefix(title, "✳"):
		return strings.TrimPrefix(title, "✳")
	case strings.HasPrefix(title, "✔ "):
		return strings.TrimPrefix(title, "✔ ")
	case strings.HasPrefix(title, "✔"):
		return strings.TrimPrefix(title, "✔")
	default:
		return title
	}
}

func hasAttentionPrefix(title string) bool {
	return strings.HasPrefix(title, "✳") || strings.HasPrefix(title, "✔")
}

func hasBraillePrefix(title string) bool {
	r, _ := utf8.DecodeRuneInString(title)
	return r >= 0x2800 && r <= 0x28ff
}

func printAttentionUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux attention toggle [pane]")
	fmt.Fprintln(w, "  projmux attention clear [pane]")
	fmt.Fprintln(w, "  projmux attention window [window]")
}
