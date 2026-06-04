package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPaths(t *testing.T) {
	t.Parallel()

	paths := DefaultPaths("/tmp/config-home", "/tmp/state-home")
	if got, want := paths.ConfigDir, filepath.Join("/tmp/config-home", AppName); got != want {
		t.Fatalf("ConfigDir = %q, want %q", got, want)
	}
	if got, want := paths.StateDir, filepath.Join("/tmp/state-home", AppName); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
}

func TestPathsPinFile(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.PinFile(), filepath.Join(paths.ConfigDir, PinsFileName); got != want {
		t.Fatalf("PinFile() = %q, want %q", got, want)
	}
}

func TestPathsTagFile(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.TagFile(), filepath.Join(paths.ConfigDir, TagsFileName); got != want {
		t.Fatalf("TagFile() = %q, want %q", got, want)
	}
}

func TestPathsPreviewStateFile(t *testing.T) {
	t.Parallel()

	paths := Paths{StateDir: "/tmp/state/projmux"}
	if got, want := paths.PreviewStateFile(), filepath.Join(paths.StateDir, PreviewStateFileName); got != want {
		t.Fatalf("PreviewStateFile() = %q, want %q", got, want)
	}
}

func TestHomesPathsUsesExplicitXDGHomes(t *testing.T) {
	t.Parallel()

	paths, err := Homes{
		HomeDir:    "/home/tester",
		ConfigHome: "/tmp/config-home",
		StateHome:  "/tmp/state-home",
	}.Paths()
	if err != nil {
		t.Fatalf("Paths() error = %v", err)
	}

	if got, want := paths.ConfigDir, filepath.Join("/tmp/config-home", AppName); got != want {
		t.Fatalf("ConfigDir = %q, want %q", got, want)
	}
	if got, want := paths.StateDir, filepath.Join("/tmp/state-home", AppName); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
}

func TestHomesPathsFallsBackToHomeDir(t *testing.T) {
	t.Parallel()

	paths, err := Homes{HomeDir: "/home/tester"}.Paths()
	if err != nil {
		t.Fatalf("Paths() error = %v", err)
	}

	if got, want := paths.ConfigDir, filepath.Join("/home/tester", ".config", AppName); got != want {
		t.Fatalf("ConfigDir = %q, want %q", got, want)
	}
	if got, want := paths.StateDir, filepath.Join("/home/tester", ".local", "state", AppName); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
}

func TestHomesPathsRequiresHomeDirWhenFallbackNeeded(t *testing.T) {
	t.Parallel()

	_, err := Homes{
		ConfigHome: "/tmp/config-home",
	}.Paths()
	if !errors.Is(err, ErrHomeDirRequired) {
		t.Fatalf("Paths() error = %v, want %v", err, ErrHomeDirRequired)
	}
}

func TestPathsProjdirFile(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.ProjdirFile(), filepath.Join(paths.ConfigDir, ProjdirFileName); got != want {
		t.Fatalf("ProjdirFile() = %q, want %q", got, want)
	}
}

func TestPathsPostCreateHookPath(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	want := filepath.Join(paths.ConfigDir, HooksDirName, PostCreateHookFileName)
	if got := paths.PostCreateHookPath(); got != want {
		t.Fatalf("PostCreateHookPath() = %q, want %q", got, want)
	}
	if want != filepath.Join("/tmp/config/projmux", "hooks", "post-create") {
		t.Fatalf("PostCreateHookPath layout drifted: want %q", want)
	}
}

func TestPathsStatusbarDecorationFile(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.StatusbarDecorationFile(), filepath.Join(paths.ConfigDir, StatusbarDecorationFileName); got != want {
		t.Fatalf("StatusbarDecorationFile() = %q, want %q", got, want)
	}
	if got, want := paths.AIBadgeStyleFile(), filepath.Join(paths.ConfigDir, AIBadgeStyleFileName); got != want {
		t.Fatalf("AIBadgeStyleFile() = %q, want %q", got, want)
	}
}

func TestPathsPickerBackendFile(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.PickerBackendFile(), filepath.Join(paths.ConfigDir, PickerBackendFileName); got != want {
		t.Fatalf("PickerBackendFile() = %q, want %q", got, want)
	}
}

