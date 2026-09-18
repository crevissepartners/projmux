// Package persona owns the persona files an Agent can be started with.
//
// A persona is a user-edited UTF-8 text file appended to a provider's system
// prompt when an Agent is created. This package is the only owner of where
// those files live, what a persona may be named, how large one may be, how its
// content is identified (the digest), and the content-addressed snapshot a
// create hands to the provider. The CLI, the web backend, create, and resume
// all call it; none of them builds a persona path or a digest on its own.
//
// Two directories are involved and they are deliberately different tiers:
//
//   - <ConfigDir>/personas/<name>.md is what the user edits. Editing or
//     deleting it never changes an Agent that already started.
//   - <StateDir>/personas/sha256-<hex>.md is the snapshot a create copied the
//     content to. The provider is given this path, and the Agent records its
//     digest, so a later resume can hand the provider the same bytes.
package persona

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/state"
)

// MaxSize is the largest persona content accepted, in bytes (64 KiB).
const MaxSize = 64 * 1024

// DirName is the directory below both ConfigDir and StateDir that holds
// persona files and persona snapshots respectively.
const DirName = "personas"

// FileExt is the extension of every persona file and snapshot.
const FileExt = ".md"

// DigestPrefix is the algorithm prefix of every persona digest.
const DigestPrefix = "sha256:"

// snapshotPrefix is the file-name spelling of DigestPrefix. A colon is not a
// portable file-name character, so the snapshot of sha256:<hex> is
// sha256-<hex>.md.
const snapshotPrefix = "sha256-"

// Refusal reason tokens. They are stable strings: every refusal this package
// or a persona consumer reports carries exactly one of them in its error text,
// so callers and operators can match the reason without parsing prose.
const (
	ReasonNameInvalid         = "persona-name-invalid"
	ReasonNotFound            = "persona-not-found"
	ReasonTooLarge            = "persona-too-large"
	ReasonProviderUnsupported = "persona-provider-unsupported"
	// ReasonUnavailable is not a refusal: a resume whose recorded snapshot
	// cannot be handed to the provider proceeds without the persona and
	// discloses this token.
	ReasonUnavailable = "persona-unavailable"
)

// Error is a persona refusal. Reason is one of the Reason* tokens.
type Error struct {
	Reason string
	Name   string
	Detail string
}

func (e *Error) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("%s: %s", e.Reason, e.Detail)
	}
	return fmt.Sprintf("%s: persona %q %s", e.Reason, e.Name, e.Detail)
}

// ReasonOf returns the refusal token carried by err, or "" when err is not a
// persona refusal.
func ReasonOf(err error) string {
	var refusal *Error
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	return ""
}

// ValidateName applies the persona name rule: a valid Projmux resource name
// (coremetadata.ValidateName, which already refuses path separators, "." and
// "..") that also does not start with "." (a hidden file) or "-" (read as a
// flag). Case is preserved and significant.
func ValidateName(name string) error {
	if err := coremetadata.ValidateName(name); err != nil {
		return &Error{Reason: ReasonNameInvalid, Name: name, Detail: "is not a valid name: " + err.Error()}
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") {
		return &Error{Reason: ReasonNameInvalid, Name: name, Detail: "must not start with \".\" or \"-\""}
	}
	return nil
}

// Digest returns the persona digest of content: sha256:<lowercase hex> over the
// raw bytes.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// Entry describes one stored persona file.
type Entry struct {
	Name    string
	Path    string
	Digest  string
	Size    int64
	ModTime time.Time
}

// Persona is one persona's name and exact content.
type Persona struct {
	Name    string
	Content []byte
	Digest  string
}

// Snapshot is one content-addressed snapshot on disk.
type Snapshot struct {
	Digest string
	Path   string
}

// Store reads and writes persona files and snapshots below two directories.
type Store struct {
	dir         string
	snapshotDir string
}

// NewStore builds a store over configDir/personas and stateDir/personas.
func NewStore(configDir, stateDir string) Store {
	return Store{
		dir:         filepath.Join(configDir, DirName),
		snapshotDir: filepath.Join(stateDir, DirName),
	}
}

// NewDefaultStore builds a store from resolved projmux paths.
func NewDefaultStore(paths config.Paths) Store {
	return NewStore(paths.ConfigDir, paths.StateDir)
}

// Dir is the directory holding the user-edited persona files.
func (s Store) Dir() string { return s.dir }

// Path returns the file path of the persona named name.
func (s Store) Path(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, name+FileExt), nil
}

