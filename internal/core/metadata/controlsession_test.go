package metadata

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// controlSessionKindToken is the Kind spelling written as a literal on purpose.
//
// Every assertion in this file that must be able to run against a build without
// KindControlSession goes through the literal, so the test measures observable
// behavior -- "does this build know a kind spelled ControlSession" -- rather than
// the presence of a constant.
const controlSessionKindToken = Kind("ControlSession")

func TestControlSessionJoinsTheClosedKindSet(t *testing.T) {
	kinds := Kinds()
	if got, want := len(kinds), 5; got != want {
		t.Fatalf("len(Kinds()) = %d, want %d: %v", got, want, kinds)
	}
	if kinds[len(kinds)-1] != controlSessionKindToken {
		t.Fatalf("Kinds() = %v, want %q last: the control session is the newest root kind and Kinds() owns declaration order", kinds, controlSessionKindToken)
	}
	// The four pre-existing kinds keep their order. A reordering would silently
	// change every consumer that indexes or ranks the closed set.
	for i, want := range []Kind{KindProject, KindWindow, KindPane, KindAgent} {
		if kinds[i] != want {
			t.Fatalf("Kinds()[%d] = %q, want %q", i, kinds[i], want)
		}
	}
}

func TestControlSessionUIDPrefixIsMintableAndReadable(t *testing.T) {
	uid, err := NewUID(controlSessionKindToken)
	if err != nil {
		t.Fatalf("NewUID(%q) error = %v", controlSessionKindToken, err)
	}
	if !strings.HasPrefix(uid, "ctl-") {
		t.Fatalf("NewUID(%q) = %q, want the %q prefix", controlSessionKindToken, uid, "ctl-")
	}
	kind, ok := UIDKind(uid)
	if !ok || kind != controlSessionKindToken {
		t.Fatalf("UIDKind(%q) = (%q, %t), want (%q, true)", uid, kind, ok, controlSessionKindToken)
	}
	// The prefix is not shared with any other kind: a uid that decoded as two
	// kinds would make the debugging aid worse than nothing.
	for _, other := range []Kind{KindProject, KindWindow, KindPane, KindAgent} {
		otherUID, err := NewUID(other)
		if err != nil {
			t.Fatalf("NewUID(%q) error = %v", other, err)
		}
		if strings.HasPrefix(otherUID, "ctl-") {
			t.Fatalf("NewUID(%q) = %q, which collides with the control session prefix", other, otherUID)
		}
	}
}

// TestControlSessionSpecCarriesNoPath is the structural half of the product
// contract's first row: $HOME never becomes a Project root, and a control session
// owns no path at all.
//
// It is a reflect test rather than a behavioral one because the guarantee being
// pinned is a compile-time one. Every path-based surface projmux has -- managed
// roots, trust, rebind, cwd defaults, ProjectByRoot -- reads Project.Spec.Root.
// As long as ControlSessionSpec has no path field, none of them can be handed a
// control session's path by accident, by a forgotten filter, or by a later
// refactor: there is nothing to hand them.
func TestControlSessionSpecCarriesNoPath(t *testing.T) {
	spec := reflect.TypeFor[ControlSessionSpec]()
	if got, want := spec.NumField(), 1; got != want {
		t.Fatalf("ControlSessionSpec has %d fields, want %d; a control session owns exactly its tmux session name", got, want)
	}
	if got := spec.Field(0).Name; got != "Session" {
		t.Fatalf("ControlSessionSpec field 0 = %q, want %q", got, "Session")
	}
	for _, forbidden := range []string{"root", "path", "cwd", "dir"} {
		for i := range spec.NumField() {
			if strings.Contains(strings.ToLower(spec.Field(i).Name), forbidden) {
				t.Fatalf("ControlSessionSpec.%s looks like a filesystem path field; a control session must own no path", spec.Field(i).Name)
			}
		}
	}
}

