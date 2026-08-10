package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/platformkeys"
)

func TestKeyBrokerNativeKeysOptOutReturnsBeforeSourceSetup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     string
		setting bool
	}{
		{name: "environment", env: "false"},
		{name: "settings", setting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.setting {
				writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"),
					"[ui]\nnative_keys = false\n")
			}
			source := &keyBrokerSourceRecorder{}
			cmd := &keyBrokerCommand{
				source:     source,
				nativeKeys: func() bool { return true },
				homeDir:    func() (string, error) { return home, nil },
				lookupEnv: func(name string) string {
					if name == nativeKeysEnvName {
						return tc.env
					}
					return ""
				},
			}

			if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if source.replaceCalls != 0 || source.enabledCalls != 0 || source.runCalls != 0 {
				t.Fatalf("source calls = replace:%d enabled:%d run:%d, want direct no-op",
					source.replaceCalls, source.enabledCalls, source.runCalls)
			}
		})
	}
}

func TestNativeKeyConsentHintIsShownOnlyOnce(t *testing.T) {
	home := t.TempDir()
	stateHome := filepath.Join(home, "state")
	lookupEnv := func(name string) string {
		if name == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	homeDir := func() (string, error) { return home, nil }
	var stderr bytes.Buffer

	showNativeKeysConsentHint(&stderr, lookupEnv, homeDir, os.ReadFile, os.WriteFile)
	first := stderr.String()
	for _, want := range []string{
		"modified chords only",
		"never plain-text typing",
		"physical Option",
		"stay local",
		"Settings > Keybindings",
		"PROJMUX_NATIVE_KEYS=0",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("first hint = %q, want %q", first, want)
		}
	}

	showNativeKeysConsentHint(&stderr, lookupEnv, homeDir, os.ReadFile, os.WriteFile)
	if got := stderr.String(); got != first {
		t.Fatalf("second hint output = %q, want unchanged %q", got, first)
	}
	path, err := nativeKeysConsentHintPath(lookupEnv, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("hint state mode = %o, want 600", got)
	}
}

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

type keyBrokerSourceRecorder struct {
	replaceCalls int
	enabledCalls int
	runCalls     int
	runErr       error
}

func (s *keyBrokerSourceRecorder) Replace([]platformkeys.Binding) error {
	s.replaceCalls++
	return nil
}

func (s *keyBrokerSourceRecorder) SetEnabled(bool) {
	s.enabledCalls++
}

func (s *keyBrokerSourceRecorder) Ready() <-chan struct{} {
	return nil
}

func (s *keyBrokerSourceRecorder) Events() <-chan string {
	return nil
}

func (s *keyBrokerSourceRecorder) Run(context.Context) error {
	s.runCalls++
	return s.runErr
}

func (r *keyBrokerRecordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.output, r.err
}
