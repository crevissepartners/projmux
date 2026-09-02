package codexbroker

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// GenerationRoute is the exact endpoint/thread tuple a pool operation names.
// Thread identity alone is never sufficient: the same provider thread token
// presented under another state domain or generation is a typed zero-write
// refusal.
type GenerationRoute struct {
	Endpoint EndpointIdentity `json:"endpoint"`
	ThreadID string           `json:"threadID"`
}

func (route GenerationRoute) valid() bool {
	return route.Endpoint.Valid() && strings.TrimSpace(route.ThreadID) != "" && route.ThreadID == strings.TrimSpace(route.ThreadID)
}

func (route GenerationRoute) key() (EndpointKey, error) {
	if !route.valid() {
		return "", refuse(RefusalRouteMismatch, nil)
	}
	return NewEndpointKey(route.Endpoint.StateDomainID, route.Endpoint.EndpointGenerationID)
}

// PoolAuthority is the complete broker-side authority for one routed binding.
// RuntimeID is deliberately outside Fence because connection and binding
// epochs are local counters and can repeat after a broker process restart.
type PoolAuthority struct {
	Endpoint EndpointKey `json:"endpoint"`
	Runtime  string      `json:"brokerRuntimeID"`
	Fence    Fence       `json:"fence"`
}

// BindingLedgerEntry is the content-free published receipt for one restored
// binding. Machine-local CWD/root inputs stay only in the live PooledBinding;
// they are deliberately absent from the durable/exported ledger.
type BindingLedgerEntry struct {
	ThreadID     string       `json:"threadID"`
	BindingEpoch BindingEpoch `json:"bindingEpoch"`
}

// GenerationLedger is one generation's content-free initialize/snapshot/
// reconnect/binding ledger. Provider payloads, socket paths, PIDs, credentials,
// and prompt content have no representation in this type.
type GenerationLedger struct {
	Endpoint        EndpointIdentity     `json:"endpoint"`
	BrokerRuntimeID string               `json:"brokerRuntimeID"`
	Preparing       bool                 `json:"preparing"`
	Ready           bool                 `json:"ready"`
	Initializes     int                  `json:"initializes"`
	ConnectionEpoch ConnectionEpoch      `json:"connectionEpoch"`
	Snapshots       int                  `json:"snapshots"`
	Reconnects      int                  `json:"reconnects"`
	Restarts        int                  `json:"restarts"`
	BindingRestores int                  `json:"bindingRestores"`
	Bindings        []BindingLedgerEntry `json:"bindings,omitempty"`
}

// PoolConfig is the closed construction input for one dark generation.
type PoolConfig struct {
	Endpoint EndpointIdentity
	Opener   Opener
	Clock    Clock
	Jitter   func() float64
	Backlog  int
}

type poolRuntime struct {
	identity    EndpointIdentity
	key         EndpointKey
	opener      Opener
	clock       Clock
	jitter      func() float64
	backlog     int
	broker      *Broker
	runtime     string
	ready       bool
	restarts    int
	restored    int
	snapshot    int
	initializes int
	reconnects  int
	bindings    map[string]*PooledBinding
	restarting  bool
	// retirementSnapshot freezes the last semantic diagnostics before an
	// administrative close. A failed restore can retry without counting that
	// same closed broker (or its close-induced disconnect) twice.
	retirementSnapshot *Diagnostics
}

// GenerationPool owns independent brokers for a bounded set of private Codex
// endpoint generations. It is intentionally dark: Prepare and exact existing
// bindings are available, while fresh create admission always remains closed
// until the Phase 4 current-pointer owner exists.
type GenerationPool struct {
	mu           sync.Mutex
	closed       bool
	runtimes     map[EndpointKey]*poolRuntime
	threadOwners map[string]EndpointKey

	// beforeBindCommit is a deterministic concurrency-test barrier. Production
	// leaves it nil; it has no product configuration surface.
	beforeBindCommit func()
	// beforeRestartSwap deterministically refuses a test restart after all
	// replacement bindings were built but before any runtime/ledger mutation.
	beforeRestartSwap func() error
}

