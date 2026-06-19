package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/recentwindows"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

const (
	recentWindowFieldSep        = "\x1f"
	recentWindowEscapedFieldSep = "\\037"
)

type recentWindowStore interface {
	Candidates(current recentwindows.WindowKey, live []recentwindows.LiveWindow, limit int) ([]recentwindows.Candidate, error)
}

type recentWindowStoreFactory func(socket string) (recentWindowStore, error)

type recentWindowOpener interface {
	OpenSessionTarget(ctx context.Context, sessionName, windowIndex, paneIndex string) error
}

type windowCommand struct {
	recent *recentWindowCommand
}

func newWindowCommand() *windowCommand {
	return &windowCommand{recent: newRecentWindowCommand()}
}

func (c *windowCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("window", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printWindowUsage(stderr)
	}
	if err := fs.Parse(args); err != nil {
		printWindowUsage(stderr)
		return err
	}
	if fs.NArg() == 0 {
		printWindowUsage(stderr)
		return errors.New("window requires a subcommand")
	}

	switch fs.Arg(0) {
	case "recent":
		return c.recent.Run(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printWindowUsage(stdout)
		return nil
	default:
		printWindowUsage(stderr)
		return fmt.Errorf("unknown window subcommand: %s", fs.Arg(0))
	}
}

func printWindowUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: projmux window <command>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  recent  Pick a recent tmux window across projects")
}

type recentWindowCommand struct {
	runner       tmuxRunner
	opener       recentWindowOpener
	storeFactory recentWindowStoreFactory
	nativePicker intpicker.Runner
	lookupEnv    func(string) string
	now          func() time.Time
}

func newRecentWindowCommand() *recentWindowCommand {
	client := defaultTmuxClient()
	return &recentWindowCommand{
		runner:       inttmux.ExecRunner{},
		opener:       client,
		storeFactory: defaultRecentWindowStore,
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		lookupEnv:    os.Getenv,
		now:          time.Now,
	}
}

func defaultRecentWindowStore(socket string) (recentWindowStore, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, err
	}
	return recentwindows.NewDefaultStore(paths, socket), nil
}

func (c *recentWindowCommand) Run(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("window recent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printWindowRecentUsage(stderr)
	}
	if err := fs.Parse(args); err != nil {
		printWindowRecentUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printWindowRecentUsage(stderr)
		return fmt.Errorf("window recent does not accept positional arguments")
	}

	ctx := context.Background()
	current, err := c.currentWindow(ctx)
	if err != nil {
		return err
	}
	live, err := c.liveWindows(ctx, current.Socket)
	if err != nil {
		return fmt.Errorf("load recent window inventory: %w", err)
	}
	store, err := c.recentStore(current.Socket)
	if err != nil {
		return err
	}
	candidates, err := store.Candidates(current, live, recentwindows.DefaultLimit)
	if err != nil {
		return fmt.Errorf("load recent windows: %w", err)
	}
	if len(candidates) == 0 {
		c.displayMessage(ctx, stderr, "no recent windows")
		return nil
	}
	if c.nativePicker == nil {
		return fmt.Errorf("native picker is not configured")
	}

	items, byValue := recentWindowPickerItems(candidates, c.currentTime())
	result, err := c.nativePicker.Run(recentWindowPickerOptions(items))
	if err != nil {
		return fmt.Errorf("run recent windows picker: %w", err)
	}
	value := strings.TrimSpace(result.Value)
	if value == "" {
		return nil
	}
	selected, ok := byValue[value]
	if !ok {
		return fmt.Errorf("recent window selection is not recognized: %s", value)
	}
	if err := c.openRecentWindow(ctx, selected); err != nil {
		c.displayMessage(ctx, stderr, "recent window unavailable: "+recentWindowTargetLabel(selected))
		c.refreshCandidates(ctx, current, store)
		return nil
	}
	return nil
}

func printWindowRecentUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: projmux window recent")
}

func (c *recentWindowCommand) recentStore(socket string) (recentWindowStore, error) {
	if c.storeFactory == nil {
		return nil, fmt.Errorf("recent window store is not configured")
	}
	store, err := c.storeFactory(socket)
	if err != nil {
		return nil, fmt.Errorf("configure recent window store: %w", err)
	}
	if store == nil {
		return nil, fmt.Errorf("recent window store is not configured")
	}
	return store, nil
}

func (c *recentWindowCommand) currentWindow(ctx context.Context) (recentwindows.WindowKey, error) {
	if c.runner == nil {
		return recentwindows.WindowKey{}, fmt.Errorf("tmux runner is not configured")
	}
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-F", strings.Join([]string{"#{socket_path}", "#{session_name}", "#{window_id}"}, recentWindowFieldSep))
	if err != nil {
		return recentwindows.WindowKey{}, fmt.Errorf("resolve current tmux window: %w", err)
	}
	fields := parseRecentWindowFields(output, 3)
	if len(fields) != 3 || fields[1] == "" || fields[2] == "" {
		return recentwindows.WindowKey{}, fmt.Errorf("resolve current tmux window: tmux returned incomplete metadata")
	}
	return recentwindows.WindowKey{Socket: fields[0], Session: fields[1], WindowID: fields[2]}, nil
}

