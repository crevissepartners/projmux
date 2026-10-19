package consumer_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalAISplitModeConsumer(t *testing.T) {
	t.Setenv("TMUX", "")
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	c, err := projmux.NewClient(projmux.Options{ConfigDir: dir, StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	var initial core.AISplitModeState = c.GetAISplitMode()
	if initial != (core.AISplitModeState{Mode: "selective", Source: "default"}) {
		t.Fatal(initial)
	}
	got, err := c.SetAISplitMode("  resume ")
	if err != nil || got != (core.AISplitModeState{Mode: "resume", Source: "tui"}) || c.GetAISplitMode() != got {
		t.Fatal(got, err)
	}
	path := filepath.Join(dir, "tmux-ai-split-mode")
	before, err := os.ReadFile(path)
	if err != nil || string(before) != "resume\n" {
		t.Fatal(string(before), err)
	}
	_, err = c.SetAISplitMode("unknown")
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeConfigValueInvalid {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal(string(after))
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = c.SetAISplitMode("shell")
	var cause *os.PathError
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAISplitModeSaveFailed || !errors.As(err, &cause) || !errors.Is(err, cause.Err) {
		t.Fatal(err)
	}
}
