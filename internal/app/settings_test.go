package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
	"github.com/crevissepartners/projmux/internal/version"
)

func TestSettingsRootEntriesHaveAxisMetadata(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	want := map[string]settingsEntryMeta{
		settingsSectionProject:       {Name: "Project Picker", Axis: settingsAxisGlobal},
		settingsSectionGlobalHooks:   {Name: "Hooks", Axis: settingsAxisGlobal},
		settingsSectionAI:            {Name: "AI Settings", Axis: settingsAxisGlobal},
		settingsSectionNotifications: {Name: "Notifications", Axis: settingsAxisGlobal},
		settingsSectionStatusbar:     {Name: "Appearance", Axis: settingsAxisGlobal},
		settingsSectionSessionState:  {Name: "Session State", Axis: settingsAxisGlobal},
		settingsSectionKeybindings:   {Name: "Keybindings", Axis: settingsAxisGlobal},
		settingsSectionLabs:          {Name: "Labs", Axis: settingsAxisGlobal},
		settingsSectionAbout:         {Name: "About", Axis: settingsAxisGlobal},
	}

	seen := map[string]bool{}
	for _, entry := range cmd.rootEntries() {
		meta, ok := settingsEntryMetaForValue(entry.Value)
		if !ok {
			t.Fatalf("root entry value %q missing settings axis metadata", entry.Value)
		}
		if got := meta; got != want[entry.Value] {
			t.Fatalf("root entry value %q metadata = %#v, want %#v", entry.Value, got, want[entry.Value])
		}
		seen[entry.Value] = true
	}
	for value := range want {
		if !seen[value] {
			t.Fatalf("root entries missing catalogued value %q", value)
		}
	}
}

func TestSettingsRootOptionsDefaultGlobalTab(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	options := cmd.rootOptions(settingsRootTabGlobal)

	if got, want := options.UI, "settings"; got != want {
		t.Fatalf("root settings UI = %q, want %q", got, want)
	}
	if got, want := options.Prompt, "Settings > "; got != want {
		t.Fatalf("root settings prompt = %q, want %q", got, want)
	}
	if got, want := options.Title, "Settings"; got != want {
		t.Fatalf("root settings title = %q, want %q", got, want)
	}
	// Phase 2.7: the popup header is intentionally empty — the titlebar
	// chip strip is the source of truth for the active scope, so the
	// redundant "Project context: (...)" line above the search bar is
	// dropped on every page.
	if got := options.Header; got != "" {
		t.Fatalf("root settings header = %q, want empty (chip strip is source of truth)", got)
	}
	wantChips := []projmuxpicker.Chip{
		{Label: "Global", Active: true, ClickValue: settingsRootTabGlobalValue},
		{Label: "Project", Disabled: true, ClickValue: settingsRootTabProjectValue},
	}
	if got := options.TitleChips; !reflect.DeepEqual(got, wantChips) {
		t.Fatalf("root settings title chips = %#v, want %#v", got, wantChips)
	}
	if got, want := options.Footer, "Enter: open  |  Alt-Shift-Left/Alt-Shift-Right or click chip: switch tab  |  Esc/Alt+5/Ctrl+Alt+S: close"; got != want {
		t.Fatalf("root settings footer = %q, want %q", got, want)
	}
	if got, want := options.ExpectKeys, []string{"enter", "ctrl-g", "ctrl-p", "alt-shift-left", "alt-shift-right"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root settings expect keys = %#v, want %#v", got, want)
	}
	if got, want := options.Bindings, []string{"esc:abort", "ctrl-c:abort", "alt-5:abort", "ctrl-alt-s:abort"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root settings close bindings = %#v, want %#v", got, want)
	}
	if got, want := entryValues(options.Entries), []string{
		settingsSectionProject,
		settingsSectionAI,
		settingsSectionNotifications,
		settingsSectionGlobalHooks,
		settingsSectionStatusbar,
		settingsSectionSessionState,
		settingsSectionKeybindings,
		settingsSectionLabs,
		settingsSectionAbout,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root settings entry order = %#v, want %#v", got, want)
	}
}

func TestSettingsRootSwitchesToProjectTab(t *testing.T) {
	t.Parallel()

	var calls int
	var projectOptions intpickercompat.Options
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if got := options.TitleChips; len(got) < 1 || !got[0].Active {
				t.Fatalf("first root chips = %#v, want Global active", got)
			}
			return intpickercompat.Result{Key: "ctrl-p"}, nil
		case 2:
			projectOptions = options
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd := &settingsCommand{
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
		lookupEnv:    func(string) string { return "" },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// Without a project context, Ctrl-P remains a no-op so the second
	// picker call still renders the Global tab.
	if got, want := projectOptions.Prompt, "Settings > "; got != want {
		t.Fatalf("second tab prompt = %q, want %q (no project context blocks tab switch)", got, want)
	}
	if got := projectOptions.TitleChips; len(got) < 2 || !got[0].Active || !got[1].Disabled {
		t.Fatalf("second tab chips = %#v, want Global active and Project disabled (no project context)", got)
	}
}

func TestSettingsRootAltArrowTogglesTabsWhenProjectAvailable(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "app")
	mkdirAll(t, filepath.Join(project, ".git"))

	var calls int
	var projectOptions intpickercompat.Options
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "alt-shift-right"}, nil
		case 2:
			projectOptions = options
			return intpickercompat.Result{}, nil
		default:
			return intpickercompat.Result{}, nil
		}
	})
	cmd := &settingsCommand{
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := projectOptions.Prompt, "Settings > Project > "; got != want {
		t.Fatalf("project tab prompt = %q, want %q after alt-shift-right", got, want)
	}
	if got := projectOptions.TitleChips; len(got) < 2 || got[0].Active || !got[1].Active || got[1].Disabled {
		t.Fatalf("project tab chips after alt-shift-right = %#v, want Project active and not disabled", got)
	}
}

func TestSettingsRootAltArrowToggleInvariantWithProjectContext(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "app")
	mkdirAll(t, filepath.Join(project, ".git"))

	var calls int
	var third intpickercompat.Options
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			// Global tab — pivot right.
			return intpickercompat.Result{Key: "alt-shift-right"}, nil
		case 2:
			// Project tab — pivot back left.
			return intpickercompat.Result{Key: "alt-shift-left"}, nil
		case 3:
			third = options
			return intpickercompat.Result{}, nil
		default:
			return intpickercompat.Result{}, nil
		}
	})
	cmd := &settingsCommand{
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := third.Prompt, "Settings > "; got != want {
		t.Fatalf("toggled-back prompt = %q, want %q", got, want)
	}
	if got := third.TitleChips; len(got) < 2 || !got[0].Active || got[1].Active {
		t.Fatalf("toggled-back chips = %#v, want Global active after alt-shift-right then alt-shift-left", got)
	}
}

func TestSettingsRootAltArrowDoesNotShadowGlobalSelectPaneChords(t *testing.T) {
	t.Parallel()

	// The settings popup binds Alt-Shift-Left/Alt-Shift-Right for tab
	// navigation while global tmux next/prev-window chords keep the same
	// physical keys. This is safe because tmux popups consume their own
	// stdin and the popup is the active terminal client when
	// Alt-Shift-Left/Right is pressed — the global chord only fires
	// outside the popup. Verify the catalog still ships M-S-Left /
	// M-S-Right (and M-Left / M-Right select-pane) so the global behaviour
	// does not regress.
	catalog := defaultKeyBindingCatalog()
	wantChords := map[string]string{
		"select-pane-left":  "M-Left",
		"select-pane-right": "M-Right",
		"previous-window":   "M-S-Left",
		"next-window":       "M-S-Right",
	}
	for id, chord := range wantChords {
		var got string
		for _, action := range catalog {
			if action.ID == id {
				got = action.PlainChord
				break
			}
		}
		if got != chord {
			t.Fatalf("keybinding catalog %q chord = %q, want %q", id, got, chord)
		}
	}

	rootOpts := (&settingsCommand{}).rootOptions(settingsRootTabGlobal)
	popupKeys := map[string]bool{}
	for _, key := range rootOpts.ExpectKeys {
		popupKeys[key] = true
	}
	for _, key := range []string{"alt-shift-left", "alt-shift-right"} {
		if !popupKeys[key] {
			t.Fatalf("settings popup expect keys = %#v, want %q", rootOpts.ExpectKeys, key)
		}
	}
	// The legacy Alt-Left/Alt-Right chord no longer toggles tabs so
	// muscle-memory holders of the new Alt-Shift chord do not double-bind.
	for _, key := range []string{"alt-left", "alt-right"} {
		if popupKeys[key] {
			t.Fatalf("settings popup expect keys = %#v, want %q removed (Phase 2.6 chord changed to alt-shift)", rootOpts.ExpectKeys, key)
		}
	}
}

func TestSettingsRootAltArrowIsNoopWithoutProjectContext(t *testing.T) {
	t.Parallel()

	var calls int
	var secondOptions intpickercompat.Options
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "alt-shift-right"}, nil
		case 2:
			secondOptions = options
			return intpickercompat.Result{}, nil
		default:
			return intpickercompat.Result{}, nil
		}
	})
	cmd := &settingsCommand{
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
		lookupEnv:    func(string) string { return "" },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := secondOptions.Prompt, "Settings > "; got != want {
		t.Fatalf("alt-shift-right with no project context = %q, want still on Global tab %q", got, want)
	}
	if got := secondOptions.TitleChips; len(got) < 2 || !got[0].Active || got[1].Active || !got[1].Disabled {
		t.Fatalf("alt-shift-right chips = %#v, want Global active and Project disabled (single-tab no-op)", got)
	}
}

func TestSettingsRootChipClickSwitchesToProjectTab(t *testing.T) {
	t.Parallel()

	// Phase 2.6: chip click resolves through Value sentinels so the
	// settings loop can treat keyboard chord (Alt-Shift-Right) and mouse
	// click on the chip strip as equivalent tab transitions.
	home := t.TempDir()
	project := filepath.Join(home, "app")
	mkdirAll(t, filepath.Join(project, ".git"))

	var calls int
	var projectOptions intpickercompat.Options
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "chip", Value: settingsRootTabProjectValue}, nil
		case 2:
			projectOptions = options
			return intpickercompat.Result{}, nil
		default:
			return intpickercompat.Result{}, nil
		}
	})
	cmd := &settingsCommand{
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := projectOptions.Prompt, "Settings > Project > "; got != want {
		t.Fatalf("project tab prompt after chip click = %q, want %q", got, want)
	}
}

func TestSettingsRootChipClickOnDisabledProjectChipIsNoop(t *testing.T) {
	t.Parallel()

	// Without a project context the Project chip is disabled — picker
	// suppresses the click at hit detection time so the settings loop
	// never sees a chip Result for the Project tab. Mimic that behaviour
	// in the runner stub by returning an empty result on the first call;
	// the loop should fall through to "close picker" rather than toggle.
	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		// First call: Global tab. Return Closed result (Key empty, Value
		// empty) so the loop exits cleanly. If chip click on a disabled
		// chip were emitted as a Value we'd loop forever.
		return intpickercompat.Result{}, nil
	})
	cmd := &settingsCommand{
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
		lookupEnv:    func(string) string { return "" },
	}
	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("runner calls = %d, want exactly one picker invocation when disabled chip click is suppressed", calls)
	}
}

func TestSettingsProjectTabNoProjectShowsDisabledState(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{lookupEnv: func(string) string { return "" }}
	options := cmd.rootOptions(settingsRootTabProject)

	// Phase 2.7: the dedicated "Project context: (none) - open
	// Settings..." header line is dropped. The Project chip rendering
	// (active + disabled) below already conveys the no-project state.
	if got := options.Header; got != "" {
		t.Fatalf("project tab header = %q, want empty (chip strip carries the no-project hint)", got)
	}
	if got := options.TitleChips; len(got) < 2 || got[0].Active || !got[1].Active || !got[1].Disabled {
		t.Fatalf("project tab chips (no project) = %#v, want Project chip active+disabled", got)
	}
	for _, value := range entryValues(options.Entries) {
		if value != settingsNoopValue {
			t.Fatalf("project tab entry values = %#v, want disabled/noop rows only without inline tab toggle", entryValues(options.Entries))
		}
	}
	for _, label := range []string{"Trust", "Hooks (project)", "Project recipe", "Effective merge view"} {
		if !hasEntryLabelContaining(options.Entries, label) {
			t.Fatalf("project tab entries = %#v, want label containing %q", options.Entries, label)
		}
	}
	// Phase 2.6: the chip strip plus popup header already announce the
	// active scope, so the entry list drops the redundant "Project
	// context" placeholder row that lived above the search bar.
	if hasEntryLabelContaining(options.Entries, "Project context") {
		t.Fatalf("project tab entries = %#v, want no \"Project context\" placeholder row", options.Entries)
	}
}

func TestSettingsProjectContextPrefersPROJMUXCWD(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	envProject := filepath.Join(home, "env-project")
	paneProject := filepath.Join(home, "source", "repos", "pane-project")
	mkdirAll(t, filepath.Join(paneProject, ".git"))

	cmd := &settingsCommand{
		switcher: &switchCommand{
			discover: candidates.Discover,
			pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
			validate: func(string) error { return nil },
			homeDir:  func() (string, error) { return home, nil },
			workingDir: func() (string, error) {
				return filepath.Join(paneProject, "nested"), nil
			},
			lookupEnv:    func(string) string { return "" },
			loadWorkdirs: func(string) ([]string, error) { return nil, nil },
		},
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return envProject
			}
			return ""
		},
	}

	ctx := cmd.resolveSettingsProjectContext()
	if got := ctx.Path; got != envProject {
		t.Fatalf("project context path = %q, want PROJMUX_CWD %q", got, envProject)
	}
	if got, want := ctx.Source, "PROJMUX_CWD env"; got != want {
		t.Fatalf("project context source = %q, want %q", got, want)
	}
	if got, want := ctx.Name, "env-project"; got != want {
		t.Fatalf("project context name = %q, want %q", got, want)
	}
}

