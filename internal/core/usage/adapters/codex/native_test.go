package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

type fakeNativeClient struct {
	mu       sync.Mutex
	response json.RawMessage
	err      error
	events   chan codexappserver.Notification
	methods  []string
	params   []any
}

func (c *fakeNativeClient) Request(_ context.Context, method string, params, result any) error {
	c.mu.Lock()
	c.methods = append(c.methods, method)
	c.params = append(c.params, params)
	err := c.err
	response := append(json.RawMessage(nil), c.response...)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	target, ok := result.(*json.RawMessage)
	if !ok {
		return errors.New("unexpected result type")
	}
	*target = response
	return nil
}

func (c *fakeNativeClient) Notifications() <-chan codexappserver.Notification { return c.events }
func (c *fakeNativeClient) Close() error                                      { return nil }

func availableNative(client nativeClient) nativeTransport {
	return nativeTransport{
		enabled: true,
		ensure: func(context.Context) (codexappserver.Health, error) {
			return codexappserver.Decide(
				codexappserver.AvailabilityAvailable,
				codexappserver.ReasonNone,
				"codex-cli/0.149.0",
				codexappserver.EndpointStdioProxy,
				codexappserver.ConnectionReady,
				true,
			), nil
		},
		open: func(context.Context) (nativeClient, error) { return client, nil },
	}
}

func TestNativeRateLimitProjectionGoldens(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		golden  string
		project func() ([]usage.Snapshot, []string, bool)
	}{
		{
			name:   "buckets",
			golden: "native-buckets.golden.json",
			project: func() ([]usage.Snapshot, []string, bool) {
				return normalizeNativeResponse(json.RawMessage(`{
					"rateLimitsByLimitId":{
						"codex":{"limitId":"codex","limitName":"General","primary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1787380200}},
						"alternate":{"limitId":"alt","limitName":"Alternate","primary":{"usedPercent":44,"windowDurationMins":1440,"resetsAt":1787380300}}
					}
				}`), now)
			},
		},
		{
			name:   "malformed-row",
			golden: "native-malformed.golden.json",
			project: func() ([]usage.Snapshot, []string, bool) {
				return normalizeNativeResponse(json.RawMessage(`{
					"rateLimitsByLimitId":{
						"codex":{"limitId":"codex","limitName":"General","primary":{"usedPercent":"private-value","windowDurationMins":300,"resetsAt":1787380200},"secondary":{"usedPercent":7,"windowDurationMins":10080,"resetsAt":1787380300}}
					}
				}`), now)
			},
		},
		{
			name:   "sparse-event",
			golden: "native-event.golden.json",
			project: func() ([]usage.Snapshot, []string, bool) {
				base := json.RawMessage(`{"rateLimits":{"limitId":"codex","limitName":"General","primary":{"usedPercent":11,"windowDurationMins":300,"resetsAt":1787380200}}}`)
				merged, reason := mergeRateLimitEvent(base, json.RawMessage(`{"rateLimits":{"limitName":null,"primary":{"usedPercent":73,"resetsAt":1787380999}}}`))
				if reason != "" {
					t.Fatalf("mergeRateLimitEvent reason = %q", reason)
				}
				return normalizeNativeResponse(merged, now)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rows, warnings, hardFailure := test.project()
			if warnings == nil {
				warnings = []string{}
			}
			got, err := json.MarshalIndent(struct {
				Rows        []usage.Snapshot `json:"rows"`
				Warnings    []string         `json:"warnings"`
				HardFailure bool             `json:"hard_failure"`
			}{rows, warnings, hardFailure}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			want, err := os.ReadFile(filepath.Join("testdata", test.golden))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("%s mismatch\n--- got ---\n%s--- want ---\n%s", test.golden, got, want)
			}
		})
	}
}

