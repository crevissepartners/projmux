package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTmuxApplyNoReloadMigratesManagedFilesWithoutLiveCalls(t *testing.T) {
	home := t.TempDir()
	codexPath := filepath.Join(home, codexConfigRelativePath)
	legacy := strings.ReplaceAll(codexHooksBlock(true), codexHookCommand, legacyCodexHookCommand)
	writeCodexTestFile(t, codexPath, legacy)
	runner := &recordingTmuxRunner{}
	cmd := managedIngestApplyFixture(home, runner)
	configPath := filepath.Join(home, "tmux.conf")

	if err := cmd.runApply([]string{"--config", configPath, "--socket", "phase0-no-reload", "--no-reload"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("--no-reload live calls = %#v, want zero", runner.calls)
	}
	got := readCodexTestFile(t, codexPath)
	if strings.Contains(got, legacyCodexHookRoute) || !strings.Contains(got, canonicalCodexHookRoute) {
		t.Fatalf("--no-reload did not migrate file:\n%s", got)
	}
}

func TestTmuxApplyMigratesBellOnlyThroughExactSocket(t *testing.T) {
	home := t.TempDir()
	socket := "phase0-exact-socket"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", socket, "list-sessions", "-F", "#{session_id}"):           "$1\n",
		recordedTmuxCallKey("tmux", "-L", socket, "show-hooks", "-g", tmuxBellHookName):             "alert-bell[0] run-shell -b 'echo unmanaged'\nalert-bell[1] " + legacyTmuxBellHookCommand + "\n",
		recordedTmuxCallKey("tmux", "-L", socket, "show-options", "-gqv", "allow-passthrough"):      "on\n",
		recordedTmuxCallKey("tmux", "-L", socket, "show-options", "-gqv", "monitor-bell"):           "on\n",
		recordedTmuxCallKey("tmux", "-L", socket, "show-options", "-gqv", "bell-action"):            "other\n",
		recordedTmuxCallKey("tmux", "-L", socket, "show-options", "-gqv", tmuxSequenceRootsOption):  "",
		recordedTmuxCallKey("tmux", "-L", socket, "show-options", "-gqv", tmuxSequenceTablesOption): "",
	}}
	cmd := managedIngestApplyFixture(home, runner)
	cmd.bindingConverger = func(context.Context, explicitTmuxTarget) error { return nil }
	if err := cmd.runApply([]string{"--config", filepath.Join(home, "tmux.conf"), "--socket", socket}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var bellWrites []recordedTmuxCall
	for _, call := range runner.calls {
		if slices.Contains(call.args, "set-hook") {
			bellWrites = append(bellWrites, call)
			if len(call.args) < 3 || !reflect.DeepEqual(call.args[:2], []string{"-L", socket}) {
				t.Fatalf("bell mutation escaped exact socket: %#v", call)
			}
		}
	}
	if len(bellWrites) != 2 || !slices.Contains(bellWrites[0].args, "alert-bell[1]") || !slices.Contains(bellWrites[1].args, tmuxBellHookCommand) {
		t.Fatalf("bell migration calls = %#v, want legacy remove + canonical append", bellWrites)
	}
}

func TestTmuxApplyBellFailureRollsBackEarlierProviderFilesAndLiveState(t *testing.T) {
	home := t.TempDir()
	codexPath := filepath.Join(home, codexConfigRelativePath)
	originalCodex := strings.ReplaceAll(codexHooksBlock(true), codexHookCommand, legacyCodexHookCommand)
	writeCodexTestFile(t, codexPath, originalCodex)
	runner := &failingBellApplyRunner{
		socket:  "phase0-rollback",
		options: map[string]string{"allow-passthrough": "off", "monitor-bell": "off", "bell-action": "none"},
		hooks:   []string{"run-shell -b 'echo unmanaged'", legacyTmuxBellHookCommand},
		failAt:  5,
	}
	cmd := managedIngestApplyFixture(home, runner)
	err := cmd.runApply([]string{"--config", filepath.Join(home, "tmux.conf"), "--socket", runner.socket}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "injected fifth bell mutation") {
		t.Fatalf("apply error = %v", err)
	}
	if got := readCodexTestFile(t, codexPath); got != originalCodex {
		t.Fatalf("provider file was not rolled back after bell failure:\n%s", got)
	}
	wantOptions := map[string]string{"allow-passthrough": "off", "monitor-bell": "off", "bell-action": "none"}
	if !reflect.DeepEqual(runner.options, wantOptions) {
		t.Fatalf("options after apply rollback = %#v, want %#v", runner.options, wantOptions)
	}
	wantHooks := []string{"run-shell -b 'echo unmanaged'", legacyTmuxBellHookCommand}
	if !reflect.DeepEqual(runner.hooks, wantHooks) {
		t.Fatalf("hooks after apply rollback = %#v, want %#v", runner.hooks, wantHooks)
	}
}

