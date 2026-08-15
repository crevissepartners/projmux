package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

// writeCreds writes a minimal `~/.claude/.credentials.json`-shaped file
// at path. Returns the path so tests can also stat / read it back.
func writeCreds(t *testing.T, path, access, refresh string) {
	t.Helper()
	body := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      access,
			"refreshToken":     refresh,
			"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
			"rateLimitTier":    "standard",
			"scopes":           []string{"user:profile"},
			"subscriptionType": "max",
		},
	}
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
}

func TestAdapterCollectHappyPath(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
			t.Errorf("Authorization = %q, want Bearer access-1", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got == "" {
			t.Errorf("missing Anthropic-Version")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"five_hour": {"utilization": 9.0, "resets_at": "2026-05-06T17:00:00Z"},
			"seven_day": {"utilization": 12.5, "resets_at": "2026-05-13T00:00:00Z"}
		}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-1", "refresh-1")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	a.now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }

	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	if snaps[0].Window != usage.Window5h || snaps[0].Pct != 9.0 {
		t.Fatalf("snaps[0] = %+v, want 5h/9.0", snaps[0])
	}
	if snaps[1].Window != usage.WindowWeekly || snaps[1].Pct != 12.5 {
		t.Fatalf("snaps[1] = %+v, want weekly/12.5", snaps[1])
	}
	wantReset := time.Date(2026, 5, 6, 17, 0, 0, 0, time.UTC)
	if !snaps[0].ResetsAt.Equal(wantReset) {
		t.Fatalf("ResetsAt = %v, want %v", snaps[0].ResetsAt, wantReset)
	}
	if snaps[0].Tokens != 0 || snaps[0].Limit != 0 {
		t.Fatalf("Tokens/Limit should be 0 for percent-only adapter, got %d/%d", snaps[0].Tokens, snaps[0].Limit)
	}
}

func TestParseUsageResponseSyntheticNamedLimitShapes(t *testing.T) {
	t.Parallel()

	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	body := []byte(`{
		"five_hour":{"utilization":11,"resets_at":"2031-02-03T09:00:00Z"},
		"seven_day":{"utilization":22,"resets_at":"2031-02-09T00:00:00Z"},
		"limits":[
			{"kind":"kind-redacted","group":"group-redacted-all","percent":33,"severity":"severity-redacted","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":null},
			{"kind":"kind-redacted","group":"group-redacted-model","percent":44.5,"severity":"severity-redacted","resets_at":"2031-02-05T00:00:00Z","is_active":true,"scope":{"model":{"id":null,"display_name":"Model Redacted Alpha"},"surface":null}},
			{"kind":"kind-redacted-2","group":"group-redacted-surface","percent":55,"severity":"severity-redacted-2","resets_at":"2031-02-06T00:00:00Z","is_active":true,"scope":{"model":{"id":"model-redacted-id","display_name":"Model Redacted Beta"},"surface":"surface-redacted"},"opaque_future":{"ignored":true}}
		],
		"seven_day_model_hint":null,
		"opaque_experiment":{"future":true},
		"extra_usage":{"enabled":true},
		"spend":{"amount":"redacted"}
	}`)
	parsed, err := parseUsageResponse(body)
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	snaps := parsed.toSnapshots(now)
	if len(snaps) != 5 {
		t.Fatalf("snapshots = %#v, want aggregate pair + 3 typed limits", snaps)
	}
	if snaps[0].Window != usage.Window5h || snaps[1].Window != usage.WindowWeekly {
		t.Fatalf("aggregate contract changed: %#v", snaps[:2])
	}
	all := snaps[2]
	if all.Window != usage.WindowQuota || all.Bucket != "group-redacted-all" || all.Pct != 33 || all.NamedQuota == nil {
		t.Fatalf("scope-null limit = %#v", all)
	}
	if all.NamedQuota.Scope != nil || all.NamedQuota.IsActive {
		t.Fatalf("scope/null or explicit false lost: %#v", all.NamedQuota)
	}
	model := snaps[3]
	if model.NamedQuota.Scope == nil || model.NamedQuota.Scope.Model == nil {
		t.Fatalf("model scope missing: %#v", model)
	}
	if model.NamedQuota.Scope.Model.ID != nil || model.NamedQuota.Scope.Model.DisplayName != "Model Redacted Alpha" || model.NamedQuota.Scope.Surface != nil {
		t.Fatalf("nullable model scope changed: %#v", model.NamedQuota.Scope)
	}
	surface := snaps[4]
	if surface.NamedQuota.Kind != "kind-redacted-2" || surface.NamedQuota.Group != surface.Bucket || surface.NamedQuota.Severity != "severity-redacted-2" || !surface.NamedQuota.IsActive {
		t.Fatalf("typed metadata changed: %#v", surface.NamedQuota)
	}
	if surface.NamedQuota.Scope.Model.ID == nil || *surface.NamedQuota.Scope.Model.ID != "model-redacted-id" || surface.NamedQuota.Scope.Surface == nil || *surface.NamedQuota.Scope.Surface != "surface-redacted" {
		t.Fatalf("non-null scope identity changed: %#v", surface.NamedQuota.Scope)
	}
	if surface.Tokens != 0 || surface.Limit != 0 {
		t.Fatalf("percent-only limit synthesized counts: %#v", surface)
	}
}

func TestParseUsageResponseAggregateOnlyAndNullHintsDoNotCreateQuota(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{name: "aggregate only", body: `{"five_hour":{"utilization":1,"resets_at":null},"seven_day":{"utilization":2,"resets_at":null}}`},
		{name: "null limits and hints", body: `{"five_hour":{"utilization":1,"resets_at":null},"seven_day":{"utilization":2,"resets_at":null},"limits":null,"seven_day_opus":null,"seven_day_sonnet":null,"opaque_future":null}`},
		{name: "empty limits", body: `{"five_hour":{"utilization":1,"resets_at":null},"seven_day":{"utilization":2,"resets_at":null},"limits":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseUsageResponse([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if snaps := parsed.toSnapshots(time.Time{}); len(snaps) != 2 {
				t.Fatalf("snapshots = %#v, want aggregate pair only", snaps)
			}
		})
	}
}

// TestParseUsageLimitsShapeFailuresStillAbort pins the two hard failures that
// survive row-level tolerance: they describe the payload's SHAPE, not one
// row's contents, so there is nothing partial to salvage.
func TestParseUsageLimitsShapeFailuresStillAbort(t *testing.T) {
	t.Parallel()

	valid := `{"kind":"k","group":"g","percent":0,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":null}`
	if parsed, err := parseUsageResponse([]byte(`{"five_hour":{"utilization":7,"resets_at":null},"limits":{}}`)); err == nil {
		t.Fatalf("parseUsageResponse = %#v, want non-array limits error", parsed)
	}
	tooMany := `[` + strings.TrimSuffix(strings.Repeat(valid+`,`, maxUsageLimits+1), `,`) + `]`
	if _, err := parseUsageResponse([]byte(`{"five_hour":{"utilization":7,"resets_at":null},"limits":` + tooMany + `}`)); err == nil || !strings.Contains(err.Error(), "limit is 64") {
		t.Fatalf("row cap error = %v", err)
	}
}

// TestParseUsageLimitsSkipsMalformedRowIndividually is the row-level-skip
// table: one bad row is dropped with a bounded reason and the OTHER rows in
// the same array still reach the snapshot. Each case surrounds the defective
// row with two healthy rows so the surviving-content assertion is real.
func TestParseUsageLimitsSkipsMalformedRowIndividually(t *testing.T) {
	t.Parallel()

	healthy := func(group string) string {
		return `{"kind":"k","group":"` + group + `","percent":11,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":true,"scope":null}`
	}
	cases := []struct {
		name   string
		row    string
		reason string
	}{
		{"wrong percent type", `{"kind":"k","group":"bad","percent":"0","severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":null}`, "row 1: invalid row object"},
		{"missing kind", `{"group":"bad","percent":0,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":null}`, "row 1: missing kind"},
		{"missing group", `{"kind":"k","percent":0,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":null}`, "row 1: missing group"},
		{"missing percent", `{"kind":"k","group":"bad","severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":null}`, "row 1: missing percent"},
		{"missing severity", `{"kind":"k","group":"bad","percent":0,"resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":null}`, "row 1: missing severity"},
		{"missing resets_at", `{"kind":"k","group":"bad","percent":0,"severity":"s","is_active":false,"scope":null}`, "row 1: missing resets_at"},
		{"invalid resets_at", `{"kind":"k","group":"bad","percent":0,"severity":"s","resets_at":"not-a-time","is_active":false,"scope":null}`, "row 1: invalid resets_at"},
		{"missing is_active", `{"kind":"k","group":"bad","percent":0,"severity":"s","resets_at":"2031-02-04T00:00:00Z","scope":null}`, "row 1: missing is_active"},
		{"missing scope", `{"kind":"k","group":"bad","percent":0,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false}`, "row 1: scope: missing field"},
		{"model id wrong type", `{"kind":"k","group":"bad","percent":0,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":{"model":{"id":7,"display_name":"M"},"surface":null}}`, "row 1: scope: model: id: expected string or null"},
		{"surface wrong type", `{"kind":"k","group":"bad","percent":0,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":{"model":{"id":null,"display_name":"M"},"surface":true}}`, "row 1: scope: surface: expected string or null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"five_hour":{"utilization":7,"resets_at":null},"limits":[` +
				healthy("keep-first") + `,` + tc.row + `,` + healthy("keep-last") + `]}`
			parsed, err := parseUsageResponse([]byte(body))
			if err != nil {
				t.Fatalf("parseUsageResponse = %v, want row-level tolerance", err)
			}
			if len(parsed.Limits) != 2 {
				t.Fatalf("surviving limits = %#v, want the two healthy rows", parsed.Limits)
			}
			if parsed.Limits[0].Group != "keep-first" || parsed.Limits[1].Group != "keep-last" {
				t.Fatalf("surviving rows are not the healthy pair: %#v", parsed.Limits)
			}
			if len(parsed.SkipReasons) != 1 || parsed.SkipReasons[0] != tc.reason {
				t.Fatalf("skip reasons = %#v, want [%q]", parsed.SkipReasons, tc.reason)
			}
			// Negative assertion: the dropped row is absent, never
			// substituted with a synthesized identity or value.
			snaps := parsed.toSnapshots(time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC))
			for _, s := range snaps {
				if s.Bucket == "bad" {
					t.Fatalf("skipped row was fabricated into a snapshot: %#v", s)
				}
			}
			if len(snaps) != 3 {
				t.Fatalf("snapshots = %#v, want 5h aggregate + 2 healthy limits", snaps)
			}
		})
	}
}

// TestParseUsageLimitsSkipCountsAndReasons covers the several-rows, all-rows
// and empty-array shapes plus the exact warning string the adapter surfaces.
func TestParseUsageLimitsSkipCountsAndReasons(t *testing.T) {
	t.Parallel()

	healthy := `{"kind":"k","group":"good","percent":11,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":true,"scope":null}`
	noReset := `{"kind":"k","group":"bad","percent":0,"severity":"s","is_active":false,"scope":null}`
	noKind := `{"group":"bad","percent":0,"severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":false,"scope":null}`

	cases := []struct {
		name        string
		limits      string
		wantLimits  int
		wantReasons []string
		wantWarning string
	}{
		{
			name:        "empty array",
			limits:      `[]`,
			wantLimits:  0,
			wantReasons: nil,
			wantWarning: "",
		},
		{
			name:        "one row missing a required field",
			limits:      `[` + healthy + `,` + noReset + `]`,
			wantLimits:  1,
			wantReasons: []string{"row 1: missing resets_at"},
			wantWarning: "skipped 1 usage row: row 1: missing resets_at",
		},
		{
			name:        "several rows missing required fields",
			limits:      `[` + noKind + `,` + healthy + `,` + noReset + `]`,
			wantLimits:  1,
			wantReasons: []string{"row 0: missing kind", "row 2: missing resets_at"},
			wantWarning: "skipped 2 usage rows: row 0: missing kind; row 2: missing resets_at",
		},
		{
			name:        "all rows missing required fields",
			limits:      `[` + noReset + `,` + noKind + `]`,
			wantLimits:  0,
			wantReasons: []string{"row 0: missing resets_at", "row 1: missing kind"},
			wantWarning: "skipped 2 usage rows: row 0: missing resets_at; row 1: missing kind",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseUsageResponse([]byte(`{"five_hour":{"utilization":7,"resets_at":null},"limits":` + tc.limits + `}`))
			if err != nil {
				t.Fatalf("parseUsageResponse: %v", err)
			}
			if len(parsed.Limits) != tc.wantLimits {
				t.Fatalf("surviving limits = %#v, want %d", parsed.Limits, tc.wantLimits)
			}
			if !slices.Equal(parsed.SkipReasons, tc.wantReasons) {
				t.Fatalf("skip reasons = %#v, want %#v", parsed.SkipReasons, tc.wantReasons)
			}
			warning := usage.RowSkipWarning(parsed.SkipReasons)
			if tc.wantWarning == "" {
				if warning != nil {
					t.Fatalf("warning = %v, want none", warning)
				}
				return
			}
			if warning == nil || warning.Error() != tc.wantWarning {
				t.Fatalf("warning = %v, want %q", warning, tc.wantWarning)
			}
			if !errors.Is(warning, usage.ErrRowsSkipped) {
				t.Fatalf("warning %v is not classified as a row-skip warning", warning)
			}
		})
	}
}