func TestNormalizeNativeSingleBucketPreservesPercentResetLabelAndNullableFields(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{
		"rateLimits": {
			"limitId": "codex",
			"limitName": "Codex shared",
			"primary": {"usedPercent": 125, "windowDurationMins": 300, "resetsAt": 1787380200},
			"secondary": {"usedPercent": 9, "windowDurationMins": 10080, "resetsAt": null}
		},
		"rateLimitsByLimitId": null
	}`)
	rows, warnings, hard := normalizeNativeResponse(raw, now)
	if hard || len(warnings) != 0 || len(rows) != 2 {
		t.Fatalf("normalize = rows %#v warnings %v hard %v", rows, warnings, hard)
	}
	if rows[0].Window != usage.Window5h || rows[0].Pct != 125 || !rows[0].ResetsAt.Equal(time.Unix(1787380200, 0).UTC()) {
		t.Fatalf("primary = %#v, want lossless over-limit 5h row", rows[0])
	}
	if rows[1].Window != usage.WindowWeekly || rows[1].Pct != 9 || !rows[1].ResetsAt.IsZero() {
		t.Fatalf("secondary = %#v, want weekly row with absent reset", rows[1])
	}
	for _, row := range rows {
		if row.Source != usage.SourceAppServer || row.Bucket != "codex" || row.RateLimit == nil ||
			row.RateLimit.LimitID == nil || *row.RateLimit.LimitID != "codex" ||
			row.RateLimit.Label == nil || *row.RateLimit.Label != "Codex shared" {
			t.Fatalf("native identity/source lost: %#v", row)
		}
	}
}

func TestNormalizeNativeMultiBucketIsAuthoritativeAndKeepsUnknownCadence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{
		"rateLimits": {
			"limitId": "ignored-mirror",
			"limitName": "ignored",
			"primary": {"usedPercent": 99, "windowDurationMins": 300, "resetsAt": 1787380200}
		},
		"rateLimitsByLimitId": {
			"codex": {
				"limitId": null,
				"limitName": "General",
				"primary": {"usedPercent": 12, "windowDurationMins": 300, "resetsAt": 1787380201},
				"secondary": {"usedPercent": 8, "windowDurationMins": null, "resetsAt": null}
			},
			"alternate": {
				"limitId": "alternate-limit",
				"limitName": "Alternate quota",
				"primary": {"usedPercent": 44, "windowDurationMins": 1440, "resetsAt": 1787380300}
			}
		}
	}`)
	rows, warnings, hard := normalizeNativeResponse(raw, now)
	if hard || len(warnings) != 0 || len(rows) != 3 {
		t.Fatalf("normalize = rows %#v warnings %v hard %v", rows, warnings, hard)
	}
	for _, row := range rows {
		if row.Pct == 99 || row.RateLimit == nil {
			t.Fatalf("backward mirror was duplicated or metadata missing: %#v", row)
		}
	}
	unknown := map[string]usage.Snapshot{}
	for _, row := range rows {
		if row.Window == usage.WindowQuota {
			unknown[row.Bucket] = row
		}
	}
	if len(unknown) != 2 {
		t.Fatalf("unknown cadence rows = %#v, want missing and future cadence preserved", unknown)
	}
	missing := unknown["codex/secondary"]
	if missing.Pct != 8 || missing.RateLimit.CadenceMinutes != nil ||
		missing.RateLimit.Label == nil || *missing.RateLimit.Label != "General" {
		t.Fatalf("missing cadence row = %#v", missing)
	}
	future := unknown["alternate/primary"]
	if future.Pct != 44 || future.RateLimit.CadenceMinutes == nil || *future.RateLimit.CadenceMinutes != 1440 ||
		future.RateLimit.LimitID == nil || *future.RateLimit.LimitID != "alternate-limit" ||
		future.RateLimit.Label == nil || *future.RateLimit.Label != "Alternate quota" {
		t.Fatalf("unknown cadence row = %#v", future)
	}
	encoded, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	for _, nullable := range []string{`"limit_id":null`, `"cadence_minutes":null`} {
		if !strings.Contains(string(encoded), nullable) {
			t.Fatalf("missing nullable native metadata %s: %s", nullable, encoded)
		}
	}
}

