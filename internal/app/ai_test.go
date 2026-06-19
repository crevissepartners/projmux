package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aiprovider"
	intpsmux "github.com/crevissepartners/projmux/internal/integrations/psmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestAISettingsGetAndSetMode(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings", "--get"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --get error = %v", err)
	}
	if got, want := stdout.String(), "selective\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	if err := cmd.Run([]string{"settings", "--set", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --set error = %v", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no tmux toast outside tmux", cmdRecorder(cmd).commands)
	}
	stdout.Reset()
	if err := cmd.Run([]string{"settings", "--get"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --get after set error = %v", err)
	}
	if got, want := stdout.String(), "codex\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAISettingsSetModeDisplaysTmuxToastInsideTmux(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}

	if err := cmd.Run([]string{"settings", "--set", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --set error = %v", err)
	}

	want := []recordedAICommand{{name: "tmux", args: []string{"display-message", "ai split default: codex"}}}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISettingsPickerSetsSelectedMode(t *testing.T) {
	home := t.TempDir()
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: "shell"}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if err := cmd.Run([]string{"settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings picker error = %v", err)
	}
	if got, want := runner.options.UI, "ai-settings"; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := runner.options.Title, "AI Settings - Default split mode"; got != want {
		t.Fatalf("runner title = %q, want %q", got, want)
	}
	if got, want := runner.options.Prompt, "AI Setting > "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got := runner.options.Header; got != "" {
		t.Fatalf("runner header = %q, want description only in title", got)
	}
	if got, want := runner.options.Footer, "Choose the default split mode for future AI launches."; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := readModeFile(t, home), "shell\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
}

func TestAISplitPickerCloseBindingsUseAISplitPickerAlias(t *testing.T) {
	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte(`[bindings.AISplitPickerToggle]
keys = ["M-a"]
[bindings.SettingsToggle]
keys = ["M-s"]
[bindings.new-window]
keys = ["M-t"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker() error = %v", err)
	}
	if !containsString(runner.options.Bindings, "alt-a:abort") {
		t.Fatalf("AI picker bindings = %#v, want custom AISplitPickerToggle alias close", runner.options.Bindings)
	}
	if containsString(runner.options.Bindings, "alt-s:abort") {
		t.Fatalf("AI picker bindings = %#v, SettingsToggle alias must not close AI picker", runner.options.Bindings)
	}
	if containsString(runner.options.Bindings, "alt-t:abort") {
		t.Fatalf("AI picker bindings = %#v, direct command alias must not close popup", runner.options.Bindings)
	}
}

func TestAISettingsRowsHideDisabledAgentDefaults(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatalf("setMode(codex) error = %v", err)
	}

	rows := cmd.settingsRows()
	if hasEntryValue(rows, aiModeCodex) {
		t.Fatalf("settings rows = %#v, want disabled Codex hidden", rows)
	}
	for _, want := range []string{aiModeSelective, aiModeClaude, aiModeShell} {
		if !hasEntryValue(rows, want) {
			t.Fatalf("settings rows = %#v, want row %q", rows, want)
		}
	}
	if !hasEntryLabelContainingAll(rows, "saved default codex is disabled", "Enabled agents") {
		t.Fatalf("settings rows = %#v, want disabled default warning", rows)
	}
}

func TestAIPickerShowsKeyFooter(t *testing.T) {
	home := t.TempDir()
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker error = %v", err)
	}
	if got, want := runner.options.UI, "ai-picker"; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := runner.options.Title, "AI Launch - Split direction: right"; got != want {
		t.Fatalf("runner title = %q, want %q", got, want)
	}
	if got := runner.options.Header; got != "" {
		t.Fatalf("runner header = %q, want direction only in title", got)
	}
	if got, want := entryValues(runner.options.Entries), []string{aiModeCodex, aiModeClaude, aiModeAntigravity, aiModeShell}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner entry order = %#v, want %#v", got, want)
	}
	for _, entry := range runner.options.Entries {
		if strings.TrimSpace(entry.SearchKey) == "" {
			t.Fatalf("runner entry %#v has empty SearchKey; want stable search-order filtering", entry)
		}
	}
	if got, want := runner.options.Footer, "Choose an agent or shell target to launch."; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
}

func TestAIPickerFiltersDisabledAgents(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker error = %v", err)
	}
	if got, want := entryValues(runner.options.Entries), []string{aiModeClaude, aiModeShell}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner entry order = %#v, want %#v", got, want)
	}
	if hasEntryValue(runner.options.Entries, aiModeCodex) {
		t.Fatalf("runner entries = %#v, want disabled Codex hidden", runner.options.Entries)
	}
}

func TestAIProviderPickerRowsDeriveFromRegistryAndHideDisabledProviders(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := testAICommand(home)

	rows := cmd.agentRows()
	if got, want := entryValues(rows), []string{string(aiprovider.Claude), aiModeShell}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agentRows values = %#v, want enabled registry providers plus shell %#v", got, want)
	}
	if hasEntryValue(rows, string(aiprovider.Codex)) {
		t.Fatalf("agentRows = %#v, want disabled Codex hidden", rows)
	}
	if hasEntryValue(rows, string(aiprovider.Antigravity)) {
		t.Fatalf("agentRows = %#v, want disabled Antigravity hidden", rows)
	}
}

func TestAIPickerAllAgentsDisabledShowsShellFallbackGuidance(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), nil); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)

	if _, err := cmd.runAgentPicker("down"); err != nil {
		t.Fatalf("runAgentPicker error = %v", err)
	}
	if got, want := entryValues(runner.options.Entries), []string{"", aiModeShell}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runner entry order = %#v, want guidance plus shell fallback %#v", got, want)
	}
	if !hasEntryLabelContainingAll(runner.options.Entries, "AI agents disabled", "shell") {
		t.Fatalf("runner entries = %#v, want disabled-agent guidance", runner.options.Entries)
	}
	if hasEntryValue(runner.options.Entries, aiModeClaude) || hasEntryValue(runner.options.Entries, aiModeCodex) || hasEntryValue(runner.options.Entries, aiModeAntigravity) {
		t.Fatalf("runner entries = %#v, want all AI agents hidden", runner.options.Entries)
	}
}

func TestAIPickerMarksAgentReadyWhenBinaryExistsWithoutLegacyWrapper(t *testing.T) {
	home := t.TempDir()
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "claude"}) {
			return []byte(claudeBin + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	rows := cmd.agentRows()
	if len(rows) < 2 {
		t.Fatalf("agentRows len = %d, want at least 2", len(rows))
	}
	for _, row := range rows[:2] {
		if !strings.Contains(row.Label, "[READY]") {
			t.Fatalf("row label = %q, want READY without legacy wrapper", row.Label)
		}
	}
}

func TestFindAgentBinaryDiscoversNodeManagerInstalls(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		binPath string
	}{
		{"codex via nvm", aiModeCodex, filepath.Join(".nvm", "versions", "node", "v24.15.0", "bin", "codex")},
		{"claude via nvm", aiModeClaude, filepath.Join(".nvm", "versions", "node", "v22.0.0", "bin", "claude")},
		{"antigravity via nvm", aiModeAntigravity, filepath.Join(".nvm", "versions", "node", "v24.15.0", "bin", "agy")},
		{"codex via fnm", aiModeCodex, filepath.Join(".fnm", "node-versions", "v22.4.0", "installation", "bin", "codex")},
		{"codex via asdf", aiModeCodex, filepath.Join(".asdf", "installs", "nodejs", "20.10.0", "bin", "codex")},
		{"claude via volta", aiModeClaude, filepath.Join(".volta", "bin", "claude")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			absBin := writeExecutable(t, filepath.Join(home, tc.binPath))
			cmd := testAICommand(home)

			got := cmd.findAgentBinary(tc.mode)
			if got != absBin {
				t.Fatalf("findAgentBinary(%q) = %q, want %q", tc.mode, got, absBin)
			}
			if !cmd.agentAvailable(tc.mode) {
				t.Fatalf("agentAvailable(%q) = false, want true", tc.mode)
			}
		})
	}
}

func TestFindAgentBinaryPrefersPathOverNodeManager(t *testing.T) {
	home := t.TempDir()
	pathBin := writeExecutable(t, filepath.Join(home, "system-bin", "codex"))
	nvmBin := writeExecutable(t, filepath.Join(home, ".nvm", "versions", "node", "v24.15.0", "bin", "codex"))
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(pathBin + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	got := cmd.findAgentBinary(aiModeCodex)
	if got != pathBin {
		t.Fatalf("findAgentBinary = %q, want PATH hit %q (nvm fallback was %q)", got, pathBin, nvmBin)
	}
}

func TestFindAgentBinaryPSMuxCodexPrefersPowerShellPS1Shim(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "npm-bin")
	codexPS1 := writeExecutable(t, filepath.Join(binDir, "codex.ps1"))
	writeExecutable(t, filepath.Join(binDir, "codex.cmd"))
	writeExecutable(t, filepath.Join(binDir, "codex.exe"))
	writeExecutable(t, filepath.Join(binDir, "codex"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case muxBackendEnvVar:
			return string(muxBackendPSMux)
		case "PATH":
			return binDir
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "powershell" && len(args) == 3 && args[0] == "-NoProfile" && args[1] == "-Command" && strings.Contains(args[2], "Get-Command") {
			return []byte(codexPS1 + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	got := cmd.findAgentBinary(aiModeCodex)
	if got != codexPS1 {
		t.Fatalf("findAgentBinary = %q, want PowerShell ps1 shim %q", got, codexPS1)
	}
	if containsRecordedAICommandPrefix(cmdRecorder(cmd).commands, recordedAICommand{name: "command", args: []string{"-v", "codex"}}) {
		t.Fatalf("commands = %#v, did not expect POSIX command -v lookup on psmux", cmdRecorder(cmd).commands)
	}
}

func TestFindAgentBinaryPSMuxCodexRecognizesWindowsShimCandidates(t *testing.T) {
	cases := []struct {
		name     string
		fileName string
	}{
		{"cmd shim", "codex.cmd"},
		{"ps1 shim without PATHEXT", "codex.ps1"},
		{"exe shim", "codex.exe"},
		{"extensionless shim", "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			binDir := filepath.Join(home, "npm-bin")
			want := writeExecutable(t, filepath.Join(binDir, tc.fileName))
			cmd := testAICommand(home)
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "HOME":
					return home
				case muxBackendEnvVar:
					return string(muxBackendPSMux)
				case "PATH":
					return binDir
				default:
					return ""
				}
			}
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
				return nil, os.ErrNotExist
			}

			got := cmd.findAgentBinary(aiModeCodex)
			if got != want {
				t.Fatalf("findAgentBinary = %q, want %q", got, want)
			}
		})
	}
}

func TestFindAgentBinaryPSMuxCodexUsesWhereFallback(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "npm-bin")
	extensionless := writeExecutable(t, filepath.Join(binDir, "codex"))
	cmdShim := writeExecutable(t, filepath.Join(binDir, "codex.cmd"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case muxBackendEnvVar:
			return string(muxBackendPSMux)
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "where.exe" && reflect.DeepEqual(args, []string{"codex"}) {
			return []byte(extensionless + "\n" + cmdShim + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	got := cmd.findAgentBinary(aiModeCodex)
	if got != cmdShim {
		t.Fatalf("findAgentBinary = %q, want where.exe cmd shim %q over extensionless %q", got, cmdShim, extensionless)
	}
}

func TestFindAgentBinaryPicksNewestNvmVersion(t *testing.T) {
	home := t.TempDir()
	older := writeExecutable(t, filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin", "codex"))
	newer := writeExecutable(t, filepath.Join(home, ".nvm", "versions", "node", "v24.15.0", "bin", "codex"))
	past := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	cmd := testAICommand(home)
	got := cmd.findAgentBinary(aiModeCodex)
	if got != newer {
		t.Fatalf("findAgentBinary = %q, want newest %q (older candidate %q)", got, newer, older)
	}
}

func TestFindAgentBinaryReturnsEmptyWhenAbsent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	if got := cmd.findAgentBinary(aiModeCodex); got != "" {
		t.Fatalf("findAgentBinary on empty home = %q, want empty", got)
	}
	if cmd.agentAvailable(aiModeCodex) {
		t.Fatalf("agentAvailable on empty home = true, want false")
	}
}

func TestAISplitMissingRunnerPreservesTmuxMessage(t *testing.T) {
	cmd := testAICommand(t.TempDir())

	err := cmd.Run([]string{"split", "--agent", "codex", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "selected runner is not installed: codex" {
		t.Fatalf("Run split missing codex error = %v, want selected runner is not installed: codex", err)
	}
}

func TestAISplitMissingAntigravityRunnerReportsMode(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		return nil, os.ErrNotExist
	}

	err := cmd.Run([]string{"split", "--agent", "antigravity", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "selected runner is not installed: antigravity" {
		t.Fatalf("Run split missing antigravity error = %v, want selected runner is not installed: antigravity", err)
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "command", []string{"-v", "agy"}) {
		t.Fatalf("commands = %#v, want agy lookup", cmdRecorder(cmd).commands)
	}
}

func TestAISplitDirectDisabledAgentFailsBeforeRunnerLookup(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentCodex}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := testAICommand(home)
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("disabled direct agent should fail before command lookup")
		return nil, os.ErrNotExist
	}

	err := cmd.Run([]string{"split", "--agent", "claude", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run split --agent claude error = nil, want disabled-agent error")
	}
	for _, want := range []string{"AI agent claude is disabled", "Settings > AI Settings > Enabled agents", "--force-agent"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want none", cmdRecorder(cmd).commands)
	}
}

func TestAISplitDefaultDisabledAgentFailsClearly(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatalf("setMode(codex) error = %v", err)
	}
	cmdRecorder(cmd).commands = nil

	err := cmd.Run([]string{"split", "down"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run split with disabled default error = nil, want disabled-default error")
	}
	for _, want := range []string{"AI split default codex is disabled", "choose another default", "--agent shell"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no launch commands", cmdRecorder(cmd).commands)
	}
}

func TestAISplitDisabledConcreteAgentFailsBeforeRunnerFocusOrSplit(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		agent      string
		enabled    []config.AIAgentProvider
		defaultSet bool
		want       []string
	}{
		{
			name:    "direct",
			args:    []string{"split", "--agent", "claude", "right"},
			agent:   aiModeClaude,
			enabled: []config.AIAgentProvider{config.AIAgentCodex},
			want:    []string{"AI agent claude is disabled", "--force-agent"},
		},
		{
			name:       "default",
			args:       []string{"split", "right"},
			agent:      aiModeCodex,
			enabled:    []config.AIAgentProvider{config.AIAgentClaude},
			defaultSet: true,
			want:       []string{"AI split default codex is disabled", "choose another default"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "repo")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), tt.enabled); err != nil {
				t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
			}
			cmd := testAICommand(home)
			if tt.defaultSet {
				if err := cmd.setMode(tt.agent); err != nil {
					t.Fatalf("setMode(%s) error = %v", tt.agent, err)
				}
				cmdRecorder(cmd).commands = nil
			}
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "HOME":
					return home
				case "TMUX":
					return "/tmp/tmux"
				default:
					return ""
				}
			}
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
				if name != "tmux" {
					return nil, os.ErrNotExist
				}
				switch {
				case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
					return []byte("%1\n"), nil
				case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
					return []byte(work + "\n"), nil
				}
				return nil, os.ErrNotExist
			}

			err := cmd.Run(tt.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Run(%v) error = nil, want disabled-agent error", tt.args)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want substring %q", err.Error(), want)
				}
			}
			commands := cmdRecorder(cmd).commands
			for _, forbidden := range [][]string{
				{"select-pane", "-t", "%2"},
				{"split-window"},
			} {
				if containsAICommandArgs(commands, "tmux", forbidden) {
					t.Fatalf("commands = %#v, disabled agent must fail before %v", commands, forbidden)
				}
			}
		})
	}
}

func TestAISplitForceAgentOverridesDisabledDirectOnly(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), nil); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", "%9")

	if err := cmd.Run([]string{"split", "--agent", "codex", "--force-agent", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --force-agent error = %v", err)
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex}) {
		t.Fatalf("commands = %#v, want forced Codex launch metadata", cmdRecorder(cmd).commands)
	}
}

func TestAISplitSelectiveDelegatesToPopupToggle(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux bin", nil }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{client_tty}"}) {
			return []byte("/dev/pts/7\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split error = %v", err)
	}

	want := []recordedAICommand{{
		name: "/tmp/projmux bin",
		args: []string{"tmux", "popup-toggle", "--client", "/dev/pts/7", "ai-split-picker-right"},
	}}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISplitDirectAlwaysCreatesNewPaneWithoutReuseProbe(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte("%1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte("%9\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", "%1", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte("%1\t0\t0\t40\t10\n%2\t41\t0\t40\t10\n%9\t82\t0\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "codex", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%1", "-c", work, "/bin/sh", "-lc"}) {
		t.Fatalf("commands = %#v, want new Codex split-window", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex}) {
		t.Fatalf("commands = %#v, want new pane Codex metadata", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"select-pane", "-t", "%2"}) {
		t.Fatalf("commands = %#v, direct split must not select preexisting AI pane", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"list-panes", "-s", "-t", "%1"}) {
		t.Fatalf("commands = %#v, direct split must not probe existing AI panes for reuse", commands)
	}
}

func TestAISplitDefaultAlwaysCreatesNewPaneWithoutReuseProbe(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte("%1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte("%9\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", "%1", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte("%1\t0\t0\t40\t10\n%2\t0\t11\t40\t10\n%9\t0\t22\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split default codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%1", "-c", work, "/bin/sh", "-lc"}) {
		t.Fatalf("commands = %#v, want new default Codex split-window", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"select-pane", "-t", "%2"}) {
		t.Fatalf("commands = %#v, default split must not select preexisting AI pane", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"list-panes", "-s", "-t", "%1"}) {
		t.Fatalf("commands = %#v, default split must not probe existing AI panes for reuse", commands)
	}
}

func TestAISplitDirectFromCurrentAIPaneStillCreatesNewPane(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte("%2\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte("%10\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", "%2", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte("%1\t0\t0\t40\t10\n%2\t41\t0\t40\t10\n%10\t82\t0\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "codex", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%2", "-c", work, "/bin/sh", "-lc"}) {
		t.Fatalf("commands = %#v, want new Codex split from current AI pane", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%10", aiPaneAgentOption, aiModeCodex}) {
		t.Fatalf("commands = %#v, want new pane Codex metadata", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"select-pane", "-t", "%1"}) {
		t.Fatalf("commands = %#v, direct split from AI pane must not select previous pane", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"list-panes", "-s", "-t", "%2"}) {
		t.Fatalf("commands = %#v, direct split from AI pane must not probe existing AI panes for reuse", commands)
	}
}

func TestAISplitPickerSelectionPreservesLaunchPathWithExistingManagedPane(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiModeCodex}}
	cmd := testAICommand(home)
	cmd.runner = runner
	cmd.nativePicker = nativePickerFromCompatRunner(runner)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte("%1\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte("%9\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", "%1", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte("%1\t0\t0\t40\t10\n%9\t41\t0\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run picker --inside error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%1", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want picker-selected Codex split-window", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"select-pane", "-t", "%2"}) {
		t.Fatalf("commands = %#v, picker path must not select preexisting AI pane", commands)
	}
}

func TestAISplitCodexRunsNativeTmuxSplitAndStartsWatcher(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	if err := cmd.setMode("codex"); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}) {
			return []byte("%7\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}) {
			return []byte(work + "\n"), nil
		}
		if name == "tmux" && len(args) >= 6 && reflect.DeepEqual(args[:6], []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t"}) {
			return []byte("%9\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-t", "%7", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}) {
			return []byte("%2\t0\t0\t20\t10\n%7\t21\t0\t10\t10\n%9\t32\t0\t10\t10\n%8\t0\t11\t42\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%7", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want native tmux split-window", commands)
	}
	for _, want := range [][]string{
		{"resize-pane", "-t", "%2", "-x", "14"},
		{"resize-pane", "-t", "%7", "-x", "13"},
		{"resize-pane", "-t", "%9", "-x", "13"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want scoped row resize %v", commands, want)
		}
	}
	if containsAICommandArgs(commands, "tmux", []string{"resize-pane", "-t", "%8", "-x", "13"}) {
		t.Fatalf("commands = %#v, did not expect resize outside target row", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"run-shell", "-b", "'/tmp/projmux' ai watch-title '%9'"}) {
		t.Fatalf("commands = %#v, want codex watch-title run-shell", commands)
	}
	for _, want := range [][]string{
		{"set-option", "-p", "-t", "%9", "@projmux_ai_managed", "1"},
		{"set-option", "-p", "-t", "%9", "@projmux_ai_agent", "codex"},
		{"set-option", "-p", "-t", "%9", "@projmux_ai_context", work},
		{"set-option", "-p", "-t", "%9", "@projmux_ai_topic", "repo"},
		{"set-option", "-p", "-t", "%9", "@projmux_ai_state", "idle"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want AI pane metadata %v", commands, want)
		}
	}
	wantLaunchPrefix := "export PATH='" + filepath.Join(home, "bin") + "'\":$PATH\" && cd '" + work + "' && __codex_title='codex:repo'"
	if !containsAICommandArgSubstring(commands, wantLaunchPrefix) {
		t.Fatalf("commands = %#v, want codex launch command starting with %q", commands, wantLaunchPrefix)
	}
}

func TestAgentLaunchCommandPrependsAgentBinDirToPath(t *testing.T) {
	cmd := &aiCommand{}
	got := cmd.agentLaunchCommand("codex", "/home/u/.nvm/versions/node/v24.0.0/bin/codex", "/work/repo", "codex:repo")
	want := `export PATH='/home/u/.nvm/versions/node/v24.0.0/bin'":$PATH"`
	if !strings.HasPrefix(got, want+" && ") {
		t.Fatalf("agentLaunchCommand = %q, want it to start with %q", got, want)
	}
	if !strings.Contains(got, "exec '/home/u/.nvm/versions/node/v24.0.0/bin/codex'") {
		t.Fatalf("agentLaunchCommand = %q, want it to exec the agent binary", got)
	}
}

func TestAISplitAgentFlagLaunchesClaudeWithoutChangingCodexDefault(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	stubAISplitReadCommand(cmd, home, work, map[string]string{"claude": claudeBin}, "%7", "%9")

	if err := cmd.Run([]string{"split", "--agent", "claude", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent claude error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeClaude}) {
		t.Fatalf("commands = %#v, want Claude AI pane metadata", commands)
	}
	if !containsAICommandArgSubstring(commands, "exec "+shellQuote(claudeBin)) {
		t.Fatalf("commands = %#v, want Claude exec", commands)
	}
}

func TestAISplitAgentFlagLaunchesCodexWithoutChangingClaudeDefault(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeClaude); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", "%9")

	if err := cmd.Run([]string{"split", "--agent=codex", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if got, want := readModeFile(t, home), "claude\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%7", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want vertical Codex split", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex}) {
		t.Fatalf("commands = %#v, want Codex AI pane metadata", commands)
	}
	if !containsAICommandArgSubstring(commands, "exec "+shellQuote(codexBin)) {
		t.Fatalf("commands = %#v, want Codex exec", commands)
	}
}

func TestAISplitAgentFlagLaunchesAntigravityWithoutChangingDefault(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	agyBin := writeExecutable(t, filepath.Join(home, "bin", "agy"))
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeClaude); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	stubAISplitReadCommand(cmd, home, work, map[string]string{"agy": agyBin}, "%7", "%9")

	if err := cmd.Run([]string{"split", "--agent=antigravity", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent antigravity error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if got, want := readModeFile(t, home), "claude\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%7", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want vertical Antigravity split", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeAntigravity}) {
		t.Fatalf("commands = %#v, want Antigravity AI pane metadata", commands)
	}
	if !containsAICommandArgSubstring(commands, "exec "+shellQuote(agyBin)) {
		t.Fatalf("commands = %#v, want Antigravity exec", commands)
	}
}

func TestAISplitCodexExtraArgsKeepsPaneSetupWatcherAndLayout(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"codex": codexBin}, "%7", "%9")

	err := cmd.Run([]string{"split", "--agent", "codex", "right", "--", "--model", "gpt-5.1 codex", "quote'd"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run split --agent codex -- extra args error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantExec := "exec " + shellQuote(codexBin) + " '--model' 'gpt-5.1 codex' 'quote'\\''d'"
	if !containsAICommandArgSubstring(commands, wantExec) {
		t.Fatalf("commands = %#v, want resolved Codex exec with extra args %q", commands, wantExec)
	}
	for _, want := range [][]string{
		{"set-option", "-p", "-t", "%9", aiPaneManagedOption, "1"},
		{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeCodex},
		{"set-option", "-p", "-t", "%9", aiPaneContextOption, work},
		{"set-option", "-p", "-t", "%9", aiPaneTopicOption, "repo"},
		{"set-option", "-p", "-t", "%9", aiPaneStateOption, "idle"},
		{"run-shell", "-b", "'/tmp/projmux' ai watch-title '%9'"},
		{"resize-pane", "-t", "%7", "-x", "40"},
		{"resize-pane", "-t", "%9", "-x", "40"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want command %v", commands, want)
		}
	}
	if !containsAICommandArgs(commands, "command", []string{"-v", "codex"}) {
		t.Fatalf("commands = %#v, want Codex binary lookup", commands)
	}
}

func TestAISplitClaudeExtraArgsUseResolvedBinary(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	stubAISplitReadCommand(cmd, home, work, map[string]string{"claude": claudeBin}, "%7", "%9")

	err := cmd.Run([]string{"split", "--agent", "claude", "down", "--", "--dangerously-skip-permissions"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run split --agent claude -- extra args error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantExec := "exec " + shellQuote(claudeBin) + " '--dangerously-skip-permissions'"
	if !containsAICommandArgSubstring(commands, wantExec) {
		t.Fatalf("commands = %#v, want resolved Claude exec with extra args %q", commands, wantExec)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-v", "-t", "%7", "-c", work, "/bin/bash", "-lc"}) {
		t.Fatalf("commands = %#v, want vertical Claude split", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeClaude}) {
		t.Fatalf("commands = %#v, want Claude AI pane metadata", commands)
	}
}

func TestAISplitAgentSelectiveDelegatesToPickerWithoutChangingDefault(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux bin", nil }
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{client_tty}"}) {
			return []byte("/dev/pts/7\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "selective", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent selective error = %v", err)
	}

	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
	want := []recordedAICommand{{
		name: "/tmp/projmux bin",
		args: []string{"tmux", "popup-toggle", "--client", "/dev/pts/7", "ai-split-picker-right"},
	}}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISplitAgentShellUsesPlainShellSplit(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/tmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%7"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", "-F", "#{pane_id}"}) {
			return []byte("%7\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-t", "%7", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}) {
			return []byte("%7\t0\t0\t40\t10\n%9\t0\t11\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "shell", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split --agent shell error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"split-window", "-v", "-t", "%7", "-c", work, "/bin/bash", "-l"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%7", "-y", "10"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%9", "-y", "10"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
	for _, forbidden := range [][]string{
		{"set-option", "-p", "-t", "%9", aiPaneManagedOption, "1"},
		{"set-option", "-p", "-t", "%9", aiPaneAgentOption, aiModeShell},
		{"run-shell", "-b", "'/tmp/projmux' ai watch-title '%9'"},
	} {
		if containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", forbidden) {
			t.Fatalf("commands = %#v, did not expect managed AI command %v", cmdRecorder(cmd).commands, forbidden)
		}
	}
}

func TestAISplitAgentFlagUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "invalid agent",
			args: []string{"split", "--agent", "openai", "right"},
			want: "unknown ai split agent: openai",
		},
		{
			name: "selective cannot use extra args",
			args: []string{"split", "--agent", "selective", "right", "--", "codex"},
			want: "ai split --agent selective cannot use extra args",
		},
		{
			name: "extra args require agent",
			args: []string{"split", "right", "--", "codex"},
			want: "ai split extra args require --agent",
		},
		{
			name: "extra args first arg empty",
			args: []string{"split", "--agent", "codex", "right", "--", ""},
			want: "ai split extra args require a non-empty first argument",
		},
		{
			name: "shell cannot use extra args",
			args: []string{"split", "--agent", "shell", "right", "--", "echo", "hi"},
			want: "ai split --agent shell cannot use extra args",
		},
		{
			name: "force agent requires direct agent",
			args: []string{"split", "--force-agent", "right"},
			want: "ai split --force-agent requires --agent claude, --agent codex, or --agent antigravity",
		},
		{
			name: "force agent does not apply to picker",
			args: []string{"split", "--agent", "selective", "--force-agent", "right"},
			want: "ai split --force-agent only applies to --agent claude, --agent codex, or --agent antigravity",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			stderr := &bytes.Buffer{}
			err := cmd.Run(tt.args, &bytes.Buffer{}, stderr)
			if err == nil {
				t.Fatalf("Run(%v) error = nil, want usage error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want contains %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
			if len(cmdRecorder(cmd).commands) != 0 {
				t.Fatalf("commands = %#v, want none", cmdRecorder(cmd).commands)
			}
		})
	}
}

func TestAISplitSelectiveTreatsCancelledPickerAsNoOp(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.runCommand = func(context.Context, string, ...string) error {
		return errors.New("exit status 1")
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split canceled picker error = %v, want nil", err)
	}
}

func TestAISplitSelectiveTreatsClosedPopupAsNoOp(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.runCommand = func(context.Context, string, ...string) error {
		return errors.New("exit status 129")
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split closed popup error = %v, want nil", err)
	}
}

func TestAISplitShellUsesTmuxSplitWindow(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	if err := cmd.setMode("shell"); err != nil {
		t.Fatal(err)
	}
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/tmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%9"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "-F", "#{pane_id}"}) {
			return []byte("%9\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-t", "%9", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}) {
			return []byte("%1\t0\t0\t80\t10\n%9\t0\t11\t80\t5\n%10\t0\t17\t80\t5\n%11\t81\t0\t20\t22\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split shell error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"split-window", "-v", "-t", "%9", "-c", work, "/bin/bash", "-l"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%1", "-y", "7"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%9", "-y", "7"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%10", "-y", "6"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISplitShellUsesPSMuxSplitWindowPowerShellTail(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	if err := cmd.setMode(aiModeShell); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case muxBackendEnvVar:
			return string(muxBackendPSMux)
		case "TMUX":
			return "/tmp/psmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%7"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "psmux" && reflect.DeepEqual(args, []string{"-L", defaultAppSocket, "display-message", "-p", "-t", "%7", "-F", "#{pane_id}"}) {
			return []byte("%7\n"), nil
		}
		if name == "psmux" && len(args) >= 5 && reflect.DeepEqual(args[:5], []string{"-L", defaultAppSocket, "split-window", "-v", "-P"}) {
			return []byte("%9\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run psmux shell split error = %v", err)
	}

	tail, err := intpsmux.RenderPowerShellCommand("powershell", "-NoLogo")
	if err != nil {
		t.Fatal(err)
	}
	want := []recordedAICommand{
		{name: "psmux", args: []string{"-L", defaultAppSocket, "display-message", "-p", "-t", "%7", "-F", "#{pane_id}"}},
		{name: "psmux", args: []string{"-L", defaultAppSocket, "split-window", "-v", "-P", "-F", "#{pane_id}", "-t", "%7", "-c", work, tail}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISplitCodexUsesPSMuxSplitWindowPowerShellTailAndSkipsTmuxMetadata(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin with space", "codex"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case muxBackendEnvVar:
			return string(muxBackendPSMux)
		case "TMUX":
			return "/tmp/psmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%7"
		case "PATH":
			return filepath.Dir(codexBin)
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "psmux" && reflect.DeepEqual(args, []string{"-L", defaultAppSocket, "display-message", "-p", "-t", "%7", "-F", "#{pane_id}"}) {
			return []byte("%7\n"), nil
		}
		if name == "psmux" && len(args) >= 5 && reflect.DeepEqual(args[:5], []string{"-L", defaultAppSocket, "split-window", "-h", "-P"}) {
			return []byte("%42\n"), nil
		}
		return nil, os.ErrNotExist
	}

	err := cmd.Run([]string{"split", "--agent", "codex", "right", "--", "--model", "gpt-5.1 codex", "quote'd"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run psmux codex split error = %v", err)
	}

	tail, err := intpsmux.RenderPowerShellCommand(codexBin, "--model", "gpt-5.1 codex", "quote'd")
	if err != nil {
		t.Fatal(err)
	}
	commands := cmdRecorder(cmd).commands
	for _, want := range []recordedAICommand{
		{name: "psmux", args: []string{"-L", defaultAppSocket, "display-message", "-p", "-t", "%7", "-F", "#{pane_id}"}},
		{name: "psmux", args: []string{"-L", defaultAppSocket, "split-window", "-h", "-P", "-F", "#{pane_id}", "-t", "%7", "-c", work, tail}},
	} {
		if !containsRecordedAICommand(commands, want) {
			t.Fatalf("commands = %#v, want command %#v", commands, want)
		}
	}
	for _, forbidden := range []recordedAICommand{
		{name: "tmux"},
		{name: "psmux", args: []string{"-L", defaultAppSocket, "set-option"}},
		{name: "psmux", args: []string{"-L", defaultAppSocket, "run-shell"}},
		{name: "psmux", args: []string{"-L", defaultAppSocket, "resize-pane"}},
	} {
		if containsRecordedAICommandPrefix(commands, forbidden) {
			t.Fatalf("commands = %#v, did not expect %#v", commands, forbidden)
		}
	}
}

func TestAISplitClaudeUsesPSMuxSplitWindowPowerShellTail(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case muxBackendEnvVar:
			return string(muxBackendPSMux)
		case "TMUX":
			return "/tmp/psmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%7"
		case "PATH":
			return filepath.Dir(claudeBin)
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "psmux" && reflect.DeepEqual(args, []string{"-L", defaultAppSocket, "display-message", "-p", "-t", "%7", "-F", "#{pane_id}"}) {
			return []byte("%7\n"), nil
		}
		if name == "psmux" && len(args) >= 5 && reflect.DeepEqual(args[:5], []string{"-L", defaultAppSocket, "split-window", "-v", "-P"}) {
			return []byte("%43\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "--agent", "claude", "down", "--", "--dangerously-skip-permissions"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run psmux claude split error = %v", err)
	}

	tail, err := intpsmux.RenderPowerShellCommand(claudeBin, "--dangerously-skip-permissions")
	if err != nil {
		t.Fatal(err)
	}
	want := recordedAICommand{name: "psmux", args: []string{"-L", defaultAppSocket, "split-window", "-v", "-P", "-F", "#{pane_id}", "-t", "%7", "-c", work, tail}}
	if !containsRecordedAICommand(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want command %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISplitClaudePSMuxReportsNativeInstallerPathGuidance(t *testing.T) {
	home := t.TempDir()
	claudeNative := writeExecutable(t, filepath.Join(home, ".local", "bin", "claude.exe"))
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case muxBackendEnvVar:
			return string(muxBackendPSMux)
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		return nil, os.ErrNotExist
	}

	err := cmd.Run([]string{"split", "--agent", "claude", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run psmux claude split error = nil, want PATH guidance for %s", claudeNative)
	}
	for _, want := range []string{
		"selected runner is installed at " + claudeNative,
		"but is not on PATH",
		"add " + filepath.Dir(claudeNative) + " to PATH",
		"restart psmux",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Run error = %q, want substring %q", err.Error(), want)
		}
	}
	if containsRecordedAICommandPrefix(cmdRecorder(cmd).commands, recordedAICommand{name: "psmux", args: []string{"-L", defaultAppSocket, "split-window"}}) {
		t.Fatalf("commands = %#v, did not expect split launch after missing Claude PATH guidance", cmdRecorder(cmd).commands)
	}
}

func TestAILabelsSayPlainShellNotZsh(t *testing.T) {
	t.Parallel()

	cmd := testAICommand(t.TempDir())
	for _, row := range append(cmd.agentRows(), cmd.settingsRows()...) {
		if strings.Contains(strings.ToLower(row.Label), "zsh") {
			t.Fatalf("AI row label = %q, did not expect zsh-specific copy", row.Label)
		}
	}
}

func TestSplitLayoutPeersPreserveOtherAxes(t *testing.T) {
	panes := []aiPaneGeometry{
		{id: "%1", left: 0, top: 0, width: 20, height: 10},
		{id: "%2", left: 21, top: 0, width: 10, height: 10},
		{id: "%3", left: 0, top: 11, width: 31, height: 10},
	}
	rightPeers := splitLayoutPeers(panes, panes[1], "right")
	if got, want := paneGeometryIDs(rightPeers), []string{"%1", "%2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("right peers = %#v, want %#v", got, want)
	}

	panes = []aiPaneGeometry{
		{id: "%1", left: 0, top: 0, width: 40, height: 10},
		{id: "%2", left: 0, top: 11, width: 40, height: 5},
		{id: "%3", left: 41, top: 0, width: 20, height: 16},
	}
	downPeers := splitLayoutPeers(panes, panes[1], "down")
	if got, want := paneGeometryIDs(downPeers), []string{"%1", "%2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("down peers = %#v, want %#v", got, want)
	}
}

func TestAIStatusSetThinkingMarksPaneBusy(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%1", "#{pane_title}"}) {
			return []byte("codex: repo\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "thinking", "%1"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set thinking error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_state", "thinking"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_ai_badge_kind", "in_progress"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@projmux_attention_state", "busy"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%1", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%1", "@projmux_attention_focus_armed"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAIStatusSetWaitingMarksPaneReplyAndNotifies(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "projmux")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			switch args[1] {
			case "notify-send":
				return []byte("/usr/bin/" + args[1] + "\n"), nil
			}
		}
		if name == "git" {
			switch {
			case reflect.DeepEqual(args, []string{"-C", work, "rev-parse", "--is-inside-work-tree"}):
				return []byte("true\n"), nil
			case reflect.DeepEqual(args, []string{"-C", work, "symbolic-ref", "--quiet", "--short", "HEAD"}):
				return []byte("main\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_title}"}):
			return []byte("Codex: approval needed\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}):
			return []byte("%99\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_state", "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_ai_badge_kind", "response_complete"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%2", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_attention_state", "reply"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@projmux_attention_focus_armed", "1"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch", commands)
	}
	for _, want := range []string{
		"--app-name=" + desktopAppID,
		desktopAppID,
		filepath.Join(home, ".local", "share", "projmux", "icons", "projmux.png"),
		"Codex · Approval required",
		"projmux/main",
	} {
		if !containsAICommandArgSubstring(commands, want) {
			t.Fatalf("commands = %#v, want notification shell containing %q", commands, want)
		}
	}
	if !containsAICommandArg(commands, "@projmux_desktop_notified") {
		t.Fatalf("commands = %#v, want notification record", commands)
	}
}

func TestAIStatusSetWaitingUsesNotificationHook(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "projmux")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(home, "notify-hook")
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_NOTIFY_HOOK":
			return hook
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			t.Fatalf("notify-send lookup should not run when PROJMUX_NOTIFY_HOOK is set")
		}
		if name == "git" {
			switch {
			case reflect.DeepEqual(args, []string{"-C", work, "rev-parse", "--is-inside-work-tree"}):
				return []byte("true\n"), nil
			case reflect.DeepEqual(args, []string{"-C", work, "symbolic-ref", "--quiet", "--short", "HEAD"}):
				return []byte("main\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{pane_title}"}):
			return []byte("Codex: answer ready\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{@projmux_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{@projmux_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{@projmux_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}):
			return []byte("%99\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%9"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, hook, []string{
		"Codex · Input required",
		"projmux/main",
		"normal",
		desktopAppID,
		"%9",
		"repo",
		filepath.Join(home, ".local", "share", "projmux", "icons", "projmux.png"),
	}) {
		t.Fatalf("commands = %#v, want notification hook dispatch", commands)
	}
	if containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, did not expect notify-send with notification hook", commands)
	}
}

func TestAIStatusSetWaitingInWSLRegistersToastAppIDAndDispatchesToast(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "projmux")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	psPath := "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
	localAppDataWin := `C:\Users\me\AppData\Local`
	localAppDataWSL := filepath.Join(home, "windows-localappdata")
	iconWSL := filepath.Join(localAppDataWSL, "projmux", "icons", "projmux.png")
	iconWin := `C:\Users\me\AppData\Local\projmux\icons\projmux.png`
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "WSL_DISTRO_NAME":
			return "Ubuntu-24.04"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			switch args[1] {
			case "powershell.exe":
				return []byte(psPath + "\n"), nil
			case "wsl-notify-send.exe":
				return nil, os.ErrNotExist
			}
		}
		if name == psPath && reflect.DeepEqual(args, []string{"-NoProfile", "-NonInteractive", "-Command", "[Environment]::GetFolderPath('LocalApplicationData')"}) {
			return []byte(localAppDataWin + "\n"), nil
		}
		if name == "wslpath" && reflect.DeepEqual(args, []string{"-u", localAppDataWin}) {
			return []byte(localAppDataWSL + "\n"), nil
		}
		if name == "wslpath" && reflect.DeepEqual(args, []string{"-w", iconWSL}) {
			return []byte(iconWin + "\n"), nil
		}
		if name == "git" {
			switch {
			case reflect.DeepEqual(args, []string{"-C", work, "rev-parse", "--is-inside-work-tree"}):
				return []byte("true\n"), nil
			case reflect.DeepEqual(args, []string{"-C", work, "symbolic-ref", "--quiet", "--short", "HEAD"}):
				return []byte("main\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_title}"}):
			return []byte("Codex: approval needed\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@projmux_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}):
			return []byte("%99\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	var powershellCommands []recordedAICommand
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == psPath {
			powershellCommands = append(powershellCommands, command)
		}
	}
	// Default WSL without WT_SESSION resolves to mode=notify. Notify mode
	// shows a toast but deliberately omits the projmux:// click target:
	//   [0] legacy AppID cleanup    (ensureWSLLegacyAppIDCleaned)
	//   [1] new AppID register      (ensureWSLToastAppID)
	//   [2] toast XML show          (dispatchWSLToast)
	if got, want := len(powershellCommands), 3; got != want {
		t.Fatalf("powershell commands len = %d, want %d, commands = %#v", got, want, cmdRecorder(cmd).commands)
	}
	cleanupScript := decodePowerShellEncodedCommand(t, powershellCommands[0])
	for _, want := range []string{
		"Get-StartApps",
		"projmux Tmux Codex",
		`HKCU:\Software\Classes\AppUserModelId\projmux.TmuxCodex`,
	} {
		if !strings.Contains(cleanupScript, want) {
			t.Fatalf("legacy cleanup script = %q, want substring %q", cleanupScript, want)
		}
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"set-option", "-g", legacyAppIDCleanedTmuxOption, "1"}) {
		t.Fatalf("commands = %#v, want legacy cleanup marker write", cmdRecorder(cmd).commands)
	}
	if containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"set-option", "-g", uriProtocolRegisteredTmuxOption, "1"}) {
		t.Fatalf("commands = %#v, did not expect uri protocol marker write in notify mode", cmdRecorder(cmd).commands)
	}
	registerScript := decodePowerShellEncodedCommand(t, powershellCommands[1])
	if !strings.Contains(registerScript, `HKCU:\SOFTWARE\Classes\AppUserModelId\`+desktopAppID) {
		t.Fatalf("register script = %q, want AppUserModelId registration for new id", registerScript)
	}
	if !strings.Contains(registerScript, desktopDisplayName) {
		t.Fatalf("register script = %q, want display name %q", registerScript, desktopDisplayName)
	}
	if !strings.Contains(registerScript, iconWin) {
		t.Fatalf("register script = %q, want icon uri", registerScript)
	}
	for _, want := range []string{
		"projmux.lnk",
		"Save($shortcutPath, $targetPath, $arguments, $description, $iconLocation, '" + desktopAppID + "')",
		"shellLink.SetPath(targetPath)",
	} {
		if !strings.Contains(registerScript, want) {
			t.Fatalf("register script = %q, want substring %q", registerScript, want)
		}
	}
	toastScript := decodePowerShellEncodedCommand(t, powershellCommands[2])
	for _, want := range []string{
		"CreateToastNotifier('" + desktopAppID + "').Show($toast)",
		"$toast.Tag = '%2'",
		"$toast.Group = 'repo'",
		`<toast duration="short">`,
		"$toast.ExpirationTime = [DateTimeOffset]::Now.AddMilliseconds(5000)",
		"Codex · Approval required",
		"projmux/main",
		iconWin,
		"appLogoOverride",
	} {
		if !strings.Contains(toastScript, want) {
			t.Fatalf("toast script = %q, want substring %q", toastScript, want)
		}
	}
	for _, absent := range []string{`activationType="protocol"`, "projmux://focus?", "pane_id=%252"} {
		if strings.Contains(toastScript, absent) {
			t.Fatalf("toast script = %q, did not want click target substring %q in notify mode", toastScript, absent)
		}
	}
	if _, err := os.Stat(iconWSL); err != nil {
		t.Fatalf("icon path %q missing: %v", iconWSL, err)
	}
}

func TestAIStatusSetIdleClearsSemanticBadge(t *testing.T) {
	cmd := testAICommand(t.TempDir())

	if err := cmd.Run([]string{"status", "set", "idle", "%3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set idle error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	for _, want := range []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%3", aiPaneStateOption, "idle"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%3", aiPaneBadgeKindOption}},
	} {
		if !containsRecordedAICommand(commands, want) {
			t.Fatalf("commands = %#v, want %#v", commands, want)
		}
	}
}

func TestAIStatusSetWaitingAcksVisiblePane(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}) {
			return []byte("%15\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%15"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_ai_state", "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_ai_badge_kind", "response_complete"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%15", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%15", "@projmux_attention_state"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_attention_ack", "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%15", "@projmux_attention_focus_armed"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
	if containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, did not expect notify-send for visible pane", commands)
	}
}

// Regression: pane_active=1 is not sufficient — when every client has moved to
// a different window/session the pane is not visible and the reply must NOT be
// auto-acked.
func TestAIStatusSetWaitingDoesNotAckWhenNoClientViewingPane(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}) {
			return []byte("%99\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%15"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_ai_state", "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_ai_badge_kind", "response_complete"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%15", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_attention_state", "reply"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%15", "@projmux_attention_focus_armed", "1"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
}

// Regression for the "stuck green badge" bug: when an AI hook (Claude Code /
// Codex native hook) fires with Force=true and the pane is already visible to
// some client, Force should only force the notify queue push — it must NOT
// set @projmux_attention_state=reply, otherwise focus can no longer clear the
// badge (focus clears attention_state, but used to leave ai_state=waiting and
// the badge formula ORed both).
func TestAIStatusSetWaitingForceDoesNotSetBadgeWhenVisible(t *testing.T) {
	home := t.TempDir()
	store := &stubNotifyStore{}
	cmd := testAICommand(home)
	cmd.producer = &storeAttentionNotifyProducer{store: store, ttl: 10 * time.Minute}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		if reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}) {
			return []byte("%21\n"), nil
		}
		if len(args) >= 5 && args[0] == "display-message" && args[1] == "-p" && args[2] == "-t" && args[3] == "%21" {
			switch args[4] {
			case "#{@projmux_ai_agent}":
				return []byte("claude\n"), nil
			case "#S":
				return []byte("main\n"), nil
			case "#W":
				return []byte("dev\n"), nil
			case "#{window_id}":
				return []byte("@4\n"), nil
			case "#{pane_id}":
				return []byte("%21\n"), nil
			case "#{pane_title}":
				return []byte("Claude: reply ready\n"), nil
			case "#{pane_current_path}":
				return []byte(home + "\n"), nil
			case "#{socket_path}":
				return []byte("/tmp/tmux/default\n"), nil
			}
		}
		return []byte("\n"), nil
	}

	if err := cmd.applyAIStatusWithNotify("waiting", "%21", attentionNotifyInput{
		ID:       "ai:test:%21",
		Text:     "forced hook",
		Metadata: map[string]string{"agent": "claude", "category": "response_complete"},
		Force:    true,
	}); err != nil {
		t.Fatalf("applyAIStatusWithNotify error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	// Badge writes must look like the visible/auto-ack path: ai_state=waiting,
	// then clear attention_state, set attention_ack=1, clear focus_armed.
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%21", "@projmux_ai_state", "waiting"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%21", "@projmux_ai_badge_kind", "response_complete"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%21", "@projmux_attention_ack"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%21", "@projmux_attention_state"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%21", "@projmux_attention_ack", "1"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%21", "@projmux_attention_focus_armed"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
	// attention_state must never be set to "reply" on the visible path,
	// regardless of Force.
	for _, got := range commands {
		if got.name == "tmux" && reflect.DeepEqual(got.args, []string{"set-option", "-p", "-t", "%21", "@projmux_attention_state", "reply"}) {
			t.Fatalf("commands = %#v, did not expect attention_state=reply when pane visible (Force=true must not touch the badge)", commands)
		}
	}
	// Force still ensures the notify queue gets a push entry even though the
	// pane is visible.
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1 (Force=true forces notify even when visible)", len(store.pushed))
	}
	// Force=true on a visible pane must still fire the OS-level notification
	// (notifyAI) — the badge stays visibility-driven but delivery channels
	// (queue + OS) follow the Force-or-not-visible rule uniformly.
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch when Force=true even with visible pane", commands)
	}
}

func TestAINotifySkipsRecentDuplicateButRefreshesRecord(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.lookupEnv = func(name string) string {
		if name == "PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS" {
			return "120"
		}
		return ""
	}
	key := "input_required|waiting for input"
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notified}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_title}"}):
			return []byte("waiting for input\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_key}"}):
			return []byte(key + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_at}"}):
			return []byte("950\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.notifyAI("%3"); err != nil {
		t.Fatalf("notifyAI error = %v", err)
	}
	commands := cmdRecorder(cmd).commands
	if containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, did not expect notify-send for duplicate", commands)
	}
	if !containsAICommandArg(commands, "@projmux_desktop_notification_at") {
		t.Fatalf("commands = %#v, want refreshed notification timestamp", commands)
	}
}

func TestAINotifyCommandBypassesDuplicateSuppression(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS":
			return "120"
		default:
			return ""
		}
	}
	key := "input_required|waiting for input"
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name == "git" {
			switch {
			case reflect.DeepEqual(args, []string{"-C", work, "rev-parse", "--is-inside-work-tree"}):
				return []byte("true\n"), nil
			case reflect.DeepEqual(args, []string{"-C", work, "symbolic-ref", "--quiet", "--short", "HEAD"}):
				return []byte("main\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notified}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_title}"}):
			return []byte("waiting for input\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_key}"}):
			return []byte(key + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_at}"}):
			return []byte("950\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"notify", "notify", "%3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run notify error = %v", err)
	}
	commands := cmdRecorder(cmd).commands
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch despite duplicate record", commands)
	}
}

func TestAINotifyUsesPaneMetadataBeforeMutableTitle(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			switch args[1] {
			case "notify-send":
				return []byte("/usr/bin/" + args[1] + "\n"), nil
			}
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%8" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("renamed by agent__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + work + "__PROJMUX_TMUX_AI_SEP__claude__PROJMUX_TMUX_AI_SEP__" + work + "__PROJMUX_TMUX_AI_SEP__approval needed__PROJMUX_TMUX_AI_SEP__waiting__PROJMUX_TMUX_AI_SEP__reply__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%8"}):
			return []byte("waiting for approval\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{@projmux_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{@projmux_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{@projmux_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"notify", "notify", "%8"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run notify error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch", commands)
	}
	for _, want := range []string{
		desktopAppID,
		filepath.Join(home, ".local", "share", "projmux", "icons", "projmux.png"),
		"Claude · Approval required",
	} {
		if !containsAICommandArgSubstring(commands, want) {
			t.Fatalf("commands = %#v, want metadata-derived Claude notification containing %q", commands, want)
		}
	}
}

func TestAIWatchTitlePromotesBusyPaneToThinking(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%4\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_title}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_state}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_ack}"}):
			return []byte("thinking hard__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_title}"}):
			return []byte("thinking hard\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"watch-title", "%4"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	if !containsAICommandArg(cmdRecorder(cmd).commands, "busy") {
		t.Fatalf("commands = %#v, want busy attention state", cmdRecorder(cmd).commands)
	}
	if !containsAICommandArgs(cmdRecorder(cmd).commands, "tmux", []string{"set-option", "-p", "-t", "%4", aiPaneBadgeKindOption, aiBadgeKindInProgress}) {
		t.Fatalf("commands = %#v, want in_progress semantic badge", cmdRecorder(cmd).commands)
	}
}

func TestAIWatchTitleStopsWhenPaneLookupReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%8", "#{pane_id}"}) {
			return []byte("\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"watch-title", "%8"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no writes for missing pane", cmdRecorder(cmd).commands)
	}
}

func TestAIWatchTitleUsesCapturePaneAsReplySignal(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%10", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%10\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%10" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("codexcli__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__codex__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__thinking__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%10"}):
			return []byte("waiting for input\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%10"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%10", "@projmux_ai_topic", "waiting for input"}) {
		t.Fatalf("commands = %#v, want capture-derived AI topic", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%10", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want waiting AI state from capture", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%10", aiPaneBadgeKindOption, aiBadgeKindInputRequired}) {
		t.Fatalf("commands = %#v, want input_required semantic badge from capture", commands)
	}
}

func TestAIWatchTitleMapsPermissionTitleToApprovalRequired(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%16", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%16\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%16" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("permission required__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__codex__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%16"}):
			return []byte("allow command?\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%16"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%16", aiPaneStateOption, "waiting"}) {
		t.Fatalf("commands = %#v, want waiting AI state", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%16", aiPaneBadgeKindOption, aiBadgeKindApprovalRequired}) {
		t.Fatalf("commands = %#v, want approval_required semantic badge", commands)
	}
}

func TestAIWatchTitlePreservesManualAITopic(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%15", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%15\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%15" && strings.Contains(args[4], aiPaneTopicManualOption):
			return []byte("codexcli__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__codex__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__manual topic__PROJMUX_TMUX_AI_SEP__1__PROJMUX_TMUX_AI_SEP__thinking__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%15"}):
			return []byte("waiting for input\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%15"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%15", "@projmux_ai_topic", "waiting for input"}) {
		t.Fatalf("commands = %#v, did not expect watcher to replace manual AI topic", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%15", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want waiting AI state from capture", commands)
	}
}

func TestAIWatchTitleBootstrapsMetadataForExistingCodexPane(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%11", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%11\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%11" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("es5h__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%11"}):
			return []byte("gpt-5.5 medium · ~\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%11"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	for _, want := range [][]string{
		{"set-option", "-p", "-t", "%11", "@projmux_ai_managed", "1"},
		{"set-option", "-p", "-t", "%11", "@projmux_ai_agent", "codex"},
		{"set-option", "-p", "-t", "%11", "@projmux_ai_context", home},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want bootstrapped metadata %v", commands, want)
		}
	}
}

func TestAIWatchTitleKeepsWaitingUntilFocusAck(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%12", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%12\n"), nil
		case len(args) == 5 && args[0] == "display-message" && args[3] == "%12" && strings.Contains(args[4], aiPaneAgentOption):
			return []byte("codexcli__PROJMUX_TMUX_AI_SEP__node__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__codex__PROJMUX_TMUX_AI_SEP__" + home + "__PROJMUX_TMUX_AI_SEP__repo__PROJMUX_TMUX_AI_SEP__waiting__PROJMUX_TMUX_AI_SEP__reply__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%12"}):
			return []byte("plain idle screen\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%12"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%12", "@projmux_ai_state", "idle"}) {
		t.Fatalf("commands = %#v, did not expect watcher to clear waiting state", commands)
	}
	if containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-u", "-t", "%12", "@projmux_attention_state"}) {
		t.Fatalf("commands = %#v, did not expect watcher to clear reply attention", commands)
	}
}

func TestAIWatchTitleSettledBusyBecomesWaitingReply(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_CODEX_REPLY_SETTLE_LOOPS":
			return "2"
		default:
			return ""
		}
	}
	checks := 0
	snapshots := []string{
		"thinking hard__PROJMUX_TMUX_AI_SEP____PROJMUX_TMUX_AI_SEP__",
		"repo__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__",
		"repo__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__",
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_id}"}):
			checks++
			if checks > len(snapshots) {
				return nil, os.ErrNotExist
			}
			return []byte("%6\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_title}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_state}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_ack}"}):
			return []byte(snapshots[checks-1] + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_title}"}):
			if checks <= 1 {
				return []byte("thinking hard\n"), nil
			}
			return []byte("repo\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%6"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%6", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want waiting ai pane state", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%6", "@projmux_attention_state", "reply"}) {
		t.Fatalf("commands = %#v, want reply attention state", commands)
	}
	if !containsAICommandArg(commands, "@projmux_desktop_notified") {
		t.Fatalf("commands = %#v, want notification record after settled busy", commands)
	}
}

func TestAIWatchTitleIgnoresStaleBusyCaptureHistory(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%13", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%13\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%13", "#{pane_title}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_state}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_ack}"}):
			return []byte("repo__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%13"}):
			return []byte("• Working (27s)\n\n  gpt-5.5 medium · ~/source/repos/projmux · main\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%13"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%13", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want stale busy history to become waiting", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%13", "@projmux_attention_state", "reply"}) {
		t.Fatalf("commands = %#v, want stale busy attention to become reply", commands)
	}
}

func TestAIWatchTitleSettlesUnchangedSpinnerTitle(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_CODEX_REPLY_SETTLE_LOOPS":
			return "2"
		default:
			return ""
		}
	}
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%14", "#{pane_id}"}):
			checks++
			if checks > 3 {
				return nil, os.ErrNotExist
			}
			return []byte("%14\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%14", "#{pane_title}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_state}__PROJMUX_TMUX_AI_SEP__#{@projmux_attention_ack}"}):
			return []byte("⠧ repo__PROJMUX_TMUX_AI_SEP__busy__PROJMUX_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"capture-pane", "-p", "-J", "-S", "-80", "-t", "%14"}):
			return []byte("idle prompt\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%14"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%14", "@projmux_ai_state", "waiting"}) {
		t.Fatalf("commands = %#v, want unchanged spinner title to settle waiting", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"set-option", "-p", "-t", "%14", "@projmux_attention_state", "reply"}) {
		t.Fatalf("commands = %#v, want unchanged spinner attention to become reply", commands)
	}
}

func TestAIReplyTitleIgnoresProjmuxAttentionMarkers(t *testing.T) {
	for _, title := range []string{"✳ repo", "✔ repo"} {
		if isAIReplyTitle(title) {
			t.Fatalf("isAIReplyTitle(%q) = true, want false for projmux marker", title)
		}
	}
}

func TestAIBadgeKindContractNormalizesAndFallsBackSafely(t *testing.T) {
	tests := []struct {
		name     string
		state    string
		explicit string
		want     string
	}{
		{name: "thinking fallback", state: "thinking", want: aiBadgeKindInProgress},
		{name: "waiting fallback", state: "waiting", want: aiBadgeKindResponseComplete},
		{name: "idle clears", state: "idle", want: ""},
		{name: "approval explicit", state: "waiting", explicit: aiBadgeKindApprovalRequired, want: aiBadgeKindApprovalRequired},
		{name: "input explicit", state: "waiting", explicit: aiBadgeKindInputRequired, want: aiBadgeKindInputRequired},
		{name: "invalid explicit falls back", state: "waiting", explicit: "future_kind", want: aiBadgeKindResponseComplete},
		{name: "invalid idle clears", state: "idle", explicit: "future_kind", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aiBadgeKindForStatus(tt.state, tt.explicit); got != tt.want {
				t.Fatalf("aiBadgeKindForStatus(%q, %q) = %q, want %q", tt.state, tt.explicit, got, tt.want)
			}
		})
	}
}

func TestAINotificationMessageLabelsClaudeAndAvoidsRootProject(t *testing.T) {
	if got, want := aiAgentDisplayName("Claude: waiting for input"), "Claude"; got != want {
		t.Fatalf("aiAgentDisplayName = %q, want %q", got, want)
	}
	if got, want := displayAITopic("Claude: waiting for input"), "waiting for input"; got != want {
		t.Fatalf("displayAITopic = %q, want %q", got, want)
	}
	if got := aiProjectName("/"); got != "" {
		t.Fatalf("aiProjectName(/) = %q, want empty", got)
	}
	if got, want := aiSummaryForKind("input_required", "Claude", "waiting for input"), "Claude · Input required"; got != want {
		t.Fatalf("aiSummaryForKind = %q, want %q", got, want)
	}
	if got, want := aiNotificationBody("waiting for input", "", "", "home", "dev"), ""; got != want {
		t.Fatalf("aiNotificationBody = %q, want %q", got, want)
	}
	if got, want := aiNotificationBody("", "projmux", "main", "home", "dev"), "projmux/main"; got != want {
		t.Fatalf("aiNotificationBody = %q, want %q", got, want)
	}
	if got, want := aiNotificationBody("Codex", "projmux", "main", "", ""), "projmux/main"; got != want {
		t.Fatalf("aiNotificationBody = %q, want %q", got, want)
	}
}

type capturingAIRunner struct {
	options intpickercompat.Options
	result  intpickercompat.Result
	err     error
}

func (r *capturingAIRunner) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	r.options = options
	return r.result, r.err
}

type recordedAICommand struct {
	name string
	args []string
}

type aiCommandRecorder struct {
	commands []recordedAICommand
}

func testAICommand(home string) *aiCommand {
	recorder := &aiCommandRecorder{}
	cmd := &aiCommand{
		runner:       &capturingAIRunner{},
		nativePicker: nativePickerFromCompatRunner(&capturingAIRunner{}),
		executable:   func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv: func(name string) string {
			switch name {
			case "HOME":
				return home
			default:
				return ""
			}
		},
		homeDir:   func() (string, error) { return home, nil },
		readFile:  func(string) ([]byte, error) { return nil, os.ErrNotExist },
		writeFile: os.WriteFile,
		mkdirAll:  os.MkdirAll,
		runCommand: func(_ context.Context, name string, args ...string) error {
			recorder.commands = append(recorder.commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
			return nil
		},
		readCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
	}
	cmd.now = func() time.Time { return time.Unix(0, 0) }
	cmd.sleep = func(time.Duration) {}
	aiRecordersMu.Lock()
	aiRecorders[cmd] = recorder
	aiRecordersMu.Unlock()
	return cmd
}

var (
	aiRecordersMu sync.Mutex
	aiRecorders   = map[*aiCommand]*aiCommandRecorder{}
)

func cmdRecorder(cmd *aiCommand) *aiCommandRecorder {
	aiRecordersMu.Lock()
	defer aiRecordersMu.Unlock()
	return aiRecorders[cmd]
}

func stubAISplitReadCommand(cmd *aiCommand, home, work string, bins map[string]string, targetPane, newPane string) {
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		case "SHELL":
			return "/bin/bash"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			if bin := bins[args[1]]; bin != "" {
				return []byte(bin + "\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}):
			return []byte(targetPane + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
			return []byte(work + "\n"), nil
		case len(args) >= 6 && reflect.DeepEqual(args[:4], []string{"split-window", "-P", "-F", "#{pane_id}"}):
			return []byte(newPane + "\n"), nil
		case reflect.DeepEqual(args, []string{"list-panes", "-t", targetPane, "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}):
			return []byte(targetPane + "\t0\t0\t40\t10\n" + newPane + "\t41\t0\t40\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}
}

func readModeFile(t *testing.T, home string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(home, ".config", "projmux", "tmux-ai-split-mode"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func containsAICommand(commands []recordedAICommand, name string) bool {
	for _, command := range commands {
		if command.name == name {
			return true
		}
	}
	return false
}

func containsAICommandArgs(commands []recordedAICommand, name string, prefix []string) bool {
	for _, command := range commands {
		if command.name != name || len(command.args) < len(prefix) {
			continue
		}
		if reflect.DeepEqual(command.args[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

func containsRecordedAICommand(commands []recordedAICommand, want recordedAICommand) bool {
	for _, command := range commands {
		if command.name == want.name && reflect.DeepEqual(command.args, want.args) {
			return true
		}
	}
	return false
}

func containsRecordedAICommandPrefix(commands []recordedAICommand, want recordedAICommand) bool {
	for _, command := range commands {
		if command.name != want.name || len(command.args) < len(want.args) {
			continue
		}
		if reflect.DeepEqual(command.args[:len(want.args)], want.args) {
			return true
		}
	}
	return false
}

func decodePowerShellEncodedCommand(t *testing.T, command recordedAICommand) string {
	t.Helper()
	if len(command.args) < 4 {
		t.Fatalf("powershell args = %#v, want encoded command", command.args)
	}
	encoded := command.args[len(command.args)-1]
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64 error = %v", err)
	}
	if len(decoded)%2 != 0 {
		t.Fatalf("decoded powershell bytes len = %d, want even", len(decoded))
	}
	words := make([]uint16, 0, len(decoded)/2)
	for i := 0; i < len(decoded); i += 2 {
		words = append(words, binary.LittleEndian.Uint16(decoded[i:i+2]))
	}
	return string(utf16.Decode(words))
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsAICommandArg(commands []recordedAICommand, arg string) bool {
	for _, command := range commands {
		if slices.Contains(command.args, arg) {
			return true
		}
	}
	return false
}

func containsAICommandArgSubstring(commands []recordedAICommand, value string) bool {
	for _, command := range commands {
		for _, commandArg := range command.args {
			if strings.Contains(commandArg, value) {
				return true
			}
		}
	}
	return false
}

func paneGeometryIDs(panes []aiPaneGeometry) []string {
	ids := make([]string, 0, len(panes))
	for _, pane := range panes {
		ids = append(ids, pane.id)
	}
	return ids
}

func TestAITopicSetWritesPaneOptionAndManualFlag(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	if err := cmd.Run([]string{"topic", "set", "fix login bug", "--pane", "%3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic set error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%3", "@projmux_ai_topic", "fix login bug"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%3", "@projmux_ai_topic_manual", "on"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAITopicSetUsesEnvPaneWhenFlagOmitted(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX_PANE":
			return "%7"
		case "HOME":
			return home
		default:
			return ""
		}
	}

	if err := cmd.Run([]string{"topic", "set", "review PR"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic set error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", "@projmux_ai_topic", "review PR"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%7", "@projmux_ai_topic_manual", "on"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAITopicClearUnsetsBothPaneOptions(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	if err := cmd.Run([]string{"topic", "clear", "--pane", "%4"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic clear error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%4", "@projmux_ai_topic"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%4", "@projmux_ai_topic_manual"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAITopicGetPrintsPaneOptionValue(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%5", "#{@projmux_ai_topic}"}) {
			return []byte("[Lead:Roadmap] ship the feature\n"), nil
		}
		return nil, os.ErrNotExist
	}

	stdout := &bytes.Buffer{}
	if err := cmd.Run([]string{"topic", "get", "--pane", "%5"}, stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic get error = %v", err)
	}

	if got, want := stdout.String(), "[Lead:Roadmap] ship the feature\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stdout.String(), "\x1b[") || strings.Contains(stdout.String(), "#[") {
		t.Fatalf("stdout = %q, want plain topic text", stdout.String())
	}
}

func TestAITopicGetEmitsBlankLineWhenUnset(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	stdout := &bytes.Buffer{}
	if err := cmd.Run([]string{"topic", "get", "--pane", "%6"}, stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run topic get error = %v", err)
	}
	if got, want := stdout.String(), "\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAITopicSetRequiresText(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"topic", "set", "--pane", "%2"}, &bytes.Buffer{}, stderr); err == nil {
		t.Fatalf("Run topic set without text expected error, got nil")
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("expected no tmux commands, got %#v", cmdRecorder(cmd).commands)
	}
}

func TestAITopicUnknownActionReturnsError(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	stderr := &bytes.Buffer{}
	err := cmd.Run([]string{"topic", "foo"}, &bytes.Buffer{}, stderr)
	if err == nil {
		t.Fatalf("Run topic foo expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown ai topic subcommand") {
		t.Fatalf("error = %v, want contains \"unknown ai topic subcommand\"", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("expected no tmux commands, got %#v", cmdRecorder(cmd).commands)
	}
}

func TestAITopicHelpListedInUsage(t *testing.T) {
	stdout := &bytes.Buffer{}
	printAIUsage(stdout)
	for _, want := range []string{
		"projmux ai topic set <text> [--pane <id>]",
		"projmux ai topic clear [--pane <id>]",
		"projmux ai topic get [--pane <id>]",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("usage = %q, want contains %q", stdout.String(), want)
		}
	}
}

func TestAITopicErrorsWhenNoPaneAvailable(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(string) string { return "" }
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not in tmux")
	}

	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"topic", "set", "anything"}, &bytes.Buffer{}, stderr); err == nil {
		t.Fatalf("Run topic set without pane expected error, got nil")
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("expected no tmux set-option commands, got %#v", cmdRecorder(cmd).commands)
	}
}

// TestBuildRegisterToastAppIDShortcutTargetIsCmdExe pins the Start Menu
// shortcut target produced by buildRegisterToastAppIDPowerShell to
// `cmd.exe /c exit`. The shortcut is a property bag for PKEY_AppUserModel_ID
// (pid=5) so the toast routes under our DisplayName; its target is never
// actually launched. We do NOT want `powershell.exe -WindowStyle Hidden ...`
// here — Windows Defender quarantines such shortcuts moments after creation,
// which silently breaks toast AppID routing and (because the click path
// depends on the AppID being live) silently breaks click activation.
func TestBuildRegisterToastAppIDShortcutTargetIsCmdExe(t *testing.T) {
	script := buildRegisterToastAppIDPowerShell(desktopAppID, desktopDisplayName, "")
	for _, want := range []string{
		`$targetPath = [Environment]::ExpandEnvironmentVariables('%SystemRoot%\System32\cmd.exe')`,
		`$arguments = '/c exit'`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("register script missing %q: %s", want, script)
		}
	}
	// Strip PS `#` comment lines before scanning for forbidden tokens —
	// the source-level guidance comment mentions the historical
	// powershell.exe target by name and we don't want the assertion to
	// flag its own do-not-do-this commentary.
	var noComments strings.Builder
	for line := range strings.SplitSeq(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		noComments.WriteString(line)
		noComments.WriteByte('\n')
	}
	effective := noComments.String()
	for _, forbidden := range []string{
		`WindowsPowerShell\v1.0\powershell.exe`,
		`-WindowStyle Hidden`,
	} {
		if strings.Contains(effective, forbidden) {
			t.Fatalf("register script contains forbidden token %q (Defender quarantines such shortcuts): %s", forbidden, effective)
		}
	}
}

func TestAICommandMuxBackendNonOutputCommandRequiresRunner(t *testing.T) {
	readerCalled := false
	backend := aiCommandMuxBackend{
		readCommand: func(context.Context, string, ...string) ([]byte, error) {
			readerCalled = true
			return []byte("unexpected"), nil
		},
	}

	_, err := backend.Run(context.Background(), "tmux", "set-hook", "-ag", "alert-bell", "run-shell -b true")
	if err == nil || err.Error() != "ai command runner is not configured" {
		t.Fatalf("Run error = %v, want ai command runner is not configured", err)
	}
	if readerCalled {
		t.Fatal("readCommand called for non-output mux command")
	}
}

// TestBuildRegisterToastAppIDDoesNotSetToastActivatorCLSID guards against a
// well-meaning "fix" that adds PKEY_AppUserModel_ToastActivatorCLSID
// (pid=26) to the shortcut. Setting that property routes Windows toast
// activation down the COM path first; in our unpackaged Win32 setup the
// COM call silently fails and Windows does NOT fall through to the
// ShellExecute(launch URI) path — i.e. click activation breaks. The
// shortcut intentionally carries only the AppUserModelID (pid=5) so the
// URI launch path is taken on click.
//
// We strip PowerShell comment lines before scanning so the source-level
// guidance comment that mentions ToastActivatorCLSID by name doesn't
// trigger the substring assertion. The check intentionally targets
// executable PS lines only.
func TestBuildRegisterToastAppIDDoesNotSetToastActivatorCLSID(t *testing.T) {
	script := buildRegisterToastAppIDPowerShell(desktopAppID, desktopDisplayName, "")
	var noComments strings.Builder
	for line := range strings.SplitSeq(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		noComments.WriteString(line)
		noComments.WriteByte('\n')
	}
	effective := noComments.String()
	for _, forbidden := range []string{
		"ToastActivatorCLSID",
		// pid=26 is the property id for ToastActivatorCLSID. The shortcut's
		// only property write is the AppUserModel_ID (pid=5) — any pid=26
		// PROPERTYKEY introduction would be a regression.
		`PROPERTYKEY("9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3", 26)`,
		// Cover the PROPERTYKEY constructor by raw pid form too.
		", 26)",
	} {
		if strings.Contains(effective, forbidden) {
			t.Fatalf("register script must not configure ToastActivatorCLSID (%q): %s", forbidden, effective)
		}
	}
}
