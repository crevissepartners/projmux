package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

func writeResumeLimitConfig(t *testing.T, path string, limit int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	body := "[ai]\nresume_picker_limit = " + strconv.Itoa(limit) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveAIResumePickerLimitPrecedence(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	globalPath := filepath.Join(home, ".config", "projmux", "config.toml")
	projectPath := filepath.Join(cwd, ".projmux", "config.toml")

	homeDir := func() (string, error) { return home, nil }
	noEnv := func(string) string { return "" }

	// Default when nothing is configured.
	got := resolveAIResumePickerLimit(homeDir, noEnv, cwd)
	if got.Limit != aiResumePickerLimitDefault || got.Source != aiResumePickerLimitSourceDefault {
		t.Fatalf("default resolution = %+v, want %d/default", got, aiResumePickerLimitDefault)
	}

	// Global config wins over default.
	writeResumeLimitConfig(t, globalPath, 70)
	got = resolveAIResumePickerLimit(homeDir, noEnv, cwd)
	if got.Limit != 70 || got.Source != aiResumePickerLimitSourceGlobal {
		t.Fatalf("global resolution = %+v, want 70/global", got)
	}

	// Project config wins over global.
	writeResumeLimitConfig(t, projectPath, 15)
	got = resolveAIResumePickerLimit(homeDir, noEnv, cwd)
	if got.Limit != 15 || got.Source != aiResumePickerLimitSourceProject {
		t.Fatalf("project resolution = %+v, want 15/project", got)
	}

	// Env wins over everything and is clamped.
	envLookup := func(name string) string {
		if name == aiResumePickerLimitEnv {
			return "999"
		}
		return ""
	}
	got = resolveAIResumePickerLimit(homeDir, envLookup, cwd)
	if got.Limit != hooks.AIResumePickerLimitMax || got.Source != aiResumePickerLimitSourceEnv {
		t.Fatalf("env resolution = %+v, want %d/env", got, hooks.AIResumePickerLimitMax)
	}

	// A non-numeric env value is ignored and falls through to project config.
	badEnv := func(name string) string {
		if name == aiResumePickerLimitEnv {
			return "notanumber"
		}
		return ""
	}
	got = resolveAIResumePickerLimit(homeDir, badEnv, cwd)
	if got.Limit != 15 || got.Source != aiResumePickerLimitSourceProject {
		t.Fatalf("bad-env resolution = %+v, want 15/project", got)
	}
}

func TestNormalizeResumePickerLimit(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{
		{in: 0, want: aiResumePickerLimitDefault},
		{in: -3, want: aiResumePickerLimitDefault},
		{in: 1, want: 1},
		{in: 30, want: 30},
		{in: hooks.AIResumePickerLimitMax, want: hooks.AIResumePickerLimitMax},
		{in: hooks.AIResumePickerLimitMax + 1, want: hooks.AIResumePickerLimitMax},
	} {
		if got := normalizeResumePickerLimit(tc.in); got != tc.want {
			t.Fatalf("normalizeResumePickerLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseAIResumePickerLimit(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "20", want: 20},
		{raw: "1", want: 1},
		{raw: "100", want: 100},
		{raw: "0", wantErr: true},
		{raw: "-5", wantErr: true},
		{raw: "101", wantErr: true},
		{raw: "abc", wantErr: true},
		{raw: "", wantErr: true},
	} {
		got, err := parseAIResumePickerLimit(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseAIResumePickerLimit(%q) = %d, want error", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseAIResumePickerLimit(%q) error = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("parseAIResumePickerLimit(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}
