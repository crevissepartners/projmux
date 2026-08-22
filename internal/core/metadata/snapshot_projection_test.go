package metadata

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

func cloneProjectionSnapshot(t *testing.T, in sessionstate.Snapshot) sessionstate.Snapshot {
	t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out sessionstate.Snapshot
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func projectionFixture(t *testing.T) (Registry, string, string, sessionstate.Snapshot) {
	t.Helper()
	m := testMutator(dirSet{"/src/one": true, "/src/two": true})
	reg := NewRegistry()
	one, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: "/src/one", SessionName: "one", DefaultShell: "/bin/zsh", Topology: []BootstrapWindow{{Name: "main", Panes: []BootstrapPane{{Name: "shell"}, {Name: "logs"}}}, {Name: "review"}}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: "/src/two", SessionName: "two", DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	primary, _ := reg.Window(one.Project.Spec.PrimaryWindowRef)
	agent, err := m.CreateAgent(&reg, primary.Metadata.UID, CreateAgentOptions{Provider: "codex", Workspace: AgentWorkspace{CWD: "/src/one"}, OperationID: "projection-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{CWD: "/src/one"}, "projection-agent-pane"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.RecordAgentSessionRef(&reg, agent.Metadata.UID, AgentSessionObservation{Provider: "codex", ThreadID: "projection-thread"}); err != nil {
		t.Fatal(err)
	}
	reg.ControlSessions = append(reg.ControlSessions, ControlSession{APIVersion: APIVersion, Kind: KindControlSession, Metadata: ObjectMeta{UID: "ctl-home", Name: "home", CreatedAt: fixedNow}, Spec: ControlSessionSpec{Session: "home"}})
	reg.Windows = append(reg.Windows, Window{APIVersion: APIVersion, Kind: KindWindow, Metadata: ObjectMeta{UID: "win-ctl-home", Name: "home", CreatedAt: fixedNow, OwnerRef: &OwnerRef{Kind: KindControlSession, UID: "ctl-home"}}, Spec: WindowSpec{PrimaryPaneRef: "pan-ctl-home"}})
	reg.Panes = append(reg.Panes, Pane{APIVersion: APIVersion, Kind: KindPane, Metadata: ObjectMeta{UID: "pan-ctl-home", Name: "shell", CreatedAt: fixedNow, OwnerRef: &OwnerRef{Kind: KindWindow, UID: "win-ctl-home"}}, Spec: PaneSpec{Role: PaneRoleShell, CWD: "/home/test", Command: "/bin/zsh"}})
	reg.NameReservations = append(reg.NameReservations,
		NameReservation{Kind: KindControlSession, Name: "home", UID: "ctl-home"},
		NameReservation{Scope: "ctl-home", Kind: KindWindow, Name: "home", UID: "win-ctl-home"},
		NameReservation{Scope: "win-ctl-home", Kind: KindPane, Name: "shell", UID: "pan-ctl-home"},
	)
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	snap := buildSnapshot(&reg, one.Project.Metadata.UID, "one")
	if err := AttachSnapshotMetadata(&reg, one.Project.Metadata.UID, &snap); err != nil {
		t.Fatal(err)
	}
	for wi := range snap.Windows {
		for pi := range snap.Windows[wi].Panes {
			if snap.Windows[wi].Panes[pi].Metadata != nil && snap.Windows[wi].Panes[pi].Metadata.OwnerKind == string(KindAgent) {
				snap.Windows[wi].Panes[pi].Recipe = sessionstate.AgentRecipe("codex", "projection-thread", "projection")
			}
		}
	}
	return reg, one.Project.Metadata.UID, two.Project.Metadata.UID, snap
}

type projectionControlGraph struct {
	Control      ControlSession
	Windows      []Window
	Panes        []Pane
	Reservations []NameReservation
}

func controlGraph(r Registry) projectionControlGraph {
	control, _ := r.ControlSession("ctl-home")
	graph := projectionControlGraph{Control: control.Clone(), Windows: r.WindowsOf("ctl-home"), Panes: r.PanesOf("win-ctl-home")}
	for _, reservation := range r.NameReservations {
		if reservation.UID == "ctl-home" || reservation.UID == "win-ctl-home" || reservation.UID == "pan-ctl-home" || reservation.Scope == "ctl-home" || reservation.Scope == "win-ctl-home" {
			graph.Reservations = append(graph.Reservations, reservation)
		}
	}
	return graph
}

func FuzzSnapshotProjectionIsScopedValidAndIdempotent(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Add(uint8(2))
	f.Fuzz(func(t *testing.T, trim uint8) {
		reg, targetUID, unrelatedUID, snap := projectionFixture(t)
		if len(snap.Windows) > 0 && trim%3 == 1 {
			snap.Windows = snap.Windows[:1]
		}
		if len(snap.Windows) > 0 && trim%3 == 2 {
			snap.Windows[0].Metadata = nil
			for i := range snap.Windows[0].Panes {
				snap.Windows[0].Panes[i].Metadata = nil
			}
		}
		unrelatedProject, _ := reg.Project(unrelatedUID)
		targetProject, _ := reg.Project(targetUID)
		controlBefore := controlGraph(reg)
		unrelatedWindows := reg.WindowsOf(unrelatedUID)
		unrelatedPanes := reg.projectPanes(unrelatedUID)
		registryBefore := reg.Clone()
		snapshotBefore := cloneProjectionSnapshot(t, snap)
		plan, err := PlanSnapshotProjection(reg, targetUID, snap, fixedNow.Add(1), sequentialUIDs())
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Desired.Validate(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(registryBefore, reg) || !reflect.DeepEqual(snapshotBefore, snap) {
			t.Fatal("pure snapshot projection mutated an input")
		}
		afterTarget, _ := plan.Desired.Project(targetUID)
		if !reflect.DeepEqual(targetProject.Clone(), afterTarget.Clone()) {
			t.Fatal("snapshot projection changed target Project uid/root/metadata/trust/session identity")
		}
		afterProject, _ := plan.Desired.Project(unrelatedUID)
		if !reflect.DeepEqual(unrelatedProject.Clone(), afterProject.Clone()) || !reflect.DeepEqual(unrelatedWindows, plan.Desired.WindowsOf(unrelatedUID)) || !reflect.DeepEqual(unrelatedPanes, plan.Desired.projectPanes(unrelatedUID)) {
			t.Fatal("snapshot projection changed unrelated Project graph")
		}
		if !reflect.DeepEqual(controlBefore, controlGraph(plan.Desired)) {
			t.Fatal("snapshot projection changed unrelated ControlSession graph or reservations")
		}
		repeat, err := PlanSnapshotProjection(plan.Desired, targetUID, snap, fixedNow.Add(2), sequentialUIDs())
		if err != nil {
			t.Fatal(err)
		}
		if repeat.Changed || !reflect.DeepEqual(repeat.Desired, plan.Desired) {
			t.Fatal("second snapshot projection was not a Registry zero-diff")
		}
	})
}

func FuzzOpenFreshKeepsOnlyCanonicalProjectShell(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(3))
	f.Fuzz(func(t *testing.T, shape uint8) {
		reg, targetUID, unrelatedUID, _ := projectionFixture(t)
		project, _ := reg.Project(targetUID)
		anchorWindow := project.Spec.PrimaryWindowRef
		window, _ := reg.Window(anchorWindow)
		anchorPane := window.Spec.PrimaryPaneRef
		pane, _ := reg.Pane(anchorPane)
		pane.Spec.Command = fmt.Sprintf("printf fresh-shape-%d", shape)
		counts := map[Kind]int{}
		vary := Mutator{
			Now: func() time.Time { return fixedNow },
			NewUID: func(kind Kind) (string, error) {
				counts[kind]++
				return fmt.Sprintf("fuzz-%s-%d", strings.ToLower(string(kind)), counts[kind]), nil
			},
			DirExists: dirSet{"/src/one": true, "/src/two": true}.exists,
		}
		for i := 0; i < int(shape&3); i++ {
			if _, _, err := vary.AddWindow(&reg, targetUID, BootstrapWindow{Name: fmt.Sprintf("extra-%d", i), Panes: []BootstrapPane{{CWD: "/src/one"}, {CWD: "/src/one/logs"}}}, "/bin/zsh", "fuzz-window"); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < int((shape>>2)&3); i++ {
			agent, err := vary.CreateAgent(&reg, anchorWindow, CreateAgentOptions{Name: fmt.Sprintf("fuzz-agent-%d", i), Provider: "codex", Workspace: AgentWorkspace{CWD: "/src/one"}, OperationID: "fuzz-agent"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := vary.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{CWD: "/src/one"}, "fuzz-agent-pane"); err != nil {
				t.Fatal(err)
			}
			if shape&0x40 != 0 {
				if _, _, err := vary.RecordAgentSessionRef(&reg, agent.Metadata.UID, AgentSessionObservation{Provider: "codex", ThreadID: fmt.Sprintf("thread-%d", i)}); err != nil {
					t.Fatal(err)
				}
			}
		}
		unrelated, _ := reg.Project(unrelatedUID)
		controlBefore := controlGraph(reg)
		targetBefore := project.Clone()
		registryBefore := reg.Clone()
		plan, err := PlanOpenFresh(reg, targetUID, fixedNow.Add(1))
		if err != nil {
			t.Fatal(err)
		}
		windows := plan.Desired.WindowsOf(targetUID)
		if len(windows) != 1 || windows[0].Metadata.UID != anchorWindow {
			t.Fatalf("fresh windows = %+v", windows)
		}
		panes := plan.Desired.PanesOf(anchorWindow)
		if len(panes) != 1 || panes[0].Metadata.UID != anchorPane || panes[0].Spec.Role != PaneRoleShell {
			t.Fatalf("fresh panes = %+v", panes)
		}
		originalAnchorPane, _ := reg.Pane(anchorPane)
		if !reflect.DeepEqual(originalAnchorPane.Clone(), panes[0].Clone()) {
			t.Fatal("fresh changed the canonical shell Pane recipe or metadata")
		}
		if len(plan.Desired.AgentsOf(anchorWindow)) != 0 {
			t.Fatal("fresh retained Agent")
		}
		if !reflect.DeepEqual(registryBefore, reg) {
			t.Fatal("pure Open fresh plan mutated its Registry input")
		}
		targetAfter, _ := plan.Desired.Project(targetUID)
		if !reflect.DeepEqual(targetBefore, targetAfter.Clone()) {
			t.Fatal("fresh changed target Project uid/root/metadata/trust/session identity")
		}
		afterUnrelated, _ := plan.Desired.Project(unrelatedUID)
		if !reflect.DeepEqual(unrelated.Clone(), afterUnrelated.Clone()) {
			t.Fatal("fresh changed unrelated Project")
		}
		if !reflect.DeepEqual(controlBefore, controlGraph(plan.Desired)) {
			t.Fatal("fresh changed unrelated ControlSession graph or reservations")
		}
		repeat, err := PlanOpenFresh(plan.Desired, targetUID, fixedNow.Add(2))
		if err != nil {
			t.Fatal(err)
		}
		if repeat.Changed || !reflect.DeepEqual(repeat.Desired, plan.Desired) {
			t.Fatal("second Open fresh was not a Registry zero-diff")
		}
	})
}

func TestSnapshotProjectionConversationPointerLossTable(t *testing.T) {
	reg, targetUID, _, snap := projectionFixture(t)
	var agentPane *sessionstate.Pane
	for wi := range snap.Windows {
		for pi := range snap.Windows[wi].Panes {
			if snap.Windows[wi].Panes[pi].Recipe.Kind == sessionstate.RecipeKindAgent {
				agentPane = &snap.Windows[wi].Panes[pi]
			}
		}
	}
	if agentPane == nil || agentPane.Recipe.ResumeID != "projection-thread" {
		t.Fatalf("fixture Agent recipe=%+v", agentPane)
	}
	for _, tc := range []struct {
		name     string
		resumeID string
		wantLoss int
	}{
		{name: "same pointer", resumeID: "projection-thread", wantLoss: 0},
		{name: "different pointer", resumeID: "different-thread", wantLoss: 1},
		{name: "absent pointer", resumeID: "", wantLoss: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cloneProjectionSnapshot(t, snap)
			for wi := range candidate.Windows {
				for pi := range candidate.Windows[wi].Panes {
					if candidate.Windows[wi].Panes[pi].Recipe.Kind == sessionstate.RecipeKindAgent {
						candidate.Windows[wi].Panes[pi].Recipe.ResumeID = tc.resumeID
					}
				}
			}
			plan, err := PlanSnapshotProjection(reg, targetUID, candidate, fixedNow.Add(time.Hour), sequentialUIDs())
			if err != nil {
				t.Fatal(err)
			}
			if plan.LostSessionRefs != tc.wantLoss {
				t.Fatalf("LostSessionRefs=%d, want %d", plan.LostSessionRefs, tc.wantLoss)
			}
		})
	}
}

func TestSnapshotProjectionMetadataLegacyAndCollisionTable(t *testing.T) {
	reg, targetUID, unrelatedUID, snap := projectionFixture(t)
	t.Run("metadata uid", func(t *testing.T) {
		if _, err := PlanSnapshotProjection(reg, targetUID, snap, fixedNow, sequentialUIDs()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("legacy positional", func(t *testing.T) {
		legacy := cloneProjectionSnapshot(t, snap)
		legacy.Metadata = nil
		for wi := range legacy.Windows {
			legacy.Windows[wi].Metadata = nil
			for pi := range legacy.Windows[wi].Panes {
				legacy.Windows[wi].Panes[pi].Metadata = nil
			}
		}
		if _, err := PlanSnapshotProjection(reg, targetUID, legacy, fixedNow, sequentialUIDs()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("legacy positional allocates duplicate names for new descendants", func(t *testing.T) {
		legacy := sessionstate.Snapshot{
			Version: sessionstate.Version,
			Session: "legacy-duplicates",
			SavedAt: fixedNow,
		}
		for wi := 0; wi < len(reg.WindowsOf(targetUID))+2; wi++ {
			window := sessionstate.Window{Index: wi, Name: "duplicate", ActivePaneIndex: 0}
			for pi := range 4 {
				window.Panes = append(window.Panes, sessionstate.Pane{Index: pi, Label: "duplicate", CWD: "/src/one", Recipe: sessionstate.ShellRecipe()})
			}
			legacy.Windows = append(legacy.Windows, window)
		}
		plan, err := PlanSnapshotProjection(reg, targetUID, legacy, fixedNow, sequentialUIDs())
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Desired.Validate(); err != nil {
			t.Fatal(err)
		}
		windows := plan.Desired.WindowsOf(targetUID)
		if got, want := windows[len(windows)-2].Metadata.Name, "duplicate"; got != want {
			t.Fatalf("first new Window name=%q, want %q", got, want)
		}
		if got, want := windows[len(windows)-1].Metadata.Name, "duplicate-1"; got != want {
			t.Fatalf("second new Window name=%q, want %q", got, want)
		}
		lastPanes := plan.Desired.PanesOf(windows[len(windows)-1].Metadata.UID)
		for i, want := range []string{"duplicate", "duplicate-1", "duplicate-2", "duplicate-3"} {
			if got := lastPanes[i].Metadata.Name; got != want {
				t.Fatalf("new Pane %d name=%q, want %q", i, got, want)
			}
		}
	})
	t.Run("project mismatch", func(t *testing.T) {
		bad := cloneProjectionSnapshot(t, snap)
		bad.Metadata = &sessionstate.ResourceMetadata{UID: unrelatedUID, Name: "two"}
		if _, err := PlanSnapshotProjection(reg, targetUID, bad, fixedNow, sequentialUIDs()); err == nil {
			t.Fatal("wanted collision refusal")
		}
	})
	t.Run("descendant collision", func(t *testing.T) {
		bad := cloneProjectionSnapshot(t, snap)
		other := reg.WindowsOf(unrelatedUID)[0]
		bad.Windows[0].Metadata = &sessionstate.ResourceMetadata{UID: other.Metadata.UID, Name: other.Metadata.Name, OwnerKind: string(KindProject), OwnerUID: targetUID}
		if _, err := PlanSnapshotProjection(reg, targetUID, bad, fixedNow, sequentialUIDs()); err == nil {
			t.Fatal("wanted uid collision refusal")
		}
	})
	t.Run("window owner mismatch", func(t *testing.T) {
		bad := cloneProjectionSnapshot(t, snap)
		meta := *bad.Windows[0].Metadata
		meta.OwnerUID = unrelatedUID
		bad.Windows[0].Metadata = &meta
		if _, err := PlanSnapshotProjection(reg, targetUID, bad, fixedNow, sequentialUIDs()); err == nil {
			t.Fatal("wanted Window owner refusal")
		}
	})
	t.Run("shell pane owner mismatch", func(t *testing.T) {
		bad := cloneProjectionSnapshot(t, snap)
		meta := *bad.Windows[0].Panes[0].Metadata
		meta.OwnerUID = "wrong-window"
		bad.Windows[0].Panes[0].Metadata = &meta
		if _, err := PlanSnapshotProjection(reg, targetUID, bad, fixedNow, sequentialUIDs()); err == nil {
			t.Fatal("wanted Pane owner refusal")
		}
	})
	t.Run("same target cross kind uid", func(t *testing.T) {
		bad := cloneProjectionSnapshot(t, snap)
		paneUID := bad.Windows[0].Panes[0].Metadata.UID
		meta := *bad.Windows[0].Metadata
		meta.UID = paneUID
		bad.Windows[0].Metadata = &meta
		if _, err := PlanSnapshotProjection(reg, targetUID, bad, fixedNow, sequentialUIDs()); err == nil {
			t.Fatal("wanted cross-kind uid refusal")
		}
	})
	t.Run("agent owner window mismatch", func(t *testing.T) {
		candidateRegistry := reg.Clone()
		windows := candidateRegistry.WindowsOf(targetUID)
		if len(windows) < 2 {
			t.Fatal("fixture requires two target Windows")
		}
		m := testMutator(dirSet{"/src/one": true, "/src/two": true})
		otherAgent, err := m.CreateAgent(&candidateRegistry, windows[1].Metadata.UID, CreateAgentOptions{Provider: "codex", Workspace: AgentWorkspace{CWD: "/src/one"}, OperationID: "other-window-agent"})
		if err != nil {
			t.Fatal(err)
		}
		bad := cloneProjectionSnapshot(t, snap)
		found := false
		for wi := range bad.Windows {
			for pi := range bad.Windows[wi].Panes {
				pane := &bad.Windows[wi].Panes[pi]
				if pane.Recipe.Kind != sessionstate.RecipeKindAgent {
					continue
				}
				pane.Metadata = &sessionstate.ResourceMetadata{UID: "pan-snapshot-cross-window", Name: "agent", OwnerKind: string(KindAgent), OwnerUID: otherAgent.Metadata.UID}
				found = true
			}
		}
		if !found {
			t.Fatal("fixture requires an Agent Pane")
		}
		if _, err := PlanSnapshotProjection(candidateRegistry, targetUID, bad, fixedNow, sequentialUIDs()); err == nil || !strings.Contains(err.Error(), "containing Window") {
			t.Fatalf("error=%v, want Agent owner Window refusal", err)
		}
	})
}
