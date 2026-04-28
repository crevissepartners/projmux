package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPathsWorkdirsFile(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: "/tmp/config/projmux"}
	if got, want := paths.WorkdirsFile(), filepath.Join(paths.ConfigDir, WorkdirsFileName); got != want {
		t.Fatalf("WorkdirsFile() = %q, want %q", got, want)
	}
}

func TestWorkdirsFile(t *testing.T) {
	t.Parallel()

	if got := WorkdirsFile(""); got != "" {
		t.Fatalf("WorkdirsFile(\"\") = %q, want empty", got)
	}
	want := filepath.Join("/home/tester", ".config", AppName, WorkdirsFileName)
	if got := WorkdirsFile("/home/tester"); got != want {
		t.Fatalf("WorkdirsFile() = %q, want %q", got, want)
	}
}

func TestLoadWorkdirsMissingFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	got, err := LoadWorkdirs(home)
	if err != nil {
		t.Fatalf("LoadWorkdirs() error = %v", err)
	}
	if got != nil {
		t.Fatalf("LoadWorkdirs() = %#v, want nil", got)
	}
}

func TestSaveAndLoadWorkdirsRoundtrip(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	want := []string{"/work/repos", "/srv/projects"}
	if err := SaveWorkdirs(home, want); err != nil {
		t.Fatalf("SaveWorkdirs() error = %v", err)
	}

	got, err := LoadWorkdirs(home)
	if err != nil {
		t.Fatalf("LoadWorkdirs() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadWorkdirs() = %#v, want %#v", got, want)
	}
}

func TestSaveWorkdirsDeduplicatesAndTrims(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	input := []string{"  /work/a ", "/work/b", "/work/a", "", "/work/b"}
	if err := SaveWorkdirs(home, input); err != nil {
		t.Fatalf("SaveWorkdirs() error = %v", err)
	}

	got, err := LoadWorkdirs(home)
	if err != nil {
		t.Fatalf("LoadWorkdirs() error = %v", err)
	}
	want := []string{"/work/a", "/work/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadWorkdirs() = %#v, want %#v", got, want)
	}
}

func TestLoadWorkdirsSkipsCommentsAndBlanks(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, ".config", AppName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	contents := "# leading comment\n\n  /work/a  \n#another\n/work/b\n/work/a\n"
	if err := os.WriteFile(filepath.Join(dir, WorkdirsFileName), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadWorkdirs(home)
	if err != nil {
		t.Fatalf("LoadWorkdirs() error = %v", err)
	}
	want := []string{"/work/a", "/work/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadWorkdirs() = %#v, want %#v", got, want)
	}
}

func TestSaveWorkdirsEmptyValueRemovesFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := SaveWorkdirs(home, []string{"/work/a"}); err != nil {
		t.Fatalf("SaveWorkdirs() initial error = %v", err)
	}

	if err := SaveWorkdirs(home, nil); err != nil {
		t.Fatalf("SaveWorkdirs() empty error = %v", err)
	}

	if _, err := os.Stat(WorkdirsFile(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want ErrNotExist", err)
	}
}

func TestSaveWorkdirsRequiresHomeDir(t *testing.T) {
	t.Parallel()

	if err := SaveWorkdirs("", []string{"/work/a"}); !errors.Is(err, ErrHomeDirRequired) {
		t.Fatalf("SaveWorkdirs() error = %v, want %v", err, ErrHomeDirRequired)
	}
}

func TestAddWorkdirAppendsNew(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	added, err := AddWorkdir(home, "/work/a")
	if err != nil {
		t.Fatalf("AddWorkdir() error = %v", err)
	}
	if !added {
		t.Fatalf("AddWorkdir() added = false, want true")
	}

	got, err := LoadWorkdirs(home)
	if err != nil {
		t.Fatalf("LoadWorkdirs() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"/work/a"}) {
		t.Fatalf("LoadWorkdirs() = %#v, want [/work/a]", got)
	}
}

func TestAddWorkdirExistingIsNoOp(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if _, err := AddWorkdir(home, "/work/a"); err != nil {
		t.Fatalf("AddWorkdir() error = %v", err)
	}

	added, err := AddWorkdir(home, "/work/a")
	if err != nil {
		t.Fatalf("AddWorkdir() error = %v", err)
	}
	if added {
		t.Fatalf("AddWorkdir() added = true, want false for duplicate")
	}

	got, err := LoadWorkdirs(home)
	if err != nil {
		t.Fatalf("LoadWorkdirs() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"/work/a"}) {
		t.Fatalf("LoadWorkdirs() = %#v, want [/work/a]", got)
	}
}

func TestRemoveWorkdirRemovesExisting(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := SaveWorkdirs(home, []string{"/work/a", "/work/b"}); err != nil {
		t.Fatalf("SaveWorkdirs() error = %v", err)
	}

	removed, err := RemoveWorkdir(home, "/work/a")
	if err != nil {
		t.Fatalf("RemoveWorkdir() error = %v", err)
	}
	if !removed {
		t.Fatalf("RemoveWorkdir() removed = false, want true")
	}

	got, err := LoadWorkdirs(home)
	if err != nil {
		t.Fatalf("LoadWorkdirs() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"/work/b"}) {
		t.Fatalf("LoadWorkdirs() = %#v, want [/work/b]", got)
	}
}

func TestRemoveWorkdirMissingIsNoOp(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := SaveWorkdirs(home, []string{"/work/a"}); err != nil {
		t.Fatalf("SaveWorkdirs() error = %v", err)
	}

	removed, err := RemoveWorkdir(home, "/work/missing")
	if err != nil {
		t.Fatalf("RemoveWorkdir() error = %v", err)
	}
	if removed {
		t.Fatalf("RemoveWorkdir() removed = true, want false for missing entry")
	}

	got, err := LoadWorkdirs(home)
	if err != nil {
		t.Fatalf("LoadWorkdirs() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"/work/a"}) {
		t.Fatalf("LoadWorkdirs() = %#v, want [/work/a]", got)
	}
}

func TestRemoveWorkdirEmptiesFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := SaveWorkdirs(home, []string{"/work/a"}); err != nil {
		t.Fatalf("SaveWorkdirs() error = %v", err)
	}

	if _, err := RemoveWorkdir(home, "/work/a"); err != nil {
		t.Fatalf("RemoveWorkdir() error = %v", err)
	}

	if _, err := os.Stat(WorkdirsFile(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want ErrNotExist after last entry removed", err)
	}
}
