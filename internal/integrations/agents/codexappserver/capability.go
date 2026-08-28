package codexappserver

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"

	"golang.org/x/mod/semver"

	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
)

const (
	maxModelPages = 32
	maxModels     = 1024
	// review/start is a stable v2 method in the oldest Codex version this Phase
	// validates. Older versions retain static launch and report review unavailable.
	minimumReviewVersion = "v0.149.0"
)

var capabilityConnectionSequence atomic.Uint64

// CapabilitySession owns the initialized app-server connection that produced a
// model snapshot. Callers keep it alive from picker render through pre-create
// validation; Refresh observes disconnects and model-set changes on that exact
// connection rather than trusting a detached cache entry.
type CapabilitySession struct {
	client   *Client
	epoch    corecap.Epoch
	snapshot corecap.Snapshot
}

// OpenDefaultCapabilitySession opens one live Phase 0 transport connection and
// discovers the initial model catalog on it.
func OpenDefaultCapabilitySession(ctx context.Context, projmuxVersion string) (*CapabilitySession, error) {
	health, err := EnsureDefaultProxyReady(ctx, TriggerNativeUserAction, projmuxVersion, true)
	if err != nil {
		return nil, err
	}
	if health.Source != SourceAppServer || health.Availability != AvailabilityAvailable || health.NativeAction == NativeActionRefused {
		return nil, unavailableHealthError(health)
	}
	client, version, err := openDefaultProxyClient(ctx, projmuxVersion)
	if err != nil {
		return nil, err
	}
	if capability := reviewCapabilityForVersion(version); !capability.Available {
		_ = client.Close()
		return nil, fmt.Errorf("%w: Codex app-server model capability discovery is unavailable for this version", corecap.ErrUnavailable)
	}
	session := &CapabilitySession{
		client: client,
		epoch: corecap.Epoch{
			Connection: fmt.Sprintf("connection-%d", capabilityConnectionSequence.Add(1)),
			Version:    version,
		},
	}
	snapshot, err := session.Refresh(ctx)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	session.snapshot = snapshot
	return session, nil
}

func (s *CapabilitySession) Snapshot() corecap.Snapshot {
	return s.snapshot.Clone()
}

// Refresh re-reads the whole paginated model catalog on the still-owned
// connection. Its epoch is unchanged only because it is the same initialized
// connection and negotiated version.
func (s *CapabilitySession) Refresh(ctx context.Context) (corecap.Snapshot, error) {
	if s == nil || s.client == nil || !s.epoch.Valid() {
		return corecap.Snapshot{}, fmt.Errorf("%w: capability connection is closed", corecap.ErrUnavailable)
	}
	snapshot, err := s.client.discoverCapabilities(ctx, s.epoch)
	if err != nil {
		return corecap.Snapshot{}, err
	}
	s.snapshot = snapshot.Clone()
	return snapshot, nil
}

func (s *CapabilitySession) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	client := s.client
	s.client = nil
	return client.Close()
}

func (c *Client) discoverCapabilities(ctx context.Context, epoch corecap.Epoch) (corecap.Snapshot, error) {
	var models []wireModel
	var cursor *string
	seenCursors := map[string]bool{}
	for range maxModelPages {
		var result modelListResult
		if err := c.Request(ctx, methodModelList, modelListParams{Cursor: cursor, IncludeHidden: false}, &result); err != nil {
			return corecap.Snapshot{}, err
		}
		models = append(models, result.Data...)
		if len(models) > maxModels {
			return corecap.Snapshot{}, fmt.Errorf("%w: model catalog exceeds bound", ErrProtocol)
		}
		if result.NextCursor == nil || strings.TrimSpace(*result.NextCursor) == "" {
			return normalizeCapabilitySnapshot(epoch, models), nil
		}
		next := strings.TrimSpace(*result.NextCursor)
		if seenCursors[next] {
			return corecap.Snapshot{}, fmt.Errorf("%w: model catalog cursor repeated", ErrProtocol)
		}
		seenCursors[next] = true
		cursor = &next
	}
	return corecap.Snapshot{}, fmt.Errorf("%w: model catalog pagination exceeds bound", ErrProtocol)
}

func normalizeCapabilitySnapshot(epoch corecap.Epoch, wireModels []wireModel) corecap.Snapshot {
	out := corecap.Snapshot{Epoch: epoch, Review: reviewCapabilityForVersion(epoch.Version)}
	seenModels := map[string]bool{}
	defaultSeen := false
	for _, raw := range wireModels {
		if raw.Hidden {
			continue
		}
		id := strings.TrimSpace(raw.ID)
		launchName := strings.TrimSpace(raw.Model)
		if launchName == "" {
			launchName = id
		}
		if id == "" || launchName == "" || seenModels[id] {
			continue
		}
		seenModels[id] = true
		model := corecap.Model{
			ID:                  id,
			LaunchName:          launchName,
			DisplayName:         strings.TrimSpace(raw.DisplayName),
			Description:         strings.TrimSpace(raw.Description),
			Default:             raw.Default && !defaultSeen,
			SupportsPersonality: raw.SupportsPersonality,
		}
		if model.DisplayName == "" {
			model.DisplayName = launchName
		}
		if model.Default {
			defaultSeen = true
		}
		for _, option := range raw.SupportedReasoningEfforts {
			appendUnique(&model.Efforts, option.Effort)
		}
		defaultEffort := strings.TrimSpace(raw.DefaultReasoningEffort)
		if slices.Contains(model.Efforts, defaultEffort) {
			model.DefaultEffort = defaultEffort
		}
		for _, modality := range raw.InputModalities {
			switch strings.TrimSpace(modality) {
			case "text", "image", "audio":
				appendUnique(&model.InputModalities, modality)
			}
		}
		out.Models = append(out.Models, model)
	}
	return out
}

func appendUnique(values *[]string, raw string) {
	value := strings.TrimSpace(raw)
	if value != "" && !slices.Contains(*values, value) {
		*values = append(*values, value)
	}
}

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

func unavailableHealthError(health Health) error {
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
