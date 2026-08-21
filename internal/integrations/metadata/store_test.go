package metadata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	// The envelope must stay bounded and clean under contention: the lock is
	// released, nothing is left staged, and the retention bound holds no matter
	// how the eight writes interleaved.
	if _, err := os.Stat(store.lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the registry lock survived the concurrent updates: %v", err)
	}
	assertNoStagedGarbage(t, store)
	if copies := recoveryCopies(t, store); len(copies) > store.retention {
		t.Fatalf("recovery copies = %v, want at most the bounded %d", copies, store.retention)
	}
	if _, err := os.Stat(store.markerPath); err != nil {
		t.Fatalf("the concurrent updates did not establish the initialized marker: %v", err)
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

func TestDegradedReadRequiresExplicitOptIn(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	invalid := fmt.Sprintf(`{"apiVersion":%q,"schemaVersion":%d,"panes":[{"apiVersion":%q,"kind":"Pane","metadata":{"uid":"pane-orphan","name":"zsh","ownerRef":{"kind":"Window","uid":"window-missing"}},"spec":{"role":"shell"}}]}`,
		coremetadata.APIVersion, coremetadata.SchemaVersion, coremetadata.APIVersion)
	writeRegistryFile(t, store, invalid)

	if _, err := store.Load(); !errors.Is(err, coremetadata.ErrInvalidRegistry) {
		t.Fatalf("ordinary Load error = %v, want ErrInvalidRegistry", err)
	}
	if _, err := store.LoadReadOnly(); !errors.Is(err, coremetadata.ErrInvalidRegistry) {
		t.Fatalf("ordinary LoadReadOnly error = %v, want ErrInvalidRegistry", err)
	}
	registry, err := store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatalf("explicit degraded read: %v", err)
	}
	if len(registry.Panes) != 1 || registry.Panes[0].Metadata.UID != "pane-orphan" {
		t.Fatalf("degraded read Registry = %+v", registry)
	}
}

func TestLoadSnapshotReadsExistingRegistryWithoutLockOrPermissionRepair(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	mutator := testMutator(map[string]bool{"/src/projmux": true})
	if _, err := store.Update(func(registry *coremetadata.Registry) error {
		_, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
			Root: "/src/projmux", DefaultShell: "/bin/zsh", OperationID: "op-snapshot-seed",
		})
		return err
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if err := os.Chmod(store.Path(), 0o644); err != nil {
		t.Fatalf("chmod registry fixture: %v", err)
	}
	beforeInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat registry before strict read: %v", err)
	}
	if _, err := store.LoadSnapshot(); err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	afterInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat registry after strict read: %v", err)
	}
	if afterInfo.Mode().Perm() != beforeInfo.Mode().Perm() {
		t.Fatalf("LoadSnapshot repaired permissions from %o to %o", beforeInfo.Mode().Perm(), afterInfo.Mode().Perm())
	}
	if _, err := os.Stat(store.Path() + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadSnapshot created a lock: %v", err)
	}
}

// --- Durable recovery envelope -------------------------------------------
//
// The registry is the source of truth for managed identity and desired
// topology, so these tests pin the two boundaries that make it trustworthy:
// first use is distinguishable from state loss, and no failure step replaces or
// damages bytes that are already committed.

func fileFingerprint(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: no inode information", path)
	}
	return fmt.Sprintf("ino=%d size=%d mtime=%d.%09d mode=%o",
		stat.Ino, info.Size(), info.ModTime().Unix(), info.ModTime().Nanosecond(), info.Mode().Perm())
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

// registerProject performs one semantic write against the store. Uids are
// derived from the root so repeated calls against one store stay unique without
// the caller threading a counter through.
func registerProject(t *testing.T, store *Store, root string) {
	t.Helper()
	counts := map[coremetadata.Kind]int{}
	mutator := coremetadata.Mutator{
		Now: func() time.Time { return fixedNow },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			counts[kind]++
			return fmt.Sprintf("%s-%s-%02d", strings.ToLower(string(kind)), filepath.Base(root), counts[kind]), nil
		},
		DirExists: func(path string) (bool, error) { return path == root, nil },
	}
	if _, err := store.Update(func(registry *coremetadata.Registry) error {
		_, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
			Root:         root,
			DefaultShell: "/bin/zsh",
			OperationID:  "op-" + filepath.Base(root),
		})
		return err
	}); err != nil {
		t.Fatalf("register %s: %v", root, err)
	}
}

