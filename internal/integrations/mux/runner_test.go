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

func TestRunnerSetPaneOptionBuildsPaneScopedSetOption(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SetPaneOption(context.Background(), " %9 ", " @projmux_ai_state ", "waiting"); err != nil {
		t.Fatalf("SetPaneOption returned error: %v", err)
	}

	wantArgs := []string{"set-option", "-p", "-t", "%9", "@projmux_ai_state", "waiting"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerUnsetPaneOptionBuildsPaneScopedUnsetOption(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.UnsetPaneOption(context.Background(), "%9", "@projmux_attention_ack"); err != nil {
		t.Fatalf("UnsetPaneOption returned error: %v", err)
	}

	wantArgs := []string{"set-option", "-p", "-u", "-t", "%9", "@projmux_attention_ack"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
}

func TestRunnerShowPaneOptionReadsDisplayMessageFormat(t *testing.T) {
	backend := &recordingBackend{out: []byte(" reply \n")}
	runner := NewRunner(backend)

	got, err := runner.ShowPaneOption(context.Background(), "%9", "@projmux_attention_state")
	if err != nil {
		t.Fatalf("ShowPaneOption returned error: %v", err)
	}

	wantArgs := []string{"display-message", "-p", "-t", "%9", "#{@projmux_attention_state}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if got != "reply" {
		t.Fatalf("ShowPaneOption = %q, want reply", got)
	}
}

func TestRunnerDisplayMessageBuildsPaneScopedRead(t *testing.T) {
	backend := &recordingBackend{out: []byte("  title  \n")}
	runner := NewRunner(backend)

	got, err := runner.DisplayMessage(context.Background(), DisplayMessageOptions{
		Target: " %9 ",
		Format: TmuxFormat("pane_title"),
	})
	if err != nil {
		t.Fatalf("DisplayMessage returned error: %v", err)
	}

	wantArgs := []string{"display-message", "-p", "-t", "%9", "#{pane_title}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if string(got) != "  title  \n" {
		t.Fatalf("DisplayMessage = %q, want raw output", string(got))
	}
}

func TestRunnerDisplayMessageAllowsTargetlessRead(t *testing.T) {
	backend := &recordingBackend{out: []byte("%3\n")}
	runner := NewRunner(backend)

	got, err := runner.DisplayMessageTrimmed(context.Background(), DisplayMessageOptions{
		Format: TmuxFormat("pane_id"),
	})
	if err != nil {
		t.Fatalf("DisplayMessageTrimmed returned error: %v", err)
	}

	wantArgs := []string{"display-message", "-p", "#{pane_id}"}
	if backend.name != "tmux" || !reflect.DeepEqual(backend.args, wantArgs) {
		t.Fatalf("backend call = %q %#v, want tmux %#v", backend.name, backend.args, wantArgs)
	}
	if got != "%3" {
		t.Fatalf("DisplayMessageTrimmed = %q, want %%3", got)
	}
}

func TestJoinFormatsKeepsCallerDelimiter(t *testing.T) {
	got := JoinFormats("|", TmuxFormat("pane_title"), PaneOptionFormat("@projmux_ai_state"))
	want := "#{pane_title}|#{@projmux_ai_state}"
	if got != want {
		t.Fatalf("JoinFormats = %q, want %q", got, want)
	}
}
