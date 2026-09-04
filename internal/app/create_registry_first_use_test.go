package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The registry file has never been created by a production path before this
// Phase: every earlier Phase either read it with LoadReadOnly or wrote it only
// in an in-memory fake. This file exercises the real store -- the O_CREATE|O_EXCL
// lock, the retry/jitter loop, and the temp-write -> validate -> atomic-rename
// write -- against a real filesystem.

// onDiskFixture is one isolated state directory plus its runtime.
type onDiskFixture struct {
	stateDir string
	roots    []string
	tmux     *fakeTmux
}

func newOnDiskFixture(t *testing.T, projects ...string) *onDiskFixture {
	t.Helper()
	base := t.TempDir()
	fixture := &onDiskFixture{stateDir: filepath.Join(base, "state"), tmux: newFakeTmux()}
	for _, name := range projects {
		root := filepath.Join(base, "work", name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create project root: %v", err)
		}
		fixture.roots = append(fixture.roots, root)
	}
	return fixture
}

func (f *onDiskFixture) registryPath() string { return intmetadata.PathFor(f.stateDir) }

// command builds a create command backed by the real locked registry file.
func (f *onDiskFixture) command(observe func(context.Context, string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error)) *createCommand {
	store := intmetadata.NewStore(f.registryPath())
	mirror := intmetadata.NewMirror(f.tmux)
	if observe == nil {
		observe = func(context.Context, string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
			return coremetadata.LegacySession{}, intmetadata.LegacyTargets{}, nil
		}
	}
	roots := slices.Clone(f.roots)
	typedMirror := runtimeMutationMetadataMirror{runner: explicitTmuxRunner{
		runner: f.tmux, target: tmuxTransport{Kind: tmuxSocketPath, Value: f.tmux.socketPath, Source: tmuxSocketPathSource},
	}}
	return &createCommand{
		store: &resourceStore{
			load:             store.LoadReadOnly,
			update:           store.Update,
			updateConvergent: store.UpdateConvergent,
			mutator: func() coremetadata.Mutator {
				return coremetadata.Mutator{
					Now:       time.Now,
					NewUID:    coremetadata.NewUID,
					DirExists: intmetadata.DirExists,
				}
			},
		},
		reconciler: &registryReconciler{
			discoverRoots: func() ([]string, error) { return roots, nil },
			liveSessions: func(context.Context) (map[string]bool, error) {
				return f.tmux.sessionNames(), nil
			},
			observeLegacy:  observe,
			mirror:         mirror,
			mirrorProject:  typedMirror.MirrorProject,
			mirrorWindow:   typedMirror.MirrorWindow,
			mirrorPane:     typedMirror.MirrorPane,
			shell:          "/bin/zsh",
			sessionNameFor: filepath.Base,
		},
		runtime: &materializer{
			runner:   f.tmux,
			mirror:   mirror,
			sessions: &fakeSessionMaterializer{tmux: f.tmux},
			target:   tmuxTransport{Kind: tmuxSocketPath, Value: f.tmux.socketPath, Source: tmuxSocketPathSource},
			warn:     io.Discard,
		},
		shell:          "/bin/zsh",
		sessionNameFor: filepath.Base,
		newOperationID: newCreateOperationID,
	}
}

// register performs the explicit Project bootstrap through the real store.
//
// Every test below that used to rely on a create registering its own Projects now
// says so out loud, because that is the change: a scan no longer decides
// membership, and `create project` is the route that does.
func (f *onDiskFixture) register(t *testing.T, roots ...string) {
	t.Helper()
	for _, root := range roots {
		if _, _, err := runRoute(t, f.command(nil), "project", "--root", root); err != nil {
			t.Fatalf("register project %q: %v", root, err)
		}
	}
}

func (f *onDiskFixture) load(t *testing.T) coremetadata.Registry {
	t.Helper()
	registry, err := intmetadata.NewStore(f.registryPath()).LoadReadOnly()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return registry
}