// TestRowSkipWarningBoundsReasonList proves the reason list cannot grow with
// the upstream row count — the payload must not dictate the length of the line
// printed to the terminal or handed to the bounded operations journal.
func TestRowSkipWarningBoundsReasonList(t *testing.T) {
	t.Parallel()

	reasons := make([]string, 0, 12)
	for i := range 12 {
		reasons = append(reasons, fmt.Sprintf("row %d: missing kind", i))
	}
	got := usage.RowSkipWarning(reasons).Error()
	want := "skipped 12 usage rows: row 0: missing kind; row 1: missing kind; row 2: missing kind; row 3: missing kind; row 4: missing kind (+7 more)"
	if got != want {
		t.Fatalf("bounded warning = %q, want %q", got, want)
	}
}

func TestParseUsageResponseErrorsDoNotEchoRawUsageOrResetValues(t *testing.T) {
	t.Parallel()

	secrets := []string{"aggregate-reset-private-synthetic", "limit-reset-private-synthetic", "percent-private-synthetic"}

	// Typed-payload failure: still a whole-parse error, still bounded.
	_, err := parseUsageResponse([]byte(`{"five_hour":{"utilization":7,"resets_at":"aggregate-reset-private-synthetic"}}`))
	if err == nil {
		t.Fatal("typed payload error = nil")
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("parser error leaked raw value: %v", err)
		}
	}

	// Row-level failure: now a skip reason and a warning rather than an
	// error, so the same privacy boundary has to hold on that path too.
	for _, body := range []string{
		`{"limits":[{"kind":"k","group":"g","percent":7,"severity":"s","resets_at":"limit-reset-private-synthetic","is_active":true,"scope":null}]}`,
		`{"limits":[{"kind":"k","group":"g","percent":"percent-private-synthetic","severity":"s","resets_at":"2031-02-04T00:00:00Z","is_active":true,"scope":null}]}`,
	} {
		parsed, parseErr := parseUsageResponse([]byte(body))
		if parseErr != nil {
			t.Fatalf("parseUsageResponse(%s) = %v, want row-level tolerance", body, parseErr)
		}
		if len(parsed.SkipReasons) != 1 {
			t.Fatalf("skip reasons = %#v, want exactly one", parsed.SkipReasons)
		}
		warning := usage.RowSkipWarning(parsed.SkipReasons)
		if warning == nil {
			t.Fatalf("parseUsageResponse(%s) produced no warning", body)
		}
		for _, secret := range secrets {
			if strings.Contains(warning.Error(), secret) {
				t.Fatalf("row-skip warning leaked raw upstream value: %v", warning)
			}
		}
	}
}

