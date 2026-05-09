// Package claude implements an authoritative usage Adapter for Claude Code.
//
// Source of truth: the Anthropic OAuth usage API at
// `GET https://api.anthropic.com/api/oauth/usage`. This endpoint reports
// the account-wide 5-hour and weekly utilisation percentages along with
// reset timestamps. Compared to the v0 JSONL-scraping implementation,
// the OAuth-backed adapter is:
//
//   - Server-side: numbers match `claude /usage` exactly because they
//     come from the same backend.
//   - Multi-machine safe: usage on another box still counts.
//   - Drift-free: there is no local accumulation that could double-count.
//
// Credentials live in `~/.claude/.credentials.json` under the
// `claudeAiOauth` object. This adapter never logs the token; on any
// failure path the access token is replaced with `****` before any
// debug output.
//
// On 401 we attempt one refresh round-trip
// (`POST https://api.anthropic.com/api/oauth/token`) using the stored
// refresh token. On success the credentials file is rewritten preserving
// its exact original schema; on failure we return zero snapshots and a
// non-fatal error.
//
// Rate-limit handling: the OAuth usage endpoint enforces a low
// per-credential request budget — much tighter than initially modelled.
// On HTTP 429 the adapter persists an exponential backoff
// (30m → 60m → 60m, doubling per consecutive failure, capped at 60m)
// so the next Collect short-circuits without making the network call.
// A `Retry-After` header (seconds or HTTP-date) is honoured up to the
// 60m cap; if the server returns an unreasonably small Retry-After
// (under retryAfterFloor, currently 60s) we clamp UP to the floor to
// avoid hammering Anthropic's rate limiter into a tighter loop.
// A non-429 success resets the streak.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

// Name is the adapter's registered identifier. Aligns with the model-key
// used by the renderer.
const Name = "claude"

// Default endpoints. Overridable in tests so the adapter can hit an
// httptest.Server without DNS or TLS faff.
const (
	defaultUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	defaultRefreshURL = "https://api.anthropic.com/api/oauth/token"
)

// httpTimeout caps each HTTP round-trip. The status segment redraws every
// few seconds, so the adapter must never hang the CLI on a slow API.
const httpTimeout = 8 * time.Second

// userAgent identifies projmux to Anthropic logs. Bumping the version
// suffix is purely informational.
const userAgent = "projmux-usage/0.2"

// anthropicVersion is the API version header recommended by Anthropic.
const anthropicVersion = "2023-06-01"

// throttleHint is the per-adapter minimum interval between Collect
// invocations. The OAuth usage endpoint enforces a much tighter quota
// than initially modelled — a 60s cadence still trips 429 in practice —
// so the floor is 5 minutes. Codex (local-file source, no quota) keeps
// the global 30s default.
const throttleHint = 5 * time.Minute

// Backoff parameters. Defaults reflect Anthropic's observed
// per-credential 429 quota for OAuth usage surfaces: start at 30
// minutes, double per consecutive 429, cap at 60 minutes. The doubling
// sequence is therefore 30m → 60m → 60m. Retry-After (when present)
// overrides the doubling progression but is still clamped to the cap
// so a hostile server cannot pin the adapter offline indefinitely.
//
// retryAfterFloor protects against an inverse failure mode: a
// misconfigured or aggressive server returning Retry-After: 1 (or
// similar) would otherwise let the CLI hammer the API every second.
// We clamp Retry-After UP to this floor (i.e. dur = max(retry_after,
// retryAfterFloor)) when the header value is below it. Anthropic's
// hint still wins when it's reasonable.
const (
	backoffDefault  = 30 * time.Minute
	backoffCap      = 60 * time.Minute
	retryAfterFloor = 60 * time.Second
)

// Adapter is the Claude implementation of usage.Adapter.
type Adapter struct {
	credentialsPath string
	usageURL        string
	refreshURL      string
	httpClient      *http.Client
	now             func() time.Time

	// Backoff bookkeeping. Mutated under mu so concurrent Collect calls
	// (rare — the CLI is single-threaded — but cheap to be safe) don't
	// race on counter increments. The Manager round-trips the persisted
	// form through snapshots.json via LoadBackoff/SaveBackoff.
	mu             sync.Mutex
	backoffUntil   time.Time
	consecutive429 int
}

