package codexbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	// defaultDialTimeout bounds one discovery dial and its handshake.
	defaultDialTimeout = 2 * time.Second
	// defaultStartupTimeout bounds waiting for a launched runtime to publish
	// its socket, and also bounds waiting for the startup lock.
	defaultStartupTimeout = 10 * time.Second
	// startupPollInterval paces the dial retries while a runtime starts.
	startupPollInterval = 25 * time.Millisecond
	// lockPollInterval paces the startup lock retries.
	lockPollInterval = 10 * time.Millisecond
	// remoteBacklog bounds one remote binding's undelivered events. It matches
	// the runtime-side policy: an overflow revokes that binding alone rather
	// than handing its consumer a stream with an undetectable hole.
	remoteBacklog = 64
)

// DialConfig is the closed input for reaching an already-running runtime.
type DialConfig struct {
	// Protocol is the local IPC version window this client speaks. Zero means
	// the window this build speaks.
	Protocol ProtocolRange
	// Timeout bounds the dial and the handshake. Zero means defaultDialTimeout.
	Timeout time.Duration
}

// Launcher starts one broker runtime process. It returns once the process has
// been started, not once it is ready; Ensure owns the readiness wait, so a
// launcher never has to guess how long publication takes.
type Launcher func(context.Context) error

// EnsureConfig is the closed input for reaching a runtime, starting one first
// if and only if none is there.
type EnsureConfig struct {
	// Protocol is the local IPC version window this client speaks.
	Protocol ProtocolRange
	// Timeout bounds each dial and handshake.
	Timeout time.Duration
	// Launch starts a runtime when discovery finds none. A nil launcher makes
	// Ensure a strict discovery: it refuses rather than starting anything.
	Launch Launcher
	// StartupTimeout bounds the startup lock wait and the readiness wait.
	StartupTimeout time.Duration
}

func (c EnsureConfig) startupTimeout() time.Duration {
	if c.StartupTimeout <= 0 {
		return defaultStartupTimeout
	}
	return c.StartupTimeout
}

// Conn is one authenticated client session with a broker runtime.
//
// It owns the runtime identity it was welcomed by. Every request carries that
// identity, and every binding it hands out dies with the connection, so
// authority granted by one runtime can never be presented to its replacement.
type Conn struct {
	conn      net.Conn
	discovery Discovery
	runtime   string
	version   int
	lifecycle bool
	session   string

	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   uint64
	pending  map[uint64]chan wireReply
	bindings map[string]*RemoteBinding
	reason   Refusal

	done chan struct{}
	once sync.Once
}

// Dial reaches the runtime published on one discovery contract.
//
// It refuses before writing anything when the published artifacts are not
// owner-private objects of the expected kind, so a client never hands its
// credential to a socket it cannot prove belongs to this user.
func Dial(ctx context.Context, discovery Discovery, cfg DialConfig) (*Conn, error) {
	return dial(ctx, discovery, cfg, "")
}

func dialLifecycleIPC(ctx context.Context, discovery Discovery, protocol ProtocolRange) (*Conn, error) {
	timeout := ownedLifecycleLimit
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return nil, refuse(RefusalDisconnectBoundary, ctx.Err())
	}
	return dial(ctx, discovery, DialConfig{Protocol: protocol, Timeout: timeout}, lifecycleSessionPurpose)
}

