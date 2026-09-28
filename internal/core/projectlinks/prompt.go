package projectlinks

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
)

// A Claude Agent's system prompt carries its Project's rules as rendered text.
// The rendering is content addressed below StateDir, like a persona snapshot:
//
//   - <StateDir>/project-links/<digest>.md is the rendered rules a launch
//     handed to Claude; the Agent records the digest.
//   - <StateDir>/project-links/composite-<digest>.md is a persona snapshot
//     followed by CompositeSeparator and the rendered rules, for a launch that
//     has both. Claude keeps only the last --append-system-prompt-file it is
//     given, so both must reach it as one file.
//
// The digest is the sha256 lowercase hex of the bytes the file holds.

// ExampleValue is the label value every rule's example link is rendered with.
const ExampleValue = "EXAMPLE-123"

// CompositeSeparator separates a persona from the rendered rules in a
// composite system prompt file.
const CompositeSeparator = "\n\n---\n\n"

// compositePrefix names a composite snapshot.
const compositePrefix = "composite-"

// snapshotFileExt is the extension of every rendered snapshot.
const snapshotFileExt = ".md"

// MaxSnapshotSize bounds every snapshot read. A rendering of the largest valid
// rule set, with a persona of at most 64 KiB in front of it, stays below it.
const MaxSnapshotSize = 1024 * 1024

// Render returns the text a Claude system prompt carries for rules, or nil
// when rules set nothing (no Jira URL, no Repo URL and no links): an empty
// rendering means "no rules". The text is deterministic: the same rules render
// the same bytes, in the stored rule order.
func Render(rules Rules) []byte {
	if rules.JiraURL == "" && rules.RepoURL == "" && len(rules.Links) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("# Project label link rules\n\n")
	b.WriteString("These are this Project's label link rules. A label is a key and a value, written key=value. ")
	b.WriteString("A label whose key has a rule below links to that rule's template, with {value} replaced by the label value (path-escaped), ")
	b.WriteString("{jira} by the Jira URL and {repo} by the Repo URL. Use these rules whenever you turn such a label into a link.\n")
	if rules.JiraURL != "" || rules.RepoURL != "" {
		b.WriteString("\n")
		if rules.JiraURL != "" {
			fmt.Fprintf(&b, "Jira URL: %s\n", rules.JiraURL)
		}
		if rules.RepoURL != "" {
			fmt.Fprintf(&b, "Repo URL: %s\n", rules.RepoURL)
		}
	}
	b.WriteString("\n")
	if len(rules.Links) == 0 {
		b.WriteString("This Project has no label rules.\n")
		return []byte(b.String())
	}
	b.WriteString("Rules:\n")
	for _, link := range rules.Links {
		example := "none (the template does not produce a link)"
		if resolved, ok := Resolve(rules, link.LabelKey, ExampleValue); ok {
			example = link.LabelKey + "=" + ExampleValue + " links to " + resolved
		}
		fmt.Fprintf(&b, "- label key %q: template %s; example: %s\n", link.LabelKey, link.Template, example)
	}
	return []byte(b.String())
}

// Digest returns the sha256 lowercase hex of rendered.
func Digest(rendered []byte) string {
	sum := sha256.Sum256(rendered)
	return hex.EncodeToString(sum[:])
}

// ValidateDigest accepts exactly 64 lowercase hex characters.
func ValidateDigest(digest string) error {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return fmt.Errorf("project link rules digest %q is not 64 lowercase hex", digest)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("project link rules digest %q is not 64 lowercase hex", digest)
	}
	return nil
}

// Snapshot is one content-addressed rendering on disk.
type Snapshot struct {
	Digest string
	Path   string
}

// SnapshotStore reads and writes rendered snapshots below one directory.
type SnapshotStore struct {
	dir string
}

// NewSnapshotStore builds a snapshot store over stateDir/project-links.
func NewSnapshotStore(stateDir string) SnapshotStore {
	return SnapshotStore{dir: filepath.Join(stateDir, DirName)}
}