func TestSettingsProjectContextFallsBackToPaneProjectRoot(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "app")
	mkdirAll(t, filepath.Join(project, ".projmux"))

	cmd := &settingsCommand{
		switcher: &switchCommand{
			discover: candidates.Discover,
			pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
			validate: func(string) error { return nil },
			homeDir:  func() (string, error) { return home, nil },
			workingDir: func() (string, error) {
				return filepath.Join(project, "subdir"), nil
			},
			lookupEnv:    func(string) string { return "" },
			loadWorkdirs: func(string) ([]string, error) { return nil, nil },
		},
		lookupEnv: func(string) string { return "" },
	}

	ctx := cmd.resolveSettingsProjectContext()
	if got := ctx.Path; got != project {
		t.Fatalf("project context path = %q, want pane project %q", got, project)
	}
	if got, want := ctx.Source, "pane_current_path"; got != want {
		t.Fatalf("project context source = %q, want %q", got, want)
	}
}

func TestSettingsProjectContextFallsBackToSwitchContext(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repoRoot := filepath.Join(home, "source", "repos")
	project := filepath.Join(repoRoot, "app")
	current := filepath.Join(project, "subdir")
	mkdirAll(t, current)

	cmd := &settingsCommand{
		switcher: &switchCommand{
			discover: candidates.Discover,
			pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
			validate: func(string) error { return nil },
			homeDir:  func() (string, error) { return home, nil },
			workingDir: func() (string, error) {
				return current, nil
			},
			lookupEnv: func(name string) string {
				if name == projdirEnvVar {
					return repoRoot
				}
				return ""
			},
			loadWorkdirs: func(string) ([]string, error) { return nil, nil },
		},
		lookupEnv: func(string) string { return "" },
	}

	ctx := cmd.resolveSettingsProjectContext()
	if got := ctx.Path; got != project {
		t.Fatalf("project context path = %q, want switch context %q", got, project)
	}
	if got, want := ctx.Source, "switch context"; got != want {
		t.Fatalf("project context source = %q, want %q", got, want)
	}
}

func TestSettingsEntryCatalogClassifiesRelevantRowsAndActions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		value string
		axis  SettingsAxis
	}{
		{settingsRootTabGlobalValue, settingsAxisBoth},
		{settingsRootTabProjectValue, settingsAxisBoth},
		{settingsSectionGlobalHooks, settingsAxisGlobal},
		{settingsSectionProjectHooks, settingsAxisProject},
		{settingsSectionProjectConfig, settingsAxisProject},
		{settingsSectionEffectiveMerge, settingsAxisProject},
		{settingsSectionProjectSessionState, settingsAxisProject},
		{settingsProjectRootManage, settingsAxisGlobal},
		{settingsWorkdirList, settingsAxisGlobal},
		{settingsProjectPins, settingsAxisGlobal},
		{settingsAIDefaultMode, settingsAxisGlobal},
		{settingsSectionNotifications, settingsAxisGlobal},
		{settingsNotificationsDesktop, settingsAxisGlobal},
		{settingsLabsProjectHooks, settingsAxisGlobal},
		{settingsActionPrefixAI + aiModeCodex, settingsAxisGlobal},
		{settingsActionPrefixStatusbar + string(config.StatusbarDecorationSymbol), settingsAxisGlobal},
		{settingsActionPrefixKeymap + "settings", settingsAxisGlobal},
		{settingsActionPrefixHooks + string(config.ProjectHooksOn), settingsAxisGlobal},
		{settingsActionPrefixProjectConfig + "startup:set", settingsAxisProject},
	}

	for _, tc := range cases {
		meta, ok := settingsEntryMetaForValue(tc.value)
		if !ok {
			t.Fatalf("settings entry value %q missing catalog metadata", tc.value)
		}
		if got := meta.Axis; got != tc.axis {
			t.Fatalf("settings entry value %q axis = %b, want %b", tc.value, got, tc.axis)
		}
	}
}

func TestSettingsEntryBuildersEmitCataloguedValues(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{}),
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(string) string {
			return ""
		},
	}

	assertCataloguedEntries := func(name string, entries []intpickercompat.Entry) {
		t.Helper()
		for _, entry := range entries {
			if _, ok := settingsEntryMetaForValue(entry.Value); !ok {
				t.Fatalf("%s entry value %q missing settings axis metadata", name, entry.Value)
			}
		}
	}

	assertCataloguedEntries("root", cmd.rootEntries())
	assertCataloguedEntries("ai root", cmd.aiRootEntries())
	assertCataloguedEntries("ai default mode", cmd.aiEntries())
	assertCataloguedEntries("notifications", cmd.notificationsEntries())
	assertCataloguedEntries("desktop notifications", cmd.desktopNotifyEntries())
	assertCataloguedEntries("appearance", cmd.statusbarEntries())
	assertCataloguedEntries("session state", cmd.sessionStateEntries())
	assertCataloguedEntries("project picker", cmd.projectPickerEntries())
	assertCataloguedEntries("labs", cmd.labsEntries())
	assertCataloguedEntries("labs project hooks", cmd.labsProjectHooksEntries())

	projectRootEntries, err := cmd.projectRootEntries()
	if err != nil {
		t.Fatalf("projectRootEntries() error = %v", err)
	}
	assertCataloguedEntries("project root", projectRootEntries)

	workdirEntries, err := cmd.workdirListEntries()
	if err != nil {
		t.Fatalf("workdirListEntries() error = %v", err)
	}
	assertCataloguedEntries("workdirs", workdirEntries)

	pinnedProjectEntries, err := cmd.pinnedProjectEntries()
	if err != nil {
		t.Fatalf("pinnedProjectEntries() error = %v", err)
	}
	assertCataloguedEntries("pinned projects", pinnedProjectEntries)

	for _, value := range []string{
		settingsActionPrefixKeymap + "settings",
		settingsActionPrefixLabKeymap + "settings",
		settingsActionPrefixWorkdir + "remove:/tmp/work",
		settingsActionPrefixSwitch + "add:/tmp/project",
		settingsActionPrefixSwitch + "pin:/tmp/project",
		settingsActionPrefixSwitch + "clear",
	} {
		if _, ok := settingsEntryMetaForValue(value); !ok {
			t.Fatalf("representative generated value %q missing settings axis metadata", value)
		}
	}
}

func TestSettingsHubSetsAIDefaultMode(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	ai := testAICommand(home)
	switcher := testSettingsSwitchCommand(t, &stubSwitchPinStore{})
	var calls int
	var rootOptions intpickercompat.Options
	var aiOptions intpickercompat.Options
	var aiDetailOptions intpickercompat.Options
	cmd := &settingsCommand{
		ai:       ai,
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			if calls == 1 {
				rootOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAI}, nil
			}
			if calls == 2 {
				aiOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsAIDefaultMode}, nil
			}
			if calls == 3 {
				aiDetailOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAI + "codex"}, nil
			}
			return intpickercompat.Result{}, nil
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			if calls == 1 {
				rootOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAI}, nil
			}
			if calls == 2 {
				aiOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsAIDefaultMode}, nil
			}
			if calls == 3 {
				aiDetailOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAI + "codex"}, nil
			}
			return intpickercompat.Result{}, nil
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := rootOptions.UI, "settings"; got != want {
		t.Fatalf("root settings UI = %q, want %q", got, want)
	}
	if got, want := rootOptions.Prompt, "Settings > "; got != want {
		t.Fatalf("root settings prompt = %q, want %q", got, want)
	}
	if got, want := rootOptions.Title, "Settings"; got != want {
		t.Fatalf("root settings title = %q, want %q", got, want)
	}
	if got := rootOptions.TitleChips; len(got) < 1 || !got[0].Active {
		t.Fatalf("root settings chips = %#v, want Global active", got)
	}
	if got, want := rootOptions.Footer, "Enter: open  |  Alt-Shift-Left/Alt-Shift-Right or click chip: switch tab  |  Esc/Alt+5/Ctrl+Alt+S: close"; got != want {
		t.Fatalf("root settings footer = %q, want %q", got, want)
	}
	if got, want := entryValues(rootOptions.Entries), []string{
		settingsSectionProject,
		settingsSectionAI,
		settingsSectionNotifications,
		settingsSectionGlobalHooks,
		settingsSectionStatusbar,
		settingsSectionSessionState,
		settingsSectionKeybindings,
		settingsSectionLabs,
		settingsSectionAbout,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root settings entry order = %#v, want %#v", got, want)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionAI) {
		t.Fatalf("root settings entries = %#v, want AI section", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionNotifications) {
		t.Fatalf("root settings entries = %#v, want Notifications section", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionProject) {
		t.Fatalf("root settings entries = %#v, want project picker section", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionStatusbar) {
		t.Fatalf("root settings entries = %#v, want statusbar section", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionKeybindings) {
		t.Fatalf("root settings entries = %#v, want keybindings section", rootOptions.Entries)
	}
	if !hasEntryLabelContaining(rootOptions.Entries, "Appearance") {
		t.Fatalf("root settings entries = %#v, want generic appearance section label", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionLabs) {
		t.Fatalf("root settings entries = %#v, want labs section", rootOptions.Entries)
	}
	if !hasEntryValue(rootOptions.Entries, settingsSectionAbout) {
		t.Fatalf("root settings entries = %#v, want about section", rootOptions.Entries)
	}
	if got, want := aiOptions.UI, "settings-ai"; got != want {
		t.Fatalf("AI settings UI = %q, want %q", got, want)
	}
	if got, want := aiOptions.Title, "AI Settings"; got != want {
		t.Fatalf("AI settings title = %q, want %q", got, want)
	}
	if got := aiOptions.Header; got != "" {
		t.Fatalf("AI settings header = %q, want description only in title", got)
	}
	if got, want := aiOptions.Prompt, "Settings > AI Settings > "; got != want {
		t.Fatalf("AI settings prompt = %q, want %q", got, want)
	}
	if !hasEntryValue(aiOptions.Entries, settingsBackValue) {
		t.Fatalf("AI settings entries = %#v, want back entry", aiOptions.Entries)
	}
	if !hasEntryValue(aiOptions.Entries, settingsAIDefaultMode) {
		t.Fatalf("AI settings entries = %#v, want Default split mode detail row", aiOptions.Entries)
	}
	if hasEntryValue(aiOptions.Entries, settingsAINotifyDiagnostics) {
		t.Fatalf("AI settings entries = %#v, want Notify integrations moved to Notifications", aiOptions.Entries)
	}
	if hasEntryValue(aiOptions.Entries, settingsActionPrefixAI+aiModeClaude) ||
		hasEntryValue(aiOptions.Entries, settingsActionPrefixAI+aiModeCodex) ||
		hasEntryValue(aiOptions.Entries, settingsActionPrefixAI+aiModeShell) {
		t.Fatalf("AI settings entries = %#v, want no direct mode choices at root", aiOptions.Entries)
	}
	if got, want := aiDetailOptions.UI, "settings-ai-default-mode"; got != want {
		t.Fatalf("AI default mode UI = %q, want %q", got, want)
	}
	for _, want := range []string{
		settingsActionPrefixAI + aiModeClaude,
		settingsActionPrefixAI + aiModeCodex,
		settingsActionPrefixAI + aiModeShell,
	} {
		if !hasEntryValue(aiDetailOptions.Entries, want) {
			t.Fatalf("AI default mode entries = %#v, want %q", aiDetailOptions.Entries, want)
		}
	}
	if got, want := readModeFile(t, home), "codex\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
}

func TestSettingsHubKeepsLabsSectionWithoutPickerBackendChoices(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	var labsOptions intpickercompat.Options
	var tmuxCalls [][]string
	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux"
			}
			return ""
		},
		runCommand: func(name string, args ...string) error {
			tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
			return nil
		},
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionLabs}, nil
			case 2:
				labsOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionLabs}, nil
			case 2:
				labsOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := labsOptions.UI, "settings-labs"; got != want {
		t.Fatalf("labs settings UI = %q, want %q", got, want)
	}
	if !hasEntryValue(labsOptions.Entries, settingsLabsProjectHooks) {
		t.Fatalf("labs settings entries = %#v, want project hooks overview row", labsOptions.Entries)
	}
	if hasEntryValue(labsOptions.Entries, settingsActionPrefixHooks+string(config.ProjectHooksOn)) ||
		hasEntryValue(labsOptions.Entries, settingsActionPrefixHooks+string(config.ProjectHooksOff)) {
		t.Fatalf("labs settings entries = %#v, want no direct project hooks mutation rows", labsOptions.Entries)
	}
	if hasEntryValue(labsOptions.Entries, settingsNotificationsDesktop) ||
		hasEntryLabelContaining(labsOptions.Entries, "Desktop notifications") {
		t.Fatalf("labs settings entries = %#v, want Desktop notifications moved to Notifications", labsOptions.Entries)
	}
	for _, entry := range labsOptions.Entries {
		if strings.HasPrefix(entry.Value, settingsActionPrefixPicker) {
			t.Fatalf("labs settings entries = %#v, want no picker backend choices", labsOptions.Entries)
		}
	}

	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadPickerBackendFile(paths.PickerBackendFile())
	if err != nil {
		t.Fatalf("LoadPickerBackendFile() error = %v", err)
	}
	if got != config.PickerBackendNative {
		t.Fatalf("picker backend = %q, want %q", got, config.PickerBackendNative)
	}
	if len(tmuxCalls) != 0 {
		t.Fatalf("tmux calls = %#v, want none", tmuxCalls)
	}
}

func TestSettingsLabsKeybindingsRedirectsToUnifiedDiagnosticView(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	var keybindingOptions intpickercompat.Options
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionLabs}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsLabKeybindings}, nil
		case 3:
			keybindingOptions = options
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 4:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := keybindingOptions.UI, "settings-keybindings"; got != want {
		t.Fatalf("redirect UI = %q, want %q", got, want)
	}
	if got := keybindingOptions.TitleChips; len(got) != 0 {
		t.Fatalf("redirect chips = %#v, want hidden chips", got)
	}
}

func TestSettingsHubSetsProjectHooksMode(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	var labsOptions intpickercompat.Options
	var overviewOptions intpickercompat.Options
	var tmuxCalls [][]string
	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux"
			}
			return ""
		},
		runCommand: func(name string, args ...string) error {
			tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
			return nil
		},
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionLabs}, nil
			case 2:
				labsOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsLabsProjectHooks}, nil
			case 3:
				overviewOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHooks + string(config.ProjectHooksOff)}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionLabs}, nil
			case 2:
				labsOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsLabsProjectHooks}, nil
			case 3:
				overviewOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHooks + string(config.ProjectHooksOff)}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !hasEntryValue(labsOptions.Entries, settingsLabsProjectHooks) {
		t.Fatalf("labs settings entries = %#v, want project hooks overview row", labsOptions.Entries)
	}
	if !hasEntryValue(overviewOptions.Entries, settingsActionPrefixHooks+string(config.ProjectHooksOff)) {
		t.Fatalf("project hooks overview entries = %#v, want project hooks off row", overviewOptions.Entries)
	}

	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadProjectHooksFile(paths.ProjectHooksFile())
	if err != nil {
		t.Fatalf("LoadProjectHooksFile() error = %v", err)
	}
	if got != config.ProjectHooksOff {
		t.Fatalf("project hooks mode = %q, want %q", got, config.ProjectHooksOff)
	}
	if !reflect.DeepEqual(tmuxCalls, [][]string{
		{"tmux", "display-message", "project hooks: off"},
	}) {
		t.Fatalf("tmux calls = %#v", tmuxCalls)
	}
}

