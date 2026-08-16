package metadata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

var fixedNow = time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)

func testStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(PathFor(t.TempDir()))
	store.SetClock(func() time.Time { return fixedNow })
	return store
}

func testMutator(roots map[string]bool) coremetadata.Mutator {
	counts := map[coremetadata.Kind]int{}
	return coremetadata.Mutator{
		Now: func() time.Time { return fixedNow },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			counts[kind]++
			return fmt.Sprintf("%s-%02d", strings.ToLower(string(kind)), counts[kind]), nil
		},
		DirExists: func(path string) (bool, error) { return roots[path], nil },
	}
}

func writeRegistryFile(t *testing.T, store *Store, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(store.Path(), []byte(contents), 0o600); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
}

func dirListing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestUpdateConvergentSkipsTheAtomicWriteForAnUnchangedRegistry(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	seed, err := store.Update(func(*coremetadata.Registry) error { return nil })
	if err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	bytesBefore := readFile(t, store.Path())
	writes := 0
	store.hooks.afterBackup = func() error {
		writes++
		return nil
	}

	got, changed, err := store.UpdateConvergent(func(working *coremetadata.Registry) error {
		if !reflect.DeepEqual(*working, seed) {
			t.Fatalf("callback registry = %+v, want seeded registry %+v", *working, seed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed || writes != 0 {
		t.Fatalf("unchanged convergence changed=%v writes=%d", changed, writes)
	}
	if !reflect.DeepEqual(got, seed) || readFile(t, store.Path()) != bytesBefore {
		t.Fatal("unchanged convergence did not preserve the registry bytes")
	}
}

const newerSchemaRegistry = `{
  "apiVersion": "projmux.io/v2",
  "schemaVersion": 2,
  "updatedAt": "2026-08-15T09:30:00Z",
  "projects": [
    {
      "apiVersion": "projmux.io/v2",
      "kind": "Project",
      "metadata": {"uid": "project-01", "name": "projmux", "createdAt": "2026-08-15T09:30:00Z"},
      "spec": {"root": "/src/projmux", "futureField": true},
      "status": {}
    }
  ]
}
`

func TestAnUnknownNewerSchemaVersionIsRejectedFailClosedWithNoWriteAtAll(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(store *Store) error
	}{
		{name: "load", run: func(store *Store) error {
			_, err := store.Load()
			return err
		}},
		{name: "update", run: func(store *Store) error {
			_, err := store.Update(func(reg *coremetadata.Registry) error {
				reg.Projects = nil
				return nil
			})
			return err
		}},
		{name: "migrate", run: func(store *Store) error {
			_, err := store.Migrate()
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			writeRegistryFile(t, store, newerSchemaRegistry)
			dir := filepath.Dir(store.Path())
			bytesBefore := readFile(t, store.Path())
			listingBefore := dirListing(t, dir)

			err := tt.run(store)
			if err == nil {
				t.Fatal("a newer schema version must be refused")
			}
			if !errors.Is(err, coremetadata.ErrSchemaTooNew) {
				t.Fatalf("error %v does not wrap ErrSchemaTooNew", err)
			}
			if got := readFile(t, store.Path()); got != bytesBefore {
				t.Fatalf("the registry file was modified:\n--- got ---\n%s\n--- want ---\n%s", got, bytesBefore)
			}
			listingAfter := dirListing(t, dir)
			if strings.Join(listingAfter, ",") != strings.Join(listingBefore, ",") {
				t.Fatalf("the state directory changed: %v -> %v", listingBefore, listingAfter)
			}
			for _, name := range listingAfter {
				if strings.Contains(name, ".bak") || strings.Contains(name, ".tmp-") || strings.Contains(name, "corrupt") || strings.Contains(name, "quarantine") {
					t.Fatalf("fail-closed must not quarantine, reset, or back up a newer registry: %q", name)
				}
			}
		})
	}
}

// TestARegistryDocumentWithoutASchemaVersionIsRejectedFailClosedWithNoWriteAtAll
// covers the unknown-document case: an absent schemaVersion decodes as 0, and
// schemaVersion 1 is the first envelope projmux has ever written, so such a
// file is a corrupt or foreign document rather than a pre-release registry. It
// must be refused without a write, a backup, or a staged temp file.
func TestARegistryDocumentWithoutASchemaVersionIsRejectedFailClosedWithNoWriteAtAll(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(store *Store) error
	}{
		{name: "load", run: func(store *Store) error {
			_, err := store.Load()
			return err
		}},
		{name: "update", run: func(store *Store) error {
			_, err := store.Update(func(reg *coremetadata.Registry) error {
				reg.Projects = nil
				return nil
			})
			return err
		}},
		{name: "migrate", run: func(store *Store) error {
			_, err := store.Migrate()
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			writeRegistryFile(t, store, unversionedRegistry)
			dir := filepath.Dir(store.Path())
			bytesBefore := readFile(t, store.Path())
			listingBefore := dirListing(t, dir)

			err := tt.run(store)
			if err == nil {
				t.Fatal("a document without a schemaVersion must be refused")
			}
			if !errors.Is(err, coremetadata.ErrSchemaUnsupported) {
				t.Fatalf("error %v does not wrap ErrSchemaUnsupported", err)
			}
			if got := readFile(t, store.Path()); got != bytesBefore {
				t.Fatalf("the registry file was modified:\n--- got ---\n%s\n--- want ---\n%s", got, bytesBefore)
			}
			listingAfter := dirListing(t, dir)
			if strings.Join(listingAfter, ",") != strings.Join(listingBefore, ",") {
				t.Fatalf("the state directory changed: %v -> %v", listingBefore, listingAfter)
			}
			for _, name := range listingAfter {
				if strings.Contains(name, ".bak") || strings.Contains(name, ".tmp-") || strings.Contains(name, "corrupt") || strings.Contains(name, "quarantine") {
					t.Fatalf("fail-closed must not back up, quarantine, or stage an unknown registry: %q", name)
				}
			}
		})
	}
}

func TestMalformedRegistryJSONFailsClosedWithoutResettingTheFile(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	writeRegistryFile(t, store, "{ this is not json")
	before := readFile(t, store.Path())

	if _, err := store.Load(); !errors.Is(err, ErrMalformedRegistry) {
		t.Fatalf("error = %v, want ErrMalformedRegistry", err)
	}
	if got := readFile(t, store.Path()); got != before {
		t.Fatal("malformed registry JSON must not be reset or quarantined")
	}
}

// olderEnvelopeRegistry is a registry document at a hypothetical older
// envelope version. Production ships no migration step for it -- schemaVersion
// 1 is the first envelope projmux has ever written -- so the migration tests
// register a step for this version into the store's private migration set.
const olderEnvelopeRegistry = `{
  "apiVersion": "projmux.io/v1alpha1",
  "schemaVersion": 0,
  "updatedAt": "2026-08-14T00:00:00Z",
  "projects": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Project",
      "metadata": {"uid": "project-01", "name": "projmux", "displayName": "projmux", "createdAt": "2026-08-14T00:00:00Z"},
      "spec": {"root": "/src/projmux"},
      "status": {}
    }
  ],
  "windows": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Window",
      "metadata": {"uid": "window-01", "name": "zsh", "ownerRef": {"kind": "Project", "uid": "project-01"}, "createdAt": "2026-08-14T00:00:00Z"},
      "spec": {"primaryPaneRef": "pane-01"}
    }
  ],
  "panes": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Pane",
      "metadata": {"uid": "pane-01", "name": "zsh", "ownerRef": {"kind": "Window", "uid": "window-01"}, "createdAt": "2026-08-14T00:00:00Z"},
      "spec": {"role": "shell", "cwd": "/src/projmux", "command": "zsh"},
      "status": {}
    }
  ],
  "nameReservations": [
    {"kind": "Project", "name": "projmux", "uid": "project-01"},
    {"scope": "project-01", "kind": "Window", "name": "zsh", "uid": "window-01"},
    {"scope": "window-01", "kind": "Pane", "name": "zsh", "uid": "pane-01"}
  ]
}
`

// unversionedRegistry parses as JSON but carries no schemaVersion. It stands
// in for a corrupt or foreign file at the registry path: it is unknown, not
// pre-release, so it must be refused without any write.
const unversionedRegistry = `{
  "apiVersion": "projmux.io/v1alpha1",
  "updatedAt": "2026-08-14T00:00:00Z",
  "projects": [
    {
      "apiVersion": "projmux.io/v1alpha1",
      "kind": "Project",
      "metadata": {"uid": "project-01", "name": "projmux", "createdAt": "2026-08-14T00:00:00Z"},
      "spec": {"root": "/src/projmux"},
      "status": {}
    }
  ]
}
`

// withOlderEnvelopeStep registers a migration step for the older envelope
// version into the store's private set, so the generic machinery and the
// atomic write sequence can be exercised without shipping a migration.
func withOlderEnvelopeStep(store *Store) {
	store.migrations = coremetadata.MigrationSet{
		0: func(reg *coremetadata.Registry) error {
			reg.SchemaVersion = coremetadata.SchemaVersion
			return nil
		},
	}
}

func TestSchemaMigrationIsAtomicAndAFailedStepLeavesTheOriginalState(t *testing.T) {
	t.Parallel()

	boom := errors.New("injected migration failure")
	tests := []struct {
		name  string
		hooks func(*Store)
	}{
		{name: "failure right after the backup", hooks: func(s *Store) {
			s.hooks.afterBackup = func() error { return boom }
		}},
		{name: "failure after the staged temp write", hooks: func(s *Store) {
			s.hooks.afterTempWrite = func() error { return boom }
		}},
		{name: "failure just before the atomic replace", hooks: func(s *Store) {
			s.hooks.beforeRename = func() error { return boom }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			writeRegistryFile(t, store, olderEnvelopeRegistry)
			withOlderEnvelopeStep(store)
			dir := filepath.Dir(store.Path())
			before := readFile(t, store.Path())
			tt.hooks(store)

			if _, err := store.Migrate(); !errors.Is(err, boom) {
				t.Fatalf("error = %v, want the injected failure", err)
			}
			if got := readFile(t, store.Path()); got != before {
				t.Fatalf("a failed migration left partially applied state:\n--- got ---\n%s\n--- want ---\n%s", got, before)
			}
			for _, name := range dirListing(t, dir) {
				if strings.Contains(name, ".tmp-") {
					t.Fatalf("a failed migration leaked a staged temp file: %q", name)
				}
			}
			// The original file must still be readable at its older envelope,
			// so a retry can migrate it cleanly.
			registry, err := store.Load()
			if err != nil {
				t.Fatalf("reload after a failed migration: %v", err)
			}
			if len(registry.Projects) != 1 {
				t.Fatalf("projects = %d, want the original 1", len(registry.Projects))
			}
		})
	}
}

func TestSuccessfulSchemaMigrationBacksUpTheOriginalAndReplacesItAtomically(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	writeRegistryFile(t, store, olderEnvelopeRegistry)
	withOlderEnvelopeStep(store)
	original := readFile(t, store.Path())

	result, err := store.Migrate()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !result.Migrated || result.FromVersion != 0 {
		t.Fatalf("result = %+v, want a migration from version 0", result)
	}
	if result.BackupPath == "" {
		t.Fatal("a migration must back up the pre-migration bytes")
	}
	if got := readFile(t, result.BackupPath); got != original {
		t.Fatal("the backup does not hold the pre-migration bytes")
	}

	migrated, err := store.Load()
	if err != nil {
		t.Fatalf("load migrated: %v", err)
	}
	if migrated.SchemaVersion != coremetadata.SchemaVersion || migrated.APIVersion != coremetadata.APIVersion {
		t.Fatalf("envelope = %s/%d", migrated.APIVersion, migrated.SchemaVersion)
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("migrated registry is invalid: %v", err)
	}
	if len(migrated.NameReservations) != 3 {
		t.Fatalf("reservations = %+v, want one per resource", migrated.NameReservations)
	}
	// The migrated file no longer classifies as older, so a reader without
	// the registered step still accepts it.
	if _, err := NewStore(store.Path()).Load(); err != nil {
		t.Fatalf("a production reader must accept the migrated file: %v", err)
	}

	// Migrating again is a no-op and takes no second backup.
	second, err := store.Migrate()
	if err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if second.Migrated || second.BackupPath != "" {
		t.Fatalf("re-migration = %+v, want a no-op", second)
	}
}

func TestUpdateWritesNothingWhenTheOperationFails(t *testing.T) {
	t.Parallel()

	roots := map[string]bool{"/src/projmux": true, "/src/other": true}
	m := testMutator(roots)
	store := testStore(t)

	if _, err := store.Update(func(reg *coremetadata.Registry) error {
		_, err := m.RegisterProject(reg, coremetadata.RegisterProjectOptions{
			Root:         "/src/projmux",
			DefaultShell: "/bin/zsh",
			OperationID:  "op-seed",
		})
		return err
	}); err != nil {
		t.Fatalf("seed update: %v", err)
	}
	before := readFile(t, store.Path())

	_, err := store.Update(func(reg *coremetadata.Registry) error {
		_, err := m.RegisterProject(reg, coremetadata.RegisterProjectOptions{
			Root:         "/src/other",
			Name:         "projmux",
			DefaultShell: "/bin/zsh",
			OperationID:  "op-collide",
		})
		return err
	})
	if !errors.Is(err, coremetadata.ErrNameConflict) {
		t.Fatalf("error = %v, want ErrNameConflict", err)
	}
	if got := readFile(t, store.Path()); got != before {
		t.Fatalf("a failed operation wrote to the registry:\n--- got ---\n%s\n--- want ---\n%s", got, before)
	}
}

func TestRegisteredProjectPersistsTheOfflineTopologyAndPrimaryPaneRef(t *testing.T) {
	t.Parallel()

	roots := map[string]bool{"/src/projmux": true}
	m := testMutator(roots)
	store := testStore(t)

	if _, err := store.Update(func(reg *coremetadata.Registry) error {
		_, err := m.RegisterProject(reg, coremetadata.RegisterProjectOptions{
			Root:         "/src/projmux",
			DefaultShell: "/bin/zsh",
			Topology: []coremetadata.BootstrapWindow{
				{Command: "nvim"},
				{Name: "server", Panes: []coremetadata.BootstrapPane{{Command: "npm run dev"}, {Command: "htop"}}},
			},
			OperationID: "op-register",
		})
		return err
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	got := readFile(t, store.Path())
	want, err := os.ReadFile("testdata/registry-bootstrap.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("registry file does not match the golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := reloaded.Validate(); err != nil {
		t.Fatalf("reloaded registry is invalid: %v", err)
	}
	project, ok := reloaded.ProjectByRoot("/src/projmux")
	if !ok {
		t.Fatal("project did not survive the round trip")
	}
	for _, window := range reloaded.WindowsOf(project.Metadata.UID) {
		pane, ok := reloaded.Pane(window.Spec.PrimaryPaneRef)
		if !ok {
			t.Fatalf("window %q primaryPaneRef %q does not resolve after reload", window.Metadata.Name, window.Spec.PrimaryPaneRef)
		}
		if pane.Metadata.OwnerUID() != window.Metadata.UID {
			t.Fatalf("window %q primaryPaneRef is owned by %q", window.Metadata.Name, pane.Metadata.OwnerUID())
		}
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestProjectAndWindowMetadataStayQueryableWhileTmuxIsDown(t *testing.T) {
	t.Parallel()

	roots := map[string]bool{"/src/projmux": true}
	m := testMutator(roots)
	store := testStore(t)

	var uid string
	if _, err := store.Update(func(reg *coremetadata.Registry) error {
		result, err := m.RegisterProject(reg, coremetadata.RegisterProjectOptions{
			Root:         "/src/projmux",
			DefaultShell: "/bin/zsh",
			OperationID:  "op-register",
		})
		if err != nil {
			return err
		}
		uid = result.Project.Metadata.UID
		_, err = m.BindProjectSession(reg, uid, "projmux", true)
		return err
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := store.Update(func(reg *coremetadata.Registry) error {
		_, err := m.BindProjectSession(reg, uid, "projmux", false)
		return err
	}); err != nil {
		t.Fatalf("mark offline: %v", err)
	}

	offline, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	project, ok := offline.Project(uid)
	if !ok {
		t.Fatal("project must stay queryable while tmux is down")
	}
	if project.Status.Session == nil || project.Status.Session.Live {
		t.Fatalf("session projection = %+v, want live=false", project.Status.Session)
	}
	if len(offline.WindowsOf(uid)) != 1 || len(offline.Panes) != 1 {
		t.Fatalf("topology = %d windows / %d panes, want 1/1", len(offline.WindowsOf(uid)), len(offline.Panes))
	}
}

func TestConcurrentUpdatesSerializeThroughTheRegistryLock(t *testing.T) {
	t.Parallel()

	roots := map[string]bool{}
	for i := range 8 {
		roots[fmt.Sprintf("/src/p%d", i)] = true
	}
	// The lock's stale-breaking window is measured against the wall clock, so
	// this test deliberately keeps the real clock instead of the fixed one.
	store := NewStore(PathFor(t.TempDir()))

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range 8 {
		wg.Go(func() {
			m := coremetadata.Mutator{
				Now:       func() time.Time { return fixedNow },
				NewUID:    coremetadata.NewUID,
				DirExists: func(path string) (bool, error) { return roots[path], nil },
			}
			_, errs[i] = store.Update(func(reg *coremetadata.Registry) error {
				_, err := m.RegisterProject(reg, coremetadata.RegisterProjectOptions{
					Root:         fmt.Sprintf("/src/p%d", i),
					DefaultShell: "/bin/zsh",
					OperationID:  fmt.Sprintf("op-%d", i),
				})
				return err
			})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}

	final, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(final.Projects) != 8 {
		t.Fatalf("projects = %d, want 8 serialized registrations", len(final.Projects))
	}
	if err := final.Validate(); err != nil {
		t.Fatalf("registry invalid after concurrent updates: %v", err)
	}
	names := map[string]bool{}
	for _, project := range final.Projects {
		if names[project.Metadata.Name] {
			t.Fatalf("duplicate project name %q survived concurrent allocation", project.Metadata.Name)
		}
		names[project.Metadata.Name] = true
	}
}

// TestAnAbsentOrContentFreeRegistryFileLoadsAnEmptyRegistry keeps the
// legitimate empty cases distinct from the fail-closed unknown-document case:
// only a file with actual content that lacks a usable schemaVersion is
// refused.
func TestAnAbsentOrContentFreeRegistryFileLoadsAnEmptyRegistry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		contents string
		seed     bool
	}{
		{name: "absent file"},
		{name: "empty file", contents: "", seed: true},
		{name: "whitespace only file", contents: "  \n\t\n", seed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			if tt.seed {
				writeRegistryFile(t, store, tt.contents)
			}
			registry, err := store.Load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if registry.SchemaVersion != coremetadata.SchemaVersion || len(registry.Projects) != 0 {
				t.Fatalf("registry = %+v, want an empty current-version registry", registry)
			}
			if !tt.seed {
				if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("a read must not create the registry file: %v", err)
				}
			}
		})
	}
}

func TestDirExistsProbeDistinguishesDirectoriesFromFilesAndAbsences(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "existing directory", path: dir, want: true},
		{name: "regular file is not a directory", path: file, want: false},
		{name: "absent path", path: filepath.Join(dir, "nope"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := DirExists(tt.path)
			if err != nil {
				t.Fatalf("DirExists: %v", err)
			}
			if got != tt.want {
				t.Fatalf("DirExists(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestDefaultStorePathFollowsTheStateDirectoryLayout(t *testing.T) {
	t.Parallel()

	if got, want := PathFor("/state/projmux"), filepath.Join("/state/projmux", "metadata", "registry.json"); got != want {
		t.Fatalf("PathFor = %q, want %q", got, want)
	}
}

// TestLoadReadOnlyCreatesNoDirectoryOrLockForAnAbsentRegistry pins the
// zero-side-effect read the read-only routes need.
//
// Load takes the cross-process lock, and acquiring it creates the registry
// directory and a lock file. An operator who has never registered a resource
// must not get <state>/projmux/metadata/ materialized just by running a read,
// so LoadReadOnly short-circuits before any directory is touched.
func TestLoadReadOnlyCreatesNoDirectoryOrLockForAnAbsentRegistry(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	store := NewStore(PathFor(stateDir))
	store.SetClock(func() time.Time { return fixedNow })
	metadataDir := filepath.Dir(store.Path())

	registry, err := store.LoadReadOnly()
	if err != nil {
		t.Fatalf("LoadReadOnly: %v", err)
	}
	if registry.SchemaVersion != coremetadata.SchemaVersion || len(registry.Projects) != 0 {
		t.Fatalf("registry = %+v, want an empty current-version registry", registry)
	}
	if _, err := os.Stat(metadataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadReadOnly created %s: %v", metadataDir, err)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("LoadReadOnly created %d entries under the state dir", len(entries))
	}

	// A nil store answers an empty registry rather than panicking, matching
	// Load.
	var nilStore *Store
	if registry, err = nilStore.LoadReadOnly(); err != nil || registry.SchemaVersion != coremetadata.SchemaVersion {
		t.Fatalf("nil store LoadReadOnly = %+v, %v", registry, err)
	}
}

// TestLoadReadOnlyReadsAnExistingRegistryAndStillFailsClosed proves the
// short-circuit does not weaken the fail-closed contract: once the file exists,
// LoadReadOnly is the ordinary locked read.
func TestLoadReadOnlyReadsAnExistingRegistryAndStillFailsClosed(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	mutator := testMutator(map[string]bool{"/src/projmux": true})
	if _, err := store.Update(func(registry *coremetadata.Registry) error {
		_, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
			Root:         "/src/projmux",
			DefaultShell: "/bin/zsh",
			OperationID:  "op-readonly-seed",
		})
		return err
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	registry, err := store.LoadReadOnly()
	if err != nil {
		t.Fatalf("LoadReadOnly: %v", err)
	}
	if len(registry.Projects) != 1 {
		t.Fatalf("registry holds %d projects, want 1", len(registry.Projects))
	}

	writeRegistryFile(t, store, fmt.Sprintf(`{"apiVersion":%q,"schemaVersion":%d}`,
		coremetadata.APIVersion, coremetadata.SchemaVersion+1))
	before := readFile(t, store.Path())
	if _, err := store.LoadReadOnly(); !errors.Is(err, coremetadata.ErrSchemaTooNew) {
		t.Fatalf("LoadReadOnly on a newer envelope = %v, want ErrSchemaTooNew", err)
	}
	if after := readFile(t, store.Path()); after != before {
		t.Fatal("a refused LoadReadOnly rewrote the registry file")
	}
}
