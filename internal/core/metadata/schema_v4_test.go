package metadata

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func v3FixedPointFixture() Registry {
	owner := func(kind Kind, uid string) *OwnerRef { return &OwnerRef{Kind: kind, UID: uid} }
	reg := Registry{
		APIVersion: APIVersion, SchemaVersion: 3, UpdatedAt: fixedNow,
		Projects: []Project{{
			APIVersion: APIVersion, Kind: KindProject,
			Metadata: ObjectMeta{UID: "project-root", Name: "project", CreatedAt: fixedNow,
				removedDisplayName: removedPresentation{present: true, value: "private project title"}},
			Spec: ProjectSpec{Root: "/src/root", PrimaryWindowRef: "window-a"},
		}},
	}
	for _, item := range []struct {
		window string
		pane   string
		name   string
	}{
		{window: "window-a", pane: "pane-a", name: "duplicate"},
		{window: "window-b", pane: "pane-b", name: "duplicate"},
		{window: "window-c", pane: "pane-c", name: "pane-a"},
		{window: "window-d", pane: "pane-d", name: "pane-c"},
		{window: "window-e", pane: "pane-e", name: "unique"},
		{window: "window-f", pane: "pane-f", name: "pane-f"},
		{window: "window-g", pane: "pane-g", name: "pane-f"},
	} {
		reg.Windows = append(reg.Windows, Window{
			APIVersion: APIVersion, Kind: KindWindow,
			Metadata: ObjectMeta{UID: item.window, Name: item.window, OwnerRef: owner(KindProject, "project-root"), CreatedAt: fixedNow},
			Spec:     WindowSpec{AnchorPaneRef: item.pane, DefaultShellPaneRef: item.pane},
		})
		pane := Pane{
			APIVersion: APIVersion, Kind: KindPane,
			Metadata: ObjectMeta{UID: item.pane, Name: item.name, OwnerRef: owner(KindWindow, item.window), CreatedAt: fixedNow},
			Spec:     PaneSpec{Role: PaneRoleShell},
		}
		if item.pane == "pane-a" {
			pane.Status.removedDisplayTitle = removedPresentation{present: true, value: "private pane title"}
		}
		if item.pane == "pane-e" {
			pane.Status.removedDisplayTitle = removedPresentation{present: true}
		}
		reg.Panes = append(reg.Panes, pane)
	}
	reg.NameReservations = append(reg.NameReservations, NameReservation{Kind: KindProject, Name: "project", UID: "project-root"})
	for _, window := range reg.Windows {
		reg.NameReservations = append(reg.NameReservations, NameReservation{Scope: "project-root", Kind: KindWindow, Name: window.Metadata.Name, UID: window.Metadata.UID})
	}
	for _, pane := range reg.Panes {
		reg.NameReservations = append(reg.NameReservations, NameReservation{Scope: pane.Metadata.OwnerUID(), Kind: KindPane, Name: pane.Metadata.Name, UID: pane.Metadata.UID})
	}
	return reg
}

