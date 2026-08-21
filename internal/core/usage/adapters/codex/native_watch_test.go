package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestWatchNativeRateLimitsPublishesInitialAndSparseEventWithoutFallback(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	client := &fakeNativeClient{
		response: json.RawMessage(`{
			"rateLimits":{"limitId":"codex","limitName":"General","primary":{"usedPercent":11,"windowDurationMins":300,"resetsAt":1787380200}},
			"rateLimitsByLimitId":{"codex":{"limitId":"codex","limitName":"General","primary":{"usedPercent":11,"windowDurationMins":300,"resetsAt":1787380200}}}
		}`),
		events: make(chan codexappserver.Notification, 1),
	}
	adapter := NewWithRoot(t.TempDir())
	adapter.now = func() time.Time { return now }
	adapter.native = availableNative(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	published := make(chan []usage.Snapshot, 2)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- adapter.WatchNativeRateLimits(ctx, func(rows []usage.Snapshot) error {
			published <- append([]usage.Snapshot(nil), rows...)
			return nil
		})
	}()

	initial := receiveNativeWatchBatch(t, published)
	if len(initial) != 1 || initial[0].Pct != 11 || initial[0].Source != usage.SourceAppServer {
		t.Fatalf("initial batch = %#v", initial)
	}
	client.events <- codexappserver.Notification{
		Method: methodRateLimitsUpdated,
		Params: json.RawMessage(`{
			"rateLimits":{"limitName":null,"primary":{"usedPercent":73,"resetsAt":1787380999}}
		}`),
	}
	updated := receiveNativeWatchBatch(t, published)
	if len(updated) != 1 || updated[0].Pct != 73 ||
		updated[0].RateLimit == nil || updated[0].RateLimit.Label == nil ||
		*updated[0].RateLimit.Label != "General" ||
		updated[0].RateLimit.CadenceMinutes == nil || *updated[0].RateLimit.CadenceMinutes != 300 {
		t.Fatalf("sparse event batch = %#v, want inherited identity/cadence with updated percent", updated)
	}
	for _, row := range updated {
		if row.Source != usage.SourceAppServer || row.FallbackReason != "" || row.StaleReason != "" {
			t.Fatalf("watcher synthesized fallback/stale provenance: %#v", row)
		}
	}
	cancel()
	select {
	case err := <-watchDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WatchNativeRateLimits stop = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WatchNativeRateLimits did not stop after cancellation")
	}
	if len(client.methods) != 1 || client.methods[0] != methodRateLimitsRead {
		t.Fatalf("watcher methods = %#v, want one read and zero credential/config/token mutation", client.methods)
	}
}

func TestWatchNativeRateLimitsMalformedEventRetainsPriorBatch(t *testing.T) {
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	client := &fakeNativeClient{
		response: json.RawMessage(`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":11,"windowDurationMins":300,"resetsAt":1787380200}}}`),
		events:   make(chan codexappserver.Notification, 2),
	}
	adapter := NewWithRoot(t.TempDir())
	adapter.now = func() time.Time { return now }
	adapter.native = availableNative(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	published := make(chan []usage.Snapshot, 3)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- adapter.WatchNativeRateLimits(ctx, func(rows []usage.Snapshot) error {
			published <- append([]usage.Snapshot(nil), rows...)
			return nil
		})
	}()
	_ = receiveNativeWatchBatch(t, published)
	client.events <- codexappserver.Notification{
		Method: methodRateLimitsUpdated,
		Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":"malformed-private-value"}}}`),
	}
	select {
	case rows := <-published:
		t.Fatalf("malformed event published a replacement batch: %#v", rows)
	case <-time.After(30 * time.Millisecond):
	}
	client.events <- codexappserver.Notification{
		Method: methodRateLimitsUpdated,
		Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":44}}}`),
	}
	updated := receiveNativeWatchBatch(t, published)
	if len(updated) != 1 || updated[0].Pct != 44 {
		t.Fatalf("valid event after malformed = %#v", updated)
	}
	cancel()
	if err := <-watchDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("watch stop = %v", err)
	}
}

func receiveNativeWatchBatch(t *testing.T, batches <-chan []usage.Snapshot) []usage.Snapshot {
	t.Helper()
	select {
	case rows := <-batches:
		return rows
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for native watcher batch")
		return nil
	}
}
