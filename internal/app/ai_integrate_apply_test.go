package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const (
	pinnedV0101Tag    = "v0.10.1"
	pinnedV0101Commit = "47a7c57dfca6ff31c67833cc87025485711a502d"
)

// v0101GitHubReleasePostReplaceFixture pins the immutable updater branch at
// internal/app/update.go:319-325 in v0.10.1. Replacement happens first; normal
// apply then executes the candidate with exact `tmux apply`, while --no-apply
// returns without executing the candidate at all.
type v0101GitHubReleasePostReplaceFixture struct {
	replaced    bool
	candidate   func([]string, io.Writer, io.Writer) error
	candidateIO [][]string
}

func (f *v0101GitHubReleasePostReplaceFixture) apply(noApply bool, stdout, stderr io.Writer) error {
	f.replaced = true
	if noApply {
		return nil
	}
	args := []string{"tmux", "apply"}
	f.candidateIO = append(f.candidateIO, append([]string(nil), args...))
	return f.candidate(args, stdout, stderr)
}

func TestV0101NormalUpdaterHandoffConvergesThroughCandidateApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROJMUX_CWD", "")
	paths := writeV0101ManagedFileFixture(t, home)
	socket := "projmux"
	physical := "/tmp/tmux-1000/" + socket
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", physical, "list-sessions", "-F", "#{session_id}"):           "$1\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-hooks", "-g", tmuxBellHookName):             "alert-bell[0] " + legacyTmuxBellHookCommand + "\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", "allow-passthrough"):      "off\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", "monitor-bell"):           "off\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", "bell-action"):            "none\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", tmuxSequenceRootsOption):  "",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", tmuxSequenceTablesOption): "",
	}}
	cmd := managedIngestApplyFixture(home, runner)
	cmd.triggerRunner = &recordingTriggering{}
	cmd.writeFile = func(path string, data []byte, mode os.FileMode) error {
		return os.WriteFile(path, data, mode)
	}
	app := &App{tmux: cmd, config: &configCommand{tmux: cmd}}
	fixture := &v0101GitHubReleasePostReplaceFixture{candidate: app.Run}
	var stdout, stderr bytes.Buffer
	if err := fixture.apply(false, &stdout, &stderr); err != nil {
		t.Fatalf("%s (%s) normal post-replace handoff error = %v; stderr=%q", pinnedV0101Tag, pinnedV0101Commit, err, stderr.String())
	}
	if !fixture.replaced || !reflect.DeepEqual(fixture.candidateIO, [][]string{{"tmux", "apply"}}) {
		t.Fatalf("v0.10.1 replacement=%t candidate argv=%#v, want replacement then exact [tmux apply]", fixture.replaced, fixture.candidateIO)
	}
	for _, path := range paths {
		got := readCodexTestFile(t, path)
		if strings.Contains(got, " ai ingest ") || !strings.Contains(got, "internal agent-hook ingest") {
			t.Fatalf("normal handoff did not canonicalize %s:\n%s", path, got)
		}
	}
	if !strings.Contains(stdout.String(), "migrated 4 managed agent hook files to canonical ingest") ||
		!strings.Contains(stdout.String(), "migrated managed tmux bell hook on -L projmux") ||
		!strings.Contains(stdout.String(), "reloaded tmux server -L projmux: 1 sessions") {
		t.Fatalf("normal handoff stdout=%q, want file, bell, and reload convergence", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("normal handoff stderr=%q, want empty", stderr.String())
	}
	var bellMutations []recordedTmuxCall
	for _, call := range runner.calls {
		if slices.Contains(call.args, "set-hook") {
			bellMutations = append(bellMutations, call)
		}
	}
	if len(bellMutations) != 2 || !slices.Contains(bellMutations[1].args, tmuxBellHookCommand) {
		t.Fatalf("normal handoff bell mutations=%#v, want legacy removal then canonical append", bellMutations)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("normal handoff did not write generated config: %v", err)
	}
}

