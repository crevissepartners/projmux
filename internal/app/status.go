package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
)

const (
	defaultKubeCacheTTL     = 5 * time.Second
	defaultKubeCommandLimit = 400 * time.Millisecond
)

type statusCommand struct {
	lookupEnv     func(string) string
	homeDir       func() (string, error)
	readCommand   func(ctx context.Context, name string, args ...string) ([]byte, error)
	now           func() time.Time
	usage         *usageCommand
	notifyStoreFn func() (notifyStore, error)
}

func newStatusCommand() *statusCommand {
	return &statusCommand{
		lookupEnv:     os.Getenv,
		homeDir:       os.UserHomeDir,
		readCommand:   readExternalCommand,
		now:           time.Now,
		usage:         newUsageCommand(),
		notifyStoreFn: defaultStatusNotifyStore,
	}
}

// defaultStatusNotifyStore resolves the canonical notify queue used by the
// status bar segment. Failures here become silent emptiness — the status
// segment must never fail loudly during the tmux refresh interval.
func defaultStatusNotifyStore() (notifyStore, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("resolve default config paths: %w", err)
	}
	return notify.NewDefaultStore(paths), nil
}

func (c *statusCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printStatusUsage(stderr)
		return errors.New("status requires a subcommand")
	}

	switch args[0] {
	case "git":
		return c.runGit(args[1:], stdout, stderr)
	case "kube":
		return c.runKube(args[1:], stdout, stderr)
	case "usage":
		if c.usage == nil {
			c.usage = newUsageCommand()
		}
		return c.usage.runStatus(args[1:], stdout, stderr)
	case "notify":
		return c.runNotify(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printStatusUsage(stdout)
		return nil
	default:
		printStatusUsage(stderr)
		return fmt.Errorf("unknown status subcommand: %s", args[0])
	}
}

func (c *statusCommand) runGit(args []string, stdout, stderr io.Writer) error {
	if len(args) > 1 {
		printStatusUsage(stderr)
		return errors.New("status git accepts at most 1 [path] argument")
	}
	path := ""
	if len(args) == 1 {
		path = strings.TrimSpace(args[0])
	} else if c.env("TMUX") != "" {
		path = c.readTrimmed("tmux", "display-message", "-p", "#{pane_current_path}")
	}
	if path == "" {
		return nil
	}
	if _, err := c.read("git", "-C", path, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil
	}
	branch := c.readTrimmed("git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branch == "" {
		branch = c.readTrimmed("git", "-C", path, "rev-parse", "--short", "HEAD")
	}
	if branch == "" {
		return nil
	}
	segment := branch
	if state := parseGitPorcelainStatus(c.readTrimmed("git", "-C", path, "status", "--porcelain=v1", "--branch")); state != "" {
		segment += " " + state
	}
	remoteURL := c.readTrimmed("git", "-C", path, "config", "--get", "remote.origin.url")
	_, err := fmt.Fprintf(stdout, " #[bold,fg=colour16,bg=colour45] %s%s #[default]", statusbarGitDecorator(c.statusbarDecoration(), remoteURL), segment)
	return err
}

func parseGitPorcelainStatus(raw string) string {
	var (
		staged      int
		ahead       int
		behind      int
		hasWorktree bool
	)
	for line := range strings.SplitSeq(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			ahead, behind = parseGitAheadBehind(line)
			continue
		}
		hasWorktree = true
		if len(line) >= 2 && line[0] != ' ' && line[0] != '?' && line[0] != '!' {
			staged++
		}
	}
	parts := []string{}
	if hasWorktree {
		parts = append(parts, gitStateToken("colour88", "*"))
	}
	if staged > 0 {
		parts = append(parts, gitStateToken("colour22", fmt.Sprintf("+%d", staged)))
	}
	if ahead > 0 {
		parts = append(parts, gitStateToken("colour17", fmt.Sprintf("↑%d", ahead)))
	}
	if behind > 0 {
		parts = append(parts, gitStateToken("colour94", fmt.Sprintf("↓%d", behind)))
	}
	return strings.Join(parts, " ")
}

