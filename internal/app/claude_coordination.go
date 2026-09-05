package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const (
	claudeCoordinationVersion       = 1
	claudeProviderFrameMaxBytes     = 8 << 10
	claudeCoordinationHookTimeout   = 24 * time.Hour
	claudeCoordinationWaitTimeout   = claudeCoordinationHookTimeout - 10*time.Second
	claudeCoordinationReceiptWindow = 5 * time.Second
	claudeCoordinationManagedMarker = "projmux-managed:claude-coordination:v1"
	claudeCoordinationHookCommand   = "exec projmux internal claude-message-wait # " + claudeCoordinationManagedMarker
)

type claudeCoordinationTarget struct {
	AgentUID   string                          `json:"agentUID"`
	PaneUID    string                          `json:"paneUID"`
	Generation string                          `json:"generation"`
	Provider   string                          `json:"provider"`
	Authority  coremetadata.ClaudeAuthorityRef `json:"authority"`
}

func claudeTargetForRoute(route coremetadata.AgentRouteRef) (claudeCoordinationTarget, bool) {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || !authority.Valid() || !route.Same(route) {
		return claudeCoordinationTarget{}, false
	}
	return claudeCoordinationTarget{AgentUID: route.AgentUID, PaneUID: route.PaneUID, Generation: route.Generation,
		Provider: aiModeClaude, Authority: authority}, true
}

func (t claudeCoordinationTarget) matches(route coremetadata.AgentRouteRef) bool {
	expected, ok := claudeTargetForRoute(route)
	return ok && t == expected
}

// claudeCoordinationSocket is derived only from the exact stable activation
// and its typed Claude authority. Names, tmux runtime IDs, provider titles,
// vendor locators, and tokens never enter this address.
func claudeCoordinationSocket(registryPath string, target claudeCoordinationTarget) string {
	identity, _ := json.Marshal(target)
	digest := sha256.Sum256(identity)
	return claudeActivationLeaseDir(registryPath, target.PaneUID, target.Generation) + "/coord-" + hex.EncodeToString(digest[:12]) + ".sock"
}

type claudeCoordinationSource struct {
	Kind      string `json:"kind"`
	Trust     string `json:"trust"`
	Authority string `json:"authority"`
}

type claudeCoordinationEnvelope struct {
	Version    int                      `json:"version"`
	MessageRef string                   `json:"messageRef"`
	Target     claudeCoordinationTarget `json:"target"`
	Source     claudeCoordinationSource `json:"source"`
	Payload    string                   `json:"payload"`
	Deadline   time.Time                `json:"deadline"`
}

func (e claudeCoordinationEnvelope) valid(now time.Time, route coremetadata.AgentRouteRef) bool {
	if e.Version != claudeCoordinationVersion || !validCoordinationRef(e.MessageRef) || !e.Target.matches(route) ||
		e.Source.Kind != "peer" || e.Source.Trust != "untrusted" || e.Source.Authority != "coordination-only" ||
		e.Payload == "" || e.Deadline.IsZero() {
		return false
	}
	payload, err := json.Marshal(e)
	return err == nil && len(payload)+1 <= claudeProviderFrameMaxBytes && e.Deadline.After(now) &&
		!e.Deadline.After(now.Add(claudeCoordinationHookTimeout))
}

type claudeCoordinationRequest struct {
	Version          int                         `json:"version"`
	Operation        string                      `json:"operation"`
	Target           claudeCoordinationTarget    `json:"target"`
	HookEvent        string                      `json:"hookEvent,omitempty"`
	SessionID        string                      `json:"sessionId,omitempty"`
	WaitUntil        time.Time                   `json:"waitUntil,omitzero"`
	Envelope         *claudeCoordinationEnvelope `json:"envelope,omitempty"`
	MessageRef       string                      `json:"messageRef,omitempty"`
	WaiterRef        string                      `json:"waiterRef,omitempty"`
	FullFrameWritten bool                        `json:"fullFrameWritten,omitempty"`
}

