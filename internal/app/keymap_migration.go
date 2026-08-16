package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// keymapMaxFileBytes bounds how much of a keymap file the migrator will read.
//
// The largest keymap projmux can legitimately produce is one table per
// catalogued action with a handful of chords each — low single-digit kilobytes.
// A megabyte is far past any real file and still small enough that a symlink
// pointed at something enormous fails fast instead of being slurped into memory.
const keymapMaxFileBytes = 1 << 20

// keymapMigrationChange is one table's disposition in a concrete plan.
type keymapMigrationChange struct {
	SourceID    string
	TargetID    string
	Disposition keymapDisposition
	// Detail carries the remediation for a retired id and stays empty
	// otherwise.
	Detail string
}

// keymapMigrationConflict is a table pair the migrator refuses to merge.
//
// Both spellings of one action are present and they disagree. There is no
// defensible winner — picking either silently discards a key the user set — so
// the whole migration stops and neither the keymap nor the live config is
// touched.
type keymapMigrationConflict struct {
	ActionID    string
	LegacyID    string
	CanonicalID string
	LegacyKeys  string
	Canonical   string
}

func (c keymapMigrationConflict) String() string {
	return fmt.Sprintf("%s: [bindings.%s] %s conflicts with [bindings.%s] %s",
		c.ActionID, c.LegacyID, c.LegacyKeys, c.CanonicalID, c.Canonical)
}

// keymapMigrationPlan is the read-only result of a preflight.
//
// Producing one writes nothing, by construction: nothing in this file's plan
// path opens a file for writing. `config render`, `update --dry-run` and the
// diagnostics routes all stop here.
type keymapMigrationPlan struct {
	// Path is the keymap file the plan was computed from.
	Path string
	// Present is false when there is no keymap file at all. A fresh install has
	// nothing to migrate and nothing to report.
	Present bool
	// FromVersion is the schema marker found on disk; 0 for an unversioned v0.
	FromVersion int
	// ToVersion is the schema this binary writes.
	ToVersion int
	// Changes lists every table's disposition, sorted by source id.
	Changes []keymapMigrationChange
	// Conflicts is non-empty when the migration must not proceed.
	Conflicts []keymapMigrationConflict
	// Required is true when applying the plan would change the file's bytes.
	// A repeat run of an already-migrated file leaves this false, which is what
	// makes the migration a no-op rather than a rewrite that happens to produce
	// the same content.
	Required bool

	// original and migrated are the parsed before/after files. They are
	// unexported because they are apply-stage inputs, not report data.
	original     keymapFile
	migrated     keymapFile
	originalRaw  []byte
	migratedBody []byte
}

// Blocked reports whether the plan must not be applied.
func (p keymapMigrationPlan) Blocked() bool { return len(p.Conflicts) > 0 }

// keymapMigrationResult is what an apply actually did.
type keymapMigrationResult struct {
	Plan keymapMigrationPlan
	// Migrated is false when the plan was already satisfied and no bytes were
	// written.
	Migrated bool
	// BackupPath is the validated v0 backup a rollback would restore from. It
	// is empty when nothing was written.
	BackupPath string
}

// planKeymapMigration computes the v0 → v1 plan for the store's keymap.
//
// This is the single preflight every entry point shares. It reads, parses,
// resolves aliases and validates the *whole* merged chord table before deciding
// anything, so a file that would not merge cleanly is rejected here rather than
// halfway through a rewrite.
func planKeymapMigration(store keymapStore) (keymapMigrationPlan, error) {
	plan := keymapMigrationPlan{ToVersion: keymapSchemaVersion}
	if store.homeDir == nil {
		return plan, nil
	}
	path, err := keymapPath(store.homeDir, store.lookupEnv)
	if err != nil {
		return plan, err
	}
	plan.Path = path

	raw, err := readKeymapForMigration(store, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return plan, nil
		}
		return plan, err
	}
	plan.Present = true
	plan.originalRaw = raw

	parsed, err := parseKeymapFile(path, string(raw))
	if err != nil {
		return plan, err
	}
	plan.original = parsed
	plan.FromVersion = parsed.SchemaVersion

	// Validate the file as it stands before planning a rewrite. A file that
	// already fails to merge is a user problem to fix, not something to
	// silently re-shape into a v1 that fails the same way.
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed); err != nil {
		return plan, fmt.Errorf("keymap %s: %w", path, err)
	}

	migrated, changes, conflicts := buildKeymapMigration(parsed)
	plan.migrated = migrated
	plan.Changes = changes
	plan.Conflicts = conflicts
	if len(conflicts) > 0 {
		return plan, nil
	}

	body := []byte(renderKeymapFile(migrated))
	plan.migratedBody = body
	plan.Required = string(body) != string(raw)
	return plan, nil
}