func TestSettingsHubSetsStatusbarDecoration(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	var statusbarOptions intpickercompat.Options
	var tmuxCalls [][]string
	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux"
			}
			return ""
		},
		runCommand: func(name string, args ...string) error {
			tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
			return nil
		},
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionStatusbar}, nil
			case 2:
				statusbarOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixStatusbar + string(config.StatusbarDecorationEmoji)}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionStatusbar}, nil
			case 2:
				statusbarOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixStatusbar + string(config.StatusbarDecorationEmoji)}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := statusbarOptions.UI, "settings-statusbar"; got != want {
		t.Fatalf("statusbar settings UI = %q, want %q", got, want)
	}
	if got, want := statusbarOptions.Title, "Appearance - Status and popup decoration mode"; got != want {
		t.Fatalf("statusbar settings title = %q, want %q", got, want)
	}
	if got, want := statusbarOptions.Prompt, "Settings > Appearance > "; got != want {
		t.Fatalf("statusbar settings prompt = %q, want %q", got, want)
	}
	if got := statusbarOptions.Header; got != "" {
		t.Fatalf("statusbar settings header = %q, want description only in title", got)
	}
	if !hasEntryValue(statusbarOptions.Entries, settingsActionPrefixStatusbar+string(config.StatusbarDecorationOff)) {
		t.Fatalf("statusbar settings entries = %#v, want off row", statusbarOptions.Entries)
	}
	if !hasEntryValue(statusbarOptions.Entries, settingsActionPrefixStatusbar+string(config.StatusbarDecorationSymbol)) {
		t.Fatalf("statusbar settings entries = %#v, want symbol row", statusbarOptions.Entries)
	}
	if !hasEntryValue(statusbarOptions.Entries, settingsActionPrefixStatusbar+string(config.StatusbarDecorationEmoji)) {
		t.Fatalf("statusbar settings entries = %#v, want emoji row", statusbarOptions.Entries)
	}

	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadStatusbarDecorationFile(paths.StatusbarDecorationFile())
	if err != nil {
		t.Fatalf("LoadStatusbarDecorationFile() error = %v", err)
	}
	if got != config.StatusbarDecorationEmoji {
		t.Fatalf("statusbar decoration = %q, want %q", got, config.StatusbarDecorationEmoji)
	}
	if !reflect.DeepEqual(tmuxCalls, [][]string{
		{"tmux", "set-option", "-g", statusbarDecorationTmuxOption, "emoji"},
		{"tmux", "display-message", "decoration mode: emoji"},
	}) {
		t.Fatalf("tmux calls = %#v", tmuxCalls)
	}
}

func TestSettingsSetDesktopNotifyModeWritesTmuxOption(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in       string
		wantOpt  string
		wantText string
	}{
		{"none", "none", "none"},
		{"notify", "notify", "notify"},
		{"raise", "raise", "raise"},
		{"off", "none", "none"}, // alias accepted by parseDesktopNotifyMode
		{"toast", "notify", "notify"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			var tmuxCalls [][]string
			cmd := &settingsCommand{
				lookupEnv: func(name string) string {
					if name == "TMUX" {
						return "/tmp/tmux"
					}
					return ""
				},
				runCommand: func(name string, args ...string) error {
					tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
					return nil
				},
			}
			if err := cmd.setDesktopNotifyMode(tc.in); err != nil {
				t.Fatalf("setDesktopNotifyMode(%q) error = %v", tc.in, err)
			}
			if !reflect.DeepEqual(tmuxCalls, [][]string{
				{"tmux", "set-option", "-g", desktopNotifyModeTmuxOption, tc.wantOpt},
				{"tmux", "display-message", "desktop notifications: " + tc.wantText},
			}) {
				t.Fatalf("tmux calls = %#v", tmuxCalls)
			}
		})
	}
}

func TestSettingsSetDesktopNotifyModeOutsideTmuxIsNoop(t *testing.T) {
	t.Parallel()

	var tmuxCalls [][]string
	cmd := &settingsCommand{
		lookupEnv: func(string) string { return "" },
		runCommand: func(name string, args ...string) error {
			tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
			return nil
		},
	}
	if err := cmd.setDesktopNotifyMode("none"); err != nil {
		t.Fatalf("setDesktopNotifyMode(none) outside tmux returned error: %v", err)
	}
	if len(tmuxCalls) != 0 {
		t.Fatalf("outside tmux: tmux calls = %#v, want no live update", tmuxCalls)
	}
}

func TestSettingsSetDesktopNotifyModeRejectsGarbage(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux"
			}
			return ""
		},
		runCommand: func(string, ...string) error { return nil },
	}
	if err := cmd.setDesktopNotifyMode("garbage"); err == nil {
		t.Fatalf("setDesktopNotifyMode(garbage) expected error, got nil")
	}
}

func TestSettingsAIRootNestsDefaultModeAndExcludesDesktopNotifications(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := &settingsCommand{
		ai:      testAICommand(home),
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_DESKTOP_NOTIFY_MODE" {
				return "raise"
			}
			return ""
		},
	}
	root := cmd.aiRootEntries()
	if !hasEntryValue(root, settingsAIDefaultMode) {
		t.Fatalf("AI root entries = %#v, want Default split mode row", root)
	}
	if hasEntryValue(root, settingsAINotifyDiagnostics) {
		t.Fatalf("AI root entries = %#v, want Notify integrations moved to Notifications", root)
	}
	if got, want := len(root), 2; got != want {
		t.Fatalf("AI root entries = %#v, want back row plus AI default mode only", root)
	}
	for _, want := range []string{
		settingsActionPrefixAI + aiModeClaude,
		settingsActionPrefixAI + aiModeCodex,
		settingsActionPrefixAI + aiModeShell,
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeNone),
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeNotify),
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeRaise),
	} {
		if hasEntryValue(root, want) {
			t.Fatalf("AI root entries = %#v, want no direct row %q", root, want)
		}
	}
	if hasEntryLabelContaining(root, "Desktop notifications") {
		t.Fatalf("AI root entries = %#v, want no Desktop notifications row", root)
	}

	detail := cmd.aiEntries()
	for _, want := range []string{
		settingsActionPrefixAI + aiModeClaude,
		settingsActionPrefixAI + aiModeCodex,
		settingsActionPrefixAI + aiModeShell,
	} {
		if !hasEntryValue(detail, want) {
			t.Fatalf("AI default mode entries = %#v, want row %q", detail, want)
		}
	}
	for _, entry := range detail {
		if strings.Contains(entry.Label, "Desktop notifications") ||
			strings.HasPrefix(entry.Value, settingsActionPrefixDesktopNotifyMode) {
			t.Fatalf("AI default mode entries = %#v, want no Desktop notifications rows", detail)
		}
	}
}

func TestSettingsAINotifyDiagnosticsRenderDoctorRowsAndCommandGuidance(t *testing.T) {
	t.Parallel()

	diagnostics := []doctorAINotifyIntegration{
		{
			ID:             "codex-legacy",
			Name:           "Codex legacy notify",
			Status:         doctorAINotifyStatusInstalled,
			ConfigPath:     "/home/tester/.codex/config.toml",
			InstallCommand: "projmux ai integrate codex --mode legacy",
			RemoveCommand:  "projmux ai integrate codex --mode legacy --remove",
			DryRunCommand:  "projmux ai integrate codex --mode legacy --dry-run",
		},
		{
			ID:             "codex-hooks",
			Name:           "Codex hooks",
			Status:         doctorAINotifyStatusConflict,
			ConfigPath:     "/home/tester/.codex/config.toml",
			ConflictReason: "unmanaged notify command",
			InstallCommand: "projmux ai integrate codex --mode hooks",
			RemoveCommand:  "projmux ai integrate codex --mode hooks --remove",
			DryRunCommand:  "projmux ai integrate codex --mode hooks --dry-run",
		},
		{
			ID:             "claude-hooks",
			Name:           "Claude Code hooks",
			Status:         doctorAINotifyStatusMissing,
			ConfigPath:     "/home/tester/.claude/settings.json",
			InstallCommand: "projmux ai integrate claude",
			RemoveCommand:  "projmux ai integrate claude --remove",
			DryRunCommand:  "projmux ai integrate claude --dry-run",
		},
		{
			ID:             "tmux-bell",
			Name:           "tmux bell fallback",
			Status:         doctorAINotifyStatusMissing,
			InstallCommand: "projmux ai integrate tmux-bell",
			RemoveCommand:  "projmux ai integrate tmux-bell --remove",
			DryRunCommand:  "projmux ai integrate tmux-bell --dry-run",
		},
	}

	var calls int
	var notificationsOptions intpickercompat.Options
	var listOptions intpickercompat.Options
	var detailOptions intpickercompat.Options
	cmd := &settingsCommand{
		ai:                  testAICommand(t.TempDir()),
		aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return diagnostics },
		runCommand: func(string, ...string) error {
			t.Fatal("settings AI notify diagnostics must not execute external commands")
			return nil
		},
		runOutput: func(string, ...string) ([]byte, error) {
			t.Fatal("settings AI notify diagnostics must not shell out for command output")
			return nil, nil
		},
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionNotifications}, nil
			case 2:
				notificationsOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsNotificationsDelivery}, nil
			case 3:
				listOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAINotifyDiagnostic + "codex-hooks"}, nil
			case 4:
				detailOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsNoopValue}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 6:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 7:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 8:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		}),
	}
	cmd.nativePicker = nativePickerFromCompatRunner(cmd.runner)

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !hasEntryValue(notificationsOptions.Entries, settingsNotificationsDelivery) {
		t.Fatalf("Notifications entries = %#v, want delivery sources row", notificationsOptions.Entries)
	}
	if got, want := listOptions.UI, "settings-notifications-delivery"; got != want {
		t.Fatalf("delivery sources UI = %q, want %q", got, want)
	}
	for _, diag := range diagnostics {
		if !hasEntryValue(listOptions.Entries, settingsActionPrefixAINotifyDiagnostic+diag.ID) {
			t.Fatalf("AI notify diagnostics entries = %#v, want %q", listOptions.Entries, diag.ID)
		}
		if !hasEntryLabelContaining(listOptions.Entries, diag.Name) {
			t.Fatalf("AI notify diagnostics entries = %#v, want label %q", listOptions.Entries, diag.Name)
		}
		if !hasEntryLabelContaining(listOptions.Entries, string(diag.Status)) {
			t.Fatalf("AI notify diagnostics entries = %#v, want status %q", listOptions.Entries, diag.Status)
		}
	}
	if !hasEntryLabelContaining(listOptions.Entries, "unmanaged notify command") {
		t.Fatalf("delivery sources entries = %#v, want conflict reason", listOptions.Entries)
	}
	if got, want := detailOptions.UI, "settings-notifications-delivery-detail"; got != want {
		t.Fatalf("delivery source detail UI = %q, want %q", got, want)
	}
	for _, want := range []string{
		"conflict",
		"/home/tester/.codex/config.toml",
		"unmanaged notify command",
		"projmux ai integrate codex --mode hooks",
		"projmux ai integrate codex --mode hooks --remove",
		"projmux ai integrate codex --mode hooks --dry-run",
		"Read-only",
	} {
		if !hasEntryLabelContaining(detailOptions.Entries, want) {
			t.Fatalf("AI notify detail entries = %#v, want %q", detailOptions.Entries, want)
		}
	}
	for _, entry := range detailOptions.Entries {
		if entry.Value != settingsBackValue && entry.Value != settingsNoopValue {
			t.Fatalf("AI notify detail entry = %#v, want read-only/noop value", entry)
		}
	}
}

func TestSettingsNotificationsDesktopNotifyDetailRows(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_DESKTOP_NOTIFY_MODE" {
				return "raise"
			}
			return ""
		},
	}

	root := cmd.notificationsEntries()
	if !hasEntryValue(root, settingsNotificationsDesktop) {
		t.Fatalf("notifications entries = %#v, want Desktop notifications detail row", root)
	}
	for _, want := range []string{
		settingsNotificationsDelivery,
		settingsNotificationsQueue,
		settingsNotificationsHookOverride,
	} {
		if !hasEntryValue(root, want) {
			t.Fatalf("notifications entries = %#v, want row %q", root, want)
		}
	}
	for _, value := range []string{
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeNone),
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeNotify),
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeRaise),
	} {
		if hasEntryValue(root, value) {
			t.Fatalf("notifications entries = %#v, want no direct desktop notification choice %q", root, value)
		}
	}

	detail := cmd.desktopNotifyEntries()
	for _, want := range []string{
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeNone),
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeNotify),
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeRaise),
	} {
		if !hasEntryValue(detail, want) {
			t.Fatalf("desktop notification entries = %#v, want row %q", detail, want)
		}
	}
	var sawInfo bool
	for _, entry := range detail {
		if strings.Contains(entry.Label, "Desktop notifications") &&
			strings.Contains(entry.Label, "raise") &&
			strings.Contains(entry.Label, "env") {
			sawInfo = true
		}
	}
	if !sawInfo {
		t.Fatalf("desktop notification entries = %#v, want info row with raise + env source", detail)
	}
}