func TestTmuxApplyGeneratedConfigFailureRollsBackManagedFilesAndLiveState(t *testing.T) {
	home := t.TempDir()
	codexPath := filepath.Join(home, codexConfigRelativePath)
	originalCodex := strings.ReplaceAll(codexHooksBlock(true), codexHookCommand, legacyCodexHookCommand)
	writeCodexTestFile(t, codexPath, originalCodex)
	runner := &failingBellApplyRunner{
		socket:  "phase0-generated-config-rollback",
		options: map[string]string{"allow-passthrough": "off", "monitor-bell": "off", "bell-action": "none"},
		hooks:   []string{"run-shell -b 'echo unmanaged'", legacyTmuxBellHookCommand},
	}
	cmd := managedIngestApplyFixture(home, runner)
	configPath := filepath.Join(home, "tmux.conf")
	cmd.writeFile = func(path string, data []byte, mode os.FileMode) error {
		if path == configPath {
			return errors.New("injected generated config write failure")
		}
		return os.WriteFile(path, data, mode)
	}

	err := cmd.runApply([]string{"--config", configPath, "--socket", runner.socket}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "injected generated config write failure") {
		t.Fatalf("apply error = %v", err)
	}
	if got := readCodexTestFile(t, codexPath); got != originalCodex {
		t.Fatalf("provider file was not rolled back after generated config failure:\n%s", got)
	}
	wantOptions := map[string]string{"allow-passthrough": "off", "monitor-bell": "off", "bell-action": "none"}
	if !reflect.DeepEqual(runner.options, wantOptions) {
		t.Fatalf("options after apply rollback = %#v, want %#v", runner.options, wantOptions)
	}
	wantHooks := []string{"run-shell -b 'echo unmanaged'", legacyTmuxBellHookCommand}
	if !reflect.DeepEqual(runner.hooks, wantHooks) {
		t.Fatalf("hooks after apply rollback = %#v, want %#v", runner.hooks, wantHooks)
	}
}

type failingBellApplyRunner struct {
	socket    string
	options   map[string]string
	hooks     []string
	mutations int
	failAt    int
}

func (r *failingBellApplyRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) < 3 || !reflect.DeepEqual(args[:2], []string{"-L", r.socket}) {
		return nil, fmt.Errorf("command escaped exact socket: %s %v", name, args)
	}
	args = args[2:]
	switch args[0] {
	case "list-sessions":
		return []byte("$1\n"), nil
	case "show-options":
		return []byte(r.options[args[len(args)-1]] + "\n"), nil
	case "show-hooks":
		var lines []string
		for i, command := range r.hooks {
			lines = append(lines, fmt.Sprintf("alert-bell[%d] %s", i, command))
		}
		return []byte(strings.Join(lines, "\n") + "\n"), nil
	case "set-option", "set-hook":
		r.mutations++
		if r.failAt > 0 && r.mutations == r.failAt {
			return nil, errors.New("injected fifth bell mutation")
		}
		if args[0] == "set-option" {
			if args[1] == "-gu" {
				delete(r.options, args[2])
			} else {
				r.options[args[2]] = args[3]
			}
			return nil, nil
		}
		if args[1] == "-gu" {
			if args[2] == tmuxBellHookName {
				r.hooks = nil
				return nil, nil
			}
			var index int
			if _, err := fmt.Sscanf(args[2], "alert-bell[%d]", &index); err == nil && index >= 0 && index < len(r.hooks) {
				r.hooks = append(r.hooks[:index], r.hooks[index+1:]...)
			}
			return nil, nil
		}
		r.hooks = append(r.hooks, args[3])
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected command before injected failure: %v", args)
	}
}

func managedIngestApplyFixture(home string, runner tmuxRunner) *tmuxCommand {
	ai := testAICommand(home)
	ai.readFile = os.ReadFile
	ai.executable = func() (string, error) { return "/opt/projmux/bin/projmux", nil }
	return &tmuxCommand{
		ai:         ai,
		executable: ai.executable,
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "HOME":
				return home
			case "XDG_CONFIG_HOME":
				return filepath.Join(home, ".config")
			case "XDG_STATE_HOME":
				return filepath.Join(home, ".state")
			default:
				return ""
			}
		},
		readFile:  os.ReadFile,
		writeFile: os.WriteFile,
		runner:    runner,
	}
}