type claudeCoordinationResponse struct {
	Version   int                         `json:"version"`
	Kind      string                      `json:"kind"`
	WaiterRef string                      `json:"waiterRef,omitempty"`
	Envelope  *claudeCoordinationEnvelope `json:"envelope,omitempty"`
	Delivery  agentdelivery.Delivery      `json:"delivery,omitzero"`
}

type claudeCoordinationWaiter struct {
	ref     string
	process coremetadata.ProcessIdentity
	result  chan claudeCoordinationResponse
}

type claudeCoordinationMessage struct {
	envelope claudeCoordinationEnvelope
	delivery agentdelivery.Delivery
	process  coremetadata.ProcessIdentity
}

type claudeCoordinationHub struct {
	mu            sync.Mutex
	now           func() time.Time
	receiptWindow time.Duration
	waiter        *claudeCoordinationWaiter
	messages      map[string]*claudeCoordinationMessage
	pending       []string
	waiterReady   chan string
	closed        bool
}

func newClaudeCoordinationHub() *claudeCoordinationHub {
	return &claudeCoordinationHub{now: time.Now, receiptWindow: claudeCoordinationReceiptWindow,
		messages: make(map[string]*claudeCoordinationMessage), waiterReady: make(chan string, 1)}
}

func (h *claudeCoordinationHub) submit(envelope claudeCoordinationEnvelope) agentdelivery.Delivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing := h.messages[envelope.MessageRef]; existing != nil {
		return existing.delivery
	}
	delivery, _ := agentdelivery.Reduce(agentdelivery.Delivery{}, agentdelivery.Event{Kind: agentdelivery.EventQueue, MessageRef: envelope.MessageRef})
	message := &claudeCoordinationMessage{envelope: envelope, delivery: delivery}
	h.messages[envelope.MessageRef] = message
	if h.closed {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventStale, MessageRef: envelope.MessageRef, Reason: "helper-stale"})
		return message.delivery
	}
	if !envelope.Deadline.After(h.now()) {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventExpire, MessageRef: envelope.MessageRef, Reason: "ttl"})
		return message.delivery
	}
	if h.waiter == nil {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventHold, MessageRef: envelope.MessageRef, Reason: "no-waiter"})
		h.pending = append(h.pending, envelope.MessageRef)
		h.scheduleTTL(envelope.MessageRef, envelope.Deadline)
		return message.delivery
	}
	h.assignLocked(message, h.waiter)
	return message.delivery
}

func (h *claudeCoordinationHub) arm(process coremetadata.ProcessIdentity) (*claudeCoordinationWaiter, *claudeCoordinationWaiter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil
	}
	waiter := &claudeCoordinationWaiter{ref: newCoordinationRef("waiter"), process: process, result: make(chan claudeCoordinationResponse, 1)}
	superseded := h.waiter
	h.waiter = waiter
	if superseded != nil {
		superseded.result <- claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "superseded", WaiterRef: superseded.ref}
	}
	select {
	case h.waiterReady <- waiter.ref:
	default:
	}
	h.expirePendingLocked()
	for len(h.pending) > 0 {
		ref := h.pending[0]
		h.pending = h.pending[1:]
		message := h.messages[ref]
		if message == nil || message.delivery.State.Terminal() || message.delivery.State == agentdelivery.StateHandoff {
			continue
		}
		h.assignLocked(message, waiter)
		break
	}
	return waiter, superseded
}

func (h *claudeCoordinationHub) cancelWaiter(waiter *claudeCoordinationWaiter, kind string) {
	if waiter == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waiter != waiter {
		return
	}
	h.waiter = nil
	waiter.result <- claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: kind, WaiterRef: waiter.ref}
}

func (h *claudeCoordinationHub) hasWaiter() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed && h.waiter != nil
}