// New returns an Adapter that reads credentials from
// `~/.claude/.credentials.json` and calls the production endpoints.
func New() *Adapter {
	a := &Adapter{
		usageURL:   defaultUsageURL,
		refreshURL: defaultRefreshURL,
		httpClient: &http.Client{Timeout: httpTimeout},
		now:        time.Now,
	}
	if home, err := os.UserHomeDir(); err == nil {
		a.credentialsPath = filepath.Join(home, ".claude", ".credentials.json")
	}
	return a
}

// NewWithConfig is intended for tests: it lets the test inject a
// credentials path and override endpoints.
func NewWithConfig(credentialsPath, usageURL, refreshURL string, client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	return &Adapter{
		credentialsPath: credentialsPath,
		usageURL:        usageURL,
		refreshURL:      refreshURL,
		httpClient:      client,
		now:             time.Now,
	}
}

// Name implements usage.Adapter.
func (a *Adapter) Name() string { return Name }

// ThrottleHint implements usage.ThrottleHinter. Claude's OAuth usage
// endpoint enforces a tight per-credential request budget; a 60s
// cadence still trips 429 in practice. We ask the Manager for at least
// 5 minutes between calls.
func (a *Adapter) ThrottleHint() time.Duration { return throttleHint }

// LoadBackoff implements usage.BackoffStater. Called by the Manager
// before Collect with the on-disk backoff snapshot, so a CLI restart
// observes prior 429 progress.
func (a *Adapter) LoadBackoff(state usage.BackoffState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.backoffUntil = state.Until
	a.consecutive429 = state.Consecutive
}

// SaveBackoff implements usage.BackoffStater. Called by the Manager
// after Collect so a fresh 429 (or success) is persisted.
func (a *Adapter) SaveBackoff() usage.BackoffState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return usage.BackoffState{
		Until:       a.backoffUntil,
		Consecutive: a.consecutive429,
	}
}

// ResetBackoff implements usage.BackoffResetter. It clears any active
// cooldown AND zeroes the consecutive counter so a `--force` refresh
// neither short-circuits nor preserves prior 429 streak. If that
// `--force` request itself returns 429, the adapter records a fresh
// streak starting at consecutive=1.
func (a *Adapter) ResetBackoff() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.backoffUntil = time.Time{}
	a.consecutive429 = 0
}

// Collect calls the OAuth usage endpoint and translates the response into
// 5h + weekly Snapshots. Best-effort: any failure (no creds, network
// error, 401 with no working refresh) returns nil snapshots and a
// non-fatal error so the status segment stays silent.
//
// During 429-induced backoff the adapter short-circuits: it returns
// (nil, nil) so the Manager's merge logic preserves the prior on-disk
// rows. The caller never observes an error during backoff because
// "we deferred the call" is not a failure — it's the desired behaviour.
func (a *Adapter) Collect(ctx context.Context) ([]usage.Snapshot, error) {
	now := a.now().UTC()
	a.mu.Lock()
	if !a.backoffUntil.IsZero() && now.Before(a.backoffUntil) {
		a.mu.Unlock()
		return nil, nil
	}
	a.mu.Unlock()

	if a.credentialsPath == "" {
		return nil, errors.New("claude: no credentials path resolved")
	}
	creds, raw, err := loadCredentials(a.credentialsPath)
	if err != nil {
		return nil, err
	}
	if creds.token() == "" {
		return nil, errors.New("claude: empty access token")
	}

	body, status, retryAfter, err := a.fetchUsage(ctx, creds.token())
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		a.recordBackoff(now, retryAfter)
		return nil, fmt.Errorf("claude: usage endpoint returned status 429 (backing off)")
	}
	if status == http.StatusUnauthorized {
		// Try a single refresh round-trip. If that fails, give up.
		if creds.refresh() == "" {
			return nil, errors.New("claude: 401 and no refresh token available")
		}
		newAccess, newRefresh, expiresIn, refreshErr := a.refreshToken(ctx, creds.refresh())
		if refreshErr != nil {
			return nil, fmt.Errorf("claude: token refresh failed: %w", refreshErr)
		}
		// Persist the rotated tokens, preserving the file's original
		// schema. Failure to write back is non-fatal: the new access
		// token is still in memory, so this Collect can proceed; the
		// next invocation may have to refresh again.
		_ = writeCredentials(a.credentialsPath, raw, newAccess, newRefresh, expiresIn, a.now())
		body, status, retryAfter, err = a.fetchUsage(ctx, newAccess)
		if err != nil {
			return nil, err
		}
		if status == http.StatusTooManyRequests {
			a.recordBackoff(now, retryAfter)
			return nil, fmt.Errorf("claude: usage endpoint returned status 429 (backing off)")
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("claude: usage endpoint returned status %d", status)
	}

	resp, err := parseUsageResponse(body)
	if err != nil {
		return nil, err
	}
	// Reset backoff on a clean 200.
	a.mu.Lock()
	a.backoffUntil = time.Time{}
	a.consecutive429 = 0
	a.mu.Unlock()
	return resp.toSnapshots(a.now().UTC()), nil
}

