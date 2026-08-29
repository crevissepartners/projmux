package codexbroker

import (
	"context"
	"errors"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// defaultBacklog bounds one binding's undelivered events. It is per binding
// rather than per connection so a consumer that stops reading can only starve
// itself; see Binding for why overflow revokes instead of dropping.
const defaultBacklog = 64

// Config is the closed construction input for one Broker.
type Config struct {
	// Endpoint names the endpoint to multiplex. Zero means DefaultEndpointKey.
	Endpoint EndpointKey
	// Opener opens one connection. Required.
	Opener Opener
	// Clock is the reconnect backoff time source. Zero means the system clock.
	Clock Clock
	// Jitter returns a value in [0,1) used to spread reconnect waits. Zero
	// means a real pseudo-random source.
	Jitter func() float64
	// Backlog bounds each binding's undelivered events. Zero means
	// defaultBacklog; a negative value is clamped to it.
	Backlog int
}

// connection is one epoch of the shared endpoint. Everything except barriers
// is guarded by Broker.mu, including answered, so approval fencing and
// connection replacement are decided under the same lock and cannot interleave.
type connection struct {
	epoch    ConnectionEpoch
	endpoint Endpoint
	// answered is the connection-scoped approval response-once ledger, keyed
	// by the raw JSON-RPC request id bytes. Entries are never released: a
	// second answer whose first delivery is unknown is worse than none.
	answered map[string]struct{}
	// barriers tracks the snapshot goroutines this connection started, so an
	// epoch is fully retired before the next one opens.
	barriers sync.WaitGroup
}

// Broker multiplexes one shared endpoint connection across many bindings.
//
// Exactly one supervisor goroutine owns connection lifecycle, and one
// notification pump lives inside it, so connection replacement, event routing,
// and epoch revocation are serialized by construction rather than by a lock
// discipline every call site has to remember.
type Broker struct {
	endpoint EndpointKey
	opener   Opener
	clock    Clock
	jitter   func() float64
	backlog  int

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
	exit   chan struct{}

	mu        sync.Mutex
	closing   bool
	conn      *connection
	connEpoch ConnectionEpoch
	bindEpoch BindingEpoch
	bindings  map[string]*Binding
	diag      Diagnostics
	// revocations counts involuntary binding terminations by closed reason.
	// It is a separate map rather than a Diagnostics field so the snapshot the
	// caller receives is a value copy that cannot alias live broker state.
	revocations map[Refusal]int
	ledger      []WriteRecord
}

// NewBroker starts one broker and its supervisor. The caller must Close it.
//
// An unknown endpoint key is refused here rather than at the first connect,
// because this phase can reach exactly one endpoint and letting a caller hold
// a Broker that can never connect only defers the same refusal.
func NewBroker(cfg Config) (*Broker, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpointKey
	}
	if cfg.Endpoint != DefaultEndpointKey || cfg.Opener == nil {
		return nil, refuse(RefusalEndpointUnknown, nil)
	}
	broker := &Broker{
		endpoint: cfg.Endpoint,
		opener:   cfg.Opener,
		clock:    cfg.Clock,
		jitter:   cfg.Jitter,
		backlog:  cfg.Backlog,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		exit:     make(chan struct{}),
		bindings: make(map[string]*Binding),
	}
	if broker.clock == nil {
		broker.clock = systemClock{}
	}
	if broker.jitter == nil {
		broker.jitter = rand.Float64
	}
	if broker.backlog <= 0 {
		broker.backlog = defaultBacklog
	}
	broker.ctx, broker.cancel = context.WithCancel(context.Background())
	go broker.supervise()
	return broker, nil
}

// Bind attaches one exact thread to the shared endpoint.
//
// threadID is mandatory and is used verbatim. The broker never derives a
// binding from cwd, from wall time, from a pid, from pane order, or from the
// newest thread it happens to know about; cwd and roots are bootstrap inputs
// for the snapshot only.
//
// Bind does not wait for a connection. The binding's stream begins with one
// EventOriginSnapshot event once the barrier closes, and that event carries
// the fence every later mutation must present.
func (b *Broker) Bind(threadID, cwd string, roots []string) (*Binding, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, refuse(RefusalThreadRequired, nil)
	}
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return nil, refuse(RefusalBrokerClosed, nil)
	}
	if _, exists := b.bindings[threadID]; exists {
		b.mu.Unlock()
		return nil, refuse(RefusalBindingExists, nil)
	}
	b.bindEpoch++
	binding := &Binding{
		broker:   b,
		threadID: threadID,
		cwd:      strings.TrimSpace(cwd),
		roots:    append([]string(nil), roots...),
		epoch:    b.bindEpoch,
		events:   make(chan Event, b.backlog),
		suspends: make(chan struct{}, 1),
		revoked:  RefusalNone,
	}
	b.bindings[threadID] = binding
	b.mu.Unlock()
	b.signal()
	return binding, nil
}

