package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultTmuxClientPropagatesAppSocketMetadata(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(filepath.Join(configHome, "projmux"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tmuxPath := filepath.Join(binDir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte(`#!/bin/sh
if [ "$1" = "has-session" ]; then
  exit 1
fi
if [ "$1" = "new-session" ]; then
  printf '%%42\n'
fi
`), 0o700); err != nil {
		t.Fatal(err)
	}

	evidencePath := filepath.Join(home, "socket.txt")
	hookCommand := `printf '%s' "$PROJMUX_SOCKET" > "$PROJMUX_TEST_HOOK_OUT"`
	config := fmt.Sprintf("[hooks.post-create]\nrun = %q\n", hookCommand)
	if err := os.WriteFile(filepath.Join(configHome, "projmux", "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PROJMUX_TEST_HOOK_OUT", evidencePath)
	t.Setenv("TMUX", "")

	client := defaultTmuxClient()
	if err := client.EnsureSession(context.Background(), "workspace", home); err != nil {
		t.Fatalf("EnsureSession returned error: %v", err)
	}

	evidence, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(evidence); got != defaultAppSocket {
		t.Fatalf("PROJMUX_SOCKET = %q, want %q", got, defaultAppSocket)
	}
}

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
