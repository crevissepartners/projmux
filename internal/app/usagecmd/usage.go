// Package usagecmd implements `projmux agent usage` and the hidden
// `projmux internal status usage`
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
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/theme"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

// Command exposes `projmux agent usage` and `projmux internal status usage`
// surfaces. Both share a single Manager so collect-once-render-twice stays
// cheap.
type Command struct {
	managerFn       func([]string) (*usage.Manager, error)
	enabledAgentsFn func() ([]config.AIAgentProvider, error)
	now             func() time.Time
	lookupEnv       func(string) string

	// journalFn resolves the operations-journal recorder used to record
	// usage collection failures; nil selects the default private journal.
	// journal caches the resolved recorder so one process emits one run
	// identity. Both are touched only from the single-threaded CLI entry
	// path, matching the statusRoles package-var pattern above.
	journalFn func() *diagnostics.UsageRecorder
	journal   *diagnostics.UsageRecorder
}

// HUDWindowCapability is one canonical account window the ambient usage HUD
// can project for a provider. It is deliberately closed over the explicit HUD
// projection below; opaque quota buckets never become Settings rows.
type HUDWindowCapability struct {
	Window usage.Window
	Key    string
	Label  string
}

// HUDProviderCapability is the Settings/render contract for one provider.
// Providers are returned in aiprovider.UsageSupported declared order.
type HUDProviderCapability struct {
	ID          aiprovider.ID
	Model       string
	DisplayName string
	Windows     []HUDWindowCapability
}

type hudVisibilityPreferences struct {
	providers map[string]bool
	windows   map[string]map[usage.Window]bool
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

// Run implements `projmux agent usage [...]`.
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
	started := time.Now()
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
	// The warning above only exists while the user is looking at it; the
	// journal is what makes a provider that stopped refreshing discoverable
	// later. Best-effort: it never changes what this command returns.
	c.recordCollectDiagnostics(collectErr, started)

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
// triggered by `projmux internal status usage` or the manual HUD refresh key. tmux
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

// RunStatus implements the `projmux internal status usage` subcommand. It triggers
// an opportunistic, throttled cache refresh (so a fresh install or a stale
// cache self-heals on the next tmux redraw) and then reads the persisted
// cache to render the HUD segment. Adapter failures during this hot path
// are swallowed unless PROJMUX_USAGE_DEBUG is set.
func (c *Command) RunStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxWidth := fs.Int("max-width", 0, "truncate output to N runes (0 = no truncation)")
	// --force / -f mirrors the `projmux agent usage` flag: bypass throttle
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
	started := time.Now()
	var refreshErr error
	if *force {
		_, refreshErr = mgr.ForceCollect(context.Background())
	} else {
		_, refreshErr = mgr.MaybeCollect(context.Background(), statusRefreshThrottle)
	}
	if refreshErr != nil {
		if c.env(usageDebugEnvVar) != "" {
			fmt.Fprintf(stderr, "usage: refresh: %v\n", refreshErr)
		}
		// The status segment stays silent by contract, so the journal is the
		// only place a repeated collection failure on this path becomes
		// visible. Best-effort; the segment still renders from cache.
		c.recordCollectDiagnostics(refreshErr, started)
	}

	snaps, err := mgr.LoadAll()
	if err != nil {
		return nil
	}
	snaps = filterSnapshotsByModels(snaps, modelScope)

	out := formatStatusUsageWithVisibility(snaps, *maxWidth, c.now(), c.loadHUDVisibilityPreferences())
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

// jsonSnapshot is the wire shape emitted by `projmux agent usage --json`. It
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
	return staleLevelForAge(now.Sub(s.UpdatedAt))
}

