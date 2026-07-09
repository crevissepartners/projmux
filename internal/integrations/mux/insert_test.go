package mux

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSendKeysLiteralTargetsPaneWithLiteralFlag(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SendKeysLiteral(context.Background(), "%7", "/home/es5h/shot.png"); err != nil {
		t.Fatalf("SendKeysLiteral returned error: %v", err)
	}

	if backend.name != "tmux" {
		t.Fatalf("backend name = %q, want tmux", backend.name)
	}
	want := []string{"send-keys", "-t", "%7", "-l", "--", "/home/es5h/shot.png"}
	if !reflect.DeepEqual(backend.args, want) {
		t.Fatalf("SendKeysLiteral args = %#v, want %#v", backend.args, want)
	}
}

func TestSendKeysLiteralEmptyTargetOmitsPaneArgs(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SendKeysLiteral(context.Background(), "", "value"); err != nil {
		t.Fatalf("SendKeysLiteral returned error: %v", err)
	}
	want := []string{"send-keys", "-l", "--", "value"}
	if !reflect.DeepEqual(backend.args, want) {
		t.Fatalf("SendKeysLiteral args = %#v, want %#v", backend.args, want)
	}
}

// TestSendKeysLiteralNeverTouchesClipboard is the guardrail behind the whole
// feature: the literal insert primitive must never emit the OSC52 (`-w`) flag or
// any clipboard/buffer command.
func TestSendKeysLiteralNeverTouchesClipboard(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.SendKeysLiteral(context.Background(), "%1", "-flag-looking-text"); err != nil {
		t.Fatalf("SendKeysLiteral returned error: %v", err)
	}
	for _, arg := range backend.args {
		if arg == "-w" {
			t.Fatalf("SendKeysLiteral emitted clipboard flag -w: %#v", backend.args)
		}
	}
	joined := strings.Join(backend.args, " ")
	if strings.Contains(joined, "set-buffer") || strings.Contains(joined, "copy") {
		t.Fatalf("SendKeysLiteral emitted a clipboard/buffer command: %#v", backend.args)
	}
}

func TestShowStatusMessageRendersMessage(t *testing.T) {
	backend := &recordingBackend{}
	runner := NewRunner(backend)

	if err := runner.ShowStatusMessage(context.Background(), "%2", "insert source empty: shot\n"); err != nil {
		t.Fatalf("ShowStatusMessage returned error: %v", err)
	}
	want := []string{"display-message", "-t", "%2", "--", "insert source empty: shot"}
	if !reflect.DeepEqual(backend.args, want) {
		t.Fatalf("ShowStatusMessage args = %#v, want %#v", backend.args, want)
	}
}