// NewDefaultSnapshotStore builds a snapshot store from resolved projmux paths.
func NewDefaultSnapshotStore(paths config.Paths) SnapshotStore {
	return NewSnapshotStore(paths.StateDir)
}

// SnapshotPath returns the path of the rendered rules digest names, whether or
// not it exists.
func (s SnapshotStore) SnapshotPath(digest string) (string, error) {
	if err := ValidateDigest(digest); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, digest+snapshotFileExt), nil
}

// WriteSnapshot stores rendered under its digest. It is idempotent: a snapshot
// that already holds exactly these bytes is left as it is, and one whose bytes
// differ is atomically replaced.
func (s SnapshotStore) WriteSnapshot(rendered []byte) (Snapshot, error) {
	if len(rendered) == 0 {
		return Snapshot{}, errors.New("write project link rules snapshot: nothing is rendered")
	}
	digest := Digest(rendered)
	return s.write(digest+snapshotFileExt, digest, rendered)
}

// RecordedSnapshotPath returns the rendered rules a recorded digest names, and
// only when that snapshot is a regular file directly inside the snapshot
// directory whose content still hashes to digest.
func (s SnapshotStore) RecordedSnapshotPath(digest string) (string, error) {
	path, _, err := s.readSnapshot(digest)
	return path, err
}

// WriteComposite stores personaContent, CompositeSeparator and the rendered
// rules digest names, in that order, as one content-addressed file and returns
// it. The persona bytes are copied exactly; the persona snapshot itself is
// never touched.
func (s SnapshotStore) WriteComposite(personaContent []byte, digest string) (Snapshot, error) {
	_, rendered, err := s.readSnapshot(digest)
	if err != nil {
		return Snapshot{}, err
	}
	content := make([]byte, 0, len(personaContent)+len(CompositeSeparator)+len(rendered))
	content = append(content, personaContent...)
	content = append(content, CompositeSeparator...)
	content = append(content, rendered...)
	compositeDigest := Digest(content)
	return s.write(compositePrefix+compositeDigest+snapshotFileExt, compositeDigest, content)
}

func (s SnapshotStore) readSnapshot(digest string) (string, []byte, error) {
	path, err := s.SnapshotPath(digest)
	if err != nil {
		return "", nil, err
	}
	root, err := os.OpenRoot(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("no project link rules snapshot at %s", path)
	}
	if err != nil {
		return "", nil, fmt.Errorf("read project link rules snapshot: %w", err)
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("no project link rules snapshot at %s", path)
	}
	if err != nil {
		return "", nil, fmt.Errorf("read project link rules snapshot: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("project link rules snapshot %s is not a regular file", path)
	}
	file, err := root.Open(name)
	if err != nil {
		return "", nil, fmt.Errorf("read project link rules snapshot: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, MaxSnapshotSize+1))
	if err != nil {
		return "", nil, fmt.Errorf("read project link rules snapshot: %w", err)
	}
	if len(content) > MaxSnapshotSize || Digest(content) != digest {
		return "", nil, fmt.Errorf("project link rules snapshot %s does not hold the content its name digests", path)
	}
	return path, content, nil
}

func (s SnapshotStore) write(name, digest string, content []byte) (Snapshot, error) {
	if len(content) > MaxSnapshotSize {
		return Snapshot{}, fmt.Errorf("write project link rules snapshot: %d bytes; the limit is %d", len(content), MaxSnapshotSize)
	}
	path := filepath.Join(s.dir, name)
	if snapshotHolds(s.dir, name, content) {
		return Snapshot{Digest: digest, Path: path}, nil
	}
	if err := writeProjectLinksAtomic(path, content); err != nil {
		return Snapshot{}, fmt.Errorf("write project link rules snapshot: %w", err)
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
	existing, err := io.ReadAll(io.LimitReader(file, MaxSnapshotSize+1))
	return err == nil && bytes.Equal(existing, content)
}