func NewGenerationPool() *GenerationPool {
	return &GenerationPool{
		runtimes:     make(map[EndpointKey]*poolRuntime),
		threadOwners: make(map[string]EndpointKey),
	}
}

// Prepare adds one independent dark broker runtime. It neither opens an
// upstream wire nor publishes create admission; the broker initializes only
// when an exact existing binding is requested.
func (pool *GenerationPool) Prepare(cfg PoolConfig) error {
	if pool == nil || !cfg.Endpoint.Valid() || cfg.Opener == nil {
		return refuse(RefusalEndpointIdentityInvalid, nil)
	}
	key, err := NewEndpointKey(cfg.Endpoint.StateDomainID, cfg.Endpoint.EndpointGenerationID)
	if err != nil {
		return err
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return refuse(RefusalBrokerClosed, nil)
	}
	if _, exists := pool.runtimes[key]; exists {
		return refuse(RefusalRuntimeExists, nil)
	}
	live := 0
	for _, runtime := range pool.runtimes {
		if runtime.identity.StateDomainID == cfg.Endpoint.StateDomainID {
			live++
		}
	}
	if live >= 2 {
		return refuse(RefusalGenerationCapacityExceeded, nil)
	}
	broker, err := NewBroker(Config{Endpoint: key, Opener: cfg.Opener, Clock: cfg.Clock, Jitter: cfg.Jitter, Backlog: cfg.Backlog})
	if err != nil {
		return err
	}
	runtimeID, err := randomToken(runtimeIDBytes)
	if err != nil {
		_ = broker.Close()
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	pool.runtimes[key] = &poolRuntime{
		identity: cfg.Endpoint, key: key, opener: cfg.Opener, clock: cfg.Clock,
		jitter: cfg.Jitter, backlog: cfg.Backlog, broker: broker, runtime: runtimeID,
		bindings: make(map[string]*PooledBinding),
	}
	return nil
}

// AdmitCreate is the Phase 2 dark-readiness gate. Even a ready Preparing
// endpoint produces no binding, provider call, Registry write, tmux write, or
// lifecycle mutation.
func (pool *GenerationPool) AdmitCreate(route GenerationRoute) error {
	key, err := route.key()
	if err != nil {
		return err
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return refuse(RefusalBrokerClosed, nil)
	}
	if _, exists := pool.runtimes[key]; !exists {
		return refuse(RefusalEndpointUnknown, nil)
	}
	return refuse(RefusalAdmissionClosed, nil)
}

// BindExisting attaches one exact, already-owned thread to its generation.
// A thread token already owned by another state-domain/generation route is
// refused before either endpoint receives a request.
func (pool *GenerationPool) BindExisting(route GenerationRoute, cwd string, roots []string) (*PooledBinding, error) {
	key, err := route.key()
	if err != nil {
		return nil, err
	}
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil, refuse(RefusalBrokerClosed, nil)
	}
	runtime := pool.runtimes[key]
	if runtime == nil {
		pool.mu.Unlock()
		return nil, refuse(RefusalEndpointUnknown, nil)
	}
	if runtime.restarting {
		pool.mu.Unlock()
		return nil, refuse(RefusalBrokerRestarting, nil)
	}
	if owner, exists := pool.threadOwners[route.ThreadID]; exists {
		pool.mu.Unlock()
		if owner != key {
			return nil, refuse(RefusalRouteMismatch, nil)
		}
		return nil, refuse(RefusalBindingExists, nil)
	}
	// Reserve the thread before opening the provider wire. A concurrent bind
	// under another route is therefore refused before either endpoint writes.
	pool.threadOwners[route.ThreadID] = key
	broker := runtime.broker
	pool.mu.Unlock()

	binding, err := broker.Bind(route.ThreadID, cwd, roots)
	if err != nil {
		pool.releaseBindingReservation(key, route.ThreadID, runtime)
		return nil, err
	}
	if pool.beforeBindCommit != nil {
		pool.beforeBindCommit()
	}
	pooled := &PooledBinding{
		pool: pool, route: route, cwd: strings.TrimSpace(cwd), roots: append([]string(nil), roots...),
		events: make(chan Event, defaultBacklog), suspends: make(chan struct{}, 1),
		inbound: make(chan Event), inboundSuspends: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go pooled.dispatch()
	pool.mu.Lock()
	if pool.closed || runtime.restarting || runtime.broker != broker {
		if owner, reserved := pool.threadOwners[route.ThreadID]; reserved && owner == key {
			delete(pool.threadOwners, route.ThreadID)
		}
		pool.mu.Unlock()
		_ = binding.Close()
		return nil, refuse(RefusalBrokerRuntimeStale, nil)
	}
	runtime.bindings[route.ThreadID] = pooled
	pool.threadOwners[route.ThreadID] = key
	pooled.attachLocked(binding, runtime.runtime)
	pool.mu.Unlock()
	return pooled, nil
}

func (pool *GenerationPool) releaseBindingReservation(key EndpointKey, threadID string, runtime *poolRuntime) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if owner, reserved := pool.threadOwners[threadID]; reserved && owner == key {
		if current := pool.runtimes[key]; current == runtime && current.bindings[threadID] == nil {
			delete(pool.threadOwners, threadID)
		}
	}
}

