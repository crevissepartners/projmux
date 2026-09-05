package hooks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseProjectConfigSupportedSections(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig(`
[startup]
run = "git status --short"

[hooks.pre-create]
run = "echo pre"

[hooks.post-create]
run = "echo post"

[hooks.post-attach]
run = "echo attached"

[hooks.send-noti]
run = "echo send-noti"

[env]
FOO = "bar"
QUOTED = "a \"quoted\" value"
KUBE_CONTEXT = "manual-context"
KUBE_NAMESPACE = "manual-namespace"
PROJMUX_KUBE_CONTEXT = "manual-projmux-context"

[theme]
font_family = "Cascadia Mono"
font_size = 12

[ui]
locale = "ko-KR"
native_keys = false

[ai]
resume_picker_limit = 50
`)
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if cfg.StartupRun != "git status --short" {
		t.Fatalf("StartupRun = %q", cfg.StartupRun)
	}
	if cfg.Hooks[EventPreCreate] != "echo pre" || cfg.Hooks[EventPostCreate] != "echo post" || cfg.Hooks[EventPostAttach] != "echo attached" || cfg.Hooks[EventSendNoti] != "echo send-noti" {
		t.Fatalf("Hooks = %#v", cfg.Hooks)
	}
	if cfg.Env["FOO"] != "bar" || cfg.Env["QUOTED"] != `a "quoted" value` {
		t.Fatalf("Env = %#v", cfg.Env)
	}
	sessionEnv := cfg.SessionEnv()
	for key, want := range map[string]string{
		"KUBE_CONTEXT":         "manual-context",
		"KUBE_NAMESPACE":       "manual-namespace",
		"PROJMUX_KUBE_CONTEXT": "manual-projmux-context",
	} {
		if sessionEnv[key] != want {
			t.Fatalf("SessionEnv[%s] = %q, want explicit generic env value %q", key, sessionEnv[key], want)
		}
	}
	// Deprecated [theme] font keys are accepted for backward compatibility but
	// ignored: they must not block parsing and must not be stored on the config.
	if cfg.Theme.HasContent() {
		t.Fatalf("Theme = %#v, want deprecated font keys ignored", cfg.Theme)
	}
	if cfg.UI.Locale != "ko-KR" {
		t.Fatalf("UI.Locale = %q, want ko-KR", cfg.UI.Locale)
	}
	if cfg.UI.NativeKeys == nil || *cfg.UI.NativeKeys {
		t.Fatalf("UI.NativeKeys = %#v, want explicit false", cfg.UI.NativeKeys)
	}
	if cfg.AI.ResumePickerLimit != 50 {
		t.Fatalf("AI.ResumePickerLimit = %d, want 50", cfg.AI.ResumePickerLimit)
	}
}

func TestProjectUINativeKeysRoundTrip(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig("[ui]\nnative_keys = false\n")
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if cfg.UI.NativeKeys == nil || *cfg.UI.NativeKeys {
		t.Fatalf("UI.NativeKeys = %#v, want explicit false", cfg.UI.NativeKeys)
	}

	rendered := renderProjectConfig(cfg)
	if rendered != "[ui]\nnative_keys = false\n" {
		t.Fatalf("rendered = %q, want bare native_keys boolean", rendered)
	}
	reparsed, err := ParseProjectConfig(rendered)
	if err != nil {
		t.Fatalf("re-parse error = %v", err)
	}
	if reparsed.UI.NativeKeys == nil || *reparsed.UI.NativeKeys {
		t.Fatalf("re-parsed UI.NativeKeys = %#v, want explicit false", reparsed.UI.NativeKeys)
	}
}

func TestProjectUINativeKeysRejectsNonBoolean(t *testing.T) {
	t.Parallel()

	if _, err := ParseProjectConfig("[ui]\nnative_keys = \"off\"\n"); err == nil {
		t.Fatal("expected quoted non-boolean native_keys to error")
	}
}

