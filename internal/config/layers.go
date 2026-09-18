package config

import (
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Settings live in layers. This file is the one place that says which layer a
// setting under ConfigDir belongs to:
//
//   - central: product behavior every surface shares (the TUI, the web, the
//     CLI). Any route may read it.
//   - TUI: how the terminal looks and launches.
//   - WEB: the web's own values for front settings (web.toml).
//   - browser: per-browser conveniences in localStorage; the server never
//     reads them.
//
// TUI and WEB are the front layers. Only the front entry points (settings,
// web, shell, switch, config render|apply|edit and the internal namespace)
// read them; every other public route reads the central layer alone, except
// that a route that opens a picker may read that picker's theme and keys to
// paint it (FrontReadPickerDisplay). Every front read is reported to the
// observer below before the file is opened, so a test can hold a route to
// that rule by counting reads rather than by judging an outcome that would
// look the same if a file had been opened and failed to parse.
//
// Files are not moved by their layer: a setting keeps its file name and place,
// and its layer is declared here next to the path symbol that names it.

// SettingLayer names who may read a setting.
type SettingLayer string

const (
	LayerCentral SettingLayer = "central"
	LayerTUI     SettingLayer = "TUI"
	LayerWeb     SettingLayer = "WEB"
	LayerBrowser SettingLayer = "browser"
)

// SettingShape is how one declared setting is stored.
type SettingShape uint8

const (
	// SettingFile is one file, ConfigDir/File.
	SettingFile SettingShape = iota + 1
	// SettingFileFamily is every file ConfigDir/File<suffix>; File is the
	// prefix the family shares.
	SettingFileFamily
	// SettingDir is a directory, ConfigDir/File/, and everything in it.
	SettingDir
	// SettingConfigKeys is a set of keys inside ConfigDir/config.toml. That
	// file holds keys of more than one layer, so its layer is declared per key
	// set, never for the file.
	SettingConfigKeys
	// SettingBrowserKeys is browser localStorage keys starting with File. It
	// is not a file; it is declared so the docs table and this one agree.
	SettingBrowserKeys
)

// SettingItem is one declared setting.
type SettingItem struct {
	// Name is the one spelling of the setting: the file name, the family
	// pattern, the directory with a trailing slash, or `config.toml` plus the
	// key set. Failure messages and front-read records use it.
	Name  string
	Layer SettingLayer
	Shape SettingShape
	// File is the path symbol the setting is tied to: the *FileName or
	// *DirName constant (the prefix constant for a family, GlobalConfigFileName
	// for a key set, the key prefix for browser keys).
	File string
	// Doc is how docs/configuration.md's "Settings live in four layers" table
	// lists the setting in its layer's row. Several items may share one Doc
	// spelling (`statusbar-visibility-*`).
	Doc string
}

// Names of the config.toml key sets. A front reader of a TUI key set passes
// one of these to NoteFrontRead.
const (
	SettingConfigUILocale       = GlobalConfigFileName + " [ui] locale"
	SettingConfigUpdate         = GlobalConfigFileName + " [update]"
	SettingConfigStartup        = GlobalConfigFileName + " [startup]"
	SettingConfigHooks          = GlobalConfigFileName + " [hooks.*]"
	SettingConfigEnv            = GlobalConfigFileName + " [env]"
	SettingConfigAISplitCWDFrom = GlobalConfigFileName + " [ai] split_cwd_from"
	SettingConfigTheme          = GlobalConfigFileName + " [theme]"
	SettingConfigUINativeKeys   = GlobalConfigFileName + " [ui] native_keys"
	SettingConfigAIResume       = GlobalConfigFileName + " [ai] resume_*"

	// PersonasDirName is the persona file directory. internal/core/persona owns
	// its storage (persona.DirName); the name is repeated here only so the
	// layer is declared in this one table, and a test holds the two equal.
	PersonasDirName = "personas"

	// WebBrowserStoragePrefix is the localStorage key prefix of the web
	// client's per-browser conveniences.
	WebBrowserStoragePrefix = "projmux.web."
)

// settingItems is the declaration. Adding a setting file is adding its path
// symbol and one row here; a test fails for a path symbol with no row.
var settingItems = []SettingItem{
	// central: config.toml central keys.
	declareKeys(SettingConfigUILocale, LayerCentral, "[ui] locale"),
	declareKeys(SettingConfigUpdate, LayerCentral, "[update]"),
	declareKeys(SettingConfigStartup, LayerCentral, "[startup]"),
	declareKeys(SettingConfigHooks, LayerCentral, "[hooks.*]"),
	declareKeys(SettingConfigEnv, LayerCentral, "[env]"),
	declareKeys(SettingConfigAISplitCWDFrom, LayerCentral, "[ai] split_cwd_from"),
	// central: files and directories.
	declareFile(AIEnabledAgentsFileName, LayerCentral, ""),
	declareFile(ProjdirFileName, LayerCentral, ""),
	declareFile(WorkdirsFileName, LayerCentral, ""),
	declareFile(PinsFileName, LayerCentral, ""),
	declareFile(TagsFileName, LayerCentral, ""),
	declareFile(ProjectHooksFileName, LayerCentral, ""),
	declareFile(DesktopNotifyModeFileName, LayerCentral, ""),
	declareFile(AINotifyDedupeSecondsFileName, LayerCentral, ""),
	declareFile(AIHookActionsFileName, LayerCentral, ""),
	declareFile(AISemanticPoliciesFileName, LayerCentral, ""),
	declareFile(LiveResourcesFileName, LayerCentral, ""),
	declareDir(AIHooksDirName, LayerCentral),
	declareDir(PersonasDirName, LayerCentral),
	// The global lifecycle hook scripts (post-create, post-attach,
	// pre-create) are the hook contract, which is product behavior.
	declareDir(HooksDirName, LayerCentral),

	// TUI.
	declareFile(StatusbarNotificationsHUDVisibilityFileName, LayerTUI, "statusbar-visibility-*"),
	declareFile(StatusbarAgentUsageHUDVisibilityFileName, LayerTUI, "statusbar-visibility-*"),
	declareFamily(StatusbarAgentUsageProviderVisibilityFilePrefix, "<provider>", LayerTUI, "statusbar-visibility-*"),
	declareFamily(StatusbarAgentUsageWindowVisibilityFilePrefix, "<provider>-<window>", LayerTUI, "statusbar-visibility-*"),
	declareFile(StatusbarProjectVisibilityFileName, LayerTUI, "statusbar-visibility-*"),
	declareFile(StatusbarWorkingDirectoryVisibilityFileName, LayerTUI, "statusbar-visibility-*"),
	declareFile(StatusbarGitVisibilityFileName, LayerTUI, "statusbar-visibility-*"),
	declareFile(StatusbarClockVisibilityFileName, LayerTUI, "statusbar-visibility-*"),
	declareFile(StatusbarSettingsLauncherVisibilityFileName, LayerTUI, "statusbar-visibility-*"),
	declareFile(StatusbarDecorationFileName, LayerTUI, "statusbar-decoration*"),
	declareFile(StatusbarDecorationCwdFileName, LayerTUI, "statusbar-decoration*"),
	declareFile(StatusbarDecorationGitFileName, LayerTUI, "statusbar-decoration*"),
	declareFile(StatusbarDecorationNotifyFileName, LayerTUI, "statusbar-decoration*"),
	declareFile(AIBadgeStyleFileName, LayerTUI, ""),
	declareFile(RuntimeDiagnosticsVisibilityFileName, LayerTUI, ""),
	declareFile(KeymapFileName, LayerTUI, ""),
	declareFile(TmuxAISplitModeFileName, LayerTUI, ""),
	declareKeys(SettingConfigTheme, LayerTUI, "[theme]"),
	declareKeys(SettingConfigUINativeKeys, LayerTUI, "[ui] native_keys"),
	declareKeys(SettingConfigAIResume, LayerTUI, "[ai] resume_*"),

	// WEB.
	declareFile(WebSettingsFileName, LayerWeb, ""),

	// browser.
	{Name: WebBrowserStoragePrefix + "*", Layer: LayerBrowser, Shape: SettingBrowserKeys, File: WebBrowserStoragePrefix, Doc: WebBrowserStoragePrefix + "*"},
}

func declareFile(name string, layer SettingLayer, doc string) SettingItem {
	if doc == "" {
		doc = name
	}
	return SettingItem{Name: name, Layer: layer, Shape: SettingFile, File: name, Doc: doc}
}

func declareFamily(prefix, suffix string, layer SettingLayer, doc string) SettingItem {
	return SettingItem{Name: prefix + suffix, Layer: layer, Shape: SettingFileFamily, File: prefix, Doc: doc}
}

func declareDir(name string, layer SettingLayer) SettingItem {
	return SettingItem{Name: name + "/", Layer: layer, Shape: SettingDir, File: name, Doc: name + "/"}
}

func declareKeys(name string, layer SettingLayer, doc string) SettingItem {
	return SettingItem{Name: name, Layer: layer, Shape: SettingConfigKeys, File: GlobalConfigFileName, Doc: doc}
}

// SettingItems returns the declaration, in declaration order.
func SettingItems() []SettingItem {
	return append([]SettingItem(nil), settingItems...)
}

// LookupSetting returns the declared item named name.
func LookupSetting(name string) (SettingItem, bool) {
	for _, item := range settingItems {
		if item.Name == name {
			return item, true
		}
	}
	return SettingItem{}, false
}

// SettingForFile returns the declared file, family or directory item that a
// path relative to ConfigDir belongs to. config.toml resolves to nothing: its
// layer is declared per key set.
func SettingForFile(rel string) (SettingItem, bool) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	first, _, nested := strings.Cut(rel, "/")
	for _, item := range settingItems {
		switch item.Shape {
		case SettingFile:
			if !nested && rel == item.File {
				return item, true
			}
		case SettingFileFamily:
			if !nested && strings.HasPrefix(rel, item.File) && len(rel) > len(item.File) {
				return item, true
			}
		case SettingDir:
			if first == item.File {
				return item, true
			}
		}
	}
	return SettingItem{}, false
}

