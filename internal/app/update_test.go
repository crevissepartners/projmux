package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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

// stubUpdateVersionProbe reports one version for the pre-publication reading and
// another for every reading taken after it.
func stubUpdateVersionProbe(before, after string) func(string) (string, error) {
	reads := 0
	return func(string) (string, error) {
		reads++
		if reads == 1 {
			return "projmux " + before + "\n", nil
		}
		return "projmux " + after + "\n", nil
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
		npmURL:     "https://example.invalid/npm/projmux",
		executable: func() (string, error) { return "/tmp/projmux", nil },
		// Every pre-existing apply test describes an ordinary landed upgrade, so
		// the default probe reports a higher version after publication.
		probeVersion: stubUpdateVersionProbe("0.13.0", "0.13.1"),
		lookPath: func(name string) (string, error) {
			if name != "projmux" {
				return "", fmt.Errorf("unexpected executable lookup %q", name)
			}
			return "/npm/bin/projmux", nil
		},
		goos:      "linux",
		goarch:    "amd64",
		mkdirTemp: os.MkdirTemp,
		removeAll: os.RemoveAll,
		rename:    os.Rename,
		chmod:     os.Chmod,
		remove:    os.Remove,
		copyFile:  copyRegularFile,
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
		"installer: go - Installed with Go tooling; update apply runs `go install ...@latest` and applies canonical config.",
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
		"would run: /tmp/projmux config apply --bin /npm/bin/projmux --socket projmux",
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
	want := []string{
		"/tmp/projmux config apply --bin /npm/bin/projmux --socket projmux",
		"npm install -g projmux@latest",
		"projmux config apply",
	}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
}

func TestUpdateApplyPublicationFailureContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		failAt     int
		wantCalls  int
		wantDetail string
	}{
		{name: "pre-apply prevents publication", failAt: 0, wantCalls: 1, wantDetail: "binary publication not started"},
		{name: "installer failure is not success", failAt: 1, wantCalls: 2, wantDetail: "binary publication failed; update not successful"},
		{name: "post-verify failure is not success", failAt: 2, wantCalls: 3, wantDetail: "post-publication convergence failed; update not successful"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
				if len(ran)-1 == tc.failAt {
					return errors.New("injected stage failure")
				}
				return nil
			}

			err := cmd.Run([]string{"apply"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.wantDetail) ||
				!strings.Contains(err.Error(), "projmux config apply --socket projmux") {
				t.Fatalf("Run() error = %v, want stage detail and exact remediation", err)
			}
			if len(ran) != tc.wantCalls {
				t.Fatalf("ran = %#v, want %d calls before stop", ran, tc.wantCalls)
			}
		})
	}
}

func TestUpdateApplyNoApplySkipsLiveConvergenceAndRequiresExplicitApply(t *testing.T) {
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
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply", "--no-apply"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"npm install -g projmux@latest",
		"projmux config apply --no-reload",
	}
	if !slices.Equal(ran, want) {
		t.Fatalf("ran = %#v, want zero-live-write sequence %#v", ran, want)
	}
	if !strings.Contains(stdout.String(), "live tmux unchanged; explicit apply required: run `projmux config apply --socket projmux`") {
		t.Fatalf("stdout = %q, want explicit apply-required state", stdout.String())
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
	want := []string{
		"go install github.com/crevissepartners/projmux/cmd/projmux@latest",
		"/home/me/bin/projmux config apply --no-reload",
	}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
}

