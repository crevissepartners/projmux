package codexbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"
)

// session is one authenticated client connection to a runtime host.
//
// One session may hold several bindings. Each binding keeps its own bounded
// delivery queue inside the broker, so the shape that isolates a slow consumer
// in-process is the same shape that isolates one across the socket: the
// session's writer blocks on a full outbound queue, which back-pressures into
// the binding's queue, which revokes that binding alone.
type session struct {
	host    *Host
	conn    *net.UnixConn
	version int

	out    chan wireReply
	closed chan struct{}
	once   sync.Once

	mu       sync.Mutex
	bindings map[string]*Binding
	pumps    sync.WaitGroup
	handlers sync.WaitGroup
}

// serveSession runs one connection from handshake to close.
func (h *Host) serveSession(conn *net.UnixConn) {
	defer conn.Close()
	if !h.track(conn) {
		return
	}
	defer h.untrack(conn)
	reader := bufio.NewReaderSize(conn, frameBufferBytes)
	version, ok := h.authenticate(conn, reader)
	if !ok {
		return
	}
	h.countSession(1)
	defer h.countSession(-1)
	s := &session{
		host:     h,
		conn:     conn,
		version:  version,
		out:      make(chan wireReply, sessionBacklog),
		closed:   make(chan struct{}),
		bindings: make(map[string]*Binding),
	}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		s.write()
	}()
	s.read(reader)
	// The client is gone or the runtime is stopping. Release every binding this
	// session held before waiting on anything: a binding whose owner has
	// disconnected has no consumer, and leaving it bound would keep the runtime
	// alive for a client that no longer exists.
	s.releaseAll()
	s.handlers.Wait()
	s.pumps.Wait()
	s.stop()
	<-writerDone
}

// stop closes the outbound queue exactly once.
func (s *session) stop() {
	s.once.Do(func() { close(s.closed) })
}

// write serializes every outbound frame onto the socket.
func (s *session) write() {
	for {
		select {
		case reply := <-s.out:
			_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if writeFrame(s.conn, reply) != nil {
				s.stop()
				// A socket that cannot be written is a client that cannot be
				// told anything. Unblock the reader by closing the connection
				// rather than waiting for it to notice on its own.
				_ = s.conn.Close()
				return
			}
		case <-s.closed:
			return
		case <-s.host.done:
			return
		}
	}
}

// send queues one outbound frame, blocking while the queue is full so the
// pressure reaches the binding that produced it.
func (s *session) send(reply wireReply) {
	select {
	case s.out <- reply:
	case <-s.closed:
	case <-s.host.done:
	}
}

// read decodes client requests until the connection ends.
func (s *session) read(reader *bufio.Reader) {
	for {
		frame, err := readFrame(reader)
		if err != nil {
			return
		}
		var request wireRequest
		if json.Unmarshal(frame, &request) != nil {
			s.send(wireReply{Kind: replyRefused, Refusal: RefusalFrameInvalid})
			return
		}
		if request.Runtime != s.host.runtimeID {
			// Authority minted by another runtime. It is refused rather than
			// interpreted, because the epochs inside it restart at one in every
			// process and would otherwise collide with this runtime's own.
			s.reply(request.ID, wireReply{Kind: replyRefused, Refusal: RefusalRuntimeReplaced})
			continue
		}
		s.handlers.Go(func() {
			s.handle(request)
		})
	}
}

// reply answers one request.
func (s *session) reply(id uint64, reply wireReply) {
	reply.ID = id
	s.send(reply)
}

// refuse answers one request with a typed refusal.
func (s *session) refuse(id uint64, reason Refusal) {
	s.host.countRefusal()
	s.reply(id, wireReply{Kind: replyRefused, Refusal: reason})
}

// handle dispatches one client request.
func (s *session) handle(request wireRequest) {
	switch request.Kind {
	case requestBind:
		s.handleBind(request)
	case requestUnbind:
		s.handleUnbind(request)
	case requestSubmit:
		s.handleSubmit(request)
	case requestAnswer:
		s.handleAnswer(request)
	case requestStats:
		s.handleStats(request)
	default:
		s.refuse(request.ID, RefusalRequestUnknown)
	}
}

// handleBind opens one exact-thread binding on the shared connection.
func (s *session) handleBind(request wireRequest) {
	if reason := s.host.refusingWork(); reason != RefusalNone {
		s.refuse(request.ID, reason)
		return
	}
	binding, err := s.host.broker.Bind(request.Thread, request.CWD, request.Roots)
	if err != nil {
		s.refuse(request.ID, RefusalOf(err))
		return
	}
	s.mu.Lock()
	if _, exists := s.bindings[binding.ThreadID()]; exists {
		s.mu.Unlock()
		_ = binding.Close()
		s.refuse(request.ID, RefusalBindingExists)
		return
	}
	s.bindings[binding.ThreadID()] = binding
	s.mu.Unlock()
	s.host.holdBinding()
	s.pumps.Go(func() {
		s.pump(binding)
	})
	s.reply(request.ID, wireReply{Kind: replyResult, Thread: binding.ThreadID()})
}

