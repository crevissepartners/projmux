package codexbroker

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- required by the RFC 6455 test handshake.
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TestLifecycleSnapshotFivePointTwoMegabytesCrossesClientBrokerIPCBodyFree is
// the acceptance path: a real Unix/WebSocket Client owns the shared route, a
// second witnessed transport scans a 5.2MB complete response, and a real
// runtime IPC client receives only the literal metadata projection.
func TestLifecycleSnapshotFivePointTwoMegabytesCrossesClientBrokerIPCBodyFree(t *testing.T) {
	provider := startLifecycleWebSocketProvider(t, 5_200_000)
	broker, err := NewBroker(Config{
		Opener: func(ctx context.Context) (Endpoint, error) {
			return codexappserver.OpenPrivateUnix(ctx, provider.path, 750*time.Millisecond, "test", true)
		},
		Lifecycle: func(ctx context.Context, expected codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return codexappserver.OpenPrivateUnixLifecycle(ctx, provider.path, "test", true, expected)
		},
	})
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	discovery := newRuntimeDiscovery(t)
	host, err := StartHost(HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("host: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, discovery, DialConfig{})
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer client.Close()
	binding, err := client.Bind(ctx, "thread-large", "/work", nil)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer binding.Close()
	var fence Fence
	select {
	case event := <-binding.Events():
		if event.Origin != EventOriginSnapshot {
			t.Fatalf("first event = %+v", event)
		}
		fence = event.Fence
	case <-ctx.Done():
		t.Fatal("snapshot barrier did not close")
	}

	snapshot, err := binding.ReadLifecycleSnapshot(ctx, fence)
	if err != nil {
		t.Fatalf("read lifecycle snapshot: %v", err)
	}
	want := codexappserver.LifecycleSnapshot{
		ThreadID: "thread-large", ThreadState: codexappserver.ThreadStateActive, TurnCount: 2,
		TurnID: "turn-last", TurnState: codexappserver.TurnStateCompleted,
		StartedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	if snapshot != want {
		t.Fatalf("snapshot = %+v, want literal %+v", snapshot, want)
	}
	if provider.largeBytes() <= maxFrameBytes {
		t.Fatalf("provider response bytes = %d, want > generic 1MiB", provider.largeBytes())
	}
	// This request uses the original shared session after the owned app-server
	// and IPC connections have both been reaped.
	if outcome, err := binding.Submit(ctx, fence, Mutation{Method: "history/ping"}); err != nil || outcome != MutationApplied {
		t.Fatalf("sibling shared request after 5.2MB read: outcome=%s err=%v", outcome, err)
	}
	if provider.methodCount("thread/read:complete") != 1 || provider.methodCount("history/ping") != 1 {
		t.Fatalf("provider methods = %#v", provider.methodsSnapshot())
	}
}

type lifecycleWebSocketProvider struct {
	path       string
	listener   *net.UnixListener
	largeFrame []byte
	mu         sync.Mutex
	methods    []string
	response   int
}

func startLifecycleWebSocketProvider(t *testing.T, bodyBytes int) *lifecycleWebSocketProvider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "provider.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen provider: %v", err)
	}
	// LifecycleClient uses request id 1 for initialize and id 2 for its sole
	// complete read. Build the deliberately oversized provider frame before
	// that fixed 750 ms operation begins. The actual 5.2 MB still crosses the
	// real Unix/WebSocket connection; only fixture allocation/copy work moves
	// out of the measured request.
	body := strings.Repeat("x", bodyBytes)
	response := fmt.Sprintf(`{"jsonrpc":"2.0","result":{"thread":{"id":"thread-large","status":{"type":"active"},"turns":[{"id":"turn-old","status":"future","items":[{"type":"agentMessage","text":"%s"}]},{"id":"turn-last","status":"completed","startedAt":1700000000}]}},"id":2}`, body)
	provider := &lifecycleWebSocketProvider{
		path: path, listener: listener, largeFrame: lifecycleServerFrame([]byte(response)), response: len(response),
	}
	go provider.accept()
	t.Cleanup(func() { _ = listener.Close() })
	return provider
}

func (p *lifecycleWebSocketProvider) accept() {
	for {
		conn, err := p.listener.AcceptUnix()
		if err != nil {
			return
		}
		go p.serve(conn)
	}
}

