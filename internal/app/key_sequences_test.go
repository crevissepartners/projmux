package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/platformkeys"
)

func TestKeymapV2SequencesParseRenderAndMerge(t *testing.T) {
	body := `schema_version = 2

[bindings."project-sidebar.toggle"]
keys = ["M-1"]
sequences = ["C-k p", "F12 Enter"]
`
	parsed, err := parseKeymapFile("keymap.toml", body)
	if err != nil {
		t.Fatal(err)
	}
	override := parsed.Bindings["project-sidebar.toggle"]
	if !override.SequencesSet || !slices.Equal(override.Sequences, []string{"C-k p", "F12 Enter"}) {
		t.Fatalf("sequences = %#v, set=%v", override.Sequences, override.SequencesSet)
	}
	rendered := renderKeymapFile(parsed)
	if !strings.Contains(rendered, `sequences = ["C-k p", "F12 Enter"]`) {
		t.Fatalf("rendered keymap = %q", rendered)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatal(err)
	}
	action, ok := keyBindingActionByID(merged, "project-sidebar.toggle")
	if !ok || !slices.Equal(action.Sequences, override.Sequences) {
		t.Fatalf("merged action = %#v", action)
	}
}

func TestKeymapSequenceGrammar(t *testing.T) {
	for _, valid := range []string{
		"C-k p", "M-x Enter", "F12 Tab", "Left Space", "C-M-k C-p C-s M-1",
	} {
		if got, err := normalizeKeymapSequence(valid); err != nil || got != valid {
			t.Errorf("normalize(%q) = %q, %v", valid, got, err)
		}
	}
	for _, invalid := range []string{
		"p C-k", "C-k", "C-k p M-x C-a C-b", "C-k  p", " C-k p",
		"C-k Escape", "Esc C-k", "C-k User1", "C-k sendInput", "C-k UnknownName",
	} {
		if _, err := normalizeKeymapSequence(invalid); err == nil {
			t.Errorf("normalize(%q) = nil error", invalid)
		}
	}
	// A terminal reports CR as Enter and TAB as Tab, never as the control
	// spelling, so these would bind a leaf the user cannot reach.
	for invalid, want := range map[string]string{
		"C-k C-m": `write "Enter"`,
		"C-k C-i": `write "Tab"`,
		"C-m C-k": `write "Enter"`,
		"C-k C-[": "reserved for cancelling",
	} {
		_, err := normalizeKeymapSequence(invalid)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("normalize(%q) error = %v, want it to contain %q", invalid, err, want)
		}
	}
	for _, valid := range []string{"C-k Enter", "C-k Tab"} {
		if got, err := normalizeKeymapSequence(valid); err != nil || got != valid {
			t.Errorf("normalize(%q) = %q, %v", valid, got, err)
		}
	}
}

func TestKeymapSequenceConflictMatrix(t *testing.T) {
	actions := defaultKeyBindingCatalog()
	set := func(id string, sequences ...string) {
		for i := range actions {
			if actions[i].ID == id {
				actions[i].Sequences = sequences
				return
			}
		}
		t.Fatalf("action %s not found", id)
	}
	set("ProjectSidebarToggle", "C-k C-p")
	set("SessionPopupToggle", "C-k C-s")
	if err := validateKeymapConflicts(actions); err != nil {
		t.Fatalf("shared prefix rejected: %v", err)
	}

	set("SessionPopupToggle", "C-k C-p")
	if err := validateKeymapConflicts(actions); err == nil || !strings.Contains(err.Error(), "bound to both") {
		t.Fatalf("duplicate error = %v", err)
	}
	set("SessionPopupToggle", "C-k C-p C-s")
	if err := validateKeymapConflicts(actions); err == nil || !strings.Contains(err.Error(), "strict-prefix") {
		t.Fatalf("prefix error = %v", err)
	}
	set("SessionPopupToggle", "M-1 C-s")
	if err := validateKeymapConflicts(actions); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("single overlap error = %v", err)
	}
	set("ProjectSidebarToggle", "C-k C-p", "C-k C-p")
	set("SessionPopupToggle")
	if err := validateKeymapConflicts(actions); err == nil || !strings.Contains(err.Error(), "bound to both") {
		t.Fatalf("same-action duplicate error = %v", err)
	}
}

