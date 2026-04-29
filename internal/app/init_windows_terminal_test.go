package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newWTTestAdapter returns a WindowsTerminalAdapter with deterministic
// timestamps and no implicit cmd.exe interop, suitable for unit tests.
func newWTTestAdapter(t *testing.T) *WindowsTerminalAdapter {
	t.Helper()
	a := NewWindowsTerminalAdapter()
	a.now = func() time.Time { return time.Date(2026, 4, 30, 1, 32, 15, 0, time.UTC) }
	a.runCmdExe = func([]string) (string, error) { return "", errors.New("interop disabled in test") }
	return a
}

// envFn returns a getenv-style closure backed by the supplied map.
func envFn(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// presetSidebarOnly builds a settings.json that already contains a single
// projmux-managed entry (User.projmuxSidebar) with the canonical ESC+1 input
// bytes encoded as the JSON escape sequence  — that's how Windows Terminal
// stores them on disk and how json.Unmarshal will decode them back into Go's
// "\x1b1" runtime representation.
func presetSidebarOnly() string {
	// Build the JSON escape `BACKSLASH u 0 0 1 b` byte-by-byte so the source
	// file itself never embeds a literal ESC character.
	jsonEsc := string([]byte{0x5c, 0x75, 0x30, 0x30, 0x31, 0x62})
	body := "{\n" +
		"  \"actions\": [\n" +
		"    { \"command\": { \"action\": \"sendInput\", \"input\": \"" + jsonEsc + "1\" }, \"id\": \"User.projmuxSidebar\" }\n" +
		"  ],\n" +
		"  \"keybindings\": [\n" +
		"    { \"id\": \"User.projmuxSidebar\", \"keys\": \"alt+1\" }\n" +
		"  ]\n" +
		"}\n"
	return body
}

func TestWTAdapterRegisteredAlongsideGhostty(t *testing.T) {
	t.Parallel()

	wt, ok := defaultTerminalRegistry.lookup("windows-terminal")
	if !ok {
		t.Fatalf("default registry missing windows-terminal adapter")
	}
	if wt.Name() != "windows-terminal" {
		t.Fatalf("name = %q, want windows-terminal", wt.Name())
	}
	if _, ok := defaultTerminalRegistry.lookup("ghostty"); !ok {
		t.Fatalf("default registry missing ghostty adapter (regression in PR-B)")
	}
}

func TestWTAdapterDetect(t *testing.T) {
	t.Parallel()

	a := NewWindowsTerminalAdapter()
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "wt session only", env: map[string]string{"WT_SESSION": "abc-123"}, want: true},
		{name: "term program windows terminal", env: map[string]string{"TERM_PROGRAM": "WindowsTerminal"}, want: true},
		{name: "term program case insensitive", env: map[string]string{"TERM_PROGRAM": "windowsterminal"}, want: true},
		{name: "wsl distro alone", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, want: true},
		{name: "wsl interop alone", env: map[string]string{"WSL_INTEROP": "/run/WSL/1_interop"}, want: true},
		{name: "wsl + wt session", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "WT_SESSION": "x"}, want: true},
		{name: "neither", env: map[string]string{"TERM_PROGRAM": "iTerm.app"}, want: false},
		{name: "empty", env: map[string]string{}, want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := a.Detect(envFn(tc.env)); got != tc.want {
				t.Fatalf("Detect(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestWTAdapterDetectNilEnv(t *testing.T) {
	t.Parallel()
	a := NewWindowsTerminalAdapter()
	if a.Detect(nil) {
		t.Fatalf("Detect(nil) = true, want false")
	}
}

func TestIsWSL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, want: true},
		{env: map[string]string{"WSL_INTEROP": "/run/WSL/1_interop"}, want: true},
		{env: map[string]string{"WT_SESSION": "x"}, want: false},
		{env: map[string]string{}, want: false},
	}
	for _, tc := range cases {
		if got := isWSL(envFn(tc.env)); got != tc.want {
			t.Fatalf("isWSL(%v) = %v, want %v", tc.env, got, tc.want)
		}
	}
	if isWSL(nil) {
		t.Fatalf("isWSL(nil) = true")
	}
}

