package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestSettingsKeybindingDetailSeparatesSingleKeysAndSequences(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := keybindingCorrectnessCommand(t, home, nil)
	if err := cmd.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("addKeymapSequenceAndApply() error = %v", err)
	}
	entries, _, err := cmd.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatalf("keybindingDetailEntries() error = %v", err)
	}
	for _, want := range []string{"Single Keys", "Sequences", "C-k,C-p", "+ Add binding", "Enter binding manually"} {
		if !hasEntryLabelContaining(entries, want) {
			t.Fatalf("detail entries = %#v, want %q", entries, want)
		}
	}
	if !hasEntryValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:sequence:C-k C-p") {
		t.Fatalf("detail entries = %#v, want sequence detail route", entries)
	}
	for _, retired := range []string{"+ Add key", "+ Add sequence", "Enter sequence manually", "Record next stroke", "Sequence Editor"} {
		if hasEntryLabelContaining(entries, retired) {
			t.Fatalf("detail entries = %#v, retired authoring label %q remains", entries, retired)
		}
	}
	for _, retiredRoute := range []string{"sequence-add", "sequence-type", "sequence-capture", "sequence-save", "sequence-stroke-remove:0"} {
		if op, ok := parseKeymapDetailAction(settingsActionPrefixKeymap+"ProjectSidebarToggle:"+retiredRoute, "ProjectSidebarToggle"); ok {
			t.Fatalf("retired route %q still parses as %q", retiredRoute, op)
		}
	}

	pickerEntries, _, err := cmd.keybindingDetailEntries("Sidebar:PinProject")
	if err != nil {
		t.Fatalf("picker-local detail error = %v", err)
	}
	if !hasEntryLabelContainingAll(pickerEntries, "Sequences", "not available", "picker-local") {
		t.Fatalf("picker-local entries = %#v, want explicit sequence boundary", pickerEntries)
	}
	for _, forbidden := range []string{"sequence-add", "sequence-type"} {
		for _, entry := range pickerEntries {
			if strings.Contains(entry.Value, forbidden) {
				t.Fatalf("picker-local entry %#v exposes forbidden %q authoring", entry, forbidden)
			}
		}
	}
}

func TestSettingsKeybindingSequenceActionDetailGoldenAndKoreanLocale(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := keybindingCorrectnessCommand(t, home, nil)
	if err := cmd.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("seed sequence error = %v", err)
	}
	entries, _, err := cmd.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatalf("detail error = %v", err)
	}
	var routes []string
	for _, entry := range entries {
		label := stripANSI(entry.Label)
		switch {
		case entry.Value == settingsNoopValue && strings.Contains(label, "Single Keys"):
			routes = append(routes, "single-keys-state\t"+entry.Value)
		case entry.Value == settingsNoopValue && strings.Contains(label, "Sequences"):
			routes = append(routes, "sequences-state\t"+entry.Value)
		case strings.Contains(entry.Value, ":sequence:"):
			routes = append(routes, "sequence-detail\t"+entry.Value+"\tdisplay="+keybindingSequenceDisplay("C-k C-p"))
		case strings.HasSuffix(entry.Value, ":add"):
			routes = append(routes, "add-binding\t"+entry.Value)
		case strings.HasSuffix(entry.Value, ":type"):
			routes = append(routes, "type-binding\t"+entry.Value)
		case strings.HasSuffix(entry.Value, ":unbind"):
			routes = append(routes, "unbind-single-keys\t"+entry.Value)
		case strings.HasSuffix(entry.Value, ":reset"):
			routes = append(routes, "reset-binding\t"+entry.Value)
		}
	}
	got := strings.Join(routes, "\n") + "\n"
	want, err := os.ReadFile(filepath.Join("testdata", "settings-keybinding-sequence.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("sequence action-detail golden mismatch:\ngot:\n%swant:\n%s", got, want)
	}

	ko := keybindingCorrectnessCommand(t, home, nil)
	ko.lookupEnv = func(name string) string {
		if name == "PROJMUX_LOCALE" {
			return "ko-KR"
		}
		return ""
	}
	koEntries, _, err := ko.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatalf("Korean detail error = %v", err)
	}
	for _, want := range []string{"단일 키", "시퀀스", "+ 바인딩 추가", "바인딩 직접 입력", "C-k,C-p"} {
		if !hasEntryLabelContaining(koEntries, want) {
			t.Fatalf("Korean detail = %#v, want %q", koEntries, want)
		}
	}
}

