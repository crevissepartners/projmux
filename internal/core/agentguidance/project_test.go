package agentguidance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

const projectTestUID = "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestProjectStoreStatesBoundsAndIsolation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := NewProjectStore(filepath.Join(root, "config"), filepath.Join(root, "state"))
	if ProjectDirName != config.ProjectGuidanceDirName {
		t.Fatal("layer declaration drift")
	}
	for _, uid := range []string{"", "../outside", "project-name", "proj-../escape"} {
		if _, err := store.Load(uid); err == nil {
			t.Fatalf("Load accepted %q", uid)
		}
		if err := store.Save(uid, []byte("bad")); err == nil {
			t.Fatalf("Save accepted %q", uid)
		}
		if err := store.Delete(uid); err == nil {
			t.Fatalf("Delete accepted %q", uid)
		}
	}
	check := func(want []byte) {
		t.Helper()
		got, err := store.Load(projectTestUID)
		if err != nil || !bytes.Equal(got.Text, want) {
			t.Fatalf("Load = %+v, %v, want %q", got, err, want)
		}
	}
	check(nil)
	for _, text := range [][]byte{nil, []byte(" \t\n"), []byte("  exact instructions\n\n"), bytes.Repeat([]byte("x"), MaxSize)} {
		if err := store.Save(projectTestUID, text); err != nil {
			t.Fatal(err)
		}
		want := text
		if len(bytes.TrimSpace(want)) == 0 {
			want = nil
		}
		check(want)
	}
	if err := store.Save(projectTestUID, bytes.Repeat([]byte("x"), MaxSize+1)); err == nil {
		t.Fatal("oversized Save accepted")
	}
	check(bytes.Repeat([]byte("x"), MaxSize))
	other, err := store.Load("proj-aaaaaaaaaaaaaaaaaaaaaaaaaq")
	if err != nil || !other.Off() {
		t.Fatal("Project isolation", other, err)
	}
	path, _ := store.Path(projectTestUID)
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(projectTestUID, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatal("mode changed")
	}
	if err := store.Delete(projectTestUID); err != nil {
		t.Fatal(err)
	}
	check(nil)
	if err := store.Delete(projectTestUID); err != nil {
		t.Fatal(err)
	}
}

func TestProjectStoreRejectsUnavailableFilesAndVerifiesSnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := NewProjectStore(filepath.Join(root, "config"), filepath.Join(root, "state"))
	path, _ := store.Path(projectTestUID)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(projectTestUID); err == nil {
		t.Fatal("directory accepted")
	}
	if err := store.Delete(projectTestUID); err == nil {
		t.Fatal("directory deleted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.md")
	if err := os.WriteFile(outside, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(projectTestUID); err == nil {
		t.Fatal("escaping symlink accepted")
	}
	if err := store.Delete(projectTestUID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), MaxSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(projectTestUID); err == nil {
		t.Fatal("oversized Load accepted")
	}
	snapshot, err := store.WriteSnapshot([]byte("Project\n"))
	if err != nil {
		t.Fatal(err)
	}
	composite, err := store.WriteComposite([]byte("Persona\n"), snapshot.Digest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(composite.Path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("Persona\n" + projectlinks.CompositeSeparator + "Project\n"); !bytes.Equal(got, want) {
		t.Fatalf("composite=%q want=%q", got, want)
	}
	if err := os.WriteFile(snapshot.Path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordedSnapshotPath(snapshot.Digest); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}