// staleLevelForAge is the single staleness ladder for this package. Both the
// per-snapshot staleLevel and the model-level age indicator read it, so
// staleAfter / veryStaleAfter are the only staleness thresholds usagecmd owns
// — the indicator no longer carries a private constant that could drift away
// from the value the table and the JSON `stale` flag use.
func staleLevelForAge(age time.Duration) int {
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
	hasWeek    bool
	weekPct    float64
	// lastSync is max(fiveUpdatedAt, weekUpdatedAt). Used together
	// with `now` to compute the age indicator. Zero when no row
	// supplied an UpdatedAt timestamp.
	lastSync time.Time
	// showAge gates the age-indicator render path. False for
	// adapters whose data is always near-current (Codex).
	showAge bool
}

// formatStatusUsage produces the HUD-style tmux status segment.
//
// Selection is NOT whole-segment. The segment starts from its richest render
// and sheds ONE optional element at a time, in the order usageShedOrder
// declares, until the result fits maxWidth. The predecessor of this code
// picked "the first whole-segment tier that fits", which meant a single
// provider's optional element — a cosmetic `(3m)` age on a healthy provider,
// say — pushed the ENTIRE segment down a tier and took every other provider's
// optional element with it, spending far more cells than the row was short.
//
// Two things are absent from usageShedOrder on purpose and therefore outlive
// everything in it:
//
//   - the `~` / `~~` staleness marker (the contract PR #620 established), and
//   - each provider's official window bar: 5h when the provider has one,
//     otherwise weekly. No step hides a provider wholesale.
//
// Only hard rune-truncation — the last resort, reached when even the fully
// shed segment overflows — can reach either of them.
//
// maxWidth is measured in display cells; tmux color escapes (`#[...]`) are
// stripped before counting so adding color does not push the segment over
// the budget.
//
// `now` is the wall-clock used for staleness detection. Pass time.Time{}
// to disable the marker (e.g. in tests that don't care).
func formatStatusUsage(snaps []usage.Snapshot, maxWidth int, now time.Time) string {
	return formatProjectedStatusUsage(projectStatusSnapshots(snaps), maxWidth, now)
}

func formatStatusUsageWithVisibility(snaps []usage.Snapshot, maxWidth int, now time.Time, prefs hudVisibilityPreferences) string {
	if len(prefs.providers) == 0 && len(prefs.windows) == 0 {
		return formatStatusUsage(snaps, maxWidth, now)
	}
	projected := filterStatusProjectionByVisibility(projectStatusSnapshots(snaps), prefs)
	return formatProjectedStatusUsage(projected, maxWidth, now)
}

func formatProjectedStatusUsage(projected []usage.Snapshot, maxWidth int, now time.Time) string {
	models := buildModelDisplays(projected)
	if len(models) == 0 {
		return ""
	}
	plan := newUsageSegmentPlan(models)
	steps := usageShedSteps(models, now)
	for step := 0; step <= len(steps); step++ {
		if step > 0 {
			steps[step-1].apply(&plan)
		}
		out := renderUsageSegment(models, now, plan)
		if out == "" {
			continue
		}
		if maxWidth <= 0 || intrender.VisualLen(out) <= maxWidth {
			return out
		}
	}
	// Last resort: hard rune-truncation of the fully shed segment. `plan` has
	// had every step applied by now, so this is the narrowest thing the
	// renderer can produce.
	return truncateWithEllipsis(renderUsageSegment(models, now, plan), maxWidth)
}

// usageSegmentPlan is the set of optional elements the segment is still
// allowed to render. It starts permissive (newUsageSegmentPlan) and only ever
// loses entries, one shed step at a time.
//
// What is NOT in this struct is the point: there is no field that can turn off
// a provider's label, its official window, or its staleness marker, so no
// sequence of shed steps can remove them.
type usageSegmentPlan struct {
	// ageText[i] renders model i's `(3m)` / `(3h~~)` age TEXT. When false the
	// model falls back to the bare `~` / `~~` marker, which is not optional.
	ageText []bool
	// secondary[i] renders model i's non-official second window — weekly on a
	// provider that also reports 5h. A provider with a single window has no
	// secondary and is unaffected.
	secondary []bool
	// bars renders the graphical HUD (`5h [████░░░░░░] 42%`). When false every
	// provider switches to the compact text pair (`5h:42% weekly:18%`), which
	// costs so much less per provider that the secondary window comes back.
	bars bool
	// longLabels renders `Claude` rather than the legacy single letter `C`.
	longLabels bool
}