func TestUpdateApplyRunsGoUpgradeInPublicationOrder(t *testing.T) {
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

	if err := cmd.Run([]string{"apply"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{
		"/home/me/bin/projmux config apply --bin /home/me/bin/projmux --socket projmux",
		"go install github.com/crevissepartners/projmux/cmd/projmux@latest",
		"/home/me/bin/projmux config apply",
	}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want exact pre-converge/publication/post-verify order %#v", ran, want)
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
		"would run before replacement: /home/me/bin/projmux config apply --bin /home/me/bin/projmux --socket projmux",
		"would run: /home/me/bin/projmux config apply",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestUpdateApplyRunsGitHubReleaseReplacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		args              []string
		wantCommands      func(string) []string
		wantExplicitApply bool
	}{
		{
			name: "normal pre-converges and post-verifies",
			args: []string{"apply"},
			wantCommands: func(target string) []string {
				return []string{
					target + " config apply --bin " + target + " --socket projmux",
					target + " config apply",
				}
			},
		},
		{
			name: "no-apply performs no live preapply and requires explicit apply",
			args: []string{"apply", "--no-apply"},
			wantCommands: func(target string) []string {
				return []string{target + " config apply --no-reload"}
			},
			wantExplicitApply: true,
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
			if err := cmd.Run(tc.args, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v\nstdout:\n%s", err, stdout.String())
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if string(got) != "new\n" {
				t.Fatalf("target content = %q, want new binary", got)
			}
			want := tc.wantCommands(target)
			if !equalStrings(ran, want) {
				t.Fatalf("ran = %#v, want %#v", ran, want)
			}
			gotExplicitApply := strings.Contains(stdout.String(),
				"live tmux unchanged; explicit apply required: run `projmux config apply --socket projmux`")
			if gotExplicitApply != tc.wantExplicitApply {
				t.Fatalf("explicit apply state = %v, want %v; stdout=%q", gotExplicitApply, tc.wantExplicitApply, stdout.String())
			}
		})
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

func testCurrentVersionTag(t *testing.T) string {
	t.Helper()
	return testVersionTag(t, 0)
}

// updateApplyVerificationCommand builds an apply command whose installer is
// fixed and whose staged commands all succeed, so the only thing under test is
// the post-publication version verification.
func updateApplyVerificationCommand(t *testing.T, installer string) (*updateCommand, string, *[]string) {
	t.Helper()
	cmd, cacheDir := testUpdateCommand(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return installer
		}
		return ""
	}
	cmd.executable = func() (string, error) { return "/home/me/bin/projmux", nil }
	ran := &[]string{}
	cmd.runExternal = func(name string, args []string, stdout, stderr io.Writer) error {
		*ran = append(*ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	return cmd, cacheDir, ran
}

// TestUpdateApplyFailsWhenTheInstalledVersionDidNotChange pins C-2 Guarantee:
// every stage exiting 0 is not an upgrade. A reinstall of the same version --
// what happens while a release exists on GitHub but not yet on the channel this
// install pulls from -- must end as an explicit failure.
func TestUpdateApplyFailsWhenTheInstalledVersionDidNotChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		installer string
		args      []string
		before    string
		after     string
		wantParts []string
	}{
		{
			name:      "npm reinstalls the same version",
			installer: "npm",
			args:      []string{"apply"},
			before:    "0.13.1",
			after:     "0.13.1",
			wantParts: []string{
				"installed version did not change",
				"current version 0.13.1 at /npm/bin/projmux",
				"expected version v0.13.2",
				"install channel npm",
				"not on the npm registry yet",
			},
		},
		{
			name:      "go reinstalls the same version",
			installer: "go",
			args:      []string{"apply"},
			before:    "0.13.1",
			after:     "0.13.1",
			wantParts: []string{
				"installed version did not change",
				"expected version v0.13.2",
				"install channel go",
				"GOBIN that PATH does not resolve first",
			},
		},
		{
			name:      "no-apply is verified too",
			installer: "npm",
			args:      []string{"apply", "--no-apply"},
			before:    "0.13.1",
			after:     "0.13.1",
			wantParts: []string{
				"installed version did not change",
				"install channel npm",
			},
		},
		{
			name:      "already at the expected version publishes nothing",
			installer: "npm",
			args:      []string{"apply"},
			before:    "0.13.2",
			after:     "0.13.2",
			wantParts: []string{
				"installed version did not change",
				"current version 0.13.2",
				"already holds the expected version",
			},
		},
		{
			name:      "a lower version afterwards is not success",
			installer: "npm",
			args:      []string{"apply"},
			before:    "0.13.1",
			after:     "0.13.0",
			wantParts: []string{
				"installed version went backwards",
				"current version 0.13.0 at /npm/bin/projmux",
				"expected version v0.13.2",
				"install channel npm",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, cacheDir, ran := updateApplyVerificationCommand(t, tc.installer)
			writeUpdateCacheFixture(t, cacheDir, updateCache{
				Version:   1,
				CheckedAt: time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC),
				Source:    availabilitySourceForInstaller(tc.installer),
				TagName:   "v0.13.2",
			})
			cmd.probeVersion = stubUpdateVersionProbe(tc.before, tc.after)

			err := cmd.Run(tc.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Run() error = nil, want an explicit failure; ran = %#v", *ran)
			}
			for _, want := range tc.wantParts {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

// TestUpdateApplyWithoutACachedCheckStillNamesTheMissingExpectedVersion keeps the
// failure message complete when no release check has been cached: the expected
// slot is filled with an explicit unknown and a way to resolve it, never
// dropped.
func TestUpdateApplyWithoutACachedCheckStillNamesTheMissingExpectedVersion(t *testing.T) {
	t.Parallel()

	cmd, _, _ := updateApplyVerificationCommand(t, "npm")
	cmd.probeVersion = stubUpdateVersionProbe("0.13.1", "0.13.1")

	err := cmd.Run([]string{"apply"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("Run() error = nil, want an explicit failure")
	}
	for _, want := range []string{
		"expected version unknown (no cached release check; run `projmux update check`)",
		"install channel npm",
		"current version 0.13.1",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestUpdateApplySucceedsWhenTheInstalledVersionRose keeps a real upgrade
// passing and names the exact executable that was verified.
func TestUpdateApplySucceedsWhenTheInstalledVersionRose(t *testing.T) {
	t.Parallel()

	cmd, cacheDir, _ := updateApplyVerificationCommand(t, "npm")
	writeUpdateCacheFixture(t, cacheDir, updateCache{
		Version:   1,
		CheckedAt: time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC),
		Source:    updateSourceNPMRegistry,
		TagName:   "v0.13.2",
	})
	cmd.probeVersion = stubUpdateVersionProbe("0.13.1", "0.13.2")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := ">> verified: projmux 0.13.2 is now the active executable at /npm/bin/projmux (was 0.13.1)"
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

// TestUpdateApplyRefusesToReportSuccessWhenTheVersionCannotBeRead covers the
// negative path: an unreadable version is reported as unverified, never
// disguised as a successful upgrade.
func TestUpdateApplyRefusesToReportSuccessWhenTheVersionCannotBeRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		probe func(string) (string, error)
		want  string
	}{
		{
			name:  "the probe itself fails after publication",
			probe: failingUpdateVersionProbeAfter(1, errors.New("exec format error")),
			want:  "exec format error",
		},
		{
			name:  "the probe fails before publication",
			probe: failingUpdateVersionProbeAfter(0, errors.New("permission denied")),
			want:  "permission denied",
		},
		{
			name: "the output is not a projmux version line",
			probe: func(string) (string, error) {
				return "some other tool 1.2.3\n", nil
			},
			want: "parse the version reported by",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, _, _ := updateApplyVerificationCommand(t, "npm")
			cmd.probeVersion = tc.probe

			var stdout bytes.Buffer
			err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{})
			if err == nil {
				t.Fatal("Run() error = nil, want an explicit unverified failure")
			}
			for _, want := range []string{
				"installed version could not be verified, so it is not reported as success",
				"install channel npm",
				tc.want,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err.Error(), want)
				}
			}
			if strings.Contains(stdout.String(), ">> verified:") {
				t.Fatalf("stdout = %q, want no verified line", stdout.String())
			}
		})
	}
}

// TestUpdateApplyVerifiesThePathResolvedExecutable pins the comparison target:
// the binary PATH resolves, not whatever the package manager reports. That is
// the only reading that catches an install which succeeded but landed somewhere
// else.
func TestUpdateApplyVerifiesThePathResolvedExecutable(t *testing.T) {
	t.Parallel()

	cmd, cacheDir, ran := updateApplyVerificationCommand(t, "npm")
	writeUpdateCacheFixture(t, cacheDir, updateCache{
		Version:   1,
		CheckedAt: time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC),
		Source:    updateSourceNPMRegistry,
		TagName:   "v0.13.2",
	})
	var probed []string
	cmd.probeVersion = func(exe string) (string, error) {
		probed = append(probed, exe)
		// npm exited 0 and published 0.13.2 under a prefix PATH does not
		// resolve, so the executable a user runs is still the old one.
		return "projmux 0.13.1\n", nil
	}

	err := cmd.Run([]string{"apply"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("Run() error = nil, want a failure for an install PATH cannot see")
	}
	if !strings.Contains(err.Error(), "outside the PATH entry that resolves `projmux`") {
		t.Fatalf("error %q does not name the misplaced-install cause", err.Error())
	}
	want := []string{"/npm/bin/projmux", "/npm/bin/projmux"}
	if !equalStrings(probed, want) {
		t.Fatalf("probed = %#v, want the PATH-resolved executable twice %#v", probed, want)
	}
	for _, command := range *ran {
		if strings.Contains(command, " version") || strings.Contains(command, "npm view") || strings.Contains(command, "npm ls") {
			t.Fatalf("ran = %#v, want no installer version report", *ran)
		}
	}
}

// TestUpdateApplyFallsBackToTheRunningExecutableWhenPathHasNoProjmux keeps an
// off-PATH install verifiable instead of permanently unverified.
func TestUpdateApplyFallsBackToTheRunningExecutableWhenPathHasNoProjmux(t *testing.T) {
	t.Parallel()

	cmd, _, _ := updateApplyVerificationCommand(t, "go")
	cmd.lookPath = func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }
	var probed []string
	cmd.probeVersion = func(exe string) (string, error) {
		probed = append(probed, exe)
		if len(probed) == 1 {
			return "projmux 0.13.1\n", nil
		}
		return "projmux 0.13.2\n", nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"/home/me/bin/projmux", "/home/me/bin/projmux"}
	if !equalStrings(probed, want) {
		t.Fatalf("probed = %#v, want the running executable %#v", probed, want)
	}
	if !strings.Contains(stdout.String(), ">> verified: projmux 0.13.2 is now the active executable at /home/me/bin/projmux") {
		t.Fatalf("stdout = %q, want the fallback executable verified", stdout.String())
	}
}

// TestUpdateApplyStageOrderIsUnchangedByVersionVerification is the change-nothing
// half of Phase 0: verification is a reading, so the published command sequence
// -- including the `config apply` stage and its position -- is byte-identical to
// what it was before.
func TestUpdateApplyStageOrderIsUnchangedByVersionVerification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		installer string
		args      []string
		want      []string
	}{
		{
			name:      "npm",
			installer: "npm",
			args:      []string{"apply"},
			want: []string{
				"/home/me/bin/projmux config apply --bin /npm/bin/projmux --socket projmux",
				"npm install -g projmux@latest",
				"projmux config apply",
			},
		},
		{
			name:      "npm no-apply",
			installer: "npm",
			args:      []string{"apply", "--no-apply"},
			want: []string{
				"npm install -g projmux@latest",
				"projmux config apply --no-reload",
			},
		},
		{
			name:      "go",
			installer: "go",
			args:      []string{"apply"},
			want: []string{
				"/home/me/bin/projmux config apply --bin /home/me/bin/projmux --socket projmux",
				"go install github.com/crevissepartners/projmux/cmd/projmux@latest",
				"/home/me/bin/projmux config apply",
			},
		},
		{
			name:      "go no-apply",
			installer: "go",
			args:      []string{"apply", "--no-apply"},
			want: []string{
				"go install github.com/crevissepartners/projmux/cmd/projmux@latest",
				"/home/me/bin/projmux config apply --no-reload",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, _, ran := updateApplyVerificationCommand(t, tc.installer)
			cmd.probeVersion = stubUpdateVersionProbe("0.13.1", "0.13.2")

			if err := cmd.Run(tc.args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !equalStrings(*ran, tc.want) {
				t.Fatalf("ran = %#v, want the unchanged stage order %#v", *ran, tc.want)
			}
		})
	}
}

// TestUpdateApplyDryRunPreviewsVerificationWithoutProbing keeps `--dry-run` a
// pure preview: it names the verification stage and reads nothing.
func TestUpdateApplyDryRunPreviewsVerificationWithoutProbing(t *testing.T) {
	t.Parallel()

	for _, installer := range []string{"npm", "go", "github-release"} {
		t.Run(installer, func(t *testing.T) {
			t.Parallel()

			cmd, _, _ := updateApplyVerificationCommand(t, installer)
			probes := 0
			cmd.probeVersion = func(string) (string, error) {
				probes++
				return "projmux 0.13.1\n", nil
			}

			var stdout bytes.Buffer
			if err := cmd.Run([]string{"apply", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !strings.Contains(stdout.String(), "would verify: the projmux that PATH resolves reports a higher version") {
				t.Fatalf("stdout = %q, want the verification preview", stdout.String())
			}
			if probes != 0 {
				t.Fatalf("probes = %d, want a dry run to read nothing", probes)
			}
		})
	}
}

func TestParseProjmuxVersionOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "plain line", raw: "projmux 0.13.1\n", want: "0.13.1", ok: true},
		{name: "tagged line", raw: "projmux v0.14.0\n", want: "v0.14.0", ok: true},
		{name: "skips leading noise", raw: "warning: something\nprojmux 0.13.1\n", want: "0.13.1", ok: true},
		{name: "other tool", raw: "npm 10.8.2\n", ok: false},
		{name: "no version token", raw: "projmux \n", ok: false},
		{name: "unparseable version", raw: "projmux dev-build\n", ok: false},
		{name: "empty", raw: "", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseProjmuxVersionOutput(tc.raw)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("parseProjmuxVersionOutput(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// failingUpdateVersionProbeAfter succeeds for the first `okReads` readings and
// fails afterwards.
func failingUpdateVersionProbeAfter(okReads int, cause error) func(string) (string, error) {
	reads := 0
	return func(string) (string, error) {
		reads++
		if reads > okReads {
			return "", cause
		}
		return "projmux 0.13.1\n", nil
	}
}

// updateAvailabilityResponder answers whichever availability authority the
// command decides to ask, and fails the test if it asks the wrong one.
//
// Both channels are served by a single client so the request timeout and the
// redirect ceiling the shell gate budgets for stay identical across them; a
// second client here would hide a divergence the gate would pay for.
func updateAvailabilityResponder(t *testing.T, cmd *updateCommand, githubTag, npmVersion string) *http.Client {
	t.Helper()
	return &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch req.URL.String() {
		case cmd.releaseAPIURL():
			if githubTag == "" {
				t.Fatalf("unexpected GitHub release request for a channel that must not ask it")
			}
			body = fmt.Sprintf(`{"tag_name":%q,"name":%q,"html_url":"https://github.com/crevissepartners/projmux/releases/tag/%s","published_at":"2026-05-06T10:00:00Z"}`, githubTag, githubTag, githubTag)
		case cmd.npmRegistryAPIURL():
			if npmVersion == "" {
				t.Fatalf("unexpected npm registry request for a channel that must not ask it")
			}
			body = fmt.Sprintf(`{"dist-tags":{"latest":%q}}`, npmVersion)
		default:
			t.Fatalf("unexpected availability request to %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
}

func updateCommandForInstaller(t *testing.T, now time.Time, installer string) (*updateCommand, string) {
	t.Helper()
	cmd, cacheDir := testUpdateCommand(t, now)
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return installer
		}
		return ""
	}
	return cmd, cacheDir
}

// TestUpdateCheckPicksTheAvailabilitySourceFromTheInstallChannel pins C-1
// Guarantee at its root: the authority asked is the one that will perform the
// install. npm asks the registry, every other channel keeps the GitHub release.
func TestUpdateCheckPicksTheAvailabilitySourceFromTheInstallChannel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		installer  string
		wantSource string
	}{
		{name: "npm asks the registry", installer: "npm", wantSource: updateSourceNPMRegistry},
		{name: "go keeps the GitHub release", installer: "go", wantSource: updateSourceGitHubRelease},
		{name: "github-release keeps the GitHub release", installer: "github-release", wantSource: updateSourceGitHubRelease},
		{name: "source builds keep the GitHub release", installer: "source", wantSource: updateSourceGitHubRelease},
		{name: "an undetected channel keeps the GitHub release", installer: "", wantSource: updateSourceGitHubRelease},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, cacheDir := updateCommandForInstaller(t, now, tc.installer)
			githubTag, npmVersion := testVersionTag(t, 1), ""
			if tc.wantSource == updateSourceNPMRegistry {
				githubTag, npmVersion = "", strings.TrimPrefix(testVersionTag(t, 1), "v")
			}
			cmd.client = updateAvailabilityResponder(t, cmd, githubTag, npmVersion)

			if err := cmd.Run([]string{"check"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			cache, ok, err := cmd.loadCache()
			if err != nil || !ok {
				t.Fatalf("loadCache() = %v, %v, %v", cache, ok, err)
			}
			if cache.Source != tc.wantSource {
				t.Fatalf("cache Source = %q, want %q", cache.Source, tc.wantSource)
			}
			// Whichever authority answered, one version string format reaches
			// the gate.
			if want := testVersionTag(t, 1); cache.TagName != want {
				t.Fatalf("cache TagName = %q, want %q", cache.TagName, want)
			}
			raw, err := os.ReadFile(filepath.Join(cacheDir, updateCacheFileName))
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if !strings.Contains(string(raw), `"source": "`+tc.wantSource+`"`) {
				t.Fatalf("cache file = %s, want a recorded source %q", raw, tc.wantSource)
			}
		})
	}
}

// TestShellGateWaitsForTheNPMRegistryToPublish is the reported symptom, both
// halves of it: no `u` while only GitHub has the version, and `u` as soon as
// npm does. The GitHub authority is not even reachable here, so an offer can
// only come from the registry.
func TestShellGateWaitsForTheNPMRegistryToPublish(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		npmVersion  string
		wantPrompt  bool
		wantState   string
		wantLatest  string
		description string
	}{
		{
			name:        "npm has not published the release yet",
			npmVersion:  strings.TrimPrefix(testCurrentVersionTag(t), "v"),
			wantPrompt:  false,
			wantState:   "current",
			wantLatest:  testCurrentVersionTag(t),
			description: "GitHub is ahead, but nothing offered is installable here",
		},
		{
			name:       "npm published the release",
			npmVersion: "0.99.0",
			wantPrompt: true,
			wantState:  "update_available",
			wantLatest: "v0.99.0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, _ := updateCommandForInstaller(t, now, "npm")
			cmd.client = updateAvailabilityResponder(t, cmd, "", tc.npmVersion)

			if err := cmd.refreshCacheIfNeeded(context.Background()); err != nil {
				t.Fatalf("refreshCacheIfNeeded() error = %v", err)
			}
			st, err := cmd.status()
			if err != nil {
				t.Fatalf("status() error = %v", err)
			}
			if st.LatestVersion != tc.wantLatest {
				t.Fatalf("LatestVersion = %q, want %q", st.LatestVersion, tc.wantLatest)
			}
			if st.UpdateState != tc.wantState {
				t.Fatalf("UpdateState = %q, want %q", st.UpdateState, tc.wantState)
			}
			if st.SourceName != updateSourceNPMRegistry {
				t.Fatalf("SourceName = %q, want %q", st.SourceName, updateSourceNPMRegistry)
			}
			if got := shouldPromptShellUpdate(st); got != tc.wantPrompt {
				t.Fatalf("shouldPromptShellUpdate() = %v, want %v", got, tc.wantPrompt)
			}
		})
	}
}

