package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type agentProcessFixture struct {
	client *Client
	state  string
	agent  string
	pane   string
	public core.AgentProcessBinding
}

// agentProcessFixtureState is the generation an agentProcessRegistryFixture
// records. No owner listens in any of them.
type agentProcessFixtureState int

const (
	// fixtureCurrent: a current activation whose owner is not running.
	fixtureCurrent agentProcessFixtureState = iota
	// fixtureCurrentExited: a current activation with its supervisor Wait.
	fixtureCurrentExited
	// fixtureRetiredExited: a retired generation with its supervisor Wait.
	fixtureRetiredExited
	// fixtureRetiredBare: a retired generation without any final evidence.
	fixtureRetiredBare
	// fixtureHostLost: the owner vanished and the generation was retired as
	// host-lost, which proves absence, not an exit.
	fixtureHostLost
)

// agentProcessRegistryFixture records one Codex process Agent in state.
func agentProcessRegistryFixture(t *testing.T, state agentProcessFixtureState) agentProcessFixture {
	t.Helper()
	dir := t.TempDir()
	storage := store.NewStore(store.PathFor(dir))
	var f agentProcessFixture
	_, err := storage.Update(func(reg *m.Registry) error {
		mut := store.DefaultMutator()
		created, e := mut.RegisterProject(reg, m.RegisterProjectOptions{Root: t.TempDir(), Name: "observe-project", DefaultShell: "/bin/sh", OperationID: "register"})
		if e != nil {
			return e
		}
		_, a, p, e := mut.CreateProcessWindow(reg, created.Project.Metadata.UID, m.BootstrapWindow{Name: "observe-window"}, m.CreateAgentOptions{Name: "observe-agent", Provider: "codex", OperationID: "create"}, m.ProcessBinding{HostInstanceID: "host", Generation: "generation", OperationID: "op"})
		if e != nil {
			return e
		}
		f.agent, f.pane = a.Metadata.UID, p.Metadata.UID
		pane, _ := reg.Pane(f.pane)
		b := pane.Status.ProcessSession.Binding
		f.public = core.AgentProcessBinding{Host: b.HostInstanceID, Pane: b.PaneUID, Generation: b.Generation}
		dead := func(pid int, start string) m.ProcessIdentity {
			return m.ProcessIdentity{PID: pid, OwnerUID: uint32(os.Getuid()), Start: start}
		}
		agent, _ := reg.Agent(f.agent)
		agent.Status.Phase = m.PhaseRunning
		activation := m.ProcessActivation{Binding: b, HostProcess: dead(2000000000, "owner"), Child: dead(2000000001, "child")}
		if e = mut.RecordProcessActivation(reg, activation, "conversation"); e != nil {
			return e
		}
		switch state {
		case fixtureHostLost:
			return mut.MarkProcessHostLostOffline(reg, activation)
		case fixtureCurrentExited, fixtureRetiredExited:
			code := 3
			wait := m.TerminationEvidence{Source: m.TerminationSourceSupervisor, Classification: m.ClassifyProcessExit(code, ""), ObservedAt: time.Now(), AgentUID: f.agent, PaneUID: f.pane, Generation: b.Generation, OperationID: b.OperationID, ExitCode: &code}
			if outcome, e := mut.RecordTermination(reg, wait); e != nil || !outcome.Applied {
				return fmt.Errorf("record wait: %+v %v", outcome, e)
			}
		}
		if state == fixtureRetiredExited || state == fixtureRetiredBare {
			pane, _ = reg.Pane(f.pane)
			agent, _ = reg.Agent(f.agent)
			pane.Status.Activation = m.PaneActivation{}
			agent.Status.Phase = m.PhaseOffline
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.state = dir
	f.client, err = NewClient(Options{ConfigDir: filepath.Join(dir, "config"), StateDir: dir, HomeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func requireAgentProcessRefusal(t *testing.T, err error, code Code, sentinel error) *Refusal {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != code || !errors.Is(err, sentinel) {
		t.Fatalf("refusal = %#v (%v), want %s", refusal, err, code)
	}
	return refusal
}

// Every public refusal carries its code, its sentinel and the original cause,
// and none of them has an effect on the Registry.
func TestPublicProcessFailureCodesAndCauses(t *testing.T) {
	ctx := context.Background()
	live := agentProcessRegistryFixture(t, fixtureCurrent)
	before, err := os.ReadFile(store.PathFor(live.state))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", live.agent, "name:observe-agent", "uid:"} {
		_, err := live.client.ReadAgentProcess(ctx, core.AgentProcessRequest{Agent: ref})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeSelectorInvalid {
			t.Fatalf("ref %q: %v", ref, err)
		}
	}
	agentRef := "uid:" + live.agent
	// An owner that does not answer is unreachable, not a provider exit.
	_, err = live.client.ReadAgentProcess(ctx, core.AgentProcessRequest{Agent: agentRef})
	if refusal := requireAgentProcessRefusal(t, err, CodeProcessOwnerUnreachable, ErrProcessOwnerUnreachable); refusal.AgentUID != live.agent || refusal.Unwrap() == nil {
		t.Fatalf("unreachable cause: %#v", refusal)
	}
	if errors.Is(err, ErrProcessClosed) || errors.Is(err, ErrProcessStale) {
		t.Fatalf("unreachable classified as exit or stale: %v", err)
	}
	_, err = live.client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: agentRef, Binding: live.public, Wait: time.Second})
	requireAgentProcessRefusal(t, err, CodeProcessOwnerUnreachable, ErrProcessOwnerUnreachable)
	// A Binding that is not the current generation is stale; the read never
	// follows the current one.
	for _, stale := range []core.AgentProcessBinding{
		{Host: live.public.Host, Pane: live.public.Pane, Generation: "old"},
		{Host: "other", Pane: live.public.Pane, Generation: live.public.Generation},
		{Host: live.public.Host, Pane: "other", Generation: live.public.Generation},
	} {
		_, err = live.client.ReadAgentProcess(ctx, core.AgentProcessRequest{Agent: agentRef, Binding: stale})
		requireAgentProcessRefusal(t, err, CodeProcessStale, ErrProcessStale)
		if !errors.Is(err, processhost.ErrStale) {
			t.Fatalf("stale cause lost: %v", err)
		}
		_, err = live.client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: agentRef, Binding: stale})
		requireAgentProcessRefusal(t, err, CodeProcessStale, ErrProcessStale)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = live.client.ReadAgentProcessEvents(cancelled, core.AgentProcessEventsRequest{Agent: agentRef}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	after, _ := os.ReadFile(store.PathFor(live.state))
	if !bytes.Equal(before, after) {
		t.Fatal("refused reads wrote the Registry")
	}

	// A generation with its supervisor Wait receipt ends with its final
	// Snapshot, whether the activation is still current or retired.
	for _, state := range []agentProcessFixtureState{fixtureCurrentExited, fixtureRetiredExited} {
		current := state == fixtureCurrentExited
		ended := agentProcessRegistryFixture(t, state)
		ref := "uid:" + ended.agent
		snapshot, err := ended.client.ReadAgentProcess(ctx, core.AgentProcessRequest{Agent: ref})
		if err != nil || snapshot.State != "exited" || snapshot.Exit == nil || snapshot.Exit.Code != 3 || snapshot.Binding != ended.public || snapshot.Provider != "codex" {
			t.Fatalf("current=%t final snapshot: %+v %v", current, snapshot, err)
		}
		batch, err := ended.client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: ref, Binding: ended.public, After: 7})
		if err != nil || !batch.Terminal || batch.Next != 7 || len(batch.Events) != 0 || batch.Snapshot.Exit == nil || batch.Binding != ended.public {
			t.Fatalf("current=%t final batch: %+v %v", current, batch, err)
		}
	}
	// A retired generation without final evidence has nothing left to read.
	closed := agentProcessRegistryFixture(t, fixtureRetiredBare)
	_, err = closed.client.ReadAgentProcess(ctx, core.AgentProcessRequest{Agent: "uid:" + closed.agent})
	requireAgentProcessRefusal(t, err, CodeProcessClosed, ErrProcessClosed)
	_, err = closed.client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: "uid:" + closed.agent, Binding: closed.public})
	requireAgentProcessRefusal(t, err, CodeProcessClosed, ErrProcessClosed)
}

