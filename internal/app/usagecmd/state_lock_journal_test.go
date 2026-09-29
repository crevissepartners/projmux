package usagecmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// stateLockJournalHarness is a Claude-scoped Command whose status-line and
// gesture managers share one private usage state dir, so a test can hold that
// dir's snapshot state lock from another file descriptor and read back what
// the journal recorded.
type stateLockJournalHarness struct {
	cmd     *Command
	store   *usage.Store
	adapter usage.Adapter
	journal *diagnostics.Store
}

func newStateLockJournalHarness(t *testing.T, adapter usage.Adapter) *stateLockJournalHarness {
	t.Helper()
	stateDir := t.TempDir()
	now := func() time.Time { return time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC) }
	registry := usage.NewRegistry()
	if err := registry.Replace(adapter); err != nil {
		t.Fatal(err)
	}
	store := usage.NewStore(stateDir)
	manager := usage.NewManager(registry, store, now)

	cmd := New(now)
	isolateUsageCommandEnv(t, cmd)
	cmd.managerFn = func([]string) (*usage.Manager, error) { return manager, nil }
	cmd.readOnlyManagerFn = func([]string) (*usage.Manager, error) { return manager, nil }
	cmd.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentClaude}, nil
	}
	journalFn, journal := newTestUsageJournal(t)
	cmd.journalFn = journalFn
	return &stateLockJournalHarness{cmd: cmd, store: store, adapter: adapter, journal: journal}
}

