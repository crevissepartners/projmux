package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/recentwindows"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

const (
	recentWindowFieldSep        = "\x1f"
	recentWindowEscapedFieldSep = "\\037"
)

type recentWindowStore interface {
	Candidates(current recentwindows.WindowKey, live []recentwindows.LiveWindow, limit int) ([]recentwindows.Candidate, error)
	Record(snapshot recentwindows.Snapshot, limit int) (recentwindows.State, error)
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
	case "record":
		return c.recent.RunRecord(fs.Args()[1:], stdout, stderr)
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
	fmt.Fprintln(w, "  record  Record the active tmux window in the recent queue")
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
	if selected.IsCurrent {
		// Staying on the current window is a no-op / stay-current: the picker
		// closes and tmux keeps the user where they already are. Never a switch
		// or an error.
		return nil
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

func (c *recentWindowCommand) RunRecord(args []string, _ io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("window record", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printWindowRecordUsage(stderr)
	}
	if err := fs.Parse(args); err != nil {
		printWindowRecordUsage(stderr)
		return err
	}
	if fs.NArg() != 0 {
		printWindowRecordUsage(stderr)
		return fmt.Errorf("window record does not accept positional arguments")
	}

	ctx := context.Background()
	snapshot, err := c.currentSnapshot(ctx)
	if err != nil {
		return err
	}
	store, err := c.recentStore(snapshot.Socket)
	if err != nil {
		return err
	}
	if _, err := store.Record(snapshot, recentwindows.DefaultLimit); err != nil {
		return fmt.Errorf("record recent window: %w", err)
	}
	return nil
}

func printWindowRecordUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: projmux window record")
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

func (c *recentWindowCommand) currentSnapshot(ctx context.Context) (recentwindows.Snapshot, error) {
	if c.runner == nil {
		return recentwindows.Snapshot{}, fmt.Errorf("tmux runner is not configured")
	}
	output, err := c.runner.Run(ctx, "tmux", "display-message", "-p", "-F", strings.Join([]string{
		"#{socket_path}",
		"#{session_name}",
		"#{window_id}",
		"#{window_name}",
		"#{pane_id}",
		"#{pane_title}",
		"#{@projmux_ai_topic}",
		"#{pane_current_command}",
		"#{pane_current_path}",
	}, recentWindowFieldSep))
	if err != nil {
		return recentwindows.Snapshot{}, fmt.Errorf("snapshot current tmux window: %w", err)
	}
	fields := parseRecentWindowFields(output, 9)
	if len(fields) != 9 || fields[1] == "" || fields[2] == "" {
		return recentwindows.Snapshot{}, fmt.Errorf("snapshot current tmux window: tmux returned incomplete metadata")
	}
	return recentwindows.Snapshot{
		Socket:        fields[0],
		Session:       fields[1],
		WindowID:      fields[2],
		WindowName:    fields[3],
		Project:       recentWindowProjectName(fields[8]),
		LastPaneID:    fields[4],
		LastPaneTitle: fields[5],
		PaneTitles:    c.windowPaneTitles(ctx, fields[2]),
		LastPaneTopic: fields[6],
		LastCommand:   fields[7],
		LastFocusedAt: c.currentTime().UTC(),
	}, nil
}

// windowPaneTitles collects the pane titles of every pane in the given window
// so the picker can render a multi-pane summary. Failures degrade gracefully:
// an empty result falls back to LastPaneTitle at display time.
func (c *recentWindowCommand) windowPaneTitles(ctx context.Context, windowID string) []string {
	if c.runner == nil || strings.TrimSpace(windowID) == "" {
		return nil
	}
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-t", windowID, "-F", "#{pane_title}")
	if err != nil {
		return nil
	}
	rows := parseRecentWindowRows(output, 1)
	titles := make([]string, 0, len(rows))
	for _, fields := range rows {
		if title := strings.TrimSpace(fields[0]); title != "" {
			titles = append(titles, title)
		}
	}
	if len(titles) == 0 {
		return nil
	}
	return titles
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

const (
	recentWindowPaneSummaryMaxRunes = 80
	// Per-component construction-time budgets keep the readable window name and
	// the age badge on line 1 even when project or window names are very long.
	// TruncateANSI trims at render time for the real terminal width; these
	// budgets only guarantee the high-priority fields are never pushed out by a
	// single overlong component before truncation runs.
	recentWindowProjectBadgeMaxRunes = 24
	recentWindowNameMaxRunes         = 48
	recentWindowTopicChipMaxRunes    = 48
)

func recentWindowPickerItem(candidate recentwindows.Candidate, now time.Time) intpicker.Item {
	// The readable window name (never a raw @window_id/%pane_id). BuildLabel
	// already falls back name -> project -> session -> pane/topic/cmd.
	name := strings.TrimSpace(candidate.Label.Primary)
	if name == "" {
		name = recentWindowTargetLabel(candidate)
	}
	name = recentWindowTruncate(name, recentWindowNameMaxRunes)

	age := recentWindowAge(candidate.LastFocusedAt, now)
	lastVisit := recentWindowLastVisit(age, candidate.LastFocusedAt)

	// Line 1: project badge -> readable window name -> last-focus age badge.
	// Reuse the notify sidebar helpers so the badges match the notification
	// sidebar palette and each badge terminates with theme.ANSIReset (so the
	// selected-row background re-applies cleanly after every badge).
	badgeText := recentWindowBadgeText(candidate)
	title := notifySidebarProjectBadge(badgeText) + " " + name + " " + notifySidebarAge(age)
	if candidate.IsCurrent {
		title = recentWindowCurrentBadge() + " " + title
	}

	// Line 2: the window's pane topic/title summary (topic leads).
	paneSummary := recentWindowPaneSummaryLine(candidate)

	// Display-only context badge (project/session). Never a recency signal.
	context := joinRecentWindowParts(candidate.Project, candidate.Session)

	meta := make([]string, 0, 3)
	if paneSummary != "" {
		meta = append(meta, paneSummary)
	}
	if lastVisit != "" {
		meta = append(meta, lastVisit)
	}
	if context != "" && context != name {
		meta = append(meta, notifySidebarDim(context))
	}

	searchTail := []string{
		candidate.LastPaneTopic,
		candidate.LastCommand,
		age,
		recentWindowFocusDate(candidate.LastFocusedAt),
		candidate.Label.Secondary,
	}
	if candidate.IsCurrent {
		searchTail = append(searchTail, recentWindowCurrentBadgeLabel)
	}
	search := joinRecentWindowParts(append([]string{
		candidate.WindowName,
		candidate.Project,
		candidate.Session,
	}, append(recentWindowPaneTitles(candidate), searchTail...)...)...)

	return intpicker.Item{
		Title:      title,
		MetaLines:  meta,
		SearchText: search,
	}
}

// recentWindowBadgeText picks the line-1 badge text: real project context when
// available, else the session, so we show meaningful context instead of an
// empty "project" placeholder. notifySidebarProjectBadge supplies the default
// only when both are truly empty.
func recentWindowBadgeText(candidate recentwindows.Candidate) string {
	badge := strings.TrimSpace(candidate.Project)
	if badge == "" {
		badge = strings.TrimSpace(candidate.Session)
	}
	return recentWindowTruncate(badge, recentWindowProjectBadgeMaxRunes)
}

// recentWindowPaneTitles returns the pane-title list, falling back to the
// single active pane title for snapshots recorded before PaneTitles existed.
func recentWindowPaneTitles(candidate recentwindows.Candidate) []string {
	titles := make([]string, 0, len(candidate.PaneTitles))
	for _, title := range candidate.PaneTitles {
		if title = strings.TrimSpace(title); title != "" {
			titles = append(titles, title)
		}
	}
	if len(titles) == 0 {
		if title := strings.TrimSpace(candidate.LastPaneTitle); title != "" {
			titles = append(titles, title)
		}
	}
	return titles
}

// recentWindowPaneSummary joins the window's pane topic/title summary with
// " | " and stably truncates the plain result so it never shifts layout. Pane
// topic leads (the user's work context) when present; otherwise pane titles,
// falling back to LastPaneTitle then LastCommand via recentWindowPaneTitles.
func recentWindowPaneSummary(candidate recentwindows.Candidate) string {
	parts := recentWindowPaneSummaryParts(candidate)
	if len(parts) == 0 {
		return ""
	}
	return recentWindowTruncate(strings.Join(parts, " | "), recentWindowPaneSummaryMaxRunes)
}

// recentWindowPaneSummaryParts returns the ordered topic/title parts for the
// pane summary, with the pane topic leading when set.
func recentWindowPaneSummaryParts(candidate recentwindows.Candidate) []string {
	titles := recentWindowPaneTitles(candidate)
	topic := strings.TrimSpace(candidate.LastPaneTopic)
	if topic == "" {
		if len(titles) > 0 {
			return titles
		}
		if cmd := strings.TrimSpace(candidate.LastCommand); cmd != "" {
			return []string{cmd}
		}
		return nil
	}
	// Topic leads; append the remaining titles (excluding any that duplicate the
	// topic) so the user's work context shows first.
	parts := make([]string, 0, len(titles)+1)
	parts = append(parts, topic)
	for _, title := range titles {
		if title != topic {
			parts = append(parts, title)
		}
	}
	return parts
}

// recentWindowPaneSummaryLine renders the line-2 pane summary with notify-level
// visibility: the leading topic is wrapped in an active chip (like
// notifySidebarTopicBadge) and the remaining pane titles are dimmed so they
// read as secondary context rather than blending into plain meta text.
func recentWindowPaneSummaryLine(candidate recentwindows.Candidate) string {
	plain := recentWindowPaneSummary(candidate)
	if plain == "" {
		return ""
	}
	parts := strings.SplitN(plain, " | ", 2)
	lead := parts[0]
	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}
	if strings.TrimSpace(candidate.LastPaneTopic) != "" {
		chip := recentWindowTopicChip(lead)
		if rest != "" {
			return chip + " " + notifySidebarDim(rest)
		}
		return chip
	}
	return notifySidebarDim(plain)
}

// recentWindowTopicChip wraps the pane topic in the shared active chip palette,
// terminating with theme.ANSIReset like notifySidebarTopicBadge.
func recentWindowTopicChip(topic string) string {
	topic = recentWindowTruncate(strings.TrimSpace(topic), recentWindowTopicChipMaxRunes)
	if topic == "" {
		return ""
	}
	return theme.ANSIChipActiveStart + " " + topic + " " + theme.ANSIReset
}

const recentWindowCurrentBadgeLabel = "CURRENT"

// recentWindowCurrentBadge marks the row the user is already on. It uses the
// notify info palette (bold dark text on a bright background) so it reads as a
// distinct "you are here" marker, terminating with theme.ANSIReset like the
// other badges so the selected-row background re-applies cleanly.
func recentWindowCurrentBadge() string {
	return theme.ANSINotifyInfoStart + " " + recentWindowCurrentBadgeLabel + " " + theme.ANSIReset
}

// recentWindowLastVisit labels the relative age so the row reads unambiguously
// as a last-visit time rather than an Alt-1 sidebar entry.
func recentWindowLastVisit(age string, focused time.Time) string {
	age = strings.TrimSpace(age)
	if age == "" {
		return ""
	}
	if date := recentWindowFocusDate(focused); date != "" {
		return "last visit · " + age + " · " + date
	}
	return "last visit · " + age
}

func recentWindowFocusDate(focused time.Time) string {
	if focused.IsZero() {
		return ""
	}
	return focused.UTC().Format("2006-01-02 15:04")
}

// recentWindowTruncate shortens value to at most maxRunes runes (rune-aware),
// appending a single-rune ellipsis when truncation occurs. Distinct from the
// package's plain truncateRunes (which hard-cuts without an ellipsis).
func recentWindowTruncate(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
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

func recentWindowProjectName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	root := nearestProjectMarker(path, os.TempDir())
	if root == "" {
		root = path
	}
	name := filepath.Base(filepath.Clean(root))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
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
