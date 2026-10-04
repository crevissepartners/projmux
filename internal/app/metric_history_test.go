package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/systemstatus"
)

func TestSystemHistoryOmitsCPUWarmupAndMeasuresStateFilesystem(t *testing.T) {
	state := filepath.Join(t.TempDir(), "not-created", "projmux")
	now := time.Now().UTC()
	memory := 38
	points, err := systemMetricPoints(systemstatus.Metrics{MemoryPercent: &memory}, state, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Name != "system.memory.percent" || points[1].Name != "system.fs.available_bytes" || points[1].Value <= 0 {
		t.Fatalf("system points: %+v", points)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("sample created state directory: %v", err)
	}
}

func TestLiveAgentHistoryCountsStatusLiveAcrossProjectsAndOmitsUnknown(t *testing.T) {
	node := func(provider string, status resourcegraph.Status) resourcegraph.AgentNode {
		return resourcegraph.AgentNode{Agent: coremetadata.Agent{Spec: coremetadata.AgentSpec{Provider: provider}}, Status: status}
	}
	graph := resourcegraph.Graph{Agents: []resourcegraph.AgentNode{
		node("claude", resourcegraph.StatusLive), node("claude", resourcegraph.StatusLive),
		node("claude", resourcegraph.StatusOffline), node("codex", resourcegraph.StatusUnknown),
	}}
	points := liveAgentMetricPoints(graph, time.Now().UTC())
	if len(points) != 1 || points[0].Provider != "claude" || points[0].Value != 2 {
		t.Fatalf("live count: %+v", points)
	}
	graph.Unavailable = []resourcegraph.Unavailability{{}}
	if got := liveAgentMetricPoints(graph, time.Now().UTC()); len(got) != 0 {
		t.Fatalf("unavailable count: %+v", got)
	}
}

func TestLiveAgentHistoryUsesResolvedProcessObservationNotRegistryPhase(t *testing.T) {
	registry, runtime, _ := processInventoryFixture(t)
	processes := runtime.inventory()
	key := processes.Declared[0]
	processes.Observed = []resourcegraph.ProcessObservation{{Key: key, Status: resourcegraph.StatusLive}}
	graph := resourcegraph.Resolve(registry, resourcegraph.Inventory{Processes: processes})
	points := liveAgentMetricPoints(graph, time.Now().UTC())
	if len(points) != 1 || points[0].Provider != "claude" || points[0].Value != 1 {
		t.Fatalf("live process: %+v", points)
	}
	processes.Observed = nil
	graph = resourcegraph.Resolve(registry, resourcegraph.Inventory{Processes: processes})
	if got := liveAgentMetricPoints(graph, time.Now().UTC()); len(got) != 0 {
		t.Fatalf("phase-only process counted: %+v", got)
	}
}

func TestLiveAgentHistoryUsesResolvedTmuxObservationAndExcludesPhantom(t *testing.T) {
	registry := resourceFixtureRegistry(t)
	inv := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "projmux", Source: resourcegraph.TransportSourceSocketName},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions:  []resourcegraph.Session{{ID: "$1", Name: "alpha", ProjectUID: "prj-alpha", Root: "/srv/alpha"}},
		Windows:   []resourcegraph.Window{{ID: "@1", SessionID: "$1", UID: "win-alpha-main", DisplayName: "main"}},
		Panes:     []resourcegraph.Pane{{ID: "%1", WindowID: "@1", UID: "pan-alpha-codex", AgentProvider: "codex"}},
	}
	graph := resourcegraph.Resolve(registry, inv)
	points := liveAgentMetricPoints(graph, time.Now().UTC())
	if len(points) != 1 || points[0].Provider != "codex" || points[0].Value != 1 {
		t.Fatalf("resolved tmux count: %+v; agents=%+v", points, graph.Agents)
	}
	inv.Panes = nil // Registry still says PhaseRunning.
	graph = resourcegraph.Resolve(registry, inv)
	points = liveAgentMetricPoints(graph, time.Now().UTC())
	if len(points) != 1 || points[0].Provider != "codex" || points[0].Value != 0 {
		t.Fatalf("Registry phantom counted: %+v; agents=%+v", points, graph.Agents)
	}
}

func TestLiveAgentHistoryRunsOnlyAfterSuccessfulReconcile(t *testing.T) {
	command, _, server, _, _ := newReconcileFixture(t, "-L", "primary")
	called := 0
	command.historyAppend = func(graph resourcegraph.Graph) error {
		called++
		if len(graph.Projects) != 1 {
			t.Fatalf("history saw partial graph: %+v", graph)
		}
		return nil
	}
	if _, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("dry-run wrote history %d times", called)
	}
	server.fail = []string{"set-option", tmuxopts.WindowUID}
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err == nil {
		t.Fatal("expected injected reconcile failure")
	}
	if called != 0 {
		t.Fatalf("failed reconcile wrote history %d times", called)
	}
	server.fail = nil
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("successful reconcile history calls=%d", called)
	}
	command.historyAppend = func(resourcegraph.Graph) error { return errors.New("injected history failure") }
	_, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(stderr, "usage: history write: injected history failure") {
		t.Fatalf("history failure changed reconcile success or lost warning: err=%v stderr=%q", err, stderr)
	}
}
