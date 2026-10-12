package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

type attentionRetryHandle struct{ *generationOwnedHandle }

func (h *attentionRetryHandle) Changed(processhost.Binding) (<-chan struct{}, error) {
	return make(chan struct{}), nil
}
func (h *attentionRetryHandle) Events(_ processhost.Binding, after uint64) ([]processhost.Event, processhost.Snapshot, error) {
	if after >= 1 {
		return nil, h.ready, nil
	}
	return []processhost.Event{{Sequence: 1, Kind: "turn-result", Raw: []byte(`{"subtype":"success"}`)}}, h.ready, nil
}

type attentionRetryQueue struct {
	*notify.Store
	fail      atomic.Bool
	attempts  atomic.Int32
	afterPush func()
	sabotaged atomic.Bool
}

func (q *attentionRetryQueue) Push(in notify.PushInput) (notify.Notification, notify.PushResult, error) {
	q.attempts.Add(1)
	if q.fail.Load() {
		return notify.Notification{}, notify.PushResult{}, errors.New("acquire notify lock: exhausted 200 attempts")
	}
	entry, result, err := q.Store.Push(in)
	if err == nil && q.afterPush != nil && q.sabotaged.CompareAndSwap(false, true) {
		q.afterPush()
	}
	return entry, result, err
}

func attentionRetryUntil(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal(description)
		}
	}
}

// Both providers and all three launch paths use this owned Wait seam. A Changed
// handle with no pending controls goes fully idle after the first snapshot.
func TestProcessOwnedAttentionFailureRetriesUnchangedSnapshot(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, fault := range []string{"attention-lock", "notify-push", "attention-io"} {
			t.Run(provider+"/"+fault, func(t *testing.T) {
				store, b := sessionBindingFixture(t, provider)
				h := &attentionRetryHandle{newGenerationOwnedHandle(store, provider, b)}
				h.ready.Sequence = 1
				s, q, projection := newProcessAttentionFixture(t, t.TempDir(), b, provider)
				s.lockWaitLimit = 20 * time.Millisecond
				queue := &attentionRetryQueue{Store: q}
				projection.queue = queue
				release := func() {}
				switch fault {
				case "attention-lock":
					lock, err := localstate.AcquireFileLock(s.path+".lock", time.Second, nil)
					if err != nil {
						t.Fatal(err)
					}
					release = func() { _ = lock.Close() }
				case "notify-push":
					queue.fail.Store(true)
					release = func() { queue.fail.Store(false) }
				case "attention-io":
					// The notification is committed, then the attention rename
					// fails. Recovery must replay the same ID without duplication.
					queue.afterPush = func() {
						if err := os.Rename(s.path, s.path+".saved"); err != nil {
							t.Error(err)
							return
						}
						if err := os.Mkdir(s.path, 0700); err != nil {
							t.Error(err)
						}
					}
					release = func() {
						_ = os.Remove(s.path)
						if err := os.Rename(s.path+".saved", s.path); err != nil {
							t.Error(err)
						}
					}
				}
				defer func() { release() }()
				owner := &processAgentCreateResult{Binding: b, Handle: h, Provider: provider, registryPath: store.Path()}
				journal := diagnostics.NewStore(filepath.Join(filepath.Dir(filepath.Dir(store.Path())), diagnostics.LogDirName, diagnostics.LogFileName))
				var attempts, snapshots atomic.Int32
				wait := processOwnedWait{owner: owner, binding: b,
					changed: func(snapshot processhost.Snapshot) error {
						snapshots.Add(1)
						return owner.recordProcessSnapshot(snapshot)
					},
					controls:  func(context.Context) error { return nil },
					attention: func() error { attempts.Add(1); return projection.sync(h, b) },
				}
				ctx, cancel := context.WithCancelCause(context.Background())
				done := make(chan error, 1)
				go func() {
					done <- wait.run(ctx, func() { cancel(processStopCause(diagnostics.OwnerStopStdinEOF)) }, nil, io.Discard)
				}()
				t.Cleanup(func() {
					cancel(processStopCause(diagnostics.OwnerStopStdinEOF))
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("owner failed to exit")
					}
				})
				attentionRetryUntil(t, "no repeated attention attempts", func() bool { return attempts.Load() >= 3 })
				events, err := journal.Read()
				if err != nil || len(events) != 1 || events[0].Event != "agent.attention.sync" || events[0].AgentUID != b.Agent {
					t.Fatalf("failure episode events=%+v err=%v", events, err)
				}
				expected := map[string]string{"attention-lock": "lock-timeout", "notify-push": "notify-write", "attention-io": "io"}[fault]
				if events[0].Code != "attention.sync."+expected {
					t.Fatalf("classification=%s", events[0].Code)
				}
				if h.stops.Load() != 0 {
					t.Fatal("attention failure stopped provider")
				}
				release()
				release = func() {}
				attentionRetryUntil(t, "no recovery event", func() bool { events, _ := journal.Read(); return len(events) == 2 })
				record := attentionRecord(t, s, b.Pane)
				entries, err := q.List()
				if err != nil || record.Sequence != 1 || len(entries) != 1 || entries[0].ID != processAttentionID(b, 1) {
					t.Fatalf("cursor=%d notifications=%+v err=%v", record.Sequence, entries, err)
				}
				if snapshots.Load() != 1 || h.stops.Load() != 0 {
					t.Fatalf("snapshots=%d stops=%d", snapshots.Load(), h.stops.Load())
				}
				events, _ = journal.Read()
				if events[1].Code != "attention.sync.recovered" {
					t.Fatalf("recovery=%+v", events)
				}
				before := attempts.Load()
				timer := time.NewTimer(2 * processOwnedRecheck)
				<-timer.C
				if attempts.Load() != before {
					t.Fatal("successful idle projection keeps polling")
				}
			})
		}
	}
}

