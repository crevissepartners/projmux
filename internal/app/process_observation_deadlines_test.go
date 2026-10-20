package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Real Unix peers return exact ownership evidence for a supervised child. All
// listeners are private fixtures; no provider or tmux server is contacted.
func deadlineObservationFixture(t *testing.T, count int, slow int, delay time.Duration) (coremetadata.Registry, *atomic.Int32) {
	t.Helper()
	reg, _, path := processAttentionWiringFixture(t)
	raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var base coremetadata.Registry
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	// Keep a real grandchild, as required by processObservationMatches.
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $!; wait")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() {
		t.Fatal("supervisor did not report its child")
	}
	pid, err := strconv.Atoi(scanner.Text())
	if err != nil {
		t.Fatal(err)
	}
	childProcess, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = childProcess.Kill(); _ = cmd.Wait() })
	host, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := localipc.Process(pid)
	if err != nil {
		t.Fatal(err)
	}
	reg.Panes = reg.Panes[:0]
	reg.Agents = reg.Agents[:0]
	// Retain the shell anchor so the fixture also supports Validate/Wait.
	reg.Panes = append(reg.Panes, base.Panes[0])
	calls := &atomic.Int32{}
	done := make(chan struct{})
	var servers sync.WaitGroup
	t.Cleanup(func() { close(done); servers.Wait() })
	for i := range count {
		cloned := base.Clone()
		pane, agent := cloned.Panes[1], cloned.Agents[0]
		pane.Metadata.UID = fmt.Sprintf("deadline-pane-%03d", i)
		pane.Metadata.Name = pane.Metadata.UID
		agent.Metadata.UID = fmt.Sprintf("deadline-agent-%03d", i)
		agent.Metadata.Name = agent.Metadata.UID
		agent.Status.PaneRef = pane.Metadata.UID
		pane.Metadata.OwnerRef.UID = agent.Metadata.UID
		pane.Status.Activation.AgentUID = agent.Metadata.UID
		activation := pane.Status.Activation.Process
		activation.Binding.AgentUID = agent.Metadata.UID
		activation.Binding.PaneUID = pane.Metadata.UID
		activation.HostProcess, activation.Child = host, child
		pane.Status.ProcessSession = nil
		reg.Panes = append(reg.Panes, pane)
		reg.Agents = append(reg.Agents, agent)
		binding := processSchemaBinding(activation.Binding)
		socket := processHostSocket(agent.Spec.Provider, path, binding.Pane, binding.Generation)
		listener, closeLease, err := listenProcessHost(socket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := closeLease(context.Background()); err != nil {
				t.Error(err)
			}
		})
		servers.Go(func() {
			for {
				conn, err := listener.Unix.AcceptUnix()
				if err != nil {
					return
				}
				servers.Go(func() {
					defer conn.Close()
					var request json.RawMessage
					if localipc.ReadJSON(conn, &request) != nil {
						return
					}
					calls.Add(1)
					wait := delay
					if i == slow {
						wait = 10 * time.Second
					}
					select {
					case <-time.After(wait):
					case <-done:
						return
					}
					view := processHostObservation{Binding: binding, Host: host, Child: child, Provider: agent.Spec.Provider, State: "ready"}
					_ = localipc.WriteJSON(conn, processForegroundResult{Accepted: true, Observation: &view})
				})
			}
		})
	}
	return reg, calls
}

func TestProcessObservationOwnerDeadlinesPreserveHealthyInventoryAndAdmission(t *testing.T) {
	for _, slow := range []int{0, 24} {
		t.Run(fmt.Sprintf("slow-%d", slow), func(t *testing.T) {
			reg, _ := deadlineObservationFixture(t, 25, slow, 110*time.Millisecond)
			// Input ordering must not decide who receives a usable read budget.
			slices.Reverse(reg.Panes)
			start := time.Now()
			inventory := observeRegistryProcesses(context.Background(), reg)
			if len(inventory.Observed) != 25 {
				t.Fatalf("lost declarations: %+v", inventory)
			}
			for _, observed := range inventory.Observed {
				want := resourcegraph.StatusLive
				if observed.Key.Pane == fmt.Sprintf("deadline-pane-%03d", slow) {
					want = resourcegraph.StatusUnknown
				}
				if observed.Status != want {
					t.Errorf("%s = %s, want %s", observed.Key.Pane, observed.Status, want)
				}
			}
			t.Logf("25 owners (one stalled): %s", time.Since(start))
			runtime := newProcessPaneRuntime()
			reads := 0
			runtime.observe = func(ctx context.Context, registry coremetadata.Registry) resourcegraph.ProcessInventory {
				reads++
				return observeRegistryProcesses(ctx, registry)
			}
			target := 24 - slow
			for _, action := range []resourcegraph.ProcessAction{resourcegraph.ProcessTurn, resourcegraph.ProcessInterrupt, resourcegraph.ProcessStop} {
				_, handled, err := runtime.admit(reg, fmt.Sprintf("deadline-pane-%03d", target), action)
				if !handled || err != nil {
					t.Errorf("healthy %s refused: handled=%v err=%v", action, handled, err)
				}
			}
			if reads != 3 {
				t.Errorf("admission did not reobserve the Registry: reads=%d", reads)
			}
		})
	}
}

