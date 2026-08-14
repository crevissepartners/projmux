package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalConfigPathPrefersXdg(t *testing.T) {
	t.Parallel()

	configHome := t.TempDir()
	getenv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return configHome
		}
		return ""
	}
	got, err := GlobalConfigPath(getenv, func() (string, error) { return "/should/not/be/used", nil })
	if err != nil {
		t.Fatalf("GlobalConfigPath() error = %v", err)
	}
	want := filepath.Join(configHome, "projmux", "config.toml")
	if got != want {
		t.Fatalf("GlobalConfigPath() = %q, want %q", got, want)
	}
}

func TestGlobalConfigPathFallsBackToHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	getenv := func(string) string { return "" }
	got, err := GlobalConfigPath(getenv, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatalf("GlobalConfigPath() error = %v", err)
	}
	want := filepath.Join(home, ".config", "projmux", "config.toml")
	if got != want {
		t.Fatalf("GlobalConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadGlobalConfigMissingFileReturnsEmpty(t *testing.T) {
	t.Parallel()

	cfg, err := LoadGlobalConfig(filepath.Join(t.TempDir(), "missing", "config.toml"))
	if err != nil {
		t.Fatalf("LoadGlobalConfig() error = %v", err)
	}
	if len(cfg.Hooks) != 0 || len(cfg.Env) != 0 || cfg.StartupRun != "" {
		t.Fatalf("LoadGlobalConfig() = %#v, want empty", cfg)
	}
}

func TestUpdateGlobalConfigWritesAndReadsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "projmux", "config.toml")
	_, err := UpdateGlobalConfig(path, func(cfg *ProjectConfig) error {
		if cfg.Hooks == nil {
			cfg.Hooks = map[Event]string{}
		}
		cfg.Hooks[EventPostCreate] = "echo global-post"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateGlobalConfig() error = %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), "[hooks.post-create]") {
		t.Fatalf("config.toml missing section:\n%s", body)
	}
	if !strings.Contains(string(body), `run = "echo global-post"`) {
		t.Fatalf("config.toml missing run line:\n%s", body)
	}

	cfg, err := LoadGlobalConfig(path)
	if err != nil {
		t.Fatalf("LoadGlobalConfig() error = %v", err)
	}
	if cfg.Hooks[EventPostCreate] != "echo global-post" {
		t.Fatalf("Hooks[post-create] = %q, want %q", cfg.Hooks[EventPostCreate], "echo global-post")
	}
	assertGlobalConfigMode(t, filepath.Dir(path), 0o700)
	assertGlobalConfigMode(t, path, 0o600)
}

func TestUpdateGlobalConfigPreservesExistingSymlinkAndTargetMetadata(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path, err := GlobalConfigPath(func(string) string { return "" }, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatalf("GlobalConfigPath() error = %v", err)
	}
	target := filepath.Join(home, "config-targets", "global.toml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("[ui]\nlocale = \"en-US\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(filepath.Dir(path), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, path); err != nil {
		t.Fatal(err)
	}
	linkBefore, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	targetBefore, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	uidBefore, gidBefore, ownerOK := projectConfigFileOwner(targetBefore)

	_, err = UpdateGlobalConfig(path, func(cfg *ProjectConfig) error {
		cfg.UI.Locale = "ko-KR"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateGlobalConfig() error = %v", err)
	}

	linkAfter, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if linkAfter.Mode()&os.ModeSymlink == 0 || !os.SameFile(linkBefore, linkAfter) {
		t.Fatal("global writer replaced the config symlink")
	}
	targetAfter, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := targetAfter.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("target mode = %#o, want %#o", got, want)
	}
	if uidAfter, gidAfter, ok := projectConfigFileOwner(targetAfter); ownerOK && (!ok || uidAfter != uidBefore || gidAfter != gidBefore) {
		t.Fatalf("target owner = (%d,%d,%v), want (%d,%d,true)", uidAfter, gidAfter, ok, uidBefore, gidBefore)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, `locale = "ko-KR"`) {
		t.Fatalf("global target content = %q, want updated locale", got)
	}
}

func assertGlobalConfigMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}

func TestUpdateGlobalConfigStoresUILocale(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "projmux", "config.toml")
	_, err := UpdateGlobalConfig(path, func(cfg *ProjectConfig) error {
		cfg.UI.Locale = "auto"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateGlobalConfig() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `[ui]
locale = "auto"
`
	if string(got) != want {
		t.Fatalf("config.toml =\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateGlobalConfigStoresNativeKeysOptOut(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "projmux", "config.toml")
	_, err := UpdateGlobalConfig(path, func(cfg *ProjectConfig) error {
		enabled := false
		cfg.UI.NativeKeys = &enabled
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateGlobalConfig() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "[ui]\nnative_keys = false\n"
	if string(got) != want {
		t.Fatalf("config.toml =\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateGlobalConfigEmptyPathErrors(t *testing.T) {
	t.Parallel()

	if _, err := UpdateGlobalConfig("", nil); err == nil {
		t.Fatal("UpdateGlobalConfig(empty path) should error")
	}
}