func TestProjectAIConfigResumePickerLimitRoundTrip(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig("[ai]\nresume_picker_limit = 42\n")
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if cfg.AI.ResumePickerLimit != 42 {
		t.Fatalf("AI.ResumePickerLimit = %d, want 42", cfg.AI.ResumePickerLimit)
	}

	rendered := renderProjectConfig(cfg)
	if !strings.Contains(rendered, "[ai]") || !strings.Contains(rendered, "resume_picker_limit = 42") {
		t.Fatalf("rendered = %q, want [ai] resume_picker_limit = 42", rendered)
	}

	reparsed, err := ParseProjectConfig(rendered)
	if err != nil {
		t.Fatalf("re-parse error = %v", err)
	}
	if reparsed.AI != cfg.AI {
		t.Fatalf("re-parsed AI = %#v, want %#v", reparsed.AI, cfg.AI)
	}
}

func TestProjectAIConfigRejectsNonInteger(t *testing.T) {
	t.Parallel()

	if _, err := ParseProjectConfig("[ai]\nresume_picker_limit = \"thirty\"\n"); err == nil {
		t.Fatal("expected non-integer resume_picker_limit to error")
	}
	if _, err := ParseProjectConfig("[ai]\nresume_scan_depth = \"two\"\n"); err == nil {
		t.Fatal("expected non-integer resume_scan_depth to error")
	}
	if _, err := ParseProjectConfig("[ai]\nunknown_key = 3\n"); err == nil {
		t.Fatal("expected unsupported ai key to error")
	}
}

func TestProjectAIConfigResumeScanDepthRoundTrip(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig("[ai]\nresume_scan_depth = 2\n")
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if cfg.AI.ResumeScanDepth != 2 {
		t.Fatalf("AI.ResumeScanDepth = %d, want 2", cfg.AI.ResumeScanDepth)
	}

	rendered := renderProjectConfig(cfg)
	if !strings.Contains(rendered, "[ai]") || !strings.Contains(rendered, "resume_scan_depth = 2") {
		t.Fatalf("rendered = %q, want [ai] resume_scan_depth = 2", rendered)
	}

	reparsed, err := ParseProjectConfig(rendered)
	if err != nil {
		t.Fatalf("re-parse error = %v", err)
	}
	if reparsed.AI != cfg.AI {
		t.Fatalf("re-parsed AI = %#v, want %#v", reparsed.AI, cfg.AI)
	}
}

func TestProjectAIConfigBothKeysShareOneSection(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig("[ai]\nresume_picker_limit = 50\nresume_scan_depth = 3\n")
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if cfg.AI.ResumePickerLimit != 50 || cfg.AI.ResumeScanDepth != 3 {
		t.Fatalf("AI = %#v, want limit 50 depth 3", cfg.AI)
	}

	rendered := renderProjectConfig(cfg)
	if strings.Count(rendered, "[ai]") != 1 {
		t.Fatalf("rendered = %q, want a single [ai] section", rendered)
	}
	if !strings.Contains(rendered, "resume_picker_limit = 50") || !strings.Contains(rendered, "resume_scan_depth = 3") {
		t.Fatalf("rendered = %q, want both ai keys", rendered)
	}

	reparsed, err := ParseProjectConfig(rendered)
	if err != nil {
		t.Fatalf("re-parse error = %v", err)
	}
	if reparsed.AI != cfg.AI {
		t.Fatalf("re-parsed AI = %#v, want %#v", reparsed.AI, cfg.AI)
	}
}

func TestNormalizeClampsResumeScanDepth(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   int
		want int
	}{
		{in: 0, want: 0},  // unset stays unset
		{in: -3, want: 0}, // negative collapses to exact cwd
		{in: 1, want: 1},  // in range
		{in: 8, want: 8},  // max
		{in: 99, want: AIResumeScanDepthMax},
	} {
		cfg := ProjectConfig{AI: AIConfig{ResumeScanDepth: tc.in}}
		normalizeProjectConfig(&cfg)
		if cfg.AI.ResumeScanDepth != tc.want {
			t.Fatalf("normalize(%d) = %d, want %d", tc.in, cfg.AI.ResumeScanDepth, tc.want)
		}
	}
}

