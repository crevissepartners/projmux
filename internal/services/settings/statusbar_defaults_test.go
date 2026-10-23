package settings

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/usage"
)

// centralDefaultsBuiltIn is the independent expectation: the twelve central
// keys by byte order with their built-in values (Codex 5h off).
var centralDefaultsBuiltIn = []struct{ key, value string }{
	{"agent-usage-hud", "on"},
	{"agent-usage-provider-claude", "on"},
	{"agent-usage-provider-codex", "on"},
	{"agent-usage-window-claude-5h", "on"},
	{"agent-usage-window-claude-weekly", "on"},
	{"agent-usage-window-codex-5h", "off"},
	{"agent-usage-window-codex-weekly", "on"},
	{"clock", "on"},
	{"git", "on"},
	{"notifications-hud", "on"},
	{"project", "on"},
	{"working-directory", "on"},
}

// expectedCentralDefaults is the built-in table with the given keys read as
// Source central.
func expectedCentralDefaults(central map[string]string) []VisibilityState {
	out := []VisibilityState{}
	for _, leaf := range centralDefaultsBuiltIn {
		state := VisibilityState{Key: leaf.key, Effective: leaf.value, Source: "default"}
		if value, ok := central[leaf.key]; ok {
			state = VisibilityState{Key: leaf.key, Effective: value, Saved: value, Source: "central"}
		}
		out = append(out, state)
	}
	return out
}

func centralDefaultsFixture(t *testing.T) config.Paths {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return paths
}

