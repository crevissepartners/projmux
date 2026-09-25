package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/theme"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
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
	runner               tmuxRunner
	producer             attentionNotifyProducer
	sidebarPreviewActive func() bool
	homeDir              func() (string, error)
	lookupEnv            func(string) string
}

func newAttentionCommand() *attentionCommand {
	return &attentionCommand{
		runner:               inttmux.ExecRunner{},
		producer:             newAttentionNotifyProducer(),
		sidebarPreviewActive: isSidebarPreviewActive,
		homeDir:              os.UserHomeDir,
		lookupEnv:            os.Getenv,
	}
}

// sidebarPreviewGateActive reports whether focus-hook attention updates must
// be skipped because the project sidebar popup is previewing sessions. Only
// `attention arm`/`attention clear` consult this gate: both subcommands are
// invoked exclusively by the pane-focus-in/out and after-select-pane hooks,
// so skipping them keeps peeked panes' markers intact. Manual
// `attention toggle` and the AI ingest path (direct set-option in ai.go)
// never route through these subcommands and stay live during preview.
func (c *attentionCommand) sidebarPreviewGateActive() bool {
	return c != nil && c.sidebarPreviewActive != nil && c.sidebarPreviewActive()
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
		printRouteUsage(stderr, "attention")
		return usageError("attention requires a subcommand")
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
		printRouteUsage(stdout, "attention")
		return nil
	default:
		printRouteUsage(stderr, "attention")
		return usageError(fmt.Sprintf("unknown attention subcommand: %s", args[0]))
	}
}

func (c *attentionCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("attention list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Live tmux pane attention state; does not read or mutate the notify queue.")
		fmt.Fprintln(stderr)
		printRouteUsage(stderr, "attention list")
	}
	asJSON := fs.Bool("json", false, "emit json instead of tabular output")
	all := fs.Bool("all", false, "include panes without attention state")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return flagParseError(fmt.Errorf("parse attention list flags: %w", err))
	}
	if fs.NArg() != 0 {
		printRouteUsage(stderr, "attention list")
		return usageError("attention list does not accept positional arguments")
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
	paneID, err := c.resolveOptionalAttentionTarget(args, "attention toggle", func() { printRouteUsage(stderr, "attention toggle") })
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
	paneID, err := c.resolveOptionalAttentionTarget(args, "attention clear", func() { printRouteUsage(stderr, "attention clear") })
	if err != nil || paneID == "" {
		return err
	}
	if c.sidebarPreviewGateActive() {
		return nil
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
	paneID, err := c.resolveOptionalAttentionTarget(args, "attention arm", func() { printRouteUsage(stderr, "attention arm") })
	if err != nil || paneID == "" {
		return err
	}
	if c.sidebarPreviewGateActive() {
		return nil
	}
	if c.paneAttentionState(paneID) == attentionStateReply {
		c.setPaneOption(paneID, attentionFocusArmedOption, "1")
	}
	return nil
}

func (c *attentionCommand) runWindow(args []string, stdout, stderr io.Writer) error {
	// Arguments are validated before the theme is resolved so a usage
	// rejection never reads config.
	windowID, style, err := parseAttentionWindowArgs(args, stderr)
	if err != nil {
		return err
	}
	// Bright Phase 2 (B1): the window badge glyph renders with the resolved
	// effective theme instead of the zero-value fallback role map.
	defer applyNativeUIThemeFromConfig(c.homeDir, c.lookupEnv, "")()
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
		_, err := fmt.Fprint(stdout, "#[fg="+tmuxAIBadgeKindFg(badgeKind, statusSegmentRoles)+"]"+glyph)
		return err
	}
	_, err = fmt.Fprint(stdout, " ")
	return err
}

