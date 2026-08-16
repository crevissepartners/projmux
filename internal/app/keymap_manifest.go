package app

import "fmt"

// keymapDisposition is what the migrator decided to do with one `[bindings.*]`
// table it found on disk.
//
// Every table in a keymap file gets exactly one of these. That exhaustiveness is
// the point: a table that matched no rule and was quietly dropped is the failure
// mode this schema exists to make impossible, so "I did not recognise this" is
// itself a named disposition with a report line rather than silence.
type keymapDisposition string

const (
	// keymapDispositionCanonical: the table names a catalogued action. It is
	// rewritten to that action's canonical id, or already carries it.
	keymapDispositionCanonical keymapDisposition = "canonical"
	// keymapDispositionCoalesce: the file carries both a legacy table and the
	// canonical table for one action, and the two overrides are equivalent, so
	// they merge into the single canonical table.
	keymapDispositionCoalesce keymapDisposition = "coalesce"
	// keymapDispositionPreserveUnknown: the table names nothing this binary
	// knows. It is copied through verbatim and reported as unmapped. A newer
	// projmux, or a hand-written experiment, must survive a migration.
	keymapDispositionPreserveUnknown keymapDisposition = "preserve-unknown"
	// keymapDispositionRetired: the table names an id projmux deliberately
	// removed. It is reported with the exact remediation and never silently
	// remapped onto a surviving action.
	keymapDispositionRetired keymapDisposition = "retired-remediation"
)

// keymapRetiredID is an action id projmux used to accept and has since removed.
//
// These are kept in the manifest rather than forgotten so that a migration can
// tell a user *why* their table stopped working. Dropping them from the manifest
// would demote them to preserve-unknown, which reads as "projmux might support
// this later" — the opposite of the truth.
type keymapRetiredID struct {
	ID          string
	Remediation string
	// Fatal marks an id whose presence already fails the parse outright. Only
	// the retired pane-rename id does: its replacement carries different
	// semantics, so reading the old table as if nothing changed would rebind a
	// key onto an action the user did not choose.
	Fatal bool
}

// keymapRetiredIDs is the closed set of retired keymap table ids.
//
// The six popup ids were dropped from the action catalog before this schema
// existed; they resolve to no action today and this Phase does not resurrect
// them as read aliases. What changes here is only that a migration now *says*
// so instead of skipping them without a word.
func keymapRetiredIDs() []keymapRetiredID {
	return []keymapRetiredID{
		{
			ID: retiredPaneRenameActionID,
			Remediation: fmt.Sprintf("replace [bindings.%s] with [bindings.%s]",
				retiredPaneRenameActionID, paneRenameActionID),
			Fatal: true,
		},
		{ID: "sessionizer-sidebar", Remediation: "use the canonical toggle action id instead of the popup mode name"},
		{ID: "notify-sidebar", Remediation: "use the canonical toggle action id instead of the popup mode name"},
		{ID: "session-popup", Remediation: "use the canonical toggle action id instead of the popup mode name"},
		{ID: "ai-split-picker-right", Remediation: "use the canonical toggle action id instead of the popup mode name"},
		{ID: "ai-split-settings", Remediation: "use the canonical toggle action id instead of the popup mode name"},
		{ID: "sessionizer", Remediation: "use the canonical toggle action id instead of the popup mode name"},
	}
}

// keymapManifestEntry is one old-id → canonical-id row.
type keymapManifestEntry struct {
	// SourceID is the id as it may appear in a keymap file.
	SourceID string
	// CanonicalID is the v1 spelling this source id migrates to.
	CanonicalID string
	// ActionID is the runtime action the pair resolves to.
	ActionID string
}

// keymapActionManifest is the exhaustive old/legacy → canonical id table.
//
// It is derived from the action catalog rather than hand-listed so the two can
// never drift: every alias a catalogue action answers to becomes a row, and
// TestKeymapManifestCoversEveryCatalogAction fails if any action contributes
// none. Callers get a map because every consumer is a lookup.
func keymapActionManifest() map[string]keymapManifestEntry {
	manifest := map[string]keymapManifestEntry{}
	for _, action := range defaultKeyBindingCatalog() {
		if action.CanonicalID == "" {
			continue
		}
		for _, id := range keyBindingActionAliases(action) {
			manifest[id] = keymapManifestEntry{
				SourceID:    id,
				CanonicalID: action.CanonicalID,
				ActionID:    action.ID,
			}
		}
	}
	return manifest
}

// keymapRetiredIDIndex is keymapRetiredIDs as a lookup.
func keymapRetiredIDIndex() map[string]keymapRetiredID {
	index := map[string]keymapRetiredID{}
	for _, retired := range keymapRetiredIDs() {
		index[retired.ID] = retired
	}
	return index
}

// keymapBindingKeyForAction picks the table id a write for this action must use.
//
// An id already present in the file wins, so a save never grows a second table
// for an action the file already names in another spelling. Otherwise the file's
// own schema version decides: a v1 file gets the canonical id, a v0 file keeps
// writing the v0 id it has always used. Settings save migrates first, so in
// practice this settles on canonical after the first write.
func keymapBindingKeyForAction(keymap keymapFile, action keyBindingAction) string {
	for _, id := range keyBindingActionAliases(action) {
		if _, ok := keymap.Bindings[id]; ok {
			return id
		}
	}
	if keymap.SchemaVersion >= keymapSchemaVersionV1 && action.CanonicalID != "" {
		return action.CanonicalID
	}
	return action.ID
}