func TestKeySequenceTrieRenderingIsDeterministicAndCancels(t *testing.T) {
	actions := defaultKeyBindingCatalog()
	for i := range actions {
		switch actions[i].ID {
		case "ProjectSidebarToggle":
			actions[i].Sequences = []string{"C-k C-p"}
		case "SessionPopupToggle":
			actions[i].Sequences = []string{"C-k C-s"}
		}
	}
	first := tmuxSequenceBindLines("/tmp/projmux", actions)
	reversed := append([]keyBindingAction(nil), actions...)
	slices.Reverse(reversed)
	second := tmuxSequenceBindLines("/tmp/projmux", reversed)
	if !slices.Equal(first, second) {
		t.Fatalf("nondeterministic render:\n%v\n%v", first, second)
	}
	joined := strings.Join(first, "\n")
	rootTable := keySequenceTableName([]string{"C-k"})
	for _, want := range []string{
		"bind-key -n C-k switch-client -T " + rootTable,
		"bind-key -T " + rootTable + " Escape switch-client -T root",
		"bind-key -T " + rootTable + " Any switch-client -T root",
		"bind-key -T " + rootTable + " C-p run-shell",
		"bind-key -T " + rootTable + " C-s run-shell",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("render = %q, want %q", joined, want)
		}
	}
	if strings.Contains(joined, "send-keys") {
		t.Fatalf("sequence renderer injects pane input: %q", joined)
	}
}

func TestKeySequenceAppRenderCombinesStandaloneAndAppSharedPrefix(t *testing.T) {
	actions := defaultKeyBindingCatalog()
	for i := range actions {
		switch actions[i].CanonicalID {
		case "project-sidebar.toggle":
			actions[i].Sequences = []string{"C-k C-p"}
		case "window.create":
			actions[i].Sequences = []string{"C-k C-w"}
		}
	}
	lines := strings.Join(tmuxAppKeyBindings("/tmp/projmux", actions, true), "\n")
	table := keySequenceTableName([]string{"C-k"})
	if got := strings.Count(lines, "bind-key -n C-k switch-client -T "+table); got != 1 {
		t.Fatalf("combined root binding count = %d, want 1\n%s", got, lines)
	}
	for _, want := range []string{
		"bind-key -T " + table + " C-p run-shell",
		"bind-key -T " + table + " C-w run-shell",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("combined render missing %q\n%s", want, lines)
		}
	}
}

func TestKeySequenceGeneratedStateIsRecordedNotRetiredByTheConfig(t *testing.T) {
	actions := defaultKeyBindingCatalog()
	actions[0].Sequences = []string{"C-k p"}
	lines := strings.Join(tmuxSequenceStateLines(actions), "\n")
	for _, want := range []string{
		"set-option -g " + tmuxSequenceRootsOption + " \"C-k\"",
		"set-option -g " + tmuxSequenceTablesOption + " \"" + keySequenceTableName([]string{"C-k"}) + "\"",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("state lines = %q, want %q", lines, want)
		}
	}
	// A `run-shell` loop is not ordered against the rest of a sourced file, so
	// removal must never be expressed in the generated config.
	config := tmuxStandaloneConfigWithKeymap("/tmp/projmux", statusbarDecorationSet{}, actions, true)
	for _, forbidden := range []string{
		"unbind-key -q -n \"$key\"",
		"show-option -gqv " + tmuxSequenceRootsOption,
		"show-option -gqv " + tmuxSequenceTablesOption,
	} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("generated config still retires sequence state via %q", forbidden)
		}
	}
}

func TestKeySequenceRetireCommandsTargetOnlyRecordedState(t *testing.T) {
	table := keySequenceTableName([]string{"C-k"})
	got := keySequenceRetireCommands("sock", "C-k F12", table)
	want := [][]string{
		{"tmux", "-L", "sock", "unbind-key", "-q", "-n", "C-k"},
		{"tmux", "-L", "sock", "unbind-key", "-q", "-n", "F12"},
		{"tmux", "-L", "sock", "unbind-key", "-a", "-q", "-T", table},
	}
	if len(got) != len(want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("command %d = %v, want %v", i, got[i], want[i])
		}
	}
	if len(keySequenceRetireCommands("sock", "", "")) != 0 {
		t.Fatal("unrecorded state retired something")
	}
}

