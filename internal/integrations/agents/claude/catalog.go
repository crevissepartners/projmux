package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// These constants freeze the existing helper read protocol and probe bounds.
const (
	CoordinationVersion            = 5
	FrozenFrameProviderVersion     = "2.1.263"
	LeaseDialTimeout               = 200 * time.Millisecond
	LeaseReadinessReadTimeout      = 350 * time.Millisecond
	LeaseCoordinationProbeTimeout  = 300 * time.Millisecond
	CoordinationEligibilityTimeout = 200 * time.Millisecond
)

// RegistrationLease and CoordinationEligible only read Projmux's local helper.
// Neither opens a provider inbox nor submits any provider operation.
func RegistrationLease(path string, route coremetadata.AgentRouteRef) bool {
	return ClassifyRegistrationLease(path, route) == ProbeReady
}
func CoordinationEligible(path string, route coremetadata.AgentRouteRef) bool {
	return ClassifyCoordinationEligibility(path, route) == ProbeReady
}

// CoordinationRequest and CoordinationResponse define the helper wire schema.
// App-owned payload types stay parameters; read probes decode unused payloads as JSON.

type CoordinationRequest[Observation, Envelope, Qualification, ToolInput, ReplyEnvelope any] struct {
	Observation      *Observation       `json:"observation,omitempty"`
	Version          int                `json:"version"`
	Operation        string             `json:"operation"`
	Target           CoordinationTarget `json:"target"`
	SessionID        string             `json:"sessionId,omitempty"`
	Envelope         *Envelope          `json:"envelope,omitempty"`
	MessageRef       string             `json:"messageRef,omitempty"`
	AssistantMessage string             `json:"assistantMessage,omitempty"`
	ReplyEnvelope    *ReplyEnvelope     `json:"replyEnvelope,omitempty"`
	StopHookActive   bool               `json:"stopHookActive,omitempty"`
	Qualification    *Qualification     `json:"qualification,omitempty"`
	QualificationRef string             `json:"qualificationRef,omitempty"`
	ToolInput        *ToolInput         `json:"toolInput,omitempty"`
	ToolMarker       string             `json:"toolMarker,omitempty"`
	ExplicitOptIn    bool               `json:"explicitOptIn,omitempty"`
}

type CoordinationResponse[ToolEvidence, Qualification, ToolResult, Delivery, ReplyDelivery any] struct {
	ToolEvidence     []ToolEvidence `json:"toolEvidence,omitempty"`
	ProfileEvidence  *Qualification `json:"profileEvidence,omitempty"`
	Version          int            `json:"version"`
	Kind             string         `json:"kind"`
	Delivery         Delivery       `json:"delivery,omitzero"`
	ReplyRef         string         `json:"replyRef,omitempty"`
	ReplyCreated     bool           `json:"replyCreated,omitempty"`
	ReplyDelivery    *ReplyDelivery `json:"replyDelivery,omitempty"`
	Reason           string         `json:"reason,omitempty"`
	QualificationRef string         `json:"qualificationRef,omitempty"`
	ProviderVersion  string         `json:"providerVersion,omitempty"`
	Ambiguous        bool           `json:"ambiguous,omitempty"`
	ToolResult       *ToolResult    `json:"toolResult,omitempty"`
	AutoResend       bool           `json:"autoResend"`
}

type SocketIdentity struct {
	Device uint64
	Inode  uint64
	Owner  uint32
	Mode   os.FileMode
}

func InspectSocket(path string) (SocketIdentity, error) {
	refused := errors.New("claude socket is unavailable")
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return SocketIdentity{}, refused
	}
	var first SocketIdentity
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || (info.Mode()&os.ModeSymlink != 0 && !trustedDarwinTempAlias(current)) {
			return SocketIdentity{}, refused
		}
		if current == path {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || int64(stat.Uid) != int64(os.Getuid()) {
				return SocketIdentity{}, refused
			}
			first = SocketIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Owner: stat.Uid, Mode: info.Mode()}
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return SocketIdentity{}, refused
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || int64(stat.Uid) != int64(os.Getuid()) {
		return SocketIdentity{}, refused
	}
	last := SocketIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Owner: stat.Uid, Mode: info.Mode()}
	if last != first {
		return SocketIdentity{}, refused
	}
	return last, nil
}