func TestPathsSessionStateFiles(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux", StateDir: "/tmp/state/projmux"}
	if got, want := paths.SessionStateAutosaveFile(), filepath.Join(paths.ConfigDir, SessionStateAutosaveFileName); got != want {
		t.Fatalf("SessionStateAutosaveFile() = %q, want %q", got, want)
	}
	if got, want := paths.SessionStateAutosaveIntervalFile(), filepath.Join(paths.ConfigDir, SessionStateAutosaveIntervalFileName); got != want {
		t.Fatalf("SessionStateAutosaveIntervalFile() = %q, want %q", got, want)
	}
	if got, want := paths.SessionStateDir(), filepath.Join(paths.StateDir, "sessions"); got != want {
		t.Fatalf("SessionStateDir() = %q, want %q", got, want)
	}
}

func TestPathsNotificationFiles(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.AINotifyDedupeSecondsFile(), filepath.Join(paths.ConfigDir, AINotifyDedupeSecondsFileName); got != want {
		t.Fatalf("AINotifyDedupeSecondsFile() = %q, want %q", got, want)
	}
	if got, want := paths.AIHookActionsFile(), filepath.Join(paths.ConfigDir, AIHookActionsFileName); got != want {
		t.Fatalf("AIHookActionsFile() = %q, want %q", got, want)
	}
	if got, want := paths.DesktopNotifyModeFile(), filepath.Join(paths.ConfigDir, DesktopNotifyModeFileName); got != want {
		t.Fatalf("DesktopNotifyModeFile() = %q, want %q", got, want)
	}
}

func TestPathsAIEnabledAgentsFile(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.AIEnabledAgentsFile(), filepath.Join(paths.ConfigDir, AIEnabledAgentsFileName); got != want {
		t.Fatalf("AIEnabledAgentsFile() = %q, want %q", got, want)
	}
}

func TestAIEnabledAgentsMissingDefaultsToKnownProviders(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", AIEnabledAgentsFileName)
	got, err := LoadAIEnabledAgentsFile(path)
	if err != nil {
		t.Fatalf("LoadAIEnabledAgentsFile(missing) error = %v", err)
	}
	assertAIEnabledAgents(t, got, []AIAgentProvider{AIAgentClaude, AIAgentCodex, AIAgentAntigravity})
}

func TestAIEnabledAgentsRoundtrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", AIEnabledAgentsFileName)
	if err := SaveAIEnabledAgentsFile(path, []AIAgentProvider{AIAgentCodex}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	got, err := LoadAIEnabledAgentsFile(path)
	if err != nil {
		t.Fatalf("LoadAIEnabledAgentsFile() error = %v", err)
	}
	assertAIEnabledAgents(t, got, []AIAgentProvider{AIAgentCodex})
}

func TestAIEnabledAgentsIgnoresUnknownProviderNames(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), AIEnabledAgentsFileName)
	if err := os.WriteFile(path, []byte("unknownai,codex\nshell\nclaude\nantigravity\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAIEnabledAgentsFile(path)
	if err != nil {
		t.Fatalf("LoadAIEnabledAgentsFile() error = %v", err)
	}
	assertAIEnabledAgents(t, got, []AIAgentProvider{AIAgentCodex, AIAgentClaude, AIAgentAntigravity})
}

func TestAIEnabledAgentsCanPersistEmptySet(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", AIEnabledAgentsFileName)
	if err := SaveAIEnabledAgentsFile(path, nil); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile(nil) error = %v", err)
	}
	got, err := LoadAIEnabledAgentsFile(path)
	if err != nil {
		t.Fatalf("LoadAIEnabledAgentsFile() error = %v", err)
	}
	assertAIEnabledAgents(t, got, nil)
}

func TestDesktopNotifyModeRoundtrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", DesktopNotifyModeFileName)
	if got, err := LoadDesktopNotifyModeFile(path); err != nil || got != DefaultDesktopNotifyMode {
		t.Fatalf("LoadDesktopNotifyModeFile(missing) = %q, %v; want %q, nil", got, err, DefaultDesktopNotifyMode)
	}

	if err := SaveDesktopNotifyModeFile(path, DesktopNotifyModeOff); err != nil {
		t.Fatalf("SaveDesktopNotifyModeFile() error = %v", err)
	}
	got, err := LoadDesktopNotifyModeFile(path)
	if err != nil {
		t.Fatalf("LoadDesktopNotifyModeFile() error = %v", err)
	}
	if got != DesktopNotifyModeOff {
		t.Fatalf("LoadDesktopNotifyModeFile() = %q, want %q", got, DesktopNotifyModeOff)
	}
}