func TestNormalizeNativeDropsOnlyMalformedRowsWithBoundedPrivateWarning(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{
		"rateLimits": {},
		"rateLimitsByLimitId": {
			"private-bucket-token": {
				"limitId": "private-bucket-token",
				"limitName": "private-label",
				"primary": {"usedPercent": "private-percent", "windowDurationMins": 300, "resetsAt": 1787380200},
				"secondary": {"usedPercent": 7, "windowDurationMins": 10080, "resetsAt": 1787380300}
			},
			"bad-reset": {
				"limitName": "private-reset-label",
				"primary": {"usedPercent": 4, "windowDurationMins": 300, "resetsAt": 0}
			},
			"bad-cadence": {
				"primary": {"usedPercent": 3, "windowDurationMins": -1, "resetsAt": 1787380400}
			},
			"zero-cadence": {
				"primary": {"usedPercent": 2, "windowDurationMins": 0, "resetsAt": 1787380400}
			},
			"negative-reset": {
				"primary": {"usedPercent": 1, "windowDurationMins": 300, "resetsAt": -1}
			},
			"negative-percent": {
				"primary": {"usedPercent": -1, "windowDurationMins": 300, "resetsAt": 1787380400}
			}
		}
	}`)
	rows, warnings, hard := normalizeNativeResponse(raw, time.Now())
	if hard || len(rows) != 1 || rows[0].Window != usage.WindowWeekly || rows[0].Pct != 7 {
		t.Fatalf("mixed normalization = rows %#v warnings %v hard %v", rows, warnings, hard)
	}
	err := usage.RowSkipWarning(warnings)
	if err == nil || !errors.Is(err, usage.ErrRowsSkipped) {
		t.Fatalf("warning = %v, want row-skip", err)
	}
	for _, secret := range []string{"private-bucket-token", "private-label", "private-percent", "178738"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("warning leaked %q: %v", secret, err)
		}
	}
}

func TestNativeEventUpdateFlowsThroughManagerStoreAndThrottle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	client := &fakeNativeClient{
		response: json.RawMessage(`{
			"rateLimits": {"limitId":"codex","limitName":"General","primary":{"usedPercent":11,"windowDurationMins":300,"resetsAt":1787380200}},
			"rateLimitsByLimitId":{"codex":{"limitId":"codex","limitName":"General","primary":{"usedPercent":11,"windowDurationMins":300,"resetsAt":1787380200}}}
		}`),
		events: make(chan codexappserver.Notification, 1),
	}
	go func() {
		time.Sleep(2 * time.Millisecond)
		client.events <- codexappserver.Notification{
			Method: methodRateLimitsUpdated,
			Params: json.RawMessage(`{"rateLimits":{"limitName":null,"primary":{"usedPercent":73,"windowDurationMins":300,"resetsAt":1787380999}}}`),
		}
	}()
	adapter := NewWithRoot(filepath.Join(t.TempDir(), "unused-rollout"))
	adapter.now = func() time.Time { return now }
	openCount := 0
	adapter.native = availableNative(client)
	originalOpen := adapter.native.open
	adapter.native.open = func(ctx context.Context) (nativeClient, error) {
		openCount++
		return originalOpen(ctx)
	}
	registry := usage.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	store := usage.NewStore(t.TempDir())
	manager := usage.NewManager(registry, store, func() time.Time { return now })
	collected, err := manager.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil || !collected {
		t.Fatalf("MaybeCollect = %v, %v", collected, err)
	}
	state, err := store.LoadState()
	if err != nil || len(state.Snapshots) != 1 {
		t.Fatalf("stored state = %#v, %v", state, err)
	}
	got := state.Snapshots[0]
	if got.Pct != 73 || got.Source != usage.SourceAppServer ||
		got.RateLimit == nil || got.RateLimit.Label == nil || *got.RateLimit.Label != "General" {
		t.Fatalf("event-refreshed snapshot = %#v", got)
	}
	now = now.Add(5 * time.Second)
	collected, err = manager.MaybeCollect(context.Background(), 30*time.Second)
	if err != nil || collected || openCount != 1 {
		t.Fatalf("throttled collect = %v, %v open=%d", collected, err, openCount)
	}
	if len(client.methods) != 1 || client.methods[0] != methodRateLimitsRead {
		t.Fatalf("native methods = %#v, want one read and no login/config/token calls", client.methods)
	}
	if raw, ok := client.params[0].(json.RawMessage); !ok || string(raw) != "null" {
		t.Fatalf("rate-limit params = %#v, want explicit null", client.params[0])
	}
}

func TestUnavailableAndUnsupportedUseOneRolloutLaneWithoutLogin(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name         string
		availability codexappserver.Availability
		reason       codexappserver.Reason
		wantReason   usage.SnapshotReason
	}{
		{"unavailable", codexappserver.AvailabilityUnavailable, codexappserver.ReasonDaemonNotRunning, usage.ReasonAppServerUnavailable},
		{"unsupported", codexappserver.AvailabilityUnsupported, codexappserver.ReasonUnsupported, usage.ReasonAppServerUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeRollout(t, root, "2026-08-22", rolloutWithRateLimits(5, 12, 1787380200, 1787380300), now)
			adapter := NewWithRoot(root)
			adapter.now = func() time.Time { return now }
			openCount := 0
			adapter.native = nativeTransport{
				enabled: true,
				ensure: func(context.Context) (codexappserver.Health, error) {
					return codexappserver.Decide(test.availability, test.reason, "", codexappserver.EndpointStdioProxy, codexappserver.ConnectionDisconnected, true), nil
				},
				open: func(context.Context) (nativeClient, error) {
					openCount++
					return nil, errors.New("must not open unavailable native transport")
				},
			}
			rows, err := adapter.Collect(context.Background())
			if err != nil || len(rows) != 2 || openCount != 0 {
				t.Fatalf("Collect = rows %#v err %v open=%d", rows, err, openCount)
			}
			for _, row := range rows {
				if row.Source != usage.SourceRollout || row.FallbackReason != test.wantReason || row.StaleReason != "" {
					t.Fatalf("fallback provenance = %#v", row)
				}
			}
		})
	}
}

func TestAPIKeyStyleEmptyAccountFallsBackWithoutAccountMutation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	root := t.TempDir()
	writeRollout(t, root, "2026-08-22", rolloutWithRateLimits(6, 13, 1787380200, 1787380300), now)
	client := &fakeNativeClient{
		response: json.RawMessage(`{"rateLimits":{},"rateLimitsByLimitId":{}}`),
		events:   make(chan codexappserver.Notification),
	}
	adapter := NewWithRoot(root)
	adapter.now = func() time.Time { return now }
	adapter.native = availableNative(client)
	rows, err := adapter.Collect(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("Collect = rows %#v err %v", rows, err)
	}
	for _, row := range rows {
		if row.Source != usage.SourceRollout || row.FallbackReason != usage.ReasonAccountUnsupported {
			t.Fatalf("API-key fallback row = %#v", row)
		}
	}
	if len(client.methods) != 1 || client.methods[0] != methodRateLimitsRead {
		t.Fatalf("methods = %#v, want rate-limit read only", client.methods)
	}
}

func TestTransportFailurePreservesLastKnownGoodWithClosedStaleReason(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	prior := usage.Snapshot{
		Model: "codex", Window: usage.Window5h, Pct: 21,
		Source: usage.SourceAppServer, UpdatedAt: now.Add(-time.Minute),
	}
	if err := store.SaveState(usage.State{Snapshots: []usage.Snapshot{prior}}); err != nil {
		t.Fatal(err)
	}
	adapter := NewWithRoot(filepath.Join(t.TempDir(), "missing-rollout"))
	adapter.now = func() time.Time { return now }
	adapter.native = nativeTransport{
		enabled: true,
		ensure: func(context.Context) (codexappserver.Health, error) {
			return codexappserver.Decide(codexappserver.AvailabilityAvailable, codexappserver.ReasonNone, "", codexappserver.EndpointStdioProxy, codexappserver.ConnectionReady, true), nil
		},
		open: func(context.Context) (nativeClient, error) {
			return nil, codexappserver.ErrDisconnected
		},
	}
	registry := usage.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	rows, err := usage.NewManager(registry, store, func() time.Time { return now }).Collect(context.Background())
	if err == nil || len(rows) != 1 {
		t.Fatalf("Collect = rows %#v err %v", rows, err)
	}
	if rows[0].Pct != prior.Pct || rows[0].Source != prior.Source ||
		rows[0].StaleReason != usage.ReasonAppServerDisconnected {
		t.Fatalf("last-known-good row = %#v", rows[0])
	}
}

func TestAllMalformedNativePreservesLastKnownGoodWithoutRolloutSynthesis(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	root := t.TempDir()
	writeRollout(t, root, "2026-08-22", rolloutWithRateLimits(99, 98, 1787380200, 1787380300), now)
	client := &fakeNativeClient{
		response: json.RawMessage(`{
			"rateLimitsByLimitId":{
				"codex":{"limitId":"codex","primary":{"usedPercent":"malformed-private-value","windowDurationMins":300,"resetsAt":1787380200}}
			}
		}`),
		events: make(chan codexappserver.Notification),
	}
	adapter := NewWithRoot(root)
	adapter.now = func() time.Time { return now }
	adapter.native = availableNative(client)
	store := usage.NewStore(t.TempDir())
	prior := usage.Snapshot{
		Model: Name, Window: usage.Window5h, Pct: 21,
		Source: usage.SourceAppServer, UpdatedAt: now.Add(-time.Minute),
	}
	if err := store.SaveState(usage.State{Snapshots: []usage.Snapshot{prior}}); err != nil {
		t.Fatal(err)
	}
	registry := usage.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	rows, err := usage.NewManager(registry, store, func() time.Time { return now }).Collect(context.Background())
	if err == nil || len(rows) != 1 {
		t.Fatalf("Collect = rows %#v err %v, want one last-known-good plus warning", rows, err)
	}
	if rows[0].Pct != 21 || rows[0].Source != usage.SourceAppServer ||
		rows[0].FallbackReason != "" || rows[0].StaleReason != usage.ReasonAppServerProtocol {
		t.Fatalf("malformed native result synthesized rollout or lost LKG: %#v", rows[0])
	}
	if len(client.methods) != 1 || client.methods[0] != methodRateLimitsRead {
		t.Fatalf("methods = %#v, want one read and zero credential/config/token mutation", client.methods)
	}
}
