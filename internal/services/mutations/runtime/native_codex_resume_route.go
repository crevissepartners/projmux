package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	NativeCodexReasonLegacyEndpointMissing   = "legacy-generation-unavailable"
	NativeCodexReasonRetiredStateDomain      = "retired-generation-state-domain-mismatch"
	NativeCodexReasonRetiredRunning          = "retired-generation-running"
	nativeCodexResumeAgentPlaceholder        = "<agent>"
	nativeCodexRetiredGenerationProbeTimeout = 250 * time.Millisecond
)

func NativeCodexRetiredGenerationRefusal(reason, socketPath, agentRef string) *NativeCodexRouteError {
	resume := "`projmux agent resume " + agentRef + "`"
	if strings.TrimSpace(agentRef) == "" {
		// No Agent exists yet (a resume-picker row): name both retries.
		resume = "the resume-picker row, or `projmux agent resume " + nativeCodexResumeAgentPlaceholder + "` for an existing Agent"
	}
	switch reason {
	case NativeCodexReasonRetiredRunning:
		return &NativeCodexRouteError{Reason: reason, SocketPath: socketPath, OperatorAction: fmt.Sprintf(
			"a retired private Codex app-server generation is still listening on %s and may hold this thread; stop that Codex app-server process, then retry %s", socketPath, resume)}
	default:
		return &NativeCodexRouteError{Reason: NativeCodexReasonRetiredStateDomain, OperatorAction: "this Codex thread is stored under a different CODEX_HOME than the shared Codex app server serves; " +
			"set CODEX_HOME to that Codex home and run `codex app-server daemon restart`, then retry " + resume +
			", or start a new conversation with `projmux create agent --provider codex`"}
	}
}

func BindCodexResumeAgentRef(err error, agentRef string) error {
	var routeErr *NativeCodexRouteError
	if !errors.As(err, &routeErr) || (routeErr.Reason != NativeCodexReasonRetiredStateDomain && routeErr.Reason != NativeCodexReasonRetiredRunning) {
		return err
	}
	return NativeCodexRetiredGenerationRefusal(routeErr.Reason, routeErr.SocketPath, agentRef)
}

func ManagedCodexRuntimeLocation(stateDir, stateDomainID, targetVersion string) (string, string, error) {
	stateDir = filepath.Clean(strings.TrimSpace(stateDir))
	if !filepath.IsAbs(stateDir) || strings.TrimSpace(stateDomainID) == "" || !codexappserver.IsSafeDiagnosticVersion(targetVersion) {
		return "", "", errors.New("managed Codex runtime identity is invalid")
	}
	sum := sha256.Sum256([]byte(stateDomainID + "\x00" + targetVersion))
	privateRoot := filepath.Join(stateDir, "g", hex.EncodeToString(sum[:16]))
	socketPath := filepath.Join(privateRoot, "s")
	if len([]byte(socketPath)) > 100 {
		return privateRoot, socketPath, errors.New("managed Codex runtime socket exceeds the platform-safe bound")
	}
	return privateRoot, socketPath, nil
}

func DialCodexRetiredGenerationSocket(ctx context.Context, socketPath string) bool {
	if _, err := os.Lstat(socketPath); err != nil {
		return false
	}
	dialCtx, cancel := context.WithTimeout(ctx, nativeCodexRetiredGenerationProbeTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", socketPath)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func ValidateStoredCodexNativeResumeRoute(ref *metadata.AgentSessionRef) error {
	if ref == nil || ref.Codex == nil || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Valid() {
		return &NativeCodexRouteError{Reason: NativeCodexReasonLegacyEndpointMissing}
	}
	return ValidateCodexNativeResumeRoute(ref, NativeCodexRoute{Endpoint: *ref.Codex.Endpoint, State: metadata.CodexGenerationCurrent})
}

func ValidateCodexNativeResumeRoute(ref *metadata.AgentSessionRef, route NativeCodexRoute) error {
	if ref == nil || ref.Codex == nil || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Valid() {
		return &NativeCodexRouteError{Reason: NativeCodexReasonLegacyEndpointMissing}
	}
	endpoint := *ref.Codex.Endpoint
	lifecycle := ref.Codex.Lifecycle
	if lifecycle == nil || !lifecycle.ValidFor(&endpoint) || !route.Endpoint.Valid() {
		return &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	if route.Endpoint.StateDomainID != endpoint.StateDomainID {
		return NativeCodexRetiredGenerationRefusal(NativeCodexReasonRetiredStateDomain, "", "")
	}
	if !route.Endpoint.Same(endpoint) && !route.Default {
		return &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	switch lifecycle.State {
	case metadata.CodexGenerationCurrent, metadata.CodexGenerationDraining, metadata.CodexGenerationHandoverPending:
	default:
		return &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	if route.State != codexgeneration.StateCurrent {
		return &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	return nil
}