func recoveryCopies(t *testing.T, store *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(store.recoveryDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read recovery dir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func assertNoStagedGarbage(t *testing.T, store *Store) {
	t.Helper()
	for _, name := range dirListing(t, filepath.Dir(store.Path())) {
		if strings.Contains(name, ".tmp-") {
			t.Fatalf("leaked staged file in the registry dir: %q", name)
		}
	}
	for _, name := range recoveryCopies(t, store) {
		if strings.Contains(name, ".tmp-") {
			t.Fatalf("leaked staged file in the recovery dir: %q", name)
		}
	}
}

// TestFirstUseIsAZeroWriteEmptyRegistryAndTheFirstCommitEstablishesTheBoundary
// pins the initialized boundary from both sides. Before any commit every read
// answers the empty registry and creates nothing at all; the first successful
// commit publishes the registry and the marker that make a later disappearance
// diagnosable.
func TestFirstUseIsAZeroWriteEmptyRegistryAndTheFirstCommitEstablishesTheBoundary(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	store := NewStore(PathFor(stateDir))
	store.SetClock(func() time.Time { return fixedNow })

	for _, read := range []struct {
		name string
		call func() (coremetadata.Registry, error)
	}{
		{name: "LoadReadOnly", call: store.LoadReadOnly},
		{name: "LoadSnapshot", call: store.LoadSnapshot},
	} {
		registry, err := read.call()
		if err != nil {
			t.Fatalf("%s on a fresh state dir: %v", read.name, err)
		}
		if registry.SchemaVersion != coremetadata.SchemaVersion || len(registry.Projects) != 0 {
			t.Fatalf("%s = %+v, want an empty current-version registry", read.name, registry)
		}
		entries, err := os.ReadDir(stateDir)
		if err != nil {
			t.Fatalf("read state dir: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("%s created %d entries under the state dir", read.name, len(entries))
		}
	}

	registerProject(t, store, "/src/projmux")

	assertMode(t, filepath.Dir(store.Path()), 0o700)
	assertMode(t, store.Path(), 0o600)
	assertMode(t, store.markerPath, 0o600)
	if registry, err := store.LoadReadOnly(); err != nil || len(registry.Projects) != 1 {
		t.Fatalf("after the first commit LoadReadOnly = %+v, %v", registry, err)
	}
	var marker initializedMarker
	if err := json.Unmarshal([]byte(readFile(t, store.markerPath)), &marker); err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	if marker.SchemaVersion != coremetadata.SchemaVersion || marker.InitializedAt != fixedNow.Format(time.RFC3339) {
		t.Fatalf("marker = %+v, want the current schema and the commit time", marker)
	}
	// The first commit replaced nothing, so it preserved nothing.
	if copies := recoveryCopies(t, store); len(copies) != 0 {
		t.Fatalf("the first commit took recovery copies %v", copies)
	}
	assertNoStagedGarbage(t, store)
}

// TestAMissingOrEmptyRegistryAfterTheFirstCommitIsTypedStateLoss is the other
// half of the boundary: once the marker exists, content-free registry bytes are
// a loss report on every route instead of a fresh identity domain.
func TestAMissingOrEmptyRegistryAfterTheFirstCommitIsTypedStateLoss(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		lose func(t *testing.T, store *Store)
	}{
		{name: "registry removed", lose: func(t *testing.T, store *Store) {
			if err := os.Remove(store.Path()); err != nil {
				t.Fatalf("remove registry: %v", err)
			}
		}},
		{name: "registry truncated", lose: func(t *testing.T, store *Store) {
			writeRegistryFile(t, store, "")
		}},
		{name: "registry whitespace only", lose: func(t *testing.T, store *Store) {
			writeRegistryFile(t, store, "\n\t \n")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			registerProject(t, store, "/src/projmux")
			tt.lose(t, store)
			markerBefore := fileFingerprint(t, store.markerPath)

			for _, read := range []struct {
				name string
				call func() (coremetadata.Registry, error)
			}{
				{name: "Load", call: store.Load},
				{name: "LoadReadOnly", call: store.LoadReadOnly},
				{name: "LoadSnapshot", call: store.LoadSnapshot},
			} {
				registry, err := read.call()
				if !errors.Is(err, ErrRegistryStateLost) {
					t.Fatalf("%s = %+v, %v, want ErrRegistryStateLost", read.name, registry, err)
				}
				if len(registry.Projects) != 0 {
					t.Fatalf("%s answered resources from a lost registry: %+v", read.name, registry)
				}
			}
			// The message has to be actionable without this store choosing a
			// recovery: both halves of the evidence and both ways out.
			_, err := store.Load()
			for _, want := range []string{store.Path(), store.markerPath, store.recoveryDir} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("state-loss error %q does not name %q", err, want)
				}
			}

			// A mutation must not paper over the loss by minting a new
			// identity domain on top of it.
			mutator := testMutator(map[string]bool{"/src/other": true})
			minted := false
			if _, err := store.Update(func(registry *coremetadata.Registry) error {
				minted = true
				_, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
					Root: "/src/other", DefaultShell: "/bin/zsh", OperationID: "op-after-loss",
				})
				return err
			}); !errors.Is(err, ErrRegistryStateLost) {
				t.Fatalf("Update after state loss = %v, want ErrRegistryStateLost", err)
			}
			if minted {
				t.Fatal("Update ran its mutation against a lost registry")
			}
			if _, _, err := store.UpdateConvergent(func(*coremetadata.Registry) error { return nil }); !errors.Is(err, ErrRegistryStateLost) {
				t.Fatalf("UpdateConvergent after state loss = %v, want ErrRegistryStateLost", err)
			}
			if fileFingerprint(t, store.markerPath) != markerBefore {
				t.Fatal("a refused route rewrote the initialized marker")
			}
			assertNoStagedGarbage(t, store)
		})
	}
}

