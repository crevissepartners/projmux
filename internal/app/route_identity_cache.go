package app

import (
	"path/filepath"
	"sync"
)

// The route guards below prove one thing over and over inside a single
// runtime-mutation transaction: that the exact tmux server the plan was built
// against is still the server the writes will reach. Each planned action runs a
// target/route guard and a semantic guard, and each pre- and post-effect
// observation re-runs the same proof, so one create/Continue/resume transaction
// re-reads #{socket_path}, #{pid}, @projmux_app and the logical socket-name
// marker dozens of times while the registry lock is held.
//
// runtimeRouteIdentityCache reuses the *result* of a proof that already ran
// against tmux in this transaction. It never stands in for a proof that has not
// run: the first guard for a given tuple always executes the full read set, and
// only an exactly equal tuple may reuse it afterwards. The cache is per process,
// per transaction, per exact route target, and it is never persisted.
//
// Every writer that reuses it writes through materializer.guardedWriteSteps,
// which drops every proof after each Apply and Undo: the materializer's own
// plans, and the typed metadata mirror when it runs inside the transaction
// (runtimeMutationMetadataMirror.scope). So no guarded write relies on a proof
// older than the previous guarded write, whoever made it.
const (
	// routeIdentityScopeExactRoute is the materializer's own
	// reobserve/guard proof (guardExactRouteOwnership with a logical marker
	// requirement).
	routeIdentityScopeExactRoute = "materializer-exact-route"
	// routeIdentityScopeResolvedRoute is the resolved-route proof shared by the
	// printable target/route guard and the materializer's ownership guard.
	routeIdentityScopeResolvedRoute = "resolved-route"
)

// runtimeRouteIdentityKey is the whole identity a guard proved. Every component
// is part of the key, so a server swap, a restarted server, a retargeted socket,
// a different authority class, or a rewritten ownership marker is a miss and
// re-proves the identity in full.
type runtimeRouteIdentityKey struct {
	// Scope names which guard produced the proof. Two guards read different
	// evidence, so one may never answer for the other.
	Scope string
	// SocketFlag and SocketValue are the exact route target: -S path or -L name.
	SocketFlag  string
	SocketValue string
	// PhysicalSocket is the observed absolute #{socket_path}.
	PhysicalSocket string
	// AuthorityClass and ServerPID are the captured server generation.
	AuthorityClass string
	ServerPID      string
	// AppMarker and SocketNameMarker are the observed @projmux_app and logical
	// socket-name marker values the proof demanded.
	AppMarker        string
	SocketNameMarker string
}

// routeTarget is the exact route the key belongs to. One cache serves one
// target; anything else invalidates it rather than growing a second entry.
func (k runtimeRouteIdentityKey) routeTarget() string {
	return k.SocketFlag + "=" + k.SocketValue + "@" + k.PhysicalSocket
}

// complete reports whether the key names a fully proven identity. A partially
// bound route (no physical socket, no server generation, or markers that do not
// match its authority class) is never cacheable, so those guards keep reading
// tmux every time.
func (k runtimeRouteIdentityKey) complete() bool {
	if k.Scope == "" || k.SocketFlag == "" || k.SocketValue == "" || k.ServerPID == "" {
		return false
	}
	if !filepath.IsAbs(k.PhysicalSocket) || filepath.Clean(k.PhysicalSocket) != k.PhysicalSocket {
		return false
	}
	switch k.AuthorityClass {
	case runtimeMutationRouteApp:
		return k.AppMarker == "1" && k.SocketNameMarker != ""
	case runtimeMutationRouteStandalone, runtimeMutationRouteStandaloneExplicit:
		return k.AppMarker == "" && k.SocketNameMarker == ""
	}
	return false
}

// runtimeRouteIdentityStats is observation only. It exists so a test can prove
// that the first proof of a transaction really executed.
type runtimeRouteIdentityStats struct {
	Proofs           int
	Reuses           int
	Invalidations    int
	LastInvalidation string
	Closed           bool
}

