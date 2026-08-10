package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

func writeResumeDepthConfig(t *testing.T, path string, depth int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	body := "[ai]\nresume_scan_depth = " + strconv.Itoa(depth) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveAIResumeScanDepthPrecedence(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	globalPath := filepath.Join(home, ".config", "projmux", "config.toml")
	projectPath := filepath.Join(cwd, ".projmux", "config.toml")

	homeDir := func() (string, error) { return home, nil }
	noEnv := func(string) string { return "" }

	// Default when nothing is configured.
	got := resolveAIResumeScanDepth(homeDir, noEnv, cwd)
	if got.Depth != aiResumeScanDepthDefault || got.Source != aiResumeScanDepthSourceDefault {
		t.Fatalf("default resolution = %+v, want %d/default", got, aiResumeScanDepthDefault)
	}

	// Global config wins over default.
	writeResumeDepthConfig(t, globalPath, 3)
	got = resolveAIResumeScanDepth(homeDir, noEnv, cwd)
	if got.Depth != 3 || got.Source != aiResumeScanDepthSourceGlobal {
		t.Fatalf("global resolution = %+v, want 3/global", got)
	}

	// Project config wins over global.
	writeResumeDepthConfig(t, projectPath, 1)
	got = resolveAIResumeScanDepth(homeDir, noEnv, cwd)
	if got.Depth != 1 || got.Source != aiResumeScanDepthSourceProject {
		t.Fatalf("project resolution = %+v, want 1/project", got)
	}

	// Env wins over everything and is clamped.
	envLookup := func(name string) string {
		if name == aiResumeScanDepthEnv {
			return "999"
		}
		return ""
	}
	got = resolveAIResumeScanDepth(homeDir, envLookup, cwd)
	if got.Depth != hooks.AIResumeScanDepthMax || got.Source != aiResumeScanDepthSourceEnv {
		t.Fatalf("env resolution = %+v, want %d/env", got, hooks.AIResumeScanDepthMax)
	}

	// Env may explicitly set depth 0 (exact cwd) and is still sourced from env.
	zeroEnv := func(name string) string {
		if name == aiResumeScanDepthEnv {
			return "0"
		}
		return ""
	}
	got = resolveAIResumeScanDepth(homeDir, zeroEnv, cwd)
	if got.Depth != 0 || got.Source != aiResumeScanDepthSourceEnv {
		t.Fatalf("zero-env resolution = %+v, want 0/env", got)
	}

	// A non-numeric env value is ignored and falls through to project config.
	badEnv := func(name string) string {
		if name == aiResumeScanDepthEnv {
			return "notanumber"
		}
		return ""
	}
	got = resolveAIResumeScanDepth(homeDir, badEnv, cwd)
	if got.Depth != 1 || got.Source != aiResumeScanDepthSourceProject {
		t.Fatalf("bad-env resolution = %+v, want 1/project", got)
	}
}

func TestNormalizeResumeScanDepth(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{
		{in: 0, want: 0},
		{in: -3, want: aiResumeScanDepthDefault},
		{in: 1, want: 1},
		{in: hooks.AIResumeScanDepthMax, want: hooks.AIResumeScanDepthMax},
		{in: hooks.AIResumeScanDepthMax + 1, want: hooks.AIResumeScanDepthMax},
	} {
		if got := normalizeResumeScanDepth(tc.in); got != tc.want {
			t.Fatalf("normalizeResumeScanDepth(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseAIResumeScanDepth(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "0", want: 0},
		{raw: "1", want: 1},
		{raw: "8", want: 8},
		{raw: "-1", wantErr: true},
		{raw: "9", wantErr: true},
		{raw: "abc", wantErr: true},
		{raw: "", wantErr: true},
	} {
		got, err := parseAIResumeScanDepth(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseAIResumeScanDepth(%q) = %d, want error", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseAIResumeScanDepth(%q) error = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("parseAIResumeScanDepth(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}