func dial(ctx context.Context, discovery Discovery, cfg DialConfig, purpose string) (*Conn, error) {
	if !platformSupported {
		return nil, refuse(RefusalUnsupportedPlatform, nil)
	}
	record, err := readRecord(discovery)
	if err != nil {
		return nil, err
	}
	path := discovery.SocketPath()
	info, err := os.Lstat(path)
	if err != nil {
		return nil, refuse(RefusalHostUnavailable, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || !ownedByCurrentUser(info) {
		return nil, refuse(RefusalDiscoveryUntrusted, nil)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	netConn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, refuse(RefusalHostUnavailable, err)
	}
	protocol := cfg.Protocol.normalize()
	conn, err := handshake(netConn, discovery, record, protocol, timeout, purpose)
	if err != nil {
		_ = netConn.Close()
		return nil, err
	}
	conn.discovery = discovery
	go conn.read()
	return conn, nil
}

// handshake sends the greeting and consumes the runtime's answer.
func handshake(netConn net.Conn, discovery Discovery, record discoveryRecord,
	protocol ProtocolRange, timeout time.Duration, purpose string) (*Conn, error) {
	_ = netConn.SetDeadline(time.Now().Add(timeout))
	greeting := hello{
		Preferred:  protocol.Preferred,
		Minimum:    protocol.Minimum,
		Endpoint:   discovery.endpoint,
		Credential: record.Credential,
		Purpose:    purpose,
	}
	if err := writeFrame(netConn, greeting); err != nil {
		return nil, refuse(RefusalHostUnavailable, err)
	}
	frame, err := readFrame(bufio.NewReaderSize(netConn, frameBufferBytes))
	if err != nil {
		return nil, refuse(RefusalHostUnavailable, err)
	}
	var reply wireReply
	if json.Unmarshal(frame, &reply) != nil {
		return nil, refuse(RefusalFrameInvalid, nil)
	}
	if reply.Kind == replyRefused {
		return nil, refuse(reply.Refusal, nil)
	}
	if reply.Kind != replyWelcome || reply.Runtime == "" {
		return nil, refuse(RefusalFrameInvalid, nil)
	}
	if reply.Protocol < protocol.Minimum || reply.Protocol > protocol.Preferred {
		return nil, refuse(RefusalProtocolIncompatible, nil)
	}
	_ = netConn.SetDeadline(time.Time{})
	return &Conn{
		conn:      netConn,
		runtime:   reply.Runtime,
		version:   reply.Protocol,
		lifecycle: slices.Contains(reply.Capabilities, lifecycleCapability),
		session:   reply.Session,
		pending:   make(map[uint64]chan wireReply),
		bindings:  make(map[string]*RemoteBinding),
		reason:    RefusalNone,
		done:      make(chan struct{}),
	}, nil
}

// Ensure reaches the runtime for one discovery contract, starting one when and
// only when discovery proves none is there.
//
// The whole start sequence runs under an exclusive startup lock, so a stale
// artifact cannot be reclaimed out from under a runtime another starter has
// just bound. An incompatible live runtime is never replaced here: the typed
// drain-required refusal is returned as-is and the caller retries once the
// running work has drained.
func Ensure(ctx context.Context, discovery Discovery, cfg EnsureConfig) (*Conn, error) {
	if !platformSupported {
		return nil, refuse(RefusalUnsupportedPlatform, nil)
	}
	dial := DialConfig{Protocol: cfg.Protocol, Timeout: cfg.Timeout}
	conn, err := Dial(ctx, discovery, dial)
	if err == nil {
		return conn, nil
	}
	if RefusalOf(err) != RefusalHostUnavailable {
		return nil, err
	}
	var started *Conn
	lockErr := withStartupLock(discovery, cfg.startupTimeout(), func() error {
		if again, dialErr := Dial(ctx, discovery, dial); dialErr == nil {
			started = again
			return nil
		}
		if reclaimErr := reclaimStale(discovery); reclaimErr != nil {
			if RefusalOf(reclaimErr) != RefusalHostLive {
				return reclaimErr
			}
			again, dialErr := Dial(ctx, discovery, dial)
			if dialErr != nil {
				return dialErr
			}
			started = again
			return nil
		}
		if cfg.Launch == nil {
			return refuse(RefusalHostUnavailable, nil)
		}
		if launchErr := cfg.Launch(ctx); launchErr != nil {
			return refuse(RefusalHostUnavailable, launchErr)
		}
		deadline := time.Now().Add(cfg.startupTimeout())
		for {
			again, dialErr := Dial(ctx, discovery, dial)
			if dialErr == nil {
				started = again
				return nil
			}
			if RefusalOf(dialErr) != RefusalHostUnavailable {
				return dialErr
			}
			if time.Now().After(deadline) {
				return refuse(RefusalHostUnavailable, nil)
			}
			select {
			case <-time.After(startupPollInterval):
			case <-ctx.Done():
				return refuse(RefusalHostUnavailable, ctx.Err())
			}
		}
	})
	if lockErr != nil {
		return nil, lockErr
	}
	return started, nil
}

// withStartupLock serializes reclaim and launch for one discovery contract.
func withStartupLock(discovery Discovery, timeout time.Duration, body func() error) error {
	if err := prepareDiscoveryDir(discovery); err != nil {
		return err
	}
	file, err := os.OpenFile(discovery.lockPath(), os.O_CREATE|os.O_RDWR, discoveryFileMode)
	if err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		locked, lockErr := tryLockExclusive(file)
		if lockErr != nil {
			return refuse(RefusalDiscoveryUntrusted, lockErr)
		}
		if locked {
			break
		}
		if time.Now().After(deadline) {
			return refuse(RefusalHostUnavailable, nil)
		}
		time.Sleep(lockPollInterval)
	}
	defer func() { _ = unlockFile(file) }()
	return body()
}

// Runtime returns the identity of the runtime this connection was welcomed by.
func (c *Conn) Runtime() string { return c.runtime }

// Protocol returns the negotiated local IPC version.
func (c *Conn) Protocol() int { return c.version }

// Done is closed when the connection has ended, for any reason.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Revocation returns the closed reason this connection ended, or RefusalNone
// while it is still live.
func (c *Conn) Revocation() Refusal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// Close ends the session and revokes every binding it holds.
func (c *Conn) Close() error {
	c.fail(RefusalBindingClosed)
	return nil
}

// Bind opens one exact-thread binding on the shared runtime connection.
func (c *Conn) Bind(ctx context.Context, threadID, cwd string, roots []string) (*RemoteBinding, error) {
	binding := &RemoteBinding{
		conn:     c,
		thread:   threadID,
		events:   make(chan Event, remoteBacklog),
		suspends: make(chan struct{}, 1),
		revoked:  RefusalNone,
	}
	c.mu.Lock()
	if c.reason != RefusalNone {
		reason := c.reason
		c.mu.Unlock()
		return nil, refuse(reason, nil)
	}
	if _, exists := c.bindings[threadID]; exists {
		c.mu.Unlock()
		return nil, refuse(RefusalBindingExists, nil)
	}
	c.bindings[threadID] = binding
	c.mu.Unlock()
	reply, err := c.call(ctx, wireRequest{Kind: requestBind, Thread: threadID, CWD: cwd, Roots: roots})
	if err != nil {
		c.detach(threadID, binding)
		return nil, err
	}
	if reply.Kind == replyRefused {
		c.detach(threadID, binding)
		return nil, refuse(reply.Refusal, nil)
	}
	return binding, nil
}

// Stats reads the runtime's content-free telemetry over this connection.
//
// A runtime that predates the stats frame answers `request-unknown`, which is
// returned as a typed refusal rather than an error string: a diagnostics
// reader renders that as an unsupported runtime, not as a broken one.
func (c *Conn) Stats(ctx context.Context) (RuntimeTelemetry, error) {
	reply, err := c.call(ctx, wireRequest{Kind: requestStats})
	if err != nil {
		return RuntimeTelemetry{}, err
	}
	if reply.Kind == replyRefused {
		return RuntimeTelemetry{}, refuse(reply.Refusal, nil)
	}
	var telemetry RuntimeTelemetry
	if err := json.Unmarshal(reply.Result, &telemetry); err != nil {
		return RuntimeTelemetry{}, refuse(RefusalRequestUnknown, err)
	}
	return telemetry, nil
}

// call sends one request and waits for the frame that answers it.
func (c *Conn) call(ctx context.Context, request wireRequest) (wireReply, error) {
	inbox := make(chan wireReply, 1)
	c.mu.Lock()
	if c.reason != RefusalNone {
		reason := c.reason
		c.mu.Unlock()
		return wireReply{}, refuse(reason, nil)
	}
	c.nextID++
	request.ID = c.nextID
	request.Runtime = c.runtime
	c.pending[request.ID] = inbox
	c.mu.Unlock()
	if err := c.write(request); err != nil {
		c.mu.Lock()
		delete(c.pending, request.ID)
		c.mu.Unlock()
		return wireReply{}, err
	}
	select {
	case reply := <-inbox:
		return reply, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, request.ID)
		c.mu.Unlock()
		return wireReply{}, refuse(RefusalDisconnectBoundary, ctx.Err())
	case <-c.done:
		reason := c.Revocation()
		if reason == RefusalNone {
			reason = RefusalHostUnavailable
		}
		return wireReply{}, refuse(reason, nil)
	}
}

