package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLiveResourcesPathsAndRoundTrip(t *testing.T) {
	t.Parallel()

	paths := DefaultPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "state"))
	if got, want := paths.LiveResourcesFile(), filepath.Join(paths.ConfigDir, LiveResourcesFileName); got != want {
		t.Fatalf("LiveResourcesFile() = %q, want %q", got, want)
	}
	if got, want := paths.LiveResourcesSampleFile(), filepath.Join(paths.StateDir, LiveResourcesSampleFileName); got != want {
		t.Fatalf("LiveResourcesSampleFile() = %q, want %q", got, want)
	}

	if got, err := LoadLiveResourcesFile(paths.LiveResourcesFile()); err != nil || got != LiveResourcesOff {
		t.Fatalf("missing LoadLiveResourcesFile() = %q, %v, want off, nil", got, err)
	}
	if err := SaveLiveResourcesFile(paths.LiveResourcesFile(), LiveResourcesOn); err != nil {
		t.Fatalf("SaveLiveResourcesFile(on) error = %v", err)
	}
	if got, err := LoadLiveResourcesFile(paths.LiveResourcesFile()); err != nil || got != LiveResourcesOn {
		t.Fatalf("LoadLiveResourcesFile() = %q, %v, want on, nil", got, err)
	}
	info, err := os.Stat(paths.LiveResourcesFile())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("live resources file mode = %o, want 600", got)
	}
}

func TestNormalizeLiveResourcesModeDefaultsOff(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "off", "unknown", "disabled"} {
		if got := NormalizeLiveResourcesMode(value); got != LiveResourcesOff {
			t.Fatalf("NormalizeLiveResourcesMode(%q) = %q, want off", value, got)
		}
	}
	if got := NormalizeLiveResourcesMode(" ON \n"); got != LiveResourcesOn {
		t.Fatalf("NormalizeLiveResourcesMode(on) = %q, want on", got)
	}
}