// TestUpdateJudgmentIsUnchangedForNonNPMChannels holds the change-freeze
// boundary: go and github-release are already in step with GitHub tags, so
// neither their authority nor their existing caches may move.
func TestUpdateJudgmentIsUnchangedForNonNPMChannels(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	for _, installer := range []string{"go", "github-release", "source"} {
		t.Run(installer+" reads a cache written before sources were recorded", func(t *testing.T) {
			t.Parallel()

			cmd, cacheDir := updateCommandForInstaller(t, now, installer)
			cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				t.Fatalf("a fresh pre-existing cache must not trigger a refetch, got %s", req.URL.String())
				return nil, nil
			})}
			writeUpdateCacheFixture(t, cacheDir, updateCache{
				Version:   1,
				CheckedAt: now.Add(-time.Hour),
				TagName:   testVersionTag(t, 1),
			})

			if err := cmd.refreshCacheIfNeeded(context.Background()); err != nil {
				t.Fatalf("refreshCacheIfNeeded() error = %v", err)
			}
			st, err := cmd.status()
			if err != nil {
				t.Fatalf("status() error = %v", err)
			}
			if st.CacheState != "fresh" || st.UpdateState != "update_available" {
				t.Fatalf("status = %q/%q, want fresh/update_available", st.CacheState, st.UpdateState)
			}
			if st.SourceName != updateSourceGitHubRelease {
				t.Fatalf("SourceName = %q, want %q", st.SourceName, updateSourceGitHubRelease)
			}
			if !shouldPromptShellUpdate(st) {
				t.Fatalf("shouldPromptShellUpdate() = false, want the unchanged offer")
			}
		})
	}
}

