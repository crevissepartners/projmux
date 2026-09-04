package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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
			if pane.Spec.Role == PaneRoleAgent {
				agent, _ := reg.Agent(pane.Metadata.OwnerUID())
				snap.Windows[wi].Panes[pi].AgentMetadata = snapshotFixtureMetadata(agent.Metadata, string(KindWindow), window.Metadata.UID)
			}
		}
	}
	return snap
}

func snapshotFixtureMetadata(meta ObjectMeta, ownerKind, ownerUID string) *sessionstate.ResourceMetadata {
	return &sessionstate.ResourceMetadata{
		UID:                   meta.UID,
		Name:                  meta.Name,
		Labels:                cloneStringMap(meta.Labels),
		OwnerKind:             ownerKind,
		OwnerUID:              ownerUID,
		RegistrySchemaVersion: SchemaVersion,
	}
}

func TestStampProjectSnapshotJoinsExactRuntimeIDsAcrossReorderAndRefusesDrift(t *testing.T) {
	t.Parallel()

	reg, projectUID, _, _ := projectionFixture(t)
	windows := reg.WindowsOf(projectUID)
	for wi, window := range windows {
		stored, _ := reg.Window(window.Metadata.UID)
		stored.Status.RuntimeSessionID = "$1"
		stored.Status.RuntimeID = fmt.Sprintf("@%d", wi+10)
		for pi, pane := range reg.snapshotPanesOf(window.Metadata.UID) {
			storedPane, _ := reg.Pane(pane.Metadata.UID)
			storedPane.Status.Activation.Generation = fmt.Sprintf("generation-%d-%d", wi, pi)
			storedPane.Status.Activation.RuntimeID = fmt.Sprintf("%%%d", wi*10+pi+20)
		}
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	captured := buildSnapshot(&reg, projectUID, "one")
	for wi, window := range reg.WindowsOf(projectUID) {
		captured.Windows[wi].RuntimeID = window.Status.RuntimeID
		captured.Windows[wi].RegistryUID = window.Metadata.UID
		for pi, pane := range reg.snapshotPanesOf(window.Metadata.UID) {
			captured.Windows[wi].Panes[pi].RuntimeID = pane.Status.Activation.RuntimeID
			captured.Windows[wi].Panes[pi].RegistryUID = pane.Metadata.UID
			if pane.Spec.Role == PaneRoleAgent {
				agent, _ := reg.Agent(pane.Metadata.OwnerUID())
				captured.Windows[wi].Panes[pi].Recipe = sessionstate.AgentRecipe(string(agent.Spec.Provider), "", "")
			}
		}
		slices.Reverse(captured.Windows[wi].Panes)
	}
	slices.Reverse(captured.Windows)
	stamped, err := StampProjectSnapshot(reg, projectUID, captured)
	if err != nil {
		t.Fatalf("stamp reordered capture: %v", err)
	}
	if stamped.Metadata == nil || stamped.Metadata.UID != projectUID || stamped.Metadata.RegistrySchemaVersion != SchemaVersion {
		t.Fatalf("Project metadata = %+v", stamped.Metadata)
	}
	for _, sw := range stamped.Windows {
		window := windowByRuntime(t, reg, sw.RuntimeID)
		if sw.Metadata == nil || sw.Metadata.UID != window.Metadata.UID || sw.Metadata.RegistrySchemaVersion != SchemaVersion {
			t.Fatalf("Window runtime %s metadata = %+v, want %s", sw.RuntimeID, sw.Metadata, window.Metadata.UID)
		}
		for _, sp := range sw.Panes {
			pane := paneByRuntime(t, reg, sp.RuntimeID)
			if sp.Metadata == nil || sp.Metadata.UID != pane.Metadata.UID || sp.Metadata.RegistrySchemaVersion != SchemaVersion {
				t.Fatalf("Pane runtime %s metadata = %+v, want %s", sp.RuntimeID, sp.Metadata, pane.Metadata.UID)
			}
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*sessionstate.Snapshot)
	}{
		{name: "foreign runtime", mutate: func(s *sessionstate.Snapshot) { s.Windows[0].RuntimeID = "@foreign" }},
		{name: "blank mirrored uid", mutate: func(s *sessionstate.Snapshot) { s.Windows[0].RegistryUID = "" }},
		{name: "wrong mirrored uid", mutate: func(s *sessionstate.Snapshot) { s.Windows[0].RegistryUID = "win-foreign" }},
		{name: "stale pane uid on matching runtime", mutate: func(s *sessionstate.Snapshot) { s.Windows[0].Panes[0].RegistryUID = "pan-foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drift := captured
			drift.Windows = append([]sessionstate.Window(nil), captured.Windows...)
			for wi := range drift.Windows {
				drift.Windows[wi].Panes = append([]sessionstate.Pane(nil), captured.Windows[wi].Panes...)
			}
			tc.mutate(&drift)
			if _, err := StampProjectSnapshot(reg, projectUID, drift); !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("drift error=%v, want ErrInvalidRegistry", err)
			}
		})
	}
	duplicate := reg.Clone()
	duplicateWindows := duplicate.WindowsOf(projectUID)
	if len(duplicateWindows) < 2 {
		t.Fatal("fixture requires two Windows")
	}
	second, _ := duplicate.Window(duplicateWindows[1].Metadata.UID)
	second.Status.RuntimeSessionID = duplicateWindows[0].Status.RuntimeSessionID
	second.Status.RuntimeID = duplicateWindows[0].Status.RuntimeID
	if _, err := StampProjectSnapshot(duplicate, projectUID, captured); !errors.Is(err, ErrInvalidRegistry) || !strings.Contains(err.Error(), "share runtime id") {
		t.Fatalf("duplicate binding error=%v", err)
	}
	duplicate = reg.Clone()
	firstWindowPanes := duplicate.snapshotPanesOf(duplicateWindows[0].Metadata.UID)
	secondWindowPanes := duplicate.snapshotPanesOf(duplicateWindows[1].Metadata.UID)
	if len(firstWindowPanes) == 0 || len(secondWindowPanes) == 0 {
		t.Fatal("fixture requires Panes in both Windows")
	}
	secondPane, _ := duplicate.Pane(secondWindowPanes[0].Metadata.UID)
	secondPane.Status.Activation.RuntimeID = firstWindowPanes[0].Status.Activation.RuntimeID
	if _, err := StampProjectSnapshot(duplicate, projectUID, captured); !errors.Is(err, ErrInvalidRegistry) || !strings.Contains(err.Error(), "share runtime id") {
		t.Fatalf("duplicate Pane binding error=%v", err)
	}
}

