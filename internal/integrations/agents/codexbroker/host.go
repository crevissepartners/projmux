package codexbroker

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"sync"
	"time"
)

const (
	// defaultIdleTimeout bounds how long a runtime survives with no binding.
	// It is short enough that an abandoned runtime does not outlive the work
	// that started it, and long enough that a sequence of short CLI calls
	// reuses one runtime instead of paying for a new endpoint connection each
	// time.
	defaultIdleTimeout = 30 * time.Second
	// handshakeTimeout bounds the first frame of a session, so a connection
	// that never speaks cannot hold a session slot open.
	handshakeTimeout = 5 * time.Second
	// writeTimeout bounds one outbound frame.
	writeTimeout = 5 * time.Second
	// sessionBacklog bounds one session's undelivered frames. The per-binding
	// queue behind it is the one that decides overflow policy, so this bound
	// only has to keep a stalled socket from growing without limit.
	sessionBacklog = 256
	// credentialBytes is the width of the per-runtime local credential.
	credentialBytes = 32
	// runtimeIDBytes is the width of the per-process runtime identity.
	runtimeIDBytes = 16
)

// HostStats is the content-free telemetry projection of one runtime host.
type HostStats struct {
	Endpoint     EndpointKey   `json:"endpoint"`
	Protocol     ProtocolRange `json:"protocol"`
	Sessions     int           `json:"sessions"`
	LiveSessions int           `json:"liveSessions"`
	Bindings     int           `json:"bindings"`
	Refused      int           `json:"refused"`
	Draining     bool          `json:"draining"`
}

// RuntimeTelemetry is the whole content-free operational projection of one
// published runtime: what the host itself is doing, and what the single
// upstream connection beneath it is doing.
//
// It exists so a diagnostics reader gets both halves in one answer. Reading
// them separately would let a report pair a host count with a broker count
// taken a reconnect apart and present the two as one moment.
type RuntimeTelemetry struct {
	Runtime  string      `json:"runtime"`
	Protocol int         `json:"protocol"`
	Host     HostStats   `json:"host"`
	Broker   Diagnostics `json:"broker"`
}

// HostConfig is the closed construction input for one runtime host.
type HostConfig struct {
	// Discovery names the state domain and endpoint this runtime owns.
	Discovery Discovery
	// Broker is the endpoint multiplexer this runtime exposes. Required. The
	// host does not construct one, so the process that owns the upstream
	// connection policy stays the process that decided it.
	Broker *Broker
	// IdleTimeout bounds how long the runtime survives with no binding. Zero
	// means defaultIdleTimeout; a negative value disables the timer, which is
	// only useful to a test that drives shutdown itself.
	IdleTimeout time.Duration
	// Protocol is the local IPC version window this host serves. Zero means
	// the window this build speaks.
	Protocol ProtocolRange
}

// Host is one broker runtime: the process-local singleton that owns one
// endpoint connection and lends it to every client in one state domain.
//
// The singleton is the socket itself. Binding a Unix socket path is atomic and
// exclusive, so two hosts cannot own one discovery contract even if they start
// at the same instant; the startup lock in Ensure exists only to keep a stale
// artifact from being reclaimed out from under a host that just bound it.
type Host struct {
	discovery  Discovery
	broker     *Broker
	idle       time.Duration
	protocol   ProtocolRange
	runtimeID  string
	credential string

	listener   *net.UnixListener
	socketInfo os.FileInfo
	recordInfo os.FileInfo

	done        chan struct{}
	acceptReady chan struct{}
	acceptDone  chan struct{}
	closeOnce   sync.Once
	sessions    sync.WaitGroup

	mu       sync.Mutex
	closing  bool
	draining bool
	bindings int
	stats    HostStats
	timer    *time.Timer
	// live is every accepted connection this runtime is still serving. A
	// shutdown closes them explicitly: a session blocked on its socket would
	// otherwise keep the runtime alive past the moment it decided to stop.
	live map[*net.UnixConn]struct{}
}