// readKeymapForMigration reads the keymap under the bounded regular-file /
// supported-symlink policy.
//
// The injected readFile hook (tests and the in-process Settings store) bypasses
// the filesystem entirely, so the policy only applies to the real path.
func readKeymapForMigration(store keymapStore, path string) ([]byte, error) {
	if store.readFile != nil {
		return store.readFile(path)
	}
	if _, _, err := resolveKeymapWriteTarget(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, keymapMaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read keymap %s: %w", path, err)
	}
	if len(raw) > keymapMaxFileBytes {
		return nil, fmt.Errorf("read keymap %s: file is larger than %d bytes", path, keymapMaxFileBytes)
	}
	return raw, nil
}

// buildKeymapMigration turns a parsed file into its v1 form plus the disposition
// of every table it contained.
func buildKeymapMigration(parsed keymapFile) (keymapFile, []keymapMigrationChange, []keymapMigrationConflict) {
	manifest := keymapActionManifest()
	retired := keymapRetiredIDIndex()

	migrated := keymapFile{
		SchemaVersion: keymapSchemaVersion,
		Bindings:      map[string]keymapOverride{},
	}
	var changes []keymapMigrationChange
	var conflicts []keymapMigrationConflict

	// Sources per canonical target, so a legacy/canonical pair is decided once
	// with both halves in hand rather than by whichever id the map yielded
	// first.
	sourcesByTarget := map[string][]string{}
	for id := range parsed.Bindings {
		entry, ok := manifest[id]
		if !ok {
			continue
		}
		sourcesByTarget[entry.CanonicalID] = append(sourcesByTarget[entry.CanonicalID], id)
	}
	for target := range sourcesByTarget {
		sort.Strings(sourcesByTarget[target])
	}

	for _, id := range sortedKeymapBindingIDs(parsed) {
		override := parsed.Bindings[id]
		if info, ok := retired[id]; ok {
			// A fatal retired id never reaches here: parseKeymapFile rejects
			// the file outright. The non-fatal ones are copied through so a
			// migration never destroys a line the user still has to read.
			migrated.Bindings[id] = override
			changes = append(changes, keymapMigrationChange{
				SourceID:    id,
				TargetID:    id,
				Disposition: keymapDispositionRetired,
				Detail:      info.Remediation,
			})
			continue
		}
		entry, ok := manifest[id]
		if !ok {
			migrated.Bindings[id] = override
			changes = append(changes, keymapMigrationChange{
				SourceID:    id,
				TargetID:    id,
				Disposition: keymapDispositionPreserveUnknown,
			})
			continue
		}

		sources := sourcesByTarget[entry.CanonicalID]
		if len(sources) > 1 {
			// Every source for this target must agree. Compare against the
			// first source only, and emit the conflict once, from that first
			// source's turn.
			primary := sources[0]
			if id != primary {
				continue
			}
			if conflict, ok := keymapCoalesceConflict(parsed, entry, sources); ok {
				conflicts = append(conflicts, conflict)
				continue
			}
			migrated.Bindings[entry.CanonicalID] = override
			for _, source := range sources {
				changes = append(changes, keymapMigrationChange{
					SourceID:    source,
					TargetID:    entry.CanonicalID,
					Disposition: keymapDispositionCoalesce,
				})
			}
			continue
		}

		migrated.Bindings[entry.CanonicalID] = override
		changes = append(changes, keymapMigrationChange{
			SourceID:    id,
			TargetID:    entry.CanonicalID,
			Disposition: keymapDispositionCanonical,
		})
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].SourceID != changes[j].SourceID {
			return changes[i].SourceID < changes[j].SourceID
		}
		return changes[i].TargetID < changes[j].TargetID
	})
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].ActionID < conflicts[j].ActionID })
	return migrated, changes, conflicts
}

// keymapCoalesceConflict reports whether the sources for one canonical target
// disagree, and describes the disagreement when they do.
func keymapCoalesceConflict(
	parsed keymapFile,
	entry keymapManifestEntry,
	sources []string,
) (keymapMigrationConflict, bool) {
	primary := parsed.Bindings[sources[0]]
	for _, source := range sources[1:] {
		other := parsed.Bindings[source]
		if keymapOverridesEquivalent(primary, other) {
			continue
		}
		return keymapMigrationConflict{
			ActionID:    entry.ActionID,
			LegacyID:    sources[0],
			CanonicalID: source,
			LegacyKeys:  describeKeymapOverride(primary),
			Canonical:   describeKeymapOverride(other),
		}, true
	}
	return keymapMigrationConflict{}, false
}

