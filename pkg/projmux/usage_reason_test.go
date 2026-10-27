package projmux

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

func TestSDKUsagePreservesRefreshReasons(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	for _, reason := range []usage.SnapshotReason{"", "unknown", "app-server-unavailable", "app-server-unsupported", "account-unsupported", "app-server-timeout", "app-server-protocol-error", "app-server-disconnected"} {
		for _, fallback := range []bool{false, true} {
			dir := t.TempDir()
			now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
			row := usage.Snapshot{Model: "codex", Window: usage.Window5h, Pct: 21, UpdatedAt: now, Source: usage.SourceAppServer}
			row.StaleReason = reason
			if fallback {
				row.Source, row.StaleReason, row.FallbackReason = usage.SourceRollout, "", reason
			}
			store := usage.NewStore(dir)
			if err := store.SaveState(usage.State{Snapshots: []usage.Snapshot{row}}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(store.FilePath())
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewClient(Options{StateDir: filepath.Join(root, "state"), UsageStateDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			cache, err := client.GetUsage(context.Background())
			if err != nil || len(cache.Snapshots) != 1 {
				t.Fatalf("GetUsage = %#v, %v", cache, err)
			}
			got := cache.Snapshots[0]
			if string(got.StaleReason) != string(row.StaleReason) || string(got.FallbackReason) != string(row.FallbackReason) || string(got.Source) != string(row.Source) || float64(got.Pct) != row.Pct || !got.UpdatedAt.Equal(row.UpdatedAt) {
				t.Fatalf("SDK row = %#v; want %#v", got, row)
			}
			after, err := os.ReadFile(store.FilePath())
			if err != nil || string(before) != string(after) {
				t.Fatalf("SDK changed saved bytes: %v", err)
			}
		}
	}
}