func TestSettingsUnifiedBindingCandidateLengthAndDisplayContract(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		input     string
		canonical string
		display   string
	}{
		{input: "C-k", canonical: "C-k", display: "C-k"},
		{input: "C-o,o", canonical: "C-o o", display: "C-o,o"},
		{input: "C-o o", canonical: "C-o o", display: "C-o,o"},
		{input: "C-k,C-p,o,F12", canonical: "C-k C-p o F12", display: "C-k,C-p,o,F12"},
	} {
		candidate, err := normalizeKeybindingAuthoringCandidate(tc.input)
		if err != nil || candidate.Canonical != tc.canonical {
			t.Fatalf("normalize candidate %q = %#v, %v, want %q", tc.input, candidate, err, tc.canonical)
		}
		if got := keybindingSequenceDisplay(candidate.Canonical); got != tc.display {
			t.Fatalf("display %q = %q, want %q", tc.input, got, tc.display)
		}
	}
	for _, input := range []string{"o", "C-o ,", "C-k C-p o F12 M-x", "C-k,Enter"} {
		if _, err := normalizeKeybindingAuthoringCandidate(input); err == nil {
			t.Fatalf("normalize candidate %q succeeded, want rejection", input)
		}
	}
}

func TestSettingsSequenceCaptureAndTypedProduceSameV2BytesAndBinding(t *testing.T) {
	t.Parallel()

	captureHome := t.TempDir()
	captured := keybindingCorrectnessCommand(t, captureHome, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		if options.UI != "settings-keybinding-recorder" || options.Recorder == nil {
			t.Fatalf("capture options = %#v", options)
		}
		return intpickercompat.Result{Key: "enter", Value: "C-k C-p"}, nil
	})
	captured.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/test,1,0"
		}
		return ""
	}
	var capturedCalls [][]string
	captured.runCommand = func(name string, args ...string) error {
		capturedCalls = append(capturedCalls, append([]string{name}, args...))
		return nil
	}
	if err := captured.runKeybindingRecorder("ProjectSidebarToggle", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("capture recorder error = %v", err)
	}

	typedHome := t.TempDir()
	typed := keybindingCorrectnessCommand(t, typedHome, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		if options.UI != "settings-keybinding-type" || !options.AcceptQuery {
			t.Fatalf("typed options = %#v", options)
		}
		return intpickercompat.Result{Key: "enter", Query: "C-k,C-p"}, nil
	})
	typed.lookupEnv = captured.lookupEnv
	var typedCalls [][]string
	typed.runCommand = func(name string, args ...string) error {
		typedCalls = append(typedCalls, append([]string{name}, args...))
		return nil
	}
	if err := typed.runKeybindingTyped("ProjectSidebarToggle", false, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("typed sequence error = %v", err)
	}

	for _, relative := range []string{"keymap.toml", "tmux.conf"} {
		captureBytes, err := os.ReadFile(filepath.Join(captureHome, ".config", "projmux", relative))
		if err != nil {
			t.Fatalf("read captured %s: %v", relative, err)
		}
		typedBytes, err := os.ReadFile(filepath.Join(typedHome, ".config", "projmux", relative))
		if err != nil {
			t.Fatalf("read typed %s: %v", relative, err)
		}
		if !bytes.Equal(captureBytes, typedBytes) {
			t.Fatalf("%s differs:\ncapture=%q\ntyped=%q", relative, captureBytes, typedBytes)
		}
	}
	if len(capturedCalls) != 1 || len(typedCalls) != 1 || len(capturedCalls[0]) != 3 || len(typedCalls[0]) != 3 ||
		strings.Join(capturedCalls[0][:2], " ") != strings.Join(typedCalls[0][:2], " ") ||
		filepath.Base(capturedCalls[0][2]) != filepath.Base(typedCalls[0][2]) {
		t.Fatalf("live reload calls capture=%#v typed=%#v", capturedCalls, typedCalls)
	}
}

func TestSettingsUnifiedBindingReplaceIsLengthDrivenAndAtomic(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := keybindingCorrectnessCommand(t, home, nil)
	if err := cmd.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("seed sequence error = %v", err)
	}
	single, err := normalizeKeybindingAuthoringCandidate("C-r")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.saveKeybindingCandidateAndApply("ProjectSidebarToggle", single, "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("sequence-to-single replace error = %v", err)
	}
	keymap := readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if !strings.Contains(keymap, `keys = ["M-1", "C-r"]`) || strings.Contains(keymap, "C-k C-p") {
		t.Fatalf("sequence-to-single keymap = %q", keymap)
	}

	sequence, err := normalizeKeybindingAuthoringCandidate("C-o,o")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.saveKeybindingCandidateAndApply("ProjectSidebarToggle", sequence, "C-r", &bytes.Buffer{}); err != nil {
		t.Fatalf("single-to-sequence replace error = %v", err)
	}
	keymap = readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if strings.Contains(keymap, `"C-r"`) || !strings.Contains(keymap, `sequences = ["C-o o"]`) || strings.Contains(keymap, "C-o,o") {
		t.Fatalf("single-to-sequence keymap = %q, want space storage only", keymap)
	}
	entries, title, err := cmd.keybindingSequenceDetailEntries("ProjectSidebarToggle", "C-o o")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "C-o,o") || !hasEntryLabelContaining(entries, "C-o,o") ||
		!hasEntryValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:sequence-remove:C-o o") {
		t.Fatalf("sequence detail title=%q entries=%#v, want comma display and space route", title, entries)
	}
}