func (h *claudeCoordinationHub) assignLocked(message *claudeCoordinationMessage, waiter *claudeCoordinationWaiter) {
	message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventBeginHandoff,
		MessageRef: message.envelope.MessageRef, WaiterRef: waiter.ref})
	message.process = waiter.process
	if h.waiter == waiter {
		h.waiter = nil
	}
	waiter.result <- claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "handoff", WaiterRef: waiter.ref,
		Envelope: &message.envelope, Delivery: message.delivery}
	window := h.receiptWindow
	go func(messageRef, waiterRef string) {
		timer := time.NewTimer(window)
		defer timer.Stop()
		<-timer.C
		h.expireHandoff(messageRef, waiterRef)
	}(message.envelope.MessageRef, waiter.ref)
}

func (h *claudeCoordinationHub) expireHandoff(messageRef, waiterRef string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tracked := h.messages[messageRef]
	if tracked == nil || tracked.delivery.WaiterRef != waiterRef {
		return
	}
	tracked.delivery, _ = agentdelivery.Reduce(tracked.delivery, agentdelivery.Event{Kind: agentdelivery.EventExpire,
		MessageRef: messageRef, WaiterRef: waiterRef})
}

func (h *claudeCoordinationHub) receipt(messageRef, waiterRef string, process coremetadata.ProcessIdentity, full bool) agentdelivery.Delivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	message := h.messages[messageRef]
	if message == nil {
		return agentdelivery.Delivery{MessageRef: messageRef, State: agentdelivery.StateStale, Reason: "unknown-message"}
	}
	if message.delivery.State != agentdelivery.StateHandoff || message.delivery.WaiterRef != waiterRef || message.process != process {
		return message.delivery
	}
	if !full {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventFail,
			MessageRef: messageRef, WaiterRef: waiterRef, Reason: "provider-pipe-write-failed"})
		return message.delivery
	}
	message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventDeliver,
		MessageRef: messageRef, WaiterRef: waiterRef, FullFrameWritten: true, HelperReceipt: true})
	return message.delivery
}

func (h *claudeCoordinationHub) status(messageRef string) agentdelivery.Delivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expirePendingLocked()
	if message := h.messages[messageRef]; message != nil {
		return message.delivery
	}
	return agentdelivery.Delivery{MessageRef: messageRef, State: agentdelivery.StateStale, Reason: "unknown-message"}
}

func (h *claudeCoordinationHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	if h.waiter != nil {
		h.waiter.result <- claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "stale", WaiterRef: h.waiter.ref}
		h.waiter = nil
	}
	for _, message := range h.messages {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventStale,
			MessageRef: message.envelope.MessageRef, Reason: "helper-restart"})
	}
}

func (h *claudeCoordinationHub) scheduleTTL(messageRef string, deadline time.Time) {
	delay := max(deadline.Sub(h.now()), 0)
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		h.mu.Lock()
		defer h.mu.Unlock()
		message := h.messages[messageRef]
		if message != nil {
			message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventExpire,
				MessageRef: messageRef, Reason: "ttl"})
		}
	}()
}

func (h *claudeCoordinationHub) expirePendingLocked() {
	now := h.now()
	for _, ref := range h.pending {
		message := h.messages[ref]
		if message != nil && !message.envelope.Deadline.After(now) {
			message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventExpire,
				MessageRef: ref, Reason: "ttl"})
		}
	}
}

func newCoordinationRef(prefix string) string {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		sum := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
		copy(data, sum[:])
	}
	return prefix + "-" + hex.EncodeToString(data)
}

func validCoordinationRef(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 160 && !strings.ContainsAny(value, "\r\n\x00")
}

type claudeCoordinationServer struct {
	listener *localipc.Listener
	hub      *claudeCoordinationHub
	route    coremetadata.AgentRouteRef
	current  func() bool
	done     chan struct{}
}

func startClaudeCoordinationServer(listener *localipc.Listener, route coremetadata.AgentRouteRef, current func() bool) *claudeCoordinationServer {
	server := &claudeCoordinationServer{listener: listener, hub: newClaudeCoordinationHub(), route: route, current: current, done: make(chan struct{})}
	go server.serve()
	return server
}