func TestV3ToV4CanonicalizationUsesDuplicateAllMembersAndDestinationFixedPoint(t *testing.T) {
	t.Parallel()

	source := v3FixedPointFixture()
	migrated, ran, report, err := MigrateRegistryWithEnvironment(nil, source, MigrationEnvironment{})
	if err != nil {
		t.Fatalf("migrate v3: %v", err)
	}
	if !ran || migrated.SchemaVersion != 4 {
		t.Fatalf("migration ran=%t schema=%d, want true/4", ran, migrated.SchemaVersion)
	}
	wantNames := map[string]string{
		"pane-a": "pane-a", "pane-b": "pane-b", "pane-c": "pane-c", "pane-d": "pane-d",
		"pane-e": "unique", "pane-f": "pane-f", "pane-g": "pane-g",
	}
	for _, pane := range migrated.Panes {
		if got := pane.Metadata.Name; got != wantNames[pane.Metadata.UID] {
			t.Fatalf("Pane %s name=%q, want %q", pane.Metadata.UID, got, wantNames[pane.Metadata.UID])
		}
	}
	wantRepairs := []MigrationNameRepair{
		{RootOwnerUID: "project-root", Kind: KindPane, UID: "pane-a", OldName: "duplicate", NewName: "pane-a", Reason: migrationReasonDuplicateGroup},
		{RootOwnerUID: "project-root", Kind: KindPane, UID: "pane-b", OldName: "duplicate", NewName: "pane-b", Reason: migrationReasonDuplicateGroup},
		{RootOwnerUID: "project-root", Kind: KindPane, UID: "pane-c", OldName: "pane-a", NewName: "pane-c", Reason: migrationReasonDestinationClosure},
		{RootOwnerUID: "project-root", Kind: KindPane, UID: "pane-d", OldName: "pane-c", NewName: "pane-d", Reason: migrationReasonDestinationClosure},
		{RootOwnerUID: "project-root", Kind: KindPane, UID: "pane-f", OldName: "pane-f", NewName: "pane-f", Reason: migrationReasonAlreadyCanonical},
		{RootOwnerUID: "project-root", Kind: KindPane, UID: "pane-g", OldName: "pane-f", NewName: "pane-g", Reason: migrationReasonDuplicateGroup},
	}
	if !reflect.DeepEqual(report.NameRepairs, wantRepairs) {
		t.Fatalf("name repairs = %#v, want %#v", report.NameRepairs, wantRepairs)
	}
	if len(report.FieldRemovals) != 3 || report.InformationLossCount() != 2 {
		t.Fatalf("field-removal receipt = %#v, loss=%d", report.FieldRemovals, report.InformationLossCount())
	}
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	foundEmpty := false
	for _, removal := range report.FieldRemovals {
		if removal.UID == "pane-e" && removal.Field == "status.displayTitle" {
			foundEmpty = removal.Present && removal.ByteLength == 0 && removal.SHA256 == emptySHA256 && !removal.InformationLoss
		}
	}
	if !foundEmpty {
		t.Fatalf("present-empty removal receipt missing sha256(empty): %#v", report.FieldRemovals)
	}
	if got := report.String(); strings.Contains(got, "private project title") || strings.Contains(got, "private pane title") {
		t.Fatalf("content-free report leaked removed presentation: %s", got)
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("migrated Registry invalid: %v", err)
	}
	golden, err := os.ReadFile("testdata/registry-v4-destination-closure.golden.json")
	if err != nil {
		t.Fatalf("read destination-closure golden: %v", err)
	}
	if got := mustJSON(t, migrated) + "\n"; got != string(golden) {
		t.Fatalf("destination-closure bytes differ from golden:\n--- got ---\n%s\n--- want ---\n%s", got, golden)
	}
	again, ranAgain, _, err := MigrateRegistryWithEnvironment(nil, migrated, MigrationEnvironment{})
	if err != nil || ranAgain || mustJSON(t, again) != mustJSON(t, migrated) {
		t.Fatalf("repeat migration changed bytes: ran=%t err=%v", ranAgain, err)
	}
}

