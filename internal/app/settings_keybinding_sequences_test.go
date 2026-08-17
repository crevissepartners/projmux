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
	for _, want := range []string{"Single Keys", "Sequences", "C-k C-p", "+ Add key", "+ Add sequence", "Enter sequence manually"} {
		if !hasEntryLabelContaining(entries, want) {
			t.Fatalf("detail entries = %#v, want %q", entries, want)
		}
	}
	if !hasEntryValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:sequence:C-k C-p") {
		t.Fatalf("detail entries = %#v, want sequence detail route", entries)
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
		case strings.Contains(entry.Value, "sequence"):
			routes = append(routes, entry.Value)
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
	for _, want := range []string{"단일 키", "시퀀스", "+ 시퀀스 추가", "시퀀스 직접 입력"} {
		if !hasEntryLabelContaining(koEntries, want) {
			t.Fatalf("Korean detail = %#v, want %q", koEntries, want)
		}
	}
}

func TestSettingsSequenceEditorLengthAndOneStrokeCaptureContract(t *testing.T) {
	t.Parallel()

	cmd := keybindingCorrectnessCommand(t, t.TempDir(), nil)
	for count := 0; count <= 4; count++ {
		strokes := []string{"C-k", "C-p", "Enter", "F12"}[:count]
		entries, _, err := cmd.keybindingSequenceEditorEntries("ProjectSidebarToggle", "", strokes)
		if err != nil {
			t.Fatalf("count %d editor entries error = %v", count, err)
		}
		if got := hasEntryValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:sequence-save"); got != (count >= 2) {
			t.Fatalf("count %d Save reachable = %v, want %v", count, got, count >= 2)
		}
		if got := hasEntryValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:sequence-capture"); got != (count < 4) {
			t.Fatalf("count %d capture reachable = %v, want %v", count, got, count < 4)
		}
	}

	var seen intpickercompat.Options
	cmd = keybindingCorrectnessCommand(t, t.TempDir(), func(options intpickercompat.Options) (intpickercompat.Result, error) {
		seen = options
		return intpickercompat.Result{Key: "enter", Value: "Enter"}, nil
	})
	stroke, cancelled, err := cmd.captureKeybindingSequenceStroke("ProjectSidebarToggle", []string{"C-k"})
	if err != nil || cancelled || stroke != "Enter" {
		t.Fatalf("capture Enter = %q, cancelled=%v, err=%v", stroke, cancelled, err)
	}
	if seen.Recorder == nil || !seen.Recorder.AutoConfirm || !seen.Recorder.CaptureEnter {
		t.Fatalf("recorder = %#v, want one-stroke auto-confirm with Enter capture", seen.Recorder)
	}
}

