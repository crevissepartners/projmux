package mux

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recordingBackend struct {
	name string
	args []string
	out  []byte
	err  error
}

func (b *recordingBackend) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	b.name = name
	b.args = append([]string(nil), args...)
	return append([]byte(nil), b.out...), b.err
}

func TestRunnerReadInvokesTmuxWithExactArgs(t *testing.T) {
	backend := &recordingBackend{out: []byte("  value \n")}
	runner := NewRunner(backend)

	out, err := runner.Read(context.Background(), "show-option", "-gqv", "@projmux_projdir")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	if backend.name != "tmux" {
		t.Fatalf("backend name = %q, want tmux", backend.name)
	}
	wantArgs := []string{"show-option", "-gqv", "@projmux_projdir"}
	if !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend args = %#v, want %#v", backend.args, wantArgs)
	}
	if string(out) != "  value \n" {
		t.Fatalf("Read output = %q, want raw output", string(out))
	}
}

func TestRunnerReadTrimmedTrimsOnlyReadTrimmedOutput(t *testing.T) {
	backend := &recordingBackend{out: []byte("  value \n")}
	runner := NewRunner(backend)

	raw, err := runner.Read(context.Background(), "display-message", "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if string(raw) != "  value \n" {
		t.Fatalf("Read output = %q, want raw output", string(raw))
	}

	trimmed, err := runner.ReadTrimmed(context.Background(), "display-message", "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("ReadTrimmed returned error: %v", err)
	}
	if trimmed != "value" {
		t.Fatalf("ReadTrimmed output = %q, want value", trimmed)
	}
}

func TestRunnerRunReturnsBackendError(t *testing.T) {
	wantErr := errors.New("boom")
	backend := &recordingBackend{err: wantErr}
	runner := NewRunner(backend)

	err := runner.Run(context.Background(), "set-option", "-g", "@projmux_app", "1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}

	wantArgs := []string{"set-option", "-g", "@projmux_app", "1"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}