func TestDesktopNotifyModeNormalizesInvalidValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), DesktopNotifyModeFileName)
	if err := os.WriteFile(path, []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDesktopNotifyModeFile(path)
	if err != nil {
		t.Fatalf("LoadDesktopNotifyModeFile() error = %v", err)
	}
	if got != DefaultDesktopNotifyMode {
		t.Fatalf("LoadDesktopNotifyModeFile() = %q, want %q", got, DefaultDesktopNotifyMode)
	}

	if err := SaveDesktopNotifyModeFile(path, DesktopNotifyMode("none")); err != nil {
		t.Fatalf("SaveDesktopNotifyModeFile() error = %v", err)
	}
	got, err = LoadDesktopNotifyModeFile(path)
	if err != nil {
		t.Fatalf("LoadDesktopNotifyModeFile() error = %v", err)
	}
	if got != DesktopNotifyModeOff {
		t.Fatalf("LoadDesktopNotifyModeFile() after save = %q, want %q", got, DesktopNotifyModeOff)
	}
}

func TestAIHookActionsFileRoundtrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", AIHookActionsFileName)
	missing, err := LoadAIHookActionsFile(path)
	if err != nil {
		t.Fatalf("LoadAIHookActionsFile(missing) error = %v", err)
	}
	if len(missing.Providers) != 0 {
		t.Fatalf("missing Providers = %#v, want empty", missing.Providers)
	}

	want := AIHookActionsFile{
		Version: 1,
		Providers: map[string]AIHookProviderActions{
			"codex": {Events: map[string]string{"Stop": "quiet"}},
			"future-agent": {Events: map[string]string{
				"FutureEvent": "state",
			}},
		},
	}
	if err := SaveAIHookActionsFile(path, want); err != nil {
		t.Fatalf("SaveAIHookActionsFile() error = %v", err)
	}
	got, err := LoadAIHookActionsFile(path)
	if err != nil {
		t.Fatalf("LoadAIHookActionsFile() error = %v", err)
	}
	if got.Providers["codex"].Events["Stop"] != "quiet" || got.Providers["future-agent"].Events["FutureEvent"] != "state" {
		t.Fatalf("AI hook actions = %#v", got)
	}
}

func TestPathsProjectHooksFile(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.ProjectHooksFile(), filepath.Join(paths.ConfigDir, ProjectHooksFileName); got != want {
		t.Fatalf("ProjectHooksFile() = %q, want %q", got, want)
	}
}

func TestPickerBackendRoundtrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", PickerBackendFileName)
	if got, err := LoadPickerBackendFile(path); err != nil || got != DefaultPickerBackend {
		t.Fatalf("LoadPickerBackendFile(missing) = %q, %v; want %q, nil", got, err, DefaultPickerBackend)
	}

	if err := SavePickerBackendFile(path, PickerBackendNative); err != nil {
		t.Fatalf("SavePickerBackendFile() error = %v", err)
	}
	got, err := LoadPickerBackendFile(path)
	if err != nil {
		t.Fatalf("LoadPickerBackendFile() error = %v", err)
	}
	if got != PickerBackendNative {
		t.Fatalf("LoadPickerBackendFile() = %q, want %q", got, PickerBackendNative)
	}
}

func TestPickerBackendNormalizesInvalidValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), PickerBackendFileName)
	if err := os.WriteFile(path, []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPickerBackendFile(path)
	if err != nil {
		t.Fatalf("LoadPickerBackendFile() error = %v", err)
	}
	if got != DefaultPickerBackend {
		t.Fatalf("LoadPickerBackendFile() = %q, want %q", got, DefaultPickerBackend)
	}
}

func TestProjectHooksRoundtrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", ProjectHooksFileName)
	if got, err := LoadProjectHooksFile(path); err != nil || got != ProjectHooksOn {
		t.Fatalf("LoadProjectHooksFile(missing) = %q, %v; want %q, nil", got, err, ProjectHooksOn)
	}

	if err := SaveProjectHooksFile(path, ProjectHooksOff); err != nil {
		t.Fatalf("SaveProjectHooksFile() error = %v", err)
	}
	got, err := LoadProjectHooksFile(path)
	if err != nil {
		t.Fatalf("LoadProjectHooksFile() error = %v", err)
	}
	if got != ProjectHooksOff {
		t.Fatalf("LoadProjectHooksFile() = %q, want %q", got, ProjectHooksOff)
	}
}

func TestProjectHooksNormalizesInvalidValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ProjectHooksFileName)
	if err := os.WriteFile(path, []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProjectHooksFile(path)
	if err != nil {
		t.Fatalf("LoadProjectHooksFile() error = %v", err)
	}
	if got != ProjectHooksOn {
		t.Fatalf("LoadProjectHooksFile() = %q, want %q", got, ProjectHooksOn)
	}
}

func assertAIEnabledAgents(t *testing.T, got, want []AIAgentProvider) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("AI enabled agents = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AI enabled agents = %#v, want %#v", got, want)
		}
	}
}

func TestSessionStateToggleRoundtrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", SessionStateAutosaveFileName)
	if got, err := LoadSessionStateToggleFile(path); err != nil || got != SessionStateToggleOn {
		t.Fatalf("LoadSessionStateToggleFile(missing) = %q, %v; want %q, nil", got, err, SessionStateToggleOn)
	}

	if err := SaveSessionStateToggleFile(path, SessionStateToggleOff); err != nil {
		t.Fatalf("SaveSessionStateToggleFile() error = %v", err)
	}
	got, err := LoadSessionStateToggleFile(path)
	if err != nil {
		t.Fatalf("LoadSessionStateToggleFile() error = %v", err)
	}
	if got != SessionStateToggleOff {
		t.Fatalf("LoadSessionStateToggleFile() = %q, want %q", got, SessionStateToggleOff)
	}
}

func TestSessionStateToggleNormalizesBooleanValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), SessionStateAutosaveFileName)
	if err := os.WriteFile(path, []byte("false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionStateToggleFile(path)
	if err != nil {
		t.Fatalf("LoadSessionStateToggleFile() error = %v", err)
	}
	if got != SessionStateToggleOff {
		t.Fatalf("LoadSessionStateToggleFile() = %q, want %q", got, SessionStateToggleOff)
	}
}

func TestStatusbarDecorationRoundtrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", StatusbarDecorationFileName)
	if got, err := LoadStatusbarDecorationFile(path); err != nil || got != StatusbarDecorationOff {
		t.Fatalf("LoadStatusbarDecorationFile(missing) = %q, %v; want %q, nil", got, err, StatusbarDecorationOff)
	}

	if err := SaveStatusbarDecorationFile(path, StatusbarDecorationEmoji); err != nil {
		t.Fatalf("SaveStatusbarDecorationFile() error = %v", err)
	}
	got, err := LoadStatusbarDecorationFile(path)
	if err != nil {
		t.Fatalf("LoadStatusbarDecorationFile() error = %v", err)
	}
	if got != StatusbarDecorationEmoji {
		t.Fatalf("LoadStatusbarDecorationFile() = %q, want %q", got, StatusbarDecorationEmoji)
	}
}

func TestStatusbarDecorationNormalizesInvalidValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), StatusbarDecorationFileName)
	if err := os.WriteFile(path, []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStatusbarDecorationFile(path)
	if err != nil {
		t.Fatalf("LoadStatusbarDecorationFile() error = %v", err)
	}
	if got != StatusbarDecorationOff {
		t.Fatalf("LoadStatusbarDecorationFile() = %q, want %q", got, StatusbarDecorationOff)
	}

	if err := SaveStatusbarDecorationFile(path, StatusbarDecoration("also-broken")); err != nil {
		t.Fatalf("SaveStatusbarDecorationFile() error = %v", err)
	}
	got, err = LoadStatusbarDecorationFile(path)
	if err != nil {
		t.Fatalf("LoadStatusbarDecorationFile() error = %v", err)
	}
	if got != StatusbarDecorationOff {
		t.Fatalf("LoadStatusbarDecorationFile() after save = %q, want %q", got, StatusbarDecorationOff)
	}
}

func TestAIBadgeStyleRoundtrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config", AIBadgeStyleFileName)
	if got, err := LoadAIBadgeStyleFile(path); err != nil || got != AIBadgeStyleDot {
		t.Fatalf("LoadAIBadgeStyleFile(missing) = %q, %v; want %q, nil", got, err, AIBadgeStyleDot)
	}

	if err := SaveAIBadgeStyleFile(path, AIBadgeStyleEmoji); err != nil {
		t.Fatalf("SaveAIBadgeStyleFile() error = %v", err)
	}
	got, err := LoadAIBadgeStyleFile(path)
	if err != nil {
		t.Fatalf("LoadAIBadgeStyleFile() error = %v", err)
	}
	if got != AIBadgeStyleEmoji {
		t.Fatalf("LoadAIBadgeStyleFile() = %q, want %q", got, AIBadgeStyleEmoji)
	}
}

