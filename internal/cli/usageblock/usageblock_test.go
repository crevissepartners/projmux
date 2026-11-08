package usageblock

import (
	"bytes"
	"testing"
)

func TestWriteFormat(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, []string{"a b [flags]", "a c"})
	if want := "Usage:\n  a b [flags]\n  a c\n"; buf.String() != want {
		t.Fatalf("Write = %q, want %q", buf.String(), want)
	}
}

func TestWriteNoLinesWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("wrote %q", buf.String())
	}
}
