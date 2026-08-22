package codex

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// WatchNativeRateLimits keeps one read-only native connection open and emits a
// complete normalized native batch after the initial read and every usable
// sparse rate-limit update. It never invokes the rollout fallback or any
// account/config mutation. The caller owns process lifetime through ctx.
func (a *Adapter) WatchNativeRateLimits(ctx context.Context, publish func([]usage.Snapshot) error) error {
	if a == nil || !a.native.enabled {
		return errors.New("codex native rate-limit watcher is disabled")
	}
	if publish == nil {
		return errors.New("codex native rate-limit watcher has no publisher")
	}
	health, err := a.native.ensure(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &usage.StaleReasonError{Reason: nativeReasonFromError(err), Err: errors.New("codex native watcher unavailable")}
	}
	if health.Availability != codexappserver.AvailabilityAvailable {
		return &usage.StaleReasonError{Reason: nativeReasonFromHealth(health), Err: errors.New("codex native watcher unavailable")}
	}
	client, err := a.native.open(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &usage.StaleReasonError{Reason: nativeReasonFromError(err), Err: errors.New("codex native watcher unavailable")}
	}
	defer client.Close()

	requestCtx, cancel := context.WithTimeout(ctx, nativeRequestTimeout)
	var response json.RawMessage
	err = client.Request(requestCtx, methodRateLimitsRead, json.RawMessage("null"), &response)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &usage.StaleReasonError{Reason: nativeReasonFromError(err), Err: errors.New("codex native watcher read failed")}
	}
	base := append(json.RawMessage(nil), response...)
	initial, valid := a.normalizeNativeWatchBatch(base)
	if !valid {
		return errors.New("codex native watcher received no valid rate-limit rows")
	}
	if err := publish(initial); err != nil {
		return err
	}

	events := client.Notifications()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return &usage.StaleReasonError{
					Reason: usage.ReasonAppServerDisconnected,
					Err:    errors.New("codex native watcher disconnected"),
				}
			}
			if event.Method != methodRateLimitsUpdated {
				continue
			}
			merged, reason := mergeRateLimitEvent(base, event.Params)
			if reason != "" {
				continue
			}
			next, valid := a.normalizeNativeWatchBatch(merged)
			if !valid {
				// A malformed sparse row is isolated just like Collect: retain the
				// prior durable batch and keep listening for a later valid update.
				continue
			}
			if err := publish(next); err != nil {
				return err
			}
			base = merged
		}
	}
}

func (a *Adapter) normalizeNativeWatchBatch(raw json.RawMessage) ([]usage.Snapshot, bool) {
	snapshots, _, hardFailure := normalizeNativeResponse(raw, a.now().UTC())
	if hardFailure || len(snapshots) == 0 {
		return nil, false
	}
	return snapshots, true
}
