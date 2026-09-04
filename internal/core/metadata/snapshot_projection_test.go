package metadata

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

type projectionRefsGolden struct {
	Case         string `json:"case"`
	WindowUID    string `json:"window_uid"`
	AnchorUID    string `json:"anchor_uid"`
	AnchorRole   string `json:"anchor_role"`
	DefaultUID   string `json:"default_shell_uid,omitempty"`
	DirectShells int    `json:"direct_shells"`
	Agents       int    `json:"agents"`
}

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
	reg.Windows = append(reg.Windows, Window{APIVersion: APIVersion, Kind: KindWindow, Metadata: ObjectMeta{UID: "win-ctl-home", Name: "home", CreatedAt: fixedNow, OwnerRef: &OwnerRef{Kind: KindControlSession, UID: "ctl-home"}}, Spec: WindowSpec{AnchorPaneRef: "pan-ctl-home"}})
	reg.Panes = append(reg.Panes, Pane{APIVersion: APIVersion, Kind: KindPane, Metadata: ObjectMeta{UID: "pan-ctl-home", Name: "shell", CreatedAt: fixedNow, OwnerRef: &OwnerRef{Kind: KindWindow, UID: "win-ctl-home"}}, Spec: PaneSpec{Role: PaneRoleShell, CWD: "/home/test", Command: "/bin/zsh"}})
	reg.NameReservations = append(reg.NameReservations,
		NameReservation{Kind: KindControlSession, Name: "home", UID: "ctl-home"},
		NameReservation{Scope: "ctl-home", Kind: KindWindow, Name: "home", UID: "win-ctl-home"},
		NameReservation{Scope: "ctl-home", Kind: KindPane, Name: "shell", UID: "pan-ctl-home"},
	)
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	snap := buildCurrentSnapshot(&reg, one.Project.Metadata.UID, "one")
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
			setSnapshotRegistrySchema(&snap, 3)
			snap.Windows[0].Metadata = nil
			for i := range snap.Windows[0].Panes {
				snap.Windows[0].Panes[i].Metadata = nil
				snap.Windows[0].Panes[i].AgentMetadata = nil
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

func setSnapshotRegistrySchema(snap *sessionstate.Snapshot, version int) {
	if snap.Metadata != nil {
		snap.Metadata.RegistrySchemaVersion = version
	}
	for wi := range snap.Windows {
		if snap.Windows[wi].Metadata != nil {
			snap.Windows[wi].Metadata.RegistrySchemaVersion = version
		}
		for pi := range snap.Windows[wi].Panes {
			if snap.Windows[wi].Panes[pi].Metadata != nil {
				snap.Windows[wi].Panes[pi].Metadata.RegistrySchemaVersion = version
			}
			if snap.Windows[wi].Panes[pi].AgentMetadata != nil {
				snap.Windows[wi].Panes[pi].AgentMetadata.RegistrySchemaVersion = version
			}
		}
	}
}

func TestSnapshotProjectionProvenanceScansEveryPresentBlock(t *testing.T) {
	t.Parallel()
	reg, targetUID, _, source := projectionFixture(t)
	if len(source.Windows) < 2 || len(source.Windows[0].Panes) == 0 || len(source.Windows[1].Panes) == 0 {
		t.Fatal("fixture requires two metadata-bearing Pane blocks")
	}
	currentChildOnly := cloneProjectionSnapshot(t, source)
	currentChildOnly.Metadata = nil
	if _, err := PlanSnapshotProjection(reg, targetUID, currentChildOnly, fixedNow.Add(time.Hour), sequentialUIDs()); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("child-only current provenance error=%v, want fail-closed ErrInvalidRegistry", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*sessionstate.Snapshot)
	}{
		{name: "missing Window metadata", mutate: func(s *sessionstate.Snapshot) { s.Windows[0].Metadata = nil }},
		{name: "missing Pane metadata", mutate: func(s *sessionstate.Snapshot) { s.Windows[0].Panes[0].Metadata = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			partial := cloneProjectionSnapshot(t, source)
			tc.mutate(&partial)
			if _, err := PlanSnapshotProjection(reg, targetUID, partial, fixedNow.Add(time.Hour), sequentialUIDs()); !errors.Is(err, ErrInvalidRegistry) && !errors.Is(err, sessionstate.ErrInvalidSnapshot) {
				t.Fatalf("partial current provenance error=%v, want fail-closed invalid snapshot/Registry", err)
			}
		})
	}

	mixed := cloneProjectionSnapshot(t, source)
	mixed.Metadata.RegistrySchemaVersion = 3
	if err := mixed.Validate(); err == nil || !strings.Contains(err.Error(), "mixed registry schema provenance") {
		t.Fatalf("mixed provenance validation error=%v", err)
	}

	newerChildOnly := cloneProjectionSnapshot(t, source)
	setSnapshotRegistrySchema(&newerChildOnly, SchemaVersion+1)
	newerChildOnly.Metadata = nil
	if _, err := PlanSnapshotProjection(reg, targetUID, newerChildOnly, fixedNow.Add(time.Hour), sequentialUIDs()); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("child-only newer provenance error=%v, want ErrSchemaTooNew", err)
	}
}

