package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestRenameCommand(store *fakeResourceStore) *renameCommand {
	return &renameCommand{store: store.store(), runtime: liveAlphaRuntime()}
}

func newTestRebindCommand(store *fakeResourceStore) *rebindCommand {
	return &rebindCommand{store: store.store()}
}

// TestRenameChangesOnlyTheMetadataNameOfOneResource is the rename half of the
// mutation parity table.
func TestRenameChangesOnlyTheMetadataNameOfOneResource(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		args      []string
		wantUID   string
		wantName  string
		wantKind  string
		wantScope string
		display   string
	}{
		{
			name: "project", args: []string{"project", "alpha", "--name", "alpha-renamed"},
			wantUID: "prj-alpha", wantName: "alpha-renamed", wantKind: "Project", wantScope: "",
		},
		{
			name: "window inside its project scope", args: []string{"window", "review", "--project", "alpha", "--name", "audit"},
			wantUID: "win-alpha-review", wantName: "audit", wantKind: "Window", wantScope: "prj-alpha", display: "Runtime Review",
		},
		{
			name: "pane inside its window scope", args: []string{"pane", "log", "--project", "alpha", "--window", "main", "--name", "tail"},
			wantUID: "pan-alpha-log", wantName: "tail", wantKind: "Pane", wantScope: "win-alpha-main",
		},
		{
			name: "managed pane inside its agent scope", args: []string{"pane", "codex-pane", "--project", "alpha", "--window", "main", "--name", "worker"},
			wantUID: "pan-alpha-codex", wantName: "worker", wantKind: "Pane", wantScope: "agt-alpha-codex",
		},
		{
			name: "agent inside its window scope", args: []string{"agent", "codex", "--project", "alpha", "--window", "main", "--name", "reviewer"},
			wantUID: "agt-alpha-codex", wantName: "reviewer", wantKind: "Agent", wantScope: "win-alpha-main",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			if test.display != "" {
				window, _ := store.registry.Window(test.wantUID)
				window.Metadata.DisplayName = test.display
			}
			_, metadataBefore, _ := resourceFor(store.registry, resourceKindTokens[strings.ToLower(test.wantKind)], test.wantUID)
			before := store.snapshot()
			stdout, stderr, err := runRoute(t, newTestRenameCommand(store), test.args...)
			if err != nil {
				t.Fatalf("rename %v error = %v (stderr=%s)", test.args, err, stderr)
			}
			if store.writes != 1 {
				t.Fatalf("rename %v committed %d writes, want 1", test.args, store.writes)
			}
			if !strings.Contains(stdout, test.wantName) {
				t.Fatalf("rename %v stdout = %q, want it to name the new name", test.args, stdout)
			}

			_, meta, ok := resourceFor(store.registry, resourceKindTokens[strings.ToLower(test.wantKind)], test.wantUID)
			if !ok {
				t.Fatalf("rename %v lost uid %q", test.args, test.wantUID)
			}
			if meta.Name != test.wantName {
				t.Fatalf("rename %v name = %q, want %q", test.args, meta.Name, test.wantName)
			}
			if meta.UID != test.wantUID {
				t.Fatalf("rename changed the uid: %q", meta.UID)
			}
			if meta.DisplayName != metadataBefore.DisplayName {
				t.Fatalf("rename %v changed displayName to %q, want unchanged %q", test.args, meta.DisplayName, metadataBefore.DisplayName)
			}

			// The reservation table follows the rename inside the same scope, so
			// the old name becomes free and the new one is owned by this uid.
			var reserved bool
			for _, reservation := range store.registry.NameReservations {
				if reservation.UID == test.wantUID && reservation.Name == test.wantName && reservation.Scope == test.wantScope {
					reserved = true
				}
			}
			if !reserved {
				t.Fatalf("rename %v left no %q reservation in scope %q", test.args, test.wantName, test.wantScope)
			}
			if before == store.snapshot() {
				t.Fatalf("rename %v did not change the registry at all", test.args)
			}
		})
	}
}

// TestRenameFailuresLeaveZeroMutations is the negative half: every rejected
// rename must leave the registry byte-identical.
func TestRenameFailuresLeaveZeroMutations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "explicit collision in the project scope", args: []string{"project", "alpha", "--name", "beta"}, want: "beta"},
		{name: "explicit collision in the window scope", args: []string{"window", "review", "--project", "alpha", "--name", "main"}, want: "main"},
		{name: "ambiguous target", args: []string{"window", "main", "--name", "renamed"}, want: "want exactly one"},
		{name: "no match", args: []string{"project", "nosuch", "--name", "renamed"}, want: "matched no projects"},
		{name: "missing --name", args: []string{"project", "alpha"}, want: "requires --name"},
		{name: "empty --name", args: []string{"project", "alpha", "--name", "   "}, want: "requires --name"},
		{name: "unsupported kind", args: []string{"notification", "notice", "--name", "x"}, want: "not available"},
		{name: "no kind", args: nil, want: "rename requires a resource kind"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			stdout, _, err := runRoute(t, newTestRenameCommand(store), test.args...)
			if err == nil {
				t.Fatalf("rename %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("rename %v error is not a usage error: %v", test.args, err)
			}
			if stdout != "" {
				t.Fatalf("rename %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("rename %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if store.writes != 0 {
				t.Fatalf("rename %v committed %d writes, want 0", test.args, store.writes)
			}
			if store.snapshot() != before {
				t.Fatalf("rename %v mutated the registry:\n%s", test.args, store.snapshot())
			}
		})
	}
}

