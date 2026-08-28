package codexbroker

import (
	"context"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// bindingStage is where one binding sits relative to the current connection's
// snapshot barrier.
type bindingStage uint8

const (
	// stageIdle means no connection is serving this binding. It is the state
	// before the first connect and after every disconnect.
	stageIdle bindingStage = iota
	// stageBuffering means the barrier is open: live events are held so they
	// can be merged behind the snapshot, and control authority is closed.
	stageBuffering
	// stageOpen means the barrier closed: events flow and mutations may fence.
	stageOpen
)

// Binding is one thread's isolated view of the shared endpoint.
//
// Isolation is the point of the type. Each binding owns a bounded delivery
// queue, so a consumer that stops reading can only starve itself; when that
// queue overflows the binding is revoked with RefusalResyncRequired while the
// shared connection and every other binding keep running. Silently dropping
// the overflow instead would hand the consumer a gapped order it has no way to
// detect.
//
// Every field below the constructor inputs is guarded by Broker.mu.
type Binding struct {
	broker   *Broker
	threadID string
	cwd      string
	roots    []string
	epoch    BindingEpoch
	events   chan Event
	// suspends carries the out-of-band notice that this binding's control
	// authority closed at a disconnect while the binding itself survived. It
	// is separate from events on purpose: a consumer must be able to retire a
	// live epoch's authority promptly, and the ordered stream cannot say that
	// because the next thing it will carry is the barrier that already
	// resolved it.
	suspends chan struct{}

	stage    bindingStage
	conn     *connection
	buffered []codexappserver.Notification
	sequence uint64
	revoked  Refusal
}

// ThreadID returns the exact thread this binding was created for.
func (bd *Binding) ThreadID() string { return bd.threadID }

// Suspensions signals every disconnect this binding survived.
//
// One notice means the authority a consumer holds is no longer current and no
// replacement is open yet. It is coalesced rather than queued, because a
// consumer that has already retired its epoch has nothing further to learn
// from a second notice about the same closed authority.
func (bd *Binding) Suspensions() <-chan struct{} { return bd.suspends }

// Events is this binding's ordered delivery stream. Each connection epoch
// contributes exactly one EventOriginSnapshot event followed by that epoch's
// live events in one merged order. The channel is closed when the binding is
// revoked; Revocation says why.
func (bd *Binding) Events() <-chan Event { return bd.events }

// Revocation returns the closed reason this binding stopped, or RefusalNone
// while it is still live.
func (bd *Binding) Revocation() Refusal {
	bd.broker.mu.Lock()
	defer bd.broker.mu.Unlock()
	return bd.revoked
}

// ControlAuthority returns the fence a mutation may currently use.
//
// It refuses with RefusalControlNotOpen for a binding whose barrier has not
// closed on the current connection. Control authority opening only after the
// snapshot merge is what keeps a caller from writing against state it has not
// yet been told about.
func (bd *Binding) ControlAuthority() (Fence, error) {
	b := bd.broker
	b.mu.Lock()
	defer b.mu.Unlock()
	if bd.revoked != RefusalNone {
		return Fence{}, refuse(bd.revoked, nil)
	}
	if bd.stage != stageOpen || bd.conn == nil || b.conn != bd.conn {
		return Fence{}, refuse(RefusalControlNotOpen, nil)
	}
	return Fence{Connection: bd.conn.epoch, Binding: bd.epoch}, nil
}

// Close releases this binding. The delivery stream is closed with
// RefusalBindingClosed, and the supervisor stops reconnecting once no binding
// is left to serve.
func (bd *Binding) Close() error {
	b := bd.broker
	b.mu.Lock()
	b.revokeBindingLocked(bd, RefusalBindingClosed)
	b.mu.Unlock()
	b.signal()
	return nil
}

// Submit sends one control request under an explicit fence.
//
// The fence is mandatory and is checked against both current epochs before the
// request reaches the wire, so a caller holding a fence from a revoked
// connection or a superseded binding writes zero bytes. A request that
// terminates at a disconnect boundary returns MutationIndeterminate and is
// never resent: the ledger records exactly one attempt for it.
func (bd *Binding) Submit(ctx context.Context, fence Fence, mutation Mutation) (MutationOutcome, error) {
	b := bd.broker
	b.mu.Lock()
	conn, err := bd.authorityLocked(fence)
	b.mu.Unlock()
	if err != nil {
		// A refused mutation never reached the wire, so it is not a write and
		// does not enter the ledger.
		return MutationRefused, err
	}
	requestErr := conn.endpoint.Request(ctx, mutation.Method, mutation.Params, mutation.Result)
	outcome := b.classify(conn, requestErr)
	b.record(WriteRecord{Fence: fence, Method: mutation.Method, Outcome: outcome, Attempts: 1})
	switch outcome {
	case MutationApplied:
		return MutationApplied, nil
	case MutationIndeterminate:
		return MutationIndeterminate, refuse(RefusalDisconnectBoundary, requestErr)
	default:
		return MutationRefused, requestErr
	}
}

// Answer responds to exactly one inbound server request.
//
// The lease must match this binding's thread and both current epochs, and it
// may be spent once per connection. Connection replacement therefore revokes
// every outstanding response authority without any per-request bookkeeping:
// the new connection starts with an empty ledger and the old one is gone.
func (bd *Binding) Answer(ctx context.Context, lease ApprovalLease, result any) error {
	b := bd.broker
	b.mu.Lock()
	conn, err := bd.authorityLocked(lease.Fence)
	if err != nil {
		b.mu.Unlock()
		return err
	}
	if !lease.held() || lease.ThreadID != bd.threadID {
		b.mu.Unlock()
		return refuse(RefusalLeaseIdentityMismatch, nil)
	}
	key := string(lease.RawRequestID)
	if _, answered := conn.answered[key]; answered {
		b.mu.Unlock()
		return refuse(RefusalResponseAlreadyAnswered, nil)
	}
	conn.answered[key] = struct{}{}
	b.mu.Unlock()
	return conn.endpoint.RespondServerRequest(ctx, lease.RawRequestID, result)
}

// authorityLocked resolves the connection one fenced operation may use.
// Caller holds Broker.mu.
func (bd *Binding) authorityLocked(fence Fence) (*connection, error) {
	b := bd.broker
	if bd.revoked != RefusalNone {
		return nil, refuse(bd.revoked, nil)
	}
	if b.closing {
		return nil, refuse(RefusalBrokerClosed, nil)
	}
	if bd.stage != stageOpen || bd.conn == nil || b.conn != bd.conn {
		return nil, refuse(RefusalControlNotOpen, nil)
	}
	if fence.Binding != bd.epoch {
		return nil, refuse(RefusalStaleBindingEpoch, nil)
	}
	if fence.Connection != bd.conn.epoch {
		return nil, refuse(RefusalStaleConnectionEpoch, nil)
	}
	return bd.conn, nil
}

// acceptLocked takes one attributed notification. Caller holds Broker.mu.
func (bd *Binding) acceptLocked(b *Broker, conn *connection, notification codexappserver.Notification) {
	switch bd.stage {
	case stageBuffering:
		if len(bd.buffered) >= b.backlog {
			b.revokeBindingLocked(bd, RefusalResyncRequired)
			return
		}
		bd.buffered = append(bd.buffered, notification)
		b.diag.BufferedEvents++
	case stageOpen:
		bd.emitLocked(b, bd.liveEventLocked(conn, notification))
	default:
		// No connection owns this binding right now; the next barrier's
		// snapshot restates whatever this message would have reported.
	}
}

// closeBarrierLocked merges the snapshot and the buffered live events into one
// order and opens control authority. Caller holds Broker.mu, so no live event
// can interleave with the merge.
func (bd *Binding) closeBarrierLocked(b *Broker, conn *connection, snapshot codexappserver.ThreadSnapshot) {
	bd.conn = conn
	bd.stage = stageOpen
	fence := Fence{Connection: conn.epoch, Binding: bd.epoch}
	bd.emitLocked(b, Event{Fence: fence, Origin: EventOriginSnapshot, Snapshot: snapshot})
	buffered := bd.buffered
	bd.buffered = nil
	for _, notification := range buffered {
		if bd.revoked != RefusalNone {
			return
		}
		bd.emitLocked(b, bd.liveEventLocked(conn, notification))
	}
}

// emitLocked numbers and delivers one event, or revokes the binding when its
// bounded queue is full. Caller holds Broker.mu.
func (bd *Binding) emitLocked(b *Broker, event Event) {
	if bd.revoked != RefusalNone {
		return
	}
	bd.sequence++
	event.Sequence = bd.sequence
	select {
	case bd.events <- event:
		b.diag.DeliveredEvents++
	default:
		b.revokeBindingLocked(bd, RefusalResyncRequired)
	}
}

// liveEventLocked projects one notification onto this binding's fence. An
// inbound server request additionally mints the single-use approval lease that
// answering it requires. Caller holds Broker.mu.
func (bd *Binding) liveEventLocked(conn *connection, notification codexappserver.Notification) Event {
	fence := Fence{Connection: conn.epoch, Binding: bd.epoch}
	event := Event{
		Fence:  fence,
		Origin: EventOriginLive,
		Method: notification.Method,
		Params: notification.Params,
	}
	if len(notification.RawRequestID) > 0 {
		event.Lease = ApprovalLease{Fence: fence, ThreadID: bd.threadID, RawRequestID: notification.RawRequestID}
	}
	return event
}

// suspendLocked returns the binding to stageIdle at a disconnect. Buffered
// events are discarded because the next epoch's snapshot restates them, and
// keeping them would splice two epochs into one order. Caller holds Broker.mu.
func (bd *Binding) suspendLocked() {
	if bd.revoked != RefusalNone {
		return
	}
	bd.stage = stageIdle
	bd.conn = nil
	bd.buffered = nil
	bd.notifySuspendedLocked()
}

// notifySuspendedLocked coalesces one authority-closed notice. Caller holds
// Broker.mu.
func (bd *Binding) notifySuspendedLocked() {
	if bd.suspends == nil {
		return
	}
	select {
	case bd.suspends <- struct{}{}:
	default:
	}
}

// revokeLocked terminates the binding. Caller holds Broker.mu.
func (bd *Binding) revokeLocked(reason Refusal) {
	bd.revoked = reason
	bd.stage = stageIdle
	bd.conn = nil
	bd.buffered = nil
	close(bd.events)
}
