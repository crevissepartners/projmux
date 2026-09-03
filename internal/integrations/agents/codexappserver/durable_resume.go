package codexappserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const defaultDurableResumeDeadline = 5 * time.Second

// DurableResumeOutcome is the closed, content-free result vocabulary for the
// independent second-client barrier between payload-free thread/start and a
// TUI launch.
type DurableResumeOutcome string

const (
	DurableResumeTimeout         DurableResumeOutcome = "deadline"
	DurableResumeThreadAbsent    DurableResumeOutcome = "thread-absent"
	DurableResumeConnectionClose DurableResumeOutcome = "connection-close"
	DurableResumeEndpointChanged DurableResumeOutcome = "endpoint-changed"
	DurableResumeProtocolRefusal DurableResumeOutcome = "protocol-refusal"
)

// DurableResumeError preserves the exact opaque thread identity and a bounded
// outcome only. It never retains an upstream response message, prompt, turn,
// path, or provider content.
type DurableResumeError struct {
	Outcome  DurableResumeOutcome
	ThreadID string
	Attempts int
	err      error
}

// NewDurableResumeError builds one typed failure without exposing the provider
// response that caused it.
func NewDurableResumeError(outcome DurableResumeOutcome, threadID string, attempts int, err error) error {
	return &DurableResumeError{
		Outcome: outcome, ThreadID: strings.TrimSpace(threadID), Attempts: attempts,
		err: contentFreeDurableResumeCause(err),
	}
}

func (e *DurableResumeError) Error() string {
	return fmt.Sprintf("Codex payload-free thread readiness: %s (thread %s, attempts %d)", e.Outcome, e.ThreadID, e.Attempts)
}

func (e *DurableResumeError) Unwrap() error { return e.err }

// DurableResumeClient is one fresh independent connection. BootstrapThread is
// the exact semantic predicate: stored thread/resume must subscribe to the
// requested identity before thread/read observes that same identity without
// turns.
type DurableResumeClient interface {
	BootstrapThread(context.Context, string, string, []string) (ThreadSnapshot, error)
	Close() error
}

// DurableResumeBarrier owns the only retry loop for a payload-free thread.
// Open must return a fresh independent client for every attempt. Wait is an
// injected adaptive backoff seam used by deterministic tests; production uses
// a context-aware timer and never polls a rollout file or its mtime.
type DurableResumeBarrier struct {
	Open func(context.Context) (DurableResumeClient, error)
	Wait func(context.Context, time.Duration) error
}

// Await blocks until an independent stored-resume followed by an exact
// content-free read succeeds, or until the bounded semantic deadline closes.
// It retries only the provider's explicit not-durable outcome. Absence,
// disconnect, and protocol refusal are terminal and cannot synthesize another
// thread, turn, or lane.
func (barrier DurableResumeBarrier) Await(ctx context.Context, threadID, cwd string, roots []string) (ThreadSnapshot, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || barrier.Open == nil {
		return ThreadSnapshot{}, NewDurableResumeError(DurableResumeProtocolRefusal, threadID, 0, ErrProtocol)
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, defaultDurableResumeDeadline)
	defer cancel()
	wait := barrier.Wait
	if wait == nil {
		wait = waitDurableResumeRetry
	}
	for attempt := 1; ; attempt++ {
		client, err := barrier.Open(deadlineCtx)
		if err == nil {
			var snapshot ThreadSnapshot
			snapshot, err = client.BootstrapThread(deadlineCtx, threadID, cwd, roots)
			// A default-route client owns a short-lived stdio proxy process. Reaping
			// that local proxy can return its exit status even after the exact
			// resume/read proof completed. Cleanup is mandatory, but its process
			// status is not a provider durability signal.
			_ = client.Close()
			if err == nil {
				if snapshot.ThreadID != threadID || snapshot.RuntimeStatus == "" {
					return ThreadSnapshot{}, NewDurableResumeError(DurableResumeProtocolRefusal, threadID, attempt, ErrProtocol)
				}
				return snapshot, nil
			}
		}
		if errors.Is(err, ErrThreadNotDurable) {
			if waitErr := wait(deadlineCtx, durableResumeRetryDelay(attempt)); waitErr == nil {
				continue
			}
			return ThreadSnapshot{}, NewDurableResumeError(DurableResumeTimeout, threadID, attempt, deadlineCtx.Err())
		}
		outcome := DurableResumeProtocolRefusal
		if errors.Is(err, ErrThreadAbsent) {
			outcome = DurableResumeThreadAbsent
		} else if errors.Is(err, ErrDisconnected) {
			outcome = DurableResumeConnectionClose
		} else if errors.Is(err, ErrEndpointChanged) {
			outcome = DurableResumeEndpointChanged
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			outcome = DurableResumeTimeout
		}
		return ThreadSnapshot{}, NewDurableResumeError(outcome, threadID, attempt, err)
	}
}

func contentFreeDurableResumeCause(err error) error {
	for _, safe := range []error{
		ErrThreadNotDurable, ErrThreadAbsent, ErrEndpointChanged, ErrDisconnected,
		ErrUnsupported, ErrExperimentalRequired, ErrProtocol, context.DeadlineExceeded, context.Canceled,
	} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if err != nil {
		return ErrProtocol
	}
	return nil
}

func durableResumeRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 5 * time.Millisecond
	for step := 1; step < attempt && delay < 250*time.Millisecond; step++ {
		delay *= 2
	}
	if delay > 250*time.Millisecond {
		return 250 * time.Millisecond
	}
	return delay
}

func waitDurableResumeRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
