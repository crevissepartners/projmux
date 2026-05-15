package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

const (
	attentionStateOption      = "@projmux_attention_state"
	attentionAckOption        = "@projmux_attention_ack"
	attentionFocusArmedOption = "@projmux_attention_focus_armed"
	attentionStateBusy        = "busy"
	attentionStateReply       = "reply"
)

const (
	attentionListSeparator = "\x1f"
	attentionListFormat    = "#{session_name}" + attentionListSeparator +
		"#{window_id}" + attentionListSeparator +
		"#{pane_id}" + attentionListSeparator +
		"#{pane_active}" + attentionListSeparator +
		"#{pane_title}" + attentionListSeparator +
		"#{" + attentionStateOption + "}" + attentionListSeparator +
		"#{" + aiPaneStateOption + "}" + attentionListSeparator +
		"#{" + aiPaneAgentOption + "}" + attentionListSeparator +
		"#{" + aiPaneTopicOption + "}" + attentionListSeparator +
		"#{socket_path}"
)

type attentionCommand struct {
	runner   tmuxRunner
	producer attentionNotifyProducer
}

func newAttentionCommand() *attentionCommand {
	return &attentionCommand{
		runner:   inttmux.ExecRunner{},
		producer: newAttentionNotifyProducer(),
	}
}

// notifyProducer returns the wired-up producer or a noop when the command
// was constructed without one (tests that focus on the existing tmux call
// surface).
func (c *attentionCommand) notifyProducer() attentionNotifyProducer {
	if c == nil || c.producer == nil {
		return noopAttentionNotifyProducer{}
	}
	return c.producer
}

// notifyLookup adapts attentionCommand's tmux helpers to the producer
// lookup contract so the producer does not need its own tmux runner. The
// helpers already short-circuit to "" on error, which is exactly the
// fallback the producer expects.
func (c *attentionCommand) notifyLookup() attentionNotifyLookup {
	return attentionLookup{cmd: c}
}

type attentionLookup struct {
	cmd *attentionCommand
}

func (l attentionLookup) PaneOption(paneID, option string) string {
	if l.cmd == nil {
		return ""
	}
	return l.cmd.paneOption(paneID, option)
}

