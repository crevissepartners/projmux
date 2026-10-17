package projmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type centralFileSnapshot struct {
	Bytes string
	Mode  fs.FileMode
	Mtime time.Time
}

func centralTree(t *testing.T, root string) map[string]centralFileSnapshot {
	t.Helper()
	out := map[string]centralFileSnapshot{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		b := []byte{}
		if !d.IsDir() {
			b, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		out[rel] = centralFileSnapshot{string(b), info.Mode(), info.ModTime()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCentralSettingsSDKContract(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	stateDir := filepath.Join(root, "state")
	c, err := projmux.NewClient(projmux.Options{ConfigDir: configDir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keys := []core.ConfigKey{core.ConfigStatusbarResources, core.ConfigAIDefaultMode, core.ConfigStatusbarVisibility}
	before := centralTree(t, root)
	for _, key := range keys {
		v, err := c.GetConfig(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		switch key {
		case core.ConfigStatusbarResources:
			if v.Resources == nil || *v.Resources != (core.ConfigToggleState{Effective: "off", Source: "default"}) {
				t.Fatalf("resources %+v", v)
			}
		case core.ConfigAIDefaultMode:
			if v.DefaultMode != "selective" {
				t.Fatal(v)
			}
		default:
			if len(v.Visibility) != 12 {
				t.Fatal(v)
			}
			for i, v := range v.Visibility {
				want := "on"
				if v.Key == "agent-usage-window-codex-5h" {
					want = "off"
				}
				if v.Effective != want || v.Source != "default" || v.Saved != "" || v.Invalid != "" {
					t.Fatal(v)
				}
				if i > 0 {
					prev, _ := c.GetConfig(ctx, key)
					if prev.Visibility[i-1].Key >= v.Key {
						t.Fatal("not sorted")
					}
				}
			}
		}
	}
	if !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("read wrote files")
	}
	on, mode := "on", "resume"
	for _, u := range []core.ConfigUpdate{{Key: keys[0], Resources: &on}, {Key: keys[1], DefaultMode: &mode}, {Key: keys[2], Visibility: map[string]string{"clock": "off", "agent-usage-window-codex-5h": "on"}}} {
		v, err := c.SetConfig(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.GetConfig(ctx, u.Key)
		if err != nil || !reflect.DeepEqual(v, got) {
			t.Fatalf("read/write %+v %+v %v", v, got, err)
		}
		v, err = c.SetConfig(ctx, core.ConfigUpdate{Key: u.Key})
		if err != nil || !reflect.DeepEqual(v, got) {
			t.Fatalf("nil lost value %+v %v", v, err)
		}
	}
	if err := os.WriteFile(filepath.Join(configDir, "live-resources"), []byte("nonsense\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	v, err := c.GetConfig(ctx, keys[0])
	if err != nil || *v.Resources != (core.ConfigToggleState{Effective: "off", Source: "default", Invalid: "nonsense"}) {
		t.Fatalf("invalid stored %+v %v", v, err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "ai-new-window-mode"), []byte("nonsense\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	v, err = c.GetConfig(ctx, keys[1])
	if err != nil || v.DefaultMode != "selective" {
		t.Fatal(v, err)
	}
}
func TestCentralSettingsSDKPatchAndEffects(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	stateDir := filepath.Join(root, "state")
	for _, dir := range []string{configDir, stateDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"statusbar-defaults.json":    `{"future":{"n":7},"visibility":{"clock":"off","git":"invalid","future-leaf":"alien","project":"on"}}`,
		"statusbar-visibility-clock": "on\n", "web.toml": "[statusbar]\nclock = true\n", "registry.json": "broken registry",
	} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sentinel"), []byte("state"), 0o640); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: configDir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before := centralTree(t, root)
	bad, good := "bogus", "on"
	for _, u := range []core.ConfigUpdate{
		{Key: "unknown"}, {Key: core.ConfigStatusbarResources, Resources: &bad}, {Key: core.ConfigAIDefaultMode, DefaultMode: &bad},
		{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"clock": "on", "git": "bad"}},
		{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"clock": "on", "settings-launcher": "off"}},
		{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"future-leaf": "on"}},
	} {
		_, err := c.SetConfig(ctx, u)
		var r *projmux.Refusal
		want := projmux.CodeConfigValueInvalid
		if u.Key == "unknown" || u.Visibility["settings-launcher"] != "" || u.Visibility["future-leaf"] != "" {
			want = projmux.CodeConfigKeyInvalid
		}
		if !errors.As(err, &r) || r.Code != want {
			t.Fatalf("%+v: %v", u, err)
		}
		if !reflect.DeepEqual(before, centralTree(t, root)) {
			t.Fatalf("invalid wrote %+v", u)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, u := range []core.ConfigUpdate{{Key: core.ConfigStatusbarResources, Resources: &good}, {Key: core.ConfigAIDefaultMode, DefaultMode: &bad}, {Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"clock": "on"}}} {
		if _, err := c.SetConfig(canceled, u); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := c.GetConfig(canceled, u.Key); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, centralTree(t, root)) {
			t.Fatal("cancel wrote")
		}
	}
	v, err := c.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"clock": "on"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, leaf := range v.Visibility {
		if leaf.Key == "git" && (leaf.Effective != "on" || leaf.Source != "default" || leaf.Invalid != "" || leaf.Saved != "") {
			t.Fatal(leaf)
		}
	}
	after := centralTree(t, root)
	for path, want := range before {
		if path != "." && path != "config" && path != "config/statusbar-defaults.json" && after[path] != want {
			t.Fatalf("other path changed %s", path)
		}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(after["config/statusbar-defaults.json"].Bytes), &raw); err != nil {
		t.Fatal(err)
	}
	var leaves map[string]string
	if err := json.Unmarshal(raw["visibility"], &leaves); err != nil {
		t.Fatal(err)
	}
	var future struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(raw["future"], &future); err != nil {
		t.Fatal(err)
	}
	if future.N != 7 || leaves["future-leaf"] != "alien" || leaves["git"] != "invalid" || leaves["project"] != "on" || leaves["clock"] != "on" {
		t.Fatalf("lost fields %s", after["config/statusbar-defaults.json"].Bytes)
	}
	// A broken document cannot be partially repaired as a side effect of a patch.
	for _, broken := range []string{`{`, `{"visibility":{"clock":{"bad":true}}}`} {
		if err := os.WriteFile(filepath.Join(configDir, "statusbar-defaults.json"), []byte(broken), 0o640); err != nil {
			t.Fatal(err)
		}
		before := centralTree(t, root)
		if _, err := c.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"project": "off"}}); err == nil {
			t.Fatal("malformed document accepted")
		}
		if !reflect.DeepEqual(before, centralTree(t, root)) {
			t.Fatal("malformed document error wrote")
		}
	}

}
func TestCentralSettingsLegacyJSON(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []core.ConfigKey{core.ConfigProviders, core.ConfigLocale, core.ConfigAgentQuestions, core.ConfigAgentApprovals, core.ConfigAgentHost} {
		v, err := c.GetConfig(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		// The complete pre-extension JSON shape and order remain unchanged.
		legacy := struct {
			Key                                            core.ConfigKey
			Providers                                      []core.ProviderState
			Locale, Source, Answering, Window, DefaultHost string
		}{v.Key, v.Providers, v.Locale, v.Source, v.Answering, v.Window, v.DefaultHost}
		got, _ := json.Marshal(v)
		want, _ := json.Marshal(legacy)
		if string(got) != string(want) {
			t.Fatalf("legacy %s: %s != %s", key, got, want)
		}
	}
}
