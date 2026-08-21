package codexappserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledIsolatedDaemonLifecycleSmoke(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_DAEMON_SMOKE_ROOT"))
	if root == "" {
		t.Skip("set PROJMUX_CODEX_DAEMON_SMOKE_ROOT for the installed Codex daemon smoke")
	}
	root = filepath.Clean(root)
	tempPrefix := filepath.Clean(os.TempDir()) + string(filepath.Separator)
	if !filepath.IsAbs(root) || root == filepath.Clean(os.TempDir()) || !strings.HasPrefix(root, tempPrefix) {
		t.Fatalf("smoke root must be an isolated child of %s", os.TempDir())
	}
	if _, present := os.LookupEnv("TMUX"); present {
		t.Fatal("TMUX must be removed for the installed daemon smoke")
	}
	if _, present := os.LookupEnv("TMUX_PANE"); present {
		t.Fatal("TMUX_PANE must be removed for the installed daemon smoke")
	}
	wantCodexHome := filepath.Join(root, "codex-home")
	if got := filepath.Clean(os.Getenv("CODEX_HOME")); got != wantCodexHome {
		t.Fatalf("CODEX_HOME = %q, want %q", got, wantCodexHome)
	}
	socketPath, ok := defaultControlSocketPath()
	wantSocket := filepath.Join(wantCodexHome, "app-server-control", "app-server-control.sock")
	if !ok || socketPath != wantSocket || !strings.HasPrefix(socketPath, root+string(filepath.Separator)) {
		t.Fatalf("control socket = %q, want contained %q", socketPath, wantSocket)
	}

	first, err := EnsureDefaultProxyReady(context.Background(), TriggerNativeUserAction, "0.13.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Lifecycle != LifecycleStarted && first.Lifecycle != LifecycleAlreadyRunning {
		t.Fatalf("first ensure = %+v", first)
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("stat control socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("control endpoint is not a socket: mode=%s", info.Mode())
	}
	second, err := EnsureDefaultProxyReady(context.Background(), TriggerNativeUserAction, "0.13.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Lifecycle != LifecycleAlreadyRunning {
		t.Fatalf("second ensure = %+v", second)
	}
}
