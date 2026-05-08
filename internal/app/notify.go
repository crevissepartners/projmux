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
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	intfzf "github.com/crevissepartners/projmux/internal/ui/fzf"
)

// notifyStore is the subset of *notify.Store the CLI dispatcher needs. The
// interface keeps the command layer mockable in tests without leaking file
// paths into the test setup.
type notifyStore interface {
	Push(notify.PushInput) (notify.Notification, notify.PushResult, error)
	List() ([]notify.Notification, error)
	Ack(id string) error
	AckAll() (int, error)
}

type notifyCommand struct {
	store      notifyStore
	storeErr   error
	now        func() time.Time
	runner     tmuxRunner
	picker     intfzf.Runner
	executable func() (string, error)
}

func newNotifyCommand() *notifyCommand {
	cmd := &notifyCommand{
		now:        time.Now,
		runner:     reconcileDefaultRunner(),
		picker:     intfzf.NewRunner(),
		executable: os.Executable,
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		cmd.storeErr = fmt.Errorf("resolve default config paths: %w", err)
		return cmd
	}
	cmd.store = notify.NewDefaultStore(paths)
	return cmd
}

// Run dispatches the configured notify subcommands.
func (c *notifyCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printNotifyUsage(stderr)
		return usageError("notify requires a subcommand")
	}

	switch args[0] {
	case "push":
		return c.runPush(args[1:], stdout, stderr)
	case "list":
		return c.runList(args[1:], stdout, stderr)
	case "ack":
		return c.runAck(args[1:], stdout, stderr)
	case "reconcile":
		return c.runReconcile(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printNotifyUsage(stdout)
		return nil
	default:
		printNotifyUsage(stderr)
		return usageError(fmt.Sprintf("unknown notify subcommand: %s", args[0]))
	}
}

func (c *notifyCommand) requireStore() (notifyStore, error) {
	if c.storeErr != nil {
		return nil, fmt.Errorf("configure notify store: %w", c.storeErr)
	}
	if c.store == nil {
		return nil, errors.New("configure notify store: notify store is not configured")
	}
	return c.store, nil
}

func (c *notifyCommand) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// --- push --------------------------------------------------------------------

func (c *notifyCommand) runPush(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notify push", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		text     = fs.String("text", "", "notification text (required)")
		target   = fs.String("target", "", "SESSION[:WINDOW[.PANE]] target (required)")
		socket   = fs.String("socket", "", "tmux socket name (optional)")
		severity = fs.String("severity", notify.SeverityInfo, "info|warn|critical")
		source   = fs.String("source", notify.SourceExternal, "ai|k8s|git|external")
		ttlSecs  = fs.Int("ttl", int(notify.DefaultTTL/time.Second), "ttl in seconds")
		id       = fs.String("id", "", "stable id for dedupe (optional)")
		asJSON   = fs.Bool("json", false, "emit json instead of human output")
	)

	if err := fs.Parse(args); err != nil {
		return usageError(fmt.Sprintf("parse notify push flags: %v", err))
	}
	if fs.NArg() != 0 {
		printNotifyUsage(stderr)
		return usageError("notify push does not accept positional arguments")
	}
	if strings.TrimSpace(*text) == "" {
		printNotifyUsage(stderr)
		return usageError("notify push requires --text")
	}
	if strings.TrimSpace(*target) == "" {
		printNotifyUsage(stderr)
		return usageError("notify push requires --target")
	}
	if err := notify.ValidateSeverity(*severity); err != nil {
		printNotifyUsage(stderr)
		return usageError(err.Error())
	}
	if err := notify.ValidateSource(*source); err != nil {
		printNotifyUsage(stderr)
		return usageError(err.Error())
	}
	if *ttlSecs <= 0 {
		printNotifyUsage(stderr)
		return usageError("notify push requires positive --ttl")
	}

	parsed, err := notify.ParseTarget(*target)
	if err != nil {
		printNotifyUsage(stderr)
		return usageError(err.Error())
	}
	parsed.Socket = strings.TrimSpace(*socket)

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	in := notify.PushInput{
		ID:       *id,
		Text:     *text,
		Severity: *severity,
		Source:   *source,
		TTL:      time.Duration(*ttlSecs) * time.Second,
		Target:   parsed,
	}
	entry, result, err := store.Push(in)
	if err != nil {
		if isInvalidInputErr(err) {
			return usageError(err.Error())
		}
		return fmt.Errorf("push notification: %w", err)
	}

	if *asJSON {
		payload := map[string]any{
			"id":     result.ID,
			"queued": result.QueueLen,
		}
		return writeJSON(stdout, payload)
	}
	_, err = fmt.Fprintf(stdout, "queued %s (%d in queue)\n", entry.ID, result.QueueLen)
	return err
}

