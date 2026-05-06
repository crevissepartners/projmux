package usage

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Adapter is the pluggable contract for an AI-usage data source.
//
// Adapters return Snapshots sourced from authoritative upstream APIs. They
// MUST NOT aggregate or merge — the upstream already reports the
// account-wide percentage; the Store's only job is to persist whatever
// the adapter returns.
//
// Best-effort contract: when data is not reachable (no credentials, no
// network, expired token, etc) implementations should return zero
// snapshots. Returning an error is fine for diagnostics under
// PROJMUX_USAGE_DEBUG; the status segment swallows it on the hot path.
type Adapter interface {
	Name() string
	Collect(ctx context.Context) ([]Snapshot, error)
}

// ThrottleHinter is an optional interface adapters may implement to
// declare their preferred minimum interval between Collect calls. The
// Manager honours this hint per-adapter; adapters that do not implement
// the interface inherit the caller-supplied default (currently 30s).
type ThrottleHinter interface {
	ThrottleHint() time.Duration
}

// BackoffStater is an optional interface adapters may implement to
// expose persisted exponential-backoff state. The Manager round-trips
// the value through the on-disk snapshots file so a CLI restart does
// not lose backoff progress. Adapters that do not implement this
// interface have no persistent backoff bookkeeping (errors are still
// surfaced for diagnostics).
type BackoffStater interface {
	// LoadBackoff installs persisted backoff state from disk. Called
	// exactly once per Manager Collect cycle, before Collect is invoked.
	LoadBackoff(state BackoffState)
	// SaveBackoff returns the current in-memory backoff state for
	// persistence. Called after Collect so a fresh 429 (or success) is
	// reflected in the next file write.
	SaveBackoff() BackoffState
}

// Registry stores adapters keyed by Name(). It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// Register adds an adapter to the registry. Re-registering the same name
// returns an error; callers that want to override should call Replace.
func (r *Registry) Register(a Adapter) error {
	if a == nil {
		return fmt.Errorf("usage: register nil adapter")
	}
	name := a.Name()
	if name == "" {
		return fmt.Errorf("usage: register adapter with empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("usage: adapter %q already registered", name)
	}
	r.adapters[name] = a
	return nil
}

// Replace registers an adapter, overwriting any prior adapter with the same
// name. Mostly useful in tests.
func (r *Registry) Replace(a Adapter) error {
	if a == nil {
		return fmt.Errorf("usage: replace with nil adapter")
	}
	name := a.Name()
	if name == "" {
		return fmt.Errorf("usage: replace adapter with empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[name] = a
	return nil
}

// Lookup returns the adapter registered under name. The bool is false when
// no adapter has been registered.
func (r *Registry) Lookup(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// Names returns the registered adapter names in lexicographic order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for n := range r.adapters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// All returns every registered adapter, ordered by Name().
func (r *Registry) All() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for n := range r.adapters {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Adapter, 0, len(names))
	for _, n := range names {
		out = append(out, r.adapters[n])
	}
	return out
}
