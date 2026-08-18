package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// fakeProjectRegistrar records the explicit Project bootstrap of one open.
type fakeProjectRegistrar struct {
	calls []string
	uid   string
	// reused reports the path as already registered, so the open is not a first
	// bootstrap.
	reused bool
	err    error
}

func (f *fakeProjectRegistrar) RegisterProjectRoot(_ context.Context, root string) (string, bool, error) {
	f.calls = append(f.calls, root)
	if f.err != nil {
		return "", false, f.err
	}
	return f.uid, !f.reused, nil
}

// TestOpeningACandidateRegistersExactlyThatPath is acceptance (3) at the switch
// boundary.
//
// Opening a directory from the sidebar is the explicit gesture that makes it a
// Project, and it registers the path being opened -- one call, that exact spelling.
// Nothing about the discovery root it came from is registered along with it.
//
// The first open then starts its session through the shipped ensure path rather
// than the topology engine, because the two would build the same first session on
// two different servers; see materializeAndOpenProjectTopology.
func TestOpeningACandidateRegistersExactlyThatPath(t *testing.T) {
	t.Parallel()

	registrar := &fakeProjectRegistrar{uid: "proj-new"}
	topology := &fakeProjectTopologyMaterializer{materialized: true}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions:         executor,
		identity:         stubSwitchIdentityResolver{name: "workspace"},
		homeDir:          func() (string, error) { return t.TempDir(), nil },
		lookupEnv:        func(string) string { return "" },
		projectTopology:  topology,
		projectRegistrar: registrar,
	}

	if err := cmd.openProjectTarget(context.Background(), "/srv/work/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if got, want := registrar.calls, []string{"/srv/work/workspace"}; !equalStrings(got, want) {
		t.Fatalf("registration calls = %q, want %q", got, want)
	}
	if len(topology.calls) != 0 {
		t.Fatalf("a first bootstrap open used the topology engine: %q", topology.calls)
	}
	if got, want := executor.calls, []string{"authorize:/srv/work/workspace", "ensure:workspace", "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// TestOpeningAnAlreadyRegisteredProjectStillUsesTheTopologyEngine is the other side
// of the same rule: only the open that created the Project takes the ensure path.
func TestOpeningAnAlreadyRegisteredProjectStillUsesTheTopologyEngine(t *testing.T) {
	t.Parallel()

	registrar := &fakeProjectRegistrar{uid: "proj-existing"}
	registrar.reused = true
	topology := &fakeProjectTopologyMaterializer{materialized: true}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions:         executor,
		identity:         stubSwitchIdentityResolver{name: "workspace"},
		homeDir:          func() (string, error) { return t.TempDir(), nil },
		lookupEnv:        func(string) string { return "" },
		projectTopology:  topology,
		projectRegistrar: registrar,
	}

	if err := cmd.openProjectTarget(context.Background(), "/srv/work/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if got, want := registrar.calls, []string{"/srv/work/workspace"}; !equalStrings(got, want) {
		t.Fatalf("registration calls = %q, want %q", got, want)
	}
	if got, want := topology.calls, []string{"topology:/srv/work/workspace:workspace"}; !equalStrings(got, want) {
		t.Fatalf("topology calls = %q, want %q", got, want)
	}
}

// TestADeniedTrustPromptRegistersNothing keeps the bootstrap behind the gate that
// decides whether the directory is opened at all. Declining to open a directory is
// not the moment to record that it is a Project.
func TestADeniedTrustPromptRegistersNothing(t *testing.T) {
	t.Parallel()

	registrar := &fakeProjectRegistrar{uid: "proj-new"}
	topology := &fakeProjectTopologyMaterializer{materialized: true}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: false}
	cmd := &switchCommand{
		sessions:         executor,
		identity:         stubSwitchIdentityResolver{name: "workspace"},
		homeDir:          func() (string, error) { return t.TempDir(), nil },
		lookupEnv:        func(string) string { return "" },
		projectTopology:  topology,
		projectRegistrar: registrar,
	}

	err := cmd.openProjectTarget(context.Background(), "/srv/work/workspace", "workspace")
	if !errors.Is(err, errProjectTrustDenied) {
		t.Fatalf("openProjectTarget() error = %v, want errProjectTrustDenied", err)
	}
	if len(registrar.calls) != 0 {
		t.Fatalf("a denied trust prompt registered %q", registrar.calls)
	}
	if len(topology.calls) != 0 {
		t.Fatalf("a denied trust prompt materialized %q", topology.calls)
	}
}

