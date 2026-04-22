package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	corepreview "github.com/es5h/projmux/internal/core/preview"
)

func TestAppRunPreviewCyclePane(t *testing.T) {
	t.Parallel()

	store := &stubPreviewStore{
		cyclePaneResult: corepreview.CycleResult{
			Cursor: corepreview.Cursor{
				WindowIndex: "2",
				PaneIndex:   "1",
			},
			Selected: true,
			Changed:  true,
		},
	}
	inventory := &stubPreviewInventory{
		panes: []corepreview.Pane{
			{WindowIndex: "2", Index: "0", Active: true},
			{WindowIndex: "2", Index: "1"},
		},
	}

	app := &App{
		preview: &previewCommand{
			store:     store,
			inventory: inventory,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"preview", "cycle-pane", "dev", "next"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := inventory.sessionPanesSession, "dev"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
	}
	if got, want := store.cyclePaneSession, "dev"; got != want {
		t.Fatalf("cycle pane session = %q, want %q", got, want)
	}
	if got, want := store.cyclePaneDirection, corepreview.DirectionNext; got != want {
		t.Fatalf("cycle pane direction = %q, want %q", got, want)
	}
	if got, want := store.cyclePanePanes, inventory.panes; !equalPreviewPanes(got, want) {
		t.Fatalf("cycle pane panes = %#v, want %#v", got, want)
	}
	if got, want := stdout.String(), "dev\t2\t1\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAppRunPreviewCycleWindow(t *testing.T) {
	t.Parallel()

	store := &stubPreviewStore{
		cycleWindowResult: corepreview.CycleResult{
			Cursor: corepreview.Cursor{
				WindowIndex: "3",
				PaneIndex:   "0",
			},
			Selected: true,
			Changed:  true,
		},
	}
	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{
			{Index: "2", Active: true},
			{Index: "3"},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "2", Index: "0", Active: true},
			{WindowIndex: "3", Index: "0"},
		},
	}

	cmd := &previewCommand{
		store:     store,
		inventory: inventory,
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"cycle-window", "dev", "prev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := inventory.sessionWindowsSession, "dev"; got != want {
		t.Fatalf("SessionWindows session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionPanesSession, "dev"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
	}
	if got, want := store.cycleWindowSession, "dev"; got != want {
		t.Fatalf("cycle window session = %q, want %q", got, want)
	}
	if got, want := store.cycleWindowDirection, corepreview.DirectionPrev; got != want {
		t.Fatalf("cycle window direction = %q, want %q", got, want)
	}
	if got, want := store.cycleWindowWindows, inventory.windows; !equalPreviewWindows(got, want) {
		t.Fatalf("cycle window windows = %#v, want %#v", got, want)
	}
	if got, want := store.cycleWindowPanes, inventory.panes; !equalPreviewPanes(got, want) {
		t.Fatalf("cycle window panes = %#v, want %#v", got, want)
	}
	if got, want := stdout.String(), "dev\t3\t0\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestPreviewCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: nil, want: "preview requires a subcommand"},
		{name: "unknown subcommand", args: []string{"nope"}, want: "unknown preview subcommand: nope"},
		{name: "missing cycle args", args: []string{"cycle-pane"}, want: "preview cycle-pane requires exactly 2 arguments"},
		{name: "blank session", args: []string{"cycle-pane", " ", "next"}, want: "preview cycle-pane requires a non-empty <session> argument"},
		{name: "bad direction", args: []string{"cycle-window", "dev", "later"}, want: "direction must be <next|prev>"},
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

func TestPreviewCommandReportsConfigurationAndRuntimeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *previewCommand
		args []string
		want string
	}{
		{
			name: "store setup",
			cmd: &previewCommand{
				storeErr: errors.New("no state dir"),
			},
			args: []string{"cycle-pane", "dev", "next"},
			want: "configure preview store",
		},
		{
			name: "inventory setup",
			cmd: &previewCommand{
				store:        &stubPreviewStore{},
				inventoryErr: errors.New("missing adapter"),
			},
			args: []string{"cycle-pane", "dev", "next"},
			want: "configure preview inventory",
		},
		{
			name: "inventory load",
			cmd: &previewCommand{
				store: &stubPreviewStore{},
				inventory: &stubPreviewInventory{
					panesErr: errors.New("tmux failed"),
				},
			},
			args: []string{"cycle-pane", "dev", "next"},
			want: "load preview panes",
		},
		{
			name: "store cycle",
			cmd: &previewCommand{
				store: &stubPreviewStore{
					cycleWindowErr: errors.New("write failed"),
				},
				inventory: &stubPreviewInventory{
					windows: []corepreview.Window{{Index: "1", Active: true}},
					panes:   []corepreview.Pane{{WindowIndex: "1", Index: "0", Active: true}},
				},
			},
			args: []string{"cycle-window", "dev", "next"},
			want: "cycle preview window",
		},
		{
			name: "no selection",
			cmd: &previewCommand{
				store: &stubPreviewStore{
					cyclePaneResult: corepreview.CycleResult{},
				},
				inventory: &stubPreviewInventory{},
			},
			args: []string{"cycle-pane", "dev", "next"},
			want: "found no panes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run(tt.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type stubPreviewStore struct {
	readSession   string
	readSelection corepreview.Selection
	readFound     bool
	readErr       error

	cyclePaneSession   string
	cyclePaneWindows   []corepreview.Window
	cyclePanePanes     []corepreview.Pane
	cyclePaneDirection corepreview.Direction
	cyclePaneResult    corepreview.CycleResult
	cyclePaneErr       error

	cycleWindowSession   string
	cycleWindowWindows   []corepreview.Window
	cycleWindowPanes     []corepreview.Pane
	cycleWindowDirection corepreview.Direction
	cycleWindowResult    corepreview.CycleResult
	cycleWindowErr       error

	writeSession     string
	writeWindowIndex string
	writePaneIndex   string
	writeErr         error
}

func (s *stubPreviewStore) ReadSelection(sessionName string) (corepreview.Selection, bool, error) {
	s.readSession = sessionName
	if s.readErr != nil {
		return corepreview.Selection{}, false, s.readErr
	}
	return s.readSelection, s.readFound, nil
}

func (s *stubPreviewStore) CyclePaneSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error) {
	s.cyclePaneSession = sessionName
	s.cyclePaneWindows = append([]corepreview.Window(nil), windows...)
	s.cyclePanePanes = append([]corepreview.Pane(nil), panes...)
	s.cyclePaneDirection = direction
	if s.cyclePaneErr != nil {
		return corepreview.CycleResult{}, s.cyclePaneErr
	}
	return s.cyclePaneResult, nil
}