// recordBackoff applies the exponential-backoff policy. retryAfter > 0
// (parsed from the Retry-After header) takes precedence over the
// doubling progression. The result is then clamped:
//
//   - upward to retryAfterFloor when Retry-After arrives unreasonably
//     small (<60s). Anthropic's hint wins when reasonable; we never
//     hammer the API every second just because the server said so.
//   - downward to backoffCap so a hostile or buggy server cannot pin
//     the adapter offline beyond the documented 60-minute ceiling.
//
// Doubling sequence (no Retry-After): n=1 → 30m, n=2 → 60m,
// n>=3 → 60m (cap).
func (a *Adapter) recordBackoff(now time.Time, retryAfter time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.consecutive429++
	var dur time.Duration
	if retryAfter > 0 {
		dur = max(
			// Server told us a value but it's smaller than our anti-hammer
			// floor: bump it up. This protects against pathological 429
			// loops where Retry-After: 1 would otherwise let the CLI
			// re-hit Anthropic once per second.
			retryAfter, retryAfterFloor)
	} else {
		// 30m, 60m, 60m (cap). n=1 → 30m, n>=2 → 60m.
		shift := max(a.consecutive429-1, 0)
		if shift > 30 {
			shift = 30 // guard against overflow on absurd streaks.
		}
		dur = backoffDefault << shift
	}
	if dur > backoffCap {
		dur = backoffCap
	}
	if dur < 0 {
		dur = backoffDefault
	}
	a.backoffUntil = now.Add(dur)
}

// fetchUsage performs a single GET against the usage endpoint. The
// returned retryAfter is non-zero only when the response carried a
// parseable Retry-After header (typically alongside a 429).
func (a *Adapter) fetchUsage(ctx context.Context, token string) ([]byte, int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.usageURL, nil)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("claude: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Anthropic-Version", anthropicVersion)
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("claude: GET usage: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, 0, fmt.Errorf("claude: read body: %w", err)
	}
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), a.now())
	return body, resp.StatusCode, retryAfter, nil
}

// parseRetryAfter accepts both forms of the Retry-After header: integer
// seconds, or an HTTP-date. Returns 0 when the value is missing or
// unparseable so the caller falls back to the doubling progression.
func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		return d
	}
	return 0
}

// refreshToken trades a refresh token for a new access (and possibly a new
// refresh) token. The returned expiresIn is in seconds.
func (a *Adapter) refreshToken(ctx context.Context, refresh string) (newAccess, newRefresh string, expiresIn int64, err error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refresh)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.refreshURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", 0, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Anthropic-Version", anthropicVersion)
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("POST refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("refresh status %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", 0, fmt.Errorf("parse refresh response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", "", 0, errors.New("refresh response missing access_token")
	}
	if payload.RefreshToken == "" {
		// Some providers omit the field when it doesn't rotate. Keep the old.
		payload.RefreshToken = refresh
	}
	return payload.AccessToken, payload.RefreshToken, payload.ExpiresIn, nil
}

// usageResponse mirrors the JSON returned by /api/oauth/usage. Only the
// fields we render are typed; the rest are tolerated as unknown.
type usageResponse struct {
	FiveHour *struct {
		Utilization float64    `json:"utilization"`
		ResetsAt    *time.Time `json:"resets_at"`
	} `json:"five_hour"`
	SevenDay *struct {
		Utilization float64    `json:"utilization"`
		ResetsAt    *time.Time `json:"resets_at"`
	} `json:"seven_day"`
}

func parseUsageResponse(body []byte) (*usageResponse, error) {
	var r usageResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("claude: parse usage response: %w", err)
	}
	return &r, nil
}