// TestUpdateCacheRecordsItsAvailabilitySourceAndDropsAForeignOne pins C-1
// Scope in time. A cached answer from another channel is not aged out, it is
// discarded, because otherwise the wrong authority would drive the gate for up
// to updateCacheMaxAge after a channel switch.
func TestUpdateCacheRecordsItsAvailabilitySourceAndDropsAForeignOne(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	cmd, cacheDir := testUpdateCommand(t, now)
	installer := "npm"
	cmd.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return installer
		}
		return ""
	}
	cmd.client = updateAvailabilityResponder(t, cmd, testVersionTag(t, 2), "0.99.0")

	if err := cmd.refreshCacheIfNeeded(context.Background()); err != nil {
		t.Fatalf("refreshCacheIfNeeded() error = %v", err)
	}
	cache, _, err := cmd.loadCache()
	if err != nil {
		t.Fatalf("loadCache() error = %v", err)
	}
	if cache.Source != updateSourceNPMRegistry || cache.TagName != "v0.99.0" {
		t.Fatalf("cache = %+v, want the npm registry answer", cache)
	}

	// Same cache file, different channel: the recorded answer is about a
	// question this install no longer asks.
	installer = "go"
	st, err := cmd.status()
	if err != nil {
		t.Fatalf("status() error = %v", err)
	}
	if st.CacheState != "unknown" || st.UpdateState != "unknown" {
		t.Fatalf("status = %q/%q, want unknown/unknown after the source changed", st.CacheState, st.UpdateState)
	}
	if got := cmd.cachedLatestVersion(); got != "" {
		t.Fatalf("cachedLatestVersion() = %q, want no expected version from a foreign source", got)
	}
	if shouldPromptShellUpdate(st) {
		t.Fatalf("shouldPromptShellUpdate() = true, want no offer from a discarded cache")
	}

	if err := cmd.refreshCacheIfNeeded(context.Background()); err != nil {
		t.Fatalf("second refreshCacheIfNeeded() error = %v", err)
	}
	cache, _, err = cmd.loadCache()
	if err != nil {
		t.Fatalf("second loadCache() error = %v", err)
	}
	if cache.Source != updateSourceGitHubRelease || cache.TagName != testVersionTag(t, 2) {
		t.Fatalf("cache = %+v, want the GitHub release answer after the switch", cache)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, updateCacheFileName)); err != nil {
		t.Fatalf("cache file name changed: %v", err)
	}
}