// handleStats answers this runtime's content-free telemetry.
//
// It is deliberately outside the refusingWork gate that guards bind and
// submit: a draining or shutting-down runtime is exactly the state an operator
// most needs described, and describing it neither opens a binding nor writes
// upstream.
func (s *session) handleStats(request wireRequest) {
	telemetry := RuntimeTelemetry{
		Runtime:  s.host.RuntimeID(),
		Protocol: s.version,
		Host:     s.host.Stats(),
		Broker:   s.host.broker.Diagnostics(),
	}
	payload, err := json.Marshal(telemetry)
	if err != nil {
		s.refuse(request.ID, RefusalRequestUnknown)
		return
	}
	s.reply(request.ID, wireReply{Kind: replyResult, Result: payload})
}

// handleUnbind releases one binding this session holds.
func (s *session) handleUnbind(request wireRequest) {
	binding := s.take(request.Thread)
	if binding == nil {
		s.refuse(request.ID, RefusalBindingClosed)
		return
	}
	_ = binding.Close()
	s.reply(request.ID, wireReply{Kind: replyResult, Thread: request.Thread})
}

// handleSubmit forwards one fenced control request.
func (s *session) handleSubmit(request wireRequest) {
	binding := s.lookup(request.Thread)
	if binding == nil {
		s.refuse(request.ID, RefusalBindingClosed)
		return
	}
	var result json.RawMessage
	outcome, err := binding.Submit(context.Background(), request.Fence, Mutation{
		Method: request.Method,
		Params: request.Params,
		Result: &result,
	})
	// Every terminated mutation answers as a result frame carrying its closed
	// outcome. An endpoint error is reported as its classification alone,
	// because the error body is provider content that stops here.
	reason := RefusalOf(err)
	if err != nil && reason == RefusalNone {
		reason = RefusalEndpointRefused
	}
	if reason != RefusalNone {
		s.host.countRefusal()
	}
	s.reply(request.ID, wireReply{
		Kind:    replyResult,
		Thread:  request.Thread,
		Outcome: outcome,
		Result:  result,
		Refusal: reason,
	})
}

// handleAnswer forwards one fenced approval response.
func (s *session) handleAnswer(request wireRequest) {
	binding := s.lookup(request.Thread)
	if binding == nil {
		s.refuse(request.ID, RefusalBindingClosed)
		return
	}
	lease := ApprovalLease{Fence: request.Fence, ThreadID: request.Thread, RawRequestID: request.RawRequestID}
	if err := binding.Answer(context.Background(), lease, request.Params); err != nil {
		s.refuse(request.ID, RefusalOf(err))
		return
	}
	s.reply(request.ID, wireReply{Kind: replyResult, Thread: request.Thread})
}

// pump forwards one binding's ordered stream and reports its revocation.
func (s *session) pump(binding *Binding) {
	thread := binding.ThreadID()
	events := binding.Events()
	suspends := binding.Suspensions()
	for events != nil {
		select {
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			s.forwardEvent(thread, event, suspends)
		case <-suspends:
			s.send(wireReply{Kind: replySuspended, Thread: thread})
		}
	}
	s.send(wireReply{Kind: replyRevoked, Thread: thread, Refusal: binding.Revocation()})
	s.forget(thread, binding)
	s.host.releaseBinding()
}

// forwardEvent preserves the binding's causal order across the local IPC.
// Suspensions and events are separate in-process channels, but the wire is the
// binding's single ordered stream. A replacement snapshot is created only
// after the preceding connection was suspended, so drain that coalesced edge
// before forwarding the snapshot. Otherwise select may put the new authority
// on the wire first and a late suspension would immediately retire it.
func (s *session) forwardEvent(thread string, event Event, suspends <-chan struct{}) {
	if event.Origin == EventOriginSnapshot {
		select {
		case <-suspends:
			s.send(wireReply{Kind: replySuspended, Thread: thread})
		default:
		}
	}
	s.send(wireReply{Kind: replyEvent, Thread: thread, Event: wireEventOf(event)})
}

// wireEventOf projects one broker event onto the wire.
func wireEventOf(event Event) *wireEvent {
	projected := &wireEvent{
		Fence:        event.Fence,
		Origin:       event.Origin,
		Sequence:     event.Sequence,
		Method:       event.Method,
		Params:       event.Params,
		RawRequestID: event.Lease.RawRequestID,
	}
	if event.Origin == EventOriginSnapshot {
		snapshot := event.Snapshot
		projected.Snapshot = &snapshot
	}
	return projected
}

func (s *session) lookup(thread string) *Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bindings[thread]
}

func (s *session) take(thread string) *Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.bindings[thread]
	delete(s.bindings, thread)
	return binding
}

// forget drops the routing entry for a binding that ended on its own.
func (s *session) forget(thread string, binding *Binding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings[thread] == binding {
		delete(s.bindings, thread)
	}
}

// releaseAll closes every binding this session still holds.
func (s *session) releaseAll() {
	s.mu.Lock()
	held := make([]*Binding, 0, len(s.bindings))
	for _, binding := range s.bindings {
		held = append(held, binding)
	}
	s.bindings = make(map[string]*Binding)
	s.mu.Unlock()
	for _, binding := range held {
		_ = binding.Close()
	}
}
