// Package usagecmd implements the `projmux usage` and `projmux status usage`
// command surfaces on top of internal/core/usage. It owns the CLI flag
// parsing, adapter registry wiring, and the HUD/table/JSON rendering tiers.
package usagecmd

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
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
	antigravityadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/antigravity"
	claudeadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/claude"
	codexadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/codex"
	"github.com/crevissepartners/projmux/internal/theme"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

// Command exposes the `projmux usage` and `projmux status usage`
// surfaces. Both share a single Manager so collect-once-render-twice stays
// cheap.
type Command struct {
	managerFn       func([]string) (*usage.Manager, error)
	enabledAgentsFn func() ([]config.AIAgentProvider, error)
	now             func() time.Time
	lookupEnv       func(string) string
}

// New builds the usage command. now is the wall clock injected into the
// Manager and staleness rendering; nil falls back to time.Now.
func New(now func() time.Time) *Command {
	if now == nil {
		now = time.Now
	}
	return &Command{
		now:       now,
		lookupEnv: os.Getenv,
	}
}

// statusRoles is the tmux-side semantic role map consumed by the status usage
// HUD renderers. It defaults to the fallback role map, whose values equal the
// historical literals (byte-identical); ApplyStatusTheme repoints it at the
// resolved effective theme at command entry (bright Phase 2, B1). The HUD
// render is single-threaded per CLI invocation, matching the app package's
// native-UI role var pattern.
var statusRoles = theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{}))

// ApplyStatusTheme repoints the usage HUD role map at a resolved effective
// theme. Applying the fallback theme restores byte-identity.
func ApplyStatusTheme(effective theme.EffectiveTheme) {
	statusRoles = theme.RenderRolesFromEffective(effective)
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
func (c *Command) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", "all", "filter by model: codex | claude | antigravity | all")
	window := fs.String("window", "all", "filter by window: 5h | weekly | context | quota | all")
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

	modelScope, explicitModel := c.modelScope(*model)
	mgr, err := c.managerForScope(modelScope)
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
	if len(modelScope) > 0 {
		if *force {
			snaps, collectErr = mgr.ForceCollect(context.Background())
		} else {
			snaps, collectErr = mgr.Collect(context.Background())
		}
	} else {
		snaps, _ = mgr.LoadAll()
	}
	// collectErr is informational — adapters that fail still keep the rest
	// of the rendering pipeline working. We surface a single warning to
	// stderr so the user knows partial data was used.
	if collectErr != nil {
		fmt.Fprintf(stderr, "usage: warning: %v\n", collectErr)
	}

	// Conversation-local context fullness is diagnostic hook metadata, not
	// account usage. Suppress legacy cached context rows before every CLI
	// rendering mode while retaining lossless named account quota buckets.
	filtered := filterSnapshots(accountUsageSnapshots(snaps), *model, *window)
	if !explicitModel {
		filtered = filterSnapshotsByModels(filtered, modelScope)
	}
	filtered = usage.SortedSnapshots(filtered)

	state, _ := mgr.LoadState()
	state = filterUsageStateByModels(state, modelScope)
	unsupported := c.unsupportedUsageProviders(*model, explicitModel)

	if *asJSON {
		return writeUsageJSON(stdout, filtered, state, c.now())
	}
	if err := writeUsageTable(stdout, filtered, c.now()); err != nil {
		return err
	}
	writeUsageUnsupportedNotes(stdout, unsupported)
	if !explicitModel && len(modelScope) == 0 {
		if len(unsupported) == 0 {
			fmt.Fprintln(stdout, "no AI usage providers enabled; enable Claude or Codex in Settings > AI Settings > Enabled agents")
		}
		return nil
	}
	// Surface backoff status in the human-readable table form so users
	// can see why their numbers aren't refreshing. The --json form
	// already includes a `backoff` block.
	writeBackoffNote(stdout, state, c.now())
	return nil
}

// statusRefreshThrottle is the minimum interval between adapter walks
// triggered by `projmux status usage` or the manual HUD refresh key. tmux
// refreshes the status line every 5s by default; 30s gives the cache enough
// breathing room while keeping the displayed numbers fresh on a human
// timescale. Adapters that implement ThrottleHinter (e.g. Claude → 60s)
// override this floor on a per-adapter basis inside the Manager.
const statusRefreshThrottle = 30 * time.Second