func TestNormalizeClampsResumePickerLimit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   int
		want int
	}{
		{in: 0, want: 0},     // unset stays unset
		{in: 1, want: 1},     // min
		{in: 50, want: 50},   // in range
		{in: 100, want: 100}, // max
		{in: 250, want: AIResumePickerLimitMax},
		{in: -5, want: AIResumePickerLimitMin},
	} {
		cfg := ProjectConfig{AI: AIConfig{ResumePickerLimit: tc.in}}
		normalizeProjectConfig(&cfg)
		if cfg.AI.ResumePickerLimit != tc.want {
			t.Fatalf("normalize(%d) = %d, want %d", tc.in, cfg.AI.ResumePickerLimit, tc.want)
		}
	}
}

func TestProjectThemeConfigRoundTripsPhase6Keys(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig(`
[theme]
chrome_foreground = "#010203"
text_primary = "#040506"
progress = "#112233"
success = "#445566"
action_required = "#778899"
pane_active_bg = "#0a0b0c"
focus = "#0d0e0f"
`)
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if cfg.Theme.ChromeForeground != "#010203" || cfg.Theme.TextPrimary != "#040506" ||
		cfg.Theme.Progress != "#112233" || cfg.Theme.Success != "#445566" ||
		cfg.Theme.ActionRequired != "#778899" || cfg.Theme.PaneActiveBg != "#0a0b0c" ||
		cfg.Theme.Focus != "#0d0e0f" {
		t.Fatalf("Theme = %#v, want all public theme keys parsed", cfg.Theme)
	}

	rendered := renderThemeConfigSection(cfg.Theme)
	for _, want := range []string{
		`chrome_foreground = "#010203"`,
		`text_primary = "#040506"`,
		`progress = "#112233"`,
		`success = "#445566"`,
		`action_required = "#778899"`,
		`pane_active_bg = "#0a0b0c"`,
		`focus = "#0d0e0f"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered theme section %q missing %q", rendered, want)
		}
	}

	// Round-trip: re-parse the rendered output and confirm equality.
	reparsed, err := ParseProjectConfig(rendered)
	if err != nil {
		t.Fatalf("re-parse error = %v", err)
	}
	if reparsed.Theme != cfg.Theme {
		t.Fatalf("re-parsed theme = %#v, want %#v", reparsed.Theme, cfg.Theme)
	}
}

func TestParseProjectConfigRejectsInternalTmuxHooks(t *testing.T) {
	t.Parallel()

	_, err := ParseProjectConfig(`
[hooks.after-select-pane]
run = "echo nope"
`)
	if err == nil {
		t.Fatal("expected unsupported hook event error")
	}
	if !strings.Contains(err.Error(), "unsupported section") {
		t.Fatalf("error = %v, want unsupported section", err)
	}
}

func TestUpdateProjectConfigPreservesHooksStartupAndGenericEnv(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	path := writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo post"

[env]
ZED = "last"
`)

	_, err := UpdateProjectConfig(path, func(cfg *ProjectConfig) error {
		cfg.StartupRun = "codex"
		cfg.Env["ALPHA"] = "first"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateProjectConfig() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `[hooks.post-create]
run = "echo post"

[startup]
run = "codex"

[env]
ALPHA = "first"
ZED = "last"
`
	if string(got) != want {
		t.Fatalf("config.toml =\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateProjectConfigLegacyKubePreservesOriginalBytesAndMtimeWithExactDiagnostic(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".projmux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("# keep formatting and comments\n[kube]\ncontext = \"dev\"\nnamespace = \"tools\"\n\n[env]\nCUSTOM = \"kept\"\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Unix(1_700_000_000, 123_000_000)
	if err := os.Chtimes(path, wantTime, wantTime); err != nil {
		t.Fatal(err)
	}
	updateCalled := false
	_, err := UpdateProjectConfig(path, func(cfg *ProjectConfig) error {
		updateCalled = true
		cfg.Env["SHOULD_NOT_APPEAR"] = "true"
		return nil
	})
	if err == nil || err.Error() != LegacyKubeConfigDiagnostic {
		t.Fatalf("UpdateProjectConfig() error = %v, want exact %q", err, LegacyKubeConfigDiagnostic)
	}
	if updateCalled {
		t.Fatal("legacy [kube] reached update callback")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("legacy config bytes changed\ngot:  %q\nwant: %q", got, original)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !info.ModTime().Equal(wantTime) {
		t.Fatalf("legacy config mtime = %v, want unchanged %v", info.ModTime(), wantTime)
	}
}

func TestUpdateProjectConfigRejectsInvalidEnvKey(t *testing.T) {
	t.Parallel()

	if err := ValidateProjectEnvKey("1BAD"); err == nil {
		t.Fatal("ValidateProjectEnvKey accepted invalid key")
	}
	path := filepath.Join(t.TempDir(), ".projmux", "config.toml")
	_, err := UpdateProjectConfig(path, func(cfg *ProjectConfig) error {
		cfg.Env["1BAD"] = "value"
		return nil
	})
	if err == nil {
		t.Fatal("UpdateProjectConfig accepted invalid env key")
	}
}

func TestWriteProjectConfigFileModePathAndMetadataContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setup         func(*testing.T, string) (path, target string, links []string)
		wantMode      os.FileMode
		wantPathIsReg bool
	}{
		{
			name: "regular file",
			setup: func(t *testing.T, root string) (string, string, []string) {
				path := filepath.Join(root, "repo", ".projmux", "config.toml")
				writeConfigFixture(t, path, "old\n", 0o640)
				return path, path, nil
			},
			wantMode:      0o640,
			wantPathIsReg: true,
		},
		{
			name: "missing path",
			setup: func(t *testing.T, root string) (string, string, []string) {
				path := filepath.Join(root, "repo", ".projmux", "config.toml")
				return path, path, nil
			},
			wantMode:      0o644,
			wantPathIsReg: true,
		},
		{
			name: "broken symlink falls back to link path",
			setup: func(t *testing.T, root string) (string, string, []string) {
				path := filepath.Join(root, "repo", ".projmux", "config.toml")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(root, "missing", "config.toml")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return path, path, nil
			},
			wantMode:      0o644,
			wantPathIsReg: true,
		},
		{
			name: "relative symlink",
			setup: func(t *testing.T, root string) (string, string, []string) {
				path := filepath.Join(root, "repo", ".projmux", "config.toml")
				target := filepath.Join(root, "repo", "config-targets", "project.toml")
				writeConfigFixture(t, target, "old\n", 0o620)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				rel, err := filepath.Rel(filepath.Dir(path), target)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(rel, path); err != nil {
					t.Fatal(err)
				}
				return path, target, []string{path}
			},
			wantMode: 0o620,
		},
		{
			name: "multi-hop relative symlink chain",
			setup: func(t *testing.T, root string) (string, string, []string) {
				path := filepath.Join(root, "repo", ".projmux", "config.toml")
				middle := filepath.Join(root, "links", "project-config")
				target := filepath.Join(root, "targets", "config.toml")
				writeConfigFixture(t, target, "old\n", 0o600)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(middle), 0o755); err != nil {
					t.Fatal(err)
				}
				middleRel, err := filepath.Rel(filepath.Dir(middle), target)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(middleRel, middle); err != nil {
					t.Fatal(err)
				}
				pathRel, err := filepath.Rel(filepath.Dir(path), middle)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(pathRel, path); err != nil {
					t.Fatal(err)
				}
				return path, target, []string{path, middle}
			},
			wantMode: 0o600,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path, target, links := tc.setup(t, t.TempDir())
			beforeLinks := make([]os.FileInfo, len(links))
			for i, link := range links {
				info, err := os.Lstat(link)
				if err != nil {
					t.Fatal(err)
				}
				beforeLinks[i] = info
			}
			var beforeUID, beforeGID int
			var beforeOwnerOK bool
			if info, err := os.Stat(target); err == nil {
				beforeUID, beforeGID, beforeOwnerOK = projectConfigFileOwner(info)
			}

			err := writeProjectConfigFileMode(path, ProjectConfig{StartupRun: "make ready"}, 0o644)
			if err != nil {
				t.Fatalf("writeProjectConfigFileMode() error = %v", err)
			}

			for i, link := range links {
				after, err := os.Lstat(link)
				if err != nil {
					t.Fatal(err)
				}
				if after.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("Lstat(%q) mode = %v, want symlink", link, after.Mode())
				}
				if !os.SameFile(beforeLinks[i], after) {
					t.Fatalf("symlink inode at %q changed", link)
				}
			}
			if tc.wantPathIsReg {
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !info.Mode().IsRegular() {
					t.Fatalf("Lstat(%q) mode = %v, want regular file", path, info.Mode())
				}
			}
			body, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := string(body), "[startup]\nrun = \"make ready\"\n"; got != want {
				t.Fatalf("config body = %q, want %q", got, want)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tc.wantMode {
				t.Fatalf("target mode = %#o, want %#o", got, tc.wantMode)
			}
			if afterUID, afterGID, ok := projectConfigFileOwner(info); beforeOwnerOK && (!ok || afterUID != beforeUID || afterGID != beforeGID) {
				t.Fatalf("target owner = (%d,%d,%v), want (%d,%d,true)", afterUID, afterGID, ok, beforeUID, beforeGID)
			}
			assertNoProjectConfigTemps(t, filepath.Dir(path), filepath.Dir(target))
		})
	}
}

func TestWriteProjectConfigFileModeFailureCleansTempAndPreservesSymlinkTarget(t *testing.T) {
	t.Parallel()

	injected := errors.New("injected writer failure")
	tests := []struct {
		name           string
		patch          func(*projectConfigFileOps, *int)
		wantCloseCalls int
	}{
		{name: "lstat", patch: func(ops *projectConfigFileOps, _ *int) {
			ops.lstat = func(string) (os.FileInfo, error) { return nil, injected }
		}},
		{name: "resolve", patch: func(ops *projectConfigFileOps, _ *int) {
			ops.evalSymlinks = func(string) (string, error) { return "", injected }
		}},
		{name: "stat", patch: func(ops *projectConfigFileOps, _ *int) {
			ops.stat = func(string) (os.FileInfo, error) { return nil, injected }
		}},
		{name: "create", patch: func(ops *projectConfigFileOps, _ *int) {
			ops.createTemp = func(string, string) (projectConfigTempFile, error) { return nil, injected }
		}},
		{name: "write", patch: func(ops *projectConfigFileOps, closeCalls *int) {
			createTemp := ops.createTemp
			ops.createTemp = func(dir, pattern string) (projectConfigTempFile, error) {
				file, err := createTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingProjectConfigTempFile{projectConfigTempFile: file, writeErr: injected, closeCalls: closeCalls}, nil
			}
		}, wantCloseCalls: 1},
		{name: "close", patch: func(ops *projectConfigFileOps, closeCalls *int) {
			createTemp := ops.createTemp
			ops.createTemp = func(dir, pattern string) (projectConfigTempFile, error) {
				file, err := createTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingProjectConfigTempFile{projectConfigTempFile: file, closeErr: injected, closeCalls: closeCalls}, nil
			}
		}, wantCloseCalls: 1},
		{name: "chmod", patch: func(ops *projectConfigFileOps, _ *int) {
			ops.chmod = func(string, os.FileMode) error { return injected }
		}},
		{name: "chown", patch: func(ops *projectConfigFileOps, _ *int) {
			ops.chown = func(string, int, int) error { return injected }
		}},
		{name: "rename", patch: func(ops *projectConfigFileOps, _ *int) {
			ops.rename = func(string, string) error { return injected }
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			path := filepath.Join(root, "repo", ".projmux", "config.toml")
			target := filepath.Join(root, "target", "config.toml")
			writeConfigFixture(t, target, "original\n", 0o640)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			linkBefore, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}

			ops := defaultProjectConfigFileOps()
			closeCalls := 0
			tc.patch(&ops, &closeCalls)
			err = writeProjectConfigFileModeWithOps(path, ProjectConfig{StartupRun: "changed"}, 0o644, ops)
			if !errors.Is(err, injected) {
				if tc.name == "chown" && runtime.GOOS == "windows" && err == nil {
					t.Skip("ownership metadata is not exposed on Windows")
				}
				t.Fatalf("write error = %v, want injected failure", err)
			}

			linkAfter, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if linkAfter.Mode()&os.ModeSymlink == 0 || !os.SameFile(linkBefore, linkAfter) {
				t.Fatalf("original symlink was not preserved after %s failure", tc.name)
			}
			body, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := string(body), "original\n"; got != want {
				t.Fatalf("target after %s failure = %q, want %q", tc.name, got, want)
			}
			if closeCalls != tc.wantCloseCalls {
				t.Fatalf("temporary file close calls after %s failure = %d, want %d", tc.name, closeCalls, tc.wantCloseCalls)
			}
			assertNoProjectConfigTemps(t, filepath.Dir(path), filepath.Dir(target))
		})
	}
}

type failingProjectConfigTempFile struct {
	projectConfigTempFile
	writeErr   error
	closeErr   error
	closeCalls *int
}

func (f *failingProjectConfigTempFile) WriteString(content string) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.projectConfigTempFile.WriteString(content)
}