func parseAttentionWindowArgs(args []string, stderr io.Writer) (windowID, style string, err error) {
	args, err = splitOperands("attention window", args)
	if err != nil {
		printRouteUsage(stderr, "attention window")
		return "", "", err
	}
	if len(args) > 2 {
		printRouteUsage(stderr, "attention window")
		return "", "", usageError("attention window accepts at most 2 arguments")
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

// parseOptionalAttentionTarget returns the optional pane target and whether an
// operand was supplied at all. Unknown flags are rejected before the arity
// check, and a bare `--` with nothing after it counts as no target.
func parseOptionalAttentionTarget(args []string, command string, printUsage func()) (string, bool, error) {
	operands, err := splitOperands(command, args)
	if err != nil {
		printUsage()
		return "", false, err
	}
	if len(operands) > 1 {
		printUsage()
		return "", false, usageError(fmt.Sprintf("%s accepts at most 1 target argument", command))
	}
	if len(operands) == 0 {
		return "", false, nil
	}
	return strings.TrimSpace(operands[0]), true, nil
}

// splitOperands separates the positional operands of a flagless subcommand
// from anything that looks like a flag. The subcommands that use it accept no
// flags, so before the first bare `--` every token that starts with `-` and is
// not exactly `-` is rejected as a usage error naming the token verbatim
// ("<command>: unknown flag <token>"). A lone `-` is an operand. The first bare
// `--` ends option parsing: it is dropped and every later token, including
// `-x` and another `--`, is an operand. All tokens are scanned before the
// caller applies its arity checks, so an unknown flag wins over "too many
// arguments". It never prints; callers print their own usage text.
func splitOperands(command string, args []string) ([]string, error) {
	operands := make([]string, 0, len(args))
	for i, tok := range args {
		if tok == "--" {
			return append(operands, args[i+1:]...), nil
		}
		if strings.HasPrefix(tok, "-") && tok != "-" {
			return nil, usageError(fmt.Sprintf("%s: unknown flag %s", command, tok))
		}
		operands = append(operands, tok)
	}
	return operands, nil
}

// resolveOptionalAttentionTarget keeps explicit targets byte-for-byte on their
// historical path. An omitted target is different: it is authority to mutate
// only the exact pane that invoked the command, so both halves of tmux's
// inherited client receipt must be present and the pane must still reobserve as
// itself before the first attention handler read or write.
func (c *attentionCommand) resolveOptionalAttentionTarget(args []string, command string, printUsage func()) (string, error) {
	paneID, explicit, err := parseOptionalAttentionTarget(args, command, printUsage)
	if err != nil || explicit {
		return paneID, err
	}

	requireTarget := func(detail string) (string, error) {
		return "", fmt.Errorf("%s requires an explicit pane or valid inherited TMUX_PANE; %s", command, detail)
	}
	if c == nil || c.lookupEnv == nil || strings.TrimSpace(c.lookupEnv("TMUX")) == "" {
		return requireTarget("run it inside the target tmux pane or pass [pane]")
	}
	inherited := c.lookupEnv("TMUX_PANE")
	if exactTmuxHandle(inherited, "%") == "" || exactTmuxHandle(inherited, "%") != inherited {
		return requireTarget("TMUX_PANE must be an exact pane id such as %7, or pass [pane]")
	}
	if c.runner == nil {
		return requireTarget("the inherited pane could not be observed; pass [pane]")
	}
	observed, observeErr := intmux.NewRunner(c.runner).DisplayMessageTrimmed(context.Background(), intmux.DisplayMessageOptions{
		Target: inherited,
		Format: intmux.TmuxFormat("pane_id"),
	})
	if observeErr != nil || observed != inherited {
		return requireTarget("the inherited pane is stale or no longer resolves on this tmux server; pass [pane]")
	}
	return inherited, nil
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

// visibleClientPaneFormat is the list-clients format both paneVisibleToClient
// methods read. In list-clients context #{pane_id} expands to the active pane
// of each attached client's current window. tmux has no
// #{client_active_pane}; an unknown name expands to nothing, which once made
// every pane read as unseen.
const visibleClientPaneFormat = "#{pane_id}"

// listClientsShowPane reports whether a line of list-clients output written
// with visibleClientPaneFormat names paneID.
func listClientsShowPane(output []byte, paneID string) bool {
	for line := range strings.SplitSeq(strings.TrimRight(string(output), "\r\n"), "\n") {
		if strings.TrimSpace(line) == paneID {
			return true
		}
	}
	return false
}

// paneVisibleToClient reports whether some attached tmux client is currently
// viewing paneID: whether it is the active pane of some client's current
// window (visibleClientPaneFormat). The naive #{pane_active} check is wrong
// here: a pane stays pane_active=1 even when every client has switched to a
// different window or session, which caused auto-ack to silently swallow
// reply notifications.
func (c *attentionCommand) paneVisibleToClient(paneID string) bool {
	output, err := c.run("tmux", "list-clients", "-F", visibleClientPaneFormat)
	if err != nil {
		return false
	}
	return listClientsShowPane(output, paneID)
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
	// Legacy: retained for old pane option format fallback; sunset when the
	// new window-attention format has a versioned migration/expiry policy and
	// old pane options are intentionally dropped.
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
	case row.State == attentionStateBusy || strings.TrimSpace(row.AIState) == "thinking" || intrender.HasBraillePrefix(row.Title):
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

func tmuxAIBadgeKindFg(kind string, roles theme.RenderRoles) string {
	switch aibadge.ThemeRole(kind) {
	case aibadge.RoleActionRequired:
		return roles.AIActionRequired
	case aibadge.RoleSuccess:
		return roles.AISuccess
	case aibadge.RoleProgress:
		return roles.AIProgress
	default:
		return roles.AIProgress
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

// attentionLivePaneLister implements the notify cluster's livePaneLister
// seam on top of [attentionCommand.listAttentionPanes], translating
// attentionPaneRow into the neutral livePaneRow DTO so the notify side does
// not depend on attention internals (state consts, title-prefix helpers).
type attentionLivePaneLister struct {
	runner tmuxRunner
}

func newAttentionLivePaneLister(runner tmuxRunner) livePaneLister {
	return attentionLivePaneLister{runner: runner}
}

// newDefaultLivePaneLister builds the production live-pane lister used by
// the app constructor wiring in [New].
func newDefaultLivePaneLister() livePaneLister {
	return newGenerationAwareLivePaneLister(newAttentionLivePaneLister(inttmux.ExecRunner{}), snapshotResourceRegistry)
}

func (l attentionLivePaneLister) ListLivePanes() ([]livePaneRow, error) {
	rows, err := (&attentionCommand{runner: l.runner}).listAttentionPanes()
	if err != nil {
		return nil, err
	}
	out := make([]livePaneRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, livePaneRow{
			Session:        row.Session,
			Window:         row.Window,
			Pane:           row.Pane,
			Socket:         row.Socket,
			Title:          row.Title,
			AttentionState: row.AttentionState,
			AIState:        row.AIState,
			Agent:          row.Agent,
			Topic:          row.Topic,
			ReplyState:     row.AttentionState == attentionStateReply,
			TitleBadge:     hasAttentionPrefix(row.Title) || intrender.HasBraillePrefix(row.Title),
		})
	}
	return out, nil
}

func filterAttentionRows(rows []attentionPaneRow) []attentionPaneRow {
	out := make([]attentionPaneRow, 0, len(rows))
	for _, row := range rows {
		if row.AttentionState != "" || hasAttentionPrefix(row.Title) || intrender.HasBraillePrefix(row.Title) {
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
	// best-effort-write: a pane title is decoration. Nothing reads it back and
	// no record claims it was set, so a failure here cannot make a surface
	// report a state the Pane does not hold.
	_, _ = c.run("tmux", "select-pane", "-T", title, "-t", paneID)
}

func (c *attentionCommand) displayPaneMessage(paneID, message string) {
	// best-effort-write: a one-shot message to the operator's own screen. Its
	// failure mode is the operator not seeing a line they were already looking
	// at, which is not a lie told to a later reader.
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
