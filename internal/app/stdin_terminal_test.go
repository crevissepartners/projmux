package app

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// replaceStdin points os.Stdin at file for the rest of the test. Tests that use
// it must not run in parallel: os.Stdin is process-global.
func replaceStdin(t *testing.T, file *os.File) {
	t.Helper()
	original := os.Stdin
	os.Stdin = file
	t.Cleanup(func() { os.Stdin = original })
}

func openDevNullStdin(t *testing.T) *os.File {
	t.Helper()
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestStdinIsTerminalFalseForDevNullAndPipe(t *testing.T) {
	replaceStdin(t, openDevNullStdin(t))
	if stdinIsTerminal() {
		t.Fatalf("stdinIsTerminal() = true with stdin %s, want false", os.DevNull)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = readEnd.Close()
		_ = writeEnd.Close()
	})
	os.Stdin = readEnd
	if stdinIsTerminal() {
		t.Fatalf("stdinIsTerminal() = true with a pipe stdin, want false")
	}
}

func TestNewConfirmerDevNullStdinRefusesWithoutPrompt(t *testing.T) {
	replaceStdin(t, openDevNullStdin(t))

	var out bytes.Buffer
	err := newConfirmer().confirm(false, "Delete X?", "refusal text", &out)
	if err == nil || !IsUsageError(err) {
		t.Fatalf("confirm() error = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "refusal text") {
		t.Fatalf("confirm() error = %q, want the refusal text", err)
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("confirmation prompt written with /dev/null stdin: %q", out.String())
	}
}
