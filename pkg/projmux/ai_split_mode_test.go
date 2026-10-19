package projmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestAISplitModePublicContract(t *testing.T) {
	t.Setenv("TMUX", "")
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	c, err := projmux.NewClient(projmux.Options{ConfigDir: dir, StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.GetAISplitMode(); got != (core.AISplitModeState{Mode: "selective", Source: "default"}) {
		t.Fatal(got)
	}
	// Invalid public input must be rejected before even creating ConfigDir.
	for _, mode := range []string{"", "unknown", "CODEX"} {
		v, err := c.SetAISplitMode(mode)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeConfigValueInvalid || v != (core.AISplitModeState{}) {
			t.Fatalf("%+v %v", v, err)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	central := "resume"
	ctx := context.Background()
	if _, err := c.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigAIDefaultMode, DefaultMode: &central}); err != nil {
		t.Fatal(err)
	}
	if got := c.GetAISplitMode(); got != (core.AISplitModeState{Mode: "resume", Source: "central"}) {
		t.Fatal(got)
	}
	v, err := c.SetAISplitMode("  codex\n")
	if err != nil || v != (core.AISplitModeState{Mode: "codex", Source: "tui"}) {
		t.Fatal(v, err)
	}
	if got := c.GetAISplitMode(); got != v {
		t.Fatal(got, v)
	}
	path := filepath.Join(dir, "tmux-ai-split-mode")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "codex\n" {
		t.Fatal(string(b), err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetAISplitMode("shell"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
	value, err := c.GetConfig(ctx, core.ConfigAIDefaultMode)
	if err != nil || value.DefaultMode != "resume" {
		t.Fatal(value, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.SetConfig(canceled, core.ConfigUpdate{Key: core.ConfigAIDefaultMode, DefaultMode: &central}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.GetConfig(canceled, core.ConfigAIDefaultMode); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := c.GetAISplitMode(); got.Source != "central" {
		t.Fatal(got)
	}
	_, err = c.SetAISplitMode("codex")
	var refusal *projmux.Refusal
	var cause *os.PathError
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAISplitModeSaveFailed || !errors.As(err, &cause) || !errors.Is(err, cause.Err) || err.Error() != cause.Error() {
		t.Fatalf("%T %v", err, err)
	}
}

func TestAISplitModePublicCurrentDisplayEnvironment(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "calls")
	// A PATH stub records argv, ambient TMUX/PANE and storage visible at display.
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s|%s|%s\\n' \"$1\" \"$2\" \"$3\" \"$TMUX\" \"$TMUX_PANE\" \"$(/bin/cat \"$AI_TEST_FILE\")\" >> \"$AI_TEST_LOG\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("PATH", bin)
	t.Setenv("AI_TEST_LOG", log)
	t.Setenv("AI_TEST_FILE", filepath.Join(dir, "tmux-ai-split-mode"))
	c, err := projmux.NewClient(projmux.Options{ConfigDir: dir, StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetAISplitMode("codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "fixture-socket")
	t.Setenv("TMUX_PANE", "%fixture")
	if _, err := c.SetAISplitMode("resume"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "-u|display-message|ai split default: resume|fixture-socket|%fixture|resume\n"
	if string(b) != want {
		t.Fatalf("%q", b)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "%changed")
	if _, err := c.SetAISplitMode("shell"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if string(b) != want {
		t.Fatal(string(b))
	}
	// Same client, current PATH becomes unavailable: display failure stays best effort.
	t.Setenv("TMUX", "fixture-socket")
	t.Setenv("PATH", filepath.Join(root, "absent"))
	if _, err := c.SetAISplitMode("claude"); err != nil {
		t.Fatal(err)
	}
	if got := c.GetAISplitMode(); got.Mode != "claude" || got.Source != "tui" {
		t.Fatal(got)
	}
	// Saving fails before any display, even when current TMUX/PATH would allow it.
	t.Setenv("PATH", bin)
	if err := os.Remove(filepath.Join(dir, "tmux-ai-split-mode")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "tmux-ai-split-mode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetAISplitMode("codex"); err == nil {
		t.Fatal("save succeeded")
	}
	b, _ = os.ReadFile(log)
	if string(b) != want {
		t.Fatal(string(b))
	}
}
