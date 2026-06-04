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

	"github.com/crevissepartners/projmux/internal/core/aibadge"
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
	attentionListSeparator = intmux.FieldDelimiter
)

var attentionListFormats = []string{
	intmux.TmuxFormat("session_name"),
	intmux.TmuxFormat("window_id"),
	intmux.TmuxFormat("pane_id"),
	intmux.TmuxFormat("pane_active"),
	intmux.TmuxFormat("pane_title"),
	intmux.PaneOptionFormat(attentionStateOption),
	intmux.PaneOptionFormat(aiPaneStateOption),
	intmux.PaneOptionFormat(aiPaneAgentOption),
	intmux.PaneOptionFormat(aiPaneTopicOption),
	intmux.TmuxFormat("socket_path"),
}

var attentionListFormat = intmux.JoinFormats(attentionListSeparator, attentionListFormats...)

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
	c.consumeResponseCompleteLiveBadge(paneID)

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
	windowID, style, err := parseAttentionWindowArgs(args, stderr)
	if err != nil {
		return err
	}
	if windowID == "" {
		_, err := fmt.Fprint(stdout, " ")
		return err
	}

	rows := c.windowAttentionRows(windowID)
	badgeKind := ""
	for _, row := range rows {
		badgeKind = aibadge.Aggregate(badgeKind, attentionWindowBadgeKind(row))
	}

	if badgeKind != "" {
		glyph := aibadge.Glyph(badgeKind, style)
		if strings.TrimSpace(glyph) == "" {
			_, err := fmt.Fprint(stdout, " ")
			return err
		}
		_, err := fmt.Fprint(stdout, "#[fg="+tmuxAIBadgeKindFg(badgeKind)+"]"+glyph)
		return err
	}
	_, err = fmt.Fprint(stdout, " ")
	return err
}

func parseAttentionWindowArgs(args []string, stderr io.Writer) (windowID, style string, err error) {
	if len(args) > 2 {
		printAttentionUsage(stderr)
		return "", "", errors.New("attention window accepts at most 2 arguments")
	}
	if len(args) > 0 {
		windowID = strings.TrimSpace(args[0])
	}
	if len(args) > 1 {
		style = aibadge.NormalizeStyle(args[1])
	} else {
		style = aibadge.StyleDot
	}
	return windowID, style, nil
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
	Title       string
	State       string
	AIState     string
	AIBadgeKind string
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
	if c == nil || c.runner == nil {
		return nil
	}
	rows, err := intmux.NewRunner(c.runner).ListPanes(context.Background(), intmux.ListPanesOptions{
		Target: windowID,
		Formats: []string{
			intmux.TmuxFormat("pane_title"),
			intmux.PaneOptionFormat(attentionStateOption),
			intmux.PaneOptionFormat(aiPaneStateOption),
			intmux.PaneOptionFormat(aiPaneBadgeKindOption),
		},
	})
	if err != nil {
		return nil
	}
	if len(rows) == 0 {
		return c.legacyWindowAttentionRows(windowID)
	}

	out := make([]attentionWindowRow, 0, len(rows))
	for _, fields := range rows {
		out = append(out, attentionWindowRow{
			Title:       fields[0],
			State:       fields[1],
			AIState:     fields[2],
			AIBadgeKind: fields[3],
		})
	}
	return out
}

func (c *attentionCommand) legacyWindowAttentionRows(windowID string) []attentionWindowRow {
	rows, err := intmux.NewRunner(c.runner).ListPanes(context.Background(), intmux.ListPanesOptions{
		Target: windowID,
		Formats: []string{
			intmux.TmuxFormat("pane_title"),
			intmux.PaneOptionFormat(attentionStateOption),
		},
	})
	if err != nil {
		return nil
	}

	out := make([]attentionWindowRow, 0, len(rows))
	for _, fields := range rows {
		out = append(out, attentionWindowRow{
			Title: fields[0],
			State: fields[1],
		})
	}
	return out
}

func attentionWindowBadgeKind(row attentionWindowRow) string {
	if kind := normalizeAIBadgeKind(row.AIBadgeKind); kind != "" {
		return kind
	}
	switch {
	case row.State == attentionStateBusy || strings.TrimSpace(row.AIState) == "thinking" || hasBraillePrefix(row.Title):
		return aiBadgeKindInProgress
	case row.State == attentionStateReply || strings.TrimSpace(row.AIState) == "waiting" || hasAttentionPrefix(row.Title):
		return aiBadgeKindResponseComplete
	default:
		return ""
	}
}

func (c *attentionCommand) consumeResponseCompleteLiveBadge(paneID string) {
	badgeKind := strings.TrimSpace(c.paneOption(paneID, aiPaneBadgeKindOption))
	aiState := strings.TrimSpace(c.paneOption(paneID, aiPaneStateOption))
	if isResponseCompleteLiveBadgeKind(badgeKind) {
		c.unsetPaneOption(paneID, aiPaneBadgeKindOption)
		if aiState == "waiting" {
			c.setPaneOption(paneID, aiPaneStateOption, "idle")
		}
		return
	}
	if normalizeAIBadgeKind(badgeKind) != "" {
		return
	}
	if aiState == "waiting" {
		c.setPaneOption(paneID, aiPaneStateOption, "idle")
	}
}

func isResponseCompleteLiveBadgeKind(kind string) bool {
	kind = strings.TrimSpace(kind)
	return normalizeAIBadgeKind(kind) == aiBadgeKindResponseComplete || kind == "response_ready"
}

func tmuxAIBadgeKindFg(kind string) string {
	switch aibadge.ThemeRole(kind) {
	case aibadge.RoleActionRequired:
		return tmuxAIBadgeActionRequiredFg
	case aibadge.RoleSuccess:
		return tmuxAIBadgeSuccessFg
	case aibadge.RoleProgress:
		return tmuxAIBadgeProgressFg
	default:
		return tmuxAIBadgeProgressFg
	}
}

func (c *attentionCommand) listAttentionPanes() ([]attentionPaneRow, error) {
	if c == nil || c.runner == nil {
		return nil, errors.New("attention tmux runner is not configured")
	}
	rows, err := intmux.NewRunner(c.runner).ListPanes(context.Background(), intmux.ListPanesOptions{
		All:              true,
		Formats:          attentionListFormats,
		Delimiter:        attentionListSeparator,
		AllowExtraFields: true,
	})
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}

	out := make([]attentionPaneRow, 0, len(rows))
	for _, fields := range rows {
		row := attentionPaneRow{
			Session:        fields[0],
			Window:         fields[1],
			Pane:           fields[2],
			Active:         fields[3] == "1",
			Title:          fields[4],
			AttentionState: fields[5],
			AIState:        fields[6],
			Agent:          fields[7],
			Topic:          fields[8],
			Socket:         fields[9],
		}
		if row.Session == "" || row.Pane == "" {
			continue
		}
		out = append(out, row)
	}
	return out, nil
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
