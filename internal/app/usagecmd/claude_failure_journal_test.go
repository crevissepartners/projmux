package usagecmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	claudeadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/claude"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// Fixture values that must never reach the journal. They are obviously fake
// and only ever live in the temp directory of the test that writes them.
const (
	claudeJournalFixtureAccess         = "fake-access-token-for-test"
	claudeJournalFixtureRefresh        = "fake-refresh-token-for-test"
	claudeJournalFixtureRotatedAccess  = "fake-rotated-access-token-for-test"
	claudeJournalFixtureRotatedRefresh = "fake-rotated-refresh-token-for-test"
	claudeJournalFixtureBodyMarker     = "fake-upstream-body-marker-for-test"
	claudeJournalFixtureRunID          = "claudefailurejournalrun"
	claudeJournalFixtureAt             = "2026-05-06T12:00:00Z"
)

// pinnedClockJournal appends through the real journal store (and so through
// its sanitizer) with the two wall-clock fields pinned. Every other byte of
// the record is what the recorder produced, so a byte scan for status numbers
// cannot match a timestamp or a duration by accident.
type pinnedClockJournal struct{ store *diagnostics.Store }

func (w pinnedClockJournal) Append(event diagnostics.Event) error {
	event.At = claudeJournalFixtureAt
	event.DurationMS = 0
	return w.store.Append(event)
}

type claudeJournalFixture struct {
	credsPath      string
	usageURL       string
	refreshURL     string
	refreshCalls   *atomic.Int64
	wantRefreshes  int64
	wantCredsBytes []string
}

