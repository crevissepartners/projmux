package consumer_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestExternalResolvePathsConsumer(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	state := filepath.Join(root, "state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", state)
	// Invalid stored contents cannot affect a path-only call.
	for _, path := range []string{
		filepath.Join(config, "projmux", "config.toml"),
		filepath.Join(config, "projmux", "profiles", "invalid.toml"),
		filepath.Join(config, "projmux", "personas", "invalid.md"),
		filepath.Join(config, "projmux", "agent-guidance.md"),
		filepath.Join(state, "projmux", "registry.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("invalid\x00["), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// PATH spies reject accidental provider/tmux startup and leave evidence.
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	commandLog := filepath.Join(root, "commands.log")
	for _, name := range []string{"tmux", "projmux", "claude", "codex", "antigravity"} {
		script := "#!/bin/sh\nprintf '%s\\n' " + strconv.Quote(name) + " >> " + strconv.Quote(commandLog) + "\nexit 99\n"
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)
	before := consumerPathTree(t, root)
	environment := os.Environ()
	got, err := projmux.ResolvePaths(projmux.ResolvePathsOptions{})
	want := projmux.Paths{ConfigDir: filepath.Join(config, "projmux"), StateDir: filepath.Join(state, "projmux")}
	if err != nil || got != want {
		t.Fatalf("OS paths=%+v error=%v, want %+v", got, err, want)
	}
	calls := []string{}
	got, err = projmux.ResolvePaths(projmux.ResolvePathsOptions{
		HomeDir: func() (string, error) { calls = append(calls, "home"); return "", nil },
		LookupEnv: func(key string) string {
			calls = append(calls, key)
			if key == "XDG_CONFIG_HOME" {
				return config
			}
			return "relative-state"
		},
	})
	if got != (projmux.Paths{}) || !errors.Is(err, projmux.ErrHomeDirRequired) || err.Error() != "HOME or an absolute XDG_STATE_HOME is required" {
		t.Fatalf("missing HOME paths=%+v error=%v", got, err)
	}
	if !reflect.DeepEqual(calls, []string{"home", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}) {
		t.Fatalf("input order=%v", calls)
	}
	if _, err := os.Stat(commandLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected provider/tmux execution, log stat: %v", err)
	}
	if !reflect.DeepEqual(before, consumerPathTree(t, root)) || !reflect.DeepEqual(environment, os.Environ()) {
		t.Fatal("path-only consumer changed files or environment")
	}
}

func consumerPathTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data := []byte{}
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		snapshot[path] = info.Mode().String() + info.ModTime().String() + string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
