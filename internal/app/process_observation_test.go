package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

type processObservationTestInput struct {
	RegistryPath string
	Binding      processhost.Binding
}

// A second actual process runs the same read client as public inventory.
func TestProcessObservationClientHelper(t *testing.T) {
	if os.Getenv("PMX_TEST_PROCESS_OBSERVE") != "1" {
		return
	}
	var input processObservationTestInput
	if json.NewDecoder(os.Stdin).Decode(&input) != nil {
		os.Exit(2)
	}
	reg, err := intmetadata.NewStore(input.RegistryPath).LoadDegradedReadOnly()
	if err != nil {
		os.Exit(3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
	defer cancel()
	r := remoteProcessObserver{ctx: ctx, registry: reg, registryPath: input.RegistryPath, binding: input.Binding}
	snap, err := r.Observe(input.Binding)
	if err != nil {
		os.Exit(4)
	}
	_ = json.NewEncoder(os.Stdout).Encode(snap)
	os.Exit(0)
}

func installProcessObservationSchema(t *testing.T, store *intmetadata.Store, b processhost.Binding, snap processhost.Snapshot) coremetadata.Registry {
	t.Helper()
	host, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := localipc.Process(snap.PID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(func(reg *coremetadata.Registry) error {
		p, _ := reg.Pane(b.Pane)
		p.Spec.Runtime.Kind = coremetadata.RuntimeProcess
		p.Status.Activation.Kind = coremetadata.RuntimeProcess
		p.Status.Activation.Claude = nil
		p.Status.Activation.Codex = nil
		p.Status.Activation.Process = &coremetadata.ProcessActivation{Binding: coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}, HostProcess: host, Child: child}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestClaudeProcessObservationExactPeerReadOnly(t *testing.T) {
	testProcessObservationExactPeerReadOnly(t, "claude")
}
func TestCodexProcessObservationExactPeerReadOnly(t *testing.T) {
	testProcessObservationExactPeerReadOnly(t, "codex")
}

func testProcessObservationExactPeerReadOnly(t *testing.T, provider string) {
	t.Helper()
	var binding processhost.Binding
	var path, binary string
	var store *intmetadata.Store
	var snap processhost.Snapshot
	var handle interface {
		Stop(processhost.Binding) error
		Wait(context.Context, processhost.Binding) (processhost.Snapshot, error)
	}
	if provider == "claude" {
		f := newProcessClaudeFixture(t, nil)
		f.turn(t, "read-pending", "question")
		snap = f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) > 0 })
		binding, path, binary, store = f.binding, f.path, f.binary, f.store
		handle = f.handle
	} else {
		f := newProcessCodexFixture(t, nil)
		f.turn(t, "read-pending", "controls")
		snap = f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
		binding, path, binary, store = f.endpoint.binding, f.path, f.binary, f.store
		handle = f.endpoint.handle
	}
	reg := installProcessObservationSchema(t, store, binding, snap)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(processObservationTestInput{RegistryPath: path, Binding: binding})
	cmd := exec.Command(binary, "-test.run=^TestProcessObservationClientHelper$")
	cmd.Env = append(os.Environ(), "PMX_TEST_PROCESS_OBSERVE=1")
	cmd.Stdin = bytes.NewReader(raw)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual peer read: %s %v", output, err)
	}
	var got processhost.Snapshot
	if json.Unmarshal(output, &got) != nil || got.Binding != binding || got.State != "ready" || got.Provider != provider || got.PID != snap.PID || got.Exit != nil {
		t.Fatalf("peer observation: %s", output)
	}
	if len(got.Pending) != 0 || got.Diagnostic != "" || got.Session != "" || got.Connection != "" || got.Turn != "" || got.MessageReservation != "" || got.Resume != nil || strings.Contains(string(output), "Color?") || strings.Contains(string(output), "fixture-tool") {
		t.Fatalf("provider content escaped: %s", output)
	}
	p, _ := reg.Pane(binding.Pane)
	socket := processClaudeHostSocket(path, binding.Pane, binding.Generation)
	stop := processForegroundRequest{Action: "stop", Authority: processhost.Authority{Binding: binding, Session: snap.Session, Connection: snap.Connection}}
	var mixed any = claudeProcessCheck{Observe: &binding, Foreground: &stop}
	if provider == "codex" {
		socket = filepath.Join(claudeActivationLeaseDir(path, binding.Pane, binding.Generation), "codex-host.sock")
		mixed = codexProcessExchange{Observe: &binding, Foreground: &stop}
	}
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := callProcessForeground(context.Background(), socket, identity, p.Status.Activation.Process.HostProcess, mixed); err == nil && result.Accepted {
		t.Fatal("mixed read/control request accepted")
	}
	if current, err := rereadProcessObserver(reg, path, binding); err != nil || current.State != "ready" {
		t.Fatalf("read protocol applied Stop: %+v %v", current, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("observation wrote Registry")
	}
	r := remoteProcessObserver{ctx: context.Background(), registry: reg, registryPath: path, binding: binding}
	stale := binding
	stale.Generation = "old"
	if _, err := r.Observe(stale); err == nil {
		t.Fatal("stale binding accepted")
	}
	foreign := reg.Clone()
	pane, _ := foreign.Pane(binding.Pane)
	pane.Status.Activation.Process.HostProcess.Start = "reused"
	r.registry = foreign
	if _, err := r.Observe(binding); err == nil {
		t.Fatal("host birth mismatch accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.ctx = ctx
	if _, err := r.Observe(binding); err == nil {
		t.Fatal("cancelled read accepted")
	}
	// Endpoint disappearance alone is unknown; a recorded exact actual
	// Wait is still usable after the owned listener has been removed.
	if err := handle.Stop(binding); err != nil {
		t.Fatal(err)
	}
	wait, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	exited, err := handle.Wait(wait, binding)
	cancelWait()
	if err != nil || exited.Exit == nil {
		t.Fatalf("actual Wait: %+v %v", exited, err)
	}
	r.ctx = context.Background()
	r.registry = reg
	if _, err := r.Observe(binding); err == nil {
		t.Fatal("disappeared listener invented exit")
	}
	code := exited.Exit.Code
	_, err = store.Update(func(reg *coremetadata.Registry) error {
		pane, _ := reg.Pane(binding.Pane)
		pane.Status.LastTermination = &coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.ClassifyProcessExit(code, exited.Exit.Signal), ObservedAt: time.Now(), PaneUID: binding.Pane, AgentUID: binding.Agent, Generation: binding.Generation, OperationID: binding.Operation, ExitCode: &code, Signal: exited.Exit.Signal}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.registry, err = store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	got, err = r.Observe(binding)
	if err != nil || got.Exit == nil || *got.Exit != *exited.Exit {
		t.Fatalf("exact Wait receipt: %+v %v", got, err)
	}
	for _, field := range []string{"generation", "operation", "agent", "pane", "source", "classification", "wait"} {
		bad := r.registry.Clone()
		p, _ := bad.Pane(binding.Pane)
		receipt := p.Status.LastTermination
		switch field {
		case "generation":
			receipt.Generation = "old"
		case "operation":
			receipt.OperationID = "other"
		case "agent":
			receipt.AgentUID = "other"
		case "pane":
			receipt.PaneUID = "other"
		case "source":
			receipt.Source = coremetadata.TerminationSourceControlAction
		case "classification":
			receipt.Classification = coremetadata.TerminationUnknown
		case "wait":
			receipt.ExitCode = nil
			receipt.Signal = ""
		}
		probe := r
		probe.registry = bad
		inventory := processhost.ObserveInventory([]processhost.InventoryTarget{{Binding: binding, Observer: probe}})
		if inventory.Observed[0].Status != resourcegraph.StatusUnknown {
			t.Fatalf("%s receipt invented offline: %+v", field, inventory)
		}
	}
}

func rereadProcessObserver(reg coremetadata.Registry, path string, binding processhost.Binding) (processhost.Snapshot, error) {
	r := remoteProcessObserver{ctx: context.Background(), registry: reg, registryPath: path, binding: binding}
	return r.Observe(binding)
}

func TestClaudeProcessObservationHostReadDeadlineKeepsUnknown(t *testing.T) {
	raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err = json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane("pane-02")
	binding := processSchemaBinding(pane.Status.Activation.Process.Binding)
	host, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pane.Status.Activation.Process.HostProcess = host
	path := filepath.Join(t.TempDir(), "registry.json")
	socket := filepath.Join(claudeActivationLeaseDir(path, binding.Pane, binding.Generation), "codex-host.sock")
	listener, closeLease, err := listenProcessHost(socket)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := closeLease(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Unix.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		<-ctx.Done()
	}()
	r := remoteProcessObserver{ctx: ctx, registry: reg, registryPath: path, binding: binding}
	got := processhost.ObserveInventory([]processhost.InventoryTarget{{Binding: binding, Observer: r}})
	if len(got.Declared) != 1 || len(got.Observed) != 1 || got.Observed[0].Status != resourcegraph.StatusUnknown {
		t.Fatalf("deadline invented liveness: %+v", got)
	}
	err = closeLease(context.Background())
	closed = true
	<-done
	if err != nil {
		t.Fatal(err)
	}
}

func TestClaudeProcessObservationPublicReadsKeepMissingHostUnknown(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "host-unavailable", true: "activation-absent"}[missing], func(t *testing.T) {
			raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				var fixture coremetadata.Registry
				if err = json.Unmarshal(raw, &fixture); err != nil {
					t.Fatal(err)
				}
				for i := range fixture.Panes {
					if fixture.Panes[i].Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
						fixture.Panes[i].Status.Activation = coremetadata.PaneActivation{}
					}
				}
				raw, err = json.Marshal(fixture)
				if err != nil {
					t.Fatal(err)
				}
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("TMUX", "")
			t.Setenv("TMUX_PANE", "")
			path := intmetadata.PathFor(filepath.Join(home, "state", "projmux"))
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			reg, err := loadResourceRegistry()
			if err != nil {
				t.Fatal(err)
			}
			var pane coremetadata.Pane
			for _, p := range reg.Panes {
				if p.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
					pane = p
				}
			}
			app := New()
			app.runtimeDiagnostics.reader.observe = func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
				return resourcegraph.Inventory{}
			}
			// The exact default reader is consumed by get, describe and navigation.
			reader := newRuntimeDiagnosticsReader(newFakeTmux())
			graph, err := reader.resolve(context.Background(), resourcegraph.Transport{})
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range graph.Panes {
				if node.Pane.Metadata.UID == pane.Metadata.UID && (node.Process == nil || node.Status != resourcegraph.StatusUnknown || node.Runtime != nil) {
					t.Fatalf("graph: %+v", node)
				}
			}
			var output, errors bytes.Buffer
			if err = app.Run([]string{"get", "pane", "--project", "uid:" + pane.Status.ProcessSession.Binding.ProjectUID, "--window", "uid:" + pane.Status.ProcessSession.Binding.WindowUID, "--pane", "uid:" + pane.Metadata.UID}, &output, &errors); err != nil {
				t.Fatalf("get: %v %s", err, &errors)
			}
			if !strings.Contains(output.String(), "unknown") {
				t.Fatalf("missing host became offline: %s", &output)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(raw, after) {
				t.Fatal("public read wrote Registry")
			}
		})
	}
}