// RestartBroker replaces only one generation's broker and restores every
// exact route in its binding ledger. Sibling generation brokers, epochs,
// bindings, and provider wires are not touched.
func (pool *GenerationPool) RestartBroker(endpoint EndpointIdentity, opener Opener) error {
	key, err := NewEndpointKey(endpoint.StateDomainID, endpoint.EndpointGenerationID)
	if err != nil || opener == nil {
		return refuse(RefusalEndpointIdentityInvalid, err)
	}
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return refuse(RefusalBrokerClosed, nil)
	}
	runtime := pool.runtimes[key]
	if runtime == nil {
		pool.mu.Unlock()
		return refuse(RefusalEndpointUnknown, nil)
	}
	if runtime.restarting {
		pool.mu.Unlock()
		return refuse(RefusalBrokerRestarting, nil)
	}
	runtime.restarting = true
	old := runtime.broker
	entries := make([]*PooledBinding, 0, len(runtime.bindings))
	for _, binding := range runtime.bindings {
		entries = append(entries, binding)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].binding.epoch < entries[j].binding.epoch })
	oldDiagnostics := old.Diagnostics()
	if runtime.retirementSnapshot != nil {
		oldDiagnostics = *runtime.retirementSnapshot
	} else {
		snapshot := oldDiagnostics
		runtime.retirementSnapshot = &snapshot
	}
	clock, jitter, backlog := runtime.clock, runtime.jitter, runtime.backlog
	pool.mu.Unlock()
	defer func() {
		pool.mu.Lock()
		runtime.restarting = false
		pool.mu.Unlock()
	}()

	if err := old.Close(); err != nil {
		return refuse(RefusalBindingRestoreFailed, err)
	}
	replacement, err := NewBroker(Config{Endpoint: key, Opener: opener, Clock: clock, Jitter: jitter, Backlog: backlog})
	if err != nil {
		return refuse(RefusalBindingRestoreFailed, err)
	}
	runtimeID, err := randomToken(runtimeIDBytes)
	if err != nil {
		_ = replacement.Close()
		return refuse(RefusalBindingRestoreFailed, err)
	}
	restored := make(map[*PooledBinding]*Binding, len(entries))
	for _, pooled := range entries {
		binding, bindErr := replacement.bindAtEpoch(pooled.route.ThreadID, pooled.cwd, pooled.roots, pooled.binding.epoch)
		if bindErr != nil {
			_ = replacement.Close()
			return refuse(RefusalBindingRestoreFailed, bindErr)
		}
		restored[pooled] = binding
	}
	if pool.beforeRestartSwap != nil {
		if err := pool.beforeRestartSwap(); err != nil {
			_ = replacement.Close()
			return refuse(RefusalBindingRestoreFailed, err)
		}
	}

	pool.mu.Lock()
	if pool.closed || runtime.broker != old {
		pool.mu.Unlock()
		_ = replacement.Close()
		return refuse(RefusalBrokerRuntimeStale, nil)
	}
	runtime.initializes += oldDiagnostics.Connects
	runtime.reconnects += oldDiagnostics.Disconnects
	runtime.retirementSnapshot = nil
	runtime.broker = replacement
	runtime.opener = opener
	runtime.runtime = runtimeID
	runtime.ready = false
	runtime.restarts++
	for pooled, binding := range restored {
		pooled.attachLocked(binding, runtimeID)
		runtime.restored++
	}
	pool.mu.Unlock()
	return nil
}

