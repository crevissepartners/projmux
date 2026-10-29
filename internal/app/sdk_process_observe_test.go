package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// sdkProcessObserveClient is a public SDK client on a fixture owner's isolated
// state.
func sdkProcessObserveClient(t *testing.T, path, root string) *projmux.Client {
	t.Helper()
	client, err := projmux.NewClient(projmux.Options{StateDir: filepath.Dir(filepath.Dir(path)), ConfigDir: filepath.Join(root, "config"), HomeDir: root})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// sameJSONValue reports whether two provider frames hold the same JSON value;
// both empty is the same.
func sameJSONValue(t *testing.T, got, want []byte) bool {
	t.Helper()
	if len(got) == 0 || len(want) == 0 {
		return len(got) == len(want)
	}
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(g, w)
}

// The public observer reads a live owner by its exact Binding, reports the
// host's own gap, continues from Next, refuses another generation, and ends
// with the final Snapshot once the provider's Wait is recorded.
func TestPublicProcessObserverGenerationGapAndTerminal(t *testing.T) {
	f := newProcessCodexFixtureWithEvents(t, nil, 8)
	b := f.endpoint.binding
	var snap processhost.Snapshot
	var direct []processhost.Event
	for i := 0; i < 8 && (len(direct) == 0 || direct[0].Kind != "stream-gap"); i++ {
		after := snap.Sequence
		f.turn(t, fmt.Sprintf("public-gap-%d", i), fmt.Sprintf("echo:<b> & %d", i))
		snap = f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" && s.Sequence > after })
		var err error
		if direct, _, err = f.endpoint.handle.Events(b, 0); err != nil {
			t.Fatal(err)
		}
	}
	if len(direct) == 0 || direct[0].Kind != "stream-gap" {
		t.Fatalf("fixture did not exercise host ring gap: %+v", direct)
	}
	installProcessObservationSchema(t, f.store, b, snap)
	client := sdkProcessObserveClient(t, f.path, f.root)
	ctx := context.Background()
	agent := "uid:" + b.Agent

	current, err := client.ReadAgentProcess(ctx, core.AgentProcessRequest{Agent: agent})
	want := core.AgentProcessBinding{Host: b.Host, Pane: b.Pane, Generation: b.Generation}
	if err != nil || current.Binding != want || current.Provider != aiModeCodex || current.State != "ready" || current.Running || current.Exit != nil || current.Child.PID != snap.PID {
		t.Fatalf("current snapshot: %+v %v", current, err)
	}
	batch, err := client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: agent, Binding: current.Binding})
	if err != nil || !batch.Gap || batch.Terminal || len(batch.Events) != len(direct) || batch.Events[0].Kind != "stream-gap" || batch.Next != direct[len(direct)-1].Sequence || batch.Snapshot.Sequence != batch.Next {
		t.Fatalf("gap batch: %+v %v", batch, err)
	}
	// The owner answer frame re-encodes each provider frame: whitespace is
	// compacted and < > & are escaped, so Payload is compared as JSON values.
	escaped := 0
	for i, event := range batch.Events {
		if event.Sequence != direct[i].Sequence || event.Kind != direct[i].Kind || !sameJSONValue(t, event.Payload, direct[i].Raw) {
			t.Fatalf("event %d: %s want %s", i, event.Payload, direct[i].Raw)
		}
		if bytes.Contains(direct[i].Raw, []byte("<b> &")) {
			escaped++
		}
	}
	if escaped == 0 {
		t.Fatalf("no returned provider frame carried < or &: %+v", direct)
	}
	// Continuing from Next repeats nothing; a bounded Wait returns empty.
	started := time.Now()
	next, err := client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: agent, Binding: current.Binding, After: batch.Next, Wait: 300 * time.Millisecond})
	if elapsed := time.Since(started); err != nil || len(next.Events) != 0 || next.Gap || next.More || next.Next != batch.Next || elapsed < 200*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("continued read after %s: %+v %v", elapsed, next, err)
	}
	stale := current.Binding
	stale.Generation = "old"
	if _, err = client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: agent, Binding: stale}); !errors.Is(err, projmux.ErrProcessStale) {
		t.Fatalf("stale generation: %v", err)
	}

	if err = f.endpoint.handle.Stop(b); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	exited, err := f.endpoint.handle.Wait(waitCtx, b)
	cancel()
	if err != nil || exited.Exit == nil {
		t.Fatalf("actual Wait: %+v %v", exited, err)
	}
	code := exited.Exit.Code
	if _, err = f.store.Update(func(reg *coremetadata.Registry) error {
		pane, _ := reg.Pane(b.Pane)
		pane.Status.LastTermination = &coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.ClassifyProcessExit(code, exited.Exit.Signal), ObservedAt: time.Now(), PaneUID: b.Pane, AgentUID: b.Agent, Generation: b.Generation, OperationID: b.Operation, ExitCode: &code, Signal: exited.Exit.Signal}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	final, err := client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: agent, Binding: current.Binding, After: batch.Next})
	if err != nil || !final.Terminal || final.Snapshot.State != "exited" || final.Snapshot.Exit == nil || final.Snapshot.Exit.Code != code || final.Snapshot.Exit.Signal != exited.Exit.Signal {
		t.Fatalf("terminal batch: %+v %v", final, err)
	}
}

