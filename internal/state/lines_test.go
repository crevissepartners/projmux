package state

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLinesFileReadMissingFile(t *testing.T) {
	t.Parallel()

	file := NewLinesFile(filepath.Join(t.TempDir(), "pins"))

	got, err := file.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Read() = %v, want empty", got)
	}
}

func TestLinesFileReadSkipsEmptyLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "pins")
	content := "\n/tmp/app\n   \n/tmp/lib\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := NewLinesFile(path).Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	want := []string{"/tmp/app", "/tmp/lib"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %v, want %v", got, want)
	}
}

func TestLinesFileWriteCreatesParentAndPersistsLines(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "pins")
	file := NewLinesFile(path)

	lines := []string{"/tmp/app", "/tmp/lib"}
	if err := file.Write(lines); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if string(raw) != "/tmp/app\n/tmp/lib\n" {
		t.Fatalf("file contents = %q, want %q", string(raw), "/tmp/app\n/tmp/lib\n")
	}
}

func TestLinesFileWriteEmptyCreatesEmptyFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "pins")
	file := NewLinesFile(path)

	if err := file.Write(nil); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if size := info.Size(); size != 0 {
		t.Fatalf("file size = %d, want 0", size)
	}
}
