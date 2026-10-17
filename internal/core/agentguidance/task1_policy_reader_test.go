package agentguidance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

// Frozen Task1 decoder from public 3c191afbb, internal/core/agentguidance/settings.go.
// Keep the two-key reader frozen: shared policy must remain readable by an
// already-running Task1 binary, independently of the new migration receipt.
func decodeTask1Settings(content []byte) (Settings, error) {
	var settings Settings
	decoder := json.NewDecoder(bytes.NewReader(content))
	token, err := decoder.Token()
	if err != nil {
		return settings, err
	}
	if token != json.Delim('{') {
		return settings, errors.New("expected a JSON object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return settings, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return settings, fmt.Errorf("invalid or duplicate settings key %v", token)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return settings, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return settings, fmt.Errorf("settings key %s cannot be null", key)
		}
		switch key {
		case "disabled":
			err = json.Unmarshal(value, &settings.Disabled)
		case "disabledFragments":
			err = json.Unmarshal(value, &settings.DisabledFragments)
		default:
			return settings, fmt.Errorf("unknown settings key %q", key)
		}
		if err != nil {
			return settings, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return settings, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return settings, errors.New("unexpected trailing settings data")
	}
	return settings, settings.validate()
}

func TestMigratedPolicyIsReadableByTask1(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save(append(Default(), []byte("USER")...)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(Settings{DisabledFragments: []string{"x.md"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(store.SettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	oldReader, err := decodeTask1Settings(content)
	if err != nil {
		t.Fatalf("Task1 reader refuses migrated policy: %v", err)
	}
	if oldReader.Disabled || len(oldReader.DisabledFragments) != 1 || oldReader.DisabledFragments[0] != "x.md" {
		t.Fatal("old reader policy changed")
	}
}

func TestMigrationPolicyOrderAndMissingUserRemainTask1Compatible(t *testing.T) {
	for _, kind := range []string{"missing", "blank", "default only", "explicit enabled", "explicit disabled"} {
		t.Run(kind, func(t *testing.T) {
			store := newTestStore(t)
			if kind != "missing" {
				content := Default()
				if kind == "blank" {
					content = []byte(" \r\n")
				}
				if err := store.Save(content); err != nil {
					t.Fatal(err)
				}
			}
			explicit := kind == "explicit enabled" || kind == "explicit disabled"
			wantDisabled := kind == "blank" || kind == "explicit disabled"
			var originalPolicy []byte
			if explicit {
				originalPolicy = []byte("{\n  \"disabledFragments\": [\"keep.md\"], \"disabled\": " + fmt.Sprint(wantDisabled) + "\n}\n")
				if err := os.WriteFile(store.SettingsPath(), originalPolicy, 0600); err != nil {
					t.Fatal(err)
				}
			}
			checked := false
			store.migrationStep = func(step string) error {
				if step != "after-policy" {
					return nil
				}
				checked = true
				policy, err := os.ReadFile(store.SettingsPath())
				if err != nil {
					return err
				}
				oldReader, err := decodeTask1Settings(policy)
				if err != nil {
					return err
				}
				if oldReader.Disabled != wantDisabled {
					return fmt.Errorf("old reader disabled=%t want=%t", oldReader.Disabled, wantDisabled)
				}
				if explicit && (!bytes.Equal(policy, originalPolicy) || len(oldReader.DisabledFragments) != 1) {
					return errors.New("explicit policy/mask rewritten")
				}
				return os.ErrPermission
			}
			if _, err := store.Load(); err == nil || !checked {
				t.Fatal("policy boundary was not observed", err)
			}
			store.migrationStep = nil
			guidance, err := store.Load()
			if err != nil || guidance.Off() != wantDisabled {
				t.Fatal("retry changed guidance state", err)
			}
			receipt, err := store.loadMigration()
			if err != nil || receipt == nil || !receipt.Complete {
				t.Fatal("missing completion receipt", err)
			}
			info, err := os.Stat(store.migrationPath())
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("receipt is not private", err)
			}
			if kind == "missing" {
				if err := store.Save(nil); err != nil {
					t.Fatal(err)
				}
				guidance, err = store.Load()
				if err != nil || guidance.Off() {
					t.Fatal("later blank file reintroduced legacy off", err)
				}
			}
		})
	}
}

func TestTask1PolicyReaderRejectsMigrationMetadata(t *testing.T) {
	if _, err := decodeTask1Settings([]byte(`{"disabled":false,"disabledFragments":[],"migration":{"version":1,"complete":true}}`)); err == nil {
		t.Fatal("frozen old reader unexpectedly accepts extra metadata")
	}
}

func TestInvalidMigrationReceiptPreservesFiles(t *testing.T) {
	for _, text := range []string{
		`null`, `{}`, `{"version":2,"complete":true}`, `{"version":1,"complete":true,"unknown":true}`, `{"version":1,"complete":true}{}`, `{"version":1,"complete":false}`, `{"version":1,"complete":true,"originalDigest":"bad"}`,
	} {
		t.Run(text, func(t *testing.T) {
			store := newTestStore(t)
			original := []byte("USER")
			if err := store.Save(original); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.migrationPath(), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			current, _ := os.ReadFile(store.Path())
			if !bytes.Equal(current, original) {
				t.Fatal("invalid receipt modified user bytes")
			}
			if _, err := os.Stat(store.Path() + ".bak"); !os.IsNotExist(err) {
				t.Fatal("invalid receipt created a backup")
			}
		})
	}
}