// Load reads one persona exactly as stored. A missing file is
// persona-not-found, and a file larger than MaxSize is persona-too-large: a
// file written by hand past the limit is refused rather than truncated.
func (s Store) Load(name string) (Persona, error) {
	path, err := s.Path(name)
	if err != nil {
		return Persona{}, err
	}
	file, err := openIn(s.dir, name+FileExt)
	if errors.Is(err, fs.ErrNotExist) {
		return Persona{}, &Error{Reason: ReasonNotFound, Name: name, Detail: "does not exist at " + path}
	}
	if err != nil {
		return Persona{}, fmt.Errorf("read persona %q: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Persona{}, fmt.Errorf("read persona %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return Persona{}, &Error{Reason: ReasonNotFound, Name: name, Detail: "is not a regular file at " + path}
	}
	content, err := ReadLimited(file)
	if err != nil {
		var refusal *Error
		if errors.As(err, &refusal) {
			refusal.Name = name
			return Persona{}, refusal
		}
		return Persona{}, fmt.Errorf("read persona %q: %w", name, err)
	}
	return Persona{Name: name, Content: content, Digest: Digest(content)}, nil
}

// ReadLimited reads r to the end, refusing content larger than MaxSize with
// persona-too-large. It never reads more than MaxSize+1 bytes, so an unbounded
// input (stdin, a large file) costs at most that much.
func ReadLimited(r io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxSize {
		return nil, tooLarge("", MaxSize+1, true)
	}
	return content, nil
}

func tooLarge(name string, size int, atLeast bool) error {
	qualifier := ""
	if atLeast {
		qualifier = "at least "
	}
	return &Error{Reason: ReasonTooLarge, Name: name,
		Detail: fmt.Sprintf("is %s%d bytes; the limit is %d bytes", qualifier, size, MaxSize)}
}

// Write replaces the persona named name with content, atomically: the bytes go
// to a temporary file in the same directory which is then renamed over the
// target, so a reader sees either the old file or the new one and never a
// partial write. The file is 0600 and its directory 0700. An invalid name or
// oversized content is refused before anything is written.
func (s Store) Write(name string, content []byte) (Entry, error) {
	path, err := s.Path(name)
	if err != nil {
		return Entry{}, err
	}
	if len(content) > MaxSize {
		return Entry{}, tooLarge(name, len(content), false)
	}
	if err := writeAtomic(path, content); err != nil {
		return Entry{}, fmt.Errorf("write persona %q: %w", name, err)
	}
	entry := Entry{Name: name, Path: path, Digest: Digest(content), Size: int64(len(content))}
	if info, err := os.Stat(path); err == nil {
		entry.ModTime = info.ModTime()
	}
	return entry, nil
}

// Delete removes the persona named name. Snapshots are never removed: an Agent
// that started with this persona keeps its content.
func (s Store) Delete(name string) error {
	path, err := s.Path(name)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return &Error{Reason: ReasonNotFound, Name: name, Detail: "does not exist at " + path}
	}
	if err != nil {
		return fmt.Errorf("delete persona %q: %w", name, err)
	}
	defer root.Close()
	fileName := name + FileExt
	info, err := root.Lstat(fileName)
	if errors.Is(err, fs.ErrNotExist) {
		return &Error{Reason: ReasonNotFound, Name: name, Detail: "does not exist at " + path}
	}
	if err != nil {
		return fmt.Errorf("delete persona %q: %w", name, err)
	}
	if info.IsDir() {
		return &Error{Reason: ReasonNotFound, Name: name, Detail: "is not a regular file at " + path}
	}
	if err := root.Remove(fileName); err != nil {
		return fmt.Errorf("delete persona %q: %w", name, err)
	}
	return nil
}

