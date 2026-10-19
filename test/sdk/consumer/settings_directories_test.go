package consumer_test

import (
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestExternalSettingsDirectoriesConsumer(t *testing.T) {
	paths, err := projmux.ResolvePaths(projmux.ResolvePathsOptions{
		HomeDir:   func() (string, error) { return "/home/sdk", nil },
		LookupEnv: func(string) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	profiles, personas := projmux.SettingsDirectories(paths.ConfigDir)
	if profiles != filepath.Join("/home/sdk/.config/projmux", "profiles") || personas != filepath.Join("/home/sdk/.config/projmux", "personas") {
		t.Fatalf("unexpected settings directories: %q, %q", profiles, personas)
	}
}
