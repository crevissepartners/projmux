package keybinding

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
	for _, action := range DefaultKeyBindingCatalog() {
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
func newKeymapFixture(t *testing.T, body string) (KeymapStore, string) {
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
	return KeymapStore{
		HomeDir:   func() (string, error) { return home, nil },
		LookupEnv: func(string) string { return "" },
	}, path
}

// --- Verification expectation 1: exhaustive manifest, orphan/duplicate guards ---

func TestKeymapManifestCoversEveryCatalogActionExactlyOnce(t *testing.T) {
	t.Parallel()

	catalog := DefaultKeyBindingCatalog()
	manifest := keymapActionManifest()

	for _, action := range catalog {
		if strings.TrimSpace(action.CanonicalID) == "" {
			t.Fatalf("action %s has no canonical id; every catalogued action must have a v1 spelling", action.ID)
		}
		for _, id := range KeyBindingActionAliases(action) {
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
		if _, ok := KeyBindingActionByID(catalog, id); !ok {
			t.Fatalf("manifest row %q resolves to no action", id)
		}
		if _, ok := KeyBindingActionByID(catalog, entry.CanonicalID); !ok {
			t.Fatalf("manifest canonical id %q resolves to no action", entry.CanonicalID)
		}
	}
}

func TestKeymapCanonicalIDsAreUniqueAndDistinctFromLegacyIDs(t *testing.T) {
	t.Parallel()

	catalog := DefaultKeyBindingCatalog()
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

	parsed, err := ParseKeymapFile("keymap.toml", `[bindings.new-window]
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

	before, _, err := LoadMergedKeyBindingCatalog(KeymapLoader{HomeDir: store.HomeDir, LookupEnv: store.LookupEnv})
	if err != nil {
		t.Fatalf("load before migration: %v", err)
	}

	result, err := MigrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	if !result.Migrated {
		t.Fatal("migrateKeymapForWrite() did not migrate a v0 file")
	}

	after, _, err := LoadMergedKeyBindingCatalog(KeymapLoader{HomeDir: store.HomeDir, LookupEnv: store.LookupEnv})
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
		gotKeys := KeyBindingEffectivePlainChords(after[i])
		wantKeys := KeyBindingEffectivePlainChords(before[i])
		if !slices.Equal(gotKeys, wantKeys) {
			t.Fatalf("%s keys = %v, want %v", before[i].ID, gotKeys, wantKeys)
		}
		if before[i].PrefixChord != after[i].PrefixChord {
			t.Fatalf("%s prefix = %q, want %q", before[i].ID, after[i].PrefixChord, before[i].PrefixChord)
		}
	}

	migrated := readFile(t, path)
	for _, want := range []string{
		"schema_version = 2\n",
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
	before, _, err := LoadMergedKeyBindingCatalog(KeymapLoader{HomeDir: store.HomeDir, LookupEnv: store.LookupEnv})
	if err != nil {
		t.Fatalf("load before migration: %v", err)
	}

	if _, err := MigrateKeymapForWrite(store); err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}

	after, _, err := LoadMergedKeyBindingCatalog(KeymapLoader{HomeDir: store.HomeDir, LookupEnv: store.LookupEnv})
	if err != nil {
		t.Fatalf("load after migration: %v", err)
	}
	for _, id := range []string{"AIResumePickerToggle", "AISplitPickerToggle"} {
		wantAction, ok := KeyBindingActionByID(before, id)
		if !ok {
			t.Fatalf("missing %s before migration", id)
		}
		gotAction, ok := KeyBindingActionByID(after, id)
		if !ok {
			t.Fatalf("missing %s after migration", id)
		}
		want := KeyBindingEffectivePlainChords(wantAction)
		got := KeyBindingEffectivePlainChords(gotAction)
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
	parsed, err := ParseKeymapFile("keymap.toml", body)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	if parsed.SchemaVersion != keymapSchemaVersionV1 {
		t.Fatalf("schema version = %d, want %d", parsed.SchemaVersion, keymapSchemaVersionV1)
	}
	merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	newWindow, ok := KeyBindingActionByID(merged, "new-window")
	if !ok {
		t.Fatal("missing new-window action")
	}
	if got := KeyBindingEffectivePlainChords(newWindow); !slices.Equal(got, []string{"C-t"}) {
		t.Fatalf("window.create keys = %v, want [C-t]", got)
	}

	rendered := RenderKeymapFile(parsed)
	reparsed, err := ParseKeymapFile("keymap.toml", rendered)
	if err != nil {
		t.Fatalf("reparse rendered file: %v", err)
	}
	if RenderKeymapFile(reparsed) != rendered {
		t.Fatalf("render is not stable:\nfirst:  %q\nsecond: %q", rendered, RenderKeymapFile(reparsed))
	}
}

func TestKeymapRejectsUnquotedDottedTableAndFutureSchema(t *testing.T) {
	t.Parallel()

	if _, err := ParseKeymapFile("keymap.toml", "[bindings.window.create]\nkeys = [\"C-t\"]\n"); err == nil {
		t.Fatal("parseKeymapFile() = nil, want rejection of an unquoted dotted table")
	}
	_, err := ParseKeymapFile("keymap.toml", "schema_version = 99\n")
	if err == nil || !strings.Contains(err.Error(), "newer than the supported version") {
		t.Fatalf("parseKeymapFile() error = %v, want a forward-version refusal", err)
	}
	if _, err := ParseKeymapFile("keymap.toml", "schema_version = 1\nschema_version = 1\n"); err == nil {
		t.Fatal("parseKeymapFile() = nil, want rejection of a duplicate schema_version")
	}
	if _, err := ParseKeymapFile("keymap.toml", "unexpected = 1\n"); err == nil {
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
	result, err := MigrateKeymapForWrite(store)
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
	result, err := MigrateKeymapForWrite(store)
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
	result, err := MigrateKeymapForWrite(store)
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
	if _, err := MigrateKeymapForWrite(store); err != nil {
		t.Fatalf("first migration error = %v", err)
	}
	first := readFile(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstModTime := info.ModTime()

	result, err := MigrateKeymapForWrite(store)
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
	result, err := MigrateKeymapForWrite(store)
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
	result, err := MigrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	wantPath := keymapMigrationBackupPathForVersion(path, []byte(original), keymapSchemaVersionV1)
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
	if _, err := MigrateKeymapForWrite(store); err != nil {
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
	backupPath := keymapMigrationBackupPathForVersion(path, []byte(original), keymapSchemaVersionV1)
	if err := os.WriteFile(backupPath, []byte("# someone else's file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateKeymapForWrite(store); err == nil ||
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

	if _, err := MigrateKeymapForWrite(store); err == nil {
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
	store := KeymapStore{
		HomeDir:   func() (string, error) { return home, nil },
		LookupEnv: func(string) string { return "" },
	}

	if _, err := MigrateKeymapForWrite(store); err != nil {
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
	result, err := MigrateKeymapForWrite(store)
	if err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	if !strings.Contains(readFile(t, path), "schema_version = 2") {
		t.Fatal("expected a migrated file before rollback")
	}

	if err := RollbackKeymapMigration(store, result.BackupPath); err != nil {
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
	if _, err := MigrateKeymapForWrite(store); err != nil {
		t.Fatalf("migrateKeymapForWrite() error = %v", err)
	}
	migrated := readFile(t, path)

	corrupt := filepath.Join(filepath.Dir(path), "keymap.toml.pre-v1-deadbeef.bak")
	if err := os.WriteFile(corrupt, []byte("[bindings.new-window]\nkeys = [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RollbackKeymapMigration(store, corrupt); err == nil {
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

	plan, err := PlanKeymapMigration(store)
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
	WriteKeymapMigrationPreflight(&report, plan)
	for _, want := range []string{
		"keymap migration pending",
		"schema_version 0 -> 2",
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
	plan, err := PlanKeymapMigration(store)
	if err != nil {
		t.Fatalf("planKeymapMigration() error = %v", err)
	}
	if !plan.Blocked() {
		t.Fatal("plan is not blocked")
	}
	var report bytes.Buffer
	WriteKeymapMigrationPreflight(&report, plan)
	if !strings.Contains(report.String(), "keymap migration blocked") {
		t.Fatalf("report = %q, want a blocked report", report.String())
	}
	if backups := keymapBackupFiles(t, filepath.Dir(path)); len(backups) != 0 {
		t.Fatalf("backups = %v, want none", backups)
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
	result, err := MigrateKeymapForWrite(store)
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

	store := KeymapStore{
		HomeDir:   func() (string, error) { return home, nil },
		LookupEnv: func(string) string { return "" },
	}
	if _, err := MigrateKeymapForWrite(store); err == nil {
		t.Fatal("expected the keymap migration to fail")
	}
	if got := readFile(t, registry); got != registryBody {
		t.Fatalf("registry = %q, want it untouched by a keymap failure", got)
	}
	if backups := keymapBackupFiles(t, filepath.Dir(registry)); len(backups) != 0 {
		t.Fatalf("keymap migration created backups in the registry directory: %v", backups)
	}
}
