package metadata

import (
	"encoding/json"
	"errors"
	"reflect"
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
			recipe := sessionstate.ShellRecipe()
			if pane.Spec.Command != "" {
				recipe = sessionstate.StartupRecipe(pane.Spec.Command)
			}
			snapWindow.Panes = append(snapWindow.Panes, sessionstate.Pane{
				Index:  pi,
				Label:  pane.Metadata.Name,
				CWD:    pane.Spec.CWD,
				Recipe: recipe,
			})
		}
		snap.Windows = append(snap.Windows, snapWindow)
	}
	return snap
}

// buildCurrentSnapshot adds the resource identity blocks emitted by current
// snapshot producers. It is a fixture constructor, not a second application
// path for projecting snapshot state into the Registry.
func buildCurrentSnapshot(reg *Registry, projectUID, session string) sessionstate.Snapshot {
	snap := buildSnapshot(reg, projectUID, session)
	project, _ := reg.Project(projectUID)
	snap.Metadata = snapshotFixtureMetadata(project.Metadata, "", "")
	for wi, window := range reg.WindowsOf(projectUID) {
		if wi >= len(snap.Windows) {
			break
		}
		snap.Windows[wi].Metadata = snapshotFixtureMetadata(window.Metadata, string(KindProject), projectUID)
		for pi, pane := range reg.snapshotPanesOf(window.Metadata.UID) {
			if pi >= len(snap.Windows[wi].Panes) {
				break
			}
			snap.Windows[wi].Panes[pi].Metadata = snapshotFixtureMetadata(pane.Metadata, string(pane.Metadata.OwnerRef.Kind), pane.Metadata.OwnerUID())
		}
	}
	return snap
}

func snapshotFixtureMetadata(meta ObjectMeta, ownerKind, ownerUID string) *sessionstate.ResourceMetadata {
	return &sessionstate.ResourceMetadata{
		UID:       meta.UID,
		Name:      meta.Name,
		Labels:    cloneStringMap(meta.Labels),
		OwnerKind: ownerKind,
		OwnerUID:  ownerUID,
	}
}

func TestSnapshotProjectionCurrentMetadataRestoresExactDesiredRegistry(t *testing.T) {
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

	snap := buildCurrentSnapshot(&reg, projectUID, "projmux")
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

	uidSourceCalled := false
	plan, err := PlanSnapshotProjection(reg, projectUID, restored, fixedNow, func(Kind) (string, error) {
		uidSourceCalled = true
		return "", errors.New("current metadata fixture unexpectedly requested a uid")
	})
	if err != nil {
		t.Fatalf("plan projection: %v", err)
	}
	if uidSourceCalled {
		t.Fatal("current metadata fixture allocated a replacement uid")
	}
	if plan.Changed || !reflect.DeepEqual(plan.Desired, reg) {
		t.Fatalf("current metadata projection changed desired Registry:\n%s", mustJSON(t, plan.Desired))
	}

	// Window ownerRef survives the round trip.
	if restored.Windows[0].Metadata.OwnerKind != string(KindProject) || restored.Windows[0].Metadata.OwnerUID != projectUID {
		t.Fatalf("window ownerRef = %+v", restored.Windows[0].Metadata)
	}
}

func TestSnapshotProjectionLegacyMetadataFallbackRestoresExactDesiredRegistry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		session    string
		defaultCWD string
	}{
		{name: "matching runtime projection", session: "projmux", defaultCWD: ""},
		{name: "matching root", session: "other", defaultCWD: "/src/projmux"},
		{name: "unrelated legacy hints", session: "other", defaultCWD: ""},
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

			plan, err := PlanSnapshotProjection(reg, projectUID, legacy, fixedNow, sequentialUIDs())
			if err != nil {
				t.Fatalf("plan legacy projection: %v", err)
			}
			if plan.Changed || !reflect.DeepEqual(plan.Desired, reg) {
				t.Fatalf("legacy positional projection changed desired Registry:\n%s", mustJSON(t, plan.Desired))
			}
			repeat, err := PlanSnapshotProjection(plan.Desired, projectUID, legacy, fixedNow.Add(1), sequentialUIDs())
			if err != nil {
				t.Fatalf("repeat legacy projection: %v", err)
			}
			if repeat.Changed || !reflect.DeepEqual(repeat.Desired, plan.Desired) {
				t.Fatal("legacy positional projection did not reach an exact Registry fixed point")
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
