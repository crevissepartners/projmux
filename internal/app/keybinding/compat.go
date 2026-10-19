package keybinding

import (
	shared "github.com/crevissepartners/projmux/internal/services/keybinding"
	"io"
	"os"
)

type KeyBindingAction = shared.KeyBindingAction
type KeymapFile = shared.KeymapFile
type KeymapLoader = shared.KeymapLoader
type KeymapMigrationResult = shared.KeymapMigrationResult
type KeymapOverride = shared.KeymapOverride
type KeymapStore = shared.KeymapStore
type keyBindingActionKind = shared.KeyBindingActionKind
type keyBindingActionSemantics = shared.KeyBindingActionSemantics
type keyBindingScope = shared.KeyBindingScope
type keyBindingTier = shared.KeyBindingTier
type keymapMigrationPlan = shared.KeymapMigrationPlan
type tmuxBindingKind = shared.TmuxBindingKind

const CanonicalCreateTargetClientEnv = shared.CanonicalCreateTargetClientEnv
const GeneratedRenameHeredocDelimiter = shared.GeneratedRenameHeredocDelimiter
const GeneratedRenameResponseSentinel = shared.GeneratedRenameResponseSentinel
const GeneratedRenameStdinFlag = shared.GeneratedRenameStdinFlag
const KeyBindingActionPickerInternal = shared.KeyBindingActionPickerInternal
const KeyBindingAnchorActiveTmuxPane = shared.KeyBindingAnchorActiveTmuxPane
const KeyBindingAnchorCurrentPaneCwdSeed = shared.KeyBindingAnchorCurrentPaneCwdSeed
const KeyBindingAnchorCurrentPaneSplitTarget = shared.KeyBindingAnchorCurrentPaneSplitTarget
const KeyBindingCategoryAgentPane = shared.KeyBindingCategoryAgentPane
const KeyBindingCategoryAgentPaneLabel = shared.KeyBindingCategoryAgentPaneLabel
const KeyBindingCategoryInput = shared.KeyBindingCategoryInput
const KeyBindingCategoryInputLabel = shared.KeyBindingCategoryInputLabel
const KeyBindingCategoryLaunch = shared.KeyBindingCategoryLaunch
const KeyBindingCategoryLaunchLabel = shared.KeyBindingCategoryLaunchLabel
const KeyBindingCategoryNavigation = shared.KeyBindingCategoryNavigation
const KeyBindingCategoryNavigationLabel = shared.KeyBindingCategoryNavigationLabel
const KeyBindingCategorySurfaces = shared.KeyBindingCategorySurfaces
const KeyBindingCategorySurfacesLabel = shared.KeyBindingCategorySurfacesLabel
const KeyBindingPlacementDown = shared.KeyBindingPlacementDown
const KeyBindingPlacementRight = shared.KeyBindingPlacementRight
const KeyBindingScopeApp = shared.KeyBindingScopeApp
const KeyBindingScopeStandalone = shared.KeyBindingScopeStandalone
const KeyBindingTierNativePickerInternal = shared.KeyBindingTierNativePickerInternal
const KeyBindingTierTransportDependent = shared.KeyBindingTierTransportDependent
const KeymapSchemaVersion = shared.KeymapSchemaVersion
const KeymapReservedAuthoringReason = shared.KeymapReservedAuthoringReason
const ManagedDeletePaneGuard = shared.ManagedDeletePaneGuard
const ManagedDeletePaneRoute = shared.ManagedDeletePaneRoute
const ManagedDeleteWindowGuard = shared.ManagedDeleteWindowGuard
const ManagedDeleteWindowRoute = shared.ManagedDeleteWindowRoute
const PaneRenameActionID = shared.PaneRenameActionID
const PaneRenameRoute = shared.PaneRenameRoute
const ResourceInspectorPopupMode = shared.ResourceInspectorPopupMode
const RetiredPaneRenameActionID = shared.RetiredPaneRenameActionID
const TmuxBindingCommand = shared.TmuxBindingCommand
const TmuxBindingCommandPrompt = shared.TmuxBindingCommandPrompt
const TmuxBindingManagedDelete = shared.TmuxBindingManagedDelete
const TmuxBindingPopupToggle = shared.TmuxBindingPopupToggle
const TmuxBindingPromptRunProjmux = shared.TmuxBindingPromptRunProjmux
const TmuxBindingRunProjmux = shared.TmuxBindingRunProjmux
const WindowRenameRoute = shared.WindowRenameRoute