// Removal correctness is an ordering property: the recorded state must be read
// and unbound strictly before source-file installs the current trie. A
// `run-shell` loop inside the generated config cannot guarantee that, so this
// pins the apply-path ordering directly.
func TestTmuxApplyRetiresRecordedSequenceStateBeforeSourcingTheNewConfig(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	writeFile(t, configPath, "previous\n")
	staleTable := keySequenceTableName([]string{"C-k"})
	physicalSocket := "/tmp/tmux-1000/seq-socket"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", physicalSocket, "list-sessions", "-F", "#{session_id}"):           "$0\n",
		recordedTmuxCallKey("tmux", "-S", physicalSocket, "show-options", "-gqv", tmuxSequenceRootsOption):  "C-k\n",
		recordedTmuxCallKey("tmux", "-S", physicalSocket, "show-options", "-gqv", tmuxSequenceTablesOption): staleTable + "\n",
	}}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
		runner:     runner,
	}
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--config", configPath, "--socket", "seq-socket"}, &stdout, &stderr); err != nil {
		t.Fatalf("apply error = %v; stderr = %q", err, stderr.String())
	}

	indexOf := func(match func([]string) bool) int {
		for i, call := range runner.calls {
			if match(tmuxCommandArgv(call.args)) {
				return i
			}
		}
		return -1
	}
	unbindRoot := indexOf(func(args []string) bool {
		return slices.Equal(args, []string{"unbind-key", "-q", "-n", "C-k"})
	})
	unbindTable := indexOf(func(args []string) bool {
		return slices.Equal(args, []string{"unbind-key", "-a", "-q", "-T", staleTable})
	})
	source := indexOf(func(args []string) bool {
		return len(args) >= 1 && args[0] == "source-file"
	})
	if unbindRoot < 0 || unbindTable < 0 || source < 0 {
		t.Fatalf("missing retire/source calls: root=%d table=%d source=%d\n%#v", unbindRoot, unbindTable, source, runner.calls)
	}
	if unbindRoot > source || unbindTable > source {
		t.Fatalf("retire ran after source-file: root=%d table=%d source=%d", unbindRoot, unbindTable, source)
	}
}

func TestKeySequenceStateRecordedWhenKeymapIsAbsent(t *testing.T) {
	config := tmuxStandaloneConfigWithKeymap("/tmp/projmux", statusbarDecorationSet{}, defaultKeyBindingCatalog(), false)
	for _, want := range []string{
		"set-option -g " + tmuxSequenceRootsOption + " \"\"",
		"set-option -g " + tmuxSequenceTablesOption + " \"\"",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config without keymap missing recorded empty state %q", want)
		}
	}
	if strings.Contains(config, "switch-client -T "+tmuxSequenceTablePrefix) {
		t.Fatalf("config without keymap rendered a sequence trigger")
	}
}