// toSnapshots converts the usage response into the canonical Snapshot
// pair. When a window block is missing entirely the corresponding
// snapshot is omitted (rather than emitted as 0% with a zero ResetsAt) so
// the renderer can drop it gracefully.
func (r *usageResponse) toSnapshots(now time.Time) []usage.Snapshot {
	out := make([]usage.Snapshot, 0, 2)
	if r.FiveHour != nil {
		s := usage.Snapshot{
			Model:     Name,
			Window:    usage.Window5h,
			Pct:       r.FiveHour.Utilization,
			UpdatedAt: now,
		}
		if r.FiveHour.ResetsAt != nil {
			s.ResetsAt = r.FiveHour.ResetsAt.UTC()
		}
		out = append(out, s)
	}
	if r.SevenDay != nil {
		s := usage.Snapshot{
			Model:     Name,
			Window:    usage.WindowWeekly,
			Pct:       r.SevenDay.Utilization,
			UpdatedAt: now,
		}
		if r.SevenDay.ResetsAt != nil {
			s.ResetsAt = r.SevenDay.ResetsAt.UTC()
		}
		out = append(out, s)
	}
	return out
}

// credentials is the typed view onto `~/.claude/.credentials.json`. The
// raw bytes are kept separately so writeCredentials can preserve fields
// the adapter doesn't know about.
type credentials struct {
	ClaudeAiOauth *struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
	// Fallback keys for alternate schemas observed in the wild.
	AccessTokenAlt  string `json:"access_token,omitempty"`
	RefreshTokenAlt string `json:"refresh_token,omitempty"`
}

func (c *credentials) token() string {
	if c == nil {
		return ""
	}
	if c.ClaudeAiOauth != nil && c.ClaudeAiOauth.AccessToken != "" {
		return c.ClaudeAiOauth.AccessToken
	}
	return c.AccessTokenAlt
}

func (c *credentials) refresh() string {
	if c == nil {
		return ""
	}
	if c.ClaudeAiOauth != nil && c.ClaudeAiOauth.RefreshToken != "" {
		return c.ClaudeAiOauth.RefreshToken
	}
	return c.RefreshTokenAlt
}

// loadCredentials reads and parses the credentials file. It also returns
// the original raw bytes so writeCredentials can preserve the exact
// schema (unknown fields included).
func loadCredentials(path string) (*credentials, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("claude: credentials not found at %s", path)
		}
		return nil, nil, fmt.Errorf("claude: read credentials: %w", err)
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, nil, fmt.Errorf("claude: parse credentials: %w", err)
	}
	return &c, data, nil
}

// writeCredentials rewrites the credentials file with the new tokens,
// preserving the original schema by patching the parsed-and-reserialised
// generic map (so unknown fields survive).
func writeCredentials(path string, original []byte, newAccess, newRefresh string, expiresInSeconds int64, now time.Time) error {
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		return fmt.Errorf("claude: reparse credentials: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	expiresAtMs := now.Add(time.Duration(expiresInSeconds) * time.Second).UnixMilli()
	if oauthAny, ok := doc["claudeAiOauth"]; ok {
		if oauth, isMap := oauthAny.(map[string]any); isMap {
			oauth["accessToken"] = newAccess
			oauth["refreshToken"] = newRefresh
			oauth["expiresAt"] = expiresAtMs
			doc["claudeAiOauth"] = oauth
		}
	} else {
		// Fallback flat schema.
		if _, has := doc["access_token"]; has {
			doc["access_token"] = newAccess
		}
		if _, has := doc["refresh_token"]; has {
			doc["refresh_token"] = newRefresh
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("claude: encode credentials: %w", err)
	}
	// Atomic write: temp + rename in the same dir so we never leave the
	// user with an empty credentials file if the write fails.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".credentials.json.tmp-*")
	if err != nil {
		return fmt.Errorf("claude: create temp credentials: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("claude: write temp credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("claude: close temp credentials: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("claude: chmod temp credentials: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("claude: rename temp credentials: %w", err)
	}
	cleanup = false
	return nil
}

// RedactToken returns the supplied string with the middle replaced by
// `****`. Exported so future debug-logging hooks (gated on
// PROJMUX_USAGE_DEBUG at the call site) have a single safe primitive for
// surfacing token-shaped strings without leaking the secret. Tokens MUST
// pass through this helper before they ever touch a Writer.
func RedactToken(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}
