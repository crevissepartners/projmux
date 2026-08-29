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
	Guidance     string
	SafeFallback bool
	err          error
}

func (e *ThreadActionError) Error() string {
	message := "Codex native thread action: " + e.Reason
	if e.Guidance != "" {
		message += "; " + e.Guidance
	}
	return message
}
func (e *ThreadActionError) Unwrap() error { return e.err }

// CanFallback reports whether a failed native preparation mutated no provider
// conversation and can therefore safely use the current CLI/hook contract.
func CanFallback(err error) bool {
	var action *ThreadActionError
	return errors.As(err, &action) && action.SafeFallback
}

// ReasonAdditionalRootsUnsupported is the typed classification of a create that
// carries additional writable roots to an endpoint that cannot negotiate the
// experimental API those roots travel on. It is deliberately not a safe
// fallback: the alternative is an Agent created with a narrower writable
// workspace than the operator asked for, which is a silent capability loss
// rather than a lane choice.
const ReasonAdditionalRootsUnsupported = "additional-writable-roots-unsupported"

// StartDefaultThread creates one daemon-owned thread and sends exactly one
// turn/start. An empty prompt is not attachable native input, so it is
// classified as a safe fallback before opening or mutating the provider.
// requestKey is the caller's opaque activation generation and becomes Codex's
// client user-message id.
//
// Additional writable roots are an experimental-only request field, so a create
// that carries them negotiates that capability on its own connection and
// delivers the exact cleaned list. A create with no roots keeps the plain
// connection: the common launch must not widen the wire surface it never uses.
// When the endpoint cannot answer the negotiated form, the create fails closed
// with ReasonAdditionalRootsUnsupported instead of quietly dropping the roots.
func StartDefaultThread(ctx context.Context, projmuxVersion, cwd string, roots []string, prompt, requestKey string) (ThreadBinding, error) {
	if prompt == "" {
		return ThreadBinding{}, &ThreadActionError{Reason: "empty-prompt", SafeFallback: true}
	}
	requestedRoots := cleanRoots(roots)
	negotiate := len(requestedRoots) > 0
	client, err := openReadyThreadClient(ctx, projmuxVersion, negotiate)
	if err != nil {
		// An endpoint that refuses the experimental handshake outright is the
		// unsupported-roots row, not the unavailable-endpoint row.
		if negotiate && errors.Is(err, ErrUnsupported) {
			return ThreadBinding{}, unsupportedRootsError(err)
		}
		return ThreadBinding{}, err
	}
	defer client.Close()
	if negotiate && !client.ExperimentalAPI() {
		return ThreadBinding{}, unsupportedRootsError(
			fmt.Errorf("%w: %w: additional writable roots", ErrUnsupported, ErrExperimentalRequired))
	}
	binding, err := client.StartThread(ctx, cwd, requestedRoots)
	if err != nil {
		if negotiate && errors.Is(err, ErrUnsupported) {
			return ThreadBinding{}, unsupportedRootsError(err)
		}
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

// unsupportedRootsError classifies a create that asked for additional writable
// roots against an endpoint that cannot carry them. It never reports a safe
// fallback, so no caller may answer it by launching the Agent anyway.
func unsupportedRootsError(err error) error {
	return &ThreadActionError{
		Reason: ReasonAdditionalRootsUnsupported,
		Guidance: "this Codex app-server endpoint does not negotiate the experimental API that carries additional writable roots; " +
			"update Codex or create the Agent without additional writable roots",
		err: err,
	}
}

// ResumeDefaultThread loads exactly the stored thread. It never calls
// thread/start and never creates a new conversation.
//
// It negotiates the upstream experimental API, because thread/resume always
// excludes turns and upstream answers `excludeTurns` only on a negotiated
// connection. Create negotiates the same capability only when it actually
// carries additional writable roots, so the common rootless create keeps the
// narrower plain connection while a rooted one delivers the exact list or
// fails closed.
func ResumeDefaultThread(ctx context.Context, projmuxVersion, cwd string, roots []string, threadID string) (ThreadBinding, error) {
	client, err := openReadyThreadClient(ctx, projmuxVersion, true)
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

// openReadyThreadClient opens the connection one native create or resume needs.
//
// The gate is endpoint attach authority, not the rendered native-action
// readiness: attaching to an endpoint that is already running is not process
// ownership, so a ready, protocol-compatible, exact-current endpoint is
// attachable whether or not the official daemon manager owns it. Every other
// row - a skew, an unknown running version, unknown ownership, an endpoint
// that is not ready - still refuses here, before the connection is opened and
// therefore before any provider conversation is mutated, and it still refuses
// as a safe fallback carrying the same typed reason it carried before.
//
// Daemon lifecycle authority is untouched by this: EnsureDefaultProxyReady
// only ever invokes the official idempotent start after an exact
// daemon-not-running probe, so an unmanaged endpoint this process may now
// attach to still receives zero lifecycle mutations.
func openReadyThreadClient(ctx context.Context, projmuxVersion string, experimental bool) (*Client, error) {
	health, err := EnsureDefaultProxyReady(ctx, TriggerNativeUserAction, projmuxVersion, true)
	if err != nil {
		return nil, &ThreadActionError{Reason: "readiness-cancelled", SafeFallback: true, err: err}
	}
	if health.Source != SourceAppServer || health.Availability != AvailabilityAvailable ||
		AuthorityFor(health).Attach != EndpointAttachAllowed {
		return nil, &ThreadActionError{Reason: string(health.LifecycleReason), Guidance: health.NativeActionGuidance(), SafeFallback: true, err: ErrDisconnected}
	}
	client, _, err := openDefaultProxyClientWith(ctx, projmuxVersion, experimental)
	if err != nil {
		return nil, &ThreadActionError{Reason: "proxy-open-failed", SafeFallback: true, err: err}
	}
	return client, nil
}

// StartThread performs the typed v2 thread/start request. Additional writable
// roots are an experimental-only field, so they are refused before the wire on
// a connection whose initialize did not negotiate that capability.
func (c *Client) StartThread(ctx context.Context, cwd string, roots []string) (ThreadBinding, error) {
	workspaceRoots, err := c.negotiatedRoots(roots)
	if err != nil {
		return ThreadBinding{}, err
	}
	var result threadResult
	if err := c.Request(ctx, methodThreadStart, threadStartParams{
		CWD: strings.TrimSpace(cwd), RuntimeWorkspaceRoots: workspaceRoots,
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
//
// It always excludes turns, and upstream refuses `thread/resume.excludeTurns`
// without the experimental API capability, so this request is available only
// on a connection whose initialize negotiated that capability. A connection
// that did not negotiate it is refused before the wire with the same typed
// unsupported error upstream would have produced after the round trip.
func (c *Client) ResumeThread(ctx context.Context, threadID, cwd string, roots []string) (ThreadBinding, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ThreadBinding{}, fmt.Errorf("%w: resume thread is empty", ErrProtocol)
	}
	if !c.ExperimentalAPI() {
		return ThreadBinding{}, fmt.Errorf("%w: %w: thread/resume excludeTurns", ErrUnsupported, ErrExperimentalRequired)
	}
	workspaceRoots, err := c.negotiatedRoots(roots)
	if err != nil {
		return ThreadBinding{}, err
	}
	var result threadResult
	if err := c.Request(ctx, methodThreadResume, threadResumeParams{
		ThreadID: threadID, CWD: strings.TrimSpace(cwd), RuntimeWorkspaceRoots: workspaceRoots, ExcludeTurns: true,
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

// negotiatedRoots returns the cleaned additional writable roots this
// connection may put on the wire. An empty list is always allowed; a non-empty
// one requires the negotiated experimental API capability and is otherwise a
// typed unsupported refusal with zero requests sent.
func (c *Client) negotiatedRoots(roots []string) ([]string, error) {
	cleaned := cleanRoots(roots)
	if len(cleaned) > 0 && !c.ExperimentalAPI() {
		return nil, fmt.Errorf("%w: %w: additional writable roots", ErrUnsupported, ErrExperimentalRequired)
	}
	return cleaned, nil
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