func TestCurrentSnapshotRestoresAbsentExplicitAgentMetadataAndRefusesAgentNameCollision(t *testing.T) {
	t.Parallel()
	m := testMutator(dirSet{"/src/agent-snapshot": true})
	sourceRegistry := NewRegistry()
	created, err := m.RegisterProject(&sourceRegistry, RegisterProjectOptions{Root: "/src/agent-snapshot", SessionName: "agent-snapshot", DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	window, _ := sourceRegistry.Window(created.Project.Spec.PrimaryWindowRef)
	agent, err := m.CreateAgent(&sourceRegistry, window.Metadata.UID, CreateAgentOptions{Name: "Explicit-Reviewer", Provider: "codex", Workspace: AgentWorkspace{CWD: "/src/agent-snapshot"}, OperationID: "snapshot-explicit-agent"})
	if err != nil {
		t.Fatal(err)
	}
	pane, err := m.AttachAgentPane(&sourceRegistry, agent.Metadata.UID, BootstrapPane{CWD: "/src/agent-snapshot"}, "snapshot-explicit-agent-pane")
	if err != nil {
		t.Fatal(err)
	}
	window, _ = sourceRegistry.Window(window.Metadata.UID)
	window.Status.RuntimeSessionID, window.Status.RuntimeID = "$1", "@1"
	storedPane, _ := sourceRegistry.Pane(pane.Metadata.UID)
	storedPane.Status.Activation.Generation, storedPane.Status.Activation.RuntimeID = "generation-explicit", "%1"
	captured := sessionstate.Snapshot{Version: sessionstate.Version, Session: "agent-snapshot", SavedAt: fixedNow,
		Windows: []sessionstate.Window{{Index: 0, RuntimeID: "@1", RegistryUID: window.Metadata.UID, ActivePaneIndex: 0,
			Panes: []sessionstate.Pane{{Index: 0, RuntimeID: "%1", RegistryUID: pane.Metadata.UID, CWD: "/src/agent-snapshot", Recipe: sessionstate.AgentRecipe("codex", "", "")}}}},
	}
	stamped, err := StampProjectSnapshot(sourceRegistry, created.Project.Metadata.UID, captured)
	if err != nil {
		t.Fatal(err)
	}
	if stamped.Windows[0].Panes[0].AgentMetadata == nil || stamped.Windows[0].Panes[0].AgentMetadata.Name != "Explicit-Reviewer" {
		t.Fatalf("stamped Agent metadata=%+v", stamped.Windows[0].Panes[0].AgentMetadata)
	}
	target := sourceRegistry.Clone()
	target.Agents = nil
	target.Panes = filter(target.Panes, func(candidate Pane) bool { return candidate.Metadata.UID != pane.Metadata.UID })
	target.NameReservations = filter(target.NameReservations, func(reservation NameReservation) bool {
		return reservation.UID != agent.Metadata.UID && reservation.UID != pane.Metadata.UID
	})
	if err := target.Validate(); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanSnapshotProjection(target, created.Project.Metadata.UID, stamped, fixedNow.Add(time.Hour), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}
	restored, ok := plan.Desired.Agent(agent.Metadata.UID)
	if !ok || restored.Metadata.Name != "Explicit-Reviewer" {
		t.Fatalf("restored absent Agent=%+v stamped=%+v receipt=%+v", restored, stamped.Windows[0].Panes[0].AgentMetadata, plan.Migration)
	}

	collision := stamped
	collision.Windows = append([]sessionstate.Window(nil), stamped.Windows...)
	collision.Windows[0].Panes = append([]sessionstate.Pane(nil), stamped.Windows[0].Panes...)
	second := collision.Windows[0].Panes[0]
	second.Index = 1
	second.RuntimeID, second.RegistryUID = "%2", "pan-second"
	second.Metadata = &sessionstate.ResourceMetadata{UID: "pan-second", Name: "pan-second", OwnerKind: string(KindAgent), OwnerUID: "agt-second", RegistrySchemaVersion: SchemaVersion}
	second.AgentMetadata = &sessionstate.ResourceMetadata{UID: "agt-second", Name: "Explicit-Reviewer", OwnerKind: string(KindWindow), OwnerUID: window.Metadata.UID, RegistrySchemaVersion: SchemaVersion}
	collision.Windows[0].Panes = append(collision.Windows[0].Panes, second)
	targetBefore := target.Clone()
	if _, err := PlanSnapshotProjection(target, created.Project.Metadata.UID, collision, fixedNow.Add(time.Hour), sequentialUIDs()); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("current Agent collision error=%v, want ErrNameConflict", err)
	}
	if !reflect.DeepEqual(targetBefore, target) {
		t.Fatal("current Agent collision mutated target Registry")
	}
}

func windowByRuntime(t *testing.T, reg Registry, runtimeID string) Window {
	t.Helper()
	for _, window := range reg.Windows {
		if window.Status.RuntimeID == runtimeID {
			return window
		}
	}
	t.Fatalf("Window runtime %q not found", runtimeID)
	return Window{}
}

func paneByRuntime(t *testing.T, reg Registry, runtimeID string) Pane {
	t.Helper()
	for _, pane := range reg.Panes {
		if pane.Status.Activation.RuntimeID == runtimeID {
			return pane
		}
	}
	t.Fatalf("Pane runtime %q not found", runtimeID)
	return Pane{}
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
					legacy.Windows[wi].Panes[pi].AgentMetadata = nil
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