func writeClaudeJournalCredentials(t *testing.T, path, access, refresh string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": access, "refreshToken": refresh, "expiresAt": int64(1778068800000),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// claudeJournalServer serves the usage and refresh endpoints. usage answers
// the original token and the rotated one separately; refresh counts calls.
func claudeJournalServer(t *testing.T, usageStale, usageRotated, refresh http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	mux := http.NewServeMux()
	mux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		handler := usageStale
		if r.Header.Get("Authorization") == "Bearer "+claudeJournalFixtureRotatedAccess {
			handler = usageRotated
		}
		if handler == nil {
			t.Errorf("unexpected usage request with %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusTeapot)
			return
		}
		handler(w, r)
	})
	mux.HandleFunc("/refresh", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if refresh == nil {
			t.Errorf("unexpected refresh request")
			w.WriteHeader(http.StatusTeapot)
			return
		}
		refresh(w, r)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, calls
}

// answer writes status plus a body that carries the upstream marker, so the
// negative scan proves no response body reaches the journal.
func answer(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func markerBody() string {
	return `{"error":{"message":"` + claudeJournalFixtureBodyMarker + `"}}`
}

var claudeJournalRotatedRefresh = answer(http.StatusOK, fmt.Sprintf(
	`{"access_token":%q,"refresh_token":%q,"expires_in":3600}`,
	claudeJournalFixtureRotatedAccess, claudeJournalFixtureRotatedRefresh,
))

func closedServerURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	return url
}

// TestClaudeWholeCollectFailureClassesLandInJournal runs the real Claude
// adapter against temp credentials and httptest endpoints through the Manager
// and Command, and requires each whole-failure class to reach the journal as
// its own closed token. Credential load failures and an empty access token
// stay distinct. The journal bytes are scanned for every fixture secret, the
// credentials path, the upstream body marker, and the HTTP status numbers.
func TestClaudeWholeCollectFailureClassesLandInJournal(t *testing.T) {
	t.Parallel()

	valid := func(t *testing.T, dir string) string {
		path := filepath.Join(dir, ".credentials.json")
		writeClaudeJournalCredentials(t, path, claudeJournalFixtureAccess, claudeJournalFixtureRefresh)
		return path
	}
	withServer := func(t *testing.T, credsPath string, usageStale, usageRotated, refresh http.HandlerFunc) claudeJournalFixture {
		server, calls := claudeJournalServer(t, usageStale, usageRotated, refresh)
		return claudeJournalFixture{
			credsPath: credsPath, usageURL: server.URL + "/usage", refreshURL: server.URL + "/refresh", refreshCalls: calls,
		}
	}
	unreachable := func(t *testing.T, credsPath string) claudeJournalFixture {
		url := closedServerURL(t)
		return claudeJournalFixture{credsPath: credsPath, usageURL: url + "/usage", refreshURL: url + "/refresh"}
	}

	tests := []struct {
		name  string
		want  diagnostics.UsageFailure
		setup func(t *testing.T, dir string) claudeJournalFixture
	}{
		{"credentials path unresolved", diagnostics.UsageFailureCredentialsUnavailable, func(t *testing.T, dir string) claudeJournalFixture {
			return unreachable(t, "")
		}},
		{"credentials file missing", diagnostics.UsageFailureCredentialsUnavailable, func(t *testing.T, dir string) claudeJournalFixture {
			return unreachable(t, filepath.Join(dir, "missing-credentials.json"))
		}},
		{"credentials unreadable", diagnostics.UsageFailureCredentialsUnavailable, func(t *testing.T, dir string) claudeJournalFixture {
			path := filepath.Join(dir, "credentials-dir")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return unreachable(t, path)
		}},
		{"credentials unparseable", diagnostics.UsageFailureCredentialsUnavailable, func(t *testing.T, dir string) claudeJournalFixture {
			path := filepath.Join(dir, ".credentials.json")
			if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"`+claudeJournalFixtureAccess+`"`), 0o600); err != nil {
				t.Fatal(err)
			}
			return unreachable(t, path)
		}},
		{"access token empty", diagnostics.UsageFailureCredentialsTokenEmpty, func(t *testing.T, dir string) claudeJournalFixture {
			path := filepath.Join(dir, ".credentials.json")
			writeClaudeJournalCredentials(t, path, "", claudeJournalFixtureRefresh)
			return unreachable(t, path)
		}},
		{"401 without refresh token", diagnostics.UsageFailureAuthRejected, func(t *testing.T, dir string) claudeJournalFixture {
			path := filepath.Join(dir, ".credentials.json")
			writeClaudeJournalCredentials(t, path, claudeJournalFixtureAccess, "")
			return withServer(t, path, answer(http.StatusUnauthorized, markerBody()), nil, nil)
		}},
		{"401 refresh rejected", diagnostics.UsageFailureAuthRejected, func(t *testing.T, dir string) claudeJournalFixture {
			f := withServer(t, valid(t, dir), answer(http.StatusUnauthorized, markerBody()), nil, answer(http.StatusBadRequest, markerBody()))
			f.wantRefreshes = 1
			return f
		}},
		{"401 refresh unreachable", diagnostics.UsageFailureAuthRejected, func(t *testing.T, dir string) claudeJournalFixture {
			f := withServer(t, valid(t, dir), answer(http.StatusUnauthorized, markerBody()), nil, nil)
			f.refreshURL = closedServerURL(t) + "/refresh"
			return f
		}},
		{"401 again after successful refresh", diagnostics.UsageFailureAuthRejected, func(t *testing.T, dir string) claudeJournalFixture {
			f := withServer(t, valid(t, dir), answer(http.StatusUnauthorized, markerBody()),
				answer(http.StatusUnauthorized, markerBody()), claudeJournalRotatedRefresh)
			f.wantRefreshes = 1
			f.wantCredsBytes = []string{claudeJournalFixtureRotatedAccess, claudeJournalFixtureRotatedRefresh}
			return f
		}},
		{"429", diagnostics.UsageFailureRateLimited, func(t *testing.T, dir string) claudeJournalFixture {
			return withServer(t, valid(t, dir), answer(http.StatusTooManyRequests, markerBody()), nil, nil)
		}},
		{"429 after successful refresh", diagnostics.UsageFailureRateLimited, func(t *testing.T, dir string) claudeJournalFixture {
			f := withServer(t, valid(t, dir), answer(http.StatusUnauthorized, markerBody()),
				answer(http.StatusTooManyRequests, markerBody()), claudeJournalRotatedRefresh)
			f.wantRefreshes = 1
			f.wantCredsBytes = []string{claudeJournalFixtureRotatedAccess, claudeJournalFixtureRotatedRefresh}
			return f
		}},
		{"500", diagnostics.UsageFailureHTTPStatus, func(t *testing.T, dir string) claudeJournalFixture {
			return withServer(t, valid(t, dir), answer(http.StatusInternalServerError, markerBody()), nil, nil)
		}},
		{"403", diagnostics.UsageFailureHTTPStatus, func(t *testing.T, dir string) claudeJournalFixture {
			return withServer(t, valid(t, dir), answer(http.StatusForbidden, markerBody()), nil, nil)
		}},
		{"usage endpoint unreachable", diagnostics.UsageFailureNetwork, func(t *testing.T, dir string) claudeJournalFixture {
			return unreachable(t, valid(t, dir))
		}},
		{"usage response not JSON", diagnostics.UsageFailureResponseInvalid, func(t *testing.T, dir string) claudeJournalFixture {
			return withServer(t, valid(t, dir), answer(http.StatusOK, `{"five_hour":"`+claudeJournalFixtureBodyMarker), nil, nil)
		}},
		{"usage limits not an array", diagnostics.UsageFailureResponseInvalid, func(t *testing.T, dir string) claudeJournalFixture {
			return withServer(t, valid(t, dir), answer(http.StatusOK, `{"limits":{"kind":"`+claudeJournalFixtureBodyMarker+`"}}`), nil, nil)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			f := test.setup(t, dir)
			now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

			registry := usage.NewRegistry()
			if err := registry.Replace(claudeadapter.NewWithConfig(
				f.credsPath, f.usageURL, f.refreshURL, &http.Client{Timeout: 5 * time.Second},
			)); err != nil {
				t.Fatal(err)
			}
			mgr := usage.NewManager(registry, usage.NewStore(filepath.Join(dir, "usage")), func() time.Time { return now })
			journalPath := filepath.Join(t.TempDir(), "logs", diagnostics.LogFileName)
			store := diagnostics.NewStore(journalPath)
			cmd := New(func() time.Time { return now })
			cmd.managerFn = func([]string) (*usage.Manager, error) { return mgr, nil }
			cmd.journalFn = func() *diagnostics.UsageRecorder {
				return diagnostics.NewUsageRecorder(pinnedClockJournal{store: store}, claudeJournalFixtureRunID, "0.0.0-test", diagnostics.MuxBackend())
			}

			if err := cmd.Run([]string{"--model", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run: %v", err)
			}

			events, err := store.Read()
			if err != nil {
				t.Fatalf("read journal: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("journal rows = %#v, want exactly one", events)
			}
			row := events[0]
			if row.Component != "usage" || row.Event != "usage.collect.outcome" ||
				row.Provider != string(diagnostics.ProviderClaude) || row.Failure != string(test.want) || row.Source != "" {
				t.Fatalf("journal row = %#v, want claude/%s without a source", row, test.want)
			}
			if row.Level != "error" || row.Result != "error" || row.Kind != "runtime" {
				t.Fatalf("whole failure must be a runtime error row: %#v", row)
			}
			if f.refreshCalls != nil && f.refreshCalls.Load() != f.wantRefreshes {
				t.Fatalf("refresh calls = %d, want %d", f.refreshCalls.Load(), f.wantRefreshes)
			}
			if len(f.wantCredsBytes) > 0 {
				// The refresh path rewrites only the temp credentials file.
				updated, err := os.ReadFile(f.credsPath)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range f.wantCredsBytes {
					if !bytes.Contains(updated, []byte(want)) {
						t.Fatalf("temp credentials were not rewritten with the rotated tokens")
					}
				}
			}

			raw, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			requireClaudeJournalBytesClosed(t, raw, f, dir)
		})
	}
}

// requireClaudeJournalBytesClosed is the negative scan over the whole journal
// file, plus a check that every record carries only the fixed usage fields.
func requireClaudeJournalBytesClosed(t *testing.T, raw []byte, f claudeJournalFixture, dir string) {
	t.Helper()
	forbidden := []string{
		claudeJournalFixtureAccess, claudeJournalFixtureRefresh,
		claudeJournalFixtureRotatedAccess, claudeJournalFixtureRotatedRefresh,
		claudeJournalFixtureBodyMarker, dir, "Bearer", "http://", "127.0.0.1", "claude:",
		"400", "401", "403", "429", "500",
	}
	if f.credsPath != "" {
		forbidden = append(forbidden, f.credsPath, filepath.Base(f.credsPath))
	}
	for _, needle := range forbidden {
		if bytes.Contains(raw, []byte(needle)) {
			t.Fatalf("journal leaked %q: %s", needle, raw)
		}
	}
	wantKeys := []string{
		"at", "component", "duration_ms", "event", "failure", "kind", "level",
		"mux_backend", "provider", "result", "run_id", "version",
	}
	for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
		var record map[string]json.RawMessage
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("journal line is not JSON: %v", err)
		}
		keys := make([]string, 0, len(record))
		for key := range record {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, wantKeys) {
			t.Fatalf("journal record fields = %v, want only %v", keys, wantKeys)
		}
	}
}

// TestUnclassifiedAndNonClaudeWholeFailuresStayCollectFailed keeps the generic
// token for every whole failure without a Claude class: a Claude error whose
// text matches a classified message but carries no class (classification never
// reads text), and Codex failures even when their error wraps a
// Claude class (classification is Claude-only).
func TestUnclassifiedAndNonClaudeWholeFailuresStayCollectFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		adapter  *stubAdapter
		provider diagnostics.Provider
	}{
		{
			"claude error with classified text but no class",
			&stubAdapter{name: "claude", err: errors.New("claude: usage endpoint returned status 429 (backing off)")},
			diagnostics.ProviderClaude,
		},
		{
			"codex error wrapping a claude class",
			&stubAdapter{name: "codex", err: fmt.Errorf("codex: %w", claudeadapter.ErrRateLimited)},
			diagnostics.ProviderCodex,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newJournalHarness(t, test.adapter)
			h.run("--model", test.adapter.name)
			rows := h.tail(10)
			if len(rows) != 1 || rows[0].Provider != string(test.provider) ||
				rows[0].Failure != string(diagnostics.UsageFailureCollect) {
				t.Fatalf("journal rows = %#v, want one %s/collect-failed", rows, test.provider)
			}
		})
	}
}

