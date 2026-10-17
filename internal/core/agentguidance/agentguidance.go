// Package agentguidance owns the agent guidance: the text projmux puts at the
// front of a managed Claude Agent's system prompt so the Agent works with
// other agents through projmux rather than through its provider's own
// subagents and message channels.
//
// Guidance is assembled from the built-in Default, regular *.md fragments
// in <ConfigDir>/agent-guidance.d (bytewise filename order), then the user's
// agent-guidance.md. Nonempty parts retain their bytes and are joined by two
// LF bytes. agent-guidance.json controls whole-guidance and fragment disabling.
// Without that settings file, an existing whitespace-only user file stays off.
//
// What a launch handed to Claude is content addressed below StateDir, like a
// persona snapshot:
//
//   - <StateDir>/agent-guidance/<digest>.md is the guidance a launch used; the
//     Agent records the digest.
//   - <StateDir>/agent-guidance/composite-<digest>.md is that guidance followed
//     by projectlinks.CompositeSeparator and the file the launch would
//     otherwise pass (a persona, the Project's label link rules, or their
//     composite). Claude keeps only the last --append-system-prompt-file it
//     is given, so every part must reach it as one file.
//
// The digest is the sha256 lowercase hex of the bytes the file holds.
package agentguidance

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
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/internal/state"
)

// FileName is the guidance file below ConfigDir.
const FileName = "agent-guidance.md"

// DirName is the directory below StateDir that holds guidance snapshots.
const DirName = "agent-guidance"

// MaxSize is the largest guidance accepted, in bytes (64 KiB). A file larger
// than it is refused on Load rather than truncated.
const MaxSize = 64 * 1024

// maxCompositeSize bounds a composite: the largest guidance, the separator,
// and the largest file it can be put in front of.
const maxCompositeSize = MaxSize + len(projectlinks.CompositeSeparator) + projectlinks.MaxSnapshotSize

// compositePrefix names a composite snapshot.
const compositePrefix = "composite-"

// snapshotFileExt is the extension of every guidance snapshot.
const snapshotFileExt = ".md"

// defaultText is the built-in guidance. It names only projmux commands: no
// path, host, user or machine policy, so it is the same on every machine.
const defaultText = "# Working with other agents through projmux\n" +
	"\n" +
	"You are running as an agent managed by projmux.\n" +
	"\n" +
	"- When you need another agent to take on work, create it with `projmux create agent` instead of starting a subagent built into your provider. An agent created this way gets its own pane, stays visible to the operator, and can be resumed and messaged.\n" +
	"- New CLI Agents follow `projmux config agent-host`: omitted host uses tmux by default, or detached process startup when configured as process. Explicit `--host tmux|process` overrides it; explicit process is foreground unless `--detach` is given. Agent routes and provider shortcuts accept `--detach=false` to keep inferred process foreground. Process creation supports Claude/Codex and exact scopes; unsupported tmux topology is refused.\n" +
	"- When you need to send a message to another agent, use `projmux agent message send` instead of a message channel local to your provider. projmux routes the message to the agent and keeps a record of it.\n" +
	"- Run `projmux create agent --help` and `projmux agent message send --help` for their options.\n" +
	"- When you need the user to decide or approve something, ask with your provider's question tool (Claude `AskUserQuestion`, Codex `requestUserInput`) so it reaches the user as a question they can answer directly, instead of leaving it in your reply. Put your recommended option first and say in a few words what each option does. Ask one decision per question, and check facts yourself rather than asking.\n" +
	"- An open question pauses your turn until it is answered, and messages other agents send you meanwhile wait and can expire. If other agents depend on you to keep working, for example because you coordinate their work or their merges, write the decision request in your reply and end your turn instead of using the question tool.\n"

// Default returns a copy of the built-in guidance, the first assembly part.
func Default() []byte {
	return []byte(defaultText)
}

// Source says where a Guidance came from.
type Source string

const (
	// SourceDefault is the built-in text without nonempty fragment or user additions.
	SourceDefault Source = "default"
	// SourceFile is built-in guidance supplemented by fragments or user content.
	SourceFile Source = "file"
	// SourceOff is explicit or legacy whole-guidance off.
	SourceOff Source = "off"
)