func (f *failingProjectConfigTempFile) Close() error {
	if f.closeCalls != nil {
		(*f.closeCalls)++
	}
	err := f.projectConfigTempFile.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func writeConfigFixture(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertNoProjectConfigTemps(t *testing.T, dirs ...string) {
	t.Helper()
	seen := map[string]bool{}
	for _, dir := range dirs {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		matches, err := filepath.Glob(filepath.Join(dir, "config.toml.tmp-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("temporary config artifacts in %q: %v", dir, matches)
		}
	}
}

func TestRunnerStartupCommandUsesTrustedProjectConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "git status --short"
`)

	promptCalls := 0
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			promptCalls++
			if req.RelativePath != projectConfigRelativePath {
				t.Fatalf("RelativePath = %q, want %q", req.RelativePath, projectConfigRelativePath)
			}
			return ProjectHookAllowOnce
		},
	}

	command, ok := runner.StartupCommand(cwd)
	if !ok {
		t.Fatal("StartupCommand() ok = false, want true")
	}
	if command != "git status --short" {
		t.Fatalf("StartupCommand() = %q, want config startup command", command)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
}

func TestRunnerStartupCommandIgnoresLegacyScriptFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	// Legacy script files in the historical layout must be silently ignored
	// by startup command lookup; only [startup] run is used.
	dir := t.TempDir()
	cwd := filepath.Join(dir, "repo")
	writeHook(t, filepath.Join(cwd, ".projmux", "pane-startup"), "echo legacy-script-should-not-run\n", 0o755)
	writeProjectConfig(t, cwd, `
[startup]
run = "startup-direct-command"
`)

	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
	}

	command, ok := runner.StartupCommand(cwd)
	if !ok {
		t.Fatal("StartupCommand() ok = false, want true")
	}
	if command != "startup-direct-command" {
		t.Fatalf("StartupCommand() = %q, want startup direct command", command)
	}
}

func TestRunnerProjectConfigExplicitGenericEnvReachesHookWithoutKubeProjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo env=$FOO ctx=$KUBE_CONTEXT ns=$KUBE_NAMESPACE"

[env]
FOO = "bar"
KUBE_CONTEXT = "manual-dev"
KUBE_NAMESPACE = "manual-tools"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := logger.String()
	if !strings.Contains(got, "[post-create] env=bar ctx=manual-dev ns=manual-tools") {
		t.Fatalf("logger output missing config env:\n%s", got)
	}
}

func TestRunnerHookEnvironmentDoesNotSynthesizeRetiredKubeVariables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	for _, key := range []string{"PROJMUX_KUBE_CONTEXT", "PROJMUX_KUBE_NAMESPACE", "KUBE_CONTEXT", "KUBE_NAMESPACE"} {
		value, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo ctx=${PROJMUX_KUBE_CONTEXT-unset} ns=${PROJMUX_KUBE_NAMESPACE-unset} legacy_ctx=${KUBE_CONTEXT-unset} legacy_ns=${KUBE_NAMESPACE-unset}"

[env]
FOO = "bar"
`)
	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	if _, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := "ctx=unset ns=unset legacy_ctx=unset legacy_ns=unset"
	if !strings.Contains(logger.String(), want) {
		t.Fatalf("hook environment = %q, want %q", logger.String(), want)
	}
}

func TestRunnerProjectConfigPreCreateAbort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.pre-create]
run = "echo before-abort; exit 9"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	_, err := runner.Run(context.Background(), EventPreCreate, Context{CWD: cwd})
	if err == nil {
		t.Fatal("expected pre-create config error")
	}
	if !strings.Contains(err.Error(), "exited with status 9") {
		t.Fatalf("pre-create error = %v, want exit status", err)
	}
	if !strings.Contains(logger.String(), "[pre-create] before-abort") {
		t.Fatalf("logger output missing config hook stdout:\n%s", logger.String())
	}
}