func (c *recentWindowCommand) liveWindows(ctx context.Context, socket string) ([]recentwindows.LiveWindow, error) {
	if c.runner == nil {
		return nil, fmt.Errorf("tmux runner is not configured")
	}
	output, err := c.runner.Run(ctx, "tmux", "list-windows", "-a", "-F", strings.Join([]string{"#{session_name}", "#{window_id}"}, recentWindowFieldSep))
	if err != nil {
		return nil, err
	}
	rows := parseRecentWindowRows(output, 2)
	windows := make([]recentwindows.LiveWindow, 0, len(rows))
	for _, fields := range rows {
		if fields[0] == "" || fields[1] == "" {
			continue
		}
		windows = append(windows, recentwindows.LiveWindow{
			Socket:   socket,
			Session:  fields[0],
			WindowID: fields[1],
		})
	}
	return windows, nil
}

func (c *recentWindowCommand) openRecentWindow(ctx context.Context, selected recentwindows.Candidate) error {
	if c.opener == nil {
		return fmt.Errorf("recent window opener is not configured")
	}
	return c.opener.OpenSessionTarget(ctx, selected.Session, selected.WindowID, "")
}

func (c *recentWindowCommand) refreshCandidates(ctx context.Context, current recentwindows.WindowKey, store recentWindowStore) {
	live, err := c.liveWindows(ctx, current.Socket)
	if err != nil {
		return
	}
	_, _ = store.Candidates(current, live, recentwindows.DefaultLimit)
}

func (c *recentWindowCommand) displayMessage(ctx context.Context, stderr io.Writer, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if c.runner != nil {
		if _, err := c.runner.Run(ctx, "tmux", "display-message", message); err == nil {
			return
		}
	}
	if stderr != nil {
		fmt.Fprintln(stderr, message)
	}
}

func (c *recentWindowCommand) currentTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func recentWindowPickerOptions(items []intpicker.Item) intpicker.Options {
	options := intpicker.Options{
		UI:        "recent-windows",
		Title:     "Recent Windows",
		Prompt:    "› ",
		Items:     items,
		MultiLine: true,
		Actions:   pickerCloseActionsForToggles(nil, nil, []string{"ProjectSidebarToggle", "NotifySidebarToggle", "RecentWindows:Open", "SessionPopupToggle"}, "esc", "ctrl-n", "alt-1", "alt-2", "alt-3"),
	}
	options = fallbackRenderThemeSource().pickerOptions(options)
	return options
}

func recentWindowPickerItems(candidates []recentwindows.Candidate, now time.Time) ([]intpicker.Item, map[string]recentwindows.Candidate) {
	items := make([]intpicker.Item, 0, len(candidates))
	byValue := make(map[string]recentwindows.Candidate, len(candidates))
	for _, candidate := range candidates {
		value := recentWindowValue(candidate)
		if value == "" {
			continue
		}
		item := recentWindowPickerItem(candidate, now)
		item.Value = value
		items = append(items, item)
		byValue[value] = candidate
	}
	return items, byValue
}

func recentWindowPickerItem(candidate recentwindows.Candidate, now time.Time) intpicker.Item {
	title := strings.TrimSpace(candidate.Label.Primary)
	if title == "" {
		title = recentWindowTargetLabel(candidate)
	}
	age := recentWindowAge(candidate.LastFocusedAt, now)
	context := joinRecentWindowParts(candidate.Project, candidate.Session)
	activity := joinRecentWindowParts(candidate.LastPaneTitle, candidate.LastPaneTopic, candidate.LastCommand, age)
	meta := make([]string, 0, 2)
	if activity != "" {
		meta = append(meta, activity)
	}
	if context != "" && context != title {
		meta = append(meta, context)
	}
	search := joinRecentWindowParts(
		candidate.WindowName,
		candidate.Project,
		candidate.Session,
		candidate.LastPaneTitle,
		candidate.LastPaneTopic,
		candidate.LastCommand,
		age,
		candidate.Label.Secondary,
	)
	return intpicker.Item{
		Title:      title,
		MetaLines:  meta,
		SearchText: search,
	}
}

func recentWindowValue(candidate recentwindows.Candidate) string {
	session := strings.TrimSpace(candidate.Session)
	windowID := strings.TrimSpace(candidate.WindowID)
	if session == "" || windowID == "" {
		return ""
	}
	return session + recentWindowFieldSep + windowID
}

func recentWindowTargetLabel(candidate recentwindows.Candidate) string {
	primary := strings.TrimSpace(candidate.Label.Primary)
	context := joinRecentWindowParts(candidate.Project, candidate.Session)
	if primary != "" && context != "" && primary != context {
		return primary + " (" + context + ")"
	}
	if primary != "" {
		return primary
	}
	return joinRecentWindowParts(candidate.Project, candidate.Session, candidate.WindowName)
}

func recentWindowAge(then, now time.Time) string {
	if then.IsZero() || now.IsZero() {
		return ""
	}
	if now.Before(then) {
		now = then
	}
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		seconds := max(int(d.Seconds()), 1)
		return fmt.Sprintf("%ds ago", seconds)
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func joinRecentWindowParts(values ...string) string {
	parts := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		parts = append(parts, value)
	}
	return strings.Join(parts, " · ")
}

func parseRecentWindowFields(output []byte, count int) []string {
	rows := parseRecentWindowRows(output, count)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func parseRecentWindowRows(output []byte, count int) [][]string {
	if count <= 0 {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := splitRecentWindowFields(line, count)
		if len(fields) < count {
			continue
		}
		if len(fields) > count {
			fields = fields[:count]
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		rows = append(rows, fields)
	}
	return rows
}

func splitRecentWindowFields(line string, count int) []string {
	for _, sep := range []string{recentWindowFieldSep, recentWindowEscapedFieldSep, "\t"} {
		fields := strings.SplitN(line, sep, count)
		if len(fields) == count || len(fields) > 1 {
			return fields
		}
	}
	return []string{line}
}
