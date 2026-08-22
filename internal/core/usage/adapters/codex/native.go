package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/version"
)

const (
	methodRateLimitsRead    = "account/rateLimits/read"
	methodRateLimitsUpdated = "account/rateLimits/updated"
	nativeRequestTimeout    = 2 * time.Second
	nativeEventSettle       = 10 * time.Millisecond
)

type nativeClient interface {
	Request(context.Context, string, any, any) error
	Notifications() <-chan codexappserver.Notification
	Close() error
}

type nativeTransport struct {
	enabled bool
	ensure  func(context.Context) (codexappserver.Health, error)
	open    func(context.Context) (nativeClient, error)
}

func defaultNativeTransport() nativeTransport {
	return defaultNativeTransportForTrigger(codexappserver.TriggerNativeUserAction)
}

func defaultNativeTransportForTrigger(trigger codexappserver.TriggerKind) nativeTransport {
	return nativeTransport{
		enabled: true,
		ensure: func(ctx context.Context) (codexappserver.Health, error) {
			return codexappserver.EnsureDefaultProxyReady(ctx, trigger, version.String(), true)
		},
		open: func(ctx context.Context) (nativeClient, error) {
			return codexappserver.OpenDefaultProxy(ctx, codexappserver.DefaultProbeTimeout, version.String())
		},
	}
}

// Collect selects exactly one source for this invocation. Native rows win as
// soon as at least one valid native row exists; rollout is attempted once only
// for unavailable/unsupported/API-key-style empty-account outcomes. Malformed
// native rows never cause source synthesis: healthy siblings are returned with
// a bounded warning, and an all-malformed response leaves Manager to preserve
// last-known-good.
func (a *Adapter) Collect(ctx context.Context) ([]usage.Snapshot, error) {
	if !a.native.enabled {
		snaps, err := a.collectRollout(ctx)
		for i := range snaps {
			snaps[i].Source = usage.SourceRollout
			snaps[i].StaleReason = ""
		}
		return snaps, err
	}

	health, err := a.native.ensure(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return a.collectFallback(ctx, nativeReasonFromError(err))
	}
	if health.Availability != codexappserver.AvailabilityAvailable {
		return a.collectFallback(ctx, nativeReasonFromHealth(health))
	}

	client, err := a.native.open(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return a.collectFallback(ctx, nativeReasonFromError(err))
	}
	defer client.Close()

	requestCtx, cancel := context.WithTimeout(ctx, nativeRequestTimeout)
	defer cancel()
	var response json.RawMessage
	if err := client.Request(requestCtx, methodRateLimitsRead, json.RawMessage("null"), &response); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return a.collectFallback(ctx, nativeReasonFromError(err))
	}

	response, eventWarnings := mergeQueuedRateLimitEvents(response, client.Notifications())
	snaps, rowWarnings, hardFailure := normalizeNativeResponse(response, a.now().UTC())
	if hardFailure {
		return a.collectFallback(ctx, usage.ReasonAppServerProtocol)
	}
	warnings := append(eventWarnings, rowWarnings...)
	if len(snaps) > 0 {
		return snaps, usage.RowSkipWarning(warnings)
	}
	if len(warnings) > 0 {
		return nil, &usage.StaleReasonError{
			Reason: usage.ReasonAppServerProtocol,
			Err:    usage.RowSkipWarning(warnings),
		}
	}
	return a.collectFallback(ctx, usage.ReasonAccountUnsupported)
}

func (a *Adapter) collectFallback(ctx context.Context, reason usage.SnapshotReason) ([]usage.Snapshot, error) {
	snaps, err := a.collectRollout(ctx)
	for i := range snaps {
		snaps[i].Source = usage.SourceRollout
		snaps[i].FallbackReason = reason
		snaps[i].StaleReason = ""
	}
	if len(snaps) > 0 {
		return snaps, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, &usage.StaleReasonError{Reason: reason, Err: err}
	}
	return nil, &usage.StaleReasonError{
		Reason: reason,
		Err:    errors.New("native account rate limits and rollout fallback unavailable"),
	}
}

func nativeReasonFromHealth(health codexappserver.Health) usage.SnapshotReason {
	switch health.Availability {
	case codexappserver.AvailabilityUnsupported:
		return usage.ReasonAppServerUnsupported
	case codexappserver.AvailabilityTimeout:
		return usage.ReasonAppServerTimeout
	case codexappserver.AvailabilityProtocolError:
		return usage.ReasonAppServerProtocol
	default:
		return usage.ReasonAppServerUnavailable
	}
}

