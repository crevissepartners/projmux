package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
	claudeadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/claude"
	codexadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/codex"
)

// usageCommand exposes the `projmux usage` and `projmux status usage`
// surfaces. Both share a single Manager so collect-once-render-twice stays
// cheap.
type usageCommand struct {
	managerFn func() (*usage.Manager, error)
	now       func() time.Time
	lookupEnv func(string) string
}

func newUsageCommand() *usageCommand {
	c := &usageCommand{
		now:       time.Now,
		lookupEnv: os.Getenv,
	}
	c.managerFn = c.defaultManager
	return c
}

// staleAfter is the age at which a snapshot is considered stale and
// flagged for the user with a single `~` marker. This is the single
// source of truth for the HUD and the table — a snapshot older than
// this triggers the marker everywhere staleness is rendered.
//
// The value is aligned with the longest expected adapter throttle
// (Claude, 5min) plus enough headroom that a healthy install never
// trips the marker on the hot path.
const staleAfter = 10 * time.Minute

// veryStaleAfter is the age at which a snapshot is considered VERY
// stale and the marker doubles to `~~` (still rendered in dim color).
// This nudges the user to manually `--force` if they care about the
// exact value: by this point the data is likely from a different
// 5-hour window than the one currently active.
const veryStaleAfter = 1 * time.Hour

// Run implements `projmux usage [...]`.
func (c *usageCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", "all", "filter by model: codex | claude | all")
	window := fs.String("window", "all", "filter by window: 5h | weekly | all")
	asJSON := fs.Bool("json", false, "emit a JSON array instead of the tab-aligned table")
	// --force / -f bypasses the per-adapter throttle floor AND clears
	// any active backoff before invoking adapters. Useful when bound
	// to a tmux key as a "force refresh now" gesture.
	force := fs.Bool("force", false, "bypass per-adapter throttle and clear active backoff before refreshing")
	fs.BoolVar(force, "f", false, "shorthand for --force")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		printUsageHelp(stderr)
		return fmt.Errorf("usage does not accept positional arguments")
	}

	mgr, err := c.managerFn()
	if err != nil {
		return err
	}
	if c.env(usageDebugEnvVar) != "" {
		mgr.SetDebug(func(format string, args ...any) {
			fmt.Fprintf(stderr, format+"\n", args...)
		})
	}
	var (
		snaps      []usage.Snapshot
		collectErr error
	)
	if *force {
		snaps, collectErr = mgr.ForceCollect(context.Background())
	} else {
		snaps, collectErr = mgr.Collect(context.Background())
	}
	// collectErr is informational — adapters that fail still keep the rest
	// of the rendering pipeline working. We surface a single warning to
	// stderr so the user knows partial data was used.
	if collectErr != nil {
		fmt.Fprintf(stderr, "usage: warning: %v\n", collectErr)
	}

	filtered := filterSnapshots(snaps, *model, *window)
	filtered = usage.SortedSnapshots(filtered)

	state, _ := mgr.LoadState()

	if *asJSON {
		return writeUsageJSON(stdout, filtered, state, c.now())
	}
	if err := writeUsageTable(stdout, filtered, c.now()); err != nil {
		return err
	}
	// Surface backoff status in the human-readable table form so users
	// can see why their numbers aren't refreshing. The --json form
	// already includes a `backoff` block.
	writeBackoffNote(stdout, state, c.now())
	return nil
}

// statusRefreshThrottle is the minimum interval between adapter walks
// triggered by `projmux status usage`. tmux refreshes the status line every
// 5s by default; 30s gives the cache enough breathing room while keeping
// the displayed numbers fresh on a human timescale. Adapters that
// implement ThrottleHinter (e.g. Claude → 60s) override this floor on a
// per-adapter basis inside the Manager.
const statusRefreshThrottle = 30 * time.Second

// usageDebugEnvVar gates whether MaybeCollect's swallowed adapter error is
// echoed to stderr. Off by default — the status segment must stay silent on
// a healthy install.
const usageDebugEnvVar = "PROJMUX_USAGE_DEBUG"