func TestV3MigrationReconstructsReservationsFromGraphNotPersistedTable(t *testing.T) {
	t.Parallel()

	canonical := v3FixedPointFixture()
	want, _, _, err := MigrateRegistryWithEnvironment(nil, canonical, MigrationEnvironment{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name         string
		reservations []NameReservation
	}{
		{name: "missing table"},
		{name: "stale and dangling table", reservations: []NameReservation{{Scope: "not-a-root", Kind: KindPane, Name: "stale", UID: "missing"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := v3FixedPointFixture()
			source.NameReservations = test.reservations
			got, ran, _, err := MigrateRegistryWithEnvironment(nil, source, MigrationEnvironment{})
			if err != nil || !ran {
				t.Fatalf("migration ran=%t err=%v", ran, err)
			}
			if gotBytes, wantBytes := mustJSON(t, got), mustJSON(t, want); gotBytes != wantBytes {
				t.Fatalf("rebuilt migration differs from graph-canonical result:\n--- got ---\n%s\n--- want ---\n%s", gotBytes, wantBytes)
			}
		})
	}
}

func TestAutomaticUIDNameExhaustionIsBoundedAndZeroWrite(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.NameReservations = append(reg.NameReservations, NameReservation{Kind: KindProject, Name: "project-collision", UID: "existing"})
	before := mustJSON(t, reg)
	calls := 0
	m := Mutator{NewUID: func(Kind) (string, error) {
		calls++
		return "project-collision", nil
	}}
	if _, _, err := m.mintAndReserveName(&reg, "test", "", KindProject, ""); !errors.Is(err, ErrNameExhausted) {
		t.Fatalf("exhaustion error = %v, want ErrNameExhausted", err)
	}
	if calls != maxUIDNameAttempts {
		t.Fatalf("uid attempts=%d, want %d", calls, maxUIDNameAttempts)
	}
	if after := mustJSON(t, reg); after != before {
		t.Fatalf("exhaustion mutated Registry:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestEveryAutomaticallyCreatedResourceKindUsesItsExactFullUID(t *testing.T) {
	t.Parallel()

	m := testMutator(dirSet{"/src/root": true})
	reg := NewRegistry()
	registered, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: "/src/root", DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateAgent(&reg, registered.Windows[0].Metadata.UID, CreateAgentOptions{Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BindControlSession(&reg, ControlSessionObservation{
		Session: "home", Windows: []ControlSessionWindow{{Panes: []ControlSessionPane{{Command: "zsh"}}}},
	}, "/bin/zsh", "control", nil); err != nil {
		t.Fatal(err)
	}
	for _, meta := range allResourceMeta(reg) {
		if meta.Name != meta.UID {
			t.Fatalf("automatic name for uid %q = %q, want the exact full uid", meta.UID, meta.Name)
		}
	}
	if len(reg.Projects) == 0 || len(reg.ControlSessions) == 0 || len(reg.Windows) < 2 || len(reg.Panes) < 2 || len(reg.Agents) == 0 {
		t.Fatalf("fixture does not cover every resource kind: projects=%d control=%d windows=%d panes=%d agents=%d",
			len(reg.Projects), len(reg.ControlSessions), len(reg.Windows), len(reg.Panes), len(reg.Agents))
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRootWideSameKindCollisionAndOriginalExplicitSpelling(t *testing.T) {
	t.Parallel()

	m := testMutator(dirSet{"/src/root": true})
	reg := NewRegistry()
	registered, err := m.RegisterProject(&reg, RegisterProjectOptions{
		Root: "/src/root", Name: "Root.Mixed", DefaultShell: "/bin/zsh",
		Topology: []BootstrapWindow{{Name: "First"}, {Name: "Second"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Project.Metadata.Name != "Root.Mixed" {
		t.Fatalf("explicit Project spelling=%q", registered.Project.Metadata.Name)
	}
	first, second := registered.Windows[0], registered.Windows[1]
	created, err := m.AddPane(&reg, first.Metadata.UID, BootstrapPane{Name: "Pane.Mixed"}, "/bin/zsh", "explicit")
	if err != nil {
		t.Fatal(err)
	}
	before := mustJSON(t, reg)
	if _, err := m.AddPane(&reg, second.Metadata.UID, BootstrapPane{Name: "Pane.Mixed"}, "/bin/zsh", "collision"); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("same-root Pane collision = %v, want ErrNameConflict", err)
	}
	if mustJSON(t, reg) != before {
		t.Fatal("same-root collision mutated Registry")
	}
	if created.Metadata.Name != "Pane.Mixed" {
		t.Fatalf("explicit Pane spelling=%q", created.Metadata.Name)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Registry invalid: %v", err)
	}
}
