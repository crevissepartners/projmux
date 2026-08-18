package app

import (
	"io"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// This file defines the role interfaces settingsCommand depends on instead of
// the concrete command structs. The dependency direction stays strictly
// settings -> {switch, ai, update, quit}; app.go keeps passing the concrete
// commands, which satisfy these interfaces structurally.

// projectContextResolver resolves the project/workdir context Settings renders
// (implemented by *switchCommand; consumed by settings_projects.go).
type projectContextResolver interface {
	resolveWorkingDir() (string, error)
	resolveHomeDir() (string, error)
	resolveSwitchTarget(args []string, command string) (string, error)
	resolveSwitchTargetNoMemoize(args []string, command string) (string, error)
	switchRepoRoot(homeDir string) string
	currentProjdirInfo() (string, string, error)
	projdirSettingsInfo() (projdirSettingsInfo, error)
}

// projectDirStore reads and writes the pin collections and the saved workdirs
// (implemented by *switchCommand; consumed by settings_projects.go).
//
// loadPinRows is the typed read: it returns the managed and candidate pins as
// rows plus the membership lookup, so Settings never has to infer a pin's kind
// from its spelling.
type projectDirStore interface {
	loadPinRows() ([]pinRow, pinSelection, error)
	loadSavedWorkdirs() ([]string, error)
	envWorkdirSources() []envWorkdirSource
	filesystemPinEntries() ([]intpickercompat.Entry, error)
	filesystemWorkdirEntries() ([]intpickercompat.Entry, error)
	saveSavedProjdir(target string, stdout io.Writer) error
	addWorkdir(target string, stdout io.Writer) error
}

// switchSettingsActions executes the switch-owned settings actions
// (implemented by *switchCommand; consumed by settings.go).
type switchSettingsActions interface {
	executeSettingsAction(action string, stdout, stderr io.Writer) error
	executeProjdirSettingsAction(action string, stdout, stderr io.Writer) error
	executeWorkdirSettingsAction(action string, stdout, stderr io.Writer) error
}

// settingsSwitcher is the composite contract behind settingsCommand.switcher.
// A single field keeps the existing struct-literal constructions (tests and
// the ctor) unchanged while the three embedded roles document which slice of
// the switch command each settings file consumes.
type settingsSwitcher interface {
	projectContextResolver
	projectDirStore
	switchSettingsActions
}

// aiModeController toggles the default AI agent mode (implemented by
// *aiCommand; consumed by settings.go and settings_ai.go).
type aiModeController interface {
	getMode() string
	setMode(mode string) error
}

// aiHookSettingsReader reads AI hook catalogs and effective runtime actions
// for the notifications settings pages (implemented by *aiCommand; consumed by
// settings_notifications.go via aiForSettings).
type aiHookSettingsReader interface {
	loadAIHookCatalog(provider string) (aiHookCatalog, error)
	aiHookEffectiveAction(provider, event string) aiHookActionResolution
}

// settingsAI is the composite contract behind settingsCommand.ai.
type settingsAI interface {
	aiModeController
	aiHookSettingsReader
}

// updateRunner runs update actions and reports cached release status
// (implemented by *updateCommand; consumed by settings.go and
// settings_about.go).
type updateRunner interface {
	Run(args []string, stdout, stderr io.Writer) error
	status() (updateStatus, error)
}

// quitRunner opens the quit actions picker (implemented by *quitCommand;
// consumed by settings.go).
type quitRunner interface {
	Run(args []string, stdout, stderr io.Writer) error
}

// Compile-time checks that the concrete commands keep satisfying the settings
// role interfaces.
var (
	_ settingsSwitcher = (*switchCommand)(nil)
	_ settingsAI       = (*aiCommand)(nil)
	_ updateRunner     = (*updateCommand)(nil)
	_ quitRunner       = (*quitCommand)(nil)
)
