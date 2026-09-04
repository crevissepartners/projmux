package metadata

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

const v3RootCollisionRegistry = `{
  "apiVersion": "projmux.io/v1alpha1",
  "schemaVersion": 3,
  "updatedAt": "2026-08-15T09:30:00Z",
  "projects": [{
    "apiVersion": "projmux.io/v1alpha1", "kind": "Project",
    "metadata": {"uid":"project-root","name":"project","displayName":"private project title","createdAt":"2026-08-15T09:30:00Z"},
    "spec": {"root":"/tmp","primaryWindowRef":"window-a"}, "status": {}
  }],
  "windows": [
    {"apiVersion":"projmux.io/v1alpha1","kind":"Window","metadata":{"uid":"window-a","name":"window-a","ownerRef":{"kind":"Project","uid":"project-root"},"createdAt":"2026-08-15T09:30:00Z"},"spec":{"anchorPaneRef":"pane-a","defaultShellPaneRef":"pane-a"}},
    {"apiVersion":"projmux.io/v1alpha1","kind":"Window","metadata":{"uid":"window-b","name":"window-b","displayName":"","ownerRef":{"kind":"Project","uid":"project-root"},"createdAt":"2026-08-15T09:30:00Z"},"spec":{"anchorPaneRef":"pane-b","defaultShellPaneRef":"pane-b"}}
  ],
  "panes": [
    {"apiVersion":"projmux.io/v1alpha1","kind":"Pane","metadata":{"uid":"pane-a","name":"duplicate","ownerRef":{"kind":"Window","uid":"window-a"},"createdAt":"2026-08-15T09:30:00Z"},"spec":{"role":"shell"},"status":{"displayTitle":"private pane title"}},
    {"apiVersion":"projmux.io/v1alpha1","kind":"Pane","metadata":{"uid":"pane-b","name":"duplicate","ownerRef":{"kind":"Window","uid":"window-b"},"createdAt":"2026-08-15T09:30:00Z"},"spec":{"role":"shell"},"status":{}}
  ],
  "nameReservations": [
    {"kind":"Project","name":"project","uid":"project-root"},
    {"scope":"project-root","kind":"Window","name":"window-a","uid":"window-a"},
    {"scope":"project-root","kind":"Window","name":"window-b","uid":"window-b"},
    {"scope":"window-a","kind":"Pane","name":"duplicate","uid":"pane-a"},
    {"scope":"window-b","kind":"Pane","name":"duplicate","uid":"pane-b"}
  ]
}`

func v3DestinationClosureRecoverySource(t *testing.T) string {
	t.Helper()
	const paneB = `{"apiVersion":"projmux.io/v1alpha1","kind":"Pane","metadata":{"uid":"pane-b","name":"duplicate","ownerRef":{"kind":"Window","uid":"window-b"},"createdAt":"2026-08-15T09:30:00Z"},"spec":{"role":"shell"},"status":{}}`
	const expandedPanes = paneB + `,
    {"apiVersion":"projmux.io/v1alpha1","kind":"Pane","metadata":{"uid":"pane-c","name":"pane-a","ownerRef":{"kind":"Window","uid":"window-a"},"createdAt":"2026-08-15T09:30:00Z"},"spec":{"role":"shell"},"status":{}},
    {"apiVersion":"projmux.io/v1alpha1","kind":"Pane","metadata":{"uid":"pane-d","name":"pane-c","ownerRef":{"kind":"Window","uid":"window-a"},"createdAt":"2026-08-15T09:30:00Z"},"spec":{"role":"shell"},"status":{}},
    {"apiVersion":"projmux.io/v1alpha1","kind":"Pane","metadata":{"uid":"pane-unique","name":"keep-me","ownerRef":{"kind":"Window","uid":"window-a"},"createdAt":"2026-08-15T09:30:00Z"},"spec":{"role":"shell"},"status":{}}`
	source := strings.Replace(v3RootCollisionRegistry, paneB, expandedPanes, 1)
	const reservationB = `{"scope":"window-b","kind":"Pane","name":"duplicate","uid":"pane-b"}`
	const expandedReservations = reservationB + `,
    {"scope":"window-a","kind":"Pane","name":"pane-a","uid":"pane-c"},
    {"scope":"window-a","kind":"Pane","name":"pane-c","uid":"pane-d"},
    {"scope":"window-a","kind":"Pane","name":"keep-me","uid":"pane-unique"}`
	source = strings.Replace(source, reservationB, expandedReservations, 1)
	if source == v3RootCollisionRegistry {
		t.Fatal("destination-closure source expansion did not apply")
	}
	return source
}

