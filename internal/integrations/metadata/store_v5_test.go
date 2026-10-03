package metadata

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestV4ReadOnlyMigrationAndV5UnknownKindNeverWrite(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../core/metadata/testdata/registry-v4-destination-closure.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	writeRegistryFile(t, store, string(raw))
	before := dirListing(t, filepath.Dir(store.Path()))
	for _, read := range []func() (coremetadata.Registry, error){store.LoadSnapshot, store.LoadDegradedReadOnly, store.LoadReadOnly} {
		reg, err := read()
		if err != nil || reg.SchemaVersion != 5 {
			t.Fatalf("read migration: %v %d", err, reg.SchemaVersion)
		}
		if readFile(t, store.Path()) != string(raw) || !reflect.DeepEqual(before, dirListing(t, filepath.Dir(store.Path()))) {
			t.Fatal("read-only migration saved state")
		}
	}
	result, err := store.Migrate()
	if err != nil || !result.Migrated || result.FromVersion != 4 {
		t.Fatalf("migration: %+v %v", result, err)
	}
	if readFile(t, result.BackupPath) != string(raw) {
		t.Fatal("backup changed v4 bytes")
	}
	v5 := []byte(readFile(t, store.Path()))
	unknown := bytes.Replace(v5, []byte(`"kind": "tmux"`), []byte(`"kind": "future"`), 1)
	if bytes.Equal(unknown, v5) {
		t.Fatal("no runtime kind fixture")
	}
	writeRegistryFile(t, store, string(unknown))
	before = dirListing(t, filepath.Dir(store.Path()))
	called := false
	if _, err := store.Update(func(*coremetadata.Registry) error { called = true; return nil }); !errors.Is(err, coremetadata.ErrInvalidRegistry) {
		t.Fatalf("unknown runtime kind: %v", err)
	}
	if called || readFile(t, store.Path()) != string(unknown) || !reflect.DeepEqual(before, dirListing(t, filepath.Dir(store.Path()))) {
		t.Fatal("unknown kind modified registry or entered mutation")
	}
}
