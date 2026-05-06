package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
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
	store    notifyStore
	storeErr error
	now      func() time.Time
	runner   tmuxRunner
}

func newNotifyCommand() *notifyCommand {
	cmd := &notifyCommand{now: time.Now, runner: reconcileDefaultRunner()}
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

	var (
		asJSON     = fs.Bool("json", false, "emit json instead of tabular output")
		limit      = fs.Int("limit", 0, "limit number of returned entries (0 = no limit)")
		severities multiFlag
		sources    multiFlag
	)
	fs.Var(&severities, "severity", "filter by severity (repeatable)")
	fs.Var(&sources, "source", "filter by source (repeatable)")

	if err := fs.Parse(args); err != nil {
		return usageError(fmt.Sprintf("parse notify list flags: %v", err))
	}
	if fs.NArg() != 0 {
		printNotifyUsage(stderr)
		return usageError("notify list does not accept positional arguments")
	}
	if *limit < 0 {
		return usageError("notify list --limit must be >= 0")
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

	if *asJSON {
		if entries == nil {
			entries = []notify.Notification{}
		}
		return writeJSON(stdout, entries)
	}
	return writeNotifyTable(stdout, entries, c.clock())
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
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux notify push  --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]")
	fmt.Fprintln(w, "                        [--severity info|warn|critical] [--source ai|k8s|git|external]")
	fmt.Fprintln(w, "                        [--ttl <seconds>] [--id <s>] [--json]")
	fmt.Fprintln(w, "  projmux notify list  [--json] [--limit N] [--severity ...] [--source ...]")
	fmt.Fprintln(w, "  projmux notify ack   <id> [--all]")
	fmt.Fprintln(w, "  projmux notify reconcile [--json]")
}