func gitStateToken(color, label string) string {
	return fmt.Sprintf("#[fg=%s]%s#[fg=colour16]", color, label)
}

func parseGitAheadBehind(line string) (int, int) {
	start := strings.Index(line, "[")
	end := strings.LastIndex(line, "]")
	if start < 0 || end <= start {
		return 0, 0
	}
	ahead := 0
	behind := 0
	for part := range strings.SplitSeq(line[start+1:end], ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "ahead":
			ahead = parsePositiveInt(fields[1])
		case "behind":
			behind = parsePositiveInt(fields[1])
		}
	}
	return ahead, behind
}

func (c *statusCommand) statusbarDecoration() config.StatusbarDecoration {
	if c.env("TMUX") != "" {
		if raw := c.readTrimmed("tmux", "show-option", "-gqv", statusbarDecorationGitTmuxOption); strings.TrimSpace(raw) != "" {
			return config.NormalizeStatusbarDecoration(raw)
		}
		if raw := c.readTrimmed("tmux", "show-option", "-gqv", statusbarDecorationTmuxOption); strings.TrimSpace(raw) != "" {
			return config.NormalizeStatusbarDecoration(raw)
		}
	}
	return loadStatusbarDecorationForTarget(c.homeDir, c.lookupEnv, statusbarDecorationTargetGit, loadStatusbarDecoration(c.homeDir, c.lookupEnv))
}

func (c *statusCommand) runKube(args []string, stdout, stderr io.Writer) error {
	if len(args) > 1 {
		printStatusUsage(stderr)
		return errors.New("status kube accepts at most 1 [session] argument")
	}
	sessionName := ""
	if len(args) == 1 {
		sessionName = strings.TrimSpace(args[0])
	} else {
		sessionName = c.readTrimmed("tmux", "display-message", "-p", "#S")
	}
	if sessionName == "" {
		return nil
	}
	segment := c.kubeSegment(sessionName)
	if segment == "" {
		return nil
	}
	_, err := fmt.Fprint(stdout, segment)
	return err
}

func (c *statusCommand) kubeSegment(sessionName string) string {
	if c.readTrimmed("command", "-v", "kubectl") == "" {
		return ""
	}
	cacheFile := c.kubeCacheFile(sessionName)
	cached := readTextFile(cacheFile)
	if info, err := os.Stat(cacheFile); err == nil && c.now().Sub(info.ModTime()) < c.kubeCacheTTL() {
		return cached
	}

	kubeConfig := c.kubeSessionPath(sessionName)
	if kubeConfig != "" {
		if _, err := os.Stat(kubeConfig); err != nil {
			kubeConfig = ""
		}
	}

	ctx := c.kubectlTrimmed(kubeConfig, "config", "current-context")
	if ctx == "" {
		return cached
	}
	ns := c.kubectlTrimmed(kubeConfig, "config", "view", "--minify", "--output", "jsonpath={..namespace}")
	if ns == "" {
		ns = "default"
	}
	segment := fmt.Sprintf("⎈ #[fg=red]%s#[default]/#[fg=blue]%s#[default]", ctx, ns)
	_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
	_ = os.WriteFile(cacheFile, []byte(segment), 0o644)
	return segment
}

func (c *statusCommand) kubectlTrimmed(kubeConfig string, args ...string) string {
	timeoutValue := formatStatusTimeout(c.kubeCommandLimit())
	if c.readTrimmed("command", "-v", "timeout") != "" {
		command := []string{"timeout", timeoutValue, "kubectl"}
		command = append(command, args...)
		if kubeConfig != "" {
			command = append([]string{"KUBECONFIG=" + kubeConfig}, command...)
			return c.readTrimmed("env", command...)
		}
		return c.readTrimmed(command[0], command[1:]...)
	}
	if kubeConfig != "" {
		command := append([]string{"KUBECONFIG=" + kubeConfig, "kubectl"}, args...)
		return c.readTrimmed("env", command...)
	}
	return c.readTrimmed("kubectl", args...)
}

