package codexupgrade

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
)

func TestQualificationStoreListPreservesPairOrderAndLoadVerdicts(t *testing.T) {
	store := NewQualificationStateStore(t.TempDir())
	pairs := []codexgeneration.VersionPair{
		{Old: "0.152.0", New: "0.152.1"},
		{Old: "0.153.2", New: "0.153.4"},
	}
	for i := len(pairs) - 1; i >= 0; i-- {
		if err := store.Save(storeReceipt(pairs[i])); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.List()
	if err != nil || len(entries) != len(pairs) {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	for i, entry := range entries {
		loaded, found, err := store.Load(pairs[i])
		if entry.Versions != pairs[i] || entry.Err != nil || !found || err != nil || entry.Result != loaded {
			t.Fatalf("entry = %+v, Load = %+v, %t, %v", entry, loaded, found, err)
		}
	}
}

func TestQualificationStoreListDistinguishesAbsentDamagedAndPairMismatch(t *testing.T) {
	pair := codexgeneration.VersionPair{Old: "0.153.2", New: "0.153.4"}
	for _, name := range []string{"absent", "empty", "corrupt", "mismatch", "invalid-name", "noncanonical-name", "directory", "symlink", "unreadable-store"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store := NewQualificationStateStore(root)
			path, err := store.Path(pair)
			if err != nil {
				t.Fatal(err)
			}
			if name != "absent" {
				if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "empty":
				err = os.WriteFile(filepath.Join(store.Dir(), ".qualification-unfinished.tmp"), []byte("{"), 0o600)
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0o600)
			case "mismatch":
				body, encodeErr := storeReceipt(codexgeneration.VersionPair{Old: "0.152.0", New: "0.152.1"}).JSON()
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				err = os.WriteFile(path, body, 0o600)
			case "invalid-name", "noncanonical-name":
				file := "private-name.json"
				if name == "noncanonical-name" {
					file = pair.Old + "_" + pair.New + "_extra.json"
				}
				err = os.WriteFile(filepath.Join(store.Dir(), file), []byte("{}"), 0o600)
			case "directory":
				err = os.Mkdir(path, 0o700)
			case "symlink":
				err = os.Symlink(filepath.Join(root, "absent-target"), path)
			case "unreadable-store":
				if err := os.Remove(store.Dir()); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(store.Dir(), []byte("not a directory"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			entries, err := store.List()
			if name == "unreadable-store" {
				if err == nil {
					t.Fatalf("unreadable store was reported as %+v", entries)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "absent" || name == "empty" {
				if len(entries) != 0 {
					t.Fatalf("empty store = %+v", entries)
				}
				return
			}
			if len(entries) != 1 || entries[0].Err == nil {
				t.Fatalf("damaged entry = %+v", entries)
			}
			entry := entries[0]
			if errors.Is(entry.Err, ErrQualificationVersionPairMismatch) != (name == "mismatch") {
				t.Fatalf("mismatch classification = %v", entry.Err)
			}
			wantPair := pair
			if name == "invalid-name" || name == "noncanonical-name" {
				wantPair = codexgeneration.VersionPair{}
			}
			if entry.Versions != wantPair || !reflect.DeepEqual(entry.Result, codexgeneration.QualificationResult{}) {
				t.Fatalf("failed entry exposed a receipt or an invalid pair: %+v", entry)
			}
		})
	}
}
