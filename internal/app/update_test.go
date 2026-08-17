package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/version"
)

func buildInfoWithVersion(v string) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
	}
}

func TestDetectInstallerAutodetection(t *testing.T) {
	t.Parallel()

	const homeDir = "/home/tester"
	goBin := filepath.Join(homeDir, "go", "bin", "projmux")
	npmBin := "/usr/lib/node_modules/@projmux/linux-x64/bin/projmux"

	tests := []struct {
		name       string
		env        map[string]string
		exe        string
		buildInfo  func() (*debug.BuildInfo, bool)
		wantSource string
		wantNote   string
	}{
		{
			name:       "explicit env wins over path",
			env:        map[string]string{"PROJMUX_INSTALLER": "npm"},
			exe:        goBin,
			buildInfo:  buildInfoWithVersion("v0.7.2"),
			wantSource: "npm",
			wantNote:   "Installed by npm",
		},
		{
			name:       "npm path detected without env",
			exe:        npmBin,
			buildInfo:  buildInfoWithVersion("v0.7.2"),
			wantSource: "npm",
			wantNote:   "Detected npm install",
		},
		{
			name:       "devel build detected as source",
			exe:        goBin,
			buildInfo:  buildInfoWithVersion("(devel)"),
			wantSource: "source",
			wantNote:   "make install",
		},
		{
			name:       "go install in home go bin",
			exe:        goBin,
			buildInfo:  buildInfoWithVersion("v0.7.2"),
			wantSource: "go",
			wantNote:   "Detected `go install`",
		},
		{
			name:       "go install honors GOBIN",
			env:        map[string]string{"GOBIN": "/opt/gobin"},
			exe:        "/opt/gobin/projmux",
			buildInfo:  buildInfoWithVersion("v0.7.2"),
			wantSource: "go",
			wantNote:   "Detected `go install`",
		},
		{
			name:       "unrecognized path stays unknown",
			exe:        "/usr/local/bin/projmux",
			buildInfo:  buildInfoWithVersion("v0.7.2"),
			wantSource: "unknown",
			wantNote:   "Could not detect the installer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
			env := tc.env
			cmd.getenv = func(name string) string { return env[name] }
			cmd.executable = func() (string, error) { return tc.exe, nil }
			cmd.buildInfo = tc.buildInfo
			cmd.userHomeDir = func() (string, error) { return homeDir, nil }

			got := cmd.detectInstaller()
			if got.Source != tc.wantSource {
				t.Fatalf("detectInstaller() source = %q, want %q", got.Source, tc.wantSource)
			}
			if !strings.Contains(got.Note, tc.wantNote) {
				t.Fatalf("detectInstaller() note = %q, want substring %q", got.Note, tc.wantNote)
			}
		})
	}
}

func TestUpdateApplyNpmUsesInstallLatest(t *testing.T) {
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
		ran = append(ran, updateApplyCommand{Name: name, Args: args}.String())
		return nil
	}
	if err := cmd.Run([]string{"apply", "--no-apply"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// --no-apply suppresses the live reload, not the post-update step itself.
	// The new binary still has to run so the keymap schema migration happens;
	// skipping it would leave a v0 keymap under a binary that writes v1.
	want := []string{"npm install -g projmux@latest", "projmux config apply --no-reload"}
	if !slices.Equal(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
}

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
		client: &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("unexpected update request to %s", req.URL.String())
		})},
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
		"installer: unknown - Could not detect the installer. Set PROJMUX_INSTALLER=npm|go|github-release",
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
		TagName:   testVersionTag(t, 1),
	})

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"status"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"latest:    " + testVersionTag(t, 1) + " (fresh)",
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
		TagName:   testVersionTag(t, 0),
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
	if want := testVersionTag(t, 0); st.LatestVersion != want {
		t.Fatalf("LatestVersion = %q, want %s", st.LatestVersion, want)
	}
}

