package codexappserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ThreadBinding is the content-free native identity returned for one launch.
// TurnID is empty when the caller intentionally resumes interactively without
// an initial prompt.
type ThreadBinding struct {
	ThreadID string
	TurnID   string
}

// ThreadActionError classifies whether the legacy CLI/hook lane can be used
// without risking a second conversation or duplicate prompt.
type ThreadActionError struct {
	Reason       string
	SafeFallback bool
	err          error
}

func (e *ThreadActionError) Error() string { return "Codex native thread action: " + e.Reason }
func (e *ThreadActionError) Unwrap() error { return e.err }

// CanFallback reports whether a failed native preparation mutated no provider
// conversation and can therefore safely use the current CLI/hook contract.
func CanFallback(err error) bool {
	var action *ThreadActionError
	return errors.As(err, &action) && action.SafeFallback
}

// StartDefaultThread creates one daemon-owned thread and sends exactly one
// turn/start. An empty prompt is not attachable native input, so it is
// classified as a safe fallback before opening or mutating the provider.
// requestKey is the caller's opaque activation generation and becomes Codex's
// client user-message id.
func StartDefaultThread(ctx context.Context, projmuxVersion, cwd string, roots []string, prompt, requestKey string) (ThreadBinding, error) {
	if prompt == "" {
		return ThreadBinding{}, &ThreadActionError{Reason: "empty-prompt", SafeFallback: true}
	}
	client, err := openReadyThreadClient(ctx, projmuxVersion)
	if err != nil {
		return ThreadBinding{}, err
	}
	defer client.Close()
	binding, err := client.StartThread(ctx, cwd, roots)
	if err != nil {
		return ThreadBinding{}, &ThreadActionError{
			Reason: "thread-start-failed", SafeFallback: errors.Is(err, ErrUnsupported), err: err,
		}
	}
	turnID, err := client.StartTurn(ctx, binding.ThreadID, prompt, requestKey)
	if err != nil {
		// thread/start already returned. Falling back to a fresh CLI launch here
		// would synthesize a second lane and might submit the prompt twice.
		return ThreadBinding{}, &ThreadActionError{Reason: "turn-start-indeterminate", err: err}
	}
	binding.TurnID = turnID
	return binding, nil
}

// ResumeDefaultThread loads exactly the stored thread. It never calls
// thread/start and never creates a new conversation.
func ResumeDefaultThread(ctx context.Context, projmuxVersion, cwd string, roots []string, threadID string) (ThreadBinding, error) {
	client, err := openReadyThreadClient(ctx, projmuxVersion)
	if err != nil {
		return ThreadBinding{}, err
	}
	defer client.Close()
	binding, err := client.ResumeThread(ctx, threadID, cwd, roots)
	if err != nil {
		return ThreadBinding{}, &ThreadActionError{Reason: "thread-resume-failed", SafeFallback: true, err: err}
	}
	return binding, nil
}

func openReadyThreadClient(ctx context.Context, projmuxVersion string) (*Client, error) {
	health, err := EnsureDefaultProxyReady(ctx, TriggerNativeUserAction, projmuxVersion, true)
	if err != nil {
		return nil, &ThreadActionError{Reason: "readiness-cancelled", SafeFallback: true, err: err}
	}
	if health.Source != SourceAppServer || health.Availability != AvailabilityAvailable {
		return nil, &ThreadActionError{Reason: string(health.LifecycleReason), SafeFallback: true, err: ErrDisconnected}
	}
	client, _, err := openDefaultProxyClient(ctx, projmuxVersion)
	if err != nil {
		return nil, &ThreadActionError{Reason: "proxy-open-failed", SafeFallback: true, err: err}
	}
	return client, nil
}

// StartThread performs the typed v2 thread/start request.
func (c *Client) StartThread(ctx context.Context, cwd string, roots []string) (ThreadBinding, error) {
	var result threadResult
	if err := c.Request(ctx, methodThreadStart, threadStartParams{
		CWD: strings.TrimSpace(cwd), RuntimeWorkspaceRoots: cleanRoots(roots),
	}, &result); err != nil {
		return ThreadBinding{}, err
	}
	threadID := strings.TrimSpace(result.Thread.ID)
	if threadID == "" {
		return ThreadBinding{}, fmt.Errorf("%w: thread/start returned no thread id", ErrProtocol)
	}
	return ThreadBinding{ThreadID: threadID}, nil
}

// ResumeThread performs the typed v2 thread/resume request and requires the
// provider to echo the exact requested thread.
func (c *Client) ResumeThread(ctx context.Context, threadID, cwd string, roots []string) (ThreadBinding, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ThreadBinding{}, fmt.Errorf("%w: resume thread is empty", ErrProtocol)
	}
	var result threadResult
	if err := c.Request(ctx, methodThreadResume, threadResumeParams{
		ThreadID: threadID, CWD: strings.TrimSpace(cwd), RuntimeWorkspaceRoots: cleanRoots(roots), ExcludeTurns: true,
	}, &result); err != nil {
		return ThreadBinding{}, err
	}
	returned := strings.TrimSpace(result.Thread.ID)
	if returned == "" || returned != threadID {
		return ThreadBinding{}, fmt.Errorf("%w: thread/resume returned a different thread", ErrProtocol)
	}
	return ThreadBinding{ThreadID: returned}, nil
}

// StartTurn submits one text input and returns the exact turn id. The prompt is
// never retained by the adapter.
func (c *Client) StartTurn(ctx context.Context, threadID, prompt, requestKey string) (string, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || prompt == "" {
		return "", fmt.Errorf("%w: turn/start requires thread and prompt", ErrProtocol)
	}
	var result turnStartResult
	if err := c.Request(ctx, methodTurnStart, turnStartParams{
		ThreadID:            threadID,
		Input:               []wireUserInput{{Type: "text", Text: prompt}},
		ClientUserMessageID: strings.TrimSpace(requestKey),
	}, &result); err != nil {
		return "", err
	}
	turnID := strings.TrimSpace(result.Turn.ID)
	if turnID == "" {
		return "", fmt.Errorf("%w: turn/start returned no turn id", ErrProtocol)
	}
	return turnID, nil
}

func cleanRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root = strings.TrimSpace(root); root != "" {
			out = append(out, root)
		}
	}
	return out
}