func TestAIBadgeStyleNormalizesInvalidValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), AIBadgeStyleFileName)
	if err := os.WriteFile(path, []byte("minimal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAIBadgeStyleFile(path)
	if err != nil {
		t.Fatalf("LoadAIBadgeStyleFile() error = %v", err)
	}
	if got != AIBadgeStyleOff {
		t.Fatalf("LoadAIBadgeStyleFile(minimal) = %q, want %q", got, AIBadgeStyleOff)
	}

	if err := SaveAIBadgeStyleFile(path, AIBadgeStyle("also-broken")); err != nil {
		t.Fatalf("SaveAIBadgeStyleFile() error = %v", err)
	}
	got, err = LoadAIBadgeStyleFile(path)
	if err != nil {
		t.Fatalf("LoadAIBadgeStyleFile() error = %v", err)
	}
	if got != AIBadgeStyleDot {
		t.Fatalf("LoadAIBadgeStyleFile() after save = %q, want %q", got, AIBadgeStyleDot)
	}
}

func TestProjdirFile(t *testing.T) {
	t.Parallel()

	if got := ProjdirFile(""); got != "" {
		t.Fatalf("ProjdirFile(\"\") = %q, want empty", got)
	}
	want := filepath.Join("/home/tester", ".config", AppName, ProjdirFileName)
	if got := ProjdirFile("/home/tester"); got != want {
		t.Fatalf("ProjdirFile() = %q, want %q", got, want)
	}
}

func TestLoadProjdirMissingFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	got, err := LoadProjdir(home)
	if err != nil {
		t.Fatalf("LoadProjdir() error = %v", err)
	}
	if got != "" {
		t.Fatalf("LoadProjdir() = %q, want empty", got)
	}
}

func TestSaveAndLoadProjdirRoundtrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := SaveProjdir(home, "/work/repos"); err != nil {
		t.Fatalf("SaveProjdir() error = %v", err)
	}

	got, err := LoadProjdir(home)
	if err != nil {
		t.Fatalf("LoadProjdir() error = %v", err)
	}
	if got != "/work/repos" {
		t.Fatalf("LoadProjdir() = %q, want %q", got, "/work/repos")
	}
}

func TestSaveProjdirCreatesParentDir(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := SaveProjdir(home, "/srv/projects"); err != nil {
		t.Fatalf("SaveProjdir() error = %v", err)
	}

	dir := filepath.Join(home, ".config", AppName)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", dir)
	}
}

func TestSaveProjdirEmptyValueRemovesFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := SaveProjdir(home, "/initial"); err != nil {
		t.Fatalf("SaveProjdir() initial error = %v", err)
	}

	if err := SaveProjdir(home, ""); err != nil {
		t.Fatalf("SaveProjdir() empty error = %v", err)
	}

	if _, err := os.Stat(ProjdirFile(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want ErrNotExist", err)
	}
}

func TestSaveProjdirEmptyMissingFileNoOp(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := SaveProjdir(home, ""); err != nil {
		t.Fatalf("SaveProjdir() empty error = %v", err)
	}

	if _, err := os.Stat(ProjdirFile(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want ErrNotExist", err)
	}
}

func TestLoadProjdirTrimsAndUsesFirstLine(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, ".config", AppName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	contents := "  /first/line  \n/second/line\n"
	if err := os.WriteFile(filepath.Join(dir, ProjdirFileName), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadProjdir(home)
	if err != nil {
		t.Fatalf("LoadProjdir() error = %v", err)
	}
	if got != "/first/line" {
		t.Fatalf("LoadProjdir() = %q, want %q", got, "/first/line")
	}
}

func TestSaveProjdirRequiresHomeDir(t *testing.T) {
	t.Parallel()

	if err := SaveProjdir("", "/anything"); !errors.Is(err, ErrHomeDirRequired) {
		t.Fatalf("SaveProjdir() error = %v, want %v", err, ErrHomeDirRequired)
	}
}

func TestDefaultPathsFromEnv(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/config-home")
	t.Setenv("XDG_STATE_HOME", "/tmp/state-home")

	paths, err := DefaultPathsFromEnv()
	if err != nil {
		t.Fatalf("DefaultPathsFromEnv() error = %v", err)
	}

	if got, want := paths.ConfigDir, filepath.Join("/tmp/config-home", AppName); got != want {
		t.Fatalf("ConfigDir = %q, want %q", got, want)
	}
	if got, want := paths.StateDir, filepath.Join("/tmp/state-home", AppName); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
}