// StartHost publishes one runtime on the discovery contract and serves it.
//
// The socket is bound before the record is written, so a record a client can
// read always names a socket that exists. A path that is already bound is
// refused rather than replaced: the artifact belongs to a runtime this process
// has no authority over, and the caller's answer is to dial it.
func StartHost(cfg HostConfig) (*Host, error) {
	if !platformSupported {
		return nil, refuse(RefusalUnsupportedPlatform, nil)
	}
	if cfg.Broker == nil || cfg.Discovery.endpoint == "" {
		return nil, refuse(RefusalEndpointUnknown, nil)
	}
	if err := prepareDiscoveryDir(cfg.Discovery); err != nil {
		return nil, err
	}
	credential, err := randomToken(credentialBytes)
	if err != nil {
		return nil, refuse(RefusalDiscoveryUntrusted, err)
	}
	runtimeID, err := randomToken(runtimeIDBytes)
	if err != nil {
		return nil, refuse(RefusalDiscoveryUntrusted, err)
	}
	path := cfg.Discovery.SocketPath()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, refuse(RefusalRuntimeExists, err)
	}
	host := &Host{
		discovery:   cfg.Discovery,
		broker:      cfg.Broker,
		idle:        cfg.IdleTimeout,
		protocol:    cfg.Protocol.normalize(),
		runtimeID:   runtimeID,
		credential:  credential,
		listener:    listener,
		live:        make(map[*net.UnixConn]struct{}),
		done:        make(chan struct{}),
		acceptReady: make(chan struct{}),
		acceptDone:  make(chan struct{}),
	}
	if host.idle == 0 {
		host.idle = defaultIdleTimeout
	}
	if err := host.publish(); err != nil {
		_ = listener.Close()
		removeIfSame(path, host.socketInfo)
		return nil, err
	}
	// Keep the default auto-unlink through publication failures. Once the Host
	// is fully published, disable path-based listener cleanup so removeIfSame
	// is the only shutdown owner and a late old Host cannot delete a replacement.
	listener.SetUnlinkOnClose(false)
	go host.accept()
	// StartHost is the public startup barrier. Waiting for acceptReady makes
	// the accept owner visible before the Host escapes to a caller that may
	// immediately call Close; acceptDone then gives Close an exact proof that
	// no session can be added before it waits for them.
	<-host.acceptReady
	host.armIdle()
	return host, nil
}

// publish secures the socket and writes the discovery record.
func (h *Host) publish() error {
	path := h.discovery.SocketPath()
	if err := os.Chmod(path, discoveryFileMode); err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	h.socketInfo = info
	record := discoveryRecord{
		Protocol:    h.protocol.Preferred,
		MinProtocol: h.protocol.Minimum,
		Endpoint:    h.discovery.endpoint,
		Runtime:     h.runtimeID,
		PID:         os.Getpid(),
		Credential:  h.credential,
	}
	if err := writeRecord(h.discovery, record); err != nil {
		return err
	}
	recordInfo, err := os.Lstat(h.discovery.RecordPath())
	if err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	h.recordInfo = recordInfo
	return nil
}

// RuntimeID returns this runtime's identity. It is the third authority axis
// clients present with every fenced request.
func (h *Host) RuntimeID() string { return h.runtimeID }

// Done is closed when the runtime has stopped serving.
func (h *Host) Done() <-chan struct{} { return h.done }

// Stats returns the current content-free telemetry snapshot.
func (h *Host) Stats() HostStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	stats := h.stats
	stats.Endpoint = h.discovery.endpoint
	stats.Protocol = h.protocol
	stats.Bindings = h.bindings
	stats.Draining = h.draining
	return stats
}

// Close stops the runtime, releases the broker, and removes exactly the
// artifacts this runtime published. It is idempotent.
//
// Removal is guarded by SameFile, so a runtime that is shutting down late can
// never delete the socket or the record of the runtime that replaced it.
func (h *Host) Close() error {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closing = true
		timer := h.timer
		h.timer = nil
		live := make([]*net.UnixConn, 0, len(h.live))
		for conn := range h.live {
			live = append(live, conn)
		}
		h.live = make(map[*net.UnixConn]struct{})
		h.mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		_ = h.listener.Close()
		// The accept loop is the only owner allowed to add a session. Once it
		// has stopped, sessions.Wait cannot race a future Add or return before
		// an already-accepted session has been accounted for.
		<-h.acceptDone
		removeIfSame(h.discovery.SocketPath(), h.socketInfo)
		removeIfSame(h.discovery.RecordPath(), h.recordInfo)
		for _, conn := range live {
			_ = conn.Close()
		}
		h.sessions.Wait()
		_ = h.broker.Close()
		close(h.done)
	})
	<-h.done
	return nil
}

// accept serves connections until the listener closes.
func (h *Host) accept() {
	defer close(h.acceptDone)
	close(h.acceptReady)
	for {
		conn, err := h.listener.AcceptUnix()
		if err != nil {
			return
		}
		h.sessions.Go(func() {
			h.serveSession(conn)
		})
	}
}

// track registers one accepted connection, refusing it outright when the
// runtime has already decided to stop.
func (h *Host) track(conn *net.UnixConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	h.live[conn] = struct{}{}
	return true
}