func (s *claudeCoordinationServer) serve() {
	defer close(s.done)
	defer s.hub.close()
	for {
		conn, err := s.listener.Unix.AcceptUnix()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *claudeCoordinationServer) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(localipc.Deadline))
	var request claudeCoordinationRequest
	if localipc.ReadJSON(conn, &request) != nil || request.Version != claudeCoordinationVersion || !request.Target.matches(s.route) || !s.current() {
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "stale"})
		return
	}
	peer, parent, err := localipc.PeerProcess(conn)
	if err != nil || peer.OwnerUID != uint32(os.Getuid()) {
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
		return
	}
	authority := request.Target.Authority
	switch request.Operation {
	case "probe":
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "ready"})
	case "waiter-ready":
		kind := "no-waiter"
		if s.hub.hasWaiter() {
			kind = "waiter-ready"
		}
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: kind})
	case "wait":
		if (request.HookEvent != "SessionStart" && request.HookEvent != "Stop") || request.SessionID != authority.SessionID || parent != authority.Process.PID {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
			return
		}
		actual, _, processErr := localipc.Process(parent)
		if processErr != nil || actual != authority.Process {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
			return
		}
		waiter, _ := s.hub.arm(peer)
		if waiter == nil {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "stale"})
			return
		}
		waitUntil := request.WaitUntil
		if waitUntil.IsZero() || waitUntil.After(time.Now().Add(claudeCoordinationHookTimeout)) {
			waitUntil = time.Now().Add(claudeCoordinationHookTimeout)
		}
		timer := time.NewTimer(time.Until(waitUntil))
		defer timer.Stop()
		select {
		case response := <-waiter.result:
			_ = conn.SetWriteDeadline(time.Now().Add(localipc.Deadline))
			_ = localipc.WriteJSON(conn, response)
		case <-timer.C:
			s.hub.cancelWaiter(waiter, "timeout")
			_ = localipc.WriteJSON(conn, <-waiter.result)
		}
	case "submit":
		if request.Envelope == nil || !request.Envelope.valid(time.Now(), s.route) {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
			return
		}
		delivery := s.hub.submit(*request.Envelope)
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: string(delivery.State), Delivery: delivery})
	case "receipt":
		if parent != authority.Process.PID || request.SessionID != authority.SessionID {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
			return
		}
		delivery := s.hub.receipt(request.MessageRef, request.WaiterRef, peer, request.FullFrameWritten)
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: string(delivery.State), Delivery: delivery})
	case "status":
		delivery := s.hub.status(request.MessageRef)
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: string(delivery.State), Delivery: delivery})
	default:
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
	}
}

func (s *claudeCoordinationServer) Close() {
	if s == nil {
		return
	}
	s.hub.close()
	_ = s.listener.Close()
	<-s.done
}

func callClaudeCoordination(ctx context.Context, registryPath string, route coremetadata.AgentRouteRef, request claudeCoordinationRequest) (claudeCoordinationResponse, error) {
	target, ok := claudeTargetForRoute(route)
	if !ok || request.Target != target {
		return claudeCoordinationResponse{}, errors.New("Claude coordination route is stale")
	}
	path := claudeCoordinationSocket(registryPath, target)
	dialer := net.Dialer{Timeout: localipc.DialTimeout}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return claudeCoordinationResponse{}, errors.New("Claude coordination helper is unavailable")
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return claudeCoordinationResponse{}, errors.New("Claude coordination helper is unavailable")
	}
	peer, _, err := localipc.PeerProcess(unixConnection)
	if err != nil || peer != target.Authority.LeaseProcess {
		return claudeCoordinationResponse{}, errors.New("Claude coordination helper peer is stale")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := localipc.WriteJSON(connection, request); err != nil {
		return claudeCoordinationResponse{}, errors.New("Claude coordination request failed")
	}
	_ = unixConnection.CloseWrite()
	var response claudeCoordinationResponse
	if err := localipc.ReadJSON(connection, &response); err != nil || response.Version != claudeCoordinationVersion {
		return claudeCoordinationResponse{}, errors.New("Claude coordination response failed")
	}
	return response, nil
}

