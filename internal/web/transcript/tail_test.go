package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

const (
	userLine      = `{"type":"user","message":{"content":"first"}}`
	assistantLine = `{"type":"assistant","message":{"content":"second"}}`
)

func TestTailerHoldsPartialLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	appendFile(t, path, `{"type":"user","message":{"content":"old"}}`+"\n")

	tailer, err := NewTailer("claude", path, false)
	if err != nil {
		t.Fatal(err)
	}
	if turns, err := tailer.Next(); err != nil || len(turns) != 0 {
		t.Fatalf("unchanged file: %v %v", turns, err)
	}

	appendFile(t, path, userLine[:20])
	if turns, err := tailer.Next(); err != nil || len(turns) != 0 {
		t.Fatalf("fragment parsed: %v %v", turns, err)
	}
	appendFile(t, path, userLine[20:]+"\n"+assistantLine[:10])
	turns, err := tailer.Next()
	if err != nil || len(turns) != 1 || turns[0].Text != "first" {
		t.Fatalf("completed line: %+v %v", turns, err)
	}
	appendFile(t, path, assistantLine[10:]+"\n")
	turns, err = tailer.Next()
	if err != nil || len(turns) != 1 || turns[0].Text != "second" {
		t.Fatalf("second line: %+v %v", turns, err)
	}
}

func TestTailerResetsOnTruncationAndToleratesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	appendFile(t, path, userLine+"\n"+assistantLine+"\n")

	tailer, err := NewTailer("claude", path, true)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := tailer.Next()
	if err != nil || len(turns) != 2 {
		t.Fatalf("from start: %+v %v", turns, err)
	}

	// A partial line is buffered, then the file is replaced by a shorter one.
	appendFile(t, path, `{"type":"user"`)
	if turns, err := tailer.Next(); err != nil || len(turns) != 0 {
		t.Fatalf("partial: %+v %v", turns, err)
	}
	if err := os.WriteFile(path, []byte(assistantLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err = tailer.Next()
	if err != nil || len(turns) != 1 || turns[0].Text != "second" {
		t.Fatalf("after truncation: %+v %v", turns, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if turns, err := tailer.Next(); err != nil || turns != nil {
		t.Fatalf("missing file: %+v %v", turns, err)
	}
}

func TestNewTailerFromEndNeedsFile(t *testing.T) {
	if _, err := NewTailer("claude", filepath.Join(t.TempDir(), "none"), false); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestReadOffsetStopsBeforeAnUnfinishedLineAndTheTailerResumesThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	done := `{"type":"user","message":{"role":"user","content":"one"}}` + "\n"
	partial := `{"type":"user","message":{"role":"user","content":"tw`
	if err := os.WriteFile(path, []byte(done+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := ReadTranscript("claude", path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if read.Offset != int64(len(done)) || len(read.Turns) != 1 {
		t.Fatalf("offset = %d turns = %d, want %d and 1", read.Offset, len(read.Turns), len(done))
	}
	tailer := NewTailerAt("claude", path, read.Offset)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`o"}}` + "\n")
	_ = file.Close()
	turns, err := tailer.Next()
	if err != nil || len(turns) != 1 || turns[0].Text != "two" {
		t.Fatalf("resumed turns = %+v %v, want the finished line", turns, err)
	}
	info, _ := os.Stat(path)
	if tailer.Offset() != info.Size() {
		t.Fatalf("tailer offset = %d, want %d", tailer.Offset(), info.Size())
	}
}
