package projmux_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestResolvePathsInputMatrix(t *testing.T) {
	// Explicit expectations pin the contract as well as parity with the CORE
	// authority. This test calls only exported SDK API from an external package.
	tests := []struct {
		name, home, config, state string
		want                      projmux.Paths
		wantError                 string
	}{
		{name: "home defaults", home: "/home/sdk", want: projmux.Paths{ConfigDir: "/home/sdk/.config/projmux", StateDir: "/home/sdk/.local/state/projmux"}},
		{name: "absolute XDG", home: "/home/sdk", config: "/cfg", state: "/state", want: projmux.Paths{ConfigDir: "/cfg/projmux", StateDir: "/state/projmux"}},
		{name: "config only", home: "/home/sdk", config: "/cfg", want: projmux.Paths{ConfigDir: "/cfg/projmux", StateDir: "/home/sdk/.local/state/projmux"}},
		{name: "state only", home: "/home/sdk", state: "/state", want: projmux.Paths{ConfigDir: "/home/sdk/.config/projmux", StateDir: "/state/projmux"}},
		{name: "blank XDG", home: "/home/sdk", config: " \t", state: "\n", want: projmux.Paths{ConfigDir: "/home/sdk/.config/projmux", StateDir: "/home/sdk/.local/state/projmux"}},
		{name: "relative XDG", home: "/home/sdk", config: "config", state: "state", want: projmux.Paths{ConfigDir: "/home/sdk/.config/projmux", StateDir: "/home/sdk/.local/state/projmux"}},
		{name: "literal tilde", home: "/home/sdk", config: "~/config", state: "~/state", want: projmux.Paths{ConfigDir: "/home/sdk/.config/projmux", StateDir: "/home/sdk/.local/state/projmux"}},
		{name: "trim and clean XDG", home: "/home/sdk", config: " /cfg/../config/ ", state: " /state// ", want: projmux.Paths{ConfigDir: "/config/projmux", StateDir: "/state/projmux"}},
		{name: "no HOME absolute XDG", config: "/cfg", state: "/state", want: projmux.Paths{ConfigDir: "/cfg/projmux", StateDir: "/state/projmux"}},
		{name: "no HOME both missing", wantError: "HOME or an absolute XDG_CONFIG_HOME is required"},
		{name: "no HOME config missing", state: "/state", wantError: "HOME or an absolute XDG_CONFIG_HOME is required"},
		{name: "no HOME config relative", config: "cfg", state: "/state", wantError: "HOME or an absolute XDG_CONFIG_HOME is required"},
		{name: "no HOME state missing", config: "/cfg", wantError: "HOME or an absolute XDG_STATE_HOME is required"},
		{name: "no HOME state relative", config: "/cfg", state: "state", wantError: "HOME or an absolute XDG_STATE_HOME is required"},
		{name: "blank HOME", home: " \t", wantError: "HOME or an absolute XDG_CONFIG_HOME is required"},
		{name: "blank HOME absolute XDG", home: " \t", config: "/cfg", state: "/state", want: projmux.Paths{ConfigDir: "/cfg/projmux", StateDir: "/state/projmux"}},
		{name: "relative HOME", home: "relative", want: projmux.Paths{ConfigDir: "relative/.config/projmux", StateDir: "relative/.local/state/projmux"}},
		{name: "spaces in HOME", home: " home sdk ", want: projmux.Paths{ConfigDir: " home sdk /.config/projmux", StateDir: " home sdk /.local/state/projmux"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			options := projmux.ResolvePathsOptions{
				HomeDir: func() (string, error) { calls = append(calls, "home"); return tt.home, nil },
				LookupEnv: func(key string) string {
					calls = append(calls, key)
					switch key {
					case "XDG_CONFIG_HOME":
						return tt.config
					case "XDG_STATE_HOME":
						return tt.state
					default:
						t.Fatalf("unexpected environment read %q", key)
						return ""
					}
				},
			}
			got, err := projmux.ResolvePaths(options)
			if got != tt.want {
				t.Fatalf("SDK paths = %+v, want %+v", got, tt.want)
			}
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tt.wantError || !errors.Is(err, projmux.ErrHomeDirRequired) {
				t.Fatalf("SDK error = %v, want %q matching public HOME sentinel", err, tt.wantError)
			}
			if !slices.Equal(calls, []string{"home", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}) {
				t.Fatalf("callback order = %v", calls)
			}
			core, coreErr := mutationruntime.ConfigPaths(options.HomeDir, options.LookupEnv)
			if got != (projmux.Paths{ConfigDir: core.ConfigDir, StateDir: core.StateDir}) || fmt.Sprint(err) != fmt.Sprint(coreErr) {
				t.Fatalf("SDK (%+v, %v) differs from CORE (%+v, %v)", got, err, core, coreErr)
			}
		})
	}
}