// keymapOverridesEquivalent reports whether two overrides mean the same thing.
//
// Only the stored fields count. lineByKey is provenance for error messages, not
// content, so two tables that set identical keys from different lines coalesce.
func keymapOverridesEquivalent(a, b keymapOverride) bool {
	if a.KeysSet != b.KeysSet {
		return false
	}
	if a.KeysSet && !slices.Equal(a.Keys, b.Keys) {
		return false
	}
	if !equalStringPointers(a.Plain, b.Plain) {
		return false
	}
	return equalStringPointers(a.Prefix, b.Prefix)
}

func equalStringPointers(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func describeKeymapOverride(override keymapOverride) string {
	var parts []string
	if override.KeysSet {
		parts = append(parts, "keys = "+formatKeymapStringArray(override.Keys))
	} else if override.Plain != nil {
		parts = append(parts, "plain = "+formatKeymapString(*override.Plain))
	}
	if override.Prefix != nil {
		parts = append(parts, "prefix = "+formatKeymapString(*override.Prefix))
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, ", ")
}

func sortedKeymapBindingIDs(keymap keymapFile) []string {
	ids := make([]string, 0, len(keymap.Bindings))
	for id := range keymap.Bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// applyKeymapMigration performs the write half of the migration.
//
// The ordering is the safety contract, not an implementation detail:
//
//  1. refuse outright if the preflight found a conflict — write 0, apply 0;
//  2. create (or re-validate) the digest-named backup of the untouched v0;
//  3. render v1 to a temp file and re-parse, re-merge and re-render *that* file,
//     proving the round trip and the effective chord table both survive;
//  4. only then rename the verified temp over the original.
//
// A caller must not apply generated or live tmux config until this returns
// without error. Everything downstream of a keymap rewrite assumes the file on
// disk is the one that was verified.
func applyKeymapMigration(store keymapStore, plan keymapMigrationPlan) (keymapMigrationResult, error) {
	result := keymapMigrationResult{Plan: plan}
	if plan.Blocked() {
		return result, keymapMigrationConflictError(plan)
	}
	if !plan.Present || !plan.Required {
		return result, nil
	}

	// Prove the migrated file is behaviour-identical before anything durable
	// happens. This runs against in-memory values, so a failure here has cost
	// the user nothing.
	if err := verifyKeymapMigrationParity(plan); err != nil {
		return result, err
	}

	backupPath, err := writeKeymapMigrationBackup(store, plan)
	if err != nil {
		return result, err
	}
	result.BackupPath = backupPath

	if err := verifyKeymapMigrationRoundTrip(plan); err != nil {
		return result, err
	}

	if err := writeKeymapBytes(plan.Path, plan.migratedBody, store.writeFile); err != nil {
		return result, err
	}

	// Read the file back through the normal load path. Everything up to here
	// verified bytes projmux was about to write; this verifies the bytes that
	// actually landed, which is the only check that can catch a truncated
	// write, a full filesystem or a rename that resolved somewhere unexpected.
	//
	// A failure here is the one situation where a rollback is the correct
	// response: the original is gone from its path but is known-good in the
	// backup, so restoring it puts the user back where they started rather than
	// leaving them with a keymap nothing can read.
	if err := verifyKeymapMigrationOnDisk(store, plan); err != nil {
		if rollbackErr := rollbackKeymapMigration(store, backupPath); rollbackErr != nil {
			return result, fmt.Errorf(
				"%w; restoring the backup also failed: %v; recover manually with: cp %s %s",
				err, rollbackErr, backupPath, plan.Path)
		}
		return result, fmt.Errorf("%w; the previous keymap was restored from %s", err, backupPath)
	}

	result.Migrated = true
	return result, nil
}

// verifyKeymapMigrationOnDisk re-reads the replaced keymap and proves it still
// merges to the bindings the plan promised.
func verifyKeymapMigrationOnDisk(store keymapStore, plan keymapMigrationPlan) error {
	raw, err := readKeymapForMigration(store, plan.Path)
	if err != nil {
		return fmt.Errorf("verify migrated keymap: %w", err)
	}
	if string(raw) != string(plan.migratedBody) {
		return errors.New("verify migrated keymap: on-disk content does not match what was written")
	}
	reparsed, err := parseKeymapFile(plan.Path, string(raw))
	if err != nil {
		return fmt.Errorf("verify migrated keymap: %w", err)
	}
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), reparsed); err != nil {
		return fmt.Errorf("verify migrated keymap: %w", err)
	}
	return nil
}

