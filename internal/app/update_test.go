package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
		now:        func() time.Time { return now },
		getenv:     func(string) string { return "" },
		cacheDir:   func() (string, error) { return cacheDir, nil },
		client:     http.DefaultClient,
		apiURL:     "https://example.invalid/latest",
		executable: func() (string, error) { return "/tmp/projmux", nil },
		goos:       "linux",
		goarch:     "amd64",
		mkdirTemp:  os.MkdirTemp,
		removeAll:  os.RemoveAll,
		rename:     os.Rename,
		chmod:      os.Chmod,
		remove:     os.Remove,
		copyFile:   copyRegularFile,
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
		"installer: go - Installed with Go tooling; update apply delegates to projmux upgrade.",
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
		"apply: run projmux update apply",
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

func TestUpdateApplyDryRunForNPM(t *testing.T) {
	t.Parallel()

	cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "npm"
		}
		return ""
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"would run: npm update -g projmux",
		"would run: projmux tmux apply",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestUpdateApplyRunsNPMCommands(t *testing.T) {
	t.Parallel()

	cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "npm"
		}
		return ""
	}
	var ran []string
	cmd.runExternal = func(name string, args []string, stdout, stderr io.Writer) error {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}

	if err := cmd.Run([]string{"apply"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"npm update -g projmux", "projmux tmux apply"}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
}

func TestUpdateApplyRunsGoUpgradeNoApply(t *testing.T) {
	t.Parallel()

	cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "go"
		}
		return ""
	}
	cmd.executable = func() (string, error) { return "/home/me/bin/projmux", nil }
	var ran []string
	cmd.runExternal = func(name string, args []string, stdout, stderr io.Writer) error {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}

	if err := cmd.Run([]string{"apply", "--no-apply"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"/home/me/bin/projmux upgrade --no-apply"}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
}

func TestUpdateApplyDryRunForGitHubRelease(t *testing.T) {
	t.Parallel()

	cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "github-release"
		}
		return ""
	}
	cmd.executable = func() (string, error) { return "/home/me/bin/projmux", nil }
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("dry-run unexpectedly requested %s", req.URL.String())
		return nil, nil
	})}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"would fetch: https://example.invalid/latest",
		"would download: projmux_latest_linux_amd64.tar.gz",
		"would replace: /home/me/bin/projmux (atomic via temp file)",
		"would run: /home/me/bin/projmux tmux apply",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestUpdateApplyRunsGitHubReleaseReplacement(t *testing.T) {
	t.Parallel()

	cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "github-release"
		}
		return ""
	}
	target := filepath.Join(t.TempDir(), "projmux")
	if err := os.WriteFile(target, []byte("old\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cmd.executable = func() (string, error) { return target, nil }
	assetURL := "https://example.invalid/download/projmux_0.4.2_linux_amd64.tar.gz"
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case cmd.apiURL:
			body := `{"tag_name":"v0.4.2","assets":[{"name":"projmux_0.4.2_linux_amd64.tar.gz","browser_download_url":"` + assetURL + `"}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		case assetURL:
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(testReleaseArchive(t, "new\n"))),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected request URL %q", req.URL.String())
			return nil, nil
		}
	})}
	var ran []string
	cmd.runExternal = func(name string, args []string, stdout, stderr io.Writer) error {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\nstdout:\n%s", err, stdout.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "new\n" {
		t.Fatalf("target content = %q, want new binary", got)
	}
	want := []string{target + " tmux apply"}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
}

func TestFindReleaseAsset(t *testing.T) {
	t.Parallel()

	asset, err := findReleaseAsset(githubRelease{
		TagName: "v0.4.2",
		Assets: []githubReleaseAsset{
			{Name: "projmux_0.4.2_linux_arm64.tar.gz", BrowserDownloadURL: "https://example.invalid/arm64"},
			{Name: "projmux_0.4.2_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.invalid/amd64"},
		},
	}, "linux", "amd64")
	if err != nil {
		t.Fatalf("findReleaseAsset() error = %v", err)
	}
	if asset.BrowserDownloadURL != "https://example.invalid/amd64" {
		t.Fatalf("BrowserDownloadURL = %q, want amd64 asset", asset.BrowserDownloadURL)
	}
}

func TestExtractProjmuxBinaryFromArchive(t *testing.T) {
	t.Parallel()

	dst := filepath.Join(t.TempDir(), "projmux")
	if err := extractProjmuxBinary(bytes.NewReader(testReleaseArchive(t, "binary\n")), dst); err != nil {
		t.Fatalf("extractProjmuxBinary() error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "binary\n" {
		t.Fatalf("extracted content = %q, want binary", got)
	}
}

func TestUpdateApplyRejectsUnsupportedInstallers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		installer string
		want      string
	}{
		{name: "unknown", installer: "", want: "requires PROJMUX_INSTALLER"},
		{name: "source", installer: "source", want: "not supported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
			cmd.getenv = func(name string) string {
				if name == "PROJMUX_INSTALLER" {
					return tc.installer
				}
				return ""
			}
			err := cmd.Run([]string{"apply"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Run() error = nil, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want %q", err, tc.want)
			}
		})
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
		{name: "apply args", args: []string{"apply", "extra"}, want: "update apply does not accept positional arguments"},
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

func testReleaseArchive(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte(content)
	if err := tw.WriteHeader(&tar.Header{
		Name: "projmux_0.4.2_linux_amd64/projmux",
		Mode: 0o755,
		Size: int64(len(data)),
	}); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return buf.Bytes()
}