func TestSettingsSessionStateDetailRowsUseEnvAndSnapshotSummary(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	store := sessionstate.NewStore(filepath.Join(xdgState, "projmux", "sessions"))
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "workspace",
		DefaultCWD: "/tmp",
		SavedAt:    time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           0,
			Name:            "main",
			ActivePaneIndex: 0,
			Panes: []sessionstate.Pane{
				{Index: 0, CWD: "/tmp", Recipe: sessionstate.ShellRecipe()},
				{Index: 1, CWD: "/tmp", Recipe: sessionstate.ShellRecipe()},
			},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return xdgState
			case "PROJMUX_SESSION":
				return "workspace"
			case sessionStateAutosaveEnv:
				return "off"
			default:
				return ""
			}
		},
	}

	entries := cmd.sessionStateEntries()
	for _, want := range []string{
		"Auto-save",
		"off",
		sessionStateAutosaveEnv + " env",
		"Startup picker",
		"on",
		"default",
		"Snapshot session",
		"workspace",
		"Snapshot source",
		"autosave",
		"Saved snapshot",
		"2026-05-12 03:04:05 UTC",
		"Preview",
		"window 0",
		"main",
		"pane 0.0",
		"shell",
		"Windows",
		"1",
		"Panes",
		"2",
	} {
		if !hasEntryLabelContaining(entries, want) {
			t.Fatalf("session state entries = %#v, want label containing %q", entries, want)
		}
	}
	if !hasEntryValue(entries, settingsSessionStateDelete) {
		t.Fatalf("session state entries = %#v, want delete action", entries)
	}
	for _, want := range []string{
		settingsActionPrefixSessionState + "autosave:on",
		settingsActionPrefixSessionState + "autosave:off",
		settingsActionPrefixSessionState + "autorestore:on",
		settingsActionPrefixSessionState + "autorestore:off",
	} {
		if !hasEntryValue(entries, want) {
			t.Fatalf("session state entries = %#v, want %q", entries, want)
		}
	}
}

func TestSettingsProjectSessionStateUsesDerivedProjectIdentity(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	project := filepath.Join(home, "source", "repos", "projmux")
	store := sessionstate.NewStore(filepath.Join(xdgState, "projmux", "sessions"))
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "repos-projmux",
		Source:     sessionstate.SourceFresh,
		DefaultCWD: project,
		SavedAt:    time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           1,
			Name:            "dev",
			ActivePaneIndex: 0,
			Panes: []sessionstate.Pane{{
				Index:  0,
				Title:  "editor",
				CWD:    project,
				Recipe: sessionstate.AgentRecipeWithResumeMetadata("codex", "codex-session", "topic", "session-id", "2026-05-12T03:04:05Z"),
			}},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		tmuxRunner: &recordingTmuxRunner{
			outputs: map[string]string{
				strings.Join([]string{"tmux", "has-session", "-t", "repos-projmux"}, "\x00"): "",
			},
		},
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return xdgState
			case "PROJMUX_CWD":
				return project
			case "PROJMUX_SESSION":
				return "live-session"
			default:
				return ""
			}
		},
	}

	options, err := cmd.sectionOptions(settingsSectionProjectSessionState)
	if err != nil {
		t.Fatalf("sectionOptions() error = %v", err)
	}
	if got, want := options.UI, "settings-project-sessionstate"; got != want {
		t.Fatalf("project session state UI = %q, want %q", got, want)
	}
	if got, want := options.Prompt, "Settings > Project > Session State > "; got != want {
		t.Fatalf("project session state prompt = %q, want %q", got, want)
	}
	if !strings.Contains(options.Title, "restore state saved") {
		t.Fatalf("project session state title = %q, want saved restore state", options.Title)
	}
	for _, want := range []string{
		"Project",
		"projmux",
		"Project path",
		project,
		"PROJMUX_CWD env",
		"Session identity",
		"repos-projmux",
		"Snapshot session",
		"repos-projmux",
		"Snapshot source",
		sessionstate.SourceFresh,
		"Preview",
		"window 1",
		"dev",
		"Window",
		"pane 1.0",
		"editor",
		"Pane cwd",
		project,
		"Pane recipe",
		"agent codex",
		"topic topic",
		"resume available",
		"source session-id",
		"status available",
		"confidence high",
		"Save snapshot",
		"capture live project session",
		"Preview restore",
		"dry-run only",
		"Delete snapshot",
		"Windows",
		"1",
		"Panes",
		"1",
	} {
		if !hasEntryLabelContaining(options.Entries, want) {
			t.Fatalf("project session state entries = %#v, want label containing %q", options.Entries, want)
		}
	}
	if hasEntryLabelContaining(options.Entries, "live-session") {
		t.Fatalf("project session state entries = %#v, want derived identity instead of live tmux session", options.Entries)
	}
	for _, want := range []string{settingsProjectSessionStateSave, settingsProjectSessionStatePreview, settingsProjectSessionStateDelete} {
		if !hasEntryValue(options.Entries, want) {
			t.Fatalf("project session state entries = %#v, want project action %q", options.Entries, want)
		}
	}
}

func TestSettingsProjectSessionStateShowsUnavailableMissingAndInvalidStates(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	project := filepath.Join(home, "source", "repos", "projmux")
	baseCmd := func() *settingsCommand {
		return &settingsCommand{
			homeDir:    func() (string, error) { return home, nil },
			tmuxRunner: &recordingTmuxRunner{err: errors.New("can't find session: repos-projmux")},
			lookupEnv: func(name string) string {
				switch name {
				case "XDG_STATE_HOME":
					return xdgState
				case "PROJMUX_CWD":
					return project
				default:
					return ""
				}
			},
		}
	}

	noProject := baseCmd()
	noProject.lookupEnv = func(name string) string {
		if name == "XDG_STATE_HOME" {
			return xdgState
		}
		return ""
	}
	noProjectOptions, err := noProject.sectionOptions(settingsSectionProjectSessionState)
	if err != nil {
		t.Fatalf("sectionOptions(no project) error = %v", err)
	}
	if !hasEntryLabelContaining(noProjectOptions.Entries, "Project") || !hasEntryLabelContaining(noProjectOptions.Entries, "no project context") {
		t.Fatalf("no project entries = %#v, want unavailable project context", noProjectOptions.Entries)
	}

	missingOptions, err := baseCmd().sectionOptions(settingsSectionProjectSessionState)
	if err != nil {
		t.Fatalf("sectionOptions(missing) error = %v", err)
	}
	for _, want := range []string{"Snapshot", "missing", "Save snapshot", "unavailable - live project session not found", "Preview restore", "unavailable without a valid snapshot", "Delete snapshot"} {
		if !hasEntryLabelContaining(missingOptions.Entries, want) {
			t.Fatalf("missing snapshot entries = %#v, want %q", missingOptions.Entries, want)
		}
	}
	if hasEntryValue(missingOptions.Entries, settingsProjectSessionStatePreview) || hasEntryValue(missingOptions.Entries, settingsProjectSessionStateDelete) {
		t.Fatalf("missing snapshot entries = %#v, want preview/delete disabled", missingOptions.Entries)
	}

	store := sessionstate.NewStore(filepath.Join(xdgState, "projmux", "sessions"))
	path, err := store.Path("repos-projmux")
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"session":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	invalidOptions, err := baseCmd().sectionOptions(settingsSectionProjectSessionState)
	if err != nil {
		t.Fatalf("sectionOptions(invalid) error = %v", err)
	}
	if !hasEntryLabelContaining(invalidOptions.Entries, "Snapshot") || !hasEntryLabelContaining(invalidOptions.Entries, "invalid") {
		t.Fatalf("invalid snapshot entries = %#v, want invalid snapshot state", invalidOptions.Entries)
	}
}

func TestSettingsSessionStateActionsPersistTogglesAndDeleteSnapshot(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	store := sessionstate.NewStore(filepath.Join(xdgState, "projmux", "sessions"))
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "workspace",
		DefaultCWD: "/tmp",
		SavedAt:    time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           0,
			ActivePaneIndex: 0,
			Panes:           []sessionstate.Pane{{Index: 0, CWD: "/tmp", Recipe: sessionstate.ShellRecipe()}},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return xdgState
			case "PROJMUX_SESSION":
				return "workspace"
			default:
				return ""
			}
		},
	}

	if err := cmd.executeSessionStateAction("autosave:off", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("autosave off error = %v", err)
	}
	if err := cmd.executeSessionStateAction("autorestore:off", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("autorestore off error = %v", err)
	}
	paths, err := config.Homes{HomeDir: home, StateHome: xdgState}.Paths()
	if err != nil {
		t.Fatalf("Paths() error = %v", err)
	}
	if got, err := config.LoadSessionStateToggleFile(paths.SessionStateAutosaveFile()); err != nil || got != config.SessionStateToggleOff {
		t.Fatalf("autosave file = %q, %v; want off, nil", got, err)
	}
	if got, err := config.LoadSessionStateToggleFile(paths.SessionStateAutorestoreFile()); err != nil || got != config.SessionStateToggleOff {
		t.Fatalf("autorestore file = %q, %v; want off, nil", got, err)
	}

	if err := cmd.executeSessionStateAction("delete", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("delete error = %v", err)
	}
	if _, err := store.Load("workspace"); !errors.Is(err, sessionstate.ErrNotFound) {
		t.Fatalf("Load() after delete error = %v, want %v", err, sessionstate.ErrNotFound)
	}
}

func TestSettingsSessionStateDeleteRequiresConfirmation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	store := sessionstate.NewStore(filepath.Join(xdgState, "projmux", "sessions"))
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "workspace",
		DefaultCWD: "/tmp",
		SavedAt:    time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           0,
			ActivePaneIndex: 0,
			Panes:           []sessionstate.Pane{{Index: 0, CWD: "/tmp", Recipe: sessionstate.ShellRecipe()}},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if got, want := options.UI, "settings-sessionstate"; got != want {
				t.Fatalf("session state UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateDelete}, nil
		case 2:
			if got, want := options.UI, "settings-sessionstate-delete-confirm"; got != want {
				t.Fatalf("confirm UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateConfirmNo}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd := &settingsCommand{
		nativePicker: nativePickerFromCompatRunner(runner),
		homeDir:      func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return xdgState
			case "PROJMUX_SESSION":
				return "workspace"
			default:
				return ""
			}
		},
	}

	if err := cmd.runSessionStateSection(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSessionStateSection() error = %v", err)
	}
	if _, err := store.Load("workspace"); err != nil {
		t.Fatalf("Load() after cancelled delete error = %v, want snapshot preserved", err)
	}
}

func TestSettingsSessionStateDeleteConfirmedRemovesSnapshot(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	store := sessionstate.NewStore(filepath.Join(xdgState, "projmux", "sessions"))
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "workspace",
		DefaultCWD: "/tmp",
		SavedAt:    time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           0,
			ActivePaneIndex: 0,
			Panes:           []sessionstate.Pane{{Index: 0, CWD: "/tmp", Recipe: sessionstate.ShellRecipe()}},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateDelete}, nil
		case 2:
			if got, want := options.UI, "settings-sessionstate-delete-confirm"; got != want {
				t.Fatalf("confirm UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateConfirmYes}, nil
		case 3:
			if hasEntryValue(options.Entries, settingsSessionStateDelete) {
				t.Fatalf("session state entries = %#v, want delete disabled after confirmed delete", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd := &settingsCommand{
		nativePicker: nativePickerFromCompatRunner(runner),
		homeDir:      func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return xdgState
			case "PROJMUX_SESSION":
				return "workspace"
			default:
				return ""
			}
		},
	}

	if err := cmd.runSessionStateSection(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSessionStateSection() error = %v", err)
	}
	if _, err := store.Load("workspace"); !errors.Is(err, sessionstate.ErrNotFound) {
		t.Fatalf("Load() after confirmed delete error = %v, want %v", err, sessionstate.ErrNotFound)
	}
}

func TestSettingsProjectSessionStateSaveNowCapturesProjectSession(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	project := filepath.Join(home, "source", "repos", "projmux")
	storeDir := filepath.Join(xdgState, "projmux", "sessions")
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	refreshFormat := strings.Join([]string{
		"#{pane_id}",
		"#{pane_current_path}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_session_id}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_transcript_path}",
	}, "\x1f")
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "has-session", "-t", "repos-projmux"}, "\x00"):                           "",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "repos-projmux", "-F", refreshFormat}, "\x00"): "",
			strings.Join([]string{"tmux", "list-windows", "-t", "repos-projmux", "-F", windowFormat}, "\x00"):      "0\x1fmain\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "repos-projmux", "-F", paneFormat}, "\x00"):    "0\x1f0\x1feditor\x1f1\x1f" + project + "\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
	cmd := &settingsCommand{
		homeDir:    func() (string, error) { return home, nil },
		tmuxRunner: runner,
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return xdgState
			case "PROJMUX_CWD":
				return project
			default:
				return ""
			}
		},
	}

	var stdout bytes.Buffer
	if err := cmd.executeSessionStateAction("project-save", &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("project-save error = %v", err)
	}
	if !strings.Contains(stdout.String(), "saved project session snapshot: repos-projmux") {
		t.Fatalf("stdout = %q, want project save message", stdout.String())
	}
	snap, err := sessionstate.NewStore(storeDir).Load("repos-projmux")
	if err != nil {
		t.Fatalf("Load(project snapshot) error = %v", err)
	}
	if snap.Session != "repos-projmux" || len(snap.Windows) != 1 || snap.Windows[0].Panes[0].Title != "editor" {
		t.Fatalf("snapshot = %#v, want captured project session", snap)
	}
	for _, call := range runner.calls {
		if len(call.args) >= 3 && call.args[0] == "display-message" && call.args[2] == "#{session_name}" {
			t.Fatalf("project save resolved current session unexpectedly: %#v", runner.calls)
		}
	}
}