// runStatus implements the `projmux status usage` subcommand. It triggers
// an opportunistic, throttled cache refresh (so a fresh install or a stale
// cache self-heals on the next tmux redraw) and then reads the persisted
// cache to render the HUD segment. Adapter failures during this hot path
// are swallowed unless PROJMUX_USAGE_DEBUG is set.
func (c *usageCommand) runStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxWidth := fs.Int("max-width", 0, "truncate output to N runes (0 = no truncation)")
	// --force / -f mirrors the `projmux usage` flag: bypass throttle
	// and clear active backoff. Suitable for tmux key bindings that
	// trigger an explicit "refresh now" gesture.
	force := fs.Bool("force", false, "bypass per-adapter throttle and clear active backoff before refreshing")
	fs.BoolVar(force, "f", false, "shorthand for --force")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("status usage does not accept positional arguments")
	}

	mgr, err := c.managerFn()
	if err != nil {
		// Status segment must never fail loudly — silently emit nothing.
		return nil
	}
	if c.env(usageDebugEnvVar) != "" {
		mgr.SetDebug(func(format string, args ...any) {
			fmt.Fprintf(stderr, format+"\n", args...)
		})
	}

	// Force path: always run every adapter, ignoring throttle/backoff.
	// Default path: opportunistic, throttled refresh. Errors here are
	// non-fatal; on debug builds we echo to stderr so the user can
	// investigate why the cache is stale.
	if *force {
		if _, refreshErr := mgr.ForceCollect(context.Background()); refreshErr != nil {
			if c.env(usageDebugEnvVar) != "" {
				fmt.Fprintf(stderr, "usage: refresh: %v\n", refreshErr)
			}
		}
	} else {
		if _, refreshErr := mgr.MaybeCollect(context.Background(), statusRefreshThrottle); refreshErr != nil {
			if c.env(usageDebugEnvVar) != "" {
				fmt.Fprintf(stderr, "usage: refresh: %v\n", refreshErr)
			}
		}
	}

	snaps, err := mgr.LoadAll()
	if err != nil {
		return nil
	}

	out := formatStatusUsage(snaps, *maxWidth, c.now())
	if out == "" {
		return nil
	}
	_, err = fmt.Fprint(stdout, out)
	return err
}

// stateDirEnvVar lets multi-machine users redirect the snapshot cache to
// a synced location (Dropbox, iCloud Drive, etc) so the HUD on every box
// reflects whichever machine collected most recently.
const stateDirEnvVar = "PROJMUX_USAGE_STATE_DIR"

// limitsEnvVar is documented as deprecated in v2: authoritative limits
// come from the upstream APIs themselves, so the override file has no
// effect. We accept the env var silently rather than rejecting it so
// existing user configs keep working.
const limitsEnvVar = "PROJMUX_USAGE_LIMITS_PATH"

func (c *usageCommand) defaultManager() (*usage.Manager, error) {
	registry := usage.NewRegistry()
	if err := registry.Register(claudeadapter.New()); err != nil {
		return nil, fmt.Errorf("usage: register claude adapter: %w", err)
	}
	if err := registry.Register(codexadapter.New()); err != nil {
		return nil, fmt.Errorf("usage: register codex adapter: %w", err)
	}
	stateDir, err := c.resolveStateDir()
	if err != nil {
		return nil, err
	}
	// PROJMUX_USAGE_LIMITS_PATH is deprecated in schema v2 — observe it
	// here purely so users who set it don't see a "permission denied" or
	// similar surprise. Authoritative limits come from the API now.
	_ = c.env(limitsEnvVar)
	store := usage.NewStore(stateDir)
	return usage.NewManager(registry, store, c.now), nil
}

// resolveStateDir honours PROJMUX_USAGE_STATE_DIR when set, falling back
// to <StateDir>/usage. The env-var path is used verbatim so users can
// point it at e.g. ~/Dropbox/projmux/usage to share the cache across
// machines.
func (c *usageCommand) resolveStateDir() (string, error) {
	if override := strings.TrimSpace(c.env(stateDirEnvVar)); override != "" {
		return override, nil
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return "", fmt.Errorf("usage: resolve config paths: %w", err)
	}
	return filepath.Join(paths.StateDir, "usage"), nil
}