// TestShellGateStaysSilentWhenTheAvailabilitySourceFails pins C-1
// Failure.Detection: an authority that cannot answer must leave the gate quiet
// and say so, never fall through to the other channel's answer.
func TestShellGateStaysSilentWhenTheAvailabilitySourceFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		transport updateRoundTripFunc
	}{
		{
			name: "the registry is unreachable",
			transport: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("dial tcp: no route to host")
			},
		},
		{
			name: "the registry answers with an error status",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			},
		},
		{
			name: "the response carries no dist-tags",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"name":"projmux"}`)),
					Header:     make(http.Header),
				}, nil
			},
		},
		{
			name: "the response is not JSON",
			transport: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("<html>502</html>")),
					Header:     make(http.Header),
				}, nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, _ := updateCommandForInstaller(t, now, "npm")
			cmd.client = &http.Client{Transport: tc.transport}

			if err := cmd.refreshCacheIfNeeded(context.Background()); err == nil {
				t.Fatalf("refreshCacheIfNeeded() error = nil, want the failure reported")
			}

			var stdout bytes.Buffer
			if err := cmd.Run([]string{"status"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, want := range []string{"latest:    unknown (unknown)", "state:     unknown", "source:    npm registry"} {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("status output missing %q\nfull output:\n%s", want, stdout.String())
				}
			}

			st, err := cmd.status()
			if err != nil {
				t.Fatalf("status() error = %v", err)
			}
			if shouldPromptShellUpdate(st) {
				t.Fatalf("shouldPromptShellUpdate() = true, want no offer when the source did not answer")
			}
		})
	}
}

// updateCommandForChannel builds a command on one exact (install path, release
// channel) pair. The channel is set through the resolver seam rather than the
// environment so the fixture states the axis it is testing.
func updateCommandForChannel(t *testing.T, now time.Time, installer, channel string) (*updateCommand, string) {
	t.Helper()
	cmd, cacheDir := updateCommandForInstaller(t, now, installer)
	cmd.releasesURL = "https://example.invalid/releases"
	cmd.releaseChannelSource = func() string { return channel }
	return cmd, cacheDir
}

// updateChannelResponder serves the three authorities a channel can ask.
//
// Every endpoint left empty is a trap rather than an empty answer: a channel
// that asks an authority it is not entitled to fails the test at the request,
// which is what pins "the default channel never even looks at a prerelease".
func updateChannelResponder(t *testing.T, cmd *updateCommand, latestTag string, distTags map[string]string, releases []string) *http.Client {
	t.Helper()
	return &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch req.URL.String() {
		case cmd.releaseAPIURL():
			if latestTag == "" {
				t.Fatalf("unexpected releases/latest request for a channel that must not ask it")
			}
			body = fmt.Sprintf(`{"tag_name":%q,"name":%q,"html_url":"https://github.com/crevissepartners/projmux/releases/tag/%s","published_at":"2026-09-05T10:00:00Z"}`, latestTag, latestTag, latestTag)
		case cmd.releaseListAPIURL():
			if len(releases) == 0 {
				t.Fatalf("unexpected release list request for a channel that must not ask it")
			}
			body = "[" + strings.Join(releases, ",") + "]"
		case cmd.npmRegistryAPIURL():
			if len(distTags) == 0 {
				t.Fatalf("unexpected npm registry request for a channel that must not ask it")
			}
			encoded, err := json.Marshal(distTags)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			body = fmt.Sprintf(`{"dist-tags":%s}`, encoded)
		default:
			t.Fatalf("unexpected availability request to %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
}

func updateReleaseListEntry(tag string, prerelease, draft bool) string {
	return fmt.Sprintf(`{"tag_name":%q,"name":%q,"html_url":"https://github.com/crevissepartners/projmux/releases/tag/%s","published_at":"2026-09-05T10:00:00Z","prerelease":%t,"draft":%t}`,
		tag, tag, tag, prerelease, draft)
}

// TestDefaultReleaseChannelNeverSeesAPrerelease is acceptance 1 of the release
// channel axis, for both install paths at once: an rc exists on npm as
// dist-tags.rc and on GitHub as a prerelease Release, and a default install
// neither reports it nor offers `u`.
//
// The negative half is structural rather than numeric. The npm judgment is
// handed the rc pointer and must ignore it, and the GitHub judgment fails the
// test outright if it so much as requests the endpoint that lists prereleases.
func TestDefaultReleaseChannelNeverSeesAPrerelease(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	stable := testCurrentVersionTag(t)
	rc := "v0.99.0-rc.1"

	t.Run("npm", func(t *testing.T) {
		t.Parallel()

		cmd, _ := updateCommandForChannel(t, now, "npm", updateReleaseChannelStable)
		cmd.client = updateChannelResponder(t, cmd, "", map[string]string{
			"latest": strings.TrimPrefix(stable, "v"),
			"rc":     strings.TrimPrefix(rc, "v"),
		}, nil)

		st := refreshedUpdateStatus(t, cmd)
		if st.ReleaseChannel != updateReleaseChannelStable {
			t.Fatalf("ReleaseChannel = %q, want %q", st.ReleaseChannel, updateReleaseChannelStable)
		}
		if st.LatestVersion != stable {
			t.Fatalf("LatestVersion = %q, want the stable dist-tag %q", st.LatestVersion, stable)
		}
		if strings.Contains(st.LatestVersion, "-") {
			t.Fatalf("LatestVersion = %q, want no prerelease on the default channel", st.LatestVersion)
		}
		if shouldPromptShellUpdate(st) {
			t.Fatalf("shouldPromptShellUpdate() = true, want no offer while only an rc is newer")
		}
	})

	t.Run("github-release", func(t *testing.T) {
		t.Parallel()

		cmd, _ := updateCommandForChannel(t, now, "github-release", updateReleaseChannelStable)
		// releases is left empty on purpose: asking it is the failure.
		cmd.client = updateChannelResponder(t, cmd, stable, nil, nil)

		st := refreshedUpdateStatus(t, cmd)
		if st.LatestVersion != stable {
			t.Fatalf("LatestVersion = %q, want %q", st.LatestVersion, stable)
		}
		if strings.Contains(st.LatestVersion, "-") {
			t.Fatalf("LatestVersion = %q, want no prerelease on the default channel", st.LatestVersion)
		}
		if shouldPromptShellUpdate(st) {
			t.Fatalf("shouldPromptShellUpdate() = true, want no offer on the default channel")
		}
	})
}

// TestPrereleaseVersionsCompareByPrecedence is acceptance 2 and 3 at the exact
// place they were broken: the judgment used to drop the prerelease suffix, so
// an rc install read its own line's stable release and its own line's next rc
// as "current" and could never move.
func TestPrereleaseVersionsCompareByPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		latest  string
		want    string
	}{
		{name: "rc advances to the next rc", current: "0.15.0-rc.1", latest: "v0.15.0-rc.2", want: "update_available"},
		{name: "rc numbering is numeric, not lexical", current: "0.15.0-rc.9", latest: "v0.15.0-rc.10", want: "update_available"},
		{name: "rc returns to its own line's stable", current: "0.15.0-rc.2", latest: "v0.15.0", want: "update_available"},
		{name: "stable is never superseded by its own rc", current: "0.15.0", latest: "v0.15.0-rc.2", want: "ahead"},
		{name: "the same rc is current", current: "0.15.0-rc.2", latest: "v0.15.0-rc.2", want: "current"},
		{name: "stable advances to a newer line's rc", current: "0.14.2", latest: "v0.15.0-rc.1", want: "update_available"},
		{name: "an rc of a newer line outranks an older stable", current: "0.15.0-rc.1", latest: "v0.14.2", want: "ahead"},
		{name: "stable to stable is unchanged", current: "0.14.2", latest: "v0.15.0", want: "update_available"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := compareUpdateState(tc.current, tc.latest); got != tc.want {
				t.Fatalf("compareUpdateState(%q, %q) = %q, want %q", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

// TestRCChannelAnswersWithTheNewerOfStableAndRC is the other half of acceptance
// 3: opting in must not strand the install on the prerelease line. Both
// authorities carry the two lines together, so the moment the stable release
// lands it is the answer an opted-in install gets.
func TestRCChannelAnswersWithTheNewerOfStableAndRC(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		distTags map[string]string
		releases []string
		want     string
	}{
		{
			name:     "the rc is ahead of the published stable",
			distTags: map[string]string{"latest": "0.14.2", "rc": "0.99.0-rc.2"},
			releases: []string{
				updateReleaseListEntry("v0.99.0-rc.2", true, false),
				updateReleaseListEntry("v0.14.2", false, false),
			},
			want: "v0.99.0-rc.2",
		},
		{
			name:     "the stable of the same line has landed",
			distTags: map[string]string{"latest": "0.99.0", "rc": "0.99.0-rc.2"},
			releases: []string{
				updateReleaseListEntry("v0.99.0", false, false),
				updateReleaseListEntry("v0.99.0-rc.2", true, false),
			},
			want: "v0.99.0",
		},
		{
			name:     "a drafted rc is not installable and is skipped",
			distTags: map[string]string{"latest": "0.99.0-rc.1", "rc": "0.99.0-rc.1"},
			releases: []string{
				updateReleaseListEntry("v0.99.0-rc.3", true, true),
				updateReleaseListEntry("v0.99.0-rc.1", true, false),
			},
			want: "v0.99.0-rc.1",
		},
		{
			name:     "the newest is not the most recently created",
			distTags: map[string]string{"latest": "0.99.0", "rc": "0.15.0-rc.1"},
			releases: []string{
				updateReleaseListEntry("v0.15.0-rc.1", true, false),
				updateReleaseListEntry("v0.99.0", false, false),
			},
			want: "v0.99.0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, installer := range []string{"npm", "github-release"} {
				t.Run(installer, func(t *testing.T) {
					t.Parallel()

					cmd, _ := updateCommandForChannel(t, now, installer, updateReleaseChannelRC)
					distTags, releases := tc.distTags, tc.releases
					if installer == "npm" {
						releases = nil
					} else {
						distTags = nil
					}
					cmd.client = updateChannelResponder(t, cmd, "", distTags, releases)

					st := refreshedUpdateStatus(t, cmd)
					if st.ReleaseChannel != updateReleaseChannelRC {
						t.Fatalf("ReleaseChannel = %q, want %q", st.ReleaseChannel, updateReleaseChannelRC)
					}
					if st.LatestVersion != tc.want {
						t.Fatalf("LatestVersion = %q, want %q", st.LatestVersion, tc.want)
					}
					if st.UpdateState != "update_available" || !shouldPromptShellUpdate(st) {
						t.Fatalf("state = %q, prompt = %v, want an offer on the rc channel", st.UpdateState, shouldPromptShellUpdate(st))
					}
				})
			}
		})
	}
}

// TestRCChannelReadsBothNPMDistTagsInOneRequest holds the budget the shell gate
// depends on. The abbreviated packument already carries every dist-tag, so
// opting in must read the rc pointer out of the response the stable judgment
// was already making, not add a second call.
func TestRCChannelReadsBothNPMDistTagsInOneRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	cmd, _ := updateCommandForChannel(t, now, "npm", updateReleaseChannelRC)
	requests := 0
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != cmd.npmRegistryAPIURL() {
			t.Fatalf("unexpected availability request to %s", req.URL.String())
		}
		if got := req.Header.Get("Accept"); got != "application/vnd.npm.install-v1+json" {
			t.Fatalf("Accept = %q, want the abbreviated packument", got)
		}
		requests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"dist-tags":{"latest":"0.14.2","rc":"0.99.0-rc.1"}}`)),
			Header:     make(http.Header),
		}, nil
	})}

	st := refreshedUpdateStatus(t, cmd)
	if requests != 1 {
		t.Fatalf("npm registry requests = %d, want exactly 1", requests)
	}
	if st.LatestVersion != "v0.99.0-rc.1" {
		t.Fatalf("LatestVersion = %q, want v0.99.0-rc.1", st.LatestVersion)
	}
}

