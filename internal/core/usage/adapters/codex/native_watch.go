package codex

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// WatchNativeRateLimits keeps one read-only native connection open and emits a
// complete normalized native batch after the initial and periodic reads and
// every usable sparse rate-limit update. Notifications never postpone the
// same-connection read that detects a silent disconnect. It never invokes the
// rollout fallback or any account/config mutation. The caller owns process
// lifetime through ctx.
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

	var base json.RawMessage
	read := func() error {
		requestCtx, cancel := context.WithTimeout(ctx, nativeRequestTimeout)
		defer cancel()
		var response json.RawMessage
		err := client.Request(requestCtx, methodRateLimitsRead, json.RawMessage("null"), &response)
		// Cancellation owns the outcome even if a response became ready at
		// the same instant. Retired clients cannot publish a late batch.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return &usage.StaleReasonError{Reason: nativeReasonFromError(err), Err: errors.New("codex native watcher read failed")}
		}
		if err := requestCtx.Err(); err != nil {
			return &usage.StaleReasonError{Reason: nativeReasonFromError(err), Err: errors.New("codex native watcher read failed")}
		}
		rows, valid := a.normalizeNativeWatchBatch(response)
		if !valid {
			return &usage.StaleReasonError{Reason: usage.ReasonAppServerProtocol, Err: errors.New("codex native watcher received no valid rate-limit rows")}
		}
		if err := publish(rows); err != nil {
			return err
		}
		base = validNativeWatchBase(response, rows)
		return nil
	}
	if err := read(); err != nil {
		return err
	}

	ticker := time.NewTicker(nativeWatchReadEvery)
	defer ticker.Stop()
	events := client.Notifications()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Prefer a due read even when the notification queue stays busy. The
		// synchronous request also ensures at most one read is in flight.
		select {
		case <-ticker.C:
			if err := read(); err != nil {
				return err
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := read(); err != nil {
				return err
			}
		case event, ok := <-events:
			if ctx.Err() != nil {
				return ctx.Err()
			}
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
			base = validNativeWatchBase(merged, next)
		}
	}
}

// validNativeWatchBase retains only windows that normalized successfully, so
// a later sparse update cannot inherit fields from a rejected row. The native
// map remains authoritative; the compatible single bucket keeps its routing
// identity for updates that omit limitId.
func validNativeWatchBase(raw json.RawMessage, rows []usage.Snapshot) json.RawMessage {
	root, _ := rawObject(raw)
	prune := func(raw json.RawMessage, rows []usage.Snapshot) json.RawMessage {
		bucket, _ := rawObject(raw)
		kept := make(map[string]json.RawMessage)
		for _, name := range []string{"limitId", "limitName"} {
			if value, ok := bucket[name]; ok {
				kept[name] = value
			}
		}
		for _, row := range rows {
			kept[row.RateLimit.Slot] = bucket[row.RateLimit.Slot]
		}
		encoded, _ := json.Marshal(kept)
		return encoded
	}
	if multi, ok := rawObject(root["rateLimitsByLimitId"]); ok {
		byBucket := make(map[string][]usage.Snapshot)
		for _, row := range rows {
			key := row.RateLimit.BucketKey
			byBucket[key] = append(byBucket[key], row)
		}
		kept := make(map[string]json.RawMessage)
		for key, rows := range byBucket {
			kept[key] = prune(multi[key], rows)
		}
		root["rateLimitsByLimitId"], _ = json.Marshal(kept)
		rows, _ = normalizeNativeBucket(root["rateLimits"], "", "", time.Time{})
	}
	root["rateLimits"] = prune(root["rateLimits"], rows)
	encoded, _ := json.Marshal(root)
	return encoded
}

func (a *Adapter) normalizeNativeWatchBatch(raw json.RawMessage) ([]usage.Snapshot, bool) {
	snapshots, _, hardFailure := normalizeNativeResponse(raw, a.now().UTC())
	if hardFailure || len(snapshots) == 0 {
		return nil, false
	}
	return snapshots, true
}