func (p *lifecycleWebSocketProvider) serve(conn *net.UnixConn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	key := request.Header.Get("Sec-WebSocket-Key")
	acceptSum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) // #nosec G401 -- RFC 6455 handshake checksum.
	accept := base64.StdEncoding.EncodeToString(acceptSum[:])
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	for {
		payload, err := readLifecycleClientFrame(reader)
		if err != nil {
			return
		}
		var message struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params struct {
				IncludeTurns bool `json:"includeTurns"`
			} `json:"params"`
		}
		if json.Unmarshal(payload, &message) != nil {
			return
		}
		switch message.Method {
		case "initialize":
			p.note("initialize")
			p.write(conn, fmt.Sprintf(`{"id":%s,"result":{"userAgent":"codex-cli/0.150.1","platformFamily":"unix","platformOs":"linux"}}`, message.ID))
		case "initialized":
			p.note("initialized")
		case "thread/resume":
			p.note("thread/resume")
			p.write(conn, fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":"thread-large"}}}`, message.ID))
		case "thread/read":
			if !message.Params.IncludeTurns {
				p.note("thread/read:catalog")
				p.write(conn, fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":"thread-large","cwd":"/work","createdAt":1,"updatedAt":2,"status":{"type":"active"}}}}`, message.ID))
				continue
			}
			p.note("thread/read:complete")
			if string(message.ID) != "2" {
				p.note("thread/read:unexpected-id")
				return
			}
			_, _ = conn.Write(p.largeFrame)
		case "history/ping":
			p.note("history/ping")
			p.write(conn, fmt.Sprintf(`{"id":%s,"result":{}}`, message.ID))
		default:
			return
		}
	}
}

func (p *lifecycleWebSocketProvider) write(conn net.Conn, payload string) {
	_, _ = conn.Write(lifecycleServerFrame([]byte(payload)))
}

func (p *lifecycleWebSocketProvider) note(method string) {
	p.mu.Lock()
	p.methods = append(p.methods, method)
	p.mu.Unlock()
}

func (p *lifecycleWebSocketProvider) largeBytes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.response
}

func (p *lifecycleWebSocketProvider) methodCount(want string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, method := range p.methods {
		if method == want {
			count++
		}
	}
	return count
}

func (p *lifecycleWebSocketProvider) methodsSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.methods...)
}

func readLifecycleClientFrame(reader *bufio.Reader) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	if header[1]&0x80 == 0 {
		return nil, errors.New("unmasked client websocket frame")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	for index := range payload {
		payload[index] ^= mask[index%len(mask)]
	}
	return payload, nil
}

func lifecycleServerFrame(payload []byte) []byte {
	header := []byte{0x81}
	switch {
	case len(payload) <= 125:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65_535:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(len(payload)))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(len(payload)))
	}
	return append(header, payload...)
}

type witnessedFakeEndpoint struct {
	*fakeEndpoint
	peer codexappserver.PeerIdentity
}

func (e *witnessedFakeEndpoint) PeerIdentity() codexappserver.PeerIdentity { return e.peer }

type gatedLifecycleEndpoint struct {
	peer      codexappserver.PeerIdentity
	threadID  string
	started   chan struct{}
	release   chan struct{}
	reaped    chan struct{}
	startOnce sync.Once
	reapOnce  sync.Once
	result    codexappserver.LifecycleSnapshot
	err       error
}

type lifecycleResultEndpoint struct {
	peer     codexappserver.PeerIdentity
	snapshot codexappserver.LifecycleSnapshot
	err      error
	closeErr error
}

func (e *lifecycleResultEndpoint) PeerIdentity() codexappserver.PeerIdentity { return e.peer }
func (e *lifecycleResultEndpoint) ReadLifecycleSnapshot(context.Context, string) (codexappserver.LifecycleSnapshot, error) {
	return e.snapshot, e.err
}
func (e *lifecycleResultEndpoint) Close() error { return e.closeErr }

type stubbornLifecycleEndpoint struct {
	peer    codexappserver.PeerIdentity
	started chan struct{}
	release chan struct{}
	reaped  chan struct{}
	once    sync.Once
}

