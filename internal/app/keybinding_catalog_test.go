package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
)

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