// TestValidateOwnershipCombinations pins the ownerRef contract in one table.
//
// The rows that existed before the control session are here to prove their
// refusal wording is byte-identical: widening a Window's allowed owner set must
// not change what a Pane owned by an Agent, an Agent owned by a Project, or a
// Project carrying an ownerRef says.
func TestValidateOwnershipCombinations(t *testing.T) {
	for _, tt := range []struct {
		name     string
		registry func() Registry
		wantErr  string
	}{
		{
			name:     "window owned by a project is legal",
			registry: func() Registry { return ownershipFixture(t, KindProject) },
		},
		{
			name:     "window owned by a control session is legal",
			registry: func() Registry { return ownershipFixture(t, controlSessionKindToken) },
		},
		{
			name: "window owned by a pane names both allowed owner kinds",
			registry: func() Registry {
				reg := ownershipFixture(t, KindProject)
				reg.Windows[0].Metadata.OwnerRef = &OwnerRef{Kind: KindPane, UID: reg.Panes[0].Metadata.UID}
				return reg
			},
			wantErr: `Window "window" ownerRef kind "Pane" is not "Project" or "ControlSession"`,
		},
		{
			name: "window with no ownerRef keeps its wording",
			registry: func() Registry {
				reg := ownershipFixture(t, KindProject)
				reg.Windows[0].Metadata.OwnerRef = nil
				return reg
			},
			wantErr: `Window "window" has no ownerRef`,
		},
		{
			name: "window owned by an absent control session keeps its wording",
			registry: func() Registry {
				reg := ownershipFixture(t, controlSessionKindToken)
				reg.Windows[0].Metadata.OwnerRef = &OwnerRef{Kind: controlSessionKindToken, UID: "ctl-missing"}
				return reg
			},
			wantErr: `Window "window" ownerRef "ctl-missing" does not exist`,
		},
		{
			name: "project carrying an ownerRef keeps its wording",
			registry: func() Registry {
				reg := ownershipFixture(t, KindProject)
				reg.Projects[0].Metadata.OwnerRef = &OwnerRef{Kind: controlSessionKindToken, UID: "ctl-01"}
				return reg
			},
			wantErr: `project "project" must not have an ownerRef`,
		},
		{
			name: "control session carrying an ownerRef is refused",
			registry: func() Registry {
				reg := ownershipFixture(t, controlSessionKindToken)
				reg.ControlSessions[0].Metadata.OwnerRef = &OwnerRef{Kind: KindProject, UID: "proj-01"}
				return reg
			},
			wantErr: `control session "home" must not have an ownerRef`,
		},
		{
			name: "control session naming no tmux session is refused",
			registry: func() Registry {
				reg := ownershipFixture(t, controlSessionKindToken)
				reg.ControlSessions[0].Spec.Session = "  "
				return reg
			},
			wantErr: `control session "home" names no tmux session`,
		},
		{
			name: "two control sessions on one tmux session are refused",
			registry: func() Registry {
				reg := ownershipFixture(t, controlSessionKindToken)
				second := reg.ControlSessions[0].Clone()
				second.Metadata.UID = "ctl-02"
				second.Metadata.Name = "home-1"
				reg.ControlSessions = append(reg.ControlSessions, second)
				reg.NameReservations = append(reg.NameReservations,
					NameReservation{Kind: controlSessionKindToken, Name: "home-1", UID: "ctl-02"})
				return reg
			},
			wantErr: `tmux session "home" is bound to both ctl-01 and ctl-02`,
		},
		{
			name: "shell pane owned by an agent keeps its single-kind wording",
			registry: func() Registry {
				reg := ownershipFixture(t, KindProject)
				reg.Panes[0].Metadata.OwnerRef = &OwnerRef{Kind: KindAgent, UID: "agent-01"}
				return reg
			},
			wantErr: `Pane "pane" ownerRef kind "Agent" is not "Window"`,
		},
		{
			name: "control session owning a pane directly is refused",
			registry: func() Registry {
				reg := ownershipFixture(t, controlSessionKindToken)
				reg.Panes[0].Metadata.OwnerRef = &OwnerRef{Kind: controlSessionKindToken, UID: "ctl-01"}
				return reg
			},
			wantErr: `Pane "pane" ownerRef kind "ControlSession" is not "Window"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.registry().Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() error = %v, want nil", err)
			case tt.wantErr == "":
				return
			case err == nil:
				t.Fatalf("Validate() = nil, want %q", tt.wantErr)
			case !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Validate() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// ownershipFixture builds the smallest valid registry whose single Window hangs
// from the requested root kind.
func ownershipFixture(t *testing.T, ownerKind Kind) Registry {
	t.Helper()
	reg := NewRegistry()
	reg.Projects = []Project{{
		APIVersion: APIVersion,
		Kind:       KindProject,
		Metadata:   ObjectMeta{UID: "proj-01", Name: "project", CreatedAt: fixedNow},
		Spec:       ProjectSpec{Root: "/tmp/projmux-ownership", PrimaryWindowRef: "win-01"},
	}}
	reg.NameReservations = []NameReservation{{Kind: KindProject, Name: "project", UID: "proj-01"}}

	ownerUID := "proj-01"
	if ownerKind == controlSessionKindToken {
		ownerUID = "ctl-01"
		reg.Projects = nil
		reg.NameReservations = nil
		reg.ControlSessions = []ControlSession{{
			APIVersion: APIVersion,
			Kind:       controlSessionKindToken,
			Metadata:   ObjectMeta{UID: "ctl-01", Name: "home", CreatedAt: fixedNow},
			Spec:       ControlSessionSpec{Session: "home"},
		}}
		reg.NameReservations = append(reg.NameReservations,
			NameReservation{Kind: controlSessionKindToken, Name: "home", UID: "ctl-01"})
	}

	reg.Windows = []Window{{
		APIVersion: APIVersion,
		Kind:       KindWindow,
		Metadata: ObjectMeta{
			UID: "win-01", Name: "window", CreatedAt: fixedNow,
			OwnerRef: &OwnerRef{Kind: ownerKind, UID: ownerUID},
		},
		Spec: WindowSpec{PrimaryPaneRef: "pane-01"},
	}}
	reg.Panes = []Pane{{
		APIVersion: APIVersion,
		Kind:       KindPane,
		Metadata: ObjectMeta{
			UID: "pane-01", Name: "pane", CreatedAt: fixedNow,
			OwnerRef: &OwnerRef{Kind: KindWindow, UID: "win-01"},
		},
		Spec: PaneSpec{Role: PaneRoleShell},
	}}
	reg.NameReservations = append(reg.NameReservations,
		NameReservation{Scope: ownerUID, Kind: KindWindow, Name: "window", UID: "win-01"},
		NameReservation{Scope: "win-01", Kind: KindPane, Name: "pane", UID: "pane-01"},
	)
	return reg
}

// controlObservation builds one live control-session observation.
func controlObservation(session string, windows ...ControlSessionWindow) ControlSessionObservation {
	return ControlSessionObservation{Session: session, Windows: windows}
}

func TestBindControlSessionMintsAndConverges(t *testing.T) {
	mutator := testMutator(dirSet{})
	reg := NewRegistry()
	observed := controlObservation("home", ControlSessionWindow{
		DisplayName: "zsh",
		Panes:       []ControlSessionPane{{Command: "zsh"}},
	})

	first, err := mutator.BindControlSession(&reg, observed, "/bin/zsh", "op-1", nil)
	if err != nil {
		t.Fatalf("BindControlSession() error = %v", err)
	}
	if first.Reused {
		t.Fatal("first bind reported Reused; nothing existed to reuse")
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate() after first bind = %v", err)
	}
	if got, want := len(reg.ControlSessions), 1; got != want {
		t.Fatalf("len(ControlSessions) = %d, want %d", got, want)
	}
	control := reg.ControlSessions[0]
	if control.Spec.Session != "home" || control.Metadata.Name != "home" {
		t.Fatalf("control session = %+v, want session/name %q", control, "home")
	}
	if control.Metadata.OwnerRef != nil {
		t.Fatalf("control session carries an ownerRef %+v; it is a root", control.Metadata.OwnerRef)
	}
	if got, want := len(reg.Windows), 1; got != want {
		t.Fatalf("len(Windows) = %d, want %d", got, want)
	}
	owner := reg.Windows[0].Metadata.OwnerRef
	if owner == nil || owner.Kind != controlSessionKindToken || owner.UID != control.Metadata.UID {
		t.Fatalf("window ownerRef = %+v, want the control session", owner)
	}
	if got, want := len(reg.Panes), 1; got != want {
		t.Fatalf("len(Panes) = %d, want %d", got, want)
	}
	if got := reg.Windows[0].Spec.PrimaryPaneRef; got != reg.Panes[0].Metadata.UID {
		t.Fatalf("primaryPaneRef = %q, want %q", got, reg.Panes[0].Metadata.UID)
	}
	// No Agent is ever minted below a control session. See controlsession.go.
	if len(reg.Agents) != 0 {
		t.Fatalf("bind minted %d Agents; a control session never owns one in this slice", len(reg.Agents))
	}
	// Zero Projects: $HOME is not registered and no root is invented.
	if len(reg.Projects) != 0 {
		t.Fatalf("bind created %d Projects; a control session owns no path", len(reg.Projects))
	}

	// The second pass is the already-live backfill: the live objects now carry
	// the uids the first pass allocated, so every binding is a rebind and the
	// registry is byte-identical.
	observed.Windows[0].UID = reg.Windows[0].Metadata.UID
	observed.Windows[0].Panes[0].UID = reg.Panes[0].Metadata.UID
	before := reg.Clone().Normalize()
	second, err := mutator.BindControlSession(&reg, observed, "/bin/zsh", "op-2", nil)
	if err != nil {
		t.Fatalf("second BindControlSession() error = %v", err)
	}
	if !second.Reused {
		t.Fatal("second bind did not report Reused; the exact session name must reuse the same uid")
	}
	if second.ControlSession.Metadata.UID != control.Metadata.UID {
		t.Fatalf("second bind uid = %q, want %q", second.ControlSession.Metadata.UID, control.Metadata.UID)
	}
	if len(second.Created) != 0 {
		t.Fatalf("second bind created %v, want nothing", second.Created)
	}
	for _, bound := range second.Windows {
		if bound.Origin != ImportRebound {
			t.Fatalf("second bind window origin = %q, want %q", bound.Origin, ImportRebound)
		}
	}
	for _, bound := range second.Panes {
		if bound.Origin != ImportRebound {
			t.Fatalf("second bind pane origin = %q, want %q", bound.Origin, ImportRebound)
		}
	}
	after := reg.Clone().Normalize()
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("second bind changed the registry:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

func TestBindControlSessionAdoptsAnUnmirroredLiveWindow(t *testing.T) {
	mutator := testMutator(dirSet{})
	reg := NewRegistry()
	observed := controlObservation("home", ControlSessionWindow{Panes: []ControlSessionPane{{}}})
	if _, err := mutator.BindControlSession(&reg, observed, "/bin/zsh", "op-1", nil); err != nil {
		t.Fatalf("BindControlSession() error = %v", err)
	}
	windowUID := reg.Windows[0].Metadata.UID
	paneUID := reg.Panes[0].Metadata.UID

	// This is the mirror-write-failed state: the registry holds the resources,
	// the machine carries no uid at all. A pass that minted here would duplicate
	// the Window on every `projmux shell` entry forever.
	result, err := mutator.BindControlSession(&reg, observed, "/bin/zsh", "op-2", nil)
	if err != nil {
		t.Fatalf("second BindControlSession() error = %v", err)
	}
	if got, want := len(reg.Windows), 1; got != want {
		t.Fatalf("len(Windows) = %d, want %d: the unmirrored live window must adopt, not mint", got, want)
	}
	if got, want := len(reg.Panes), 1; got != want {
		t.Fatalf("len(Panes) = %d, want %d", got, want)
	}
	if len(result.Windows) != 1 || result.Windows[0].UID != windowUID || result.Windows[0].Origin != ImportAdopted {
		t.Fatalf("window binding = %+v, want %q adopted", result.Windows, windowUID)
	}
	if len(result.Panes) != 1 || result.Panes[0].UID != paneUID || result.Panes[0].Origin != ImportAdopted {
		t.Fatalf("pane binding = %+v, want %q adopted", result.Panes, paneUID)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestBindControlSessionRefusesAForeignScopedUID(t *testing.T) {
	mutator := testMutator(dirSet{"/tmp/projmux-control": true})
	reg := NewRegistry()
	if _, err := registerFixture(mutator, &reg, "/tmp/projmux-control"); err != nil {
		t.Fatalf("registerFixture() error = %v", err)
	}
	projectWindowUID := reg.Windows[0].Metadata.UID

	// The live window claims a uid that exists and belongs to a Project. Adopting
	// it would move a Project's Window under the control session, which is the
	// one mistake adoption can make that no later pass can undo.
	observed := controlObservation("home", ControlSessionWindow{
		UID:   projectWindowUID,
		Panes: []ControlSessionPane{{}},
	})
	result, err := mutator.BindControlSession(&reg, observed, "/bin/zsh", "op-1", nil)
	if err != nil {
		t.Fatalf("BindControlSession() error = %v", err)
	}
	if len(result.Windows) != 0 || len(result.Panes) != 0 {
		t.Fatalf("bind reported windows=%+v panes=%+v, want a refusal to bind anything", result.Windows, result.Panes)
	}
	window, _ := reg.Window(projectWindowUID)
	if owner := window.Metadata.OwnerRef; owner == nil || owner.Kind != KindProject {
		t.Fatalf("project window ownerRef = %+v, want it untouched under its Project", owner)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestBindControlSessionMintsForAnUnknownLiveUID(t *testing.T) {
	mutator := testMutator(dirSet{})
	reg := NewRegistry()
	observed := controlObservation("home", ControlSessionWindow{
		UID:   "win-nobody-knows",
		Panes: []ControlSessionPane{{UID: "pane-nobody-knows"}},
	})
	result, err := mutator.BindControlSession(&reg, observed, "/bin/zsh", "op-1", nil)
	if err != nil {
		t.Fatalf("BindControlSession() error = %v", err)
	}
	if len(result.Windows) != 1 || result.Windows[0].Origin != ImportCreated {
		t.Fatalf("window binding = %+v, want one created Window", result.Windows)
	}
	if result.Windows[0].UID == "win-nobody-knows" {
		t.Fatal("bind reused the foreign uid; a fresh uid must be allocated instead")
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestBindControlSessionRequiresASessionName(t *testing.T) {
	mutator := testMutator(dirSet{})
	reg := NewRegistry()
	if _, err := mutator.BindControlSession(&reg, controlObservation("   "), "/bin/zsh", "op-1", nil); err == nil {
		t.Fatal("BindControlSession() with a blank session name = nil, want a refusal")
	}
	if len(reg.ControlSessions) != 0 {
		t.Fatalf("a refused bind left %d control sessions behind", len(reg.ControlSessions))
	}
}

func TestControlSessionAndProjectMayShareAName(t *testing.T) {
	mutator := testMutator(dirSet{"/tmp/home": true})
	reg := NewRegistry()
	project, err := mutator.RegisterProject(&reg, RegisterProjectOptions{
		Root: "/tmp/home", Name: "home", DefaultShell: "/bin/zsh", OperationID: "op-project",
	})
	if err != nil {
		t.Fatalf("RegisterProject() error = %v", err)
	}
	result, err := mutator.BindControlSession(&reg, controlObservation("home",
		ControlSessionWindow{Panes: []ControlSessionPane{{}}}), "/bin/zsh", "op-control", nil)
	if err != nil {
		t.Fatalf("BindControlSession() error = %v", err)
	}
	if got, want := result.ControlSession.Metadata.Name, "home"; got != want {
		t.Fatalf("control session name = %q, want %q: the two root kinds hold separate reservation slots", got, want)
	}
	if project.Project.Metadata.Name != "home" {
		t.Fatalf("project name = %q, want %q", project.Project.Metadata.Name, "home")
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

// TestPrePhase0RegistryMigratesToTheCanonicalProjectAnchor is the v1 parity
// proof for the additive ControlSession field plus the v2 anchor repair.
//
// The document below is exactly what a build without control sessions writes. It
// It must classify as a known migration and must not invent a control session.
func TestPrePhase0RegistryMigratesToTheCanonicalProjectAnchor(t *testing.T) {
	const document = `{
  "apiVersion": "projmux.io/v1alpha1",
  "schemaVersion": 1,
  "updatedAt": "2026-08-15T09:30:00Z",
  "projects": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Project",
      "metadata": {"uid": "proj-01", "name": "projmux", "displayName": "projmux", "createdAt": "2026-08-15T09:30:00Z"},
      "spec": {"root": "/tmp/projmux-parity"},
      "status": {}
    }
  ],
  "windows": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Window",
      "metadata": {"uid": "win-01", "name": "zsh", "ownerRef": {"kind": "Project", "uid": "proj-01"}, "createdAt": "2026-08-15T09:30:00Z"},
      "spec": {"primaryPaneRef": "pane-01"}
    }
  ],
  "panes": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Pane",
      "metadata": {"uid": "pane-01", "name": "zsh", "ownerRef": {"kind": "Window", "uid": "win-01"}, "createdAt": "2026-08-15T09:30:00Z"},
      "spec": {"role": "shell", "cwd": "/tmp/projmux-parity"},
      "status": {}
    }
  ],
  "nameReservations": [
    {"kind": "Project", "name": "projmux", "uid": "proj-01"},
    {"scope": "proj-01", "kind": "Window", "name": "zsh", "uid": "win-01"},
    {"scope": "win-01", "kind": "Pane", "name": "zsh", "uid": "pane-01"}
  ]
}`

	var reg Registry
	if err := json.Unmarshal([]byte(document), &reg); err != nil {
		t.Fatalf("decode pre-Phase-0 registry: %v", err)
	}
	action, err := ClassifySchemaVersion(reg.SchemaVersion)
	if err != nil {
		t.Fatalf("ClassifySchemaVersion(%d) error = %v", reg.SchemaVersion, err)
	}
	if action != SchemaMigrate {
		t.Fatalf("ClassifySchemaVersion(%d) = %v, want %v", reg.SchemaVersion, action, SchemaMigrate)
	}
	if reg.ControlSessions != nil {
		t.Fatalf("ControlSessions decoded to %+v, want nil for an absent key", reg.ControlSessions)
	}
	migrated, ran, err := MigrateRegistry(reg)
	if err != nil {
		t.Fatalf("MigrateRegistry() error = %v", err)
	}
	if !ran {
		t.Fatal("MigrateRegistry() did not run the v1 -> v2 repair")
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if migrated.Projects[0].Spec.PrimaryWindowRef != "win-01" {
		t.Fatalf("primaryWindowRef = %q, want win-01", migrated.Projects[0].Spec.PrimaryWindowRef)
	}
	// The additive ControlSession key still stays absent when there are none.
	encoded, err := json.Marshal(migrated)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(encoded), "controlSessions") {
		t.Fatalf("re-encoded registry mentions controlSessions: %s", encoded)
	}
}

// TestControlSessionRollbackRemovesOnlyWhatItCreated pins the transaction ledger
// for the new kind: a post-create failure must not leave a control session, a
// Window, or a name reservation behind.
func TestControlSessionRollbackRemovesOnlyWhatItCreated(t *testing.T) {
	mutator := testMutator(dirSet{})
	mutator.NewUID = failingUIDAfter(sequentialUIDs(), 2)
	reg := NewRegistry()
	_, err := mutator.BindControlSession(&reg, controlObservation("home",
		ControlSessionWindow{Panes: []ControlSessionPane{{}}}), "/bin/zsh", "op-1", nil)
	if err == nil {
		t.Fatal("BindControlSession() = nil, want the injected uid failure")
	}
	if len(reg.ControlSessions) != 0 || len(reg.Windows) != 0 || len(reg.Panes) != 0 {
		t.Fatalf("rollback left control=%d windows=%d panes=%d", len(reg.ControlSessions), len(reg.Windows), len(reg.Panes))
	}
	if len(reg.NameReservations) != 0 {
		t.Fatalf("rollback left reservations %+v", reg.NameReservations)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate() after rollback = %v", err)
	}
}

// failingUIDAfter mints successfully `allowed` times and then fails, which is the
// shortest way to reach the rollback path of a multi-mint transaction.
func failingUIDAfter(next func(Kind) (string, error), allowed int) func(Kind) (string, error) {
	minted := 0
	return func(kind Kind) (string, error) {
		minted++
		if minted > allowed {
			return "", stateErr("mint uid", ErrInvalidRegistry, "injected uid failure for %s", kind)
		}
		return next(kind)
	}
}