// Close stops the supervisor, revokes every binding with RefusalBrokerClosed,
// and returns only after the endpoint and every barrier goroutine are gone.
// It is idempotent and safe to call concurrently.
func (b *Broker) Close() error {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		<-b.exit
		return nil
	}
	b.closing = true
	b.mu.Unlock()
	b.cancel()
	close(b.done)
	<-b.exit
	return nil
}

// Diagnostics returns the current content-free telemetry snapshot.
func (b *Broker) Diagnostics() Diagnostics {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := b.diag
	snapshot.Endpoint = b.endpoint
	snapshot.ConnectionEpoch = b.connEpoch
	snapshot.Bindings = len(b.bindings)
	snapshot.Revocations = b.revocationCountsLocked()
	return snapshot
}

// WriteLedger returns a copy of every mutation this broker has terminated.
func (b *Broker) WriteLedger() []WriteRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]WriteRecord(nil), b.ledger...)
}

// supervise owns the whole connection lifecycle. It reconnects for as long as
// at least one binding exists and the broker is open, with no retry-count
// exhaustion: a bound thread that is merely waiting for its endpoint to come
// back must not be abandoned because an arbitrary counter ran out.
func (b *Broker) supervise() {
	defer close(b.exit)
	attempt := 0
	for {
		if b.stopping() {
			b.shutdown()
			return
		}
		if !b.wanted() {
			select {
			case <-b.wake:
			case <-b.done:
			}
			continue
		}
		b.mu.Lock()
		b.diag.OpenAttempts++
		b.mu.Unlock()
		endpoint, err := b.opener(b.ctx)
		if err != nil {
			attempt++
			select {
			case <-b.clock.After(backoffDelay(attempt, b.jitter())):
			case <-b.done:
			}
			continue
		}
		attempt = 0
		b.serve(endpoint)
	}
}

// serve runs one connection epoch from open to fully revoked.
func (b *Broker) serve(endpoint Endpoint) {
	conn := b.open(endpoint)
	events := endpoint.Notifications()
	b.startBarriers(conn)
	for alive := true; alive; {
		select {
		case notification, ok := <-events:
			if !ok {
				alive = false
				break
			}
			b.routeNotification(conn, notification)
		case <-b.wake:
			// A bind joined, or the last binding left. Either the new binding
			// needs its barrier, or this connection has nothing left to serve.
			if !b.wanted() {
				alive = false
				break
			}
			b.startBarriers(conn)
		case <-b.done:
			alive = false
		}
	}
	// Revoke this epoch before anything can open the next one, then close the
	// endpoint so a barrier still waiting on it is released, and only then
	// declare the epoch retired.
	b.revoke(conn)
	_ = endpoint.Close()
	conn.barriers.Wait()
}

// open installs the next connection epoch.
func (b *Broker) open(endpoint Endpoint) *connection {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connEpoch++
	b.diag.Connects++
	conn := &connection{epoch: b.connEpoch, endpoint: endpoint, answered: make(map[string]struct{})}
	b.conn = conn
	return conn
}

// startBarriers opens the snapshot barrier for every binding this connection
// is not already serving.
//
// The barrier is deliberately "buffer, then snapshot, then merge" and never
// "subscribe, then snapshot": upstream offers no pre-turn subscription that
// can be confirmed, so the only gap-free order is to start buffering live
// events at connect and fold them in behind the snapshot afterwards.
func (b *Broker) startBarriers(conn *connection) {
	b.mu.Lock()
	var pending []*Binding
	if b.conn == conn {
		for _, binding := range b.bindings {
			if binding.stage == stageIdle {
				binding.stage = stageBuffering
				binding.conn = conn
				pending = append(pending, binding)
			}
		}
	}
	b.mu.Unlock()
	for _, binding := range pending {
		conn.barriers.Add(1)
		go b.runBarrier(conn, binding)
	}
}

