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
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/core/projectidentity"
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
	homeDir      func() (string, error)
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
		homeDir:      os.UserHomeDir,
		now:          time.Now,
	}
}

// recentWindowAIBadgeStyle resolves the configured AI badge glyph style for the
// picker, mirroring how switch.go reads it. It falls back to the dot style when
// the home dir resolver is unset (e.g. in tests that do not exercise badges).
func (c *recentWindowCommand) recentWindowAIBadgeStyle() string {
	if c.homeDir == nil {
		return aibadge.StyleDot
	}
	return aibadge.NormalizeStyle(string(loadAIBadgeStyle(c.homeDir, c.lookupEnv)))
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
	defer applyNativeUIThemeFromConfig(c.homeDir, c.lookupEnv, "")()

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

	items, byValue, initialIndex := recentWindowPickerItems(candidates, c.currentTime(), c.recentWindowAIBadgeStyle())
	result, err := c.nativePicker.Run(recentWindowPickerOptions(items, initialIndex, c.homeDir, c.lookupEnv))
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
		"#{@projmux_project_path}",
	}, recentWindowFieldSep))
	if err != nil {
		return recentwindows.Snapshot{}, fmt.Errorf("snapshot current tmux window: %w", err)
	}
	fields := parseRecentWindowFields(output, 10)
	if len(fields) != 10 || fields[1] == "" || fields[2] == "" {
		return recentwindows.Snapshot{}, fmt.Errorf("snapshot current tmux window: tmux returned incomplete metadata")
	}
	titles, badgeKinds, topics, commands := c.windowPaneMetadata(ctx, fields[2])
	return recentwindows.Snapshot{
		Socket:     fields[0],
		Session:    fields[1],
		WindowID:   fields[2],
		WindowName: fields[3],
		Project: resolveProjectDisplayName(projectidentity.Inputs{
			AnchorPath:  fields[9],
			PaneCWD:     fields[8],
			SessionName: fields[1],
		}, projectidentity.OSFS),
		LastPaneID:     fields[4],
		LastPaneTitle:  fields[5],
		PaneTitles:     titles,
		PaneBadgeKinds: badgeKinds,
		PaneTopics:     topics,
		PaneCommands:   commands,
		LastPaneTopic:  fields[6],
		LastCommand:    fields[7],
		LastFocusedAt:  c.currentTime().UTC(),
	}, nil
}

