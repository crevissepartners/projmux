package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"os"
	"path/filepath"
	"strings"
)

const NativeCodexReasonGenerationUnavailable = "generation-unavailable"

type NativeCodexRoute struct {
	Endpoint                  metadata.CodexEndpointRef
	State                     codexgeneration.GenerationState
	SocketPath, TUIExecutable string
	Default                   bool
}

func (route NativeCodexRoute) Valid() bool {
	socket := strings.TrimSpace(route.SocketPath)
	return route.Endpoint.Valid() && route.State != "" && filepath.IsAbs(strings.TrimSpace(route.TUIExecutable)) && ((route.Default && socket == "") || (!route.Default && filepath.IsAbs(socket)))
}

type NativeCodexRouteError struct {
	Reason, OperatorAction, SocketPath string
	Cause                              error
}

func (e *NativeCodexRouteError) Error() string {
	message := "Codex generation route: " + e.Reason
	if strings.TrimSpace(e.OperatorAction) != "" {
		message += "; operator action: " + e.OperatorAction
	}
	return message
}
func (e *NativeCodexRouteError) Unwrap() error { return e.Cause }

const NativeCodexDaemonStartGuidance = "Start the shared Codex app server with `codex app-server daemon start`, or run `codex app-server daemon restart` after upgrading Codex, then retry."
const NativeCodexDaemonOwnerAgreedGuidance = "Once the owner agrees, `codex app-server daemon restart` replaces an app server the official daemon manages."

func NativeCodexDaemonGuidance(health codexappserver.Health) string {
	if guidance := health.OperatorRecovery.Guidance(); guidance != "" {
		if health.OperatorRecovery == codexappserver.OperatorRecoveryInspectProcessOwnership {
			return guidance + " " + NativeCodexDaemonOwnerAgreedGuidance
		}
		return guidance
	}
	if health.EndpointReadiness != codexappserver.EndpointReady {
		return NativeCodexDaemonStartGuidance
	}
	return ""
}

// NativeCodexDaemonOperatorAction is the refusal's action for a default endpoint
// that was not attachable. A refusal always names a daemon step.
func NativeCodexDaemonOperatorAction(health codexappserver.Health) string {
	if guidance := NativeCodexDaemonGuidance(health); guidance != "" {
		return guidance
	}
	return NativeCodexDaemonStartGuidance
}

// CurrentNativeCodexRoute combines read-only readiness with the exact canonical provider root.
func CurrentNativeCodexRoute(ctx context.Context, probe func(context.Context) codexappserver.Health, lookPath func(string) (string, error), lookupEnv func(string) string, homeDir func() (string, error)) (NativeCodexRoute, error) {
	health := probe(ctx)
	if codexappserver.AuthorityFor(health).Attach != codexappserver.EndpointAttachAllowed {
		return NativeCodexRoute{}, codexappserver.WithHealthDiagnostic(&NativeCodexRouteError{
			Reason: NativeCodexReasonGenerationUnavailable, OperatorAction: NativeCodexDaemonOperatorAction(health),
		}, health)
	}
	runningVersion := strings.TrimSpace(health.RunningVersion)
	if !codexappserver.IsSafeDiagnosticVersion(runningVersion) {
		return NativeCodexRoute{}, &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	executable, err := lookPath("codex")
	if err != nil {
		return NativeCodexRoute{}, &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return NativeCodexRoute{}, &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(executable) {
		return NativeCodexRoute{}, &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	stateDomainID, err := NativeCodexStateDomainID(lookupEnv, homeDir)
	if err != nil {
		return NativeCodexRoute{}, &NativeCodexRouteError{Reason: NativeCodexReasonGenerationUnavailable}
	}
	return NativeCodexRoute{
		Endpoint: metadata.CodexEndpointRef{
			StateDomainID:        stateDomainID,
			EndpointGenerationID: "codex-" + runningVersion,
		},
		State: metadata.CodexGenerationCurrent, TUIExecutable: executable, Default: true,
	}, nil
}
func NativeCodexStateDomainID(lookupEnv func(string) string, homeDir func() (string, error)) (string, error) {
	_, id, err := NativeCodexStateDomain(lookupEnv, homeDir)
	return id, err
}

func NativeCodexStateDomain(lookupEnv func(string) string, homeDir func() (string, error)) (string, string, error) {
	root := strings.TrimSpace(lookupEnv("CODEX_HOME"))
	if root == "" {
		home, err := homeDir()
		if err != nil {
			return "", "", err
		}
		root = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(root) {
		return "", "", errors.New("codex state domain must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("codex state domain must be an existing directory")
	}
	sum := sha256.Sum256([]byte(canonical))
	return canonical, "codex-state-" + hex.EncodeToString(sum[:16]), nil
}
