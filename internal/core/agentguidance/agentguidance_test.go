package agentguidance

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	return NewStore(filepath.Join(root, "config"), filepath.Join(root, "state"))
}

// TestDefaultGuidanceNamesTheProjmuxCommandsAndNothingMachineLocal pins that
// the built-in text points at the two projmux commands and carries nothing
// that belongs to one machine: no absolute or home path, no uid, no host and
// no user name.
func TestDefaultGuidanceNamesTheProjmuxCommandsAndNothingMachineLocal(t *testing.T) {
	t.Parallel()
	text := string(Default())
	for _, want := range []string{"projmux create agent", "projmux agent message send"} {
		if !strings.Contains(text, want) {
			t.Fatalf("default guidance does not name %q:\n%s", want, text)
		}
	}
	if !strings.HasSuffix(text, ".\n") || strings.HasSuffix(text, "\n\n") {
		t.Fatalf("default guidance must end with exactly one newline: %q", text)
	}
	absolutePath := regexp.MustCompile(`(^|[\s("'` + "`" + `])/[A-Za-z0-9._-]`)
	if match := absolutePath.FindString(text); match != "" {
		t.Fatalf("default guidance holds an absolute path %q", match)
	}
	hostname, _ := os.Hostname()
	for _, forbidden := range []string{"~/", "uid:", "$HOME", "localhost", "@"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("default guidance holds %q", forbidden)
		}
	}
	for _, name := range []string{hostname, os.Getenv("USER"), os.Getenv("LOGNAME")} {
		if len(name) >= 3 && strings.Contains(strings.ToLower(text), strings.ToLower(name)) {
			t.Fatalf("default guidance holds the machine-local name %q", name)
		}
	}
	// Default returns a copy: a caller cannot change the next launch's text.
	first := Default()
	first[0] = 'X'
	if Default()[0] != '#' {
		t.Fatal("Default shares its bytes with callers")
	}
}

func TestLoadWithoutAFileIsTheDefault(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceDefault || !bytes.Equal(got.Text, Default()) || got.Digest != Digest(Default()) || got.Off() {
		t.Fatalf("Load = %+v, want the default", got)
	}
	if want := filepath.Join(store.configDir, "agent-guidance.md"); store.Path() != want {
		t.Fatalf("Path = %q, want %q", store.Path(), want)
	}
}

func TestLoadReadsTheFileVerbatimAndWhitespaceIsOff(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	custom := []byte("  Use projmux for everything.\n\n")
	if err := store.Save(custom); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceFile || !bytes.Equal(got.Text, custom) || got.Digest != Digest(custom) {
		t.Fatalf("Load = %+v, want the file verbatim", got)
	}
	for _, blank := range [][]byte{nil, []byte(" \n\t\r\n")} {
		if err := store.Save(blank); err != nil {
			t.Fatal(err)
		}
		got, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != SourceOff || !got.Off() || got.Digest != "" || got.Text != nil {
			t.Fatalf("Load of %q = %+v, want off", blank, got)
		}
	}
	if err := store.Disable(); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(store.Path()); err != nil || len(content) != 0 {
		t.Fatalf("Disable wrote %q, %v; want an empty file", content, err)
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || got.Source != SourceDefault {
		t.Fatalf("Load after Reset = %+v, %v; want the default", got, err)
	}
	if err := store.Reset(); err != nil {
		t.Fatalf("Reset without a file: %v", err)
	}
}

func TestSaveKeepsTheModeOfAnExistingFile(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	if err := store.Save([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(store.Path()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode = %v, %v; want 0600", info.Mode(), err)
	}
	if err := os.Chmod(store.Path(), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := store.Save([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(store.Path()); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("rewritten file mode = %v, %v; want the kept 0640", info.Mode(), err)
	}
}

func TestLoadRefusesAnOversizeOrUnreadableFile(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	if err := store.Save(bytes.Repeat([]byte("x"), MaxSize+1)); err == nil {
		t.Fatal("Save accepted guidance past MaxSize")
	}
	if err := store.Save(bytes.Repeat([]byte("x"), MaxSize)); err != nil {
		t.Fatalf("Save at MaxSize: %v", err)
	}
	if got, err := store.Load(); err != nil || len(got.Text) != MaxSize {
		t.Fatalf("Load at MaxSize = %d bytes, %v", len(got.Text), err)
	}
	if err := os.WriteFile(store.Path(), bytes.Repeat([]byte("x"), MaxSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err == nil {
		t.Fatalf("Load of an oversize file = %+v, want an error", got)
	}

	if err := os.Remove(store.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Load of a directory = %+v, %v; want not a regular file", got, err)
	}
	if err := store.Reset(); err == nil {
		t.Fatal("Reset removed a directory")
	}
}

func TestSnapshotsAreContentAddressedAndVerified(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	text := Default()
	snapshot, err := store.WriteSnapshot(text)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Digest != Digest(text) || snapshot.Path != filepath.Join(store.snapshotDir, snapshot.Digest+".md") {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	again, err := store.WriteSnapshot(text)
	if err != nil || again != snapshot {
		t.Fatalf("second WriteSnapshot = %+v, %v; want %+v", again, err, snapshot)
	}
	if path, err := store.RecordedSnapshotPath(snapshot.Digest); err != nil || path != snapshot.Path {
		t.Fatalf("RecordedSnapshotPath = %q, %v", path, err)
	}
	if _, err := store.WriteSnapshot(nil); err == nil {
		t.Fatal("WriteSnapshot of off guidance succeeded")
	}
	if _, err := store.RecordedSnapshotPath(Digest([]byte("never written"))); err == nil {
		t.Fatal("RecordedSnapshotPath found a snapshot that was never written")
	}
	for _, bad := range []string{"", "ABC", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if ValidateDigest(bad) == nil {
			t.Fatalf("ValidateDigest(%q) accepted", bad)
		}
	}
	if err := os.WriteFile(snapshot.Path, []byte("edited by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordedSnapshotPath(snapshot.Digest); err == nil {
		t.Fatal("RecordedSnapshotPath accepted a snapshot whose content no longer hashes to its name")
	}
}

func TestWriteCompositePutsTheGuidanceFirst(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	guidance := []byte("GUIDANCE\n")
	snapshot, err := store.WriteSnapshot(guidance)
	if err != nil {
		t.Fatal(err)
	}
	alone, err := store.WriteComposite(snapshot.Digest, nil)
	if err != nil || alone != snapshot {
		t.Fatalf("WriteComposite with no tail = %+v, %v; want the guidance snapshot %+v", alone, err, snapshot)
	}
	tail := []byte("PERSONA\n")
	composite, err := store.WriteComposite(snapshot.Digest, tail)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("GUIDANCE\n" + projectlinks.CompositeSeparator + "PERSONA\n")
	got, err := os.ReadFile(composite.Path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("composite = %q, %v; want %q", got, err, want)
	}
	if composite.Digest != Digest(want) || filepath.Base(composite.Path) != "composite-"+Digest(want)+".md" {
		t.Fatalf("composite = %+v", composite)
	}
	if again, err := store.WriteComposite(snapshot.Digest, tail); err != nil || again != composite {
		t.Fatalf("second WriteComposite = %+v, %v", again, err)
	}
	if _, err := store.WriteComposite(Digest([]byte("missing")), tail); err == nil {
		t.Fatal("WriteComposite of a missing guidance snapshot succeeded")
	}
	if _, err := store.WriteComposite(snapshot.Digest, bytes.Repeat([]byte("x"), projectlinks.MaxSnapshotSize+1)); err != nil {
		t.Fatalf("WriteComposite of the largest tail: %v", err)
	}
}
