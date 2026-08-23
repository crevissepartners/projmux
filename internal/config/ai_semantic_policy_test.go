package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAISemanticPoliciesAreSeparateFromRawHookOverrides(t *testing.T) {
	root := t.TempDir()
	semanticPath := filepath.Join(root, AISemanticPoliciesFileName)
	hookPath := filepath.Join(root, AIHookActionsFileName)
	rawHooks := []byte(`{"version":1,"providers":{"codex":{"events":{"Stop":"quiet","PermissionRequest":"state"}}}}` + "\n")
	if err := os.WriteFile(hookPath, rawHooks, 0o644); err != nil {
		t.Fatal(err)
	}

	defaults, err := LoadAISemanticPoliciesFile(semanticPath)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Events[AISemanticApprovalRequired] != AISemanticNotify || defaults.Events[AISemanticResponseComplete] != AISemanticNotify {
		t.Fatalf("raw hooks inferred semantic policy: %#v", defaults)
	}
	defaults.Events[AISemanticResponseComplete] = AISemanticQuiet
	if err := SaveAISemanticPoliciesFile(semanticPath, defaults); err != nil {
		t.Fatal(err)
	}
	afterHooks, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterHooks, rawHooks) {
		t.Fatalf("semantic save changed raw hook bytes: before=%q after=%q", rawHooks, afterHooks)
	}
	loaded, err := LoadAISemanticPoliciesFile(semanticPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Events[AISemanticApprovalRequired] != AISemanticNotify || loaded.Events[AISemanticResponseComplete] != AISemanticQuiet {
		t.Fatalf("semantic roundtrip = %#v", loaded)
	}
}

func TestAISemanticPoliciesIgnoreUnknownKeysAndInvalidClosedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), AISemanticPoliciesFileName)
	if err := os.WriteFile(path, []byte(`{"events":{"approval_required":"state","response_complete":"raw-hook","future":"quiet"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAISemanticPoliciesFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 2 || got.Events[AISemanticApprovalRequired] != AISemanticStateOnly || got.Events[AISemanticResponseComplete] != AISemanticNotify {
		t.Fatalf("normalized = %#v", got)
	}
}

func TestSaveAISemanticPoliciesUsesPrivateModes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config", "projmux")
	path := filepath.Join(dir, AISemanticPoliciesFileName)
	if err := SaveAISemanticPoliciesFile(path, DefaultAISemanticPolicies()); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("semantic policy directory mode = %04o, want 0700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("semantic policy file mode = %04o, want 0600", got)
	}
}