func TestUpdateCheckFetchesAndWritesCache(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	cmd, cacheDir := testUpdateCommand(t, now)
	latest := testVersionTag(t, 2)
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != cmd.apiURL {
			t.Fatalf("request URL = %q, want %q", req.URL.String(), cmd.apiURL)
		}
		if got := req.Header.Get("User-Agent"); !strings.Contains(got, "projmux/") {
			t.Fatalf("User-Agent = %q, want projmux prefix", got)
		}
		body := fmt.Sprintf(`{"tag_name":%q,"name":%q,"html_url":"https://github.com/crevissepartners/projmux/releases/tag/%s","published_at":"2026-05-06T10:00:00Z"}`, latest, latest, latest)
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
		"latest: " + latest,
		"state: update_available",
		"apply: run projmux update apply",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}

	cache := readUpdateCacheFixture(t, cacheDir)
	if cache.TagName != latest {
		t.Fatalf("cache.TagName = %q, want %s", cache.TagName, latest)
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
		"would run: npm install -g projmux@latest",
		"would run: projmux config apply",
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
	want := []string{"npm install -g projmux@latest", "projmux config apply"}
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
		"would run: /home/me/bin/projmux config apply",
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
	assetURL := "https://github.com/crevissepartners/projmux/releases/download/v0.4.2/projmux_0.4.2_linux_amd64.tar.gz"
	archive := testReleaseArchive(t, "new\n")
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case cmd.apiURL:
			body := `{"tag_name":"v0.4.2","assets":[{"name":"projmux_0.4.2_linux_amd64.tar.gz","browser_download_url":"` + assetURL + `","digest":"` + testReleaseDigest(archive) + `"}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		case assetURL:
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(archive)),
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
	want := []string{target + " config apply"}
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
	if err := extractProjmuxBinaryWithLimits(testReleaseArchive(t, "binary\n"), dst, defaultUpdateArchiveLimits()); err != nil {
		t.Fatalf("extractProjmuxBinaryWithLimits() error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "binary\n" {
		t.Fatalf("extracted content = %q, want binary", got)
	}
}

func TestUpdateApplyRejectsUnsafeReleaseArtifactsWithoutReplacement(t *testing.T) {
	t.Parallel()

	archive := testReleaseArchive(t, "new\n")
	compressionBomb := testTarArchive(t, []testTarEntry{
		{name: "padding", data: bytes.Repeat([]byte("a"), 4096)},
		{name: "projmux", data: []byte("new\n")},
	})
	tests := []struct {
		name        string
		assetURL    string
		digest      string
		limits      updateArchiveLimits
		assetBody   []byte
		want        string
		wantRequest bool
	}{
		{
			name:        "compressed byte limit",
			assetURL:    "https://github.com/crevissepartners/projmux/releases/download/v0.4.2/projmux_0.4.2_linux_amd64.tar.gz",
			digest:      testReleaseDigest(archive),
			limits:      updateArchiveLimits{compressedBytes: int64(len(archive) - 1)},
			assetBody:   archive,
			want:        "compressed size exceeds limit",
			wantRequest: true,
		},
		{
			name:     "expanded tar byte limit",
			assetURL: "https://github.com/crevissepartners/projmux/releases/download/v0.4.2/projmux_0.4.2_linux_amd64.tar.gz",
			digest:   testReleaseDigest(compressionBomb),
			limits: updateArchiveLimits{
				compressedBytes:   1 << 20,
				tarBytes:          1024,
				totalRegularBytes: 8192,
				regularFileBytes:  8192,
				entries:           10,
			},
			assetBody:   compressionBomb,
			want:        "extracted byte limit",
			wantRequest: true,
		},
		{
			name:        "checksum mismatch",
			assetURL:    "https://github.com/crevissepartners/projmux/releases/download/v0.4.2/projmux_0.4.2_linux_amd64.tar.gz",
			digest:      "sha256:" + strings.Repeat("0", 64),
			assetBody:   archive,
			want:        "sha256 digest mismatch",
			wantRequest: true,
		},
		{
			name:        "missing checksum",
			assetURL:    "https://github.com/crevissepartners/projmux/releases/download/v0.4.2/projmux_0.4.2_linux_amd64.tar.gz",
			assetBody:   archive,
			want:        "digest is missing",
			wantRequest: true,
		},
		{
			name:      "disallowed host",
			assetURL:  "https://example.invalid/projmux_0.4.2_linux_amd64.tar.gz",
			digest:    testReleaseDigest(archive),
			assetBody: archive,
			want:      `host "example.invalid" is not allowed`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
			cmd.getenv = func(name string) string {
				if name == "PROJMUX_INSTALLER" {
					return "github-release"
				}
				return ""
			}
			parent := t.TempDir()
			target := filepath.Join(parent, "projmux")
			if err := os.WriteFile(target, []byte("old\n"), 0o755); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			cmd.executable = func() (string, error) { return target, nil }
			cmd.limits = tc.limits
			assetRequested := false
			cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() == cmd.apiURL {
					body := `{"tag_name":"v0.4.2","assets":[{"name":"projmux_0.4.2_linux_amd64.tar.gz","browser_download_url":"` + tc.assetURL + `","digest":"` + tc.digest + `"}]}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				}
				if req.URL.String() == tc.assetURL {
					assetRequested = true
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(tc.assetBody)),
						Header:     make(http.Header),
					}, nil
				}
				t.Fatalf("unexpected request URL %q", req.URL.String())
				return nil, nil
			})}
			replaced := false
			cmd.rename = func(oldpath, newpath string) error {
				replaced = true
				return os.Rename(oldpath, newpath)
			}

			err := cmd.Run([]string{"apply", "--no-apply"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want %q", err, tc.want)
			}
			if assetRequested != tc.wantRequest {
				t.Fatalf("asset requested = %v, want %v", assetRequested, tc.wantRequest)
			}
			if replaced {
				t.Fatal("atomic replacement ran after rejected release asset")
			}
			got, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatalf("ReadFile() error = %v", readErr)
			}
			if string(got) != "old\n" {
				t.Fatalf("target content = %q, want old binary", got)
			}
			scratch, globErr := filepath.Glob(filepath.Join(parent, ".projmux-release-*"))
			if globErr != nil {
				t.Fatalf("Glob() error = %v", globErr)
			}
			if len(scratch) != 0 {
				t.Fatalf("scratch artifacts remain: %v", scratch)
			}
		})
	}
}