// TestALegacyRegistryWithoutTheMarkerReadsAndTheNextSemanticWriteBackfillsIt
// keeps the boundary backward compatible. A registry written before the marker
// existed is ordinary state, not a loss, and it gains the marker on its next
// write without a separate migration.
func TestALegacyRegistryWithoutTheMarkerReadsAndTheNextSemanticWriteBackfillsIt(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	registerProject(t, store, "/src/projmux")
	legacyBytes := readFile(t, store.Path())
	if err := os.Remove(store.markerPath); err != nil {
		t.Fatalf("remove marker: %v", err)
	}

	registry, err := store.Load()
	if err != nil {
		t.Fatalf("a registry without the marker must still read: %v", err)
	}
	if len(registry.Projects) != 1 {
		t.Fatalf("projects = %d, want the legacy 1", len(registry.Projects))
	}
	if _, statErr := os.Stat(store.markerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a read created the marker: %v", statErr)
	}

	registerProject(t, store, "/src/other")
	if _, statErr := os.Stat(store.markerPath); statErr != nil {
		t.Fatalf("the next semantic write did not backfill the marker: %v", statErr)
	}
	// The legacy bytes are the prior verified state, so they are the copy.
	copies := recoveryCopies(t, store)
	if len(copies) != 1 {
		t.Fatalf("recovery copies = %v, want exactly the legacy bytes", copies)
	}
	if got := readFile(t, filepath.Join(store.recoveryDir, copies[0])); got != legacyBytes {
		t.Fatal("the recovery copy does not hold the replaced legacy bytes")
	}
}

