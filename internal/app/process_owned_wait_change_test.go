package app

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ownedWaitSyncs records every snapshot owned Wait synchronizes.
type ownedWaitSyncs struct {
	mu        sync.Mutex
	snapshots []processhost.Snapshot
	synced    chan struct{}
}

func newOwnedWaitSyncs() *ownedWaitSyncs { return &ownedWaitSyncs{synced: make(chan struct{}, 1024)} }

func (s *ownedWaitSyncs) record(snapshot processhost.Snapshot) {
	s.mu.Lock()
	s.snapshots = append(s.snapshots, snapshot)
	s.mu.Unlock()
	s.synced <- struct{}{}
}

func (s *ownedWaitSyncs) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.snapshots)
}

// until returns once a snapshot synchronized after the first from satisfies
// predicate.
func (s *ownedWaitSyncs) until(t *testing.T, from int, description string, predicate func(processhost.Snapshot) bool) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for seen := from; ; {
		s.mu.Lock()
		for ; seen < len(s.snapshots); seen++ {
			if predicate(s.snapshots[seen]) {
				s.mu.Unlock()
				return
			}
		}
		s.mu.Unlock()
		select {
		case <-s.synced:
		case <-timeout:
			t.Fatalf("owned Wait never synchronized %s", description)
		}
	}
}

// idle waits until owned Wait has synchronized the Handle's current snapshot,
// then proves it stays asleep while nothing changes: the old loop synchronized
// every 100ms.
func (s *ownedWaitSyncs) idle(t *testing.T, handle processOwnedHandle, binding processhost.Binding) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		current, err := handle.Observe(binding)
		if err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		caughtUp := len(s.snapshots) > 0 && reflect.DeepEqual(s.snapshots[len(s.snapshots)-1], current)
		s.mu.Unlock()
		if caughtUp {
			break
		}
		select {
		case <-s.synced:
		case <-time.After(processOwnedRecheck):
		case <-timeout:
			t.Fatalf("owned Wait never synchronized the current snapshot %+v", current)
		}
	}
	before := s.count()
	time.Sleep(5 * processOwnedRecheck)
	if after := s.count(); after != before {
		for _, x := range s.snapshots[before-1:] {
			t.Logf("synchronized state=%s turn=%s seq=%d pending=%d resv=%q pid=%d fail=%q diag=%d exit=%v", x.State, x.Turn, x.Sequence, len(x.Pending), x.MessageReservation, x.PID, x.Failure, len(x.Diagnostic), x.Exit)
		}
		t.Fatalf("idle owned Wait synchronized %d times", after-before)
	}
}

func runOwnedWaitForTest(t *testing.T, result *processAgentCreateResult, changed func(processhost.Snapshot) error, controls func(context.Context) error, syncs *ownedWaitSyncs) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := result.waitProcessAgent(ctx, processSnapshotSynchronizer(func(snapshot processhost.Snapshot) error {
			defer syncs.record(snapshot)
			return changed(snapshot)
		}, func(snapshot processhost.Snapshot) error {
			defer syncs.record(snapshot)
			if len(snapshot.Pending) > 0 {
				return controls(ctx)
			}
			return nil
		}))
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("owned Wait did not end within its stop bound")
		}
	})
	return cancel
}

// Owned Wait wakes for host changes only: a conversation that binds after the
// first synchronization is recorded from its notification alone, pending
// creation and the turn end after an answer another process stores reach the
// synchronizer without a poll, and an idle owner never wakes, even inside an
// open turn.
func TestProcessOwnedWaitWakesOnlyForHostChanges(t *testing.T) {
	t.Run(aiModeClaude, func(t *testing.T) {
		f := newProcessClaudeFixture(t, nil)
		result := &processAgentCreateResult{Binding: f.binding, Handle: f.handle, Provider: aiModeClaude, registryPath: f.path}
		syncs := newOwnedWaitSyncs()
		runOwnedWaitForTest(t, result, result.recordProcessSnapshot, func(context.Context) error { return nil }, syncs)
		syncs.until(t, 0, "the started provider", func(s processhost.Snapshot) bool { return s.PID > 0 })
		syncs.idle(t, result.Handle, result.Binding)
		mark := syncs.count()
		// The conversation binds only now, after the first synchronization,
		// while the lifetime is still open: the notification alone records it.
		f.turn(t, "first", "hold")
		// hold opens the turn with init, ready, and one assistant output.
		syncs.until(t, mark, "the turn start", func(s processhost.Snapshot) bool { return s.Turn == "first" && s.State == "ready" && s.Sequence >= 3 })
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		pane, _ := reg.Pane(f.binding.Pane)
		if record := pane.Status.ProcessSession; record == nil || record.SessionID == "" || record.ConnectionID != f.binding.Operation || record.TurnID != "first" {
			t.Fatalf("the conversation bound after the first synchronization was not recorded: %+v", record)
		}
		syncs.idle(t, result.Handle, result.Binding)
	})
	t.Run(aiModeCodex, func(t *testing.T) {
		f := newProcessCodexFixture(t, nil)
		result := &processAgentCreateResult{Binding: f.endpoint.binding, Handle: f.endpoint.handle, Provider: aiModeCodex, codexEndpoint: f.endpoint, registryPath: f.path}
		syncs := newOwnedWaitSyncs()
		runOwnedWaitForTest(t, result, func(processhost.Snapshot) error { return f.control.sync(context.Background()) }, f.control.syncControls, syncs)
		syncs.until(t, 0, "the ready provider", func(s processhost.Snapshot) bool { return s.State == "ready" })
		syncs.idle(t, result.Handle, result.Binding)
		mark := syncs.count()
		f.turn(t, "first", "controls")
		syncs.until(t, mark, "both pending controls", func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
		q, err := f.control.questions.List(f.endpoint.binding.Agent)
		if err != nil || len(q) != 1 {
			t.Fatalf("questions %v %v", q, err)
		}
		a, err := f.control.approvals.List(f.endpoint.binding.Agent)
		if err != nil || len(a) != 1 {
			t.Fatalf("approvals %v %v", a, err)
		}
		// Stored answers send no Handle change; only the pending recheck
		// consumes them.
		mark = syncs.count()
		if _, err = f.control.questions.Answer(q[0].ID, f.endpoint.binding.Agent, map[string]string{"q": `["blue"]`}); err != nil {
			t.Fatal(err)
		}
		if _, err = f.control.approvals.Answer(a[0].ID, f.endpoint.binding.Agent, false, "fixture"); err != nil {
			t.Fatal(err)
		}
		syncs.until(t, mark, "the answered turn end", func(s processhost.Snapshot) bool { return s.Turn == "" && len(s.Pending) == 0 })
		syncs.idle(t, result.Handle, result.Binding)
	})
}
