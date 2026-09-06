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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const (
	claudeCoordinationVersion            = 3
	claudeProviderFrameMaxBytes          = 8 << 10
	claudeCoordinationHookTimeout        = 5 * time.Second
	priorClaudeCoordinationManagedMarker = "projmux-managed:claude-coordination:v1"
	priorClaudeCoordinationV2Marker      = "projmux-managed:claude-coordination:v2"
	claudeCoordinationManagedMarker      = "projmux-managed:claude-coordination:v3"
	claudeCoordinationHookCommand        = "exec projmux internal claude-message-reply >/dev/null 2>&1 # " + claudeCoordinationManagedMarker
	claudeCoordinationBoundaryCommand    = "exec projmux internal claude-message-boundary >/dev/null 2>&1 # " + claudeCoordinationManagedMarker
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
	Version        int                      `json:"version"`
	MessageRef     string                   `json:"messageRef"`
	Target         claudeCoordinationTarget `json:"target"`
	Source         claudeCoordinationSource `json:"source"`
	Payload        string                   `json:"payload,omitempty"`
	Deadline       time.Time                `json:"deadline"`
	BrokerEnvelope *coremessage.Envelope    `json:"brokerEnvelope,omitempty"`
}

func (e claudeCoordinationEnvelope) valid(now time.Time, route coremetadata.AgentRouteRef) bool {
	if e.Version != claudeCoordinationVersion || !validCoordinationRef(e.MessageRef) || !e.Target.matches(route) ||
		e.Source.Kind != "peer" || e.Source.Trust != "untrusted" || e.Source.Authority != "coordination-only" ||
		e.Deadline.IsZero() {
		return false
	}
	if e.BrokerEnvelope != nil {
		broker := *e.BrokerEnvelope
		if broker.Validate() != nil || e.Payload != "" || broker.MessageRef != e.MessageRef ||
			!broker.Deadline.Equal(e.Deadline) || broker.Target.AgentUID != e.Target.AgentUID ||
			broker.Target.PaneUID != e.Target.PaneUID || broker.Target.ActivationGeneration != e.Target.Generation ||
			broker.Target.Provider != e.Target.Provider || broker.Target.Incarnation != route.Incarnation() ||
			broker.Authority != coremessage.PeerAuthority() {
			return false
		}
	} else if e.Payload == "" {
		return false
	}
	payload, err := json.Marshal(e)
	return err == nil && len(payload)+1 <= claudeProviderFrameMaxBytes && e.Deadline.After(now) &&
		!e.Deadline.After(now.Add(coremessage.MaxTTL))
}

type claudeCoordinationRequest struct {
	Version          int                          `json:"version"`
	Operation        string                       `json:"operation"`
	Target           claudeCoordinationTarget     `json:"target"`
	SessionID        string                       `json:"sessionId,omitempty"`
	Envelope         *claudeCoordinationEnvelope  `json:"envelope,omitempty"`
	MessageRef       string                       `json:"messageRef,omitempty"`
	AssistantMessage string                       `json:"assistantMessage,omitempty"`
	StopHookActive   bool                         `json:"stopHookActive,omitempty"`
	Qualification    *claudeQualificationEvidence `json:"qualification,omitempty"`
	QualificationRef string                       `json:"qualificationRef,omitempty"`
	ExplicitOptIn    bool                         `json:"explicitOptIn,omitempty"`
}

type claudeCoordinationResponse struct {
	Version          int                    `json:"version"`
	Kind             string                 `json:"kind"`
	Delivery         agentdelivery.Delivery `json:"delivery,omitzero"`
	ReplyRef         string                 `json:"replyRef,omitempty"`
	Reason           string                 `json:"reason,omitempty"`
	QualificationRef string                 `json:"qualificationRef,omitempty"`
	ProviderVersion  string                 `json:"providerVersion,omitempty"`
	Ambiguous        bool                   `json:"ambiguous,omitempty"`
	AutoResend       bool                   `json:"autoResend"`
}

type claudeCoordinationMessage struct {
	envelope          claudeCoordinationEnvelope
	delivery          agentdelivery.Delivery
	boundary          uint64
	dialogueReady     bool
	dialogueAmbiguous bool
	dialogueReason    string
	replyReserved     bool
	replyRef          string
}