// FrontReadPurpose is why a front setting is read.
type FrontReadPurpose uint8

const (
	// FrontReadSetting is a read that lets the setting decide behavior. Only
	// the front entry points may make one.
	FrontReadSetting FrontReadPurpose = iota
	// FrontReadPickerDisplay is a read that only paints a picker: its theme
	// and the keys its footer and actions show. A picker's look is not a
	// guaranteed CLI result (C-1 Non-Guarantee, D5), so any route that opens
	// a picker may make one -- but only of pickerDisplayItems.
	FrontReadPickerDisplay
)

// pickerDisplayItems are the only front settings a picker display read may
// name. A picker display read of anything else is reported as a setting
// read, so the exemption cannot widen past these two.
var pickerDisplayItems = []string{SettingConfigTheme, KeymapFileName}

// FrontRead is one read of a front-layer (TUI or WEB) setting.
type FrontRead struct {
	// Item is the declared setting read. A reader handed a path this
	// declaration does not name reports an item with Name set to the path's
	// base name and Shape zero.
	Item SettingItem
	// Path is the file about to be read.
	Path string
	// Purpose is FrontReadPickerDisplay only for a picker display read of
	// one of pickerDisplayItems; every other read is FrontReadSetting.
	Purpose FrontReadPurpose
}