func TestSettingsSequenceAddReplaceRemoveConflictAndNoLiveRecovery(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := keybindingCorrectnessCommand(t, home, nil)
	var out bytes.Buffer
	if err := cmd.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &out); err != nil {
		t.Fatalf("add error = %v", err)
	}
	for _, want := range []string{"Saved: ok", "Prepared: ok", "Running session: skipped", "projmux tmux apply"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("apply report = %q, want %q", out.String(), want)
		}
	}
	replacement, err := normalizeKeybindingAuthoringCandidate("C-k C-s")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.saveKeybindingCandidateAndApply("ProjectSidebarToggle", replacement, "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("replace error = %v", err)
	}
	beforeConflict := settingsNavConfigSnapshot(t, home)
	if err := cmd.validateKeymapSequenceForAction("NotifySidebarToggle", "C-k C-s F12", ""); err == nil || !strings.Contains(err.Error(), "strict-prefix") {
		t.Fatalf("strict-prefix validation error = %v", err)
	}
	if after := settingsNavConfigSnapshot(t, home); after != beforeConflict {
		t.Fatalf("conflict validation mutated config")
	}
	if err := cmd.removeKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-s", &bytes.Buffer{}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	keymap, err := os.ReadFile(filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if err != nil {
		t.Fatalf("read keymap: %v", err)
	}
	if strings.Contains(string(keymap), "sequences") {
		t.Fatalf("keymap = %q, want sequence field reset after final remove", keymap)
	}
}

func TestSettingsSequenceRemovalRetiresLiveTrieBeforeReload(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	seed := keybindingCorrectnessCommand(t, home, nil)
	if err := seed.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("seed sequence error = %v", err)
	}
	table := keySequenceTableName([]string{"C-k"})
	cmd := keybindingCorrectnessCommand(t, home, nil)
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/projmux,1,0"
		}
		return ""
	}
	var reads [][]string
	cmd.runOutput = func(name string, args ...string) ([]byte, error) {
		reads = append(reads, append([]string{name}, args...))
		switch args[len(args)-1] {
		case tmuxSequenceRootsOption:
			return []byte("C-k\n"), nil
		case tmuxSequenceTablesOption:
			return []byte(table + "\n"), nil
		default:
			return nil, nil
		}
	}
	var calls [][]string
	cmd.runCommand = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	if err := cmd.removeKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("remove sequence error = %v", err)
	}
	if len(reads) != 2 || reads[0][len(reads[0])-1] != tmuxSequenceRootsOption || reads[1][len(reads[1])-1] != tmuxSequenceTablesOption {
		t.Fatalf("state reads = %#v, want roots then tables", reads)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	wantCalls := [][]string{
		{"tmux", "unbind-key", "-q", "-n", "C-k"},
		{"tmux", "unbind-key", "-a", "-q", "-T", table},
		{"tmux", "source-file", configPath},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("live calls = %#v, want %#v", calls, wantCalls)
	}
	for i := range wantCalls {
		if strings.Join(calls[i], "\x00") != strings.Join(wantCalls[i], "\x00") {
			t.Fatalf("live call %d = %#v, want %#v", i, calls[i], wantCalls[i])
		}
	}
}

