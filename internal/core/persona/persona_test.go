package persona

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

func newTestStore(t *testing.T) (Store, string, string) {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config", "projmux")
	stateDir := filepath.Join(root, "state", "projmux")
	return NewStore(configDir, stateDir), configDir, stateDir
}

func assertReason(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %s", want)
	}
	if got := ReasonOf(err); got != want {
		t.Fatalf("ReasonOf(%v) = %q, want %q", err, got, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error text %q does not carry the token %q", err, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func TestNewDefaultStoreUsesConfigAndStateDirs(t *testing.T) {
	t.Parallel()
	paths := config.DefaultPaths("/tmp/config-home", "/tmp/state-home")
	store := NewDefaultStore(paths)
	if got, want := store.Dir(), filepath.Join(paths.ConfigDir, "personas"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
	snapshot, err := store.SnapshotPath(Digest(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Dir(snapshot), filepath.Join(paths.StateDir, "personas"); got != want {
		t.Fatalf("snapshot dir = %q, want %q", got, want)
	}
	path, err := store.Path("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(paths.ConfigDir, "personas", "reviewer.md"); path != want {
		t.Fatalf("Path() = %q, want %q", path, want)
	}
}

func TestValidateNameTable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		valid bool
	}{
		{"reviewer", true},
		{"Reviewer", true},
		{"lead.epic_1", true},
		{"a-b", true},
		{"a..b", true},
		{strings.Repeat("x", 128), true},
		{"", false},
		{"../x", false},
		{"x/y", false},
		{`x\y`, false},
		{"..", false},
		{".", false},
		{".hidden", false},
		{"-x", false},
		{"--persona", false},
		{" reviewer", false},
		{"two words", false},
		{"a:b", false},
		{strings.Repeat("x", 129), false},
	} {
		err := ValidateName(test.name)
		if test.valid {
			if err != nil {
				t.Errorf("ValidateName(%q) = %v, want valid", test.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("ValidateName(%q) accepted an invalid name", test.name)
			continue
		}
		if ReasonOf(err) != ReasonNameInvalid || !strings.Contains(err.Error(), ReasonNameInvalid) {
			t.Errorf("ValidateName(%q) = %v, want %s", test.name, err, ReasonNameInvalid)
		}
	}
}

func TestDigestIsSHA256OfRawBytes(t *testing.T) {
	t.Parallel()
	content := []byte("You are a careful reviewer.\r\n\x00trailing")
	sum := sha256.Sum256(content)
	if got, want := Digest(content), "sha256:"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("Digest = %q, want %q", got, want)
	}
}

// TestWriteThenLoadListIsByteIdenticalAndPrivate is C-3's round trip: what is
// written is what is read, byte for byte, listed with its digest, and stored
// 0600 in a 0700 directory.
func TestWriteThenLoadListIsByteIdenticalAndPrivate(t *testing.T) {
	t.Parallel()
	store, configDir, _ := newTestStore(t)
	content := []byte("# Reviewer\n\nBe terse.\n\xff\xfe not utf-8 is kept as-is\n")

	entry, err := store.Write("Reviewer", content)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(configDir, "personas", "Reviewer.md")
	if entry.Path != wantPath || entry.Digest != Digest(content) || entry.Size != int64(len(content)) {
		t.Fatalf("entry = %+v", entry)
	}
	assertMode(t, wantPath, 0o600)
	assertMode(t, filepath.Dir(wantPath), 0o700)

	loaded, err := store.Load("Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Content, content) || loaded.Digest != Digest(content) {
		t.Fatalf("loaded = %+v", loaded)
	}
	// Case is significant: a differently cased name is another persona.
	if _, err := store.Load("reviewer"); ReasonOf(err) != ReasonNotFound {
		t.Fatalf("Load(reviewer) = %v, want %s", err, ReasonNotFound)
	}

	if _, err := store.Write("alpha", []byte("a")); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != "Reviewer" || listed[1].Name != "alpha" {
		t.Fatalf("List = %+v", listed)
	}
	if listed[0].Digest != Digest(content) || listed[0].Size != int64(len(content)) || listed[0].ModTime.IsZero() {
		t.Fatalf("List[0] = %+v", listed[0])
	}

	// Overwrite replaces the content in place.
	if _, err := store.Write("Reviewer", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load("Reviewer")
	if err != nil || string(loaded.Content) != "v2" {
		t.Fatalf("after overwrite: %+v, %v", loaded, err)
	}
	// No temporary file survives a completed write.
	names, err := os.ReadDir(filepath.Dir(wantPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.Contains(name.Name(), ".tmp-") {
			t.Fatalf("temporary file %q left behind", name.Name())
		}
	}
}

func TestSizeLimitIsExactlySixtyFourKiB(t *testing.T) {
	t.Parallel()
	store, configDir, _ := newTestStore(t)
	if MaxSize != 65536 {
		t.Fatalf("MaxSize = %d, want 65536", MaxSize)
	}
	atLimit := bytes.Repeat([]byte("a"), MaxSize)
	if _, err := store.Write("limit", atLimit); err != nil {
		t.Fatalf("a persona of exactly %d bytes was refused: %v", MaxSize, err)
	}
	overLimit := bytes.Repeat([]byte("a"), MaxSize+1)
	_, err := store.Write("over", overLimit)
	assertReason(t, err, ReasonTooLarge)
	if _, statErr := os.Stat(filepath.Join(configDir, "personas", "over.md")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("an oversized persona was written: %v", statErr)
	}

	// A file made oversized by hand is refused on read, not truncated.
	if err := os.WriteFile(filepath.Join(configDir, "personas", "handmade.md"), overLimit, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("handmade")
	assertReason(t, err, ReasonTooLarge)

	_, err = ReadLimited(bytes.NewReader(overLimit))
	assertReason(t, err, ReasonTooLarge)
	got, err := ReadLimited(bytes.NewReader(atLimit))
	if err != nil || len(got) != MaxSize {
		t.Fatalf("ReadLimited(at limit) = %d bytes, %v", len(got), err)
	}
}

func TestRefusalsWriteNothing(t *testing.T) {
	t.Parallel()
	store, configDir, stateDir := newTestStore(t)
	for _, name := range []string{"../x", ".hidden", "-x", strings.Repeat("n", 129), "a/b", ""} {
		_, err := store.Write(name, []byte("x"))
		assertReason(t, err, ReasonNameInvalid)
		_, err = store.Load(name)
		assertReason(t, err, ReasonNameInvalid)
		err = store.Delete(name)
		assertReason(t, err, ReasonNameInvalid)
	}
	_, err := store.Load("missing")
	assertReason(t, err, ReasonNotFound)
	err = store.Delete("missing")
	assertReason(t, err, ReasonNotFound)
	for _, dir := range []string{configDir, stateDir, filepath.Dir(configDir)} {
		if _, err := os.Stat(filepath.Join(dir, "personas")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a refused operation created %s/personas: %v", dir, err)
		}
	}
	// "../x" would have escaped the personas directory.
	if _, err := os.Stat(filepath.Join(configDir, "x.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path traversal wrote a file: %v", err)
	}
}

func TestDeleteRemovesOnlyThePersonaFile(t *testing.T) {
	t.Parallel()
	store, _, _ := newTestStore(t)
	content := []byte("keep my snapshot")
	if _, err := store.Write("reviewer", content); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.WriteSnapshot(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("reviewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("reviewer"); ReasonOf(err) != ReasonNotFound {
		t.Fatalf("Load after delete = %v", err)
	}
	if listed, err := store.List(); err != nil || len(listed) != 0 {
		t.Fatalf("List after delete = %+v, %v", listed, err)
	}
	if kept, err := os.ReadFile(snapshot.Path); err != nil || !bytes.Equal(kept, content) {
		t.Fatalf("deleting a persona removed its snapshot: %q, %v", kept, err)
	}
}

func TestListSkipsNonPersonaFilesAndMissingDir(t *testing.T) {
	t.Parallel()
	store, configDir, _ := newTestStore(t)
	listed, err := store.List()
	if err != nil || len(listed) != 0 {
		t.Fatalf("List on a missing dir = %+v, %v", listed, err)
	}
	dir := filepath.Join(configDir, "personas")
	if err := os.MkdirAll(filepath.Join(dir, "sub.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".hidden.md", ".ok.md.tmp-123", "notes.txt", "-flag.md", "good.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	listed, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "good" || listed[0].Digest != Digest([]byte("good.md")) {
		t.Fatalf("List = %+v", listed)
	}
}

// TestSnapshotIsContentAddressedIdempotentAndPrivate covers A4: the snapshot
// path is derived from the digest, a second write of the same content is a
// no-op, and FindSnapshot resolves the digest a create recorded.
func TestSnapshotIsContentAddressedIdempotentAndPrivate(t *testing.T) {
	t.Parallel()
	store, _, stateDir := newTestStore(t)
	content := []byte("You are the release captain.\n")
	digest := Digest(content)

	snapshot, err := store.WriteSnapshot(content)
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	wantPath := filepath.Join(stateDir, "personas", "sha256-"+hexDigest+".md")
	if snapshot.Digest != digest || snapshot.Path != wantPath {
		t.Fatalf("snapshot = %+v, want digest %s at %s", snapshot, digest, wantPath)
	}
	written, err := os.ReadFile(wantPath)
	if err != nil || !bytes.Equal(written, content) {
		t.Fatalf("snapshot bytes = %q, %v", written, err)
	}
	assertMode(t, wantPath, 0o600)
	assertMode(t, filepath.Dir(wantPath), 0o700)

	before, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.WriteSnapshot(content)
	if err != nil || again != snapshot {
		t.Fatalf("second WriteSnapshot = %+v, %v", again, err)
	}
	after, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("an identical snapshot was rewritten")
	}

	// A consumer holding only the recorded digest finds the same file.
	found, err := store.SnapshotPath(digest)
	if err != nil || found != snapshot.Path {
		t.Fatalf("SnapshotPath(%s) = %q, %v", digest, found, err)
	}

	// A snapshot edited by hand no longer matches its name; the next
	// WriteSnapshot restores the recorded bytes.
	if err := os.WriteFile(wantPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteSnapshot(content); err != nil {
		t.Fatal(err)
	}
	if restored, _ := os.ReadFile(wantPath); !bytes.Equal(restored, content) {
		t.Fatalf("WriteSnapshot did not restore the tampered snapshot: %q", restored)
	}

	_, err = store.WriteSnapshot(bytes.Repeat([]byte("z"), MaxSize+1))
	assertReason(t, err, ReasonTooLarge)
}

func TestSnapshotPathRejectsMalformedDigests(t *testing.T) {
	t.Parallel()
	store, _, _ := newTestStore(t)
	valid := Digest([]byte("x"))
	for _, digest := range []string{
		"",
		strings.TrimPrefix(valid, "sha256:"),
		"sha1:" + strings.TrimPrefix(valid, "sha256:"),
		"sha256:" + strings.ToUpper(strings.TrimPrefix(valid, "sha256:")),
		"sha256:../../etc/passwd",
		valid + "00",
		"sha256:" + strings.Repeat("g", 64),
	} {
		if _, err := store.SnapshotPath(digest); err == nil {
			t.Errorf("SnapshotPath(%q) accepted a malformed digest", digest)
		}
	}
	if _, err := store.SnapshotPath(valid); err != nil {
		t.Fatalf("SnapshotPath(valid) = %v", err)
	}
}

// TestPersonaReadsStayInsideTheirDirectory pins the os.Root confinement: a
// persona or snapshot that is a symlink leaving its directory is not read.
func TestPersonaReadsStayInsideTheirDirectory(t *testing.T) {
	t.Parallel()
	store, configDir, stateDir := newTestStore(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	content := []byte("outside the persona directory")
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "personas")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.md")); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load("escape"); err == nil {
		t.Fatalf("Load followed a symlink out of the persona directory: %q", loaded.Content)
	}

	snapshotPath, err := store.SnapshotPath(Digest(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "personas"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, snapshotPath); err != nil {
		t.Fatal(err)
	}
	// The escaping symlink is not accepted as the snapshot; it is replaced by
	// a regular file holding the content.
	if _, err := store.WriteSnapshot(content); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(snapshotPath)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("snapshot after WriteSnapshot = %v, %v; want a regular file", info, err)
	}
}

// TestRecordedSnapshotPathFindsOnlyARegularSnapshot pins the resume lookup: a
// recorded digest yields the snapshot path only while that snapshot is a
// regular file inside the snapshot directory, and every other outcome is
// persona-unavailable.
func TestRecordedSnapshotPathFindsOnlyARegularSnapshot(t *testing.T) {
	t.Parallel()
	store, _, stateDir := newTestStore(t)

	// A missing snapshot directory is the same outcome as a missing file.
	absent := Digest([]byte("never snapshotted"))
	_, err := store.RecordedSnapshotPath(absent)
	assertReason(t, err, ReasonUnavailable)

	snapshot, err := store.WriteSnapshot([]byte("you review Go code"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.RecordedSnapshotPath(snapshot.Digest)
	if err != nil {
		t.Fatalf("RecordedSnapshotPath(present) = %v", err)
	}
	if got != snapshot.Path {
		t.Fatalf("RecordedSnapshotPath = %q, want %q", got, snapshot.Path)
	}

	_, err = store.RecordedSnapshotPath(absent)
	assertReason(t, err, ReasonUnavailable)

	for _, digest := range []string{"", "sha256:../../etc/passwd", strings.TrimPrefix(snapshot.Digest, DigestPrefix)} {
		_, err := store.RecordedSnapshotPath(digest)
		assertReason(t, err, ReasonUnavailable)
	}

	// A symlink leaving the snapshot directory is not a snapshot, even when
	// its target holds exactly the recorded content.
	outsideContent := []byte("outside the snapshot directory")
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, outsideContent, 0o600); err != nil {
		t.Fatal(err)
	}
	linked, err := store.SnapshotPath(Digest(outsideContent))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	_, err = store.RecordedSnapshotPath(Digest(outsideContent))
	assertReason(t, err, ReasonUnavailable)

	// A directory under the snapshot name is not a snapshot either.
	dirContent := []byte("a directory, not a file")
	dirPath, err := store.SnapshotPath(Digest(dirContent))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = store.RecordedSnapshotPath(Digest(dirContent))
	assertReason(t, err, ReasonUnavailable)

	if filepath.Dir(snapshot.Path) != filepath.Join(stateDir, DirName) {
		t.Fatalf("snapshot %q is not under the state snapshot directory", snapshot.Path)
	}
}
