package codexappserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstalledIsolatedConversationCatalogSmoke(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_CATALOG_SMOKE_ROOT"))
	if root == "" {
		t.Skip("set PROJMUX_CODEX_CATALOG_SMOKE_ROOT for the installed Codex catalog smoke")
	}
	root = filepath.Clean(root)
	tmpRoot := filepath.Clean("/tmp")
	if !filepath.IsAbs(root) || root == tmpRoot || !strings.HasPrefix(root, tmpRoot+string(filepath.Separator)) {
		t.Fatalf("smoke root must be an isolated child of %s", tmpRoot)
	}
	if _, present := os.LookupEnv("TMUX"); present {
		t.Fatal("TMUX must be removed for the installed catalog smoke")
	}
	if _, present := os.LookupEnv("TMUX_PANE"); present {
		t.Fatal("TMUX_PANE must be removed for the installed catalog smoke")
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	health, err := EnsureDefaultProxyReady(ctx, TriggerNativeUserAction, "0.13.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if health.Source != SourceAppServer || health.Availability != AvailabilityAvailable {
		t.Fatalf("catalog ensure = %+v", health)
	}
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("stat control socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("control endpoint is not a socket: mode=%s", info.Mode())
	}

	client, err := OpenDefaultProxy(ctx, DefaultProbeTimeout, "0.13.0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	page, err := client.ListCatalogThreads(ctx, CatalogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Threads) == 0 {
		return
	}
	listedID := page.Threads[0].ID
	thread, err := client.ReadCatalogThread(ctx, listedID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.ID != listedID {
		t.Fatalf("thread/read id = %q, want listed id %q", thread.ID, listedID)
	}
}
