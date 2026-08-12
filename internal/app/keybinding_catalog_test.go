package app

import (
	"bytes"
	"strings"
	"testing"
)

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
