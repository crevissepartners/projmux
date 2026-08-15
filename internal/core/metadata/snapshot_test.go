package metadata

import (
	"encoding/json"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

// buildSnapshot renders the tmux-shaped snapshot for a registered project.
func buildSnapshot(reg *Registry, projectUID, session string) sessionstate.Snapshot {
	project, _ := reg.Project(projectUID)
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    session,
		Source:     sessionstate.SourceAutosave,
		DefaultCWD: project.Spec.Root,
		SavedAt:    fixedNow,
	}
	for wi, window := range reg.WindowsOf(projectUID) {
		snapWindow := sessionstate.Window{Index: wi, Name: window.Metadata.Name}
		for pi, pane := range reg.snapshotPanesOf(window.Metadata.UID) {
			snapWindow.Panes = append(snapWindow.Panes, sessionstate.Pane{
				Index:  pi,
				Label:  pane.Metadata.Name,
				CWD:    pane.Spec.CWD,
				Recipe: sessionstate.ShellRecipe(),
			})
		}
		snap.Windows = append(snap.Windows, snapWindow)
	}
	return snap
}

func TestSnapshotCarriesResourceMetadataAndRestoresTheSameLogicalResources(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()
	registered, err := m.RegisterProject(&reg, RegisterProjectOptions{
		Root:         "/src/projmux",
		DefaultShell: "/bin/zsh",
		Topology: []BootstrapWindow{
			{Command: "nvim"},
			{Name: "server", Panes: []BootstrapPane{{Command: "npm"}, {Command: "htop"}}},
		},
		Labels:      map[string]string{"tier": "primary"},
		OperationID: "op-snapshot",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	projectUID := registered.Project.Metadata.UID

	snap := buildSnapshot(&reg, projectUID, "projmux")
	if err := AttachSnapshotMetadata(&reg, projectUID, &snap); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := snap.Validate(); err != nil {
		t.Fatalf("snapshot with resource metadata is invalid: %v", err)
	}
	if snap.Version != sessionstate.Version {
		t.Fatalf("snapshot version = %d, want the unchanged %d", snap.Version, sessionstate.Version)
	}
	if snap.Metadata == nil || snap.Metadata.UID != projectUID || snap.Metadata.Labels["tier"] != "primary" {
		t.Fatalf("snapshot project metadata = %+v", snap.Metadata)
	}

	// Round-trip through JSON exactly as the store does.
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored sessionstate.Snapshot
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatalf("restored snapshot is invalid: %v", err)
	}

	reconciled := ReconcileSnapshot(&reg, restored)
	if reconciled.ProjectUID != projectUID || reconciled.ProjectMatch != MatchUID {
		t.Fatalf("project reconciliation = %+v", reconciled)
	}
	for _, binding := range reconciled.Windows {
		if binding.Match != MatchUID {
			t.Fatalf("window %d matched by %q, want uid", binding.WindowIndex, binding.Match)
		}
	}
	if len(reconciled.Panes) != 3 {
		t.Fatalf("pane bindings = %d, want 3", len(reconciled.Panes))
	}
	for _, binding := range reconciled.Panes {
		if binding.Match != MatchUID {
			t.Fatalf("pane %d/%d matched by %q, want uid", binding.WindowIndex, binding.PaneIndex, binding.Match)
		}
		if _, ok := reg.Pane(binding.UID); !ok {
			t.Fatalf("pane binding %q does not resolve", binding.UID)
		}
	}

	// Window ownerRef survives the round trip.
	if restored.Windows[0].Metadata.OwnerKind != string(KindProject) || restored.Windows[0].Metadata.OwnerUID != projectUID {
		t.Fatalf("window ownerRef = %+v", restored.Windows[0].Metadata)
	}
}

func TestSnapshotsWithoutResourceMetadataStillLoadAndReconcileDeterministically(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		session    string
		defaultCWD string
		wantMatch  MatchSource
	}{
		{name: "legacy snapshot matches by session projection", session: "projmux", defaultCWD: "", wantMatch: MatchSession},
		{name: "legacy snapshot matches by root", session: "other", defaultCWD: "/src/projmux", wantMatch: MatchRoot},
		{name: "unrelated legacy snapshot matches nothing", session: "other", defaultCWD: "", wantMatch: MatchNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			roots := dirSet{"/src/projmux": true}
			m := testMutator(roots)
			reg := NewRegistry()
			registered, err := m.RegisterProject(&reg, RegisterProjectOptions{
				Root:         "/src/projmux",
				DefaultShell: "/bin/zsh",
				Topology:     []BootstrapWindow{{Command: "nvim"}, {Command: "zsh"}},
				SessionName:  "projmux",
			})
			if err != nil {
				t.Fatalf("register: %v", err)
			}
			projectUID := registered.Project.Metadata.UID

			legacy := buildSnapshot(&reg, projectUID, tt.session)
			legacy.DefaultCWD = tt.defaultCWD
			// A pre-metadata snapshot: every block is absent.
			legacy.Metadata = nil
			for wi := range legacy.Windows {
				legacy.Windows[wi].Metadata = nil
				for pi := range legacy.Windows[wi].Panes {
					legacy.Windows[wi].Panes[pi].Metadata = nil
				}
			}
			if err := legacy.Validate(); err != nil {
				t.Fatalf("a legacy snapshot must still validate: %v", err)
			}

			reconciled := ReconcileSnapshot(&reg, legacy)
			if reconciled.ProjectMatch != tt.wantMatch {
				t.Fatalf("project match = %q, want %q", reconciled.ProjectMatch, tt.wantMatch)
			}
			if tt.wantMatch == MatchNone {
				for _, binding := range reconciled.Windows {
					if binding.Match != MatchNone {
						t.Fatalf("window %d matched %q without a project", binding.WindowIndex, binding.Match)
					}
				}
				return
			}
			if reconciled.ProjectUID != projectUID {
				t.Fatalf("project uid = %q, want %q", reconciled.ProjectUID, projectUID)
			}
			windows := reg.WindowsOf(projectUID)
			for i, binding := range reconciled.Windows {
				if binding.Match != MatchPositional {
					t.Fatalf("window %d matched by %q, want positional", i, binding.Match)
				}
				if binding.UID != windows[i].Metadata.UID {
					t.Fatalf("window %d bound to %q, want %q", i, binding.UID, windows[i].Metadata.UID)
				}
			}
			// Reconciliation is deterministic: repeated runs agree.
			if mustJSON(t, ReconcileSnapshot(&reg, legacy)) != mustJSON(t, reconciled) {
				t.Fatal("legacy reconciliation is not deterministic")
			}
		})
	}
}