type claudeCoordinationHub struct {
	mu                     sync.Mutex
	now                    func() time.Time
	messages               map[string]*claudeCoordinationMessage
	boundary               uint64
	humanTurnOpen          bool
	closed                 bool
	qualification          *claudeQualificationState
	qualifiedVersion       string
	replyCorrelationReason string
	boundaryAnnouncements  atomic.Uint64
	replyBoundaryLost      atomic.Bool
}

func newClaudeCoordinationHub() *claudeCoordinationHub {
	return &claudeCoordinationHub{now: time.Now, messages: make(map[string]*claudeCoordinationMessage)}
}

// userPrompt closes every previously open safe boundary before Claude starts a
// human-authored turn. It also makes all delivered-but-unreplied peer messages
// ineligible for Stop correlation: the following assistant text could belong
// to that user turn, and text matching is never authority.
func (h *claudeCoordinationHub) userPromptAt(announced uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.boundary = max(h.boundary, announced)
	h.humanTurnOpen = true
	h.closeQualificationForUserPromptLocked()
	for _, message := range h.messages {
		if message.dialogueReady && message.replyRef == "" {
			h.replyCorrelationReason = "concurrent-user-turn-ambiguous"
			message.dialogueAmbiguous = true
			message.dialogueReason = "concurrent-user-turn-ambiguous"
		}
	}
}

// reserveReply returns exactly one pending correlation. Zero, multiple,
// already-replied, or user-turn-ambiguous candidates fail closed without using
// assistant text as a selector.
func (h *claudeCoordinationHub) reserveReply(stopHookActive bool) (*coremessage.Envelope, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.replyBoundaryLost.Load() || h.boundaryAnnouncements.Load() != h.boundary {
		h.replyCorrelationReason = "concurrent-user-turn-ambiguous"
	}
	if h.replyCorrelationReason != "" {
		return nil, h.replyCorrelationReason
	}
	now := h.now()
	var candidates []*claudeCoordinationMessage
	ambiguousReason := ""
	for _, message := range h.messages {
		if message.replyRef != "" || message.replyReserved || message.envelope.BrokerEnvelope == nil {
			continue
		}
		if !message.envelope.Deadline.After(now) {
			message.dialogueReady = false
			message.dialogueAmbiguous = true
			message.dialogueReason = "reply-correlation-expired"
			if message.delivery.State == agentdelivery.StateDelivered {
				h.replyCorrelationReason = message.dialogueReason
			}
		}
		if message.dialogueAmbiguous {
			if ambiguousReason == "" {
				ambiguousReason = message.dialogueReason
				if ambiguousReason == "" {
					ambiguousReason = "concurrent-user-turn-ambiguous"
				}
			}
			continue
		}
		if !message.dialogueReady {
			continue
		}
		candidates = append(candidates, message)
	}
	if h.replyCorrelationReason != "" {
		return nil, h.replyCorrelationReason
	}
	if len(candidates) > 1 {
		h.replyCorrelationReason = "multiple-pending-correlations"
		for _, message := range candidates {
			message.dialogueAmbiguous = true
			message.dialogueReason = "multiple-pending-correlations"
		}
		return nil, "multiple-pending-correlations"
	}
	if len(candidates) == 0 {
		if ambiguousReason != "" {
			return nil, ambiguousReason
		}
		return nil, "no-pending-correlation"
	}
	candidate := candidates[0]
	// Push ingress is not a Stop asyncRewake. A recursive Stop cannot prove the
	// current assistant text belongs to this pending coordination message.
	if stopHookActive {
		candidate.dialogueAmbiguous = true
		candidate.dialogueReason = "stop-origin-mismatch"
		h.replyCorrelationReason = candidate.dialogueReason
		return nil, "stop-origin-mismatch"
	}
	candidate.replyReserved = true
	envelope := *candidate.envelope.BrokerEnvelope
	return &envelope, ""
}

func (h *claudeCoordinationHub) finishReply(messageRef, replyRef string, committed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	message := h.messages[messageRef]
	if message == nil || !message.replyReserved {
		return
	}
	message.replyReserved = false
	if committed {
		message.replyRef = replyRef
		return
	}
	message.dialogueReady = false
	message.dialogueAmbiguous = true
	message.dialogueReason = "broker-reply-outcome-unknown"
	h.replyCorrelationReason = message.dialogueReason
}

