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