// untrack drops one finished connection.
func (h *Host) untrack(conn *net.UnixConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.live, conn)
}

// armIdle starts or restarts the bounded idle shutdown timer. A runtime with
// no binding is a runtime holding an upstream connection nobody asked for.
func (h *Host) armIdle() {
	if h.idle < 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing || h.bindings > 0 {
		return
	}
	if h.timer != nil {
		h.timer.Stop()
	}
	h.timer = time.AfterFunc(h.idle, h.idleFired)
}

// idleFired shuts the runtime down if it is still idle when the timer expires.
func (h *Host) idleFired() {
	h.mu.Lock()
	idle := !h.closing && h.bindings == 0
	h.mu.Unlock()
	if idle {
		go h.Close()
	}
}

// holdBinding accounts one new binding and cancels the idle timer.
func (h *Host) holdBinding() {
	h.mu.Lock()
	h.bindings++
	timer := h.timer
	h.timer = nil
	h.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// releaseBinding accounts one removed binding. A draining runtime stops as
// soon as its last binding is gone, because the only reason it is still
// running is the work it had already accepted.
func (h *Host) releaseBinding() {
	h.mu.Lock()
	if h.bindings > 0 {
		h.bindings--
	}
	last := h.bindings == 0
	draining := h.draining
	h.mu.Unlock()
	if !last {
		return
	}
	if draining {
		go h.Close()
		return
	}
	h.armIdle()
}

// drain marks the runtime as replaced-in-waiting. Active bindings keep
// running: an incompatible client is a binary replacement, not a fault, and
// severing live work to install one would be a worse outcome than waiting.
func (h *Host) drain() {
	h.mu.Lock()
	h.draining = true
	last := h.bindings == 0
	h.mu.Unlock()
	if last {
		go h.Close()
	}
}

// refusingWork reports the closed reason the runtime is no longer accepting
// new work, or RefusalNone while it still is. A drain and a shutdown are told
// apart because they call for different things from the caller: one waits for
// the running work to finish, the other looks for a runtime again.
func (h *Host) refusingWork() Refusal {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closing:
		return RefusalHostClosed
	case h.draining:
		return RefusalDrainRequired
	default:
		return RefusalNone
	}
}

func (h *Host) countRefusal() {
	h.mu.Lock()
	h.stats.Refused++
	h.mu.Unlock()
}

func (h *Host) countSession(delta int) {
	h.mu.Lock()
	if delta > 0 {
		h.stats.Sessions++
	}
	h.stats.LiveSessions += delta
	h.mu.Unlock()
}

// randomToken returns a hex token of the requested byte width.
func randomToken(width int) (string, error) {
	buffer := make([]byte, width)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// authenticate runs the handshake and returns the negotiated version.
//
// Authentication is two independent facts. The credential proves the caller
// read a file only this uid can read, and the owner-private directory that
// file sits in is what makes reading it proof of anything. Neither is a
// substitute for the other, and a caller that fails either one is refused
// before it can bind, submit, or answer.
func (h *Host) authenticate(conn *net.UnixConn, reader *bufio.Reader) (int, bool) {
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	frame, err := readFrame(reader)
	if err != nil {
		return 0, false
	}
	_ = conn.SetReadDeadline(time.Time{})
	var greeting hello
	if json.Unmarshal(frame, &greeting) != nil {
		h.refuseSession(conn, RefusalFrameInvalid)
		return 0, false
	}
	if subtle.ConstantTimeCompare([]byte(greeting.Credential), []byte(h.credential)) != 1 {
		h.refuseSession(conn, RefusalCredentialRejected)
		return 0, false
	}
	if greeting.Endpoint != h.discovery.endpoint {
		h.refuseSession(conn, RefusalEndpointMismatch)
		return 0, false
	}
	version, ok := negotiate(greeting.protocol(), h.protocol)
	if !ok {
		// A binary this runtime cannot talk to has arrived. Start draining so
		// the replacement can take over once the work in flight is done, and
		// tell the caller exactly that instead of failing anonymously.
		h.drain()
		h.refuseSession(conn, RefusalDrainRequired)
		return 0, false
	}
	if reason := h.refusingWork(); reason != RefusalNone {
		h.refuseSession(conn, reason)
		return 0, false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if writeFrame(conn, wireReply{Kind: replyWelcome, Runtime: h.runtimeID, Protocol: version}) != nil {
		return 0, false
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return version, true
}

// refuseSession writes one typed refusal and counts it.
func (h *Host) refuseSession(conn *net.UnixConn, reason Refusal) {
	h.countRefusal()
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_ = writeFrame(conn, wireReply{Kind: replyRefused, Refusal: reason})
}
