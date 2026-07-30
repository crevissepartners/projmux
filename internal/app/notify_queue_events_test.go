package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifyQueueRefreshTransportPublishesToSubscriber(t *testing.T) {
	t.Parallel()

	transport := newNotifyQueueRefreshTransport(t.TempDir())
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

	transport := newNotifyQueueRefreshTransport(t.TempDir())
	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
}

func TestNotifyQueueRefreshTransportSweepsDeadPIDSocketAndPreservesLivePIDSocket(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	transport := newNotifyQueueRefreshTransport(dir)
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