func (h *claudeCoordinationHub) status(messageRef string) agentdelivery.Delivery {
	h.mu.Lock()
	defer h.mu.Unlock()
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
	h.closeQualificationLocked()
	for _, message := range h.messages {
		message.delivery, _ = agentdelivery.Reduce(message.delivery, agentdelivery.Event{Kind: agentdelivery.EventStale,
			MessageRef: message.envelope.MessageRef, Reason: "helper-restart"})
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
	// Serialize reply commits without making official hooks wait for each other.
	// A concurrent boundary announces invalidation before trying this mutex.
	hookMu   sync.Mutex
	listener *localipc.Listener
	hub      *claudeCoordinationHub
	route    coremetadata.AgentRouteRef
	current  func() bool
	broker   claudeDialogueBroker
	poster   claudeProviderPoster
	done     chan struct{}
}

type claudeCoordinationCallError struct {
	possiblyDispatched bool
}

func (claudeCoordinationCallError) Error() string { return "claude coordination call failed" }

func claudeCoordinationCallPossiblyDispatched(err error) bool {
	var callErr claudeCoordinationCallError
	return errors.As(err, &callErr) && callErr.possiblyDispatched
}

type claudeDialogueBroker interface {
	Current(coremessage.Envelope) bool
	MarkHandoff(coremessage.Envelope) error
	MarkDelivered(coremessage.Envelope, time.Time) error
	Reply(coremessage.Envelope, coremetadata.AgentRouteRef, string, time.Time) (string, error)
}

type liveClaudeDialogueBroker struct {
	registryPath string
	store        *messagestore.Store
}

func newLiveClaudeDialogueBroker(registryPath string) (*liveClaudeDialogueBroker, error) {
	clean := filepath.Clean(registryPath)
	stateDir := filepath.Dir(filepath.Dir(clean))
	if registryPath == "" || intmetadata.PathFor(stateDir) != clean {
		return nil, errors.New("agent message registry path is not canonical")
	}
	return &liveClaudeDialogueBroker{registryPath: clean, store: messagestore.NewNonblockingStore(stateDir)}, nil
}

// Current proves both ends again at the helper boundary. Target socket and
// process incarnation are additionally checked by the provider poster. Claude
// sources must still own their live registration lease; Codex consumes its
// existing composite Registry authority without an app-server write.
func (b *liveClaudeDialogueBroker) Current(envelope coremessage.Envelope) bool {
	if b == nil || envelope.Validate() != nil {
		return false
	}
	registry, err := intmetadata.NewStore(b.registryPath).LoadDegradedReadOnly()
	if err != nil {
		return false
	}
	for _, expected := range []coremessage.Route{envelope.Source, envelope.Target} {
		route, reason := coremetadata.ResolveAgentRoute(registry, expected.AgentUID)
		if reason != "" || publicMessageRoute(route) != expected {
			return false
		}
		if authority, ok := route.Authority().(coremetadata.CodexRouteAuthority); ok {
			if !probeCodexMessageAuthority(filepath.Dir(filepath.Dir(b.registryPath)), authority) {
				return false
			}
		}
		if authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef); ok {
			for _, process := range []coremetadata.ProcessIdentity{authority.Process, authority.LeaseProcess} {
				actual, _, err := localipc.Process(process.PID)
				if err != nil || actual != process {
					return false
				}
			}
			// Avoid probing this helper recursively. Its poster owns the target
			// lease/socket check, including the source==target case.
			if expected == envelope.Source && expected != envelope.Target && !probeClaudeRegistrationLease(b.registryPath, route) {
				return false
			}
		}
	}
	return true
}

func probeCodexMessageAuthority(stateDir string, authority coremetadata.CodexRouteAuthority) bool {
	key, err := codexbroker.NewEndpointKey(authority.Authority.StateDomainID, authority.Authority.EndpointGenerationID)
	if err != nil {
		return false
	}
	discovery, err := codexbroker.NewDiscovery(stateDir, key)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	connection, err := codexbroker.Dial(ctx, discovery, codexbroker.DialConfig{Timeout: 200 * time.Millisecond})
	if err != nil {
		return false
	}
	defer connection.Close()
	return connection.CheckAuthority(ctx, authority.Authority.BrokerRuntimeID, authority.ThreadID,
		codexbroker.Fence{Connection: codexbroker.ConnectionEpoch(authority.Authority.ConnectionEpoch),
			Binding: codexbroker.BindingEpoch(authority.Authority.BindingEpoch)}) == nil
}