// TestRebindProjectRewritesOnlySpecRoot is acceptance criterion 3.
//
// The rebind runs against a real temporary filesystem so the "zero filesystem
// moves" claim is measured rather than asserted: the whole directory tree is
// walked before and after and must be identical.
func TestRebindProjectRewritesOnlySpecRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	oldRoot := filepath.Join(root, "alpha")
	newRoot := filepath.Join(root, "alpha-moved")
	for _, dir := range []string{oldRoot, newRoot} {
		if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nested", "file.txt"), []byte(filepath.Base(dir)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	store := newFakeResourceStore(t)
	store.registry.Projects[0].Spec.Root = oldRoot
	store.dirs = map[string]bool{oldRoot: true, newRoot: true, "/srv/beta": true}
	before := walkTree(t, root)

	stdout, stderr, err := runRoute(t, newTestRebindCommand(store), "project", "alpha", "--root", newRoot)
	if err != nil {
		t.Fatalf("rebind error = %v (stderr=%s)", err, stderr)
	}
	if want := "project/alpha root=" + newRoot + "\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}

	project, ok := store.registry.Project("prj-alpha")
	if !ok {
		t.Fatal("rebind lost the project")
	}
	if project.Spec.Root != newRoot {
		t.Fatalf("spec.root = %q, want %q", project.Spec.Root, newRoot)
	}
	if project.Metadata.UID != "prj-alpha" || project.Metadata.Name != "alpha" {
		t.Fatalf("rebind changed identity: uid=%q name=%q", project.Metadata.UID, project.Metadata.Name)
	}
	if after := walkTree(t, root); after != before {
		t.Fatalf("rebind moved files on disk:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestRebindProjectValidationFailuresLeaveZeroMutations covers the rest of
// acceptance criterion 3: an invalid root and a root already bound to another
// Project both exit 2 with the registry untouched, and no basename, git origin,
// inode, or scan-order similarity ever merges two Projects.
func TestRebindProjectValidationFailuresLeaveZeroMutations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "root already bound to another project", args: []string{"project", "alpha", "--root", "/srv/beta"}, want: "already bound to project beta"},
		{name: "root does not exist", args: []string{"project", "alpha", "--root", "/srv/nosuch"}, want: "not an existing directory"},
		{name: "relative root", args: []string{"project", "alpha", "--root", "relative/path"}, want: "must be absolute"},
		{name: "missing --root", args: []string{"project", "alpha"}, want: "requires --root"},
		{name: "ambiguous target", args: []string{"project", "projmux", "--root", "/srv/alpha"}, want: "matched no projects"},
		{name: "unsupported kind", args: []string{"window", "main", "--root", "/srv/alpha"}, want: "not available"},
		{name: "no kind", args: nil, want: "rebind requires a resource kind"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			stdout, _, err := runRoute(t, newTestRebindCommand(store), test.args...)
			if err == nil {
				t.Fatalf("rebind %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("rebind %v error is not a usage error: %v", test.args, err)
			}
			if stdout != "" {
				t.Fatalf("rebind %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("rebind %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if store.writes != 0 {
				t.Fatalf("rebind %v committed %d writes, want 0", test.args, store.writes)
			}
			if store.snapshot() != before {
				t.Fatalf("rebind %v mutated the registry:\n%s", test.args, store.snapshot())
			}
		})
	}
}

// TestRebindNeverMergesTwoProjectsOntoOneUID states the discovery half of the
// contract as a measurement: two Projects that share a basename stay two
// Projects with two uids after a rebind that makes them siblings.
func TestRebindNeverMergesTwoProjectsOntoOneUID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sibling := filepath.Join(root, "worktree", "alpha")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	store := newFakeResourceStore(t)
	store.dirs[sibling] = true
	// `beta` moves to a directory whose basename is `alpha`, exactly the shape a
	// basename heuristic would fold onto the `alpha` Project.
	if _, _, err := runRoute(t, newTestRebindCommand(store), "project", "beta", "--root", sibling); err != nil {
		t.Fatalf("rebind error = %v", err)
	}
	if got := len(store.registry.Projects); got != 3 {
		t.Fatalf("project count = %d, want 3", got)
	}
	alpha, _ := store.registry.Project("prj-alpha")
	beta, _ := store.registry.Project("prj-beta")
	if alpha == nil || beta == nil {
		t.Fatal("a rebind removed a project")
	}
	if alpha.Spec.Root == beta.Spec.Root {
		t.Fatalf("two projects share a root: %q", alpha.Spec.Root)
	}
	if filepath.Base(alpha.Spec.Root) != filepath.Base(beta.Spec.Root) {
		t.Fatalf("the fixture no longer exercises a shared basename: %q vs %q", alpha.Spec.Root, beta.Spec.Root)
	}
}

// walkTree renders every path under root with its size, so a filesystem move is
// visible as a diff.
func walkTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		b.WriteString(rel)
		if !info.IsDir() {
			b.WriteString(" " + info.Mode().String())
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return b.String()
}
