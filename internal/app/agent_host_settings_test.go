package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestAgentHostSettingsSameFileAndCancel(t *testing.T) {
	client, cli, paths := settingsClient(t, t.TempDir())
	c := &settingsCommand{ai: testAICommand(t.TempDir()), homeDir: cli.homeDir, lookupEnv: cli.lookupEnv}
	if !hasEntryValue(c.aiRootEntries(), settingsAIAgentHost) || c.currentAgentHost() != "tmux" {
		t.Fatal("default row missing")
	}
	before := settingsFiles(t, paths)
	c.runner, c.nativePicker = scriptedPicker(t, []pickerStep{{reply: intpickercompat.Result{Key: "esc", Value: settingsActionPrefixAgentHost + "process"}}})
	if err := c.runAIAgentHostSection(&bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, errSettingsClosed) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, settingsFiles(t, paths)) {
		t.Fatal("cancel wrote")
	}
	c.runner, c.nativePicker = scriptedPicker(t, []pickerStep{{reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAgentHost + "process"}}, {reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}}})
	if err := c.runAIAgentHostSection(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	v, err := client.GetConfig(context.Background(), core.ConfigAgentHost)
	if err != nil || v.DefaultHost != "process" || c.currentAgentHost() != "process" {
		t.Fatalf("TUI/SDK: %+v %v", v, err)
	}
	out, err := settingsCLI(cli, core.ConfigAgentHost)
	if err != nil || out != "default process\n" {
		t.Fatalf("CLI: %q %v", out, err)
	}
	files := settingsFiles(t, paths)
	if len(files) != 1 || files[config.AgentDefaultHostFileName] != "process\n" {
		t.Fatalf("other settings changed: %+v", files)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	host := "tmux"
	if _, err = client.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigAgentHost, DefaultHost: &host}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, settingsFiles(t, paths)) {
		t.Fatal("canceled SDK wrote")
	}
	v, err = client.SetConfig(context.Background(), core.ConfigUpdate{Key: core.ConfigAgentHost})
	if err != nil || v.DefaultHost != "process" {
		t.Fatal("nil lost value", err)
	}
	if err = os.WriteFile(paths.AgentDefaultHostFile(), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err = client.GetConfig(context.Background(), core.ConfigAgentHost)
	if err != nil || v.DefaultHost != "tmux" {
		t.Fatal("invalid stored fallback", err)
	}
}

func TestAgentHostCLIRefusesBeforeWrite(t *testing.T) {
	_, cli, paths := settingsClient(t, t.TempDir())
	for _, args := range [][]string{{"--default"}, {"--default="}, {"--default", "invalid"}, {"process"}, {"--unknown"}, {"--default", "process", "extra"}} {
		var out, stderr bytes.Buffer
		if err := cli.runAgentHost(args, &out, &stderr); err == nil || !IsUsageError(err) || out.Len() != 0 {
			t.Fatalf("%v: %v stdout %q", args, err, out.String())
		}
		if len(settingsFiles(t, paths)) != 0 {
			t.Fatalf("%v wrote", args)
		}
	}
}