func TestProcessObservationUnresponsiveHostsShareShortReadBudget(t *testing.T) {
	reg, _, path := processAttentionWiringFixture(t)
	pane, _ := reg.Pane("pane-02")
	host, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pane.Status.Activation.Process.HostProcess = host
	done := make(chan struct{})
	defer close(done)
	for i := range 3 {
		current := *pane
		activation := *pane.Status.Activation.Process
		current.Status.Activation.Process = &activation
		current.Metadata.UID = fmt.Sprintf("slow-pane-%d", i)
		activation.Binding.PaneUID = current.Metadata.UID
		reg.Panes = append(reg.Panes, current)
	}
	reg.Panes = reg.Panes[1:]
	for _, current := range reg.Panes {
		binding := processSchemaBinding(current.Status.Activation.Process.Binding)
		socket := filepath.Join(claudeActivationLeaseDir(path, binding.Pane, binding.Generation), "codex-host.sock")
		listener, closeLease, err := listenProcessHost(socket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := closeLease(context.Background()); err != nil {
				t.Error(err)
			}
		})
		go func() {
			conn, err := listener.Unix.AcceptUnix()
			if err != nil {
				return
			}
			defer conn.Close()
			<-done
		}()
	}
	start := time.Now()
	inventory := observeRegistryProcesses(context.Background(), reg)
	elapsed := time.Since(start)
	if len(inventory.Declared) != 3 || len(inventory.Observed) != 3 {
		t.Fatalf("missing declarations: %+v", inventory)
	}
	for _, observed := range inventory.Observed {
		if observed.Status != resourcegraph.StatusUnknown {
			t.Fatalf("timeout invented state: %+v", observed)
		}
	}
	// Scheduling allowance is separate from the production 250ms I/O budget.
	if elapsed > time.Second {
		t.Fatalf("read exceeded short shared budget: %s", elapsed)
	}
	t.Logf("three unresponsive hosts: %s; I/O budget %s", elapsed, 250*time.Millisecond)
}
