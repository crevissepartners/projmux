package metadata

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestProjectBootstrapCreatesTheOfflineTopologyWithAValidPrimaryPaneRef(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		topology        []BootstrapWindow
		wantWindowNames []string
		wantPaneNames   []string
	}{
		{
			name:            "default topology is one shell window with one shell pane",
			wantWindowNames: []string{"zsh"},
			wantPaneNames:   []string{"zsh"},
		},
		{
			name: "configured topology is used verbatim",
			topology: []BootstrapWindow{
				{Command: "nvim ."},
				{Name: "server", Panes: []BootstrapPane{{Command: "npm run dev"}, {Command: "/usr/bin/htop"}}},
			},
			wantWindowNames: []string{"nvim", "server"},
			wantPaneNames:   []string{"nvim", "npm", "htop"},
		},
		{
			name: "duplicate topology names get the lowest free suffix",
			topology: []BootstrapWindow{
				{Command: "zsh"},
				{Command: "zsh"},
				{Command: "zsh"},
			},
			wantWindowNames: []string{"zsh", "zsh-1", "zsh-2"},
			wantPaneNames:   []string{"zsh", "zsh", "zsh"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			roots := dirSet{"/src/projmux": true}
			m := testMutator(roots)
			reg := NewRegistry()

			result, err := m.RegisterProject(&reg, RegisterProjectOptions{
				Root:         "/src/projmux",
				Topology:     tt.topology,
				DefaultShell: "/bin/zsh",
				OperationID:  "op-bootstrap",
			})
			if err != nil {
				t.Fatalf("register: %v", err)
			}
			if result.Reused {
				t.Fatal("a fresh root must not be reported as reused")
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("registry invalid immediately after registration: %v", err)
			}

			var gotWindows []string
			for _, window := range reg.WindowsOf(result.Project.Metadata.UID) {
				gotWindows = append(gotWindows, window.Metadata.Name)
				if window.Spec.PrimaryPaneRef == "" {
					t.Fatalf("window %q has no primaryPaneRef", window.Metadata.Name)
				}
				pane, ok := reg.Pane(window.Spec.PrimaryPaneRef)
				if !ok {
					t.Fatalf("window %q primaryPaneRef %q does not resolve", window.Metadata.Name, window.Spec.PrimaryPaneRef)
				}
				if pane.Metadata.OwnerUID() != window.Metadata.UID {
					t.Fatalf("window %q primaryPaneRef is owned by %q", window.Metadata.Name, pane.Metadata.OwnerUID())
				}
			}
			var gotPanes []string
			for _, pane := range reg.Panes {
				gotPanes = append(gotPanes, pane.Metadata.Name)
			}
			if !equalStrings(gotWindows, tt.wantWindowNames) {
				t.Fatalf("windows = %v, want %v", gotWindows, tt.wantWindowNames)
			}
			if !equalStrings(gotPanes, tt.wantPaneNames) {
				t.Fatalf("panes = %v, want %v", gotPanes, tt.wantPaneNames)
			}
			if result.Project.Status.Session != nil {
				t.Fatal("offline registration must not invent a session projection")
			}
		})
	}
}

func TestProjectUIDIsUnchangedByRuntimeCreationTeardownAndRebind(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true, "/src/moved": true}
	m := testMutator(roots)
	reg := NewRegistry()

	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	uid := registered.Project.Metadata.UID
	name := registered.Project.Metadata.Name

	live, err := m.BindProjectSession(&reg, uid, "projmux", true)
	if err != nil {
		t.Fatalf("bind session: %v", err)
	}
	if live.Metadata.UID != uid {
		t.Fatalf("uid changed on runtime creation: %q -> %q", uid, live.Metadata.UID)
	}
	if live.Status.Session == nil || live.Status.Session.Name != "projmux" || !live.Status.Session.Live {
		t.Fatalf("session projection = %+v", live.Status.Session)
	}

	down, err := m.BindProjectSession(&reg, uid, "projmux", false)
	if err != nil {
		t.Fatalf("mark session offline: %v", err)
	}
	if down.Metadata.UID != uid || down.Status.Session.Live {
		t.Fatalf("offline projection = %+v uid=%q", down.Status.Session, down.Metadata.UID)
	}
	if len(reg.WindowsOf(uid)) == 0 {
		t.Fatal("window metadata must survive tmux being down")
	}

	rebound, err := m.RebindProjectRoot(&reg, uid, "/src/moved")
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if rebound.Metadata.UID != uid {
		t.Fatalf("uid changed on rebind: %q -> %q", uid, rebound.Metadata.UID)
	}
	if rebound.Metadata.Name != name {
		t.Fatalf("name changed on rebind: %q -> %q", name, rebound.Metadata.Name)
	}
	if rebound.Spec.Root != "/src/moved" {
		t.Fatalf("root = %q, want /src/moved", rebound.Spec.Root)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after rebind: %v", err)
	}
}

