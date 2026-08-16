package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/core/projectidentity"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/theme"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

const (
	defaultStatusCommandLimit = 500 * time.Millisecond
)

// statusbar git segment colors source from the semantic role map
// (single source shared with the decoration renderer) instead of bare tmux
// literals. The defaults below are the fallback role map, whose values equal
// the historical literals (byte-identical); applyStatusSegmentTheme repoints
// them at the resolved effective theme at command entry (bright Phase 2, B1),
// so the status/notify subprocess paths no longer pin the zero-value theme.
var (
	statusSegmentRoles = theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{}))

	tmuxGitSegmentBg = statusSegmentRoles.GitSegmentBg
	tmuxGitSegmentFg = statusSegmentRoles.GitSegmentFg
	tmuxGitDirtyFg   = statusSegmentRoles.GitDirty
	tmuxGitStagedFg  = statusSegmentRoles.GitStaged
	tmuxGitAheadFg   = statusSegmentRoles.GitAhead
	tmuxGitBehindFg  = statusSegmentRoles.GitBehind
)

// applyStatusSegmentTheme repoints every tmux-side status segment / notify HUD
// role escape at a resolved effective theme. Call once at command entry (it is
// wired into applyNativeUITheme). Applying the fallback theme restores the
// historical literals for every role these paths consume, so fallback and all
// current dark presets stay byte-identical.
func applyStatusSegmentTheme(effective theme.EffectiveTheme) {
	roles := theme.RenderRolesFromEffective(effective)
	statusSegmentRoles = roles

	tmuxGitSegmentBg = roles.GitSegmentBg
	tmuxGitSegmentFg = roles.GitSegmentFg
	tmuxGitDirtyFg = roles.GitDirty
	tmuxGitStagedFg = roles.GitStaged
	tmuxGitAheadFg = roles.GitAhead
	tmuxGitBehindFg = roles.GitBehind

	notifyStateRoles = roles
	notifyProjectOpen = "#[bg=" + theme.TmuxAttentionProjectBg + ",fg=" + roles.StatusTextPrimary + ",bold]"
	notifyBadgeStaleOpen = "#[bg=" + theme.TmuxMutedBg + ",fg=" + roles.StatusTextPrimary + ",bold]"
	notifyBadgeGoneOpen = "#[bg=" + theme.TmuxGoneBg + ",fg=" + roles.StatusTextPrimary + ",dim]"
	notifyLineOpen = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + roles.StateProgress + "]"
	notifyLineCountOpen = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + roles.StateProgress + ",bold]"
	notifyBadgeInfoOpen = "#[bg=" + roles.StateProgress + ",fg=" + theme.TmuxPaneActiveFg + ",bold]"
	notifyBadgeWarnOpen = "#[bg=" + roles.StateWarning + ",fg=" + theme.TmuxPaneActiveFg + ",bold]"
	notifyBadgeCritOpen = "#[bg=" + roles.StateCritical + ",fg=" + roles.StatusTextPrimary + ",bold]"
	notifySeverityInfo = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + roles.StateProgress + "]"
	notifySeverityWarn = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + roles.StateWarning + "]"
	notifySeverityCrit = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + roles.StateCritical + ",bold]"
}

type statusCommand struct {
	lookupEnv     func(string) string
	homeDir       func() (string, error)
	readCommand   func(ctx context.Context, name string, args ...string) ([]byte, error)
	commandLimit  time.Duration
	now           func() time.Time
	usage         *usagecmd.Command
	notifyStoreFn func() (notifyStore, error)
}

