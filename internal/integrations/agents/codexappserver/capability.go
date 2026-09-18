package codexappserver

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/mod/semver"

	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
)

const (
	// review/start is a stable v2 method in the oldest Codex version this Phase
	// validates. Older versions retain static launch and report review unavailable.
	minimumReviewVersion = "v0.149.0"
)

func reviewCapabilityForVersion(version string) corecap.ReviewCapability {
	match := versionPattern.FindStringSubmatch(version)
	if len(match) != 2 || !semver.IsValid("v"+match[1]) || semver.Compare("v"+match[1], minimumReviewVersion) < 0 {
		return corecap.ReviewCapability{Reason: "Codex app-server review/start is unavailable for this version"}
	}
	return corecap.ReviewCapability{Available: true}
}

// StartDefaultReview starts an inline review on one already exact-bound thread.
// Exact Agent/Pane binding is an app-layer precondition; this adapter owns only
// capability/version validation and wire translation.
func StartDefaultReview(ctx context.Context, projmuxVersion, threadID string, target corecap.ReviewTarget) (corecap.ReviewResult, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return corecap.ReviewResult{}, fmt.Errorf("%w: review thread is empty", corecap.ErrUnavailable)
	}
	health, err := EnsureDefaultProxyReady(ctx, TriggerNativeUserAction, projmuxVersion, true)
	if err != nil {
		return corecap.ReviewResult{}, err
	}
	if health.Source != SourceAppServer || health.Availability != AvailabilityAvailable || health.NativeAction == NativeActionRefused {
		return corecap.ReviewResult{}, unavailableHealthError(health)
	}
	client, version, err := openDefaultProxyClient(ctx, projmuxVersion)
	if err != nil {
		return corecap.ReviewResult{}, err
	}
	defer client.Close()
	if capability := reviewCapabilityForVersion(version); !capability.Available {
		return corecap.ReviewResult{}, fmt.Errorf("%w: %s", corecap.ErrUnavailable, capability.Reason)
	}
	wireTarget, err := reviewTargetParams(target)
	if err != nil {
		return corecap.ReviewResult{}, err
	}
	var result reviewStartResult
	if err := client.Request(ctx, methodReviewStart, reviewStartParams{ThreadID: threadID, Target: wireTarget}, &result); err != nil {
		if errors.Is(err, ErrUnsupported) {
			return corecap.ReviewResult{}, fmt.Errorf("%w: Codex app-server review/start is unsupported", corecap.ErrUnavailable)
		}
		return corecap.ReviewResult{}, err
	}
	projected := corecap.ReviewResult{
		ThreadID: strings.TrimSpace(result.ReviewThreadID),
		TurnID:   strings.TrimSpace(result.Turn.ID),
		Status:   normalizeReviewStatus(result.Turn.Status),
	}
	if projected.ThreadID == "" || projected.TurnID == "" || projected.Status == corecap.ReviewUnknown {
		return corecap.ReviewResult{}, fmt.Errorf("%w: review/start returned an incomplete or unknown initial turn", ErrProtocol)
	}
	return projected, nil
}

func unavailableHealthError(health Health) (err error) {
	defer func() { err = WithHealthDiagnostic(err, health) }()
	if guidance := health.NativeActionGuidance(); guidance != "" {
		return fmt.Errorf("%w: %s; %s", corecap.ErrUnavailable, health.LifecycleReason, guidance)
	}
	return fmt.Errorf("%w: %s", corecap.ErrUnavailable, health.LifecycleReason)
}

func reviewTargetParams(target corecap.ReviewTarget) (any, error) {
	value := strings.TrimSpace(target.Value)
	switch target.Kind {
	case corecap.ReviewUncommitted:
		return struct {
			Type string `json:"type"`
		}{Type: "uncommittedChanges"}, nil
	case corecap.ReviewBaseBranch:
		if value == "" {
			return nil, errors.New("review base branch is empty")
		}
		return struct {
			Type   string `json:"type"`
			Branch string `json:"branch"`
		}{Type: "baseBranch", Branch: value}, nil
	case corecap.ReviewCommit:
		if value == "" {
			return nil, errors.New("review commit is empty")
		}
		return struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		}{Type: "commit", SHA: value}, nil
	case corecap.ReviewCustom:
		if value == "" {
			return nil, errors.New("review instructions are empty")
		}
		return struct {
			Type         string `json:"type"`
			Instructions string `json:"instructions"`
		}{Type: "custom", Instructions: value}, nil
	default:
		return nil, fmt.Errorf("unsupported review target %q", target.Kind)
	}
}

func normalizeReviewStatus(status string) corecap.ReviewStatus {
	switch strings.TrimSpace(status) {
	case "inProgress":
		return corecap.ReviewInProgress
	case "completed":
		return corecap.ReviewCompleted
	case "failed":
		return corecap.ReviewFailed
	case "interrupted":
		return corecap.ReviewInterrupted
	default:
		return corecap.ReviewUnknown
	}
}

func openDefaultProxyClient(ctx context.Context, projmuxVersion string) (*Client, string, error) {
	return openDefaultProxyClientWith(ctx, projmuxVersion, false)
}

// openDefaultProxyClientWith opens the same proxy connection and negotiates the
// upstream experimental API when the caller requires a request that upstream
// only answers on a negotiated connection.
func openDefaultProxyClientWith(ctx context.Context, projmuxVersion string, experimental bool) (*Client, string, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return nil, "", fmt.Errorf("%w: Codex executable missing", corecap.ErrUnavailable)
	}
	// The caller's context bounds upgrade/initialize and every later request,
	// but it must not own the proxy process lifetime: a capability session keeps
	// this exact connection alive after the picker discovery context returns.
	// commandStream.Close remains the sole process-lifetime owner.
	cmd := exec.Command("codex", "app-server", "proxy")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, "", err
	}
	stream := &commandStream{stdin: stdin, stdout: stdout, cmd: cmd}
	websocket, err := upgradeProxyWebSocket(ctx, stream)
	if err != nil {
		_ = stream.Close()
		return nil, "", err
	}
	client := NewClient(websocket)
	initialize := client.Initialize
	if experimental {
		initialize = client.InitializeExperimental
	}
	version, err := initialize(ctx, projmuxVersion)
	if err != nil {
		_ = client.Close()
		return nil, "", err
	}
	return client, version, nil
}
