package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

func TestAppRunPruneEphemeralKillsTargetsBeyondKeep(t *testing.T) {
	t.Parallel()

	client := &recordingPruneClient{
		inventory: []lifecycle.SessionInventory{
			{Name: "newest", Ephemeral: true, LastAttached: 30},
			{Name: "middle", Ephemeral: true, LastAttached: 20},
			{Name: "older", Ephemeral: true, LastAttached: 10},
		},
	}
	reconcileCalls := 0
	app := &App{
		prune: &pruneCommand{
			inventory:       client,
			killer:          client,
			reconcileNotify: func() { reconcileCalls++ },
		},
	}

	if err := app.Run([]string{"prune", "ephemeral", "--keep=2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := client.killed, []string{"older"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("KillSession calls = %#v, want %#v", got, want)
	}
	if reconcileCalls != 1 {
		t.Fatalf("notify reconcile calls = %d, want 1 after all kills", reconcileCalls)
	}
}

func TestPruneDoesNotReconcileNotifyWithoutKilledTargets(t *testing.T) {
	t.Parallel()

	client := &recordingPruneClient{
		inventory: []lifecycle.SessionInventory{{Name: "only", Ephemeral: true, LastAttached: 10}},
	}
	reconcileCalls := 0
	cmd := &pruneCommand{
		inventory:       client,
		killer:          client,
		reconcileNotify: func() { reconcileCalls++ },
	}
	if err := cmd.Run([]string{"ephemeral", "--keep=1"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if reconcileCalls != 0 {
		t.Fatalf("notify reconcile calls = %d, want 0", reconcileCalls)
	}
}

func TestPruneReconcilesNotifyAfterPartialKillFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		failAt             int
		wantReconcileCalls int
		wantSuccesses      int
	}{
		{
			name:               "partial success then failure",
			failAt:             1,
			wantReconcileCalls: 1,
			wantSuccesses:      1,
		},
		{
			name:          "first kill fails",
			failAt:        0,
			wantSuccesses: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inventory := pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
				return []lifecycle.SessionInventory{
					{Name: "newest", Ephemeral: true, LastAttached: 30},
					{Name: "middle", Ephemeral: true, LastAttached: 20},
					{Name: "oldest", Ephemeral: true, LastAttached: 10},
				}, nil
			})
			killer := &failingPruneKiller{failAt: tt.failAt}
			reconcileCalls := 0
			var cleaned []string
			cmd := &pruneCommand{
				inventory:       inventory,
				killer:          killer,
				reconcileNotify: func() { reconcileCalls++ },
				cleanupKilledSession: func(sessionName string) {
					cleaned = append(cleaned, sessionName)
				},
			}

			err := cmd.Run([]string{"ephemeral", "--keep=0"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "kill ephemeral session") {
				t.Fatalf("Run() error = %v, want kill failure", err)
			}
			if reconcileCalls != tt.wantReconcileCalls {
				t.Fatalf("notify reconcile calls = %d, want %d", reconcileCalls, tt.wantReconcileCalls)
			}
			if len(killer.successes) != tt.wantSuccesses {
				t.Fatalf("successful kills = %v, want count %d", killer.successes, tt.wantSuccesses)
			}
			if !reflect.DeepEqual(cleaned, killer.successes) {
				t.Fatalf("cleaned sessions = %v, want successful kills %v", cleaned, killer.successes)
			}
		})
	}
}