func newStatusCommand() *statusCommand {
	return &statusCommand{
		lookupEnv:     os.Getenv,
		homeDir:       os.UserHomeDir,
		readCommand:   readExternalCommand,
		now:           time.Now,
		usage:         usagecmd.New(nil),
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
	// Bright Phase 2 (B1): the status segment subprocesses render with the
	// resolved effective theme instead of the fallback role map. The restore
	// keeps the process-global role escapes deterministic.
	defer applyNativeUIThemeFromConfig(c.homeDir, c.lookupEnv, "")()

	switch args[0] {
	case "git":
		return c.runGit(args[1:], stdout, stderr)
	case "project":
		return c.runProject(args[1:], stdout, stderr)
	case "usage":
		if c.usage == nil {
			c.usage = usagecmd.New(nil)
		}
		return c.usage.RunStatus(args[1:], stdout, stderr)
	case "notify":
		return c.runNotify(args[1:], stdout, stderr)
	case "resources":
		return c.runResources(args[1:], stdout, stderr)
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
	_, err := fmt.Fprintf(stdout, " #[bold,fg=%s,bg=%s] %s%s #[default]", tmuxGitSegmentFg, tmuxGitSegmentBg, statusbarGitDecorator(c.statusbarDecoration(), remoteURL), segment)
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
		parts = append(parts, gitStateToken(tmuxGitDirtyFg, "*"))
	}
	if staged > 0 {
		parts = append(parts, gitStateToken(tmuxGitStagedFg, fmt.Sprintf("+%d", staged)))
	}
	if ahead > 0 {
		parts = append(parts, gitStateToken(tmuxGitAheadFg, fmt.Sprintf("↑%d", ahead)))
	}
	if behind > 0 {
		parts = append(parts, gitStateToken(tmuxGitBehindFg, fmt.Sprintf("↓%d", behind)))
	}
	return strings.Join(parts, " ")
}

func gitStateToken(color, label string) string {
	return fmt.Sprintf("#[nobold,fg=%s]%s#[bold,fg=%s]", color, label, tmuxGitSegmentFg)
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
		if raw := c.readTrimmed("tmux", "show-options", "-gqv", statusbarDecorationGitTmuxOption); strings.TrimSpace(raw) != "" {
			return config.NormalizeStatusbarDecoration(raw)
		}
		if raw := c.readTrimmed("tmux", "show-options", "-gqv", statusbarDecorationTmuxOption); strings.TrimSpace(raw) != "" {
			return config.NormalizeStatusbarDecoration(raw)
		}
	}
	return loadStatusbarDecorationForTarget(c.homeDir, c.lookupEnv, statusbarDecorationTargetGit, loadStatusbarDecoration(c.homeDir, c.lookupEnv))
}