// usageDebugEnvVar gates whether MaybeCollect's swallowed adapter error is
// echoed to stderr. Off by default — the status segment must stay silent on
// a healthy install.
const usageDebugEnvVar = "PROJMUX_USAGE_DEBUG"

// MaybeCollect performs the same opportunistic, throttled adapter walk used
// by the status-line renderer without rendering any output. It is the narrow
// entry point used by explicit UI refresh gestures that must respect the
// existing per-adapter throttle hints and backoff state.
func (c *Command) MaybeCollect(ctx context.Context) (bool, error) {
	modelScope := c.ambientModelScope()
	if len(modelScope) == 0 {
		return false, nil
	}
	mgr, err := c.managerForScope(modelScope)
	if err != nil {
		return false, err
	}
	return mgr.MaybeCollect(ctx, statusRefreshThrottle)
}

// RunStatus implements the `projmux status usage` subcommand. It triggers
// an opportunistic, throttled cache refresh (so a fresh install or a stale
// cache self-heals on the next tmux redraw) and then reads the persisted
// cache to render the HUD segment. Adapter failures during this hot path
// are swallowed unless PROJMUX_USAGE_DEBUG is set.
func (c *Command) RunStatus(args []string, stdout, stderr io.Writer) error {
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

	modelScope := c.ambientModelScope()
	if len(modelScope) == 0 {
		return nil
	}
	mgr, err := c.managerForScope(modelScope)
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
	snaps = filterSnapshotsByModels(snaps, modelScope)

	out := formatStatusUsage(snaps, *maxWidth, c.now())
	if out == "" {
		return nil
	}
	_, err = fmt.Fprint(stdout, out)
	return err
}

// StateDirEnvVar lets multi-machine users redirect the snapshot cache to
// a synced location (Dropbox, iCloud Drive, etc) so the HUD on every box
// reflects whichever machine collected most recently.
const StateDirEnvVar = "PROJMUX_USAGE_STATE_DIR"

// limitsEnvVar is documented as deprecated in v2: authoritative limits
// come from the upstream APIs themselves, so the override file has no
// effect. We accept the env var silently rather than rejecting it so
// existing user configs keep working.
const limitsEnvVar = "PROJMUX_USAGE_LIMITS_PATH"

func (c *Command) managerForScope(modelScope []string) (*usage.Manager, error) {
	if c.managerFn != nil {
		return c.managerFn(modelScope)
	}
	return c.defaultManager(modelScope)
}