// TestOpeningHomeRegistersNothing keeps the Home row chrome.
//
// Home is offered by filesystem discovery and leads the Projects list, so it is a
// row an operator can select. Selecting it opens a session and nothing else:
// `$HOME` alone has never been evidence of a Project and the explicit-bootstrap
// gesture does not make it one.
func TestOpeningHomeRegistersNothing(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	registrar := &fakeProjectRegistrar{uid: "proj-home"}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions:         executor,
		identity:         stubSwitchIdentityResolver{name: "home"},
		homeDir:          func() (string, error) { return home, nil },
		lookupEnv:        func(string) string { return "" },
		projectRegistrar: registrar,
	}

	if err := cmd.openProjectTarget(context.Background(), home, "home"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if len(registrar.calls) != 0 {
		t.Fatalf("opening Home registered %q", registrar.calls)
	}
	// The open itself still happened; only the registration was withheld.
	if got, want := executor.calls, []string{"authorize:" + home, "ensure:home", "open:home"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// TestRegisterProjectRootIsExactAndIdempotent is the seam both the sidebar open and
// `create project` share, against the real on-disk store.
//
// It asserts the two halves acceptance (3) names: only the selected path becomes a
// Project, and registering it again performs no write at all.
func TestRegisterProjectRootIsExactAndIdempotent(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	selected := filepath.Join(base, "work", "selected")
	sibling := filepath.Join(base, "work", "sibling")
	for _, root := range []string{selected, sibling} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	registryPath := intmetadata.PathFor(filepath.Join(base, "state"))
	backing := intmetadata.NewStore(registryPath)
	store := &resourceStore{
		load:             backing.LoadReadOnly,
		update:           backing.Update,
		updateConvergent: backing.UpdateConvergent,
		mutator: func() coremetadata.Mutator {
			return coremetadata.Mutator{Now: time.Now, NewUID: coremetadata.NewUID, DirExists: intmetadata.DirExists}
		},
	}

	uid, created, err := registerProjectRoot(context.Background(), store, "/bin/zsh", filepath.Base, selected)
	if err != nil {
		t.Fatalf("registerProjectRoot() error = %v", err)
	}
	if !created {
		t.Fatal("the first registration did not report a new Project")
	}
	registry, err := backing.LoadReadOnly()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(registry.Projects) != 1 {
		t.Fatalf("projects = %d, want only the selected path", len(registry.Projects))
	}
	if got := registry.Projects[0].Spec.Root; got != selected {
		t.Fatalf("registered root = %q, want %q", got, selected)
	}
	if _, ok := registry.ProjectByRoot(sibling); ok {
		t.Fatal("a sibling candidate under the same discovery root was registered too")
	}

	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	repeatUID, repeatCreated, err := registerProjectRoot(context.Background(), store, "/bin/zsh", filepath.Base, selected)
	if err != nil {
		t.Fatalf("repeat registerProjectRoot() error = %v", err)
	}
	if repeatUID != uid {
		t.Fatalf("repeat uid = %q, want the first %q", repeatUID, uid)
	}
	if repeatCreated {
		t.Fatal("the repeat reported a new Project")
	}
	after, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("re-read registry: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("a repeated registration rewrote the Registry; the repeat must be write-free")
	}
}

// TestRegisterProjectRootRefusesARelativePath keeps an unresolved spelling from
// becoming a Project root.
func TestRegisterProjectRootRefusesARelativePath(t *testing.T) {
	t.Parallel()

	if _, _, err := registerProjectRoot(context.Background(), nil, "/bin/zsh", filepath.Base, "work/relative"); err == nil {
		t.Fatal("a relative root must be refused")
	}
}