// TestUpdateCacheIsKeyedByInstallPathAndReleaseChannel is acceptance 4. A cache
// from the other channel is not stale, it answers a different question, so
// freshness must not let it drive the gate.
func TestUpdateCacheIsKeyedByInstallPathAndReleaseChannel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	t.Run("an rc install does not reuse a fresh stable answer", func(t *testing.T) {
		t.Parallel()

		cmd, cacheDir := updateCommandForChannel(t, now, "npm", updateReleaseChannelRC)
		writeUpdateCacheFixture(t, cacheDir, updateCache{
			Version:   1,
			CheckedAt: now.Add(-time.Minute),
			Source:    updateSourceNPMRegistry,
			TagName:   "v0.14.2",
		})
		cmd.client = updateChannelResponder(t, cmd, "", map[string]string{
			"latest": "0.14.2",
			"rc":     "0.99.0-rc.1",
		}, nil)

		st := refreshedUpdateStatus(t, cmd)
		if st.LatestVersion != "v0.99.0-rc.1" {
			t.Fatalf("LatestVersion = %q, want the refetched rc answer", st.LatestVersion)
		}
		cache := readUpdateCacheFixture(t, cacheDir)
		if cache.Channel != updateReleaseChannelRC {
			t.Fatalf("cache Channel = %q, want %q", cache.Channel, updateReleaseChannelRC)
		}
	})

	t.Run("a default install does not reuse a fresh rc answer", func(t *testing.T) {
		t.Parallel()

		cmd, cacheDir := updateCommandForChannel(t, now, "npm", updateReleaseChannelStable)
		writeUpdateCacheFixture(t, cacheDir, updateCache{
			Version:   1,
			CheckedAt: now.Add(-time.Minute),
			Source:    updateSourceNPMRegistry,
			Channel:   updateReleaseChannelRC,
			TagName:   "v0.99.0-rc.1",
		})
		cmd.client = updateChannelResponder(t, cmd, "", map[string]string{
			"latest": "0.14.2",
			"rc":     "0.99.0-rc.1",
		}, nil)

		st := refreshedUpdateStatus(t, cmd)
		if st.LatestVersion != "v0.14.2" {
			t.Fatalf("LatestVersion = %q, want the refetched stable answer", st.LatestVersion)
		}
		if shouldPromptShellUpdate(st) {
			t.Fatalf("shouldPromptShellUpdate() = true, want the rc answer discarded outright")
		}
	})

	t.Run("a default install writes no channel field", func(t *testing.T) {
		t.Parallel()

		cmd, cacheDir := updateCommandForChannel(t, now, "npm", updateReleaseChannelStable)
		cmd.client = updateChannelResponder(t, cmd, "", map[string]string{"latest": "0.14.2"}, nil)

		if err := cmd.refreshCacheIfNeeded(context.Background()); err != nil {
			t.Fatalf("refreshCacheIfNeeded() error = %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(cacheDir, updateCacheFileName))
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if strings.Contains(string(raw), "release_channel") {
			t.Fatalf("cache file = %s, want the default channel to keep the pre-axis shape", raw)
		}
	})

	t.Run("a cache written before the axis existed reads as the default channel", func(t *testing.T) {
		t.Parallel()

		cmd, cacheDir := updateCommandForChannel(t, now, "npm", updateReleaseChannelStable)
		cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("a fresh pre-axis cache must not trigger a refetch, got %s", req.URL.String())
			return nil, nil
		})}
		writeUpdateCacheFixture(t, cacheDir, updateCache{
			Version:   1,
			CheckedAt: now.Add(-time.Minute),
			Source:    updateSourceNPMRegistry,
			TagName:   "v0.14.2",
		})

		if err := cmd.refreshCacheIfNeeded(context.Background()); err != nil {
			t.Fatalf("refreshCacheIfNeeded() error = %v", err)
		}
		st, err := cmd.status()
		if err != nil {
			t.Fatalf("status() error = %v", err)
		}
		if st.ReleaseChannel != updateReleaseChannelStable || st.CacheState != "fresh" {
			t.Fatalf("channel/cache = %q/%q, want stable/fresh", st.ReleaseChannel, st.CacheState)
		}
	})
}

