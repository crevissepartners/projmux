package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/processattention"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type processAttentionFixture struct {
	client   *Client
	state    string
	bindings []processhost.Binding
}

// processAttentionRegistryFixture records n current Codex process Agents.
func processAttentionRegistryFixture(t *testing.T, n int) processAttentionFixture {
	t.Helper()
	dir := t.TempDir()
	f := processAttentionFixture{state: dir}
	_, err := store.NewStore(store.PathFor(dir)).Update(func(reg *m.Registry) error {
		mut := store.DefaultMutator()
		created, e := mut.RegisterProject(reg, m.RegisterProjectOptions{Root: t.TempDir(), Name: "attention-project", DefaultShell: "/bin/sh", OperationID: "register"})
		if e != nil {
			return e
		}
		for i := range n {
			name := fmt.Sprintf("attention-%d", i)
			_, a, p, e := mut.CreateProcessWindow(reg, created.Project.Metadata.UID, m.BootstrapWindow{Name: name}, m.CreateAgentOptions{Name: name, Provider: "codex", OperationID: "create-" + name}, m.ProcessBinding{HostInstanceID: "host", Generation: "generation-" + name, OperationID: "op-" + name})
			if e != nil {
				return e
			}
			pane, _ := reg.Pane(p.Metadata.UID)
			b := pane.Status.ProcessSession.Binding
			agent, _ := reg.Agent(a.Metadata.UID)
			agent.Status.Phase = m.PhaseRunning
			identity := func(pid int, start string) m.ProcessIdentity {
				return m.ProcessIdentity{PID: pid, OwnerUID: uint32(os.Getuid()), Start: start}
			}
			if e = mut.RecordProcessActivation(reg, m.ProcessActivation{Binding: b, HostProcess: identity(2000000000+i, "owner"), Child: identity(2100000000+i, "child")}, "conversation"); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.client, err = NewClient(Options{ConfigDir: filepath.Join(dir, "config"), StateDir: dir, HomeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := store.NewStore(store.PathFor(dir)).Load()
	if err != nil {
		t.Fatal(err)
	}
	f.bindings = processattention.Declarations(reg).Bindings
	if len(f.bindings) != n {
		t.Fatalf("declared %d Agents, want %d", len(f.bindings), n)
	}
	return f
}

func writeProcessAttentionStore(t *testing.T, state string, records map[string]processattention.Record) {
	t.Helper()
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(processattention.StorePath(state), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func inputRequired(b processhost.Binding) processattention.Record {
	return processattention.Record{Binding: b, Provider: "codex", Pending: map[string]processattention.Pending{"request": {Kind: aibadge.InputRequired, Sequence: 1}}}
}

// N Agents cost one store read and no owner access: a listening owner socket
// is never accepted from, and the same result comes back for any N.
func TestPublicProcessAttentionReadsStoreOnceWithoutOwnerAccess(t *testing.T) {
	const agents = 20
	f := processAttentionRegistryFixture(t, agents)
	records := map[string]processattention.Record{}
	for _, b := range f.bindings {
		records[b.Pane] = inputRequired(b)
	}
	writeProcessAttentionStore(t, f.state, records)
	first := f.bindings[0]
	socket := runtimeservice.ProcessHostSocket("codex", store.PathFor(f.state), first.Pane, first.Generation)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conn.Close()
		}
	}()
	var reads atomic.Int32
	original := readProcessAttentionStore
	readProcessAttentionStore = func(dir string) (map[string]processattention.Record, error) {
		reads.Add(1)
		return original(dir)
	}
	defer func() { readProcessAttentionStore = original }()

	got, err := f.client.ReadProcessAttention(context.Background(), core.ProcessAttentionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 || accepted.Load() != 0 {
		t.Fatalf("store reads %d, owner accepts %d, want 1 and 0", reads.Load(), accepted.Load())
	}
	if len(got.Agents) != agents {
		t.Fatalf("%d Agents with a badge, want %d", len(got.Agents), agents)
	}
	for _, b := range f.bindings {
		if got.Agents[b.Agent] != core.ProcessAttentionInputRequired {
			t.Fatalf("Agent %s badge %q", b.Agent, got.Agents[b.Agent])
		}
	}
}

// A missing or empty store is an empty map and never reads the Registry; a
// damaged one is a refusal that reaches the sentinel and the store's own error.
func TestPublicProcessAttentionMissingEmptyDamaged(t *testing.T) {
	ctx := context.Background()
	f := processAttentionRegistryFixture(t, 1)
	// An unreadable Registry proves the Registry is not read.
	if err := os.WriteFile(store.PathFor(f.state), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"", "{}"} {
		if content != "" {
			if err := os.WriteFile(processattention.StorePath(f.state), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := f.client.ReadProcessAttention(ctx, core.ProcessAttentionRequest{})
		if err != nil || got.Agents == nil || len(got.Agents) != 0 {
			t.Fatalf("store %q: %+v %v", content, got, err)
		}
	}
	for _, damaged := range []string{"{", "null"} {
		if err := os.WriteFile(processattention.StorePath(f.state), []byte(damaged), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := f.client.ReadProcessAttention(ctx, core.ProcessAttentionRequest{})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeProcessAttentionDamaged || refusal.Code != "process-attention-damaged" {
			t.Fatalf("damaged %q: %#v %v", damaged, refusal, err)
		}
		if !errors.Is(err, ErrProcessAttentionDamaged) || !errors.Is(err, processattention.ErrDamaged) {
			t.Fatalf("damaged %q does not reach the sentinels: %v", damaged, err)
		}
		if raw, _ := os.ReadFile(processattention.StorePath(f.state)); string(raw) != damaged {
			t.Fatalf("read changed the damaged store: %q", raw)
		}
	}
}

// The public values are the ones the badge aggregation produces.
func TestPublicProcessAttentionBadgeValuesMatchAibadge(t *testing.T) {
	for public, want := range map[core.ProcessAttentionBadge]string{
		core.ProcessAttentionInProgress:       aibadge.InProgress,
		core.ProcessAttentionApprovalRequired: aibadge.ApprovalRequired,
		core.ProcessAttentionInputRequired:    aibadge.InputRequired,
		core.ProcessAttentionResponseComplete: aibadge.ResponseComplete,
	} {
		if string(public) != want {
			t.Fatalf("%q != aibadge %q", public, want)
		}
		if got, ok := publicProcessAttentionBadge(want); !ok || got != public {
			t.Fatalf("badge %q maps to %q, %v", want, got, ok)
		}
	}
	if _, ok := publicProcessAttentionBadge(""); ok {
		t.Fatal("an empty badge is shown")
	}
}

// The returned map is the caller's: changing it changes nothing later.
func TestPublicProcessAttentionCopyIsCallerOwned(t *testing.T) {
	f := processAttentionRegistryFixture(t, 1)
	writeProcessAttentionStore(t, f.state, map[string]processattention.Record{f.bindings[0].Pane: inputRequired(f.bindings[0])})
	first, err := f.client.ReadProcessAttention(context.Background(), core.ProcessAttentionRequest{})
	if err != nil || len(first.Agents) != 1 {
		t.Fatalf("first read: %+v %v", first, err)
	}
	first.Agents[f.bindings[0].Agent] = core.ProcessAttentionResponseComplete
	delete(first.Agents, f.bindings[0].Agent)
	second, err := f.client.ReadProcessAttention(context.Background(), core.ProcessAttentionRequest{})
	if err != nil || second.Agents[f.bindings[0].Agent] != core.ProcessAttentionInputRequired {
		t.Fatalf("second read: %+v %v", second, err)
	}
}