// TestSemanticWritesKeepTheLastVerifiedBytesInBoundedRecoveryCopies pins the
// rolling copy: every same-version semantic write preserves what it replaced,
// the newest retained copies are the ones that survive, and the copies are
// ordinary private files an operator can read.
func TestSemanticWritesKeepTheLastVerifiedBytesInBoundedRecoveryCopies(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	var replaced []string
	for i := range store.retention + 3 {
		if i > 0 {
			replaced = append(replaced, readFile(t, store.Path()))
		}
		registerProject(t, store, fmt.Sprintf("/src/p%d", i))
	}

	copies := recoveryCopies(t, store)
	if len(copies) != store.retention {
		t.Fatalf("recovery copies = %v, want the bounded %d", copies, store.retention)
	}
	assertMode(t, store.recoveryDir, 0o700)
	// Names sort chronologically, so the retained window is the newest slice of
	// what each write replaced.
	wantBytes := replaced[len(replaced)-store.retention:]
	for i, name := range copies {
		assertMode(t, filepath.Join(store.recoveryDir, name), 0o600)
		got := readFile(t, filepath.Join(store.recoveryDir, name))
		if got != wantBytes[i] {
			t.Fatalf("recovery copy %s does not hold the bytes it replaced:\n--- got ---\n%s\n--- want ---\n%s", name, got, wantBytes[i])
		}
		if !sort.StringsAreSorted(copies) {
			t.Fatalf("recovery copies are not in a deterministic order: %v", copies)
		}
	}
	// Every retained copy is a registry a reader still accepts, which is what
	// makes it a recovery source rather than a byte dump.
	for _, name := range copies {
		path := filepath.Join(store.recoveryDir, name)
		if _, err := NewStore(path).LoadSnapshot(); err != nil {
			t.Fatalf("retained recovery copy %s does not read as a registry: %v", name, err)
		}
	}
	assertNoStagedGarbage(t, store)
}

// TestOrdinaryUpdateCannotReplaceAnInvalidRegistry keeps repair authority out
// of the normal mutation transaction. Invalid prior bytes are not safe recovery
// history, and replacing them is reserved for RestoreFrom's explicitly selected
// and independently verified source.
func TestOrdinaryUpdateCannotReplaceAnInvalidRegistry(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	registerProject(t, store, "/src/projmux")
	invalid := fmt.Sprintf(`{"apiVersion":%q,"schemaVersion":%d,"panes":[{"apiVersion":%q,"kind":"Pane","metadata":{"uid":"pane-orphan","name":"zsh","ownerRef":{"kind":"Window","uid":"window-missing"}},"spec":{"role":"shell"}}]}`,
		coremetadata.APIVersion, coremetadata.SchemaVersion, coremetadata.APIVersion)
	writeRegistryFile(t, store, invalid)
	registryBefore := readFile(t, store.Path())
	before := recoveryCopies(t, store)

	called := false
	_, err := store.Update(func(registry *coremetadata.Registry) error {
		called = true
		*registry = coremetadata.NewRegistry()
		return nil
	})
	if !errors.Is(err, ErrRegistryDegraded) || !errors.Is(err, coremetadata.ErrInvalidRegistry) {
		t.Fatalf("ordinary replacement error = %v, want degraded ErrInvalidRegistry", err)
	}
	if called {
		t.Fatal("ordinary Update invoked its callback against an invalid Registry")
	}
	if !strings.Contains(err.Error(), RegistryRecoveryPlanCommand) {
		t.Fatalf("degraded refusal %q does not name the exact recovery plan command", err)
	}
	if got := readFile(t, store.Path()); got != registryBefore {
		t.Fatal("ordinary Update replaced invalid Registry bytes")
	}

	if got := recoveryCopies(t, store); len(got) != len(before) {
		t.Fatalf("recovery copies = %v, want the unchanged %v: invalid prior bytes are not a recovery source", got, before)
	}
}