func TestWTConfigPathNativePrefersStorePathWhenItExists(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	storeWant := filepath.Join("C:\\AppData\\Local", "Packages", "Microsoft.WindowsTerminal_8wekyb3d8bbcwe", "LocalState", "settings.json")
	a.stat = func(p string) (os.FileInfo, error) {
		if p == storeWant {
			return fakeFileInfo{name: "settings.json"}, nil
		}
		return nil, os.ErrNotExist
	}
	got, err := a.ConfigPath(envFn(map[string]string{"LOCALAPPDATA": "C:\\AppData\\Local"}))
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if got != storeWant {
		t.Fatalf("ConfigPath = %q, want %q", got, storeWant)
	}
}

func TestWTConfigPathNativeFallsBackToUnpackagedWhenStoreMissing(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	unpackagedWant := filepath.Join("C:\\AppData\\Local", "Microsoft", "Windows Terminal", "settings.json")
	a.stat = func(p string) (os.FileInfo, error) {
		if p == unpackagedWant {
			return fakeFileInfo{name: "settings.json"}, nil
		}
		return nil, os.ErrNotExist
	}
	got, err := a.ConfigPath(envFn(map[string]string{"LOCALAPPDATA": "C:\\AppData\\Local"}))
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if got != unpackagedWant {
		t.Fatalf("ConfigPath = %q, want %q", got, unpackagedWant)
	}
}

