package metadata

import (
	"fmt"
	"testing"
	"time"
)

// The write-closure property.
//
// Validate is the one gate every registry write passes, so the set of registries
// the Mutator can produce has to be a subset of the set Validate accepts. If it
// is not, the offending write does not fail at the time it is made: it commits a
// document that the *next* write rejects, and from that point every mutation of
// that registry fails until somebody repairs the file by hand. A pair of
// individually legal commands then adds up to a registry nothing can change.
//
// The tests below drive the shipped Mutator rather than building structs, because
// the question is not whether an invalid registry can be constructed. It is
// whether the product writes one.

// pbtMutatorOver builds a fully deterministic Mutator over one existing root.
func pbtMutatorOver(root string) Mutator {
	counters := map[Kind]int{}
	clock := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	return Mutator{
		Now: func() time.Time {
			clock = clock.Add(time.Second)
			return clock
		},
		NewUID: func(kind Kind) (string, error) {
			counters[kind]++
			return fmt.Sprintf("%s-pbt%03d", kind, counters[kind]), nil
		},
		DirExists: func(path string) (bool, error) { return path == root, nil },
	}
}

func pbtRegistryOver(t *testing.T, root string) (*Registry, Project, Mutator) {
	t.Helper()
	mutator := pbtMutatorOver(root)
	registry := NewRegistry()
	result, err := mutator.RegisterProject(&registry, RegisterProjectOptions{Root: root, OperationID: "pbt-register"})
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	return &registry, result.Project, mutator
}

// TestDeletedLastPaneThenCreateKeepsRegistryValid pins the exact pair of legal
// commands that produced an unwritable registry in the field: deleting a Window's
// last Pane empties primaryPaneRef by design, and the next Pane added under that
// Window has to reclaim it.
func TestDeletedLastPaneThenCreateKeepsRegistryValid(t *testing.T) {
	root := t.TempDir()
	registry, project, mutator := pbtRegistryOver(t, root)

	window := registry.WindowsOf(project.Metadata.UID)[0]
	first := registry.PanesOf(window.Metadata.UID)[0]
	if err := mutator.DeletePane(registry, first.Metadata.UID); err != nil {
		t.Fatalf("delete last pane: %v", err)
	}
	stored, _ := registry.Window(window.Metadata.UID)
	if stored.Spec.PrimaryPaneRef != "" {
		t.Fatalf("primaryPaneRef = %q, want empty once the Window owns no Pane", stored.Spec.PrimaryPaneRef)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("a Window with no Pane must stay valid: %v", err)
	}

	added, err := mutator.AddPane(registry, window.Metadata.UID, BootstrapPane{}, "sh", "pbt-add")
	if err != nil {
		t.Fatalf("add pane: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("AddPane committed a registry Validate rejects: %v", err)
	}
	stored, _ = registry.Window(window.Metadata.UID)
	if stored.Spec.PrimaryPaneRef != added.Metadata.UID {
		t.Fatalf("primaryPaneRef = %q, want the returning Pane %q", stored.Spec.PrimaryPaneRef, added.Metadata.UID)
	}
}

// TestAgentPaneReclaimsPrimaryPaneRef pins the same rule for the Agent half.
// Validate counts a Pane owned by one of the Window's Agents as a Pane the Window
// owns, so attaching one to an empty Window has to reclaim primaryPaneRef too.
func TestAgentPaneReclaimsPrimaryPaneRef(t *testing.T) {
	root := t.TempDir()
	registry, project, mutator := pbtRegistryOver(t, root)

	window := registry.WindowsOf(project.Metadata.UID)[0]
	first := registry.PanesOf(window.Metadata.UID)[0]
	if err := mutator.DeletePane(registry, first.Metadata.UID); err != nil {
		t.Fatalf("delete last pane: %v", err)
	}
	agent, err := mutator.CreateAgent(registry, window.Metadata.UID, CreateAgentOptions{Provider: "codex", OperationID: "pbt-agent"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	pane, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, BootstrapPane{}, "pbt-attach")
	if err != nil {
		t.Fatalf("attach agent pane: %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("AttachAgentPane committed a registry Validate rejects: %v", err)
	}
	stored, _ := registry.Window(window.Metadata.UID)
	if stored.Spec.PrimaryPaneRef != pane.Metadata.UID {
		t.Fatalf("primaryPaneRef = %q, want the Agent's Pane %q", stored.Spec.PrimaryPaneRef, pane.Metadata.UID)
	}
}

// FuzzMutatorNeverCommitsInvalidRegistry generalizes both. Every operation is one
// the product exposes and every intermediate registry is asserted valid, so the
// property is the write closure itself.
func FuzzMutatorNeverCommitsInvalidRegistry(f *testing.F) {
	f.Add([]byte{2, 0})
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{4, 1, 2, 0, 3})
	f.Add([]byte{4, 1, 2, 0, 3, 5, 0, 2})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 48 {
			data = data[:48]
		}
		root := t.TempDir()
		registry, project, mutator := pbtRegistryOver(t, root)

		for step, op := range data {
			windows := registry.WindowsOf(project.Metadata.UID)
			if len(windows) == 0 {
				break
			}
			window := windows[int(op)%len(windows)]
			switch op % 6 {
			case 0:
				_, _ = mutator.AddPane(registry, window.Metadata.UID, BootstrapPane{}, "sh", fmt.Sprintf("pbt-pane-%d", step))
			case 1:
				if agent, err := mutator.CreateAgent(registry, window.Metadata.UID, CreateAgentOptions{
					Provider:    "codex",
					OperationID: fmt.Sprintf("pbt-agent-%d", step),
				}); err == nil {
					_, _ = mutator.AttachAgentPane(registry, agent.Metadata.UID, BootstrapPane{}, fmt.Sprintf("pbt-attach-%d", step))
				}
			case 2:
				if panes := registry.PanesOf(window.Metadata.UID); len(panes) > 0 {
					_ = mutator.DeletePane(registry, panes[0].Metadata.UID)
				}
			case 3:
				if agents := registry.AgentsOf(window.Metadata.UID); len(agents) > 0 {
					_, _ = mutator.ReleaseAgentPane(registry, agents[0].Metadata.UID, AgentExitNormal, "pbt")
				}
			case 4:
				_, _, _ = mutator.AddWindow(registry, project.Metadata.UID, BootstrapWindow{}, "sh", fmt.Sprintf("pbt-window-%d", step))
			case 5:
				if agents := registry.AgentsOf(window.Metadata.UID); len(agents) > 0 {
					_ = mutator.DeleteAgent(registry, agents[0].Metadata.UID)
				}
			}
			if err := registry.Validate(); err != nil {
				t.Fatalf("step %d (op %d) committed a registry Validate rejects: %v", step, op, err)
			}
		}
	})
}