func (c *statusCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

func (c *statusCommand) locale() i18n.Locale {
	if c == nil {
		return i18n.FallbackLocale
	}
	return appLocale(c.homeDir, c.lookupEnv)
}

func (c *statusCommand) read(name string, args ...string) ([]byte, error) {
	return c.readWithLimit(c.statusCommandLimit(), name, args...)
}

func (c *statusCommand) readWithLimit(limit time.Duration, name string, args ...string) ([]byte, error) {
	if c.readCommand == nil {
		return nil, errors.New("status command reader is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return c.readCommand(ctx, name, args...)
}

func (c *statusCommand) readTrimmed(name string, args ...string) string {
	out, err := c.read(name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (c *statusCommand) statusCommandLimit() time.Duration {
	if c.commandLimit > 0 {
		return c.commandLimit
	}
	return defaultStatusCommandLimit
}

func printStatusUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux status git [path]")
	fmt.Fprintln(w, "  projmux status project")
	fmt.Fprintln(w, "  projmux status usage [--max-width N]")
	fmt.Fprintln(w, "  projmux status notify [--max-width N]")
	fmt.Fprintln(w, "  projmux status resources")
}

func (c *statusCommand) runResources(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		printStatusUsage(stderr)
		return errors.New("status resources does not accept positional arguments")
	}
	if !systemstatus.Supported() {
		return nil
	}
	// live-resources is the single enablement source for both presentation and
	// sampling. This guard also protects a stale generated config or a direct
	// internal invocation from mutating the sampler cache after Settings turns
	// the component off.
	if loadLiveResourcesMode(c.homeDir, c.lookupEnv) != config.LiveResourcesOn {
		return nil
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return nil
	}
	metrics := (systemstatus.Sampler{CachePath: paths.LiveResourcesSampleFile()}).Sample()
	_, err = fmt.Fprint(stdout, formatLiveResourcesStatus(metrics))
	return err
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
	liveByID, paneSet := c.notifyLiveStateBestEffort()
	out := formatStatusNotifyWithLiveLocale(entries, *maxWidth, now, liveByID, paneSet, c.locale())
	if out == "" {
		return nil
	}
	_, err = fmt.Fprint(stdout, out)
	return err
}

// notifyLiveStateBestEffort reads the live reply+agent index and the full pane
// inventory from a single tmux subprocess, swallowing errors into nil/nil. A
// nil set means "inventory unavailable" so the head entry is never falsely
// classified GONE during a tmux outage.
func (c *statusCommand) notifyLiveStateBestEffort() (map[string]notifyLivePane, notifyLivePaneSet) {
	if c == nil || c.readCommand == nil {
		return nil, nil
	}
	runner := statusCommandRunnerAdapter{read: c.readCommand}
	return (&notifyCommand{livePanes: newAttentionLivePaneLister(runner)}).notifyLiveStateBestEffort()
}

// statusCommandRunnerAdapter wraps statusCommand.readCommand so the
// attention-backed live-pane lister can be reused without exporting the
// underlying runner.
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
	notifyLineDimOpen = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + theme.TmuxStateAheadFg + "]"
	notifyAgentOpen   = "#[bg=" + tmuxAccentAIBg + ",fg=" + theme.TmuxPaneActiveFg + ",bold]"
	notifyAgentClaude = notifyAgentOpen
	notifyAgentCodex  = notifyAgentOpen
	notifyIcon        = "●"
	notifyMidDot      = "·"
	notifyEllipsis    = "…"
	notifyReset       = "#[default]"
)

// Badge tokens whose foreground migrated from the bare TmuxPrimaryFg literal
// to the status.text_primary role (bright Phase 2, B1). Defaults equal the
// historical literals; applyStatusSegmentTheme rebuilds them.
var (
	notifyProjectOpen = "#[bg=" + theme.TmuxAttentionProjectBg + ",fg=" + statusSegmentRoles.StatusTextPrimary + ",bold]"
	// Inactive/gone badges share a muted palette so target-state hints are
	// visually distinct from the active NEED/INFO/WARN/CRIT badges without
	// stealing focus. The colours land in the same neutral grey family the
	// sidebar uses so users learn one visual language for target state.
	notifyBadgeStaleOpen = "#[bg=" + theme.TmuxMutedBg + ",fg=" + statusSegmentRoles.StatusTextPrimary + ",bold]"
	notifyBadgeGoneOpen  = "#[bg=" + theme.TmuxGoneBg + ",fg=" + statusSegmentRoles.StatusTextPrimary + ",dim]"
)

// State/severity-colored notify tokens. These source their state colors from
// the semantic role map (single source shared with statusbar/usage HUD) instead
// of the bare tmux literal aliases. The defaults below use the fallback role
// map, whose values equal the historical literals (byte-identical);
// applyStatusSegmentTheme rebuilds them from the resolved effective theme at
// command entry (bright Phase 2, B1).
var (
	notifyStateRoles = statusSegmentRoles

	notifyLineOpen      = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + notifyStateRoles.StateProgress + "]"
	notifyLineCountOpen = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + notifyStateRoles.StateProgress + ",bold]"
	notifyBadgeInfoOpen = "#[bg=" + notifyStateRoles.StateProgress + ",fg=" + theme.TmuxPaneActiveFg + ",bold]"
	notifyBadgeWarnOpen = "#[bg=" + notifyStateRoles.StateWarning + ",fg=" + theme.TmuxPaneActiveFg + ",bold]"
	notifyBadgeCritOpen = "#[bg=" + notifyStateRoles.StateCritical + ",fg=" + notifyStateRoles.StatusTextPrimary + ",bold]"
	notifySeverityInfo  = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + notifyStateRoles.StateProgress + "]"
	notifySeverityWarn  = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + notifyStateRoles.StateWarning + "]"
	notifySeverityCrit  = "#[bg=" + tmuxAccentAttentionBg + ",fg=" + notifyStateRoles.StateCritical + ",bold]"
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
//  2. Truncate `<text>` with a trailing `…`, preserving age and count.
//  3. Drop `<age>` (and its preceding `·`) while preserving badges and count.
//  4. Drop the badges; fall back to clipped text and count.
//  5. Hard truncate everything, appending the reset directive.
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
	return formatStatusNotifyWithLiveLocale(entries, maxWidth, now, liveByID, nil, i18n.FallbackLocale)
}

// formatStatusNotifyWithLiveLocale renders the status segment. `paneSet` is the
// full live tmux pane inventory used for real GONE classification of the head
// entry; pass nil when unavailable (membership-based GONE is then skipped).
func formatStatusNotifyWithLiveLocale(entries []notify.Notification, maxWidth int, now time.Time, liveByID map[string]notifyLivePane, paneSet notifyLivePaneSet, locale i18n.Locale) string {
	if len(entries) == 0 {
		return ""
	}
	head := entries[0]
	extras := len(entries) - 1

	display := classifyNotifyRowState(head, liveByID, paneSet)
	agent, text := splitAgentPrefix(head)
	if head.Source == notify.SourceAI {
		text = notifyAIStatusBodyTextLocale(head, text, locale)
	}
	badge := assembleNotifyBadges(
		renderNotifyProjectBadge(notifyProjectName(head.Session)),
		renderNotifyStatusMiddleBadge(head, text, display),
		renderNotifyStatusAgentBadge(head, agent),
	)
	age := ""
	if !now.IsZero() && !head.CreatedAt.IsZero() {
		age = formatRelativeAgeLocale(now.Sub(head.CreatedAt), locale)
	}
	plus := ""
	if extras > 0 {
		plus = fmt.Sprintf("   %s+%d%s", notifyLineCountOpen, extras, notifyLineOpen)
	}

	tiers := []func() string{
		// Tier 1: full long form.
		func() string { return assembleNotify(badge, text, age, plus) },
		// Tier 2: keep badges, age, and count; shrink body text first.
		func() string {
			budget := tierBudget(maxWidth, badge, age, plus)
			truncated := shrinkText(text, budget)
			return assembleNotify(badge, truncated, age, plus)
		},
		// Tier 3: if clipping text is still too wide, drop age next.
		func() string {
			budget := tierBudget(maxWidth, badge, "", plus)
			truncated := shrinkText(text, budget)
			return assembleNotify(badge, truncated, "", plus)
		},
		// Tier 4: drop badges; keep dotless clipped text and count.
		func() string {
			budget := max(maxWidth-notifyVisualLen(plus), 1)
			truncated := shrinkText(text, budget)
			if truncated == "" {
				return plus
			}
			return truncated + plus
		},
	}
	for _, tier := range tiers {
		out := tier()
		if out == "" {
			continue
		}
		if maxWidth <= 0 || notifyVisualLen(out) <= maxWidth {
			return notifyLineOpen + out + notifyReset
		}
	}

	// Hard truncate. Keep the dotless text + plus payload. The reset directive
	// is appended unconditionally so we never leak color into the next segment.
	short := text + plus
	return notifyLineOpen + truncateNotifyWithEllipsis(short, maxWidth) + notifyReset
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
	overhead := notifyVisualLen(assembleNotify(badge, "", age, plus))
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

func renderNotifyStatusMiddleBadge(n notify.Notification, text string, display notifyRowDisplayState) string {
	if display != notifyDisplayLive {
		return renderNotifyStateBadgeFor(n, text, display)
	}
	if n.Source == notify.SourceAI {
		return renderNotifyTopicBadge(n)
	}
	return renderNotifyStateBadgeFor(n, text, display)
}

func renderNotifyStatusAgentBadge(n notify.Notification, agent string) string {
	if n.Source == notify.SourceAI {
		return ""
	}
	return renderNotifyAgentBadge(agent)
}

func renderNotifyTopicBadge(n notify.Notification) string {
	if n.Source != notify.SourceAI {
		return ""
	}
	topic := strings.TrimSpace(n.Metadata["topic"])
	if topic == "" {
		return ""
	}
	return renderNotifyBlockBadge(topic, notifyAgentOpen)
}

func notifyAIStatusBodyTextLocale(n notify.Notification, text string, locale i18n.Locale) string {
	if rendered := renderAINotifyText(n.Text, n.Metadata, locale); rendered.Full != "" {
		return rendered.Full
	}
	topic := strings.TrimSpace(n.Metadata["topic"])
	if topic != "" && topic == strings.TrimSpace(text) {
		return "Ready"
	}
	return text
}

// renderNotifyStateBadgeFor renders the head-entry state badge using the
// 3-rune short label (INA/GON) when the entry is inactive/gone, and the legacy
// 4-rune NEED/INFO/WARN/CRIT label otherwise. The palette is overridden for
// inactive/gone so target state stands out before the user clicks.
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

// notifyStateLabelFor renders the long-form state badge label. When the
// classifier reports inactive/gone it overrides the severity-derived label so
// target state is visible before the user clicks. Unknown display
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
	category := strings.ToLower(strings.TrimSpace(n.Metadata["category"]))
	state := strings.ToLower(strings.TrimSpace(n.Metadata["state"]))
	if n.Source == notify.SourceAI && (state == "need" ||
		category == "approval_required" || category == "input_required" ||
		strings.Contains(normalized, "reply ready") ||
		strings.Contains(normalized, "needs reply") ||
		strings.Contains(normalized, "approval needed") ||
		strings.Contains(normalized, "waiting for input")) {
		return "NEED"
	}
	return "INFO"
}

func notifyStateLabelForLocale(n notify.Notification, text string, display notifyRowDisplayState, locale i18n.Locale) string {
	switch display {
	case notifyDisplayStale:
		return i18n.FormatStatusToken(i18n.StatusTokenStale, locale, i18n.FormatFull)
	case notifyDisplayGone:
		return i18n.FormatStatusToken(i18n.StatusTokenGone, locale, i18n.FormatCompact)
	default:
		return notifyStateLabelFor(n, text, display)
	}
}

// notifyStateShortLabel returns the statusbar abbreviation (3 runes) for the
// resolved label. Inactive/gone collapse to `INA` / `GON` so the segment stays
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
	if notifyVisualLen(s) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return notifyEllipsis
	}
	return i18n.TruncateTerminalCells(s, maxRunes-1) + notifyEllipsis
}