// TestConvergentNoOpTouchesNeitherTheRegistryNorItsRecoveryCopies pins the
// zero-write side of convergence. A lifecycle pass that agrees with the stored
// state must not spend a recovery slot or a byte replace merely because it took
// the mutation lock.
func TestConvergentNoOpTouchesNeitherTheRegistryNorItsRecoveryCopies(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	registerProject(t, store, "/src/projmux")
	registerProject(t, store, "/src/other")
	registryBefore := fileFingerprint(t, store.Path())
	markerBefore := fileFingerprint(t, store.markerPath)
	copiesBefore := recoveryCopies(t, store)

	for range 3 {
		if _, changed, err := store.UpdateConvergent(func(*coremetadata.Registry) error { return nil }); err != nil || changed {
			t.Fatalf("convergent no-op changed=%v err=%v", changed, err)
		}
	}

	if got := fileFingerprint(t, store.Path()); got != registryBefore {
		t.Fatalf("registry fingerprint = %s, want the unchanged %s", got, registryBefore)
	}
	if got := fileFingerprint(t, store.markerPath); got != markerBefore {
		t.Fatalf("marker fingerprint = %s, want the unchanged %s", got, markerBefore)
	}
	if got := recoveryCopies(t, store); !reflect.DeepEqual(got, copiesBefore) {
		t.Fatalf("recovery copies = %v, want the unchanged %v", got, copiesBefore)
	}
	assertNoStagedGarbage(t, store)
}

// TestAFailureAtEachDurabilityStepLeavesThePriorRegistryByteIdentical walks the
// whole envelope. Every step is injected in turn against a registry that already
// holds committed state, and the committed state has to survive each of them
// unchanged -- same bytes, same inode, same mtime -- with nothing staged left
// behind and the next attempt still able to succeed.
func TestAFailureAtEachDurabilityStepLeavesThePriorRegistryByteIdentical(t *testing.T) {
	t.Parallel()

	boom := errors.New("injected durability failure")
	tests := []struct {
		name   string
		inject func(t *testing.T, store *Store)
	}{
		{name: "staged temp fsync", inject: func(_ *testing.T, store *Store) {
			store.hooks.syncFile = func(*os.File) error { return boom }
		}},
		{name: "after the staged temp write", inject: func(_ *testing.T, store *Store) {
			store.hooks.afterTempWrite = func() error { return boom }
		}},
		{name: "staged validation", inject: func(_ *testing.T, store *Store) {
			store.hooks.validateStaged = func(string) error { return boom }
		}},
		{name: "recovery copy", inject: func(_ *testing.T, store *Store) {
			store.hooks.afterRecoveryCopy = func() error { return boom }
		}},
		{name: "initialized marker", inject: func(_ *testing.T, store *Store) {
			store.hooks.afterMarker = func() error { return boom }
		}},
		{name: "directory sync before the replace", inject: func(_ *testing.T, store *Store) {
			registryDir := filepath.Dir(store.Path())
			store.hooks.syncDir = func(dir string) error {
				if dir == registryDir {
					return boom
				}
				return nil
			}
		}},
		{name: "atomic replace", inject: func(_ *testing.T, store *Store) {
			store.hooks.beforeRename = func() error { return boom }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			registerProject(t, store, "/src/projmux")
			registerProject(t, store, "/src/other")
			registryBytes := readFile(t, store.Path())
			registryBefore := fileFingerprint(t, store.Path())
			markerBefore := fileFingerprint(t, store.markerPath)
			copiesBefore := recoveryCopies(t, store)
			if len(copiesBefore) != 1 {
				t.Fatalf("fixture recovery copies = %v, want exactly one", copiesBefore)
			}

			tt.inject(t, store)
			mutator := testMutator(map[string]bool{"/src/third": true})
			_, err := store.Update(func(registry *coremetadata.Registry) error {
				_, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
					Root: "/src/third", DefaultShell: "/bin/zsh", OperationID: "op-injected",
				})
				return err
			})
			if !errors.Is(err, boom) {
				t.Fatalf("Update = %v, want the injected failure", err)
			}

			if got := readFile(t, store.Path()); got != registryBytes {
				t.Fatalf("a failed write changed the registry bytes:\n--- got ---\n%s\n--- want ---\n%s", got, registryBytes)
			}
			if got := fileFingerprint(t, store.Path()); got != registryBefore {
				t.Fatalf("registry fingerprint = %s, want the unchanged %s", got, registryBefore)
			}
			if got := fileFingerprint(t, store.markerPath); got != markerBefore {
				t.Fatalf("marker fingerprint = %s, want the unchanged %s", got, markerBefore)
			}
			if got := recoveryCopies(t, store); !reflect.DeepEqual(got, copiesBefore) {
				t.Fatalf("recovery copies = %v, want the unchanged %v: a failed write must roll back the copy it took", got, copiesBefore)
			}
			assertNoStagedGarbage(t, store)
			if _, statErr := os.Stat(store.lockPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a failed write left the lock behind: %v", statErr)
			}

			// The retry is the whole point of preserving the prior state.
			store.hooks = storeHooks{}
			registerProject(t, store, "/src/third")
			registry, err := store.Load()
			if err != nil {
				t.Fatalf("load after the retry: %v", err)
			}
			if len(registry.Projects) != 3 {
				t.Fatalf("projects = %d, want the retried 3", len(registry.Projects))
			}
			if got := recoveryCopies(t, store); len(got) != 2 {
				t.Fatalf("recovery copies after the retry = %v, want the prior copy plus one", got)
			}
		})
	}
}

