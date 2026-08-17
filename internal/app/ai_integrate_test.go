package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAIIntegrateAntigravityInstallsNamedEntryWithAbsoluteExplicitEvents(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.executable = func() (string, error) { return "/opt/projmux/bin/projmux", nil }

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "antigravity"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate antigravity error = %v", err)
	}
	path := filepath.Join(home, antigravityHooksRelativePath)
	data := readCodexTestFile(t, path)
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &hooks); err != nil {
		t.Fatalf("hooks JSON = %s: %v", data, err)
	}
	if len(hooks) != 1 || hooks[antigravityManagedHookName] == nil {
		t.Fatalf("hooks = %#v, want exactly named projmux entry", hooks)
	}
	managed := string(hooks[antigravityManagedHookName])
	for _, event := range antigravityManagedEvents {
		if !strings.Contains(managed, `"`+event+`"`) || !strings.Contains(managed, "--event "+event) {
			t.Fatalf("managed entry missing explicit %s command:\n%s", event, managed)
		}
	}
	for _, want := range []string{"/opt/projmux/bin/projmux", antigravityManagedMarker, `printf '%s\\n' '{}'`, `\"decision\":\"stop\"`, `"matcher": "*"`} {
		if !strings.Contains(managed, want) {
			t.Fatalf("managed entry = %s, want %q", managed, want)
		}
	}
	for _, forbidden := range []string{"PreToolUse", `"decision":"allow"`, `"decision":"deny"`, `"decision":"ask"`, "force_continue"} {
		if strings.Contains(managed, forbidden) {
			t.Fatalf("managed entry = %s, must not contain %q", managed, forbidden)
		}
	}
	if !strings.Contains(stdout.String(), "configured Antigravity hooks") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestAntigravityManagedHookGolden(t *testing.T) {
	got, err := encodeAntigravityManagedHook("/opt/projmux/bin/projmux")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "antigravity", "managed_hooks_entry.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got+"\n" != string(want) {
		t.Fatalf("managed Antigravity entry golden mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAIIntegrateAntigravityManagesOfficialStackedStatusLineAndRestoresSettings(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.executable = func() (string, error) { return "/opt/projmux/bin/projmux", nil }
	settingsPath := filepath.Join(home, antigravitySettingsRelativePath)
	original := "{\n  \"theme\": \"keep\",\n  \"unknown\": {\"spacing\" : [3, 2, 1]}\n}\n"
	writeCodexTestFile(t, settingsPath, original)
	if err := os.Chmod(settingsPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	installed := readCodexTestFile(t, settingsPath)
	if !strings.Contains(installed, `"type": "command"`) || !strings.Contains(installed, `"enabled": true`) || !strings.Contains(installed, `"stack_with_default": true`) || !strings.Contains(installed, antigravityManagedStatusLineMarker) {
		t.Fatalf("settings missing official managed statusLine:\n%s", installed)
	}
	if !strings.Contains(installed, `"unknown": {"spacing" : [3, 2, 1]}`) {
		t.Fatalf("install normalized unrelated settings:\n%s", installed)
	}
	if info, err := os.Stat(settingsPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v err=%v, want 0600", info.Mode().Perm(), err)
	}
	first := installed
	if err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readCodexTestFile(t, settingsPath); got != first {
		t.Fatalf("reinstall changed settings bytes:\n%s", got)
	}
	if err := cmd.Run([]string{"integrate", "antigravity", "--remove"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readCodexTestFile(t, settingsPath); got != original {
		t.Fatalf("remove did not exactly restore unrelated settings:\ngot:\n%s\nwant:\n%s", got, original)
	}
	if info, err := os.Stat(settingsPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored settings mode = %v err=%v, want 0600", info.Mode().Perm(), err)
	}
}

func TestAIIntegrateAntigravityStatusLineEmptyAndConflictPolicy(t *testing.T) {
	for _, empty := range []string{"null", "{}"} {
		t.Run("empty "+empty, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.readFile = os.ReadFile
			path := filepath.Join(home, antigravitySettingsRelativePath)
			writeCodexTestFile(t, path, `{"keep":true,"statusLine":`+empty+`}`)
			if err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(readCodexTestFile(t, path), antigravityManagedStatusLineMarker) {
				t.Fatalf("empty statusLine was not installed: %s", readCodexTestFile(t, path))
			}
		})
	}

	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	settingsPath := filepath.Join(home, antigravitySettingsRelativePath)
	hooksPath := filepath.Join(home, antigravityHooksRelativePath)
	custom := `{"theme":"keep","statusLine":{"type":"command","command":"/home/user/custom","stack_with_default":true}}`
	writeCodexTestFile(t, settingsPath, custom)
	err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unmanaged \"statusLine\" command") || !strings.Contains(err.Error(), "/statusline delete") {
		t.Fatalf("conflict error = %v", err)
	}
	if got := readCodexTestFile(t, settingsPath); got != custom {
		t.Fatalf("conflict changed settings: %s", got)
	}
	if _, statErr := os.Stat(hooksPath); !os.IsNotExist(statErr) {
		t.Fatalf("statusLine preflight conflict left hooks behind: %v", statErr)
	}
	// Removal preserves separately owned custom statusline state while still
	// removing the independently managed Phase 2 named hooks entry.
	managedHooks, err := encodeAntigravityManagedHook("/tmp/projmux")
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, hooksPath, `{"projmux":`+managedHooks+`}`)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "antigravity", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("remove with custom statusline error = %v", err)
	}
	if got := readCodexTestFile(t, settingsPath); got != custom {
		t.Fatalf("remove changed unmanaged statusline settings: %s", got)
	}
	if got := readCodexTestFile(t, hooksPath); strings.Contains(got, antigravityManagedMarker) {
		t.Fatalf("remove retained independently managed hooks: %s", got)
	}
	if !strings.Contains(stdout.String(), "preserved unmanaged Antigravity statusline") {
		t.Fatalf("stdout = %q, want preservation diagnostic", stdout.String())
	}
}