var frontReadObserver atomic.Pointer[func(FrontRead)]

// ObserveFrontReads makes observe the receiver of every front-layer read
// until the returned restore runs.
//
// It is a test hook: the settings layer guard installs it to prove that a
// public route outside the front entry points reads no front setting.
// Production code never installs an observer, and with none installed a front
// read costs one atomic load. observe may be called from any goroutine.
func ObserveFrontReads(observe func(FrontRead)) (restore func()) {
	previous := frontReadObserver.Swap(&observe)
	return func() { frontReadObserver.Store(previous) }
}

// NoteFrontRead reports that the caller is about to read the front setting
// named name (a declared item Name, such as KeymapFileName or
// SettingConfigTheme) at path so the setting can decide behavior. Every front
// reader calls it (or NotePickerDisplayRead), or reads through a loader in
// this package that does, before it opens the file.
func NoteFrontRead(name, path string) {
	noteFrontRead(name, path, FrontReadSetting)
}

// NotePickerDisplayRead is NoteFrontRead for a read made only to paint a
// picker. Only the picker render path passes it, explicitly; a shared helper
// called outside a picker reports through NoteFrontRead. A picker display
// read of a setting outside pickerDisplayItems is reported as a setting read.
func NotePickerDisplayRead(name, path string) {
	purpose := FrontReadSetting
	for _, exempt := range pickerDisplayItems {
		if name == exempt {
			purpose = FrontReadPickerDisplay
		}
	}
	noteFrontRead(name, path, purpose)
}

func noteFrontRead(name, path string, purpose FrontReadPurpose) {
	observe := frontReadObserver.Load()
	if observe == nil {
		return
	}
	item, ok := LookupSetting(name)
	if !ok {
		item = SettingItem{Name: name}
	}
	(*observe)(FrontRead{Item: item, Path: path, Purpose: purpose})
}

// noteFrontFile is NoteFrontRead for the loaders in this package, which are
// handed a path: the item is resolved from the file name.
func noteFrontFile(path string) {
	observe := frontReadObserver.Load()
	if observe == nil {
		return
	}
	item, ok := SettingForFile(filepath.Base(path))
	if !ok {
		item = SettingItem{Name: filepath.Base(path)}
	}
	(*observe)(FrontRead{Item: item, Path: path, Purpose: FrontReadSetting})
}
