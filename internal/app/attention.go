package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

const (
	attentionStateOption      = "@projmux_attention_state"
	attentionAckOption        = "@projmux_attention_ack"
	attentionFocusArmedOption = "@projmux_attention_focus_armed"
	attentionStateBusy        = "busy"
	attentionStateReply       = "reply"
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
	case "arm":
		return c.runArm(args[1:], stderr)
	case "window":
		return c.runWindow(args[1:], stdout, stderr)
	case "list":
		return c.runList(args[1:], stdout, stderr)
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

	state := c.paneAttentionState(paneID)
	if state == attentionStateBusy {
		return nil
	}
	if state == attentionStateReply && c.paneOption(paneID, attentionFocusArmedOption) != "1" && !c.paneActive(paneID) {
		return nil
	}
	c.unsetPaneOption(paneID, attentionStateOption)
	c.setPaneOption(paneID, attentionAckOption, "1")
	c.unsetPaneOption(paneID, attentionFocusArmedOption)

	title := c.paneTitle(paneID)
	clean := trimAttentionPrefix(title)
	if clean == title {
		return nil
	}
	c.selectPaneTitle(paneID, clean)
	return nil
}

func (c *attentionCommand) runArm(args []string, stderr io.Writer) error {
	paneID, err := parseOptionalAttentionTarget(args, "attention arm", stderr)
	if err != nil || paneID == "" {
		return err
	}
	if c.paneAttentionState(paneID) == attentionStateReply {
		c.setPaneOption(paneID, attentionFocusArmedOption, "1")
	}
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

func (c *attentionCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("attention list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "include panes without attention, AI, or notification state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAttentionUsage(stderr)
		return errors.New("attention list does not accept positional arguments")
	}

	rows := c.attentionListRows()
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tSESSION\tWIN\tPANE\tACTIVE\tATTENTION\tAI\tAGENT\tTOPIC\tNOTIFIED\tKEY\tAT\tTITLE")
	wrote := false
	for _, row := range rows {
		if !*all && !row.HasState() {
			continue
		}
		wrote = true
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Target,
			row.Session,
			row.WindowIndex,
			row.PaneIndex,
			row.Active,
			row.Attention,
			row.AIState,
			row.Agent,
			row.Topic,
			row.Notified,
			row.NotificationKey,
			row.NotificationAt,
			row.Title,
		)
	}
	if !wrote {
		fmt.Fprintln(tw, "(no live attention, AI, or notification state)")
	}
	return tw.Flush()
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

type attentionListRow struct {
	Session         string
	WindowIndex     string
	PaneIndex       string
	Target          string
	Active          string
	Title           string
	Attention       string
	AIState         string
	Agent           string
	Topic           string
	Notified        string
	NotificationKey string
	NotificationAt  string
}

func (r attentionListRow) HasState() bool {
	return strings.TrimSpace(r.Attention) != "" ||
		strings.TrimSpace(r.AIState) != "" ||
		strings.TrimSpace(r.Agent) != "" ||
		strings.TrimSpace(r.Topic) != "" ||
		strings.TrimSpace(r.Notified) != "" ||
		strings.TrimSpace(r.NotificationKey) != "" ||
		strings.TrimSpace(r.NotificationAt) != ""
}

func (c *attentionCommand) paneTitle(paneID string) string {
	output, err := c.run("tmux", "display-message", "-p", "-t", paneID, "#{pane_title}")
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(output), "\r\n")
}

func (c *attentionCommand) paneAttentionState(paneID string) string {
	return c.paneOption(paneID, attentionStateOption)
}

func (c *attentionCommand) paneActive(paneID string) bool {
	return c.paneOption(paneID, "pane_active") == "1"
}

func (c *attentionCommand) paneOption(paneID, option string) string {
	output, err := c.run("tmux", "display-message", "-p", "-t", paneID, "#{"+option+"}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (c *attentionCommand) windowAttentionRows(windowID string) []attentionWindowRow {
	output, err := c.run("tmux", "list-panes", "-t", windowID, "-F", "#{pane_title}\t#{@projmux_attention_state}")
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

func (c *attentionCommand) attentionListRows() []attentionListRow {
	format := strings.Join([]string{
		"#{session_name}",
		"#{window_index}",
		"#{pane_index}",
		"#{pane_id}",
		"#{?pane_active,1,0}",
		"#{pane_title}",
		"#{@projmux_attention_state}",
		"#{@projmux_ai_state}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_desktop_notified}",
		"#{@projmux_desktop_notification_key}",
		"#{@projmux_desktop_notification_at}",
	}, "\t")
	output, err := c.run("tmux", "list-panes", "-a", "-F", format)
	if err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	rows := make([]attentionListRow, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		for len(fields) < 13 {
			fields = append(fields, "")
		}
		rows = append(rows, attentionListRow{
			Session:         fields[0],
			WindowIndex:     fields[1],
			PaneIndex:       fields[2],
			Target:          fields[3],
			Active:          fields[4],
			Title:           fields[5],
			Attention:       fields[6],
			AIState:         fields[7],
			Agent:           fields[8],
			Topic:           fields[9],
			Notified:        fields[10],
			NotificationKey: fields[11],
			NotificationAt:  fields[12],
		})
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
	fmt.Fprintln(w, "  projmux attention arm [pane]")
	fmt.Fprintln(w, "  projmux attention window [window]")
	fmt.Fprintln(w, "  projmux attention list [--all]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Attention is live pane state. AI notification reset/recovery uses pane")
	fmt.Fprintln(w, "options: `projmux ai notify reset [pane]` clears the desktop notification")
	fmt.Fprintln(w, "dedupe marker, and `projmux ai notify notify [pane]` forces a send.")
}