// KeyBindingCategoryOrder is the render order of the Keybindings categories.
// Input delivery holds no keymap action; it owns the native-key toggle only.
var KeyBindingCategoryOrder = []struct {
	ID    string
	Label string
}{
	{KeyBindingCategoryLaunch, KeyBindingCategoryLaunchLabel},
	{KeyBindingCategoryAgentPane, KeyBindingCategoryAgentPaneLabel},
	{KeyBindingCategoryNavigation, KeyBindingCategoryNavigationLabel},
	{KeyBindingCategorySurfaces, KeyBindingCategorySurfacesLabel},
	{KeyBindingCategoryInput, KeyBindingCategoryInputLabel},
}

// KeyBindingSurfaceOrder is the second navigation level inside the
// sidebar/picker category. The surface keys are the existing catalog `Surface`
// values; only the display labels follow the canonical resource vocabulary.
var KeyBindingSurfaceOrder = []struct {
	ID    string
	Label string
}{
	{"Sidebar", "Project Sidebar"},
	{"SessionPopup", "Session Picker"},
	{"NotifySidebar", "Notification Sidebar"},
	{"Resources", "Registry Inspector"},
	{"RuntimeDiagnostics", "Runtime Diagnostics"},
	{"Settings", "Settings"},
}

func DefaultKeyBindingCatalog() []KeyBindingAction { return shared.DefaultKeyBindingCatalog() }
func KeyBindingCatalogForScope(scope keyBindingScope) []KeyBindingAction {
	return shared.KeyBindingCatalogForScope(scope)
}
func KeyBindingCatalogForScopeFrom(catalog []KeyBindingAction, scope keyBindingScope) []KeyBindingAction {
	return shared.KeyBindingCatalogForScopeFrom(catalog, scope)
}
func KeyBindingActionSemanticsFor(action KeyBindingAction) (keyBindingActionSemantics, bool) {
	return shared.KeyBindingActionSemanticsFor(action)
}
func KeyBindingDisplayName(action KeyBindingAction) string {
	return shared.KeyBindingDisplayName(action)
}
func KeyBindingActionCategory(action KeyBindingAction) (string, bool) {
	return shared.KeyBindingActionCategory(action)
}

// KeyBindingSurfaceLabel maps a catalog `Surface` value to its canonical
// display label.
func KeyBindingSurfaceLabel(surface string) (string, bool) {
	for _, entry := range KeyBindingSurfaceOrder {
		if entry.ID == surface {
			return entry.Label, true
		}
	}
	return "", false
}