// newUsageSegmentPlan is the richest render: every optional element on.
func newUsageSegmentPlan(models []modelDisplay) usageSegmentPlan {
	plan := usageSegmentPlan{
		ageText:    make([]bool, len(models)),
		secondary:  make([]bool, len(models)),
		bars:       true,
		longLabels: true,
	}
	for i := range models {
		plan.ageText[i] = true
		plan.secondary[i] = true
	}
	return plan
}

// ageMode maps a model's remaining age budget onto the renderer's age element.
func (p usageSegmentPlan) ageMode(model int) ageMode {
	if p.ageText[model] {
		return ageModeFull
	}
	return ageModeStaleCompact
}

// usageShedRule is one entry in the usage segment's drop order.
//
// A rule with segment=true fires once for the whole segment. Otherwise it
// fires once per eligible provider, tail-first (see usageShedSteps).
type usageShedRule struct {
	// name is the rule's identity in docs/statusbar.md and in test failures.
	name string
	// segment marks a rule that applies to the whole segment at once.
	segment bool
	// eligible reports whether a provider actually carries this element. A
	// provider that does not is skipped rather than producing a no-op step.
	eligible func(m modelDisplay, now time.Time) bool
	// apply removes the element from the plan. model is -1 for segment rules.
	apply func(plan *usageSegmentPlan, model int)
}

// usageShedOrder IS THE DROP ORDER. It is the single definition of what the
// usage segment gives up first when the row is too narrow, in this package and
// in the product; docs/statusbar.md ("Usage element drop order") mirrors it and
// the width-sweep table test pins it. Index 0 goes first.
//
// Two invariants are structural rather than conventional:
//
//   - The `~` / `~~` staleness marker has NO entry here, so no width can shed
//     it while any element in this list survives. That is PR #620's contract.
//   - A provider's official window (5h, or weekly when 5h is absent) has NO
//     entry either. Rule 3 sheds only the SECOND window of a provider that
//     reports two; rules 4 and 5 change how the official window is drawn, never
//     whether it is drawn.
//
// Within a per-provider rule, steps run tail-first over the canonical provider
// order (claude, codex, antigravity), so the provider a user reads first is the
// last to lose detail.
var usageShedOrder = []usageShedRule{
	{
		// 1. The cosmetic age text on a provider that is NOT stale — `(3m)`.
		// It is decoration: the data behind it is current.
		name:     "cosmetic age text",
		eligible: func(m modelDisplay, now time.Time) bool { return hasHUDAgeText(m, now) && modelStaleLevel(m, now) == 0 },
		apply:    func(plan *usageSegmentPlan, model int) { plan.ageText[model] = false },
	},
	{
		// 2. The age text on a STALE provider — `(3h~~)` collapses to `~~`.
		// The marker survives; only the "how old exactly" text goes.
		name:     "stale age text (the ~ / ~~ marker stays)",
		eligible: func(m modelDisplay, now time.Time) bool { return hasHUDAgeText(m, now) && modelStaleLevel(m, now) > 0 },
		apply:    func(plan *usageSegmentPlan, model int) { plan.ageText[model] = false },
	},
	{
		// 3. A provider's SECOND window bar — Claude's weekly next to its 5h.
		// The official window bar is never a candidate.
		name:     "secondary window bar",
		eligible: func(m modelDisplay, _ time.Time) bool { return m.hasFive && m.hasWeek },
		apply:    func(plan *usageSegmentPlan, model int) { plan.secondary[model] = false },
	},
	{
		// 4. Bars, segment-wide: `5h [████░░░░░░] 42%` becomes `5h:42%`. This
		// is segment-wide because a row that mixes bar and text providers reads
		// as a rendering bug, and because the text pair is cheap enough that
		// every provider's second window comes back with it.
		name:    "bars (every provider switches to text pairs)",
		segment: true,
		apply:   func(plan *usageSegmentPlan, _ int) { plan.bars = false },
	},
	{
		// 5. Long labels, segment-wide: `Claude` becomes `C`.
		name:    "long labels (single-letter fallback)",
		segment: true,
		apply:   func(plan *usageSegmentPlan, _ int) { plan.longLabels = false },
	},
}