func trustedDarwinTempAlias(path string) bool {
	if runtime.GOOS != "darwin" || (path != "/tmp" && path != "/var") {
		return false
	}
	target, err := os.Readlink(path)
	return err == nil && (target == "/private"+path || target == "private"+path)
}

// This is Projmux's own private readiness socket, never the provider address.
// It can be derived from nonsecret exact registration identity without storing
// either socket path in Registry or printing it in a capability projection.
func ActivationLeaseDir(registryPath, paneUID, generation string) string {
	digest := sha256.Sum256([]byte(registryPath + "\x00" + paneUID + "\x00" + generation))
	// Both supported operating systems provide /tmp. A fixed short root avoids
	// Darwin's long per-user TMPDIR exceeding sun_path and makes the creator,
	// hook, supervisor, and read-only client independent of inherited TMPDIR.
	return filepath.Join("/tmp", "pmx-ce-"+hex.EncodeToString(digest[:16]))
}

func LeaseSocket(registryPath, paneUID, generation, registrationGeneration string) string {
	digest := sha256.Sum256([]byte(registrationGeneration))
	return filepath.Join(ActivationLeaseDir(registryPath, paneUID, generation), hex.EncodeToString(digest[:16])+".sock")
}

type CoordinationTarget struct {
	AgentUID   string                          `json:"agentUID"`
	PaneUID    string                          `json:"paneUID"`
	Generation string                          `json:"generation"`
	Provider   string                          `json:"provider"`
	Authority  coremetadata.ClaudeAuthorityRef `json:"authority"`
}

func TargetForRoute(route coremetadata.AgentRouteRef) (CoordinationTarget, bool) {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || !authority.Valid() || !route.Same(route) {
		return CoordinationTarget{}, false
	}
	return CoordinationTarget{AgentUID: route.AgentUID, PaneUID: route.PaneUID, Generation: route.Generation,
		Provider: "claude", Authority: authority}, true
}

// CoordinationSocket is derived only from the exact stable activation
// and its typed Claude authority. Names, tmux runtime IDs, provider titles,
// vendor locators, and tokens never enter this address.
func CoordinationSocket(registryPath string, target CoordinationTarget) string {
	identity, _ := json.Marshal(target)
	digest := sha256.Sum256(identity)
	return ActivationLeaseDir(registryPath, target.PaneUID, target.Generation) + "/coord-" + hex.EncodeToString(digest[:12]) + ".sock"
}

func CallCoordination[O, E, Q, I, B, T, R, D, RD any](ctx context.Context, registryPath string, route coremetadata.AgentRouteRef, request CoordinationRequest[O, E, Q, I, B]) (CoordinationResponse[T, Q, R, D, RD], error) {
	target, ok := TargetForRoute(route)
	if !ok || request.Target != target {
		return CoordinationResponse[T, Q, R, D, RD]{}, errors.New("claude coordination route is stale")
	}
	path := CoordinationSocket(registryPath, target)
	dialer := net.Dialer{Timeout: localipc.DialTimeout}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return CoordinationResponse[T, Q, R, D, RD]{}, CoordinationCallError{}
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return CoordinationResponse[T, Q, R, D, RD]{}, CoordinationCallError{}
	}
	peer, _, err := localipc.PeerProcess(unixConnection)
	if err != nil || peer != target.Authority.LeaseProcess {
		return CoordinationResponse[T, Q, R, D, RD]{}, CoordinationCallError{}
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := localipc.WriteJSON(connection, request); err != nil {
		return CoordinationResponse[T, Q, R, D, RD]{}, CoordinationCallError{PossiblyDispatched: true}
	}
	_ = unixConnection.CloseWrite()
	var response CoordinationResponse[T, Q, R, D, RD]
	if err := localipc.ReadJSON(connection, &response); err != nil || response.Version != CoordinationVersion {
		return CoordinationResponse[T, Q, R, D, RD]{}, CoordinationCallError{PossiblyDispatched: true}
	}
	return response, nil
}

type ProbeOutcome uint8