// callCancelable is the lifecycle-only request path. Its cancel frame is
// correlated to the exact in-flight request id; normal submit/answer keeps its
// existing indeterminate cancellation semantics.
func (c *Conn) callCancelable(ctx context.Context, request wireRequest) (wireReply, error) {
	inbox := make(chan wireReply, 1)
	c.mu.Lock()
	if c.reason != RefusalNone {
		reason := c.reason
		c.mu.Unlock()
		return wireReply{}, refuse(reason, nil)
	}
	c.nextID++
	request.ID = c.nextID
	request.Runtime = c.runtime
	c.pending[request.ID] = inbox
	c.mu.Unlock()
	if err := c.writeLifecycle(ctx, request); err != nil {
		c.mu.Lock()
		delete(c.pending, request.ID)
		c.mu.Unlock()
		return wireReply{}, err
	}
	select {
	case reply := <-inbox:
		return reply, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, request.ID)
		c.nextID++
		cancelRequest := wireRequest{ID: c.nextID, Runtime: c.runtime, Kind: requestCancel, CancelID: request.ID}
		c.mu.Unlock()
		c.writeCancel(cancelRequest)
		// This connection is lifecycle-only. Closing it is the bounded fallback
		// when the correlated cancel frame cannot get onto a blocked socket; it
		// cannot revoke or delay any sibling binding on the shared session.
		c.fail(RefusalBindingClosed)
		return wireReply{}, refuse(RefusalDisconnectBoundary, ctx.Err())
	case <-c.done:
		reason := c.Revocation()
		if reason == RefusalNone {
			reason = RefusalHostUnavailable
		}
		return wireReply{}, refuse(reason, nil)
	}
}