// usageShedStep is one concrete removal: a rule bound to a provider.
type usageShedStep struct {
	rule  int
	model int // -1 for whole-segment rules.
}

// apply removes this step's element from the plan.
func (s usageShedStep) apply(plan *usageSegmentPlan) {
	usageShedOrder[s.rule].apply(plan, s.model)
}

// usageShedSteps expands usageShedOrder against a concrete provider set: every
// rule in order, and inside a per-provider rule every eligible provider,
// tail-first. The result is the exact, finite sequence of removals
// formatStatusUsage walks — there is no other path to a degraded segment.
func usageShedSteps(models []modelDisplay, now time.Time) []usageShedStep {
	steps := make([]usageShedStep, 0, len(usageShedOrder)+2*len(models))
	for i, rule := range usageShedOrder {
		if rule.segment {
			steps = append(steps, usageShedStep{rule: i, model: -1})
			continue
		}
		for m := len(models) - 1; m >= 0; m-- {
			if rule.eligible(models[m], now) {
				steps = append(steps, usageShedStep{rule: i, model: m})
			}
		}
	}
	return steps
}

// hasHUDAgeText reports whether a provider would actually render an age text
// element, so an ineligible provider never becomes a no-op shed step.
func hasHUDAgeText(m modelDisplay, now time.Time) bool {
	return renderHUDAgeSuffix(m, now, ageModeFull) != ""
}

// ageMode selects how much of a model's last-sync age the segment renders.
type ageMode int

const (
	// ageModeFull renders the `(3m)` / `(15m~)` / `(3h~~)` indicator for every
	// model that opted in, including purely cosmetic level-0 ages.
	ageModeFull ageMode = iota
	// ageModeStaleCompact drops the age text and keeps only the bare `~` /
	// `~~` marker (1-2 cells). Renders nothing at all for a fresh model, so a
	// fresh segment is byte-identical to one built with no age element.
	ageModeStaleCompact
)

// renderUsageSegment renders the segment under a plan. It is the ONLY renderer
// of the status usage segment: every width, degraded or not, comes out of here,
// and the plan is the only thing that varies.
func renderUsageSegment(models []modelDisplay, now time.Time, plan usageSegmentPlan) string {
	if plan.bars {
		return renderUsageHUD(models, now, plan)
	}
	return renderUsageText(models, now, plan)
}

