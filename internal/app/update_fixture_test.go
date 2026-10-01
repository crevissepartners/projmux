package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app/updatecmd"
	"github.com/crevissepartners/projmux/internal/version"
)

// The fixtures below build `projmux update` from its exported edges for the
// app tests that drive it through the shell welcome, Settings, and the
// flag-parse tables. updatecmd's own tests keep theirs beside the unexported
// seams they also replace.

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testUpdateCommand(t *testing.T, now time.Time) (*updatecmd.Command, string) {
	t.Helper()
	cacheDir := t.TempDir()
	reads := 0
	cmd := &updatecmd.Command{
		Now:       func() time.Time { return now },
		Getenv:    func(string) string { return "" },
		CacheDir:  func() (string, error) { return cacheDir, nil },
		AppSocket: defaultAppSocket,
		Client: &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("unexpected update request to %s", req.URL.String())
		})},
		APIURL:     "https://example.invalid/latest",
		NPMURL:     "https://example.invalid/npm/projmux",
		Executable: func() (string, error) { return "/tmp/projmux", nil },
		// An apply driven from these surfaces describes an ordinary landed
		// upgrade, so the probe reports a higher version after publication.
		ProbeVersion: func(string) (string, error) {
			reads++
			if reads == 1 {
				return "projmux 0.13.0\n", nil
			}
			return "projmux 0.13.1\n", nil
		},
		LookPath: func(name string) (string, error) {
			if name != "projmux" {
				return "", fmt.Errorf("unexpected executable lookup %q", name)
			}
			return "/npm/bin/projmux", nil
		},
	}
	return cmd, cacheDir
}

func writeUpdateCacheFixture(t *testing.T, cacheDir string, cache updatecmd.Cache) {
	t.Helper()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, updatecmd.CacheFileName), data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func testVersionTag(t *testing.T, patchDelta int) string {
	t.Helper()
	parts, ok := updatecmd.ParseVersion(version.String())
	if !ok {
		t.Fatalf("cannot parse current version %q", version.String())
	}
	parts[2] += patchDelta
	if parts[2] < 0 {
		t.Fatalf("invalid patch delta %d for current version %q", patchDelta, version.String())
	}
	return fmt.Sprintf("v%d.%d.%d", parts[0], parts[1], parts[2])
}
