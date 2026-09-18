package metadata

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realDirMutator registers against the real filesystem with real minted UIDs,
// the way `projmux create project --root <dir>` does.
func realDirMutator() Mutator {
	return Mutator{
		Now:    func() time.Time { return fixedNow },
		NewUID: NewUID,
		DirExists: func(path string) (bool, error) {
			info, err := os.Stat(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return false, nil
				}
				return false, err
			}
			return info.IsDir(), nil
		},
	}
}

func mkdirRoot(t *testing.T, parts ...string) string {
	t.Helper()
	root := filepath.Join(append([]string{t.TempDir()}, parts...)...)
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRegisterProjectWithoutANameIsNamedAfterItsRootBasename(t *testing.T) {
	t.Parallel()

	m := realDirMutator()
	reg := NewRegistry()
	root := mkdirRoot(t, "myrepo")
	registered, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: root, DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	project := registered.Project
	if project.Metadata.Name != "myrepo" {
		t.Fatalf("Project name = %q, want root basename %q", project.Metadata.Name, "myrepo")
	}
	if !isProjectUIDShaped(project.Metadata.UID) {
		t.Fatalf("Project uid %q is not a minted Project uid", project.Metadata.UID)
	}
	if owner, ok := reg.nameOwner("", KindProject, "myrepo"); !ok || owner != project.Metadata.UID {
		t.Fatalf("basename reservation owner = %q/%t, want %q", owner, ok, project.Metadata.UID)
	}
	for _, window := range registered.Windows {
		if window.Metadata.Name != window.Metadata.UID {
			t.Fatalf("Window automatic name = %q, want exact uid %q", window.Metadata.Name, window.Metadata.UID)
		}
	}
	for _, pane := range registered.Panes {
		if pane.Metadata.Name != pane.Metadata.UID {
			t.Fatalf("Pane automatic name = %q, want exact uid %q", pane.Metadata.Name, pane.Metadata.UID)
		}
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestASecondRootWithATakenBasenameFallsBackToItsExactUIDWithoutASuffix(t *testing.T) {
	t.Parallel()

	m := realDirMutator()
	reg := NewRegistry()
	first, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: mkdirRoot(t, "a", "myrepo"), DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: mkdirRoot(t, "b", "myrepo"), DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Project.Metadata.Name != second.Project.Metadata.UID {
		t.Fatalf("colliding basename Project name = %q, want its exact uid %q", second.Project.Metadata.Name, second.Project.Metadata.UID)
	}
	if strings.HasPrefix(second.Project.Metadata.Name, "myrepo") {
		t.Fatalf("colliding basename produced a derived name %q", second.Project.Metadata.Name)
	}
	stored, ok := reg.Project(first.Project.Metadata.UID)
	if !ok || stored.Metadata.Name != "myrepo" {
		t.Fatalf("first Project name changed: %+v", stored)
	}
	if owner, _ := reg.nameOwner("", KindProject, "myrepo"); owner != first.Project.Metadata.UID {
		t.Fatalf("basename reservation moved to %q", owner)
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterProjectSanitizesTheRootBasenameOrFallsBackToTheExactUID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		root string
		// want is the expected name; empty means the exact minted uid.
		want string
	}{
		{name: "plain basename", root: "/src/projmux", want: "projmux"},
		{name: "case is preserved", root: "/src/Projmux", want: "Projmux"},
		{name: "spaces collapse", root: "/src/my  repo", want: "my-repo"},
		{name: "selector punctuation collapses", root: "/src/team:web=v2", want: "team-web-v2"},
		{name: "trailing separator is cleaned", root: "/src/trail/", want: "trail"},
		{name: "punctuation-only basename uses the uid", root: "/src/@@@"},
		{name: "filesystem root uses the uid, never project", root: "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := testMutator(dirSet{filepath.Clean(tt.root): true})
			m.NewUID = NewUID
			reg := NewRegistry()
			registered, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: tt.root, DefaultShell: "/bin/zsh"})
			if err != nil {
				t.Fatal(err)
			}
			want := tt.want
			if want == "" {
				want = registered.Project.Metadata.UID
			}
			if got := registered.Project.Metadata.Name; got != want {
				t.Fatalf("Project name = %q, want %q", got, want)
			}
			if registered.Project.Metadata.Name == "project" {
				t.Fatal("automatic Project naming must never use the legacy `project` fallback")
			}
			if err := reg.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSameRootReRegistrationReusesTheBasenameNamedProject(t *testing.T) {
	t.Parallel()

	m := realDirMutator()
	reg := NewRegistry()
	root := mkdirRoot(t, "myrepo")
	first, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: root, DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	before := mustJSON(t, reg)
	for _, name := range []string{"", "myrepo"} {
		again, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: root, Name: name, DefaultShell: "/bin/zsh"})
		if err != nil || !again.Reused || again.Project.Metadata.UID != first.Project.Metadata.UID {
			t.Fatalf("re-register with name %q = %+v, %v; want reuse of %q", name, again.Project.Metadata, err, first.Project.Metadata.UID)
		}
	}
	if _, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: root, Name: "renamed", DefaultShell: "/bin/zsh"}); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("re-register with a different explicit name = %v, want ErrNameConflict", err)
	}
	if mustJSON(t, reg) != before {
		t.Fatal("same-root re-registration mutated the Registry")
	}
}