func TestAIIntegrateAntigravityStatusLineMalformedSymlinkAndNewMode(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		home := t.TempDir()
		cmd := testAICommand(home)
		cmd.readFile = os.ReadFile
		path := filepath.Join(home, antigravitySettingsRelativePath)
		writeCodexTestFile(t, path, `{"statusLine":`)
		err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "parse Antigravity settings") || !strings.Contains(err.Error(), "malformed JSON") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		home := t.TempDir()
		cmd := testAICommand(home)
		cmd.readFile = os.ReadFile
		path := filepath.Join(home, antigravitySettingsRelativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "settings.json")
		writeCodexTestFile(t, target, `{}`)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "refusing symlink path component") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("new file private", func(t *testing.T) {
		home := t.TempDir()
		cmd := testAICommand(home)
		cmd.readFile = os.ReadFile
		if err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(home, antigravitySettingsRelativePath))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v err=%v, want 0600", info.Mode().Perm(), err)
		}
	})
	t.Run("settings permission preflight", func(t *testing.T) {
		home := t.TempDir()
		cmd := testAICommand(home)
		cmd.readFile = os.ReadFile
		settingsPath := filepath.Join(home, antigravitySettingsRelativePath)
		writeCodexTestFile(t, settingsPath, `{}`)
		if err := os.Chmod(settingsPath, 0o400); err != nil {
			t.Fatal(err)
		}
		err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "Antigravity settings") {
			t.Fatalf("error = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(home, antigravityHooksRelativePath)); !os.IsNotExist(statErr) {
			t.Fatalf("permission preflight left hooks behind: %v", statErr)
		}
	})
}

func TestAntigravityManagedStatusLineGolden(t *testing.T) {
	got, err := encodeAntigravityManagedStatusLine("/opt/projmux/bin/projmux")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"type\": \"command\",\n  \"command\": \"'/opt/projmux/bin/projmux' internal agent-hook ingest antigravity-hook --event Statusline # projmux-managed:antigravity-statusline:v1\",\n  \"enabled\": true,\n  \"stack_with_default\": true\n}"
	if got != want || !isManagedAntigravityStatusLine(got) {
		t.Fatalf("managed statusLine mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	for _, invalid := range []string{
		`{"type":"command","command":"'/tmp/projmux' ai ingest antigravity-hook --event Statusline # projmux-managed:antigravity-statusline:v1","enabled":true,"stack_with_default":false}`,
		`{"type":"command","command":"echo fake ai ingest antigravity-hook --event Statusline # projmux-managed:antigravity-statusline:v1","enabled":true,"stack_with_default":true}`,
		`{"type":"command","command":"'/tmp/projmux' ai ingest antigravity-hook --event Statusline # projmux-managed:antigravity-statusline:v1","enabled":true,"stack_with_default":true,"extra":1}`,
	} {
		if isManagedAntigravityStatusLine(invalid) {
			t.Fatalf("accepted non-exact managed object: %s", invalid)
		}
	}
}

func TestAIIntegrateAntigravityPreservesUnmanagedBytesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, antigravityHooksRelativePath)
	unmanaged := `"user-hook": { "enabled": false, "note": "projmux ai ingest antigravity-hook is documentation, not a command", "FutureEvent": [{"unknown": [3, 2, 1]}], "PostToolUse": [{"matcher":"run_command","hooks":[{"command":"./keep.sh","vendorField":{"x":true}}]}] }`
	writeCodexTestFile(t, path, "{\n  "+unmanaged+",\n  \"top-level-unknown\": [true, {\"raw\":\"keep spacing\"}]\n}\n")

	if err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	first := readCodexTestFile(t, path)
	if !strings.Contains(first, unmanaged) || !strings.Contains(first, `"top-level-unknown": [true, {"raw":"keep spacing"}]`) {
		t.Fatalf("install normalized or lost unmanaged JSON:\n%s", first)
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "antigravity"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	second := readCodexTestFile(t, path)
	if second != first {
		t.Fatalf("reinstall changed hooks:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	if err := cmd.Run([]string{"integrate", "antigravity", "--remove"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	afterRemove := readCodexTestFile(t, path)
	if strings.Contains(afterRemove, antigravityManagedMarker) || !strings.Contains(afterRemove, unmanaged) || !strings.Contains(afterRemove, `"top-level-unknown": [true, {"raw":"keep spacing"}]`) {
		t.Fatalf("remove changed unmanaged JSON or retained managed entry:\n%s", afterRemove)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(afterRemove), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded[antigravityManagedHookName]; ok {
		t.Fatalf("managed entry remains after remove: %s", afterRemove)
	}
	var removeAgain bytes.Buffer
	if err := cmd.Run([]string{"integrate", "antigravity", "--remove"}, &removeAgain, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readCodexTestFile(t, path); got != afterRemove || !strings.Contains(removeAgain.String(), "no changes") {
		t.Fatalf("second remove changed hooks or lacked no-op diagnostic: hooks=%q stdout=%q", got, removeAgain.String())
	}
}

func TestAIIntegrateAntigravityDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "antigravity", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, antigravityHooksRelativePath)); !os.IsNotExist(err) {
		t.Fatalf("hooks stat = %v, want missing", err)
	}
	for _, want := range []string{"dry-run", "/tmp/projmux", "PreInvocation, PostInvocation, PostToolUse, Stop", "--event Stop"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestAIIntegrateAntigravityReportsUnmanagedConflicts(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "reserved named entry", content: `{"projmux":{"Stop":[{"command":"echo user"}]}}`, want: `unmanaged named entry "projmux"`},
		{name: "duplicate ingest elsewhere", content: `{"user":{"Stop":[{"command":"projmux ai ingest antigravity-hook --event Stop"}]}}`, want: `outside the managed "projmux" entry`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.readFile = os.ReadFile
			path := filepath.Join(home, antigravityHooksRelativePath)
			writeCodexTestFile(t, path, tt.content)
			before := readCodexTestFile(t, path)
			err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "--dry-run") {
				t.Fatalf("error = %v, want %q with dry-run guidance", err, tt.want)
			}
			if got := readCodexTestFile(t, path); got != before {
				t.Fatalf("conflict changed hooks: %q -> %q", before, got)
			}
			var stdout bytes.Buffer
			if err := cmd.Run([]string{"integrate", "antigravity", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("dry-run = %q, want %q", stdout.String(), tt.want)
			}
		})
	}
}