func TestResolvePathsErrorIdentityAndOrder(t *testing.T) {
	if projmux.ErrHomeDirRequired != config.ErrHomeDirRequired {
		t.Fatal("public sentinel is not the CORE value")
	}
	if got := projmux.ErrHomeDirRequired.Error(); got != "home directory is required when XDG homes are unset" {
		t.Fatalf("sentinel wording = %q", got)
	}
	for _, cause := range []error{errors.New("home callback failed"), projmux.ErrHomeDirRequired, fmt.Errorf("wrapped: %w", projmux.ErrHomeDirRequired)} {
		t.Run(cause.Error(), func(t *testing.T) {
			envCalls := 0
			got, err := projmux.ResolvePaths(projmux.ResolvePathsOptions{
				HomeDir:   func() (string, error) { return "/ignored", cause },
				LookupEnv: func(string) string { envCalls++; return "/absolute" },
			})
			if got != (projmux.Paths{}) || !errors.Is(err, cause) || err.Error() != "resolve home directory: "+cause.Error() || envCalls != 0 {
				t.Fatalf("paths=%+v error=%v env calls=%d; want zero paths, wrapped cause before environment", got, err, envCalls)
			}
		})
	}
}

func TestResolvePathsNilCallbacks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	tests := []struct {
		name    string
		options projmux.ResolvePathsOptions
		want    projmux.Paths
	}{
		{"both nil", projmux.ResolvePathsOptions{}, projmux.Paths{ConfigDir: filepath.Join(root, "config", "projmux"), StateDir: filepath.Join(root, "state", "projmux")}},
		{"nil home explicit empty env", projmux.ResolvePathsOptions{LookupEnv: func(string) string { return "" }}, projmux.Paths{ConfigDir: filepath.Join(root, "home", ".config", "projmux"), StateDir: filepath.Join(root, "home", ".local", "state", "projmux")}},
		{"nil env explicit empty home", projmux.ResolvePathsOptions{HomeDir: func() (string, error) { return "", nil }}, projmux.Paths{ConfigDir: filepath.Join(root, "config", "projmux"), StateDir: filepath.Join(root, "state", "projmux")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := projmux.ResolvePaths(tt.options)
			if err != nil || got != tt.want {
				t.Fatalf("paths=%+v error=%v, want %+v", got, err, tt.want)
			}
		})
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	got, err := projmux.ResolvePaths(projmux.ResolvePathsOptions{})
	if got != (projmux.Paths{}) || err == nil || err.Error() != "resolve home directory: $HOME is not defined" {
		t.Fatalf("OS home error = (%+v, %v)", got, err)
	}
}

func TestResolvePathsNoSideEffects(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	configDir := filepath.Join(root, "config", "projmux")
	stateDir := filepath.Join(root, "state", "projmux")
	for _, dir := range []string{configDir, stateDir, filepath.Join(configDir, "profiles"), filepath.Join(configDir, "personas")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(configDir, "locale"), filepath.Join(configDir, "profiles", "broken.toml"), filepath.Join(configDir, "personas", "broken.md"), filepath.Join(configDir, "agent-guidance.md"), filepath.Join(stateDir, "registry.json")} {
		if err := os.WriteFile(path, []byte("malformed\x00["), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// An inaccessible profile directory must not prevent path resolution.
	blocked := filepath.Join(configDir, "profiles", "unreadable")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
	before := centralTree(t, root)
	if err := os.Chmod(blocked, 0000); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	restore := config.ObserveFrontReads(func(config.FrontRead) { reads.Add(1) })
	defer restore()
	// Positive control proves that the observer is installed, without opening a
	// file. This does not claim coverage of loaders outside the observer seam.
	config.NoteFrontRead(config.SettingConfigTheme, filepath.Join(configDir, "theme"))
	if reads.Swap(0) != 1 {
		t.Fatal("front read observer positive control failed")
	}
	environment := os.Environ()
	for _, options := range []projmux.ResolvePathsOptions{
		{},
		{HomeDir: func() (string, error) { return root, nil }, LookupEnv: func(string) string { return "" }},
		{HomeDir: func() (string, error) { return "", nil }, LookupEnv: func(string) string { return "" }},
	} {
		_, _ = projmux.ResolvePaths(options)
	}
	if reads.Load() != 0 {
		t.Fatalf("path resolver observed %d front reads", reads.Load())
	}
	if !reflect.DeepEqual(environment, os.Environ()) {
		t.Fatal("path resolver changed the environment")
	}
	if err := os.Chmod(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("path resolution changed directory tree, bytes, permissions or mtimes")
	}
}