func TestPublicProcessObserverCancelDoesNotStop(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			var binding processhost.Binding
			var path, root string
			var store *intmetadata.Store
			var snap processhost.Snapshot
			var observe func(processhost.Binding) (processhost.Snapshot, error)
			if provider == aiModeClaude {
				f := newProcessClaudeFixture(t, nil)
				f.turn(t, "public-cancel", "question")
				snap = f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) > 0 })
				binding, path, root, store, observe = f.binding, f.path, f.root, f.store, f.handle.Observe
			} else {
				f := newProcessCodexFixture(t, nil)
				f.turn(t, "public-cancel", "controls")
				snap = f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
				binding, path, root, store, observe = f.endpoint.binding, f.path, f.root, f.store, f.endpoint.handle.Observe
			}
			installProcessObservationSchema(t, store, binding, snap)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			client := sdkProcessObserveClient(t, path, root)
			agent := "uid:" + binding.Agent
			public := core.AgentProcessBinding{Host: binding.Host, Pane: binding.Pane, Generation: binding.Generation}
			first, err := client.ReadAgentProcessEvents(context.Background(), core.AgentProcessEventsRequest{Agent: agent, Binding: public})
			if err != nil || len(first.Events) == 0 || first.Terminal || !first.Snapshot.Running || first.Snapshot.Pending != len(snap.Pending) {
				t.Fatalf("first read: %+v %v", first, err)
			}

			// Cancelling a long poll ends only the read.
			ctx, cancel := context.WithCancel(context.Background())
			timer := time.AfterFunc(100*time.Millisecond, cancel)
			started := time.Now()
			_, err = client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: agent, Binding: public, After: first.Next, Wait: 4 * time.Second})
			timer.Stop()
			cancel()
			if !errors.Is(err, context.Canceled) || time.Since(started) > 2*time.Second {
				t.Fatalf("cancelled long poll after %s: %v", time.Since(started), err)
			}
			if current, err := observe(binding); err != nil || current.State != "ready" || current.Exit != nil {
				t.Fatalf("cancelled read changed the owner: %+v %v", current, err)
			}
			again, err := client.ReadAgentProcess(context.Background(), core.AgentProcessRequest{Agent: agent, Binding: public})
			if err != nil || again.State != "ready" || again.Exit != nil || !again.Running {
				t.Fatalf("read after cancel: %+v %v", again, err)
			}

			// An owner that does not advertise the events read is refused before
			// the request; observation still works.
			processHostReadsWithheld.Store(true)
			t.Cleanup(func() { processHostReadsWithheld.Store(false) })
			_, err = client.ReadAgentProcessEvents(context.Background(), core.AgentProcessEventsRequest{Agent: agent, Binding: public})
			var refusal *projmux.Refusal
			if !errors.Is(err, projmux.ErrProcessReadUnsupported) || !errors.As(err, &refusal) || refusal.Code != projmux.CodeProcessReadUnsupported {
				t.Fatalf("old owner events read: %v", err)
			}
			if old, err := client.ReadAgentProcess(context.Background(), core.AgentProcessRequest{Agent: agent, Binding: public}); err != nil || old.State != "ready" {
				t.Fatalf("old owner observation: %+v %v", old, err)
			}
			processHostReadsWithheld.Store(false)

			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("public reads wrote the Registry")
			}
		})
	}
}