func TestV0101NoApplyIsReplaceOnlyThenExplicitCandidateNoReloadConverges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROJMUX_CWD", "")
	paths := writeV0101ManagedFileFixture(t, home)
	original := make(map[string]string, len(paths))
	for _, path := range paths {
		original[path] = readCodexTestFile(t, path)
	}
	runner := &recordingTmuxRunner{}
	cmd := managedIngestApplyFixture(home, runner)
	app := &App{tmux: cmd, config: &configCommand{tmux: cmd}}
	fixture := &v0101GitHubReleasePostReplaceFixture{candidate: app.Run}

	var stdout, stderr bytes.Buffer
	if err := fixture.apply(true, &stdout, &stderr); err != nil {
		t.Fatalf("%s --no-apply post-replace error = %v", pinnedV0101Tag, err)
	}
	if !fixture.replaced || len(fixture.candidateIO) != 0 {
		t.Fatalf("v0.10.1 --no-apply replacement=%t candidate argv=%#v, want replace-only", fixture.replaced, fixture.candidateIO)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 || len(runner.calls) != 0 {
		t.Fatalf("v0.10.1 --no-apply streams=(%q,%q) live calls=%#v, want zero", stdout.String(), stderr.String(), runner.calls)
	}
	for _, path := range paths {
		if got := readCodexTestFile(t, path); got != original[path] {
			t.Fatalf("v0.10.1 --no-apply changed %s before candidate ran", path)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if err := app.Run([]string{"config", "apply", "--no-reload", "--config", filepath.Join(home, "tmux.conf")}, &stdout, &stderr); err != nil {
		t.Fatalf("explicit candidate config apply --no-reload error = %v; stderr=%q", err, stderr.String())
	}
	if len(runner.calls) != 0 {
		t.Fatalf("explicit candidate --no-reload live calls=%#v, want zero", runner.calls)
	}
	for _, path := range paths {
		got := readCodexTestFile(t, path)
		if strings.Contains(got, " ai ingest ") || !strings.Contains(got, "internal agent-hook ingest") {
			t.Fatalf("explicit candidate --no-reload did not canonicalize %s:\n%s", path, got)
		}
	}
	if !strings.Contains(stdout.String(), "migrated 4 managed agent hook files to canonical ingest") ||
		!strings.Contains(stdout.String(), "skipped reload: --no-reload") || stderr.Len() != 0 {
		t.Fatalf("explicit candidate no-reload streams=(%q,%q), want deterministic migration/skip output", stdout.String(), stderr.String())
	}
}

func writeV0101ManagedFileFixture(t *testing.T, home string) []string {
	t.Helper()
	codexPath := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, codexPath, strings.ReplaceAll(codexHooksBlock(true), codexHookCommand, legacyCodexHookCommand))
	claudePath := filepath.Join(home, claudeSettingsRelativePath)
	legacyClaude := strings.ReplaceAll(claudeHookCommand, canonicalClaudeHookRoute, legacyClaudeHookRoute)
	writeCodexTestFile(t, claudePath, "{\n  \"hooks\": {\n    \"Notification\": [{\"hooks\": [{\"type\": \"command\", \"command\": "+string(mustJSONMarshal(legacyClaude))+"}]}]\n  }\n}\n")
	hooksPath := filepath.Join(home, antigravityHooksRelativePath)
	hooks, err := encodeAntigravityManagedHook("/opt/projmux/bin/projmux")
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, hooksPath, "{\n  \"projmux\": "+strings.ReplaceAll(hooks, antigravityCanonicalIngestPath, antigravityLegacyIngestPath)+"\n}\n")
	statusPath := filepath.Join(home, antigravitySettingsRelativePath)
	status, err := encodeAntigravityManagedStatusLine("/opt/projmux/bin/projmux")
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, statusPath, "{\n  \"statusLine\": "+strings.ReplaceAll(status, antigravityCanonicalIngestPath, antigravityLegacyIngestPath)+"\n}\n")
	return []string{codexPath, claudePath, hooksPath, statusPath}
}

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