func TestRunnerProjectConfigAllowAlwaysPersistsHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	configPath := writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       trustPath,
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowAlways },
	}
	if _, ok := runner.StartupCommand(cwd); !ok {
		t.Fatal("StartupCommand() ok = false, want true")
	}

	sum, _, err := hashHookFile(configPath)
	if err != nil {
		t.Fatalf("hashHookFile: %v", err)
	}
	store, err := loadTrustedProjects(trustPath)
	if err != nil {
		t.Fatalf("loadTrustedProjects: %v", err)
	}
	file, ok := store.trustedFile(cwd, projectConfigRelativePath)
	if !ok {
		t.Fatalf("trusted config missing: %#v", store)
	}
	if file.SHA256 != sum {
		t.Fatalf("stored sha256 = %q, want %q", file.SHA256, sum)
	}
}

func TestTrustProjectConfigPersistsHash(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	configPath := writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)

	sum, err := TrustProjectConfig(cwd, trustPath)
	if err != nil {
		t.Fatalf("TrustProjectConfig() error = %v", err)
	}
	wantSum, _, err := hashHookFile(configPath)
	if err != nil {
		t.Fatalf("hashHookFile() error = %v", err)
	}
	if sum != wantSum {
		t.Fatalf("TrustProjectConfig hash = %q, want %q", sum, wantSum)
	}
	store, err := loadTrustedProjects(trustPath)
	if err != nil {
		t.Fatalf("loadTrustedProjects() error = %v", err)
	}
	file, ok := store.trustedFile(cwd, projectConfigRelativePath)
	if !ok {
		t.Fatalf("trusted config missing: %#v", store)
	}
	if file.SHA256 != wantSum {
		t.Fatalf("stored sha256 = %q, want %q", file.SHA256, wantSum)
	}
	info, err := os.Stat(trustPath)
	if err != nil {
		t.Fatalf("Stat(trust store) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("trust store mode = %#o, want 0600", got)
	}
}