// holdUsageStateLock takes the snapshot state lock from a separate open file
// description, the way a stuck holder in another process would, and returns
// the release. The lock is released at cleanup if the test does not.
func holdUsageStateLock(t *testing.T, store *usage.Store) func() {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(store.LockPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(store.LockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX); err != nil {
		_ = holder.Close()
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_ = unix.Flock(int(holder.Fd()), unix.LOCK_UN)
		_ = holder.Close()
	}
	t.Cleanup(release)
	return release
}

// usageRows returns the journal's usage rows.
func (h *stateLockJournalHarness) usageRows(t *testing.T) []diagnostics.Event {
	t.Helper()
	events, err := h.journal.Read()
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	rows := make([]diagnostics.Event, 0, len(events))
	for _, event := range events {
		if event.Component == "usage" {
			rows = append(rows, event)
		}
	}
	return rows
}

// requireStateLockTimeoutRow asserts that row is the closed state lock
// timeout record: projmux, no source, an error that refreshed nothing.
func requireStateLockTimeoutRow(t *testing.T, row diagnostics.Event) {
	t.Helper()
	if row.Event != "usage.collect.outcome" || row.Provider != string(diagnostics.ProviderProjmux) ||
		row.Source != "" || row.Failure != string(diagnostics.UsageFailureStateLockTimeout) ||
		row.Level != "error" || row.Result != "error" || row.Kind != "runtime" {
		t.Fatalf("journal row = %#v, want usage.collect.outcome projmux/state-lock-timeout error/error/runtime without source", row)
	}
	if row.Message != "" {
		t.Fatalf("journal row carried the lock error text: %q", row.Message)
	}
}

// TestStateLockTimeoutLandsInJournalOncePerRun covers every journal entry
// point that can wait on the state lock: the status line's throttled and
// forced refreshes, the background refresh, and `projmux agent usage`. Each
// run that times out records exactly one projmux/state-lock-timeout row.
func TestStateLockTimeoutLandsInJournalOncePerRun(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T, cmd *Command){
		"status refresh": func(t *testing.T, cmd *Command) {
			if err := cmd.RunStatus(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("RunStatus: %v", err)
			}
		},
		"forced status refresh": func(t *testing.T, cmd *Command) {
			if err := cmd.RunStatus([]string{"--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("RunStatus --force: %v", err)
			}
		},
		"background refresh": func(t *testing.T, cmd *Command) {
			if _, err := cmd.BackgroundRefresh(context.Background()); !errors.Is(err, usage.ErrStateLockTimeout) {
				t.Fatalf("BackgroundRefresh err = %v, want ErrStateLockTimeout", err)
			}
		},
		"agent usage": func(t *testing.T, cmd *Command) {
			if err := cmd.Run([]string{"--model", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run: %v", err)
			}
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			adapter := &stubAdapter{name: "claude", snaps: []usage.Snapshot{{Model: "claude", Window: usage.Window5h, Pct: 7}}}
			h := newStateLockJournalHarness(t, adapter)
			holdUsageStateLock(t, h.store)

			run(t, h.cmd)

			if adapter.collectCalls != 0 {
				t.Fatalf("collect calls = %d, want 0: a lock timeout walks nothing", adapter.collectCalls)
			}
			rows := h.usageRows(t)
			if len(rows) != 1 {
				t.Fatalf("journal rows = %#v, want exactly one", rows)
			}
			requireStateLockTimeoutRow(t, rows[0])
		})
	}
}

// TestStateLockTimeoutRepeatedInOneRunRecordsOneRow pins the suppression:
// a refresh loop that keeps timing out inside one process adds one row.
func TestStateLockTimeoutRepeatedInOneRunRecordsOneRow(t *testing.T) {
	t.Parallel()

	h := newStateLockJournalHarness(t, &stubAdapter{name: "claude"})
	holdUsageStateLock(t, h.store)
	for i := range 2 {
		if _, err := h.cmd.BackgroundRefresh(context.Background()); !errors.Is(err, usage.ErrStateLockTimeout) {
			t.Fatalf("BackgroundRefresh #%d err = %v, want ErrStateLockTimeout", i, err)
		}
	}
	rows := h.usageRows(t)
	if len(rows) != 1 {
		t.Fatalf("journal rows = %#v, want one for two timeouts in one run", rows)
	}
	requireStateLockTimeoutRow(t, rows[0])
}

// TestStateLockFreeSuccessfulRefreshRecordsNoRow is the negative control: the
// same harness without a lock holder and with a healthy adapter stays silent.
func TestStateLockFreeSuccessfulRefreshRecordsNoRow(t *testing.T) {
	t.Parallel()

	adapter := &stubAdapter{name: "claude", snaps: []usage.Snapshot{{Model: "claude", Window: usage.Window5h, Pct: 7}}}
	h := newStateLockJournalHarness(t, adapter)
	if refreshed, err := h.cmd.BackgroundRefresh(context.Background()); err != nil || !refreshed {
		t.Fatalf("BackgroundRefresh = %v, %v, want a clean refresh", refreshed, err)
	}
	if err := h.cmd.Run([]string{"--model", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if adapter.collectCalls != 2 {
		t.Fatalf("collect calls = %d, want 2", adapter.collectCalls)
	}
	if rows := h.usageRows(t); len(rows) != 0 {
		t.Fatalf("journal rows = %#v, want none for a healthy refresh", rows)
	}
}

// lockTakingAdapter fails its collection and, while it runs, lets another
// holder take the state lock, so the walk's commit times out after the
// adapter already failed and the two errors arrive joined.
type lockTakingAdapter struct {
	take func()
}

func (a *lockTakingAdapter) Name() string { return "claude" }

func (a *lockTakingAdapter) Collect(context.Context) ([]usage.Snapshot, error) {
	a.take()
	return nil, errors.New("claude: usage endpoint returned status 500")
}

// TestStateLockTimeoutJoinedWithAdapterFailureRecordsBoth covers the commit
// timeout: the adapter failure and the lock timeout each keep their row.
func TestStateLockTimeoutJoinedWithAdapterFailureRecordsBoth(t *testing.T) {
	t.Parallel()

	adapter := &lockTakingAdapter{}
	h := newStateLockJournalHarness(t, adapter)
	adapter.take = func() { holdUsageStateLock(t, h.store) }
	if err := h.cmd.Run([]string{"--model", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rows := h.usageRows(t)
	if len(rows) != 2 {
		t.Fatalf("journal rows = %#v, want the adapter failure and the lock timeout", rows)
	}
	var sawAdapter, sawLock bool
	for _, row := range rows {
		switch row.Provider {
		case string(diagnostics.ProviderClaude):
			if row.Failure != string(diagnostics.UsageFailureCollect) {
				t.Fatalf("claude row = %#v, want collect-failed", row)
			}
			sawAdapter = true
		case string(diagnostics.ProviderProjmux):
			requireStateLockTimeoutRow(t, row)
			sawLock = true
		}
	}
	if !sawAdapter || !sawLock {
		t.Fatalf("journal rows = %#v, want one claude and one projmux row", rows)
	}
}