func (l attentionLookup) PaneFormat(paneID, format string) string {
	if l.cmd == nil || l.cmd.runner == nil {
		return ""
	}
	output, err := intmux.NewRunner(l.cmd.runner).DisplayMessageTrimmed(context.Background(), intmux.DisplayMessageOptions{
		Target: paneID,
		Format: format,
	})
	if err != nil {
		return ""
	}
	return output
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
	case "list":
		return c.runList(args[1:], stdout, stderr)
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

func (c *attentionCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("attention list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printAttentionListUsage(stderr) }
	asJSON := fs.Bool("json", false, "emit json instead of tabular output")
	all := fs.Bool("all", false, "include panes without attention state")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse attention list flags: %w", err)
	}
	if fs.NArg() != 0 {
		printAttentionUsage(stderr)
		return fmt.Errorf("attention list does not accept positional arguments")
	}

	rows, err := c.listAttentionPanes()
	if err != nil {
		return err
	}
	if !*all {
		rows = filterAttentionRows(rows)
	}

	if *asJSON {
		if rows == nil {
			rows = []attentionPaneRow{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	return writeAttentionTable(stdout, rows)
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
		c.notifyProducer().AckReplyReady(attentionNotifyInput{PaneID: paneID, Lookup: c.notifyLookup()})
		return nil
	}

	c.setPaneOption(paneID, attentionStateOption, attentionStateReply)
	c.selectPaneTitle(paneID, "✳ "+title)
	c.displayPaneMessage(paneID, "attention: needs reply")
	c.notifyProducer().PushReplyReady(attentionNotifyInput{PaneID: paneID, Lookup: c.notifyLookup()})
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
	if state == attentionStateReply && c.paneOption(paneID, attentionFocusArmedOption) != "1" && !c.paneVisibleToClient(paneID) {
		return nil
	}
	c.unsetPaneOption(paneID, attentionStateOption)
	c.setPaneOption(paneID, attentionAckOption, "1")
	c.unsetPaneOption(paneID, attentionFocusArmedOption)
	c.notifyProducer().AckReplyReady(attentionNotifyInput{PaneID: paneID, Lookup: c.notifyLookup()})

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

type attentionPaneRow struct {
	Session        string `json:"session"`
	Window         string `json:"window"`
	Pane           string `json:"pane"`
	Active         bool   `json:"active"`
	Title          string `json:"title"`
	AttentionState string `json:"attention_state"`
	AIState        string `json:"ai_state"`
	Agent          string `json:"agent"`
	Topic          string `json:"topic"`
	Socket         string `json:"socket"`
}

func (c *attentionCommand) paneTitle(paneID string) string {
	if c.runner == nil {
		return ""
	}
	output, err := intmux.NewRunner(c.runner).DisplayMessage(context.Background(), intmux.DisplayMessageOptions{
		Target: paneID,
		Format: intmux.TmuxFormat("pane_title"),
	})
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(output), "\r\n")
}

func (c *attentionCommand) paneAttentionState(paneID string) string {
	return c.paneOption(paneID, attentionStateOption)
}

// paneVisibleToClient reports whether some attached tmux client is currently
// viewing paneID. The naive #{pane_active} check is wrong here: a pane stays
// pane_active=1 even when every client has switched to a different window or
// session, which caused auto-ack to silently swallow reply notifications.
func (c *attentionCommand) paneVisibleToClient(paneID string) bool {
	output, err := c.run("tmux", "list-clients", "-F", "#{client_active_pane}")
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(strings.TrimRight(string(output), "\r\n"), "\n") {
		if strings.TrimSpace(line) == paneID {
			return true
		}
	}
	return false
}

func (c *attentionCommand) paneOption(paneID, option string) string {
	if c.runner == nil {
		return ""
	}
	output, err := intmux.NewRunner(c.runner).ShowPaneOption(context.Background(), paneID, option)
	if err != nil {
		return ""
	}
	return output
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

func (c *attentionCommand) listAttentionPanes() ([]attentionPaneRow, error) {
	output, err := c.run("tmux", "list-panes", "-a", "-F", attentionListFormat)
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}

	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	rows := make([]attentionPaneRow, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, attentionListSeparator)
		if len(fields) < 10 {
			continue
		}
		row := attentionPaneRow{
			Session:        strings.TrimSpace(fields[0]),
			Window:         strings.TrimSpace(fields[1]),
			Pane:           strings.TrimSpace(fields[2]),
			Active:         strings.TrimSpace(fields[3]) == "1",
			Title:          strings.TrimSpace(fields[4]),
			AttentionState: strings.TrimSpace(fields[5]),
			AIState:        strings.TrimSpace(fields[6]),
			Agent:          strings.TrimSpace(fields[7]),
			Topic:          strings.TrimSpace(fields[8]),
			Socket:         strings.TrimSpace(fields[9]),
		}
		if row.Session == "" || row.Pane == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func filterAttentionRows(rows []attentionPaneRow) []attentionPaneRow {
	out := make([]attentionPaneRow, 0, len(rows))
	for _, row := range rows {
		if row.AttentionState != "" || hasAttentionPrefix(row.Title) || hasBraillePrefix(row.Title) {
			out = append(out, row)
		}
	}
	return out
}

func writeAttentionTable(w io.Writer, rows []attentionPaneRow) error {
	if _, err := fmt.Fprintln(w, "SESSION\tWINDOW\tPANE\tACTIVE\tATTENTION\tAI\tAGENT\tTOPIC\tTITLE"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(
			w,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			attentionTableCell(row.Session),
			attentionTableCell(row.Window),
			attentionTableCell(row.Pane),
			formatAttentionActive(row.Active),
			attentionTableCell(row.AttentionState),
			attentionTableCell(row.AIState),
			attentionTableCell(row.Agent),
			attentionTableCell(row.Topic),
			attentionTableCell(row.Title),
		); err != nil {
			return err
		}
	}
	return nil
}

func attentionTableCell(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func formatAttentionActive(active bool) string {
	if active {
		return "yes"
	}
	return "no"
}

func (c *attentionCommand) setPaneOption(paneID, option, value string) {
	if c.runner == nil {
		return
	}
	_ = intmux.NewRunner(c.runner).SetPaneOption(context.Background(), paneID, option, value)
}

func (c *attentionCommand) unsetPaneOption(paneID, option string) {
	if c.runner == nil {
		return
	}
	_ = intmux.NewRunner(c.runner).UnsetPaneOption(context.Background(), paneID, option)
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
	fmt.Fprintln(w, "  projmux attention list [--json] [--all]")
	fmt.Fprintln(w, "  projmux attention window [window]")
}

func printAttentionListUsage(w io.Writer) {
	fmt.Fprintln(w, "Live tmux pane attention state; does not read or mutate the notify queue.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux attention list [--json] [--all]")
}
