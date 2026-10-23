package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// TestCentralStatusbarDefaultsSDKParityWithLegacyLoaders is acceptance 1, 4 and
// 5: in every central file state the public read equals the receiver-less app
// loaders the web reads through, leaf by leaf. Where the strict GetConfig read
// succeeds it returns the same states; where the central file cannot be read
// or parsed GetConfig keeps its empty value and error while the public read
// keeps the built-in defaults.
func TestCentralStatusbarDefaultsSDKParityWithLegacyLoaders(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(*testing.T, config.Paths)
		strictErr bool
	}{
		{name: "missing file", setup: func(*testing.T, config.Paths) {}},
		{name: "stored values", setup: func(t *testing.T, p config.Paths) {
			reclaimWrite(t, p.StatusbarDefaultsFile(), `{"visibility":{"clock":"off","notifications-hud":"off","agent-usage-window-codex-5h":"on","agent-usage-provider-claude":"off","settings-launcher":"off"}}`)
		}},
		{name: "invalid leaf value", setup: func(t *testing.T, p config.Paths) {
			reclaimWrite(t, p.StatusbarDefaultsFile(), `{"visibility":{"clock":"maybe","git":"off"}}`)
		}},
		{name: "valid TUI leaves ignored", setup: func(t *testing.T, p config.Paths) {
			for _, leaf := range centralStatusbarLeaves() {
				reclaimWrite(t, leaf.path(p), "off\n")
			}
			reclaimWrite(t, p.StatusbarDefaultsFile(), `{"visibility":{"project":"on"}}`)
		}},
		{name: "malformed json", strictErr: true, setup: func(t *testing.T, p config.Paths) {
			reclaimWrite(t, p.StatusbarDefaultsFile(), `{"visibility":`)
		}},
		{name: "directory read error", strictErr: true, setup: func(t *testing.T, p config.Paths) {
			reclaimMkdir(t, p.StatusbarDefaultsFile())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			homeDir := func() (string, error) { return root, nil }
			lookupEnv := func(string) string { return "" }
			paths, err := configPaths(homeDir, lookupEnv)
			if err != nil {
				t.Fatal(err)
			}
			client, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, paths)

			public := client.GetCentralStatusbarDefaults()
			legacy := map[string]config.StatusbarVisibilityState{}
			for _, leaf := range centralStatusbarLeaves() {
				legacy[centralStatusbarKey(t, leaf.path(paths))] = leaf.central(homeDir, lookupEnv)
			}
			if len(public) != len(legacy) {
				t.Fatalf("public leaves = %d, legacy leaves = %d", len(public), len(legacy))
			}
			for _, got := range public {
				old, ok := legacy[got.Key]
				if !ok {
					t.Fatalf("public key %q has no legacy loader", got.Key)
				}
				want := core.ConfigVisibilityState{Key: got.Key, Effective: string(old.Effective), Saved: old.Saved, Source: string(old.Source), Invalid: old.Invalid}
				if got != want {
					t.Fatalf("%s: public %+v, legacy %+v", got.Key, got, want)
				}
				if tc.strictErr && (got.Source != string(config.StatusbarVisibilitySourceDefault) || got.Saved != "" || got.Invalid != "") {
					t.Fatalf("%s: unreadable central file read as %+v, want the built-in default", got.Key, got)
				}
			}

			strict, err := client.GetConfig(context.Background(), core.ConfigStatusbarVisibility)
			if tc.strictErr {
				if err == nil || !reflect.DeepEqual(strict, core.ConfigValue{}) {
					t.Fatalf("strict read = %+v, %v; want the empty value and the read error", strict, err)
				}
				var refusal *projmux.Refusal
				if errors.As(err, &refusal) {
					t.Fatalf("strict read error became a refusal: %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(strict.Visibility, public) {
				t.Fatalf("strict read = %+v, %v; want %+v", strict.Visibility, err, public)
			}
		})
	}
}

// TestCentralStatusbarDefaultsFollowSetConfig is acceptance 4 for writes: a
// SetConfig patch is what both readers see next, and the TUI loader still
// prefers its own valid leaf.
func TestCentralStatusbarDefaultsFollowSetConfig(t *testing.T) {
	root := t.TempDir()
	homeDir := func() (string, error) { return root, nil }
	lookupEnv := func(string) string { return "" }
	client, _, paths := settingsClient(t, root)
	reclaimWrite(t, paths.StatusbarClockVisibilityFile(), "on\n")
	if _, err := client.SetConfig(context.Background(), core.ConfigUpdate{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"clock": "off", "agent-usage-window-codex-5h": "on"}}); err != nil {
		t.Fatal(err)
	}
	got := map[string]core.ConfigVisibilityState{}
	for _, state := range client.GetCentralStatusbarDefaults() {
		got[state.Key] = state
	}
	if want := (core.ConfigVisibilityState{Key: "clock", Effective: "off", Saved: "off", Source: "central"}); got["clock"] != want {
		t.Fatalf("clock = %+v, want %+v", got["clock"], want)
	}
	if want := (core.ConfigVisibilityState{Key: "agent-usage-window-codex-5h", Effective: "on", Saved: "on", Source: "central"}); got["agent-usage-window-codex-5h"] != want {
		t.Fatalf("codex 5h = %+v, want %+v", got["agent-usage-window-codex-5h"], want)
	}
	if old := loadCentralStatusbarRowOneVisibility(homeDir, lookupEnv, statusbarRowOneClock); old.Effective != config.StatusbarVisibilityOff || old.Source != config.StatusbarVisibilitySourceCentral {
		t.Fatalf("legacy central clock = %+v", old)
	}
	if tui := loadStatusbarRowOneVisibilityState(homeDir, lookupEnv, statusbarRowOneClock); tui.Effective != config.StatusbarVisibilityOn {
		t.Fatalf("TUI clock = %+v, want its own on", tui)
	}
}