func (c *Command) defaultManager(modelScope []string) (*usage.Manager, error) {
	// Resolve the state dir up front: the Antigravity adapter reads its
	// context sidecar from the same directory the snapshot cache lives in,
	// so it needs the resolved path at construction time.
	stateDir, err := c.resolveStateDir()
	if err != nil {
		return nil, err
	}
	registry := usage.NewRegistry()
	for _, model := range normalizeUsageModelScope(modelScope) {
		provider, ok := aiprovider.Lookup(model)
		if !ok || !provider.UsageSupported || provider.UsageModel != model {
			continue
		}
		switch provider.ID {
		case aiprovider.Claude:
			if err := registry.Register(claudeadapter.New()); err != nil {
				return nil, fmt.Errorf("usage: register claude adapter: %w", err)
			}
		case aiprovider.Codex:
			if err := registry.Register(codexadapter.New()); err != nil {
				return nil, fmt.Errorf("usage: register codex adapter: %w", err)
			}
		case aiprovider.Antigravity:
			if err := registry.Register(antigravityadapter.New(stateDir)); err != nil {
				return nil, fmt.Errorf("usage: register antigravity adapter: %w", err)
			}
		}
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
func (c *Command) resolveStateDir() (string, error) {
	if override := strings.TrimSpace(c.env(StateDirEnvVar)); override != "" {
		return override, nil
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return "", fmt.Errorf("usage: resolve config paths: %w", err)
	}
	return filepath.Join(paths.StateDir, "usage"), nil
}

// CachedState loads the persisted usage state scoped to the ambient
// enabled agents without triggering any adapter collection. It returns the
// filtered state, the enabled providers whose usage is unsupported, and the
// mtime of the on-disk snapshot cache (zero when the cache is missing).
// Used by the statusbar usage popup, which renders from cache only.
func (c *Command) CachedState() (usage.State, []UnsupportedProvider, time.Time, error) {
	modelScope := c.ambientModelScope()
	mgr, err := c.managerForScope(modelScope)
	if err != nil {
		return usage.State{}, nil, time.Time{}, err
	}
	state, err := mgr.LoadState()
	if err != nil {
		return usage.State{}, nil, time.Time{}, err
	}
	state = filterUsageStateByModels(state, modelScope)
	unsupported := c.unsupportedUsageProviders("all", false)
	var cacheMTime time.Time
	if stateDir, err := c.resolveStateDir(); err == nil {
		if info, statErr := os.Stat(usage.NewStore(stateDir).FilePath()); statErr == nil {
			cacheMTime = info.ModTime()
		}
	}
	return state, unsupported, cacheMTime, nil
}

func (c *Command) modelScope(model string) ([]string, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		model = "all"
	}
	switch model {
	case "all":
		return c.ambientModelScope(), false
	default:
		if provider, ok := aiprovider.Lookup(model); ok && provider.UsageSupported && provider.UsageModel == model {
			return []string{model}, true
		}
		return nil, true
	}
}

func (c *Command) ambientModelScope() []string {
	return aiAgentProvidersToUsageModels(c.currentUsageEnabledAgents())
}

func (c *Command) currentUsageEnabledAgents() []config.AIAgentProvider {
	if c.enabledAgentsFn != nil {
		agents, err := c.enabledAgentsFn()
		if err == nil {
			return normalizeAIAgentProviders(agents)
		}
	}
	if c.managerFn != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	agents, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	return normalizeAIAgentProviders(agents)
}

func normalizeAIAgentProviders(agents []config.AIAgentProvider) []config.AIAgentProvider {
	values := make([]string, 0, len(agents))
	for _, agent := range agents {
		values = append(values, string(agent))
	}
	return config.NormalizeAIEnabledAgents(values)
}

func aiAgentProvidersToUsageModels(agents []config.AIAgentProvider) []string {
	out := make([]string, 0, len(agents))
	for _, agent := range normalizeAIAgentProviders(agents) {
		provider, ok := aiprovider.Lookup(string(agent))
		if !ok || !provider.UsageSupported || strings.TrimSpace(provider.UsageModel) == "" {
			continue
		}
		out = append(out, provider.UsageModel)
	}
	return out
}

// UnsupportedProvider describes an enabled AI agent whose usage cannot be
// reported (no supported adapter). Rendered as a one-line note by the table
// form and the statusbar usage popup.
type UnsupportedProvider struct {
	Model  string
	Label  string
	Reason string
}

func (c *Command) unsupportedUsageProviders(model string, explicitModel bool) []UnsupportedProvider {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		model = "all"
	}
	if explicitModel {
		provider, ok := aiprovider.Lookup(model)
		if !ok || provider.UsageSupported {
			return nil
		}
		return []UnsupportedProvider{unsupportedUsageProviderFor(provider)}
	}
	out := make([]UnsupportedProvider, 0)
	for _, agent := range c.currentUsageEnabledAgents() {
		provider, ok := aiprovider.Lookup(string(agent))
		if !ok || provider.UsageSupported {
			continue
		}
		out = append(out, unsupportedUsageProviderFor(provider))
	}
	return out
}

func unsupportedUsageProviderFor(provider aiprovider.Metadata) UnsupportedProvider {
	return UnsupportedProvider{
		Model:  string(provider.ID),
		Label:  provider.DisplayName,
		Reason: "no supported usage adapter",
	}
}

func writeUsageUnsupportedNotes(w io.Writer, providers []UnsupportedProvider) {
	for _, provider := range providers {
		label := strings.TrimSpace(provider.Label)
		if label == "" {
			label = provider.Model
		}
		fmt.Fprintf(w, "%s usage unsupported: %s\n", label, provider.Reason)
	}
}