// processOwnerProxy stands in front of a fixture owner's socket. It forwards
// each request after delay; while hold is set it signals held and keeps the
// request until release forwards it or drop closes it unanswered.
type processOwnerProxy struct {
	delay   time.Duration
	hold    atomic.Bool
	held    chan struct{}
	release chan struct{}
	drop    chan struct{}
	dropped sync.Once
}

// dropHeld closes every held request unanswered.
func (p *processOwnerProxy) dropHeld() { p.dropped.Do(func() { close(p.drop) }) }

func proxyProcessOwner(t *testing.T, socket string, delay time.Duration) *processOwnerProxy {
	t.Helper()
	p := &processOwnerProxy{delay: delay, held: make(chan struct{}, 64), release: make(chan struct{}), drop: make(chan struct{})}
	ownerSocket := socket + ".ownerSocket"
	if err := os.Rename(socket, ownerSocket); err != nil {
		t.Fatal(err)
	}
	listener, err := localipc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.dropHeld()
		_ = listener.Close()
		_ = os.Rename(ownerSocket, socket)
	})
	go func() {
		for {
			conn, err := listener.Unix.AcceptUnix()
			if err != nil {
				return
			}
			go p.serve(conn, ownerSocket)
		}
	}()
	return p
}

func (p *processOwnerProxy) serve(conn *net.UnixConn, ownerSocket string) {
	defer conn.Close()
	request, err := io.ReadAll(conn)
	if err != nil {
		return
	}
	if p.hold.Load() {
		p.held <- struct{}{}
		select {
		case <-p.release:
		case <-p.drop:
			return
		}
	}
	time.Sleep(p.delay)
	owner, err := net.Dial("unix", ownerSocket)
	if err != nil {
		return
	}
	defer owner.Close()
	if _, err = owner.Write(request); err != nil {
		return
	}
	_ = owner.(*net.UnixConn).CloseWrite()
	answer, _ := io.ReadAll(owner)
	_, _ = conn.Write(answer)
}