func (c *usageCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

// filterSnapshots applies the --model and --window filters. "all" passes
// every value through.
func filterSnapshots(snaps []usage.Snapshot, model, window string) []usage.Snapshot {
	model = strings.ToLower(strings.TrimSpace(model))
	window = strings.ToLower(strings.TrimSpace(window))
	if model == "" {
		model = "all"
	}
	if window == "" {
		window = "all"
	}
	out := snaps[:0:0]
	for _, s := range snaps {
		if model != "all" && !strings.EqualFold(s.Model, model) {
			continue
		}
		if window != "all" && string(s.Window) != window {
			continue
		}
		out = append(out, s)
	}
	return out
}

// jsonSnapshot is the wire shape emitted by `projmux usage --json`. It
// extends the core Snapshot with a per-row `stale` boolean so callers
// (e.g. dashboards, tmux modules) can flag stale data without
// re-implementing the staleness rule.
type jsonSnapshot struct {
	usage.Snapshot
	Stale bool `json:"stale"`
}

// jsonBackoff is the wire shape for per-model backoff state surfaced
// alongside the snapshots. Omitted when no backoff is active so the
// healthy-case payload stays compact.
type jsonBackoff struct {
	Until       time.Time `json:"until"`
	Consecutive int       `json:"consecutive"`
}

// jsonOutput is the top-level wrapper when callers ask for --json AND
// per-model state is non-empty (backoff active). When everything is
// healthy we emit just the snapshot array for backwards compatibility.
type jsonOutput struct {
	Snapshots []jsonSnapshot         `json:"snapshots"`
	Backoff   map[string]jsonBackoff `json:"backoff,omitempty"`
}

func writeUsageJSON(w io.Writer, snaps []usage.Snapshot, state usage.State, now time.Time) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	rows := make([]jsonSnapshot, 0, len(snaps))
	for _, s := range snaps {
		rows = append(rows, jsonSnapshot{Snapshot: s, Stale: isStale(s, now)})
	}

	backoff := map[string]jsonBackoff{}
	for k, v := range state.Backoff {
		if v.Until.IsZero() {
			continue
		}
		backoff[k] = jsonBackoff{Until: v.Until, Consecutive: v.Consecutive}
	}

	if len(backoff) == 0 {
		// Healthy case: stay backwards-compatible with prior --json
		// consumers that expected a bare array.
		if len(rows) == 0 {
			return enc.Encode([]jsonSnapshot{})
		}
		return enc.Encode(rows)
	}
	return enc.Encode(jsonOutput{Snapshots: rows, Backoff: backoff})
}

