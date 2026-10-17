package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestCentralSettingsAuthoritySweep(t *testing.T) {
	c, _, paths := settingsClient(t, t.TempDir())
	got, err := c.GetConfig(context.Background(), core.ConfigStatusbarVisibility)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for _, path := range centralStatusbarSeedLeaves(paths) {
		key, ok := config.StatusbarDefaultKey(path)
		if !ok {
			t.Fatal(path)
		}
		want = append(want, key)
	}
	slices.Sort(want)
	keys := []string{}
	for _, v := range got.Visibility {
		keys = append(keys, v.Key)
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("central authority drift: %v != %v", keys, want)
	}
	for _, p := range usagecmd.HUDProviderCapabilities() {
		for _, w := range p.Windows {
			key := "agent-usage-window-" + string(p.ID) + "-" + w.Key
			for _, v := range got.Visibility {
				if v.Key == key && v.Effective != string(w.DefaultVisibility) {
					t.Fatalf("default drift %s", key)
				}
			}
		}
	}
	// A valid front leaf wins over a central saved value, which wins over built-in.
	if err := os.MkdirAll(paths.ConfigDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarVisibilityFile(paths.StatusbarClockVisibilityFile(), config.StatusbarVisibilityOff); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetConfig(context.Background(), core.ConfigUpdate{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"clock": "on"}}); err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadLayeredStatusbarVisibility(paths.StatusbarClockVisibilityFile(), paths.StatusbarDefaultsFile(), config.StatusbarVisibilityOn)
	if err != nil || state.Effective != config.StatusbarVisibilityOff || state.Source != config.StatusbarVisibilitySourceSaved {
		t.Fatalf("override %+v %v", state, err)
	}
}
func TestCentralSettingsCLIParity(t *testing.T) {
	c, cmd, _ := settingsClient(t, t.TempDir())
	for _, tc := range []struct {
		route string
		key   core.ConfigKey
		args  []string
		u     core.ConfigUpdate
	}{
		{"resources", core.ConfigStatusbarResources, []string{"--set", "on"}, core.ConfigUpdate{Resources: new("on")}},
		{"ai-default-mode", core.ConfigAIDefaultMode, []string{"--set", "shell"}, core.ConfigUpdate{DefaultMode: new("shell")}},
		{"statusbar-visibility", core.ConfigStatusbarVisibility, []string{"--key", "clock", "--set", "off"}, core.ConfigUpdate{Visibility: map[string]string{"clock": "off"}}},
	} {
		for _, write := range []bool{false, true} {
			args := []string{tc.route}
			if write {
				args = append(args, tc.args...)
			}
			var out, stderr bytes.Buffer
			if err := cmd.Run(args, &out, &stderr); err != nil || stderr.Len() != 0 {
				t.Fatalf("CLI %v: %v %s", args, err, stderr.String())
			}
			var v core.ConfigValue
			var err error
			if write {
				tc.u.Key = tc.key
				v, err = c.SetConfig(context.Background(), tc.u)
			} else {
				v, err = c.GetConfig(context.Background(), tc.key)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Decode the lowercase CLI wire into the independently declared public DTO.
			var wire core.ConfigValue
			if err := json.Unmarshal(out.Bytes(), &wire); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(wire, v) {
				t.Fatalf("CLI/SDK %s: %s vs %+v", tc.route, out.String(), v)
			}
			var shape map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &shape); err != nil || len(shape) != 2 {
				t.Fatalf("projection %s %v", out.String(), err)
			}
			field := map[string]string{"resources": "resources", "ai-default-mode": "defaultMode", "statusbar-visibility": "visibility"}[tc.route]
			if shape["key"] == nil || shape[field] == nil {
				t.Fatalf("wire field names %s", out.String())
			}
			switch tc.route {
			case "resources":
				var state map[string]json.RawMessage
				if err := json.Unmarshal(shape[field], &state); err != nil || len(state) != 4 {
					t.Fatalf("toggle shape %s", out.String())
				}
				for _, key := range []string{"effective", "saved", "source", "invalid"} {
					if state[key] == nil {
						t.Fatal("missing toggle field", key)
					}
				}
			case "statusbar-visibility":
				var leaves []map[string]json.RawMessage
				if err := json.Unmarshal(shape[field], &leaves); err != nil || len(leaves) != 12 {
					t.Fatalf("visibility array %s", out.String())
				}
				for _, leaf := range leaves {
					if len(leaf) != 5 {
						t.Fatal(leaf)
					}
					for _, key := range []string{"key", "effective", "saved", "source", "invalid"} {
						if leaf[key] == nil {
							t.Fatal("missing leaf field", key)
						}
					}
				}
			}

		}
	}
}
func TestCentralSettingsCLIEntrypoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	c, err := projmux.NewClient(projmux.Options{HomeDir: home, ConfigDir: filepath.Join(home, "config", "projmux"), StateDir: filepath.Join(home, "state", "projmux")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		key  core.ConfigKey
		good bool
	}{
		{[]string{"resources"}, core.ConfigStatusbarResources, true},
		{[]string{"resources", "--set", "on"}, core.ConfigStatusbarResources, true},
		{[]string{"ai-default-mode", "--set", "resume"}, core.ConfigAIDefaultMode, true},
		{[]string{"statusbar-visibility", "--key", "clock", "--set", "off"}, core.ConfigStatusbarVisibility, true},
		{[]string{"resources", "--set", "bad"}, core.ConfigStatusbarResources, false},
		{[]string{"ai-default-mode", "--set", "CLAUDE"}, core.ConfigAIDefaultMode, false},
		{[]string{"statusbar-visibility", "--key", "settings-launcher", "--set", "on"}, core.ConfigStatusbarVisibility, false},
		{[]string{"statusbar-visibility", "--key", "clock"}, core.ConfigStatusbarVisibility, false},
		{[]string{"statusbar-visibility", "--set", "on"}, core.ConfigStatusbarVisibility, false},
		{[]string{"resources", "extra"}, core.ConfigStatusbarResources, false},
		{[]string{"resources", "--unknown"}, core.ConfigStatusbarResources, false},
		{[]string{"resources", "--set"}, core.ConfigStatusbarResources, false},
	} {
		var out, stderr bytes.Buffer
		err := Run(append([]string{"config"}, tc.args...), &out, &stderr)
		if !tc.good {
			if err == nil || !IsUsageError(err) || out.Len() != 0 {
				t.Fatalf("refusal %v: %v %s", tc.args, err, out.String())
			}
			continue
		}
		if err != nil {
			t.Fatalf("entrypoint %v: %v %s", tc.args, err, stderr.String())
		}
		var got core.ConfigValue
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want, err := c.GetConfig(context.Background(), tc.key)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("entrypoint %v: %+v != %+v %v", tc.args, got, want, err)
		}
	}
	// CLI bootstrap/journal follows existing policy; this is not a HOME no-write assertion.
}