func resolveCurrentClaudeCoordinationRoute(registryPath, paneUID, generation, sessionID string) (coremetadata.AgentRouteRef, bool) {
	if exactActivationRegistryPath(registryPath) != nil {
		return coremetadata.AgentRouteRef{}, false
	}
	reg, err := intmetadata.NewStore(registryPath).LoadDegradedReadOnly()
	if err != nil {
		return coremetadata.AgentRouteRef{}, false
	}
	pane, ok := reg.Pane(paneUID)
	if !ok || pane.Status.Activation.Generation != generation || pane.Status.Activation.AgentUID == "" {
		return coremetadata.AgentRouteRef{}, false
	}
	route, reason := coremetadata.ResolveAgentRoute(reg, pane.Status.Activation.AgentUID)
	authority, authorityOK := route.Authority().(coremetadata.ClaudeAuthorityRef)
	return route, reason == "" && authorityOK && route.PaneUID == paneUID && route.Generation == generation && authority.SessionID == sessionID
}

type claudeHookWakeError struct{}

func (claudeHookWakeError) Error() string { return "Claude coordination handoff complete" }
func (claudeHookWakeError) ExitCode() int { return 2 }

// runClaudeMessageWait is invoked only by the managed asyncRewake hook. It
// writes no diagnostics or migration output to the provider pipe. The payload
// remains an explicitly untrusted coordination record; a slash command has no
// special authority and is transferred as ordinary JSON string data.
func runClaudeMessageWait(args []string, stderr io.Writer) error {
	return runClaudeMessageWaitInput(args, os.Stdin, stderr, os.Getenv)
}

func runClaudeMessageWaitInput(args []string, stdin io.Reader, stderr io.Writer, getenv func(string) string) error {
	if len(args) != 0 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(stdin, localipc.MaxFrameBytes+1))
	if err != nil || len(data) > localipc.MaxFrameBytes {
		return nil
	}
	var hook struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(data, &hook) != nil || (hook.Event != "SessionStart" && hook.Event != "Stop") || !validCoordinationRef(hook.SessionID) {
		return nil
	}
	registryPath := getenv(internalClaudeRegistryPathEnv)
	paneUID := getenv(internalActivationPaneUIDEnv)
	generation := getenv(internalActivationGenerationEnv)
	deadline := time.Now().Add(5 * time.Second)
	var route coremetadata.AgentRouteRef
	for {
		var current bool
		if route, current = resolveCurrentClaudeCoordinationRoute(registryPath, paneUID, generation, hook.SessionID); current {
			break
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(claudeEndpointPollInterval)
	}
	target, ok := claudeTargetForRoute(route)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeCoordinationWaitTimeout)
	defer cancel()
	response, err := callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "wait", Target: target, HookEvent: hook.Event, SessionID: hook.SessionID, WaitUntil: time.Now().Add(claudeCoordinationWaitTimeout)})
	if err != nil || response.Kind != "handoff" || response.Envelope == nil || response.WaiterRef == "" ||
		!response.Envelope.valid(time.Now(), route) || response.Envelope.MessageRef != response.Delivery.MessageRef {
		return nil
	}
	payload, err := json.Marshal(response.Envelope)
	if err != nil || len(payload)+1 > claudeProviderFrameMaxBytes {
		return nil
	}
	payload = append(payload, '\n')
	fullWrite := writeFullProviderFrame(stderr, payload) == nil
	receiptCtx, receiptCancel := context.WithTimeout(context.Background(), localipc.Deadline)
	defer receiptCancel()
	receipt, err := callClaudeCoordination(receiptCtx, registryPath, route, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "receipt", Target: target, SessionID: hook.SessionID, MessageRef: response.Envelope.MessageRef,
		WaiterRef: response.WaiterRef, FullFrameWritten: fullWrite})
	if err != nil || !fullWrite || receipt.Delivery.MessageRef != response.Envelope.MessageRef || receipt.Delivery.State != agentdelivery.StateDelivered {
		return nil
	}
	if receipt.Delivery.WaiterRef != response.WaiterRef {
		return nil
	}
	return claudeHookWakeError{}
}

func writeFullProviderFrame(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := writer.Write(payload)
		if n > 0 {
			payload = payload[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