// writeLifecycle is the queue-free write boundary of a request-owned IPC
// connection. Its deadline is the caller's operation deadline, and failure
// affects only this disposable connection.
func (c *Conn) writeLifecycle(ctx context.Context, request wireRequest) error {
	if err := ctx.Err(); err != nil {
		return refuse(RefusalDisconnectBoundary, err)
	}
	if !c.writeMu.TryLock() {
		return refuse(RefusalLifecycleBusy, nil)
	}
	defer c.writeMu.Unlock()
	deadline := time.Now().Add(ownedLifecycleLimit)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	_ = c.conn.SetWriteDeadline(deadline)
	if err := writeFrame(c.conn, request); err != nil {
		return refuse(RefusalHostUnavailable, err)
	}
	_ = c.conn.SetWriteDeadline(time.Time{})
	return nil
}

// writeCancel is deliberately queue-free. A lifecycle caller whose deadline
// has fired must not wait behind a blocked IPC write. Whether this write
// succeeds or is skipped, callCancelable closes this disposable sidecar; that
// departure cancels the host operation without touching the shared session.
func (c *Conn) writeCancel(request wireRequest) {
	if !c.writeMu.TryLock() {
		return
	}
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(ownedLifecycleCleanup))
	_ = writeFrame(c.conn, request)
	_ = c.conn.SetWriteDeadline(time.Time{})
}

// write serializes one outbound frame.
func (c *Conn) write(request wireRequest) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := writeFrame(c.conn, request); err != nil {
		return refuse(RefusalHostUnavailable, err)
	}
	return nil
}