func TestSettingsProjectSessionStatePreviewAndDeleteAreProjectScopedAndConfirmed(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	project := filepath.Join(home, "source", "repos", "projmux")
	store := sessionstate.NewStore(filepath.Join(xdgState, "projmux", "sessions"))
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "repos-projmux",
		DefaultCWD: project,
		SavedAt:    time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           0,
			Name:            "main",
			Layout:          "layout",
			ActivePaneIndex: 0,
			Panes:           []sessionstate.Pane{{Index: 0, Title: "editor", CWD: project, Recipe: sessionstate.ShellRecipe()}},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	cmd := &settingsCommand{
		homeDir:    func() (string, error) { return home, nil },
		tmuxRunner: &recordingTmuxRunner{},
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return xdgState
			case "PROJMUX_CWD":
				return project
			case "PROJMUX_SESSION":
				return "live-session"
			default:
				return ""
			}
		},
	}

	var preview bytes.Buffer
	if err := cmd.executeSessionStateAction("project-preview", &preview, &bytes.Buffer{}); err != nil {
		t.Fatalf("project-preview error = %v", err)
	}
	if output := preview.String(); !strings.Contains(output, "repos-projmux") || !strings.Contains(output, "Restore Preview") || strings.Contains(output, "live-session") {
		t.Fatalf("preview output = %q, want project-scoped read-only preview", output)
	}

	var calls int
	cmd.nativePicker = nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if got, want := options.UI, "settings-project-sessionstate"; got != want {
				t.Fatalf("project session state UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsProjectSessionStateDelete}, nil
		case 2:
			if got, want := options.UI, "settings-project-sessionstate-delete-confirm"; got != want {
				t.Fatalf("confirm UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateConfirmNo}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	}))
	if err := cmd.runProjectSessionStateSection(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runProjectSessionStateSection(cancel delete) error = %v", err)
	}
	if _, err := store.Load("repos-projmux"); err != nil {
		t.Fatalf("Load() after cancelled project delete error = %v, want snapshot preserved", err)
	}

	calls = 0
	cmd.nativePicker = nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsProjectSessionStateDelete}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateConfirmYes}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	}))
	if err := cmd.runProjectSessionStateSection(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runProjectSessionStateSection(confirm delete) error = %v", err)
	}
	if _, err := store.Load("repos-projmux"); !errors.Is(err, sessionstate.ErrNotFound) {
		t.Fatalf("Load() after confirmed project delete error = %v, want %v", err, sessionstate.ErrNotFound)
	}
}

func TestSettingsSessionStateMissingSnapshotDisablesDelete(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_SESSION" {
				return "workspace"
			}
			return ""
		},
	}

	entries := cmd.sessionStateEntries()
	if !hasEntryLabelContaining(entries, "Snapshot") || !hasEntryLabelContaining(entries, "missing") {
		t.Fatalf("session state entries = %#v, want missing snapshot status", entries)
	}
	if hasEntryValue(entries, settingsSessionStateDelete) {
		t.Fatalf("session state entries = %#v, want delete disabled when missing", entries)
	}
}

func TestSettingsHubRunsProjectPickerActions(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{}
	switcher := testSettingsSwitchCommand(t, store)
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			if calls == 1 {
				if got, want := options.UI, "settings"; got != want {
					t.Fatalf("settings UI = %q, want %q", got, want)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			}
			if calls == 2 {
				if got, want := options.UI, "settings-project-picker"; got != want {
					t.Fatalf("project settings UI = %q, want %q", got, want)
				}
				if !hasEntryValue(options.Entries, settingsBackValue) {
					t.Fatalf("project settings entries = %#v, want back entry", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "add:/home/tester/source/repos/app"}, nil
			}
			return intpickercompat.Result{}, nil
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			if calls == 1 {
				if got, want := options.UI, "settings"; got != want {
					t.Fatalf("settings UI = %q, want %q", got, want)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			}
			if calls == 2 {
				if got, want := options.UI, "settings-project-picker"; got != want {
					t.Fatalf("project settings UI = %q, want %q", got, want)
				}
				if !hasEntryValue(options.Entries, settingsBackValue) {
					t.Fatalf("project settings entries = %#v, want back entry", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "add:/home/tester/source/repos/app"}, nil
			}
			return intpickercompat.Result{}, nil
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := store.addCalls, []string{"/home/tester/source/repos/app"}; !equalStrings(got, want) {
		t.Fatalf("add calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "pinned: /home/tester/source/repos/app\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSettingsHubKeybindingsListsCurrentValues(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.sessionizer-sidebar]\nplain = \"M-a\"\nprefix = \"A\"\n")

	var calls int
	var keybindingOptions intpickercompat.Options
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			keybindingOptions = options
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 3:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := keybindingOptions.UI, "settings-keybindings"; got != want {
		t.Fatalf("keybindings UI = %q, want %q", got, want)
	}
	if !hasEntryValue(keybindingOptions.Entries, settingsBackValue) {
		t.Fatalf("keybindings entries = %#v, want back entry", keybindingOptions.Entries)
	}
	if !hasEntryLabelContaining(keybindingOptions.Entries, "Terminal fallback mappings still require rerunning projmux init") {
		t.Fatalf("keybindings entries = %#v, want terminal fallback note", keybindingOptions.Entries)
	}
	if !hasEntryValue(keybindingOptions.Entries, settingsActionPrefixKeymap+"sessionizer-sidebar") {
		t.Fatalf("keybindings entries = %#v, want sessionizer-sidebar action", keybindingOptions.Entries)
	}
	if !hasEntryLabelContaining(keybindingOptions.Entries, "key M-a (custom)") {
		t.Fatalf("keybindings entries = %#v, want custom plain value", keybindingOptions.Entries)
	}
	if hasEntryLabelContaining(keybindingOptions.Entries, "prefix") {
		t.Fatalf("keybindings entries = %#v, did not want prefix value", keybindingOptions.Entries)
	}
}

func TestSettingsHubKeybindingsCapturePlainWritesKeymapAndSourcesTmux(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var tmuxCalls [][]string
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar:capture"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 6:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux,1,0"
		}
		return ""
	}
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	cmd.probeKeybinding = func(key probeKey, timeout time.Duration) (probeResult, error) {
		return classifyProbeInput(key, []byte("\x1ba")), nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	keymap := readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if !strings.Contains(keymap, "[bindings.sessionizer-sidebar]\nplain = \"M-a\"\n") {
		t.Fatalf("keymap = %q, want custom plain binding", keymap)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	configText := readFile(t, configPath)
	if !strings.Contains(configText, "bind-key -n M-a") {
		t.Fatalf("tmux config = %q, want M-a bind", configText)
	}
	if !reflect.DeepEqual(tmuxCalls, [][]string{{"tmux", "source-file", configPath}}) {
		t.Fatalf("tmux calls = %#v, want source-file app config", tmuxCalls)
	}
	if !strings.Contains(stdout.String(), "reloaded tmux config") {
		t.Fatalf("stdout = %q, want reload message", stdout.String())
	}
}

func TestSettingsHubKeybindingsRejectsUnsafeRawCapture(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar:capture"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 6:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.probeKeybinding = func(key probeKey, timeout time.Duration) (probeResult, error) {
		return classifyProbeInput(key, []byte("\x1b[A")), nil
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "projmux", "keymap.toml")); !os.IsNotExist(err) {
		t.Fatalf("keymap stat error = %v, want missing file after invalid chord", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "projmux", "tmux.conf")); !os.IsNotExist(err) {
		t.Fatalf("tmux config stat error = %v, want missing file after invalid chord", err)
	}
	if got, want := stdout.String(), "not safe to persist"; !strings.Contains(got, want) {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestSettingsHubKeybindingsCaptureTimeoutDoesNotSaveOrReload(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var tmuxCalls [][]string
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar:capture"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 6:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux,1,0"
		}
		return ""
	}
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	cmd.probeKeybinding = func(key probeKey, timeout time.Duration) (probeResult, error) {
		return classifyProbeInput(key, nil), nil
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "projmux", "keymap.toml")); !os.IsNotExist(err) {
		t.Fatalf("keymap stat error = %v, want missing file after typed cancel", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "projmux", "tmux.conf")); !os.IsNotExist(err) {
		t.Fatalf("tmux config stat error = %v, want missing file after typed cancel", err)
	}
	if len(tmuxCalls) != 0 {
		t.Fatalf("tmux calls = %#v, want none after typed cancel", tmuxCalls)
	}
	if got, want := stdout.String(), "no key was captured"; !strings.Contains(got, want) {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestSettingsHubKeybindingsDisablePlainSavesWithoutLiveTmux(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var tmuxCalls [][]string
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar:disable"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 6:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	keymap := readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if !strings.Contains(keymap, "[bindings.sessionizer-sidebar]\nplain = \"\"\n") {
		t.Fatalf("keymap = %q, want disabled plain key", keymap)
	}
	if len(tmuxCalls) != 0 {
		t.Fatalf("tmux calls = %#v, want none outside TMUX", tmuxCalls)
	}
	if !strings.Contains(stdout.String(), "no live tmux reload outside TMUX") {
		t.Fatalf("stdout = %q, want no-live-tmux message", stdout.String())
	}
}

func TestSettingsHubKeybindingsResetRemovesOverride(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.sessionizer-sidebar]\nplain = \"M-a\"\n")

	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "sessionizer-sidebar:reset"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 6:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	keymap := readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if strings.Contains(keymap, "[bindings.sessionizer-sidebar]") || strings.Contains(keymap, "plain =") {
		t.Fatalf("keymap = %q, want override removed", keymap)
	}
}

func TestSettingsHubKeybindingsInvalidKeymapShowsErrorRow(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.sessionizer-sidebar]\nplain = \"M-a\" # ok\nplain = \"M-b\"\n")

	var calls int
	var keybindingOptions intpickercompat.Options
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			keybindingOptions = options
			return intpickercompat.Result{Key: "enter", Value: settingsNoopValue}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 4:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !hasEntryLabelContaining(keybindingOptions.Entries, "Keymap error") {
		t.Fatalf("keybindings entries = %#v, want parse error row", keybindingOptions.Entries)
	}
	if !hasEntryLabelContaining(keybindingOptions.Entries, "duplicate") {
		t.Fatalf("keybindings entries = %#v, want duplicate parse error", keybindingOptions.Entries)
	}
	if hasEntryValue(keybindingOptions.Entries, settingsActionPrefixKeymap+"sessionizer-sidebar") {
		t.Fatalf("keybindings entries = %#v, want no editable action rows when parse failed", keybindingOptions.Entries)
	}
}

func TestSettingsKeybindingsDiagnosticListsActions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.sessionizer-sidebar]\nplain = \"M-a\"\n")
	var calls int
	var labsOptions, listOptions intpickercompat.Options
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionLabs}, nil
		case 2:
			labsOptions = options
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsKeybindingsDiagnostic}, nil
		case 5:
			listOptions = options
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 6:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.lookupEnv = func(name string) string {
		if name == "TERM_PROGRAM" {
			return "ghostty"
		}
		return ""
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := labsOptions.UI, "settings-labs"; got != want {
		t.Fatalf("labs UI = %q, want %q", got, want)
	}
	if hasEntryValue(labsOptions.Entries, settingsLabKeybindings) {
		t.Fatalf("labs entries = %#v, did not want visible keybinding row", labsOptions.Entries)
	}
	if got, want := listOptions.UI, "settings-keybindings"; got != want {
		t.Fatalf("keybindings UI = %q, want %q", got, want)
	}
	if !hasEntryLabelContaining(listOptions.Entries, "Ghostty") {
		t.Fatalf("keybinding lab entries = %#v, want detected terminal", listOptions.Entries)
	}
	if !hasEntryValue(listOptions.Entries, settingsActionPrefixLabKeymap+"sessionizer-sidebar") {
		t.Fatalf("keybinding lab entries = %#v, want sessionizer-sidebar action", listOptions.Entries)
	}
	if !hasEntryLabelContaining(listOptions.Entries, "Alt-1") {
		t.Fatalf("keybinding lab entries = %#v, want Alt-1 probe label", listOptions.Entries)
	}
	if !hasEntryLabelContaining(listOptions.Entries, "plain M-a (custom)") {
		t.Fatalf("keybinding lab entries = %#v, want custom plain summary", listOptions.Entries)
	}
}

func TestSettingsKeybindingsHideLegacyModeChips(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	options := cmd.keybindingsOptions(settingsKeybindingsProbe)

	if got := options.TitleChips; len(got) != 0 {
		t.Fatalf("TitleChips = %#v, want hidden legacy chips", got)
	}
}

func TestSettingsLabsKeybindingDetailShowsProbeOutcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		seed       string
		result     probeResult
		wantLabel  []string
		wantAbsent []string
	}{
		{
			name: "plain",
			seed: "[bindings.sessionizer-sidebar]\nplain = \"\"\n",
			result: classifyProbeInput(
				probeKey{ActionID: "sessionizer-sidebar", Label: "Alt-1", Plain: "\x1b1", CSIu: "\x1b[9005u", UserKey: "User4"},
				[]byte("\x1b1"),
			),
			wantLabel: []string{"Plain key reached", "Save plain tmux binding"},
		},
		{
			name: "csi-u",
			result: classifyProbeInput(
				probeKey{ActionID: "sessionizer-sidebar", Label: "Alt-1", Plain: "\x1b1", CSIu: "\x1b[9005u", UserKey: "User4"},
				[]byte("\x1b[9005u"),
			),
			wantLabel: []string{"CSI-u reached", "already routed through terminal fallback"},
		},
		{
			name: "unknown",
			result: classifyProbeInput(
				probeKey{ActionID: "sessionizer-sidebar", Label: "Alt-1", Plain: "\x1b1", CSIu: "\x1b[9005u", UserKey: "User4"},
				[]byte("\x1b[A"),
			),
			wantLabel:  []string{"Unexpected sequence", "no keymap overwrite"},
			wantAbsent: []string{"Save as plain override"},
		},
		{
			name: "timeout",
			result: classifyProbeInput(
				probeKey{ActionID: "sessionizer-sidebar", Label: "Alt-1", Plain: "\x1b1", CSIu: "\x1b[9005u", UserKey: "User4"},
				nil,
			),
			wantLabel: []string{"Timeout or swallowed", "projmux init ghostty --apply"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			if tc.seed != "" {
				writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), tc.seed)
			}
			cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
				return intpickercompat.Result{}, nil
			})
			cmd.lookupEnv = func(name string) string {
				if name == "TERM_PROGRAM" {
					return "ghostty"
				}
				return ""
			}
			cmd.lastLabProbe = map[string]probeResult{"sessionizer-sidebar": tc.result}

			entries, title, err := cmd.labKeybindingDetailEntries("sessionizer-sidebar")
			if err != nil {
				t.Fatalf("labKeybindingDetailEntries() error = %v", err)
			}
			if !strings.Contains(title, "Project sidebar") {
				t.Fatalf("title = %q, want action description", title)
			}
			for _, want := range tc.wantLabel {
				if !hasEntryLabelContaining(entries, want) {
					t.Fatalf("entries = %#v, want label containing %q", entries, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if hasEntryLabelContaining(entries, absent) {
					t.Fatalf("entries = %#v, did not want label containing %q", entries, absent)
				}
			}
		})
	}
}