func nativeReasonFromError(err error) usage.SnapshotReason {
	switch {
	case errors.Is(err, codexappserver.ErrUnsupported):
		return usage.ReasonAppServerUnsupported
	case errors.Is(err, context.DeadlineExceeded):
		return usage.ReasonAppServerTimeout
	case errors.Is(err, codexappserver.ErrProtocol):
		return usage.ReasonAppServerProtocol
	case errors.Is(err, codexappserver.ErrDisconnected):
		return usage.ReasonAppServerDisconnected
	default:
		return usage.ReasonAppServerUnavailable
	}
}

func normalizeNativeResponse(raw json.RawMessage, now time.Time) ([]usage.Snapshot, []string, bool) {
	root, ok := rawObject(raw)
	if !ok {
		return nil, nil, true
	}
	var warnings []string
	if multiRaw, present := root["rateLimitsByLimitId"]; present && !isJSONNull(multiRaw) {
		buckets, ok := rawObject(multiRaw)
		if !ok {
			return nil, []string{"multi-bucket view: invalid object"}, false
		}
		keys := make([]string, 0, len(buckets))
		for key := range buckets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make([]usage.Snapshot, 0, len(keys)*2)
		for i, key := range keys {
			prefix := fmt.Sprintf("bucket %d", i+1)
			if key == "" {
				warnings = append(warnings, prefix+": missing map key")
				continue
			}
			rows, skipped := normalizeNativeBucket(buckets[key], key, prefix, now)
			out = append(out, rows...)
			warnings = append(warnings, skipped...)
		}
		return out, warnings, false
	}

	single, present := root["rateLimits"]
	if !present {
		return nil, []string{"single bucket: missing rateLimits"}, false
	}
	if isJSONNull(single) {
		return nil, nil, false
	}
	rows, skipped := normalizeNativeBucket(single, "", "bucket 1", now)
	return rows, skipped, false
}

func normalizeNativeBucket(raw json.RawMessage, bucketKey, warningPrefix string, now time.Time) ([]usage.Snapshot, []string) {
	object, ok := rawObject(raw)
	if !ok {
		return nil, []string{warningPrefix + ": invalid object"}
	}
	limitID, invalidID := nullableString(object, "limitId")
	label, invalidLabel := nullableString(object, "limitName")
	if invalidID || invalidLabel {
		field := "limitId"
		if invalidLabel {
			field = "limitName"
		}
		return nil, []string{warningPrefix + ": invalid " + field}
	}
	identity := bucketKey
	if identity == "" && limitID != nil {
		identity = *limitID
	}
	rows := make([]usage.Snapshot, 0, 2)
	var warnings []string
	for _, slot := range []string{"primary", "secondary"} {
		windowRaw, present := object[slot]
		if !present || isJSONNull(windowRaw) {
			continue
		}
		row, reason := normalizeNativeWindow(windowRaw, identity, bucketKey, limitID, label, slot, warningPrefix, now)
		if reason != "" {
			warnings = append(warnings, reason)
			continue
		}
		rows = append(rows, row)
	}
	return rows, warnings
}