type runtimeRouteIdentityCache struct {
	mu          sync.Mutex
	operationID string
	open        bool
	// bound is the single exact route target this cache serves.
	bound  string
	proved map[runtimeRouteIdentityKey]struct{}
	stats  runtimeRouteIdentityStats
}

// newRuntimeRouteIdentityCache opens a cache for exactly one transaction. A
// blank operation id yields no cache at all, which keeps every guard on the
// uncached path.
func newRuntimeRouteIdentityCache(operationID string) *runtimeRouteIdentityCache {
	if operationID == "" {
		return nil
	}
	return &runtimeRouteIdentityCache{operationID: operationID, open: true, proved: map[runtimeRouteIdentityKey]struct{}{}}
}

// reuse reports whether this exact identity was already proved against tmux in
// this transaction. A nil cache, a closed cache, a different route target, or
// any differing key component answers false, and the caller then runs the full
// verification.
func (c *runtimeRouteIdentityCache) reuse(key runtimeRouteIdentityKey) bool {
	if c == nil || !key.complete() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return false
	}
	if c.bound != "" && c.bound != key.routeTarget() {
		// A different -S path, -L name, or physical socket is a server swap as
		// far as this transaction is concerned.
		c.invalidateLocked("route-target-changed")
		return false
	}
	if _, ok := c.proved[key]; !ok {
		return false
	}
	c.stats.Reuses++
	return true
}

// record stores the result of a verification that just succeeded against tmux.
func (c *runtimeRouteIdentityCache) record(key runtimeRouteIdentityKey) {
	if c == nil || !key.complete() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return
	}
	if c.bound != key.routeTarget() {
		if c.bound != "" {
			c.invalidateLocked("route-target-changed")
		}
		c.bound = key.routeTarget()
	}
	if c.proved == nil {
		c.proved = map[runtimeRouteIdentityKey]struct{}{}
	}
	c.proved[key] = struct{}{}
	c.stats.Proofs++
}

// invalidate drops every proof. The next guard performs the full check again.
func (c *runtimeRouteIdentityCache) invalidate(reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateLocked(reason)
}

func (c *runtimeRouteIdentityCache) invalidateLocked(reason string) {
	if len(c.proved) == 0 && c.bound == "" {
		return
	}
	c.proved = map[runtimeRouteIdentityKey]struct{}{}
	c.bound = ""
	c.stats.Invalidations++
	c.stats.LastInvalidation = reason
}

// close ends the transaction scope. It is idempotent, and everything after it
// -- runtime ledger rollback included -- re-proves identity against tmux.
func (c *runtimeRouteIdentityCache) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateLocked("transaction-end")
	c.open = false
	c.stats.Closed = true
}

func (c *runtimeRouteIdentityCache) snapshot() runtimeRouteIdentityStats {
	if c == nil {
		return runtimeRouteIdentityStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// runtimeRouteIdentityKeyForRoute builds the key a resolved-route proof covers.
// It is cacheable only when the route already carries a physical socket and a
// captured server generation; anything less keeps the guard fully uncached.
func runtimeRouteIdentityKeyForRoute(scope string, route runtimeMutationRoute) (runtimeRouteIdentityKey, bool) {
	if route.authority == nil || route.expectedSocketPath == "" {
		return runtimeRouteIdentityKey{}, false
	}
	key := runtimeRouteIdentityKey{
		Scope:          scope,
		SocketFlag:     route.target.Flag(),
		SocketValue:    route.target.Value,
		PhysicalSocket: filepath.Clean(route.expectedSocketPath),
		AuthorityClass: route.authority.Class,
		ServerPID:      route.authority.ServerPID,
	}
	if key.AuthorityClass == runtimeMutationRouteApp {
		key.AppMarker, key.SocketNameMarker = "1", route.socketName
	}
	if !key.complete() {
		return runtimeRouteIdentityKey{}, false
	}
	return key, true
}