func TestUIDsAreNeverMergedHeuristicallyAndOnlyAnExactSavedRootReusesOne(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/a/projmux": true, "/b/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()

	first, err := registerFixture(m, &reg, "/a/projmux")
	if err != nil {
		t.Fatalf("register first: %v", err)
	}
	second, err := registerFixture(m, &reg, "/b/projmux")
	if err != nil {
		t.Fatalf("register second: %v", err)
	}

	if first.Project.Metadata.UID == second.Project.Metadata.UID {
		t.Fatal("projects sharing a root basename must not share a uid")
	}
	if first.Project.Metadata.Name != "projmux" || second.Project.Metadata.Name != "projmux-1" {
		t.Fatalf("names = %q/%q, want projmux/projmux-1", first.Project.Metadata.Name, second.Project.Metadata.Name)
	}
	if first.Project.Metadata.DisplayName != "projmux" || second.Project.Metadata.DisplayName != "projmux" {
		t.Fatalf("display names = %q/%q, want both projmux", first.Project.Metadata.DisplayName, second.Project.Metadata.DisplayName)
	}

	windowsBefore := len(reg.Windows)
	again, err := registerFixture(m, &reg, "/a/projmux/")
	if err != nil {
		t.Fatalf("re-register exact root: %v", err)
	}
	if !again.Reused {
		t.Fatal("an exact saved root reappearing must be reported as reused")
	}
	if again.Project.Metadata.UID != first.Project.Metadata.UID {
		t.Fatalf("exact saved root did not recover its uid: %q != %q", again.Project.Metadata.UID, first.Project.Metadata.UID)
	}
	if len(reg.Projects) != 2 || len(reg.Windows) != windowsBefore {
		t.Fatalf("re-registering an exact root mutated the registry: projects=%d windows=%d", len(reg.Projects), len(reg.Windows))
	}
}

func TestExplicitNameCollisionExitsAsAUsageErrorWithZeroMutations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(m Mutator, reg *Registry, uid string) error
		wantErr error
	}{
		{
			name: "register with a taken explicit project name",
			mutate: func(m Mutator, reg *Registry, _ string) error {
				_, err := m.RegisterProject(reg, RegisterProjectOptions{
					Root:         "/src/other",
					Name:         "projmux",
					DefaultShell: "/bin/zsh",
					OperationID:  "op-collide",
				})
				return err
			},
			wantErr: ErrNameConflict,
		},
		{
			name: "rename a project onto a taken name",
			mutate: func(m Mutator, reg *Registry, uid string) error {
				if _, err := m.RegisterProject(reg, RegisterProjectOptions{Root: "/src/other", DefaultShell: "/bin/zsh"}); err != nil {
					return err
				}
				other, _ := reg.ProjectByRoot("/src/other")
				_, err := m.RenameProject(reg, other.Metadata.UID, "projmux")
				_ = uid
				return err
			},
			wantErr: ErrNameConflict,
		},
		{
			name: "rebind onto a root bound to another project",
			mutate: func(m Mutator, reg *Registry, uid string) error {
				if _, err := m.RegisterProject(reg, RegisterProjectOptions{Root: "/src/other", DefaultShell: "/bin/zsh"}); err != nil {
					return err
				}
				other, _ := reg.ProjectByRoot("/src/other")
				_, err := m.RebindProjectRoot(reg, other.Metadata.UID, "/src/projmux")
				_ = uid
				return err
			},
			wantErr: ErrRootConflict,
		},
		{
			name: "rebind onto a root that does not exist",
			mutate: func(m Mutator, reg *Registry, uid string) error {
				_, err := m.RebindProjectRoot(reg, uid, "/src/missing")
				return err
			},
			wantErr: ErrInvalidRoot,
		},
		{
			name: "rebind onto a relative root",
			mutate: func(m Mutator, reg *Registry, uid string) error {
				_, err := m.RebindProjectRoot(reg, uid, "relative/path")
				return err
			},
			wantErr: ErrInvalidRoot,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			roots := dirSet{"/src/projmux": true, "/src/other": true}
			m := testMutator(roots)
			reg := NewRegistry()
			registered, err := registerFixture(m, &reg, "/src/projmux")
			if err != nil {
				t.Fatalf("seed register: %v", err)
			}

			// Snapshot the registry so "zero mutations" is byte-exact, not
			// merely "the project still exists".
			before := mustJSON(t, reg)

			err = tt.mutate(m, &reg, registered.Project.Metadata.UID)
			if err == nil {
				t.Fatal("expected the operation to fail")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error %v does not wrap %v", err, tt.wantErr)
			}
			if !IsUsageError(err) {
				t.Fatalf("error must be a usage error so the CLI exits 2: %v", err)
			}
			// The seed project itself must be untouched. Operations that
			// legitimately created another project before failing are
			// compared on the seed subtree only.
			seed, ok := reg.Project(registered.Project.Metadata.UID)
			if !ok {
				t.Fatal("the pre-existing project was removed by a failed operation")
			}
			if seed.Metadata.Name != registered.Project.Metadata.Name || seed.Spec.Root != registered.Project.Spec.Root {
				t.Fatalf("pre-existing project mutated: %+v", seed)
			}
			if tt.name == "register with a taken explicit project name" && mustJSON(t, reg) != before {
				t.Fatalf("a failed pre-create operation mutated the registry:\nbefore=%s\nafter=%s", before, mustJSON(t, reg))
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("registry invalid after a failed operation: %v", err)
			}
		})
	}
}

