package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// codexAuthorityPaneState models the three Pane options
// aiCodexLifecycleSink.SetAuthority publishes with three separate tmux
// set-option calls. Each option is individually atomic exactly as tmux makes
// it, and the triple is not: a reader can land on any prefix of a transition.
type codexAuthorityPaneState struct {
	mu        sync.Mutex
	authority string
	epoch     string
	reason    string
}

func (s *codexAuthorityPaneState) set(authority, epoch, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authority, s.epoch, s.reason = authority, epoch, reason
}

func (s *codexAuthorityPaneState) setAuthority(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authority = value
}

func (s *codexAuthorityPaneState) setEpoch(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch = value
}

func (s *codexAuthorityPaneState) setReason(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reason = value
}

func (s *codexAuthorityPaneState) read() (string, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authority, s.epoch, s.reason
}

// publish replays one complete SetAuthority transition in the exact order the
// sink writes it: authority, then epoch, then reason.
func (s *codexAuthorityPaneState) publish(authority, epoch, reason string) {
	s.setAuthority(authority)
	time.Sleep(200 * time.Microsecond)
	s.setEpoch(epoch)
	time.Sleep(200 * time.Microsecond)
	s.setReason(reason)
}

// codexAuthorityPaneLookup is the live tmux binding read, sourced from the
// unsynchronized Pane triple above. It records every snapshot it handed to
// admission so a test can assert no torn triple ever reached the judgment.
type codexAuthorityPaneLookup struct {
	state    *codexAuthorityPaneState
	observed []agentControlLive
}

func (l *codexAuthorityPaneLookup) Live(_ context.Context, paneUID string) (agentControlLive, bool, error) {
	authority, epoch, reason := l.state.read()
	live := agentControlLive{
		RuntimeID: "%7", PaneUID: paneUID, ThreadID: "thread-1",
		Authority: authority, Epoch: epoch, Reason: reason,
	}
	l.observed = append(l.observed, live)
	return live, true, nil
}

func settledAuthorityFixture(t *testing.T) (coremetadata.Registry, coremetadata.Agent) {
	t.Helper()
	_, store, _ := exactControlCLICommand(t)
	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("control fixture is missing its Codex Agent")
	}
	return store.registry, *agent
}