func TestSettingsSequenceConflictStaysInRecorderWithoutMutation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	seed := keybindingCorrectnessCommand(t, home, nil)
	if err := seed.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("seed sequence error = %v", err)
	}
	before := settingsNavConfigSnapshot(t, home)
	cmd := keybindingCorrectnessCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		if options.UI != "settings-keybinding-recorder" || options.Recorder == nil {
			t.Fatalf("options = %#v, want unified recorder", options)
		}
		if err := options.Recorder.Validate("C-k C-p"); err == nil || !strings.Contains(err.Error(), "bound to both") {
			t.Fatalf("recorder conflict = %v", err)
		}
		return intpickercompat.Result{Key: "esc"}, nil
	})
	var tmuxCalls [][]string
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	if err := cmd.runKeybindingRecorder("NotifySidebarToggle", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("conflicting recorder error = %v", err)
	}
	if len(tmuxCalls) != 0 {
		t.Fatalf("tmux calls=%#v, want retry/cancel with zero live calls", tmuxCalls)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("conflict changed config:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestSettingsSequenceNavigationCancelAndTestAreNoWrite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := keybindingCorrectnessCommand(t, home, nil)
	before := settingsNavConfigSnapshot(t, home)
	if _, _, err := cmd.keybindingDetailEntries("ProjectSidebarToggle"); err != nil {
		t.Fatalf("detail render error = %v", err)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("navigation mutated config")
	}

	calls := 0
	cmd = keybindingCorrectnessCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		return intpickercompat.Result{Key: "esc"}, nil
	})
	var tmuxCalls [][]string
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	if err := cmd.runKeybindingRecorder("ProjectSidebarToggle", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("cancel recorder error = %v", err)
	}
	if calls != 1 || len(tmuxCalls) != 0 || settingsNavConfigSnapshot(t, home) != before {
		t.Fatalf("cancel calls=%d tmux=%#v snapshot changed=%v", calls, tmuxCalls, settingsNavConfigSnapshot(t, home) != before)
	}

	seed := keybindingCorrectnessCommand(t, home, nil)
	if err := seed.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("seed sequence error = %v", err)
	}
	beforeTest := settingsNavConfigSnapshot(t, home)
	step := 0
	cmd = keybindingCorrectnessCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		step++
		if options.UI != "settings-keybinding-sequence-stroke" || options.Recorder == nil || !options.Recorder.AutoConfirm {
			t.Fatalf("delivery step %d options = %#v", step, options)
		}
		if step == 1 {
			return intpickercompat.Result{Key: "enter", Value: "C-k"}, nil
		}
		return intpickercompat.Result{Key: "enter", Value: "C-p"}, nil
	})
	tmuxCalls = nil
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	if err := cmd.runKeybindingSequenceDeliveryTest("ProjectSidebarToggle", "C-k C-p"); err != nil {
		t.Fatalf("delivery test error = %v", err)
	}
	if step != 2 || len(tmuxCalls) != 0 || settingsNavConfigSnapshot(t, home) != beforeTest {
		t.Fatalf("delivery steps=%d tmux=%#v snapshot changed=%v", step, tmuxCalls, settingsNavConfigSnapshot(t, home) != beforeTest)
	}
	if cmd.feedback == nil || cmd.feedback.Summary != "Sequence delivery complete" ||
		!strings.Contains(cmd.feedback.Detail, "Observed C-k,C-p exactly once") ||
		!strings.Contains(cmd.feedback.Detail, "without writes") {
		t.Fatalf("delivery feedback = %#v", cmd.feedback)
	}
}

func TestSettingsSequencePlatformDeliveryModelStaysInternal(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	base := keybindingCorrectnessCommand(t, home, nil)
	if err := base.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("seed sequence error = %v", err)
	}
	on := keybindingCorrectnessCommand(t, home, nil)
	off := keybindingCorrectnessCommand(t, home, nil)
	off.lookupEnv = func(name string) string {
		if name == "PROJMUX_NATIVE_KEYS" {
			return "0"
		}
		return ""
	}
	onDetail, _, err := on.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatalf("on detail error = %v", err)
	}
	offDetail, _, err := off.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatalf("off detail error = %v", err)
	}
	if len(onDetail) != len(offDetail) {
		t.Fatalf("authoring row count on=%d off=%d", len(onDetail), len(offDetail))
	}
	for i := range onDetail {
		if onDetail[i].Value != offDetail[i].Value {
			t.Fatalf("authoring route %d on=%q off=%q", i, onDetail[i].Value, offDetail[i].Value)
		}
	}
	onSequence, _, err := on.keybindingSequenceDetailEntries("ProjectSidebarToggle", "C-k C-p")
	if err != nil {
		t.Fatalf("on sequence detail error = %v", err)
	}
	offSequence, _, err := off.keybindingSequenceDetailEntries("ProjectSidebarToggle", "C-k C-p")
	if err != nil {
		t.Fatalf("off sequence detail error = %v", err)
	}
	for _, entries := range [][]intpickercompat.Entry{onSequence, offSequence} {
		for _, entry := range entries {
			label := stripANSI(entry.Label)
			for _, forbidden := range []string{"Cancellation", "saved logical strokes", "authoring and saved bytes"} {
				if strings.Contains(label, forbidden) {
					t.Fatalf("sequence detail=%#v, forbidden passive copy %q", entries, forbidden)
				}
			}
			if strings.Contains(label, "Delivery") && !strings.Contains(label, "Test sequence delivery") {
				t.Fatalf("sequence detail=%#v, forbidden passive Delivery row", entries)
			}
		}
	}
	for i := range onSequence {
		if onSequence[i].Value != offSequence[i].Value || onSequence[i].Label != offSequence[i].Label {
			t.Fatalf("sequence surface %d differs by platform: on=%#v off=%#v", i, onSequence[i], offSequence[i])
		}
	}
	if on.keybindingSequenceDeliveryDiagnostic("C-k C-p") == off.keybindingSequenceDeliveryDiagnostic("C-k C-p") {
		t.Fatal("internal delivery diagnostic lost its platform distinction")
	}
}