func TestWTConfigPathNativeDefaultsToStoreWhenNothingExists(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	a.stat = func(p string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	got, err := a.ConfigPath(envFn(map[string]string{"LOCALAPPDATA": "C:\\Users\\me\\AppData\\Local"}))
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if !strings.Contains(got, "Microsoft.WindowsTerminal_8wekyb3d8bbcwe") {
		t.Fatalf("ConfigPath fallback = %q, want Store path", got)
	}
}

func TestWTConfigPathNativeMissingLocalAppData(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	a.stat = func(p string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	_, err := a.ConfigPath(envFn(map[string]string{}))
	if err == nil {
		t.Fatalf("ConfigPath() with no LOCALAPPDATA/USERPROFILE returned nil error")
	}
}

func TestWTConfigPathNativeFallsBackToUserProfile(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	a.stat = func(p string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	got, err := a.ConfigPath(envFn(map[string]string{"USERPROFILE": "C:\\Users\\me"}))
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	wantPrefix := filepath.Join("C:\\Users\\me", "AppData", "Local")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("ConfigPath = %q, want prefix %q", got, wantPrefix)
	}
}

func TestWTConfigPathWSLUsesCmdExeInterop(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	wantBase := "/mnt/c/Users/foo/AppData/Local"
	a.runCmdExe = func(args []string) (string, error) {
		if len(args) < 3 || args[0] != "cmd.exe" {
			t.Fatalf("unexpected runCmdExe args: %v", args)
		}
		return "C:\\Users\\foo\\AppData\\Local\r\n", nil
	}
	a.stat = func(p string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	got, err := a.ConfigPath(envFn(map[string]string{
		"WSL_DISTRO_NAME": "Ubuntu",
		"WSL_INTEROP":     "/run/WSL/1_interop",
	}))
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if !strings.HasPrefix(got, wantBase) {
		t.Fatalf("ConfigPath = %q, want prefix %q", got, wantBase)
	}
	if !strings.Contains(got, "Microsoft.WindowsTerminal_8wekyb3d8bbcwe") {
		t.Fatalf("ConfigPath = %q, expected default Store path under WSL mount", got)
	}
}

func TestWTConfigPathWSLFallbackOnInteropFailure(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	a.runCmdExe = func([]string) (string, error) { return "", errors.New("cmd.exe not found") }
	a.userHomeDir = func() (string, error) { return "/home/linuxuser", nil }
	a.stat = func(p string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	got, err := a.ConfigPath(envFn(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}))
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	wantBase := "/mnt/c/Users/linuxuser/AppData/Local"
	if !strings.HasPrefix(got, wantBase) {
		t.Fatalf("ConfigPath fallback = %q, want prefix %q", got, wantBase)
	}
}

func TestWTConfigPathWSLFallbackBubblesHomeError(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	a.runCmdExe = func([]string) (string, error) { return "", errors.New("cmd.exe not found") }
	a.userHomeDir = func() (string, error) { return "", errors.New("no home") }
	a.stat = func(p string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	_, err := a.ConfigPath(envFn(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}))
	if err == nil {
		t.Fatalf("ConfigPath WSL with no home/interop returned nil error")
	}
}

func TestWinPathToMnt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "C:\\Users\\foo\\AppData\\Local", want: "/mnt/c/Users/foo/AppData/Local"},
		{in: "D:\\Tools", want: "/mnt/d/Tools"},
		{in: "C:", want: "/mnt/c/"},
		{in: "no-drive", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tc := range cases {
		got, err := winPathToMnt(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("winPathToMnt(%q) err = nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("winPathToMnt(%q) err = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("winPathToMnt(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripJSONCRemovesCommentsAndTrailingCommas(t *testing.T) {
	t.Parallel()

	raw := "{\n" +
		"  // top-level comment\n" +
		"  \"name\": \"wt\", /* inline */\n" +
		"  \"list\": [\n" +
		"    1,\n" +
		"    2, // trailing comment\n" +
		"    3,\n" +
		"  ],\n" +
		"  \"nested\": {\n" +
		"    \"key\": \"value with // not a comment\",\n" +
		"    \"other\": \"string with /* still not comment */\"\n" +
		"  }\n" +
		"}"
	cleaned := stripJSONC(raw)
	if strings.Contains(cleaned, "// top-level") {
		t.Fatalf("stripJSONC left a // comment behind:\n%s", cleaned)
	}
	if strings.Contains(cleaned, "/* inline") {
		t.Fatalf("stripJSONC left a /* */ comment behind:\n%s", cleaned)
	}
	// Strings must still contain their literal // and /* tokens.
	if !strings.Contains(cleaned, "value with // not a comment") {
		t.Fatalf("stripJSONC removed an in-string //:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "string with /* still not comment */") {
		t.Fatalf("stripJSONC removed an in-string /* */:\n%s", cleaned)
	}
	// The result must be valid JSON (trailing commas have been stripped).
	var v any
	if err := json.Unmarshal([]byte(cleaned), &v); err != nil {
		t.Fatalf("stripJSONC output is not valid JSON: %v\n%s", err, cleaned)
	}
}

func TestParseSettingsHandlesJSONCAndMissing(t *testing.T) {
	t.Parallel()

	root, err := parseSettings("", false)
	if err != nil {
		t.Fatalf("parseSettings empty: %v", err)
	}
	if len(root) != 0 {
		t.Fatalf("parseSettings empty = %v, want empty map", root)
	}

	jsonc := "{\n" +
		"  // user comment\n" +
		"  \"actions\": [],\n" +
		"  \"keybindings\": [\n" +
		"    { \"id\": \"User.foo\", \"keys\": \"ctrl+x\" }, // trailing comma below\n" +
		"  ]\n" +
		"}"
	root, err = parseSettings(jsonc, true)
	if err != nil {
		t.Fatalf("parseSettings jsonc: %v", err)
	}
	if _, ok := root["keybindings"].([]any); !ok {
		t.Fatalf("keybindings not parsed as array: %T", root["keybindings"])
	}
}

func TestWTPlanMergeEmptyAddsAllBindings(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	plan, err := a.PlanMerge("", false)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if !plan.HasEffect() {
		t.Fatalf("expected effect on empty config")
	}
	if !plan.CreateNew {
		t.Fatalf("CreateNew = false, want true")
	}
	addCount := 0
	for _, ch := range plan.Changes {
		if ch.Kind == "add" {
			addCount++
		}
	}
	if addCount != len(wtDesiredBindings) {
		t.Fatalf("add count = %d, want %d", addCount, len(wtDesiredBindings))
	}
	// Verify the merged JSON parses and has the expected entries.
	root := mustJSON(t, plan.Updated)
	actions := asObjectArray(root["actions"])
	if len(actions) != len(wtDesiredBindings) {
		t.Fatalf("actions len = %d, want %d", len(actions), len(wtDesiredBindings))
	}
	keybindings := asObjectArray(root["keybindings"])
	if len(keybindings) != len(wtDesiredBindings) {
		t.Fatalf("keybindings len = %d, want %d", len(keybindings), len(wtDesiredBindings))
	}
	// Spot-check one binding round-trips with the expected escape bytes.
	var foundSidebar bool
	for _, action := range actions {
		if action["id"] == "User.projmuxSidebar" {
			cmd := action["command"].(map[string]any)
			if cmd["input"] != "\x1b1" {
				t.Fatalf("Sidebar input = %q, want ESC+1", cmd["input"])
			}
			foundSidebar = true
		}
	}
	if !foundSidebar {
		t.Fatalf("Sidebar action not found in merged settings")
	}
}

func TestWTPlanMergeIdempotent(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	first, err := a.PlanMerge("", false)
	if err != nil {
		t.Fatalf("PlanMerge first: %v", err)
	}
	second, err := a.PlanMerge(first.Updated, true)
	if err != nil {
		t.Fatalf("PlanMerge second: %v", err)
	}
	if second.HasEffect() {
		t.Fatalf("second merge unexpectedly changed config\nbefore:\n%s\nafter:\n%s", first.Updated, second.Updated)
	}
	for _, ch := range second.Changes {
		if ch.Kind != "noop" {
			t.Fatalf("second pass change not noop: %+v", ch)
		}
	}
}

func TestWTPlanMergePartialFillsGaps(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	plan, err := a.PlanMerge(presetSidebarOnly(), true)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if !plan.HasEffect() {
		t.Fatalf("expected merge to add missing entries")
	}
	addCount, noopCount := 0, 0
	for _, ch := range plan.Changes {
		switch ch.Kind {
		case "add":
			addCount++
		case "noop":
			noopCount++
		}
	}
	if addCount != len(wtDesiredBindings)-1 {
		t.Fatalf("add count = %d, want %d", addCount, len(wtDesiredBindings)-1)
	}
	if noopCount != 1 {
		t.Fatalf("noop count = %d, want 1", noopCount)
	}
}

func TestWTPlanMergeKeysConflictSkipsAndWarns(t *testing.T) {
	t.Parallel()

	// User has `alt+1` mapped to a non-projmux action. The merge must not
	// overwrite it.
	preset := "{\n" +
		"  \"actions\": [\n" +
		"    { \"command\": { \"action\": \"newTab\" }, \"id\": \"User.userNewTab\" }\n" +
		"  ],\n" +
		"  \"keybindings\": [\n" +
		"    { \"id\": \"User.userNewTab\", \"keys\": \"alt+1\" }\n" +
		"  ]\n" +
		"}\n"
	a := newWTTestAdapter(t)
	plan, err := a.PlanMerge(preset, true)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}

	var conflict *MergeChange
	for i := range plan.Changes {
		if plan.Changes[i].Trigger == "alt+1" {
			conflict = &plan.Changes[i]
			break
		}
	}
	if conflict == nil {
		t.Fatalf("no alt+1 change in plan")
	}
	if conflict.Kind != "skip-conflict" {
		t.Fatalf("alt+1 kind = %q, want skip-conflict", conflict.Kind)
	}
	if conflict.Existing != "User.userNewTab" {
		t.Fatalf("alt+1 existing = %q, want User.userNewTab", conflict.Existing)
	}

	// Verify Updated still has the user's mapping intact and no projmux
	// keybinding pointing at alt+1 was added.
	root := mustJSON(t, plan.Updated)
	keybindings := asObjectArray(root["keybindings"])
	for _, kb := range keybindings {
		if kb["keys"] == "alt+1" && kb["id"] != "User.userNewTab" {
			t.Fatalf("alt+1 remapped despite conflict: %+v", kb)
		}
	}
}

func TestWTPlanMergeUpdatesDriftedManagedEntry(t *testing.T) {
	t.Parallel()

	// Old projmux entry with stale input bytes — the merge must replace it.
	preset := "{\n" +
		"  \"actions\": [\n" +
		"    { \"command\": { \"action\": \"sendInput\", \"input\": \"STALE\" }, \"id\": \"User.projmuxSidebar\" }\n" +
		"  ],\n" +
		"  \"keybindings\": [\n" +
		"    { \"id\": \"User.projmuxSidebar\", \"keys\": \"alt+1\" }\n" +
		"  ]\n" +
		"}\n"
	a := newWTTestAdapter(t)
	plan, err := a.PlanMerge(preset, true)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	if !plan.HasEffect() {
		t.Fatalf("expected drift to trigger update")
	}
	root := mustJSON(t, plan.Updated)
	for _, action := range asObjectArray(root["actions"]) {
		if action["id"] == "User.projmuxSidebar" {
			cmd := action["command"].(map[string]any)
			if cmd["input"] != "\x1b1" {
				t.Fatalf("Sidebar input = %q, want ESC+1 after update", cmd["input"])
			}
		}
	}
}

func TestWTPlanMergePreservesUnrelatedUserContent(t *testing.T) {
	t.Parallel()

	preset := "{\n" +
		"  \"schemes\": [{ \"name\": \"Solarized\" }],\n" +
		"  \"profiles\": { \"defaults\": { \"fontFace\": \"Cascadia\" } },\n" +
		"  \"actions\": [\n" +
		"    { \"command\": { \"action\": \"newTab\" }, \"id\": \"User.userNewTab\" }\n" +
		"  ],\n" +
		"  \"keybindings\": [\n" +
		"    { \"id\": \"User.userNewTab\", \"keys\": \"ctrl+t\" }\n" +
		"  ]\n" +
		"}\n"
	a := newWTTestAdapter(t)
	plan, err := a.PlanMerge(preset, true)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	root := mustJSON(t, plan.Updated)
	if _, ok := root["schemes"]; !ok {
		t.Fatalf("schemes dropped from merged settings")
	}
	if _, ok := root["profiles"]; !ok {
		t.Fatalf("profiles dropped from merged settings")
	}
	// User's keybinding survives.
	var foundUserKB bool
	for _, kb := range asObjectArray(root["keybindings"]) {
		if kb["id"] == "User.userNewTab" && kb["keys"] == "ctrl+t" {
			foundUserKB = true
		}
	}
	if !foundUserKB {
		t.Fatalf("user's ctrl+t -> User.userNewTab keybinding lost")
	}
}

func TestWTApplyMergeWritesBackupAndSettings(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "settings.json")
	original := "{ \"actions\": [], \"keybindings\": [] }\n"
	if err := os.WriteFile(cfg, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	a := newWTTestAdapter(t)
	plan, err := a.PlanMerge(original, true)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	plan.ConfigPath = cfg
	if err := a.ApplyMerge(plan); err != nil {
		t.Fatalf("ApplyMerge: %v", err)
	}

	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read written: %v", err)
	}
	if string(got) != plan.Updated {
		t.Fatalf("written settings != plan.Updated")
	}

	backup := cfg + ".bak.20260430-013215"
	bak, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(bak) != original {
		t.Fatalf("backup contents = %q, want %q", bak, original)
	}
}

func TestWTApplyMergeCreatesNewWithoutBackup(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "wt", "settings.json")
	a := newWTTestAdapter(t)
	plan, err := a.PlanMerge("", false)
	if err != nil {
		t.Fatalf("PlanMerge: %v", err)
	}
	plan.ConfigPath = cfg
	if err := a.ApplyMerge(plan); err != nil {
		t.Fatalf("ApplyMerge: %v", err)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("settings missing: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(cfg))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak.") {
			t.Fatalf("unexpected backup created: %s", e.Name())
		}
	}
}

func TestWTApplyMergeNoEffectIsNoop(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "settings.json")
	a := newWTTestAdapter(t)
	plan := MergePlan{ConfigPath: cfg, Original: "x", Updated: "x"}
	if err := a.ApplyMerge(plan); err != nil {
		t.Fatalf("ApplyMerge no-op: %v", err)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("expected no file written, stat err = %v", err)
	}
}

func TestWTApplyMergeRequiresConfigPath(t *testing.T) {
	t.Parallel()

	a := newWTTestAdapter(t)
	plan := MergePlan{Original: "a", Updated: "b"}
	err := a.ApplyMerge(plan)
	if err == nil || !strings.Contains(err.Error(), "ConfigPath") {
		t.Fatalf("ApplyMerge without ConfigPath: err = %v", err)
	}
}

// mustJSON parses s as JSON and returns the root object, failing the test on
// parse errors. Used to sanity-check the merge output.
func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("merge output is not valid JSON: %v\n%s", err, s)
	}
	root, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("merge output is not a JSON object: %T", v)
	}
	return root
}

// fakeFileInfo is a stub os.FileInfo implementation for stat hooks in tests.
type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