func TestConfigApplyNoReloadPreservesClaudeSettingsBytesWithCoordinationAbsentOrPresent(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("coordination-present-%t", present), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PROJMUX_CWD", "")
			runner := &recordingTmuxRunner{}
			cmd := managedIngestApplyFixture(home, runner)
			plan, err := cmd.ai.planClaudeHookIntegrationMode(false, present)
			if err != nil {
				t.Fatal(err)
			}
			writeCodexTestFile(t, plan.path, plan.next)
			before, err := os.ReadFile(plan.path)
			if err != nil {
				t.Fatal(err)
			}
			claudeWrites := 0
			cmd.ai.writeFile = func(path string, data []byte, mode os.FileMode) error {
				if path == plan.path {
					claudeWrites++
				}
				return os.WriteFile(path, data, mode)
			}
			app := &App{tmux: cmd, config: &configCommand{tmux: cmd}}
			var stdout, stderr bytes.Buffer
			if err := app.Run([]string{"config", "apply", "--no-reload", "--config", filepath.Join(home, "tmux.conf")}, &stdout, &stderr); err != nil {
				t.Fatalf("config apply --no-reload error = %v; stderr=%q", err, stderr.String())
			}
			after, err := os.ReadFile(plan.path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) || claudeWrites != 0 {
				t.Fatalf("settings changed: present=%t writes=%d bytesEqual=%t", present, claudeWrites, bytes.Equal(after, before))
			}
			if len(runner.calls) != 0 || !strings.Contains(stdout.String(), "skipped reload: --no-reload") || stderr.Len() != 0 {
				t.Fatalf("apply effects: calls=%#v stdout=%q stderr=%q", runner.calls, stdout.String(), stderr.String())
			}
		})
	}
}

func TestTmuxApplyMigratesBellOnlyThroughExactSocket(t *testing.T) {
	home := t.TempDir()
	socket := "phase0-exact-socket"
	physical := "/tmp/tmux-1000/" + socket
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", physical, "list-sessions", "-F", "#{session_id}"):           "$1\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-hooks", "-g", tmuxBellHookName):             "alert-bell[0] run-shell -b 'echo unmanaged'\nalert-bell[1] " + legacyTmuxBellHookCommand + "\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", "allow-passthrough"):      "on\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", "monitor-bell"):           "on\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", "bell-action"):            "other\n",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", tmuxSequenceRootsOption):  "",
		recordedTmuxCallKey("tmux", "-S", physical, "show-options", "-gqv", tmuxSequenceTablesOption): "",
	}}
	cmd := managedIngestApplyFixture(home, runner)
	cmd.triggerRunner = &recordingTriggering{}
	if err := cmd.runApply([]string{"--config", filepath.Join(home, "tmux.conf"), "--socket", socket}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var bellWrites []recordedTmuxCall
	for _, call := range runner.calls {
		if slices.Contains(call.args, "set-hook") {
			bellWrites = append(bellWrites, call)
			if len(call.args) < 3 || !reflect.DeepEqual(call.args[:2], []string{"-S", physical}) {
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
		t.Fatalf("apply error = %v (bell mutations=%d)", err, runner.mutations)
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
	if name != "tmux" || len(args) < 3 {
		return nil, fmt.Errorf("command escaped exact socket: %s %v", name, args)
	}
	physical := "/tmp/tmux-1000/" + r.socket
	if reflect.DeepEqual(args[:2], []string{"-L", r.socket}) && len(args) == 6 && args[2] == "display-message" && args[5] == "#{socket_path}" {
		return []byte(physical + "\n"), nil
	}
	if !reflect.DeepEqual(args[:2], []string{"-S", physical}) {
		return nil, fmt.Errorf("command escaped exact socket: %s %v", name, args)
	}
	args = args[2:]
	switch args[0] {
	case "display-message":
		if args[len(args)-1] == "#{pid}" {
			return []byte("4242\n"), nil
		}
		return []byte(physical + "\n"), nil
	case "list-sessions":
		return []byte("$1\n"), nil
	case "show-options":
		if args[len(args)-1] == tmuxopts.AppGlobal {
			return []byte("1\n"), nil
		}
		if args[len(args)-1] == runtimeMutationSocketNameOption {
			return []byte(r.socket + "\n"), nil
		}
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
