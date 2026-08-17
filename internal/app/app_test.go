package app

import (
	"bytes"
	"testing"
)

func TestNewWiresPreviewCleanupAcrossSessionKillFlows(t *testing.T) {
	t.Parallel()

	app := New()
	if app.attach.cleanupKilledSession == nil {
		t.Fatal("attach cleanupKilledSession is nil")
	}
	if app.kill.cleanupKilledSession == nil {
		t.Fatal("kill cleanupKilledSession is nil")
	}
	if app.prune.cleanupKilledSession == nil {
		t.Fatal("prune cleanupKilledSession is nil")
	}
	if app.sessions.cleanupKilledSession == nil {
		t.Fatal("sessions cleanupKilledSession is nil")
	}
	if app.switcher.cleanupKilledSession == nil {
		t.Fatal("switch cleanupKilledSession is nil")
	}
}

// TestAppRunDispatchesPopupWaitKey verifies the hidden helper subcommand is
// wired through the internal namespace. The statusbar display-only popups
// embed `<binary> internal popup-wait-key` into their payloads, so a missing
// dispatch entry would silently turn every popup into an Enter-only surface.
func TestAppRunDispatchesPopupWaitKey(t *testing.T) {
	t.Parallel()

	called := false
	app := &App{
		popupWaitKey: &popupWaitKeyCommand{
			openTTY: func() (popupTTY, error) {
				called = true
				return &fakePopupTTY{readBuf: []byte("k"), name: "/dev/tty"}, nil
			},
			setRawMode: func(_ popupTTY) (func(), error) {
				return func() {}, nil
			},
		},
	}
	if err := app.Run([]string{"internal", "popup-wait-key"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !called {
		t.Fatalf("popup-wait-key dispatch did not invoke handler")
	}
}