func (s *stubPreviewStore) CycleWindowSelection(sessionName string, windows []corepreview.Window, panes []corepreview.Pane, direction corepreview.Direction) (corepreview.CycleResult, error) {
	s.cycleWindowSession = sessionName
	s.cycleWindowWindows = append([]corepreview.Window(nil), windows...)
	s.cycleWindowPanes = append([]corepreview.Pane(nil), panes...)
	s.cycleWindowDirection = direction
	if s.cycleWindowErr != nil {
		return corepreview.CycleResult{}, s.cycleWindowErr
	}
	return s.cycleWindowResult, nil
}

func (s *stubPreviewStore) WriteSelection(sessionName, windowIndex, paneIndex string) error {
	s.writeSession = sessionName
	s.writeWindowIndex = windowIndex
	s.writePaneIndex = paneIndex
	return s.writeErr
}

type stubPreviewInventory struct {
	sessionWindowsSession string
	sessionPanesSession   string
	windows               []corepreview.Window
	panes                 []corepreview.Pane
	windowsErr            error
	panesErr              error
}

func (s *stubPreviewInventory) SessionWindows(_ context.Context, sessionName string) ([]corepreview.Window, error) {
	s.sessionWindowsSession = sessionName
	if s.windowsErr != nil {
		return nil, s.windowsErr
	}
	return append([]corepreview.Window(nil), s.windows...), nil
}

func (s *stubPreviewInventory) SessionPanes(_ context.Context, sessionName string) ([]corepreview.Pane, error) {
	s.sessionPanesSession = sessionName
	if s.panesErr != nil {
		return nil, s.panesErr
	}
	return append([]corepreview.Pane(nil), s.panes...), nil
}

func equalPreviewWindows(got, want []corepreview.Window) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalPreviewPanes(got, want []corepreview.Pane) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