func TestV3SnapshotCanonicalizationReceiptIsSortedFixedPointAndByteIdentical(t *testing.T) {
	t.Parallel()
	m := testMutator(dirSet{"/src/receipt": true})
	reg := NewRegistry()
	created, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: "/src/receipt", SessionName: "receipt", DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	projectUID := created.Project.Metadata.UID
	window, _ := reg.Window(created.Project.Spec.PrimaryWindowRef)
	meta := func(uid, name string) *sessionstate.ResourceMetadata {
		return &sessionstate.ResourceMetadata{UID: uid, Name: name, OwnerKind: string(KindWindow), OwnerUID: window.Metadata.UID, RegistrySchemaVersion: 3}
	}
	snap := sessionstate.Snapshot{Version: sessionstate.Version, Session: "receipt", SavedAt: fixedNow,
		Metadata: &sessionstate.ResourceMetadata{UID: projectUID, Name: created.Project.Metadata.Name, RegistrySchemaVersion: 3},
		Windows: []sessionstate.Window{{Index: 0, Name: "ignored-display", ActivePaneIndex: 0,
			Metadata: &sessionstate.ResourceMetadata{UID: window.Metadata.UID, Name: window.Metadata.Name, OwnerKind: string(KindProject), OwnerUID: projectUID, RegistrySchemaVersion: 3},
			Panes: []sessionstate.Pane{
				{Index: 0, CWD: "/src/receipt", Metadata: meta("pan-a", "pan-a"), Recipe: sessionstate.ShellRecipe()},
				{Index: 1, CWD: "/src/receipt", Metadata: meta("pan-b", "pan-a"), Recipe: sessionstate.ShellRecipe()},
				{Index: 2, CWD: "/src/receipt", Metadata: meta("pan-c", "pan-b"), Recipe: sessionstate.ShellRecipe()},
				{Index: 3, CWD: "/src/receipt", Metadata: meta("pan-d", "pan-c"), Recipe: sessionstate.ShellRecipe()},
				{Index: 4, CWD: "/src/receipt", Metadata: meta("pan-unique", "keep-me"), Recipe: sessionstate.ShellRecipe()},
			}}},
	}
	plan, err := PlanSnapshotProjection(reg, projectUID, snap, fixedNow.Add(time.Hour), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := json.Marshal(plan.Migration.NameRepairs)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"rootOwnerUid":"` + projectUID + `","kind":"Pane","uid":"pan-a","oldName":"pan-a","newName":"pan-a","reason":"already-canonical"},{"rootOwnerUid":"` + projectUID + `","kind":"Pane","uid":"pan-b","oldName":"pan-a","newName":"pan-b","reason":"duplicate-group"},{"rootOwnerUid":"` + projectUID + `","kind":"Pane","uid":"pan-c","oldName":"pan-b","newName":"pan-c","reason":"destination-closure"},{"rootOwnerUid":"` + projectUID + `","kind":"Pane","uid":"pan-d","oldName":"pan-c","newName":"pan-d","reason":"destination-closure"}]`
	if string(bytes) != want {
		t.Fatalf("receipt bytes=%s\nwant=%s", bytes, want)
	}
	unique, _ := plan.Desired.Pane("pan-unique")
	if unique.Metadata.Name != "keep-me" {
		t.Fatalf("set-outside unique name=%q", unique.Metadata.Name)
	}
	repeat, err := PlanSnapshotProjection(plan.Desired, projectUID, snap, fixedNow.Add(2*time.Hour), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}
	repeatBytes, _ := json.Marshal(repeat.Migration.NameRepairs)
	if repeat.Changed || !reflect.DeepEqual(plan.Desired, repeat.Desired) || !reflect.DeepEqual(bytes, repeatBytes) {
		t.Fatalf("repeat changed=%t receipt=%s", repeat.Changed, repeatBytes)
	}
}

func TestSnapshotAutomaticUIDChecksNamesProjectedEarlierInSameSnapshot(t *testing.T) {
	t.Parallel()
	m := testMutator(dirSet{"/src/mint": true})
	reg := NewRegistry()
	created, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: "/src/mint", SessionName: "mint", DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	window, _ := reg.Window(created.Project.Spec.PrimaryWindowRef)
	snap := sessionstate.Snapshot{Version: sessionstate.Version, Session: "mint", SavedAt: fixedNow,
		Metadata: &sessionstate.ResourceMetadata{UID: created.Project.Metadata.UID, Name: created.Project.Metadata.Name, RegistrySchemaVersion: 3},
		Windows: []sessionstate.Window{{Index: 0, ActivePaneIndex: 0,
			Metadata: &sessionstate.ResourceMetadata{UID: window.Metadata.UID, Name: window.Metadata.Name, OwnerKind: string(KindProject), OwnerUID: created.Project.Metadata.UID, RegistrySchemaVersion: 3},
			Panes: []sessionstate.Pane{
				{Index: 0, CWD: "/src/mint", Metadata: &sessionstate.ResourceMetadata{UID: "pan-explicit", Name: "pan-candidate", OwnerKind: string(KindWindow), OwnerUID: window.Metadata.UID, RegistrySchemaVersion: 3}, Recipe: sessionstate.ShellRecipe()},
				{Index: 1, CWD: "/src/mint", Recipe: sessionstate.ShellRecipe()},
				{Index: 2, CWD: "/src/mint", Recipe: sessionstate.ShellRecipe()},
			}}},
	}
	candidates := []string{"pan-candidate", "pan-generated"}
	calls := 0
	plan, err := PlanSnapshotProjection(reg, created.Project.Metadata.UID, snap, fixedNow.Add(time.Hour), func(Kind) (string, error) {
		uid := candidates[calls]
		calls++
		return uid, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("uid calls=%d, want collision remint", calls)
	}
	generated, ok := plan.Desired.Pane("pan-generated")
	if !ok || generated.Metadata.Name != generated.Metadata.UID {
		t.Fatalf("generated Pane=%+v, want exact UID name", generated)
	}
	before := reg.Clone()
	if _, err := PlanSnapshotProjection(reg, created.Project.Metadata.UID, snap, fixedNow.Add(time.Hour), func(Kind) (string, error) {
		return "pan-candidate", nil
	}); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("exhaustion error=%v, want ErrInvalidRegistry", err)
	}
	if !reflect.DeepEqual(before, reg) {
		t.Fatal("UID exhaustion mutated Registry input")
	}
}

func TestV3SnapshotImportCanonicalizesRootWideCollisionAndCurrentV4Refuses(t *testing.T) {
	t.Parallel()

	reg, targetUID, _, source := projectionFixture(t)
	if len(source.Windows) < 2 || len(source.Windows[0].Panes) < 2 || len(source.Windows[1].Panes) == 0 {
		t.Fatal("fixture requires two Windows, two Panes in the first, and one Pane in the second")
	}
	first := source.Windows[0].Panes[0].Metadata
	second := source.Windows[1].Panes[0].Metadata
	unique := source.Windows[0].Panes[1].Metadata
	if first == nil || second == nil || unique == nil {
		t.Fatal("fixture requires metadata-bearing Panes")
	}
	uniqueName := unique.Name
	first.Name = "v3-snapshot-duplicate"
	second.Name = "v3-snapshot-duplicate"
	setSnapshotRegistrySchema(&source, 3)
	registryBefore := reg.Clone()
	snapshotBefore := cloneProjectionSnapshot(t, source)

	plan, err := PlanSnapshotProjection(reg, targetUID, source, fixedNow.Add(time.Hour), sequentialUIDs())
	if err != nil {
		t.Fatalf("v3 snapshot import: %v", err)
	}
	for _, meta := range []*sessionstate.ResourceMetadata{first, second} {
		pane, ok := plan.Desired.Pane(meta.UID)
		if !ok || pane.Metadata.Name != pane.Metadata.UID {
			t.Fatalf("canonicalized Pane %q = %+v, want exact uid name", meta.UID, pane.Metadata)
		}
	}
	uniquePane, ok := plan.Desired.Pane(unique.UID)
	if !ok || uniquePane.Metadata.Name != uniqueName {
		t.Fatalf("set-outside unique Pane name=%q, want preserved %q", uniquePane.Metadata.Name, uniqueName)
	}
	if !reflect.DeepEqual(registryBefore, reg) || !reflect.DeepEqual(snapshotBefore, source) {
		t.Fatal("v3 snapshot import mutated an input")
	}
	repeat, err := PlanSnapshotProjection(plan.Desired, targetUID, source, fixedNow.Add(2*time.Hour), sequentialUIDs())
	if err != nil {
		t.Fatalf("repeat v3 snapshot import: %v", err)
	}
	if repeat.Changed || !reflect.DeepEqual(repeat.Desired, plan.Desired) {
		t.Fatal("repeat v3 snapshot import did not reach an exact Registry fixed point")
	}

	current := cloneProjectionSnapshot(t, source)
	setSnapshotRegistrySchema(&current, SchemaVersion)
	currentBefore := cloneProjectionSnapshot(t, current)
	if _, err := PlanSnapshotProjection(reg, targetUID, current, fixedNow.Add(time.Hour), sequentialUIDs()); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("current-v4 collision error=%v, want ErrNameConflict", err)
	}
	if !reflect.DeepEqual(registryBefore, reg) || !reflect.DeepEqual(currentBefore, current) {
		t.Fatal("current-v4 snapshot collision mutated an input")
	}
}

func TestSnapshotProjectionMetadataUIDsTakePriorityOverPosition(t *testing.T) {
	reg, targetUID, _, snap := projectionFixture(t)
	if len(snap.Windows) < 2 || len(snap.Windows[0].Panes) < 2 {
		t.Fatal("fixture requires two Windows and two Panes in the first Window")
	}
	snap.Windows[0].Panes[0], snap.Windows[0].Panes[1] = snap.Windows[0].Panes[1], snap.Windows[0].Panes[0]
	snap.Windows[0], snap.Windows[1] = snap.Windows[1], snap.Windows[0]

	plan, err := PlanSnapshotProjection(reg, targetUID, snap, fixedNow.Add(time.Minute), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}
	windows := plan.Desired.WindowsOf(targetUID)
	if len(windows) != len(snap.Windows) {
		t.Fatalf("projected Windows=%d, want %d", len(windows), len(snap.Windows))
	}
	for wi, snapshotWindow := range snap.Windows {
		if got, want := windows[wi].Metadata.UID, snapshotWindow.Metadata.UID; got != want {
			t.Fatalf("Window %d uid=%q, want metadata uid %q", wi, got, want)
		}
		panes := plan.Desired.snapshotPanesOf(gotUID(snapshotWindow.Metadata))
		if len(panes) != len(snapshotWindow.Panes) {
			t.Fatalf("Window %q projected Panes=%d, want %d", gotUID(snapshotWindow.Metadata), len(panes), len(snapshotWindow.Panes))
		}
		for pi, snapshotPane := range snapshotWindow.Panes {
			if got, want := panes[pi].Metadata.UID, gotUID(snapshotPane.Metadata); got != want {
				t.Fatalf("Window %d Pane %d uid=%q, want metadata uid %q", wi, pi, got, want)
			}
		}
	}
}

func TestSnapshotProjectionPreservesAgentOwnerChainAtExactFixedPoint(t *testing.T) {
	reg, targetUID, _, snap := projectionFixture(t)
	plan, err := PlanSnapshotProjection(reg, targetUID, snap, fixedNow.Add(time.Minute), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, snapshotWindow := range snap.Windows {
		for _, snapshotPane := range snapshotWindow.Panes {
			if snapshotPane.Recipe.Kind != sessionstate.RecipeKindAgent {
				continue
			}
			found = true
			paneUID := gotUID(snapshotPane.Metadata)
			agentUID := snapshotPane.Metadata.OwnerUID
			pane, ok := plan.Desired.Pane(paneUID)
			if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerUID() != agentUID {
				t.Fatalf("projected Agent Pane owner chain=%+v, want Agent %q", pane.Metadata.OwnerRef, agentUID)
			}
			agent, ok := plan.Desired.Agent(agentUID)
			if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != KindWindow || agent.Metadata.OwnerUID() != gotUID(snapshotWindow.Metadata) {
				t.Fatalf("projected Agent owner chain=%+v, want Window %q", agent.Metadata.OwnerRef, gotUID(snapshotWindow.Metadata))
			}
			if agent.Status.PaneRef != paneUID {
				t.Fatalf("projected Agent paneRef=%q, want %q", agent.Status.PaneRef, paneUID)
			}
		}
	}
	if !found {
		t.Fatal("fixture requires an Agent Pane")
	}

	repeat, err := PlanSnapshotProjection(plan.Desired, targetUID, snap, fixedNow.Add(2*time.Minute), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Changed || !reflect.DeepEqual(repeat.Desired, plan.Desired) {
		t.Fatal("owner-chain projection did not reach an exact desired Registry fixed point")
	}
}

func gotUID(meta *sessionstate.ResourceMetadata) string {
	if meta == nil {
		return ""
	}
	return meta.UID
}

func TestSnapshotProjectionFinalV2RefsGolden(t *testing.T) {
	reg, targetUID, _, snap := projectionFixture(t)
	project, _ := reg.Project(targetUID)
	window, _ := reg.Window(project.Spec.PrimaryWindowRef)
	agents := reg.AgentsOf(window.Metadata.UID)
	if len(agents) != 1 {
		t.Fatalf("fixture Agents=%d, want 1", len(agents))
	}
	agentPane, _ := reg.Pane(agents[0].Status.PaneRef)
	window.Spec.AnchorPaneRef = agentPane.Metadata.UID
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	snap = buildCurrentSnapshot(&reg, targetUID, "one")
	for wi := range snap.Windows {
		for pi := range snap.Windows[wi].Panes {
			if snap.Windows[wi].Panes[pi].Metadata != nil && snap.Windows[wi].Panes[pi].Metadata.OwnerKind == string(KindAgent) {
				snap.Windows[wi].Panes[pi].Recipe = sessionstate.AgentRecipe("codex", "projection-thread", "projection")
			}
		}
	}
	sourceBytes, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}

	projected, err := PlanSnapshotProjection(reg, targetUID, snap, fixedNow.Add(time.Minute), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}

	agentOnly := cloneProjectionSnapshot(t, snap)
	for wi := range agentOnly.Windows {
		if agentOnly.Windows[wi].Metadata == nil || agentOnly.Windows[wi].Metadata.UID != window.Metadata.UID {
			continue
		}
		panes := agentOnly.Windows[wi].Panes[:0]
		for _, pane := range agentOnly.Windows[wi].Panes {
			if pane.Recipe.Kind == sessionstate.RecipeKindAgent {
				panes = append(panes, pane)
			}
		}
		agentOnly.Windows[wi].Panes = panes
		agentOnly.Windows[wi].ActivePaneIndex = panes[0].Index
		agentOnly.Windows = agentOnly.Windows[wi : wi+1]
		break
	}
	agentOnlyPlan, err := PlanSnapshotProjection(reg, targetUID, agentOnly, fixedNow.Add(2*time.Minute), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}
	repeatAgentOnly, err := PlanSnapshotProjection(agentOnlyPlan.Desired, targetUID, agentOnly, fixedNow.Add(3*time.Minute), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}
	if repeatAgentOnly.Changed || !reflect.DeepEqual(repeatAgentOnly.Desired, agentOnlyPlan.Desired) {
		t.Fatal("metadata-bearing Agent-only projection was not a fixed point")
	}

	legacyAgentOnly := cloneProjectionSnapshot(t, agentOnly)
	legacyAgentOnly.Metadata = nil
	legacyAgentOnly.Windows[0].Metadata = nil
	legacyAgentOnly.Windows[0].Panes[0].Metadata = nil
	legacyAgentOnly.Windows[0].Panes[0].AgentMetadata = nil
	legacyPlan, err := PlanSnapshotProjection(reg, targetUID, legacyAgentOnly, fixedNow.Add(4*time.Minute), sequentialUIDs())
	if err != nil {
		t.Fatal(err)
	}

	if got, err := json.Marshal(snap); err != nil || !reflect.DeepEqual(got, sourceBytes) {
		t.Fatal("projection rewrote source snapshot bytes")
	}

	rows := []projectionRefsGolden{
		projectionGoldenRow(t, "metadata mixed preserves Agent anchor", projected.Desired, targetUID),
		projectionGoldenRow(t, "metadata Agent-only keeps empty default", agentOnlyPlan.Desired, targetUID),
		projectionGoldenRow(t, "legacy Agent-only uses first Pane", legacyPlan.Desired, targetUID),
	}
	got, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile(filepath.Join("testdata", "snapshot-projection-final-v2.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection golden drifted:\n%s", got)
	}
}

func projectionGoldenRow(t *testing.T, name string, registry Registry, projectUID string) projectionRefsGolden {
	t.Helper()
	project, ok := registry.Project(projectUID)
	if !ok {
		t.Fatalf("%s: Project missing", name)
	}
	window, ok := registry.Window(project.Spec.PrimaryWindowRef)
	if !ok {
		t.Fatalf("%s: primary Window missing", name)
	}
	anchor, ok := registry.WindowAnchor(window.Metadata.UID)
	if !ok {
		t.Fatalf("%s: anchor missing", name)
	}
	return projectionRefsGolden{
		Case: name, WindowUID: window.Metadata.UID, AnchorUID: anchor.Metadata.UID,
		AnchorRole: string(anchor.Spec.Role), DefaultUID: window.Spec.DefaultShellPaneRef,
		DirectShells: len(registry.PanesOf(window.Metadata.UID)), Agents: len(registry.AgentsOf(window.Metadata.UID)),
	}
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
				legacy.Windows[wi].Panes[pi].AgentMetadata = nil
			}
		}
		if _, err := PlanSnapshotProjection(reg, targetUID, legacy, fixedNow, sequentialUIDs()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("legacy positional assigns exact UID names to new descendants", func(t *testing.T) {
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
		for _, window := range windows[len(windows)-2:] {
			if window.Metadata.Name != window.Metadata.UID {
				t.Fatalf("new Window name=%q, want exact uid %q", window.Metadata.Name, window.Metadata.UID)
			}
		}
		lastPanes := plan.Desired.PanesOf(windows[len(windows)-1].Metadata.UID)
		for i, pane := range lastPanes {
			if pane.Metadata.Name != pane.Metadata.UID {
				t.Fatalf("new Pane %d name=%q, want exact uid %q", i, pane.Metadata.Name, pane.Metadata.UID)
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
				pane.Metadata = &sessionstate.ResourceMetadata{UID: "pan-snapshot-cross-window", Name: "agent", OwnerKind: string(KindAgent), OwnerUID: otherAgent.Metadata.UID, RegistrySchemaVersion: SchemaVersion}
				pane.AgentMetadata = &sessionstate.ResourceMetadata{UID: otherAgent.Metadata.UID, Name: otherAgent.Metadata.Name, OwnerKind: string(KindWindow), OwnerUID: windows[1].Metadata.UID, RegistrySchemaVersion: SchemaVersion}
				found = true
			}
		}
		if !found {
			t.Fatal("fixture requires an Agent Pane")
		}
		if _, err := PlanSnapshotProjection(candidateRegistry, targetUID, bad, fixedNow, sequentialUIDs()); err == nil || (!strings.Contains(err.Error(), "containing Window") && !strings.Contains(err.Error(), "owner chain")) {
			t.Fatalf("error=%v, want Agent owner Window refusal", err)
		}
	})
}
