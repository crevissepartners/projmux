package app

import (
	"fmt"
	"sort"
	"sync"
)

// MergeChange describes a single binding-level diff produced by a
// TerminalAdapter while planning a merge into the user's terminal config.
type MergeChange struct {
	// Trigger is the canonical key combo (e.g. "alt+1").
	Trigger string
	// Action is the canonical action string (e.g. "csi:9005u").
	Action string
	// Existing is the user's current action mapped to Trigger, if any.
	Existing string
	// Kind is one of "add", "update", "skip-conflict", "noop".
	Kind string
	// Reason is a short human-readable note (used for skip-conflict).
	Reason string
}

// MergePlan is the output of TerminalAdapter.PlanMerge. It contains the
// pre-image, the post-image, and a list of per-binding changes that callers
// can render as a preview/diff before applying.
type MergePlan struct {
	// ConfigPath is the absolute config file path the plan targets.
	ConfigPath string
	// Original is the verbatim file contents read from disk (empty if missing).
	Original string
	// Updated is the file contents that ApplyMerge would write.
	Updated string
	// Changes is the per-binding decision list, ordered by trigger.
	Changes []MergeChange
	// CreateNew is true when the config file does not exist yet.
	CreateNew bool
}

// HasEffect reports whether applying this plan would change anything on disk.
func (p MergePlan) HasEffect() bool {
	return p.Original != p.Updated
}

// TerminalAdapter is the per-terminal contract for `projmux init`.
//
// Implementations register themselves in the package-level registry via
// RegisterTerminalAdapter so the init command can dispatch by name and
// auto-detect the active terminal.
type TerminalAdapter interface {
	// Name returns the terminal's canonical CLI name, e.g. "ghostty".
	Name() string
	// Detect reports whether the running shell appears to be inside this
	// terminal. env is an indirection over os.Getenv so tests can drive it.
	Detect(env func(string) string) bool
	// ConfigPath resolves the absolute config file path for this terminal.
	ConfigPath(env func(string) string) (string, error)
	// PlanMerge returns the diff of merging projmux bindings into the
	// supplied current config. fileExists is true when the config file
	// already exists on disk; an empty string with fileExists=false means
	// the file should be created.
	PlanMerge(currentConfig string, fileExists bool) (MergePlan, error)
	// ApplyMerge writes the plan's Updated contents to disk, creating a
	// timestamped backup of the previous file when one exists.
	ApplyMerge(plan MergePlan) error
}

// ConfigPathCandidatesResolver is an optional interface for terminal adapters
// whose configuration may live at one of several well-known paths (e.g.
// Ghostty looks at both `config` and `config.ghostty`). The init command uses
// the candidate list to pick the existing file, surface ambiguity when more
// than one exists, and fall back to the first entry when none exists. Adapters
// that map cleanly to a single path can ignore this interface; the init
// dispatcher falls back to TerminalAdapter.ConfigPath in that case.
type ConfigPathCandidatesResolver interface {
	// ConfigPathCandidates returns the ordered list of well-known config
	// file paths, most-canonical first (used as the "default" when none of
	// the candidates exist on disk).
	ConfigPathCandidates(env func(string) string) ([]string, error)
}

// terminalRegistry is the global registry of TerminalAdapter implementations.
type terminalRegistry struct {
	mu       sync.RWMutex
	adapters map[string]TerminalAdapter
}

var defaultTerminalRegistry = &terminalRegistry{adapters: map[string]TerminalAdapter{}}

// RegisterTerminalAdapter adds a TerminalAdapter to the default registry.
// It panics on duplicate names so registration bugs surface at init time.
func RegisterTerminalAdapter(a TerminalAdapter) {
	defaultTerminalRegistry.register(a)
}

func (r *terminalRegistry) register(a TerminalAdapter) {
	if a == nil {
		panic("RegisterTerminalAdapter: nil adapter")
	}
	name := a.Name()
	if name == "" {
		panic("RegisterTerminalAdapter: empty Name()")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[name]; exists {
		panic(fmt.Sprintf("RegisterTerminalAdapter: %q already registered", name))
	}
	r.adapters[name] = a
}

// lookup returns the adapter registered under name, or false.
func (r *terminalRegistry) lookup(name string) (TerminalAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// names returns the registered adapter names sorted alphabetically.
func (r *terminalRegistry) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// detect walks the registry and returns the first adapter whose Detect
// reports a match. If no adapter matches, the second return is false.
func (r *terminalRegistry) detect(env func(string) string) (TerminalAdapter, bool) {
	for _, name := range r.names() {
		a, _ := r.lookup(name)
		if a == nil {
			continue
		}
		if a.Detect(env) {
			return a, true
		}
	}
	return nil, false
}

// reset clears the registry. Test helper.
func (r *terminalRegistry) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters = map[string]TerminalAdapter{}
}