func TestUntrustProjectConfigClearsIsTrusted(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)

	if _, err := TrustProjectConfig(cwd, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig() error = %v", err)
	}
	removed, err := UntrustProjectConfig(cwd, trustPath)
	if err != nil {
		t.Fatalf("UntrustProjectConfig() error = %v", err)
	}
	if !removed {
		t.Fatalf("UntrustProjectConfig returned removed=false, want true")
	}
	trusted, _, err := IsProjectConfigTrusted(cwd, trustPath)
	if err != nil {
		t.Fatalf("IsProjectConfigTrusted() error = %v", err)
	}
	if trusted {
		t.Fatalf("config still reported as trusted after untrust")
	}
	removedAgain, err := UntrustProjectConfig(cwd, trustPath)
	if err != nil {
		t.Fatalf("UntrustProjectConfig() second call error = %v", err)
	}
	if removedAgain {
		t.Fatalf("UntrustProjectConfig second call removed=true, want false (idempotent)")
	}
}

func TestRunnerProjectConfigKillSwitchDisablesConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}

	t.Setenv("PROJMUX_PROJECT_HOOKS", "off")
	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "should-not-run"
`)
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			t.Fatal("project config prompt should not be called when kill switch is off")
			return ProjectHookDeny
		},
	}

	if command, ok := runner.StartupCommand(cwd); ok || command != "" {
		t.Fatalf("StartupCommand() = %q, %v; want empty false", command, ok)
	}
}

func TestRunnerProjectSessionEnvUsesTrustedGenericEnvWithoutKubeProjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[env]
FOO = "bar"
`)
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
	}

	env := runner.ProjectSessionEnv(cwd)
	want := map[string]string{"FOO": "bar"}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("ProjectSessionEnv()[%s] = %q, want %q; env=%#v", key, env[key], value, env)
		}
	}
	for _, removed := range []string{"PROJMUX_KUBE_CONTEXT", "KUBE_CONTEXT", "PROJMUX_KUBE_NAMESPACE", "KUBE_NAMESPACE"} {
		if _, ok := env[removed]; ok {
			t.Fatalf("ProjectSessionEnv() synthesized retired key %s: %#v", removed, env)
		}
	}
}