// Ledger returns stable, content-free per-generation runtime state.
func (pool *GenerationPool) Ledger() []GenerationLedger {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	ledgers := make([]GenerationLedger, 0, len(pool.runtimes))
	for _, runtime := range pool.runtimes {
		diag := runtime.broker.Diagnostics()
		if runtime.retirementSnapshot != nil {
			diag = *runtime.retirementSnapshot
		}
		entries := make([]BindingLedgerEntry, 0, len(runtime.bindings))
		for _, binding := range runtime.bindings {
			entry := BindingLedgerEntry{ThreadID: binding.route.ThreadID}
			if binding.binding != nil {
				entry.BindingEpoch = binding.binding.epoch
			}
			entries = append(entries, entry)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].ThreadID < entries[j].ThreadID })
		ledgers = append(ledgers, GenerationLedger{
			Endpoint: runtime.identity, BrokerRuntimeID: runtime.runtime, Preparing: true,
			Ready: runtime.ready, Initializes: runtime.initializes + diag.Connects, ConnectionEpoch: diag.ConnectionEpoch,
			Snapshots: runtime.snapshot, Reconnects: runtime.reconnects + diag.Disconnects,
			Restarts: runtime.restarts, BindingRestores: runtime.restored,
			Bindings: entries,
		})
	}
	sort.Slice(ledgers, func(i, j int) bool {
		if ledgers[i].Endpoint.StateDomainID != ledgers[j].Endpoint.StateDomainID {
			return ledgers[i].Endpoint.StateDomainID < ledgers[j].Endpoint.StateDomainID
		}
		return ledgers[i].Endpoint.EndpointGenerationID < ledgers[j].Endpoint.EndpointGenerationID
	})
	return ledgers
}

// Close terminates only brokers the pool created.
func (pool *GenerationPool) Close() error {
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil
	}
	pool.closed = true
	runtimes := make([]*poolRuntime, 0, len(pool.runtimes))
	for _, runtime := range pool.runtimes {
		runtimes = append(runtimes, runtime)
		for _, binding := range runtime.bindings {
			binding.closeLocked()
		}
	}
	pool.mu.Unlock()
	for _, runtime := range runtimes {
		_ = runtime.broker.Close()
	}
	return nil
}

// PooledBinding is one logical binding that survives a broker restart while
// retaining its exact endpoint/thread route.
type PooledBinding struct {
	pool  *GenerationPool
	route GenerationRoute
	cwd   string
	roots []string

	events          chan Event
	suspends        chan struct{}
	inbound         chan Event
	inboundSuspends chan struct{}
	done            chan struct{}

	binding *Binding
	runtime string
	closed  bool
}

func (binding *PooledBinding) Route() GenerationRoute { return binding.route }
func (binding *PooledBinding) Events() <-chan Event   { return binding.events }
func (binding *PooledBinding) Suspensions() <-chan struct{} {
	return binding.suspends
}

// ControlAuthority returns the exact endpoint/runtime/local-epoch tuple.
func (binding *PooledBinding) ControlAuthority() (PoolAuthority, error) {
	pool := binding.pool
	pool.mu.Lock()
	if binding.closed || binding.binding == nil {
		pool.mu.Unlock()
		return PoolAuthority{}, refuse(RefusalBindingClosed, nil)
	}
	key, _ := binding.route.key()
	if runtime := pool.runtimes[key]; runtime != nil && runtime.restarting {
		pool.mu.Unlock()
		return PoolAuthority{}, refuse(RefusalBrokerRestarting, nil)
	}
	underlying, runtime := binding.binding, binding.runtime
	pool.mu.Unlock()
	fence, err := underlying.ControlAuthority()
	if err != nil {
		return PoolAuthority{}, err
	}
	return PoolAuthority{Endpoint: key, Runtime: runtime, Fence: fence}, nil
}

