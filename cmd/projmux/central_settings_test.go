package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestCentralSettingsMainEntrypoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config", "projmux"), StateDir: filepath.Join(root, "state", "projmux")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		route string
		key   core.ConfigKey
		args  []string
		code  int
	}{
		{"resources", core.ConfigStatusbarResources, nil, 0},
		{"resources", core.ConfigStatusbarResources, []string{"--set", "on"}, 0},
		{"ai-default-mode", core.ConfigAIDefaultMode, []string{"--set", "shell"}, 0},
		{"statusbar-visibility", core.ConfigStatusbarVisibility, []string{"--key", "clock", "--set", "off"}, 0},
		{"resources", core.ConfigStatusbarResources, []string{"--set", "invalid"}, 2},
		{"ai-default-mode", core.ConfigAIDefaultMode, []string{"--set", "invalid"}, 2},
		{"statusbar-visibility", core.ConfigStatusbarVisibility, []string{"--key", "settings-launcher", "--set", "on"}, 2},
		{"resources", core.ConfigStatusbarResources, []string{"--unknown"}, 2},
	} {
		argv := append([]string{"config", tc.route}, tc.args...)
		store := diagnostics.NewStore(filepath.Join(t.TempDir(), "diagnostics.jsonl"))
		recorder := diagnostics.NewLifecycleRecorder(store, "central-test", "test", "tmux")
		var out, stderr bytes.Buffer
		code := executeCLI(func() error { return app.RunWithLifecycleDiagnostics(argv, &out, &stderr, recorder) }, func(err error) {
			if e := diagnostics.RecordOutcome(store, argv, "central-test", "test", "tmux", time.Now(), err, app.IsUsageError(err), recorder.RecordedOutcome()); e != nil {
				t.Fatal(e)
			}
		}, &stderr)
		if code != tc.code {
			t.Fatalf("%v exit %d stderr %s", argv, code, stderr.String())
		}
		events, err := store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if code == 2 {
			if out.Len() != 0 || len(events) != 1 || events[0].Kind != "usage" || events[0].Command != "config" || events[0].Subcommand != tc.route {
				t.Fatalf("main refusal %v: %s %+v", argv, out.String(), events)
			}
		} else {
			var got core.ConfigValue
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want, err := c.GetConfig(context.Background(), tc.key)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("main/SDK %+v %+v %v", got, want, err)
			}
		}
	}
}
