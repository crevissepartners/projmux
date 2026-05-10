package app

import (
	"path/filepath"
	"testing"
)

func TestDefaultLifecycleHookRunnerWiresGlobalConfigPath(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	stateHome := filepath.Join(home, ".local", "state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("TMUX", "")

	runner := defaultLifecycleHookRunner()
	if runner == nil {
		t.Fatal("defaultLifecycleHookRunner() = nil")
	}

	want := filepath.Join(configHome, "projmux", "config.toml")
	if runner.GlobalConfigPath != want {
		t.Fatalf("GlobalConfigPath = %q, want %q", runner.GlobalConfigPath, want)
	}
	if !runner.DiscoverProjectHooks {
		t.Fatal("DiscoverProjectHooks = false, want true")
	}
	if runner.ProjectHookPrompt != nil {
		t.Fatal("ProjectHookPrompt = non-nil outside tmux, want terminal fallback")
	}
}

func TestDefaultLifecycleHookRunnerUsesPopupPromptInsideTmux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("TMUX", "/tmp/tmux/default,1,0")

	runner := defaultLifecycleHookRunner()
	if runner == nil {
		t.Fatal("defaultLifecycleHookRunner() = nil")
	}
	if runner.ProjectHookPrompt == nil {
		t.Fatal("ProjectHookPrompt = nil inside tmux, want popup prompt")
	}
}

func TestDefaultLifecycleHookRunnerUsesTerminalPromptWhenInlineTrustSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("TMUX", "/tmp/tmux/default,1,0")
	t.Setenv(hookTrustInlineEnv, "1")

	runner := defaultLifecycleHookRunner()
	if runner == nil {
		t.Fatal("defaultLifecycleHookRunner() = nil")
	}
	if runner.ProjectHookPrompt != nil {
		t.Fatal("ProjectHookPrompt = non-nil with inline trust env, want terminal fallback")
	}
}
