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
	return &createCommand{
		store: &resourceStore{
			load:   store.LoadReadOnly,
			update: store.Update,
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
			shell:          "/bin/zsh",
			sessionNameFor: filepath.Base,
		},
		runtime: &materializer{
			runner:   f.tmux,
			mirror:   mirror,
			sessions: &fakeSessionMaterializer{tmux: f.tmux},
			warn:     io.Discard,
		},
		shell:          "/bin/zsh",
		sessionNameFor: filepath.Base,
		newOperationID: newCreateOperationID,
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
// first-real-creation check.
func TestTheFirstMutationCreatesTheRegistryFromACompletelyEmptyState(t *testing.T) {
	t.Parallel()

	fixture := newOnDiskFixture(t, "alpha", "beta")
	if _, err := os.Stat(fixture.registryPath()); !os.IsNotExist(err) {
		t.Fatalf("the fixture did not start from an empty state: %v", err)
	}

	stdout, stderr, err := runRoute(t, fixture.command(nil), "window", "--project", "alpha")
	if err != nil {
		t.Fatalf("first create error = %v (stderr %q)", err, stderr)
	}
	if !strings.HasPrefix(stdout, "window/") {
		t.Fatalf("stdout = %q", stdout)
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
	// Every selectable workdir became a Project, each with a bootstrap Window
	// and a primaryPaneRef, and registration order followed the sorted roots.
	var names []string
	for _, project := range registry.Projects {
		names = append(names, project.Metadata.Name)
		windows := registry.WindowsOf(project.Metadata.UID)
		if len(windows) == 0 {
			t.Fatalf("project %s registered with no bootstrap Window", project.Metadata.Name)
		}
		if _, ok := registry.Pane(windows[0].Spec.PrimaryPaneRef); !ok {
			t.Fatalf("project %s bootstrap Window has no resolvable primaryPaneRef", project.Metadata.Name)
		}
	}
	if !slices.Contains(names, "alpha") || !slices.Contains(names, "beta") {
		t.Fatalf("registered projects = %v, want alpha and beta", names)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("the written registry does not validate: %v", err)
	}
}

// TestASecondMutationExtendsTheExistingRegistryWithoutRenumbering is the
// existing-registry check.
func TestASecondMutationExtendsTheExistingRegistryWithoutRenumbering(t *testing.T) {
	t.Parallel()

	fixture := newOnDiskFixture(t, "alpha", "beta")
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

// TestConcurrentCreatesSerializeOnTheOnDiskLockAndConvergeOnOneWindow proves the
// cross-process lock actually serializes, which has never run in production.
func TestConcurrentCreatesSerializeOnTheOnDiskLockAndConvergeOnOneWindow(t *testing.T) {
	t.Parallel()

	const racers = 6
	fixture := newOnDiskFixture(t, "alpha")

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
	// One live tmux window, one Projmux uid on it.
	live := 0
	for _, session := range fixture.tmux.sessions {
		for _, window := range session.windows {
			if window.opts[tmuxopts.WindowUID] == shared[0] {
				live++
			}
		}
	}
	if live != 1 {
		t.Fatalf("live tmux windows mirroring the shared uid = %d, want 1", live)
	}
	if _, err := os.Stat(fixture.registryPath() + ".lock"); !os.IsNotExist(err) {
		t.Fatal("the store lock was left behind after the race")
	}
}

// TestTheFirstLegacyMigrationSeedsStableNamesOnceAndMirrorsThemBack is the
// legacy-state check at the reconciler seam.
//
// The tmux ids come back from the observation, so the migration can mirror the
// uids it allocated onto exactly the objects it imported. Without that, a
// migrated Window has registry identity and no transport binding, and the next
// create builds a duplicate next to it.
func TestTheFirstLegacyMigrationSeedsStableNamesOnceAndMirrorsThemBack(t *testing.T) {
	t.Parallel()

	fixture := newOnDiskFixture(t)
	root := t.TempDir()
	session := fixture.tmux.addSession("legacy")
	// Two pre-v2 windows: one with automatic-rename off (its window_name is the
	// seed) and one with automatic-rename on (the user pane label is the seed).
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

	if _, _, err := runRoute(t, fixture.command(observe), "pane", "--project", filepath.Base(root), "--window", "editor"); err != nil {
		t.Fatalf("create over a legacy session error = %v", err)
	}

	registry := fixture.load(t)
	project, ok := registry.ProjectByName(filepath.Base(root))
	if !ok {
		t.Fatalf("the legacy session did not become a Project:\n%+v", registry.Projects)
	}
	windows := registry.WindowsOf(project.Metadata.UID)
	var names []string
	for _, window := range windows {
		names = append(names, window.Metadata.Name)
	}
	// automatic-rename off keeps its window_name; automatic-rename on takes the
	// user pane label, never the raw title or the topic.
	if !slices.Contains(names, "editor") || !slices.Contains(names, "logs") {
		t.Fatalf("migrated Window names = %v, want editor and logs", names)
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
	if _, _, err := runRoute(t, fixture.command(observe), "pane", "--project", filepath.Base(root), "--window", "editor"); err != nil {
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