// --- list --------------------------------------------------------------------

func (c *notifyCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notify list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printNotifyListUsage(stderr) }

	var (
		asJSON     = fs.Bool("json", false, "emit json instead of tabular output")
		live       = fs.Bool("live", false, "include live tmux pane attention explanations")
		limit      = fs.Int("limit", 0, "limit number of returned entries (0 = no limit)")
		ui         = fs.String("ui", "table", "table|sidebar")
		severities multiFlag
		sources    multiFlag
	)
	fs.Var(&severities, "severity", "filter by severity (repeatable)")
	fs.Var(&sources, "source", "filter by source (repeatable)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageError(fmt.Sprintf("parse notify list flags: %v", err))
	}
	if fs.NArg() != 0 {
		printNotifyUsage(stderr)
		return usageError("notify list does not accept positional arguments")
	}
	if *limit < 0 {
		return usageError("notify list --limit must be >= 0")
	}
	if *ui != "table" && *ui != "sidebar" {
		return usageError("notify list --ui must be table or sidebar")
	}
	if *ui == "sidebar" && *asJSON {
		return usageError("notify list --ui=sidebar cannot be combined with --json")
	}
	if *ui == "sidebar" && *live {
		return usageError("notify list --ui=sidebar cannot be combined with --live")
	}
	for _, s := range severities {
		if err := notify.ValidateSeverity(s); err != nil {
			return usageError(err.Error())
		}
	}
	for _, s := range sources {
		if err := notify.ValidateSource(s); err != nil {
			return usageError(err.Error())
		}
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	entries, err := store.List()
	if err != nil {
		return fmt.Errorf("list notifications: %w", err)
	}

	entries = filterEntries(entries, severities, sources)
	if *limit > 0 && len(entries) > *limit {
		entries = entries[:*limit]
	}

	if *ui == "sidebar" {
		return c.runSidebar(entries, stdout, stderr)
	}

	if *asJSON {
		if entries == nil {
			entries = []notify.Notification{}
		}
		if *live {
			report, err := c.buildNotifyLiveReport(entries)
			if err != nil {
				return err
			}
			return writeJSON(stdout, report)
		}
		return writeJSON(stdout, entries)
	}
	if *live {
		report, err := c.buildNotifyLiveReport(entries)
		if err != nil {
			return err
		}
		return writeNotifyLiveTable(stdout, report, c.clock())
	}
	return writeNotifyTable(stdout, entries, c.clock())
}