func (c *statusCommand) kubeSessionPath(sessionName string) string {
	if strings.TrimSpace(sessionName) == "" {
		return ""
	}
	return filepath.Join(c.kubeSessionBaseDir(), sessionName+".yaml")
}

func (c *statusCommand) kubeSessionBaseDir() string {
	root := strings.TrimRight(c.env("XDG_RUNTIME_DIR"), string(os.PathSeparator))
	if root == "" {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			root = "."
		} else {
			root = filepath.Join(homeDir, ".cache")
		}
	}
	return filepath.Join(root, "kube-sessions")
}

func (c *statusCommand) kubeCacheFile(sessionName string) string {
	slug := strings.ReplaceAll(sessionName, "/", "-")
	slug = strings.ReplaceAll(slug, ".", "_")
	return filepath.Join(c.kubeCacheDir(), "kube-segment-"+slug+".txt")
}

func (c *statusCommand) kubeCacheDir() string {
	cacheHome := strings.TrimRight(c.env("XDG_CACHE_HOME"), string(os.PathSeparator))
	if cacheHome == "" {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			cacheHome = ".cache"
		} else {
			cacheHome = filepath.Join(homeDir, ".cache")
		}
	}
	return filepath.Join(cacheHome, "tmux")
}

func (c *statusCommand) kubeCacheTTL() time.Duration {
	seconds := parsePositiveInt(c.env("TMUX_KUBE_CACHE_TTL"))
	if seconds <= 0 {
		return defaultKubeCacheTTL
	}
	return time.Duration(seconds) * time.Second
}

func (c *statusCommand) kubeCommandLimit() time.Duration {
	value := strings.TrimSpace(c.env("TMUX_KUBE_TIMEOUT"))
	if value == "" {
		return defaultKubeCommandLimit
	}
	if strings.ContainsAny(value, "hmsuµns") {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	parts := strings.SplitN(value, ".", 2)
	seconds := parsePositiveInt(parts[0])
	millis := 0
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		for len(frac) < 3 {
			frac += "0"
		}
		millis = parsePositiveInt(frac)
	}
	d := time.Duration(seconds)*time.Second + time.Duration(millis)*time.Millisecond
	if d <= 0 {
		return defaultKubeCommandLimit
	}
	return d
}

func (c *statusCommand) home() (string, error) {
	if c.homeDir == nil {
		return "", errors.New("status home directory resolver is not configured")
	}
	return c.homeDir()
}

