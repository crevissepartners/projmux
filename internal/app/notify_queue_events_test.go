package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestNotifyQueueRefreshTransport builds a transport over stateDir and, when
// the socket dir falls back outside stateDir (t.TempDir() is too long for a
// unix socket path), removes that fallback dir from os.TempDir() on cleanup.
func newTestNotifyQueueRefreshTransport(t *testing.T, stateDir string) notifyQueueRefreshTransport {
	t.Helper()
	transport := newNotifyQueueRefreshTransport(stateDir)
	if transport.dir != "" && !notifyQueueTestPathWithin(stateDir, transport.dir) {
		// Runs after t.Context() is cancelled; a subscriber goroutine may be
		// removing its socket concurrently, which RemoveAll tolerates.
		t.Cleanup(func() {
			if err := os.RemoveAll(transport.dir); err != nil {
				t.Errorf("RemoveAll(%q) error = %v", transport.dir, err)
			}
		})
	}
	return transport
}

func notifyQueueTestPathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestNotifyQueueRefreshTransportPublishesToSubscriber(t *testing.T) {
	t.Parallel()

	transport := newTestNotifyQueueRefreshTransport(t, shortTempDomain(t))
	ctx := t.Context()

	events, err := transport.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notify queue refresh event")
	}
}

func TestNotifyQueueRefreshTransportPublishWithoutSubscribersIsNoop(t *testing.T) {
	t.Parallel()

	transport := newTestNotifyQueueRefreshTransport(t, t.TempDir())
	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
}

func TestNotifyQueueRefreshTransportSweepsDeadPIDSocketAndPreservesLivePIDSocket(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	transport := newTestNotifyQueueRefreshTransport(t, dir)
	if err := os.MkdirAll(transport.dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	deadPath := filepath.Join(transport.dir, "refresh-1001-1.sock")
	livePath := filepath.Join(transport.dir, "refresh-1002-2.sock")
	for _, path := range []string{deadPath, livePath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	transport.processAlive = func(pid int) bool {
		return pid == 1002
	}

	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Fatalf("dead socket Stat() error = %v, want not exist", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live socket Stat() error = %v, want preserved", err)
	}
}

// TestNotifyQueueRefreshTransportTestHelperRemovesFallbackDir guards against
// leaving projmux-notify-queue-events-* dirs in TMPDIR on every test run.
func TestNotifyQueueRefreshTransportTestHelperRemovesFallbackDir(t *testing.T) {
	var dir string
	t.Run("fallback", func(t *testing.T) {
		stateDir := t.TempDir()
		transport := newTestNotifyQueueRefreshTransport(t, stateDir)
		dir = transport.dir
		if notifyQueueTestPathWithin(stateDir, dir) || !notifyQueueTestPathWithin(os.TempDir(), dir) {
			t.Fatalf("transport dir = %q, want fallback under %q outside %q", dir, os.TempDir(), stateDir)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "refresh-1002-2.sock"), nil, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	})
	if dir == "" {
		t.Fatal("fallback subtest did not record a transport dir")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		_ = os.RemoveAll(dir)
		t.Fatalf("fallback dir Stat(%q) error = %v, want not exist after subtest cleanup", dir, err)
	}
}

func TestNotifyQueueEventSocketPID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		pid  int
		ok   bool
	}{
		{name: "valid", path: "/tmp/refresh-123-9.sock", pid: 123, ok: true},
		{name: "missing sequence", path: "/tmp/refresh-123.sock"},
		{name: "non numeric pid", path: "/tmp/refresh-nope-9.sock"},
		{name: "wrong suffix", path: "/tmp/refresh-123-9.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pid, ok := notifyQueueEventSocketPID(tt.path)
			if pid != tt.pid || ok != tt.ok {
				t.Fatalf("notifyQueueEventSocketPID(%q) = (%d, %v), want (%d, %v)", tt.path, pid, ok, tt.pid, tt.ok)
			}
		})
	}
}
