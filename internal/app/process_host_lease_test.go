package app

import (
	"context"
	"errors"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeProcessStopReclaimsHostLease(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	dir := filepath.Dir(processClaudeHostSocket(f.path, f.binding.Pane, f.binding.Generation))
	assertProcessLeaseRemoved(t, dir, func() {
		if err := f.handle.Stop(f.binding); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.handle.Wait(ctx, f.binding); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCodexProcessStopReclaimsHostLease(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	assertProcessLeaseRemoved(t, filepath.Dir(f.endpoint.socket), func() {
		if err := f.endpoint.handle.Stop(f.endpoint.binding); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.endpoint.handle.Wait(ctx, f.endpoint.binding); err != nil {
			t.Fatal(err)
		}
	})
}

func assertProcessLeaseRemoved(t *testing.T, dir string, stop func()) {
	t.Helper()
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(dir) })
	stop()
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("lease remains after Stop/Wait: %s (%v)", dir, err)
	}
}

func TestClaudeProcessLeaseOwnership(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	t.Run("existing directory", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, err := listenProcessHost(filepath.Join(dir, "host.sock")); !errors.Is(err, os.ErrExist) {
			t.Fatalf("existing dir: %v", err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("listen failure rollback", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lease")
		if _, _, err := listenProcessHost(filepath.Join(dir, strings.Repeat("x", 100)+".sock")); err == nil {
			t.Fatal("oversized socket must fail")
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Fatalf("failed listener left lease: %v", err)
		}
	})
	t.Run("foreign entry", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lease")
		_, closeLease, err := listenProcessHost(filepath.Join(dir, "host.sock"))
		if err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(dir, "other")
		if err := os.WriteFile(other, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := closeLease(ctx); err == nil {
			t.Fatal("nonempty directory cleanup should report failure")
		}
		if raw, err := os.ReadFile(other); err != nil || string(raw) != "keep" {
			t.Fatalf("foreign entry changed: %s %v", raw, err)
		}
	})
	t.Run("replaced empty directory", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "lease")
		socket := filepath.Join(dir, "host.sock")
		owned, closeLease, err := listenProcessHost(socket)
		if err != nil {
			t.Fatal(err)
		}
		moved := filepath.Join(root, "moved")
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		defer localipc.RemoveOwnedSocket(filepath.Join(moved, "host.sock"), owned.Identity())
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := closeLease(context.Background()); err == nil {
			t.Fatal("empty replacement must refuse cleanup")
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			t.Fatalf("empty replacement removed: %v", err)
		}
	})
	t.Run("replaced directory and socket", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "lease")
		socket := filepath.Join(dir, "host.sock")
		owned, closeLease, err := listenProcessHost(socket)
		if err != nil {
			t.Fatal(err)
		}
		moved := filepath.Join(root, "moved")
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		defer localipc.RemoveOwnedSocket(filepath.Join(moved, "host.sock"), owned.Identity())
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer replacement.Close()
		if err := os.Chmod(socket, 0600); err != nil {
			t.Fatal(err)
		}
		identity, err := localipc.InspectOwnedSocket(socket)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := closeLease(ctx); err == nil {
			t.Fatal("replacement must refuse directory cleanup")
		}
		current, err := localipc.InspectOwnedSocket(socket)
		if err != nil || current != identity {
			t.Fatalf("replacement socket changed: %v", err)
		}
	})
}