func (c *notifyCommand) runSidebar(entries []notify.Notification, stdout, stderr io.Writer) error {
	if c.picker == nil {
		return errors.New("notify sidebar picker is not configured")
	}
	now := c.clock()
	fzfOptions := intfzf.Options{
		UI:         "notify-sidebar",
		Read0:      true,
		Prompt:     "Notify > ",
		Header:     "Pending notifications, newest first",
		Footer:     "Enter: focus + ack  |  a: ack  |  Ctrl-A: clear all  |  Esc/Alt-2: close",
		ExpectKeys: []string{"a", "ctrl-a"},
		Bindings:   []string{"alt-2:abort"},
		Entries:    notifySidebarEntries(entries, now),
	}
	result, err := c.picker.Run(fzfOptions)
	if err != nil {
		return fmt.Errorf("run notify sidebar: %w", err)
	}
	id := strings.TrimSpace(result.Value)
	if id == "" || id == notifySidebarEmptyValue {
		return nil
	}
	store, err := c.requireStore()
	if err != nil {
		return err
	}
	switch result.Key {
	case "ctrl-a":
		removed, err := store.AckAll()
		if err != nil {
			return fmt.Errorf("clear all notifications: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "cleared %d notification(s)\n", removed)
		return err
	case "a":
		if err := store.Ack(id); err != nil {
			return fmt.Errorf("ack notification: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "ack %s\n", id)
		return err
	default:
		entry, ok := findNotificationByID(entries, id)
		if !ok {
			return fmt.Errorf("focus notification: %w: %s", notify.ErrNotFound, id)
		}
		if err := c.focusNotification(entry, "notify-sidebar", "row-select"); err != nil {
			return err
		}
		if err := store.Ack(id); err != nil {
			return fmt.Errorf("ack focused notification: %w", err)
		}
		return nil
	}
}

const notifySidebarEmptyValue = "__projmux_notify_empty__"

func notifySidebarEntries(entries []notify.Notification, now time.Time) []intfzf.Entry {
	if len(entries) == 0 {
		return []intfzf.Entry{{
			Label:     "No pending notifications",
			Value:     notifySidebarEmptyValue,
			SearchKey: "empty no pending notifications",
		}}
	}
	out := make([]intfzf.Entry, 0, len(entries))
	for _, e := range entries {
		target := notify.FormatTarget(notify.Target{
			Socket:  e.Socket,
			Session: e.Session,
			Window:  e.Window,
			Pane:    e.Pane,
		})
		label := notifySidebarLabel(e, now)
		out = append(out, intfzf.Entry{
			Label:     label,
			Value:     e.ID,
			SearchKey: strings.Join([]string{e.ID, e.Text, e.Severity, e.Source, target}, " "),
		})
	}
	return out
}

func notifySidebarLabel(e notify.Notification, now time.Time) string {
	age := formatAge(now.Sub(e.CreatedAt))
	text := notifySidebarLabelCell(e.Text)
	if text == "" {
		text = "(empty notification)"
	}
	metaParts := []string{
		notifySidebarAge(age),
		notifySidebarProjectBadge(notifyProjectName(e.Session)),
	}
	if window := notifySidebarTargetPart("window", e.Window); window != "" {
		metaParts = append(metaParts, window)
	}
	if pane := notifySidebarTargetPart("pane", e.Pane); pane != "" {
		metaParts = append(metaParts, pane)
	}
	return text + "\n  " + strings.Join(metaParts, " ")
}

func notifySidebarLabelCell(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

func notifySidebarTarget(e notify.Notification) string {
	pane := strings.TrimSpace(e.Pane)
	if strings.HasPrefix(pane, "%") {
		pane = ""
	}
	return notifySidebarLabelCell(notify.FormatTarget(notify.Target{
		Session: e.Session,
		Window:  e.Window,
		Pane:    pane,
	}))
}

const (
	notifySidebarReset   = "\x1b[0m"
	notifySidebarDimOpen = "\x1b[38;5;245m"
	notifySidebarProject = "\x1b[1;38;5;231;48;5;90m"
)

func notifySidebarAge(age string) string {
	age = strings.TrimSpace(age)
	if age == "" {
		age = "0s"
	}
	return "\x1b[1;38;5;45m age " + age + " " + notifySidebarReset
}

func notifySidebarProjectBadge(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		project = "project"
	}
	return notifySidebarProject + " " + project + " " + notifySidebarReset
}

func notifySidebarTargetPart(label, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimLeft(value, "@%")
	value = notifySidebarLabelCell(value)
	if value == "" {
		return ""
	}
	return notifySidebarDim(label + " " + value)
}

func notifySidebarDim(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return notifySidebarDimOpen + value + notifySidebarReset
}

func findNotificationByID(entries []notify.Notification, id string) (notify.Notification, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return notify.Notification{}, false
}

func (c *notifyCommand) focusNotification(entry notify.Notification, source, kind string) error {
	if c.runner == nil {
		return errors.New("notify focus runner is not configured")
	}
	if c.executable == nil {
		return errors.New("notify executable resolver is not configured")
	}
	binaryPath, err := c.executable()
	if err != nil {
		return fmt.Errorf("resolve notify executable: %w", err)
	}
	target := notify.FormatTarget(notify.Target{
		Session: entry.Session,
		Window:  entry.Window,
		Pane:    entry.Pane,
	})
	if strings.TrimSpace(target) == "" {
		return errors.New("notification has no routable target")
	}
	args := []string{"focus", "--target", target, "--source", source, "--kind", kind}
	if socket := strings.TrimSpace(entry.Socket); socket != "" {
		args = append(args, "--socket", socket)
	}
	if _, err := c.runner.Run(context.Background(), binaryPath, args...); err != nil {
		return fmt.Errorf("focus notification: %w", err)
	}
	return nil
}

// --- ack ---------------------------------------------------------------------

func (c *notifyCommand) runAck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notify ack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "remove every queued entry")

	if err := fs.Parse(args); err != nil {
		return usageError(fmt.Sprintf("parse notify ack flags: %v", err))
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	if *all {
		if fs.NArg() != 0 {
			printNotifyUsage(stderr)
			return usageError("notify ack --all does not accept positional arguments")
		}
		removed, err := store.AckAll()
		if err != nil {
			return fmt.Errorf("ack all notifications: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "ack %d notification(s)\n", removed)
		return err
	}

	if fs.NArg() != 1 {
		printNotifyUsage(stderr)
		return usageError("notify ack requires exactly 1 <id> argument or --all")
	}
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		printNotifyUsage(stderr)
		return usageError("notify ack requires a non-empty <id> argument")
	}
	if err := store.Ack(id); err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			return fmt.Errorf("ack notification: %w", err)
		}
		return fmt.Errorf("ack notification: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "ack %s\n", id)
	return err
}

// --- helpers -----------------------------------------------------------------

func filterEntries(entries []notify.Notification, severities, sources []string) []notify.Notification {
	if len(severities) == 0 && len(sources) == 0 {
		return entries
	}
	sevSet := toSet(severities)
	srcSet := toSet(sources)
	out := make([]notify.Notification, 0, len(entries))
	for _, e := range entries {
		if len(sevSet) > 0 {
			if _, ok := sevSet[e.Severity]; !ok {
				continue
			}
		}
		if len(srcSet) > 0 {
			if _, ok := srcSet[e.Source]; !ok {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func toSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}

// writeNotifyTable renders a tab-aligned table: ID  AGE  SEV  SRC  TARGET  TEXT.
func writeNotifyTable(w io.Writer, entries []notify.Notification, now time.Time) error {
	if _, err := fmt.Fprintln(w, "ID\tAGE\tSEV\tSRC\tTARGET\tTEXT"); err != nil {
		return err
	}
	for _, e := range entries {
		target := notify.FormatTarget(notify.Target{
			Socket:  e.Socket,
			Session: e.Session,
			Window:  e.Window,
			Pane:    e.Pane,
		})
		if _, err := fmt.Fprintf(
			w,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID,
			formatAge(now.Sub(e.CreatedAt)),
			e.Severity,
			e.Source,
			target,
			e.Text,
		); err != nil {
			return err
		}
	}
	return nil
}

type notifyLiveReport struct {
	Queue  []notify.Notification `json:"queue"`
	Live   []notifyLivePane      `json:"live"`
	Rows   []notifyLiveRow       `json:"rows"`
	Errors []string              `json:"errors"`
}

type notifyLivePane struct {
	ID             string `json:"id"`
	Session        string `json:"session"`
	Window         string `json:"window,omitempty"`
	Pane           string `json:"pane"`
	Socket         string `json:"socket,omitempty"`
	Title          string `json:"title,omitempty"`
	AttentionState string `json:"attention_state"`
	AIState        string `json:"ai_state,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Topic          string `json:"topic,omitempty"`
	Target         string `json:"target"`
	ShouldQueue    bool   `json:"should_queue"`
}

type notifyLiveRow struct {
	State       string               `json:"state"`
	ID          string               `json:"id,omitempty"`
	Target      string               `json:"target"`
	Text        string               `json:"text,omitempty"`
	Explanation string               `json:"explanation"`
	Queue       *notify.Notification `json:"queue,omitempty"`
	Live        *notifyLivePane      `json:"live,omitempty"`
}

func (c *notifyCommand) buildNotifyLiveReport(entries []notify.Notification) (notifyLiveReport, error) {
	report := notifyLiveReport{
		Queue:  nonNilNotifications(entries),
		Live:   []notifyLivePane{},
		Rows:   []notifyLiveRow{},
		Errors: []string{},
	}

	panes, err := c.listNotifyLivePanes()
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
		for _, entry := range entries {
			report.Rows = append(report.Rows, notifyLiveQueueOnlyRow(entry, "live-unavailable", "live tmux pane state could not be read; queue entry remains pending"))
		}
		return report, nil
	}

	liveByID := make(map[string]notifyLivePane, len(panes))
	for _, live := range panes {
		report.Live = append(report.Live, live)
		if live.ShouldQueue {
			liveByID[live.ID] = live
		}
	}

	queueByID := make(map[string]notify.Notification, len(entries))
	for _, entry := range entries {
		queueByID[entry.ID] = entry
	}

	for _, live := range report.Live {
		liveCopy := live
		if !live.ShouldQueue {
			state := "live-title-attention"
			explanation := "live title attention badge exists but pane is not reply+agent state; title-only/manual attention does not create notify queue entries"
			if live.AttentionState == attentionStateReply {
				state = "live-manual-reply"
				explanation = "live reply badge exists but no AI agent metadata is set; manual attention panes do not create notify queue entries"
			}
			report.Rows = append(report.Rows, notifyLiveRow{
				State:       state,
				ID:          live.ID,
				Target:      live.Target,
				Explanation: explanation,
				Live:        &liveCopy,
			})
			continue
		}
		if entry, ok := queueByID[live.ID]; ok {
			entryCopy := entry
			report.Rows = append(report.Rows, notifyLiveRow{
				State:       "live-ai-reply-queued",
				ID:          live.ID,
				Target:      live.Target,
				Text:        entry.Text,
				Explanation: "live AI reply pane has a matching actionable notify queue entry",
				Queue:       &entryCopy,
				Live:        &liveCopy,
			})
			continue
		}
		report.Rows = append(report.Rows, notifyLiveRow{
			State:       "live-ai-reply-missing-queue",
			ID:          live.ID,
			Target:      live.Target,
			Explanation: "live AI reply pane has no matching queue entry; run `projmux notify reconcile` to back-fill it",
			Live:        &liveCopy,
		})
	}

	for _, entry := range entries {
		if _, ok := liveByID[entry.ID]; ok {
			continue
		}
		state := "queue-only"
		explanation := "queue entry is pending; no matching live AI reply pane was found"
		if strings.HasPrefix(entry.ID, "ai:") {
			state = "queue-stale"
			explanation = "queue entry exists but live pane no longer matches reply+agent state; it remains pending until explicit ack"
		}
		report.Rows = append(report.Rows, notifyLiveQueueOnlyRow(entry, state, explanation))
	}

	return report, nil
}

func (c *notifyCommand) listNotifyLivePanes() ([]notifyLivePane, error) {
	rows, err := (&attentionCommand{runner: c.runner}).listAttentionPanes()
	if err != nil {
		return nil, err
	}

	out := make([]notifyLivePane, 0, len(rows))
	for _, row := range rows {
		live := notifyLivePane{
			ID:             buildAttentionNotifyID(row.Session, row.Pane),
			Session:        row.Session,
			Window:         row.Window,
			Pane:           row.Pane,
			Socket:         row.Socket,
			Title:          row.Title,
			AttentionState: row.AttentionState,
			AIState:        row.AIState,
			Agent:          row.Agent,
			Topic:          row.Topic,
			Target: notify.FormatTarget(notify.Target{
				Session: row.Session,
				Window:  row.Window,
				Pane:    row.Pane,
			}),
			ShouldQueue: row.AttentionState == attentionStateReply && strings.TrimSpace(row.Agent) != "",
		}
		if live.AttentionState == attentionStateReply || hasAttentionPrefix(live.Title) || hasBraillePrefix(live.Title) {
			out = append(out, live)
		}
	}
	return out, nil
}

func nonNilNotifications(entries []notify.Notification) []notify.Notification {
	if entries == nil {
		return []notify.Notification{}
	}
	return entries
}

func notifyLiveQueueOnlyRow(entry notify.Notification, state, explanation string) notifyLiveRow {
	entryCopy := entry
	return notifyLiveRow{
		State: state,
		ID:    entry.ID,
		Target: notify.FormatTarget(notify.Target{
			Socket:  entry.Socket,
			Session: entry.Session,
			Window:  entry.Window,
			Pane:    entry.Pane,
		}),
		Text:        entry.Text,
		Explanation: explanation,
		Queue:       &entryCopy,
	}
}

func writeNotifyLiveTable(w io.Writer, report notifyLiveReport, now time.Time) error {
	if err := writeNotifyTable(w, report.Queue, now); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, ""); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "STATE\tTARGET\tID\tEXPLANATION\tTEXT"); err != nil {
		return err
	}
	for _, row := range report.Rows {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			notifyTableCell(row.State),
			notifyTableCell(row.Target),
			notifyTableCell(row.ID),
			notifyTableCell(row.Explanation),
			notifyTableCell(row.Text),
		); err != nil {
			return err
		}
	}
	for _, e := range report.Errors {
		if _, err := fmt.Fprintf(w, "live error\t-\t-\t%s\t-\n", notifyTableCell(e)); err != nil {
			return err
		}
	}
	return nil
}

func notifyTableCell(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

// formatAge renders a duration as a short age string (e.g. "12s", "5m").
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// writeJSON encodes payload as a single newline-terminated json document.
func writeJSON(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// isInvalidInputErr reports whether the supplied store error stems from input
// validation rather than IO. Validation errors should map to exit code 2.
func isInvalidInputErr(err error) bool {
	switch {
	case errors.Is(err, notify.ErrInvalidSeverity),
		errors.Is(err, notify.ErrInvalidSource),
		errors.Is(err, notify.ErrInvalidTarget),
		errors.Is(err, notify.ErrInvalidText),
		errors.Is(err, notify.ErrInvalidTTL):
		return true
	}
	return false
}

// multiFlag collects repeated string flag values (e.g. --severity warn
// --severity critical).
type multiFlag []string

func (m *multiFlag) String() string {
	if m == nil {
		return ""
	}
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	*m = append(*m, value)
	return nil
}

func printNotifyUsage(w io.Writer) {
	fmt.Fprintln(w, "Pending AI notify queue. Attention is live pane state; notify rows remain until explicit ack.")
	fmt.Fprintln(w, "Use `notify list --live` to explain queue/live drift and `notify reconcile` to repair AI reply entries.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux notify push  --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]")
	fmt.Fprintln(w, "                        [--severity info|warn|critical] [--source ai|k8s|git|external]")
	fmt.Fprintln(w, "                        [--ttl <seconds>] [--id <s>] [--json]")
	fmt.Fprintln(w, "  projmux notify list  [--live] [--json] [--limit N] [--ui table|sidebar] [--severity ...] [--source ...]")
	fmt.Fprintln(w, "  projmux notify ack   <id> | --all")
	fmt.Fprintln(w, "  projmux notify reconcile [--json]")
}

func printNotifyListUsage(w io.Writer) {
	fmt.Fprintln(w, "Pending AI notify queue entries only; rows remain until explicit ack.")
	fmt.Fprintln(w, "Use `--live` to explain queue entries against live pane attention state without mutating either surface.")
	fmt.Fprintln(w, "Use `--ui=sidebar` for the interactive right-side notify list.")
	fmt.Fprintln(w, "Use `projmux attention list` for live pane attention state only.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux notify list [--live] [--json] [--limit N] [--ui table|sidebar] [--severity ...] [--source ...]")
}

func printNotifyReconcileUsage(w io.Writer) {
	fmt.Fprintln(w, "Repair the pending AI notify queue from live tmux pane attention state.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux notify reconcile [--json]")
}