func TestManagerAggregateOnlySuccessReplacesPriorNamedLimits(t *testing.T) {
	t.Parallel()

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&calls, 1) == 1 {
			_, _ = w.Write([]byte(`{
				"five_hour":{"utilization":10,"resets_at":"2031-02-03T09:00:00Z"},
				"seven_day":{"utilization":20,"resets_at":"2031-02-09T00:00:00Z"},
				"limits":[{"kind":"kind-redacted","group":"group-redacted","percent":30,"severity":"severity-redacted","resets_at":"2031-02-04T00:00:00Z","is_active":true,"scope":{"model":{"id":null,"display_name":"Model Redacted"},"surface":null}}]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"five_hour":{"utilization":11,"resets_at":"2031-02-03T10:00:00Z"},
			"seven_day":{"utilization":21,"resets_at":"2031-02-09T01:00:00Z"}
		}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-synthetic", "refresh-synthetic")
	adapter := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	registry := usage.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	store := usage.NewStore(filepath.Join(dir, "state"))
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	mgr := usage.NewManager(registry, store, func() time.Time { return now })

	first, err := mgr.Collect(context.Background())
	if err != nil || len(first) != 3 {
		t.Fatalf("first collect = %#v, %v", first, err)
	}
	second, err := mgr.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].Window != usage.Window5h || second[1].Window != usage.WindowWeekly {
		t.Fatalf("aggregate-only replacement = %#v, want exactly canonical pair", second)
	}
	loaded, _, err := store.LoadAll()
	if err != nil || len(loaded) != 2 {
		t.Fatalf("stored replacement = %#v, %v", loaded, err)
	}
}

// TestManagerMalformedLimitRowKeepsTheRestOfTheResponse is the regression for
// the reported failure: one `limits[]` row missing `resets_at` used to abort
// the whole parse, so Claude's rows froze at their previous UpdatedAt while
// `last_collect` kept advancing. The healthy rows must now refresh, the broken
// row must be dropped (not fabricated), and the warning must reach the caller.
func TestManagerMalformedLimitRowKeepsTheRestOfTheResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"five_hour":{"utilization":99,"resets_at":"2031-02-03T09:00:00Z"},
			"seven_day":{"utilization":98,"resets_at":"2031-02-09T00:00:00Z"},
			"limits":[
				{"kind":"kind-redacted","group":"group-kept","percent":30,"severity":"severity-redacted","resets_at":"2031-02-04T00:00:00Z","is_active":true,"scope":null},
				{"kind":"kind-redacted","group":"group-broken","percent":40,"severity":"severity-redacted","is_active":true,"scope":null}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-synthetic", "refresh-synthetic")
	adapter := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	registry := usage.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	store := usage.NewStore(filepath.Join(dir, "state"))
	priorTime := time.Date(2031, 2, 3, 3, 0, 0, 0, time.UTC)
	if err := store.SaveState(usage.State{Snapshots: []usage.Snapshot{
		{Model: Name, Window: usage.Window5h, Pct: 10, UpdatedAt: priorTime},
		{Model: Name, Window: usage.WindowWeekly, Pct: 20, UpdatedAt: priorTime},
	}}); err != nil {
		t.Fatal(err)
	}
	now := priorTime.Add(time.Hour)
	adapter.now = func() time.Time { return now }
	mgr := usage.NewManager(registry, store, func() time.Time { return now })

	got, err := mgr.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: skipped 1 usage row: row 1: missing resets_at") {
		t.Fatalf("Collect warning = %v, want the bounded row-skip warning", err)
	}
	if !errors.Is(err, usage.ErrRowsSkipped) {
		t.Fatalf("Collect error %v is not classified as a partial row-skip", err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %#v, want 5h + weekly + the one healthy named quota", got)
	}
	for _, s := range got {
		if !s.UpdatedAt.Equal(now) {
			t.Fatalf("row %#v kept the stale UpdatedAt; the healthy rows must refresh", s)
		}
		if s.Bucket == "group-broken" || s.Pct == 40 {
			t.Fatalf("skipped row was fabricated into the snapshot: %#v", s)
		}
	}
	loaded, _, err := store.LoadAll()
	if err != nil || len(loaded) != 3 {
		t.Fatalf("persisted rows = %#v, %v", loaded, err)
	}
}

func TestManagerHardCollectFailurePreservesCompleteClaudeLastKnownGood(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"five_hour":{"utilization":99,"resets_at":"2031-02-03T09:00:00Z"},
			"seven_day":{"utilization":98,"resets_at":"2031-02-09T00:00:00Z"},
			"limits":{"not":"an array"}
		}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-synthetic", "refresh-synthetic")
	adapter := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	registry := usage.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	store := usage.NewStore(filepath.Join(dir, "state"))
	priorTime := time.Date(2031, 2, 3, 3, 0, 0, 0, time.UTC)
	prior := []usage.Snapshot{
		{Model: Name, Window: usage.Window5h, Pct: 10, UpdatedAt: priorTime},
		{Model: Name, Window: usage.WindowWeekly, Pct: 20, UpdatedAt: priorTime},
		{Model: Name, Window: usage.WindowQuota, Bucket: "group-redacted", Pct: 30, UpdatedAt: priorTime, NamedQuota: &usage.NamedQuota{Kind: "kind-redacted", Group: "group-redacted", Severity: "severity-redacted", IsActive: true, Scope: nil}},
	}
	if err := store.SaveState(usage.State{Snapshots: prior}); err != nil {
		t.Fatal(err)
	}
	now := priorTime.Add(time.Hour)
	mgr := usage.NewManager(registry, store, func() time.Time { return now })
	got, err := mgr.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "parse usage response limits") {
		t.Fatalf("Collect error = %v", err)
	}
	if len(got) != len(prior) {
		t.Fatalf("last-known-good rows = %#v, want %#v", got, prior)
	}
	for i := range prior {
		if got[i].Window != prior[i].Window || got[i].Pct != prior[i].Pct || !got[i].UpdatedAt.Equal(prior[i].UpdatedAt) {
			t.Fatalf("LKG[%d] = %#v, want %#v", i, got[i], prior[i])
		}
	}
}

func TestAdapterCollect401WithoutRefreshFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	// No refresh token at all.
	writeCreds(t, credsPath, "access-stale", "")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	snaps, err := a.Collect(context.Background())
	if err == nil {
		t.Fatalf("expected error on 401 with no refresh, got nil")
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil", snaps)
	}
}

func TestAdapterCollect401TriggersRefresh(t *testing.T) {
	t.Parallel()

	var refreshCalls atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer stale" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if auth != "Bearer fresh-access" {
			t.Errorf("unexpected Authorization: %q", auth)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":1.0,"resets_at":"2026-05-06T17:00:00Z"}}`))
	})
	mux.HandleFunc("/refresh", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "refresh-original" {
			t.Errorf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh-access","refresh_token":"refresh-rotated","expires_in":3600}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "stale", "refresh-original")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	a.now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }

	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}
	if len(snaps) != 1 || snaps[0].Pct != 1.0 {
		t.Fatalf("snaps after refresh = %+v, want one at 1%%", snaps)
	}
	// Credentials file must have been rewritten with the new tokens.
	updated, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("read creds: %v", err)
	}
	if !strings.Contains(string(updated), "fresh-access") {
		t.Fatalf("creds file not updated: %s", string(updated))
	}
	if !strings.Contains(string(updated), "refresh-rotated") {
		t.Fatalf("rotated refresh token not persisted: %s", string(updated))
	}
}