func TestPruneEphemeralDeletesPreviewStateButPreservesSessionSnapshot(t *testing.T) {
	t.Parallel()

	previewPath := filepath.Join(t.TempDir(), "preview-state")
	previewStore := corepreview.NewStore(previewPath)
	if err := previewStore.WriteSelection("old", "1", "2"); err != nil {
		t.Fatalf("WriteSelection() error = %v", err)
	}
	snapshotStore := sessionstate.NewStore(t.TempDir())
	savePruneSnapshot(t, snapshotStore, "old", time.Now().Add(-90*24*time.Hour))

	client := &recordingPruneClient{
		inventory: []lifecycle.SessionInventory{{Name: "old", Ephemeral: true, LastAttached: 10}},
	}
	cmd := &pruneCommand{
		inventory: client,
		killer:    client,
		cleanupKilledSession: (&killedSessionPreviewCleaner{
			store: previewStore,
		}).cleanup,
	}
	if err := cmd.Run([]string{"ephemeral", "--keep=0"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, found, err := previewStore.ReadSelection("old"); err != nil {
		t.Fatalf("ReadSelection() error = %v", err)
	} else if found {
		t.Fatal("preview selection still exists after session prune")
	}
	if _, err := snapshotStore.Load("old"); err != nil {
		t.Fatalf("session snapshot was implicitly deleted: %v", err)
	}
}

func TestKilledSessionPreviewCleanerIsBestEffort(t *testing.T) {
	t.Parallel()

	called := false
	cleaner := &killedSessionPreviewCleaner{
		store: previewSelectionDeleterFunc(func(string) error {
			called = true
			return errors.New("read-only state")
		}),
	}
	cleaner.cleanup("old")
	if !called {
		t.Fatal("preview cleanup was not attempted")
	}
}

func TestPruneSessionStateListsDeadAndOldWithoutDeletingSnapshots(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	store := sessionstate.NewStore(t.TempDir())
	savePruneSnapshot(t, store, "dead-recent", now.Add(-time.Hour))
	savePruneSnapshot(t, store, "live-old", now.Add(-60*24*time.Hour))
	savePruneSnapshot(t, store, "live-recent", now.Add(-time.Hour))

	cmd := &pruneCommand{
		liveSessions: pruneLiveSessionResolverFunc(func(context.Context) (map[string]bool, error) {
			return map[string]bool{
				"live-old":    true,
				"live-recent": true,
			}, nil
		}),
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
		now:          func() time.Time { return now },
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"session-state", "--older-than=720h"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "dead-recent\tdead\t") {
		t.Fatalf("output = %q, want dead recent snapshot", output)
	}
	if !strings.Contains(output, "live-old\told\t") {
		t.Fatalf("output = %q, want old live persistent snapshot", output)
	}
	if strings.Contains(output, "live-recent") {
		t.Fatalf("output = %q, do not want recent live persistent snapshot", output)
	}
	for _, sessionName := range []string{"dead-recent", "live-old", "live-recent"} {
		if _, err := store.Load(sessionName); err != nil {
			t.Fatalf("Load(%q) after list error = %v, listing must never delete", sessionName, err)
		}
	}
}

func TestPruneSessionStateDeleteRemovesOnlyExplicitSnapshots(t *testing.T) {
	t.Parallel()

	store := sessionstate.NewStore(t.TempDir())
	now := time.Now()
	savePruneSnapshot(t, store, "remove-me", now)
	savePruneSnapshot(t, store, "keep-me", now)
	cmd := &pruneCommand{
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"session-state", "delete", "remove-me"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "deleted session snapshot: remove-me\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if _, err := store.Load("remove-me"); !errors.Is(err, sessionstate.ErrNotFound) {
		t.Fatalf("Load(remove-me) error = %v, want not found", err)
	}
	if _, err := store.Load("keep-me"); err != nil {
		t.Fatalf("Load(keep-me) error = %v, explicit delete removed another snapshot", err)
	}
}

func TestPruneSessionStateListingWarnsAndPreservesMalformedSnapshots(t *testing.T) {
	t.Parallel()

	store := sessionstate.NewStore(t.TempDir())
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(store.Dir, "broken.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cmd := &pruneCommand{
		liveSessions: pruneLiveSessionResolverFunc(func(context.Context) (map[string]bool, error) {
			return nil, nil
		}),
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
		now:          time.Now,
	}
	var stderr bytes.Buffer
	if err := cmd.Run([]string{"session-state"}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stderr.String(), `warning: inspect session snapshot "broken"`) {
		t.Fatalf("stderr = %q, want malformed warning", stderr.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("malformed snapshot was implicitly deleted: %v", err)
	}
}

func TestPruneCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		want      string
		wantUsage bool
	}{
		{
			name:      "missing subcommand",
			args:      nil,
			want:      "prune requires a subcommand",
			wantUsage: true,
		},
		{
			name:      "unknown subcommand",
			args:      []string{"nope"},
			want:      "unknown prune subcommand: nope",
			wantUsage: true,
		},
		{
			name:      "positional arguments",
			args:      []string{"ephemeral", "extra"},
			want:      "prune ephemeral does not accept positional arguments",
			wantUsage: true,
		},
		{
			name:      "session-state positional arguments",
			args:      []string{"session-state", "extra"},
			want:      "prune session-state does not accept positional arguments",
			wantUsage: true,
		},
		{
			name:      "session-state delete missing name",
			args:      []string{"session-state", "delete"},
			want:      "prune session-state delete requires at least 1 session",
			wantUsage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&pruneCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if tt.wantUsage && !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestPruneCommandPropagatesSetupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *pruneCommand
		want string
	}{
		{
			name: "inventory missing",
			cmd:  &pruneCommand{},
			want: "resolve ephemeral sessions to prune",
		},
		{
			name: "inventory error",
			cmd: &pruneCommand{
				inventory: pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, errors.New("tmux exploded")
				}),
				killer: &recordingPruneClient{},
			},
			want: "resolve ephemeral sessions to prune",
		},
		{
			name: "plan error",
			cmd: &pruneCommand{
				inventory: pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return nil, nil
				}),
				killer: &recordingPruneClient{},
			},
			want: "plan ephemeral prune",
		},
		{
			name: "killer missing",
			cmd: &pruneCommand{
				inventory: pruneInventoryResolverFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
					return []lifecycle.SessionInventory{{Name: "older", Ephemeral: true, LastAttached: 10}}, nil
				}),
			},
			want: "kill ephemeral sessions to prune",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run([]string{"ephemeral", "--keep=-1"}, &bytes.Buffer{}, &bytes.Buffer{})
			if tt.name == "killer missing" {
				err = tt.cmd.Run([]string{"ephemeral", "--keep=0"}, &bytes.Buffer{}, &bytes.Buffer{})
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type recordingPruneClient struct {
	inventory []lifecycle.SessionInventory
	killed    []string
}

type failingPruneKiller struct {
	failAt    int
	calls     int
	successes []string
}

func (k *failingPruneKiller) KillSession(_ context.Context, sessionName string) error {
	call := k.calls
	k.calls++
	if call == k.failAt {
		return errors.New("kill failed")
	}
	k.successes = append(k.successes, sessionName)
	return nil
}

func (c *recordingPruneClient) ListEphemeralSessions(context.Context) ([]lifecycle.SessionInventory, error) {
	return c.inventory, nil
}

func (c *recordingPruneClient) KillSession(_ context.Context, sessionName string) error {
	c.killed = append(c.killed, sessionName)
	return nil
}

type pruneInventoryResolverFunc func(context.Context) ([]lifecycle.SessionInventory, error)

func (fn pruneInventoryResolverFunc) ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error) {
	return fn(ctx)
}

type pruneLiveSessionResolverFunc func(context.Context) (map[string]bool, error)

func (fn pruneLiveSessionResolverFunc) ExistingSessions(ctx context.Context) (map[string]bool, error) {
	return fn(ctx)
}

type previewSelectionDeleterFunc func(string) error

func (fn previewSelectionDeleterFunc) Delete(sessionName string) error {
	return fn(sessionName)
}

func savePruneSnapshot(t *testing.T, store sessionstate.Store, sessionName string, savedAt time.Time) {
	t.Helper()
	if err := store.Save(sessionstate.Snapshot{
		Version: sessionstate.Version,
		Session: sessionName,
		SavedAt: savedAt,
		Windows: []sessionstate.Window{{
			Index: 0,
			Name:  "main",
			Panes: []sessionstate.Pane{{
				Index:  0,
				CWD:    "/tmp/" + sessionName,
				Recipe: sessionstate.ShellRecipe(),
			}},
		}},
	}); err != nil {
		t.Fatalf("Save(%q) error = %v", sessionName, err)
	}
}