// read consumes runtime frames until the connection ends.
func (c *Conn) read() {
	reader := bufio.NewReaderSize(c.conn, frameBufferBytes)
	for {
		frame, err := readFrame(reader)
		if err != nil {
			break
		}
		var reply wireReply
		if json.Unmarshal(frame, &reply) != nil {
			break
		}
		c.dispatch(reply)
	}
	// The runtime is gone. Revoking here, before any caller can present the
	// authority it held, is what makes a crashed runtime fail closed instead
	// of letting a fence outlive the process that granted it.
	c.fail(RefusalHostUnavailable)
}

// dispatch routes one runtime frame to its waiter or its binding.
func (c *Conn) dispatch(reply wireReply) {
	if reply.ID != 0 {
		c.mu.Lock()
		inbox := c.pending[reply.ID]
		delete(c.pending, reply.ID)
		c.mu.Unlock()
		if inbox != nil {
			inbox <- reply
		}
		return
	}
	c.mu.Lock()
	binding := c.bindings[reply.Thread]
	c.mu.Unlock()
	if binding == nil {
		return
	}
	switch reply.Kind {
	case replyEvent:
		binding.deliver(reply.Event)
	case replySuspended:
		binding.suspend()
	case replyRevoked:
		reason := reply.Refusal
		if reason == RefusalNone {
			reason = RefusalBindingClosed
		}
		c.detach(reply.Thread, binding)
		binding.revoke(reason)
	}
}

// detach removes one binding from routing.
func (c *Conn) detach(thread string, binding *RemoteBinding) {
	c.mu.Lock()
	if c.bindings[thread] == binding {
		delete(c.bindings, thread)
	}
	c.mu.Unlock()
}

// fail ends the connection and revokes everything it granted.
func (c *Conn) fail(reason Refusal) {
	c.once.Do(func() {
		c.mu.Lock()
		c.reason = reason
		bindings := make([]*RemoteBinding, 0, len(c.bindings))
		for _, binding := range c.bindings {
			bindings = append(bindings, binding)
		}
		c.bindings = make(map[string]*RemoteBinding)
		pending := c.pending
		c.pending = make(map[uint64]chan wireReply)
		c.mu.Unlock()
		close(c.done)
		_ = c.conn.Close()
		for _, inbox := range pending {
			inbox <- wireReply{Kind: replyRefused, Refusal: reason}
		}
		for _, binding := range bindings {
			binding.revoke(reason)
		}
	})
}

// RemoteBinding is one thread's isolated view of a runtime-hosted endpoint.
//
// It mirrors the in-process Binding exactly: a bounded ordered stream, a fence
// that opens only behind the snapshot barrier, a mutation path that refuses a
// stale fence before it writes, and a single-use approval lease.
type RemoteBinding struct {
	conn     *Conn
	thread   string
	events   chan Event
	suspends chan struct{}

	mu      sync.Mutex
	fence   Fence
	open    bool
	revoked Refusal
	once    sync.Once
}

// ThreadID returns the exact thread this binding was created for.
func (b *RemoteBinding) ThreadID() string { return b.thread }

// Events is this binding's ordered delivery stream. It is closed when the
// binding is revoked; Revocation says why.
func (b *RemoteBinding) Events() <-chan Event { return b.events }

// Suspensions mirrors the in-process signal: one coalesced notice per
// disconnect this binding survived, so a consumer retires the authority it
// holds instead of waiting for a barrier that may be a long outage away.
func (b *RemoteBinding) Suspensions() <-chan struct{} { return b.suspends }

// Revocation returns the closed reason this binding stopped, or RefusalNone
// while it is still live.
func (b *RemoteBinding) Revocation() Refusal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.revoked
}

// ControlAuthority returns the fence a mutation may currently use.
func (b *RemoteBinding) ControlAuthority() (Fence, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.revoked != RefusalNone {
		return Fence{}, refuse(b.revoked, nil)
	}
	if !b.open {
		return Fence{}, refuse(RefusalControlNotOpen, nil)
	}
	return b.fence, nil
}