func (c *Command) env(name string) string {
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

func filterSnapshotsByModels(snaps []usage.Snapshot, models []string) []usage.Snapshot {
	allowed := usageModelSet(models)
	if len(allowed) == 0 {
		return nil
	}
	out := snaps[:0:0]
	for _, s := range snaps {
		if allowed[strings.ToLower(strings.TrimSpace(s.Model))] {
			out = append(out, s)
		}
	}
	return out
}

// accountUsageSnapshots is the shared public-surface boundary between
// account quota and conversation-local diagnostic state. WindowContext was
// emitted by older Antigravity adapters and may remain in snapshots.json; it
// must not reappear in text, JSON, or popup usage views. Named quota identity
// remains untouched.
func accountUsageSnapshots(snaps []usage.Snapshot) []usage.Snapshot {
	out := make([]usage.Snapshot, 0, len(snaps))
	for _, snapshot := range snaps {
		if snapshot.Window == usage.WindowContext {
			continue
		}
		out = append(out, snapshot)
	}
	return out
}

func filterUsageStateByModels(state usage.State, models []string) usage.State {
	allowed := usageModelSet(models)
	if len(allowed) == 0 {
		return usage.State{
			LastCollect: map[string]time.Time{},
			Backoff:     map[string]usage.BackoffState{},
		}
	}
	out := usage.State{
		Snapshots:   accountUsageSnapshots(filterSnapshotsByModels(state.Snapshots, models)),
		LastCollect: map[string]time.Time{},
		Backoff:     map[string]usage.BackoffState{},
	}
	for name, value := range state.LastCollect {
		if allowed[strings.ToLower(strings.TrimSpace(name))] {
			out.LastCollect[name] = value
		}
	}
	for name, value := range state.Backoff {
		if allowed[strings.ToLower(strings.TrimSpace(name))] {
			out.Backoff[name] = value
		}
	}
	return out
}

func usageModelSet(models []string) map[string]bool {
	scope := normalizeUsageModelScope(models)
	out := make(map[string]bool, len(scope))
	for _, model := range scope {
		out[model] = true
	}
	return out
}

func normalizeUsageModelScope(models []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
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

	snaps = accountUsageSnapshots(snaps)
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
	snaps = accountUsageSnapshots(snaps)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "MODEL\tWINDOW\tPCT\tRESETS_AT\tRESET_IN\tSTALE"); err != nil {
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
		resetIn := ResetInText(s.ResetInSeconds)
		stale := ""
		if isStale(s, now) {
			stale = "*"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Model, SnapshotWindowLabel(s), pct, resets, resetIn, stale); err != nil {
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

// HUD layout constants. Separators are constant runes so render.VisualLen
// can reliably count cells without ever falling back to the tmux-escape
// stripper.
const (
	statusInnerSeparator = " · " // between 5h and weekly inside a model.
	statusModelSeparator = "   " // 3 spaces between Claude and Codex blocks.
	statusDefaultReset   = "#[default]"
)

// Age-indicator colour threshold for the Claude HUD block. The age indicator
// carries the staleness signal (it replaces the legacy `~` / `~~` markers)
// and stays muted so usage threshold warning/critical colors remain reserved:
//
//   - age <  ageWarnAfter (1h) → dim grey
//   - age >= ageWarnAfter (1h) → muted grey
const (
	ageWarnAfter = 1 * time.Hour
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
//  3. Keep one primary bar per provider (5h, otherwise weekly).
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
	models := buildModelDisplays(projectStatusSnapshots(snaps), now)
	if len(models) == 0 {
		return ""
	}
	tiers := []func([]modelDisplay, time.Time) string{
		renderTierLongHUDWithAge,
		renderTierLongHUD,
		renderTierPrimaryHUD,
		renderTierTextLong,
		renderTierTextShort,
	}
	for _, tier := range tiers {
		out := tier(models, now)
		if out == "" {
			continue
		}
		if maxWidth <= 0 || intrender.VisualLen(out) <= maxWidth {
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
		b.WriteString("#[fg=" + statusRoles.AccentAIFg + ",bold]")
		b.WriteString(m.label)
		b.WriteString(statusDefaultReset)
		if withAge {
			if ind := renderAgeIndicator(m, now); ind != "" {
				b.WriteByte(' ')
				b.WriteString(ind)
			}
		}
		first := true
		writePair := func(window string, pct float64) {
			if first {
				b.WriteByte(' ')
				first = false
			} else {
				b.WriteString(statusInnerSeparator)
			}
			b.WriteString(renderHUDPair(window, pct))
		}
		if m.hasFive {
			writePair("5h", m.fivePct)
		}
		if m.hasWeek {
			writePair("weekly", m.weekPct)
		}
		b.WriteString(statusDefaultReset)
		blocks = append(blocks, b.String())
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, statusModelSeparator) + statusDefaultReset
}

// renderTierPrimaryHUD keeps one official-window bar per provider: 5h when
// present, otherwise weekly. This preserves weekly-only providers at every
// provider-preserving tier before hard truncation.
func renderTierPrimaryHUD(models []modelDisplay, now time.Time) string {
	_ = now
	blocks := make([]string, 0, len(models))
	for _, m := range models {
		window := ""
		var pct float64
		switch {
		case m.hasFive:
			window, pct = "5h", m.fivePct
		case m.hasWeek:
			window, pct = "weekly", m.weekPct
		default:
			continue
		}
		var b strings.Builder
		b.WriteString("#[fg=" + statusRoles.AccentAIFg + ",bold]")
		b.WriteString(m.label)
		b.WriteString(statusDefaultReset)
		b.WriteByte(' ')
		b.WriteString(renderHUDPair(window, pct))
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
		parts = append(parts, fmt.Sprintf("5h:%s", PercentText(m.fivePct)))
	}
	if m.hasWeek {
		parts = append(parts, fmt.Sprintf("weekly:%s", PercentText(m.weekPct)))
	}
	if len(parts) == 0 {
		return ""
	}
	return label + " " + strings.Join(parts, " ")
}

// renderHUDPair renders a single `<window> [bar] N%` substring with color
// escapes. Used for both 5h and weekly pairs in the HUD tiers.
func renderHUDPair(window string, pct float64) string {
	// State/severity colors come from the semantic role map (single source
	// shared with notify/statusbar), repointed at the resolved effective theme
	// by ApplyStatusTheme at command entry (bright Phase 2, B1).
	roles := statusRoles
	color := intrender.BarColorForPct(pct, roles)
	bar := intrender.RenderColoredBar(pct, color, intrender.BarEmptyColorForRoles(roles))
	// `#[default]` after the bar restores label fg before the weekly text. The
	// percent number then re-applies the same color as the bar fill so the
	// numeric matches visually (a red bar's 90% reads red too).
	return fmt.Sprintf("%s%s #[fg=%s]%s%s",
		window+" ", bar, color, PercentText(pct), statusDefaultReset)
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
// is missing. Staleness stays muted here so warning/critical colors are
// reserved for usage thresholds:
//
//   - <1h:  secondary text
//   - >=1h: muted text
func renderAgeIndicator(m modelDisplay, now time.Time) string {
	if !m.showAge || now.IsZero() || m.lastSync.IsZero() {
		return ""
	}
	age := now.Sub(m.lastSync)
	text := formatLastSyncAge(age)
	if text == "" {
		return ""
	}
	color := statusRoles.StatusTextSecondary
	if age >= ageWarnAfter {
		color = statusRoles.StatusTextMuted
	}
	return fmt.Sprintf("#[fg=%s](%s)%s", color, text, statusDefaultReset)
}

// PercentText formats a percentage. Negative values are clamped to 0.
// Above 100 we still print the actual number (the bar saturates, the
// numeric does not — the user must see how far over they are).
func PercentText(pct float64) string {
	if pct < 0 {
		pct = 0
	}
	return fmt.Sprintf("%.0f%%", pct)
}

// SnapshotWindowLabel distinguishes fixed windows, conversation context, and
// opaque account-quota IDs without inventing a cadence alias for the latter.
func SnapshotWindowLabel(snapshot usage.Snapshot) string {
	if snapshot.Window == usage.WindowQuota {
		return "quota/" + BucketDisplayID(snapshot.Bucket)
	}
	return string(snapshot.Window)
}

// BucketDisplayID escapes control characters and quotes without changing the
// opaque ID stored in Snapshot.Bucket. Removing only the surrounding JSON
// quotes keeps ordinary IDs compact while keeping escaped and literal text
// distinct (newline -> `\n`, backslash+n -> `\\n`).
func BucketDisplayID(id string) string {
	quoted := strconv.QuoteToGraphic(id)
	if len(quoted) >= 2 {
		return strings.ReplaceAll(quoted[1:len(quoted)-1], "#", `\x23`)
	}
	return strings.ReplaceAll(quoted, "#", `\x23`)
}

// ResetInText preserves an upstream relative-reset observation as exact
// seconds. It deliberately does not derive it from ResetsAt.
func ResetInText(seconds *int64) string {
	if seconds == nil {
		return "-"
	}
	return fmt.Sprintf("%ds", *seconds)
}

// projectStatusSnapshots derives the ambient HUD input without mutating the
// lossless snapshot/cache identity used by account-inspection surfaces. Fixed
// 5h/weekly rows are canonical for every provider. Antigravity's exact
// upstream gemini-weekly bucket is the sole named quota projected as weekly;
// context, 3p-weekly, and unknown valid buckets remain outside the HUD.
func projectStatusSnapshots(snaps []usage.Snapshot) []usage.Snapshot {
	projected := make([]usage.Snapshot, 0, len(snaps))
	seen := make(map[string]bool)
	key := func(snapshot usage.Snapshot) string {
		return strings.ToLower(strings.TrimSpace(snapshot.Model)) + "\x00" + string(snapshot.Window)
	}
	for _, snapshot := range snaps {
		if snapshot.Window != usage.Window5h && snapshot.Window != usage.WindowWeekly {
			continue
		}
		k := key(snapshot)
		if seen[k] {
			continue
		}
		seen[k] = true
		projected = append(projected, snapshot)
	}
	for _, snapshot := range snaps {
		if !strings.EqualFold(strings.TrimSpace(snapshot.Model), "antigravity") ||
			snapshot.Window != usage.WindowQuota || snapshot.Bucket != "gemini-weekly" {
			continue
		}
		weekly := snapshot
		weekly.Window = usage.WindowWeekly
		weekly.Bucket = ""
		k := key(weekly)
		if seen[k] {
			continue
		}
		seen[k] = true
		projected = append(projected, weekly)
	}
	return usage.SortedSnapshots(projected)
}

// buildModelDisplays converts snapshots into the canonical-ordered display
// rows the renderers iterate over. In schema v2 the Pct value is the
// authoritative percentage from the upstream API, so we no longer drop
// rows by Limit. We still drop rows whose Pct is exactly zero AND whose
// ResetsAt is the zero time — those are placeholders that represent "no
// data" rather than a genuine 0% (e.g. when the upstream returned
// `seven_day_oauth_apps: null`).
func buildModelDisplays(snaps []usage.Snapshot, now time.Time) []modelDisplay {
	snaps = usage.SortedSnapshots(snaps)
	byModel := make(map[string]*modelDisplay)
	order := make([]string, 0, 2)
	for i := range snaps {
		s := snaps[i]
		if s.Pct == 0 && s.ResetsAt.IsZero() && s.Limit == 0 && s.Window != usage.WindowContext && s.Window != usage.WindowQuota {
			continue
		}
		row, ok := byModel[s.Model]
		if !ok {
			row = &modelDisplay{
				model:      s.Model,
				label:      ModelDisplayLabel(s.Model),
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
		// Claude data is throttled/backoff-gated and Antigravity data is
		// hook-driven (only refreshed when agy emits a statusline), so both
		// can go stale — surface the age indicator. Codex reads the latest
		// rollout every call so its age is always ~now (showAge stays false).
		if strings.EqualFold(s.Model, "claude") || strings.EqualFold(s.Model, "antigravity") {
			row.showAge = true
		}
	}
	canonical := []string{"claude", "codex", "antigravity"}
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

// ModelDisplayLabel maps a model name to its capitalized HUD label.
func ModelDisplayLabel(model string) string {
	switch strings.ToLower(model) {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "antigravity":
		return "Antigravity"
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
	case "antigravity":
		return "A"
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
	plain := intrender.StripTmuxEscapes(s)
	if intrender.VisualLen(plain) <= maxWidth {
		return plain
	}
	rs := []rune(plain)
	if maxWidth == 1 {
		return string(rs[:1])
	}
	return string(rs[:maxWidth-1]) + "…"
}

func runeLen(s string) int {
	return len([]rune(s))
}

func printUsageHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux usage [--model codex|claude|antigravity|all] [--window 5h|weekly|context|quota|all] [--json] [--force|-f]")
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
		fmt.Fprintf(w, "%s is in backoff, try again in %s (use --force to bypass)\n", name, FormatBackoffDuration(remaining))
	}
}

// FormatBackoffDuration renders a duration in compact form (e.g. "12m",
// "1h3m", "45s"). Designed to round-trip through both the table note
// and the HUD without ever emitting the noisy default Go form.
func FormatBackoffDuration(d time.Duration) string {
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
