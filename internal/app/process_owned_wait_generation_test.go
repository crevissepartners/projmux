package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// generationOwnedHandle is a provider that runs until Stop and asks the
// production create Transactions.Current about its generation, as a host
// Handle does. fail, when set, replaces that answer.
type generationOwnedHandle struct {
	*triggerOwnedHandle
	current func(context.Context, processhost.Binding) error
	fail    atomic.Pointer[error]
	checks  atomic.Int32
	checked chan struct{}
}

func (h *generationOwnedHandle) CheckGeneration(ctx context.Context, b processhost.Binding) error {
	defer func() {
		h.checks.Add(1)
		select {
		case h.checked <- struct{}{}:
		default:
		}
	}()
	if err := h.fail.Load(); err != nil {
		return *err
	}
	return h.current(ctx, b)
}

func newGenerationOwnedHandle(store *intmetadata.Store, provider string, b processhost.Binding) *generationOwnedHandle {
	reg, _ := store.LoadReadOnly()
	pane, _ := reg.Pane(b.Pane)
	conversation := pane.Status.ProcessSession.SessionID
	if provider == aiModeCodex {
		conversation = pane.Status.ProcessSession.ThreadID
	}
	ready := processhost.Snapshot{Binding: b, Provider: provider, State: "ready", Session: conversation, Connection: b.Operation}
	return &generationOwnedHandle{
		triggerOwnedHandle: &triggerOwnedHandle{ready: ready, stopped: make(chan struct{}), observed: make(chan struct{})},
		current:            (&createCommand{}).processCreateTransactions(store.Path()).Current,
		checked:            make(chan struct{}, 64),
	}
}

func (h *generationOwnedHandle) awaitChecks(t *testing.T, n int32) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for h.checks.Load() < n {
		select {
		case <-h.checked:
		case <-timeout:
			t.Fatalf("owned Wait made %d generation checks, want %d", h.checks.Load(), n)
		}
	}
}

func ownedWaitForGenerationTest(t *testing.T, owner *processAgentCreateResult) (processOwnedWait, *atomic.Int32) {
	var failed atomic.Int32
	return processOwnedWait{owner: owner, binding: owner.Binding,
		changed:   func(processhost.Snapshot) error { return nil },
		controls:  func(context.Context) error { return nil },
		attention: func() error { return nil },
		fail: func(err error) error {
			failed.Add(1)
			return err
		}}, &failed
}

func TestProcessOwnedWaitStopsAbandonedGenerationWithoutReceipt(t *testing.T) {
	abandon := map[string]func(t *testing.T, store *intmetadata.Store, b processhost.Binding){
		"deleted": func(t *testing.T, store *intmetadata.Store, b processhost.Binding) {
			if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				return intmetadata.DefaultMutator().DeleteAgent(reg, b.Agent)
			}); err != nil {
				t.Fatal(err)
			}
		},
		"moved": func(t *testing.T, store *intmetadata.Store, b processhost.Binding) {
			if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				pane, _ := reg.Pane(b.Pane)
				pane.Status.ProcessSession.Binding.Generation = "generation-after"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		},
		"restored": func(t *testing.T, store *intmetadata.Store, b processhost.Binding) {
			// The backup predates this generation: its Pane holds the one before.
			reg, err := store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			pane, _ := reg.Pane(b.Pane)
			pane.Status.ProcessSession.Binding.Generation = "generation-before"
			backup := intmetadata.NewStore(intmetadata.PathFor(t.TempDir()))
			if _, _, err := backup.UpdateConvergent(func(r *coremetadata.Registry) error { *r = reg.Clone(); return nil }); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(backup.Path())
			if err != nil {
				t.Fatal(err)
			}
			restored := store.Path() + ".restore"
			if err := os.WriteFile(restored, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(restored, store.Path()); err != nil {
				t.Fatal(err)
			}
		},
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for name, abandonGeneration := range abandon {
			t.Run(provider+"/"+name, func(t *testing.T) {
				store, b := sessionBindingFixture(t, provider)
				handle := newGenerationOwnedHandle(store, provider, b)
				owner := &processAgentCreateResult{Binding: b, Handle: handle, Provider: provider, registryPath: store.Path(),
					generationChecks: processGenerationSchedule{first: 20 * time.Millisecond, max: 40 * time.Millisecond}}
				wait, failed := ownedWaitForGenerationTest(t, owner)
				ctx, end := context.WithCancel(context.Background())
				defer end()
				done := make(chan error, 1)
				go func() { done <- wait.run(ctx, end, nil, nil) }()

				// The current generation survives its checks.
				handle.awaitChecks(t, 2)
				if handle.stops.Load() != 0 {
					t.Fatal("current generation was stopped")
				}
				abandonGeneration(t, store, b)
				var err error
				select {
				case err = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("abandoned owner did not end")
				}
				if handle.stops.Load() != 1 {
					t.Fatalf("provider stops=%d, want 1", handle.stops.Load())
				}
				if failed.Load() != 0 {
					t.Fatalf("abandoned owner printed its own cleanup guidance: %v", err)
				}
				if owner.waitRecorded || owner.waitReceipt != nil {
					t.Fatal("abandoned generation recorded a Wait")
				}
				journal, jerr := terminationJournalForRegistryPath(store.Path())
				if jerr != nil {
					t.Fatal(jerr)
				}
				if receipts, rerr := journal.read(); rerr != nil || len(receipts) != 0 {
					t.Fatalf("termination journal has %d receipts, want 0: %v", len(receipts), rerr)
				}
				reg, _ := store.LoadReadOnly()
				if pane, ok := reg.Pane(b.Pane); ok && coremetadata.MatchesProcessWait(metadataProcessBinding(b), pane.Status.LastTermination) {
					t.Fatalf("Registry holds the abandoned generation's Wait: %+v", pane.Status.LastTermination)
				}
			})
		}
	}
}