// TestAFailedFirstCommitLeavesNeitherARegistryNorTheInitializedMarker keeps the
// boundary from being poisoned by a write that never landed. A first commit that
// fails must leave the state dir exactly as first use found it, so the next read
// is still the empty registry rather than a state-loss report.
func TestAFailedFirstCommitLeavesNeitherARegistryNorTheInitializedMarker(t *testing.T) {
	t.Parallel()

	boom := errors.New("injected first-commit failure")
	tests := []struct {
		name   string
		inject func(*Store)
	}{
		{name: "initialized marker", inject: func(store *Store) {
			store.hooks.afterMarker = func() error { return boom }
		}},
		{name: "atomic replace", inject: func(store *Store) {
			store.hooks.beforeRename = func() error { return boom }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			tt.inject(store)
			mutator := testMutator(map[string]bool{"/src/projmux": true})
			if _, err := store.Update(func(registry *coremetadata.Registry) error {
				_, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
					Root: "/src/projmux", DefaultShell: "/bin/zsh", OperationID: "op-first",
				})
				return err
			}); !errors.Is(err, boom) {
				t.Fatalf("Update = %v, want the injected failure", err)
			}

			if _, statErr := os.Stat(store.Path()); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a failed first commit published a registry: %v", statErr)
			}
			if _, statErr := os.Stat(store.markerPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a failed first commit left the initialized marker behind: %v", statErr)
			}
			assertNoStagedGarbage(t, store)

			store.hooks = storeHooks{}
			registry, err := store.LoadReadOnly()
			if err != nil {
				t.Fatalf("a read after a failed first commit = %v, want the empty first-use registry", err)
			}
			if len(registry.Projects) != 0 {
				t.Fatalf("registry = %+v, want empty", registry)
			}
		})
	}
}