func TestProcessOwnedAttentionWriteDoesNotDelayProviderStop(t *testing.T) {
	store, b := sessionBindingFixture(t, aiModeCodex)
	h := &attentionRetryHandle{newGenerationOwnedHandle(store, aiModeCodex, b)}
	owner := &processAgentCreateResult{Binding: b, Handle: h, Provider: aiModeCodex, registryPath: store.Path()}
	started, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(release)
	done := make(chan error, 1)
	go func() {
		_, err := owner.waitProcessAgentWithAttention(ctx, func(processhost.Snapshot) error { return nil }, func() error { close(started); <-release; close(completed); return nil })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("attention never started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("attention IO delayed provider Stop/Wait")
	}
	if h.stops.Load() != 1 {
		t.Fatal("provider was not stopped")
	}
	t.Cleanup(func() {
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Error("attention goroutine did not finish")
		}
	})
}

func TestProcessOwnedAttentionAuthorityAndControlFailuresRemainFatal(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, fault := range []string{"control-response", "registry", "stale-attention", "closed-attention"} {
			t.Run(provider+"/"+fault, func(t *testing.T) {
				store, b := sessionBindingFixture(t, provider)
				h := &attentionRetryHandle{newGenerationOwnedHandle(store, provider, b)}
				owner := &processAgentCreateResult{Binding: b, Handle: h, Provider: provider, registryPath: store.Path()}
				failure := errors.New(fault)
				changed := func(processhost.Snapshot) error { return failure }
				attention := func() error { return nil }
				if fault == "stale-attention" || fault == "closed-attention" {
					failure = processhost.ErrStale
					if fault == "closed-attention" {
						failure = processhost.ErrClosed
					}
					changed = func(processhost.Snapshot) error { return nil }
					attention = func() error { return failure }
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, err := owner.waitProcessAgentWithAttention(ctx, changed, attention)
				if !errors.Is(err, failure) || h.stops.Load() != 1 {
					t.Fatalf("err=%v stops=%d", err, h.stops.Load())
				}
			})
		}
	}
}

func TestProcessOwnedControlResponseFailureStopsProvider(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			store, b := sessionBindingFixture(t, provider)
			h := &attentionRetryHandle{newGenerationOwnedHandle(store, provider, b)}
			h.ready.Pending = []processhost.Request{{ID: "request", Kind: "question"}}
			owner := &processAgentCreateResult{Binding: b, Handle: h, Provider: provider, registryPath: store.Path()}
			failure := errors.New("control response write failed")
			var responses atomic.Int32
			wait := processOwnedWait{owner: owner, binding: b,
				changed:   func(processhost.Snapshot) error { return nil },
				controls:  func(context.Context) error { responses.Add(1); return failure },
				attention: func() error { return nil },
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := wait.run(ctx, cancel, nil, io.Discard)
			if !errors.Is(err, failure) || responses.Load() == 0 || h.stops.Load() != 1 {
				t.Fatalf("err=%v responses=%d stops=%d", err, responses.Load(), h.stops.Load())
			}
		})
	}
}

func TestProcessOwnedAttentionDoesNotStartAfterOwnerCancellation(t *testing.T) {
	store, b := sessionBindingFixture(t, aiModeCodex)
	h := &attentionRetryHandle{newGenerationOwnedHandle(store, aiModeCodex, b)}
	owner := &processAgentCreateResult{Binding: b, Handle: h, Provider: aiModeCodex, registryPath: store.Path()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	done := make(chan error, 1)
	go func() {
		_, err := owner.waitProcessAgentWithAttention(ctx, func(processhost.Snapshot) error { cancel(); return nil }, func() error { attempts.Add(1); return nil })
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("owner cancellation did not finish")
	}
	if attempts.Load() != 0 || h.stops.Load() != 1 {
		t.Fatalf("attention attempts=%d stops=%d", attempts.Load(), h.stops.Load())
	}
}