// The call is bounded by localipc.Deadline, not by Wait; a cancel ends a read
// the owner holds, even if its answer follows; and a generation that ends
// while a long poll waits returns its final Snapshot.
func TestPublicProcessObserverSettlesAndBoundsReads(t *testing.T) {
	start := func(t *testing.T) (*processCodexFixture, *projmux.Client, string, core.AgentProcessBinding, uint64, string) {
		f := newProcessCodexFixture(t, nil)
		b := f.endpoint.binding
		f.turn(t, "public-bound", "fixture")
		snap := f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" && s.Sequence > 0 })
		installProcessObservationSchema(t, f.store, b, snap)
		client := sdkProcessObserveClient(t, f.path, f.root)
		agent := "uid:" + b.Agent
		public := core.AgentProcessBinding{Host: b.Host, Pane: b.Pane, Generation: b.Generation}
		first, err := client.ReadAgentProcessEvents(context.Background(), core.AgentProcessEventsRequest{Agent: agent, Binding: public})
		if err != nil || len(first.Events) == 0 {
			t.Fatalf("first read: %+v %v", first, err)
		}
		return f, client, agent, public, first.Next, runtimeservice.ProcessHostSocket(aiModeCodex, f.path, b.Pane, b.Generation)
	}
	t.Run("slow owner is not cut short by Wait", func(t *testing.T) {
		_, client, agent, public, next, socket := start(t)
		proxyProcessOwner(t, socket, 300*time.Millisecond)
		started := time.Now()
		batch, err := client.ReadAgentProcessEvents(context.Background(), core.AgentProcessEventsRequest{Agent: agent, Binding: public, After: next, Wait: 100 * time.Millisecond})
		if elapsed := time.Since(started); err != nil || len(batch.Events) != 0 || batch.Next != next || elapsed < 600*time.Millisecond || elapsed > localipc.Deadline {
			t.Fatalf("slow owner read after %s: %+v %v", elapsed, batch, err)
		}
	})
	t.Run("cancel ends a held read and a late answer", func(t *testing.T) {
		_, client, agent, public, next, socket := start(t)
		proxy := proxyProcessOwner(t, socket, 0)
		proxy.hold.Store(true)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-proxy.held
			cancel()
			close(proxy.release)
		}()
		started := time.Now()
		_, err := client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: agent, Binding: public, After: next, Wait: time.Second})
		if elapsed := time.Since(started); !errors.Is(err, context.Canceled) || elapsed > time.Second {
			t.Fatalf("held read after cancel: %v after %s", err, elapsed)
		}
	})
	t.Run("owner silent for the whole bound is unreachable", func(t *testing.T) {
		_, client, agent, public, _, socket := start(t)
		proxy := proxyProcessOwner(t, socket, 0)
		proxy.hold.Store(true)
		started := time.Now()
		_, err := client.ReadAgentProcess(context.Background(), core.AgentProcessRequest{Agent: agent, Binding: public})
		elapsed := time.Since(started)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeProcessOwnerUnreachable || !errors.Is(err, context.DeadlineExceeded) || elapsed < localipc.Deadline-time.Second || elapsed > localipc.Deadline+time.Second {
			t.Fatalf("silent owner after %s: %v", elapsed, err)
		}
	})
	t.Run("wait recorded during a long poll is terminal", func(t *testing.T) {
		f, client, agent, public, next, socket := start(t)
		b := f.endpoint.binding
		proxy := proxyProcessOwner(t, socket, 0)
		type result struct {
			batch core.AgentProcessEventBatch
			err   error
		}
		done := make(chan result, 1)
		go func() {
			batch, err := client.ReadAgentProcessEvents(context.Background(), core.AgentProcessEventsRequest{Agent: agent, Binding: public, After: next, Wait: localipc.Deadline})
			done <- result{batch, err}
		}()
		time.Sleep(200 * time.Millisecond)
		proxy.hold.Store(true)
		<-proxy.held
		if err := f.endpoint.handle.Stop(b); err != nil {
			t.Fatal(err)
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		exited, err := f.endpoint.handle.Wait(waitCtx, b)
		cancel()
		if err != nil || exited.Exit == nil {
			t.Fatalf("actual Wait: %+v %v", exited, err)
		}
		code := exited.Exit.Code
		if _, err = f.store.Update(func(reg *coremetadata.Registry) error {
			pane, _ := reg.Pane(b.Pane)
			pane.Status.LastTermination = &coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.ClassifyProcessExit(code, exited.Exit.Signal), ObservedAt: time.Now(), PaneUID: b.Pane, AgentUID: b.Agent, Generation: b.Generation, OperationID: b.Operation, ExitCode: &code, Signal: exited.Exit.Signal}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		proxy.dropHeld()
		got := <-done
		if got.err != nil || !got.batch.Terminal || got.batch.Next != next || got.batch.Snapshot.Exit == nil || got.batch.Snapshot.Exit.Code != code || got.batch.Binding != public {
			t.Fatalf("long poll across the Wait: %+v %v", got.batch, got.err)
		}
	})
}
