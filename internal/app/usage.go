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

// Run implements `projmux usage [...]`.
func (c *usageCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", "all", "filter by model: codex | claude | all")
	window := fs.String("window", "all", "filter by window: 5h | weekly | all")
	asJSON := fs.Bool("json", false, "emit a JSON array instead of the tab-aligned table")
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
	snaps, collectErr := mgr.Collect(context.Background())
	// collectErr is informational — adapters that fail still keep the rest
	// of the rendering pipeline working. We surface a single warning to
	// stderr so the user knows partial data was used.
	if collectErr != nil {
		fmt.Fprintf(stderr, "usage: warning: %v\n", collectErr)
	}

	filtered := filterSnapshots(snaps, *model, *window)
	filtered = usage.SortedSnapshots(filtered)

	if *asJSON {
		return writeUsageJSON(stdout, filtered)
	}
	return writeUsageTable(stdout, filtered)
}

// runStatus implements the `projmux status usage` subcommand. It reads from
// the persisted cache (no adapter walk) so it is safe to call from the
// tmux status interval.
func (c *usageCommand) runStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxWidth := fs.Int("max-width", 0, "truncate output to N runes (0 = no truncation)")
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
	snaps, err := mgr.LoadAll()
	if err != nil {
		return nil
	}

	out := formatStatusUsage(snaps, *maxWidth)
	if out == "" {
		return nil
	}
	_, err = fmt.Fprint(stdout, out)
	return err
}

func (c *usageCommand) defaultManager() (*usage.Manager, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("usage: resolve config paths: %w", err)
	}
	registry := usage.NewRegistry()
	if err := registry.Register(claudeadapter.New()); err != nil {
		return nil, fmt.Errorf("usage: register claude adapter: %w", err)
	}
	if err := registry.Register(codexadapter.New()); err != nil {
		return nil, fmt.Errorf("usage: register codex adapter: %w", err)
	}
	limits, err := usage.LoadLimits(c.env(usage.LimitsEnvVar))
	if err != nil {
		return nil, err
	}
	store := usage.NewStore(filepath.Join(paths.StateDir, "usage"))
	return usage.NewManager(registry, store, limits, c.now), nil
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

func writeUsageJSON(w io.Writer, snaps []usage.Snapshot) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if snaps == nil {
		return enc.Encode([]usage.Snapshot{})
	}
	return enc.Encode(snaps)
}