func (e *stubbornLifecycleEndpoint) PeerIdentity() codexappserver.PeerIdentity { return e.peer }
func (e *stubbornLifecycleEndpoint) ReadLifecycleSnapshot(context.Context, string) (codexappserver.LifecycleSnapshot, error) {
	e.once.Do(func() { close(e.started) })
	<-e.release
	return codexappserver.LifecycleSnapshot{ThreadID: "thread-late", ThreadState: codexappserver.ThreadStateIdle}, nil
}
func (e *stubbornLifecycleEndpoint) Close() error {
	select {
	case <-e.reaped:
	default:
		close(e.reaped)
	}
	return nil
}

func newGatedLifecycleEndpoint(peer codexappserver.PeerIdentity, threadID string) *gatedLifecycleEndpoint {
	return &gatedLifecycleEndpoint{
		peer: peer, threadID: threadID, started: make(chan struct{}), release: make(chan struct{}), reaped: make(chan struct{}),
		result: codexappserver.LifecycleSnapshot{
			ThreadID: threadID, ThreadState: codexappserver.ThreadStateIdle,
		},
	}
}

func (e *gatedLifecycleEndpoint) PeerIdentity() codexappserver.PeerIdentity { return e.peer }

func (e *gatedLifecycleEndpoint) ReadLifecycleSnapshot(ctx context.Context, threadID string) (codexappserver.LifecycleSnapshot, error) {
	e.startOnce.Do(func() { close(e.started) })
	if threadID != e.threadID {
		return codexappserver.LifecycleSnapshot{}, codexappserver.ErrProtocol
	}
	select {
	case <-e.release:
		return e.result, e.err
	case <-ctx.Done():
		return codexappserver.LifecycleSnapshot{}, ctx.Err()
	}
}

func (e *gatedLifecycleEndpoint) Close() error {
	e.reapOnce.Do(func() { close(e.reaped) })
	return nil
}

type lifecycleRuntimeFixture struct {
	discovery Discovery
	host      *Host
	broker    *Broker
	client    *Conn
	binding   *RemoteBinding
	fence     Fence
	shared    *fakeEndpoint
}

