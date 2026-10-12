package runtime

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
	RouteIdentityScopeExactRoute = "materializer-exact-route"
	// routeIdentityScopeResolvedRoute is the resolved-route proof shared by the
	// printable target/route guard and the materializer's ownership guard.
	RouteIdentityScopeResolvedRoute = "resolved-route"
)

// runtimeRouteIdentityKey is the whole identity a guard proved. Every component
// is part of the key, so a server swap, a restarted server, a retargeted socket,
// a different authority class, or a rewritten ownership marker is a miss and
// re-proves the identity in full.
type RuntimeRouteIdentityKey struct {
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
func (k RuntimeRouteIdentityKey) RouteTarget() string {
	return k.SocketFlag + "=" + k.SocketValue + "@" + k.PhysicalSocket
}

// complete reports whether the key names a fully proven identity. A partially
// bound route (no physical socket, no server generation, or markers that do not
// match its authority class) is never cacheable, so those guards keep reading
// tmux every time.
func (k RuntimeRouteIdentityKey) Complete() bool {
	if k.Scope == "" || k.SocketFlag == "" || k.SocketValue == "" || k.ServerPID == "" {
		return false
	}
	if !filepath.IsAbs(k.PhysicalSocket) || filepath.Clean(k.PhysicalSocket) != k.PhysicalSocket {
		return false
	}
	switch k.AuthorityClass {
	case RuntimeMutationRouteApp:
		return k.AppMarker == "1" && k.SocketNameMarker != ""
	case RuntimeMutationRouteStandalone, RuntimeMutationRouteStandaloneExplicit:
		return k.AppMarker == "" && k.SocketNameMarker == ""
	}
	return false
}

// runtimeRouteIdentityStats is observation only. It exists so a test can prove
// that the first proof of a transaction really executed.
type RuntimeRouteIdentityStats struct {
	Proofs           int
	Reuses           int
	Invalidations    int
	LastInvalidation string
	Closed           bool
}

type RuntimeRouteIdentityCache struct {
	Mu          sync.Mutex
	OperationID string
	Open        bool
	// bound is the single exact route target this cache serves.
	Bound  string
	Proved map[RuntimeRouteIdentityKey]struct{}
	Stats  RuntimeRouteIdentityStats
}

// reuse reports whether this exact identity was already proved against tmux in
// this transaction. A nil cache, a closed cache, a different route target, or
// any differing key component answers false, and the caller then runs the full
// verification.
func (c *RuntimeRouteIdentityCache) Reuse(key RuntimeRouteIdentityKey) bool {
	if c == nil || !key.Complete() {
		return false
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if !c.Open {
		return false
	}
	if c.Bound != "" && c.Bound != key.RouteTarget() {
		// A different -S path, -L name, or physical socket is a server swap as
		// far as this transaction is concerned.
		c.InvalidateLocked("route-target-changed")
		return false
	}
	if _, ok := c.Proved[key]; !ok {
		return false
	}
	c.Stats.Reuses++
	return true
}

// wouldReuse answers what reuse would answer for key right now, with none of
// its effects: no reuse is counted and nothing is invalidated. A caller uses
// it only to decide how many values to read in one tmux invocation; the proof
// itself still asks reuse at its usual point.
func (c *RuntimeRouteIdentityCache) WouldReuse(key RuntimeRouteIdentityKey) bool {
	if c == nil || !key.Complete() {
		return false
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if !c.Open || (c.Bound != "" && c.Bound != key.RouteTarget()) {
		return false
	}
	_, ok := c.Proved[key]
	return ok
}

// record stores the result of a verification that just succeeded against tmux.
func (c *RuntimeRouteIdentityCache) Record(key RuntimeRouteIdentityKey) {
	if c == nil || !key.Complete() {
		return
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if !c.Open {
		return
	}
	if c.Bound != key.RouteTarget() {
		if c.Bound != "" {
			c.InvalidateLocked("route-target-changed")
		}
		c.Bound = key.RouteTarget()
	}
	if c.Proved == nil {
		c.Proved = map[RuntimeRouteIdentityKey]struct{}{}
	}
	c.Proved[key] = struct{}{}
	c.Stats.Proofs++
}

// invalidate drops every proof. The next guard performs the full check again.
func (c *RuntimeRouteIdentityCache) Invalidate(reason string) {
	if c == nil {
		return
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.InvalidateLocked(reason)
}

func (c *RuntimeRouteIdentityCache) InvalidateLocked(reason string) {
	if len(c.Proved) == 0 && c.Bound == "" {
		return
	}
	c.Proved = map[RuntimeRouteIdentityKey]struct{}{}
	c.Bound = ""
	c.Stats.Invalidations++
	c.Stats.LastInvalidation = reason
}

// close ends the transaction scope. It is idempotent, and everything after it
// -- runtime ledger rollback included -- re-proves identity against tmux.
func (c *RuntimeRouteIdentityCache) Close() {
	if c == nil {
		return
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.InvalidateLocked("transaction-end")
	c.Open = false
	c.Stats.Closed = true
}

func (c *RuntimeRouteIdentityCache) Snapshot() RuntimeRouteIdentityStats {
	if c == nil {
		return RuntimeRouteIdentityStats{}
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return c.Stats
}

// runtimeRouteIdentityKeyForRoute builds the key a resolved-route proof covers.
// It is cacheable only when the route already carries a physical socket and a
// captured server generation; anything less keeps the guard fully uncached.
func RuntimeRouteIdentityKeyForRoute(scope string, route RuntimeMutationRoute) (RuntimeRouteIdentityKey, bool) {
	if route.Authority == nil || route.ExpectedSocketPath == "" {
		return RuntimeRouteIdentityKey{}, false
	}
	key := RuntimeRouteIdentityKey{
		Scope:          scope,
		SocketFlag:     route.Target.Flag(),
		SocketValue:    route.Target.Value,
		PhysicalSocket: filepath.Clean(route.ExpectedSocketPath),
		AuthorityClass: route.Authority.Class,
		ServerPID:      route.Authority.ServerPID,
	}
	if key.AuthorityClass == RuntimeMutationRouteApp {
		key.AppMarker, key.SocketNameMarker = "1", route.SocketName
	}
	if !key.Complete() {
		return RuntimeRouteIdentityKey{}, false
	}
	return key, true
}
