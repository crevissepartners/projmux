package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ReasonRestoreFailed means replacement failed and the original could not be
// put back. The prepared snapshot remains at RestoreError.BackupPath.
const ReasonRestoreFailed = "profile-restore-failed"

// RestoreError preserves both failures and the location of the original bytes.
// The caller must inspect the file and retained snapshot before retrying.
type RestoreError struct {
	Name         string
	BackupPath   string
	WriteError   error
	RestoreError error
}

func (e *RestoreError) Error() string {
	return fmt.Sprintf("%s: profile %q original was not restored; original snapshot retained at %q; replacement: %v; restoration: %v", ReasonRestoreFailed, e.Name, e.BackupPath, e.WriteError, e.RestoreError)
}
func (e *RestoreError) Unwrap() []error { return []error{e.WriteError, e.RestoreError} }

// Recreate snapshots an existing, parseable profile before deleting and writing
// it through the existing store operations. A failed Write restores only this
// call's original bytes. Instructions and role claims are not revalidated on
// restoration. It does not serialize concurrent external edits.
func (s Store) Recreate(name string, content []byte) (Entry, error) {
	return s.recreate(name, content, s.Delete, s.Write, os.Rename)
}

// Operation arguments allow deterministic I/O failure tests at the destructive
// boundaries; production always supplies the unchanged Delete and Write paths.
func (s Store) recreate(name string, content []byte, remove func(string) error, write func(string, []byte) (Entry, error), restore func(string, string) error) (Entry, error) {
	path, err := s.Path(name)
	if err != nil {
		return Entry{}, err
	}
	if len(content) > MaxSize {
		return Entry{}, &Error{Reason: ReasonTooLarge, Name: name, Detail: fmt.Sprintf("is more than %d bytes", MaxSize)}
	}
	original, originalMode, err := s.recreateSnapshot(name, path)
	if err != nil {
		return Entry{}, err
	}
	if _, err := Parse(original); err != nil {
		return Entry{}, named(err, name)
	}
	backup, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".restore-*")
	if err != nil {
		return Entry{}, err
	}
	backupPath := backup.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(backupPath)
		}
	}()
	if _, err := backup.Write(original); err != nil {
		_ = backup.Close()
		return Entry{}, err
	}
	if err := backup.Chmod(originalMode); err != nil {
		_ = backup.Close()
		return Entry{}, err
	}
	if err := backup.Sync(); err != nil {
		_ = backup.Close()
		return Entry{}, err
	}
	if err := backup.Close(); err != nil {
		return Entry{}, err
	}
	if err := remove(name); err != nil {
		return Entry{}, err
	}
	entry, writeErr := write(name, content)
	if writeErr == nil {
		return entry, nil
	}
	if restoreErr := restore(backupPath, path); restoreErr != nil {
		keep = true
		return Entry{}, &RestoreError{Name: name, BackupPath: backupPath, WriteError: writeErr, RestoreError: restoreErr}
	}
	return Entry{}, writeErr
}

// Keep errors traversable even when both underlying operations are I/O errors.
var _ interface{ Unwrap() []error } = (*RestoreError)(nil)

// Read bytes and mode from the same opened file, using Load's os.Root boundary
// and missing/nonregular semantics. In-directory symlinks retain those semantics.
func (s Store) recreateSnapshot(name, path string) ([]byte, os.FileMode, error) {
	missing := func() error { return &Error{Reason: ReasonNotFound, Name: name, Detail: "does not exist at " + path} }
	root, err := os.OpenRoot(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, missing()
	}
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	file, err := root.Open(name + FileExt)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, missing()
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, missing()
	}
	content, err := ReadLimited(file)
	if err != nil {
		return nil, 0, named(err, name)
	}
	return content, info.Mode(), nil
}