func (c *statusCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

func (c *statusCommand) read(name string, args ...string) ([]byte, error) {
	if c.readCommand == nil {
		return nil, errors.New("status command reader is not configured")
	}
	return c.readCommand(context.Background(), name, args...)
}

func (c *statusCommand) readTrimmed(name string, args ...string) string {
	out, err := c.read(name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func formatStatusTimeout(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d", int(d/time.Second))
	}
	return fmt.Sprintf("%.3f", d.Seconds())
}

func readTextFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func printStatusUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux status git [path]")
	fmt.Fprintln(w, "  projmux status kube [session]")
	fmt.Fprintln(w, "  projmux status usage [--max-width N]")
	fmt.Fprintln(w, "  projmux status notify [--max-width N]")
}

// defaultStatusNotifyMaxWidth bounds the rendered notification segment so it
// cannot blow out the status line on narrow terminals.
const defaultStatusNotifyMaxWidth = 200

// runNotify renders the newest entry in the notify queue as a tmux status
// segment. The segment is intentionally silent on failure — the tmux status
// interval polls this command and must never produce a stack trace.
func (c *statusCommand) runNotify(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status notify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxWidth := fs.Int("max-width", defaultStatusNotifyMaxWidth, "truncate the inner text to N runes (0 = no truncation)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("status notify does not accept positional arguments")
	}

	store, err := c.notifyStore()
	if err != nil {
		// Status segments must never fail loudly.
		return nil
	}
	entries, err := store.List()
	if err != nil {
		return nil
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	liveByID := c.notifyLiveByIDBestEffort()
	out := formatStatusNotifyWithLive(entries, *maxWidth, now, liveByID)
	if out == "" {
		return nil
	}
	_, err = fmt.Fprint(stdout, out)
	return err
}

// notifyLiveByIDBestEffort returns the live AI reply-state pane index keyed
// by notify id, swallowing tmux errors so the status segment never fails
// loudly. Returns nil when the runner is unavailable or tmux refuses to
// list panes — in that case the segment renders without stale/gone hints.
func (c *statusCommand) notifyLiveByIDBestEffort() map[string]notifyLivePane {
	if c == nil || c.readCommand == nil {
		return nil
	}
	runner := statusCommandRunnerAdapter{read: c.readCommand}
	panes, err := (&notifyCommand{runner: runner}).listNotifyLivePanes()
	if err != nil {
		return nil
	}
	return notifyLiveShouldQueueByID(panes)
}

// statusCommandRunnerAdapter wraps statusCommand.readCommand so the existing
// notifyCommand.listNotifyLivePanes helper can be reused without exporting
// the underlying runner.
type statusCommandRunnerAdapter struct {
	read func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (a statusCommandRunnerAdapter) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if a.read == nil {
		return nil, errors.New("status command reader is not configured")
	}
	return a.read(ctx, name, args...)
}

func (c *statusCommand) notifyStore() (notifyStore, error) {
	if c.notifyStoreFn == nil {
		return nil, errors.New("status notify store factory is not configured")
	}
	return c.notifyStoreFn()
}

// HUD-style design tokens for the notify status segment. The whole segment
// carries a notification-colored background; badges are stronger blocks on top
// of that same line instead of separate outline glyphs.
const (
	notifyLineOpen      = "#[bg=colour24,fg=colour231]"
	notifyLineDimOpen   = "#[bg=colour24,fg=colour117]"
	notifyLineCountOpen = "#[bg=colour24,fg=colour153,bold]"
	notifyProjectOpen   = "#[bg=colour31,fg=colour231,bold]"
	notifyBadgeInfoOpen = "#[bg=brightcyan,fg=black,bold]"
	notifyBadgeWarnOpen = "#[bg=yellow,fg=black,bold]"
	notifyBadgeCritOpen = "#[bg=red,fg=white,bold]"
	// Stale/gone badges share a muted palette so the ack-only state is
	// visually distinct from the active NEED/INFO/WARN/CRIT badges without
	// stealing focus. The colours land in the same neutral grey family the
	// sidebar uses so users learn a single ack-only affordance.
	notifyBadgeStaleOpen = "#[bg=colour240,fg=colour231,bold]"
	notifyBadgeGoneOpen  = "#[bg=colour238,fg=colour231,dim]"
	notifyAgentOpen      = "#[bg=colour51,fg=black,bold]"
	notifyAgentClaude    = "#[bg=colour208,fg=black,bold]"
	notifyAgentCodex     = "#[bg=colour33,fg=colour231,bold]"
	notifySeverityInfo   = "#[bg=colour24,fg=brightcyan]"
	notifySeverityWarn   = "#[bg=colour24,fg=yellow]"
	notifySeverityCrit   = "#[bg=colour24,fg=red,bold]"
	notifyIcon           = "●"
	notifyMidDot         = "·"
	notifyEllipsis       = "…"
	notifyReset          = "#[default]"
)

// known agent prefixes recognised when stripping a leading `<agent>:` from
// the producer-rendered text. Lower-case match only. New agents can be added
// here without touching call sites.
var notifyKnownAgents = []string{"claude", "codex"}

// formatStatusNotify renders the newest entry of the queue as a single
// HUD-style tmux status segment. The output ends with a tmux `#[default]`
// reset so adjacent segments are not stained by colors.
//
// Long form: `<project> <state> [agent] <text>  · <age>   +<N>`
//
// The segment background carries the notification affordance. The body text
// uses the line foreground so it stays readable next to the stronger badges.
//
// The renderer degrades through five tiers as `maxWidth` shrinks:
//
//  1. Full long form: line block + badges + text + age + count.
//  2. Drop `<age>` (and its preceding `·`).
//  3. Truncate `<text>` with a trailing `…`.
//  4. Drop the badges entirely; fall back to a standalone `●` severity icon.
//  5. Hard truncate everything (icon + count are still preserved).
//
// `now` is the wall-clock used to compute the relative age. Pass the zero
// time to suppress the age field entirely.
func formatStatusNotify(entries []notify.Notification, maxWidth int, now time.Time) string {
	return formatStatusNotifyWithLive(entries, maxWidth, now, nil)
}

// formatStatusNotifyWithLive renders the status segment with awareness of
// stale/gone display state for the head entry. `liveByID` may be nil when
// the caller could not read live tmux pane state; in that case the segment
// degrades to the legacy NEED/INFO/WARN/CRIT badge set.
func formatStatusNotifyWithLive(entries []notify.Notification, maxWidth int, now time.Time, liveByID map[string]notifyLivePane) string {
	if len(entries) == 0 {
		return ""
	}
	head := entries[0]
	extras := len(entries) - 1

	display := classifyNotifyRowState(head, liveByID)
	agent, text := splitAgentPrefix(head)
	badge := assembleNotifyBadges(
		renderNotifyProjectBadge(notifyProjectName(head.Session)),
		renderNotifyStateBadgeFor(head, text, display),
		renderNotifyAgentBadge(agent),
	)
	icon := renderNotifyIcon(head.Severity)
	age := ""
	if !now.IsZero() && !head.CreatedAt.IsZero() {
		age = formatRelativeAge(now.Sub(head.CreatedAt))
	}
	plus := ""
	if extras > 0 {
		plus = fmt.Sprintf("   %s+%d%s", notifyLineCountOpen, extras, notifyLineOpen)
	}

	tiers := []func() string{
		// Tier 1: full long form.
		func() string { return assembleNotify(badge, text, age, plus) },
		// Tier 2: drop age.
		func() string { return assembleNotify(badge, text, "", plus) },
		// Tier 3: truncate text with trailing ellipsis.
		func() string {
			budget := tierBudget(maxWidth, badge, "", plus)
			truncated := shrinkText(text, budget)
			return assembleNotify(badge, truncated, "", plus)
		},
		// Tier 4: drop badge — fall back to bare severity icon + text.
		func() string {
			iconLead := icon + "  "
			budget := tierBudget(maxWidth, iconLead, "", plus)
			truncated := shrinkText(text, budget)
			if truncated == "" {
				return iconLead + plus
			}
			return iconLead + truncated + plus
		},
	}
	for _, tier := range tiers {
		out := tier()
		if out == "" {
			continue
		}
		if maxWidth <= 0 || visualLen(out) <= maxWidth {
			return notifyLineOpen + out + notifyReset
		}
	}

	// Hard truncate. Keep the icon + plus suffix; rune-truncate everything
	// in between. The reset directive is appended unconditionally so we
	// never leak color into the next segment.
	short := icon + "  " + text + plus
	return notifyLineOpen + truncateWithEllipsis(short, maxWidth) + notifyReset
}

// assembleNotify glues the long-form parts together. Empty parts are
// omitted along with their preceding separator so we can reuse this for
// every degradation tier.
//
// Layout: `<badge> <text>  · <age><plus>`
//
// The badge group already restores `notifyLineOpen`, so subsequent text stays
// on the notification background.
func assembleNotify(badge, text, age, plus string) string {
	var b strings.Builder
	b.WriteString(badge)
	if text != "" {
		b.WriteString(" ")
		b.WriteString(text)
	}
	if age != "" {
		b.WriteString(" ")
		b.WriteString(notifyLineDimOpen)
		b.WriteString(notifyMidDot)
		b.WriteString(" ")
		b.WriteString(age)
		b.WriteString(notifyLineOpen)
	}
	if plus != "" {
		b.WriteString(plus)
	}
	return b.String()
}

// tierBudget returns how many runes of `text` fit within maxWidth given the
// fixed-width pieces we are committing to render at this tier. Returns the
// full budget when maxWidth is non-positive.
//
// The text is rendered with a single leading space, which we charge back
// here so callers don't need to know the assembly's gap rules.
func tierBudget(maxWidth int, badge, age, plus string) int {
	if maxWidth <= 0 {
		return 0
	}
	overhead := visualLen(assembleNotify(badge, "", age, plus))
	textGap := 1
	room := max(maxWidth-overhead-textGap, 1)
	return room
}

// assembleNotifyBadges joins the leading project/state/agent block badges.
func assembleNotifyBadges(badges ...string) string {
	parts := make([]string, 0, len(badges))
	for _, badge := range badges {
		badge = strings.TrimSpace(badge)
		if badge != "" {
			parts = append(parts, badge)
		}
	}
	return strings.Join(parts, " ")
}

func renderNotifyProjectBadge(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return ""
	}
	return renderNotifyBlockBadge(project, notifyProjectOpen)
}

func renderNotifyBadge(label, severity string) string {
	return renderNotifyBlockBadge(label, notifyBadgeOpen(severity))
}

// renderNotifyStateBadgeFor renders the head-entry state badge using the
// 3-rune short label (STL/GON) when the entry is stale/gone, and the legacy
// 4-rune NEED/INFO/WARN/CRIT label otherwise. The palette is overridden for
// stale/gone so the ack-only condition stands out before the user clicks.
func renderNotifyStateBadgeFor(n notify.Notification, text string, display notifyRowDisplayState) string {
	open := notifyDisplayStateOpen(display)
	if open == "" {
		open = notifyBadgeOpen(n.Severity)
	}
	return renderNotifyBlockBadge(notifyStateShortLabel(n, text, display), open)
}

// notifyDisplayStateOpen maps a stale/gone display state to its tmux color
// directive. Live entries return the empty string so the caller falls back
// to the severity palette.
func notifyDisplayStateOpen(state notifyRowDisplayState) string {
	switch state {
	case notifyDisplayStale:
		return notifyBadgeStaleOpen
	case notifyDisplayGone:
		return notifyBadgeGoneOpen
	}
	return ""
}

func renderNotifyAgentBadge(agent string) string {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return ""
	}
	return renderNotifyBlockBadge(agent, notifyAgentOpenFor(agent))
}