func TestRunnerProjectConfigTrustCacheSharedByHooksAndSessionEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.pre-create]
run = "true"

[env]
FOO = "bar"
`)
	promptCalls := 0
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			promptCalls++
			return ProjectHookAllowOnce
		},
	}

	if _, err := runner.Run(context.Background(), EventPreCreate, Context{CWD: cwd, SessionName: "workspace"}); err != nil {
		t.Fatalf("Run(pre-create) error = %v", err)
	}
	env := runner.ProjectSessionEnv(cwd)
	if env["FOO"] != "bar" {
		t.Fatalf("ProjectSessionEnv()[FOO] = %q, want bar; env=%#v", env["FOO"], env)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
}

func writeProjectConfig(t *testing.T, cwd, body string) string {
	t.Helper()
	path := filepath.Join(cwd, projectConfigRelativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestProjectUpdateConfigReleaseChannelRoundTrip(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig("[update]\nrelease_channel = \"rc\"\n")
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if cfg.Update.ReleaseChannel != "rc" {
		t.Fatalf("Update.ReleaseChannel = %q, want %q", cfg.Update.ReleaseChannel, "rc")
	}

	rendered := renderProjectConfig(cfg)
	if !strings.Contains(rendered, "[update]") || !strings.Contains(rendered, "release_channel = \"rc\"") {
		t.Fatalf("rendered = %q, want [update] release_channel = \"rc\"", rendered)
	}

	reparsed, err := ParseProjectConfig(rendered)
	if err != nil {
		t.Fatalf("re-parse error = %v", err)
	}
	if reparsed.Update != cfg.Update {
		t.Fatalf("re-parsed Update = %#v, want %#v", reparsed.Update, cfg.Update)
	}
}

// TestProjectUpdateConfigOmitsAnUnsetReleaseChannel keeps "never configured"
// distinguishable from "configured to the default" on disk. The reader treats
// an absent key as the environment's turn to answer, so rendering an empty
// value would silently retire PROJMUX_RELEASE_CHANNEL for every install whose
// config is rewritten for an unrelated reason.
func TestProjectUpdateConfigOmitsAnUnsetReleaseChannel(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig("[ui]\nlocale = \"en\"\n")
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if rendered := renderProjectConfig(cfg); strings.Contains(rendered, "[update]") {
		t.Fatalf("rendered = %q, want no [update] section for an unset release channel", rendered)
	}
	if _, err := ParseProjectConfig("[update]\nunknown_key = \"x\"\n"); err == nil {
		t.Fatal("expected unsupported update key to error")
	}
}