func writeUsageTable(w io.Writer, snaps []usage.Snapshot) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "MODEL\tWINDOW\tTOKENS\tPCT\tRESETS_AT"); err != nil {
		return err
	}
	for _, s := range snaps {
		pct := "-"
		if s.Limit > 0 {
			pct = fmt.Sprintf("%.0f%%", s.Pct)
		}
		resets := "-"
		if !s.ResetsAt.IsZero() {
			resets = s.ResetsAt.Local().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", s.Model, s.Window, s.Tokens, pct, resets); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// HUD layout constants. Separators are constant runes so visualLen can
// reliably count cells without ever falling back to the tmux-escape
// stripper.
const (
	statusInnerSeparator = " · " // between 5h and wk inside a model.
	statusModelSeparator = "   " // 3 spaces between Claude and Codex blocks.
	statusDefaultReset   = "#[default]"
)

// modelDisplay is the in-memory representation of a single model's
// snapshots. Pct values are floats so the >100% over-limit branch can
// surface the actual number (e.g. `319%`) instead of capping at 100%.
type modelDisplay struct {
	model      string // canonical lowercase key.
	label      string // user-facing label (Claude / Codex / ...).
	shortLabel string // legacy single-letter (C / X / ...).
	hasFive    bool
	fivePct    float64
	hasWeek    bool
	weekPct    float64
}

// formatStatusUsage produces the HUD-style tmux status segment. The output
// degrades gracefully through five tiers:
//
//  1. Long form with bars + wk: `Claude 5h [████████░░] 80% · wk [...]`
//  2. Drop wk bars (label + 5h bar only).
//  3. Drop bars entirely (`Claude 5h:80% wk:30%`).
//  4. Single-letter labels (`C 5h:80% wk:30%`).
//  5. Hard rune-truncation with trailing `…`.
//
// maxWidth is measured in display cells; tmux color escapes (`#[...]`) are
// stripped before counting so adding color does not push the segment over
// the budget.
func formatStatusUsage(snaps []usage.Snapshot, maxWidth int) string {
	models := buildModelDisplays(snaps)
	if len(models) == 0 {
		return ""
	}
	tiers := []func([]modelDisplay) string{
		renderTierLongHUD,
		renderTierFiveHOnlyHUD,
		renderTierTextLong,
		renderTierTextShort,
	}
	for _, tier := range tiers {
		out := tier(models)
		if out == "" {
			continue
		}
		if maxWidth <= 0 || visualLen(out) <= maxWidth {
			return out
		}
	}
	// Last resort: rune-truncate the shortest tier.
	short := renderTierTextShort(models)
	return truncateWithEllipsis(short, maxWidth)
}

// renderTierLongHUD renders the full HUD: label + 5h bar + wk bar per model.
func renderTierLongHUD(models []modelDisplay) string {
	blocks := make([]string, 0, len(models))
	for _, m := range models {
		if !m.hasFive && !m.hasWeek {
			continue
		}
		var b strings.Builder
		b.WriteString("#[fg=cyan,bold]")
		b.WriteString(m.label)
		b.WriteString(statusDefaultReset)
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
			b.WriteString(renderHUDPair("wk", m.weekPct))
		}
		b.WriteString(statusDefaultReset)
		blocks = append(blocks, b.String())
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, statusModelSeparator) + statusDefaultReset
}

// renderTierFiveHOnlyHUD drops the wk bar but keeps the 5h bar.
func renderTierFiveHOnlyHUD(models []modelDisplay) string {
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
func renderTierTextLong(models []modelDisplay) string {
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
func renderTierTextShort(models []modelDisplay) string {
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

// renderTextPair builds a `<label> 5h:N% wk:N%` substring used by both
// non-HUD tiers. Returns "" when the model has nothing to show.
func renderTextPair(m modelDisplay, label string) string {
	parts := make([]string, 0, 2)
	if m.hasFive {
		parts = append(parts, fmt.Sprintf("5h:%s", percentText(m.fivePct)))
	}
	if m.hasWeek {
		parts = append(parts, fmt.Sprintf("wk:%s", percentText(m.weekPct)))
	}
	if len(parts) == 0 {
		return ""
	}
	return label + " " + strings.Join(parts, " ")
}

// renderHUDPair renders a single `<window> [bar] N%` substring with color
// escapes. Used for both 5h and wk pairs in the HUD tiers.
func renderHUDPair(window string, pct float64) string {
	color := usage.BarColorForPct(pct)
	bar := usage.RenderColoredBar(pct, color, usage.BarEmptyColor)
	// `#[default]` after the bar restores label fg before the wk text. The
	// percent number then re-applies the same color as the bar fill so the
	// numeric matches visually (a red bar's 90% reads red too).
	return fmt.Sprintf("%s%s #[fg=%s]%s%s",
		window+" ", bar, color, percentText(pct), statusDefaultReset)
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
// rows the renderers iterate over. Snapshots without a Limit are dropped
// because we cannot compute a percentage to display.
func buildModelDisplays(snaps []usage.Snapshot) []modelDisplay {
	byModel := make(map[string]*modelDisplay)
	order := make([]string, 0, 2)
	for i := range snaps {
		s := snaps[i]
		if s.Limit <= 0 {
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
		switch s.Window {
		case usage.Window5h:
			row.hasFive = true
			row.fivePct = s.Pct
		case usage.WindowWeekly:
			row.hasWeek = true
			row.weekPct = s.Pct
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
	fmt.Fprintln(w, "  projmux usage [--model codex|claude|all] [--window 5h|weekly|all] [--json]")
	fmt.Fprintln(w, "  projmux status usage [--max-width N]")
}
