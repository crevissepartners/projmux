package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