func TestAIIntegrateAntigravityReportsMalformedAndInvalidRoot(t *testing.T) {
	for _, tt := range []struct{ name, content, want string }{
		{name: "malformed", content: `{"user":`, want: "malformed JSON"},
		{name: "array root", content: `[]`, want: "top-level value must be a JSON object"},
		{name: "duplicate key", content: `{"user":{},"user":{}}`, want: `duplicate top-level key "user"`},
		{name: "empty", content: ``, want: "file is empty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.readFile = os.ReadFile
			path := filepath.Join(home, antigravityHooksRelativePath)
			writeCodexTestFile(t, path, tt.content)
			err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("error = %v, want path and %q", err, tt.want)
			}
		})
	}
}

func TestAIIntegrateAntigravityRejectsSymlinkComponents(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		home := t.TempDir()
		cmd := testAICommand(home)
		cmd.readFile = os.ReadFile
		target := filepath.Join(t.TempDir(), "hooks.json")
		writeCodexTestFile(t, target, `{}`)
		path := filepath.Join(home, antigravityHooksRelativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "refusing symlink path component") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		home := t.TempDir()
		cmd := testAICommand(home)
		cmd.readFile = os.ReadFile
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(home, ".gemini")); err != nil {
			t.Fatal(err)
		}
		err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "refusing symlink path component") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestAIIntegrateAntigravityReportsPermissionFailures(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		cmd := testAICommand(t.TempDir())
		cmd.readFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
		err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "read Antigravity hooks") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("write", func(t *testing.T) {
		cmd := testAICommand(t.TempDir())
		cmd.readFile = os.ReadFile
		cmd.writeFile = func(string, []byte, os.FileMode) error { return fs.ErrPermission }
		err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "write Antigravity hooks") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("mkdir", func(t *testing.T) {
		cmd := testAICommand(t.TempDir())
		cmd.readFile = os.ReadFile
		cmd.mkdirAll = func(string, os.FileMode) error { return fs.ErrPermission }
		err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "create Antigravity hooks directory") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestAIIntegrateAntigravityRejectsRelativeExecutable(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readFile = os.ReadFile
	cmd.executable = func() (string, error) { return "bin/projmux", nil }
	err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("error = %v", err)
	}
}

func TestAIIntegrateCodexDefaultsToManagedHooks(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}

	path := filepath.Join(home, codexConfigRelativePath)
	got := readCodexTestFile(t, path)
	if !strings.Contains(got, codexHooksMarkerBegin) || !strings.Contains(got, codexHookCommand) || strings.Contains(got, "codex-notify") {
		t.Fatalf("config missing managed hooks block or unexpectedly installed removed notify integration:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "configured Codex hooks") {
		t.Fatalf("stdout = %q, want configured message", stdout.String())
	}
}

func TestAIIntegrateCodexDefaultHooksInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run integrate codex error = %v", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	first := readCodexTestFile(t, path)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run integrate codex error = %v", err)
	}
	second := readCodexTestFile(t, path)
	if second != first {
		t.Fatalf("second install changed config:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("stdout = %q, want no changes", stdout.String())
	}
}

func TestAIIntegrateCodexDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --dry-run error = %v", err)
	}

	path := filepath.Join(home, codexConfigRelativePath)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want missing file", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, codexHookCommand) || strings.Contains(out, "codex-notify") {
		t.Fatalf("stdout = %q, want dry-run managed hooks preview", out)
	}
}

func TestAIIntegrateCodexRejectsRemovedModeFlag(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	err := cmd.Run([]string{"integrate", "codex", "--mode", "legacy-notify"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run integrate codex expected removed flag error, got nil")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined: -mode") {
		t.Fatalf("error = %v, want removed mode flag error", err)
	}
}

func TestAIIntegrateCodexHooksDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --dry-run error = %v", err)
	}

	path := filepath.Join(home, codexConfigRelativePath)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want missing file", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, codexHooksMarkerBegin) || !strings.Contains(out, codexHookCommand) || !strings.Contains(out, "hooks = true") {
		t.Fatalf("stdout = %q, want hooks dry-run preview", out)
	}
}