func TestAdapterCollectNetworkErrorReturnsZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-1", "refresh-1")

	// Point at an address that nothing is listening on.
	a := NewWithConfig(credsPath, "http://127.0.0.1:1/usage", "http://127.0.0.1:1/refresh", &http.Client{Timeout: 200 * time.Millisecond})
	a.now = func() time.Time { return time.Now() }

	snaps, err := a.Collect(context.Background())
	if err == nil {
		t.Fatalf("expected network error, got nil")
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil on network error", snaps)
	}
}

func TestAdapterCollectMissingCredentialsFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	credsPath := filepath.Join(dir, "does-not-exist.json")

	a := NewWithConfig(credsPath, "http://nope/usage", "http://nope/refresh", &http.Client{Timeout: 100 * time.Millisecond})
	snaps, err := a.Collect(context.Background())
	if err == nil {
		t.Fatalf("expected error for missing creds, got nil")
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil", snaps)
	}
}

func TestAdapter429SetsBackoffWithRetryAfter(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-1", "refresh-1")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	snaps, err := a.Collect(context.Background())
	if err == nil {
		t.Fatalf("expected error on 429, got nil")
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil on 429", snaps)
	}
	state := a.SaveBackoff()
	// 120s is above the retryAfterFloor (60s) so it's used verbatim.
	want := now.Add(120 * time.Second)
	if !state.Until.Equal(want) {
		t.Fatalf("backoff.Until = %v, want %v (Retry-After=120)", state.Until, want)
	}
	if state.Consecutive != 1 {
		t.Fatalf("backoff.Consecutive = %d, want 1", state.Consecutive)
	}
}

