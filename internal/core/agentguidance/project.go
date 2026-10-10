package agentguidance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/internal/state"
)

// ProjectDirName holds Project common instructions under ConfigDir and their
// content-addressed snapshots under StateDir. Missing or whitespace-only files
// mean off; Project instructions have no built-in default.
const ProjectDirName = "project-guidance"

// ProjectStore keys instructions by stable Project UID, never its name or root.
// Load and Save accept at most MaxSize bytes (64 KiB).
type ProjectStore struct {
	configDir string
	snapshots Store
}

func NewProjectStore(configDir, stateDir string) ProjectStore {
	return ProjectStore{filepath.Join(configDir, ProjectDirName), Store{snapshotDir: filepath.Join(stateDir, ProjectDirName)}}
}

func NewDefaultProjectStore(paths config.Paths) ProjectStore {
	return NewProjectStore(paths.ConfigDir, paths.StateDir)
}

// Path validates uid before constructing its file path.
func (s ProjectStore) Path(uid string) (string, error) {
	if err := projectlinks.ValidateProjectUID(uid); err != nil {
		return "", err
	}
	return filepath.Join(s.configDir, uid+snapshotFileExt), nil
}

// Load returns exact file bytes, or off when the file is absent or whitespace.
// Non-regular files, escaping symlinks and oversized files are errors.
func (s ProjectStore) Load(uid string) (Guidance, error) {
	path, err := s.Path(uid)
	if err != nil {
		return Guidance{}, err
	}
	root, err := os.OpenRoot(s.configDir)
	if errors.Is(err, fs.ErrNotExist) {
		return Guidance{Source: SourceOff}, nil
	}
	if err != nil {
		return Guidance{}, fmt.Errorf("read Project guidance: %w", err)
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return Guidance{Source: SourceOff}, nil
	}
	if err != nil {
		return Guidance{}, fmt.Errorf("read Project guidance: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Guidance{}, fmt.Errorf("Project guidance %s is not a regular file", path)
	}
	file, err := root.Open(name)
	if err != nil {
		return Guidance{}, fmt.Errorf("read Project guidance: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, MaxSize+1))
	if err != nil {
		return Guidance{}, fmt.Errorf("read Project guidance: %w", err)
	}
	if len(content) > MaxSize {
		return Guidance{}, fmt.Errorf("Project guidance %s exceeds %d bytes", path, MaxSize)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return Guidance{Source: SourceOff}, nil
	}
	return Guidance{Text: content, Source: SourceFile, Digest: Digest(content)}, nil
}

// Save atomically writes verbatim bytes, preserving a regular file's mode.
func (s ProjectStore) Save(uid string, text []byte) error {
	path, err := s.Path(uid)
	if err != nil {
		return err
	}
	if len(text) > MaxSize {
		return fmt.Errorf("Project guidance is %d bytes; the limit is %d", len(text), MaxSize)
	}
	mode := state.PrivateFileMode
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	return writeAtomic(path, text, mode)
}

// Delete removes only this Project's file. An absent file is already deleted.
func (s ProjectStore) Delete(uid string) error {
	path, err := s.Path(uid)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.configDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("Project guidance %s is not a regular file", path)
	}
	return root.Remove(filepath.Base(path))
}

func (s ProjectStore) WriteSnapshot(text []byte) (Snapshot, error) {
	return s.snapshots.WriteSnapshot(text)
}
func (s ProjectStore) RecordedSnapshotPath(digest string) (string, error) {
	return s.snapshots.RecordedSnapshotPath(digest)
}

// WriteComposite puts Project guidance after personaContent. The result is a
// single immutable file that the link-rule and global-guidance layers can wrap.
func (s ProjectStore) WriteComposite(personaContent []byte, digest string) (Snapshot, error) {
	path, text, err := s.snapshots.readSnapshot(digest)
	if err != nil {
		return Snapshot{}, err
	}
	if len(personaContent) == 0 {
		return Snapshot{Path: path, Digest: digest}, nil
	}
	content := append(bytes.Clone(personaContent), []byte(projectlinks.CompositeSeparator)...)
	content = append(content, text...)
	compositeDigest := Digest(content)
	return s.snapshots.write(compositePrefix+compositeDigest+snapshotFileExt, compositeDigest, content)
}
