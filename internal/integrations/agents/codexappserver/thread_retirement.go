package codexappserver

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ThreadRetirementWire is the already captured provider connection. Retirement
// never opens, resumes or replaces an endpoint.
type ThreadRetirementWire interface {
	Request(context.Context, string, any, any) error
}

type ThreadUnsubscribeStatus string

const (
	ThreadUnsubscribed  ThreadUnsubscribeStatus = "unsubscribed"
	ThreadNotSubscribed ThreadUnsubscribeStatus = "notSubscribed"
	ThreadNotLoaded     ThreadUnsubscribeStatus = "notLoaded"
)

// UnsubscribeThread removes only this connection's exact thread subscription.
// The acknowledgement is not a writer-retirement receipt.
func UnsubscribeThread(ctx context.Context, wire ThreadRetirementWire, thread string) (ThreadUnsubscribeStatus, error) {
	if wire == nil || thread == "" || strings.TrimSpace(thread) != thread || len(thread) > 256 {
		return "", fmt.Errorf("%w: invalid retirement thread", ErrProtocol)
	}
	var result struct {
		Status ThreadUnsubscribeStatus `json:"status"`
	}
	err := wire.Request(ctx, "thread/unsubscribe", struct {
		ThreadID string `json:"threadId"`
	}{thread}, &result)
	if err != nil {
		return "", err
	}
	switch result.Status {
	case ThreadUnsubscribed, ThreadNotSubscribed, ThreadNotLoaded:
		return result.Status, nil
	}
	return "", fmt.Errorf("%w: invalid unsubscribe acknowledgement", ErrProtocol)
}

// AwaitThreadRetirement witnesses absence across the entire loaded catalog,
// under the caller's exact-connection guard. It never subscribes to the thread.
// Codex can delay unload while activity or another subscriber remains.
func AwaitThreadRetirement(ctx context.Context, wire ThreadRetirementWire, thread string, guard func() error) error {
	if wire == nil || thread == "" || strings.TrimSpace(thread) != thread || len(thread) > 256 || guard == nil {
		return fmt.Errorf("%w: invalid retirement barrier", ErrProtocol)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		present, err := retirementThreadLoaded(ctx, wire, thread, guard)
		if err != nil {
			return err
		}
		if !present {
			return guard()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func retirementThreadLoaded(ctx context.Context, wire ThreadRetirementWire, thread string, guard func() error) (bool, error) {
	var cursor *string
	seen := make(map[string]bool)
	found := false
	limit := uint32(100)
	for range 1024 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := guard(); err != nil {
			return false, err
		}
		var result threadLoadedListResult
		if err := wire.Request(ctx, methodThreadLoadedList, threadLoadedListParams{Cursor: cursor, Limit: &limit}, &result); err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := guard(); err != nil {
			return false, err
		}
		if result.Data == nil {
			return false, ErrProtocol
		}
		for _, id := range result.Data {
			if id == "" || strings.TrimSpace(id) != id {
				return false, ErrProtocol
			}
			found = found || id == thread
		}
		if result.NextCursor == nil {
			return found, nil
		}
		next := *result.NextCursor
		if next == "" || seen[next] {
			return false, fmt.Errorf("%w: invalid loaded pagination", ErrProtocol)
		}
		seen[next] = true
		cursor = result.NextCursor
	}
	return false, fmt.Errorf("%w: loaded pagination limit", ErrProtocol)
}