func TestAdapter429RetryAfterBelowFloorIsClampedUp(t *testing.T) {
	t.Parallel()

	// Server returns Retry-After: 5 (seconds). Adapter must clamp UP
	// to retryAfterFloor (60s) so we never hammer Anthropic at sub-60s
	// cadence even if the upstream tells us we may.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-1", "refresh-1")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	if _, err := a.Collect(context.Background()); err == nil {
		t.Fatalf("expected 429 error")
	}
	state := a.SaveBackoff()
	want := now.Add(retryAfterFloor)
	if !state.Until.Equal(want) {
		t.Fatalf("backoff.Until = %v, want %v (Retry-After=5 clamped UP to %v)", state.Until, want, retryAfterFloor)
	}
}

func TestAdapter429NoHeaderUsesDefault(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-1", "refresh-1")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	if _, err := a.Collect(context.Background()); err == nil {
		t.Fatalf("expected 429 error")
	}
	state := a.SaveBackoff()
	want := now.Add(30 * time.Minute)
	if !state.Until.Equal(want) {
		t.Fatalf("backoff.Until = %v, want %v (default 30m)", state.Until, want)
	}
}

func TestAdapter429ConsecutiveDoublesUpToCap(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-1", "refresh-1")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// Synthesise consecutive 429s by clearing the backoff window
	// between calls (otherwise the adapter short-circuits during
	// backoff and never bumps the counter).
	for i := 1; i <= 4; i++ {
		a.LoadBackoff(usage.BackoffState{Until: time.Time{}, Consecutive: i - 1})
		if _, err := a.Collect(context.Background()); err == nil {
			t.Fatalf("iter %d: expected 429 error", i)
		}
	}
	state := a.SaveBackoff()
	// 30m → 60m (cap) → 120m capped at 60m → 240m capped at 60m. Final: 60m.
	want := now.Add(60 * time.Minute)
	if !state.Until.Equal(want) {
		t.Fatalf("backoff.Until = %v, want %v (capped at 60m)", state.Until, want)
	}
	if state.Consecutive != 4 {
		t.Fatalf("backoff.Consecutive = %d, want 4", state.Consecutive)
	}
}