func startLifecycleRuntimeFixture(t *testing.T, threadID string, opener LifecycleOpener) lifecycleRuntimeFixture {
	t.Helper()
	peer := codexappserver.PeerIdentity{PID: 42, OwnerUID: 1000, Start: "test:owned-peer"}
	shared := newFakeEndpoint()
	broker, err := NewBroker(Config{
		Opener: func(context.Context) (Endpoint, error) {
			return &witnessedFakeEndpoint{fakeEndpoint: shared, peer: peer}, nil
		},
		Lifecycle: opener,
	})
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	discovery := newRuntimeDiscovery(t)
	host, err := StartHost(HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("host: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, discovery, DialConfig{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	binding, err := client.Bind(ctx, threadID, "", nil)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = binding.Close() })
	var fence Fence
	select {
	case event := <-binding.Events():
		fence = event.Fence
	case <-ctx.Done():
		t.Fatal("binding snapshot barrier timed out")
	}
	return lifecycleRuntimeFixture{
		discovery: discovery, host: host, broker: broker, client: client,
		binding: binding, fence: fence, shared: shared,
	}
}

func TestLifecycleSidecarRolesTokensAndOldNewCompatibility(t *testing.T) {
	peer := codexappserver.PeerIdentity{PID: 42, OwnerUID: 1000, Start: "test:owned-peer"}
	fixture := startLifecycleRuntimeFixture(t, "thread-role", func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
		return newGatedLifecycleEndpoint(peer, "thread-role"), nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	// The ordinary shared session cannot invoke either owned operation kind.
	reply, err := fixture.client.call(ctx, wireRequest{
		Kind: requestLifecycle, TargetSession: fixture.client.session, Thread: "thread-role", Fence: fixture.fence,
	})
	if err != nil || reply.Kind != replyRefused || reply.Refusal != RefusalRequestUnknown {
		t.Fatalf("shared lifecycle dispatch reply=%+v err=%v", reply, err)
	}

	sidecar, err := dialLifecycleIPC(ctx, fixture.discovery, CurrentProtocol())
	if err != nil {
		t.Fatalf("dial lifecycle sidecar: %v", err)
	}
	defer sidecar.Close()
	// Conversely, the request-owned session cannot bind or become event/control
	// authority.
	reply, err = sidecar.call(ctx, wireRequest{Kind: requestBind, Thread: "sidecar-thread"})
	if err != nil || reply.Kind != replyRefused || reply.Refusal != RefusalRequestUnknown {
		t.Fatalf("sidecar bind reply=%+v err=%v", reply, err)
	}
	for name, target := range map[string]string{
		"wrong":          "not-a-session-token",
		"sidecar target": sidecar.session,
	} {
		t.Run(name, func(t *testing.T) {
			reply, err := sidecar.callCancelable(ctx, wireRequest{
				Kind: requestLifecycle, TargetSession: target, Thread: "thread-role", Fence: fixture.fence,
			})
			if err != nil || reply.Kind != replyRefused || reply.Refusal != RefusalBindingClosed {
				t.Fatalf("reply=%+v err=%v", reply, err)
			}
		})
	}

	// A new host remains consumable by an old client shape: unknown welcome
	// fields are ignored, then the unchanged bind, submit, event, and answer
	// frames retain their original meanings.
	raw, err := net.DialTimeout("unix", fixture.discovery.SocketPath(), time.Second)
	if err != nil {
		t.Fatalf("old client dial: %v", err)
	}
	defer raw.Close()
	record, err := readRecord(fixture.discovery)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(raw, hello{Preferred: 1, Minimum: 1, Endpoint: fixture.discovery.Endpoint(), Credential: record.Credential}); err != nil {
		t.Fatal(err)
	}
	oldReader := bufio.NewReader(raw)
	frame, err := readFrame(oldReader)
	if err != nil {
		t.Fatal(err)
	}
	var oldWelcome struct {
		Kind     replyKind `json:"kind"`
		Runtime  string    `json:"runtime"`
		Protocol int       `json:"protocol"`
	}
	if json.Unmarshal(frame, &oldWelcome) != nil || oldWelcome.Kind != replyWelcome || oldWelcome.Runtime == "" || oldWelcome.Protocol != 1 {
		t.Fatalf("old client welcome = %+v", oldWelcome)
	}
	type legacyReply struct {
		ID      uint64          `json:"id,omitempty"`
		Kind    replyKind       `json:"kind"`
		Refusal Refusal         `json:"refusal,omitempty"`
		Outcome MutationOutcome `json:"outcome,omitempty"`
		Thread  string          `json:"thread,omitempty"`
		Event   *wireEvent      `json:"event,omitempty"`
	}
	nextLegacy := func() legacyReply {
		t.Helper()
		if err := raw.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		frame, err := readFrame(oldReader)
		if err != nil {
			t.Fatalf("old client read: %v", err)
		}
		var reply legacyReply
		if json.Unmarshal(frame, &reply) != nil {
			t.Fatalf("old client reply: %s", frame)
		}
		return reply
	}
	const legacyThread = "thread-legacy-client"
	if err := writeFrame(raw, wireRequest{
		ID: 1, Kind: requestBind, Runtime: oldWelcome.Runtime, Thread: legacyThread,
	}); err != nil {
		t.Fatal(err)
	}
	var legacyFence Fence
	for bound, snapshotted := false, false; !bound || !snapshotted; {
		reply := nextLegacy()
		switch {
		case reply.ID == 1 && reply.Kind == replyResult && reply.Thread == legacyThread:
			bound = true
		case reply.Kind == replyEvent && reply.Thread == legacyThread && reply.Event != nil && reply.Event.Origin == EventOriginSnapshot:
			legacyFence = reply.Event.Fence
			snapshotted = true
		default:
			t.Fatalf("old client bind frame = %+v", reply)
		}
	}
	if err := writeFrame(raw, wireRequest{
		ID: 2, Kind: requestSubmit, Runtime: oldWelcome.Runtime, Thread: legacyThread,
		Fence: legacyFence, Method: "legacy/submit",
	}); err != nil {
		t.Fatal(err)
	}
	if reply := nextLegacy(); reply.ID != 2 || reply.Kind != replyResult || reply.Outcome != MutationApplied {
		t.Fatalf("old client submit reply = %+v", reply)
	}
	fixture.shared.push(serverRequestEvent(legacyThread, "item/commandExecution/requestApproval", "919"))
	approval := nextLegacy()
	if approval.Kind != replyEvent || approval.Event == nil || string(approval.Event.RawRequestID) != "919" {
		t.Fatalf("old client approval event = %+v", approval)
	}
	if err := writeFrame(raw, wireRequest{
		ID: 3, Kind: requestAnswer, Runtime: oldWelcome.Runtime, Thread: legacyThread,
		Fence: approval.Event.Fence, RawRequestID: approval.Event.RawRequestID,
		Params: json.RawMessage(`{"decision":"accept"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if reply := nextLegacy(); reply.ID != 3 || reply.Kind != replyResult || reply.Thread != legacyThread {
		t.Fatalf("old client answer reply = %+v", reply)
	}
	if answered := fixture.shared.answered(); len(answered) != 1 || answered[0] != "919" {
		t.Fatalf("old client answers = %v", answered)
	}

	// A new client faced with an old host welcome has no lifecycle capability
	// or session token and refuses before writing a raw thread/read fallback.
	clientSide, serverSide := net.Pipe()
	serverRead := make(chan []byte, 1)
	go func() {
		reader := bufio.NewReader(serverSide)
		_, _ = readFrame(reader)
		_ = writeFrame(serverSide, wireReply{Kind: replyWelcome, Runtime: "old-runtime", Protocol: 1})
		_ = serverSide.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		extra, _ := readFrame(reader)
		serverRead <- extra
	}()
	oldConn, err := handshake(clientSide, Discovery{endpoint: DefaultEndpointKey}, discoveryRecord{Credential: "old"}, CurrentProtocol(), time.Second, "")
	if err != nil {
		t.Fatalf("old host handshake: %v", err)
	}
	oldBinding := &RemoteBinding{
		conn: oldConn, thread: "thread-old", open: true,
		fence: Fence{Connection: 1, Binding: 1}, revoked: RefusalNone,
	}
	if _, err := oldBinding.ReadLifecycleSnapshot(ctx, oldBinding.fence); RefusalOf(err) != RefusalLifecycleUnsupported {
		t.Fatalf("old host lifecycle error = %v", err)
	}
	if extra := <-serverRead; len(extra) != 0 {
		t.Fatalf("new client wrote raw fallback to old host: %q", extra)
	}
	_ = oldConn.Close()
	_ = serverSide.Close()

	staleToken := fixture.client.session
	_ = fixture.client.Close()
	for deadline := time.Now().Add(time.Second); fixture.host.lookupSession(staleToken) != nil; {
		if time.Now().After(deadline) {
			t.Fatal("closed shared session token stayed registered")
		}
		time.Sleep(time.Millisecond)
	}
	staleSidecar, err := dialLifecycleIPC(ctx, fixture.discovery, CurrentProtocol())
	if err != nil {
		t.Fatalf("stale sidecar dial: %v", err)
	}
	defer staleSidecar.Close()
	reply, err = staleSidecar.callCancelable(ctx, wireRequest{
		Kind: requestLifecycle, TargetSession: staleToken, Thread: "thread-role", Fence: fixture.fence,
	})
	if err != nil || reply.Kind != replyRefused || reply.Refusal != RefusalBindingClosed {
		t.Fatalf("stale target reply=%+v err=%v", reply, err)
	}
}

func TestLifecycleSidecarBlockedWritesCancelWithinCleanupAndPreservesSibling(t *testing.T) {
	peer := codexappserver.PeerIdentity{PID: 42, OwnerUID: 1000, Start: "test:owned-peer"}
	owned := newGatedLifecycleEndpoint(peer, "thread-cancel")
	fixture := startLifecycleRuntimeFixture(t, "thread-cancel", func(_ context.Context, expected codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
		if !codexappserver.SamePeerIdentity(expected, peer) {
			return nil, codexappserver.ErrEndpointChanged
		}
		return owned, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	// A blocked initial sidecar writer is a queue-free local refusal. The host
	// opens no owned provider transport and the shared session remains usable.
	blockedInitial, err := dialLifecycleIPC(ctx, fixture.discovery, CurrentProtocol())
	if err != nil {
		t.Fatal(err)
	}
	blockedInitial.writeMu.Lock()
	startedAt := time.Now()
	_, err = blockedInitial.callCancelable(ctx, wireRequest{
		Kind: requestLifecycle, TargetSession: fixture.client.session, Thread: "thread-cancel", Fence: fixture.fence,
	})
	blockedInitial.writeMu.Unlock()
	_ = blockedInitial.Close()
	if RefusalOf(err) != RefusalLifecycleBusy || time.Since(startedAt) > ownedLifecycleCleanup {
		t.Fatalf("blocked initial write err=%v elapsed=%s", err, time.Since(startedAt))
	}
	select {
	case <-owned.started:
		t.Fatal("blocked initial IPC write opened an owned provider transport")
	default:
	}

	// Once the request has started, block the correlated cancel write itself.
	// Closing this request-owned IPC session must still reap the host operation
	// within 250ms without closing the shared binding session.
	sidecar, err := dialLifecycleIPC(ctx, fixture.discovery, CurrentProtocol())
	if err != nil {
		t.Fatal(err)
	}
	readCtx, cancelRead := context.WithCancel(ctx)
	readDone := make(chan error, 1)
	go func() {
		_, err := sidecar.callCancelable(readCtx, wireRequest{
			Kind: requestLifecycle, TargetSession: fixture.client.session, Thread: "thread-cancel", Fence: fixture.fence,
		})
		readDone <- err
	}()
	select {
	case <-owned.started:
	case <-time.After(time.Second):
		t.Fatal("owned provider read did not start")
	}
	sidecar.writeMu.Lock()
	cancelAt := time.Now()
	cancelRead()
	select {
	case err := <-readDone:
		if RefusalOf(err) != RefusalDisconnectBoundary && RefusalOf(err) != RefusalBindingClosed {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(ownedLifecycleCleanup):
		t.Fatal("caller stayed blocked past lifecycle cleanup budget")
	}
	select {
	case <-owned.reaped:
	case <-time.After(time.Until(cancelAt.Add(ownedLifecycleCleanup))):
		t.Fatal("host owned transport was not reaped within 250ms")
	}
	sidecar.writeMu.Unlock()
	_ = sidecar.Close()

	if outcome, err := fixture.binding.Submit(ctx, fixture.fence, Mutation{Method: "sibling/after-cancel"}); err != nil || outcome != MutationApplied {
		t.Fatalf("sibling after blocked cancel: outcome=%s err=%v", outcome, err)
	}
	if fixture.client.Revocation() != RefusalNone || fixture.shared.visited("request:sibling/after-cancel") != 1 {
		t.Fatalf("shared session changed after owned cancel: refusal=%s methods=%v", fixture.client.Revocation(), fixture.shared.methods())
	}
}

func TestLifecycleOwnedReadIsCanceledByTargetSessionAndHostClose(t *testing.T) {
	for _, test := range []struct {
		name  string
		close func(lifecycleRuntimeFixture)
	}{
		{name: "target session", close: func(f lifecycleRuntimeFixture) { _ = f.client.Close() }},
		{name: "host", close: func(f lifecycleRuntimeFixture) { _ = f.host.Close() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := codexappserver.PeerIdentity{PID: 42, OwnerUID: 1000, Start: "test:owned-peer"}
			owned := newGatedLifecycleEndpoint(peer, "thread-close")
			fixture := startLifecycleRuntimeFixture(t, "thread-close", func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
				return owned, nil
			})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := fixture.binding.ReadLifecycleSnapshot(ctx, fixture.fence)
				done <- err
			}()
			select {
			case <-owned.started:
			case <-time.After(time.Second):
				t.Fatal("owned read did not start")
			}
			closedAt := time.Now()
			test.close(fixture)
			select {
			case <-owned.reaped:
			case <-time.After(time.Until(closedAt.Add(ownedLifecycleCleanup))):
				t.Fatal("owned read survived session/host close cleanup budget")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("closed authority published a lifecycle result")
				}
			case <-time.After(ownedLifecycleCleanup):
				t.Fatal("lifecycle caller survived session/host close")
			}
		})
	}
}

func TestLifecycleAdmissionRetryAndIdleLedgerReclaimAreBounded(t *testing.T) {
	peer := codexappserver.PeerIdentity{PID: 55, OwnerUID: 1000, Start: "test:admission-peer"}
	shared := newFakeEndpoint()
	first := newGatedLifecycleEndpoint(peer, "thread-a")
	second := newGatedLifecycleEndpoint(peer, "thread-b")
	var openerMu sync.Mutex
	owned := []codexappserver.LifecycleEndpoint{first, second}
	clock := newFakeClock()
	broker, err := NewBroker(Config{
		Clock: clock,
		Opener: func(context.Context) (Endpoint, error) {
			return &witnessedFakeEndpoint{fakeEndpoint: shared, peer: peer}, nil
		},
		Lifecycle: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			openerMu.Lock()
			defer openerMu.Unlock()
			if len(owned) == 0 {
				return nil, errors.New("unexpected owned open")
			}
			next := owned[0]
			owned = owned[1:]
			return next, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	bindings := map[string]*Binding{}
	fences := map[string]Fence{}
	for _, threadID := range []string{"thread-a", "thread-b", "thread-c"} {
		binding, err := broker.Bind(threadID, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		bindings[threadID] = binding
		defer binding.Close()
		select {
		case event := <-binding.Events():
			fences[threadID] = event.Fence
		case <-time.After(time.Second):
			t.Fatalf("barrier for %s", threadID)
		}
	}

	type result struct {
		snapshot codexappserver.LifecycleSnapshot
		err      error
	}
	read := func(threadID string) <-chan result {
		done := make(chan result, 1)
		go func() {
			snapshot, err := bindings[threadID].ReadLifecycleSnapshot(t.Context(), fences[threadID])
			done <- result{snapshot: snapshot, err: err}
		}()
		return done
	}
	firstDone := read("thread-a")
	<-first.started
	if _, err := bindings["thread-a"].ReadLifecycleSnapshot(t.Context(), fences["thread-a"]); RefusalOf(err) != RefusalLifecycleBusy {
		t.Fatalf("same-thread concurrent refusal = %v", err)
	}
	secondDone := read("thread-b")
	<-second.started
	if _, err := bindings["thread-c"].ReadLifecycleSnapshot(t.Context(), fences["thread-c"]); RefusalOf(err) != RefusalLifecycleBusy {
		t.Fatalf("endpoint third concurrent refusal = %v", err)
	}
	close(first.release)
	close(second.release)
	for threadID, done := range map[string]<-chan result{"thread-a": firstDone, "thread-b": secondDone} {
		got := <-done
		if got.err != nil || got.snapshot.ThreadID != threadID {
			t.Fatalf("%s result=%+v err=%v", threadID, got.snapshot, got.err)
		}
	}
	if _, err := bindings["thread-a"].ReadLifecycleSnapshot(t.Context(), fences["thread-a"]); RefusalOf(err) != RefusalLifecycleRetry {
		t.Fatalf("immediate retry refusal = %v", err)
	}
	if err := bindings["thread-a"].Close(); err != nil {
		t.Fatal(err)
	}
	rebound, err := broker.Bind("thread-a", "", nil)
	if err != nil {
		t.Fatalf("rebind inside retry interval: %v", err)
	}
	bindings["thread-a"] = rebound
	select {
	case event := <-rebound.Events():
		fences["thread-a"] = event.Fence
	case <-time.After(time.Second):
		t.Fatal("rebound snapshot barrier timed out")
	}
	if _, err := rebound.ReadLifecycleSnapshot(t.Context(), fences["thread-a"]); RefusalOf(err) != RefusalLifecycleRetry {
		t.Fatalf("disconnect/rebind bypassed retry fence: %v", err)
	}

	clock.Advance(ownedLifecycleRetry)
	for deadline := time.Now().Add(time.Second); ; {
		broker.mu.Lock()
		entries := len(broker.lastOwned)
		broker.mu.Unlock()
		if entries == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle retry ledger retained %d entries", entries)
		}
		time.Sleep(time.Millisecond)
	}

	broker.mu.Lock()
	for index := range ownedLifecycleRetryEntries {
		broker.lastOwned[fmt.Sprintf("synthetic-%04d", index)] = clock.Now()
	}
	broker.mu.Unlock()
	if _, err := bindings["thread-c"].ReadLifecycleSnapshot(t.Context(), fences["thread-c"]); RefusalOf(err) != RefusalLifecycleBusy {
		t.Fatalf("full retry ledger refusal = %v", err)
	}
	broker.mu.Lock()
	entries := len(broker.lastOwned)
	broker.mu.Unlock()
	if entries != ownedLifecycleRetryEntries {
		t.Fatalf("retry ledger entries = %d, want hard cap %d", entries, ownedLifecycleRetryEntries)
	}
}

func TestEveryOwnedFailureLeavesSharedSiblingAuthorityUnchanged(t *testing.T) {
	peer := codexappserver.PeerIdentity{PID: 42, OwnerUID: 1000, Start: "test:owned-peer"}
	for _, test := range []struct {
		name   string
		want   Refusal
		opener LifecycleOpener
		short  bool
	}{
		{name: "unsupported", want: RefusalLifecycleUnsupported, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return nil, codexappserver.ErrUnsupported
		}},
		{name: "thread absent", want: RefusalThreadAbsent, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return nil, codexappserver.ErrThreadAbsent
		}},
		{name: "thread not durable", want: RefusalThreadNotDurable, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return nil, codexappserver.ErrThreadNotDurable
		}},
		{name: "payload", want: RefusalPayloadTooLarge, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return &lifecycleResultEndpoint{peer: peer, err: codexappserver.ErrPayloadTooLarge}, nil
		}},
		{name: "protocol", want: RefusalLifecycleProtocol, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return &lifecycleResultEndpoint{peer: peer, err: codexappserver.ErrProtocol}, nil
		}},
		{name: "disconnect", want: RefusalDisconnectBoundary, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return &lifecycleResultEndpoint{peer: peer, err: codexappserver.ErrDisconnected}, nil
		}},
		{name: "peer replacement", want: RefusalLifecycleProtocol, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return &lifecycleResultEndpoint{peer: codexappserver.PeerIdentity{PID: 43, OwnerUID: 1000, Start: "test:replacement"}}, nil
		}},
		{name: "close failure", want: RefusalEndpointRefused, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return &lifecycleResultEndpoint{
				peer: peer, snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-failure", ThreadState: codexappserver.ThreadStateIdle},
				closeErr: errors.New("close failed"),
			}, nil
		}},
		{name: "short caller deadline", want: RefusalDisconnectBoundary, short: true, opener: func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return newGatedLifecycleEndpoint(peer, "thread-failure"), nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := startLifecycleRuntimeFixture(t, "thread-failure", test.opener)
			ctx := t.Context()
			var cancel context.CancelFunc
			if test.short {
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			_, err := fixture.binding.ReadLifecycleSnapshot(ctx, fixture.fence)
			if RefusalOf(err) != test.want {
				t.Fatalf("lifecycle refusal = %s (%v), want %s", RefusalOf(err), err, test.want)
			}
			if outcome, siblingErr := fixture.binding.Submit(t.Context(), fixture.fence, Mutation{Method: "sibling/healthy"}); siblingErr != nil || outcome != MutationApplied {
				t.Fatalf("sibling after %s: outcome=%s err=%v", test.name, outcome, siblingErr)
			}
			if fixture.client.Revocation() != RefusalNone || fixture.shared.visited("request:sibling/healthy") != 1 {
				t.Fatalf("shared authority after %s: refusal=%s methods=%v", test.name, fixture.client.Revocation(), fixture.shared.methods())
			}
		})
	}
}

func TestCanceledOwnedLateSummaryIsDiscardedWhileSiblingSucceeds(t *testing.T) {
	peer := codexappserver.PeerIdentity{PID: 42, OwnerUID: 1000, Start: "test:owned-peer"}
	owned := &stubbornLifecycleEndpoint{
		peer: peer, started: make(chan struct{}), release: make(chan struct{}), reaped: make(chan struct{}),
	}
	fixture := startLifecycleRuntimeFixture(t, "thread-late", func(context.Context, codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
		return owned, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := fixture.binding.ReadLifecycleSnapshot(ctx, fixture.fence)
		done <- err
	}()
	<-owned.started
	cancel()
	select {
	case err := <-done:
		if RefusalOf(err) != RefusalDisconnectBoundary && RefusalOf(err) != RefusalBindingClosed {
			t.Fatalf("cancel result = %v", err)
		}
	case <-time.After(ownedLifecycleCleanup):
		t.Fatal("canceled client waited for a late summary")
	}
	if outcome, err := fixture.binding.Submit(t.Context(), fixture.fence, Mutation{Method: "sibling/during-late"}); err != nil || outcome != MutationApplied {
		t.Fatalf("sibling during late result: outcome=%s err=%v", outcome, err)
	}
	close(owned.release)
	select {
	case <-owned.reaped:
	case <-time.After(time.Second):
		t.Fatal("late owned endpoint was not eventually reaped")
	}
	// The request-owned IPC connection was already closed, so the host's late
	// typed reply has no pending request or shared-session destination.
	if fixture.client.Revocation() != RefusalNone || fixture.shared.visited("request:sibling/during-late") != 1 {
		t.Fatalf("late result changed shared authority: refusal=%s", fixture.client.Revocation())
	}
}
