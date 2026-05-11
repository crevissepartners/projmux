package app

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakePopupTTY is a no-op popupTTY that records whether Read was called and
// returns the configured byte stream. It lets tests exercise popupWaitKey
// without depending on /dev/tty.
type fakePopupTTY struct {
	io.Reader
	closed   bool
	name     string
	readErr  error
	readN    int
	readBuf  []byte
	readDone chan struct{}
}

func (f *fakePopupTTY) Close() error {
	f.closed = true
	return nil
}

func (f *fakePopupTTY) Name() string { return f.name }

func (f *fakePopupTTY) Read(p []byte) (int, error) {
	if f.readDone != nil {
		defer close(f.readDone)
	}
	if f.readErr != nil {
		return 0, f.readErr
	}
	if len(f.readBuf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, f.readBuf)
	f.readN += n
	return n, nil
}

func TestPopupWaitKeyConsumesOneByte(t *testing.T) {
	t.Parallel()

	tty := &fakePopupTTY{readBuf: []byte("a"), name: "/dev/tty"}
	restored := false
	cmd := &popupWaitKeyCommand{
		openTTY: func() (popupTTY, error) { return tty, nil },
		setRawMode: func(_ popupTTY) (func(), error) {
			return func() { restored = true }, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tty.readN != 1 {
		t.Fatalf("Read bytes = %d, want 1", tty.readN)
	}
	if !tty.closed {
		t.Fatalf("Close() not called on tty")
	}
	if !restored {
		t.Fatalf("setRawMode restore func not invoked")
	}
}

func TestPopupWaitKeyRecoversWhenTTYOpenFails(t *testing.T) {
	t.Parallel()

	// Even when /dev/tty cannot be opened (e.g. some container substrates
	// without a controlling terminal), Run must succeed so the popup
	// payload still has a clean exit. The fallback path reads from stdin;
	// for the test we only assert the error contract — not the byte read.
	cmd := &popupWaitKeyCommand{
		openTTY: func() (popupTTY, error) { return nil, errors.New("no tty") },
		setRawMode: func(_ popupTTY) (func(), error) {
			return func() {}, nil
		},
	}
	// Use a goroutine + closed stdin sentinel: os.Stdin in `go test` is
	// typically /dev/null, so Read returns 0, io.EOF immediately. That's
	// the behavior we want — fallback returns nil without hanging.
	done := make(chan error, 1)
	go func() { done <- cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	}
}

func TestPopupWaitKeyTolerantOfSetRawModeFailure(t *testing.T) {
	t.Parallel()

	tty := &fakePopupTTY{readBuf: []byte("x"), name: "/dev/tty"}
	cmd := &popupWaitKeyCommand{
		openTTY: func() (popupTTY, error) { return tty, nil },
		setRawMode: func(_ popupTTY) (func(), error) {
			return func() {}, errors.New("stty unavailable")
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tty.readN != 1 {
		t.Fatalf("Read bytes = %d, want 1", tty.readN)
	}
}

func TestPopupWaitKeyNotConfiguredReturnsError(t *testing.T) {
	t.Parallel()

	cmd := &popupWaitKeyCommand{}
	err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Run() error = %v, want not-configured error", err)
	}
}