func TestAdapterBackoffShortCircuitsCollect(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":1.0,"resets_at":"2026-05-06T17:00:00Z"}}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-1", "refresh-1")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.LoadBackoff(usage.BackoffState{Until: now.Add(time.Minute), Consecutive: 1})

	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect during backoff must be silent: %v", err)
	}
	if snaps != nil {
		t.Fatalf("snaps = %+v, want nil during backoff", snaps)
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0 (backoff short-circuit)", calls)
	}
}

func TestAdapter200ResetsBackoff(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":3.0,"resets_at":"2026-05-06T17:00:00Z"}}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	credsPath := filepath.Join(dir, ".credentials.json")
	writeCreds(t, credsPath, "access-1", "refresh-1")

	a := NewWithConfig(credsPath, server.URL+"/usage", server.URL+"/refresh", server.Client())
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	// Pre-load a stale backoff that has expired (so Collect proceeds).
	a.LoadBackoff(usage.BackoffState{Until: now.Add(-time.Minute), Consecutive: 3})

	snaps, err := a.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snaps len=%d, want 1", len(snaps))
	}
	state := a.SaveBackoff()
	if !state.Until.IsZero() {
		t.Fatalf("backoff.Until = %v, want zero (reset on 200)", state.Until)
	}
	if state.Consecutive != 0 {
		t.Fatalf("backoff.Consecutive = %d, want 0 (reset on 200)", state.Consecutive)
	}
}