// TestTheFirstMutationCreatesTheRegistryFromACompletelyEmptyState is the
// first-real-creation check, and it is now also the exact-path check.
//
// The first mutation is an explicit `create project`, and the fixture has two
// discovered workdirs. Exactly one of them becomes a Project: the one that was
// named. `beta` is discovered, adjacent, and sitting under the same scan root, and
// it stays unregistered -- which under the previous behaviour it would not have.
func TestTheFirstMutationCreatesTheRegistryFromACompletelyEmptyState(t *testing.T) {
	t.Parallel()

	fixture := newOnDiskFixture(t, "alpha", "beta")
	if _, err := os.Stat(fixture.registryPath()); !os.IsNotExist(err) {
		t.Fatalf("the fixture did not start from an empty state: %v", err)
	}

	stdout, stderr, err := runRoute(t, fixture.command(nil), "project", "--root", fixture.roots[0])
	if err != nil {
		t.Fatalf("register error = %v (stderr %q)", err, stderr)
	}
	if got, want := stdout, "project/alpha created\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	info, err := os.Stat(fixture.registryPath())
	if err != nil {
		t.Fatalf("the first mutation did not create the registry: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("registry mode = %v, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(fixture.registryPath()))
	if err != nil {
		t.Fatalf("stat registry dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("registry dir mode = %v, want 0700", got)
	}
	// The lock file is released, not leaked.
	if _, err := os.Stat(fixture.registryPath() + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("the store lock was left behind: %v", err)
	}

	registry := fixture.load(t)
	if registry.SchemaVersion != coremetadata.SchemaVersion || registry.APIVersion != coremetadata.APIVersion {
		t.Fatalf("envelope = %d/%s", registry.SchemaVersion, registry.APIVersion)
	}
	var names []string
	for _, project := range registry.Projects {
		names = append(names, project.Metadata.Name)
		windows := registry.WindowsOf(project.Metadata.UID)
		if len(windows) == 0 {
			t.Fatalf("project %s registered with no bootstrap Window", project.Metadata.Name)
		}
		if _, ok := registry.Pane(windows[0].Spec.AnchorPaneRef); !ok {
			t.Fatalf("project %s bootstrap Window has no resolvable default shell ref", project.Metadata.Name)
		}
	}
	if !slices.Equal(names, []string{"alpha"}) {
		t.Fatalf("registered projects = %v, want only the named root; a discovered sibling must stay unregistered", names)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("the written registry does not validate: %v", err)
	}

	// Registering the same root again is a write-free no-op that still reports the
	// same Project.
	before, err := os.ReadFile(fixture.registryPath())
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	repeatOut, _, err := runRoute(t, fixture.command(nil), "project", "--root", fixture.roots[0])
	if err != nil {
		t.Fatalf("repeat register error = %v", err)
	}
	if got, want := repeatOut, "project/alpha created\n"; got != want {
		t.Fatalf("repeat stdout = %q, want %q", got, want)
	}
	after, err := os.ReadFile(fixture.registryPath())
	if err != nil {
		t.Fatalf("re-read registry: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("registering an already-registered root rewrote the Registry")
	}

	// And the Project the bootstrap created is the one the ordinary create routes
	// resolve.
	windowOut, windowErr, err := runRoute(t, fixture.command(nil), "window", "--project", "alpha")
	if err != nil {
		t.Fatalf("create window error = %v (stderr %q)", err, windowErr)
	}
	if !strings.HasPrefix(windowOut, "window/") {
		t.Fatalf("stdout = %q", windowOut)
	}
}

// TestCreateRefusesAnUnregisteredDiscoveredWorkdirWithABootstrapInstruction is the
// other half of the same change. A discovered directory is not a Project, and the
// refusal says which route would make it one instead of quietly registering it.
func TestCreateRefusesAnUnregisteredDiscoveredWorkdirWithABootstrapInstruction(t *testing.T) {
	t.Parallel()

	fixture := newOnDiskFixture(t, "alpha")
	stdout, _, err := runRoute(t, fixture.command(nil), "window", "--project", "alpha")
	if err == nil {
		t.Fatal("create resolved a Project that nothing registered")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	for _, want := range []string{"no Registry Project is named", fixture.roots[0], "projmux create project --root"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(fixture.registryPath()); !os.IsNotExist(statErr) {
		t.Fatalf("the refusal created Registry state: %v", statErr)
	}
}

func TestFirstUseDiscoveredProjectRefusesBlankSameNameSessionBeforeRegistryOrMirror(t *testing.T) {
	t.Parallel()
	fixture := newOnDiskFixture(t, "alpha")
	foreign := fixture.tmux.addSession("alpha")
	before := fixture.tmux.state()
	stdout, _, err := runRoute(t, fixture.command(nil), "window", "--project", "alpha")
	if err == nil || stdout != "" {
		t.Fatalf("stdout/error = %q / %v", stdout, err)
	}
	if _, statErr := os.Stat(fixture.registryPath()); !os.IsNotExist(statErr) {
		t.Fatalf("refusal created Registry state: %v", statErr)
	}
	if fixture.tmux.state() != before || foreign.opts[tmuxopts.ProjectUIDSession] != "" || foreign.windows[0].opts[tmuxopts.WindowUID] != "" || foreign.windows[0].panes[0].opts[tmuxopts.PaneUID] != "" {
		t.Fatalf("first-use refusal mutated foreign runtime:\n%s", fixture.tmux.state())
	}
	if fixture.tmux.argvContains("set-option") || fixture.tmux.argvContains("set-environment") || fixture.tmux.argvContains("new-window") {
		t.Fatalf("first-use refusal reached a mutation: %v", fixture.tmux.calls)
	}
}

func TestFirstUseDiscoveredProjectRefusesRootClaimedByOtherSession(t *testing.T) {
	t.Parallel()
	fixture := newOnDiskFixture(t, "alpha")
	foreign := fixture.tmux.addSession("different-name")
	foreign.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
	foreign.opts[tmuxopts.ProjectPathSession] = fixture.roots[0]
	before := fixture.tmux.state()
	stdout, _, err := runRoute(t, fixture.command(nil), "window", "--project", "alpha")
	if err == nil || stdout != "" {
		t.Fatalf("stdout/error = %q / %v", stdout, err)
	}
	if _, statErr := os.Stat(fixture.registryPath()); !os.IsNotExist(statErr) {
		t.Fatalf("root-claim refusal created Registry state: %v", statErr)
	}
	if fixture.tmux.state() != before || fixture.tmux.argvContains("set-option") || fixture.tmux.argvContains("set-environment") || fixture.tmux.argvContains("new-window") {
		t.Fatalf("root-claim refusal mutated foreign runtime:\n%s", fixture.tmux.state())
	}
}

// TestASecondMutationExtendsTheExistingRegistryWithoutRenumbering is the
// existing-registry check.
func TestASecondMutationExtendsTheExistingRegistryWithoutRenumbering(t *testing.T) {
	t.Parallel()

	fixture := newOnDiskFixture(t, "alpha", "beta")
	fixture.register(t, fixture.roots...)
	if _, _, err := runRoute(t, fixture.command(nil), "window", "--project", "alpha"); err != nil {
		t.Fatalf("first create error = %v", err)
	}
	first := fixture.load(t)
	identity := map[string]string{}
	for _, project := range first.Projects {
		identity[project.Metadata.UID] = project.Metadata.Name
	}
	firstBytes, err := os.ReadFile(fixture.registryPath())
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}

	if _, _, err := runRoute(t, fixture.command(nil), "pane", "--project", "beta", "--window", "zsh"); err != nil {
		t.Fatalf("second create error = %v", err)
	}
	second := fixture.load(t)

	if len(second.Projects) != len(first.Projects) {
		t.Fatalf("projects = %d, want %d; the second run re-registered", len(second.Projects), len(first.Projects))
	}
	for _, project := range second.Projects {
		if identity[project.Metadata.UID] != project.Metadata.Name {
			t.Fatalf("project %s was renumbered or re-identified: %+v", project.Metadata.UID, project.Metadata)
		}
	}
	if string(firstBytes) == "" {
		t.Fatal("the first write produced no bytes")
	}
	if err := second.Validate(); err != nil {
		t.Fatalf("registry does not validate after the second write: %v", err)
	}
}

// TestConcurrentCreatesSerializeOnTheOnDiskLockAndConvergeOnOneWindow is the
// fake-tmux/on-disk owner for lost-update and Registry owner convergence. Real
// tmux materialization and mirror convergence belong to L06 E2E.
func TestConcurrentCreatesSerializeOnTheOnDiskLockAndConvergeOnOneWindow(t *testing.T) {
	t.Parallel()

	const racers = 6
	fixture := newOnDiskFixture(t, "alpha")
	fixture.register(t, fixture.roots...)

	var wg sync.WaitGroup
	errs := make([]error, racers)
	outs := make([]string, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			stdout, _, err := runRoute(t, fixture.command(nil),
				"pane", "--project", "alpha", "--window", "shared", "--create-window", "-o", "uid")
			outs[i], errs[i] = stdout, err
		})
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d failed: %v", i, err)
		}
	}

	registry := fixture.load(t)
	if err := registry.Validate(); err != nil {
		t.Fatalf("the raced registry does not validate: %v", err)
	}
	var shared []string
	for _, window := range registry.Windows {
		if window.Metadata.Name == "shared" {
			shared = append(shared, window.Metadata.UID)
		}
	}
	if len(shared) != 1 {
		t.Fatalf("Windows named `shared` = %v, want exactly one uid", shared)
	}
	// Every racer produced its own Pane below that one Window, so serialization
	// converged instead of dropping work.
	panes := registry.PanesOf(shared[0])
	if len(panes) != racers+1 {
		t.Fatalf("panes below the shared Window = %d, want %d (one initial plus one per racer)", len(panes), racers+1)
	}
	uids := map[string]bool{}
	for _, out := range outs {
		uids[strings.TrimSpace(out)] = true
	}
	if len(uids) != racers {
		t.Fatalf("distinct result uids = %d, want %d", len(uids), racers)
	}
}

// TestTheFirstLegacyMigrationAllocatesStableNamesAndProjectsRuntimeDisplayNames
// is the legacy-state check at the reconciler and describe seams.
//
// The tmux ids come back from the observation, so the migration can mirror the
// uids it allocated onto exactly the objects it imported. Without that, a
// migrated Window has registry identity and no transport binding, and the next
// create builds a duplicate next to it.
func TestTheFirstLegacyMigrationAllocatesStableNamesAndProjectsRuntimeDisplayNames(t *testing.T) {
	t.Parallel()

	fixture := newOnDiskFixture(t)
	root := t.TempDir()
	targetRoot := filepath.Join(t.TempDir(), "create-target")
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatalf("create safe target root: %v", err)
	}
	fixture.roots = append(fixture.roots, targetRoot)
	session := fixture.tmux.addSession("legacy")
	// Two pre-v2 windows with every formerly trusted runtime seed populated. None
	// of those values may become metadata.name; window_name is displayName only.
	editor := session.windows[0]
	editor.name = "editor"
	extra := &fakeTmuxWindow{id: fixture.tmux.mint("@"), name: "zsh", opts: map[string]string{}}
	extra.panes = append(extra.panes, &fakeTmuxPane{id: fixture.tmux.mint("%"), opts: map[string]string{}})
	session.windows = append(session.windows, extra)

	// The observation reports the uid each live object currently carries, the
	// same way ObserveLegacySessionTargets does. That is what makes the second
	// pass below a rebind rather than a fresh import: the objects the first pass
	// mirrored come back carrying their bindings.
	observe := func(_ context.Context, name string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
		if name != "legacy" {
			return coremetadata.LegacySession{}, intmetadata.LegacyTargets{}, nil
		}
		return coremetadata.LegacySession{
				Session: "legacy",
				Root:    root,
				Windows: []coremetadata.LegacyWindow{
					{
						Name: "editor", AutomaticRename: false, UID: editor.opts[tmuxopts.WindowUID],
						Panes: []coremetadata.LegacyPane{{Command: "nvim", UID: editor.panes[0].opts[tmuxopts.PaneUID]}},
					},
					{
						Name: "zsh", AutomaticRename: true, UID: extra.opts[tmuxopts.WindowUID],
						Panes: []coremetadata.LegacyPane{{Label: "logs", Command: "tail", UID: extra.panes[0].opts[tmuxopts.PaneUID]}},
					},
				},
			}, intmetadata.LegacyTargets{
				Windows: []string{editor.id, extra.id},
				Panes:   [][]string{{editor.panes[0].id}, {extra.panes[0].id}},
			}, nil
	}

	// The create target is registered explicitly; the legacy session's own root is
	// not, because importing a live session is the reconciler's job and this test
	// is about exactly that import happening beside an unrelated create.
	fixture.register(t, targetRoot)
	if _, _, err := runRoute(t, fixture.command(observe), "pane", "--project", filepath.Base(targetRoot)); err != nil {
		t.Fatalf("create beside an unrelated legacy session error = %v", err)
	}

	registry := fixture.load(t)
	project, ok := registry.ProjectByName(filepath.Base(root))
	if !ok {
		t.Fatalf("the legacy session did not become a Project:\n%+v", registry.Projects)
	}
	windows := registry.WindowsOf(project.Metadata.UID)
	var names, displayNames []string
	for _, window := range windows {
		names = append(names, window.Metadata.Name)
		displayNames = append(displayNames, window.Metadata.DisplayName)
	}
	if !slices.Equal(names, []string{"window", "window-1"}) {
		t.Fatalf("migrated Window names = %v, want window and window-1", names)
	}
	if !slices.Equal(displayNames, []string{"editor", "zsh"}) {
		t.Fatalf("migrated Window displayNames = %v, want observed editor and zsh", displayNames)
	}
	if editor.name != "editor" || extra.name != "zsh" {
		t.Fatalf("runtime window names changed: %q/%q, want observed editor/zsh", editor.name, extra.name)
	}

	describe := &describeCommand{
		loadRegistry: func() (coremetadata.Registry, error) { return fixture.load(t), nil },
	}
	description, stderr, err := runRoute(t, describe, "window", "window", "--project", filepath.Base(root))
	if err != nil {
		t.Fatalf("describe imported window: %v (stderr=%s)", err, stderr)
	}
	hasName, hasContext, hasContextSource := false, false, false
	for line := range strings.SplitSeq(description, "\n") {
		hasName = hasName || (strings.HasPrefix(line, "Name:") && strings.TrimSpace(strings.TrimPrefix(line, "Name:")) == "window")
		hasContext = hasContext || (strings.HasPrefix(line, "Context:") && strings.TrimSpace(strings.TrimPrefix(line, "Context:")) == "nvim")
		hasContextSource = hasContextSource || (strings.HasPrefix(line, "ContextSource:") && strings.TrimSpace(strings.TrimPrefix(line, "ContextSource:")) == "command-executable")
	}
	if !hasName || !hasContext || !hasContextSource {
		t.Fatalf("describe window did not separate durable name and ephemeral context:\n%s", description)
	}
	// The allocated uids are mirrored back onto the live objects.
	if got := editor.opts[tmuxopts.WindowUID]; got == "" {
		t.Fatal("the migration did not mirror the Window uid onto the live tmux window")
	}
	if got := editor.opts["automatic-rename"]; got != "off" {
		t.Fatalf("automatic-rename = %q on a managed Window, want off", got)
	}
	if got := extra.opts[tmuxopts.WindowUID]; got == "" {
		t.Fatal("the second migrated Window was not mirrored")
	}
	if got := editor.panes[0].opts[tmuxopts.PaneUID]; got == "" {
		t.Fatal("the migration did not mirror the Pane uid onto the live tmux pane")
	}

	// The migration is one-time: a second run assigns nothing new.
	before := len(registry.Windows)
	if _, _, err := runRoute(t, fixture.command(observe), "pane", "--project", filepath.Base(targetRoot)); err != nil {
		t.Fatalf("second create error = %v", err)
	}
	after := fixture.load(t)
	if got := len(after.Windows); got != before {
		t.Fatalf("windows = %d, want %d; the legacy migration ran twice", got, before)
	}
	for _, window := range after.WindowsOf(project.Metadata.UID) {
		for _, original := range windows {
			if window.Metadata.UID == original.Metadata.UID && window.Metadata.Name != original.Metadata.Name {
				t.Fatalf("Window %s was renamed by a second migration: %q -> %q",
					window.Metadata.UID, original.Metadata.Name, window.Metadata.Name)
			}
		}
	}
}