// List returns every stored persona sorted by name. A missing directory is an
// empty list. Files whose name is not a valid persona name -- including the
// hidden temporary files an in-flight Write uses -- are not personas and are
// skipped.
func (s Store) List() ([]Entry, error) {
	dirEntries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list personas: %w", err)
	}
	entries := make([]Entry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		fileName := dirEntry.Name()
		name, ok := strings.CutSuffix(fileName, FileExt)
		if !ok || ValidateName(name) != nil {
			continue
		}
		entry, ok, err := describe(s.dir, name)
		if err != nil {
			return nil, err
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// describe stats and hashes one persona file. A path that vanished between
// the directory read and the stat, or that is not a regular file, is skipped.
func describe(dir, name string) (Entry, bool, error) {
	path := filepath.Join(dir, name+FileExt)
	file, err := openIn(dir, name+FileExt)
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("list personas: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Entry{}, false, fmt.Errorf("list personas: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Entry{}, false, nil
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return Entry{}, false, fmt.Errorf("list personas: %w", err)
	}
	return Entry{
		Name:    name,
		Path:    path,
		Digest:  DigestPrefix + hex.EncodeToString(hash.Sum(nil)),
		Size:    size,
		ModTime: info.ModTime(),
	}, true, nil
}

// SnapshotPath returns the snapshot path for digest, whether or not the
// snapshot exists. digest must be sha256:<64 lowercase hex>. It is how a
// consumer that holds only a recorded digest (a resume reading the Agent's
// persona-digest annotation) finds the bytes the Agent started with; the
// file's content hashes to its name unless it was edited by hand.
func (s Store) SnapshotPath(digest string) (string, error) {
	hexDigest, ok := strings.CutPrefix(digest, DigestPrefix)
	if !ok || len(hexDigest) != sha256.Size*2 || strings.ToLower(hexDigest) != hexDigest {
		return "", fmt.Errorf("persona digest %q is not %s<64 lowercase hex>", digest, DigestPrefix)
	}
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return "", fmt.Errorf("persona digest %q is not %s<64 lowercase hex>", digest, DigestPrefix)
	}
	return filepath.Join(s.snapshotDir, snapshotPrefix+hexDigest+FileExt), nil
}

// RecordedSnapshotPath returns the snapshot a recorded digest names, and only
// when that snapshot is a regular file directly inside the snapshot directory.
// It is how a resume finds the bytes an Agent started with from the digest the
// Agent recorded; the current persona file is never consulted, so editing or
// deleting it cannot change what a resumed Agent gets.
//
// Every failure is persona-unavailable: no recorded digest, a malformed one, a
// missing snapshot, or a path that is not a regular file (a symlink included).
// The cost is one stat. The content is deliberately not re-hashed: a snapshot
// edited by hand is outside what this package guarantees.
func (s Store) RecordedSnapshotPath(digest string) (string, error) {
	if digest == "" {
		return "", &Error{Reason: ReasonUnavailable, Detail: "has no recorded digest"}
	}
	path, err := s.SnapshotPath(digest)
	if err != nil {
		return "", &Error{Reason: ReasonUnavailable, Detail: "has an unusable recorded digest: " + err.Error()}
	}
	root, err := os.OpenRoot(s.snapshotDir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", &Error{Reason: ReasonUnavailable, Detail: "has no snapshot at " + path}
	}
	if err != nil {
		return "", &Error{Reason: ReasonUnavailable, Detail: "snapshot is unreadable: " + err.Error()}
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return "", &Error{Reason: ReasonUnavailable, Detail: "has no snapshot at " + path}
	}
	if err != nil {
		return "", &Error{Reason: ReasonUnavailable, Detail: "snapshot is unreadable: " + err.Error()}
	}
	if !info.Mode().IsRegular() {
		return "", &Error{Reason: ReasonUnavailable, Detail: "snapshot is not a regular file at " + path}
	}
	return path, nil
}

// WriteSnapshot stores content under its digest and returns the snapshot. It
// is idempotent: a snapshot that already holds exactly these bytes is left as
// it is, and one whose bytes differ from its name is atomically replaced.
func (s Store) WriteSnapshot(content []byte) (Snapshot, error) {
	if len(content) > MaxSize {
		return Snapshot{}, tooLarge("", len(content), false)
	}
	digest := Digest(content)
	path, err := s.SnapshotPath(digest)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshotHolds(s.snapshotDir, filepath.Base(path), content) {
		state.RepairPrivateFile(path)
		return Snapshot{Digest: digest, Path: path}, nil
	}
	if err := writeAtomic(path, content); err != nil {
		return Snapshot{}, fmt.Errorf("write persona snapshot: %w", err)
	}
	return Snapshot{Digest: digest, Path: path}, nil
}

// snapshotHolds reports whether the snapshot file name in dir already holds
// exactly content. Any read failure -- including a missing directory -- is
// "no", which makes the caller write the snapshot.
func snapshotHolds(dir, name string, content []byte) bool {
	file, err := openIn(dir, name)
	if err != nil {
		return false
	}
	defer file.Close()
	existing, err := io.ReadAll(io.LimitReader(file, MaxSize+1))
	return err == nil && bytes.Equal(existing, content)
}

// openIn opens name inside dir through an os.Root, so the open cannot leave
// dir: not through "..", and not through a symlink that points outside it. A
// missing dir reports fs.ErrNotExist like a missing file.
func openIn(dir, name string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(name)
}

// writeAtomic writes content to a hidden temporary file beside path and
// renames it into place. The directory is created 0700 and the file is 0600.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := state.EnsurePrivateDir(dir); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(state.PrivateFileMode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	committed = true
	state.RepairPrivateFile(path)
	return nil
}