// verifyKeymapMigrationParity proves the v1 file merges to exactly the effective
// key bindings the v0 file did.
//
// This is the acceptance the user actually cares about: custom keys, an empty
// `keys = []` unbind, a transport-dependent default plus its aliases and a
// legacy `plain`/`prefix` pair must all behave the same after the rewrite.
func verifyKeymapMigrationParity(plan keymapMigrationPlan) error {
	before, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), plan.original)
	if err != nil {
		return fmt.Errorf("verify keymap migration: read current bindings: %w", err)
	}
	after, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), plan.migrated)
	if err != nil {
		return fmt.Errorf("verify keymap migration: read migrated bindings: %w", err)
	}
	if len(before) != len(after) {
		return fmt.Errorf("verify keymap migration: action count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID {
			return fmt.Errorf("verify keymap migration: action %d changed from %s to %s", i, before[i].ID, after[i].ID)
		}
		if !slices.Equal(keyBindingEffectivePlainChords(before[i]), keyBindingEffectivePlainChords(after[i])) {
			return fmt.Errorf("verify keymap migration: %s keys changed from %v to %v",
				before[i].ID,
				keyBindingEffectivePlainChords(before[i]),
				keyBindingEffectivePlainChords(after[i]))
		}
		if before[i].PrefixChord != after[i].PrefixChord {
			return fmt.Errorf("verify keymap migration: %s prefix changed from %q to %q",
				before[i].ID, before[i].PrefixChord, after[i].PrefixChord)
		}
	}
	return nil
}

// verifyKeymapMigrationRoundTrip writes the v1 body to a temp file and proves it
// reads back as the same file.
//
// The temp write is what makes this more than an in-memory check: it exercises
// the real parser against real bytes on the real filesystem, so an encoding bug
// that only shows up after a write is caught while the original is still intact.
func verifyKeymapMigrationRoundTrip(plan keymapMigrationPlan) error {
	dir := filepath.Dir(plan.Path)
	tmp, err := os.CreateTemp(dir, "keymap-verify-*.toml")
	if err != nil {
		return fmt.Errorf("verify keymap migration: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(plan.migratedBody); err != nil {
		return fmt.Errorf("verify keymap migration: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("verify keymap migration: sync temp file: %w", err)
	}
	// Read back through the same handle rather than reopening by name. The
	// bytes under test are the ones that reached the filesystem, and there is
	// no window in which something else could take over the path.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("verify keymap migration: rewind temp file: %w", err)
	}
	raw, err := io.ReadAll(tmp)
	if err != nil {
		return fmt.Errorf("verify keymap migration: read temp file: %w", err)
	}
	reparsed, err := parseKeymapFile(tmpName, string(raw))
	if err != nil {
		return fmt.Errorf("verify keymap migration: reparse temp file: %w", err)
	}
	if reparsed.SchemaVersion != keymapSchemaVersion {
		return fmt.Errorf("verify keymap migration: temp file schema version is %d, want %d",
			reparsed.SchemaVersion, keymapSchemaVersion)
	}
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), reparsed); err != nil {
		return fmt.Errorf("verify keymap migration: merge temp file: %w", err)
	}
	if rendered := renderKeymapFile(reparsed); rendered != string(plan.migratedBody) {
		return errors.New("verify keymap migration: temp file does not round-trip to the same content")
	}
	return nil
}

// writeKeymapMigrationBackup creates the pre-migration backup, or re-validates
// the one an earlier attempt already made.
//
// The name carries the original's digest, so the same v0 file always maps to the
// same backup: a retried migration reuses it instead of piling up copies, and a
// *different* original can never overwrite a backup that belongs to another one.
// Creation is exclusive for the same reason.
func writeKeymapMigrationBackup(store keymapStore, plan keymapMigrationPlan) (string, error) {
	path := keymapMigrationBackupPath(plan.Path, plan.originalRaw)

	if store.writeFile != nil {
		if err := store.writeFile(path, plan.originalRaw, defaultKeymapFileMode); err != nil {
			return "", fmt.Errorf("write keymap backup %s: %w", path, err)
		}
		return path, nil
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, defaultKeymapFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", fmt.Errorf("read existing keymap backup %s: %w", path, readErr)
			}
			if string(existing) != string(plan.originalRaw) {
				return "", fmt.Errorf("keymap backup %s exists with different content; move it aside and retry", path)
			}
			return path, nil
		}
		return "", fmt.Errorf("create keymap backup %s: %w", path, err)
	}
	if _, err := file.Write(plan.originalRaw); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write keymap backup %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close keymap backup %s: %w", path, err)
	}
	return path, nil
}

