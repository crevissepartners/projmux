package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

const (
	// codexBrokerObserverOpenTimeout bounds one wait for the snapshot barrier
	// to close. It is far longer than a proxy dial because the wait it bounds
	// is a reconnect the broker is already retrying with its own capped
	// backoff; a short bound here would turn a served outage into a hot loop
	// of observer-side reopens.
	codexBrokerObserverOpenTimeout = 15 * time.Second
	// codexBrokerObserverBacklog bounds one epoch view's undelivered
	// notifications. It matches the broker's own per-binding bound, so a
	// consumer that stops reading is cut at the same depth on both sides
	// rather than being handed a stream with an undetectable hole.
	codexBrokerObserverBacklog = 64
	// codexBrokerObserverStartupTimeout bounds reaching or starting the
	// runtime for one binding.
	codexBrokerObserverStartupTimeout = 10 * time.Second
)

// The broker epoch is the whole native producer for one activation: the
// lifecycle stream the observer reduces, the control wire the exact-Agent
// control epoch mutates through, and the typed requester those wire shapes are
// defined on.
var (
	_ codexLifecycleConnection  = (*codexBrokerLifecycleEpoch)(nil)
	_ codexLifecycleStreamCause = (*codexBrokerLifecycleEpoch)(nil)
	_ agentControlWire          = (*codexBrokerLifecycleEpoch)(nil)
	_ codexappserver.Requester  = (*codexBrokerLifecycleEpoch)(nil)
)

// codexBrokerEpochRecord is one closed snapshot barrier waiting to be consumed
// by the next observer epoch.
type codexBrokerEpochRecord struct {
	fence    codexbroker.Fence
	snapshot codexappserver.ThreadSnapshot
}

// codexBrokerObserverSession owns one durable broker binding for one exact
// Agent activation, across every connection epoch that binding outlives.
//
// The observer above it still thinks in terms of "open a connection, consume
// until it ends, reopen". That shape is preserved deliberately: each closed
// snapshot barrier is handed up as a fresh connection, and a barrier that
// reopens ends the previous one, so the observer publishes its exact
// invalidation before the replacement epoch's authority is claimed. What the
// observer no longer owns is the reconnect itself. The binding survives the
// outage, the broker retries it with capped backoff for as long as the binding
// exists, and there is therefore no attempt ceiling that could strand a live
// activation in the unavailable reconnect projection.
type codexBrokerObserverSession struct {
	identity  codexLifecycleIdentity
	endpoint  coremetadata.CodexEndpointRef
	cwd       string
	roots     []string
	discovery codexbroker.Discovery
	launch    codexbroker.Launcher

	mu         sync.Mutex
	closed     bool
	conn       *codexbroker.Conn
	binding    *codexbroker.RemoteBinding
	current    *codexBrokerLifecycleEpoch
	ready      chan codexBrokerEpochRecord
	pumped     chan struct{}
	pending    codexBrokerEpochRecord
	hasPending bool
}

// newCodexBrokerObserverSessionForRoute resolves one broker singleton from the
// durable endpoint generation selected before provider creation. The broker
// runtime is keyed by that exact endpoint and its launcher receives only the
// corresponding attach transport; changing admission-current cannot retarget
// a live session.
func newCodexBrokerObserverSessionForRoute(identity codexLifecycleIdentity, cwd string, roots []string, route codexNativeEndpointRoute) (*codexBrokerObserverSession, error) {
	if !identity.valid() || !route.valid() {
		return nil, errors.New("codex generation broker binding requires exact Agent, Pane, endpoint, runtime, generation, and thread identity")
	}
	if !codexbroker.Supported() {
		return nil, errors.New("codex broker runtime requires Unix filesystem semantics")
	}
	domain, err := codexBrokerStateDomain(os.Getenv, os.UserHomeDir)
	if err != nil {
		return nil, err
	}
	key, err := codexbroker.NewEndpointKey(route.Endpoint.StateDomainID, route.Endpoint.EndpointGenerationID)
	if err != nil {
		return nil, err
	}
	discovery, err := codexBrokerDiscoveryForEndpoint(domain, key)
	if err != nil {
		return nil, err
	}
	brokerRoute := route.brokerRoute()
	launch := func(context.Context) error {
		path, err := os.Executable()
		if err != nil {
			return err
		}
		return startCodexBrokerRuntimeProcessForRoute(path, discovery, brokerRoute)
	}
	session := newCodexBrokerObserverSessionOn(identity, cwd, roots, discovery, launch)
	session.endpoint = route.Endpoint
	return session, nil
}