const (
	ProbeReady ProbeOutcome = iota
	// ProbeStale: identity, socket, or peer checks failed, nothing listened,
	// the helper closed without answering, or it answered not current.
	ProbeStale
	// ProbeUnanswered: a live helper did not answer within a client bound.
	ProbeUnanswered
	// ProbeUnqualified: the helper answered that coordination is not qualified.
	ProbeUnqualified
)

// claudeProbeWaitOutcome classifies a failed bounded wait: only an expired bound
// is unanswered; EOF, refusal, and every other error are stale.
func claudeProbeWaitOutcome(err error) ProbeOutcome {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ProbeUnanswered
	}
	return ProbeStale
}

func ClassifyRegistrationLease(registryPath string, route coremetadata.AgentRouteRef) ProbeOutcome {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || !authority.Valid() {
		return ProbeStale
	}
	for _, expected := range []coremetadata.ProcessIdentity{authority.Process, authority.LeaseProcess} {
		actual, _, err := Process(expected.PID)
		if err != nil || actual != expected {
			return ProbeStale
		}
	}
	path := LeaseSocket(registryPath, route.PaneUID, route.Generation, authority.RegistrationGeneration)
	if _, err := InspectSocket(path); err != nil {
		return ProbeStale
	}
	// Dial only Projmux's readiness helper. Never connect to the provider inbox;
	// its secret path exists only in serveClaudeRegistration's private memory.
	connection, err := net.DialTimeout("unix", path, LeaseDialTimeout)
	if err != nil {
		return claudeProbeWaitOutcome(err)
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return ProbeStale
	}
	peer, err := PeerProcess(unixConnection)
	if err != nil || peer != authority.LeaseProcess {
		return ProbeStale
	}
	_ = connection.SetDeadline(time.Now().Add(LeaseReadinessReadTimeout))
	var ready [1]byte
	if _, err := io.ReadFull(connection, ready[:]); err != nil {
		return claudeProbeWaitOutcome(err)
	}
	if ready[0] != 1 {
		return ProbeStale
	}
	target, ok := TargetForRoute(route)
	if !ok {
		return ProbeStale
	}
	ctx, cancel := context.WithTimeout(context.Background(), LeaseCoordinationProbeTimeout)
	defer cancel()
	response, err := CallCoordination[json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage](ctx, registryPath, route, CoordinationRequest[json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage]{
		Version: CoordinationVersion, Operation: "probe", Target: target,
	})
	if err != nil {
		return claudeCoordinationCallOutcome(ctx)
	}
	if response.Kind != "ready" {
		return ProbeStale
	}
	return ProbeReady
}

// claudeCoordinationCallOutcome classifies a failed coordination call by its own
// bound: CallCoordination arms the connection with the context deadline.
func claudeCoordinationCallOutcome(ctx context.Context) ProbeOutcome {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ProbeUnanswered
	}
	return ProbeStale
}

func ClassifyCoordinationEligibility(registryPath string, route coremetadata.AgentRouteRef) ProbeOutcome {
	if lease := ClassifyRegistrationLease(registryPath, route); lease != ProbeReady {
		return lease
	}
	target, ok := TargetForRoute(route)
	if !ok {
		return ProbeStale
	}
	ctx, cancel := context.WithTimeout(context.Background(), CoordinationEligibilityTimeout)
	defer cancel()
	response, err := CallCoordination[json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage](ctx, registryPath, route, CoordinationRequest[json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage, json.RawMessage]{
		Version: CoordinationVersion, Operation: "eligibility", Target: target,
	})
	if err != nil {
		return claudeCoordinationCallOutcome(ctx)
	}
	if response.Kind == "stale" {
		return ProbeStale
	}
	if response.Kind == "qualified" && response.ProviderVersion == FrozenFrameProviderVersion &&
		response.Reason == "exact-public-init-and-explicit-reply" && !response.AutoResend && !response.Ambiguous {
		return ProbeReady
	}
	return ProbeUnqualified
}

type CoordinationCallError struct{ PossiblyDispatched bool }

func (CoordinationCallError) Error() string { return "claude coordination call failed" }
func (t CoordinationTarget) Matches(route coremetadata.AgentRouteRef) bool {
	expected, ok := TargetForRoute(route)
	return ok && t == expected
}