func TestSettingsLabsUnknownProbeSaveOverrideUsesSuggestedPlainChord(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var tmuxCalls [][]string
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsKeybindingsDiagnostic}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar:probe"}, nil
		case 5:
			if !hasEntryLabelContaining(options.Entries, "Unexpected sequence") {
				t.Fatalf("detail entries = %#v, want unexpected sequence row", options.Entries)
			}
			if !hasEntryLabelContaining(options.Entries, "Save as plain override") {
				t.Fatalf("detail entries = %#v, want save override row", options.Entries)
			}
			if !hasEntryLabelContaining(options.Entries, "plain = M-a") {
				t.Fatalf("detail entries = %#v, want suggested M-a description", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar:save-plain-override:M-a"}, nil
		case 6:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 7:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 8:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/tmux,1,0"
		case "TERM_PROGRAM":
			return "ghostty"
		default:
			return ""
		}
	}
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	cmd.probeKeybinding = func(key probeKey, timeout time.Duration) (probeResult, error) {
		return classifyProbeInput(key, []byte("\x1ba")), nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	keymap := readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if !strings.Contains(keymap, "[bindings.sessionizer-sidebar]\nplain = \"M-a\"\n") {
		t.Fatalf("keymap = %q, want M-a plain override", keymap)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	if !reflect.DeepEqual(tmuxCalls, [][]string{{"tmux", "source-file", configPath}}) {
		t.Fatalf("tmux calls = %#v, want source-file app config", tmuxCalls)
	}
	if !strings.Contains(stdout.String(), "reloaded tmux config") {
		t.Fatalf("stdout = %q, want reload message", stdout.String())
	}
}

func TestSettingsLabsPlainProbeSaveUsesKeymapApplyPath(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.sessionizer-sidebar]\nplain = \"\"\n")

	var tmuxCalls [][]string
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsKeybindingsDiagnostic}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar"}, nil
		case 4:
			if got, want := options.UI, "settings-lab-keybinding-detail"; got != want {
				t.Fatalf("detail UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar:probe"}, nil
		case 5:
			if !hasEntryLabelContaining(options.Entries, "Save plain tmux binding") {
				t.Fatalf("detail entries = %#v, want save row after plain probe", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar:save-plain"}, nil
		case 6:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 7:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 8:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/tmux,1,0"
		case "TERM_PROGRAM":
			return "ghostty"
		default:
			return ""
		}
	}
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	cmd.probeKeybinding = func(key probeKey, timeout time.Duration) (probeResult, error) {
		return classifyProbeInput(key, []byte(key.Plain)), nil
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	keymap := readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if strings.Contains(keymap, "[bindings.sessionizer-sidebar]") || strings.Contains(keymap, "plain =") {
		t.Fatalf("keymap = %q, want plain override reset", keymap)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	if !reflect.DeepEqual(tmuxCalls, [][]string{{"tmux", "source-file", configPath}}) {
		t.Fatalf("tmux calls = %#v, want source-file app config", tmuxCalls)
	}
	if !strings.Contains(stdout.String(), "reloaded tmux config") {
		t.Fatalf("stdout = %q, want reload message", stdout.String())
	}
}

func TestSettingsLabsTerminalReloadCapabilityRows(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{
			name: "ghostty reload",
			env:  map[string]string{"TERM_PROGRAM": "ghostty"},
			want: []string{"After fallback apply", "ghostty reload-config", "reload Ghostty config"},
		},
		{
			name: "wsl windows terminal restart",
			env:  map[string]string{"WSL_DISTRO_NAME": "Ubuntu"},
			want: []string{"After fallback apply", "restart terminal", "restart Windows Terminal tabs"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
				return intpickercompat.Result{}, nil
			})
			cmd.lookupEnv = func(name string) string {
				return tc.env[name]
			}

			entries, _, err := cmd.labKeybindingDetailEntries("sessionizer-sidebar")
			if err != nil {
				t.Fatalf("labKeybindingDetailEntries() error = %v", err)
			}
			for _, want := range tc.want {
				if !hasEntryLabelContaining(entries, want) {
					t.Fatalf("entries = %#v, want label containing %q", entries, want)
				}
			}
		})
	}
}

func TestSettingsLabsInitPreviewApplyDelegatesToInitEngine(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	var ran [][]string
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsKeybindingsDiagnostic}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar"}, nil
		case 4:
			if !hasEntryLabelContaining(options.Entries, "Preview terminal fallback") {
				t.Fatalf("detail entries = %#v, want preview row", options.Entries)
			}
			if !hasEntryLabelContaining(options.Entries, "Apply terminal fallback") {
				t.Fatalf("detail entries = %#v, want apply row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar:init-preview"}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar:init-apply"}, nil
		case 6:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 7:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 8:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.lookupEnv = func(name string) string {
		if name == "TERM_PROGRAM" {
			return "ghostty"
		}
		return ""
	}
	cmd.runInitKeybindings = func(args []string, stdout, stderr io.Writer) error {
		ran = append(ran, append([]string(nil), args...))
		return nil
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(ran, [][]string{
		{"ghostty", "--dry-run"},
		{"ghostty", "--apply"},
	}) {
		t.Fatalf("init calls = %#v", ran)
	}
}

func TestSettingsLabsWSLEnvDelegatesWindowsTerminalFallback(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	var ran [][]string
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsKeybindingsDiagnostic}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar"}, nil
		case 4:
			if !hasEntryLabelContaining(options.Entries, "Windows Terminal") {
				t.Fatalf("detail entries = %#v, want Windows Terminal fallback", options.Entries)
			}
			if !hasEntryLabelContaining(options.Entries, "projmux init windows-terminal") {
				t.Fatalf("detail entries = %#v, want windows-terminal init command", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar:init-preview"}, nil
		case 5:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixLabKeymap + "sessionizer-sidebar:init-apply"}, nil
		case 6:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 7:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 8:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})
	cmd.lookupEnv = func(name string) string {
		if name == "WSL_DISTRO_NAME" {
			return "Ubuntu"
		}
		return ""
	}
	cmd.runInitKeybindings = func(args []string, stdout, stderr io.Writer) error {
		ran = append(ran, append([]string(nil), args...))
		return nil
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(ran, [][]string{
		{"windows-terminal", "--dry-run"},
		{"windows-terminal", "--apply"},
	}) {
		t.Fatalf("init calls = %#v", ran)
	}
}

func TestSettingsHubShowsAboutSection(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	update, cacheDir := testUpdateCommand(t, now)
	latest := testVersionTag(t, 1)
	update.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "go"
		}
		return ""
	}
	writeUpdateCacheFixture(t, cacheDir, updateCache{
		Version:   1,
		CheckedAt: now.Add(-time.Hour),
		TagName:   latest,
		HTMLURL:   "https://github.com/crevissepartners/projmux/releases/tag/" + latest,
	})

	var calls int
	var aboutOptions intpickercompat.Options
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		update:   update,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}, nil
			case 2:
				aboutOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsNoopValue}, nil
			case 3:
				if got, want := options.UI, "settings-about"; got != want {
					t.Fatalf("settings about UI after noop = %q, want %q", got, want)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 4:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}, nil
			case 2:
				aboutOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsNoopValue}, nil
			case 3:
				if got, want := options.UI, "settings-about"; got != want {
					t.Fatalf("settings about UI after noop = %q, want %q", got, want)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 4:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := aboutOptions.UI, "settings-about"; got != want {
		t.Fatalf("settings about UI = %q, want %q", got, want)
	}
	if got, want := aboutOptions.Title, "About - Version, updates, key setup"; got != want {
		t.Fatalf("settings about title = %q, want %q", got, want)
	}
	if got := aboutOptions.Header; got != "" {
		t.Fatalf("settings about header = %q, want description only in title", got)
	}
	if got, want := aboutOptions.Prompt, "Settings > About > "; got != want {
		t.Fatalf("settings about prompt = %q, want %q", got, want)
	}
	if got, want := aboutOptions.Footer, "Enter: action  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"; got != want {
		t.Fatalf("settings about footer = %q, want %q", got, want)
	}
	if !hasEntryValue(aboutOptions.Entries, settingsBackValue) {
		t.Fatalf("settings about entries = %#v, want back entry", aboutOptions.Entries)
	}
	if !hasEntryValue(aboutOptions.Entries, settingsUpdateCheck) {
		t.Fatalf("settings about entries = %#v, want update check action", aboutOptions.Entries)
	}
	if !hasEntryValue(aboutOptions.Entries, settingsUpdateApply) {
		t.Fatalf("settings about entries = %#v, want update apply action", aboutOptions.Entries)
	}
	for _, want := range []string{
		"projmux " + version.String(),
		"https://github.com/crevissepartners/projmux",
		"Update Now",
		"Check Updates",
		latest,
		"update_available",
		"Installer",
		"Installed with Go tooling",
		"https://github.com/crevissepartners/projmux/releases/tag/" + latest,
		"sidebar, sessions, projects",
		"new window, rename window/pane",
		"Alt-1..5 work zero-config",
		"projmux setup reports swallowed shortcuts",
		"projmux init applies supported terminal key mappings",
		"projmux doctor checks tmux",
		"Ctrl-M sends 9011u",
		"bind alt/ctrl keys",
		"tmux/meta sequences",
		"docs/keybindings.md",
	} {
		if !hasEntryLabelContaining(aboutOptions.Entries, want) {
			t.Fatalf("settings about entries = %#v, want label containing %q", aboutOptions.Entries, want)
		}
	}
}

func TestSettingsHubRunsUpdateApplyAction(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	update, _ := testUpdateCommand(t, now)
	update.getenv = func(name string) string {
		if name == "PROJMUX_INSTALLER" {
			return "npm"
		}
		return ""
	}
	var ran []string
	update.runExternal = func(name string, args []string, stdout, stderr io.Writer) error {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil
	}

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		update:   update,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}, nil
			case 2:
				if !hasEntryValue(options.Entries, settingsUpdateApply) {
					t.Fatalf("settings about entries = %#v, want update apply action", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsUpdateApply}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 4:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}, nil
			case 2:
				if !hasEntryValue(options.Entries, settingsUpdateApply) {
					t.Fatalf("settings about entries = %#v, want update apply action", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsUpdateApply}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 4:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"npm update -g projmux", "projmux tmux apply"}
	if !equalStrings(ran, want) {
		t.Fatalf("ran = %#v, want %#v", ran, want)
	}
}