func TestProcessOwnedWaitKeepsCurrentGenerationThroughReadFailures(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			store, b := sessionBindingFixture(t, provider)
			handle := newGenerationOwnedHandle(store, provider, b)
			owner := &processAgentCreateResult{Binding: b, Handle: handle, Provider: provider, registryPath: store.Path(),
				generationChecks: processGenerationSchedule{first: 10 * time.Millisecond, max: 20 * time.Millisecond}}
			wait, failed := ownedWaitForGenerationTest(t, owner)
			ctx, end := context.WithCancel(context.Background())
			defer end()
			done := make(chan error, 1)
			go func() { done <- wait.run(ctx, end, nil, nil) }()

			handle.awaitChecks(t, 3)
			// A Registry that cannot be read decides nothing.
			for _, transient := range []error{errors.New("metadata: read registry: input/output error"), context.DeadlineExceeded} {
				handle.fail.Store(&transient)
				handle.awaitChecks(t, handle.checks.Load()+3)
			}
			handle.fail.Store(nil)
			handle.awaitChecks(t, handle.checks.Load()+2)
			if handle.stops.Load() != 0 {
				t.Fatal("generation checks stopped a current generation")
			}
			end()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Wait exit 0 returned %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("lifetime did not end the owned Wait")
			}
			if failed.Load() != 0 || !owner.waitRecorded {
				t.Fatalf("current generation failed=%d waitRecorded=%v", failed.Load(), owner.waitRecorded)
			}
		})
	}
}

func TestProcessOwnedWaitRenewsLeaseDirectory(t *testing.T) {
	root := t.TempDir()
	path := intmetadata.PathFor(root)
	b := processhost.Binding{Host: "op", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "op"}
	dir := claudeActivationLeaseDir(path, b.Pane, b.Generation)
	requireClaudeLeaseDirRemoved(t, func() string { return dir })
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "host.sock")
	listener, err := localipc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	aged := time.Now().Add(-9 * 24 * time.Hour)
	if err := os.Chtimes(dir, aged, aged); err != nil {
		t.Fatal(err)
	}
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		t.Fatal(err)
	}

	handle := &generationOwnedHandle{
		triggerOwnedHandle: &triggerOwnedHandle{ready: processhost.Snapshot{Binding: b, State: "ready"}, stopped: make(chan struct{}), observed: make(chan struct{})},
		current:            func(context.Context, processhost.Binding) error { return nil },
		checked:            make(chan struct{}, 64),
	}
	owner := &processAgentCreateResult{Binding: b, Handle: handle, registryPath: path,
		generationChecks: processGenerationSchedule{first: 10 * time.Millisecond, max: 10 * time.Millisecond}}
	ctx, end := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = owner.waitProcessAgent(ctx, func(processhost.Snapshot) error { return nil })
	}()
	t.Cleanup(func() { end(); <-done })

	renewed := func(name string) bool {
		info, err := os.Lstat(name)
		return err == nil && time.Since(info.ModTime()) < time.Hour
	}
	handle.awaitChecks(t, 1)
	deadline := time.After(10 * time.Second)
	for !renewed(dir) {
		select {
		case <-handle.checked:
		case <-deadline:
			t.Fatal("generation checks did not renew the lease directory")
		}
	}
	// Socket owners recognize their socket by change time: renewal never
	// touches it, so the owner still removes it on close.
	if after, err := localipc.InspectOwnedSocket(socket); err != nil || after != identity {
		t.Fatalf("renewal changed the owner's socket identity: %v", err)
	}

	// A renewal never creates a lease directory that is gone.
	end()
	<-done
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket owner could not remove its socket after renewals: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	touchProcessLeaseDir(dir, time.Now())
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("renewal created the lease directory: %v", err)
	}
}

func TestProcessGenerationScheduleBacksOffToItsCap(t *testing.T) {
	var got []time.Duration
	var interval time.Duration
	for range 6 {
		interval = processGenerationSchedule{}.next(interval)
		got = append(got, interval)
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("default schedule %v, want %v", got, want)
		}
	}
}