// Submit refuses every route/runtime mismatch before delegating to the exact
// broker binding, which performs the connection/binding epoch check.
func (binding *PooledBinding) Submit(ctx context.Context, route GenerationRoute, authority PoolAuthority, mutation Mutation) (MutationOutcome, error) {
	wantKey, err := binding.route.key()
	if err != nil {
		return MutationRefused, err
	}
	gotKey, routeErr := route.key()
	if routeErr != nil || gotKey != wantKey || route.ThreadID != binding.route.ThreadID || authority.Endpoint != wantKey {
		return MutationRefused, refuse(RefusalRouteMismatch, routeErr)
	}
	pool := binding.pool
	pool.mu.Lock()
	if binding.closed || binding.binding == nil {
		pool.mu.Unlock()
		return MutationRefused, refuse(RefusalBindingClosed, nil)
	}
	if runtime := pool.runtimes[wantKey]; runtime != nil && runtime.restarting {
		pool.mu.Unlock()
		return MutationRefused, refuse(RefusalBrokerRestarting, nil)
	}
	if authority.Runtime != binding.runtime {
		pool.mu.Unlock()
		return MutationRefused, refuse(RefusalBrokerRuntimeStale, nil)
	}
	underlying := binding.binding
	pool.mu.Unlock()
	return underlying.Submit(ctx, authority.Fence, mutation)
}

func (binding *PooledBinding) Close() error {
	pool := binding.pool
	pool.mu.Lock()
	if binding.closed {
		pool.mu.Unlock()
		return nil
	}
	key, _ := binding.route.key()
	if runtime := pool.runtimes[key]; runtime != nil && runtime.restarting {
		pool.mu.Unlock()
		return refuse(RefusalBrokerRestarting, nil)
	} else if runtime != nil {
		delete(runtime.bindings, binding.route.ThreadID)
	}
	binding.closeLocked()
	delete(pool.threadOwners, binding.route.ThreadID)
	underlying := binding.binding
	binding.binding = nil
	pool.mu.Unlock()
	if underlying != nil {
		return underlying.Close()
	}
	return nil
}

func (binding *PooledBinding) attachLocked(underlying *Binding, runtime string) {
	binding.binding = underlying
	binding.runtime = runtime
	go binding.pump(underlying, runtime)
}

func (binding *PooledBinding) pump(underlying *Binding, runtime string) {
	events, suspends := underlying.Events(), underlying.Suspensions()
	for events != nil || suspends != nil {
		select {
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			pool := binding.pool
			pool.mu.Lock()
			current := !binding.closed && binding.binding == underlying && binding.runtime == runtime
			if current && event.Origin == EventOriginSnapshot {
				if key, err := binding.route.key(); err == nil && pool.runtimes[key] != nil {
					pool.runtimes[key].ready = true
					pool.runtimes[key].snapshot++
				}
			}
			pool.mu.Unlock()
			if current {
				select {
				case binding.inbound <- event:
				case <-binding.done:
					return
				}
			}
		case _, open := <-suspends:
			if !open {
				suspends = nil
				continue
			}
			binding.pool.mu.Lock()
			current := !binding.closed && binding.binding == underlying && binding.runtime == runtime
			binding.pool.mu.Unlock()
			if current {
				select {
				case binding.inboundSuspends <- struct{}{}:
				default:
				}
			}
		case <-binding.done:
			return
		}
	}
}

// dispatch is the sole closer and writer of the public channels. Restart
// pumps only write to the private inbound channels, so Close cannot race a
// stale pump between a liveness check and a send to a closed public stream.
func (binding *PooledBinding) dispatch() {
	defer close(binding.events)
	defer close(binding.suspends)
	for {
		select {
		case event := <-binding.inbound:
			select {
			case binding.events <- event:
			case <-binding.done:
				return
			}
		case <-binding.inboundSuspends:
			select {
			case binding.suspends <- struct{}{}:
			default:
			}
		case <-binding.done:
			return
		}
	}
}

func (binding *PooledBinding) closeLocked() {
	if binding.closed {
		return
	}
	binding.closed = true
	close(binding.done)
}