func TestAIIntegrateCodexHooksInstallsManagedBlockAndPreservesConfig(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, `model = "gpt-5.1-codex"
[features]
experimental_resume = true

[[hooks.Stop]]
matcher = "*"
[[hooks.Stop.hooks]]
type = "command"
command = "echo keep"

# keep this user setting
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if !strings.Contains(got, codexHooksMarkerBegin) || !strings.Contains(got, "[features]") || !strings.Contains(got, "hooks = true") {
		t.Fatalf("config missing hooks feature block:\n%s", got)
	}
	if strings.Contains(got, "codex_hooks = true") {
		t.Fatalf("config kept deprecated codex_hooks feature:\n%s", got)
	}
	if strings.Count(got, "[features]") != 1 {
		t.Fatalf("config duplicated [features]:\n%s", got)
	}
	if strings.Index(got, `model = "gpt-5.1-codex"`) > strings.Index(got, codexHooksMarkerBegin) {
		t.Fatalf("managed hooks block was inserted before root config, which would scope root keys into the last hook table:\n%s", got)
	}
	codexHookEvents := defaultAIHookInstallEvents(aiHookProviderCodex)
	if len(codexHookEvents) != 8 {
		t.Fatalf("default Codex hook catalog has %d events, want 8", len(codexHookEvents))
	}
	for _, event := range codexHookEvents {
		assertCodexHookNestedHandler(t, got, event)
	}
	if strings.Count(got, codexHookCommand) != len(codexHookEvents) {
		t.Fatalf("config command count = %d, want %d:\n%s", strings.Count(got, codexHookCommand), len(codexHookEvents), got)
	}
	if !strings.Contains(got, `model = "gpt-5.1-codex"`) ||
		!strings.Contains(got, "experimental_resume = true") ||
		!strings.Contains(got, "echo keep") ||
		!strings.Contains(got, "# keep this user setting") {
		t.Fatalf("config did not preserve unmanaged content:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "configured Codex hooks") {
		t.Fatalf("stdout = %q, want configured hooks message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksMigratesDeprecatedManagedFeature(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, `[features]
experimental_resume = true
# projmux-managed:codex-hooks-feature:v1
codex_hooks = true
`)

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if strings.Contains(got, "codex_hooks = true") {
		t.Fatalf("config kept deprecated codex_hooks feature:\n%s", got)
	}
	if strings.Count(got, codexHooksFeatureMarker) != 1 || !strings.Contains(got, "hooks = true") {
		t.Fatalf("config did not migrate managed feature cleanly:\n%s", got)
	}
	if !strings.Contains(got, "experimental_resume = true") {
		t.Fatalf("config did not preserve existing feature:\n%s", got)
	}
}

func TestAIIntegrateCodexHooksReplacesOldManagedBlock(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, oldCodexHooksBlock()+`model = "gpt-5.1-codex"
`)

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if strings.Contains(got, `command = "projmux ai ingest codex-hook`) && !strings.Contains(got, "[[hooks.PermissionRequest.hooks]]") {
		t.Fatalf("config still appears to use flat hook command schema:\n%s", got)
	}
	codexHookEvents := defaultAIHookInstallEvents(aiHookProviderCodex)
	for _, event := range codexHookEvents {
		assertCodexHookNestedHandler(t, got, event)
	}
	if strings.Contains(got, "codex_hooks = true") {
		t.Fatalf("config kept deprecated codex_hooks feature:\n%s", got)
	}
}

func TestAIIntegrateCodexHooksInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run integrate codex error = %v", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	first := readCodexTestFile(t, path)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run integrate codex error = %v", err)
	}
	second := readCodexTestFile(t, path)
	if second != first {
		t.Fatalf("second hooks install changed config:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("stdout = %q, want no changes", stdout.String())
	}
}

func TestAIIntegrateCodexHooksRemoveOnlySelectedManagedBlock(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, codexHooksBlock(true)+`model = "gpt-5.1-codex"
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --remove error = %v", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, codexHooksMarkerBegin) || !strings.Contains(got, `model = "gpt-5.1-codex"`) {
		t.Fatalf("config after selected hooks remove =\n%s", got)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed Codex hooks") {
		t.Fatalf("stdout = %q, want removed hooks message", stdout.String())
	}
}

func TestAIIntegrateCodexRemoveWithoutModeRemovesManagedHooks(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, codexHooksBlock(true)+`model = "gpt-5.1-codex"
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --remove error = %v", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, codexHooksMarkerBegin) || !strings.Contains(got, `model = "gpt-5.1-codex"`) {
		t.Fatalf("config after remove-all =\n%s", got)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed Codex hooks") {
		t.Fatalf("stdout = %q, want removed hooks message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksRemoveCleansManagedFeatureEntry(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, `[features]
experimental_resume = true
`+"\n"+codexHooksBlock(false))

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --remove error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if strings.Contains(got, codexHooksMarkerBegin) || strings.Contains(got, codexHooksFeatureMarker) || strings.Contains(got, "hooks = true") || strings.Contains(got, "codex_hooks = true") {
		t.Fatalf("config after hooks remove kept managed entries:\n%s", got)
	}
	if !strings.Contains(got, "experimental_resume = true") {
		t.Fatalf("config after hooks remove did not preserve feature table:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed Codex hooks") {
		t.Fatalf("stdout = %q, want removed hooks message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksDoesNotTouchClaudeSettings(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	claudePath := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, claudePath, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo claude"}]}]}}`+"\n")
	before := readCodexTestFile(t, claudePath)

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}
	if got := readCodexTestFile(t, claudePath); got != before {
		t.Fatalf("Claude settings changed:\nbefore:%s\nafter:%s", before, got)
	}
}

