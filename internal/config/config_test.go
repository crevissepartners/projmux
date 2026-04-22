package config

import (
	"errors"
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