// Close releases this binding.
func (b *RemoteBinding) Close() error {
	if _, err := b.conn.call(context.Background(), wireRequest{Kind: requestUnbind, Thread: b.thread}); err != nil {
		b.conn.detach(b.thread, b)
		b.revoke(RefusalBindingClosed)
		return nil
	}
	b.conn.detach(b.thread, b)
	b.revoke(RefusalBindingClosed)
	return nil
}

// Submit sends one control request under an explicit fence.
func (b *RemoteBinding) Submit(ctx context.Context, fence Fence, mutation Mutation) (MutationOutcome, error) {
	if err := b.authority(fence); err != nil {
		return MutationRefused, err
	}
	params, err := json.Marshal(mutation.Params)
	if err != nil {
		return MutationRefused, refuse(RefusalFrameInvalid, err)
	}
	reply, err := b.conn.call(ctx, wireRequest{
		Kind: requestSubmit, Thread: b.thread, Fence: fence,
		Method: mutation.Method, Params: params,
	})
	if err != nil {
		// The request left this process and its result was lost with the
		// connection. It is unknown, and it is never resent.
		return MutationIndeterminate, err
	}
	if reply.Kind == replyRefused {
		return MutationRefused, refuse(reply.Refusal, nil)
	}
	if mutation.Result != nil && len(reply.Result) > 0 {
		if err := json.Unmarshal(reply.Result, mutation.Result); err != nil {
			return reply.Outcome, refuse(RefusalFrameInvalid, err)
		}
	}
	switch reply.Outcome {
	case MutationApplied:
		return MutationApplied, nil
	case MutationIndeterminate:
		// The runtime already named why the answer was lost. Collapsing every
		// indeterminate outcome onto one reason here would throw that away at
		// the process boundary.
		reason := reply.Refusal
		if reason == RefusalNone {
			reason = RefusalDisconnectBoundary
		}
		return MutationIndeterminate, refuse(reason, nil)
	default:
		reason := reply.Refusal
		if reason == RefusalNone {
			reason = RefusalEndpointRefused
		}
		return MutationRefused, refuse(reason, nil)
	}
}

// ReadLifecycleSnapshot uses the additive body-free IPC operation. A host
// without the explicit capability is refused locally; the client never falls
// back to submit/thread-read and therefore never carries turn bodies over IPC.
func (b *RemoteBinding) ReadLifecycleSnapshot(ctx context.Context, fence Fence) (codexappserver.LifecycleSnapshot, error) {
	if err := b.authority(fence); err != nil {
		return codexappserver.LifecycleSnapshot{}, err
	}
	if !b.conn.lifecycle || b.conn.session == "" {
		return codexappserver.LifecycleSnapshot{}, refuse(RefusalLifecycleUnsupported, codexappserver.ErrUnsupported)
	}
	operationCtx, cancel := boundedLifecycleContext(ctx)
	defer cancel()
	ownedIPC, err := dialLifecycleIPC(operationCtx, b.conn.discovery, ProtocolRange{
		Preferred: b.conn.version, Minimum: b.conn.version,
	})
	if err != nil {
		return codexappserver.LifecycleSnapshot{}, err
	}
	defer ownedIPC.Close()
	if !ownedIPC.lifecycle || ownedIPC.session == "" || ownedIPC.runtime != b.conn.runtime {
		return codexappserver.LifecycleSnapshot{}, refuse(RefusalLifecycleUnsupported, codexappserver.ErrUnsupported)
	}
	reply, err := ownedIPC.callCancelable(operationCtx, wireRequest{
		Kind: requestLifecycle, TargetSession: b.conn.session, Thread: b.thread, Fence: fence,
	})
	if err != nil {
		return codexappserver.LifecycleSnapshot{}, err
	}
	if reply.Kind == replyRefused || reply.Refusal != "" && reply.Refusal != RefusalNone {
		reason := reply.Refusal
		if reason == RefusalNone {
			reason = RefusalLifecycleProtocol
		}
		return codexappserver.LifecycleSnapshot{}, refuse(reason, lifecycleCause(reason))
	}
	if reply.Snapshot == nil || reply.Snapshot.ThreadID != b.thread || len(reply.Result) != 0 {
		return codexappserver.LifecycleSnapshot{}, refuse(RefusalFrameInvalid, codexappserver.ErrProtocol)
	}
	return *reply.Snapshot, nil
}