func TestSettingsSequenceCaptureAndTypedProduceSameV2BytesAndBinding(t *testing.T) {
	t.Parallel()

	captureHome := t.TempDir()
	prefix := settingsActionPrefixKeymap + "ProjectSidebarToggle:"
	steps := 0
	captured := keybindingCorrectnessCommand(t, captureHome, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		steps++
		switch steps {
		case 1, 3:
			if options.UI != "settings-keybinding-sequence-editor" {
				t.Fatalf("step %d UI = %q, want editor", steps, options.UI)
			}
			return intpickercompat.Result{Key: "enter", Value: prefix + "sequence-capture"}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: "C-k"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: "C-p"}, nil
		case 5:
			if !hasEntryValue(options.Entries, prefix+"sequence-save") || !hasEntryLabelContaining(options.Entries, "C-k C-p") {
				t.Fatalf("final editor = %#v, want accumulated sequence and Save", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: prefix + "sequence-save"}, nil
		default:
			t.Fatalf("unexpected capture picker step %d", steps)
			return intpickercompat.Result{}, nil
		}
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
	if err := captured.runKeybindingSequenceEditor("ProjectSidebarToggle", "", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("capture editor error = %v", err)
	}

	typedHome := t.TempDir()
	typed := keybindingCorrectnessCommand(t, typedHome, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		if options.UI != "settings-keybinding-sequence-type" || !options.AcceptQuery {
			t.Fatalf("typed options = %#v", options)
		}
		return intpickercompat.Result{Key: "enter", Query: "C-k C-p"}, nil
	})
	typed.lookupEnv = captured.lookupEnv
	var typedCalls [][]string
	typed.runCommand = func(name string, args ...string) error {
		typedCalls = append(typedCalls, append([]string{name}, args...))
		return nil
	}
	if err := typed.runKeybindingSequenceTyped("ProjectSidebarToggle", "", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
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
	if err := cmd.replaceKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", "C-k C-s", &bytes.Buffer{}); err != nil {
		t.Fatalf("replace error = %v", err)
	}
	beforeConflict := settingsNavConfigSnapshot(t, home)
	if err := cmd.validateKeymapSequenceForAction("NotifySidebarToggle", "C-k C-s Enter", ""); err == nil || !strings.Contains(err.Error(), "strict-prefix") {
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

func TestSettingsSequenceConflictStaysInEditorWithoutMutation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	seed := keybindingCorrectnessCommand(t, home, nil)
	if err := seed.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("seed sequence error = %v", err)
	}
	before := settingsNavConfigSnapshot(t, home)
	prefix := settingsActionPrefixKeymap + "NotifySidebarToggle:"
	step := 0
	cmd := keybindingCorrectnessCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		step++
		switch step {
		case 1, 3:
			return intpickercompat.Result{Key: "enter", Value: prefix + "sequence-capture"}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: "C-k"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: "C-p"}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: prefix + "sequence-save"}, nil
		case 6:
			if options.UI != "settings-keybinding-sequence-editor" ||
				!hasEntryLabelContainingAll(options.Entries, "Feedback", "Sequence failed", "bound to both") {
				t.Fatalf("post-conflict editor = %#v, want visible conflict feedback", options.Entries)
			}
			if !hasEntryLabelContaining(options.Entries, "C-k C-p") ||
				!hasEntryValue(options.Entries, prefix+"sequence-save") {
				t.Fatalf("post-conflict editor = %#v, want intact draft and retryable Save", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected picker step %d", step)
			return intpickercompat.Result{}, nil
		}
	})
	var tmuxCalls [][]string
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	if err := cmd.runKeybindingSequenceEditor("NotifySidebarToggle", "", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("conflicting editor error = %v", err)
	}
	if step != 6 || len(tmuxCalls) != 0 {
		t.Fatalf("steps=%d tmux calls=%#v, want editor recovery and zero live calls", step, tmuxCalls)
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
	if _, _, err := cmd.keybindingSequenceEditorEntries("ProjectSidebarToggle", "", []string{"C-k"}); err != nil {
		t.Fatalf("editor render error = %v", err)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("navigation mutated config")
	}

	calls := 0
	cmd = keybindingCorrectnessCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
	})
	var tmuxCalls [][]string
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	if err := cmd.runKeybindingSequenceEditor("ProjectSidebarToggle", "", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("cancel editor error = %v", err)
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
		!strings.Contains(cmd.feedback.Detail, "Observed C-k C-p exactly once") ||
		!strings.Contains(cmd.feedback.Detail, "without writes") {
		t.Fatalf("delivery feedback = %#v", cmd.feedback)
	}
}

func TestSettingsSequencePlatformModelDiffersOnlyInDeliveryDiagnostic(t *testing.T) {
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
	if !hasEntryLabelContainingAll(onSequence, "Delivery", "Native macOS") || !hasEntryLabelContainingAll(offSequence, "Delivery", "Native macOS keybindings are off") {
		t.Fatalf("delivery diagnostics on=%#v off=%#v", onSequence, offSequence)
	}
	for i := range onSequence {
		if onSequence[i].Value != offSequence[i].Value {
			t.Fatalf("sequence action route %d on=%q off=%q", i, onSequence[i].Value, offSequence[i].Value)
		}
	}
}