// TestRegistryReadDiagnosticsAreDistinctPerFailureMode pins the four unreadable
// states apart. They ask an operator for four different actions -- recover lost
// state, fix a corrupt file, upgrade the binary, fix an access mode -- so
// collapsing any two of them into one error would send the wrong repair.
func TestRegistryReadDiagnosticsAreDistinctPerFailureMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		seed  func(t *testing.T, store *Store)
		want  error
		other []error
	}{
		{
			name: "missing after initialization",
			seed: func(t *testing.T, store *Store) {
				registerProject(t, store, "/src/projmux")
				if err := os.Remove(store.Path()); err != nil {
					t.Fatalf("remove registry: %v", err)
				}
			},
			want:  ErrRegistryStateLost,
			other: []error{ErrMalformedRegistry, coremetadata.ErrSchemaTooNew, ErrRegistryPermission},
		},
		{
			name: "malformed",
			seed: func(t *testing.T, store *Store) {
				writeRegistryFile(t, store, "{ this is not json")
			},
			want:  ErrMalformedRegistry,
			other: []error{ErrRegistryStateLost, coremetadata.ErrSchemaTooNew, ErrRegistryPermission},
		},
		{
			name: "newer schema",
			seed: func(t *testing.T, store *Store) {
				writeRegistryFile(t, store, fmt.Sprintf(`{"apiVersion":%q,"schemaVersion":%d}`,
					coremetadata.APIVersion, coremetadata.SchemaVersion+1))
			},
			want:  coremetadata.ErrSchemaTooNew,
			other: []error{ErrRegistryStateLost, ErrMalformedRegistry, ErrRegistryPermission},
		},
		{
			name: "unreadable",
			seed: func(t *testing.T, store *Store) {
				if os.Geteuid() == 0 {
					t.Skip("root bypasses the file mode, so an unreadable registry cannot be staged")
				}
				registerProject(t, store, "/src/projmux")
				if err := os.Chmod(store.Path(), 0o000); err != nil {
					t.Fatalf("chmod registry: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(store.Path(), 0o600) })
			},
			want:  ErrRegistryPermission,
			other: []error{ErrRegistryStateLost, ErrMalformedRegistry, coremetadata.ErrSchemaTooNew},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			tt.seed(t, store)
			// LoadSnapshot is the strict route: it neither locks nor repairs
			// permissions, so it classifies without touching anything.
			registry, err := store.LoadSnapshot()
			if !errors.Is(err, tt.want) {
				t.Fatalf("LoadSnapshot = %v, want %v", err, tt.want)
			}
			for _, other := range tt.other {
				if errors.Is(err, other) {
					t.Fatalf("%v is not distinguishable from %v", tt.want, other)
				}
			}
			if len(registry.Projects) != 0 || registry.SchemaVersion != 0 {
				t.Fatalf("a refused read answered %+v, want the zero registry", registry)
			}
			// No refused read may invent state: no registry, no marker, no uid.
			if tt.want == ErrRegistryStateLost {
				if _, statErr := os.Stat(store.Path()); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("a refused read recreated the registry: %v", statErr)
				}
			}
			assertNoStagedGarbage(t, store)
		})
	}
}

// TestMigrationKeepsItsVersionedBackupAndTakesNoRecoveryCopy pins the parity
// between the two copies. The pre-migration bytes already have a dedicated
// versioned backup, so a migration must not also spend a same-version recovery
// slot, and it must still establish the initialized boundary.
func TestMigrationKeepsItsVersionedBackupAndTakesNoRecoveryCopy(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	writeRegistryFile(t, store, olderEnvelopeRegistry)
	withOlderEnvelopeStep(store)

	result, err := store.Migrate()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !result.Migrated || result.BackupPath == "" {
		t.Fatalf("result = %+v, want a migration with a backup", result)
	}
	if got := readFile(t, result.BackupPath); got != olderEnvelopeRegistry {
		t.Fatal("the versioned backup does not hold the pre-migration bytes")
	}
	if copies := recoveryCopies(t, store); len(copies) != 0 {
		t.Fatalf("recovery copies = %v, want none: the versioned backup already holds the replaced bytes", copies)
	}
	if _, statErr := os.Stat(store.markerPath); statErr != nil {
		t.Fatalf("a migration write did not establish the initialized marker: %v", statErr)
	}
	migratedBytes := readFile(t, store.Path())

	// The next ordinary semantic write is back on the rolling copy path.
	registerProject(t, store, "/src/other")
	copies := recoveryCopies(t, store)
	if len(copies) != 1 {
		t.Fatalf("recovery copies = %v, want the migrated bytes", copies)
	}
	if got := readFile(t, filepath.Join(store.recoveryDir, copies[0])); got != migratedBytes {
		t.Fatal("the recovery copy does not hold the post-migration bytes it replaced")
	}
}
