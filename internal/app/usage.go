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

// formatStatusUsage produces the one-line tmux status segment described in
// the spec: `c:42% w:18% | x:71% w:55%`.
//
//   - claude => c:, codex => x:.
//   - First number is the 5h window, second is the weekly window.
//   - A model with no snapshots (or no limits to compute pct) is omitted.
//   - When maxWidth > 0 and the rendered output exceeds that rune count,
//     trailing groups are dropped first and, if still too long, the last
//     group is truncated and ellipsised.
func formatStatusUsage(snaps []usage.Snapshot, maxWidth int) string {
	groups := buildStatusGroups(snaps)
	if len(groups) == 0 {
		return ""
	}
	full := strings.Join(groups, " | ")
	if maxWidth <= 0 || runeLen(full) <= maxWidth {
		return full
	}
	// Drop trailing groups until it fits.
	for len(groups) > 1 {
		groups = groups[:len(groups)-1]
		candidate := strings.Join(groups, " | ")
		if runeLen(candidate) <= maxWidth {
			return candidate
		}
	}
	// Last resort: truncate the single remaining group.
	only := groups[0]
	if runeLen(only) <= maxWidth {
		return only
	}
	if maxWidth <= 1 {
		return string([]rune(only)[:maxWidth])
	}
	rs := []rune(only)
	return string(rs[:maxWidth-1]) + "…"
}

// statusGroup builds the per-model substring like "c:42% w:18%". Returns ""
// when the model has no usable snapshots.
func statusGroup(prefix string, fiveH, weekly *usage.Snapshot) string {
	parts := make([]string, 0, 2)
	if fiveH != nil && fiveH.Limit > 0 {
		parts = append(parts, fmt.Sprintf("%s:%.0f%%", prefix, fiveH.Pct))
	}
	if weekly != nil && weekly.Limit > 0 {
		parts = append(parts, fmt.Sprintf("w:%.0f%%", weekly.Pct))
	}
	return strings.Join(parts, " ")
}

// statusPrefix maps known model names to their single-letter status prefix.
// Unknown models get the first letter of their name as a graceful fallback.
func statusPrefix(model string) string {
	switch strings.ToLower(model) {
	case "claude":
		return "c"
	case "codex":
		return "x"
	default:
		if model == "" {
			return "?"
		}
		return strings.ToLower(model[:1])
	}
}

// buildStatusGroups groups snapshots by model in the canonical claude-then-
// codex order and renders each group.
func buildStatusGroups(snaps []usage.Snapshot) []string {
	byModel := make(map[string]map[usage.Window]*usage.Snapshot)
	order := make([]string, 0, 2)
	for i := range snaps {
		s := snaps[i]
		row, ok := byModel[s.Model]
		if !ok {
			row = make(map[usage.Window]*usage.Snapshot, 2)
			byModel[s.Model] = row
			order = append(order, s.Model)
		}
		row[s.Window] = &s
	}
	// Stable canonical order: claude first, codex second, anything else after
	// in first-seen order.
	canonical := []string{"claude", "codex"}
	priority := map[string]int{}
	for i, m := range canonical {
		priority[m] = i
	}
	sortedModels := make([]string, 0, len(order))
	for _, m := range canonical {
		if _, ok := byModel[m]; ok {
			sortedModels = append(sortedModels, m)
		}
	}
	for _, m := range order {
		if _, listed := priority[m]; listed {
			continue
		}
		sortedModels = append(sortedModels, m)
	}
	groups := make([]string, 0, len(sortedModels))
	for _, m := range sortedModels {
		row := byModel[m]
		group := statusGroup(statusPrefix(m), row[usage.Window5h], row[usage.WindowWeekly])
		if group != "" {
			groups = append(groups, group)
		}
	}
	return groups
}

func runeLen(s string) int {
	return len([]rune(s))
}

func printUsageHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux usage [--model codex|claude|all] [--window 5h|weekly|all] [--json]")
	fmt.Fprintln(w, "  projmux status usage [--max-width N]")
}