func TestIsProjectUIDShapedMatchesOnlyWhatNewUIDMintsForAProject(t *testing.T) {
	t.Parallel()

	minted, err := NewUID(KindProject)
	if err != nil {
		t.Fatal(err)
	}
	window, err := NewUID(KindWindow)
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.TrimPrefix(minted, "proj-")
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "minted Project uid", value: minted, want: true},
		{name: "all-zero payload", value: "proj-" + strings.Repeat("a", len(payload)), want: true},
		{name: "ordinary proj- name", value: "proj-front"},
		{name: "plain name", value: "stable-project"},
		{name: "Window uid", value: window},
		{name: "Window prefix on a Project payload", value: "win-" + payload},
		{name: "uppercase payload", value: "proj-" + strings.ToUpper(payload)},
		{name: "short payload", value: "proj-" + payload[1:]},
		{name: "long payload", value: minted + "a"},
		{name: "padding alphabet", value: "proj-" + payload[:len(payload)-1] + "="},
		{name: "digit outside base32", value: "proj-" + payload[:len(payload)-1] + "8"},
		{name: "non-canonical trailing bits", value: "proj-" + strings.Repeat("a", len(payload)-1) + "b"},
		{name: "empty", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isProjectUIDShaped(tt.value); got != tt.want {
				t.Fatalf("isProjectUIDShaped(%q) = %t, want %t", tt.value, got, tt.want)
			}
		})
	}
}

func TestPlanProjectFreshReplacementInheritsOnlyANonUIDShapedName(t *testing.T) {
	t.Parallel()

	predecessor, err := NewUID(KindProject)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		// oldName returns the name the old Project carries; empty keeps the
		// basename it was registered with.
		oldName func(project Project) string
		want    string
	}{
		{name: "own exact uid is replaced by the basename", oldName: func(p Project) string { return p.Metadata.UID }, want: "alpha"},
		{name: "another Project's uid is replaced by the basename", oldName: func(Project) string { return predecessor }, want: "alpha"},
		{name: "explicit name is inherited", oldName: func(Project) string { return "stable-project" }, want: "stable-project"},
		{name: "proj- prefixed ordinary name is inherited", oldName: func(Project) string { return "proj-front" }, want: "proj-front"},
		{name: "basename name is inherited", oldName: func(p Project) string { return p.Metadata.Name }, want: "alpha"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := testMutator(dirSet{"/srv/alpha": true})
			m.NewUID = NewUID
			reg := NewRegistry()
			registered, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: "/srv/alpha", DefaultShell: "/bin/zsh"})
			if err != nil {
				t.Fatal(err)
			}
			old, err := m.RenameProject(&reg, registered.Project.Metadata.UID, tt.oldName(registered.Project))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := PlanProjectFreshReplacement(reg, old.Metadata.UID, RegisterProjectOptions{DefaultShell: "/bin/zsh"}, m)
			if err != nil {
				t.Fatal(err)
			}
			fresh, ok := plan.Desired.Project(plan.NewProjectUID)
			if !ok {
				t.Fatalf("Fresh plan has no new Project %q", plan.NewProjectUID)
			}
			if fresh.Metadata.Name != tt.want {
				t.Fatalf("Fresh Project name = %q (old %q), want %q", fresh.Metadata.Name, old.Metadata.Name, tt.want)
			}
			if owner, _ := plan.Desired.nameOwner("", KindProject, tt.want); owner != plan.NewProjectUID {
				t.Fatalf("name %q reservation owner = %q, want %q", tt.want, owner, plan.NewProjectUID)
			}
			if err := plan.Desired.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlanProjectFreshReplacementFallsBackToTheUIDWhenTheBasenameIsTaken(t *testing.T) {
	t.Parallel()

	m := testMutator(dirSet{"/srv/alpha": true, "/other/alpha": true})
	m.NewUID = NewUID
	reg := NewRegistry()
	holder, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: "/other/alpha", DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: "/srv/alpha", DefaultShell: "/bin/zsh"})
	if err != nil {
		t.Fatal(err)
	}
	if holder.Project.Metadata.Name != "alpha" || target.Project.Metadata.Name != target.Project.Metadata.UID {
		t.Fatalf("fixture names = %q / %q", holder.Project.Metadata.Name, target.Project.Metadata.Name)
	}
	plan, err := PlanProjectFreshReplacement(reg, target.Project.Metadata.UID, RegisterProjectOptions{DefaultShell: "/bin/zsh"}, m)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := plan.Desired.Project(plan.NewProjectUID)
	if fresh == nil || fresh.Metadata.Name != plan.NewProjectUID {
		t.Fatalf("Fresh Project = %+v, want its own exact uid as name", fresh)
	}
	if kept, _ := plan.Desired.Project(holder.Project.Metadata.UID); kept == nil || kept.Metadata.Name != "alpha" {
		t.Fatalf("basename holder changed: %+v", kept)
	}
}