func TestSettingsHubRunsUpdateCheckAction(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	update, _ := testUpdateCommand(t, now)
	latest := testVersionTag(t, 2)
	update.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"tag_name":%q,"name":%q,"html_url":"https://github.com/crevissepartners/projmux/releases/tag/%s","published_at":"2026-05-06T10:00:00Z"}`, latest, latest, latest)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	var calls int
	var refreshedAbout intpickercompat.Options
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		update:   update,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}, nil
			case 2:
				if !hasEntryValue(options.Entries, settingsUpdateCheck) {
					t.Fatalf("settings about entries = %#v, want update check action", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsUpdateCheck}, nil
			case 3:
				refreshedAbout = options
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 4:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}, nil
			case 2:
				if !hasEntryValue(options.Entries, settingsUpdateCheck) {
					t.Fatalf("settings about entries = %#v, want update check action", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsUpdateCheck}, nil
			case 3:
				refreshedAbout = options
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 4:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if want := "latest: " + latest; !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if !hasEntryLabelContaining(refreshedAbout.Entries, latest) {
		t.Fatalf("refreshed about entries = %#v, want latest %s", refreshedAbout.Entries, latest)
	}
}

func TestSettingsHubAddProjectScansFilesystem(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	mkdirAll(t, filepath.Join(home, "source", "repos", "app"))
	mkdirAll(t, filepath.Join(home, "work", "service", "nested"))
	mkdirAll(t, filepath.Join(home, ".config"))
	mkdirAll(t, filepath.Join(home, ".cache"))

	store := &stubSwitchPinStore{}
	switcher := testSettingsSwitchCommandWithHome(t, home, store)
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				if hasEntryValue(options.Entries, settingsProjectAdd) {
					t.Fatalf("project settings entries = %#v, want Add Project moved out of root", options.Entries)
				}
				if !hasEntryValue(options.Entries, settingsProjectPins) {
					t.Fatalf("project settings entries = %#v, want Pinned Projects", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjectPins}, nil
			case 3:
				if got, want := options.UI, "settings-project-pins"; got != want {
					t.Fatalf("pinned projects UI = %q, want %q", got, want)
				}
				if got := entryIndexValue(options.Entries, settingsProjectAdd); got != 1 {
					t.Fatalf("pinned project entries = %#v, want Add Project at index 1, got %d", options.Entries, got)
				}
				if got := entryIndexLabelContaining(options.Entries, "Add Current Project"); got != 2 {
					t.Fatalf("pinned project entries = %#v, want Add Current Project at index 2, got %d", options.Entries, got)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjectAdd}, nil
			case 4:
				if got, want := options.UI, "settings-project-add"; got != want {
					t.Fatalf("add project UI = %q, want %q", got, want)
				}
				app := filepath.Join(home, "source", "repos", "app")
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+app) {
					t.Fatalf("add project entries = %#v, want scanned app", options.Entries)
				}
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+filepath.Join(home, ".config")) {
					t.Fatalf("add project entries = %#v, want hidden whitelist entry", options.Entries)
				}
				if hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+filepath.Join(home, ".cache")) {
					t.Fatalf("add project entries = %#v, want hidden non-whitelist skipped", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "add:" + app}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				if hasEntryValue(options.Entries, settingsProjectAdd) {
					t.Fatalf("project settings entries = %#v, want Add Project moved out of root", options.Entries)
				}
				if !hasEntryValue(options.Entries, settingsProjectPins) {
					t.Fatalf("project settings entries = %#v, want Pinned Projects", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjectPins}, nil
			case 3:
				if got, want := options.UI, "settings-project-pins"; got != want {
					t.Fatalf("pinned projects UI = %q, want %q", got, want)
				}
				if got := entryIndexValue(options.Entries, settingsProjectAdd); got != 1 {
					t.Fatalf("pinned project entries = %#v, want Add Project at index 1, got %d", options.Entries, got)
				}
				if got := entryIndexLabelContaining(options.Entries, "Add Current Project"); got != 2 {
					t.Fatalf("pinned project entries = %#v, want Add Current Project at index 2, got %d", options.Entries, got)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjectAdd}, nil
			case 4:
				if got, want := options.UI, "settings-project-add"; got != want {
					t.Fatalf("add project UI = %q, want %q", got, want)
				}
				app := filepath.Join(home, "source", "repos", "app")
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+app) {
					t.Fatalf("add project entries = %#v, want scanned app", options.Entries)
				}
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+filepath.Join(home, ".config")) {
					t.Fatalf("add project entries = %#v, want hidden whitelist entry", options.Entries)
				}
				if hasEntryValue(options.Entries, settingsActionPrefixSwitch+"add:"+filepath.Join(home, ".cache")) {
					t.Fatalf("add project entries = %#v, want hidden non-whitelist skipped", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "add:" + app}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := store.addCalls, []string{filepath.Join(home, "source", "repos", "app")}; !equalStrings(got, want) {
		t.Fatalf("add calls = %q, want %q", got, want)
	}
}

func TestSettingsHubPinnedProjectsRemovesPins(t *testing.T) {
	t.Parallel()

	pin := "/home/tester/source/repos/app"
	store := &stubSwitchPinStore{list: []string{pin}}
	switcher := testSettingsSwitchCommand(t, store)
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsProjectPins}, nil
			case 3:
				if got, want := options.UI, "settings-project-pins"; got != want {
					t.Fatalf("pinned projects UI = %q, want %q", got, want)
				}
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"clear") {
					t.Fatalf("pinned project entries = %#v, want clear", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "pin:" + pin}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsProjectPins}, nil
			case 3:
				if got, want := options.UI, "settings-project-pins"; got != want {
					t.Fatalf("pinned projects UI = %q, want %q", got, want)
				}
				if !hasEntryValue(options.Entries, settingsActionPrefixSwitch+"clear") {
					t.Fatalf("pinned project entries = %#v, want clear", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "pin:" + pin}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := store.toggleCalls, []string{pin}; !equalStrings(got, want) {
		t.Fatalf("toggle calls = %q, want %q", got, want)
	}
}

func TestProjectPickerEntriesIncludesWorkdirsRows(t *testing.T) {
	t.Parallel()

	const home = "/home/tester"
	cmd := &settingsCommand{
		switcher: &switchCommand{
			homeDir:      func() (string, error) { return home, nil },
			lookupEnv:    func(string) string { return "" },
			tmuxProjdir:  emptyTmuxOption,
			loadProjdir:  func(string) (string, error) { return "", nil },
			saveProjdir:  func(string, string) error { return nil },
			loadWorkdirs: func(string) ([]string, error) { return nil, nil },
		},
	}

	entries := cmd.projectPickerEntries()
	if hasEntryValue(entries, settingsWorkdirAdd) {
		t.Fatalf("project picker entries = %#v, want Add Workdir moved out of root", entries)
	}
	if !hasEntryValue(entries, settingsWorkdirList) {
		t.Fatalf("project picker entries = %#v, want Workdirs entry", entries)
	}
	if hasEntryLabelContaining(entries, "Add Workdir...") {
		t.Fatalf("project picker entries = %#v, want no root-level 'Add Workdir...' label", entries)
	}
	if hasEntryLabelContaining(entries, "Add Current Project") {
		t.Fatalf("project picker entries = %#v, want no root-level 'Add Current Project' label", entries)
	}
	if !hasEntryLabelContaining(entries, "Workdirs") {
		t.Fatalf("project picker entries = %#v, want 'Workdirs' label", entries)
	}
}

func TestSettingsHubAddWorkdirAppendsToSavedFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	mkdirAll(t, filepath.Join(home, "source", "repos", "app"))

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				if hasEntryValue(options.Entries, settingsWorkdirAdd) {
					t.Fatalf("project settings entries = %#v, want Add Workdir moved out of root", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				if got, want := options.UI, "settings-workdirs"; got != want {
					t.Fatalf("workdirs list UI = %q, want %q", got, want)
				}
				if got := entryIndexValue(options.Entries, settingsWorkdirAdd); got < 0 {
					t.Fatalf("workdirs list entries = %#v, want Add Workdir row", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 4:
				if got, want := options.UI, "settings-workdir-add"; got != want {
					t.Fatalf("add workdir UI = %q, want %q", got, want)
				}
				app := filepath.Join(home, "source", "repos", "app")
				want := settingsActionPrefixWorkdir + "add:" + app
				if !hasEntryValue(options.Entries, want) {
					t.Fatalf("add workdir entries = %#v, want value %q", options.Entries, want)
				}
				return intpickercompat.Result{Key: "enter", Value: want}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				if hasEntryValue(options.Entries, settingsWorkdirAdd) {
					t.Fatalf("project settings entries = %#v, want Add Workdir moved out of root", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				if got, want := options.UI, "settings-workdirs"; got != want {
					t.Fatalf("workdirs list UI = %q, want %q", got, want)
				}
				if got := entryIndexValue(options.Entries, settingsWorkdirAdd); got < 0 {
					t.Fatalf("workdirs list entries = %#v, want Add Workdir row", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 4:
				if got, want := options.UI, "settings-workdir-add"; got != want {
					t.Fatalf("add workdir UI = %q, want %q", got, want)
				}
				app := filepath.Join(home, "source", "repos", "app")
				want := settingsActionPrefixWorkdir + "add:" + app
				if !hasEntryValue(options.Entries, want) {
					t.Fatalf("add workdir entries = %#v, want value %q", options.Entries, want)
				}
				return intpickercompat.Result{Key: "enter", Value: want}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	saved, err := readWorkdirsFile(t, home)
	if err != nil {
		t.Fatalf("readWorkdirsFile() error = %v", err)
	}
	app := filepath.Join(home, "source", "repos", "app")
	if !equalStrings(saved, []string{app}) {
		t.Fatalf("saved workdirs = %#v, want [%q]", saved, app)
	}
	if got, want := stdout.String(), "added workdir: "+app+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSettingsHubWorkdirsListRemovesSavedEntry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	target := filepath.Join(home, "source", "repos", "app")
	if err := os.MkdirAll(filepath.Join(home, ".config", "projmux"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "projmux", "workdirs"), []byte(target+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(homeDir string) ([]string, error) {
		// Use the real loader so removal is observed end-to-end via the saved file.
		return loadSavedWorkdirsFromFile(homeDir), nil
	}

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				if got, want := options.UI, "settings-workdirs"; got != want {
					t.Fatalf("workdirs list UI = %q, want %q", got, want)
				}
				want := settingsActionPrefixWorkdir + "remove:" + target
				if !hasEntryValue(options.Entries, want) {
					t.Fatalf("workdirs list entries = %#v, want %q", options.Entries, want)
				}
				return intpickercompat.Result{Key: "enter", Value: want}, nil
			case 4:
				// After remove, list should be empty (just back + placeholder).
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				if got, want := options.UI, "settings-workdirs"; got != want {
					t.Fatalf("workdirs list UI = %q, want %q", got, want)
				}
				want := settingsActionPrefixWorkdir + "remove:" + target
				if !hasEntryValue(options.Entries, want) {
					t.Fatalf("workdirs list entries = %#v, want %q", options.Entries, want)
				}
				return intpickercompat.Result{Key: "enter", Value: want}, nil
			case 4:
				// After remove, list should be empty (just back + placeholder).
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	saved, err := readWorkdirsFile(t, home)
	if err != nil {
		t.Fatalf("readWorkdirsFile() error = %v", err)
	}
	if len(saved) != 0 {
		t.Fatalf("saved workdirs = %#v, want empty", saved)
	}
	if got, want := stdout.String(), "removed workdir: "+target+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestWorkdirListEntriesSurfacesEnvSources(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		switcher: &switchCommand{
			homeDir: func() (string, error) { return "/home/tester", nil },
			lookupEnv: func(name string) string {
				if name == managedRootsEnvVar {
					return "/env/one:/env/two"
				}
				return ""
			},
			tmuxProjdir:  emptyTmuxOption,
			loadProjdir:  func(string) (string, error) { return "", nil },
			saveProjdir:  func(string, string) error { return nil },
			loadWorkdirs: func(string) ([]string, error) { return []string{"/saved/a"}, nil },
		},
	}

	entries, err := cmd.workdirListEntries()
	if err != nil {
		t.Fatalf("workdirListEntries() error = %v", err)
	}
	if got, savedRow := entryIndexValue(entries, settingsWorkdirAdd), entryIndexLabelContaining(entries, "Saved workdirs"); got < 0 || savedRow < 0 || got <= savedRow {
		t.Fatalf("workdir list entries = %#v, want Add Workdir after the saved summary block", entries)
	}
	if !hasEntryLabelContaining(entries, "/saved/a") {
		t.Fatalf("workdir list entries = %#v, want saved entry", entries)
	}
	// The env source row now renders the variable name in the label column
	// and the colon-separated value in the value column, with a "(env, ...)"
	// source annotation. Verify the parts appear; the exact spacing comes
	// from settingsLabelInfo padding.
	if !hasEntryLabelContaining(entries, managedRootsEnvVar) {
		t.Fatalf("workdir list entries = %#v, want env variable name", entries)
	}
	if !hasEntryLabelContaining(entries, "/env/one:/env/two") {
		t.Fatalf("workdir list entries = %#v, want env value", entries)
	}
	if !hasEntryLabelContaining(entries, "(env, read-only)") {
		t.Fatalf("workdir list entries = %#v, want env source annotation", entries)
	}
}

func TestAddWorkdirEntriesIncludesTypedRow(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	mkdirAll(t, filepath.Join(home, "source", "repos", "app"))

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var addOptions intpickercompat.Options
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 4:
				addOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 4:
				addOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := addOptions.UI, "settings-workdir-add"; got != want {
		t.Fatalf("add workdir UI = %q, want %q", got, want)
	}
	if !hasEntryValue(addOptions.Entries, settingsWorkdirTyped) {
		t.Fatalf("add workdir entries = %#v, want typed-entry row", addOptions.Entries)
	}
	if !hasEntryLabelContaining(addOptions.Entries, "Type path manually") {
		t.Fatalf("add workdir entries = %#v, want 'Type path manually' label", addOptions.Entries)
	}
}

func TestSettingsHubAddWorkdirTypedAppendsTypedPath(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	typed := filepath.Join(home, "mnt", "c", "Users", "me", "code")
	mkdirAll(t, typed)

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var typedOptions intpickercompat.Options
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 4:
				if !hasEntryValue(options.Entries, settingsWorkdirTyped) {
					t.Fatalf("add workdir entries = %#v, want typed row", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirTyped}, nil
			case 5:
				typedOptions = options
				return intpickercompat.Result{Key: "enter", Query: typed}, nil
			case 6:
				// After typed flow returns, the workdirs list reopens. Close it.
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 7:
				// After typed flow returns, the project picker reopens. Close it.
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 4:
				if !hasEntryValue(options.Entries, settingsWorkdirTyped) {
					t.Fatalf("add workdir entries = %#v, want typed row", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirTyped}, nil
			case 5:
				typedOptions = options
				return intpickercompat.Result{Key: "enter", Query: typed}, nil
			case 6:
				// After typed flow returns, the workdirs list reopens. Close it.
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 7:
				// After typed flow returns, the project picker reopens. Close it.
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := typedOptions.UI, "settings-workdir-typed"; got != want {
		t.Fatalf("typed picker UI = %q, want %q", got, want)
	}
	if !typedOptions.AcceptQuery {
		t.Fatalf("typed picker AcceptQuery = false, want true")
	}
	if got, want := typedOptions.Prompt, "Type workdir path > "; got != want {
		t.Fatalf("typed picker prompt = %q, want %q", got, want)
	}

	saved, err := readWorkdirsFile(t, home)
	if err != nil {
		t.Fatalf("readWorkdirsFile() error = %v", err)
	}
	if !equalStrings(saved, []string{typed}) {
		t.Fatalf("saved workdirs = %#v, want [%q]", saved, typed)
	}
	if got, want := stdout.String(), "added workdir: "+typed+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSettingsHubAddWorkdirTypedRejectsRelativePath(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 4:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirTyped}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Query: "relative/path"}, nil
			case 6:
				// After typed-flow falls back, settings should return to the
				// workdirs list. Close it.
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 7:
				// After typed-flow falls back, settings should return to the
				// project picker section. Close to terminate the run.
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}, nil
			case 4:
				return intpickercompat.Result{Key: "enter", Value: settingsWorkdirTyped}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Query: "relative/path"}, nil
			case 6:
				// After typed-flow falls back, settings should return to the
				// workdirs list. Close it.
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 7:
				// After typed-flow falls back, settings should return to the
				// project picker section. Close to terminate the run.
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			default:
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "absolute path") {
		t.Fatalf("stderr = %q, want absolute-path error", got)
	}
	saved, err := readWorkdirsFile(t, home)
	if err != nil {
		t.Fatalf("readWorkdirsFile() error = %v", err)
	}
	if len(saved) != 0 {
		t.Fatalf("saved workdirs = %#v, want empty after rejected typed input", saved)
	}
}

func TestSettingsHubBackReturnsToRoot(t *testing.T) {
	t.Parallel()

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAI}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 3:
				if got, want := options.UI, "settings"; got != want {
					t.Fatalf("settings UI after back = %q, want %q", got, want)
				}
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionAI}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 3:
				if got, want := options.UI, "settings"; got != want {
					t.Fatalf("settings UI after back = %q, want %q", got, want)
				}
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		})),
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestSettingsHubRejectsArguments(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	var stderr bytes.Buffer
	err := cmd.Run([]string{"extra"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
	if !strings.Contains(stderr.String(), "projmux settings") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func testSettingsSwitchCommand(t *testing.T, store *stubSwitchPinStore) *switchCommand {
	t.Helper()
	return testSettingsSwitchCommandWithHome(t, "/home/tester", store)
}

func testSettingsSwitchCommandWithHome(t *testing.T, home string, store *stubSwitchPinStore) *switchCommand {
	t.Helper()

	return &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{filepath.Join(home, "source", "repos", "app")}, nil
		},
		pinStore: func() (switchPinStore, error) { return store, nil },
		runner: switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			return intpickercompat.Result{}, nil
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			return intpickercompat.Result{}, nil
		})),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return home, nil },
		workingDir: func() (string, error) { return filepath.Join(home, "source", "repos", "app"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return filepath.Join(home, "source", "repos")
			}
			return ""
		},
	}
}

func TestCurrentProjdirInfoSourcePriority(t *testing.T) {
	t.Parallel()

	const home = "/home/tester"
	tests := []struct {
		name       string
		lookup     func(string) string
		tmuxOption func() string
		load       func(string) (string, error)
		wantValue  string
		wantSource string
	}{
		{
			name: "PROJMUX_PROJDIR env wins",
			lookup: func(name string) string {
				if name == projdirEnvVar {
					return "/from/projdir"
				}
				return ""
			},
			tmuxOption: func() string { return "/from/tmux" },
			load:       func(string) (string, error) { return "/from/saved", nil },
			wantValue:  "/from/projdir",
			wantSource: projdirSourcePROJDIRenv,
		},
		{
			name: "PROJMUX_PROJDIR multi-path uses first entry as primary",
			lookup: func(name string) string {
				if name == projdirEnvVar {
					return "/from/projdir" + string(os.PathListSeparator) + "/extra/one"
				}
				return ""
			},
			tmuxOption: emptyTmuxOption,
			load:       func(string) (string, error) { return "/from/saved", nil },
			wantValue:  "/from/projdir",
			wantSource: projdirSourcePROJDIRenv,
		},
		{
			name:       "tmux option used when PROJMUX_PROJDIR empty",
			lookup:     func(string) string { return "" },
			tmuxOption: func() string { return "/from/tmux" },
			load:       func(string) (string, error) { return "/from/saved", nil },
			wantValue:  "/from/tmux",
			wantSource: projdirSourceTmuxOption,
		},
		{
			name:       "saved file used when env unset",
			lookup:     func(string) string { return "" },
			tmuxOption: emptyTmuxOption,
			load:       func(string) (string, error) { return "/from/saved", nil },
			wantValue:  "/from/saved",
			wantSource: projdirSourceSaved,
		},
		{
			name:       "unresolved when nothing set",
			lookup:     func(string) string { return "" },
			tmuxOption: emptyTmuxOption,
			load:       func(string) (string, error) { return "", nil },
			wantValue:  "",
			wantSource: projdirSourceUnresolved,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			saveCalls := 0
			cmd := &switchCommand{
				homeDir:     func() (string, error) { return home, nil },
				lookupEnv:   tc.lookup,
				tmuxProjdir: tc.tmuxOption,
				loadProjdir: tc.load,
				saveProjdir: func(string, string) error {
					saveCalls++
					return nil
				},
			}

			value, source, err := cmd.currentProjdirInfo()
			if err != nil {
				t.Fatalf("currentProjdirInfo() error = %v", err)
			}
			if value != tc.wantValue {
				t.Fatalf("value = %q, want %q", value, tc.wantValue)
			}
			if source != tc.wantSource {
				t.Fatalf("source = %q, want %q", source, tc.wantSource)
			}
			if saveCalls != 0 {
				t.Fatalf("save calls = %d, want 0 (currentProjdirInfo must not memoize)", saveCalls)
			}
		})
	}
}

func TestProjectRootTypedInitialQueryUsesEffectiveRootOrHome(t *testing.T) {
	t.Parallel()

	const home = "/home/tester"

	tests := []struct {
		name       string
		lookup     func(string) string
		tmuxOption func() string
		load       func(string) (string, error)
		want       string
	}{
		{
			name:       "effective root",
			lookup:     func(string) string { return "" },
			tmuxOption: emptyTmuxOption,
			load:       func(string) (string, error) { return "/from/saved", nil },
			want:       "/from/saved",
		},
		{
			name:       "unconfigured root",
			lookup:     func(string) string { return "" },
			tmuxOption: emptyTmuxOption,
			load:       func(string) (string, error) { return "", nil },
			want:       home,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := &settingsCommand{
				switcher: &switchCommand{
					homeDir:     func() (string, error) { return home, nil },
					lookupEnv:   tc.lookup,
					tmuxProjdir: tc.tmuxOption,
					loadProjdir: tc.load,
				},
			}

			if got := cmd.projectRootTypedInitialQuery(); got != tc.want {
				t.Fatalf("projectRootTypedInitialQuery() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProjectPickerEntriesIncludesProjdirRow(t *testing.T) {
	t.Parallel()

	const home = "/home/tester"
	cmd := &settingsCommand{
		switcher: &switchCommand{
			homeDir: func() (string, error) { return home, nil },
			lookupEnv: func(name string) string {
				if name == projdirEnvVar {
					return "/from/projdir"
				}
				return ""
			},
			tmuxProjdir: emptyTmuxOption,
			loadProjdir: func(string) (string, error) { return "", nil },
			saveProjdir: func(string, string) error { return nil },
		},
	}

	entries := cmd.projectPickerEntries()
	if !hasEntryLabelContaining(entries, "Project Root") {
		t.Fatalf("project picker entries = %#v, want Project Root row", entries)
	}
	if !hasEntryLabelContaining(entries, "/from/projdir") {
		t.Fatalf("project picker entries = %#v, want resolved value in label", entries)
	}
	if !hasEntryLabelContaining(entries, "("+projdirSourcePROJDIRenv+")") {
		t.Fatalf("project picker entries = %#v, want source label", entries)
	}
	if hasEntryLabelContaining(entries, "Set PROJMUX_PROJDIR") {
		t.Fatalf("project picker entries = %#v, want project-root hint moved into submenu", entries)
	}
}

func TestProjectPickerEntriesShowsUnconfiguredProjdir(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		switcher: &switchCommand{
			homeDir:     func() (string, error) { return "/home/tester", nil },
			lookupEnv:   func(string) string { return "" },
			tmuxProjdir: emptyTmuxOption,
			loadProjdir: func(string) (string, error) { return "", nil },
		},
	}

	entries := cmd.projectPickerEntries()
	if !hasEntryLabelContaining(entries, "Project Root") {
		t.Fatalf("project picker entries = %#v, want Project Root row", entries)
	}
	if !hasEntryLabelContaining(entries, "not configured") {
		t.Fatalf("project picker entries = %#v, want not configured label", entries)
	}
}

func TestProjectRootEntriesShowShadowedSavedProjdir(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		switcher: &switchCommand{
			homeDir: func() (string, error) { return "/home/tester", nil },
			lookupEnv: func(name string) string {
				if name == projdirEnvVar {
					return "/from/env"
				}
				return ""
			},
			tmuxProjdir: emptyTmuxOption,
			loadProjdir: func(string) (string, error) { return "/from/saved", nil },
			saveProjdir: func(string, string) error {
				t.Fatalf("project root settings display must not memoize env values")
				return nil
			},
		},
	}

	entries, err := cmd.projectRootEntries()
	if err != nil {
		t.Fatalf("projectRootEntries() error = %v", err)
	}
	if !hasEntryLabelContaining(entries, "Effective Project Root") {
		t.Fatalf("project root entries = %#v, want effective row", entries)
	}
	if !hasEntryLabelContaining(entries, "/from/env") {
		t.Fatalf("project root entries = %#v, want effective env value", entries)
	}
	if !hasEntryLabelContaining(entries, "("+projdirSourcePROJDIRenv+")") {
		t.Fatalf("project root entries = %#v, want env source label", entries)
	}
	if !hasEntryLabelContaining(entries, "Saved Project Root") {
		t.Fatalf("project root entries = %#v, want saved row", entries)
	}
	if !hasEntryLabelContaining(entries, "/from/saved") {
		t.Fatalf("project root entries = %#v, want saved value", entries)
	}
	if !hasEntryLabelContaining(entries, "shadowed by "+projdirSourcePROJDIRenv) {
		t.Fatalf("project root entries = %#v, want shadowed relationship", entries)
	}
	if !hasEntryValue(entries, settingsProjdirSetTyped) {
		t.Fatalf("project root entries = %#v, want typed set action", entries)
	}
	if !hasEntryLabelContaining(entries, "Use Current Project as Root") {
		t.Fatalf("project root entries = %#v, want current project row", entries)
	}
	if !hasEntryValue(entries, settingsProjdirClear) {
		t.Fatalf("project root entries = %#v, want clear action", entries)
	}
	if !hasEntryLabelContaining(entries, "Set PROJMUX_PROJDIR") {
		t.Fatalf("project root entries = %#v, want project-root hint row", entries)
	}
	if got, wantBefore := entryIndexValue(entries, settingsProjdirSetTyped), entryIndexLabelContaining(entries, "Env PROJMUX_PROJDIR"); got < 0 || wantBefore < 0 || got > wantBefore {
		t.Fatalf("project root entries = %#v, want action rows before the explanatory hint rows", entries)
	}
}

func TestSettingsHubSetProjectRootTypedSavesProjdir(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	target := filepath.Join(home, "projects")
	mkdirAll(t, target)

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.lookupEnv = func(string) string { return "" }
	switcher.loadProjdir = config.LoadProjdir
	switcher.saveProjdir = config.SaveProjdir
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var typedOptions intpickercompat.Options
	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				if !hasEntryValue(options.Entries, settingsProjectRootManage) {
					t.Fatalf("project picker entries = %#v, want project root management row", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}, nil
			case 3:
				if got, want := options.UI, "settings-project-root"; got != want {
					t.Fatalf("project root UI = %q, want %q", got, want)
				}
				if got, want := options.Title, "Project Root - Effective and saved root"; got != want {
					t.Fatalf("project root title = %q, want %q", got, want)
				}
				if got := options.Header; got != "" {
					t.Fatalf("project root header = %q, want description only in title", got)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjdirSetTyped}, nil
			case 4:
				typedOptions = options
				return intpickercompat.Result{Key: "enter", Query: target}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 6:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 7:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				if !hasEntryValue(options.Entries, settingsProjectRootManage) {
					t.Fatalf("project picker entries = %#v, want project root management row", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}, nil
			case 3:
				if got, want := options.UI, "settings-project-root"; got != want {
					t.Fatalf("project root UI = %q, want %q", got, want)
				}
				if got, want := options.Title, "Project Root - Effective and saved root"; got != want {
					t.Fatalf("project root title = %q, want %q", got, want)
				}
				if got := options.Header; got != "" {
					t.Fatalf("project root header = %q, want description only in title", got)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjdirSetTyped}, nil
			case 4:
				typedOptions = options
				return intpickercompat.Result{Key: "enter", Query: target}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 6:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 7:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := typedOptions.UI, "settings-project-root-typed"; got != want {
		t.Fatalf("typed project root UI = %q, want %q", got, want)
	}
	if !typedOptions.AcceptQuery {
		t.Fatalf("typed project root AcceptQuery = false, want true")
	}
	if got, want := typedOptions.InitialQuery, home; got != want {
		t.Fatalf("typed project root InitialQuery = %q, want %q", got, want)
	}
	if got, want := readProjdirFile(t, home), target; got != want {
		t.Fatalf("saved project root = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "saved project root: "+target+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSettingsHubUseCurrentProjectAsRootSavesProjdir(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	currentProject := filepath.Join(home, "source", "repos", "app")
	mkdirAll(t, currentProject)

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.lookupEnv = func(string) string { return "" }
	switcher.loadProjdir = config.LoadProjdir
	switcher.saveProjdir = config.SaveProjdir
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}, nil
			case 3:
				if !hasEntryLabelContaining(options.Entries, "Use Current Project as Root") {
					t.Fatalf("project root entries = %#v, want current project action", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjdirSetCurrent}, nil
			case 4:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 6:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}, nil
			case 3:
				if !hasEntryLabelContaining(options.Entries, "Use Current Project as Root") {
					t.Fatalf("project root entries = %#v, want current project action", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjdirSetCurrent}, nil
			case 4:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 6:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := readProjdirFile(t, home), currentProject; got != want {
		t.Fatalf("saved project root = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "saved project root: "+currentProject+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSettingsHubClearProjectRootRemovesSavedProjdir(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	home := t.TempDir()
	if err := config.SaveProjdir(home, "/saved/root"); err != nil {
		t.Fatalf("SaveProjdir() error = %v", err)
	}

	switcher := testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{})
	switcher.lookupEnv = func(string) string { return "" }
	switcher.loadProjdir = config.LoadProjdir
	switcher.saveProjdir = config.SaveProjdir
	switcher.loadWorkdirs = func(string) ([]string, error) { return nil, nil }

	var calls int
	cmd := &settingsCommand{
		ai:       testAICommand(t.TempDir()),
		switcher: switcher,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}, nil
			case 3:
				if !hasEntryLabelContaining(options.Entries, "/saved/root") {
					t.Fatalf("project root entries = %#v, want saved value", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjdirClear}, nil
			case 4:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 6:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		}),
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionProject}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}, nil
			case 3:
				if !hasEntryLabelContaining(options.Entries, "/saved/root") {
					t.Fatalf("project root entries = %#v, want saved value", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: settingsProjdirClear}, nil
			case 4:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 6:
				return intpickercompat.Result{}, nil
			default:
				t.Fatalf("unexpected settings picker call %d", calls)
				return intpickercompat.Result{}, nil
			}
		})),
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := readProjdirFile(t, home); got != "" {
		t.Fatalf("saved project root = %q, want empty", got)
	}
	if got, want := stdout.String(), "cleared saved project root\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func hasEntryValue(entries []intpickercompat.Entry, value string) bool {
	for _, entry := range entries {
		if entry.Value == value {
			return true
		}
	}
	return false
}

func entryIndexValue(entries []intpickercompat.Entry, value string) int {
	for i, entry := range entries {
		if entry.Value == value {
			return i
		}
	}
	return -1
}

func entryValues(entries []intpickercompat.Entry) []string {
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		values = append(values, entry.Value)
	}
	return values
}

func hasEntryLabelContaining(entries []intpickercompat.Entry, value string) bool {
	for _, entry := range entries {
		if strings.Contains(entry.Label, value) {
			return true
		}
	}
	return false
}

func entryIndexLabelContaining(entries []intpickercompat.Entry, value string) int {
	for i, entry := range entries {
		if strings.Contains(entry.Label, value) {
			return i
		}
	}
	return -1
}

func testKeybindingSettingsCommand(t *testing.T, home string, run func(intpickercompat.Options) (intpickercompat.Result, error)) *settingsCommand {
	t.Helper()
	runner := switchRunnerFunc(run)
	return &settingsCommand{
		ai:           testAICommand(home),
		switcher:     testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		homeDir:      func() (string, error) { return home, nil },
		lookupEnv:    func(string) string { return "" },
		runCommand:   func(string, ...string) error { return nil },
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}

func readWorkdirsFile(t *testing.T, home string) ([]string, error) {
	t.Helper()
	path := filepath.Join(home, ".config", "projmux", "workdirs")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

func readProjdirFile(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".config", "projmux", "projdir")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

func loadSavedWorkdirsFromFile(home string) []string {
	path := filepath.Join(home, ".config", "projmux", "workdirs")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := []string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