// windowPaneMetadata collects every pane's title, AI badge kind, AI topic, and
// current command for the given window so the picker can mirror the app
// pane-border visible label rule. The returned slices are positionally aligned.
// Failures degrade gracefully: an empty result falls back to LastPaneTitle at
// display time, missing badge kinds render no status glyph, and missing
// topics/commands fall back to the pane title.
func (c *recentWindowCommand) windowPaneMetadata(ctx context.Context, windowID string) (titles, badgeKinds, topics, commands []string) {
	if c.runner == nil || strings.TrimSpace(windowID) == "" {
		return nil, nil, nil, nil
	}
	format := strings.Join([]string{"#{pane_title}", "#{@projmux_ai_badge_kind}", "#{@projmux_ai_topic}", "#{pane_current_command}"}, recentWindowFieldSep)
	output, err := c.runner.Run(ctx, "tmux", "list-panes", "-t", windowID, "-F", format)
	if err != nil {
		return nil, nil, nil, nil
	}
	rows := parseRecentWindowRows(output, 4)
	titles = make([]string, 0, len(rows))
	badgeKinds = make([]string, 0, len(rows))
	topics = make([]string, 0, len(rows))
	commands = make([]string, 0, len(rows))
	for _, fields := range rows {
		title := strings.TrimSpace(fields[0])
		kind := strings.TrimSpace(fields[1])
		topic := strings.TrimSpace(fields[2])
		command := strings.TrimSpace(fields[3])
		if title == "" {
			continue
		}
		titles = append(titles, title)
		badgeKinds = append(badgeKinds, kind)
		topics = append(topics, topic)
		commands = append(commands, command)
	}
	if len(titles) == 0 {
		return nil, nil, nil, nil
	}
	return titles, badgeKinds, topics, commands
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

// recentWindowPickerOptions builds the picker options for the recent-windows UI.
// initialIndex lands the cursor on the first non-current row (the most recent
// switch target) instead of the current-window row; a negative index means there
// is no non-current row (current-only state), so the cursor stays on index 0.
func recentWindowPickerOptions(items []intpicker.Item, initialIndex int, homeDir func() (string, error), lookupEnv func(string) string) intpicker.Options {
	options := intpicker.Options{
		UI:        "recent-windows",
		Title:     "Recent Windows",
		Prompt:    "› ",
		Items:     items,
		MultiLine: true,
		Actions:   pickerCloseActionsForPopupToggleMode(homeDir, lookupEnv, "recent-windows", "esc", "ctrl-n"),
	}
	if initialIndex > 0 {
		options.InitialIndex = initialIndex
		options.InitialIndexSet = true
	}
	if source, err := configRenderThemeSource(homeDir, lookupEnv, ""); err == nil {
		options = source.pickerOptions(options)
	} else {
		options = fallbackRenderThemeSource().pickerOptions(options)
	}
	return options
}

// recentWindowPickerItems builds the picker items in display (MRU) order and
// returns the index of the first non-current row in that final item order so the
// cursor can default to the most recent switch target rather than the
// current-window row. The index is computed against the FINAL items slice (rows
// with an empty value are skipped), and is -1 when every row is the current
// window (current-only state) so the caller leaves the cursor on index 0.
func recentWindowPickerItems(candidates []recentwindows.Candidate, now time.Time, badgeStyle string) ([]intpicker.Item, map[string]recentwindows.Candidate, int) {
	items := make([]intpicker.Item, 0, len(candidates))
	byValue := make(map[string]recentwindows.Candidate, len(candidates))
	firstNonCurrent := -1
	for _, candidate := range candidates {
		value := recentWindowValue(candidate)
		if value == "" {
			continue
		}
		if firstNonCurrent < 0 && !candidate.IsCurrent {
			firstNonCurrent = len(items)
		}
		item := recentWindowPickerItem(candidate, now, badgeStyle)
		item.Value = value
		items = append(items, item)
		byValue[value] = candidate
	}
	return items, byValue, firstNonCurrent
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
	// recentWindowMaxPanes caps how many pane titles render on line 2 before the
	// remainder collapses into a compact "+N" count (the Alt-1 sidebar pattern).
	recentWindowMaxPanes = 4
)

func recentWindowPickerItem(candidate recentwindows.Candidate, now time.Time, badgeStyle string) intpicker.Item {
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
	// selected-row background re-applies cleanly after every badge). The current
	// window stays in history as a normal card with NO CURRENT badge.
	badgeText := recentWindowBadgeText(candidate)
	title := notifySidebarProjectBadge(badgeText) + " " + name + " " + notifySidebarAge(age)

	// Line 2: the window's pane topic/title summary (topic leads, per-pane AI
	// badges). Line 3: last-visit metadata. We deliberately do NOT add a context
	// (project · session) line: the project already leads line 1 and the session
	// unique id stays in SearchText/debug only.
	paneSummary := recentWindowPaneSummaryLine(candidate, badgeStyle)

	meta := make([]string, 0, 2)
	if paneSummary != "" {
		meta = append(meta, paneSummary)
	}
	if lastVisit != "" {
		meta = append(meta, lastVisit)
	}

	searchTail := append([]string{candidate.LastPaneTopic}, candidate.PaneTopics...)
	searchTail = append(searchTail, candidate.PaneCommands...)
	searchTail = append(searchTail,
		candidate.LastCommand,
		age,
		recentWindowFocusDate(candidate.LastFocusedAt),
		candidate.Label.Secondary,
	)
	// SearchText still carries the session unique id and pane metadata even though
	// they are dropped from the visible card lines, so search stays comprehensive.
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

// recentWindowPaneSummaryParts returns the ordered perceived-title parts for the
// pane summary, one per pane in display order. Each part is a pane's perceived
// title (topic > known interactive shell command > title), matching the app
// pane-border visible label rule for stored pane metadata.
func recentWindowPaneSummaryParts(candidate recentwindows.Candidate) []string {
	cells := recentWindowPaneCells(candidate)
	parts := make([]string, 0, len(cells))
	for _, cell := range cells {
		if title := strings.TrimSpace(cell.perceivedTitle()); title != "" {
			parts = append(parts, title)
		}
	}
	return parts
}

// recentWindowPaneCell pairs a pane's perceived title with its own AI badge kind,
// AI topic, and current command so the fields never desync as the summary is
// overflow-collapsed. Each pane renders at the same hierarchy on line 2:
// "<status glyph> <perceived title>".
type recentWindowPaneCell struct {
	title   string
	kind    string
	topic   string
	command string
}

// perceivedTitle returns the pane's display title for line 2, mirroring the app
// pane-border visible label rule using captured fields: AI topic first, known
// interactive shell command second, raw pane title last.
func (cell recentWindowPaneCell) perceivedTitle() string {
	if topic := strings.TrimSpace(cell.topic); topic != "" {
		return topic
	}
	if command := strings.TrimSpace(cell.command); recentWindowKnownInteractiveShell(command) {
		return command
	}
	if title := strings.TrimSpace(cell.title); title != "" {
		return title
	}
	return strings.TrimSpace(cell.command)
}

// recentWindowPaneSummaryLine renders the line-2 pane summary as a flat list of
// every pane at the same hierarchy, joined by " | ". Each pane reads as
// "<AI status glyph> <perceived title>": an AI pane (non-empty badge kind) leads
// with its themed status glyph and shows its own AI topic; a non-AI pane shows
// its title with no glyph. Panes beyond recentWindowMaxPanes collapse into a
// compact "+N" count. The AI status glyph is the only remaining visual signal —
// no active chip, no grey dim decoration. Layout stays stable because every
// visible title is rune-truncated identically to the plain
// recentWindowPaneSummary path.
func recentWindowPaneSummaryLine(candidate recentwindows.Candidate, badgeStyle string) string {
	// recentWindowPaneSummary owns the canonical plain text (and its stable
	// truncation); bail early when there is nothing to render so the no-summary
	// path matches exactly.
	if recentWindowPaneSummary(candidate) == "" {
		return ""
	}

	cells := recentWindowPaneCells(candidate)

	visible := cells
	overflow := 0
	if len(cells) > recentWindowMaxPanes {
		visible = cells[:recentWindowMaxPanes]
		overflow = len(cells) - recentWindowMaxPanes
	}

	// Pane cells join with " | " so the visible separator survives ANSI stripping.
	rendered := make([]string, 0, len(visible)+1)
	for _, cell := range visible {
		// Truncate identically to the plain summary path so layout never shifts.
		body := recentWindowTruncate(cell.perceivedTitle(), recentWindowPaneSummaryMaxRunes)
		if glyph := recentWindowPaneKindGlyph(cell.kind, badgeStyle); glyph != "" {
			body = glyph + " " + body
		}
		rendered = append(rendered, body)
	}
	if overflow > 0 {
		rendered = append(rendered, fmt.Sprintf("+%d", overflow))
	}

	return strings.Join(rendered, " | ")
}

// recentWindowPaneCells builds the per-pane (title, badge kind, topic, command)
// units, keeping each visible-title source bound to its own status metadata
// regardless of later overflow collapse. It mirrors recentWindowPaneTitles'
// fallbacks: when no PaneTitles exist it falls back to a single LastPaneTitle
// (then LastCommand) cell with no badge kind.
func recentWindowPaneCells(candidate recentwindows.Candidate) []recentWindowPaneCell {
	kinds := candidate.PaneBadgeKinds
	topics := candidate.PaneTopics
	commands := candidate.PaneCommands
	cells := make([]recentWindowPaneCell, 0, len(candidate.PaneTitles))
	for i, title := range candidate.PaneTitles {
		title = strings.TrimSpace(title)
		kind := ""
		if i < len(kinds) {
			kind = strings.TrimSpace(kinds[i])
		}
		topic := ""
		if i < len(topics) {
			topic = strings.TrimSpace(topics[i])
		}
		command := ""
		if i < len(commands) {
			command = strings.TrimSpace(commands[i])
		}
		if title == "" && kind == "" && topic == "" && command == "" {
			continue
		}
		cells = append(cells, recentWindowPaneCell{title: title, kind: kind, topic: topic, command: command})
	}
	if len(cells) == 0 {
		if title := strings.TrimSpace(candidate.LastPaneTitle); title != "" {
			cells = append(cells, recentWindowPaneCell{title: title})
		} else if cmd := strings.TrimSpace(candidate.LastCommand); cmd != "" {
			cells = append(cells, recentWindowPaneCell{title: cmd})
		}
	}
	return cells
}

func recentWindowKnownInteractiveShell(command string) bool {
	switch strings.TrimSpace(command) {
	case "zsh", "bash", "fish", "sh", "nu", "xonsh":
		return true
	default:
		return false
	}
}

// recentWindowPaneKindGlyph renders the themed AI badge glyph for a single
// pane's badge kind, honoring the configured style (dot/emoji/off). It
// terminates with theme.ANSIReset so the selected-row background re-applies
// cleanly. Returns "" when the kind is unrecognized or the style is off.
func recentWindowPaneKindGlyph(kind, badgeStyle string) string {
	kind = aibadge.Normalize(kind)
	if kind == "" {
		return ""
	}
	glyph := aibadge.Glyph(kind, badgeStyle)
	if strings.TrimSpace(glyph) == "" {
		return ""
	}
	style := recentWindowAIBadgeKindStart(kind)
	if style == "" {
		return ""
	}
	return style + glyph + theme.ANSIReset
}

// recentWindowAIBadgeKindStart maps an AI badge kind to its themed ANSI start
// sequence, mirroring the sidebar's ansiAIBadgeKindStart so the picker reuses
// the same per-role palette.
func recentWindowAIBadgeKindStart(kind string) string {
	switch aibadge.ThemeRole(kind) {
	case aibadge.RoleActionRequired:
		return appAIBadgeActionRequired
	case aibadge.RoleSuccess:
		return appAIBadgeSuccess
	case aibadge.RoleProgress:
		return appAIBadgeProgress
	default:
		return ""
	}
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
	return focused.Local().Format("2006-01-02 15:04")
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
