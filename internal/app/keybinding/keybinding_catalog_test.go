package keybinding

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestKeyBindingActionMetadataIsCanonicalAndExhaustive(t *testing.T) {
	t.Parallel()

	seenRuntime := map[string]bool{}
	seenCanonical := map[string]bool{}
	for _, action := range DefaultKeyBindingCatalog() {
		if strings.TrimSpace(action.ID) == "" || seenRuntime[action.ID] {
			t.Fatalf("runtime action id %q is empty or duplicated", action.ID)
		}
		seenRuntime[action.ID] = true
		if strings.TrimSpace(action.CanonicalID) == "" || seenCanonical[action.CanonicalID] {
			t.Fatalf("canonical action id %q is empty or duplicated", action.CanonicalID)
		}
		seenCanonical[action.CanonicalID] = true
		if got := KeyBindingDisplayName(action); got != action.DisplayName || strings.TrimSpace(got) == "" {
			t.Fatalf("action %q display projection = %q, canonical record = %q", action.ID, got, action.DisplayName)
		}
		if got, ok := KeyBindingActionCategory(action); !ok || got != action.Category {
			t.Fatalf("action %q category projection = (%q, %v), canonical record = %q", action.ID, got, ok, action.Category)
		}
		if got, ok := KeyBindingActionSemanticsFor(action); !ok || got != action.Semantics {
			t.Fatalf("action %q semantics projection = (%#v, %v), canonical record = %#v", action.ID, got, ok, action.Semantics)
		}
		handler, ok := KeyBindingActionHandlerFor(action)
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