func renderNotifyBlockBadge(label, open string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	open = strings.TrimSpace(open)
	if open == "" {
		open = notifyProjectOpen
	}
	return open + " " + label + " " + notifyLineOpen
}

func notifyAgentOpenFor(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "claude":
		return notifyAgentClaude
	case "codex":
		return notifyAgentCodex
	default:
		return notifyAgentOpen
	}
}

// notifyBadgeOpen maps a severity to the state block color. Unknown
// severities fall through to the info palette so we never emit a stripped
// escape.
func notifyBadgeOpen(severity string) string {
	switch severity {
	case notify.SeverityWarn:
		return notifyBadgeWarnOpen
	case notify.SeverityCritical:
		return notifyBadgeCritOpen
	default:
		return notifyBadgeInfoOpen
	}
}

func notifyStateLabel(n notify.Notification, text string) string {
	return notifyStateLabelFor(n, text, notifyDisplayLive)
}

// notifyStateLabelFor renders the long-form state badge label. When the
// classifier reports STALE/GONE it overrides the severity-derived label so
// the ack-only condition is visible before the user clicks. Unknown display
// states fall through to the live-row classification.
func notifyStateLabelFor(n notify.Notification, text string, display notifyRowDisplayState) string {
	if override := notifyDisplayStateLabel(display); override != "" {
		return override
	}
	switch n.Severity {
	case notify.SeverityWarn:
		return "WARN"
	case notify.SeverityCritical:
		return "CRIT"
	}
	normalized := strings.ToLower(strings.TrimSpace(text))
	if n.Source == notify.SourceAI && (strings.Contains(normalized, "reply ready") ||
		strings.Contains(normalized, "needs reply") ||
		strings.Contains(normalized, "approval needed") ||
		strings.Contains(normalized, "waiting for input")) {
		return "NEED"
	}
	return "INFO"
}