// A Binding whose owner vanished and was retired as host-lost reads as closed:
// absence is never a provider exit, and the read records nothing.
func TestPublicProcessHostLostReadsClosedWithoutExit(t *testing.T) {
	ctx := context.Background()
	lost := agentProcessRegistryFixture(t, fixtureHostLost)
	path := store.PathFor(lost.state)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ref := "uid:" + lost.agent
	for _, binding := range []core.AgentProcessBinding{{}, lost.public} {
		snapshot, err := lost.client.ReadAgentProcess(ctx, core.AgentProcessRequest{Agent: ref, Binding: binding})
		requireAgentProcessRefusal(t, err, CodeProcessClosed, ErrProcessClosed)
		if snapshot.Exit != nil || errors.Is(err, ErrProcessOwnerUnreachable) {
			t.Fatalf("host-lost read reported an exit: %+v %v", snapshot, err)
		}
		batch, err := lost.client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{Agent: ref, Binding: binding, Wait: time.Second})
		requireAgentProcessRefusal(t, err, CodeProcessClosed, ErrProcessClosed)
		if batch.Terminal || batch.Snapshot.Exit != nil {
			t.Fatalf("host-lost batch reported an exit: %+v", batch)
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("host-lost reads wrote the Registry")
	}
	reg, err := store.NewStore(path).LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(lost.pane)
	if !reg.ProcessHostLostOffline(lost.pane) || pane.Status.LastTermination == nil || pane.Status.LastTermination.ExitCode != nil || pane.Status.LastTermination.Source != m.TerminationSourceReconcile {
		t.Fatalf("host-lost evidence changed: %+v", pane.Status.LastTermination)
	}
}