// newCodexBrokerObserverSessionOn binds one session to an explicit runtime
// contract. A nil launcher makes the session a strict discovery: it reaches an
// already published runtime and refuses rather than starting one.
func newCodexBrokerObserverSessionOn(
	identity codexLifecycleIdentity,
	cwd string,
	roots []string,
	discovery codexbroker.Discovery,
	launch codexbroker.Launcher,
) *codexBrokerObserverSession {
	return &codexBrokerObserverSession{identity: identity, cwd: cwd, roots: roots, discovery: discovery, launch: launch}
}

// Open returns the next closed snapshot barrier as one lifecycle connection.
//
// It reaches the runtime, starting one only when discovery proves none is
// there, and binds exactly the thread this activation owns. It never guesses a
// thread from the working directory, and it never creates one.
func (s *codexBrokerObserverSession) Open(ctx context.Context) (codexLifecycleConnection, error) {
	binding, ready, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	// A barrier that is still closed keeps serving. The observer above tears an
	// epoch down for reasons the connection knows nothing about - a snapshot
	// read that timed out against a wedged endpoint, a sink write that failed -
	// and making it wait for a fresh barrier there would strand it on hook
	// fallback until the endpoint happened to reconnect. A suspension discards
	// the record, so a closed authority is never re-served.
	s.mu.Lock()
	if s.hasPending && s.current == nil {
		record := s.pending
		s.mu.Unlock()
		return s.publish(record)
	}
	s.mu.Unlock()
	select {
	case record, open := <-ready:
		if !open {
			s.discard()
			return nil, s.revocation(binding)
		}
		return s.publish(record)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close releases the binding and the runtime session. The runtime idles itself
// out once no binding is left, so releasing here is the whole shutdown.
func (s *codexBrokerObserverSession) Close() error {
	s.mu.Lock()
	s.closed = true
	binding, conn, current := s.binding, s.conn, s.current
	s.binding, s.conn, s.current, s.ready = nil, nil, nil, nil
	s.pending, s.hasPending = codexBrokerEpochRecord{}, false
	pumped := s.pumped
	s.pumped = nil
	s.mu.Unlock()
	if current != nil {
		current.end(codexObserverReasonEpochClosed)
	}
	if binding != nil {
		_ = binding.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	if pumped != nil {
		<-pumped
	}
	return nil
}

// ensure resolves the live binding, establishing the connection and the
// binding first when this is the first epoch or the previous one was revoked.
func (s *codexBrokerObserverSession) ensure(ctx context.Context) (*codexbroker.RemoteBinding, chan codexBrokerEpochRecord, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, errors.New("codex broker binding session is closed")
	}
	if s.binding != nil {
		binding, ready := s.binding, s.ready
		s.mu.Unlock()
		return binding, ready, nil
	}
	conn := s.conn
	s.mu.Unlock()

	if conn != nil {
		select {
		case <-conn.Done():
			_ = conn.Close()
			conn = nil
		default:
		}
	}
	if conn == nil {
		opened, err := codexbroker.Ensure(ctx, s.discovery, codexbroker.EnsureConfig{
			Launch:         s.launch,
			StartupTimeout: codexBrokerObserverStartupTimeout,
		})
		if err != nil {
			return nil, nil, err
		}
		conn = opened
	}
	binding, err := conn.Bind(ctx, s.identity.ThreadID, s.cwd, s.roots)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	ready := make(chan codexBrokerEpochRecord, 1)
	pumped := make(chan struct{})
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = binding.Close()
		_ = conn.Close()
		close(pumped)
		return nil, nil, errors.New("codex broker binding session is closed")
	}
	s.conn, s.binding, s.ready, s.pumped = conn, binding, ready, pumped
	s.mu.Unlock()
	go s.pump(binding, ready, pumped)
	return binding, ready, nil
}

// pump is the single reader of one binding's ordered stream.
//
// A snapshot event is a barrier close, so it ends whichever epoch was live and
// offers the new one. Every other event belongs to the live epoch. When the
// stream closes the binding has been revoked, and the session drops it so the
// next Open rebinds rather than presenting authority the runtime already
// retired.
func (s *codexBrokerObserverSession) pump(binding *codexbroker.RemoteBinding, ready chan codexBrokerEpochRecord, pumped chan struct{}) {
	defer close(pumped)
	defer close(ready)
	events := binding.Events()
	suspends := binding.Suspensions()
	for events != nil {
		select {
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			if event.Origin == codexbroker.EventOriginSnapshot {
				s.rotateAfterPendingSuspension(
					codexBrokerEpochRecord{fence: event.Fence, snapshot: event.Snapshot}, ready, suspends,
				)
				continue
			}
			s.mu.Lock()
			current := s.current
			s.mu.Unlock()
			if current != nil {
				current.deliver(event)
			}
		case <-suspends:
			// The connection this epoch was minted on is gone and no
			// replacement is open yet. Ending the epoch here promptly revokes
			// exact control and lets the observer publish its one stable
			// unavailable projection while this binding keeps reconnecting.
			s.retire(ready)
		}
	}
	s.endAfterStreamRevoked(binding)
}

// endAfterStreamRevoked ends the live epoch after one binding's ordered stream
// closed, which is the runtime having revoked that binding. It is a named
// method rather than a tail block so the cause it records is reachable from a
// test without a live broker.
func (s *codexBrokerObserverSession) endAfterStreamRevoked(binding *codexbroker.RemoteBinding) {
	s.mu.Lock()
	var current *codexBrokerLifecycleEpoch
	if s.binding == binding {
		current = s.current
		s.current, s.binding, s.ready = nil, nil, nil
		s.pending, s.hasPending = codexBrokerEpochRecord{}, false
	}
	s.mu.Unlock()
	if current != nil {
		current.end(codexObserverReasonBindingRevoked)
	}
}

// rotateAfterPendingSuspension preserves the remote binding's causal order.
// IPC guarantees the suspension frame precedes a replacement snapshot, but
// select may still choose the buffered snapshot first because the client
// mirrors them on separate channels. Retire the old epoch before rotating so
// that edge cannot arrive later and tear down the replacement authority.
func (s *codexBrokerObserverSession) rotateAfterPendingSuspension(
	record codexBrokerEpochRecord,
	ready chan codexBrokerEpochRecord,
	suspends <-chan struct{},
) {
	select {
	case <-suspends:
		s.retire(ready)
	default:
	}
	s.rotate(record, ready)
}

// retire ends the live epoch and discards the barrier it was opened from, so
// the next Open waits for a real replacement instead of re-serving authority
// the runtime has already closed.
func (s *codexBrokerObserverSession) retire(ready chan codexBrokerEpochRecord) {
	s.mu.Lock()
	current := s.current
	s.current = nil
	s.pending, s.hasPending = codexBrokerEpochRecord{}, false
	s.mu.Unlock()
	select {
	case <-ready:
	default:
	}
	if current != nil {
		current.end(codexObserverReasonEndpointSuspended)
	}
}

// rotate ends the live epoch and offers its replacement.
func (s *codexBrokerObserverSession) rotate(record codexBrokerEpochRecord, ready chan codexBrokerEpochRecord) {
	s.mu.Lock()
	current := s.current
	s.current = nil
	s.pending, s.hasPending = record, true
	s.mu.Unlock()
	if current != nil {
		current.end(codexObserverReasonEpochRotated)
	}
	// The channel holds one record: only the newest barrier can be opened, and
	// an older one that was never consumed describes a connection epoch the
	// broker has already retired.
	select {
	case <-ready:
	default:
	}
	select {
	case ready <- record:
	default:
	}
}

// publish makes one closed barrier the live epoch.
func (s *codexBrokerObserverSession) publish(record codexBrokerEpochRecord) (*codexBrokerLifecycleEpoch, error) {
	s.mu.Lock()
	if s.closed || s.conn == nil || s.binding == nil {
		s.mu.Unlock()
		return nil, errors.New("codex broker binding session is closed")
	}
	runtimeID := ""
	runtimeID = s.conn.Runtime()
	epoch := &codexBrokerLifecycleEpoch{
		session:       s,
		fence:         record.fence,
		brokerRuntime: runtimeID,
		snapshot:      record.snapshot,
		notifications: make(chan codexappserver.Notification, codexBrokerObserverBacklog),
		leases:        map[string]codexbroker.ApprovalLease{},
	}
	epoch.binding = s.binding
	s.current = epoch
	s.mu.Unlock()
	return epoch, nil
}

// discard drops a revoked binding so the next Open establishes a fresh one.
func (s *codexBrokerObserverSession) discard() {
	s.mu.Lock()
	binding := s.binding
	s.binding, s.ready, s.pumped = nil, nil, nil
	s.pending, s.hasPending = codexBrokerEpochRecord{}, false
	s.mu.Unlock()
	if binding != nil {
		_ = binding.Close()
	}
}

// revocation renders the closed reason one binding stopped.
func (s *codexBrokerObserverSession) revocation(binding *codexbroker.RemoteBinding) error {
	reason := codexbroker.RefusalNone
	if binding != nil {
		reason = binding.Revocation()
	}
	if reason == codexbroker.RefusalNone {
		reason = codexbroker.RefusalBindingClosed
	}
	return errors.New("codex broker binding revoked: " + string(reason))
}

// codexBrokerLifecycleEpoch is one connection epoch of a broker binding,
// presented as the lifecycle connection and control wire the observer and the
// exact-Agent control epoch already speak.
//
// Its fence is captured when the barrier closed and is never refreshed. That
// is the whole point: a mutation issued after the broker has replaced the
// connection presents a retired connection epoch and is refused before a byte
// reaches the endpoint, so an old epoch's control, approval, and Registry
// writes are all zero without this layer tracking connection state itself.
type codexBrokerLifecycleEpoch struct {
	session       *codexBrokerObserverSession
	binding       *codexbroker.RemoteBinding
	fence         codexbroker.Fence
	brokerRuntime string
	snapshot      codexappserver.ThreadSnapshot
	notifications chan codexappserver.Notification

	mu     sync.Mutex
	ended  bool
	leases map[string]codexbroker.ApprovalLease
	// cause is why this epoch's notification stream closed, recorded by
	// whichever call ended it. The observer above cannot derive this: a closed
	// channel looks identical whether the upstream connection went away, the
	// broker rotated in a replacement barrier, the binding was revoked, or the
	// observer closed the connection itself. Without it the loop can only
	// report that the stream ended.
	cause codexObserverReason
}

// GenerationAuthority returns the complete live authority granted by the
// generation-keyed broker runtime. Broker runtime and local epochs come from
// the same authenticated snapshot barrier; an observer process never invents
// or substitutes any of these values.
func (e *codexBrokerLifecycleEpoch) GenerationAuthority() (coremetadata.CodexAuthorityRef, error) {
	if e == nil || e.session == nil || !e.session.endpoint.Valid() || e.brokerRuntime == "" ||
		e.fence.Connection == 0 || e.fence.Binding == 0 {
		return coremetadata.CodexAuthorityRef{}, errors.New("codex generation broker authority is unavailable")
	}
	want, err := codexbroker.NewEndpointKey(e.session.endpoint.StateDomainID, e.session.endpoint.EndpointGenerationID)
	if err != nil || e.session.discovery.Endpoint() != want {
		return coremetadata.CodexAuthorityRef{}, errors.New("codex generation broker route does not match the durable endpoint")
	}
	return coremetadata.CodexAuthorityRef{
		StateDomainID: e.session.endpoint.StateDomainID, EndpointGenerationID: e.session.endpoint.EndpointGenerationID,
		BrokerRuntimeID: e.brokerRuntime, ConnectionEpoch: uint64(e.fence.Connection), BindingEpoch: uint64(e.fence.Binding),
	}, nil
}

// Notifications is this epoch's ordered delivery stream. It closes when the
// barrier reopens or the binding is revoked, which is the exact disconnect
// signal the observer already reacts to.
func (e *codexBrokerLifecycleEpoch) Notifications() <-chan codexappserver.Notification {
	return e.notifications
}

// LifecycleEventsAvailable reports the capability decision the bind already
// proved.
//
// A binding exists only after the runtime attached through the Phase 0 attach
// seam, which refuses a protocol mismatch and a version skew before it dials,
// and only after the pre-turn bootstrap sent an `excludeTurns` thread/resume,
// which upstream answers only on a connection that negotiated the experimental
// API. Both are strictly stronger than the version comparison a directly owned
// client makes, and neither is observable from this side of the local IPC, so
// re-deriving a weaker answer here would only be able to disagree.
func (e *codexBrokerLifecycleEpoch) LifecycleEventsAvailable() bool { return true }

// ReadLifecycleSnapshot converges the exact thread over the fenced request
// path.
//
// The barrier's own snapshot is the pre-turn projection, which carries no turn
// identity, so the thread/turn state this epoch's control decisions rest on is
// read here through the same fence every mutation uses. Going through the
// fence rather than around it is what keeps a retired connection epoch from
// converging state it no longer owns.
func (e *codexBrokerLifecycleEpoch) ReadLifecycleSnapshot(ctx context.Context, threadID string) (codexappserver.LifecycleSnapshot, error) {
	return codexappserver.ReadLifecycleSnapshotOn(ctx, e, threadID)
}

// Close ends this epoch without releasing the binding. The binding is the
// thing that must survive a disconnect for the broker to keep reconnecting, so
// the observer closing one connection never retires it.
func (e *codexBrokerLifecycleEpoch) Close() error {
	e.session.clear(e)
	e.end(codexObserverReasonEpochClosed)
	return nil
}

// Request issues one fenced endpoint request for this epoch.
func (e *codexBrokerLifecycleEpoch) Request(ctx context.Context, method string, params, result any) error {
	if e.binding == nil {
		return errors.New("codex broker binding is unavailable")
	}
	if _, err := e.binding.Submit(ctx, e.fence, codexbroker.Mutation{Method: method, Params: params, Result: result}); err != nil {
		return err
	}
	return nil
}

// StartExactTurn starts one turn on the exact bound thread.
func (e *codexBrokerLifecycleEpoch) StartExactTurn(ctx context.Context, threadID, text string) (codexappserver.ControlResult, error) {
	return codexappserver.StartExactTurnOn(ctx, e, threadID, text)
}

// SteerExactTurn steers the exact in-progress turn of the bound thread.
func (e *codexBrokerLifecycleEpoch) SteerExactTurn(ctx context.Context, threadID, expectedTurnID, text string) (codexappserver.ControlResult, error) {
	return codexappserver.SteerExactTurnOn(ctx, e, threadID, expectedTurnID, text)
}

// InterruptExactTurn interrupts the exact in-progress turn of the bound thread.
func (e *codexBrokerLifecycleEpoch) InterruptExactTurn(ctx context.Context, threadID, turnID string) (codexappserver.ControlResult, error) {
	return codexappserver.InterruptExactTurnOn(ctx, e, threadID, turnID)
}

// RespondServerRequest answers exactly one inbound server request through the
// single-use lease the broker minted when it delivered that request.
//
// The raw id alone is not authority: it repeats across connections, so an
// answer is admitted only when this epoch is the one that received the request
// and the lease it minted still matches both current epochs.
func (e *codexBrokerLifecycleEpoch) RespondServerRequest(ctx context.Context, rawID json.RawMessage, result any) error {
	if e.binding == nil {
		return errors.New("codex broker binding is unavailable")
	}
	e.mu.Lock()
	lease, held := e.leases[string(rawID)]
	if held {
		delete(e.leases, string(rawID))
	}
	e.mu.Unlock()
	if !held {
		return errors.New("codex broker approval lease is not held for this request")
	}
	return e.binding.Answer(ctx, lease, result)
}

// deliver admits one live broker event as the notification shape the observer
// and the control epoch decode.
func (e *codexBrokerLifecycleEpoch) deliver(event codexbroker.Event) {
	notification := codexappserver.Notification{Method: event.Method, Params: event.Params}
	if len(event.Lease.RawRequestID) > 0 {
		requestID, err := codexappserver.NormalizeServerRequestID(event.Lease.RawRequestID)
		if err != nil {
			// An id this build cannot render is an id it cannot match a pending
			// request against, so no lease is minted and no response authority
			// exists. The event is still delivered so lifecycle state stays true.
			requestID = ""
		} else {
			e.mu.Lock()
			if !e.ended {
				e.leases[string(event.Lease.RawRequestID)] = event.Lease
			}
			e.mu.Unlock()
			notification.RawRequestID = append(json.RawMessage(nil), event.Lease.RawRequestID...)
		}
		notification.RequestID = requestID
	}
	e.mu.Lock()
	if e.ended {
		e.mu.Unlock()
		return
	}
	select {
	case e.notifications <- notification:
		e.mu.Unlock()
	default:
		e.mu.Unlock()
		// The consumer fell a full backlog behind. Ending the epoch hands it a
		// closed stream it must resync from. Retiring the exact binding forces
		// the next observer epoch through a fresh broker snapshot barrier while
		// leaving every sibling binding and the shared upstream connection live;
		// silently dropping would leave a hole nothing downstream could detect.
		e.end(codexObserverReasonBacklogOverflow)
		e.session.resync(e)
	}
}

// end closes this epoch exactly once and retires every lease it minted. The
// first caller to close it also names why; later calls cannot overwrite that.
func (e *codexBrokerLifecycleEpoch) end(cause codexObserverReason) {
	e.mu.Lock()
	if e.ended {
		e.mu.Unlock()
		return
	}
	e.ended = true
	e.cause = cause
	e.leases = map[string]codexbroker.ApprovalLease{}
	close(e.notifications)
	e.mu.Unlock()
}

// NotificationsClosedCause reports why this epoch's stream closed, or the
// empty reason while it is still open. It is the observer's only truthful
// answer to who ended the epoch.
func (e *codexBrokerLifecycleEpoch) NotificationsClosedCause() codexObserverReason {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.ended {
		return ""
	}
	return e.cause
}

// clear drops one epoch from the session when it is still the live one.
func (s *codexBrokerObserverSession) clear(epoch *codexBrokerLifecycleEpoch) {
	s.mu.Lock()
	if s.current == epoch {
		s.current = nil
	}
	s.mu.Unlock()
}

// resync retires only the binding whose app-side epoch overflowed. The runtime
// connection is deliberately retained: Open will bind this exact thread again
// and wait for its new snapshot barrier, while sibling bindings keep their
// authority and ordered streams unchanged.
func (s *codexBrokerObserverSession) resync(epoch *codexBrokerLifecycleEpoch) {
	s.mu.Lock()
	if s.current != epoch {
		s.mu.Unlock()
		return
	}
	binding := s.binding
	s.current, s.binding, s.ready = nil, nil, nil
	s.pending, s.hasPending = codexBrokerEpochRecord{}, false
	s.mu.Unlock()
	if binding != nil {
		_ = binding.Close()
	}
}
