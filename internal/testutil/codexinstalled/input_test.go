package codexinstalled

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitInputIsBoundedAndCannotTraverseOrFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "input.json")
	if err := os.WriteFile(valid, []byte(`{"root":"/tmp/owned"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("PRIVATE-SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "dir.json")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(root, "large.json")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", maxExplicitInputBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"input.json", root + "/../" + filepath.Base(root) + "/input.json", link, directory, large} {
		if raw, err := ReadExplicitInput(path); err == nil || len(raw) != 0 || strings.Contains(err.Error(), "PRIVATE-SENTINEL") {
			t.Fatalf("unsafe input result bytes=%d err=%v", len(raw), err)
		}
	}
	if raw, err := ReadExplicitInput(valid); err != nil || string(raw) != `{"root":"/tmp/owned"}` {
		t.Fatalf("explicit regular input=%q err=%v", raw, err)
	}
}