func TestV3ToV4StoreMigrationPublishesExactBackupContentFreeReportAndRepeatNoop(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	writeRegistryFile(t, store, v3RootCollisionRegistry)
	result, err := store.Migrate()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !result.Migrated || result.FromVersion != 3 || result.Report.ToVersion != coremetadata.SchemaVersion {
		t.Fatalf("migration result = %+v", result)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != v3RootCollisionRegistry {
		t.Fatal("backup is not the exact source bytes")
	}
	var evidence migrationEvidence
	reportBytes, err := os.ReadFile(result.ReportPath)
	if err != nil || json.Unmarshal(reportBytes, &evidence) != nil {
		t.Fatalf("read report: %v", err)
	}
	if evidence.BackupSHA256 != fmt.Sprintf("%x", sha256.Sum256(backup)) || len(evidence.NameRepairs) != 2 || len(evidence.FieldRemovals) != 3 {
		t.Fatalf("migration evidence = %+v", evidence)
	}
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	foundEmpty := false
	for _, removal := range evidence.FieldRemovals {
		if removal.UID == "window-b" && removal.Field == "metadata.displayName" {
			foundEmpty = removal.Present && removal.ByteLength == 0 && removal.SHA256 == emptySHA256 && !removal.InformationLoss
		}
	}
	if !foundEmpty {
		t.Fatalf("present-empty content-free receipt missing: %+v", evidence.FieldRemovals)
	}
	if strings.Contains(string(reportBytes), "private project title") || strings.Contains(string(reportBytes), "private pane title") {
		t.Fatalf("report leaked removed content: %s", reportBytes)
	}
	firstBytes := readFile(t, store.Path())
	if strings.Contains(firstBytes, "displayName") || strings.Contains(firstBytes, "displayTitle") || !strings.Contains(firstBytes, `"schemaVersion": 4`) {
		t.Fatalf("v4 registry retained removed schema fields:\n%s", firstBytes)
	}
	listingAfterFirst := dirListing(t, filepath.Dir(store.Path()))
	backupAfterFirst := readFile(t, result.BackupPath)
	reportAfterFirst := readFile(t, result.ReportPath)
	backupCount, reportCount := 0, 0
	for _, name := range listingAfterFirst {
		if strings.Contains(name, ".v3.") && strings.HasSuffix(name, ".bak") {
			backupCount++
		}
		if strings.HasSuffix(name, migrationReportSuffix) {
			reportCount++
		}
	}
	if backupCount != 1 || reportCount != 1 {
		t.Fatalf("migration evidence files backup=%d report=%d listing=%v", backupCount, reportCount, listingAfterFirst)
	}
	repeat, err := store.Migrate()
	if err != nil || repeat.Migrated || repeat.BackupPath != "" || repeat.ReportPath != "" {
		t.Fatalf("repeat migration = %+v, err=%v", repeat, err)
	}
	if got := readFile(t, store.Path()); got != firstBytes {
		t.Fatal("repeat migration changed registry bytes")
	}
	if got := dirListing(t, filepath.Dir(store.Path())); !reflect.DeepEqual(got, listingAfterFirst) {
		t.Fatalf("repeat migration changed backup/report count: before=%v after=%v", listingAfterFirst, got)
	}
	if got := readFile(t, result.BackupPath); got != backupAfterFirst {
		t.Fatal("repeat migration changed exact backup bytes")
	}
	if got := readFile(t, result.ReportPath); got != reportAfterFirst {
		t.Fatal("repeat migration changed report bytes")
	}
}

func TestCurrentV4RootWideCollisionLoadIsTotalZeroWrite(t *testing.T) {
	t.Parallel()

	currentCollision := strings.Replace(v3RootCollisionRegistry, `"schemaVersion": 3`, `"schemaVersion": 4`, 1)
	store := testStore(t)
	writeRegistryFile(t, store, currentCollision)
	before := readFile(t, store.Path())
	listing := dirListing(t, filepath.Dir(store.Path()))
	if _, err := store.Load(); !errors.Is(err, coremetadata.ErrInvalidRegistry) {
		t.Fatalf("Load error = %v, want ErrInvalidRegistry", err)
	}
	if got := readFile(t, store.Path()); got != before {
		t.Fatal("invalid current-v4 load changed Registry bytes")
	}
	if got := dirListing(t, filepath.Dir(store.Path())); !reflect.DeepEqual(got, listing) {
		t.Fatalf("invalid current-v4 load wrote backup/report/stage files: before=%v after=%v", listing, got)
	}
}

func TestInvalidV3OwnerGraphMigrationIsTotalZeroWrite(t *testing.T) {
	t.Parallel()

	invalid := strings.Replace(v3RootCollisionRegistry,
		`"ownerRef":{"kind":"Project","uid":"project-root"}`,
		`"ownerRef":{"kind":"Project","uid":"missing-root"}`, 1)
	store := testStore(t)
	writeRegistryFile(t, store, invalid)
	before := readFile(t, store.Path())
	listing := dirListing(t, filepath.Dir(store.Path()))

	if _, err := store.Migrate(); !errors.Is(err, coremetadata.ErrInvalidRegistry) {
		t.Fatalf("Migrate error = %v, want ErrInvalidRegistry", err)
	}
	if got := readFile(t, store.Path()); got != before {
		t.Fatal("invalid v3 owner graph changed Registry bytes")
	}
	if got := dirListing(t, filepath.Dir(store.Path())); !reflect.DeepEqual(got, listing) {
		t.Fatalf("invalid v3 owner graph wrote backup/report/stage files: before=%v after=%v", listing, got)
	}
}

func TestCurrentV4RootWideCollisionRecoveryImportRestoreIsTotalZeroWrite(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	if _, err := store.Update(func(*coremetadata.Registry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	liveBefore := readFile(t, store.Path())
	durableBefore := durableFingerprint(t, filepath.Dir(store.Path()))
	currentCollision := strings.Replace(v3RootCollisionRegistry, `"schemaVersion": 3`, `"schemaVersion": 4`, 1)
	source := filepath.Join(t.TempDir(), "current-v4-collision.json")
	if err := os.WriteFile(source, []byte(currentCollision), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceBefore := readFile(t, source)

	if _, err := store.RestoreFrom(RestoreRequest{SourcePath: source}); !errors.Is(err, ErrRecoverySourceRejected) {
		t.Fatalf("RestoreFrom error=%v, want ErrRecoverySourceRejected", err)
	}
	if got := readFile(t, store.Path()); got != liveBefore {
		t.Fatal("rejected current-v4 recovery import changed Registry bytes")
	}
	if got := durableFingerprint(t, filepath.Dir(store.Path())); !reflect.DeepEqual(got, durableBefore) {
		t.Fatalf("rejected current-v4 recovery import wrote durable state: before=%v after=%v", durableBefore, got)
	}
	if got := readFile(t, source); got != sourceBefore {
		t.Fatal("rejected current-v4 recovery import changed source bytes")
	}
}

func TestV3RecoveryImportCommitsCanonicalV4WithExactSourceEvidenceAndRepeatNoop(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	store.SetClock(func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) })
	if _, err := store.Update(func(*coremetadata.Registry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	sourceBytes := []byte(v3DestinationClosureRecoverySource(t))
	source := filepath.Join(t.TempDir(), "copied-v3.json")
	if err := os.WriteFile(source, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.RestoreFrom(RestoreRequest{SourcePath: source, ExpectSourceChecksum: checksumOf(sourceBytes)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.SourceChecksum != checksumOf(sourceBytes) || result.PublishedChecksum == result.SourceChecksum {
		t.Fatalf("restore result=%+v", result)
	}
	if result.Migration.FromVersion != 3 || result.Migration.ToVersion != coremetadata.SchemaVersion || len(result.Migration.NameRepairs) != 4 {
		t.Fatalf("migration receipt=%+v", result.Migration)
	}
	backup, err := os.ReadFile(result.SourceBackupPath)
	if err != nil || !reflect.DeepEqual(backup, sourceBytes) {
		t.Fatalf("exact source backup err=%v equal=%t", err, reflect.DeepEqual(backup, sourceBytes))
	}
	var evidence migrationEvidence
	reportBytes, err := os.ReadFile(result.MigrationReportPath)
	if err != nil || json.Unmarshal(reportBytes, &evidence) != nil {
		t.Fatalf("read migration evidence: %v", err)
	}
	if evidence.BackupSHA256 != fmt.Sprintf("%x", sha256.Sum256(sourceBytes)) || evidence.RepairCount != result.Migration.RepairCount() || len(evidence.NameRepairs) != 4 || len(evidence.FieldRemovals) != 3 {
		t.Fatalf("evidence=%+v", evidence)
	}
	if strings.Contains(string(reportBytes), "private project title") || strings.Contains(string(reportBytes), "private pane title") {
		t.Fatalf("migration evidence leaked removed content: %s", reportBytes)
	}
	var live coremetadata.Registry
	liveBytes := []byte(readFile(t, store.Path()))
	if err := json.Unmarshal(liveBytes, &live); err != nil || live.SchemaVersion != coremetadata.SchemaVersion || live.Validate() != nil {
		t.Fatalf("live canonical Registry error=%v schema=%d", err, live.SchemaVersion)
	}
	for _, uid := range []string{"pane-a", "pane-b", "pane-c", "pane-d"} {
		pane, _ := live.Pane(uid)
		if pane == nil || pane.Metadata.Name != uid {
			t.Fatalf("canonical Pane %s=%+v", uid, pane)
		}
	}
	unique, _ := live.Pane("pane-unique")
	if unique == nil || unique.Metadata.Name != "keep-me" {
		t.Fatalf("set-outside unique Pane=%+v", unique)
	}
	listing := dirListing(t, filepath.Dir(store.Path()))
	repeat, err := store.RestoreFrom(RestoreRequest{SourcePath: source, ExpectSourceChecksum: checksumOf(sourceBytes)})
	if err != nil || repeat.Changed {
		t.Fatalf("repeat restore=%+v err=%v", repeat, err)
	}
	if got := []byte(readFile(t, store.Path())); !reflect.DeepEqual(got, liveBytes) {
		t.Fatal("repeat restore changed canonical Registry bytes")
	}
	if got := dirListing(t, filepath.Dir(store.Path())); !reflect.DeepEqual(got, listing) {
		t.Fatalf("repeat restore changed durable evidence: before=%v after=%v", listing, got)
	}
}

func TestV3RecoveryImportFailpointsLeaveLiveRegistryByteIdentical(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		hook func(*Store)
	}{
		{name: "after source backup", hook: func(store *Store) {
			store.hooks.afterBackup = func() error { return errors.New("stop after source backup") }
		}},
		{name: "staged fsync", hook: func(store *Store) {
			store.hooks.syncFile = func(file *os.File) error {
				if strings.HasPrefix(filepath.Base(file.Name()), ".registry.json.tmp-") {
					return errors.New("stop at staged registry fsync")
				}
				return file.Sync()
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := testStore(t)
			if _, err := store.Update(func(*coremetadata.Registry) error { return nil }); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, store.Path())
			source := filepath.Join(t.TempDir(), "copied-v3.json")
			if err := os.WriteFile(source, []byte(v3DestinationClosureRecoverySource(t)), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.hook(store)
			if _, err := store.RestoreFrom(RestoreRequest{SourcePath: source}); err == nil {
				t.Fatal("injected failure succeeded")
			}
			if got := readFile(t, store.Path()); got != before {
				t.Fatal("failed v3 import changed live Registry bytes")
			}
			if names := store.preservedCopyNames(); len(names) != 0 {
				t.Fatalf("failed v3 import retained replaced copies: %v", names)
			}
			if staged := stagedTempFiles(t, filepath.Dir(store.Path())); len(staged) != 0 {
				t.Fatalf("failed v3 import leaked staged files: %v", staged)
			}
		})
	}
}
