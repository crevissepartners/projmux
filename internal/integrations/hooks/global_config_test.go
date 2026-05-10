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
}

func TestUpdateGlobalConfigEmptyPathErrors(t *testing.T) {
	t.Parallel()

	if _, err := UpdateGlobalConfig("", nil); err == nil {
		t.Fatal("UpdateGlobalConfig(empty path) should error")
	}
}
