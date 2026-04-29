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