func writeCentralDefaultsFile(t *testing.T, paths config.Paths, content string) {
	t.Helper()
	if err := os.WriteFile(paths.StatusbarDefaultsFile(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeValidTUILeaves stores a valid "off" in every TUI leaf, which the
// central read must ignore.
func writeValidTUILeaves(t *testing.T, paths config.Paths) {
	t.Helper()
	for _, path := range append(CentralStatusbarSeedLeaves(paths), paths.StatusbarSettingsLauncherVisibilityFile()) {
		if err := config.SaveStatusbarVisibilityFile(path, config.StatusbarVisibilityOff); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCentralStatusbarDefaultsMatrix is acceptance 1 and 5 for the shared
// read: stored values read as central, and a missing, invalid, unparsable or
// unreadable central file or an unresolvable path reads as the built-in
// defaults without a new diagnostic. Valid TUI leaves never count.
func TestCentralStatusbarDefaultsMatrix(t *testing.T) {
	pathErr := errors.New("path resolver unavailable")
	cases := []struct {
		name    string
		setup   func(*testing.T, config.Paths)
		pathErr error
		central map[string]string
	}{
		{name: "missing file", setup: func(*testing.T, config.Paths) {}},
		{name: "stored values", setup: func(t *testing.T, p config.Paths) {
			writeCentralDefaultsFile(t, p, `{"visibility":{"clock":"off","git":" ON ","agent-usage-window-codex-5h":"on","settings-launcher":"off","unknown-leaf":"off"},"other":1}`)
		}, central: map[string]string{"clock": "off", "git": "on", "agent-usage-window-codex-5h": "on"}},
		{name: "invalid leaf value", setup: func(t *testing.T, p config.Paths) {
			writeCentralDefaultsFile(t, p, `{"visibility":{"clock":"maybe","agent-usage-window-codex-5h":"","project":"off"}}`)
		}, central: map[string]string{"project": "off"}},
		{name: "malformed json", setup: func(t *testing.T, p config.Paths) {
			writeCentralDefaultsFile(t, p, `{"visibility":{"clock":"off"`)
		}},
		{name: "directory read error", setup: func(t *testing.T, p config.Paths) {
			if err := os.Mkdir(p.StatusbarDefaultsFile(), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "config dir is a file", setup: func(t *testing.T, p config.Paths) {
			if err := os.RemoveAll(p.ConfigDir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p.ConfigDir, []byte("not a directory\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "path error", setup: func(t *testing.T, p config.Paths) {
			writeCentralDefaultsFile(t, p, `{"visibility":{"clock":"off"}}`)
		}, pathErr: pathErr},
		{name: "valid TUI leaves ignored", setup: func(t *testing.T, p config.Paths) {
			writeValidTUILeaves(t, p)
			writeCentralDefaultsFile(t, p, `{"visibility":{"clock":"on"}}`)
		}, central: map[string]string{"clock": "on"}},
	}
	if os.Geteuid() != 0 {
		cases = append(cases, struct {
			name    string
			setup   func(*testing.T, config.Paths)
			pathErr error
			central map[string]string
		}{name: "permission read error", setup: func(t *testing.T, p config.Paths) {
			writeCentralDefaultsFile(t, p, `{"visibility":{"clock":"off"}}`)
			if err := os.Chmod(p.StatusbarDefaultsFile(), 0); err != nil {
				t.Fatal(err)
			}
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := centralDefaultsFixture(t)
			tc.setup(t, paths)
			if tc.central == nil && tc.pathErr == nil && tc.name != "missing file" {
				// The strict read must really fail here, or the row proves nothing.
				if _, err := config.LoadStatusbarDefaults(paths.StatusbarDefaultsFile()); err == nil {
					t.Fatal("fixture did not make the central read fail")
				}
			}
			got := CentralStatusbarDefaults(func() (config.Paths, error) { return paths, tc.pathErr })
			if want := expectedCentralDefaults(tc.central); !reflect.DeepEqual(got, want) {
				t.Fatalf("defaults =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// TestCentralStatusbarDefaultsFollowSeedAuthority is acceptance 2: the keys are
// the seed leaves of the shared capability table, a window's default is its
// capability default, and the set equals the strict facade's keys.
func TestCentralStatusbarDefaultsFollowSeedAuthority(t *testing.T) {
	paths := centralDefaultsFixture(t)
	wantLeaves := []string{paths.StatusbarNotificationsHUDVisibilityFile(), paths.StatusbarAgentUsageHUDVisibilityFile()}
	windowDefaults := map[string]string{}
	for _, capability := range usage.HUDProviderCapabilities() {
		wantLeaves = append(wantLeaves, paths.StatusbarAgentUsageProviderVisibilityFile(string(capability.ID)))
		for _, window := range capability.Windows {
			leaf := paths.StatusbarAgentUsageWindowVisibilityFile(string(capability.ID), window.Key)
			wantLeaves = append(wantLeaves, leaf)
			key, _ := config.StatusbarDefaultKey(leaf)
			windowDefaults[key] = string(window.DefaultVisibility)
		}
	}
	wantLeaves = append(wantLeaves, paths.StatusbarProjectVisibilityFile(), paths.StatusbarWorkingDirectoryVisibilityFile(), paths.StatusbarGitVisibilityFile(), paths.StatusbarClockVisibilityFile())
	if got := CentralStatusbarSeedLeaves(paths); !reflect.DeepEqual(got, wantLeaves) {
		t.Fatalf("seed leaves = %v, want %v", got, wantLeaves)
	}
	wantKeys := []string{}
	for _, leaf := range wantLeaves {
		key, ok := config.StatusbarDefaultKey(leaf)
		if !ok {
			t.Fatal(leaf)
		}
		wantKeys = append(wantKeys, key)
	}
	slices.Sort(wantKeys)
	if !reflect.DeepEqual(wantKeys, centralVisibilityKeys) {
		t.Fatalf("seed keys %v drift from the strict facade keys %v", wantKeys, centralVisibilityKeys)
	}
	got := CentralStatusbarDefaults(func() (config.Paths, error) { return paths, nil })
	keys := []string{}
	for _, state := range got {
		keys = append(keys, state.Key)
		if def, ok := windowDefaults[state.Key]; ok && state.Effective != def {
			t.Fatalf("%s default = %s, want capability %s", state.Key, state.Effective, def)
		}
	}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("keys = %v, want %v", keys, wantKeys)
	}
	if windowDefaults["agent-usage-window-codex-5h"] != "off" {
		t.Fatalf("Codex 5h capability default = %q, want off", windowDefaults["agent-usage-window-codex-5h"])
	}
}

// TestCentralStatusbarDefaultsReadsNothingElse is acceptance 3 for the shared
// read: no front read, no file change and an independent slice per call.
func TestCentralStatusbarDefaultsReadsNothingElse(t *testing.T) {
	paths := centralDefaultsFixture(t)
	writeValidTUILeaves(t, paths)
	writeCentralDefaultsFile(t, paths, `{"visibility":{"clock":"off"}}`)
	before := settingsTree(t, filepath.Dir(paths.ConfigDir))
	var reads []config.FrontRead
	restore := config.ObserveFrontReads(func(read config.FrontRead) { reads = append(reads, read) })
	got := CentralStatusbarDefaults(func() (config.Paths, error) { return paths, nil })
	restore()
	if len(reads) != 0 {
		t.Fatalf("front reads = %+v, want none", reads)
	}
	if after := settingsTree(t, filepath.Dir(paths.ConfigDir)); !reflect.DeepEqual(before, after) {
		t.Fatalf("files changed:\n%v\n%v", before, after)
	}
	got[0].Effective = "changed"
	if again := CentralStatusbarDefaults(func() (config.Paths, error) { return paths, nil }); !reflect.DeepEqual(again, expectedCentralDefaults(map[string]string{"clock": "off"})) {
		t.Fatalf("a caller's change leaked into the next read: %+v", again)
	}
}

func settingsTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
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
			if data, err = os.ReadFile(path); err != nil {
				return err
			}
		}
		tree[path] = info.Mode().String() + info.ModTime().String() + string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