func TestKeyBrokerSequenceAllowlistIsTransportOnly(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `schema_version = 2
[bindings."project-sidebar.toggle"]
sequences = ["C-k p", "F12 Enter", "M-x C-p"]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &keyBrokerCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
		readFile:  os.ReadFile,
	}
	bindings, err := cmd.loadBindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, chord := range []string{"C-k", "M-x", "C-p"} {
		want, _ := platformkeys.ParseBinding(chord)
		if !containsPlatformBinding(bindings, want) {
			t.Errorf("native allowlist missing %s", chord)
		}
	}
	for _, chord := range []string{"p", "F12", "Enter"} {
		for _, binding := range bindings {
			if binding.Chord == chord {
				t.Errorf("ordinary stroke %s entered native allowlist", chord)
			}
		}
	}
	catalog, _, err := loadMergedKeyBindingCatalog(keymapLoader{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
		readFile:  os.ReadFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	sequenceLines := strings.Join(tmuxSequenceBindLines("/tmp/projmux", catalog), "\n")
	if strings.Contains(sequenceLines, "bind-key -n C-p ") {
		t.Fatalf("native continuation stroke gained an independent root binding: %s", sequenceLines)
	}
	action, ok := keyBindingActionByID(catalog, "ProjectSidebarToggle")
	if !ok {
		t.Fatal("ProjectSidebarToggle action missing")
	}
	if !strings.Contains(sequenceLines, " C-p "+renderTmuxBindingBody("/tmp/projmux", action)) {
		t.Fatalf("native continuation stroke is not scoped to its generated table: %s", sequenceLines)
	}
}

func TestKeymapV1ToV2MigrationPreservesBytesAndRollsBack(t *testing.T) {
	original := "# keep this comment\nschema_version = 1 # old\n\n[bindings.unknown]\nkeys = [\"C-x\"]\n\n[bindings.\"window.create\"]\nkeys = [\"C-t\"]\n"
	store, path := newKeymapFixture(t, original)
	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(original, "schema_version = 1", "schema_version = 2", 1)
	if got := readFile(t, path); got != want {
		t.Fatalf("migration changed more than marker:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(result.BackupPath, ".pre-v2-") {
		t.Fatalf("backup = %q", result.BackupPath)
	}
	second, err := migrateKeymapForWrite(store)
	if err != nil || second.Migrated || second.Plan.Required {
		t.Fatalf("repeat migration = %+v, %v", second, err)
	}
	if err := rollbackKeymapMigration(store, result.BackupPath); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != original {
		t.Fatalf("rollback = %q, want %q", got, original)
	}
}

func TestKeymapV2SequenceFailuresPrecedeEveryWrite(t *testing.T) {
	tests := map[string]string{
		"duplicate": `[bindings."project-sidebar.toggle"]
sequences = ["C-k C-p", "C-k C-p"]`,
		"strict-prefix": `[bindings."project-sidebar.toggle"]
sequences = ["C-k C-p"]
[bindings."session-picker.toggle"]
sequences = ["C-k C-p C-s"]`,
		"single-overlap": `[bindings."project-sidebar.toggle"]
sequences = ["M-1 C-p"]`,
		"unsafe": `[bindings."project-sidebar.toggle"]
sequences = ["C-k Escape"]`,
	}
	for name, tables := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
			original := "schema_version = 2\n" + tables + "\n"
			writeFile(t, keymap, original)
			configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
			configBefore := []byte("previous-generated-config\n")
			writeFile(t, configPath, string(configBefore))
			runner := &recordingTmuxRunner{outputs: map[string]string{}}
			writeCalls := 0
			cmd := &tmuxCommand{
				executable: func() (string, error) { return "/tmp/projmux", nil },
				homeDir:    func() (string, error) { return home, nil },
				lookupEnv:  func(string) string { return "" },
				readFile:   os.ReadFile,
				writeFile: func(path string, body []byte, mode os.FileMode) error {
					writeCalls++
					return os.WriteFile(path, body, mode)
				},
				runner: runner,
			}
			var stdout, stderr bytes.Buffer
			if err := cmd.Run([]string{"apply", "--config", configPath, "--socket", "must-not-touch"}, &stdout, &stderr); err == nil {
				t.Fatal("invalid sequence apply succeeded")
			}
			if writeCalls != 0 {
				t.Fatalf("write calls = %d, want 0", writeCalls)
			}
			if got := readFile(t, keymap); got != original {
				t.Fatalf("keymap changed: %q", got)
			}
			if got := readFile(t, configPath); got != string(configBefore) {
				t.Fatalf("generated config changed: %q", got)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("tmux calls = %#v, want none", runner.calls)
			}
		})
	}
}

func TestKeymapSequencesDoNotExpandPhaseZeroSurface(t *testing.T) {
	for _, action := range defaultKeyBindingCatalog() {
		if len(action.Sequences) != 0 {
			t.Fatalf("built-in action %s gained a default sequence", action.ID)
		}
	}
	for _, body := range []string{
		"schema_version = 2\n[bindings.\"project-sidebar.toggle\"]\ncommand = \"display-message nope\"\n",
		"schema_version = 2\n[bindings.\"project-sidebar.toggle\"]\noutput = \"text\"\n",
	} {
		if _, err := parseKeymapFile("keymap.toml", body); err == nil {
			t.Fatalf("excluded command/output field parsed: %q", body)
		}
	}
	picker := `schema_version = 2
[bindings."project-sidebar.project.pin-toggle"]
sequences = ["C-k C-o"]
`
	parsed, err := parseKeymapFile("keymap.toml", picker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed); err == nil || !strings.Contains(err.Error(), "picker-local") {
		t.Fatalf("picker-local sequence merge error = %v", err)
	}
}