// TestUpdateStatusJSONRevealsTheReleaseChannel is the observable surface the
// axis is judged from, and the default half of it is the smoke check: a default
// install's latest_version can never carry a prerelease suffix.
func TestUpdateStatusJSONRevealsTheReleaseChannel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		channel     string
		distTags    map[string]string
		wantLatest  string
		wantChannel string
	}{
		{
			channel:     updateReleaseChannelStable,
			distTags:    map[string]string{"latest": "0.14.2", "rc": "0.99.0-rc.1"},
			wantLatest:  "v0.14.2",
			wantChannel: updateReleaseChannelStable,
		},
		{
			channel:     updateReleaseChannelRC,
			distTags:    map[string]string{"latest": "0.14.2", "rc": "0.99.0-rc.1"},
			wantLatest:  "v0.99.0-rc.1",
			wantChannel: updateReleaseChannelRC,
		},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			t.Parallel()

			cmd, _ := updateCommandForChannel(t, now, "npm", tc.channel)
			cmd.client = updateChannelResponder(t, cmd, "", tc.distTags, nil)
			if err := cmd.refreshCacheIfNeeded(context.Background()); err != nil {
				t.Fatalf("refreshCacheIfNeeded() error = %v", err)
			}

			var stdout bytes.Buffer
			if err := cmd.Run([]string{"status", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			var st updateStatus
			if err := json.Unmarshal(stdout.Bytes(), &st); err != nil {
				t.Fatalf("json.Unmarshal error = %v\noutput=%s", err, stdout.String())
			}
			if st.ReleaseChannel != tc.wantChannel {
				t.Fatalf("release_channel = %q, want %q", st.ReleaseChannel, tc.wantChannel)
			}
			if st.LatestVersion != tc.wantLatest {
				t.Fatalf("latest_version = %q, want %q", st.LatestVersion, tc.wantLatest)
			}
			if tc.wantChannel == updateReleaseChannelStable && strings.Contains(st.LatestVersion, "-") {
				t.Fatalf("latest_version = %q, want no prerelease on the default channel", st.LatestVersion)
			}
		})
	}
}

// TestReleaseChannelDefaultsToStableForEveryUnrecognisedValue pins the strong
// reading of the default: no misconfiguration opts an install into prereleases.
func TestReleaseChannelDefaultsToStableForEveryUnrecognisedValue(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "   ", "stable", "STABLE", "beta", "rc.1", "true", "1"} {
		if got := normalizeUpdateReleaseChannel(raw); got != updateReleaseChannelStable {
			t.Fatalf("normalizeUpdateReleaseChannel(%q) = %q, want %q", raw, got, updateReleaseChannelStable)
		}
	}
	for _, raw := range []string{"rc", "RC", "  rc  "} {
		if got := normalizeUpdateReleaseChannel(raw); got != updateReleaseChannelRC {
			t.Fatalf("normalizeUpdateReleaseChannel(%q) = %q, want %q", raw, got, updateReleaseChannelRC)
		}
	}
}

// TestReleaseChannelEnvOptsIn covers the resolution order: the seam a stored
// setting will drive wins, and the environment answers until it exists.
func TestReleaseChannelEnvOptsIn(t *testing.T) {
	t.Parallel()

	cmd, _ := testUpdateCommand(t, time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC))
	if got := cmd.releaseChannel(); got != updateReleaseChannelStable {
		t.Fatalf("releaseChannel() = %q, want %q with nothing set", got, updateReleaseChannelStable)
	}
	cmd.getenv = func(name string) string {
		if name == updateReleaseChannelEnv {
			return "rc"
		}
		return ""
	}
	if got := cmd.releaseChannel(); got != updateReleaseChannelRC {
		t.Fatalf("releaseChannel() = %q, want %q from %s", got, updateReleaseChannelRC, updateReleaseChannelEnv)
	}
	cmd.releaseChannelSource = func() string { return updateReleaseChannelStable }
	if got := cmd.releaseChannel(); got != updateReleaseChannelStable {
		t.Fatalf("releaseChannel() = %q, want the resolver to win over the environment", got)
	}
}

// TestUpdateStatusTextNamesOnlyANonDefaultChannel keeps the default channel's
// report byte-for-byte what it was: the axis is worth a line only once it has
// been moved off its default.
func TestUpdateStatusTextNamesOnlyANonDefaultChannel(t *testing.T) {
	t.Parallel()

	base := updateStatus{
		CurrentVersion: "0.14.2",
		LatestVersion:  "v0.14.2",
		CacheState:     "fresh",
		SourceName:     updateSourceNPMRegistry,
		UpdateState:    "current",
		Installer:      updateInstaller{Source: "npm", Note: "npm shim"},
		CachePath:      "/cache/update.json",
	}

	var stable bytes.Buffer
	stableStatus := base
	stableStatus.ReleaseChannel = updateReleaseChannelStable
	if err := writeUpdateStatusText(&stable, stableStatus); err != nil {
		t.Fatalf("writeUpdateStatusText() error = %v", err)
	}
	if strings.Contains(stable.String(), "channel:") {
		t.Fatalf("default channel report = %q, want no channel row", stable.String())
	}

	var rc bytes.Buffer
	rcStatus := base
	rcStatus.ReleaseChannel = updateReleaseChannelRC
	if err := writeUpdateStatusText(&rc, rcStatus); err != nil {
		t.Fatalf("writeUpdateStatusText() error = %v", err)
	}
	if !strings.Contains(rc.String(), "channel:   rc") {
		t.Fatalf("rc channel report = %q, want a channel row", rc.String())
	}
}

// refreshedUpdateStatus runs the check the shell gate runs and returns the
// judgment it produced.
func refreshedUpdateStatus(t *testing.T, cmd *updateCommand) updateStatus {
	t.Helper()
	if err := cmd.refreshCacheIfNeeded(context.Background()); err != nil {
		t.Fatalf("refreshCacheIfNeeded() error = %v", err)
	}
	st, err := cmd.status()
	if err != nil {
		t.Fatalf("status() error = %v", err)
	}
	return st
}

// TestRCChannelAppliesTheDistTagItsJudgmentRead is ledger 1 and acceptance 1 for
// the npm install path.
//
// Apply used to install `projmux@latest` unconditionally, so an opted-in install
// was told an rc was available and then handed the stable line anyway. The fix
// is not "install @rc on the rc channel" either: once a line's stable release
// lands it supersedes that line's rc, the judgment offers the stable version,
// and apply has to follow it back off the prerelease.
func TestRCChannelAppliesTheDistTagItsJudgmentRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		distTags map[string]string
		want     string
	}{
		{
			name:     "the rc pointer is ahead of the stable line",
			distTags: map[string]string{"latest": "0.14.2", "rc": "0.15.0-rc.1"},
			want:     "npm install -g projmux@rc",
		},
		{
			name:     "the line's own stable release supersedes its rc",
			distTags: map[string]string{"latest": "0.15.0", "rc": "0.15.0-rc.1"},
			want:     "npm install -g projmux@latest",
		},
		{
			name:     "a registry with no rc pointer stays on the stable line",
			distTags: map[string]string{"latest": "0.14.2"},
			want:     "npm install -g projmux@latest",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, _, ran := updateApplyVerificationCommand(t, "npm")
			cmd.releaseChannelSource = func() string { return updateReleaseChannelRC }
			cmd.client = updateChannelResponder(t, cmd, "", tc.distTags, nil)

			var stdout bytes.Buffer
			if err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v\nstdout:\n%s", err, stdout.String())
			}
			if !slices.Contains(*ran, tc.want) {
				t.Fatalf("ran = %#v, want it to contain %q", *ran, tc.want)
			}
		})
	}
}

// TestDefaultChannelNpmApplyIsUnchangedAndAsksNoRegistry is acceptance 3 for the
// npm path. dist-tags.latest is the default channel's authority by definition,
// so its published sequence must be byte-identical and it must resolve its
// target without a single request: any request at all fails this test.
func TestDefaultChannelNpmApplyIsUnchangedAndAsksNoRegistry(t *testing.T) {
	t.Parallel()

	cmd, _, ran := updateApplyVerificationCommand(t, "npm")
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("default-channel apply unexpectedly requested %s", req.URL.String())
		return nil, nil
	})}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\nstdout:\n%s", err, stdout.String())
	}
	want := []string{
		"/home/me/bin/projmux config apply --bin /npm/bin/projmux --socket projmux",
		"npm install -g projmux@latest",
		"projmux config apply",
	}
	if !equalStrings(*ran, want) {
		t.Fatalf("ran = %#v, want %#v", *ran, want)
	}
}

