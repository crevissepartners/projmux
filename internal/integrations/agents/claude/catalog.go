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

// The read-only subset of the helper wire protocol; other response fields are ignored.
type claudeCoordinationRequest struct {
	Version   int                      `json:"version"`
	Operation string                   `json:"operation"`
	Target    claudeCoordinationTarget `json:"target"`
}
type claudeCoordinationResponse struct {
	Version         int    `json:"version"`
	Kind            string `json:"kind"`
	Reason          string `json:"reason,omitempty"`
	ProviderVersion string `json:"providerVersion,omitempty"`
	Ambiguous       bool   `json:"ambiguous,omitempty"`
	AutoResend      bool   `json:"autoResend"`
}
type claudeSocketIdentity struct {
	device uint64
	inode  uint64
	owner  uint32
	mode   os.FileMode
}

func inspectClaudeSocket(path string) (claudeSocketIdentity, error) {
	refused := errors.New("claude socket is unavailable")
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return claudeSocketIdentity{}, refused
	}
	var first claudeSocketIdentity
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || (info.Mode()&os.ModeSymlink != 0 && !trustedDarwinTempAlias(current)) {
			return claudeSocketIdentity{}, refused
		}
		if current == path {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || int64(stat.Uid) != int64(os.Getuid()) {
				return claudeSocketIdentity{}, refused
			}
			first = claudeSocketIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: stat.Uid, mode: info.Mode()}
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return claudeSocketIdentity{}, refused
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || int64(stat.Uid) != int64(os.Getuid()) {
		return claudeSocketIdentity{}, refused
	}
	last := claudeSocketIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: stat.Uid, mode: info.Mode()}
	if last != first {
		return claudeSocketIdentity{}, refused
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
func claudeActivationLeaseDir(registryPath, paneUID, generation string) string {
	digest := sha256.Sum256([]byte(registryPath + "\x00" + paneUID + "\x00" + generation))
	// Both supported operating systems provide /tmp. A fixed short root avoids
	// Darwin's long per-user TMPDIR exceeding sun_path and makes the creator,
	// hook, supervisor, and read-only client independent of inherited TMPDIR.
	return filepath.Join("/tmp", "pmx-ce-"+hex.EncodeToString(digest[:16]))
}

func claudeLeaseSocket(registryPath, paneUID, generation, registrationGeneration string) string {
	digest := sha256.Sum256([]byte(registrationGeneration))
	return filepath.Join(claudeActivationLeaseDir(registryPath, paneUID, generation), hex.EncodeToString(digest[:16])+".sock")
}

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
		Provider: "claude", Authority: authority}, true
}

// claudeCoordinationSocket is derived only from the exact stable activation
// and its typed Claude authority. Names, tmux runtime IDs, provider titles,
// vendor locators, and tokens never enter this address.
func claudeCoordinationSocket(registryPath string, target claudeCoordinationTarget) string {
	identity, _ := json.Marshal(target)
	digest := sha256.Sum256(identity)
	return claudeActivationLeaseDir(registryPath, target.PaneUID, target.Generation) + "/coord-" + hex.EncodeToString(digest[:12]) + ".sock"
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
		return claudeCoordinationResponse{}, errors.New("claude coordination call failed")
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return claudeCoordinationResponse{}, errors.New("claude coordination call failed")
	}
	peer, _, err := localipc.PeerProcess(unixConnection)
	if err != nil || peer != target.Authority.LeaseProcess {
		return claudeCoordinationResponse{}, errors.New("claude coordination call failed")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := localipc.WriteJSON(connection, request); err != nil {
		return claudeCoordinationResponse{}, errors.New("claude coordination call failed")
	}
	_ = unixConnection.CloseWrite()
	var response claudeCoordinationResponse
	if err := localipc.ReadJSON(connection, &response); err != nil || response.Version != CoordinationVersion {
		return claudeCoordinationResponse{}, errors.New("claude coordination call failed")
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
	path := claudeLeaseSocket(registryPath, route.PaneUID, route.Generation, authority.RegistrationGeneration)
	if _, err := inspectClaudeSocket(path); err != nil {
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
	target, ok := claudeTargetForRoute(route)
	if !ok {
		return ProbeStale
	}
	ctx, cancel := context.WithTimeout(context.Background(), LeaseCoordinationProbeTimeout)
	defer cancel()
	response, err := callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{
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
// bound: callClaudeCoordination arms the connection with the context deadline.
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
	target, ok := claudeTargetForRoute(route)
	if !ok {
		return ProbeStale
	}
	ctx, cancel := context.WithTimeout(context.Background(), CoordinationEligibilityTimeout)
	defer cancel()
	response, err := callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{
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