func TestMissingProjectRootRecordsAConditionAndAReturningRootRecoversTheSameUID(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()

	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	uid := registered.Project.Metadata.UID
	reservationsBefore := len(reg.NameReservations)
	windowsBefore := len(reg.WindowsOf(uid))

	delete(roots, "/src/projmux")
	if err := m.ObserveProjectRoots(&reg); err != nil {
		t.Fatalf("observe missing root: %v", err)
	}
	project, ok := reg.Project(uid)
	if !ok {
		t.Fatal("a missing root must never delete the project")
	}
	condition, ok := project.HasCondition(ConditionMissingRoot)
	if !ok {
		t.Fatal("expected a MissingRoot condition")
	}
	if !condition.FirstObservedAt.Equal(fixedNow) {
		t.Fatalf("firstObservedAt = %v, want %v", condition.FirstObservedAt, fixedNow)
	}
	if len(reg.NameReservations) != reservationsBefore {
		t.Fatalf("name reservations were released for a missing root: %d -> %d", reservationsBefore, len(reg.NameReservations))
	}
	if len(reg.WindowsOf(uid)) != windowsBefore {
		t.Fatal("window metadata must survive a missing root")
	}

	// A repeat observation must not move the first-observed timestamp.
	later := m
	later.Now = func() time.Time { return fixedNow.Add(time.Hour) }
	if err := later.ObserveProjectRoots(&reg); err != nil {
		t.Fatalf("observe again: %v", err)
	}
	project, _ = reg.Project(uid)
	condition, _ = project.HasCondition(ConditionMissingRoot)
	if !condition.FirstObservedAt.Equal(fixedNow) {
		t.Fatalf("firstObservedAt moved on a repeat observation: %v", condition.FirstObservedAt)
	}

	roots["/src/projmux"] = true
	if err := m.ObserveProjectRoots(&reg); err != nil {
		t.Fatalf("observe returning root: %v", err)
	}
	project, _ = reg.Project(uid)
	if _, ok := project.HasCondition(ConditionMissingRoot); ok {
		t.Fatal("a returning root must clear the MissingRoot condition")
	}
	if project.Metadata.UID != uid {
		t.Fatalf("a returning root must recover the same uid: %q != %q", project.Metadata.UID, uid)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after root lifecycle: %v", err)
	}
}

func TestOperationRollbackRemovesOnlyTheResourcesThisOperationCreated(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()
	if _, err := registerFixture(m, &reg, "/src/projmux"); err != nil {
		t.Fatalf("seed register: %v", err)
	}
	before := mustJSON(t, reg)

	project, _ := reg.ProjectByRoot("/src/projmux")
	txn := m.Begin(&reg, "op-rollback")
	window, panes, err := m.addWindowTx(txn, &reg, "test", project.Metadata.UID, BootstrapWindow{Command: "nvim"}, "/bin/zsh", "/src/projmux", fixedNow)
	if err != nil {
		t.Fatalf("create window: %v", err)
	}
	if len(txn.Created()) != 2 {
		t.Fatalf("ledger = %v, want one window and one pane", txn.Created())
	}
	if _, ok := reg.Window(window.Metadata.UID); !ok {
		t.Fatal("window was not created")
	}

	txn.Rollback()

	if _, ok := reg.Window(window.Metadata.UID); ok {
		t.Fatal("rollback must remove the window this operation created")
	}
	if _, ok := reg.Pane(panes[0].Metadata.UID); ok {
		t.Fatal("rollback must remove the pane this operation created")
	}
	if got := mustJSON(t, reg); got != before {
		t.Fatalf("rollback did not restore the pre-operation registry:\nbefore=%s\nafter=%s", before, got)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after rollback: %v", err)
	}
}

func TestDeleteReleasesNameReservationsSoTheSuffixBecomesAvailableAgain(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/a": true, "/src/b": true}
	m := testMutator(roots)
	reg := NewRegistry()

	if _, err := registerFixture(m, &reg, "/src/a"); err != nil {
		t.Fatalf("register a: %v", err)
	}
	first, _ := reg.ProjectByRoot("/src/a")
	uid := first.Metadata.UID

	if err := m.DeleteProject(&reg, uid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(reg.Projects) != 0 || len(reg.Windows) != 0 || len(reg.Panes) != 0 {
		t.Fatalf("delete left resources behind: %d/%d/%d", len(reg.Projects), len(reg.Windows), len(reg.Panes))
	}
	if len(reg.NameReservations) != 0 {
		t.Fatalf("delete left reservations behind: %+v", reg.NameReservations)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after delete: %v", err)
	}

	if _, err := registerFixture(m, &reg, "/src/b"); err != nil {
		t.Fatalf("register b: %v", err)
	}
	replacement, _ := reg.ProjectByRoot("/src/b")
	if replacement.Metadata.Name != "b" {
		t.Fatalf("name = %q, want b", replacement.Metadata.Name)
	}
	if replacement.Metadata.UID == uid {
		t.Fatal("a new project must never reuse a deleted uid")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}
