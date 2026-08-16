package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// keymapBackupFiles lists the pre-v1 backups sitting next to a keymap.
func keymapBackupFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read keymap directory: %v", err)
	}
	var out []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".pre-v1-") && strings.HasSuffix(entry.Name(), ".bak") {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// testKeymapCanonicalIDs lists every canonical id, sorted.
func testKeymapCanonicalIDs() []string {
	seen := map[string]bool{}
	var ids []string
	for _, action := range defaultKeyBindingCatalog() {
		if action.CanonicalID == "" || seen[action.CanonicalID] {
			continue
		}
		seen[action.CanonicalID] = true
		ids = append(ids, action.CanonicalID)
	}
	sort.Strings(ids)
	return ids
}

// newKeymapFixture writes a keymap under an isolated HOME and returns the store
// plus the keymap path.
func newKeymapFixture(t *testing.T, body string) (keymapStore, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return keymapStore{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}, path
}

// --- Verification expectation 1: exhaustive manifest, orphan/duplicate guards ---

func TestKeymapManifestCoversEveryCatalogActionExactlyOnce(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	manifest := keymapActionManifest()

	for _, action := range catalog {
		if strings.TrimSpace(action.CanonicalID) == "" {
			t.Fatalf("action %s has no canonical id; every catalogued action must have a v1 spelling", action.ID)
		}
		for _, id := range keyBindingActionAliases(action) {
			entry, ok := manifest[id]
			if !ok {
				t.Fatalf("manifest has no disposition for %q (action %s)", id, action.ID)
			}
			if entry.CanonicalID != action.CanonicalID {
				t.Fatalf("manifest maps %q to %q, want %q", id, entry.CanonicalID, action.CanonicalID)
			}
		}
	}

	// Every manifest row must resolve back to a live action: an orphan row is a
	// rename with no destination.
	for id, entry := range manifest {
		if _, ok := keyBindingActionByID(catalog, id); !ok {
			t.Fatalf("manifest row %q resolves to no action", id)
		}
		if _, ok := keyBindingActionByID(catalog, entry.CanonicalID); !ok {
			t.Fatalf("manifest canonical id %q resolves to no action", entry.CanonicalID)
		}
	}
}

func TestKeymapCanonicalIDsAreUniqueAndDistinctFromLegacyIDs(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	owner := map[string]string{}
	legacy := map[string]string{}
	for _, action := range catalog {
		if prev, ok := owner[action.CanonicalID]; ok {
			t.Fatalf("canonical id %q is claimed by both %s and %s", action.CanonicalID, prev, action.ID)
		}
		owner[action.CanonicalID] = action.ID
		legacy[action.ID] = action.ID
	}
	// A canonical id that collides with some *other* action's legacy id would
	// make a migration silently move a binding between actions.
	for canonical, actionID := range owner {
		if other, ok := legacy[canonical]; ok && other != actionID {
			t.Fatalf("canonical id %q collides with the legacy id of %s", canonical, other)
		}
	}
}

func TestKeymapRetiredIDsAreDisjointFromTheManifest(t *testing.T) {
	t.Parallel()

	manifest := keymapActionManifest()
	seen := map[string]bool{}
	for _, retired := range keymapRetiredIDs() {
		if seen[retired.ID] {
			t.Fatalf("retired id %q is listed twice", retired.ID)
		}
		seen[retired.ID] = true
		if _, ok := manifest[retired.ID]; ok {
			t.Fatalf("retired id %q also has a manifest disposition; an id gets exactly one", retired.ID)
		}
		if strings.TrimSpace(retired.Remediation) == "" {
			t.Fatalf("retired id %q has no remediation", retired.ID)
		}
	}
}

func TestKeymapCanonicalIDsUseTheDottedSchema(t *testing.T) {
	t.Parallel()

	for _, id := range testKeymapCanonicalIDs() {
		if !strings.Contains(id, ".") {
			t.Fatalf("canonical id %q is not dotted", id)
		}
		if id != strings.ToLower(id) {
			t.Fatalf("canonical id %q is not lower-case", id)
		}
		if strings.ContainsAny(id, " \t:\"") {
			t.Fatalf("canonical id %q contains an unsupported character", id)
		}
		// The reader only accepts a dotted id when it is quoted, so the writer
		// has to quote it.
		if formatKeymapActionID(id) != `"`+id+`"` {
			t.Fatalf("canonical id %q would be written unquoted", id)
		}
	}
}

func TestKeymapDispositionIsExactlyOnePerTable(t *testing.T) {
	t.Parallel()

	parsed, err := parseKeymapFile("keymap.toml", `[bindings.new-window]
keys = ["C-t"]

[bindings.sessionizer]
keys = ["M-9"]

[bindings.some-future-action]
keys = ["M-0"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	_, changes, conflicts := buildKeymapMigration(parsed)
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}

	got := map[string]keymapDisposition{}
	for _, change := range changes {
		if prev, ok := got[change.SourceID]; ok {
			t.Fatalf("%q has two dispositions: %s and %s", change.SourceID, prev, change.Disposition)
		}
		got[change.SourceID] = change.Disposition
	}
	want := map[string]keymapDisposition{
		"new-window":         keymapDispositionCanonical,
		"sessionizer":        keymapDispositionRetired,
		"some-future-action": keymapDispositionPreserveUnknown,
	}
	for id, disposition := range want {
		if got[id] != disposition {
			t.Fatalf("%q disposition = %q, want %q", id, got[id], disposition)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("dispositions = %v, want exactly %v", got, want)
	}
}

// --- Verification expectation 2: parse/render/merge golden and behaviour parity ---

func TestKeymapMigrationPreservesEffectiveBindings(t *testing.T) {
	t.Parallel()

	// One table per interesting v0 shape: a custom multi-alias, an explicit
	// unbind, a transport-dependent action whose default must not be stored,
	// a legacy `plain` single-primary and a legacy `prefix` remnant.
	store, path := newKeymapFixture(t, `[bindings.ProjectSidebarToggle]
keys = ["M-1", "M-a"]

[bindings.SessionPopupToggle]
keys = []

[bindings.previous-window]
keys = ["M-["]

[bindings.new-window]
plain = "C-t"

[bindings."Sidebar:PinProject"]
keys = ["p"]
prefix = "P"
`)

	before, _, err := loadMergedKeyBindingCatalog(keymapLoader{homeDir: store.homeDir, lookupEnv: store.lookupEnv})
	if err != nil {
		t.Fatalf("load before migration: %v", err)
	}

	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	if !result.Migrated {
		t.Fatal("migrateKeymapForWrite() did not migrate a v0 file")
	}

	after, _, err := loadMergedKeyBindingCatalog(keymapLoader{homeDir: store.homeDir, lookupEnv: store.lookupEnv})
	if err != nil {
		t.Fatalf("load after migration: %v", err)
	}

	if len(before) != len(after) {
		t.Fatalf("action count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID {
			t.Fatalf("action %d changed from %s to %s", i, before[i].ID, after[i].ID)
		}
		gotKeys := keyBindingEffectivePlainChords(after[i])
		wantKeys := keyBindingEffectivePlainChords(before[i])
		if !slices.Equal(gotKeys, wantKeys) {
			t.Fatalf("%s keys = %v, want %v", before[i].ID, gotKeys, wantKeys)
		}
		if before[i].PrefixChord != after[i].PrefixChord {
			t.Fatalf("%s prefix = %q, want %q", before[i].ID, after[i].PrefixChord, before[i].PrefixChord)
		}
	}

	migrated := readFile(t, path)
	for _, want := range []string{
		"schema_version = 1\n",
		`[bindings."project-sidebar.toggle"]`,
		`[bindings."session-picker.toggle"]`,
		"keys = []\n",
		`[bindings."window.focus-previous"]`,
		`[bindings."window.create"]`,
		`plain = "C-t"`,
		`[bindings."project-sidebar.project.pin-toggle"]`,
		`prefix = "P"`,
	} {
		if !strings.Contains(migrated, want) {
			t.Fatalf("migrated keymap = %q, want %q", migrated, want)
		}
	}
	// No v0 spelling may survive the rewrite.
	for _, gone := range []string{"[bindings.ProjectSidebarToggle]", "[bindings.new-window]", `[bindings."Sidebar:PinProject"]`} {
		if strings.Contains(migrated, gone) {
			t.Fatalf("migrated keymap still contains v0 table %q:\n%s", gone, migrated)
		}
	}
}

// TestKeymapMigrationKeepsTheAIPickerDefaultSwapWorking is a regression test for
// an alias-blind lookup found by migrating a real user keymap.
//
// `migrateLegacyAIPickerDefaultOverrides` resolves the M-4/M-7 collision
// between the AI split picker and the AI resume picker by reading the file's
// binding table for one action and moving the *other* action's default out of
// the way. It used to index keymap.Bindings by the v0 action id directly. Once a
// file is migrated the table is named `agent-resume-picker.toggle`, the direct
// lookup misses, the swap does not run, and the merge fails with M-7 bound to
// both actions — so a v0 file that worked would refuse to migrate.
//
// The failure is caught before any write, but "refuses to migrate" is not an
// acceptable outcome for a file the user already has.
func TestKeymapMigrationKeepsTheAIPickerDefaultSwapWorking(t *testing.T) {
	t.Parallel()

	store, path := newKeymapFixture(t, `[bindings.AIResumePickerToggle]
keys = ["M-7", "C-r"]

[bindings.ProjectSidebarToggle]
keys = ["M-1"]
`)
	before, _, err := loadMergedKeyBindingCatalog(keymapLoader{homeDir: store.homeDir, lookupEnv: store.lookupEnv})
	if err != nil {
		t.Fatalf("load before migration: %v", err)
	}

	if _, err := migrateKeymapForWrite(store); err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}

	after, _, err := loadMergedKeyBindingCatalog(keymapLoader{homeDir: store.homeDir, lookupEnv: store.lookupEnv})
	if err != nil {
		t.Fatalf("load after migration: %v", err)
	}
	for _, id := range []string{"AIResumePickerToggle", "AISplitPickerToggle"} {
		wantAction, ok := keyBindingActionByID(before, id)
		if !ok {
			t.Fatalf("missing %s before migration", id)
		}
		gotAction, ok := keyBindingActionByID(after, id)
		if !ok {
			t.Fatalf("missing %s after migration", id)
		}
		want := keyBindingEffectivePlainChords(wantAction)
		got := keyBindingEffectivePlainChords(gotAction)
		if !slices.Equal(got, want) {
			t.Fatalf("%s keys = %v, want %v", id, got, want)
		}
	}
	if !strings.Contains(readFile(t, path), `[bindings."agent-resume-picker.toggle"]`) {
		t.Fatal("expected the resume picker table to be migrated")
	}
}

func TestKeymapV1FileParsesAndRoundTrips(t *testing.T) {
	t.Parallel()

	body := `schema_version = 1

[bindings."window.create"]
keys = ["C-t"]

[bindings."project-sidebar.runtime.stop"]
keys = ["C-x"]
`
	parsed, err := parseKeymapFile("keymap.toml", body)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	if parsed.SchemaVersion != keymapSchemaVersionV1 {
		t.Fatalf("schema version = %d, want %d", parsed.SchemaVersion, keymapSchemaVersionV1)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	newWindow, ok := keyBindingActionByID(merged, "new-window")
	if !ok {
		t.Fatal("missing new-window action")
	}
	if got := keyBindingEffectivePlainChords(newWindow); !slices.Equal(got, []string{"C-t"}) {
		t.Fatalf("window.create keys = %v, want [C-t]", got)
	}

	rendered := renderKeymapFile(parsed)
	reparsed, err := parseKeymapFile("keymap.toml", rendered)
	if err != nil {
		t.Fatalf("reparse rendered file: %v", err)
	}
	if renderKeymapFile(reparsed) != rendered {
		t.Fatalf("render is not stable:\nfirst:  %q\nsecond: %q", rendered, renderKeymapFile(reparsed))
	}
}

func TestKeymapRejectsUnquotedDottedTableAndFutureSchema(t *testing.T) {
	t.Parallel()

	if _, err := parseKeymapFile("keymap.toml", "[bindings.window.create]\nkeys = [\"C-t\"]\n"); err == nil {
		t.Fatal("parseKeymapFile() = nil, want rejection of an unquoted dotted table")
	}
	_, err := parseKeymapFile("keymap.toml", "schema_version = 99\n")
	if err == nil || !strings.Contains(err.Error(), "newer than the supported version") {
		t.Fatalf("parseKeymapFile() error = %v, want a forward-version refusal", err)
	}
	if _, err := parseKeymapFile("keymap.toml", "schema_version = 1\nschema_version = 1\n"); err == nil {
		t.Fatal("parseKeymapFile() = nil, want rejection of a duplicate schema_version")
	}
	if _, err := parseKeymapFile("keymap.toml", "unexpected = 1\n"); err == nil {
		t.Fatal("parseKeymapFile() = nil, want rejection of an unknown root key")
	}
}

// --- Verification expectation 3: dual tables, unknown preservation, repeat no-op ---

func TestKeymapMigrationCoalescesIdenticalDualTables(t *testing.T) {
	t.Parallel()

	store, path := newKeymapFixture(t, `[bindings.new-window]
keys = ["C-t"]

[bindings."window.create"]
keys = ["C-t"]
`)
	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	if !result.Migrated {
		t.Fatal("expected a migration")
	}
	migrated := readFile(t, path)
	if got := strings.Count(migrated, "\n[bindings."); got != 1 {
		t.Fatalf("migrated keymap has %d tables, want 1 coalesced table:\n%s", got, migrated)
	}
	if !strings.Contains(migrated, `[bindings."window.create"]`) {
		t.Fatalf("coalesced table is not canonical:\n%s", migrated)
	}
	for _, change := range result.Plan.Changes {
		if change.Disposition != keymapDispositionCoalesce {
			t.Fatalf("change %+v disposition = %q, want coalesce", change, change.Disposition)
		}
	}
}

func TestKeymapMigrationRefusesConflictingDualTablesWithZeroWrites(t *testing.T) {
	t.Parallel()

	original := `[bindings.new-window]
keys = ["C-t"]

[bindings."window.create"]
keys = ["C-n"]
`
	store, path := newKeymapFixture(t, original)
	result, err := migrateKeymapForWrite(store)
	if err == nil {
		t.Fatal("migrateKeymapForWrite() = nil, want a conflict refusal")
	}
	if !strings.Contains(err.Error(), "conflicting binding table") {
		t.Fatalf("error = %v, want a conflict report", err)
	}
	if result.Migrated {
		t.Fatal("a blocked plan must not report a migration")
	}
	if got := readFile(t, path); got != original {
		t.Fatalf("keymap was rewritten despite the conflict:\n%s", got)
	}
	if backups := keymapBackupFiles(t, filepath.Dir(path)); len(backups) != 0 {
		t.Fatalf("backups = %v, want none; a blocked plan writes nothing at all", backups)
	}
}

func TestKeymapMigrationPreservesUnknownTables(t *testing.T) {
	t.Parallel()

	store, path := newKeymapFixture(t, `[bindings.new-window]
keys = ["C-t"]

[bindings.action-from-a-newer-projmux]
keys = ["M-0"]
`)
	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	migrated := readFile(t, path)
	if !strings.Contains(migrated, "[bindings.action-from-a-newer-projmux]") {
		t.Fatalf("unknown table was dropped:\n%s", migrated)
	}
	var reported bool
	for _, change := range result.Plan.Changes {
		if change.SourceID == "action-from-a-newer-projmux" {
			reported = change.Disposition == keymapDispositionPreserveUnknown
		}
	}
	if !reported {
		t.Fatalf("unknown table was not reported as unmapped: %+v", result.Plan.Changes)
	}
}

func TestKeymapMigrationRepeatRunIsAByteWriteFreeNoOp(t *testing.T) {
	t.Parallel()

	store, path := newKeymapFixture(t, "[bindings.new-window]\nkeys = [\"C-t\"]\n")
	if _, err := migrateKeymapForWrite(store); err != nil {
		t.Fatalf("first migration error = %v", err)
	}
	first := readFile(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstModTime := info.ModTime()

	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("second migration error = %v", err)
	}
	if result.Migrated {
		t.Fatal("second migration rewrote an already-current file")
	}
	if result.Plan.Required {
		t.Fatal("second plan still reports a required migration")
	}
	if got := readFile(t, path); got != first {
		t.Fatalf("second migration changed bytes:\ngot:  %q\nwant: %q", got, first)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstModTime) {
		t.Fatal("second migration rewrote the file in place; a no-op must not touch it at all")
	}
	if backups := keymapBackupFiles(t, filepath.Dir(path)); len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one", backups)
	}
}

func TestKeymapMigrationOnAbsentFileIsANoOp(t *testing.T) {
	t.Parallel()

	store, path := newKeymapFixture(t, "")
	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	if result.Migrated || result.Plan.Present {
		t.Fatalf("result = %+v, want an absent-file no-op", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("migration created a keymap for a fresh install: %v", err)
	}
}

// --- Verification expectation 4: backup, failure injection, rollback ---

func TestKeymapMigrationBackupIsDigestNamedExclusiveAndReused(t *testing.T) {
	t.Parallel()

	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	store, path := newKeymapFixture(t, original)
	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	wantPath := keymapMigrationBackupPath(path, []byte(original))
	if result.BackupPath != wantPath {
		t.Fatalf("backup path = %q, want %q", result.BackupPath, wantPath)
	}
	if got := readFile(t, wantPath); got != original {
		t.Fatalf("backup = %q, want the original v0 bytes", got)
	}

	// A retry against the same original reuses the same backup rather than
	// accumulating copies.
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateKeymapForWrite(store); err != nil {
		t.Fatalf("retry migration error = %v", err)
	}
	if backups := keymapBackupFiles(t, filepath.Dir(path)); len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one reused backup", backups)
	}
}

func TestKeymapMigrationRefusesToClobberAForeignBackup(t *testing.T) {
	t.Parallel()

	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	store, path := newKeymapFixture(t, original)
	backupPath := keymapMigrationBackupPath(path, []byte(original))
	if err := os.WriteFile(backupPath, []byte("# someone else's file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := migrateKeymapForWrite(store); err == nil ||
		!strings.Contains(err.Error(), "exists with different content") {
		t.Fatalf("error = %v, want a refusal to overwrite a foreign backup", err)
	}
	if got := readFile(t, path); got != original {
		t.Fatal("keymap was migrated even though the backup could not be established")
	}
}

func TestKeymapMigrationFailsClosedWhenTheDirectoryIsReadOnly(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	store, path := newKeymapFixture(t, original)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if _, err := migrateKeymapForWrite(store); err == nil {
		t.Fatal("migrateKeymapForWrite() = nil, want a backup-creation failure")
	}
	if got := readFile(t, path); got != original {
		t.Fatalf("keymap changed after a failed migration:\n%s", got)
	}
}

func TestKeymapMigrationPreservesFileModeAndSymlinkTarget(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "projmux")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(home, "dotfiles-keymap.toml")
	if err := os.WriteFile(real, []byte("[bindings.new-window]\nkeys = [\"C-t\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(configDir, "keymap.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := keymapStore{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}

	if _, err := migrateKeymapForWrite(store); err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("migration replaced the symlink with a regular file")
	}
	target, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if got := target.Mode().Perm(); got != 0o600 {
		t.Fatalf("target mode = %o, want 0600 preserved", got)
	}
	if !strings.Contains(readFile(t, real), `[bindings."window.create"]`) {
		t.Fatal("migration did not write through the symlink to the real file")
	}
}

func TestKeymapRollbackRestoresV0(t *testing.T) {
	t.Parallel()

	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	store, path := newKeymapFixture(t, original)
	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	if !strings.Contains(readFile(t, path), "schema_version = 1") {
		t.Fatal("expected a migrated file before rollback")
	}

	if err := rollbackKeymapMigration(store, result.BackupPath); err != nil {
		t.Fatalf("rollbackKeymapMigration() error = %v", err)
	}
	if got := readFile(t, path); got != original {
		t.Fatalf("rollback produced %q, want the original v0 bytes %q", got, original)
	}
	// The restored file must be readable by a binary that predates the schema,
	// which means no marker at all.
	if strings.Contains(readFile(t, path), keymapSchemaVersionKey) {
		t.Fatal("rolled back file still carries a schema marker")
	}
}

func TestKeymapRollbackRefusesACorruptBackup(t *testing.T) {
	t.Parallel()

	store, path := newKeymapFixture(t, "[bindings.new-window]\nkeys = [\"C-t\"]\n")
	if _, err := migrateKeymapForWrite(store); err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	migrated := readFile(t, path)

	corrupt := filepath.Join(filepath.Dir(path), "keymap.toml.pre-v1-deadbeef.bak")
	if err := os.WriteFile(corrupt, []byte("[bindings.new-window]\nkeys = [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rollbackKeymapMigration(store, corrupt); err == nil {
		t.Fatal("rollbackKeymapMigration() = nil, want a refusal to restore an unreadable backup")
	}
	if got := readFile(t, path); got != migrated {
		t.Fatal("a refused rollback changed the live keymap")
	}
}

// --- Verification expectation 6: preflight is read-only, apply converges ---

func TestKeymapPreflightWritesNothing(t *testing.T) {
	t.Parallel()

	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	store, path := newKeymapFixture(t, original)

	plan, err := planKeymapMigration(store)
	if err != nil {
		t.Fatalf("planKeymapMigration() error = %v", err)
	}
	if !plan.Required || plan.FromVersion != keymapSchemaVersionV0 {
		t.Fatalf("plan = %+v, want a required v0 migration", plan)
	}
	if got := readFile(t, path); got != original {
		t.Fatal("the preflight rewrote the keymap")
	}
	if backups := keymapBackupFiles(t, filepath.Dir(path)); len(backups) != 0 {
		t.Fatalf("backups = %v, want none from a preflight", backups)
	}

	var report bytes.Buffer
	writeKeymapMigrationPreflight(&report, plan)
	for _, want := range []string{
		"keymap migration pending",
		"schema_version 0 -> 1",
		"rename: new-window -> window.create",
		"projmux config apply",
	} {
		if !strings.Contains(report.String(), want) {
			t.Fatalf("preflight report = %q, want %q", report.String(), want)
		}
	}
}

func TestKeymapPreflightReportsConflictsWithoutWriting(t *testing.T) {
	t.Parallel()

	store, path := newKeymapFixture(t, `[bindings.new-window]
keys = ["C-t"]

[bindings."window.create"]
keys = ["C-n"]
`)
	plan, err := planKeymapMigration(store)
	if err != nil {
		t.Fatalf("planKeymapMigration() error = %v", err)
	}
	if !plan.Blocked() {
		t.Fatal("plan is not blocked")
	}
	var report bytes.Buffer
	writeKeymapMigrationPreflight(&report, plan)
	if !strings.Contains(report.String(), "keymap migration blocked") {
		t.Fatalf("report = %q, want a blocked report", report.String())
	}
	if backups := keymapBackupFiles(t, filepath.Dir(path)); len(backups) != 0 {
		t.Fatalf("backups = %v, want none", backups)
	}
}

func TestConfigRenderReportsMigrationOnStderrAndWritesNoKeymap(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	writeFile(t, keymap, original)

	for _, artifact := range []string{"print-config", "print-app-config"} {
		cmd := &tmuxCommand{
			executable: func() (string, error) { return "/tmp/projmux", nil },
			homeDir:    func() (string, error) { return home, nil },
			lookupEnv:  func(string) string { return "" },
			readFile:   os.ReadFile,
			writeFile:  os.WriteFile,
		}
		var stdout, stderr bytes.Buffer
		if err := cmd.Run([]string{artifact}, &stdout, &stderr); err != nil {
			t.Fatalf("%s error = %v", artifact, err)
		}
		if !strings.Contains(stderr.String(), "keymap migration pending") {
			t.Fatalf("%s stderr = %q, want the preflight report", artifact, stderr.String())
		}
		if strings.Contains(stdout.String(), "keymap migration") {
			t.Fatalf("%s leaked the preflight onto the generated artifact:\n%s", artifact, stdout.String())
		}
		if got := readFile(t, keymap); got != original {
			t.Fatalf("%s rewrote the keymap", artifact)
		}
		if backups := keymapBackupFiles(t, filepath.Dir(keymap)); len(backups) != 0 {
			t.Fatalf("%s created backups %v", artifact, backups)
		}
	}
}

func TestSettingsKeySaveConvergesAV0KeymapToV1(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	writeFile(t, keymap, "[bindings.new-window]\nkeys = [\"C-t\"]\n")
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}

	var stdout bytes.Buffer
	if err := cmd.saveKeymapKeysAndApply("ProjectSidebarToggle", []string{"M-a"}, &stdout); err != nil {
		t.Fatalf("saveKeymapKeysAndApply() error = %v", err)
	}

	got := readFile(t, keymap)
	for _, want := range []string{
		"schema_version = 1\n",
		`[bindings."window.create"]`,
		`[bindings."project-sidebar.toggle"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("keymap = %q, want %q", got, want)
		}
	}
	// The save must not have grown a second table for the action it wrote.
	if strings.Contains(got, "[bindings.ProjectSidebarToggle]") {
		t.Fatalf("save wrote a v0 table into a migrated file:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "Schema: ok") {
		t.Fatalf("stdout = %q, want a Schema stage", stdout.String())
	}
	if backups := keymapBackupFiles(t, filepath.Dir(keymap)); len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one", backups)
	}
}

func TestSettingsKeySaveAbortsWhenMigrationConflicts(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	original := `[bindings.new-window]
keys = ["C-t"]

[bindings."window.create"]
keys = ["C-n"]
`
	writeFile(t, keymap, original)
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}

	var stdout bytes.Buffer
	err := cmd.saveKeymapKeysAndApply("ProjectSidebarToggle", []string{"M-a"}, &stdout)
	if err == nil || !strings.Contains(err.Error(), "migrate keymap schema") {
		t.Fatalf("error = %v, want a schema-stage abort", err)
	}
	if got := readFile(t, keymap); got != original {
		t.Fatalf("a blocked save changed the keymap:\n%s", got)
	}
	for _, want := range []string{
		"  Schema: failed (keymap schema:",
		"  Saved: skipped (keymap schema was not migrated)",
		"  Prepared: skipped (keymap schema was not migrated)",
		"  Running session: skipped (keymap schema was not migrated)",
		"Recovery: resolve the keymap schema problem",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestTmuxApplyRefusesToApplyWhenTheKeymapMigrationFails(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	original := `[bindings.new-window]
keys = ["C-t"]

[bindings."window.create"]
keys = ["C-n"]
`
	writeFile(t, keymap, original)
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	runner := &recordingTmuxRunner{outputs: map[string]string{}}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"apply", "--config", configPath, "--socket", "projmux-test"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "migrate keymap before apply") {
		t.Fatalf("error = %v, want an apply refusal", err)
	}
	if got := readFile(t, keymap); got != original {
		t.Fatal("a refused apply changed the keymap")
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Fatal("a refused apply wrote the generated tmux config")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("a refused apply touched tmux: %v", runner.calls)
	}
	for _, want := range []string{"keymap unchanged", "generated tmux config unchanged", "skipped reload"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestTmuxApplyNoReloadMigratesWithoutTouchingTheServer(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	writeFile(t, keymap, "[bindings.new-window]\nkeys = [\"C-t\"]\n")
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	runner := &recordingTmuxRunner{outputs: map[string]string{}}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--config", configPath, "--no-reload"}, &stdout, &stderr); err != nil {
		t.Fatalf("apply --no-reload error = %v; stderr = %q", err, stderr.String())
	}
	if !strings.Contains(readFile(t, keymap), "schema_version = 1") {
		t.Fatal("--no-reload skipped the keymap migration")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("--no-reload contacted the tmux server: %v", runner.calls)
	}
	if !strings.Contains(stdout.String(), "skipped reload: --no-reload") {
		t.Fatalf("stdout = %q, want the suppressed-reload line", stdout.String())
	}
}

// TestReadOnlyRoutesNeverRewriteTheKeymap is the negative half of the write
// boundary.
//
// Migration is supposed to happen at exactly two kinds of moment: an explicit
// apply, and a Settings save. Everything else — printing help, reporting a
// version, reading a resource, rendering a config, previewing an update — is a
// read, and a read that silently rewrote the user's keymap would make the
// schema's careful backup-then-replace ordering pointless, because it would run
// at times the user never asked for it.
//
// It drives the real top-level dispatcher against an isolated HOME/XDG rather
// than a hand-built command struct, because the property under test is about
// routes rather than about any one handler.
func TestReadOnlyRoutesNeverRewriteTheKeymap(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	keymap := filepath.Join(configHome, "projmux", "keymap.toml")
	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	writeFile(t, keymap, original)

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// Pin the installer so the update preview is deterministic and never probes
	// the real install layout.
	t.Setenv("PROJMUX_INSTALLER", "npm")

	for _, args := range [][]string{
		{"help"},
		{"version"},
		{"get"},
		{"describe"},
		{"agent", "usage"},
		{"doctor", "--help"},
		{"config", "render", "standalone"},
		{"config", "render", "app"},
		{"update", "apply", "--dry-run"},
	} {
		var stdout, stderr bytes.Buffer
		// The exit status is not the assertion. A usage error is a perfectly
		// good read; what matters is that nothing on disk moved.
		_ = Run(args, &stdout, &stderr)

		if got := readFile(t, keymap); got != original {
			t.Fatalf("%v rewrote the keymap:\ngot:  %q\nwant: %q", args, got, original)
		}
		if backups := keymapBackupFiles(t, filepath.Dir(keymap)); len(backups) != 0 {
			t.Fatalf("%v created keymap backups %v", args, backups)
		}
	}
}

// TestUpdateDryRunPreviewsTheMigrationStageWithoutPromisingADiff pins the
// updater preview contract.
func TestUpdateDryRunPreviewsTheMigrationStageWithoutPromisingADiff(t *testing.T) {
	t.Parallel()

	line := keymapMigrationStagePreviewLine("/usr/local/bin/projmux")
	if !strings.Contains(line, "would migrate: keymap schema via /usr/local/bin/projmux") {
		t.Fatalf("preview = %q, want the migration stage named", line)
	}
	if !strings.Contains(line, "the installed binary computes the exact action-id table") {
		t.Fatalf("preview = %q, want an explicit disclaimer about the rename table", line)
	}
	for _, canonical := range testKeymapCanonicalIDs() {
		if strings.Contains(line, canonical) {
			t.Fatalf("preview leaks a canonical id %q; a dry run cannot know the candidate binary's table", canonical)
		}
	}
}

// TestPostUpdateApplyArgsAlwaysReachTheNewBinary pins the install-path ordering:
// replace, then migrate through the new binary, then apply.
func TestPostUpdateApplyArgsAlwaysReachTheNewBinary(t *testing.T) {
	t.Parallel()

	if got := postUpdateApplyArgs(false); !slices.Equal(got, []string{"tmux", "apply"}) {
		t.Fatalf("apply args = %v, want [tmux apply]", got)
	}
	// --no-apply must still reach the binary; it only suppresses the reload.
	if got := postUpdateApplyArgs(true); !slices.Equal(got, []string{"tmux", "apply", "--no-reload"}) {
		t.Fatalf("no-apply args = %v, want [tmux apply --no-reload]", got)
	}
}

// --- Verification expectation 7: independence from the resource registry ---

func TestKeymapAndRegistrySchemaMarkersAreIndependent(t *testing.T) {
	t.Parallel()

	// Different marker spelling, different version domain. A shared constant
	// here would let a registry bump silently demand a keymap rewrite.
	if keymapSchemaVersionKey != "schema_version" {
		t.Fatalf("keymap marker = %q, want snake_case schema_version", keymapSchemaVersionKey)
	}

	store, path := newKeymapFixture(t, "[bindings.new-window]\nkeys = [\"C-t\"]\n")
	result, err := migrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}

	// The keymap backup lives beside the keymap and names the keymap schema.
	// Nothing about it references the registry envelope, and nothing in the
	// migrated file carries the registry's camelCase marker or apiVersion.
	if !strings.Contains(result.BackupPath, ".pre-v1-") || !strings.HasSuffix(result.BackupPath, ".bak") {
		t.Fatalf("backup path = %q, want a keymap-owned pre-v1 backup", result.BackupPath)
	}
	if filepath.Dir(result.BackupPath) != filepath.Dir(path) {
		t.Fatal("keymap backup does not live beside the keymap")
	}
	migrated := readFile(t, path)
	for _, foreign := range []string{"schemaVersion", "apiVersion", "projmux.io/v1alpha1"} {
		if strings.Contains(migrated, foreign) {
			t.Fatalf("migrated keymap carries the registry marker %q:\n%s", foreign, migrated)
		}
	}
}

func TestKeymapMigrationFailureLeavesTheRegistryUntouched(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	writeFile(t, keymap, `[bindings.new-window]
keys = ["C-t"]

[bindings."window.create"]
keys = ["C-n"]
`)
	// A registry document sitting in the same isolated state tree must survive
	// a keymap migration failure byte-for-byte: the two migrations do not share
	// a transaction, so one failing cannot roll the other back.
	registry := filepath.Join(home, ".local", "state", "projmux", "resources.json")
	registryBody := `{"apiVersion":"projmux.io/v1alpha1","schemaVersion":1,"projects":[]}`
	writeFile(t, registry, registryBody)

	store := keymapStore{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	if _, err := migrateKeymapForWrite(store); err == nil {
		t.Fatal("expected the keymap migration to fail")
	}
	if got := readFile(t, registry); got != registryBody {
		t.Fatalf("registry = %q, want it untouched by a keymap failure", got)
	}
	if backups := keymapBackupFiles(t, filepath.Dir(registry)); len(backups) != 0 {
		t.Fatalf("keymap migration created backups in the registry directory: %v", backups)
	}
}