// Guidance is the guidance a launch uses. Text and Digest are empty when
// Source is SourceOff.
type Guidance struct {
	Text   []byte
	Source Source
	Digest string
}

// Off reports that there is no guidance to give.
func (g Guidance) Off() bool {
	return len(g.Text) == 0
}

// Digest returns the sha256 lowercase hex of content.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// ValidateDigest accepts exactly 64 lowercase hex characters.
func ValidateDigest(digest string) error {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return fmt.Errorf("agent guidance digest %q is not 64 lowercase hex", digest)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("agent guidance digest %q is not 64 lowercase hex", digest)
	}
	return nil
}

// Store reads and writes the guidance file and its snapshots.
type Store struct {
	configDir   string
	snapshotDir string
}

// NewStore builds a store over configDir/agent-guidance.md and
// stateDir/agent-guidance.
func NewStore(configDir, stateDir string) Store {
	return Store{configDir: configDir, snapshotDir: filepath.Join(stateDir, DirName)}
}

// NewDefaultStore builds a store from resolved projmux paths.
func NewDefaultStore(paths config.Paths) Store {
	return NewStore(paths.ConfigDir, paths.StateDir)
}

// Path is the guidance file the user edits.
func (s Store) Path() string {
	return filepath.Join(s.configDir, FileName)
}

// Load assembles the current built-in, fragment and user guidance.
// Read or size errors fail the entire assembly; callers report unavailable
// guidance and continue launching without it.
func (s Store) Load() (Guidance, error) {
	return s.load(false)
}

// LoadProcess puts process execution context at the end of the built-in
// guidance, before fragments and the user file. Whole-guidance off stays off.
func (s Store) LoadProcess() (Guidance, error) {
	return s.load(true)
}

const processText = "\n# Process host\n\n" +
	"This agent runs in a foreground process host without a tmux pane. Use `projmux agent turn`, `projmux agent message send`, and `projmux describe` to control and inspect it. Your identity comes from the process binding; a process ID is not a pane ID.\n"

// Save replaces the guidance file with text, verbatim and atomically. Text of
// only whitespace is saved as it is; it means off only without explicit
// settings. Text larger than MaxSize is refused before anything is written. An existing file keeps its mode; a
// new one is 0600.
func (s Store) Save(text []byte) error {
	if len(text) > MaxSize {
		return fmt.Errorf("agent guidance is %d bytes; the limit is %d", len(text), MaxSize)
	}
	mode := state.PrivateFileMode
	if info, err := os.Lstat(s.Path()); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(s.Path(), text, mode); err != nil {
		return fmt.Errorf("write agent guidance: %w", err)
	}
	return nil
}

// Disable turns all guidance off without changing user content or fragments.
func (s Store) Disable() error {
	settings, _, err := s.LoadSettings()
	if err != nil {
		return err
	}
	settings.Disabled = true
	return s.SaveSettings(settings)
}

