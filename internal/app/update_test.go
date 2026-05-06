package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/version"
)

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testUpdateCommand(t *testing.T, now time.Time) (*updateCommand, string) {
	t.Helper()
	cacheDir := t.TempDir()
	cmd := &updateCommand{
		now:      func() time.Time { return now },
		getenv:   func(string) string { return "" },
		cacheDir: func() (string, error) { return cacheDir, nil },
		client:   http.DefaultClient,
		apiURL:   "https://example.invalid/latest",
	}
	return cmd, cacheDir
}

func TestUpdateStatusUnknownWithoutCache(t *testing.T) {
	t.Parallel()

	cmd, cacheDir := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"current:   " + version.String(),
		"latest:    unknown (unknown)",
		"state:     unknown",
		"installer: unknown - Set PROJMUX_INSTALLER=npm|go|github-release|source",
		filepath.Join(cacheDir, updateCacheFileName),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestUpdateStatusReadsFreshCacheAndInstaller(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	cmd, cacheDir := testUpdateCommand(t, now)
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "go"
		}
		return ""
	}
	writeUpdateCacheFixture(t, cacheDir, updateCache{
		Version:   1,
		CheckedAt: now.Add(-time.Hour),
		TagName:   "v0.4.1",
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"latest:    v0.4.1 (fresh)",
		"state:     update_available",
		"installer: go - Installed with Go tooling",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestUpdateStatusJSONMarksStaleCache(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	cmd, cacheDir := testUpdateCommand(t, now)
	writeUpdateCacheFixture(t, cacheDir, updateCache{
		Version:   1,
		CheckedAt: now.Add(-25 * time.Hour),
		TagName:   "v0.4.0",
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"status", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var st updateStatus
	if err := json.Unmarshal(stdout.Bytes(), &st); err != nil {
		t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
	}
	if st.CacheState != "stale" {
		t.Fatalf("CacheState = %q, want stale", st.CacheState)
	}
	if st.UpdateState != "current" {
		t.Fatalf("UpdateState = %q, want current", st.UpdateState)
	}
	if st.LatestVersion != "v0.4.0" {
		t.Fatalf("LatestVersion = %q, want v0.4.0", st.LatestVersion)
	}
}

func TestUpdateCheckFetchesAndWritesCache(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	cmd, cacheDir := testUpdateCommand(t, now)
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != cmd.apiURL {
			t.Fatalf("request URL = %q, want %q", req.URL.String(), cmd.apiURL)
		}
		if got := req.Header.Get("User-Agent"); !strings.Contains(got, "projmux/") {
			t.Fatalf("User-Agent = %q, want projmux prefix", got)
		}
		body := `{"tag_name":"v0.4.2","name":"v0.4.2","html_url":"https://github.com/crevissepartners/projmux/releases/tag/v0.4.2","published_at":"2026-05-06T10:00:00Z"}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"check"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"latest: v0.4.2",
		"state: update_available",
		"apply: future work; no update was installed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}

	cache := readUpdateCacheFixture(t, cacheDir)
	if cache.TagName != "v0.4.2" {
		t.Fatalf("cache.TagName = %q, want v0.4.2", cache.TagName)
	}
	if !cache.CheckedAt.Equal(now) {
		t.Fatalf("cache.CheckedAt = %v, want %v", cache.CheckedAt, now)
	}
}

func TestUpdateRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: nil, want: "update requires a subcommand"},
		{name: "unknown", args: []string{"bad"}, want: "unknown update subcommand"},
		{name: "status args", args: []string{"status", "extra"}, want: "update status does not accept positional arguments"},
		{name: "check args", args: []string{"check", "extra"}, want: "update check does not accept positional arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cmd.Run(tc.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Run() error = nil, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func writeUpdateCacheFixture(t *testing.T, cacheDir string, cache updateCache) {
	t.Helper()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, updateCacheFileName), data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readUpdateCacheFixture(t *testing.T, cacheDir string) updateCache {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cacheDir, updateCacheFileName))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var cache updateCache
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return cache
}