// TestSettledCodexAuthorityAdmissionIgnoresTornAuthorityWrites is the C-5
// Guarantee row. The writer publishes (authority, epoch, reason) as three
// sequential tmux writes under its own fence; admission must judge only a
// completed transition. Each torn row below is injected as the Pane state that
// exists while the writer still holds the fence, and the settled row is what
// the writer leaves behind once it releases it.
func TestSettledCodexAuthorityAdmissionIgnoresTornAuthorityWrites(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		// midWrite is the triple visible while the transition is in flight.
		midWrite [3]string
		// settled is the triple the completed transition leaves behind.
		settled [3]string
		// tornWouldRefuse records whether judging the mid-write triple would
		// have refused a turn on a connection that is in fact alive.
		tornWouldRefuse bool
		wantEpoch       string
		wantRefusal     string
	}{
		{
			name:            "authority written epoch and reason still pending",
			midWrite:        [3]string{codexAuthorityControlPlane, "", "endpoint-suspended"},
			settled:         [3]string{codexAuthorityControlPlane, "752812-41", "ready"},
			tornWouldRefuse: true,
			wantEpoch:       "752812-41",
		},
		{
			name:      "authority and epoch written reason still pending",
			midWrite:  [3]string{codexAuthorityControlPlane, "752812-40", "endpoint-suspended"},
			settled:   [3]string{codexAuthorityControlPlane, "752812-41", "ready"},
			wantEpoch: "752812-41",
		},
		{
			name:      "already settled control plane",
			midWrite:  [3]string{codexAuthorityControlPlane, "752812-41", "ready"},
			settled:   [3]string{codexAuthorityControlPlane, "752812-41", "ready"},
			wantEpoch: "752812-41",
		},
		{
			name:            "settled loss to the provider hook",
			midWrite:        [3]string{codexAuthorityHook, "", "endpoint-suspended"},
			settled:         [3]string{codexAuthorityHook, "", "endpoint-suspended"},
			tornWouldRefuse: true,
			wantRefusal:     "the native connection epoch is unavailable (endpoint-suspended)",
		},
		{
			name:            "settled pending connection",
			midWrite:        [3]string{codexAuthorityPending, "", "connecting"},
			settled:         [3]string{codexAuthorityPending, "", "connecting"},
			tornWouldRefuse: true,
			wantRefusal:     "the native connection epoch is unavailable (connecting)",
		},
		{
			name:            "settled invalidation without an epoch",
			midWrite:        [3]string{codexAuthorityInvalidating, "", ""},
			settled:         [3]string{codexAuthorityInvalidating, "", ""},
			tornWouldRefuse: true,
			wantRefusal:     "the native connection epoch is unavailable (not-ready)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registry, agent := settledAuthorityFixture(t)
			state := &codexAuthorityPaneState{}
			state.set(test.midWrite[0], test.midWrite[1], test.midWrite[2])
			lookup := &codexAuthorityPaneLookup{state: state}

			// The unfenced control: judging the in-flight triple directly is
			// exactly the defect, so assert whether that snapshot refuses.
			torn, _, err := lookup.Live(context.Background(), "pan-alpha-codex")
			if err != nil {
				t.Fatal(err)
			}
			_, tornErr := resolveExactAgentControlBinding(registry, agent, torn, true, "/tmp/projmux-settled")
			tornRefused := tornErr != nil && strings.Contains(tornErr.Error(), "the native connection epoch is unavailable")
			if tornRefused != test.tornWouldRefuse {
				t.Fatalf("unfenced judgment of %v refused=%t (%v), want refused=%t", test.midWrite, tornRefused, tornErr, test.tornWouldRefuse)
			}

			// The fence is what forces the writer to finish first: a reader can
			// only enter once the remaining writes have landed.
			acquire := func(context.Context, string) (func(), error) {
				state.set(test.settled[0], test.settled[1], test.settled[2])
				return func() {}, nil
			}
			live, observed, err := readSettledAgentControlBinding(context.Background(), lookup, acquire, "pan-alpha-codex")
			if err != nil || !observed {
				t.Fatalf("settled read observed=%t err=%v", observed, err)
			}
			if live.Authority != test.settled[0] || live.Epoch != test.settled[1] || live.Reason != test.settled[2] {
				t.Fatalf("settled snapshot = %+v, want %v", live, test.settled)
			}

			binding, err := resolveExactAgentControlBinding(registry, agent, live, observed, "/tmp/projmux-settled")
			if test.wantRefusal != "" {
				var bindingErr *exactAgentControlBindingError
				if err == nil || !errors.As(err, &bindingErr) || bindingErr.Reason != test.wantRefusal+"; Open Codex" {
					t.Fatalf("refusal = %v, want %q", err, test.wantRefusal)
				}
				return
			}
			if err != nil {
				t.Fatalf("settled admission refused a live connection: %v", err)
			}
			if binding.Epoch != test.wantEpoch {
				t.Fatalf("admitted epoch = %q, want %q", binding.Epoch, test.wantEpoch)
			}
		})
	}
}

// TestSettledCodexAuthorityReadKeepsTheRegistryRefusalForAPanelessAgent pins
// the one case that must stay unfenced: an Agent with no current Pane has no
// fence to take, and the refusal must remain the Registry judgment that owns it
// rather than a fence-path error.
func TestSettledCodexAuthorityReadKeepsTheRegistryRefusalForAPanelessAgent(t *testing.T) {
	t.Parallel()
	state := &codexAuthorityPaneState{}
	lookup := &codexAuthorityPaneLookup{state: state}
	acquired := false
	acquire := func(context.Context, string) (func(), error) {
		acquired = true
		return nil, errors.New("fence must not be taken for a Pane-less Agent")
	}
	if _, _, err := readSettledAgentControlBinding(context.Background(), lookup, acquire, "  "); err != nil {
		t.Fatalf("Pane-less read = %v, want the lookup answer", err)
	}
	if acquired {
		t.Fatal("Pane-less read reached for an authority fence")
	}
}