func lifecycleCause(reason Refusal) error {
	switch reason {
	case RefusalLifecycleUnsupported:
		return codexappserver.ErrUnsupported
	case RefusalThreadAbsent:
		return codexappserver.ErrThreadAbsent
	case RefusalThreadNotDurable:
		return codexappserver.ErrThreadNotDurable
	case RefusalPayloadTooLarge:
		return codexappserver.ErrPayloadTooLarge
	case RefusalLifecycleProtocol, RefusalFrameInvalid:
		return codexappserver.ErrProtocol
	case RefusalDisconnectBoundary:
		return codexappserver.ErrDisconnected
	default:
		return nil
	}
}

// Answer responds to exactly one inbound server request.
func (b *RemoteBinding) Answer(ctx context.Context, lease ApprovalLease, result any) error {
	if err := b.authority(lease.Fence); err != nil {
		return err
	}
	if !lease.held() || lease.ThreadID != b.thread {
		return refuse(RefusalLeaseIdentityMismatch, nil)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return refuse(RefusalFrameInvalid, err)
	}
	reply, err := b.conn.call(ctx, wireRequest{
		Kind: requestAnswer, Thread: b.thread, Fence: lease.Fence,
		RawRequestID: lease.RawRequestID, Params: payload,
	})
	if err != nil {
		return err
	}
	if reply.Kind == replyRefused {
		return refuse(reply.Refusal, nil)
	}
	return nil
}

// authority checks the fence locally before anything leaves this process.
//
// The check is not a duplicate of the runtime's. A client that survived its
// runtime holds a fence whose epochs restart at one in the replacement, so
// refusing it here is what keeps a revoked epoch from numerically matching a
// fresh one on the far side.
func (b *RemoteBinding) authority(fence Fence) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.revoked != RefusalNone {
		return refuse(b.revoked, nil)
	}
	if !b.open {
		return refuse(RefusalControlNotOpen, nil)
	}
	if fence.Binding != b.fence.Binding {
		return refuse(RefusalStaleBindingEpoch, nil)
	}
	if fence.Connection != b.fence.Connection {
		return refuse(RefusalStaleConnectionEpoch, nil)
	}
	return nil
}

// deliver admits one runtime event, or revokes this binding when its bounded
// queue is full.
func (b *RemoteBinding) deliver(wire *wireEvent) {
	if wire == nil {
		return
	}
	event := Event{
		Fence:    wire.Fence,
		Origin:   wire.Origin,
		Sequence: wire.Sequence,
		Method:   wire.Method,
		Params:   wire.Params,
	}
	if wire.Snapshot != nil {
		event.Snapshot = *wire.Snapshot
	}
	if len(wire.RawRequestID) > 0 {
		event.Lease = ApprovalLease{Fence: wire.Fence, ThreadID: b.thread, RawRequestID: wire.RawRequestID}
	}
	b.mu.Lock()
	if b.revoked != RefusalNone {
		b.mu.Unlock()
		return
	}
	b.fence = wire.Fence
	if wire.Origin == EventOriginSnapshot {
		b.open = true
	}
	b.mu.Unlock()
	select {
	case b.events <- event:
	default:
		b.revoke(RefusalResyncRequired)
	}
}

// suspend closes the authority this binding currently grants and tells its
// consumer, without ending the binding: the next barrier reopens it.
func (b *RemoteBinding) suspend() {
	b.mu.Lock()
	if b.revoked != RefusalNone {
		b.mu.Unlock()
		return
	}
	b.open = false
	b.mu.Unlock()
	select {
	case b.suspends <- struct{}{}:
	default:
	}
}

// revoke terminates this binding exactly once.
func (b *RemoteBinding) revoke(reason Refusal) {
	b.once.Do(func() {
		b.mu.Lock()
		b.revoked = reason
		b.open = false
		b.mu.Unlock()
		close(b.events)
	})
}
