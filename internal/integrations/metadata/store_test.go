package metadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
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

// dirListing reports the registry state a directory holds. The persistent lock
// descriptor is skipped: an advisory flock belongs to an open file description,
// so that file is created once and never unlinked, and its appearance the first
// time a store touches the directory says nothing about whether an operation
// wrote registry state. Every other name -- staged temps, backups, quarantine
// copies, the initialized marker -- is still reported.
func dirListing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), flockFileSuffix) {
			continue
		}
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
  "schemaVersion": 5,
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
	store.migrations = coremetadata.ProductionMigrationSet()
	store.migrations[0] = func(reg *coremetadata.Registry, _ coremetadata.MigrationEnvironment, _ *coremetadata.MigrationReport) error {
		reg.SchemaVersion = 1
		return nil
	}
}

func TestSchemaMigrationIsAtomicAndAFailedStepLeavesTheOriginalState(t *testing.T) {
	t.Parallel()

	boom := errors.New("injected migration failure")
	tests := []struct {
		name         string
		hooks        func(*Store)
		wantEvidence bool
	}{
		{name: "failure right after the backup", hooks: func(s *Store) {
			s.hooks.afterBackup = func() error { return boom }
		}},
		{name: "failure after the migration report", hooks: func(s *Store) {
			s.hooks.afterMigrationReport = func() error { return boom }
		}, wantEvidence: true},
		{name: "failure after the staged temp write", hooks: func(s *Store) {
			s.hooks.afterTempWrite = func() error { return boom }
		}, wantEvidence: true},
		{name: "failure just before the atomic replace", hooks: func(s *Store) {
			s.hooks.beforeRename = func() error { return boom }
		}, wantEvidence: true},
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
			var reports, backups []string
			for _, name := range dirListing(t, dir) {
				if strings.Contains(name, ".tmp-") {
					t.Fatalf("a failed migration leaked a staged temp file: %q", name)
				}
				if strings.HasSuffix(name, migrationReportSuffix) {
					reports = append(reports, filepath.Join(dir, name))
				} else if strings.Contains(name, ".bak") {
					backups = append(backups, filepath.Join(dir, name))
				}
			}
			if !tt.wantEvidence && (len(reports) != 0 || len(backups) != 0) {
				t.Fatalf("a pre-publication failure published operator evidence: reports=%v backups=%v", reports, backups)
			}
			if tt.wantEvidence {
				if len(reports) != 1 {
					t.Fatalf("post-publication evidence reports = %v, want one", reports)
				}
				var evidence migrationEvidence
				if err := json.Unmarshal([]byte(readFile(t, reports[0])), &evidence); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256([]byte(before))
				if readFile(t, evidence.BackupPath) != before || evidence.BackupSHA256 != fmt.Sprintf("%x", digest) {
					t.Fatalf("durable evidence does not preserve the exact source: %+v", evidence)
				}
			}
			// The original file must still be readable at its older envelope,
			// so a retry can migrate it cleanly.
			store.hooks = storeHooks{}
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

func TestIntermediateV2NormalizationPublishesExactEvidenceAndSecondPassIsZeroByte(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("../../core/metadata/testdata/registry-v010-intermediate-v2-source.json")
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	writeRegistryFile(t, store, string(source))
	sourceInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}

	result, err := store.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Migrated || result.FromVersion != 2 ||
		result.Report.FromVersion != 2 || result.Report.ToVersion != coremetadata.SchemaVersion ||
		len(result.Report.Repairs) != 1 {
		t.Fatalf("normalization result = %+v", result)
	}
	if got := []byte(readFile(t, result.BackupPath)); !bytes.Equal(got, source) {
		t.Fatal("same-version backup did not preserve exact source bytes")
	}
	backupInfo, err := os.Stat(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if backupInfo.Mode().Perm() != sourceInfo.Mode().Perm() || backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("source/backup mode = %v/%v, want exact 0600", sourceInfo.Mode().Perm(), backupInfo.Mode().Perm())
	}
	var evidence migrationEvidence
	if err := json.Unmarshal([]byte(readFile(t, result.ReportPath)), &evidence); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(source)
	if evidence.FromVersion != 2 || evidence.ToVersion != coremetadata.SchemaVersion ||
		evidence.BackupPath != result.BackupPath || evidence.BackupSHA256 != fmt.Sprintf("%x", digest) {
		t.Fatalf("same-version evidence = %+v", evidence)
	}
	got := []byte(readFile(t, store.Path()))
	if bytes.Contains(got, []byte(`"primaryPaneRef"`)) || bytes.Contains(got, []byte(`"displayName"`)) ||
		bytes.Contains(got, []byte(`"displayTitle"`)) || !bytes.Contains(got, []byte(`"schemaVersion": 4`)) {
		t.Fatalf("migrated bytes do not use the canonical v4 shape:\n%s", got)
	}
	var migrated coremetadata.Registry
	if err := json.Unmarshal(got, &migrated); err != nil {
		t.Fatal(err)
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("migrated v4 registry: %v", err)
	}

	registryBefore := fileFingerprint(t, store.Path())
	backupBefore := fileFingerprint(t, result.BackupPath)
	reportBefore := fileFingerprint(t, result.ReportPath)
	listingBefore := dirListing(t, filepath.Dir(store.Path()))
	second, err := store.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if second.Migrated || second.BackupPath != "" || second.ReportPath != "" || len(second.Report.Repairs) != 0 {
		t.Fatalf("second pass = %+v, want no-op", second)
	}
	if fileFingerprint(t, store.Path()) != registryBefore || fileFingerprint(t, result.BackupPath) != backupBefore ||
		fileFingerprint(t, result.ReportPath) != reportBefore ||
		!reflect.DeepEqual(dirListing(t, filepath.Dir(store.Path())), listingBefore) {
		t.Fatal("second pass changed final-v2 bytes, evidence, or directory entries")
	}
}

func TestCanonicalV2ToV4PublishesOneLosslessBackupReportAndRepeatsNoop(t *testing.T) {
	t.Parallel()
	current, err := os.ReadFile("../../core/metadata/testdata/registry-v010-v3-bytes.golden")
	if err != nil {
		t.Fatal(err)
	}
	v2 := bytes.Replace(current, []byte(`"schemaVersion": 3`), []byte(`"schemaVersion": 2`), 1)
	store := testStore(t)
	writeRegistryFile(t, store, string(v2))

	result, err := store.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Migrated || result.FromVersion != 2 || result.Report.FromVersion != 2 ||
		result.Report.ToVersion != coremetadata.SchemaVersion || len(result.Report.Repairs) != 0 || result.Report.InformationLossCount() != 0 {
		t.Fatalf("migration result = %+v", result)
	}
	if got := []byte(readFile(t, result.BackupPath)); !bytes.Equal(got, v2) {
		t.Fatal("v2 backup did not preserve exact source bytes")
	}
	got := []byte(readFile(t, store.Path()))
	var migrated coremetadata.Registry
	if err := json.Unmarshal(got, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.SchemaVersion != coremetadata.SchemaVersion {
		t.Fatalf("migrated schemaVersion = %d, want %d", migrated.SchemaVersion, coremetadata.SchemaVersion)
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("migrated v4 registry: %v", err)
	}
	var evidence migrationEvidence
	if err := json.Unmarshal([]byte(readFile(t, result.ReportPath)), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.FromVersion != 2 || evidence.ToVersion != coremetadata.SchemaVersion || evidence.RepairCount != 0 ||
		evidence.InformationLossCount != 0 || evidence.BackupPath != result.BackupPath {
		t.Fatalf("migration evidence = %+v", evidence)
	}
	listing := dirListing(t, filepath.Dir(store.Path()))
	second, err := store.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if second.Migrated || second.BackupPath != "" || second.ReportPath != "" ||
		!reflect.DeepEqual(listing, dirListing(t, filepath.Dir(store.Path()))) {
		t.Fatalf("repeat migration changed evidence: result=%+v", second)
	}
}

func TestSchemaV3WriterSimulationRefusesV4BeforeItsMutationCallback(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	writeRegistryFile(t, store, v3RootCollisionRegistry)
	if _, err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	data := []byte(readFile(t, store.Path()))
	before := bytes.Clone(data)
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	writes := 0
	legacyV3Write := func(body []byte, mutate func()) error {
		var header struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		if err := json.Unmarshal(body, &header); err != nil {
			return err
		}
		if header.SchemaVersion > 3 {
			return coremetadata.ErrSchemaTooNew
		}
		mutate()
		return nil
	}
	err := legacyV3Write(data, func() { writes++ })
	if envelope.SchemaVersion != 4 || !errors.Is(err, coremetadata.ErrSchemaTooNew) || writes != 0 || !bytes.Equal(data, before) {
		t.Fatalf("v3 writer downgrade refusal = version:%d err:%v writes:%d bytesChanged:%t",
			envelope.SchemaVersion, err, writes, !bytes.Equal(data, before))
	}
}

func TestIntermediateV2NormalizationFailureHonorsEvidencePublicationBoundary(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("../../core/metadata/testdata/registry-v010-intermediate-v2-source.json")
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("normalization cut point")
	tests := []struct {
		name         string
		hook         func(*Store)
		wantEvidence bool
	}{
		{name: "backup fsync before evidence pair", hook: func(store *Store) {
			store.hooks.syncFile = func(*os.File) error { return boom }
		}},
		{name: "backup directory publish before evidence pair", hook: func(store *Store) {
			store.hooks.syncDir = func(string) error { return boom }
		}},
		{name: "report fsync before evidence pair", hook: func(store *Store) {
			calls := 0
			store.hooks.syncFile = func(*os.File) error {
				calls++
				if calls == 2 {
					return boom
				}
				return nil
			}
		}},
		{name: "before evidence pair", hook: func(store *Store) {
			store.hooks.afterBackup = func() error { return boom }
		}},
		{name: "after evidence pair before replace", wantEvidence: true, hook: func(store *Store) {
			store.hooks.beforeRename = func() error { return boom }
		}},
		{name: "staged v4 validation after evidence pair", wantEvidence: true, hook: func(store *Store) {
			store.hooks.validateStaged = func(string) error { return boom }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := testStore(t)
			writeRegistryFile(t, store, string(source))
			tt.hook(store)
			if _, err := store.Migrate(); !errors.Is(err, boom) {
				t.Fatalf("Migrate error = %v", err)
			}
			if got := []byte(readFile(t, store.Path())); !bytes.Equal(got, source) {
				t.Fatal("failed normalization changed source Registry bytes")
			}
			var reportPaths, backupPaths []string
			for _, name := range dirListing(t, filepath.Dir(store.Path())) {
				if strings.Contains(name, ".tmp-") {
					t.Fatalf("failed normalization leaked staged file %q", name)
				}
				if strings.HasSuffix(name, migrationReportSuffix) {
					reportPaths = append(reportPaths, filepath.Join(filepath.Dir(store.Path()), name))
				} else if strings.Contains(name, ".bak") {
					backupPaths = append(backupPaths, filepath.Join(filepath.Dir(store.Path()), name))
				}
			}
			if !tt.wantEvidence && (len(reportPaths) != 0 || len(backupPaths) != 0) {
				t.Fatalf("pre-publication failure left evidence reports=%v backups=%v", reportPaths, backupPaths)
			}
			if tt.wantEvidence {
				if len(reportPaths) != 1 {
					t.Fatalf("reports = %v, want one durable pair", reportPaths)
				}
				var evidence migrationEvidence
				if err := json.Unmarshal([]byte(readFile(t, reportPaths[0])), &evidence); err != nil {
					t.Fatal(err)
				}
				if got := []byte(readFile(t, evidence.BackupPath)); !bytes.Equal(got, source) {
					t.Fatal("durable rollback backup changed source bytes")
				}
			}
		})
	}
}

func TestPrivateEvidencePublicationIsExclusive(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	path := filepath.Join(filepath.Dir(store.Path()), "evidence.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("operator-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.writePrivateFile(path, []byte("replacement\n")); err == nil {
		t.Fatal("exclusive evidence publication overwrote an existing path")
	}
	if got := readFile(t, path); got != "operator-owned\n" {
		t.Fatalf("existing evidence bytes = %q", got)
	}
}

func TestEveryFirstMigratorLeavesDurableV012RepairAndLossEvidenceOnce(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("../../core/metadata/testdata/registry-v012-source.json")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func(*Store) (coremetadata.Registry, MigrationResult, error)
	}{
		{name: "normal Load", run: func(store *Store) (coremetadata.Registry, MigrationResult, error) {
			registry, err := store.Load()
			return registry, MigrationResult{}, err
		}},
		{name: "Update", run: func(store *Store) (coremetadata.Registry, MigrationResult, error) {
			registry, err := store.Update(func(*coremetadata.Registry) error { return nil })
			return registry, MigrationResult{}, err
		}},
		{name: "UpdateConvergent", run: func(store *Store) (coremetadata.Registry, MigrationResult, error) {
			registry, changed, err := store.UpdateConvergent(func(*coremetadata.Registry) error { return nil })
			if err == nil && !changed {
				return coremetadata.Registry{}, MigrationResult{}, errors.New("first convergent migration reported no write")
			}
			return registry, MigrationResult{}, err
		}},
		{name: "Migrate", run: func(store *Store) (coremetadata.Registry, MigrationResult, error) {
			result, err := store.Migrate()
			if err != nil {
				return coremetadata.Registry{}, result, err
			}
			registry, err := store.LoadReadOnly()
			return registry, result, err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := testStore(t)
			writeRegistryFile(t, store, string(source))

			registry, result, err := tt.run(store)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Validate(); err != nil {
				t.Fatalf("migrated Registry: %v", err)
			}
			reportPath, evidence := requireSingleMigrationEvidence(t, store, source)
			if result.Migrated && (result.BackupPath != evidence.BackupPath || result.ReportPath != reportPath) {
				t.Fatalf("returned evidence = %+v, durable evidence = %s / %+v", result, reportPath, evidence)
			}

			registryBefore := fileFingerprint(t, store.Path())
			reportBefore := fileFingerprint(t, reportPath)
			listingBefore := dirListing(t, filepath.Dir(store.Path()))
			second, err := store.Migrate()
			if err != nil {
				t.Fatal(err)
			}
			if second.Migrated || second.BackupPath != "" || second.ReportPath != "" || len(second.Report.Repairs) != 0 {
				t.Fatalf("second pass = %+v, want no-op", second)
			}
			if fileFingerprint(t, store.Path()) != registryBefore || fileFingerprint(t, reportPath) != reportBefore ||
				!reflect.DeepEqual(dirListing(t, filepath.Dir(store.Path())), listingBefore) {
				t.Fatal("second migration pass changed Registry, evidence, or directory entries")
			}
		})
	}
}

func requireSingleMigrationEvidence(t *testing.T, store *Store, source []byte) (string, migrationEvidence) {
	t.Helper()
	var reportPaths []string
	for _, name := range dirListing(t, filepath.Dir(store.Path())) {
		if strings.HasSuffix(name, migrationReportSuffix) {
			reportPaths = append(reportPaths, filepath.Join(filepath.Dir(store.Path()), name))
		}
	}
	if len(reportPaths) != 1 {
		t.Fatalf("migration report paths = %v, want exactly one", reportPaths)
	}
	var evidence migrationEvidence
	if err := json.Unmarshal([]byte(readFile(t, reportPaths[0])), &evidence); err != nil {
		t.Fatalf("decode migration evidence: %v", err)
	}
	digest := sha256.Sum256(source)
	if evidence.EvidenceVersion != 1 || evidence.FromVersion != 1 || evidence.ToVersion != coremetadata.SchemaVersion ||
		evidence.RepairCount != 4 || evidence.InformationLossCount != 1 ||
		evidence.BackupSHA256 != fmt.Sprintf("%x", digest) || readFile(t, evidence.BackupPath) != string(source) {
		t.Fatalf("migration evidence = %+v", evidence)
	}
	encoded := readFile(t, reportPaths[0])
	for _, detail := range []string{"downgrade-missing-cwd", "migrate-window-anchor", "create-bare-shell", `"informationLoss": true`} {
		if !strings.Contains(encoded, detail) {
			t.Fatalf("migration evidence omitted %q:\n%s", detail, encoded)
		}
	}
	return reportPaths[0], evidence
}

func TestMigrateRejectsAnInvalidCurrentRegistryWithZeroWrite(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	invalid := fmt.Sprintf(`{
  "apiVersion": %q,
  "schemaVersion": %d,
  "projects": [{
    "apiVersion": %q,
    "kind": "Project",
    "metadata": {"uid": "project-invalid", "name": "invalid"},
    "spec": {"root": "/src/invalid"},
    "status": {}
  }]
}
`, coremetadata.APIVersion, coremetadata.SchemaVersion, coremetadata.APIVersion)
	writeRegistryFile(t, store, invalid)
	before := readFile(t, store.Path())
	listingBefore := dirListing(t, filepath.Dir(store.Path()))

	if result, err := store.Migrate(); !errors.Is(err, coremetadata.ErrInvalidRegistry) {
		t.Fatalf("Migrate = %+v, %v, want ErrInvalidRegistry", result, err)
	}
	if after := readFile(t, store.Path()); after != before {
		t.Fatalf("invalid current Registry changed:\n--- got ---\n%s\n--- want ---\n%s", after, before)
	}
	if listingAfter := dirListing(t, filepath.Dir(store.Path())); !reflect.DeepEqual(listingAfter, listingBefore) {
		t.Fatalf("invalid current migration changed directory: %v -> %v", listingBefore, listingAfter)
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
			Name:         "project-01",
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

func TestRegisteredProjectPersistsTheOfflineTopologyAndFinalWindowRefs(t *testing.T) {
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
	if !strings.Contains(got, `"schemaVersion": 4`) || strings.Contains(got, `"displayName"`) || strings.Contains(got, `"displayTitle"`) {
		t.Fatalf("registry file does not use the canonical v4 shape:\n%s", got)
	}

	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := reloaded.Validate(); err != nil {
		t.Fatalf("reloaded registry is invalid: %v", err)
	}
	for _, project := range reloaded.Projects {
		if project.Metadata.Name != project.Metadata.UID {
			t.Fatalf("automatic Project name = %q, want exact uid %q", project.Metadata.Name, project.Metadata.UID)
		}
	}
	for _, window := range reloaded.Windows {
		if window.Metadata.Name != "server" && window.Metadata.Name != window.Metadata.UID {
			t.Fatalf("automatic Window name = %q, want exact uid %q", window.Metadata.Name, window.Metadata.UID)
		}
	}
	for _, pane := range reloaded.Panes {
		if pane.Metadata.Name != pane.Metadata.UID {
			t.Fatalf("automatic Pane name = %q, want exact uid %q", pane.Metadata.Name, pane.Metadata.UID)
		}
	}
	project, ok := reloaded.ProjectByRoot("/src/projmux")
	if !ok {
		t.Fatal("project did not survive the round trip")
	}
	for _, window := range reloaded.WindowsOf(project.Metadata.UID) {
		pane, ok := reloaded.Pane(window.Spec.AnchorPaneRef)
		if !ok {
			t.Fatalf("window %q anchorPaneRef %q does not resolve after reload", window.Metadata.Name, window.Spec.AnchorPaneRef)
		}
		if pane.Metadata.OwnerUID() != window.Metadata.UID {
			t.Fatalf("window %q anchorPaneRef is owned by %q", window.Metadata.Name, pane.Metadata.OwnerUID())
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
	// Nothing on the lock path reads the wall clock any more: exclusion is the
	// kernel queue and staleness is the owner pid's liveness, so this test runs
	// on the fixed clock like every other store test. Its outcome no longer
	// depends on how much CPU the eight writers were given.
	store := NewStore(PathFor(t.TempDir()))
	store.SetClock(func() time.Time { return fixedNow })

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
	// Every lease released its kernel lock too, so the next writer is granted
	// without waiting at all.
	lease, err := store.acquireLock(t.Context())
	if err != nil {
		t.Fatalf("acquire after the concurrent updates: %v", err)
	}
	lease.release()
}

// steppingClock returns a clock that advances by step on every read. It is how
// a deadline test reaches its deadline: the lock's time budget is spent in
// simulated time, so the assertions never depend on how fast the machine ran
// the loop.
func steppingClock(start time.Time, step time.Duration) func() time.Time {
	var mu sync.Mutex
	now := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		reading := now
		now = now.Add(step)
		return reading
	}
}

// holdRegistryFlock takes the kernel lock through an independent descriptor, the
// way a second process would. flock is owned by the open file description, not
// by the process, so this contends with the store under test exactly as another
// install does.
func holdRegistryFlock(t *testing.T, store *Store) {
	t.Helper()
	if err := localstate.EnsurePrivateDir(filepath.Dir(store.flockPath)); err != nil {
		t.Fatalf("create lock dir: %v", err)
	}
	held, err := os.OpenFile(store.flockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open registry flock: %v", err)
	}
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("hold registry flock: %v", err)
	}
	release := sync.OnceFunc(func() {
		_ = unix.Flock(int(held.Fd()), unix.LOCK_UN)
		_ = held.Close()
	})
	heldRegistryFlocks.Store(store, release)
	t.Cleanup(release)
}

// heldRegistryFlocks lets a test release the stand-in holder before its cleanup
// runs, for the cases that need the wait to end inside the test body.
var heldRegistryFlocks sync.Map

func releaseRegistryFlock(t *testing.T, store *Store) {
	t.Helper()
	release, ok := heldRegistryFlocks.Load(store)
	if !ok {
		t.Fatal("no registry flock is held for this store")
	}
	release.(func())()
}

// writeLegacyMarker reproduces exactly what an install from before the kernel
// lock writes: the O_EXCL marker file and nothing else.
func writeLegacyMarker(t *testing.T, store *Store, contents string) {
	t.Helper()
	if err := localstate.EnsurePrivateDir(filepath.Dir(store.lockPath)); err != nil {
		t.Fatalf("create lock dir: %v", err)
	}
	if err := os.WriteFile(store.lockPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}
}

// exitedPID returns the pid of a process that has really been reaped, so the
// liveness predicate is exercised against an absent owner rather than against a
// number chosen in the hope that nothing is using it.
func exitedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reap throwaway process: %v", err)
	}
	return pid
}

// TestRegistryLockFailsOnItsDeadlineNotOnAnAttemptBudget pins the failure
// condition the C-1 contract now states: a mutation is allowed a fixed amount
// of time, and running out of it is reported as that timeout. The old budget
// reported "exhausted N attempts", which named how busy the machine was rather
// than anything the caller decided.
func TestRegistryLockFailsOnItsDeadlineNotOnAnAttemptBudget(t *testing.T) {
	t.Parallel()

	store := NewStore(PathFor(t.TempDir()))
	holdRegistryFlock(t, store)
	// The first reading starts the budget and the second is already past it, so
	// the contended wait ends on the deadline without any real elapsed time.
	store.SetClock(steppingClock(fixedNow, time.Minute))

	sleeps := 0
	lease, err := store.acquireLockWithDeadline(t.Context(), 30*time.Second, func(time.Duration) { sleeps++ })
	if lease != nil {
		lease.release()
		t.Fatal("a lease was granted while another writer held the kernel lock")
	}
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("contended acquire error = %v, want %v", err, ErrLockTimeout)
	}
	if got := err.Error(); !strings.Contains(got, "after 30s") || strings.Contains(got, "attempt") {
		t.Fatalf("timeout error = %q, want the granted budget and no attempt count", got)
	}
	if sleeps != 0 {
		t.Fatalf("backoff sleeps = %d, want 0 before the kernel lock is granted", sleeps)
	}
	assertNoStagedGarbage(t, store)
}

// TestRegistryLockDeadlineIsMeasuredOnTheInjectedClock is the test that the
// wall-clock stale window used to make impossible. Nothing here reads the real
// clock, so the result cannot change with machine load.
func TestRegistryLockDeadlineIsMeasuredOnTheInjectedClock(t *testing.T) {
	t.Parallel()

	store := NewStore(PathFor(t.TempDir()))
	writeLegacyMarker(t, store, fmt.Sprintf("pid=%d\n", os.Getpid()))
	store.SetClock(steppingClock(fixedNow, 250*time.Millisecond))

	sleeps := 0
	lease, err := store.acquireLockWithDeadline(t.Context(), time.Second, func(time.Duration) { sleeps++ })
	if lease != nil {
		lease.release()
		t.Fatal("a lease was granted while a live legacy holder owned the marker")
	}
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("legacy contention error = %v, want %v", err, ErrLockTimeout)
	}
	if sleeps == 0 {
		t.Fatal("the legacy marker wait never backed off, so it never actually waited")
	}
	assertNoStagedGarbage(t, store)
}

// TestRegistryLockReclaimsAMarkerOnlyWhenItsOwnerIsGone covers the stale
// predicate from both sides. A dead owner is reclaimed immediately instead of
// after a fixed wall-clock window, and everything that does not prove the owner
// is gone -- a live pid, an empty file, a malformed line, an unreadable path --
// keeps the marker, because reclaiming on a guess hands two writers the lock.
func TestRegistryLockReclaimsAMarkerOnlyWhenItsOwnerIsGone(t *testing.T) {
	t.Parallel()

	t.Run("a reaped owner is reclaimed at once", func(t *testing.T) {
		t.Parallel()

		store := NewStore(PathFor(t.TempDir()))
		writeLegacyMarker(t, store, fmt.Sprintf("pid=%d\n", exitedPID(t)))
		store.SetClock(steppingClock(fixedNow, time.Minute))

		sleeps := 0
		lease, err := store.acquireLockWithDeadline(t.Context(), 30*time.Second, func(time.Duration) { sleeps++ })
		if err != nil {
			t.Fatalf("acquire over a dead owner's marker: %v", err)
		}
		if sleeps != 0 {
			t.Fatalf("backoff sleeps = %d, want 0 for an owner already gone", sleeps)
		}
		owner, ok := observedLockOwnerPID(store.lockPath)
		if !ok || owner != os.Getpid() {
			t.Fatalf("marker owner = %d (parsed=%t), want this process %d", owner, ok, os.Getpid())
		}
		lease.release()
		if _, err := os.Stat(store.lockPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the marker survived release: %v", err)
		}
		assertNoStagedGarbage(t, store)
	})

	for _, test := range []struct {
		name  string
		write func(*testing.T, *Store)
	}{
		{name: "a live owner", write: func(t *testing.T, store *Store) {
			t.Helper()
			writeLegacyMarker(t, store, fmt.Sprintf("pid=%d\n", os.Getpid()))
		}},
		{name: "an empty marker", write: func(t *testing.T, store *Store) {
			t.Helper()
			writeLegacyMarker(t, store, "")
		}},
		{name: "a malformed marker", write: func(t *testing.T, store *Store) {
			t.Helper()
			writeLegacyMarker(t, store, "owner=not-a-pid\n")
		}},
		{name: "an unreadable marker", write: func(t *testing.T, store *Store) {
			t.Helper()
			if err := localstate.EnsurePrivateDir(filepath.Dir(store.lockPath)); err != nil {
				t.Fatalf("create lock dir: %v", err)
			}
			if err := os.Mkdir(store.lockPath, 0o700); err != nil {
				t.Fatalf("replace the marker with an unreadable directory: %v", err)
			}
		}},
	} {
		t.Run(test.name+" is never reclaimed", func(t *testing.T) {
			t.Parallel()

			store := NewStore(PathFor(t.TempDir()))
			test.write(t, store)
			store.SetClock(steppingClock(fixedNow, time.Minute))

			lease, err := store.acquireLockWithDeadline(t.Context(), 30*time.Second, func(time.Duration) {})
			if lease != nil {
				lease.release()
				t.Fatal("the marker was reclaimed without proof that its owner is gone")
			}
			if !errors.Is(err, ErrLockTimeout) {
				t.Fatalf("error = %v, want %v", err, ErrLockTimeout)
			}
			if err := os.RemoveAll(store.lockPath); err != nil {
				t.Fatalf("remove marker fixture: %v", err)
			}
			assertNoStagedGarbage(t, store)
		})
	}
}

// TestRegistryLockKeepsMutualExclusionWithAMarkerOnlyInstall covers the
// compatibility window in both directions. An older install sees only the
// marker, so the marker must still be written while the kernel lock is held;
// and an older install that took the marker first must still exclude us even
// though it never touches the kernel lock.
func TestRegistryLockKeepsMutualExclusionWithAMarkerOnlyInstall(t *testing.T) {
	t.Parallel()

	t.Run("a held lease blocks the marker-only path", func(t *testing.T) {
		t.Parallel()

		store := NewStore(PathFor(t.TempDir()))
		if err := localstate.EnsurePrivateDir(filepath.Dir(store.lockPath)); err != nil {
			t.Fatalf("create lock dir: %v", err)
		}
		store.SetClock(func() time.Time { return fixedNow })

		lease, err := store.acquireLockWithDeadline(t.Context(), 30*time.Second, func(time.Duration) {})
		if err != nil {
			t.Fatalf("acquire on an idle registry: %v", err)
		}
		// This is the whole of the older install's acquisition.
		legacy, err := os.OpenFile(store.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = legacy.Close()
			t.Fatal("a marker-only install acquired the lock while a lease was held")
		}
		if !errors.Is(err, os.ErrExist) {
			t.Fatalf("marker-only acquisition error = %v, want %v", err, os.ErrExist)
		}
		lease.release()
		// Once the lease is released the older install proceeds, so the
		// compatibility window costs it nothing but the wait it already had.
		reopened, err := os.OpenFile(store.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("marker-only acquisition after release: %v", err)
		}
		_ = reopened.Close()
		if err := os.Remove(store.lockPath); err != nil {
			t.Fatalf("release the marker-only lock: %v", err)
		}
		assertNoStagedGarbage(t, store)
	})

	t.Run("a marker-only holder blocks a lease", func(t *testing.T) {
		t.Parallel()

		store := NewStore(PathFor(t.TempDir()))
		writeLegacyMarker(t, store, fmt.Sprintf("pid=%d\n", os.Getpid()))
		store.SetClock(steppingClock(fixedNow, time.Minute))

		lease, err := store.acquireLockWithDeadline(t.Context(), 30*time.Second, func(time.Duration) {})
		if lease != nil {
			lease.release()
			t.Fatal("a lease was granted while a marker-only install held the lock")
		}
		if !errors.Is(err, ErrLockTimeout) {
			t.Fatalf("error = %v, want %v", err, ErrLockTimeout)
		}
		// The refused acquisition must leave the older install's marker alone.
		owner, ok := observedLockOwnerPID(store.lockPath)
		if !ok || owner != os.Getpid() {
			t.Fatalf("marker owner after a refused acquire = %d (parsed=%t), want it untouched", owner, ok)
		}
		if err := os.Remove(store.lockPath); err != nil {
			t.Fatalf("remove marker fixture: %v", err)
		}
		assertNoStagedGarbage(t, store)
	})
}

// TestRegistryLockGrantsEveryWriterUnderDeliberateContention is the direct
// enforcement of the C-1 guarantee. Contention is created on purpose -- every
// worker loops on acquire and release with no pacing at all -- and the
// assertions are that every acquisition eventually succeeds and that no two
// critical sections overlap.
//
// The occupancy counter is atomic rather than plain. A file lock is invisible
// to the race detector, which builds its happens-before edges out of Go
// synchronization only, so a plain counter here would be reported as a race
// even when exclusion is perfect. What `-race` contributes to this test is
// coverage of the store's own concurrent internals; the exclusion itself is
// asserted by the counter.
func TestRegistryLockGrantsEveryWriterUnderDeliberateContention(t *testing.T) {
	t.Parallel()

	const (
		writers = 8
		rounds  = 16
	)
	store := NewStore(PathFor(t.TempDir()))
	store.SetClock(func() time.Time { return fixedNow })
	if err := localstate.EnsurePrivateDir(filepath.Dir(store.lockPath)); err != nil {
		t.Fatalf("create lock dir: %v", err)
	}

	var inside, overlaps atomic.Int32
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for worker := range writers {
		wg.Go(func() {
			for range rounds {
				lease, err := store.acquireLock(t.Context())
				if err != nil {
					errs[worker] = err
					return
				}
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				inside.Add(-1)
				lease.release()
			}
		})
	}
	wg.Wait()

	for worker, err := range errs {
		if err != nil {
			t.Fatalf("writer %d never acquired the lock: %v", worker, err)
		}
	}
	if overlaps.Load() != 0 {
		t.Fatalf("overlapping critical sections = %d, want 0", overlaps.Load())
	}
	if _, err := os.Stat(store.lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the marker survived the contended rounds: %v", err)
	}
	assertNoStagedGarbage(t, store)
}

// TestAConcurrentWriterIsNotRefusedInsideAnotherCommitsMarkerWindow pins the
// C-1 assumption against the one window where a healthy Registry looks lost.
//
// A commit publishes the initialized marker before it renames the staged
// registry into place. An observer without the lock that lands in that window
// sees "the marker records a completed write but registry.json is missing" --
// byte for byte the state-loss signature -- while nothing is wrong at all. The
// degraded gate must not turn a neighbour's in-flight commit into a refusal: a
// writer that is alive and progressing is exactly what the deadline says a
// waiter should wait for.
//
// The window is built directly rather than raced into: the kernel lock stands in
// for the committing writer, and the registry file is moved aside to reproduce
// what that writer's transaction would transiently leave on disk.
func TestAConcurrentWriterIsNotRefusedInsideAnotherCommitsMarkerWindow(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	registerProject(t, store, "/src/first")
	staged := store.Path() + ".staged-by-the-committing-writer"
	if err := os.Rename(store.Path(), staged); err != nil {
		t.Fatalf("open the marker window: %v", err)
	}
	holdRegistryFlock(t, store)
	suspected := make(chan struct{})
	continueToLock := make(chan struct{})
	waitingOnFlock := make(chan struct{})
	store.hooks.afterDegradedSuspect = func() {
		close(suspected)
		<-continueToLock
	}
	store.hooks.afterContendedFlock = func() { close(waitingOnFlock) }

	done := make(chan error, 1)
	go func() {
		mutator := coremetadata.Mutator{
			Now:       func() time.Time { return fixedNow },
			NewUID:    coremetadata.NewUID,
			DirExists: func(path string) (bool, error) { return path == "/src/second", nil },
		}
		_, err := store.Update(func(registry *coremetadata.Registry) error {
			_, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
				Root:         "/src/second",
				DefaultShell: "/bin/zsh",
				OperationID:  "op-second",
			})
			return err
		})
		done <- err
	}()

	// The two test seams turn the pre-lock suspicion and the contended flock into
	// explicit barriers. A mutant that returns the lock-free suspicion answers
	// on done; the production path reaches waitingOnFlock and cannot be confused
	// with a slow scheduler.
	<-suspected
	close(continueToLock)
	select {
	case err := <-done:
		t.Fatalf("the second writer answered inside another commit's marker window: %v", err)
	case <-waitingOnFlock:
	}

	// The committing writer finishes: the staged bytes become the registry and
	// its lock is released.
	if err := os.Rename(staged, store.Path()); err != nil {
		t.Fatalf("close the marker window: %v", err)
	}
	releaseRegistryFlock(t, store)

	if err := <-done; err != nil {
		t.Fatalf("the second writer was refused by a window that had closed: %v", err)
	}
	registry, err := store.Load()
	if err != nil {
		t.Fatalf("load after the window: %v", err)
	}
	if len(registry.Projects) != 2 {
		t.Fatalf("projects = %d, want both writers' registrations", len(registry.Projects))
	}
	assertNoStagedGarbage(t, store)
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
