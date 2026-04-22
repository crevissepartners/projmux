package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	corepreview "github.com/es5h/projmux/internal/core/preview"
)

func TestAppRunSessionPopupPreview(t *testing.T) {
	t.Parallel()

	store := &stubPreviewStore{
		readSelection: corepreview.Selection{
			SessionName: "dev",
			WindowIndex: "3",
			PaneIndex:   "8",
		},
		readFound: true,
	}
	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{
			{Index: "2", Active: true},
			{Index: "3"},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "2", Index: "4", Active: true},
			{WindowIndex: "3", Index: "7"},
			{WindowIndex: "3", Index: "8"},
		},
	}

	app := &App{
		sessionPopup: &sessionPopupCommand{
			store:     store,
			inventory: inventory,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"session-popup", "preview", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.readSession, "dev"; got != want {
		t.Fatalf("ReadSelection session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionWindowsSession, "dev"; got != want {
		t.Fatalf("SessionWindows session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionPanesSession, "dev"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
	}

	const wantOutput = "" +
		"session: dev\n" +
		"selected: window=3 pane=8\n" +
		"windows:\n" +
		"    2\n" +
		"  * 3\n" +
		"panes:\n" +
		"    7\n" +
		"  * 8\n"
	if got := stdout.String(); got != wantOutput {
		t.Fatalf("stdout = %q, want %q", got, wantOutput)
	}
}

func TestAppRunSessionPopupCyclePane(t *testing.T) {
	t.Parallel()

	store := &stubPreviewStore{
		cyclePaneResult: corepreview.CycleResult{
			Cursor: corepreview.Cursor{
				WindowIndex: "3",
				PaneIndex:   "8",
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
			{WindowIndex: "2", Index: "4", Active: true},
			{WindowIndex: "3", Index: "7", Active: true},
			{WindowIndex: "3", Index: "8"},
		},
	}

	app := &App{
		sessionPopup: &sessionPopupCommand{
			store:     store,
			inventory: inventory,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"session-popup", "cycle-pane", "dev", "next"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := inventory.sessionPanesSession, "dev"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionWindowsSession, "dev"; got != want {
		t.Fatalf("SessionWindows session = %q, want %q", got, want)
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
	const wantOutput = "" +
		"session: dev\n" +
		"selected: window=3 pane=8\n" +
		"windows:\n" +
		"    2\n" +
		"  * 3\n" +
		"panes:\n" +
		"    7\n" +
		"  * 8\n"
	if got := stdout.String(); got != wantOutput {
		t.Fatalf("stdout = %q, want %q", got, wantOutput)
	}
}

func TestAppRunSessionPopupCycleWindow(t *testing.T) {
	t.Parallel()

	store := &stubPreviewStore{
		cycleWindowResult: corepreview.CycleResult{
			Cursor: corepreview.Cursor{
				WindowIndex: "4",
				PaneIndex:   "0",
			},
			Selected: true,
			Changed:  true,
		},
	}
	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{
			{Index: "3", Active: true},
			{Index: "4"},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "3", Index: "1", Active: true},
			{WindowIndex: "4", Index: "0"},
		},
	}

	cmd := &sessionPopupCommand{
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
	const wantOutput = "" +
		"session: dev\n" +
		"selected: window=4 pane=0\n" +
		"windows:\n" +
		"    3\n" +
		"  * 4\n" +
		"panes:\n" +
		"  * 0\n"
	if got := stdout.String(); got != wantOutput {
		t.Fatalf("stdout = %q, want %q", got, wantOutput)
	}
}

func TestSessionPopupPreviewReportsNoSelectionModel(t *testing.T) {
	t.Parallel()

	cmd := &sessionPopupCommand{
		store: &stubPreviewStore{},
		inventory: &stubPreviewInventory{
			panes: []corepreview.Pane{
				{WindowIndex: "1", Index: "0", Active: true},
			},
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"preview", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	const wantOutput = "" +
		"session: dev\n" +
		"selected: none\n" +
		"windows:\n" +
		"  (none)\n" +
		"panes:\n" +
		"  (none)\n"
	if got := stdout.String(); got != wantOutput {
		t.Fatalf("stdout = %q, want %q", got, wantOutput)
	}
}

func TestSessionPopupCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: nil, want: "session-popup requires a subcommand"},
		{name: "unknown subcommand", args: []string{"nope"}, want: "unknown session-popup subcommand: nope"},
		{name: "missing preview args", args: []string{"preview"}, want: "session-popup preview requires exactly 1 argument"},
		{name: "blank session", args: []string{"preview", " "}, want: "session-popup preview requires a non-empty <session> argument"},
		{name: "missing cycle args", args: []string{"cycle-pane"}, want: "session-popup cycle-pane requires exactly 2 arguments"},
		{name: "bad cycle direction", args: []string{"cycle-window", "dev", "later"}, want: "direction must be <next|prev>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&sessionPopupCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
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

func TestSessionPopupCommandReportsConfigurationAndRuntimeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *sessionPopupCommand
		args []string
		want string
	}{
		{name: "store setup", cmd: &sessionPopupCommand{storeErr: errors.New("no state dir")}, want: "configure session-popup store"},
		{name: "inventory setup", cmd: &sessionPopupCommand{store: &stubPreviewStore{}, inventoryErr: errors.New("missing adapter")}, want: "configure session-popup inventory"},
		{
			name: "selection load",
			cmd: &sessionPopupCommand{
				store:     &stubPreviewStore{readErr: errors.New("read failed")},
				inventory: &stubPreviewInventory{},
			},
			want: "load popup preview selection",
		},
		{
			name: "window load",
			cmd: &sessionPopupCommand{
				store:     &stubPreviewStore{},
				inventory: &stubPreviewInventory{windowsErr: errors.New("tmux failed")},
			},
			want: "load popup preview windows",
		},
		{
			name: "pane load",
			cmd: &sessionPopupCommand{
				store: &stubPreviewStore{},
				inventory: &stubPreviewInventory{
					windows:  []corepreview.Window{{Index: "1", Active: true}},
					panesErr: errors.New("tmux panes failed"),
				},
			},
			want: "load popup preview panes",
		},
		{
			name: "cycle pane load",
			cmd: &sessionPopupCommand{
				store: &stubPreviewStore{},
				inventory: &stubPreviewInventory{
					panesErr: errors.New("tmux panes failed"),
				},
			},
			want: "load popup cycle panes",
			args: []string{"cycle-pane", "dev", "next"},
		},
		{
			name: "cycle window store",
			cmd: &sessionPopupCommand{
				store: &stubPreviewStore{
					cycleWindowErr: errors.New("write failed"),
				},
				inventory: &stubPreviewInventory{
					windows: []corepreview.Window{{Index: "1", Active: true}},
					panes:   []corepreview.Pane{{WindowIndex: "1", Index: "0", Active: true}},
				},
			},
			want: "cycle popup window",
			args: []string{"cycle-window", "dev", "next"},
		},
		{
			name: "cycle pane no selection",
			cmd: &sessionPopupCommand{
				store: &stubPreviewStore{
					cyclePaneResult: corepreview.CycleResult{},
				},
				inventory: &stubPreviewInventory{},
			},
			want: "found no panes",
			args: []string{"cycle-pane", "dev", "next"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := tt.args
			if len(args) == 0 {
				args = []string{"preview", "dev"}
			}

			err := tt.cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