// TestCodexAuthorityAdmissionTakesTheExactWriterFence pins the reader to the
// writer's kernel lock rather than a second, independent one. A separate fence
// would serialize nothing and reintroduce the torn read.
func TestCodexAuthorityAdmissionTakesTheExactWriterFence(t *testing.T) {
	home := t.TempDir()
	writer := testAICommand(home)
	paths, err := configPaths(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}

	writerPath, err := writer.codexAuthorityFencePath("pan-alpha-codex")
	if err != nil {
		t.Fatal(err)
	}
	readerPath, err := codexAuthorityFencePathIn(paths.StateDir, "pan-alpha-codex")
	if err != nil {
		t.Fatal(err)
	}
	if writerPath != readerPath {
		t.Fatalf("reader fence %q is not the writer fence %q", readerPath, writerPath)
	}

	release, err := writer.acquireCodexAuthorityFence("pan-alpha-codex")
	if err != nil {
		t.Fatal(err)
	}
	// While the writer holds the transition the admission read must wait, and
	// it must give the wait up at the control deadline instead of hanging.
	bounded, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := acquireCodexAuthorityReadFenceIn(bounded, paths.StateDir, "pan-alpha-codex"); err == nil {
		t.Fatal("admission read entered the fence while a transition was in flight")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded fence wait error = %v, want the control deadline", err)
	}
	if waited := time.Since(start); waited < 20*time.Millisecond {
		t.Fatalf("admission read waited %s, want a real wait on the writer fence", waited)
	}

	release()
	readRelease, err := acquireCodexAuthorityReadFenceIn(context.Background(), paths.StateDir, "pan-alpha-codex")
	if err != nil {
		t.Fatalf("admission read after the transition completed: %v", err)
	}
	// The reader's own hold is exclusive too, so a concurrent transition cannot
	// start publishing underneath an admission snapshot.
	// #nosec G304 -- test-owned fence path under t.TempDir().
	contender, err := os.OpenFile(readerPath, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("writer contention during an admission read = %v, want busy", err)
	}
	readRelease()
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("fence after the admission read released: %v", err)
	}
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

// TestConcurrentCodexAuthorityTransitionsNeverExposeATornAdmissionSnapshot runs
// the real fence between a transition writer and the real
// `agent turn`/`agent approval` admission path. Every snapshot admission reads
// must be one of the two complete triples the writer publishes; observing any
// other triple is a torn read, and observing a control-plane authority with no
// epoch is the false refusal this Phase closes.
func TestConcurrentCodexAuthorityTransitionsNeverExposeATornAdmissionSnapshot(t *testing.T) {
	home := t.TempDir()
	writer := testAICommand(home)
	paths, err := configPaths(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cmd, _, _ := exactControlCLICommand(t)
	cmd.controlPaths = func() (config.Paths, error) { return paths, nil }
	state := &codexAuthorityPaneState{}
	state.set(codexAuthorityHook, "", "endpoint-suspended")
	lookup := &codexAuthorityPaneLookup{state: state}
	cmd.controlBinding = lookup

	const transitions = 120
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range transitions {
			release, err := writer.acquireCodexAuthorityFence("pan-alpha-codex")
			if err != nil {
				return
			}
			state.publish(codexAuthorityControlPlane, fmt.Sprintf("752812-%d", i), "ready")
			release()
			runtime.Gosched()
			release, err = writer.acquireCodexAuthorityFence("pan-alpha-codex")
			if err != nil {
				return
			}
			state.publish(codexAuthorityHook, "", "endpoint-suspended")
			release()
			runtime.Gosched()
		}
	}()

	admitted, refused := 0, 0
	for {
		select {
		case <-done:
			if len(lookup.observed) == 0 {
				t.Fatal("admission performed no live reads")
			}
			for _, live := range lookup.observed {
				settledLive := live.Authority == codexAuthorityControlPlane && live.Epoch != "" && live.Reason == "ready"
				settledLost := live.Authority == codexAuthorityHook && live.Epoch == "" && live.Reason == "endpoint-suspended"
				if !settledLive && !settledLost {
					t.Fatalf("admission read a torn authority triple: %+v", live)
				}
			}
			t.Logf("admission reads=%d admitted=%d refused=%d", len(lookup.observed), admitted, refused)
			return
		default:
		}
		binding, err := cmd.resolveControlBinding("agent turn interrupt", "uid:agt-alpha-codex")
		switch {
		case err == nil:
			if binding.Epoch == "" {
				t.Fatalf("admitted a turn with no control epoch: %+v", binding)
			}
			admitted++
		case strings.Contains(err.Error(), "the native connection epoch is unavailable (endpoint-suspended)"):
			refused++
		default:
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
}
