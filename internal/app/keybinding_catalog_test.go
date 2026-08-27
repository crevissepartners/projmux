package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/platformkeys"
)

func TestKeyBindingActionMetadataIsCanonicalAndExhaustive(t *testing.T) {
	t.Parallel()

	seenRuntime := map[string]bool{}
	seenCanonical := map[string]bool{}
	for _, action := range defaultKeyBindingCatalog() {
		if strings.TrimSpace(action.ID) == "" || seenRuntime[action.ID] {
			t.Fatalf("runtime action id %q is empty or duplicated", action.ID)
		}
		seenRuntime[action.ID] = true
		if strings.TrimSpace(action.CanonicalID) == "" || seenCanonical[action.CanonicalID] {
			t.Fatalf("canonical action id %q is empty or duplicated", action.CanonicalID)
		}
		seenCanonical[action.CanonicalID] = true
		if got := keyBindingDisplayName(action); got != action.DisplayName || strings.TrimSpace(got) == "" {
			t.Fatalf("action %q display projection = %q, canonical record = %q", action.ID, got, action.DisplayName)
		}
		if got, ok := keyBindingActionCategory(action); !ok || got != action.Category {
			t.Fatalf("action %q category projection = (%q, %v), canonical record = %q", action.ID, got, ok, action.Category)
		}
		if got, ok := keyBindingActionSemanticsFor(action); !ok || got != action.Semantics {
			t.Fatalf("action %q semantics projection = (%#v, %v), canonical record = %#v", action.ID, got, ok, action.Semantics)
		}
		handler, ok := keyBindingActionHandlerFor(action)
		if !ok || handler.Note != action.HandlerBoundaryNote {
			t.Fatalf("action %q handler note projection = (%q, %v), canonical record = %q", action.ID, handler.Note, ok, action.HandlerBoundaryNote)
		}
	}
}

func TestRetiredKeyBindingMetadataMapsStayAbsent(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	retiredOwners := []string{
		"keyBindingCategory" + "ByActionID",
		"keyBindingDisplay" + "Names",
		"keyBindingActionSemantics" + "ByID",
		"keyBindingActionHandler" + "Notes",
	}
	productionFiles := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		productionFiles++
		source, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, retired := range retiredOwners {
			if strings.Contains(string(source), retired) {
				t.Fatalf("retired parallel metadata owner %q reappeared in %s", retired, file)
			}
		}
	}
	if productionFiles == 0 {
		t.Fatal("retired metadata audit found no production Go files")
	}
}

func TestCatalogProjectsTerminalNativeAndProbeInventories(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	ghosttyWant := map[[2]string]int{}
	windowsWant := map[[3]string]int{}
	probeWant := map[[5]string]int{}
	var nativeChords []string
	for _, action := range catalog {
		if action.GhosttyTrigger != "" || action.GhosttyAction != "" {
			ghosttyWant[[2]string{action.GhosttyTrigger, action.GhosttyAction}]++
		}
		if action.WTID != "" {
			windowsWant[[3]string{action.WTID, action.WTKeys, action.WTInput}]++
		}
		if action.ProbeLabel != "" {
			probeWant[[5]string{action.ID, action.ProbeLabel, action.ProbeAction, action.ProbePlain, firstNonEmptyString(keyBindingEffectivePlainChords(action))}]++
		}
		if action.Kind != keyBindingActionPickerInternal {
			nativeChords = append(nativeChords, keyBindingEffectivePlainChords(action)...)
		}
	}
	nativeChords = append(nativeChords, keyBindingSequenceTransportChords(catalog)...)

	ghosttyGot := map[[2]string]int{}
	for _, binding := range ghosttyBindingsFromCatalog() {
		ghosttyGot[[2]string{binding.Trigger, binding.Action}]++
	}
	if !reflect.DeepEqual(ghosttyGot, ghosttyWant) {
		t.Fatalf("Ghostty projection = %#v, want canonical catalog inventory %#v", ghosttyGot, ghosttyWant)
	}
	windowsGot := map[[3]string]int{}
	for _, binding := range windowsTerminalBindingsFromCatalog() {
		windowsGot[[3]string{binding.ID, binding.Keys, binding.Input}]++
	}
	if !reflect.DeepEqual(windowsGot, windowsWant) {
		t.Fatalf("Windows Terminal projection = %#v, want canonical catalog inventory %#v", windowsGot, windowsWant)
	}
	probeGot := map[[5]string]int{}
	for _, key := range probeKeysFromCatalog() {
		probeGot[[5]string{key.ActionID, key.Label, key.Action, key.Plain, key.PlainChord}]++
	}
	if !reflect.DeepEqual(probeGot, probeWant) {
		t.Fatalf("probe projection = %#v, want canonical catalog inventory %#v", probeGot, probeWant)
	}

	home := t.TempDir()
	cmd := &keyBrokerCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
		readFile:  os.ReadFile,
	}
	nativeGot, err := cmd.loadBindings()
	if err != nil {
		t.Fatalf("macOS native binding projection: %v", err)
	}
	nativeWant := platformkeys.ParseBindings(nativeChords)
	if !reflect.DeepEqual(nativeGot, nativeWant) {
		t.Fatalf("macOS native projection = %#v, want canonical catalog inventory %#v", nativeGot, nativeWant)
	}
}