func (b *liveClaudeDialogueBroker) MarkHandoff(envelope coremessage.Envelope) error {
	if !b.Current(envelope) {
		return coremessage.ErrInvalidEnvelope
	}
	record, found, err := b.store.Get(envelope.MessageRef)
	if err != nil || !found || !record.Envelope.SameRetry(envelope) || record.Adapter != "claude-coordination" {
		if err != nil {
			return err
		}
		return coremessage.ErrInvalidEnvelope
	}
	_, _, err = b.store.MarkHandoff(envelope.MessageRef)
	return err
}

func (b *liveClaudeDialogueBroker) MarkDelivered(envelope coremessage.Envelope, observedAt time.Time) error {
	if envelope.Validate() != nil {
		return coremessage.ErrInvalidEnvelope
	}
	stored, found, err := b.store.Get(envelope.MessageRef)
	if err != nil {
		return err
	}
	if !found || stored.Adapter != "claude-coordination" || !stored.Envelope.SameRetry(envelope) {
		return coremessage.ErrInvalidEnvelope
	}
	record, _, err := b.store.Apply(envelope.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: observedAt.UTC()})
	if err != nil {
		return err
	}
	if record.Delivery.State != coremessage.StateDelivered {
		return coremessage.ErrInvalidEnvelope
	}
	return nil
}

func (b *liveClaudeDialogueBroker) Reply(original coremessage.Envelope, source coremetadata.AgentRouteRef, payload string, now time.Time) (string, error) {
	if b == nil || b.store == nil || original.Validate() != nil || !validClaudeAssistantReply(payload) {
		return "", coremessage.ErrInvalidEnvelope
	}
	registry, err := intmetadata.NewStore(b.registryPath).LoadDegradedReadOnly()
	if err != nil {
		return "", err
	}
	currentSource, reason := coremetadata.ResolveAgentRoute(registry, source.AgentUID)
	if reason != "" || !currentSource.Same(source) || publicMessageRoute(currentSource) != original.Target {
		return "", coremessage.ErrInvalidEnvelope
	}
	currentTarget, reason := coremetadata.ResolveAgentRoute(registry, original.Source.AgentUID)
	if reason != "" || publicMessageRoute(currentTarget) != original.Source {
		return "", coremessage.ErrInvalidEnvelope
	}
	digest := sha256.Sum256([]byte("claude-stop-reply-v1\x00" + original.MessageRef))
	replyRef := "reply-" + hex.EncodeToString(digest[:18])
	_, _, err = b.store.PutReply(original.MessageRef, replyRef, payload, publicMessageRoute(currentSource),
		publicMessageRoute(currentTarget), now.UTC(), now.UTC().Add(10*time.Minute))
	if err != nil {
		return "", err
	}
	return replyRef, nil
}