func TestAIIntegrateClaudeInstallsManagedHooks(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{
  "theme": "dark",
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit",
        "hooks": [
          {
            "type": "command",
            "command": "echo keep"
          }
        ]
      }
    ]
  }
}
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude error = %v", err)
	}

	settings := readClaudeSettingsTestFile(t, path)
	if got := settings["theme"]; got != "dark" {
		t.Fatalf("theme = %#v, want preserved dark", got)
	}
	claudeHookEvents := defaultAIHookInstallEvents(aiHookProviderClaude)
	if len(claudeHookEvents) != 29 {
		t.Fatalf("default Claude hook catalog has %d events, want 29", len(claudeHookEvents))
	}
	for _, event := range claudeHookEvents {
		if !claudeSettingsHasManagedCommand(t, settings, event) {
			t.Fatalf("settings missing managed command for %s:\n%s", event, readCodexTestFile(t, path))
		}
	}
	if !strings.Contains(readCodexTestFile(t, path), `"PostToolUse"`) || !strings.Contains(readCodexTestFile(t, path), "echo keep") {
		t.Fatalf("settings did not preserve user hook:\n%s", readCodexTestFile(t, path))
	}
	if !strings.Contains(stdout.String(), "configured Claude Code hooks") {
		t.Fatalf("stdout = %q, want configured message", stdout.String())
	}
}

func TestAIIntegrateClaudeInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run integrate claude error = %v", err)
	}
	path := filepath.Join(home, claudeSettingsRelativePath)
	first := readCodexTestFile(t, path)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run integrate claude error = %v", err)
	}
	second := readCodexTestFile(t, path)
	if second != first {
		t.Fatalf("second install changed settings:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("stdout = %q, want no changes", stdout.String())
	}
}

func TestAIIntegrateClaudeDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude --dry-run error = %v", err)
	}

	path := filepath.Join(home, claudeSettingsRelativePath)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("settings stat err = %v, want missing file", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, claudeHookCommand) {
		t.Fatalf("stdout = %q, want dry-run managed hook preview", out)
	}
}