func writeUsageTable(w io.Writer, snaps []usage.Snapshot, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "MODEL\tWINDOW\tPCT\tRESETS_AT\tSTALE"); err != nil {
		return err
	}
	for _, s := range snaps {
		// Pct comes straight from the upstream API in schema v2, so we
		// always render it (even at 0% — that's a real value).
		pct := fmt.Sprintf("%.0f%%", s.Pct)
		resets := "-"
		if !s.ResetsAt.IsZero() {
			resets = s.ResetsAt.Local().Format(time.RFC3339)
		}
		stale := ""
		if isStale(s, now) {
			stale = "*"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Model, s.Window, pct, resets, stale); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// isStale reports whether a snapshot's UpdatedAt is far enough in the
// past that the user should be warned. now=zero disables the check (used
// by callers that don't have a reliable clock — they get fresh
// rendering, which is the safe default). Returns true for any age
// >staleAfter; callers that need the very-stale level use
// staleLevel directly.
func isStale(s usage.Snapshot, now time.Time) bool {
	return staleLevel(s, now) > 0
}

// staleLevel returns 0 (fresh), 1 (stale, >staleAfter) or 2 (very
// stale, >veryStaleAfter) for the supplied snapshot. The renderer
// uses this to pick between `~` and `~~` markers.
func staleLevel(s usage.Snapshot, now time.Time) int {
	if now.IsZero() || s.UpdatedAt.IsZero() {
		return 0
	}
	age := now.Sub(s.UpdatedAt)
	switch {
	case age > veryStaleAfter:
		return 2
	case age > staleAfter:
		return 1
	default:
		return 0
	}
}

// HUD layout constants. Separators are constant runes so visualLen can
// reliably count cells without ever falling back to the tmux-escape
// stripper.
const (
	statusInnerSeparator = " · " // between 5h and weekly inside a model.
	statusModelSeparator = "   " // 3 spaces between Claude and Codex blocks.
	statusDefaultReset   = "#[default]"
)

// Age-indicator colour thresholds for the Claude HUD block. The age
// indicator carries the staleness signal (it replaces the legacy `~` /
// `~~` markers) so its colour ramps with the staleness level:
//
//   - age <  ageWarnAfter (1h)   → dim grey, informational
//   - age >= ageWarnAfter (1h)   → dim yellow, attention
//   - age >= ageAlertAfter (6h)  → bold red, alert
const (
	ageWarnAfter  = 1 * time.Hour
	ageAlertAfter = 6 * time.Hour
)

// modelDisplay is the in-memory representation of a single model's
// snapshots. Pct values are floats so the >100% over-limit branch can
// surface the actual number (e.g. `319%`) instead of capping at 100%.
//
// Staleness is surfaced via lastSync (the most recent UpdatedAt across
// the model's rows) — the long HUD tier renders an age indicator
// (`(3m)`, `(1h)`, `(8h)`) for adapters whose data may be throttled or
// in backoff (currently Claude). Adapters whose data is always
// near-current (Codex reads from the latest rollout file every call)
// have showAge=false so the indicator is suppressed.
type modelDisplay struct {
	model      string // canonical lowercase key.
	label      string // user-facing label (Claude / Codex / ...).
	shortLabel string // legacy single-letter (C / X / ...).
	hasFive    bool
	fivePct    float64
	fiveStale  int
	hasWeek    bool
	weekPct    float64
	weekStale  int
	// lastSync is max(fiveUpdatedAt, weekUpdatedAt). Used together
	// with `now` to compute the age indicator. Zero when no row
	// supplied an UpdatedAt timestamp.
	lastSync time.Time
	// showAge gates the age-indicator render path. False for
	// adapters whose data is always near-current (Codex).
	showAge bool
}

// formatStatusUsage produces the HUD-style tmux status segment. The output
// degrades gracefully through six tiers:
//
//  1. Long form with age indicator + bars + weekly:
//     `Claude (3m) 5h [████████░░] 80% · weekly [...]   Codex 5h [...] 20% · weekly [...]`
//  2. Drop the age indicator (the legacy `Claude 5h [bar] N% · weekly [bar] N%`
//     form, current default).
//  3. Drop the weekly bar (label + 5h bar only).
//  4. Drop bars entirely (`Claude 5h:80% weekly:30%`).
//  5. Single-letter labels (`C 5h:80% weekly:30%`).
//  6. Hard rune-truncation with trailing `…`.
//
// maxWidth is measured in display cells; tmux color escapes (`#[...]`) are
// stripped before counting so adding color does not push the segment over
// the budget.
//
// `now` is the wall-clock used for staleness detection. Pass time.Time{}
// to disable the marker (e.g. in tests that don't care).
func formatStatusUsage(snaps []usage.Snapshot, maxWidth int, now time.Time) string {
	models := buildModelDisplays(snaps, now)
	if len(models) == 0 {
		return ""
	}
	tiers := []func([]modelDisplay, time.Time) string{
		renderTierLongHUDWithAge,
		renderTierLongHUD,
		renderTierFiveHOnlyHUD,
		renderTierTextLong,
		renderTierTextShort,
	}
	for _, tier := range tiers {
		out := tier(models, now)
		if out == "" {
			continue
		}
		if maxWidth <= 0 || visualLen(out) <= maxWidth {
			return out
		}
	}
	// Last resort: rune-truncate the shortest tier.
	short := renderTierTextShort(models, now)
	return truncateWithEllipsis(short, maxWidth)
}

// renderTierLongHUDWithAge renders the long HUD plus a per-model
// last-sync age indicator. Only models with showAge=true and a
// non-empty rendered age (>= 60s old) emit the indicator — fresh data
// keeps the segment tight.
func renderTierLongHUDWithAge(models []modelDisplay, now time.Time) string {
	return renderLongHUDInternal(models, now, true)
}

// renderTierLongHUD renders the full HUD without the age indicator
// (current-default form). Used as tier 2 once the age block doesn't
// fit the budget.
func renderTierLongHUD(models []modelDisplay, now time.Time) string {
	return renderLongHUDInternal(models, now, false)
}

// renderLongHUDInternal is the shared implementation of the two long
// HUD tiers (with/without age indicator). When withAge is true and the
// model carries showAge + an age >= 60s, the indicator is injected
// between the label and the 5h bar.
func renderLongHUDInternal(models []modelDisplay, now time.Time, withAge bool) string {
	blocks := make([]string, 0, len(models))
	for _, m := range models {
		if !m.hasFive && !m.hasWeek {
			continue
		}
		var b strings.Builder
		b.WriteString("#[fg=cyan,bold]")
		b.WriteString(m.label)
		b.WriteString(statusDefaultReset)
		if withAge {
			if ind := renderAgeIndicator(m, now); ind != "" {
				b.WriteByte(' ')
				b.WriteString(ind)
			}
		}
		first := true
		if m.hasFive {
			b.WriteByte(' ')
			b.WriteString(renderHUDPair("5h", m.fivePct))
			first = false
		}
		if m.hasWeek {
			if first {
				b.WriteByte(' ')
			} else {
				b.WriteString(statusInnerSeparator)
			}
			b.WriteString(renderHUDPair("weekly", m.weekPct))
		}
		b.WriteString(statusDefaultReset)
		blocks = append(blocks, b.String())
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, statusModelSeparator) + statusDefaultReset
}

// renderTierFiveHOnlyHUD drops the weekly bar but keeps the 5h bar.
func renderTierFiveHOnlyHUD(models []modelDisplay, now time.Time) string {
	_ = now
	blocks := make([]string, 0, len(models))
	for _, m := range models {
		if !m.hasFive {
			continue
		}
		var b strings.Builder
		b.WriteString("#[fg=cyan,bold]")
		b.WriteString(m.label)
		b.WriteString(statusDefaultReset)
		b.WriteByte(' ')
		b.WriteString(renderHUDPair("5h", m.fivePct))
		b.WriteString(statusDefaultReset)
		blocks = append(blocks, b.String())
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, statusModelSeparator) + statusDefaultReset
}

// renderTierTextLong drops bars entirely but keeps the long model labels.
func renderTierTextLong(models []modelDisplay, now time.Time) string {
	_ = now
	blocks := make([]string, 0, len(models))
	for _, m := range models {
		text := renderTextPair(m, m.label)
		if text != "" {
			blocks = append(blocks, text)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, statusModelSeparator)
}

// renderTierTextShort uses single-letter labels (legacy form, no color).
func renderTierTextShort(models []modelDisplay, now time.Time) string {
	_ = now
	blocks := make([]string, 0, len(models))
	for _, m := range models {
		text := renderTextPair(m, m.shortLabel)
		if text != "" {
			blocks = append(blocks, text)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, statusModelSeparator)
}

// renderTextPair builds a `<label> 5h:N% weekly:N%` substring used by both
// non-HUD tiers. Returns "" when the model has nothing to show. The
// legacy `~` / `~~` stale markers are not emitted here — the long HUD
// tier carries staleness via the age indicator. The non-JSON `usage`
// table CLI still surfaces a STALE column for verbose inspection.
func renderTextPair(m modelDisplay, label string) string {
	parts := make([]string, 0, 2)
	if m.hasFive {
		parts = append(parts, fmt.Sprintf("5h:%s", percentText(m.fivePct)))
	}
	if m.hasWeek {
		parts = append(parts, fmt.Sprintf("weekly:%s", percentText(m.weekPct)))
	}
	if len(parts) == 0 {
		return ""
	}
	return label + " " + strings.Join(parts, " ")
}

// renderHUDPair renders a single `<window> [bar] N%` substring with color
// escapes. Used for both 5h and weekly pairs in the HUD tiers.
func renderHUDPair(window string, pct float64) string {
	color := usage.BarColorForPct(pct)
	bar := usage.RenderColoredBar(pct, color, usage.BarEmptyColor)
	// `#[default]` after the bar restores label fg before the weekly text. The
	// percent number then re-applies the same color as the bar fill so the
	// numeric matches visually (a red bar's 90% reads red too).
	return fmt.Sprintf("%s%s #[fg=%s]%s%s",
		window+" ", bar, color, percentText(pct), statusDefaultReset)
}

// formatLastSyncAge formats the age payload used by the HUD's
// last-sync indicator. Returns "" for an age below 1 minute (the
// indicator is omitted to keep the bar tight when fresh) so callers
// can branch on the empty string. Otherwise the unit ladders through
// minutes, hours and days.
func formatLastSyncAge(d time.Duration) string {
	if d < time.Minute {
		return ""
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// renderAgeIndicator returns the colored `(<age>)` block injected
// between the model label and the 5h bar. Returns "" when the age is
// fresh (<1m), the model opted out (Codex), or the now/lastSync clock
// is missing. Colours ramp with staleness so the user can spot a
// stagnant cache at a glance:
//
//   - <1h:  dim grey  (#[fg=colour245])
//   - 1-6h: dim yellow (#[fg=yellow])
//   - >=6h: bold red   (#[fg=red,bold])
func renderAgeIndicator(m modelDisplay, now time.Time) string {
	if !m.showAge || now.IsZero() || m.lastSync.IsZero() {
		return ""
	}
	age := now.Sub(m.lastSync)
	text := formatLastSyncAge(age)
	if text == "" {
		return ""
	}
	color := "colour245"
	switch {
	case age >= ageAlertAfter:
		color = "red,bold"
	case age >= ageWarnAfter:
		color = "yellow"
	}
	return fmt.Sprintf("#[fg=%s](%s)%s", color, text, statusDefaultReset)
}

// percentText formats a percentage. Negative values are clamped to 0.
// Above 100 we still print the actual number (the bar saturates, the
// numeric does not — the user must see how far over they are).
func percentText(pct float64) string {
	if pct < 0 {
		pct = 0
	}
	return fmt.Sprintf("%.0f%%", pct)
}

// buildModelDisplays converts snapshots into the canonical-ordered display
// rows the renderers iterate over. In schema v2 the Pct value is the
// authoritative percentage from the upstream API, so we no longer drop
// rows by Limit. We still drop rows whose Pct is exactly zero AND whose
// ResetsAt is the zero time — those are placeholders that represent "no
// data" rather than a genuine 0% (e.g. when the upstream returned
// `seven_day_oauth_apps: null`).
func buildModelDisplays(snaps []usage.Snapshot, now time.Time) []modelDisplay {
	byModel := make(map[string]*modelDisplay)
	order := make([]string, 0, 2)
	for i := range snaps {
		s := snaps[i]
		if s.Pct == 0 && s.ResetsAt.IsZero() && s.Limit == 0 {
			continue
		}
		row, ok := byModel[s.Model]
		if !ok {
			row = &modelDisplay{
				model:      s.Model,
				label:      modelDisplayLabel(s.Model),
				shortLabel: modelShortLabel(s.Model),
			}
			byModel[s.Model] = row
			order = append(order, s.Model)
		}
		level := staleLevel(s, now)
		switch s.Window {
		case usage.Window5h:
			row.hasFive = true
			row.fivePct = s.Pct
			row.fiveStale = level
		case usage.WindowWeekly:
			row.hasWeek = true
			row.weekPct = s.Pct
			row.weekStale = level
		}
		// lastSync tracks the most recent successful refresh
		// across the model's rows; the long HUD tier renders its
		// age relative to `now`. Codex reads from the latest
		// rollout file every call so its age is always near "now"
		// — uninteresting for the HUD, so showAge stays false.
		if !s.UpdatedAt.IsZero() && s.UpdatedAt.After(row.lastSync) {
			row.lastSync = s.UpdatedAt
		}
		if strings.EqualFold(s.Model, "claude") {
			row.showAge = true
		}
	}
	canonical := []string{"claude", "codex"}
	listed := make(map[string]bool, len(canonical))
	out := make([]modelDisplay, 0, len(byModel))
	for _, m := range canonical {
		if row, ok := byModel[m]; ok {
			out = append(out, *row)
			listed[m] = true
		}
	}
	for _, m := range order {
		if listed[m] {
			continue
		}
		out = append(out, *byModel[m])
	}
	return out
}

// modelDisplayLabel maps a model name to its capitalized HUD label.
func modelDisplayLabel(model string) string {
	switch strings.ToLower(model) {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	default:
		if model == "" {
			return "?"
		}
		// Title-case the first rune; rest stays lowercase.
		rs := []rune(strings.ToLower(model))
		rs[0] = []rune(strings.ToUpper(string(rs[0])))[0]
		return string(rs)
	}
}

// modelShortLabel returns the one-letter legacy fallback used by the
// narrowest text tier. Stable across models so the segment stays scannable
// when squeezed.
func modelShortLabel(model string) string {
	switch strings.ToLower(model) {
	case "claude":
		return "C"
	case "codex":
		return "X"
	default:
		if model == "" {
			return "?"
		}
		return strings.ToUpper(model[:1])
	}
}

// truncateWithEllipsis hard-truncates s so its visual length is at most
// maxWidth, appending an ellipsis when truncation actually occurred. Tmux
// color escapes are stripped first so we never split inside an escape.
func truncateWithEllipsis(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	plain := stripTmuxEscapes(s)
	if visualLen(plain) <= maxWidth {
		return plain
	}
	rs := []rune(plain)
	if maxWidth == 1 {
		return string(rs[:1])
	}
	return string(rs[:maxWidth-1]) + "…"
}

// visualLen returns the rune count of s after stripping tmux `#[...]`
// escape sequences. The HUD format strings only contain single-cell runes
// (ASCII + `█` + `░` + `·`), so rune count matches display width 1:1.
func visualLen(s string) int {
	return len([]rune(stripTmuxEscapes(s)))
}

// stripTmuxEscapes removes `#[...]` escape sequences from s. A literal `#`
// followed by a non-`[` is preserved untouched so user content with `#` is
// not mangled.
func stripTmuxEscapes(s string) string {
	if !strings.Contains(s, "#[") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '#' && i+1 < len(s) && s[i+1] == '[' {
			end := strings.IndexByte(s[i+2:], ']')
			if end < 0 {
				// Unterminated escape — emit verbatim and stop scanning.
				b.WriteString(s[i:])
				break
			}
			i += 2 + end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func runeLen(s string) int {
	return len([]rune(s))
}

func printUsageHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux usage [--model codex|claude|all] [--window 5h|weekly|all] [--json] [--force|-f]")
	fmt.Fprintln(w, "  projmux status usage [--max-width N] [--force|-f]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --force, -f   bypass per-adapter throttle and clear active backoff before refreshing.")
	fmt.Fprintln(w, "                Useful when bound to a tmux key as a manual 'refresh now' gesture.")
}

// writeBackoffNote appends a one-line note to stdout when an adapter
// is currently in backoff. The note tells the user how long until the
// adapter's next attempt and points them at `--force`. No-op when no
// adapter is in backoff so healthy installs see a clean table.
func writeBackoffNote(w io.Writer, state usage.State, now time.Time) {
	if now.IsZero() {
		return
	}
	// Stable ordering across runs.
	names := make([]string, 0, len(state.Backoff))
	for name := range state.Backoff {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		bs := state.Backoff[name]
		if bs.Until.IsZero() || !now.Before(bs.Until) {
			continue
		}
		remaining := bs.Until.Sub(now).Round(time.Minute)
		if remaining <= 0 {
			remaining = bs.Until.Sub(now).Round(time.Second)
		}
		fmt.Fprintf(w, "%s is in backoff, try again in %s (use --force to bypass)\n", name, formatBackoffDuration(remaining))
	}
}

// formatBackoffDuration renders a duration in compact form (e.g. "12m",
// "1h3m", "45s"). Designed to round-trip through both the table note
// and the HUD without ever emitting the noisy default Go form.
func formatBackoffDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		secs := max(int(d.Round(time.Second).Seconds()), 1)
		return fmt.Sprintf("%ds", secs)
	}
	hours := int(d / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	if hours == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}