// Reset removes the user file and resets the whole-guidance switch. Fragment
// settings are preserved; fragments still supplement the built-in default.
func (s Store) Reset() error {
	settings, explicit, err := s.LoadSettings()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.configDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reset agent guidance: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(FileName)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reset agent guidance: %w", err)
	}
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("reset agent guidance: %s is not a regular file", s.Path())
		}
		if err := root.Remove(FileName); err != nil {
			return fmt.Errorf("reset agent guidance: %w", err)
		}
	}
	if explicit {
		settings.Disabled = false
		if err := s.SaveSettings(settings); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot is one content-addressed guidance file on disk.
type Snapshot struct {
	Digest string
	Path   string
}

// SnapshotPath returns the path of the guidance digest names, whether or not
// it exists.
func (s Store) SnapshotPath(digest string) (string, error) {
	if err := ValidateDigest(digest); err != nil {
		return "", err
	}
	return filepath.Join(s.snapshotDir, digest+snapshotFileExt), nil
}

// WriteSnapshot stores text under its digest. It is idempotent: a snapshot
// that already holds exactly these bytes is left as it is, and one whose bytes
// differ is atomically replaced.
func (s Store) WriteSnapshot(text []byte) (Snapshot, error) {
	if len(text) == 0 {
		return Snapshot{}, errors.New("write agent guidance snapshot: the guidance is off")
	}
	if len(text) > MaxSize {
		return Snapshot{}, fmt.Errorf("write agent guidance snapshot: %d bytes; the limit is %d", len(text), MaxSize)
	}
	digest := Digest(text)
	return s.write(digest+snapshotFileExt, digest, text)
}

// RecordedSnapshotPath returns the guidance a recorded digest names, and only
// when that snapshot is a regular file directly inside the snapshot directory
// whose content still hashes to digest.
func (s Store) RecordedSnapshotPath(digest string) (string, error) {
	path, _, err := s.readSnapshot(digest)
	return path, err
}

// WriteComposite stores the guidance digest names, CompositeSeparator and
// tail, in that order, as one content-addressed file and returns it. tail is
// copied exactly; whatever file it was read from is never touched. An empty
// tail has nothing to follow the guidance, so the guidance snapshot itself is
// returned.
func (s Store) WriteComposite(digest string, tail []byte) (Snapshot, error) {
	path, guidance, err := s.readSnapshot(digest)
	if err != nil {
		return Snapshot{}, err
	}
	if len(tail) == 0 {
		return Snapshot{Digest: digest, Path: path}, nil
	}
	content := make([]byte, 0, len(guidance)+len(projectlinks.CompositeSeparator)+len(tail))
	content = append(content, guidance...)
	content = append(content, projectlinks.CompositeSeparator...)
	content = append(content, tail...)
	compositeDigest := Digest(content)
	return s.write(compositePrefix+compositeDigest+snapshotFileExt, compositeDigest, content)
}

func (s Store) readSnapshot(digest string) (string, []byte, error) {
	path, err := s.SnapshotPath(digest)
	if err != nil {
		return "", nil, err
	}
	root, err := os.OpenRoot(s.snapshotDir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("no agent guidance snapshot at %s", path)
	}
	if err != nil {
		return "", nil, fmt.Errorf("read agent guidance snapshot: %w", err)
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("no agent guidance snapshot at %s", path)
	}
	if err != nil {
		return "", nil, fmt.Errorf("read agent guidance snapshot: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("agent guidance snapshot %s is not a regular file", path)
	}
	file, err := root.Open(name)
	if err != nil {
		return "", nil, fmt.Errorf("read agent guidance snapshot: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, MaxSize+1))
	if err != nil {
		return "", nil, fmt.Errorf("read agent guidance snapshot: %w", err)
	}
	if len(content) > MaxSize || Digest(content) != digest {
		return "", nil, fmt.Errorf("agent guidance snapshot %s does not hold the content its name digests", path)
	}
	return path, content, nil
}

func (s Store) write(name, digest string, content []byte) (Snapshot, error) {
	if len(content) > maxCompositeSize {
		return Snapshot{}, fmt.Errorf("write agent guidance snapshot: %d bytes; the limit is %d", len(content), maxCompositeSize)
	}
	path := filepath.Join(s.snapshotDir, name)
	if snapshotHolds(s.snapshotDir, name, content) {
		return Snapshot{Digest: digest, Path: path}, nil
	}
	if err := writeAtomic(path, content, state.PrivateFileMode); err != nil {
		return Snapshot{}, fmt.Errorf("write agent guidance snapshot: %w", err)
	}
	return Snapshot{Digest: digest, Path: path}, nil
}

// snapshotHolds reports whether the regular file name in dir already holds
// exactly content. Any failure is "no", which makes the caller write it.
func snapshotHolds(dir, name string, content []byte) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	existing, err := io.ReadAll(io.LimitReader(file, int64(maxCompositeSize)+1))
	return err == nil && bytes.Equal(existing, content)
}

// writeAtomic writes content to a hidden temporary file beside path, syncs
// it, and renames it into place with mode. The directory is created 0700.
func writeAtomic(path string, content []byte, mode os.FileMode) error {
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
	if err := temp.Chmod(mode); err != nil {
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
	return nil
}