func validClaudeAssistantReply(value string) bool {
	return value != "" && len(value) <= coremessage.MaxPayloadBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func startClaudeCoordinationServerWithPoster(listener *localipc.Listener, route coremetadata.AgentRouteRef, current func() bool,
	broker claudeDialogueBroker, poster claudeProviderPoster,
) *claudeCoordinationServer {
	server := &claudeCoordinationServer{listener: listener, hub: newClaudeCoordinationHub(), route: route, current: current,
		broker: broker, poster: poster, done: make(chan struct{})}
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
	if err != nil || int64(peer.OwnerUID) != int64(os.Getuid()) {
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
		return
	}
	authority := request.Target.Authority
	switch request.Operation {
	case "probe":
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "ready"})
	case "eligibility":
		kind, reason := "unqualified", "exact-version-isolated-qualification-required"
		if s.hub.coordinationEligible() {
			kind, reason = "qualified", "exact-public-init-and-stop-marker"
		}
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: kind,
			ProviderVersion: claudeFrozenFrameProviderVersion, Reason: reason})
	case "qualify":
		if request.Qualification == nil || !request.ExplicitOptIn {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion,
				Kind: "qualification-refused", Reason: "missing-public-init-evidence"})
			return
		}
		_ = localipc.WriteJSON(conn, s.hub.beginQualification(*request.Qualification, s.route, s.poster))
	case "qualification-status":
		_ = localipc.WriteJSON(conn, s.hub.qualificationResponse(request.QualificationRef))
	case "submit":
		if request.Envelope == nil || !request.Envelope.valid(time.Now(), s.route) {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
			return
		}
		delivery := s.hub.submitPush(*request.Envelope, s.broker, s.poster)
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: string(delivery.State), Delivery: delivery})
	case "user-prompt":
		if parent != authority.Process.PID || request.SessionID != authority.SessionID {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "refused"})
			return
		}
		// Announce before any mutex: even a timed-out hook closes reply
		// correlation before the corresponding human turn may begin.
		announced := s.hub.boundaryAnnouncements.Add(1)
		if s.hookMu.TryLock() {
			s.hub.userPromptAt(announced)
			s.hookMu.Unlock()
		} else {
			s.hub.replyBoundaryLost.Store(true)
		}
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "boundary-closed"})
	case "stop-reply":
		if parent != authority.Process.PID || request.SessionID != authority.SessionID ||
			!validClaudeAssistantReply(request.AssistantMessage) {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "reply-refused", Reason: "invalid-stop-correlation"})
			return
		}
		if !s.hookMu.TryLock() {
			s.hub.replyBoundaryLost.Store(true)
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "reply-refused", Reason: "hook-busy"})
			return
		}
		defer s.hookMu.Unlock()
		if response, handled := s.hub.consumeQualificationStop(request.AssistantMessage, request.StopHookActive); handled {
			_ = localipc.WriteJSON(conn, response)
			return
		}
		if s.broker == nil {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "reply-refused", Reason: "invalid-stop-correlation"})
			return
		}
		original, reason := s.hub.reserveReply(request.StopHookActive)
		if original == nil {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "reply-refused", Reason: reason})
			return
		}
		replyRef, err := s.broker.Reply(*original, s.route, request.AssistantMessage, time.Now())
		s.hub.finishReply(original.MessageRef, replyRef, err == nil)
		if err != nil {
			_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "reply-refused", Reason: "broker-reply-refused"})
			return
		}
		_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "reply-accepted", ReplyRef: replyRef})
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
		return claudeCoordinationResponse{}, errors.New("claude coordination route is stale")
	}
	path := claudeCoordinationSocket(registryPath, target)
	dialer := net.Dialer{Timeout: localipc.DialTimeout}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return claudeCoordinationResponse{}, claudeCoordinationCallError{}
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return claudeCoordinationResponse{}, claudeCoordinationCallError{}
	}
	peer, _, err := localipc.PeerProcess(unixConnection)
	if err != nil || peer != target.Authority.LeaseProcess {
		return claudeCoordinationResponse{}, claudeCoordinationCallError{}
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := localipc.WriteJSON(connection, request); err != nil {
		return claudeCoordinationResponse{}, claudeCoordinationCallError{possiblyDispatched: true}
	}
	_ = unixConnection.CloseWrite()
	var response claudeCoordinationResponse
	if err := localipc.ReadJSON(connection, &response); err != nil || response.Version != claudeCoordinationVersion {
		return claudeCoordinationResponse{}, claudeCoordinationCallError{possiblyDispatched: true}
	}
	return response, nil
}

func resolveCurrentClaudeCoordinationRoute(registryPath, paneUID, generation, sessionID string) (coremetadata.AgentRouteRef, bool) {
	route, current := resolveCurrentClaudeCoordinationActivation(registryPath, paneUID, generation)
	if !current {
		return coremetadata.AgentRouteRef{}, false
	}
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	return route, ok && authority.SessionID == sessionID
}

func resolveCurrentClaudeCoordinationActivation(registryPath, paneUID, generation string) (coremetadata.AgentRouteRef, bool) {
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
	_, authorityOK := route.Authority().(coremetadata.ClaudeAuthorityRef)
	return route, reason == "" && authorityOK && route.PaneUID == paneUID && route.Generation == generation
}

// runClaudeMessageBoundary is the short UserPromptSubmit guard. Its authority
// is the exact official-hook parent plus the private activation envelope, not
// user-controlled hook JSON. It deliberately does not parse or buffer stdin,
// so an oversized or malformed human prompt still closes correlation before
// the provider starts that turn.
func runClaudeMessageBoundary(args []string) error {
	return runClaudeMessageBoundaryInput(args, os.Stdin, os.Getenv)
}

func runClaudeMessageBoundaryInput(args []string, _ io.Reader, getenv func(string) string) error {
	if len(args) != 0 {
		return nil
	}
	registryPath := getenv(internalClaudeRegistryPathEnv)
	paneUID := getenv(internalActivationPaneUIDEnv)
	generation := getenv(internalActivationGenerationEnv)
	route, current := resolveCurrentClaudeCoordinationActivation(registryPath, paneUID, generation)
	if !current {
		return nil
	}
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	target, targetOK := claudeTargetForRoute(route)
	if !ok || !targetOK {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
	defer cancel()
	_, _ = callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "user-prompt", Target: target, SessionID: authority.SessionID})
	return nil
}