// renderUsageHUD renders the graphical form: `<label><age> 5h [bar] N% · weekly
// [bar] N%` per provider. The age element selected by the plan is injected
// right after the model label (see renderHUDAgeSuffix for its exact shape), and
// the second window is emitted only while the plan still allows it.
func renderUsageHUD(models []modelDisplay, now time.Time, plan usageSegmentPlan) string {
	blocks := make([]string, 0, len(models))
	for i, m := range models {
		if !m.hasFive && !m.hasWeek {
			continue
		}
		var b strings.Builder
		b.WriteString("#[fg=" + statusRoles.AccentAIFg + ",bold]")
		b.WriteString(m.label)
		b.WriteString(statusDefaultReset)
		b.WriteString(renderHUDAgeSuffix(m, now, plan.ageMode(i)))
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
		// The official window first: 5h when the provider reports one,
		// otherwise weekly. Only the SECOND window is ever conditional, which
		// is what keeps a weekly-only provider whole at every plan.
		if m.hasFive {
			writePair("5h", m.fivePct)
		}
		if m.hasWeek && (!m.hasFive || plan.secondary[i]) {
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

// renderUsageText renders the colorless legacy form, `Claude 5h:42%
// weekly:18%` or its single-letter variant. Both windows are always spelled
// here: the whole text pair costs less than one bar, so the plan's secondary
// flags do not apply.
func renderUsageText(models []modelDisplay, now time.Time, plan usageSegmentPlan) string {
	blocks := make([]string, 0, len(models))
	for _, m := range models {
		label := m.shortLabel
		if plan.longLabels {
			label = m.label
		}
		text := renderTextPair(m, label, staleMarkerText(modelStaleLevel(m, now)))
		if text != "" {
			blocks = append(blocks, text)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, statusModelSeparator)
}

// renderTextPair builds a `<label><marker> 5h:N% weekly:N%` substring used by
// both non-HUD tiers. Returns "" when the model has nothing to show.
//
// These two tiers are the colorless legacy forms, so staleness is carried as
// the plain, uncolored `~` / `~~` marker glued directly to the label
// (`Claude~ 5h:80%`, `A~~ weekly:38%`) — same vocabulary as the colored HUD
// indicator, minus the age text and the tmux escapes these tiers never emit.
// marker is "" for a fresh model, which makes the output byte-identical to the
// historical marker-free form. The non-JSON `usage` table CLI still surfaces a
// STALE column for verbose inspection.
func renderTextPair(m modelDisplay, label, marker string) string {
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
	return label + marker + " " + strings.Join(parts, " ")
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
// is missing.
//
// The indicator's tier is driven by the package's staleness thresholds
// (staleAfter / veryStaleAfter) so a value the table already calls stale
// cannot render as if it were current. The legacy `~` / `~~` marker
// vocabulary is restored INSIDE the indicator, which is where staleness
// now lives:
//
//   - <1m:            no indicator at all
//   - level 0 (≤10m): `(3m)`   secondary text
//   - level 1 (≤1h):  `(15m~)` muted text
//   - level 2 (>1h):  `(3h~~)` muted text
//
// Staleness stays muted at every tier: warning/critical colors are
// reserved for usage thresholds, and a stale value is not a usage alarm.
func renderAgeIndicator(m modelDisplay, now time.Time) string {
	if !m.showAge || now.IsZero() || m.lastSync.IsZero() {
		return ""
	}
	text := formatLastSyncAge(now.Sub(m.lastSync))
	if text == "" {
		return ""
	}
	color := statusRoles.StatusTextSecondary
	if marker := staleMarkerText(modelStaleLevel(m, now)); marker != "" {
		color, text = statusRoles.StatusTextMuted, text+marker
	}
	return fmt.Sprintf("#[fg=%s](%s)%s", color, text, statusDefaultReset)
}

// modelStaleLevel is the model-level view of the package's single staleness
// ladder: 0 fresh, 1 stale (>staleAfter), 2 very stale (>veryStaleAfter). It
// returns 0 for models that opted out of the age signal (Codex, whose data is
// re-read from the latest rollout on every call) and for missing clocks, which
// keeps the marker and the indicator agreeing on exactly one definition of
// "stale" rather than each carrying its own gate.
func modelStaleLevel(m modelDisplay, now time.Time) int {
	if !m.showAge || now.IsZero() || m.lastSync.IsZero() {
		return 0
	}
	return staleLevelForAge(now.Sub(m.lastSync))
}

// staleMarkerText maps a staleness level onto the package's marker
// vocabulary: "" fresh, `~` stale, `~~` very stale. It is the one place the
// marker runes are spelled, shared by the colored HUD indicator, the compact
// HUD marker and the plain text tiers.
func staleMarkerText(level int) string {
	switch level {
	case 1:
		return "~"
	case 2:
		return "~~"
	default:
		return ""
	}
}

// renderHUDAgeSuffix builds the age element the HUD injects between a model
// label and its first window pair, including any leading space. The empty
// string means "render nothing", which is what the compact mode produces for
// any model at staleness level 0 — the property that keeps a healthy install
// byte-identical however many elements the segment has shed.
//
//   - ageModeFull:         ` (3m)` / ` (15m~)` / ` (3h~~)`, colored.
//   - ageModeStaleCompact: `~` / `~~` glued to the label, muted, no age text.
func renderHUDAgeSuffix(m modelDisplay, now time.Time, mode ageMode) string {
	if mode == ageModeStaleCompact {
		marker := staleMarkerText(modelStaleLevel(m, now))
		if marker == "" {
			return ""
		}
		return "#[fg=" + statusRoles.StatusTextMuted + "]" + marker + statusDefaultReset
	}
	indicator := renderAgeIndicator(m, now)
	if indicator == "" {
		return ""
	}
	return " " + indicator
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
		label := "quota/" + boundedOpaqueDisplayID(snapshot.Bucket, 24)
		if quota := snapshot.NamedQuota; quota != nil && quota.Scope != nil && quota.Scope.Model != nil {
			label += " · " + boundedOpaqueDisplayID(quota.Scope.Model.DisplayName, 24)
		}
		if quota := snapshot.NamedQuota; quota != nil && !quota.IsActive {
			label += " [inactive]"
		}
		return truncateDisplayRunes(label, 72)
	}
	return string(snapshot.Window)
}

func boundedOpaqueDisplayID(id string, maxRunes int) string {
	return truncateDisplayRunes(BucketDisplayID(id), maxRunes)
}

func truncateDisplayRunes(value string, maxRunes int) string {
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

// HUDProviderCapabilities is the single explicit capability seam shared by
// ambient projection and Settings. Provider inventory/order comes from the
// usage-supported catalog; window inventory is intentionally enumerated here
// because it cannot be inferred from opaque account snapshots.
func HUDProviderCapabilities() []HUDProviderCapability {
	providers := aiprovider.UsageSupported()
	out := make([]HUDProviderCapability, 0, len(providers))
	for _, provider := range providers {
		capability := HUDProviderCapability{
			ID:          provider.ID,
			Model:       provider.UsageModel,
			DisplayName: provider.DisplayName,
		}
		switch provider.ID {
		case aiprovider.Claude, aiprovider.Codex:
			capability.Windows = []HUDWindowCapability{
				{Window: usage.Window5h, Key: "5h", Label: "5h"},
				{Window: usage.WindowWeekly, Key: "weekly", Label: "Weekly"},
			}
		case aiprovider.Antigravity:
			capability.Windows = []HUDWindowCapability{
				{Window: usage.WindowWeekly, Key: "weekly", Label: "Weekly"},
			}
		}
		out = append(out, capability)
	}
	return out
}

func (c *Command) loadHUDVisibilityPreferences() hudVisibilityPreferences {
	prefs := hudVisibilityPreferences{
		providers: make(map[string]bool),
		windows:   make(map[string]map[usage.Window]bool),
	}
	paths, err := c.hudVisibilityConfigPaths()
	if err != nil {
		return prefs
	}
	for _, provider := range HUDProviderCapabilities() {
		model := strings.ToLower(strings.TrimSpace(provider.Model))
		providerState, err := config.LoadStatusbarVisibilityFile(paths.StatusbarAgentUsageProviderVisibilityFile(string(provider.ID)))
		prefs.providers[model] = err != nil || providerState.Effective == config.StatusbarVisibilityOn
		prefs.windows[model] = make(map[usage.Window]bool, len(provider.Windows))
		for _, window := range provider.Windows {
			state, err := config.LoadStatusbarVisibilityFile(paths.StatusbarAgentUsageWindowVisibilityFile(string(provider.ID), window.Key))
			prefs.windows[model][window.Window] = err != nil || state.Effective == config.StatusbarVisibilityOn
		}
	}
	return prefs
}

func (c *Command) hudVisibilityConfigPaths() (config.Paths, error) {
	home := strings.TrimSpace(c.env("HOME"))
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return config.Paths{}, err
		}
	}
	return config.Homes{
		HomeDir:    home,
		ConfigHome: c.env("XDG_CONFIG_HOME"),
		StateHome:  c.env("XDG_STATE_HOME"),
	}.Paths()
}

func filterStatusProjectionByVisibility(projected []usage.Snapshot, prefs hudVisibilityPreferences) []usage.Snapshot {
	if len(projected) == 0 {
		return nil
	}
	out := make([]usage.Snapshot, 0, len(projected))
	for _, snapshot := range projected {
		model := strings.ToLower(strings.TrimSpace(snapshot.Model))
		if enabled, known := prefs.providers[model]; known && !enabled {
			continue
		}
		if windows, known := prefs.windows[model]; known {
			if enabled, supported := windows[snapshot.Window]; !supported || !enabled {
				continue
			}
		}
		out = append(out, snapshot)
	}
	return out
}

// projectStatusSnapshots derives the ambient HUD input without mutating the
// lossless snapshot/cache identity used by account-inspection surfaces. The
// explicit HUD capability seam admits Claude/Codex fixed 5h/weekly rows.
// Antigravity's exact upstream gemini-weekly bucket is the sole named quota
// projected as weekly; coincidental fixed windows, context, 3p-weekly, future
// providers without a declared projection, and unknown buckets stay outside.
func projectStatusSnapshots(snaps []usage.Snapshot) []usage.Snapshot {
	projected := make([]usage.Snapshot, 0, len(snaps))
	seen := make(map[string]bool)
	key := func(snapshot usage.Snapshot) string {
		return strings.ToLower(strings.TrimSpace(snapshot.Model)) + "\x00" + string(snapshot.Window)
	}
	for _, snapshot := range snaps {
		if !directHUDWindowSupported(snapshot.Model, snapshot.Window) {
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

// directHUDWindowSupported is the source half of the explicit projection
// seam. Claude/Codex publish canonical fixed windows directly. Antigravity's
// weekly capability is sourced only from its exact gemini-weekly quota below,
// never from a coincidental fixed-window snapshot.
func directHUDWindowSupported(model string, window usage.Window) bool {
	for _, capability := range HUDProviderCapabilities() {
		if !strings.EqualFold(capability.Model, strings.TrimSpace(model)) || capability.ID == aiprovider.Antigravity {
			continue
		}
		for _, candidate := range capability.Windows {
			if candidate.Window == window {
				return true
			}
		}
	}
	return false
}

// buildModelDisplays converts snapshots into the canonical-ordered display
// rows the renderers iterate over. In schema v2 the Pct value is the
// authoritative percentage from the upstream API, so we no longer drop
// rows by Limit. We still drop rows whose Pct is exactly zero AND whose
// ResetsAt is the zero time — those are placeholders that represent "no
// data" rather than a genuine 0% (e.g. when the upstream returned
// `seven_day_oauth_apps: null`).
func buildModelDisplays(snaps []usage.Snapshot) []modelDisplay {
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
		// Per-window staleness is deliberately NOT tracked here: the HUD's
		// staleness signal is model-level (the lastSync age indicator), and a
		// second, competing per-window notion would be written and never read.
		switch s.Window {
		case usage.Window5h:
			row.hasFive = true
			row.fivePct = s.Pct
		case usage.WindowWeekly:
			row.hasWeek = true
			row.weekPct = s.Pct
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
	fmt.Fprintln(w, "  projmux agent usage [--model codex|claude|antigravity|all] [--window 5h|weekly|context|quota|all] [--json] [--force|-f]")
	fmt.Fprintln(w, "  projmux internal status usage [--max-width N] [--force|-f]")
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
