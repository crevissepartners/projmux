package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

func processRuntimeDisplayRegistry(t *testing.T) coremetadata.Registry {
	t.Helper()
	raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err = json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// describeProcessRuntime renders one describe route through the same
// process-aware read path production wires, with the host observation fixed.
func describeProcessRuntime(t *testing.T, reg coremetadata.Registry, observed []resourcegraph.ProcessObservation, args ...string) string {
	t.Helper()
	reader := &runtimeDiagnosticsReader{processes: func(context.Context, coremetadata.Registry) resourcegraph.ProcessInventory {
		inventory := resourcegraph.ProcessInventory{Observed: observed}
		for _, observation := range observed {
			inventory.Declared = append(inventory.Declared, observation.Key)
		}
		return inventory
	}}
	cmd := &describeCommand{loadRegistry: func() (coremetadata.Registry, error) { return reg.Clone(), nil }, reads: runtimeResourceReadLookup(reader)}
	stdout, _, err := runRoute(t, cmd, args...)
	if err != nil {
		t.Fatalf("describe %v: %v", args, err)
	}
	return stdout
}

// The process rows are ordinary user-language rows: host, PIDs, resume state,
// and a pending count. No JSON block or provider content reaches describe.
func TestProcessRuntimeDescribeGolden(t *testing.T) {
	reg := processRuntimeDisplayRegistry(t)
	pane, _ := reg.Pane("pane-02")
	key := resourcegraph.ProcessKey{Host: pane.Status.Activation.Process.Binding.HostInstanceID, Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation}
	live := []resourcegraph.ProcessObservation{{Key: key, Status: resourcegraph.StatusLive}}
	scope := []string{"--project", "uid:project-01", "--window", "uid:window-01"}
	const processRows = "RuntimeKind:     process\n" +
		"ProcessHost:     host-one\n" +
		"HostPID:         11\n" +
		"ChildPID:        12\n" +
		"ResumeState:     resumable\n" +
		"PendingControls: 1\n"
	agent := func(status string) string {
		return "Kind:            Agent\n" +
			"Name:            agent-01\n" +
			"UID:             agent-01\n" +
			"CreatedAt:       2026-08-15T09:30:00Z\n" +
			"Context:         codex\n" +
			"ContextSource:   agent-provider\n" +
			"ContextObserved: false\n" +
			"Owner:           project/alpha window/window-01\n" +
			"Status:          " + status + "\n" +
			"Provider:        codex\n" +
			"Phase:           Running\n" +
			"Interaction:     unknown\n" +
			"Activation:      not_requested\n" +
			"WorkspaceCWD:    /srv/alpha\n" +
			"PhaseSince:      2026-08-15T09:30:00Z\n" +
			"PaneRef:         pane-02\n" +
			processRows
	}
	for _, test := range []struct {
		name     string
		observed []resourcegraph.ProcessObservation
		args     []string
		want     string
	}{
		{"live pane", live, append([]string{"pane", "uid:pane-02"}, scope...), "Kind:            Pane\n" +
			"Name:            pane-02\n" +
			"UID:             pane-02\n" +
			"CreatedAt:       2026-08-15T09:30:00Z\n" +
			"Context:         \n" +
			"ContextSource:   \n" +
			"ContextObserved: false\n" +
			"Owner:           project/alpha window/window-01 agent/agent-01\n" +
			"Status:          live\n" +
			"Role:            agent\n" +
			processRows +
			"CWD:             /srv/alpha\n"},
		{"live agent", live, append([]string{"agent", "uid:agent-01"}, scope...), agent("live")},
		// An unreachable owner with no recorded exit is unknown, not offline.
		{"host unavailable", nil, append([]string{"agent", "uid:agent-01"}, scope...), agent("unknown")},
	} {
		if got := describeProcessRuntime(t, reg, test.observed, test.args...); got != test.want {
			t.Fatalf("%s:\n%s\nwant\n%s", test.name, got, test.want)
		}
	}
}
