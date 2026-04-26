package app

import (
	"bytes"
	"context"
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
			{Index: "2", Name: "shell", PaneCount: 1, Path: "~/", Active: true},
			{Index: "3", Name: "dev", PaneCount: 2, Path: "~rp/dev"},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "2", Index: "4", Title: "shell", Command: "zsh", Path: "~/", Active: true},
			{WindowIndex: "3", Index: "7", Title: "server", Command: "go", Path: "~rp/dev"},
			{ID: "%8", WindowIndex: "3", Index: "8", Title: "tests", Command: "gotest", Path: "~rp/dev"},
		},
		snapshot: "go test ./...\nok",
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
	if got, want := inventory.snapshotTarget, "%8"; got != want {
		t.Fatalf("CapturePane target = %q, want %q", got, want)
	}
	if got, want := inventory.snapshotStartLine, -80; got != want {
		t.Fatalf("CapturePane start line = %d, want %d", got, want)
	}

	const wantOutput = "" +
		"\x1b[1m\x1b[36mSession\x1b[0m\n" +
		"  \x1b[2mname\x1b[0m  dev\n" +
		"  \x1b[2mwindows\x1b[0m  2\n" +
		"  \x1b[2mpane\x1b[0m  8 (window 3)\n" +
		"  \x1b[2mcmd\x1b[0m  gotest\n" +
		"  \x1b[2mtitle\x1b[0m  tests\n" +
		"  \x1b[2mpath\x1b[0m  ~rp/dev\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[2] shell               1p\n" +
		"\x1b[1m\x1b[32m[3] dev                 2p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"[3.7] server             go\n" +
		"\x1b[1m\x1b[32m[3.8] tests              gotest\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPane Snapshot\x1b[0m\n" +
		"\x1b[2m────────────────────────────────────────────────────────────────\x1b[0m\n" +
		"go test ./...\nok\n"
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
			{Index: "2", Name: "shell", PaneCount: 1, Path: "~/", Active: true},
			{Index: "3", Name: "dev", PaneCount: 2, Path: "~rp/dev"},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "2", Index: "4", Title: "shell", Command: "zsh", Path: "~/", Active: true},
			{WindowIndex: "3", Index: "7", Title: "server", Command: "go", Path: "~rp/dev", Active: true},
			{WindowIndex: "3", Index: "8", Title: "tests", Command: "gotest", Path: "~rp/dev"},
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
		"\x1b[1m\x1b[36mSession\x1b[0m\n" +
		"  \x1b[2mname\x1b[0m  dev\n" +
		"  \x1b[2mwindows\x1b[0m  2\n" +
		"  \x1b[2mpane\x1b[0m  8 (window 3)\n" +
		"  \x1b[2mcmd\x1b[0m  gotest\n" +
		"  \x1b[2mtitle\x1b[0m  tests\n" +
		"  \x1b[2mpath\x1b[0m  ~rp/dev\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[2] shell               1p\n" +
		"\x1b[1m\x1b[32m[3] dev                 2p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"[3.7] server             go\n" +
		"\x1b[1m\x1b[32m[3.8] tests              gotest\x1b[0m\n"
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
			{Index: "3", Name: "dev", PaneCount: 1, Path: "~rp/dev", Active: true},
			{Index: "4", Name: "logs", PaneCount: 1, Path: "~rp/dev"},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "3", Index: "1", Title: "server", Command: "go", Path: "~rp/dev", Active: true},
			{WindowIndex: "4", Index: "0", Title: "tail", Command: "tail", Path: "~rp/dev"},
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
		"\x1b[1m\x1b[36mSession\x1b[0m\n" +
		"  \x1b[2mname\x1b[0m  dev\n" +
		"  \x1b[2mwindows\x1b[0m  2\n" +
		"  \x1b[2mpane\x1b[0m  0 (window 4)\n" +
		"  \x1b[2mcmd\x1b[0m  tail\n" +
		"  \x1b[2mtitle\x1b[0m  tail\n" +
		"  \x1b[2mpath\x1b[0m  ~rp/dev\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[3] dev                 1p\n" +
		"\x1b[1m\x1b[32m[4] logs                1p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"\x1b[1m\x1b[32m[4.0] tail               tail\x1b[0m\n"
	if got := stdout.String(); got != wantOutput {
		t.Fatalf("stdout = %q, want %q", got, wantOutput)
	}
}

func TestAppRunSessionPopupOpen(t *testing.T) {
	t.Parallel()

	store := &stubPreviewStore{
		readSelection: corepreview.Selection{
			SessionName: "dev",
			WindowIndex: "3",
			PaneIndex:   "8",
		},
		readFound: true,
	}
	opener := &stubSessionPopupOpener{}

	app := &App{
		sessionPopup: &sessionPopupCommand{
			store:  store,
			opener: opener,
		},
	}

	if err := app.Run([]string{"session-popup", "open", "dev"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.readSession, "dev"; got != want {
		t.Fatalf("ReadSelection session = %q, want %q", got, want)
	}
	if got, want := opener.sessionName, "dev"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
	if got, want := opener.windowIndex, "3"; got != want {
		t.Fatalf("open window = %q, want %q", got, want)
	}
	if got, want := opener.paneIndex, "8"; got != want {
		t.Fatalf("open pane = %q, want %q", got, want)
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
		"\x1b[1m\x1b[36mSession\x1b[0m\n" +
		"  \x1b[2mname\x1b[0m  dev\n" +
		"  \x1b[2mwindows\x1b[0m  0\n" +
		"  \x1b[2mpane\x1b[0m  ? (window ?)\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"(none)\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"(none)\n"
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
		{name: "missing open args", args: []string{"open"}, want: "session-popup open requires exactly 1 argument"},
		{name: "blank open session", args: []string{"open", " "}, want: "session-popup open requires a non-empty <session> argument"},
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
		{name: "opener setup", cmd: &sessionPopupCommand{store: &stubPreviewStore{}, openerErr: errors.New("missing tmux adapter")}, args: []string{"open", "dev"}, want: "configure session-popup opener"},
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
			name: "open selection load",
			cmd: &sessionPopupCommand{
				store:  &stubPreviewStore{readErr: errors.New("read failed")},
				opener: &stubSessionPopupOpener{},
			},
			want: "load popup open selection",
			args: []string{"open", "dev"},
		},
		{
			name: "open target",
			cmd: &sessionPopupCommand{
				store: &stubPreviewStore{
					readSelection: corepreview.Selection{SessionName: "dev", WindowIndex: "2", PaneIndex: "1"},
					readFound:     true,
				},
				opener: &stubSessionPopupOpener{err: errors.New("switch failed")},
			},
			want: "open popup target",
			args: []string{"open", "dev"},
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

type stubSessionPopupOpener struct {
	sessionName string
	windowIndex string
	paneIndex   string
	err         error
}

func (s *stubSessionPopupOpener) OpenSessionTarget(_ context.Context, sessionName, windowIndex, paneIndex string) error {
	s.sessionName = sessionName
	s.windowIndex = windowIndex
	s.paneIndex = paneIndex
	return s.err
}