func TestProjectRuntimeStopCopyHasEnglishKoreanIdentityAndTopologyParity(t *testing.T) {
	t.Parallel()
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), "Sidebar:KillSession")
	if !ok {
		t.Fatal("Sidebar:KillSession missing from keybinding catalog")
	}
	semantics, ok := keyBindingActionSemanticsFor(action)
	if !ok {
		t.Fatal("Sidebar:KillSession has no semantic contract")
	}
	for _, test := range []struct {
		locale                          i18n.Locale
		wantLabel, wantDesc, wantResult string
	}{
		{locale: "en-US", wantLabel: "Stop Project Runtime (keep UID/topology)",
			wantDesc:   "Stop only the focused Project runtime; keep its Project UID and desired Window/Pane topology",
			wantResult: "stop only the Project runtime; keep its Project UID and desired Window/Pane topology"},
		{locale: "ko-KR", wantLabel: "Project 런타임 중지 (UID/토폴로지 유지)",
			wantDesc:   "포커스한 Project 런타임만 중지하고 Project UID와 desired Window/Pane 토폴로지는 유지",
			wantResult: "Project 런타임만 중지하고 Project UID와 desired Window/Pane 토폴로지는 유지"},
	} {
		if got := settingsCatalogTextLocale(test.locale, keyBindingDisplayName(action)); got != test.wantLabel {
			t.Fatalf("locale=%s Stop label=%q, want %q", test.locale, got, test.wantLabel)
		}
		if got := settingsCatalogTextLocale(test.locale, action.Description); got != test.wantDesc {
			t.Fatalf("locale=%s Stop description=%q, want %q", test.locale, got, test.wantDesc)
		}
		if got := settingsCatalogTextLocale(test.locale, semantics.ResultKind); got != test.wantResult {
			t.Fatalf("locale=%s Stop result=%q, want %q", test.locale, got, test.wantResult)
		}
	}
}

func TestRuntimeSessionStopCopyHasEnglishKoreanManagedIdentityParity(t *testing.T) {
	t.Parallel()
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), "SessionPopup:KillSession")
	if !ok {
		t.Fatal("SessionPopup:KillSession missing from keybinding catalog")
	}
	semantics, ok := keyBindingActionSemanticsFor(action)
	if !ok {
		t.Fatal("SessionPopup:KillSession has no semantic contract")
	}
	for _, test := range []struct {
		locale                          i18n.Locale
		wantLabel, wantDesc, wantResult string
	}{
		{locale: "en-US", wantLabel: "Stop Runtime Session (keep managed identity)",
			wantDesc:   "Stop only the focused runtime Session; keep managed Registry identity and desired topology",
			wantResult: "stop only the runtime Session; keep managed Registry identity and desired topology"},
		{locale: "ko-KR", wantLabel: "런타임 Session 중지 (관리 identity 유지)",
			wantDesc:   "포커스한 런타임 Session만 중지하고 관리 Registry identity와 desired 토폴로지는 유지",
			wantResult: "런타임 Session만 중지하고 관리 Registry identity와 desired 토폴로지는 유지"},
	} {
		if got := settingsCatalogTextLocale(test.locale, keyBindingDisplayName(action)); got != test.wantLabel {
			t.Fatalf("locale=%s generic Stop label=%q, want %q", test.locale, got, test.wantLabel)
		}
		if got := settingsCatalogTextLocale(test.locale, action.Description); got != test.wantDesc {
			t.Fatalf("locale=%s generic Stop description=%q, want %q", test.locale, got, test.wantDesc)
		}
		if got := settingsCatalogTextLocale(test.locale, semantics.ResultKind); got != test.wantResult {
			t.Fatalf("locale=%s generic Stop result=%q, want %q", test.locale, got, test.wantResult)
		}
	}
}

func TestGhosttyBindingsFromCatalogUsePlainMeta(t *testing.T) {
	t.Parallel()

	bindings := ghosttyBindingsFromCatalog()
	if len(bindings) == 0 {
		t.Fatal("ghosttyBindingsFromCatalog() is empty, want Alt-1..5 plain Meta mappings")
	}
	for _, binding := range bindings {
		if strings.Contains(binding.Action, "c"+"si:") {
			t.Fatalf("ghostty binding uses retired CSI action: %#v", binding)
		}
		if !strings.HasPrefix(binding.Action, `text:\x1b`) {
			t.Fatalf("ghostty binding action = %q, want plain Meta text action", binding.Action)
		}
	}
}

func TestWindowsTerminalBindingsFromCatalogDoNotUseAppCSIu(t *testing.T) {
	t.Parallel()

	bindings := windowsTerminalBindingsFromCatalog()
	if len(bindings) == 0 {
		t.Fatal("windowsTerminalBindingsFromCatalog() is empty, want managed WT bindings")
	}
	for _, binding := range bindings {
		if strings.Contains(binding.Input, "\x1b[900") || strings.Contains(binding.Input, "\x1b[901") {
			t.Fatalf("windows-terminal binding uses retired app modified-key input: %#v", binding)
		}
	}
}

// TestNewInitCommandRegistersBundledTerminals exercises the production wiring:
// the terminal remediation command built by newInitCommand must know both bundled adapters,
// which surfaces in the "unknown terminal" error's known-terminals list.
func TestNewInitCommandRegistersBundledTerminals(t *testing.T) {
	t.Parallel()

	cmd := newInitCommand()
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"no-such-terminal"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Run(no-such-terminal) error = nil, want unknown terminal error")
	}
	for _, name := range []string{"ghostty", "windows-terminal"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("unknown-terminal error %q missing bundled adapter %q", err, name)
		}
	}
}