func TestSnapshotResourceMetadataValidationRejectsIdentityFreeBlocks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*sessionstate.Snapshot)
		wantErr bool
	}{
		{name: "absent block is valid", mutate: func(s *sessionstate.Snapshot) { s.Metadata = nil }},
		{name: "block without uid is invalid", mutate: func(s *sessionstate.Snapshot) {
			s.Metadata = &sessionstate.ResourceMetadata{Name: "projmux"}
		}, wantErr: true},
		{name: "block without name is invalid", mutate: func(s *sessionstate.Snapshot) {
			s.Metadata = &sessionstate.ResourceMetadata{UID: "project-01"}
		}, wantErr: true},
		{name: "half an owner ref is invalid", mutate: func(s *sessionstate.Snapshot) {
			s.Windows[0].Metadata = &sessionstate.ResourceMetadata{UID: "window-01", Name: "zsh", OwnerKind: "Project"}
		}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			roots := dirSet{"/src/projmux": true}
			m := testMutator(roots)
			reg := NewRegistry()
			registered, err := registerFixture(m, &reg, "/src/projmux")
			if err != nil {
				t.Fatalf("register: %v", err)
			}
			snap := buildSnapshot(&reg, registered.Project.Metadata.UID, "projmux")
			tt.mutate(&snap)
			err = snap.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
