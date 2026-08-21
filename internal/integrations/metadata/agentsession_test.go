package metadata

import (
	"os"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// preSessionRefRegistry is a registry document exactly as builds before the
// Agent session ref existed wrote it: an Agent whose status carries a phase and
// a transition timestamp and no sessionRef key at all.
const preSessionRefRegistry = `{
  "apiVersion": "projmux.io/v1alpha1",
  "schemaVersion": 1,
  "updatedAt": "2026-08-14T00:00:00Z",
  "projects": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Project",
      "metadata": {
        "uid": "project-01",
        "name": "projmux",
        "createdAt": "2026-08-14T00:00:00Z"
      },
      "spec": {
        "root": "/src/projmux"
      },
      "status": {}
    }
  ],
  "windows": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Window",
      "metadata": {
        "uid": "window-01",
        "name": "zsh",
        "ownerRef": {
          "kind": "Project",
          "uid": "project-01"
        },
        "createdAt": "2026-08-14T00:00:00Z"
      },
      "spec": {
        "primaryPaneRef": "pane-01"
      }
    }
  ],
  "panes": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Pane",
      "metadata": {
        "uid": "pane-01",
        "name": "zsh",
        "ownerRef": {
          "kind": "Window",
          "uid": "window-01"
        },
        "createdAt": "2026-08-14T00:00:00Z"
      },
      "spec": {
        "role": "shell",
        "cwd": "/src/projmux",
        "command": "zsh"
      },
      "status": {}
    },
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Pane",
      "metadata": {
        "uid": "pane-02",
        "name": "codex-pane",
        "ownerRef": {
          "kind": "Agent",
          "uid": "agent-01"
        },
        "createdAt": "2026-08-14T00:00:00Z"
      },
      "spec": {
        "role": "agent",
        "cwd": "/src/projmux",
        "command": "codex"
      },
      "status": {}
    }
  ],
  "agents": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Agent",
      "metadata": {
        "uid": "agent-01",
        "name": "codex",
        "ownerRef": {
          "kind": "Window",
          "uid": "window-01"
        },
        "createdAt": "2026-08-14T00:00:00Z"
      },
      "spec": {
        "provider": "codex"
      },
      "status": {
        "phase": "Running",
        "paneRef": "pane-02",
        "lastTransitionAt": "2026-08-14T00:00:00Z"
      }
    }
  ],
  "nameReservations": [
    {"kind": "Project", "name": "projmux", "uid": "project-01"},
    {"scope": "project-01", "kind": "Window", "name": "zsh", "uid": "window-01"},
    {"scope": "window-01", "kind": "Pane", "name": "zsh", "uid": "pane-01"},
    {"scope": "agent-01", "kind": "Pane", "name": "codex-pane", "uid": "pane-02"},
    {"scope": "window-01", "kind": "Agent", "name": "codex", "uid": "agent-01"}
  ]
}
`

// TestARegistryWrittenWithoutASessionRefMigratesWithoutInventingThePointer is
// the session-pointer preservation half of the v1 -> v2 repair.
func TestARegistryWrittenWithoutASessionRefMigratesWithoutInventingThePointer(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	writeRegistryFile(t, store, preSessionRefRegistry)
	before := readFile(t, store.Path())

	registry, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(registry.Agents) != 1 {
		t.Fatalf("registry holds %d agents, want 1", len(registry.Agents))
	}
	if ref := registry.Agents[0].Status.SessionRef; ref != nil {
		t.Fatalf("session ref = %#v, want nil for a document that has no such key", ref)
	}
	if registry.Agents[0].Status.Phase != coremetadata.PhaseRunning || registry.Agents[0].Status.PaneRef != "pane-02" {
		t.Fatalf("existing status fields changed: %+v", registry.Agents[0].Status)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("a pre-field registry must validate: %v", err)
	}
	if registry.SchemaVersion != coremetadata.SchemaVersion || registry.Projects[0].Spec.PrimaryWindowRef != "window-01" {
		t.Fatalf("migrated envelope/anchor = %d/%q", registry.SchemaVersion, registry.Projects[0].Spec.PrimaryWindowRef)
	}

	// The normal locked Load durably migrates once and retains the v1 bytes.
	afterFirst := readFile(t, store.Path())
	if afterFirst == before {
		t.Fatal("Load did not durably migrate the v1 registry")
	}
	names := dirListing(t, dirOf(store.Path()))
	if !slices.ContainsFunc(names, func(name string) bool { return strings.Contains(name, ".v1.") && strings.HasSuffix(name, ".bak") }) {
		t.Fatalf("Load left %v beside the registry, want the versioned v1 backup", names)
	}

	result, err := store.Migrate()
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if result.Migrated {
		t.Fatal("the second migration pass must be a no-op")
	}
	if after := readFile(t, store.Path()); after != afterFirst {
		t.Fatal("the second migration pass rewrote the current-version registry")
	}
}

// TestRecordingASessionRefOnAPreFieldRegistryOnlyAddsThatKey proves the upgrade
// path in the other direction: writing the new field onto an old document is an
// ordinary transaction that leaves every pre-existing value alone.
func TestRecordingASessionRefOnAPreFieldRegistryOnlyAddsThatKey(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	writeRegistryFile(t, store, preSessionRefRegistry)

	mutator := testMutator(map[string]bool{"/src/projmux": true})
	updated, err := store.Update(func(registry *coremetadata.Registry) error {
		_, changed, err := mutator.RecordAgentSessionRef(registry, "agent-01", coremetadata.AgentSessionObservation{
			Provider:  "codex",
			ThreadID:  "codex-thread-1",
			SessionID: "codex-session-1",
		})
		if err != nil {
			return err
		}
		if !changed {
			t.Fatal("recording onto a pre-field registry reported no change")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	agent, ok := updated.Agent("agent-01")
	if !ok {
		t.Fatal("agent-01 disappeared")
	}
	if got := agent.Status.SessionRef.Summary(); got != "codex:codex-thread-1" {
		t.Fatalf("session ref = %q, want the recorded codex conversation", got)
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != "pane-02" {
		t.Fatalf("the write disturbed the existing status: %+v", agent.Status)
	}

	// The persisted file gained exactly the new key and stayed loadable.
	persisted := readFile(t, store.Path())
	if !strings.Contains(persisted, `"sessionRef"`) || !strings.Contains(persisted, `"threadId": "codex-thread-1"`) {
		t.Fatalf("persisted registry does not carry the session ref:\n%s", persisted)
	}
	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Agents[0].Status.SessionRef.Summary(); got != "codex:codex-thread-1" {
		t.Fatalf("reloaded session ref = %q", got)
	}
}

func dirOf(path string) string {
	if idx := strings.LastIndex(path, string(os.PathSeparator)); idx >= 0 {
		return path[:idx]
	}
	return path
}