func TestAIIntegrateClaudeRefusesUnmanagedProjmuxHook(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux ai ingest claude-hook >/dev/null 2>&1 || true"
          }
        ]
      }
    ]
  }
}
`)
	before := readCodexTestFile(t, path)

	err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run integrate claude expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "unmanaged projmux ingest command") || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("error = %v, want conflict with dry-run guidance", err)
	}
	if got := readCodexTestFile(t, path); got != before {
		t.Fatalf("stale unmanaged settings were modified automatically:\nbefore:\n%s\nafter:\n%s", before, got)
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude --dry-run with conflict error = %v", err)
	}
	if !strings.Contains(stdout.String(), "would refuse") || !strings.Contains(stdout.String(), "unmanaged projmux ingest command") {
		t.Fatalf("stdout = %q, want conflict preview", stdout.String())
	}
}

func TestAIIntegrateClaudeRemoveOnlyManagedHooks(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude setup error = %v", err)
	}
	settings := readClaudeSettingsTestFile(t, path)
	hooks := settings["hooks"].(map[string]any)
	hooks["Notification"] = append(hooks["Notification"].([]any), map[string]any{
		"matcher": "idle_prompt",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": "echo user-notify",
			},
		},
	})
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, path, string(data)+"\n")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude --remove error = %v", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, claudeHookManagedMarker) || !strings.Contains(got, "echo user-notify") {
		t.Fatalf("settings after remove =\n%s", got)
	}
	claudeHookEvents := defaultAIHookInstallEvents(aiHookProviderClaude)
	for _, event := range claudeHookEvents {
		if event == "Notification" {
			continue
		}
		if strings.Contains(got, `"`+event+`"`) {
			t.Fatalf("settings retained empty managed-only event %s:\n%s", event, got)
		}
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed") {
		t.Fatalf("stdout = %q, want removed message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksUsesCatalogOverride(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".xdg-config")
	writeCodexTestFile(t, filepath.Join(configHome, "projmux", "ai-hooks.d", "codex.json"), `{
  "provider": "codex",
  "observed_version": "codex-test",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
    { "name": "ExperimentalEvent", "install": true, "action": "quiet" }
  ]
}
`)
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return configHome
		default:
			return ""
		}
	}
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}

	got := readCodexTestFile(t, filepath.Join(home, codexConfigRelativePath))
	if strings.Contains(got, "[[hooks.Stop]]") {
		t.Fatalf("config installed catalog-disabled Stop event:\n%s", got)
	}
	assertCodexHookNestedHandler(t, got, "ExperimentalEvent")
}

func TestAIIntegrateClaudeUsesCatalogOverride(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".xdg-config")
	writeCodexTestFile(t, filepath.Join(configHome, "projmux", "ai-hooks.d", "claude.json"), `{
  "provider": "claude",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
    { "name": "ExperimentalEvent", "install": true, "action": "quiet" }
  ]
}
`)
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return configHome
		default:
			return ""
		}
	}
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude error = %v", err)
	}

	settings := readClaudeSettingsTestFile(t, filepath.Join(home, claudeSettingsRelativePath))
	if claudeSettingsHasManagedCommand(t, settings, "Stop") {
		t.Fatalf("settings installed catalog-disabled Stop event:\n%s", readCodexTestFile(t, filepath.Join(home, claudeSettingsRelativePath)))
	}
	if !claudeSettingsHasManagedCommand(t, settings, "ExperimentalEvent") {
		t.Fatalf("settings missing override event:\n%s", readCodexTestFile(t, filepath.Join(home, claudeSettingsRelativePath)))
	}
}

func TestAIIntegrateClaudeRemoveScansManagedMarkersOutsideCatalog(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{
  "hooks": {
    "OldRemovedEvent": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux ai ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ],
    "Notification": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo keep"
          }
        ]
      }
    ]
  }
}
`)

	if err := cmd.Run([]string{"integrate", "claude", "--remove"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude --remove error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if strings.Contains(got, "OldRemovedEvent") || strings.Contains(got, claudeHookManagedMarker) {
		t.Fatalf("settings retained stale managed marker outside catalog:\n%s", got)
	}
	if !strings.Contains(got, "echo keep") {
		t.Fatalf("settings removed user hook:\n%s", got)
	}
}

func TestAIIntegrateClaudeConflictScansUnmanagedCommandsOutsideCatalog(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{
  "hooks": {
    "OldRemovedEvent": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux ai ingest claude-hook >/dev/null 2>&1 || true"
          }
        ]
      }
    ]
  }
}
`)

	err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run integrate claude expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "OldRemovedEvent") || !strings.Contains(err.Error(), "unmanaged projmux ingest command") {
		t.Fatalf("error = %v, want stale event unmanaged command conflict", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, claudeHookManagedMarker) {
		t.Fatalf("settings was modified unexpectedly:\n%s", got)
	}
}

func TestAIIntegrateTmuxBellDryRunPlansInstallCommands(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = tmuxBellReadFixture("")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "tmux-bell", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate tmux-bell --dry-run error = %v", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no writes on dry-run", cmdRecorder(cmd).commands)
	}
	out := stdout.String()
	for _, want := range []string{
		"set-option -g allow-passthrough on",
		"set-option -g monitor-bell on",
		"set-option -g bell-action other",
		"set-hook -ag alert-bell",
		"#{pane_id}",
		tmuxBellManagedMarker,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestAIIntegrateTmuxBellInstallAppendsManagedHookAndPreservesExisting(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = tmuxBellReadFixture("alert-bell[0] run-shell -b 'echo user-hook'\n")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "tmux-bell"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate tmux-bell error = %v", err)
	}

	wantCommands := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-g", "allow-passthrough", "on"}},
		{name: "tmux", args: []string{"set-option", "-g", "monitor-bell", "on"}},
		{name: "tmux", args: []string{"set-option", "-g", "bell-action", "other"}},
		{name: "tmux", args: []string{"set-hook", "-ag", tmuxBellHookName, tmuxBellHookCommand}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, wantCommands)
	}
	if !strings.Contains(stdout.String(), "configured tmux bell fallback") {
		t.Fatalf("stdout = %q, want configured message", stdout.String())
	}
}

func TestAIIntegrateTmuxBellInstallSkipsDuplicateManagedHook(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = tmuxBellReadFixture("alert-bell[1] " + tmuxBellHookCommand + "\n")

	if err := cmd.Run([]string{"integrate", "tmux-bell"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate tmux-bell error = %v", err)
	}
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "tmux" && len(command.args) >= 2 && command.args[0] == "set-hook" && command.args[1] == "-ag" {
			t.Fatalf("commands = %#v, did not want duplicate managed hook append", cmdRecorder(cmd).commands)
		}
	}
}

func TestAIIntegrateTmuxBellRemoveOnlyManagedHook(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = tmuxBellReadFixture(strings.Join([]string{
		"alert-bell[0] run-shell -b 'echo user-hook'",
		"alert-bell[2] " + tmuxBellHookCommand,
	}, "\n") + "\n")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "tmux-bell", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate tmux-bell --remove error = %v", err)
	}
	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-hook", "-gu", "alert-bell[2]"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed tmux bell hook") {
		t.Fatalf("stdout = %q, want removed message", stdout.String())
	}
}

func TestManagedIngestProducerMigrationUpgradesV0101AndRepeatsWithoutWrites(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.executable = func() (string, error) { return "/opt/projmux/bin/projmux", nil }

	codexPath := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, codexPath, strings.ReplaceAll(codexHooksBlock(true), codexHookCommand, legacyCodexHookCommand))
	claudePath := filepath.Join(home, claudeSettingsRelativePath)
	legacyClaude := strings.ReplaceAll(claudeHookCommand, canonicalClaudeHookRoute, legacyClaudeHookRoute)
	writeCodexTestFile(t, claudePath, "{\n  \"hooks\": {\n    \"Notification\": [{\"hooks\": [{\"type\": \"command\", \"command\": "+string(mustJSONMarshal(legacyClaude))+"}]}]\n  }\n}\n")
	hooksPath := filepath.Join(home, antigravityHooksRelativePath)
	hooks, err := encodeAntigravityManagedHook("/opt/projmux/bin/projmux")
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, hooksPath, "{\n  \"projmux\": "+strings.ReplaceAll(hooks, antigravityCanonicalIngestPath, antigravityLegacyIngestPath)+"\n}\n")
	statusPath := filepath.Join(home, antigravitySettingsRelativePath)
	status, err := encodeAntigravityManagedStatusLine("/opt/projmux/bin/projmux")
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, statusPath, "{\n  \"statusLine\": "+strings.ReplaceAll(status, antigravityCanonicalIngestPath, antigravityLegacyIngestPath)+"\n}\n")

	writes := 0
	cmd.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		return os.WriteFile(path, data, mode)
	}
	count, _, err := cmd.beginManagedIngestProducerFileMigration()
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 || writes != 4 {
		t.Fatalf("first migration count=%d writes=%d, want 4/4", count, writes)
	}
	for _, path := range []string{codexPath, claudePath, hooksPath, statusPath} {
		got := readCodexTestFile(t, path)
		if strings.Contains(got, " ai ingest ") || !strings.Contains(got, "internal agent-hook ingest") {
			t.Fatalf("%s did not converge to canonical ingest:\n%s", path, got)
		}
	}
	count, _, err = cmd.beginManagedIngestProducerFileMigration()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || writes != 4 {
		t.Fatalf("repeat migration count=%d total writes=%d, want 0/4", count, writes)
	}
}

func TestManagedIngestProducerMigrationPlansAllConflictsBeforeWrites(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	codexPath := filepath.Join(home, codexConfigRelativePath)
	original := strings.ReplaceAll(codexHooksBlock(true), codexHookCommand, legacyCodexHookCommand)
	writeCodexTestFile(t, codexPath, original)
	writeCodexTestFile(t, filepath.Join(home, claudeSettingsRelativePath), `{"hooks":{"Notification":[{"hooks":[{"type":"command","command":"projmux ai ingest claude-hook --custom"}]}]}}`)
	writes := 0
	cmd.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		return os.WriteFile(path, data, mode)
	}
	if _, _, err := cmd.beginManagedIngestProducerFileMigration(); err == nil || !strings.Contains(err.Error(), "unmanaged projmux ingest command") {
		t.Fatalf("migration error = %v, want later-provider conflict", err)
	}
	if writes != 0 {
		t.Fatalf("writes = %d, want zero", writes)
	}
	if got := readCodexTestFile(t, codexPath); got != original {
		t.Fatalf("earlier managed provider changed before later conflict:\n%s", got)
	}
}

func TestManagedIngestProducerMigrationRollsBackAntigravitySecondWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.executable = func() (string, error) { return "/opt/projmux/bin/projmux", nil }
	hooksPath := filepath.Join(home, antigravityHooksRelativePath)
	statusPath := filepath.Join(home, antigravitySettingsRelativePath)
	hooks, _ := encodeAntigravityManagedHook("/old/projmux")
	status, _ := encodeAntigravityManagedStatusLine("/old/projmux")
	originalHooks := "{\n  \"projmux\": " + strings.ReplaceAll(hooks, antigravityCanonicalIngestPath, antigravityLegacyIngestPath) + "\n}\n"
	originalStatus := "{\n  \"statusLine\": " + strings.ReplaceAll(status, antigravityCanonicalIngestPath, antigravityLegacyIngestPath) + "\n}\n"
	writeCodexTestFile(t, hooksPath, originalHooks)
	writeCodexTestFile(t, statusPath, originalStatus)
	writes := 0
	cmd.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			if err := os.WriteFile(path, []byte("partial"), mode); err != nil {
				return err
			}
			return errors.New("injected second write failure")
		}
		return os.WriteFile(path, data, mode)
	}
	if _, _, err := cmd.beginManagedIngestProducerFileMigration(); err == nil || !strings.Contains(err.Error(), "injected second write failure") {
		t.Fatalf("migration error = %v", err)
	}
	if got := readCodexTestFile(t, hooksPath); got != originalHooks {
		t.Fatalf("hooks rollback mismatch:\n%s", got)
	}
	if got := readCodexTestFile(t, statusPath); got != originalStatus {
		t.Fatalf("statusline rollback mismatch:\n%s", got)
	}
}

func TestAIIntegrateAntigravityFreshSecondWriteFailureRemovesCreatedLedger(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	writes := 0
	cmd.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			if err := os.WriteFile(path, []byte("partial"), mode); err != nil {
				return err
			}
			return errors.New("injected fresh second write failure")
		}
		return os.WriteFile(path, data, mode)
	}
	if err := cmd.Run([]string{"integrate", "antigravity"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "fresh second write failure") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created Antigravity ledger remains after rollback: %v", err)
	}
}

func TestV0101ManagedProducerDryRunsShowEveryTargetAndOldToCanonical(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.executable = func() (string, error) { return "/opt/projmux/bin/projmux", nil }
	codexPath := filepath.Join(home, codexConfigRelativePath)
	claudePath := filepath.Join(home, claudeSettingsRelativePath)
	hooksPath := filepath.Join(home, antigravityHooksRelativePath)
	statusPath := filepath.Join(home, antigravitySettingsRelativePath)
	writeCodexTestFile(t, codexPath, strings.ReplaceAll(codexHooksBlock(true), codexHookCommand, legacyCodexHookCommand))
	writeCodexTestFile(t, claudePath, `{"hooks":{"Notification":[{"hooks":[{"type":"command","command":"`+legacyClaudeHookCommand+`"}]}]}}`)
	hooks, _ := encodeAntigravityManagedHook("/opt/projmux/bin/projmux")
	status, _ := encodeAntigravityManagedStatusLine("/opt/projmux/bin/projmux")
	writeCodexTestFile(t, hooksPath, "{\n  \"projmux\": "+strings.ReplaceAll(hooks, antigravityCanonicalIngestPath, antigravityLegacyIngestPath)+"\n}\n")
	writeCodexTestFile(t, statusPath, "{\n  \"statusLine\": "+strings.ReplaceAll(status, antigravityCanonicalIngestPath, antigravityLegacyIngestPath)+"\n}\n")
	writes := 0
	cmd.writeFile = func(string, []byte, os.FileMode) error {
		writes++
		return nil
	}

	for _, test := range []struct {
		provider string
		targets  []string
		old      string
		new      string
	}{
		{provider: "codex", targets: []string{codexPath}, old: legacyCodexHookRoute, new: canonicalCodexHookRoute},
		{provider: "claude", targets: []string{claudePath}, old: legacyClaudeHookRoute, new: canonicalClaudeHookRoute},
		{provider: "antigravity", targets: []string{hooksPath, statusPath}, old: strings.TrimSpace(antigravityLegacyIngestPath), new: strings.TrimSpace(antigravityCanonicalIngestPath)},
	} {
		var stdout bytes.Buffer
		if err := cmd.Run([]string{"integrate", test.provider, "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s dry-run: %v", test.provider, err)
		}
		out := stdout.String()
		for _, target := range test.targets {
			if !strings.Contains(out, target) {
				t.Errorf("%s dry-run missing target %s:\n%s", test.provider, target, out)
			}
		}
		if !strings.Contains(out, test.old) || !strings.Contains(out, test.new) {
			t.Errorf("%s dry-run missing old→new command:\n%s", test.provider, out)
		}
	}

	cmd.readCommand = tmuxBellReadFixture("alert-bell[1] " + legacyTmuxBellHookCommand + "\n")
	var tmuxOut bytes.Buffer
	if err := cmd.Run([]string{"integrate", "tmux-bell", "--dry-run"}, &tmuxOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{tmuxBellHookName, legacyTmuxBellHookCommand, tmuxBellHookCommand} {
		if !strings.Contains(tmuxOut.String(), want) {
			t.Errorf("tmux dry-run missing %q:\n%s", want, tmuxOut.String())
		}
	}
	if writes != 0 {
		t.Fatalf("dry-run writes = %d, want zero", writes)
	}
}

func TestAIIntegrateTmuxBellReadFailureIsZeroMutation(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("injected inventory failure")
	}
	if err := cmd.Run([]string{"integrate", "tmux-bell"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "inventory failure") {
		t.Fatalf("error = %v", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want zero", cmdRecorder(cmd).commands)
	}
}

func TestAIIntegrateTmuxBellNthFailureRollsBackExactOptionsAndUnmanagedHook(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	options := map[string]string{
		"allow-passthrough": "off",
		"monitor-bell":      "off",
		"bell-action":       "none",
	}
	hookCommands := []string{
		"run-shell -b 'echo unmanaged'",
		legacyTmuxBellHookCommand,
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || len(args) == 0 {
			return nil, errors.New("unexpected read")
		}
		switch args[0] {
		case "show-options":
			return []byte(options[args[len(args)-1]] + "\n"), nil
		case "show-hooks":
			var lines []string
			for i, command := range hookCommands {
				lines = append(lines, fmt.Sprintf("alert-bell[%d] %s", i, command))
			}
			return []byte(strings.Join(lines, "\n") + "\n"), nil
		default:
			return nil, errors.New("unexpected read")
		}
	}
	mutation := 0
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		if name != "tmux" {
			return errors.New("unexpected command")
		}
		mutation++
		if mutation == 5 {
			return errors.New("injected fifth tmux command failure")
		}
		if args[0] == "set-option" {
			if args[1] == "-gu" {
				delete(options, args[2])
			} else {
				options[args[2]] = args[3]
			}
			return nil
		}
		if args[0] == "set-hook" && args[1] == "-gu" {
			if args[2] == tmuxBellHookName {
				hookCommands = nil
				return nil
			}
			var index int
			if _, err := fmt.Sscanf(args[2], "alert-bell[%d]", &index); err == nil && index >= 0 && index < len(hookCommands) {
				hookCommands = append(hookCommands[:index], hookCommands[index+1:]...)
			}
			return nil
		}
		if args[0] == "set-hook" && args[1] == "-ag" {
			hookCommands = append(hookCommands, args[3])
			return nil
		}
		return errors.New("unexpected command shape")
	}

	err := cmd.Run([]string{"integrate", "tmux-bell"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "fifth tmux command failure") {
		t.Fatalf("error = %v", err)
	}
	wantOptions := map[string]string{"allow-passthrough": "off", "monitor-bell": "off", "bell-action": "none"}
	if !reflect.DeepEqual(options, wantOptions) {
		t.Fatalf("options after rollback = %#v, want %#v", options, wantOptions)
	}
	wantHooks := []string{"run-shell -b 'echo unmanaged'", legacyTmuxBellHookCommand}
	if !reflect.DeepEqual(hookCommands, wantHooks) {
		t.Fatalf("hooks after rollback = %#v, want %#v", hookCommands, wantHooks)
	}
}

func readCodexTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeCodexTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tmuxBellReadFixture(hooks string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || len(args) == 0 {
			return nil, os.ErrNotExist
		}
		switch args[0] {
		case "show-hooks":
			return []byte(hooks), nil
		case "show-options":
			return nil, nil
		default:
			return nil, os.ErrNotExist
		}
	}
}

func readClaudeSettingsTestFile(t *testing.T, path string) map[string]any {
	t.Helper()
	var settings map[string]any
	if err := json.Unmarshal([]byte(readCodexTestFile(t, path)), &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func claudeSettingsHasManagedCommand(t *testing.T, settings map[string]any, event string) bool {
	t.Helper()
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	entries, ok := hooks[event].([]any)
	if !ok {
		return false
	}
	for _, entryValue := range entries {
		entry, ok := entryValue.(map[string]any)
		if !ok {
			continue
		}
		hookValues, _ := entry["hooks"].([]any)
		for _, hookValue := range hookValues {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				continue
			}
			if hook["type"] == "command" && hook["command"] == claudeHookCommand {
				return true
			}
		}
	}
	return false
}

func assertCodexHookNestedHandler(t *testing.T, config, event string) {
	t.Helper()
	for _, want := range []string{
		"[[hooks." + event + "]]",
		`matcher = "*"`,
		"[[hooks." + event + ".hooks]]",
		`type = "command"`,
		`command = "` + codexHookCommand + `"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q for %s:\n%s", want, event, config)
		}
	}
}

func oldCodexHooksBlock() string {
	lines := []string{
		codexHooksMarkerBegin,
		"[features]",
		"codex_hooks = true",
		"",
	}
	for _, event := range defaultAIHookInstallEvents(aiHookProviderCodex) {
		lines = append(lines,
			"[[hooks."+event+"]]",
			`command = "`+codexHookCommand+`"`,
			"",
		)
	}
	lines = append(lines, codexHooksMarkerEnd)
	return strings.Join(lines, "\n") + "\n"
}