// notifyStateShortLabel returns the statusbar abbreviation (3 runes) for the
// resolved label. STALE/GONE collapse to `STL` / `GON` so the segment stays
// inside its width budget.
func notifyStateShortLabel(n notify.Notification, text string, display notifyRowDisplayState) string {
	if override := notifyDisplayStateShortLabel(display); override != "" {
		return override
	}
	return notifyStateLabelFor(n, text, notifyDisplayLive)
}

// shrinkText shrinks s so its rune length is at most maxRunes, replacing
// the trailing rune with `…` when truncation actually occurs. A budget of
// zero means "no shrinking".
func shrinkText(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return notifyEllipsis
	}
	return string(rs[:maxRunes-1]) + notifyEllipsis
}

// renderNotifyIcon returns the severity-tinted bullet that opens the
// segment. Unknown severities fall through to the info color so we never
// emit a stripped escape.
func renderNotifyIcon(severity string) string {
	color := notifySeverityInfo
	switch severity {
	case notify.SeverityWarn:
		color = notifySeverityWarn
	case notify.SeverityCritical:
		color = notifySeverityCrit
	}
	return color + notifyIcon + notifyLineOpen
}

// splitAgentPrefix extracts the leading `<agent>:` from the queue entry's
// text when the source is `ai`. Returns the lower-cased agent token and the
// remaining text (whitespace-trimmed). When no recognised prefix is found
// the agent is empty and the original text (trimmed) is returned.
//
// Edge inputs (text without `:`, unknown agents, empty text) all degrade to
// "no agent prefix" rather than panicking.
func splitAgentPrefix(n notify.Notification) (string, string) {
	text := strings.TrimSpace(n.Text)
	if n.Source != notify.SourceAI {
		return "", text
	}
	idx := strings.Index(text, ":")
	if idx <= 0 {
		return "", text
	}
	candidate := strings.ToLower(strings.TrimSpace(text[:idx]))
	if !isKnownAgent(candidate) {
		return "", text
	}
	rest := strings.TrimSpace(text[idx+1:])
	if rest == "" {
		// The text was just `claude:` — no body left, so don't strip.
		return "", text
	}
	return candidate, rest
}

func isKnownAgent(name string) bool {
	return slices.Contains(notifyKnownAgents, name)
}

func notifyProjectName(session string) string {
	session = strings.TrimSpace(session)
	if session == "" {
		return ""
	}
	if _, after, ok := strings.Cut(session, "-"); ok && strings.TrimSpace(after) != "" {
		return strings.TrimSpace(after)
	}
	return session
}

// formatRelativeAge renders a duration as `just now`, `<N>s` (only via the
// "just now" branch), `<N>m`, `<N>h`, or `<N>d`. Negative durations (clock
// skew) are clamped to "just now".
func formatRelativeAge(d time.Duration) string {
	if d < 60*time.Second {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