// keymapMigrationBackupPath names the pre-v1 backup for one original file.
func keymapMigrationBackupPath(path string, original []byte) string {
	sum := sha256.Sum256(original)
	return path + ".pre-v1-" + hex.EncodeToString(sum[:])[:16] + ".bak"
}

// rollbackKeymapMigration restores a keymap from a migration backup.
//
// The backup is parsed before it is restored: a corrupted or truncated backup
// must fail loudly here rather than become the live keymap. Restoration goes
// through the same atomic writer as the migration, so a rollback has the same
// crash semantics as the write it undoes.
//
// Downgrading to a projmux that predates this schema requires running this
// first — an older binary reads the v1 marker as an unsupported root key and
// refuses the file.
func rollbackKeymapMigration(store keymapStore, backupPath string) error {
	path, err := keymapPath(store.homeDir, store.lookupEnv)
	if err != nil {
		return err
	}
	readFile := store.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	raw, err := readFile(backupPath)
	if err != nil {
		return fmt.Errorf("read keymap backup %s: %w", backupPath, err)
	}
	parsed, err := parseKeymapFile(backupPath, string(raw))
	if err != nil {
		return fmt.Errorf("keymap backup %s is not a readable keymap: %w", backupPath, err)
	}
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed); err != nil {
		return fmt.Errorf("keymap backup %s does not merge: %w", backupPath, err)
	}
	return writeKeymapBytes(path, raw, store.writeFile)
}

// keymapMigrationConflictError renders a blocked plan as the error every entry
// point reports.
func keymapMigrationConflictError(plan keymapMigrationPlan) error {
	lines := make([]string, 0, len(plan.Conflicts)+1)
	lines = append(lines, fmt.Sprintf(
		"keymap %s has %d conflicting binding table(s); no keymap or tmux config was changed",
		plan.Path, len(plan.Conflicts)))
	for _, conflict := range plan.Conflicts {
		lines = append(lines, "  "+conflict.String())
	}
	return errors.New(strings.Join(lines, "\n"))
}

// migrateKeymapForWrite is the preflight-then-apply pair every explicit write or
// apply boundary calls.
//
// Settings key save, `projmux config apply`, the compatibility
// `projmux tmux apply` and every post-update install path converge here. That is
// what makes the migration lazy but inevitable: whichever of them the user
// reaches first performs it, and the rest then find nothing to do.
func migrateKeymapForWrite(store keymapStore) (keymapMigrationResult, error) {
	plan, err := planKeymapMigration(store)
	if err != nil {
		return keymapMigrationResult{Plan: plan}, err
	}
	return applyKeymapMigration(store, plan)
}

// writeKeymapMigrationPreflight reports a plan without changing anything.
//
// It writes to the caller's diagnostic stream, never to stdout of a route whose
// stdout is a generated artifact: `config render` must stay byte-identical to
// the printer it forwards to.
func writeKeymapMigrationPreflight(w io.Writer, plan keymapMigrationPlan) {
	if w == nil || !plan.Present {
		return
	}
	if plan.Blocked() {
		fmt.Fprintf(w, "keymap migration blocked: %s\n", plan.Path)
		for _, conflict := range plan.Conflicts {
			fmt.Fprintf(w, "  conflict: %s\n", conflict.String())
		}
		fmt.Fprintf(w, "  no keymap or tmux config will be written until this is resolved\n")
		return
	}
	if !plan.Required {
		return
	}
	fmt.Fprintf(w, "keymap migration pending: %s (schema_version %d -> %d)\n",
		plan.Path, plan.FromVersion, plan.ToVersion)
	for _, change := range plan.Changes {
		switch change.Disposition {
		case keymapDispositionCanonical:
			if change.SourceID == change.TargetID {
				continue
			}
			fmt.Fprintf(w, "  rename: %s -> %s\n", change.SourceID, change.TargetID)
		case keymapDispositionCoalesce:
			fmt.Fprintf(w, "  coalesce: %s -> %s\n", change.SourceID, change.TargetID)
		case keymapDispositionPreserveUnknown:
			fmt.Fprintf(w, "  unmapped: %s (preserved)\n", change.SourceID)
		case keymapDispositionRetired:
			fmt.Fprintf(w, "  retired: %s (%s)\n", change.SourceID, change.Detail)
		}
	}
	fmt.Fprintf(w, "  run 'projmux config apply' to migrate\n")
}