// TestClaudeFailureClassMappingIsClosedAndDistinct requires every Claude
// failure class to map onto its own non-generic journal token, through any
// amount of wrapping.
func TestClaudeFailureClassMappingIsClosedAndDistinct(t *testing.T) {
	t.Parallel()

	classes := []error{
		claudeadapter.ErrCredentialsUnavailable, claudeadapter.ErrCredentialsTokenEmpty,
		claudeadapter.ErrAuthRejected, claudeadapter.ErrRateLimited, claudeadapter.ErrHTTPStatus,
		claudeadapter.ErrNetwork, claudeadapter.ErrResponseInvalid,
	}
	if len(claudeCollectFailures) != len(classes) {
		t.Fatalf("mapping has %d entries, want %d", len(claudeCollectFailures), len(classes))
	}
	seen := map[diagnostics.UsageFailure]error{}
	for _, class := range classes {
		wrapped := &usage.AdapterError{Model: "claude", Err: fmt.Errorf("outer: %w", class)}
		failure := claudeCollectFailure(wrapped)
		if failure == diagnostics.UsageFailureCollect {
			t.Fatalf("class %v falls back to collect-failed", class)
		}
		if previous, dup := seen[failure]; dup {
			t.Fatalf("classes %v and %v share token %q", previous, class, failure)
		}
		seen[failure] = class
	}
	if got := claudeCollectFailure(errors.New("claude: empty access token")); got != diagnostics.UsageFailureCollect {
		t.Fatalf("unclassified error = %q, want collect-failed", got)
	}
}
