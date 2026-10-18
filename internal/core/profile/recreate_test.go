package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRecreateRestoresOriginalModeAndCleansBackup(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0640} {
		for _, phase := range []string{"success", "delete", "write"} {
			t.Run(fmt.Sprintf("%04o/%s", mode, phase), func(t *testing.T) {
				root := t.TempDir()
				s := NewStore(root, filepath.Join(root, "state"))
				original := []byte("# exact\nmodel=\"opus\"\n")
				entry, err := s.Write("candidate", original)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(entry.Path, mode); err != nil {
					t.Fatal(err)
				}
				injected := &os.PathError{Op: phase, Path: entry.Path, Err: fs.ErrPermission}
				remove := s.Delete
				write := s.Write
				switch phase {
				case "delete":
					remove = func(string) error { return injected }
				case "write":
					write = func(string, []byte) (Entry, error) { return Entry{}, injected }
				}
				_, err = s.recreate("candidate", []byte("model=\"sonnet\""), remove, write, os.Rename)
				wantMode := mode
				if phase == "success" {
					if err != nil {
						t.Fatal(err)
					}
					wantMode = 0600
				} else {
					if !errors.Is(err, injected) {
						t.Fatal(err)
					}
					got, err := s.Load("candidate")
					if err != nil || !bytes.Equal(got.Content, original) {
						t.Fatalf("original lost %v", err)
					}
				}
				info, err := os.Stat(entry.Path)
				if err != nil || info.Mode().Perm() != wantMode {
					t.Fatalf("mode %v %v, want %04o", info, err, wantMode)
				}
				files, err := os.ReadDir(filepath.Dir(entry.Path))
				if err != nil || len(files) != 1 {
					t.Fatalf("backup residue %v %v", files, err)
				}
			})
		}
	}
}

func TestStoreRecreateReportsRestoreFailure(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root, filepath.Join(root, "state"))
	original := []byte("instructions=\"missing\"\n")
	if err := os.MkdirAll(filepath.Join(root, DirName), 0700); err != nil {
		t.Fatal(err)
	}
	path, _ := s.Path("candidate")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	writeErr := &os.PathError{Op: "write", Path: path, Err: fs.ErrPermission}
	restoreErr := &os.LinkError{Op: "rename", Old: "backup", New: path, Err: fs.ErrExist}
	_, err := s.recreate("candidate", nil, s.Delete, func(string, []byte) (Entry, error) { return Entry{}, writeErr }, func(string, string) error { return restoreErr })
	var failure *RestoreError
	if !errors.As(err, &failure) || !errors.Is(err, writeErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("lost causes %v", err)
	}
	got, err := os.ReadFile(failure.BackupPath)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("snapshot lost %v", err)
	}
	info, err := os.Stat(failure.BackupPath)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("retained mode %v %v", info, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("false restoration %v", err)
	}
}
