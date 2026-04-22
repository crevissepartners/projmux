package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAppRunPreviewSelect(t *testing.T) {
	t.Parallel()

	store := &stubPreviewStore{}
	app := &App{
		preview: &previewCommand{
			store: store,
		},
	}

	if err := app.Run([]string{"preview", "select", "dev", "2", "1"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.writeSession, "dev"; got != want {
		t.Fatalf("write session = %q, want %q", got, want)
	}
	if got, want := store.writeWindowIndex, "2"; got != want {
		t.Fatalf("write window = %q, want %q", got, want)
	}
	if got, want := store.writePaneIndex, "1"; got != want {
		t.Fatalf("write pane = %q, want %q", got, want)
	}
}

func TestPreviewSelectOmittedPanePersistsEmptyPaneIndex(t *testing.T) {
	t.Parallel()

	store := &stubPreviewStore{}
	cmd := &previewCommand{store: store}

	if err := cmd.Run([]string{"select", "dev", "2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.writePaneIndex, ""; got != want {
		t.Fatalf("write pane = %q, want empty pane index", got)
	}
}

func TestPreviewSelectRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing args", args: []string{"select"}, want: "preview select requires 2 or 3 arguments"},
		{name: "blank session", args: []string{"select", " ", "2"}, want: "preview select requires a non-empty <session> argument"},
		{name: "blank window", args: []string{"select", "dev", " "}, want: "preview select requires a non-empty <window> argument"},
		{name: "blank pane", args: []string{"select", "dev", "2", " "}, want: "preview select requires a non-empty <pane> argument when provided"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&previewCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestPreviewSelectReportsStoreErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *previewCommand
		want string
	}{
		{
			name: "store setup",
			cmd: &previewCommand{
				storeErr: errors.New("no state dir"),
			},
			want: "configure preview store",
		},
		{
			name: "store write",
			cmd: &previewCommand{
				store: &stubPreviewStore{
					writeErr: errors.New("write failed"),
				},
			},
			want: "persist preview selection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run([]string{"select", "dev", "2", "1"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