func normalizeNativeWindow(raw json.RawMessage, identity, bucketKey string, limitID, label *string, slot, warningPrefix string, now time.Time) (usage.Snapshot, string) {
	object, ok := rawObject(raw)
	if !ok {
		return usage.Snapshot{}, warningPrefix + " " + slot + ": invalid row"
	}
	used, ok := requiredInt64(object, "usedPercent")
	// Percentages above 100 are valid over-limit evidence and are preserved;
	// the existing public renderer deliberately saturates only the bar, not
	// the numeric value. Negative values and values outside the wire int32 are
	// malformed.
	if !ok || used < 0 || used > math.MaxInt32 {
		return usage.Snapshot{}, warningPrefix + " " + slot + ": invalid usedPercent"
	}
	cadence, invalidCadence := nullableInt64(object, "windowDurationMins")
	if invalidCadence {
		return usage.Snapshot{}, warningPrefix + " " + slot + ": invalid windowDurationMins"
	}
	if cadence != nil && *cadence <= 0 {
		return usage.Snapshot{}, warningPrefix + " " + slot + ": invalid windowDurationMins"
	}
	reset, invalidReset := nullableInt64(object, "resetsAt")
	if invalidReset {
		return usage.Snapshot{}, warningPrefix + " " + slot + ": invalid resetsAt"
	}
	if reset != nil && *reset <= 0 {
		return usage.Snapshot{}, warningPrefix + " " + slot + ": invalid resetsAt"
	}
	window := usage.WindowQuota
	if cadence != nil {
		switch *cadence {
		case 300:
			window = usage.Window5h
		case 10080:
			window = usage.WindowWeekly
		}
	}
	bucket := identity
	if window == usage.WindowQuota {
		if bucket == "" {
			bucket = "native"
		}
		bucket += "/" + slot
	}
	row := usage.Snapshot{
		Model:     Name,
		Window:    window,
		Bucket:    bucket,
		Pct:       float64(used),
		UpdatedAt: now,
		Source:    usage.SourceAppServer,
		RateLimit: &usage.RateLimitMetadata{
			BucketKey:      bucketKey,
			LimitID:        cloneString(limitID),
			Label:          cloneString(label),
			Slot:           slot,
			CadenceMinutes: cloneInt64(cadence),
		},
	}
	if reset != nil {
		row.ResetsAt = time.Unix(*reset, 0).UTC()
	}
	return row, ""
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || isJSONNull(raw) {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func nullableString(object map[string]json.RawMessage, field string) (*string, bool) {
	raw, present := object[field]
	if !present || isJSONNull(raw) {
		return nil, false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, true
	}
	return &value, false
}

func requiredInt64(object map[string]json.RawMessage, field string) (int64, bool) {
	raw, present := object[field]
	if !present || isJSONNull(raw) {
		return 0, false
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func nullableInt64(object map[string]json.RawMessage, field string) (*int64, bool) {
	raw, present := object[field]
	if !present || isJSONNull(raw) {
		return nil, false
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return nil, true
	}
	return &value, false
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// mergeQueuedRateLimitEvents drains only notifications already delivered for
// this read. It never waits beyond the request and never invokes rollout. Each
// valid sparse event is merged into the backward-compatible native snapshot
// and, when present, its matching authoritative map bucket.
func mergeQueuedRateLimitEvents(response json.RawMessage, events <-chan codexappserver.Notification) (json.RawMessage, []string) {
	merged := append(json.RawMessage(nil), response...)
	var warnings []string
	index := 0
	settle := time.NewTimer(nativeEventSettle)
	defer settle.Stop()
	for events != nil {
		select {
		case event, ok := <-events:
			if !ok {
				return merged, warnings
			}
			if event.Method != methodRateLimitsUpdated {
				continue
			}
			index++
			var reason string
			merged, reason = mergeRateLimitEvent(merged, event.Params)
			if reason != "" {
				warnings = append(warnings, fmt.Sprintf("event %d: %s", index, reason))
			}
		case <-settle.C:
			return merged, warnings
		}
	}
	return merged, warnings
}

func mergeRateLimitEvent(response, params json.RawMessage) (json.RawMessage, string) {
	root, ok := rawObject(response)
	if !ok {
		return response, "invalid base response"
	}
	eventObject, ok := rawObject(params)
	if !ok {
		return response, "invalid params"
	}
	eventSnapshot, present := eventObject["rateLimits"]
	if !present || isJSONNull(eventSnapshot) {
		return response, "missing rateLimits"
	}
	eventBucket, ok := rawObject(eventSnapshot)
	if !ok {
		return response, "invalid rateLimits"
	}

	baseSingle, _ := rawObject(root["rateLimits"])
	mergedSingle := mergeSparseObject(baseSingle, eventBucket)
	encodedSingle, _ := json.Marshal(mergedSingle)
	root["rateLimits"] = encodedSingle

	if multiRaw, present := root["rateLimitsByLimitId"]; present && !isJSONNull(multiRaw) {
		multi, ok := rawObject(multiRaw)
		if !ok {
			return response, "invalid multi-bucket view"
		}
		targetID := stringField(eventBucket, "limitId")
		if targetID == "" {
			targetID = stringField(mergedSingle, "limitId")
		}
		if targetID != "" {
			targetKey := ""
			if _, exists := multi[targetID]; exists {
				targetKey = targetID
			} else {
				keys := make([]string, 0, len(multi))
				for key := range multi {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					bucket, _ := rawObject(multi[key])
					if stringField(bucket, "limitId") == targetID {
						targetKey = key
						break
					}
				}
			}
			if targetKey != "" {
				base, _ := rawObject(multi[targetKey])
				encoded, _ := json.Marshal(mergeSparseObject(base, eventBucket))
				multi[targetKey] = encoded
			}
		}
		encodedMulti, _ := json.Marshal(multi)
		root["rateLimitsByLimitId"] = encodedMulti
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return response, "merge failed"
	}
	return encoded, ""
}

func mergeSparseObject(base, update map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(base)+len(update))
	for key, value := range base {
		out[key] = append(json.RawMessage(nil), value...)
	}
	for key, value := range update {
		if isJSONNull(value) {
			continue
		}
		if key == "primary" || key == "secondary" {
			baseWindow, _ := rawObject(out[key])
			updateWindow, ok := rawObject(value)
			if ok {
				encoded, _ := json.Marshal(mergeSparseObject(baseWindow, updateWindow))
				out[key] = encoded
				continue
			}
		}
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func stringField(object map[string]json.RawMessage, field string) string {
	value, invalid := nullableString(object, field)
	if invalid || value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
