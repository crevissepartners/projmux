package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	coreusage "github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/web"
)

// webLayerPaths is the config layout of a harness home.
func webLayerPaths(t *testing.T, home string) config.Paths {
	t.Helper()
	paths, err := configPaths(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func writeWebLayerFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hashTree hashes every file under dir except the ones named in skip.
func hashTree(t *testing.T, dir string, skip ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || slices.Contains(skip, entry.Name()) {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		out[path] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func settingsSection(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	section, ok := body[name].(map[string]any)
	if !ok {
		t.Fatalf("settings has no %s object: %v", name, body)
	}
	return section
}

func settingsOrigin(t *testing.T, body map[string]any, key string) string {
	t.Helper()
	origin, _ := settingsSection(t, body, "origins")[key].(string)
	return origin
}

func usageLeafVisible(t *testing.T, body map[string]any, provider, window string) bool {
	t.Helper()
	rows, _ := settingsSection(t, body, "statusbar")["usageProviders"].([]any)
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["id"] != provider {
			continue
		}
		if window == "" {
			return row["visible"].(bool)
		}
		for _, w := range row["windows"].([]any) {
			if leaf := w.(map[string]any); leaf["key"] == window {
				return leaf["visible"].(bool)
			}
		}
	}
	t.Fatalf("no usage leaf %s/%s in %v", provider, window, rows)
	return false
}

// Acceptance 1: a web change of a front setting writes web.toml and nothing
// else: every TUI and central file is byte-identical, no other file appears,
// and no tmux config is regenerated or reloaded.
func TestWebFrontSettingPatchWritesOnlyWebToml(t *testing.T) {
	send, home, tmux := webSettingsHarness(t)
	paths := webLayerPaths(t, home)
	writeWebLayerFile(t, paths.GlobalConfigFile(), "[ai]\nsplit_cwd_from = \"project\"\n")
	for _, path := range []string{paths.StatusbarGitVisibilityFile(), paths.StatusbarClockVisibilityFile(), paths.StatusbarNotificationsHUDVisibilityFile(), paths.StatusbarAgentUsageHUDVisibilityFile()} {
		if err := config.SaveStatusbarVisibilityFile(path, config.StatusbarVisibilityOn); err != nil {
			t.Fatal(err)
		}
	}
	for _, leaf := range []agentUsageVisibilityLeaf{{provider: "claude"}, {provider: "codex", window: "5h"}} {
		path, _ := agentUsageVisibilityPath(paths, leaf)
		if err := config.SaveStatusbarVisibilityFile(path, config.StatusbarVisibilityOn); err != nil {
			t.Fatal(err)
		}
	}
	before := hashTree(t, home)

	changes := [][2]string{
		{"ai.splitCwdFrom", "pane"},
		{"statusbar.notifications", "off"},
		{"statusbar.usage", "off"},
		{"statusbar.project", "off"},
		{"statusbar.working-directory", "off"},
		{"statusbar.git", "off"},
		{"statusbar.clock", "off"},
		{"statusbar.usage.claude", "off"},
		{"statusbar.usage.Codex.5H", "off"},
	}
	for _, change := range changes {
		body := `{"key":"` + change[0] + `","value":"` + change[1] + `"}`
		if code, resp := send("PATCH", body); code != 200 {
			t.Fatalf("PATCH %s = %d %v", body, code, resp)
		}
	}

	after := hashTree(t, home, config.WebSettingsFileName)
	if len(after) != len(before) {
		t.Errorf("files other than web.toml appeared or vanished:\nbefore %v\nafter  %v", before, after)
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("%s changed", path)
		}
	}
	if _, err := os.Stat(filepath.Join(paths.ConfigDir, "tmux.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a front setting regenerated the tmux config: %v", err)
	}
	if len(tmux.calls) != 0 {
		t.Errorf("a front setting called tmux: %v", tmux.calls)
	}

	content, err := os.ReadFile(paths.WebSettingsFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"[ai]\nsplit_cwd_from = \"pane\"\n",
		"[statusbar]\nnotifications = false\nproject = false\nworking_directory = false\ngit = false\nclock = false\n",
		"[statusbar.usage]\nvisible = false\n",
		"[statusbar.usage.claude]\nvisible = false\n",
		"[statusbar.usage.codex]\n5h = false\n",
	} {
		if !strings.Contains(string(content), line) {
			t.Errorf("web.toml lacks %q:\n%s", line, content)
		}
	}
	if info, err := os.Stat(paths.WebSettingsFile()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("web.toml mode = %v %v, want 0600", info, err)
	}

	code, body := send("GET", "")
	if code != 200 {
		t.Fatalf("GET = %d %v", code, body)
	}
	if ai := settingsSection(t, body, "ai"); ai["splitCwdFrom"] != "pane" || ai["splitCwdOrigin"] != "web" {
		t.Errorf("ai = %v", ai)
	}
	for _, change := range changes {
		key := strings.ToLower(change[0])
		if change[0] == "ai.splitCwdFrom" {
			key = change[0]
		}
		if got := settingsOrigin(t, body, key); got != "web" {
			t.Errorf("origin of %s = %q, want web", key, got)
		}
	}
	bar := settingsSection(t, body, "statusbar")
	for _, field := range []string{"notifications", "usage", "project", "workingDirectory", "git", "clock"} {
		if bar[field] != false {
			t.Errorf("statusbar.%s = %v, want the web's off", field, bar[field])
		}
	}
	if usageLeafVisible(t, body, "claude", "") || usageLeafVisible(t, body, "codex", "5h") {
		t.Error("the web's usage leaves are not shown")
	}
	// The TUI still reads its own, untouched values.
	if !loadStatusbarRowOneVisibilitySet(func() (string, error) { return home, nil }, nil).visible(statusbarRowOneGit) {
		t.Error("the TUI git part followed the web")
	}
}

// Acceptance 2: with no web.toml, or without the key in it, the web shows
// the TUI's value, and follows a TUI change until the web sets its own.
func TestWebFrontSettingsShowTheTUIValueUntilTheWebOverrides(t *testing.T) {
	send, home, _ := webSettingsHarness(t)
	paths := webLayerPaths(t, home)
	handler := web.New(func() *webBackend { b, _ := webFixtureBackend(t); return b }(), nil).Handler()

	code, body := send("GET", "")
	if code != 200 {
		t.Fatalf("GET = %d %v", code, body)
	}
	if bar := settingsSection(t, body, "statusbar"); bar["git"] != true || settingsOrigin(t, body, "statusbar.git") != "default" {
		t.Fatalf("no files: git = %v origin %q", bar["git"], settingsOrigin(t, body, "statusbar.git"))
	}
	if usageLeafVisible(t, body, "codex", "5h") || settingsOrigin(t, body, "statusbar.usage.codex.5h") != "default" {
		t.Fatal("codex 5h is not its capability default (off)")
	}
	if ai := settingsSection(t, body, "ai"); ai["splitCwdFrom"] != "project" || ai["splitCwdOrigin"] != "default" {
		t.Fatalf("no files: ai = %v", ai)
	}

	// TUI changes show on the web.
	if err := config.SaveStatusbarVisibilityFile(paths.StatusbarGitVisibilityFile(), config.StatusbarVisibilityOff); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarVisibilityFile(paths.StatusbarClockVisibilityFile(), config.StatusbarVisibilityOff); err != nil {
		t.Fatal(err)
	}
	window, _ := agentUsageVisibilityPath(paths, agentUsageVisibilityLeaf{provider: "codex", window: "5h"})
	if err := config.SaveStatusbarVisibilityFile(window, config.StatusbarVisibilityOn); err != nil {
		t.Fatal(err)
	}
	writeWebLayerFile(t, paths.GlobalConfigFile(), "[ai]\nsplit_cwd_from = \"pane\"\n")
	_, body = send("GET", "")
	if bar := settingsSection(t, body, "statusbar"); bar["git"] != false || settingsOrigin(t, body, "statusbar.git") != "tui" {
		t.Fatalf("TUI off: git = %v origin %q", bar["git"], settingsOrigin(t, body, "statusbar.git"))
	}
	if !usageLeafVisible(t, body, "codex", "5h") || settingsOrigin(t, body, "statusbar.usage.codex.5h") != "tui" {
		t.Fatal("the TUI's codex 5h on is not shown")
	}
	if ai := settingsSection(t, body, "ai"); ai["splitCwdFrom"] != "pane" || ai["splitCwdOrigin"] != "global" {
		t.Fatalf("global pane: ai = %v", ai)
	}

	// The web overrides git only; clock, absent from web.toml, keeps
	// following the TUI.
	if code, resp := send("PATCH", `{"key":"statusbar.git","value":"on"}`); code != 200 {
		t.Fatalf("PATCH = %d %v", code, resp)
	}
	if err := config.SaveStatusbarVisibilityFile(paths.StatusbarGitVisibilityFile(), config.StatusbarVisibilityOff); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarVisibilityFile(paths.StatusbarClockVisibilityFile(), config.StatusbarVisibilityOn); err != nil {
		t.Fatal(err)
	}
	_, body = send("GET", "")
	bar := settingsSection(t, body, "statusbar")
	if bar["git"] != true || settingsOrigin(t, body, "statusbar.git") != "web" {
		t.Errorf("web on: git = %v origin %q", bar["git"], settingsOrigin(t, body, "statusbar.git"))
	}
	if bar["clock"] != true || settingsOrigin(t, body, "statusbar.clock") != "tui" {
		t.Errorf("clock = %v origin %q, want the TUI's on", bar["clock"], settingsOrigin(t, body, "statusbar.clock"))
	}

	// The status bar read is the same overlay.
	code, status := webGet(t, handler, "/api/v1/web/statusbar")
	if code != 200 || status["git"] != true || status["clock"] != true {
		t.Errorf("statusbar = %d %v", code, status)
	}
}

// Acceptance 3: central settings keep saving to their central files and never
// create web.toml.
func TestWebCentralSettingPatchWritesCentralFilesAndNoWebToml(t *testing.T) {
	send, home, _ := webSettingsHarness(t)
	paths := webLayerPaths(t, home)

	for _, change := range []string{
		`{"key":"ai.provider.codex","value":"off"}`,
		`{"key":"locale","value":"ko-KR"}`,
	} {
		if code, body := send("PATCH", change); code != 200 {
			t.Fatalf("PATCH %s = %d %v", change, code, body)
		}
	}
	agents, err := os.ReadFile(paths.AIEnabledAgentsFile())
	if err != nil || strings.Contains(string(agents), "codex") {
		t.Errorf("enabled agents file = %q %v", agents, err)
	}
	global, err := os.ReadFile(paths.GlobalConfigFile())
	if err != nil || !strings.Contains(string(global), "ko-KR") {
		t.Errorf("config.toml = %q %v", global, err)
	}
	if systemstatus.Supported() {
		// The reload finds no app server and answers refused, as before; the
		// file is saved either way.
		code, body := send("PATCH", `{"key":"statusbar.resources","value":"on"}`)
		if code != 200 && errorCode(body) != web.CodeRefused {
			t.Fatalf("PATCH resources = %d %v", code, body)
		}
		if mode, err := os.ReadFile(paths.LiveResourcesFile()); err != nil || strings.TrimSpace(string(mode)) != "on" {
			t.Errorf("live resources file = %q %v", mode, err)
		}
	}
	if _, err := os.Stat(paths.WebSettingsFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a central setting created web.toml: %v", err)
	}
}

// Acceptance 4: a web.toml the web cannot read is an error naming the file
// and the line on every read, and a change never overwrites it.
func TestWebSettingsRefuseAnUnreadableWebTomlWithFileAndLine(t *testing.T) {
	send, home, _ := webSettingsHarness(t)
	paths := webLayerPaths(t, home)
	handler := web.New(func() *webBackend { b, _ := webFixtureBackend(t); return b }(), nil).Handler()
	message := func(body map[string]any) string {
		envelope, _ := body["error"].(map[string]any)
		text, _ := envelope["message"].(string)
		return text
	}

	// Keys this build does not know are skipped: the reads answer, a web
	// change lands, and the file keeps them. kept is empty for a file that is
	// refused.
	for _, tc := range []struct {
		content, want string
		kept          []string
	}{
		{"[statusbar]\ngit = false\ngti = false\n", "", []string{"\ngti = false\n"}},
		{"# usage\n[statusbar.usage.nosuchprovider]\nvisible = false\n", "", []string{"\n[statusbar.usage.nosuchprovider]\nvisible = false\n"}},
		{"[statusbar.usage.codex]\nmonthly = false\n", "", []string{"\n[statusbar.usage.codex]\n", "\nmonthly = false\n"}},
		{"[statusbar]\ngit = \"off\"\n", paths.WebSettingsFile() + `:2: key "statusbar.git" must be true or false`, nil},
	} {
		writeWebLayerFile(t, paths.WebSettingsFile(), tc.content)

		if tc.want == "" {
			if code, body := send("GET", ""); code != 200 {
				t.Errorf("GET settings = %d %v, want 200", code, body)
			}
			if code, body := webGet(t, handler, "/api/v1/web/statusbar"); code != 200 {
				t.Errorf("GET statusbar = %d %v, want 200", code, body)
			}
			if code, body := send("PATCH", `{"key":"statusbar.clock","value":"off"}`); code != 200 {
				t.Errorf("PATCH = %d %v, want 200", code, body)
			}
			content, _ := os.ReadFile(paths.WebSettingsFile())
			for _, line := range tc.kept {
				if !strings.Contains(string(content), line) {
					t.Errorf("PATCH dropped %q:\n%s", line, content)
				}
			}
			if !strings.Contains(string(content), "\nclock = false\n") {
				t.Errorf("PATCH did not land:\n%s", content)
			}
			continue
		}

		code, body := send("GET", "")
		if code != 409 || errorCode(body) != web.CodeRefused || message(body) != tc.want {
			t.Errorf("GET settings = %d %v, want 409 %q", code, body, tc.want)
		}
		if details, _ := body["error"].(map[string]any)["details"].(map[string]any); details["file"] != paths.WebSettingsFile() {
			t.Errorf("details = %v", details)
		}
		code, body = webGet(t, handler, "/api/v1/web/statusbar")
		if code != 409 || message(body) != tc.want {
			t.Errorf("GET statusbar = %d %v", code, body)
		}
		code, body = send("PATCH", `{"key":"statusbar.clock","value":"off"}`)
		if code != 409 || message(body) != tc.want {
			t.Errorf("PATCH = %d %v", code, body)
		}
		if content, _ := os.ReadFile(paths.WebSettingsFile()); string(content) != tc.content {
			t.Errorf("PATCH rewrote the broken web.toml:\n%s", content)
		}
	}
}

// Acceptance 6: every field the web UI reads is still in the response.
func TestWebSettingsResponseKeepsEveryExistingFieldName(t *testing.T) {
	send, _, _ := webSettingsHarness(t)
	code, body := send("GET", "")
	if code != 200 {
		t.Fatalf("GET = %d %v", code, body)
	}
	has := func(where string, object map[string]any, fields ...string) {
		t.Helper()
		for _, field := range fields {
			if _, ok := object[field]; !ok {
				t.Errorf("%s lost field %q: %v", where, field, object)
			}
		}
	}
	has("settings", body, "ai", "statusbar", "locale")
	has("ai", settingsSection(t, body, "ai"), "defaultMode", "modes", "providers", "splitCwdFrom", "splitCwdOrigin")
	bar := settingsSection(t, body, "statusbar")
	has("statusbar", bar, "notifications", "usage", "project", "workingDirectory", "git", "resources", "clock", "resourcesSupported", "usageProviders")
	has("locale", settingsSection(t, body, "locale"), "value", "choices")
	providers, _ := bar["usageProviders"].([]any)
	if len(providers) == 0 {
		t.Fatal("no usage providers")
	}
	provider := providers[0].(map[string]any)
	has("usageProviders[0]", provider, "id", "name", "visible", "windows")
	windows, _ := provider["windows"].([]any)
	if len(windows) == 0 {
		t.Fatal("no usage windows")
	}
	has("usageProviders[0].windows[0]", windows[0].(map[string]any), "key", "label", "visible")
	for _, choice := range settingsSection(t, body, "ai")["modes"].([]any) {
		has("ai.modes[]", choice.(map[string]any), "value", "enabled")
	}
	// The addition: one origin per front setting.
	origins := settingsSection(t, body, "origins")
	has("origins", origins, "ai.splitCwdFrom", "statusbar.notifications", "statusbar.usage", "statusbar.project",
		"statusbar.working-directory", "statusbar.git", "statusbar.clock", "statusbar.usage.claude", "statusbar.usage.claude.5h")
}

// The web split: request, then the Project's config, then web.toml, then the
// global config, then project. A Project's config still beats a web change.
func TestWebSplitCWDResolvesWebTomlBelowTheProjectConfig(t *testing.T) {
	home := t.TempDir()
	homeDir := func() (string, error) { return home, nil }
	env := func(string) string { return "" }
	paths := webLayerPaths(t, home)
	root := t.TempDir()

	writeWebLayerFile(t, paths.GlobalConfigFile(), "[ai]\nsplit_cwd_from = \"project\"\n")
	if err := saveWebSetting(homeDir, env, webSettingSplitCWDFrom, "pane"); err != nil {
		t.Fatal(err)
	}
	got, err := resolveWebSplitCWDSource("", root, homeDir, env)
	if err != nil || got != (splitCWDResolution{Source: splitCWDFromPane, Origin: splitCWDOriginWeb}) {
		t.Fatalf("web.toml over global = %+v %v", got, err)
	}

	writeWebLayerFile(t, filepath.Join(root, ".projmux", "config.toml"), "[ai]\nsplit_cwd_from = \"project\"\n")
	got, err = resolveWebSplitCWDSource("", root, homeDir, env)
	if err != nil || got != (splitCWDResolution{Source: splitCWDFromProject, Origin: splitCWDOriginProject}) {
		t.Fatalf("project config over web.toml = %+v %v", got, err)
	}
	got, err = resolveWebSplitCWDSource("pane", root, homeDir, env)
	if err != nil || got != (splitCWDResolution{Source: splitCWDFromPane, Origin: splitCWDOriginFlag}) {
		t.Fatalf("request over project = %+v %v", got, err)
	}

	// The web split route itself follows web.toml.
	backend, _ := webFixtureBackend(t)
	backend.home, backend.env = homeDir, env
	if source, err := backend.splitCWDFrom(webSnapshot{}, "prj-none", ""); err != nil || source != "pane" {
		t.Fatalf("web split = %q %v", source, err)
	}

	writeWebLayerFile(t, paths.WebSettingsFile(), "[ai]\nsplit_cwd_from = \"home\"\n")
	if _, err := resolveWebSplitCWDSource("", "", homeDir, env); err == nil || !strings.Contains(err.Error(), paths.WebSettingsFile()+":2:") {
		t.Fatalf("broken web.toml err = %v", err)
	}
}

// The TUI split chain -- keybinding, launcher, pane menu -- never reads
// web.toml.
func TestTUISplitCWDResolverIgnoresWebToml(t *testing.T) {
	home := t.TempDir()
	homeDir := func() (string, error) { return home, nil }
	env := func(string) string { return "" }
	paths := webLayerPaths(t, home)
	if err := saveWebSetting(homeDir, env, webSettingSplitCWDFrom, "pane"); err != nil {
		t.Fatal(err)
	}
	if got := resolveUISplitCWDSource("", "", homeDir, env); got != (splitCWDResolution{Source: splitCWDFromProject, Origin: splitCWDOriginDefault}) {
		t.Fatalf("TUI split with only web.toml = %+v", got)
	}
	writeWebLayerFile(t, paths.GlobalConfigFile(), "[ai]\nsplit_cwd_from = \"project\"\n")
	if got := resolveUISplitCWDSource("", "", homeDir, env); got.Origin != splitCWDOriginGlobal || got.Source != splitCWDFromProject {
		t.Fatalf("TUI split = %+v, want the global project", got)
	}
	// A broken web.toml does not reach the TUI either.
	writeWebLayerFile(t, paths.WebSettingsFile(), "garbage\n")
	if got := resolveUISplitCWDSource("", "", homeDir, env); got.Origin != splitCWDOriginGlobal {
		t.Fatalf("TUI split with a broken web.toml = %+v", got)
	}
}

// The web usage HUD follows web.toml; the TUI / `internal status usage` HUD
// selection keeps reading only the TUI visibility files.
func TestWebUsageHUDFollowsWebTomlWhileTheTUIHUDKeepsTheTUIFiles(t *testing.T) {
	send, home, _ := webSettingsHarness(t)
	stateDir := t.TempDir()
	t.Setenv(usagecmd.StateDirEnvVar, stateDir)
	t.Setenv("XDG_STATE_HOME", "")
	now := time.Now().UTC()
	var snaps []coreusage.Snapshot
	for _, capability := range usagecmd.HUDProviderCapabilities() {
		for _, window := range capability.Windows {
			snaps = append(snaps, coreusage.Snapshot{Model: capability.Model, Window: window.Window, Pct: 40, ResetsAt: now.Add(time.Hour), UpdatedAt: now})
		}
	}
	if err := coreusage.NewStore(stateDir).SaveAll(snaps, now); err != nil {
		t.Fatal(err)
	}
	// Make codex 5h visible in the TUI so both providers draw both windows.
	paths := webLayerPaths(t, home)
	window, _ := agentUsageVisibilityPath(paths, agentUsageVisibilityLeaf{provider: "codex", window: "5h"})
	if err := config.SaveStatusbarVisibilityFile(window, config.StatusbarVisibilityOn); err != nil {
		t.Fatal(err)
	}
	cells := func(snaps []coreusage.Snapshot) []string {
		var out []string
		for _, s := range snaps {
			out = append(out, s.Model+"/"+string(s.Window))
		}
		return out
	}
	tuiBefore := cells(usagecmd.New(nil).HUDSnapshots(snaps))
	if !slices.Contains(tuiBefore, "claude/5h") || !slices.Contains(tuiBefore, "codex/weekly") {
		t.Fatalf("TUI HUD before = %v", tuiBefore)
	}

	for _, change := range []string{
		`{"key":"statusbar.usage.claude.5h","value":"off"}`,
		`{"key":"statusbar.usage.codex","value":"off"}`,
	} {
		if code, body := send("PATCH", change); code != 200 {
			t.Fatalf("PATCH %s = %d %v", change, code, body)
		}
	}

	if tuiAfter := cells(usagecmd.New(nil).HUDSnapshots(snaps)); !slices.Equal(tuiAfter, tuiBefore) {
		t.Fatalf("the TUI HUD followed web.toml: %v, was %v", tuiAfter, tuiBefore)
	}
	backend, _ := webFixtureBackend(t)
	code, body := webGet(t, web.New(backend, nil).Handler(), "/api/v1/usage")
	if code != 200 || body["error"] != nil {
		t.Fatalf("GET usage = %d %v", code, body)
	}
	var webCells []string
	for _, raw := range body["hud"].([]any) {
		cell := raw.(map[string]any)
		webCells = append(webCells, cell["model"].(string)+"/"+cell["window"].(string))
	}
	if len(webCells) != 1 || !strings.HasSuffix(webCells[0], "/weekly") || strings.Contains(strings.ToLower(webCells[0]), "codex") {
		t.Fatalf("web HUD = %v, want only claude weekly", webCells)
	}
}