// After an owner read fails, the Registry is read again for the fixed
// Binding: a recorded Wait is final, a host-lost or bare retirement is
// closed, a newer generation is stale, and a generation still current
// without final evidence is left to the unreachable refusal.
func TestPublicProcessSettleClassifiesEndedGeneration(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		state    agentProcessFixtureState
		replaced bool
		code     Code
		sentinel error
		done     bool
	}{
		{name: "current", state: fixtureCurrent},
		{name: "current-exited", state: fixtureCurrentExited, done: true},
		{name: "retired-exited", state: fixtureRetiredExited, done: true},
		{name: "host-lost", state: fixtureHostLost, done: true, code: CodeProcessClosed, sentinel: ErrProcessClosed},
		{name: "retired-bare", state: fixtureRetiredBare, done: true, code: CodeProcessClosed, sentinel: ErrProcessClosed},
		{name: "replaced", state: fixtureCurrent, replaced: true, done: true, code: CodeProcessStale, sentinel: ErrProcessStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := agentProcessRegistryFixture(t, tc.state)
			fixed := f.public
			if tc.replaced {
				fixed.Generation = "replaced"
			}
			before, _ := os.ReadFile(store.PathFor(f.state))
			snapshot, done, err := f.client.settleAgentProcess(ctx, "uid:"+f.agent, "AgentProcessEventsRequest", agentProcessRead{public: fixed})
			if done != tc.done {
				t.Fatalf("done = %t, want %t (%+v %v)", done, tc.done, snapshot, err)
			}
			if tc.sentinel != nil {
				requireAgentProcessRefusal(t, err, tc.code, tc.sentinel)
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.done && tc.sentinel == nil && (snapshot.Exit == nil || snapshot.Exit.Code != 3 || snapshot.Binding != f.public) {
				t.Fatalf("final snapshot: %+v", snapshot)
			}
			if after, _ := os.ReadFile(store.PathFor(f.state)); !bytes.Equal(before, after) {
				t.Fatal("settling wrote the Registry")
			}
		})
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	f := agentProcessRegistryFixture(t, fixtureCurrentExited)
	if _, done, err := f.client.settleAgentProcess(cancelled, "uid:"+f.agent, "AgentProcessEventsRequest", agentProcessRead{public: f.public}); done || err != nil {
		t.Fatalf("cancelled settle: %t %v", done, err)
	}
}

// An owner batch reaches the public batch field by field: Next is the next
// After, and Gap, More and Omitted cross unchanged. No session, connection,
// turn or request content crosses.
func TestPublicProcessEventBatchCarriesOwnerBatch(t *testing.T) {
	read := agentProcessRead{public: core.AgentProcessBinding{Host: "h", Pane: "p", Generation: "g"}, provider: "codex"}
	b := processhost.Binding{Host: "h", Pane: "p", Generation: "g"}
	raw := json.RawMessage(`{"method":"item/agentMessage/delta"}`)
	owner := runtimeservice.ProcessHostEvents{
		Events: []processhost.Event{
			{Binding: b, Sequence: 4, Kind: "stream-gap", Raw: json.RawMessage("null")},
			{Binding: b, Session: "secret-session", Connection: "secret-connection", Turn: "secret-turn", Sequence: 5, Kind: "provider", Raw: raw, Request: &processhost.Request{ID: "secret-request", Input: raw}},
			{Binding: b, Sequence: 6, Kind: "exit", Exit: &processhost.Exit{Code: 1}},
		},
		Observation: runtimeservice.ProcessHostObservation{Provider: "codex", State: "exited", Turn: true, Pending: 2, OwnerMode: "detached", Exit: &processhost.Exit{Code: 1}},
		Sequence:    6, Gap: true, More: true, Last: 6, Omitted: []uint64{6},
	}
	batch := read.batch(owner)
	if batch.Next != 6 || !batch.Gap || !batch.More || !batch.Terminal || len(batch.Omitted) != 1 || batch.Omitted[0] != 6 || len(batch.Events) != 3 {
		t.Fatalf("batch: %+v", batch)
	}
	if batch.Events[0].Kind != "stream-gap" || batch.Events[0].Payload != nil || batch.Events[2].Payload != nil || !bytes.Equal(batch.Events[1].Payload, raw) || batch.Events[2].Exit == nil || batch.Events[2].Exit.Code != 1 {
		t.Fatalf("events: %+v", batch.Events)
	}
	s := batch.Snapshot
	if s.Binding != read.public || s.Sequence != 6 || !s.Running || s.Pending != 2 || s.OwnerMode != "detached" || s.Exit == nil {
		t.Fatalf("snapshot: %+v", s)
	}
	encoded, _ := json.Marshal(batch)
	if bytes.Contains(encoded, []byte(`"payload":null`)) {
		t.Fatalf("an event without a provider frame carried a payload: %s", encoded)
	}
	for _, secret := range []string{"secret-session", "secret-connection", "secret-turn", "secret-request"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("public batch carried %q: %s", secret, encoded)
		}
	}
	owner.Omitted[0] = 9
	owner.Events[1].Raw[2] = 'X'
	if batch.Omitted[0] != 6 || !bytes.Equal(batch.Events[1].Payload, json.RawMessage(`{"method":"item/agentMessage/delta"}`)) {
		t.Fatal("public batch shares owner memory")
	}
}