// runBarrier takes one binding's snapshot and closes its barrier.
func (b *Broker) runBarrier(conn *connection, binding *Binding) {
	defer conn.barriers.Done()
	snapshot, err := conn.endpoint.BootstrapThread(b.ctx, binding.threadID, binding.cwd, binding.roots)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != conn || binding.revoked != RefusalNone {
		// The epoch was replaced, or the binding died, while the snapshot was
		// in flight. Whatever came back describes a world that no longer has
		// authority, so it is dropped rather than merged.
		return
	}
	if err != nil {
		if errors.Is(err, codexappserver.ErrDisconnected) || errors.Is(err, context.Canceled) {
			// Connection-scoped fault. The next epoch re-runs this barrier.
			binding.stage = stageIdle
			binding.conn = nil
			return
		}
		// A live endpoint refused this exact thread, which is a thread-scoped
		// fault. Only this binding is revoked; the shared connection and every
		// other binding keep running.
		b.revokeBindingLocked(binding, RefusalSnapshotUnavailable)
		return
	}
	binding.closeBarrierLocked(b, conn, snapshot)
}

// routeNotification delivers one endpoint message to at most one binding.
func (b *Broker) routeNotification(conn *connection, notification codexappserver.Notification) {
	threadID, attributed := AttributeNotification(notification)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != conn {
		// A message from a revoked epoch. It cannot write over the current
		// one, so it is counted and dropped.
		b.diag.StaleEvents++
		return
	}
	if !attributed {
		b.diag.ThreadlessEvents++
		return
	}
	binding, bound := b.bindings[threadID]
	if !bound {
		b.diag.UnboundEvents++
		return
	}
	binding.acceptLocked(b, conn, notification)
}

// revoke retires one connection epoch and suspends every binding it served.
func (b *Broker) revoke(conn *connection) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != conn {
		return
	}
	b.conn = nil
	b.diag.Disconnects++
	for _, binding := range b.bindings {
		binding.suspendLocked()
	}
}

// shutdown terminates every remaining binding and endpoint at Close.
func (b *Broker) shutdown() {
	b.mu.Lock()
	conn := b.conn
	b.conn = nil
	for _, binding := range b.bindings {
		b.revokeBindingLocked(binding, RefusalBrokerClosed)
	}
	b.mu.Unlock()
	if conn != nil {
		_ = conn.endpoint.Close()
		conn.barriers.Wait()
	}
}

// revokeBindingLocked terminates one binding and removes it from routing.
// Caller holds b.mu.
func (b *Broker) revokeBindingLocked(binding *Binding, reason Refusal) {
	if binding.revoked != RefusalNone {
		return
	}
	binding.revokeLocked(reason)
	delete(b.bindings, binding.threadID)
	switch reason {
	case RefusalBindingClosed, RefusalBrokerClosed:
		b.diag.ReleasedBindings++
	default:
		b.diag.RevokedBindings++
		b.countRevocationLocked(reason)
	}
}

// countRevocationLocked records one involuntary revocation under its closed
// reason. Caller holds b.mu.
func (b *Broker) countRevocationLocked(reason Refusal) {
	if b.revocations == nil {
		b.revocations = make(map[Refusal]int)
	}
	b.revocations[reason]++
}

// revocationCountsLocked projects the reason breakdown in a stable order.
// Caller holds b.mu.
func (b *Broker) revocationCountsLocked() []RevocationCount {
	if len(b.revocations) == 0 {
		return nil
	}
	counts := make([]RevocationCount, 0, len(b.revocations))
	for reason, count := range b.revocations {
		counts = append(counts, RevocationCount{Reason: reason, Count: count})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].Reason < counts[j].Reason })
	return counts
}

// classify turns one control request's transport result into its terminal
// outcome. A disconnect, a cancellation, or a connection that was replaced
// while the request was in flight all leave the result unknown.
func (b *Broker) classify(conn *connection, err error) MutationOutcome {
	if err == nil {
		return MutationApplied
	}
	if errors.Is(err, codexappserver.ErrDisconnected) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return MutationIndeterminate
	}
	b.mu.Lock()
	current := b.conn == conn
	b.mu.Unlock()
	if !current {
		return MutationIndeterminate
	}
	return MutationRefused
}

// record appends one terminated mutation to the write ledger.
func (b *Broker) record(entry WriteRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ledger = append(b.ledger, entry)
	switch entry.Outcome {
	case MutationApplied:
		b.diag.Applied++
	case MutationIndeterminate:
		b.diag.Indeterminate++
	default:
		b.diag.Refused++
	}
}

func (b *Broker) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *Broker) stopping() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

func (b *Broker) wanted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closing && len(b.bindings) > 0
}
