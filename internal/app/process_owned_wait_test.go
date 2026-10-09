package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// triggerOwnedHandle is a provider that runs until Stop and then reports its
// actual Wait.
type triggerOwnedHandle struct {
	ready    processhost.Snapshot
	stopped  chan struct{}
	stops    atomic.Int32
	observed chan struct{}
	once     atomic.Bool
}

func (h *triggerOwnedHandle) Observe(processhost.Binding) (processhost.Snapshot, error) {
	if h.once.CompareAndSwap(false, true) {
		close(h.observed)
	}
	return h.ready, nil
}

func (h *triggerOwnedHandle) Events(processhost.Binding, uint64) ([]processhost.Event, processhost.Snapshot, error) {
	return nil, h.ready, nil
}

func (h *triggerOwnedHandle) Wait(ctx context.Context, _ processhost.Binding) (processhost.Snapshot, error) {
	select {
	case <-h.stopped:
		exited := h.ready
		exited.State, exited.Exit = "exited", &processhost.Exit{Code: 0}
		return exited, nil
	case <-ctx.Done():
		return processhost.Snapshot{}, ctx.Err()
	}
}

func (h *triggerOwnedHandle) Stop(processhost.Binding) error {
	if h.stops.Add(1) == 1 {
		close(h.stopped)
	}
	return nil
}

func (h *triggerOwnedHandle) Turn(context.Context, processhost.Authority, string, string) error {
	return nil
}

func TestProcessOwnedWaitInjectedTriggerStopsAndRecordsWaitOnce(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			store, b := sessionBindingFixture(t, provider)
			reg, _ := store.LoadReadOnly()
			pane, _ := reg.Pane(b.Pane)
			conversation := pane.Status.ProcessSession.SessionID
			if provider == aiModeCodex {
				conversation = pane.Status.ProcessSession.ThreadID
			}
			handle := &triggerOwnedHandle{ready: processhost.Snapshot{Binding: b, Provider: provider, State: "ready", Session: conversation, Connection: b.Operation}, stopped: make(chan struct{}), observed: make(chan struct{})}
			owner := &processAgentCreateResult{Binding: b, Handle: handle, Provider: provider, registryPath: store.Path()}
			var changed, controls, attention atomic.Int32
			wait := processOwnedWait{owner: owner, binding: b,
				changed:   func(processhost.Snapshot) error { changed.Add(1); return nil },
				controls:  func(context.Context) error { controls.Add(1); return nil },
				attention: func() error { attention.Add(1); return nil },
				fail:      func(err error) error { t.Errorf("owned Wait failed: %v", err); return err }}

			ctx, end := context.WithCancel(context.Background())
			defer end()
			fire := make(chan struct{})
			started := make(chan struct{})
			trigger := func(end context.CancelFunc) {
				close(started)
				go func() { <-fire; end() }()
			}
			done := make(chan error, 1)
			go func() { done <- wait.run(ctx, end, trigger, nil) }()
			<-started
			select {
			case <-handle.observed:
			case <-time.After(10 * time.Second):
				t.Fatal("owned Wait never observed the provider")
			}
			if handle.stops.Load() != 0 {
				t.Fatal("provider stopped before the trigger fired")
			}
			close(fire)
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Wait exit 0 returned %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("trigger did not end the owned Wait")
			}
			if handle.stops.Load() != 1 || changed.Load() != 1 || attention.Load() != 1 || controls.Load() < 1 {
				t.Fatalf("stops=%d changed=%d controls=%d attention=%d", handle.stops.Load(), changed.Load(), controls.Load(), attention.Load())
			}
			if !owner.waitRecorded {
				t.Fatal("actual Wait was not recorded")
			}
			journal, err := terminationJournalForRegistryPath(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			receipts, err := journal.read()
			if err != nil || len(receipts) != 1 {
				t.Fatalf("termination journal has %d receipts, want 1: %v", len(receipts), err)
			}
			reg, _ = store.LoadReadOnly()
			pane, _ = reg.Pane(b.Pane)
			agent, _ := reg.Agent(b.Agent)
			own := metadataProcessBinding(b)
			if !pane.Status.Activation.IsZero() || !coremetadata.MatchesProcessWait(own, pane.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
				t.Fatalf("Registry lacks the exact Wait: activation=%+v termination=%+v", pane.Status.Activation, pane.Status.LastTermination)
			}
		})
	}
}