func KeyBindingActionAliases(action KeyBindingAction) []string {
	return shared.KeyBindingActionAliases(action)
}
func PopupToggleActionIDForMode(mode string) (string, bool) {
	return shared.PopupToggleActionIDForMode(mode)
}
func KeyBindingEditable(action KeyBindingAction) bool { return shared.KeyBindingEditable(action) }
func KeyBindingProtectedActionReason(action KeyBindingAction) (string, bool) {
	return shared.KeyBindingProtectedActionReason(action)
}
func KeyBindingEffectivePlainChords(action KeyBindingAction) []string {
	return shared.KeyBindingEffectivePlainChords(action)
}
func KeyBindingEffectiveSequences(action KeyBindingAction) []string {
	return shared.KeyBindingEffectiveSequences(action)
}
func UniqueNonEmptyStrings(values []string) []string      { return shared.UniqueNonEmptyStrings(values) }
func TmuxUnbindLines(actions []KeyBindingAction) []string { return shared.TmuxUnbindLines(actions) }
func TmuxMergedUnbindLines(defaults, merged []KeyBindingAction) []string {
	return shared.TmuxMergedUnbindLines(defaults, merged)
}
func TmuxRetiredKeyUnbindLines() []string { return shared.TmuxRetiredKeyUnbindLines() }
func TmuxBindLines(binaryPath string, actions []KeyBindingAction) []string {
	return shared.TmuxBindLines(binaryPath, actions)
}
func TmuxStockPrefixRestoreLines(defaults, merged []KeyBindingAction) []string {
	return shared.TmuxStockPrefixRestoreLines(defaults, merged)
}
func RenderTmuxBindingBody(binaryPath string, action KeyBindingAction) string {
	return shared.RenderTmuxBindingBody(binaryPath, action)
}
func FilterKeyBindingActions(actions []KeyBindingAction, keep func(KeyBindingAction) bool) []KeyBindingAction {
	return shared.FilterKeyBindingActions(actions, keep)
}
func FirstNonEmptyString(values []string) string { return shared.FirstNonEmptyString(values) }
func TmuxShellQuote(value string) string         { return shared.TmuxShellQuote(value) }
func TmuxConfigQuote(value string) string        { return shared.TmuxConfigQuote(value) }
func LoadMergedKeyBindingCatalog(loader KeymapLoader) ([]KeyBindingAction, bool, error) {
	return shared.LoadMergedKeyBindingCatalog(loader)
}
func LoadKeymapForEdit(store KeymapStore) (KeymapFile, []KeyBindingAction, bool, string, error) {
	return shared.LoadKeymapForEdit(store)
}
func SaveKeymapKeys(store KeymapStore, actionID string, keys []string) (string, error) {
	return shared.SaveKeymapKeys(store, actionID, keys)
}
func SaveKeymapSequences(store KeymapStore, actionID string, sequences []string) (string, error) {
	return shared.SaveKeymapSequences(store, actionID, sequences)
}
func ResetKeymapSequences(store KeymapStore, actionID string) (string, error) {
	return shared.ResetKeymapSequences(store, actionID)
}
func ResetKeymapBinding(store KeymapStore, actionID string) (string, error) {
	return shared.ResetKeymapBinding(store, actionID)
}
func ResetKeymapKeys(store KeymapStore, actionID string) (string, error) {
	return shared.ResetKeymapKeys(store, actionID)
}
func KeyBindingActionByID(actions []KeyBindingAction, id string) (KeyBindingAction, bool) {
	return shared.KeyBindingActionByID(actions, id)
}
func WriteKeymapFile(path string, keymap KeymapFile, writeFile func(string, []byte, os.FileMode) error) error {
	return shared.WriteKeymapFile(path, keymap, writeFile)
}
func RenderKeymapFile(keymap KeymapFile) string { return shared.RenderKeymapFile(keymap) }
func KeymapPath(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
	return shared.KeymapPath(homeDir, lookupEnv)
}
func ParseKeymapFile(path, raw string) (KeymapFile, error) { return shared.ParseKeymapFile(path, raw) }
func MergeKeymapOverrides(actions []KeyBindingAction, keymap KeymapFile) ([]KeyBindingAction, error) {
	return shared.MergeKeymapOverrides(actions, keymap)
}
func ValidateKeymapChord(value string) error { return shared.ValidateKeymapChord(value) }
func NormalizeKeymapTypedChord(value string) (string, error) {
	return shared.NormalizeKeymapTypedChord(value)
}
func ReservedKeymapAuthoringBase(chord string) (string, bool) {
	return shared.ReservedKeymapAuthoringBase(chord)
}
func ValidateKeymapAuthoringChord(chord string) error {
	return shared.ValidateKeymapAuthoringChord(chord)
}
func NormalizeKeymapAuthoringChord(chord string) (string, error) {
	return shared.NormalizeKeymapAuthoringChord(chord)
}
func ValidateKeymapAuthoringSequence(sequence string) error {
	return shared.ValidateKeymapAuthoringSequence(sequence)
}
func NormalizeKeymapSequence(value string) (string, error) {
	return shared.NormalizeKeymapSequence(value)
}
func ValidateKeymapConflicts(actions []KeyBindingAction) error {
	return shared.ValidateKeymapConflicts(actions)
}
func KeymapBindingKeyForAction(keymap KeymapFile, action KeyBindingAction) string {
	return shared.KeymapBindingKeyForAction(keymap, action)
}
func PlanKeymapMigration(store KeymapStore) (keymapMigrationPlan, error) {
	return shared.PlanKeymapMigration(store)
}
func ApplyKeymapMigration(store KeymapStore, plan keymapMigrationPlan) (KeymapMigrationResult, error) {
	return shared.ApplyKeymapMigration(store, plan)
}
func RollbackKeymapMigration(store KeymapStore, backupPath string) error {
	return shared.RollbackKeymapMigration(store, backupPath)
}
func MigrateKeymapForWrite(store KeymapStore) (KeymapMigrationResult, error) {
	return shared.MigrateKeymapForWrite(store)
}
func WriteKeymapMigrationPreflight(w io.Writer, plan keymapMigrationPlan) {
	shared.WriteKeymapMigrationPreflight(w, plan)
}

const TmuxPaneEnvPrefix = shared.TmuxPaneEnvPrefix

const keyBindingActionCommand = shared.KeyBindingActionCommand