func TestAdapterThrottleHint(t *testing.T) {
	t.Parallel()

	a := New()
	if got := a.ThrottleHint(); got != 5*time.Minute {
		t.Fatalf("ThrottleHint = %v, want 5m", got)
	}
}

func TestAdapterResetBackoffClearsState(t *testing.T) {
	t.Parallel()

	a := New()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	a.LoadBackoff(usage.BackoffState{Until: now.Add(30 * time.Minute), Consecutive: 3})
	a.ResetBackoff()
	state := a.SaveBackoff()
	if !state.Until.IsZero() {
		t.Fatalf("backoff.Until = %v, want zero after ResetBackoff", state.Until)
	}
	if state.Consecutive != 0 {
		t.Fatalf("backoff.Consecutive = %d, want 0 after ResetBackoff", state.Consecutive)
	}
}

func TestRedactTokenNeverLeaksFullSecret(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"", "****"},
		{"short", "****"},
		{"abcdefghij", "abcd****ghij"},
	}
	for _, tc := range cases {
		got := RedactToken(tc.in)
		if got != tc.want {
			t.Fatalf("RedactToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if len(tc.in) > 8 && strings.Contains(got, tc.in) {
			t.Fatalf("RedactToken leaked full secret: %q", got)
		}
	}
}