// TestRCChannelGitHubReleaseApplyDownloadsThePrerelease is ledger 2 and
// acceptance 1 for the github-release install path.
//
// releases/latest is defined to skip prereleases, so it cannot answer for the
// channel at all -- asking it is the failure this fixture pins, the same way the
// Phase 1 judgment fixture pins it.
func TestRCChannelGitHubReleaseApplyDownloadsThePrerelease(t *testing.T) {
	t.Parallel()

	cmd, _ := updateCommandForChannel(t, time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), "github-release", updateReleaseChannelRC)
	target := filepath.Join(t.TempDir(), "projmux")
	if err := os.WriteFile(target, []byte("old\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cmd.executable = func() (string, error) { return target, nil }
	cmd.probeVersion = stubUpdateVersionProbe("0.14.2", "0.15.0-rc.1")
	archive := testReleaseArchive(t, "rc\n")
	assetURL := "https://github.com/crevissepartners/projmux/releases/download/v0.15.0-rc.1/projmux_0.15.0-rc.1_linux_amd64.tar.gz"
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case cmd.releaseAPIURL():
			t.Fatalf("rc apply asked releases/latest, which by definition never carries a prerelease")
			return nil, nil
		case cmd.releaseListAPIURL():
			body := `[{"tag_name":"v0.14.2","prerelease":false,"draft":false,"assets":[]},` +
				`{"tag_name":"v0.15.0-rc.1","prerelease":true,"draft":false,"assets":[{"name":"projmux_0.15.0-rc.1_linux_amd64.tar.gz","browser_download_url":"` + assetURL + `","digest":"` + testReleaseDigest(archive) + `"}]}]`
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
	cmd.runExternal = func(name string, args []string, stdout, stderr io.Writer) error { return nil }

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\nstdout:\n%s", err, stdout.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "rc\n" {
		t.Fatalf("target content = %q, want the prerelease binary", got)
	}
	if !strings.Contains(stdout.String(), "projmux_0.15.0-rc.1_linux_amd64.tar.gz") {
		t.Fatalf("stdout = %q, want the prerelease asset named", stdout.String())
	}
}

// TestDefaultChannelGitHubReleaseApplyNeverAsksTheReleaseList is acceptance 3
// for the github-release path: the default channel keeps downloading whatever
// releases/latest resolves, and reading the prerelease-carrying list is the
// failure.
func TestDefaultChannelGitHubReleaseApplyNeverAsksTheReleaseList(t *testing.T) {
	t.Parallel()

	cmd, _ := updateCommandForChannel(t, time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), "github-release", updateReleaseChannelStable)
	target := filepath.Join(t.TempDir(), "projmux")
	if err := os.WriteFile(target, []byte("old\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cmd.executable = func() (string, error) { return target, nil }
	archive := testReleaseArchive(t, "stable\n")
	assetURL := "https://github.com/crevissepartners/projmux/releases/download/v0.14.2/projmux_0.14.2_linux_amd64.tar.gz"
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case cmd.releaseListAPIURL():
			t.Fatalf("default-channel apply asked the release list, which carries prereleases")
			return nil, nil
		case cmd.releaseAPIURL():
			body := `{"tag_name":"v0.14.2","assets":[{"name":"projmux_0.14.2_linux_amd64.tar.gz","browser_download_url":"` + assetURL + `","digest":"` + testReleaseDigest(archive) + `"}]}`
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
	cmd.runExternal = func(name string, args []string, stdout, stderr io.Writer) error { return nil }

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v\nstdout:\n%s", err, stdout.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "stable\n" {
		t.Fatalf("target content = %q, want the stable binary", got)
	}
}

// TestUpdateApplyDryRunNamesTheAuthorityItsChannelWillRead keeps `--dry-run`
// honest across the axis: it may not promise releases/latest to an install that
// will read the list, nor `projmux@latest` to one that will install the rc
// pointer.
func TestUpdateApplyDryRunNamesTheAuthorityItsChannelWillRead(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	t.Run("github-release rc names the release list", func(t *testing.T) {
		t.Parallel()

		cmd, _ := updateCommandForChannel(t, now, "github-release", updateReleaseChannelRC)
		cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("dry-run unexpectedly requested %s", req.URL.String())
			return nil, nil
		})}

		var stdout bytes.Buffer
		if err := cmd.Run([]string{"apply", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if want := "would fetch: " + cmd.releaseListAPIURL(); !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	})

	t.Run("npm rc names the dist-tag it resolved", func(t *testing.T) {
		t.Parallel()

		cmd, _ := updateCommandForChannel(t, now, "npm", updateReleaseChannelRC)
		cmd.client = updateChannelResponder(t, cmd, "", map[string]string{
			"latest": "0.14.2",
			"rc":     "0.15.0-rc.1",
		}, nil)

		var stdout bytes.Buffer
		if err := cmd.Run([]string{"apply", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if want := "would run: npm install -g projmux@rc"; !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	})
}

// TestUpdateApplyVerifiesPrereleaseUpgradesByPrecedence is ledger 3 and
// acceptance 2.
//
// The post-apply check compared only the numeric core, which reads 0.15.0-rc.1
// and 0.15.0 as the same version: a successful move off a prerelease -- the
// exact move the rc channel exists to allow -- was reported as
// "installed version did not change". The stall and backwards verdicts stay
// exactly as they were; only prerelease precedence is new.
func TestUpdateApplyVerifiesPrereleaseUpgradesByPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		before   string
		after    string
		expected string
		wantErr  string
	}{
		{name: "an rc reaches its own line's stable release", before: "0.15.0-rc.1", after: "0.15.0", expected: "v0.15.0"},
		{name: "an rc advances within its line", before: "0.15.0-rc.1", after: "0.15.0-rc.2", expected: "v0.15.0-rc.2"},
		{name: "rc numbering is numeric, not lexical", before: "0.15.0-rc.9", after: "0.15.0-rc.10", expected: "v0.15.0-rc.10"},
		{
			name: "reinstalling the same rc is still a stall", before: "0.15.0-rc.1", after: "0.15.0-rc.1", expected: "v0.15.0-rc.1",
			wantErr: "installed version did not change",
		},
		{
			name: "a stable install replaced by its own rc still went backwards", before: "0.15.0", after: "0.15.0-rc.1", expected: "v0.15.0-rc.1",
			wantErr: "installed version went backwards",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, cacheDir, _ := updateApplyVerificationCommand(t, "npm")
			writeUpdateCacheFixture(t, cacheDir, updateCache{
				Version:   1,
				CheckedAt: time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC),
				Source:    updateSourceNPMRegistry,
				TagName:   tc.expected,
			})
			cmd.probeVersion = stubUpdateVersionProbe(tc.before, tc.after)

			var stdout bytes.Buffer
			err := cmd.Run([]string{"apply"}, &stdout, &bytes.Buffer{})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Run() error = %v\nstdout:\n%s", err, stdout.String())
				}
				want := ">> verified: projmux " + tc.after + " is now the active executable at /npm/bin/projmux (was " + tc.before + ")"
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("stdout = %q, want %q", stdout.String(), want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Run() error = nil, want %q\nstdout:\n%s", tc.wantErr, stdout.String())
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Run() error = %v, want substring %q", err, tc.wantErr)
			}
			if strings.Contains(stdout.String(), ">> verified:") {
				t.Fatalf("stdout = %q, want no verified line", stdout.String())
			}
		})
	}
}