func TestUpdateApplyRejectsDisallowedRedirect(t *testing.T) {
	t.Parallel()

	cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "github-release"
		}
		return ""
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "projmux")
	if err := os.WriteFile(target, []byte("old\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cmd.executable = func() (string, error) { return target, nil }
	assetURL := "https://github.com/crevissepartners/projmux/releases/download/v0.4.2/projmux_0.4.2_linux_amd64.tar.gz"
	disallowedURL := "https://example.invalid/download"
	disallowedRequested := false
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case cmd.apiURL:
			body := `{"tag_name":"v0.4.2","assets":[{"name":"projmux_0.4.2_linux_amd64.tar.gz","browser_download_url":"` + assetURL + `","digest":"sha256:` + strings.Repeat("0", 64) + `"}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		case assetURL:
			return &http.Response{
				StatusCode: http.StatusFound,
				Body:       io.NopCloser(strings.NewReader("redirect")),
				Header:     http.Header{"Location": []string{disallowedURL}},
				Request:    req,
			}, nil
		case disallowedURL:
			disallowedRequested = true
			return nil, errors.New("disallowed redirect was followed")
		default:
			t.Fatalf("unexpected request URL %q", req.URL.String())
			return nil, nil
		}
	})}

	err := cmd.Run([]string{"apply", "--no-apply"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `redirect URL: host "example.invalid" is not allowed`) {
		t.Fatalf("Run() error = %v, want redirect host rejection", err)
	}
	if disallowedRequested {
		t.Fatal("HTTP client followed a disallowed redirect")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(got) != "old\n" {
		t.Fatalf("target content = %q, want old binary", got)
	}
	scratch, globErr := filepath.Glob(filepath.Join(parent, ".projmux-release-*"))
	if globErr != nil {
		t.Fatalf("Glob() error = %v", globErr)
	}
	if len(scratch) != 0 {
		t.Fatalf("scratch artifacts remain: %v", scratch)
	}
}

func TestReleaseAssetRequestPreservesExistingRedirectPolicy(t *testing.T) {
	t.Parallel()

	const (
		assetURL    = "https://github.com/crevissepartners/projmux/releases/download/v0.4.2/projmux_0.4.2_linux_amd64.tar.gz"
		redirectURL = "https://release-assets.githubusercontent.com/download"
	)
	redirectRequested := false
	cmd := &updateCommand{
		client: &http.Client{
			Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case assetURL:
					return &http.Response{
						StatusCode: http.StatusFound,
						Body:       io.NopCloser(strings.NewReader("redirect")),
						Header:     http.Header{"Location": []string{redirectURL}},
						Request:    req,
					}, nil
				case redirectURL:
					redirectRequested = true
					return nil, errors.New("existing redirect policy was bypassed")
				default:
					t.Fatalf("unexpected request URL %q", req.URL.String())
					return nil, nil
				}
			}),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return errors.New("existing redirect policy rejected request")
			},
		},
	}
	req, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if _, err := cmd.doReleaseAssetRequest(req); err == nil || !strings.Contains(err.Error(), "existing redirect policy rejected request") {
		t.Fatalf("doReleaseAssetRequest() error = %v, want existing policy rejection", err)
	}
	if redirectRequested {
		t.Fatal("redirect bypassed the existing client policy")
	}
}

func TestExtractProjmuxBinaryEnforcesArchiveLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []testTarEntry
		limits  updateArchiveLimits
		want    string
	}{
		{
			name: "expanded tar bytes",
			entries: []testTarEntry{
				{name: "padding", data: bytes.Repeat([]byte("a"), 4096)},
				{name: "projmux", data: []byte("binary")},
			},
			limits: updateArchiveLimits{compressedBytes: 1 << 20, tarBytes: 1024, totalRegularBytes: 8192, regularFileBytes: 8192, entries: 10},
			want:   "extracted byte limit",
		},
		{
			name: "entry count",
			entries: []testTarEntry{
				{name: "one", data: []byte("1")},
				{name: "two", data: []byte("2")},
				{name: "projmux", data: []byte("binary")},
			},
			limits: updateArchiveLimits{compressedBytes: 1 << 20, tarBytes: 1 << 20, totalRegularBytes: 8192, regularFileBytes: 8192, entries: 2},
			want:   "more than 2 entries",
		},
		{
			name: "regular file size",
			entries: []testTarEntry{
				{name: "projmux", data: bytes.Repeat([]byte("b"), 16)},
			},
			limits: updateArchiveLimits{compressedBytes: 1 << 20, tarBytes: 1 << 20, totalRegularBytes: 8192, regularFileBytes: 8, entries: 10},
			want:   "regular file",
		},
		{
			name: "total regular file size",
			entries: []testTarEntry{
				{name: "one", data: bytes.Repeat([]byte("a"), 700)},
				{name: "projmux", data: bytes.Repeat([]byte("b"), 700)},
			},
			limits: updateArchiveLimits{compressedBytes: 1 << 20, tarBytes: 1 << 20, totalRegularBytes: 1024, regularFileBytes: 800, entries: 10},
			want:   "regular file bytes exceed total limit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dst := filepath.Join(t.TempDir(), "projmux")
			err := extractProjmuxBinaryWithLimits(testTarArchive(t, tc.entries), dst, tc.limits)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("extractProjmuxBinaryWithLimits() error = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("extracted destination stat error = %v, want not exist", statErr)
			}
		})
	}
}

func TestExtractProjmuxBinaryRemovesOutputAfterLateArchiveFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		archive func(*testing.T) []byte
		want    string
	}{
		{
			name: "gzip checksum after binary",
			archive: func(t *testing.T) []byte {
				archive := testReleaseArchive(t, "binary")
				archive[len(archive)-1] ^= 0xff
				return archive
			},
			want: "checksum",
		},
		{
			name: "duplicate binary",
			archive: func(t *testing.T) []byte {
				return testTarArchive(t, []testTarEntry{
					{name: "first/projmux", data: []byte("one")},
					{name: "second/projmux", data: []byte("two")},
				})
			},
			want: "multiple projmux binaries",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dst := filepath.Join(t.TempDir(), "projmux")
			err := extractProjmuxBinaryWithLimits(tc.archive(t), dst, defaultUpdateArchiveLimits())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("extractProjmuxBinaryWithLimits() error = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("extracted destination stat error = %v, want not exist", statErr)
			}
		})
	}
}

func TestVerifyReleaseAssetDigestRejectsMissingMalformedAndUnsupported(t *testing.T) {
	t.Parallel()

	for _, digest := range []string{"", "sha512:" + strings.Repeat("0", 128), "sha256:not-hex"} {
		if err := verifyReleaseAssetDigest([]byte("archive"), digest); err == nil {
			t.Fatalf("verifyReleaseAssetDigest(%q) error = nil", digest)
		}
	}
}

func TestUpdateApplyRejectsUnsupportedInstallers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		installer string
		want      string
	}{
		{name: "unknown", installer: "", want: "could not detect a supported installer"},
		{name: "source", installer: "source", want: "not available for source installs"},
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
	return testTarArchive(t, []testTarEntry{{
		name: "projmux_0.4.2_linux_amd64/projmux",
		data: []byte(content),
	}})
}

type testTarEntry struct {
	name string
	data []byte
}

func testTarArchive(t *testing.T, entries []testTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: entry.name,
			Mode: 0o755,
			Size: int64(len(entry.data)),
		}); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return buf.Bytes()
}

func testReleaseDigest(archive []byte) string {
	sum := sha256.Sum256(archive)
	return fmt.Sprintf("sha256:%x", sum)
}

func testVersionTag(t *testing.T, patchDelta int) string {
	t.Helper()
	parts, ok := parseUpdateVersion(version.String())
	if !ok {
		t.Fatalf("cannot parse current version %q", version.String())
	}
	parts[2] += patchDelta
	if parts[2] < 0 {
		t.Fatalf("invalid patch delta %d for current version %q", patchDelta, version.String())
	}
	return fmt.Sprintf("v%d.%d.%d", parts[0], parts[1], parts[2])
}