func notifyVisualLen(s string) int {
	return i18n.TerminalCellWidth(s)
}

func truncateNotifyWithEllipsis(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	plain := intrender.StripTmuxEscapes(s)
	if notifyVisualLen(plain) <= maxWidth {
		return plain
	}
	if maxWidth == 1 {
		return i18n.TruncateTerminalCells(plain, 1)
	}
	return i18n.TruncateTerminalCells(plain, maxWidth-1) + notifyEllipsis
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
	if parts := parseAITextNotificationParts(text); parts.Agent != "" {
		agent := strings.ToLower(parts.Agent)
		if parts.Detail == "" {
			return agent, ""
		}
		return agent, parts.Detail
	}
	if agent := strings.ToLower(strings.TrimSpace(n.Metadata["agent"])); isKnownAgent(agent) {
		return agent, text
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

// notifyProjectName resolves the notify sidebar's project label from a session
// name via the unified resolver, then applies the de-slug reduction the sidebar
// has always shown (e.g. "repos-projmux" -> "projmux"). A notify entry carries
// only its session name, so Resolve falls through to the SessionName source
// (returned verbatim) and DeSlug produces the compact label — identical output
// to the former inline cut-at-first-dash.
func notifyProjectName(session string) string {
	res := projectidentity.Resolve(projectidentity.Inputs{SessionName: session}, projectidentity.OSFS)
	return projectidentity.DeSlug(res.Name)
}

// resolveProjectDisplayName resolves the unified project display label from the
// identity signals a surface knows, applying the session-name de-slug ONLY when
// the resolver fell through to the session slug. Anchor, worktree-main, and
// cwd-marker names are already compact basenames and must not be lossy-cut (a
// real repo like "my-app" would otherwise collapse to "app"). This generalizes
// the notify sidebar's Resolve+DeSlug so the switch sidebar, statusbar session
// segment, and path popup all resolve names the same way.
func resolveProjectDisplayName(in projectidentity.Inputs, f projectidentity.FS) string {
	res := projectidentity.Resolve(in, f)
	if res.Source == projectidentity.SessionName {
		return projectidentity.DeSlug(res.Name)
	}
	return res.Name
}

// runProject prints the unified project display name for the current session,
// consumed by the status-left session segment (`#(projmux status project)`). It
// reads the session name, session anchor (@projmux_project_path), and active
// pane cwd from tmux and resolves them through the shared project-identity
// resolver, so the statusbar shows the same name as recent windows, notify, and
// the switch sidebar. Any failure degrades to empty output so a status refresh
// never fails loudly.
func (c *statusCommand) runProject(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		printStatusUsage(stderr)
		return errors.New("status project accepts no arguments")
	}
	if c.env("TMUX") == "" {
		return nil
	}
	// Read each identity signal with its own display-message: tmux escapes a raw
	// field separator (e.g. 0x1f) to a literal "\037" in -F output, so a single
	// multi-field format cannot be split back apart reliably.
	name := resolveProjectDisplayName(projectidentity.Inputs{
		SessionName: c.readTrimmed("tmux", "display-message", "-p", "#{session_name}"),
		AnchorPath:  c.readTrimmed("tmux", "display-message", "-p", "#{@projmux_project_path}"),
		PaneCWD:     c.readTrimmed("tmux", "display-message", "-p", "#{pane_current_path}"),
	}, projectidentity.OSFS)
	if name == "" {
		return nil
	}
	_, err := fmt.Fprint(stdout, name)
	return err
}

// formatRelativeAge renders a compact English relative age.
func formatRelativeAge(d time.Duration) string {
	return formatRelativeAgeLocale(d, i18n.FallbackLocale)
}

func formatRelativeAgeLocale(d time.Duration, locale i18n.Locale) string {
	if d < 0 {
		d = 0
	}
	return i18n.FormatRelativeAge(d, locale, i18n.FormatCompact)
}
