package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestDeferredLaunchSnapshotProofDetectsChanges(t *testing.T) {
	root := t.TempDir()
	settings, composite := filepath.Join(root, "settings.json"), filepath.Join(root, "instructions.md")
	for _, path := range []string{settings, composite} {
		if err := os.WriteFile(path, []byte("frozen"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--settings", settings, "--append-system-prompt-file=" + composite, "--model", "literal"}
	files, err := deferredLaunchFiles(args)
	if err != nil || len(files) != 2 {
		t.Fatal(files, err)
	}
	record := deferredLaunchRecord{Command: processhost.Command{Args: args}, Files: files}
	if err = record.validateFiles(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(composite, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = record.validateFiles(); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("changed snapshot accepted", err)
	}
	if err = os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if err = record.validateFiles(); err == nil {
		t.Fatal("missing snapshot accepted")
	}
}

func TestDeferredSidecarBoundPrivateAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "record.json")
	value := map[string]string{"proof": "old"}
	if err := writeDeferredState(path, value); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, err := os.Stat(candidate.path)
		if err != nil || info.Mode().Perm() != candidate.mode {
			t.Fatal("permissions", candidate, err)
		}
	}
	if err := writeDeferredState(path, strings.Repeat("x", 65536)); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("oversized state accepted", err)
	}
	var restored map[string]string
	if found, err := readDeferredState(path, &restored); err != nil || !found || restored["proof"] != "old" {
		t.Fatal("failed write replaced state", restored, err)
	}
	link := filepath.Join(filepath.Dir(path), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readDeferredState(link, &restored); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("symlink accepted", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDeferredState(path, &restored); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("public state accepted", err)
	}
}

func TestDeferredLaunchDigestIncludesWholeRecipe(t *testing.T) {
	r := &deferredLaunchRecord{Version: 1, Agent: "agent", Command: processhost.Command{Path: "/provider", Args: []string{"--model", "a"}, Dir: "/workspace"}, Files: map[string]string{"snapshot": "digest"}}
	before := deferredLaunchDigest(r)
	r.Command.Args[1] = "b"
	if deferredLaunchDigest(r) == before {
		t.Fatal("arguments excluded from proof")
	}
	before = deferredLaunchDigest(r)
	r.Files["snapshot"] = "changed"
	if deferredLaunchDigest(r) == before {
		t.Fatal("snapshot excluded from proof")
	}
	before = deferredLaunchDigest(r)
	r.NewAnnotations = map[string]string{"recipe": "changed"}
	if deferredLaunchDigest(r) == before {
		t.Fatal("registry recipe excluded from proof")
	}
}
