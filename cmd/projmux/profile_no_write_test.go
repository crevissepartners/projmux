package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Each child calls the production main, including bootstrap migrations,
// invocation observers and RecordOutcome. It joins this test's liveguard;
// only its fixture HOME/config/state/project are exposed to the command.
func TestProfileInspectionMainHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PROFILE_MAIN_HELPER") != "1" {
		return
	}
	root := os.Getenv("GO_PROFILE_MAIN_ROOT")
	if !filepath.IsAbs(root) {
		panic("fixture root must be absolute")
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PROJMUX_") || key == "TMUX" || key == "TMUX_PANE" || key == "__PROJMUX_RUNTIME_ANCHOR_PANE" {
			if err := os.Unsetenv(key); err != nil {
				panic(err)
			}
		}
	}
	for key, leaf := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "TMUX_TMPDIR": "tmux", "PROJMUX_CWD": "project"} {
		if err := os.Setenv(key, filepath.Join(root, leaf)); err != nil {
			panic(err)
		}
	}
	if os.Getenv("GO_PROFILE_MAIN_NO_HOME") == "1" {
		for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
			if err := os.Setenv(key, ""); err != nil {
				panic(err)
			}
		}
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("GO_PROFILE_MAIN_ARGS")), &args); err != nil {
		panic(err)
	}
	os.Args = append([]string{"projmux"}, args...)
	main()
	os.Exit(0)
}

func profileMainTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		var digest string
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest = fmt.Sprintf("%x", sha256.Sum256(content))
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%v:%d:%s", info.Mode(), info.ModTime().UnixNano(), digest)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProfileInspectionMainWritesNothing(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"parse", "preview"} {
		for _, scenario := range []string{"success", "syntax", "flag", "io", "no-home"} {
			t.Run(verb+"/"+scenario, func(t *testing.T) {
				root := t.TempDir()
				for _, leaf := range []string{"home", "tmux", "project/.projmux", "config/projmux/hooks"} {
					if err := os.MkdirAll(filepath.Join(root, leaf), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				for _, leaf := range []string{"project/.projmux/post-create", "config/projmux/hooks/post-create"} {
					if err := os.WriteFile(filepath.Join(root, leaf), []byte("#!/bin/sh\necho fixture\n"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				input := filepath.Join(root, "profile.toml")
				content := []byte("provider=\"codex\"\n")
				if scenario == "syntax" {
					content = []byte("model=")
				}
				if err := os.WriteFile(input, content, 0o600); err != nil {
					t.Fatal(err)
				}
				args := []string{"profile", verb}
				if verb == "preview" {
					args = append(args, "candidate")
				}
				if scenario == "flag" {
					args = append(args, "--zz-probe")
				} else {
					if scenario == "io" {
						input = filepath.Join(root, "missing")
					}
					args = append(args, "--file", input)
				}
				encoded, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				before := profileMainTree(t, root)
				cmd := exec.Command(executable, "-test.run=^TestProfileInspectionMainHelper$")
				cmd.Env = append(os.Environ(), "GO_WANT_PROFILE_MAIN_HELPER=1", "GO_PROFILE_MAIN_ROOT="+root, "GO_PROFILE_MAIN_ARGS="+string(encoded))
				if scenario == "no-home" {
					cmd.Env = append(cmd.Env, "GO_PROFILE_MAIN_NO_HOME=1")
				}
				output, runErr := cmd.CombinedOutput()
				code := 0
				if runErr != nil {
					var exit *exec.ExitError
					if !errors.As(runErr, &exit) {
						t.Fatal(runErr)
					}
					code = exit.ExitCode()
				}
				want := 0
				if scenario == "syntax" || scenario == "flag" {
					want = 2
				}
				if scenario == "io" {
					want = 1
				}
				if scenario == "no-home" && verb == "preview" {
					want = 1
				}
				if code != want {
					t.Errorf("exit=%d want=%d output=%s", code, want, output)
				}
				if after := profileMainTree(t, root); !reflect.DeepEqual(before, after) {
					t.Errorf("main %s %s changed HOME/config/state/project tree\nbefore=%v\nafter=%v\noutput=%s", verb, scenario, before, after, output)
				}
			})
		}
	}
}