func TestProcessObservationRetiredEvidenceSkipsOwnerQueue(t *testing.T) {
	for _, evidence := range []string{"exact", "missing", "forged"} {
		t.Run(evidence, func(t *testing.T) {
			reg, _, path := processAttentionWiringFixtureMode(t, true)
			pane, _ := reg.Pane("pane-02")
			activation := *pane.Status.Activation.Process
			pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: "codex", Binding: activation.Binding, ResumeState: coremetadata.ProcessResumeUnknown}
			code := 0
			receipt := coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationNormal, ObservedAt: time.Now().UTC(), PaneUID: activation.Binding.PaneUID, AgentUID: activation.Binding.AgentUID, Generation: activation.Binding.Generation, OperationID: activation.Binding.OperationID, ExitCode: &code}
			if err := (coremetadata.Mutator{}).RecordProcessWait(&reg, activation, receipt); err != nil {
				t.Fatal(err)
			}
			pane, _ = reg.Pane(activation.Binding.PaneUID)
			want := resourcegraph.StatusOffline
			switch evidence {
			case "missing":
				pane.Status.LastTermination = nil
				want = resourcegraph.StatusUnknown
			case "forged":
				pane.Status.LastTermination.Generation = "forged"
				want = resourcegraph.StatusUnknown
			}
			binding := processSchemaBinding(activation.Binding)
			listener, closeLease, err := listenProcessHost(processHostSocket("codex", path, binding.Pane, binding.Generation))
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				for {
					conn, err := listener.Unix.AcceptUnix()
					if err != nil {
						return
					}
					calls.Add(1)
					_ = conn.Close()
				}
			}()
			defer func() {
				if err := closeLease(context.Background()); err != nil {
					t.Error(err)
				}
				<-stopped
			}()
			// Durable local evidence needs no live owner budget, even if the caller's
			// network read budget has already expired.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			inventory := observeRegistryProcesses(ctx, reg)
			if len(inventory.Observed) != 1 || inventory.Observed[0].Status != want {
				t.Fatalf("retired %s: %+v, want %s", evidence, inventory, want)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("retired owner IPC=%d, want 0", got)
			}
		})
	}
}

func TestProcessObservationTotalDeadlineLeavesUnknownAndSignal(t *testing.T) {
	reg, calls := deadlineObservationFixture(t, 80, -1, 10*time.Second)
	signal, err := os.CreateTemp(t.TempDir(), "observation-signal")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = signal
	defer func() { os.Stderr = previous; _ = signal.Close() }()
	start := time.Now()
	inventory := observeRegistryProcesses(context.Background(), reg)
	elapsed := time.Since(start)
	os.Stderr = previous
	if len(inventory.Declared) != 80 || len(inventory.Observed) != 80 {
		t.Fatalf("total deadline erased owners: %+v", inventory)
	}
	for _, observed := range inventory.Observed {
		if observed.Status != resourcegraph.StatusUnknown {
			t.Errorf("timeout invented status: %+v", observed)
		}
	}
	if elapsed > 3*time.Second {
		t.Errorf("total observation exceeded separate cap: %s", elapsed)
	}
	if got := calls.Load(); got >= 80 {
		t.Errorf("total cap failed to stop queued IPC: calls=%d", got)
	}
	raw, err := os.ReadFile(signal.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "process observation deadline:") || !strings.Contains(string(raw), "total_limited=") || strings.Contains(string(raw), "total_limited=0") {
		t.Errorf("missing total deadline signal: %q", raw)
	}
	t.Logf("80 stalled owners: elapsed=%s IPC=%d signal=%s", elapsed, calls.Load(), raw)
}
