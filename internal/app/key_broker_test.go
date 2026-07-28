package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/platformkeys"
)

func TestKeyBrokerLoadsCustomPortableChordFromSharedKeymap(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[bindings.ProjectSidebarToggle]\nkeys = [\"M-a\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &keyBrokerCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
		readFile:  os.ReadFile,
	}

	bindings, err := cmd.loadBindings()
	if err != nil {
		t.Fatalf("loadBindings() error = %v", err)
	}
	want := platformkeys.Binding{
		Chord:     "M-a",
		KeyCode:   0,
		Modifiers: platformkeys.ModifierAlt,
	}
	if !containsPlatformBinding(bindings, want) {
		t.Fatalf("loadBindings() = %#v, want custom binding %#v", bindings, want)
	}
	if containsPlatformBinding(bindings, platformkeys.Binding{
		Chord:     "M-1",
		KeyCode:   18,
		Modifiers: platformkeys.ModifierAlt,
	}) {
		t.Fatalf("loadBindings() retained replaced default M-1: %#v", bindings)
	}
}

func TestKeyBrokerFindsFocusedTmuxClient(t *testing.T) {
	runner := &keyBrokerRecordingRunner{
		output: []byte("/dev/ttys001\tattached,UTF-8\n/dev/ttys002\tattached,focused,UTF-8\n"),
	}
	cmd := &keyBrokerCommand{runner: runner}

	client, server, err := cmd.focusedClient(context.Background(), "projmux")
	if err != nil {
		t.Fatalf("focusedClient() error = %v", err)
	}
	if !server || client != "/dev/ttys002" {
		t.Fatalf("focusedClient() = %q, %v, want /dev/ttys002, true", client, server)
	}
	wantArgs := []string{"-L", "projmux", "list-clients", "-F", keyBrokerClientFormat}
	if runner.name != "tmux" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("runner = %s %#v, want tmux %#v", runner.name, runner.args, wantArgs)
	}
}

func TestKeyBrokerInjectsCanonicalChordThroughClientKeyTable(t *testing.T) {
	runner := &keyBrokerRecordingRunner{}
	cmd := &keyBrokerCommand{runner: runner}

	if err := cmd.sendChord(context.Background(), "projmux", "/dev/ttys002", "M-a"); err != nil {
		t.Fatalf("sendChord() error = %v", err)
	}
	wantArgs := []string{"-L", "projmux", "send-keys", "-K", "-c", "/dev/ttys002", "M-a"}
	if runner.name != "tmux" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("runner = %s %#v, want tmux %#v", runner.name, runner.args, wantArgs)
	}
}

func containsPlatformBinding(bindings []platformkeys.Binding, want platformkeys.Binding) bool {
	for _, binding := range bindings {
		if reflect.DeepEqual(binding, want) {
			return true
		}
	}
	return false
}

type keyBrokerRecordingRunner struct {
	name   string
	args   []string
	output []byte
	err    error
}

func (r *keyBrokerRecordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.output, r.err
}
