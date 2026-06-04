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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
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
		settingsSectionGlobalTheme:   {Name: "Theme", Axis: settingsAxisGlobal},
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
	if got, want := options.Footer, "Open rows or click a scope chip to switch tabs."; got != want {
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
		settingsSectionGlobalTheme,
		settingsSectionSessionState,
		settingsSectionKeybindings,
		settingsSectionLabs,
		settingsSectionAbout,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root settings entry order = %#v, want %#v", got, want)
	}
}

func TestSettingsRootOptionsKoreanCatalogDoesNotOverflow(t *testing.T) {
	t.Setenv("LANG", "ko_KR.UTF-8")

	cmd := &settingsCommand{lookupEnv: os.Getenv}
	options := cmd.rootOptions(settingsRootTabGlobal)

	if got, want := options.Title, "설정"; got != want {
		t.Fatalf("root settings title = %q, want %q", got, want)
	}
	if got, want := options.Prompt, "설정 > "; got != want {
		t.Fatalf("root settings prompt = %q, want %q", got, want)
	}
	wantChips := []projmuxpicker.Chip{
		{Label: "전체", Active: true, ClickValue: settingsRootTabGlobalValue},
		{Label: "프로젝트", Disabled: true, ClickValue: settingsRootTabProjectValue},
	}
	if got := options.TitleChips; !reflect.DeepEqual(got, wantChips) {
		t.Fatalf("root settings title chips = %#v, want %#v", got, wantChips)
	}
	if got, want := options.Footer, "행을 열거나 범위 칩을 클릭해 탭을 전환합니다."; got != want {
		t.Fatalf("root settings footer = %q, want %q", got, want)
	}
	if !hasEntryLabelContaining(options.Entries, "프로젝트 선택기") {
		t.Fatalf("root settings entries = %#v, want Korean Project Picker row", options.Entries)
	}
	for _, entry := range options.Entries {
		if width := i18n.TerminalCellWidth(entry.Label); width > 96 {
			t.Fatalf("Korean root row width = %d, want <= 96: %q", width, entry.Label)
		}
	}
}

func TestSettingsRootOptionsUsesCommandLookupEnvLocale(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")

	cmd := &settingsCommand{lookupEnv: func(name string) string {
		if name == "LANG" {
			return "ko_KR.UTF-8"
		}
		return ""
	}}
	options := cmd.rootOptions(settingsRootTabGlobal)

	if got, want := options.Title, "설정"; got != want {
		t.Fatalf("root settings title = %q, want command lookup locale %q", got, want)
	}
	if got, want := options.Prompt, "설정 > "; got != want {
		t.Fatalf("root settings prompt = %q, want command lookup locale %q", got, want)
	}
	wantChips := []projmuxpicker.Chip{
		{Label: "전체", Active: true, ClickValue: settingsRootTabGlobalValue},
		{Label: "프로젝트", Disabled: true, ClickValue: settingsRootTabProjectValue},
	}
	if got := options.TitleChips; !reflect.DeepEqual(got, wantChips) {
		t.Fatalf("root settings title chips = %#v, want %#v", got, wantChips)
	}
	if got, want := options.Footer, "행을 열거나 범위 칩을 클릭해 탭을 전환합니다."; got != want {
		t.Fatalf("root settings footer = %q, want command lookup locale %q", got, want)
	}
	if !hasEntryLabelContaining(options.Entries, "프로젝트 선택기") {
		t.Fatalf("root settings entries = %#v, want command lookup locale Korean labels", options.Entries)
	}
}

func TestSettingsRootOptionsUsesGlobalConfigLocale(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[ui]
locale = "ko-KR"
`)
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "LC_ALL" {
				return "en_US.UTF-8"
			}
			return ""
		},
	}

	options := cmd.rootOptions(settingsRootTabGlobal)
	if got, want := options.Title, "설정"; got != want {
		t.Fatalf("root settings title = %q, want global config locale %q", got, want)
	}
	if !hasEntryLabelContaining(options.Entries, "프로젝트 선택기") {
		t.Fatalf("root settings entries = %#v, want Korean labels from global config locale", options.Entries)
	}
}

func TestSettingsTextKeysHaveFallbackCatalogEntries(t *testing.T) {
	t.Parallel()

	keys := make([]i18n.Key, 0, len(settingsTextKeys))
	seen := map[i18n.Key]bool{}
	for _, key := range settingsTextKeys {
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if missing := i18n.DefaultCatalog().MissingFallbackKeys(keys); len(missing) > 0 {
		t.Fatalf("settingsTextKeys missing en-US fallback entries: %#v", missing)
	}
}

func TestLiteralProjmuxFootersHaveCatalogMappings(t *testing.T) {
	t.Parallel()

	re := regexp.MustCompile(`projmuxFooter\("([^"]+)"\)`)
	var missing []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range re.FindAllSubmatch(data, -1) {
			footer := strings.TrimSpace(string(match[1]))
			if _, ok := settingsTextKeys[footer]; !ok {
				missing = append(missing, path+": "+footer)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("literal projmuxFooter strings missing settingsTextKeys mappings: %#v", missing)
	}
}

func TestSettingsKoreanVisibleStringsHaveNoEnglishChromeResidue(t *testing.T) {
	home := t.TempDir()
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == i18n.LocaleEnvName {
				return "ko-KR"
			}
			return ""
		},
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{}),
	}

	options := settingsKoreanVisibleOptionSamples(t, cmd)
	for _, option := range options {
		for _, visible := range settingsVisibleOptionStrings(option) {
			if visible == "" {
				continue
			}
			if phrase, ok := settingsEnglishChromeResidue(visible); ok {
				t.Fatalf("%s visible string still contains English %q: %q", option.UI, phrase, visible)
			}
		}
	}
	for _, visible := range settingsKoreanStaticRowSamples() {
		if phrase, ok := settingsEnglishChromeResidue(visible); ok {
			t.Fatalf("static settings row still contains English %q: %q", phrase, visible)
		}
	}
}

func settingsKoreanVisibleOptionSamples(t *testing.T, cmd *settingsCommand) []intpickercompat.Options {
	t.Helper()

	var samples []intpickercompat.Options
	add := func(options intpickercompat.Options) {
		samples = append(samples, cmd.localizeSettingsOptions(options))
	}
	add(cmd.rootOptions(settingsRootTabGlobal))
	for _, section := range []string{
		settingsSectionProject,
		settingsSectionNotifications,
		settingsSectionStatusbar,
		settingsSectionSessionState,
		settingsSectionKeybindings,
		settingsSectionLabs,
		settingsSectionAbout,
	} {
		options, err := cmd.sectionOptions(section)
		if err != nil {
			t.Fatalf("sectionOptions(%q): %v", section, err)
		}
		add(options)
	}
	if options, err := cmd.statusbarDecorationTargetOptions(statusbarDecorationTargetNotify); err == nil {
		add(options)
	} else {
		t.Fatalf("statusbarDecorationTargetOptions(notify): %v", err)
	}
	if options, err := cmd.themeLayerOptions(themeLayerGlobal); err == nil {
		add(options)
	} else {
		t.Fatalf("themeLayerOptions(global): %v", err)
	}
	add(cmd.effectiveMergeOptions(settingsProjectContext{}))
	add(intpickercompat.Options{
		UI:      "settings-notifications-desktop",
		Entries: cmd.desktopNotifyEntries(),
		Title:   "Notifications - Desktop notifications",
		Prompt:  "Settings > Notifications > Desktop notifications > ",
		Footer:  projmuxFooter("Enter: apply  |  Back row: parent "),
	})
	add(intpickercompat.Options{
		UI:      "settings-notifications-ai-dedupe",
		Entries: cmd.aiNotifyDedupeEntries(),
		Title:   "Notifications - AI dedupe window",
		Prompt:  "Settings > Notifications > AI dedupe > ",
		Footer:  projmuxFooter("Enter: apply  |  Back row: parent "),
	})
	return samples
}

func settingsVisibleOptionStrings(options intpickercompat.Options) []string {
	values := []string{options.Title, options.Prompt, options.Header, options.Footer}
	for _, chip := range options.TitleChips {
		values = append(values, chip.Label)
	}
	return values
}

func settingsKoreanStaticRowSamples() []string {
	locale := i18n.Locale("ko-KR")
	samples := []string{
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Project Root", "not configured"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Workdirs", "add or remove scan roots"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Pinned Projects", "add or remove pins"),
		settingsLabelInfoLocale(locale, "Saved workdirs", "(none)", "~/.config/projmux/workdirs"),
		settingsLabelInfoLocale(locale, "Effective Project Root", "not configured", "no env, tmux option, or saved value"),
		settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Use Current Project as Root", "/tmp/project"),
		settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Remove", "/tmp/project"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Desktop notifications", "notify - default"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "AI notification dedupe", "60s - default"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Delivery sources", "doctor"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Hook quiet policy", "catalog defaults"),
		settingsLabelInfoLocale(locale, "In-app queue", "statusbar/sidebar", "consume pending notify rows"),
		settingsLabelInfoLocale(locale, "Notification hook override", "PROJMUX_NOTIFY_HOOK", "PROJMUX_NOTIFY_HOOK env"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Path icon", "symbol"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Git icon", "emoji"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Notify icon", "off"),
		settingsLabelInfoLocale(locale, "Current", "symbol", "bell marker in notification sidebar"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Auto-save", "on - default"),
		settingsLabelInfoLocale(locale, "Storage", "latest snapshot store", "per-session JSON under XDG state"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Snapshot actions", "latest/named save"),
		settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Save latest snapshot", "capture live project session as latest"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Preview restore", "dry-run only"),
		settingsLabelInfoLocale(locale, "Action", "Toggle Settings", "Settings"),
		settingsLabelInfoLocale(locale, "Action ID", "SettingsToggle", ""),
		settingsLabelInfoLocale(locale, "Terminal", "Ghostty", "supported mappings: projmux init ghostty"),
		settingsLabelLocale(locale, settingsGlyphType, settingsColorType, "Add alias", "press new key capture"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Project Hooks", "on - default"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Sidebar startup picker", "on - default"),
		settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Welcome", "revisit the shell quickstart guide"),
		settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Quit projmux", "open quit actions"),
		settingsLabelInfoLocale(locale, "Latest", "v0.0.0", "cache"),
	}
	for i := range samples {
		samples[i] = stripANSI(samples[i])
	}
	return samples
}

func settingsEnglishChromeResidue(visible string) (string, bool) {
	visible = strings.Join(strings.Fields(visible), " ")
	allowed := []string{
		"AI",
		"Alt-1",
		"Codex",
		"Ctrl-C",
		"Esc",
		"Git",
		"GitHub",
		"JSON",
		"Meta",
		"Nerd Font",
		"OS",
		"PROJMUX_NOTIFY_HOOK",
		"XDG",
		"tmux",
		"notify-send",
		"osfocus",
		"statusbar/sidebar",
	}
	normalized := visible
	for _, literal := range allowed {
		normalized = strings.ReplaceAll(normalized, literal, "")
	}
	for _, phrase := range []string{
		"Settings >",
		"Back row",
		"Project Picker",
		"Project Root",
		"Workdirs",
		"Pinned Projects",
		"Notifications",
		"Desktop notifications",
		"AI notification dedupe",
		"Delivery sources",
		"Hook quiet policy",
		"In-app queue",
		"Notification hook override",
		"Appearance",
		"Path icon",
		"Git icon",
		"Notify icon",
		"Current",
		"Session State",
		"Auto-save",
		"Snapshot actions",
		"Keybindings",
		"Labs",
		"About",
		"Welcome",
		"Quit projmux",
		"Update Now",
		"Check Updates",
		"Pending Notifications",
	} {
		if strings.Contains(normalized, phrase) {
			return phrase, true
		}
	}
	return "", false
}

func TestSettingsRootRowsUsePhase0ChromePalette(t *testing.T) {
	t.Parallel()

	if settingsColorType != "\x1b[38;2;141;205;142m" || settingsColorDim != "\x1b[90m" || settingsColorInfo != "\x1b[38;2;216;224;228m" {
		t.Fatalf("shared settings colors changed: type=%q dim=%q info=%q", settingsColorType, settingsColorDim, settingsColorInfo)
	}
	options := (&settingsCommand{}).rootOptions(settingsRootTabGlobal)
	if got := options.TitleChips; len(got) != 2 || !got[0].Active || !got[1].Disabled {
		t.Fatalf("root settings title chips = %#v, want active Global and disabled Project chrome", got)
	}
	if got, want := options.Footer, "Open rows or click a scope chip to switch tabs."; got != want {
		t.Fatalf("root settings footer = %q, want %q", got, want)
	}
	for _, entry := range options.Entries {
		if !strings.Contains(entry.Label, settingsRootColorOpen) {
			t.Fatalf("root settings row label = %q, want root action color %q", entry.Label, settingsRootColorOpen)
		}
		if !strings.Contains(entry.Label, settingsRootColorDim) {
			t.Fatalf("root settings row label = %q, want root secondary color %q", entry.Label, settingsRootColorDim)
		}
		if strings.Contains(entry.Label, settingsColorType) {
			t.Fatalf("root settings row label = %q, want root-only color instead of shared type color", entry.Label)
		}
	}

	projectOptions := (&settingsCommand{}).rootOptions(settingsRootTabProject)
	for _, entry := range projectOptions.Entries {
		if !strings.Contains(entry.Label, settingsRootColorDim) {
			t.Fatalf("project root disabled row label = %q, want root secondary color %q", entry.Label, settingsRootColorDim)
		}
		if strings.Contains(entry.Label, settingsColorDim) {
			t.Fatalf("project root disabled row label = %q, want root-only dim color instead of shared dim color", entry.Label)
		}
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

func TestSettingsRootAltArrowChordsAreTransportTierWithDefaultBindings(t *testing.T) {
	t.Parallel()

	// Alt-arrow and Alt-Shift-arrow chords are transport-dependent. They
	// remain catalogued outside the guaranteed zero-config launch tier, but
	// still render app-scoped tmux binds where terminals forward them.
	wantChords := map[string]string{
		"select-pane-left":  "M-Left",
		"select-pane-right": "M-Right",
		"select-pane-up":    "M-Up",
		"select-pane-down":  "M-Down",
		"previous-window":   "M-S-Left",
		"next-window":       "M-S-Right",
	}
	catalog := defaultKeyBindingCatalog()
	for id, wantChord := range wantChords {
		var got keyBindingAction
		var found bool
		for _, action := range catalog {
			if action.ID == id {
				got = action
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("keybinding catalog missing %q", id)
		}
		if got.Tier != keyBindingTierTransportDependent {
			t.Fatalf("keybinding catalog %q tier = %q, want %q", id, got.Tier, keyBindingTierTransportDependent)
		}
		if got.PlainChord != wantChord {
			t.Fatalf("keybinding catalog %q chord = %q, want %q", id, got.PlainChord, wantChord)
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
	for _, label := range []string{"Trust", "Hooks (project)", "Project recipe", "Theme override", "Effective theme", "Effective merge view"} {
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
		{settingsAIEnabledAgents, settingsAxisGlobal},
		{settingsSectionNotifications, settingsAxisGlobal},
		{settingsNotificationsDesktop, settingsAxisGlobal},
		{settingsLabsProjectHooks, settingsAxisGlobal},
		{settingsActionPrefixAI + aiModeCodex, settingsAxisGlobal},
		{settingsActionPrefixAIEnabledAgent + aiModeCodex, settingsAxisGlobal},
		{settingsActionPrefixAIBadgeStyle + string(config.AIBadgeStyleEmoji), settingsAxisGlobal},
		{settingsActionPrefixStatusbar + string(statusbarDecorationTargetGit) + ":" + string(config.StatusbarDecorationSymbol), settingsAxisGlobal},
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
	assertCataloguedEntries("ai enabled agents", cmd.aiEnabledAgentEntries())
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
	var rootOptions intpickercompat.Options
	var aiOptions intpickercompat.Options
	var aiDetailOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { rootOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsSectionAI}},
		{observe: func(o intpickercompat.Options) { aiOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsAIDefaultMode}},
		{observe: func(o intpickercompat.Options) { aiDetailOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAI + "codex"}},
	})
	cmd := &settingsCommand{
		ai:           ai,
		switcher:     switcher,
		homeDir:      func() (string, error) { return home, nil },
		lookupEnv:    func(string) string { return "" },
		runner:       runner,
		nativePicker: native,
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
	if got, want := rootOptions.Footer, "Open rows or click a scope chip to switch tabs."; got != want {
		t.Fatalf("root settings footer = %q, want %q", got, want)
	}
	if got, want := entryValues(rootOptions.Entries), []string{
		settingsSectionProject,
		settingsSectionAI,
		settingsSectionNotifications,
		settingsSectionGlobalHooks,
		settingsSectionStatusbar,
		settingsSectionGlobalTheme,
		settingsSectionSessionState,
		settingsSectionKeybindings,
		settingsSectionLabs,
		settingsSectionAbout,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root settings entry order = %#v, want %#v", got, want)
	}
	if !hasEntryLabelContaining(rootOptions.Entries, "Appearance") {
		t.Fatalf("root settings entries = %#v, want generic appearance section label", rootOptions.Entries)
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
	if !hasEntryValue(aiOptions.Entries, settingsAIEnabledAgents) {
		t.Fatalf("AI settings entries = %#v, want Enabled agents detail row", aiOptions.Entries)
	}
	if hasEntryValue(aiOptions.Entries, settingsAINotifyDiagnostics) {
		t.Fatalf("AI settings entries = %#v, want Notify integrations moved to Notifications", aiOptions.Entries)
	}
	if hasEntryValue(aiOptions.Entries, settingsActionPrefixAI+aiModeClaude) ||
		hasEntryValue(aiOptions.Entries, settingsActionPrefixAI+aiModeCodex) ||
		hasEntryValue(aiOptions.Entries, settingsActionPrefixAI+aiModeAntigravity) ||
		hasEntryValue(aiOptions.Entries, settingsActionPrefixAI+aiModeShell) {
		t.Fatalf("AI settings entries = %#v, want no direct mode choices at root", aiOptions.Entries)
	}
	if got, want := aiDetailOptions.UI, "settings-ai-default-mode"; got != want {
		t.Fatalf("AI default mode UI = %q, want %q", got, want)
	}
	for _, want := range []string{
		settingsActionPrefixAI + aiModeClaude,
		settingsActionPrefixAI + aiModeCodex,
		settingsActionPrefixAI + aiModeAntigravity,
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

func TestSettingsHubTogglesAIEnabledAgentPersists(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	ai := testAICommand(home)
	switcher := testSettingsSwitchCommand(t, &stubSwitchPinStore{})
	var enabledOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionAI}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsAIEnabledAgents}},
		{observe: func(o intpickercompat.Options) { enabledOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAIEnabledAgent + aiModeClaude}},
	})
	cmd := &settingsCommand{
		ai:           ai,
		switcher:     switcher,
		homeDir:      func() (string, error) { return home, nil },
		lookupEnv:    func(string) string { return "" },
		runner:       runner,
		nativePicker: native,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := enabledOptions.UI, "settings-ai-enabled-agents"; got != want {
		t.Fatalf("AI enabled agents UI = %q, want %q", got, want)
	}
	for _, want := range []string{
		settingsActionPrefixAIEnabledAgent + aiModeClaude,
		settingsActionPrefixAIEnabledAgent + aiModeCodex,
		settingsActionPrefixAIEnabledAgent + aiModeAntigravity,
	} {
		if !hasEntryValue(enabledOptions.Entries, want) {
			t.Fatalf("AI enabled agents entries = %#v, want %q", enabledOptions.Entries, want)
		}
	}
	for _, unwanted := range []string{
		settingsActionPrefixAIEnabledAgent + aiModeShell,
		settingsActionPrefixAIEnabledAgent + aiModeSelective,
	} {
		if hasEntryValue(enabledOptions.Entries, unwanted) {
			t.Fatalf("AI enabled agents entries = %#v, want no %q", enabledOptions.Entries, unwanted)
		}
	}

	paths, err := pickerBackendConfigPaths(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatalf("pickerBackendConfigPaths() error = %v", err)
	}
	got, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		t.Fatalf("LoadAIEnabledAgentsFile() error = %v", err)
	}
	want := []config.AIAgentProvider{config.AIAgentCodex, config.AIAgentAntigravity}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled agents = %#v, want %#v", got, want)
	}
}

func TestSettingsAIProviderRegistryDrivesEnabledAgentRows(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		ai:        testAICommand(t.TempDir()),
		homeDir:   func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(string) string { return "" },
	}

	entries := cmd.aiEnabledAgentEntries()
	for _, provider := range aiprovider.SettingsVisible() {
		value := settingsActionPrefixAIEnabledAgent + string(provider.ID)
		if !hasEntryValue(entries, value) {
			t.Fatalf("AI enabled agents entries = %#v, want provider row %q", entries, value)
		}
		if !hasEntryLabelContainingAll(entries, provider.DisplayName, provider.DisplayName+" split") {
			t.Fatalf("AI enabled agents entries = %#v, want display metadata for %q", entries, provider.ID)
		}
	}
	for _, unwanted := range []string{
		settingsActionPrefixAIEnabledAgent + aiModeShell,
		settingsActionPrefixAIEnabledAgent + aiModeSelective,
	} {
		if hasEntryValue(entries, unwanted) {
			t.Fatalf("AI enabled agents entries = %#v, want no %q", entries, unwanted)
		}
	}
}

func TestSettingsAIEnabledAgentsWarnsWhenDefaultModeDisabled(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	ai := testAICommand(home)
	if err := ai.setMode(aiModeCodex); err != nil {
		t.Fatalf("setMode(codex) error = %v", err)
	}
	paths, err := pickerBackendConfigPaths(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatalf("pickerBackendConfigPaths() error = %v", err)
	}
	if err := config.SaveAIEnabledAgentsFile(paths.AIEnabledAgentsFile(), []config.AIAgentProvider{config.AIAgentClaude}); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
	cmd := &settingsCommand{
		ai:        ai,
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}

	if !hasEntryLabelContainingAll(cmd.aiRootEntries(), "Default split mode", "codex", "disabled") {
		t.Fatalf("AI root entries = %#v, want disabled default-mode warning", cmd.aiRootEntries())
	}
	detail := cmd.aiEnabledAgentEntries()
	if !hasEntryLabelContainingAll(detail, "Warning", "Default split mode", "codex disabled") {
		t.Fatalf("AI enabled agents entries = %#v, want disabled default-mode warning", detail)
	}
	defaultModeDetail := cmd.aiEntries()
	if hasEntryValue(defaultModeDetail, settingsActionPrefixAI+aiModeCodex) {
		t.Fatalf("AI default mode entries = %#v, want disabled Codex hidden", defaultModeDetail)
	}
	if !hasEntryValue(defaultModeDetail, settingsActionPrefixAI+aiModeClaude) {
		t.Fatalf("AI default mode entries = %#v, want enabled Claude row", defaultModeDetail)
	}
	if !hasEntryLabelContainingAll(defaultModeDetail, "Warning", "Default split mode", "codex disabled") {
		t.Fatalf("AI default mode entries = %#v, want disabled default-mode warning", defaultModeDetail)
	}
	if !hasEntryLabelContainingAll(detail, "Enabled agents", "Claude") {
		t.Fatalf("AI enabled agents entries = %#v, want current enabled set", detail)
	}
	if hasEntryLabelContainingAll(detail, "Enabled agents", "Codex") {
		t.Fatalf("AI enabled agents entries = %#v, want current enabled set without Codex", detail)
	}
}

func TestSettingsHubKeepsLabsSectionWithoutPickerBackendChoices(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var labsOptions intpickercompat.Options
	var tmuxCalls [][]string
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionLabs}},
		{observe: func(o intpickercompat.Options) { labsOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
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
		runner:       runner,
		nativePicker: native,
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
	if hasEntryLabelContaining(labsOptions.Entries, "Sidebar startup picker") ||
		hasEntryValue(labsOptions.Entries, settingsCompatSidebarStartupPicker) {
		t.Fatalf("labs settings entries = %#v, want sidebar startup picker moved to Session State", labsOptions.Entries)
	}
	if hasEntryValue(labsOptions.Entries, settingsActionPrefixSessionState+"sidebar-startup:off") ||
		hasEntryValue(labsOptions.Entries, settingsActionPrefixSessionState+"sidebar-startup:on") {
		t.Fatalf("labs settings entries = %#v, want no direct sidebar startup picker mutation rows", labsOptions.Entries)
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

func TestSettingsLabsKeybindingsRedirectsToUnifiedRootView(t *testing.T) {
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
	if got := keybindingOptions.TitleChips; len(got) < 2 || !got[0].Active || strings.TrimSpace(got[0].ClickValue) != "" {
		t.Fatalf("redirect chips = %#v, want passive Global/Project tabs", got)
	}
	if hasEntryLabelContaining(keybindingOptions.Entries, "Terminal") ||
		hasEntryLabelContaining(keybindingOptions.Entries, "Preview terminal mappings") ||
		hasEntryLabelContaining(keybindingOptions.Entries, "Apply terminal mappings") {
		t.Fatalf("redirect entries = %#v, want Settings Keybindings root without terminal remediation rows", keybindingOptions.Entries)
	}
	if !hasEntryValue(keybindingOptions.Entries, settingsActionPrefixKeymap+"ProjectSidebarToggle") {
		t.Fatalf("redirect entries = %#v, want Settings Keybindings action list", keybindingOptions.Entries)
	}
}

func TestSettingsHubSetsProjectHooksMode(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var labsOptions intpickercompat.Options
	var overviewOptions intpickercompat.Options
	var tmuxCalls [][]string
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionLabs}},
		{observe: func(o intpickercompat.Options) { labsOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsLabsProjectHooks}},
		{observe: func(o intpickercompat.Options) { overviewOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixHooks + string(config.ProjectHooksOff)}},
	})
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
		runner:       runner,
		nativePicker: native,
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

	tests := []struct {
		name    string
		target  statusbarDecorationTarget
		initial config.StatusbarDecoration
		selects config.StatusbarDecoration
		preview string
	}{
		{
			name:    "path",
			target:  statusbarDecorationTargetCwd,
			initial: config.StatusbarDecorationOff,
			selects: config.StatusbarDecorationEmoji,
			preview: "📁 ~/source/repos/projmux",
		},
		{
			name:    "git",
			target:  statusbarDecorationTargetGit,
			initial: config.StatusbarDecorationOff,
			selects: config.StatusbarDecorationSymbol,
			preview: " main * ↑1",
		},
		{
			name:    "notify",
			target:  statusbarDecorationTargetNotify,
			initial: config.StatusbarDecorationEmoji,
			selects: config.StatusbarDecorationOff,
			preview: "Pending Notifications",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			paths, err := config.Homes{HomeDir: home}.Paths()
			if err != nil {
				t.Fatal(err)
			}
			if err := config.SaveStatusbarDecorationFile(statusbarDecorationTargetFile(paths, tc.target), tc.initial); err != nil {
				t.Fatalf("seed statusbar decoration: %v", err)
			}

			var statusbarOptions intpickercompat.Options
			var detailOptions intpickercompat.Options
			var refreshedOptions intpickercompat.Options
			var sawChangePage bool
			var tmuxCalls [][]string
			actionPrefix := settingsActionPrefixStatusbar + string(tc.target)
			runner, native := scriptedPicker(t, []pickerStep{
				{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionStatusbar}},
				{observe: func(o intpickercompat.Options) {
					statusbarOptions = o
					sawChangePage = sawChangePage || o.UI == "settings-statusbar-change"
				},
					reply: intpickercompat.Result{Key: "enter", Value: actionPrefix}},
				{observe: func(o intpickercompat.Options) {
					detailOptions = o
					sawChangePage = sawChangePage || o.UI == "settings-statusbar-change"
				},
					reply: intpickercompat.Result{Key: "enter", Value: actionPrefix + ":" + string(tc.selects)}},
				{observe: func(o intpickercompat.Options) {
					refreshedOptions = o
					sawChangePage = sawChangePage || o.UI == "settings-statusbar-change"
				}},
			})
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
				runner:       runner,
				nativePicker: native,
			}

			if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got, want := statusbarOptions.UI, "settings-statusbar"; got != want {
				t.Fatalf("statusbar settings UI = %q, want %q", got, want)
			}
			if got, want := statusbarOptions.Title, "Appearance - Theme font and icon decoration"; got != want {
				t.Fatalf("statusbar settings title = %q, want %q", got, want)
			}
			if got, want := statusbarOptions.Prompt, "Settings > Appearance > "; got != want {
				t.Fatalf("statusbar settings prompt = %q, want %q", got, want)
			}
			if got := statusbarOptions.Header; got != "" {
				t.Fatalf("statusbar settings header = %q, want description only in title", got)
			}
			if got := statusbarOptions.TitleChips; len(got) < 2 || !got[0].Active || strings.TrimSpace(got[0].ClickValue) != "" {
				t.Fatalf("statusbar settings chips = %#v, want passive Global/Project tabs", got)
			}
			for _, target := range []statusbarDecorationTarget{statusbarDecorationTargetCwd, statusbarDecorationTargetGit, statusbarDecorationTargetNotify} {
				if !hasEntryValue(statusbarOptions.Entries, settingsActionPrefixStatusbar+string(target)) {
					t.Fatalf("statusbar settings entries = %#v, want %s detail row", statusbarOptions.Entries, target)
				}
				if hasEntryValue(statusbarOptions.Entries, settingsActionPrefixStatusbar+string(target)+":"+string(config.StatusbarDecorationEmoji)) {
					t.Fatalf("statusbar settings entries = %#v, want no direct mutation row at root", statusbarOptions.Entries)
				}
			}
			if got, want := detailOptions.UI, "settings-statusbar-detail"; got != want {
				t.Fatalf("detail UI = %q, want %q", got, want)
			}
			if strings.Contains(detailOptions.Title, "Change") || strings.Contains(detailOptions.Prompt, "Change") {
				t.Fatalf("detail options = %#v, want no Change page title or prompt", detailOptions)
			}
			for _, mode := range statusbarDecorationModes() {
				value := actionPrefix + ":" + string(mode)
				if !hasEntryValue(detailOptions.Entries, value) {
					t.Fatalf("detail entries = %#v, want direct %s row", detailOptions.Entries, value)
				}
				if !hasEntryLabelContaining(detailOptions.Entries, "Preview "+string(mode)) {
					t.Fatalf("detail entries = %#v, want preview label for %s", detailOptions.Entries, mode)
				}
			}
			if !hasEntryLabelContaining(detailOptions.Entries, "Current") {
				t.Fatalf("detail entries = %#v, want current row", detailOptions.Entries)
			}
			if !hasEntryLabelContaining(detailOptions.Entries, tc.preview) {
				t.Fatalf("detail entries = %#v, want preview %q", detailOptions.Entries, tc.preview)
			}
			if hasEntryValue(detailOptions.Entries, actionPrefix+":change") || hasEntryLabelContaining(detailOptions.Entries, "Change") {
				t.Fatalf("detail entries = %#v, want no Change row", detailOptions.Entries)
			}
			if sawChangePage {
				t.Fatalf("picker opened settings-statusbar-change")
			}
			if !hasEntryLabelContainingAll(refreshedOptions.Entries, "Current", string(tc.selects)) {
				t.Fatalf("refreshed entries = %#v, want current display refreshed to %s", refreshedOptions.Entries, tc.selects)
			}
			if !hasEntryLabelContainingAll(refreshedOptions.Entries, "Preview "+string(tc.selects), "current") {
				t.Fatalf("refreshed entries = %#v, want selected preview marked current", refreshedOptions.Entries)
			}

			got, err := config.LoadStatusbarDecorationFile(statusbarDecorationTargetFile(paths, tc.target))
			if err != nil {
				t.Fatalf("LoadStatusbarDecorationFile() error = %v", err)
			}
			if got != tc.selects {
				t.Fatalf("%s decoration = %q, want %q", tc.target, got, tc.selects)
			}
			if !reflect.DeepEqual(tmuxCalls, [][]string{
				{"tmux", "set-option", "-g", statusbarDecorationTmuxOptionForTarget(tc.target), string(tc.selects)},
				{"tmux", "display-message", "decoration " + string(tc.target) + ": " + string(tc.selects)},
			}) {
				t.Fatalf("tmux calls = %#v", tmuxCalls)
			}
		})
	}
}

func TestSettingsHubSetsAIBadgeStyle(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAIBadgeStyleFile(paths.AIBadgeStyleFile(), config.AIBadgeStyleDot); err != nil {
		t.Fatalf("seed AI badge style: %v", err)
	}

	var appearanceOptions intpickercompat.Options
	var detailOptions intpickercompat.Options
	var refreshedOptions intpickercompat.Options
	var tmuxCalls [][]string
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionStatusbar}},
		{observe: func(o intpickercompat.Options) {
			appearanceOptions = o
		},
			reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAIBadgeStyle}},
		{observe: func(o intpickercompat.Options) {
			detailOptions = o
		},
			reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAIBadgeStyle + string(config.AIBadgeStyleEmoji)}},
		{observe: func(o intpickercompat.Options) {
			refreshedOptions = o
		}},
	})
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
		runner:       runner,
		nativePicker: native,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !hasEntryValue(appearanceOptions.Entries, settingsActionPrefixAIBadgeStyle) {
		t.Fatalf("appearance entries = %#v, want AI badge style row", appearanceOptions.Entries)
	}
	if got, want := detailOptions.UI, "settings-ai-badge-style"; got != want {
		t.Fatalf("AI badge style UI = %q, want %q", got, want)
	}
	for _, style := range aiBadgeStyles() {
		value := settingsActionPrefixAIBadgeStyle + string(style)
		if !hasEntryValue(detailOptions.Entries, value) {
			t.Fatalf("AI badge style entries = %#v, want %s row", detailOptions.Entries, value)
		}
	}
	if !hasEntryLabelContaining(detailOptions.Entries, "⏳ prompt") {
		t.Fatalf("AI badge style entries = %#v, want emoji preview", detailOptions.Entries)
	}
	if !hasEntryLabelContainingAll(refreshedOptions.Entries, "Preview "+string(config.AIBadgeStyleEmoji), "current") {
		t.Fatalf("refreshed entries = %#v, want emoji marked current", refreshedOptions.Entries)
	}

	got, err := config.LoadAIBadgeStyleFile(paths.AIBadgeStyleFile())
	if err != nil {
		t.Fatalf("LoadAIBadgeStyleFile() error = %v", err)
	}
	if got != config.AIBadgeStyleEmoji {
		t.Fatalf("AI badge style = %q, want %q", got, config.AIBadgeStyleEmoji)
	}
	if len(tmuxCalls) != 5 {
		t.Fatalf("tmux calls = %#v, want style option, pane-border-format, window-status formats, display-message", tmuxCalls)
	}
	if !reflect.DeepEqual(tmuxCalls[0], []string{"tmux", "set-option", "-g", aiBadgeStyleTmuxOption, string(config.AIBadgeStyleEmoji)}) {
		t.Fatalf("first tmux call = %#v", tmuxCalls[0])
	}
	if !reflect.DeepEqual(tmuxCalls[1][:4], []string{"tmux", "set-option", "-g", "pane-border-format"}) {
		t.Fatalf("second tmux call = %#v", tmuxCalls[1])
	}
	if !strings.Contains(tmuxCalls[1][4], "⏳") || !strings.Contains(tmuxCalls[1][4], "✅") || !strings.Contains(tmuxCalls[1][4], "🔄") {
		t.Fatalf("pane-border-format call = %#v, want emoji markers", tmuxCalls[1])
	}
	if !reflect.DeepEqual(tmuxCalls[2][:4], []string{"tmux", "set-option", "-g", "window-status-format"}) {
		t.Fatalf("third tmux call = %#v", tmuxCalls[2])
	}
	if !strings.Contains(tmuxCalls[2][4], "attention window #{window_id} #{@projmux_ai_badge_style}") {
		t.Fatalf("window-status-format call = %#v, want dynamic AI badge style", tmuxCalls[2])
	}
	if !reflect.DeepEqual(tmuxCalls[3][:4], []string{"tmux", "set-option", "-g", "window-status-current-format"}) {
		t.Fatalf("fourth tmux call = %#v", tmuxCalls[3])
	}
	if !strings.Contains(tmuxCalls[3][4], "attention window #{window_id} #{@projmux_ai_badge_style}") {
		t.Fatalf("window-status-current-format call = %#v, want dynamic AI badge style", tmuxCalls[3])
	}
	if !reflect.DeepEqual(tmuxCalls[4], []string{"tmux", "display-message", "AI badge style: emoji"}) {
		t.Fatalf("fifth tmux call = %#v", tmuxCalls[4])
	}
}

func TestSettingsAppearanceShowsThemeFontNotApplied(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "app")
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[theme]
font_family = "Cascadia Mono"
font_size = 12
`)
	writeFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[theme]
font_family = "JetBrains Mono"
`)

	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommandWithHome(t, home, &stubSwitchPinStore{}),
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
		runCommand: func(string, ...string) error { return nil },
	}

	entries := cmd.statusbarEntries()
	if !hasEntryLabelContainingAll(entries, "Theme font", "JetBrains Mono 12", "not applied") {
		t.Fatalf("appearance entries = %#v, want theme font not-applied row", entries)
	}
	if !hasEntryLabelContainingAll(entries, "Theme font", "font_family project", "font_size global") {
		t.Fatalf("appearance entries = %#v, want font source labels", entries)
	}
}

func TestSettingsThemeResetBoundaries(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "app")
	mkdirAll(t, filepath.Join(project, ".git"))
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[theme]
preset = "midnight"
background = "#101820"
`)
	writeFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[theme]
preset = "rose"
background = "#20151c"
`)
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	if err := cmd.resetThemeLayer(themeLayerProject, &bytes.Buffer{}); err != nil {
		t.Fatalf("reset project theme error = %v", err)
	}
	globalCfg, err := hooks.LoadProjectConfigFile(filepath.Join(home, ".config", "projmux", "config.toml"))
	if err != nil {
		t.Fatalf("load global config after project reset: %v", err)
	}
	if globalCfg.Theme.Preset != "midnight" || globalCfg.Theme.Background != "#101820" {
		t.Fatalf("global theme after project reset = %#v, want preserved", globalCfg.Theme)
	}
	projectCfg, err := hooks.LoadProjectConfigFile(filepath.Join(project, ".projmux", "config.toml"))
	if err != nil {
		t.Fatalf("load project config after project reset: %v", err)
	}
	if projectCfg.Theme.HasContent() {
		t.Fatalf("project theme after project reset = %#v, want removed", projectCfg.Theme)
	}

	if err := cmd.setThemePreset(themeLayerProject, "forest", &bytes.Buffer{}); err != nil {
		t.Fatalf("set project preset error = %v", err)
	}
	if err := cmd.setThemeColor(themeLayerProject, theme.TokenBackground, "#14201a", &bytes.Buffer{}); err != nil {
		t.Fatalf("set project background error = %v", err)
	}
	if err := cmd.resetThemeLayer(themeLayerGlobal, &bytes.Buffer{}); err != nil {
		t.Fatalf("reset global theme error = %v", err)
	}
	globalCfg, err = hooks.LoadProjectConfigFile(filepath.Join(home, ".config", "projmux", "config.toml"))
	if err != nil {
		t.Fatalf("load global config after global reset: %v", err)
	}
	if globalCfg.Theme.HasContent() {
		t.Fatalf("global theme after global reset = %#v, want removed", globalCfg.Theme)
	}
	projectCfg, err = hooks.LoadProjectConfigFile(filepath.Join(project, ".projmux", "config.toml"))
	if err != nil {
		t.Fatalf("load project config after global reset: %v", err)
	}
	if projectCfg.Theme.Preset != "forest" || projectCfg.Theme.Background != "#14201a" {
		t.Fatalf("project theme after global reset = %#v, want preserved", projectCfg.Theme)
	}
}

func TestSettingsEffectiveThemeShowsSourceLabels(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "app")
	mkdirAll(t, filepath.Join(project, ".git"))
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[theme]
foreground = "#eeeeee"
`)
	writeFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[theme]
background = "#010203"
`)
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	entries := cmd.effectiveThemeEntries()
	if !hasEntryLabelContainingAll(entries, "background", "#010203", "project") {
		t.Fatalf("effective theme entries = %#v, want project background source", entries)
	}
	if !hasEntryLabelContainingAll(entries, "foreground", "#eeeeee", "global") {
		t.Fatalf("effective theme entries = %#v, want global foreground source", entries)
	}
	if !hasEntryLabelContainingAll(entries, "accent", "fallback") {
		t.Fatalf("effective theme entries = %#v, want fallback accent source", entries)
	}
}

func TestSettingsProjectThemeDistinguishesInheritAndOverride(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "app")
	mkdirAll(t, filepath.Join(project, ".git"))
	writeFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[theme]
preset = "inherit"
background = "inherit"
accent = "#9bcf8f"
`)
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
	}

	entries, err := cmd.themeLayerEntries(themeLayerProject)
	if err != nil {
		t.Fatalf("themeLayerEntries(project) error = %v", err)
	}
	if !hasEntryLabelContainingAll(entries, "Preset selector", "inherit global") {
		t.Fatalf("project theme entries = %#v, want preset inherit state", entries)
	}
	if !hasEntryLabelContainingAll(entries, "background", "inherit global") {
		t.Fatalf("project theme entries = %#v, want background inherit state", entries)
	}
	if !hasEntryLabelContainingAll(entries, "accent", "#9bcf8f", "set override") {
		t.Fatalf("project theme entries = %#v, want accent set override state", entries)
	}
}

func TestSettingsAppearanceShowsLanguageLocaleDetail(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[ui]
locale = "auto"
`)
	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "LC_MESSAGES" {
				return "ko_KR.UTF-8"
			}
			return ""
		},
		runCommand: func(string, ...string) error { return nil },
	}

	appearance := cmd.statusbarEntries()
	if !hasEntryValue(appearance, settingsAppearanceLanguage) {
		t.Fatalf("appearance entries = %#v, want language/locale row", appearance)
	}
	if !hasEntryLabelContainingAll(appearance, "언어 / Locale", "auto", "ko-KR", "LC_MESSAGES env") {
		t.Fatalf("appearance entries = %#v, want auto detected locale/source preview", appearance)
	}

	detail := cmd.localeEntries()
	for _, want := range []string{
		settingsActionPrefixLocale + "auto",
		settingsActionPrefixLocale + "en-US",
		settingsActionPrefixLocale + "ko-KR",
	} {
		if !hasEntryValue(detail, want) {
			t.Fatalf("locale detail entries = %#v, want row %q", detail, want)
		}
	}
	if !hasEntryLabelContainingAll(detail, "현재", "ko-KR", "LC_MESSAGES env") {
		t.Fatalf("locale detail entries = %#v, want detected current locale/source", detail)
	}
	if !hasEntryLabelContainingAll(detail, "[ui].locale", "auto", "config.toml") {
		t.Fatalf("locale detail entries = %#v, want global config setting row", detail)
	}
}

func TestSettingsAppearanceLocaleEnvAutoBypassesGlobalConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[ui]
locale = "ko-KR"
`)
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_LOCALE":
				return "auto"
			case "LC_ALL":
				return "en_US.UTF-8"
			case "LC_MESSAGES":
				return "ko_KR.UTF-8"
			default:
				return ""
			}
		},
	}

	detail := cmd.localeEntries()
	if !hasEntryLabelContainingAll(detail, "Current", "en-US", "LC_ALL env") {
		t.Fatalf("locale detail entries = %#v, want PROJMUX_LOCALE=auto to bypass global config and use LC_ALL", detail)
	}
	if !hasEntryLabelContainingAll(detail, "PROJMUX_LOCALE", "auto", "env override") {
		t.Fatalf("locale detail entries = %#v, want env auto override row", detail)
	}
	if !hasEntryLabelContainingAll(detail, "[ui].locale", "ko-KR", "config.toml") {
		t.Fatalf("locale detail entries = %#v, want global config pin shown but not effective", detail)
	}
}

func TestSettingsAppearanceLocaleDetailUsesCatalog(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[ui]
locale = "ko-KR"
`)
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}

	options := cmd.localeOptions()
	if got, want := options.Title, "모양 - 언어 / Locale"; got != want {
		t.Fatalf("locale title = %q, want %q", got, want)
	}
	if got, want := options.Prompt, "설정 > 모양 > 언어 / Locale > "; got != want {
		t.Fatalf("locale prompt = %q, want %q", got, want)
	}
	if got, want := options.Footer, "Enter: 적용  |  뒤로 행: 상위"; got != want {
		t.Fatalf("locale footer = %q, want %q", got, want)
	}
	if !hasEntryLabelContainingAll(options.Entries, "현재", "ko-KR", "config.toml") {
		t.Fatalf("locale entries = %#v, want Korean Current row with preserved config.toml literal", options.Entries)
	}
	if !hasEntryLabelContainingAll(options.Entries, "한국어 UI", "현재") {
		t.Fatalf("locale entries = %#v, want localized Korean UI/current description", options.Entries)
	}
}

func TestSettingsAppearanceLocaleUnsupportedWarning(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[ui]
locale = "ja-JP"
`)
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "LC_MESSAGES" {
				return "ko_KR.UTF-8"
			}
			return ""
		},
	}

	detail := cmd.localeEntries()
	if !hasEntryLabelContainingAll(detail, "Current", "en-US", "config.toml") {
		t.Fatalf("locale detail entries = %#v, want fallback current locale from config source", detail)
	}
	if !hasEntryLabelContainingAll(detail, "Warning", "Unsupported locale ja-JP", "using en-US") {
		t.Fatalf("locale detail entries = %#v, want unsupported locale fallback warning", detail)
	}
}

func TestSettingsAppearanceLocaleEnvOverrideWinsAndLiteralsRemain(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[ui]
locale = "ko-KR"
`)
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_LOCALE" {
				return "en-US"
			}
			if name == "LC_ALL" {
				return "ko_KR.UTF-8"
			}
			return ""
		},
	}

	detail := cmd.localeEntries()
	if !hasEntryLabelContainingAll(detail, "Current", "en-US", "PROJMUX_LOCALE env") {
		t.Fatalf("locale detail entries = %#v, want env override to win", detail)
	}
	for _, literal := range []string{"PROJMUX_LOCALE", "[ui].locale", "config.toml"} {
		if !hasEntryLabelContaining(detail, literal) {
			t.Fatalf("locale detail entries = %#v, want literal %q preserved", detail, literal)
		}
	}
}

func TestSettingsSetGlobalLocaleWritesUIConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := &settingsCommand{
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		runCommand: func(string, ...string) error { return nil },
	}

	if err := cmd.setGlobalLocale("ko-KR"); err != nil {
		t.Fatalf("setGlobalLocale() error = %v", err)
	}
	got, err := hooks.LoadGlobalConfig(filepath.Join(home, ".config", "projmux", "config.toml"))
	if err != nil {
		t.Fatalf("LoadGlobalConfig() error = %v", err)
	}
	if got.UI.Locale != "ko-KR" {
		t.Fatalf("global UI locale = %q, want ko-KR", got.UI.Locale)
	}
}

func TestSettingsHubStatusbarDecorationChangeActionIsUnreachable(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	actionPrefix := settingsActionPrefixStatusbar + string(statusbarDecorationTargetGit)
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionStatusbar}},
		{reply: intpickercompat.Result{Key: "enter", Value: actionPrefix}},
		{reply: intpickercompat.Result{Key: "enter", Value: actionPrefix + ":change"}},
	})
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
		runCommand:   func(string, ...string) error { return nil },
		runner:       runner,
		nativePicker: native,
	}

	err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unknown appearance detail action") {
		t.Fatalf("Run() error = %v, want stale Change action rejected", err)
	}
}

func TestSettingsSetDesktopNotifyModePersistsAndWritesTmuxOption(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in       string
		wantOpt  string
		wantText string
	}{
		{"none", "off", "off"},
		{"notify", "notify", "notify"},
		{"raise", "raise", "raise"},
		{"off", "off", "off"},
		{"toast", "notify", "notify"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			home := t.TempDir()
			var tmuxCalls [][]string
			cmd := &settingsCommand{
				homeDir: func() (string, error) { return home, nil },
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
			paths, err := config.Homes{HomeDir: home}.Paths()
			if err != nil {
				t.Fatal(err)
			}
			got, err := config.LoadDesktopNotifyModeFile(paths.DesktopNotifyModeFile())
			if err != nil {
				t.Fatalf("LoadDesktopNotifyModeFile() error = %v", err)
			}
			if got != config.DesktopNotifyMode(tc.wantOpt) {
				t.Fatalf("saved desktop notify mode = %q, want %q", got, tc.wantOpt)
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

func TestSettingsSetDesktopNotifyModeOutsideTmuxPersistsWithoutLiveUpdate(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var tmuxCalls [][]string
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
		runCommand: func(name string, args ...string) error {
			tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
			return nil
		},
	}
	if err := cmd.setDesktopNotifyMode("none"); err != nil {
		t.Fatalf("setDesktopNotifyMode(none) outside tmux returned error: %v", err)
	}
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadDesktopNotifyModeFile(paths.DesktopNotifyModeFile())
	if err != nil {
		t.Fatalf("LoadDesktopNotifyModeFile() error = %v", err)
	}
	if got != config.DesktopNotifyModeOff {
		t.Fatalf("saved desktop notify mode = %q, want %q", got, config.DesktopNotifyModeOff)
	}
	if len(tmuxCalls) != 0 {
		t.Fatalf("outside tmux: tmux calls = %#v, want no live update", tmuxCalls)
	}
}

func TestSettingsSetDesktopNotifyModeRejectsGarbage(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return t.TempDir(), nil },
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

func TestSettingsAIRootNestsAIDetailsAndExcludesDesktopNotifications(t *testing.T) {
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
	if !hasEntryValue(root, settingsAIEnabledAgents) {
		t.Fatalf("AI root entries = %#v, want Enabled agents row", root)
	}
	if hasEntryValue(root, settingsAINotifyDiagnostics) {
		t.Fatalf("AI root entries = %#v, want Notify integrations moved to Notifications", root)
	}
	if got, want := len(root), 3; got != want {
		t.Fatalf("AI root entries = %#v, want back row plus AI detail rows", root)
	}
	for _, want := range []string{
		settingsActionPrefixAI + aiModeClaude,
		settingsActionPrefixAI + aiModeCodex,
		settingsActionPrefixAI + aiModeAntigravity,
		settingsActionPrefixAI + aiModeShell,
		settingsActionPrefixDesktopNotifyMode + string(config.DesktopNotifyModeOff),
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
		settingsActionPrefixAI + aiModeAntigravity,
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

	enabledDetail := cmd.aiEnabledAgentEntries()
	for _, want := range []string{
		settingsActionPrefixAIEnabledAgent + aiModeClaude,
		settingsActionPrefixAIEnabledAgent + aiModeCodex,
		settingsActionPrefixAIEnabledAgent + aiModeAntigravity,
	} {
		if !hasEntryValue(enabledDetail, want) {
			t.Fatalf("AI enabled agent entries = %#v, want row %q", enabledDetail, want)
		}
	}
	for _, unwanted := range []string{
		settingsActionPrefixAIEnabledAgent + aiModeShell,
		settingsActionPrefixAIEnabledAgent + aiModeSelective,
		settingsActionPrefixAI + aiModeShell,
		settingsActionPrefixAI + aiModeSelective,
	} {
		if hasEntryValue(enabledDetail, unwanted) {
			t.Fatalf("AI enabled agent entries = %#v, want no row %q", enabledDetail, unwanted)
		}
	}
}

func TestSettingsAINotifyDiagnosticsRenderDoctorRowsAndCommandGuidance(t *testing.T) {
	t.Parallel()

	diagnostics := []doctorAINotifyIntegration{
		{
			ID:             "codex-hooks",
			Name:           "Codex hooks",
			Status:         doctorAINotifyStatusConflict,
			ConfigPath:     "/home/tester/.codex/config.toml",
			ConflictReason: "unmanaged notify command",
			Guidance:       "Codex requires reviewing/enabling installed hook commands from /hooks before they run.",
			TestedVersion:  "codex-cli 0.130.0",
			InstallCommand: "projmux ai integrate codex",
			RemoveCommand:  "projmux ai integrate codex --remove",
			DryRunCommand:  "projmux ai integrate codex --dry-run",
		},
		{
			ID:             "claude-hooks",
			Name:           "Claude Code hooks",
			Status:         doctorAINotifyStatusMissing,
			ConfigPath:     "/home/tester/.claude/settings.json",
			TestedVersion:  "Claude Code 2.1.140",
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
	visibleDiagnostics := diagnostics[1:]
	tmuxRunner := &recordingTmuxRunner{}
	cmd := &settingsCommand{
		ai:                  testAICommand(t.TempDir()),
		aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return diagnostics },
		tmuxRunner:          tmuxRunner,
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
	if got, want := listOptions.Footer, "Enter: view details"; !strings.Contains(got, want) {
		t.Fatalf("delivery sources footer = %q, want %q", got, want)
	}
	for _, diag := range visibleDiagnostics {
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
	for _, want := range []string{"tested with codex-cli 0.130.0", "tested with Claude Code 2.1.140"} {
		if !hasEntryLabelContaining(listOptions.Entries, want) {
			t.Fatalf("delivery sources entries = %#v, want %q", listOptions.Entries, want)
		}
	}
	if got, want := detailOptions.UI, "settings-notifications-delivery-detail"; got != want {
		t.Fatalf("delivery source detail UI = %q, want %q", got, want)
	}
	if got, want := detailOptions.Footer, "Enter: copy command"; !strings.Contains(got, want) {
		t.Fatalf("delivery source detail footer = %q, want %q", got, want)
	}
	for _, want := range []string{
		"conflict",
		"/home/tester/.codex/config.toml",
		"unmanaged notify command",
		"codex-cli 0.130.0",
		"Codex requires reviewing/enabling installed hook commands from /hooks before they run.",
		"projmux ai integrate codex",
		"projmux ai integrate codex --remove",
		"projmux ai integrate codex --dry-run",
		"Copy only",
	} {
		if !hasEntryLabelContaining(detailOptions.Entries, want) {
			t.Fatalf("AI notify detail entries = %#v, want %q", detailOptions.Entries, want)
		}
	}
	for _, want := range []string{
		settingsActionPrefixAINotifyCommand + "codex-hooks:install",
		settingsActionPrefixAINotifyCommand + "codex-hooks:remove",
		settingsActionPrefixAINotifyCommand + "codex-hooks:dry-run",
	} {
		if !hasEntryValue(detailOptions.Entries, want) {
			t.Fatalf("AI notify detail entries = %#v, want command action %q", detailOptions.Entries, want)
		}
	}
	if hasEntryLabelContaining(detailOptions.Entries, "--mode hooks") {
		t.Fatalf("AI notify detail entries = %#v, want no --mode hooks command", detailOptions.Entries)
	}
	for _, entry := range detailOptions.Entries {
		if entry.Value != settingsBackValue && entry.Value != settingsNoopValue && !strings.HasPrefix(entry.Value, settingsActionPrefixAINotifyCommand) {
			t.Fatalf("AI notify detail entry = %#v, want back/noop/command value", entry)
		}
	}
	if len(tmuxRunner.calls) != 0 {
		t.Fatalf("tmux calls = %#v, want no clipboard copy while opening detail", tmuxRunner.calls)
	}
}

func TestSettingsAINotifyDiagnosticsDetailCommandRowsCopyCommands(t *testing.T) {
	t.Parallel()

	diagnostics := []doctorAINotifyIntegration{{
		ID:             "claude-hooks",
		Name:           "Claude Code hooks",
		Status:         doctorAINotifyStatusMissing,
		ConfigPath:     "/home/tester/.claude/settings.json",
		InstallCommand: "projmux ai integrate claude",
		RemoveCommand:  "projmux ai integrate claude --remove",
		DryRunCommand:  "projmux ai integrate claude --dry-run",
	}}

	var calls int
	tmuxRunner := &recordingTmuxRunner{}
	cmd := &settingsCommand{
		ai:                  testAICommand(t.TempDir()),
		aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return diagnostics },
		tmuxRunner:          tmuxRunner,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionNotifications}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsNotificationsDelivery}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAINotifyDiagnostic + "claude-hooks"}, nil
			case 4:
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAINotifyCommand + "claude-hooks:install"}, nil
			case 5:
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAINotifyCommand + "claude-hooks:remove"}, nil
			case 6:
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAINotifyCommand + "claude-hooks:dry-run"}, nil
			case 7:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 8:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 9:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 10:
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
	for _, want := range []recordedTmuxCall{
		{name: "tmux", args: []string{"set-buffer", "-w", "--", "projmux ai integrate claude"}},
		{name: "tmux", args: []string{"display-message", "Claude Code hooks install command copied to clipboard"}},
		{name: "tmux", args: []string{"set-buffer", "-w", "--", "projmux ai integrate claude --remove"}},
		{name: "tmux", args: []string{"display-message", "Claude Code hooks remove command copied to clipboard"}},
		{name: "tmux", args: []string{"set-buffer", "-w", "--", "projmux ai integrate claude --dry-run"}},
		{name: "tmux", args: []string{"display-message", "Claude Code hooks dry-run command copied to clipboard"}},
	} {
		if !hasRecordedTmuxCall(tmuxRunner.calls, want) {
			t.Fatalf("tmux calls = %#v, want %#v", tmuxRunner.calls, want)
		}
	}
}

func TestSettingsAINotifyDiagnosticsCommandCopyFailureStaysInDetail(t *testing.T) {
	t.Parallel()

	diagnostics := []doctorAINotifyIntegration{{
		ID:             "claude-hooks",
		Name:           "Claude Code hooks",
		Status:         doctorAINotifyStatusMissing,
		ConfigPath:     "/home/tester/.claude/settings.json",
		InstallCommand: "projmux ai integrate claude",
		RemoveCommand:  "projmux ai integrate claude --remove",
		DryRunCommand:  "projmux ai integrate claude --dry-run",
	}}

	var calls int
	var detailOptions intpickercompat.Options
	tmuxRunner := &recordingTmuxRunner{err: errors.New("tmux clipboard unavailable")}
	cmd := &settingsCommand{
		ai:                  testAICommand(t.TempDir()),
		aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return diagnostics },
		tmuxRunner:          tmuxRunner,
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionNotifications}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: settingsNotificationsDelivery}, nil
			case 3:
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAINotifyDiagnostic + "claude-hooks"}, nil
			case 4:
				detailOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixAINotifyCommand + "claude-hooks:install"}, nil
			case 5:
				detailOptions = options
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

	var stderr bytes.Buffer
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := detailOptions.UI, "settings-notifications-delivery-detail"; got != want {
		t.Fatalf("delivery source detail UI = %q, want %q", got, want)
	}
	if !hasEntryLabelContaining(detailOptions.Entries, "projmux ai integrate claude") {
		t.Fatalf("AI notify detail entries = %#v, want install command despite clipboard failure", detailOptions.Entries)
	}
	wantCopy := recordedTmuxCall{name: "tmux", args: []string{"set-buffer", "-w", "--", "projmux ai integrate claude"}}
	if !hasRecordedTmuxCall(tmuxRunner.calls, wantCopy) {
		t.Fatalf("tmux calls = %#v, want attempted clipboard copy %#v", tmuxRunner.calls, wantCopy)
	}
	if hasRecordedTmuxCall(tmuxRunner.calls, recordedTmuxCall{name: "tmux", args: []string{"display-message", "Claude Code hooks install command copied to clipboard"}}) {
		t.Fatalf("tmux calls = %#v, did not expect success message after failed copy", tmuxRunner.calls)
	}
	if got := stderr.String(); !strings.Contains(got, "warning: copy Claude Code hooks install command to clipboard: tmux clipboard unavailable") {
		t.Fatalf("stderr = %q, want clipboard failure warning", got)
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
		settingsNotificationsAIDedupe,
		settingsNotificationsDelivery,
		settingsNotificationsHookActions,
		settingsNotificationsQueue,
		settingsNotificationsHookOverride,
	} {
		if !hasEntryValue(root, want) {
			t.Fatalf("notifications entries = %#v, want row %q", root, want)
		}
	}
	for _, value := range []string{
		settingsActionPrefixDesktopNotifyMode + string(config.DesktopNotifyModeOff),
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeNotify),
		settingsActionPrefixDesktopNotifyMode + string(desktopNotifyModeRaise),
	} {
		if hasEntryValue(root, value) {
			t.Fatalf("notifications entries = %#v, want no direct desktop notification choice %q", root, value)
		}
	}

	detail := cmd.desktopNotifyEntries()
	for _, want := range []string{
		settingsActionPrefixDesktopNotifyMode + string(config.DesktopNotifyModeOff),
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

func TestSettingsNotificationsDesktopNotifyRowsUseKoreanCatalog(t *testing.T) {
	t.Parallel()

	var calls int
	var notificationsOptions intpickercompat.Options
	var desktopOptions intpickercompat.Options
	cmd := &settingsCommand{
		lookupEnv: func(name string) string {
			switch name {
			case i18n.LocaleEnvName:
				return "ko-KR"
			case "PROJMUX_DESKTOP_NOTIFY_MODE":
				return "raise"
			default:
				return ""
			}
		},
		runner: switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				return intpickercompat.Result{Key: "enter", Value: settingsSectionNotifications}, nil
			case 2:
				notificationsOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsNotificationsDesktop}, nil
			case 3:
				desktopOptions = options
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 4:
				return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
			case 5:
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
	if got, want := desktopOptions.Title, "알림 - 데스크톱 알림"; got != want {
		t.Fatalf("desktop notifications title = %q, want %q", got, want)
	}
	if got, want := desktopOptions.Prompt, "설정 > 알림 > 데스크톱 알림 > "; got != want {
		t.Fatalf("desktop notifications prompt = %q, want %q", got, want)
	}
	if !hasEntryLabelContaining(notificationsOptions.Entries, "데스크톱 알림") {
		t.Fatalf("notifications entries = %#v, want Korean desktop notifications row", notificationsOptions.Entries)
	}
	if !hasEntryLabelContaining(desktopOptions.Entries, "데스크톱 알림") {
		t.Fatalf("desktop notification entries = %#v, want Korean info row", desktopOptions.Entries)
	}
	visible := strings.Join([]string{
		notificationsOptions.Title,
		notificationsOptions.Prompt,
		desktopOptions.Title,
		desktopOptions.Prompt,
		settingsEntryLabelsText(notificationsOptions.Entries),
		settingsEntryLabelsText(desktopOptions.Entries),
	}, "\n")
	for _, residue := range []string{"Desktop notifications", "DesktopNotification"} {
		if strings.Contains(visible, residue) {
			t.Fatalf("localized notifications visible text contains %q: %q", residue, visible)
		}
	}
}

func TestSettingsNotificationsHookActionsShowsAndSavesRuntimeQuietPolicy(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	ai := testAICommand(home)
	ai.readFile = os.ReadFile
	cmd := &settingsCommand{
		ai:        ai,
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(name string) string { return "" },
		runCommand: func(string, ...string) error {
			t.Fatal("hook quiet policy settings must not execute external commands")
			return nil
		},
		runOutput: func(string, ...string) ([]byte, error) {
			t.Fatal("hook quiet policy settings must not shell out for command output")
			return nil, nil
		},
	}

	root := cmd.notificationsEntries()
	if !hasEntryValue(root, settingsNotificationsHookActions) {
		t.Fatalf("notifications entries = %#v, want hook quiet policy row", root)
	}
	providers := cmd.aiHookProviderEntries()
	for _, want := range []string{
		settingsActionPrefixAIHookProvider + aiHookProviderCodex,
		settingsActionPrefixAIHookProvider + aiHookProviderClaude,
	} {
		if !hasEntryValue(providers, want) {
			t.Fatalf("provider entries = %#v, want %q", providers, want)
		}
	}
	codexEvents := cmd.aiHookEventEntries(aiHookProviderCodex)
	if !hasEntryValue(codexEvents, settingsActionPrefixAIHookEvent+aiHookProviderCodex+":Stop") {
		t.Fatalf("codex hook entries = %#v, want Stop row", codexEvents)
	}
	if !hasEntryLabelContaining(codexEvents, "install=true") {
		t.Fatalf("codex hook entries = %#v, want install read-only hint", codexEvents)
	}
	choices := cmd.aiHookActionChoiceEntries(aiHookProviderCodex, "PreToolUse")
	if !hasEntryLabelContaining(choices, "generic in-app queue only") || !hasEntryLabelContaining(choices, "OS toast unsupported") {
		t.Fatalf("PreToolUse action choices = %#v, want generic in-app-only hint", choices)
	}
	stopChoices := cmd.aiHookActionChoiceEntries(aiHookProviderCodex, "Stop")
	if !hasEntryLabelContaining(stopChoices, "OS toast supported") {
		t.Fatalf("Stop action choices = %#v, want specialized OS toast support hint", stopChoices)
	}

	var stdout bytes.Buffer
	if err := cmd.setAIHookAction(aiHookProviderCodex, "Stop", aiHookActionQuiet, &stdout); err != nil {
		t.Fatalf("setAIHookAction error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "codex Stop = quiet") {
		t.Fatalf("stdout = %q, want saved action", got)
	}
	if got := ai.aiHookEffectiveAction(aiHookProviderCodex, "Stop"); got.Action != aiHookActionQuiet || got.Source != aiHookActionSourceRuntime {
		t.Fatalf("effective Stop action = %#v, want runtime quiet", got)
	}
	if events, err := ai.aiHookInstallEvents(aiHookProviderCodex); err != nil || !containsString(events, "Stop") {
		t.Fatalf("install events = %#v, err = %v; want Stop preserved", events, err)
	}
}

func TestSettingsNotificationsAIDedupeRowsAndCustomWrite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(name string) string { return "" },
	}

	root := cmd.notificationsEntries()
	if !hasEntryValue(root, settingsNotificationsAIDedupe) {
		t.Fatalf("notifications entries = %#v, want AI dedupe row", root)
	}

	detail := cmd.aiNotifyDedupeEntries()
	for _, want := range []string{
		settingsActionPrefixAINotifyDedupe + "30",
		settingsActionPrefixAINotifyDedupe + "60",
		settingsActionPrefixAINotifyDedupe + "120",
		settingsActionPrefixAINotifyDedupe + "300",
		settingsActionPrefixAINotifyDedupe + "custom",
	} {
		if !hasEntryValue(detail, want) {
			t.Fatalf("AI dedupe entries = %#v, want row %q", detail, want)
		}
	}
	if !hasEntryLabelContaining(detail, "tmux bell fallback stays 5s") {
		t.Fatalf("AI dedupe entries = %#v, want scope row preserving bell fallback", detail)
	}

	var stdout bytes.Buffer
	if err := cmd.setAINotifyDedupeSeconds(75, &stdout); err != nil {
		t.Fatalf("setAINotifyDedupeSeconds error = %v", err)
	}
	if got := cmd.currentAINotifyDedupeSeconds(); got.Seconds != 75 || got.Source != aiNotifyDedupeSourceSetting {
		t.Fatalf("current AI dedupe = %#v, want 75/setting", got)
	}
	if got, want := stdout.String(), "AI notification dedupe: 75s\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
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
		"interval",
		"1m",
		"Sidebar startup picker",
		"off",
		"default",
		"Storage",
		"latest snapshot store",
		"Retention",
		"latest snapshot only",
	} {
		if !hasEntryLabelContaining(entries, want) {
			t.Fatalf("session state entries = %#v, want label containing %q", entries, want)
		}
	}
	for _, absent := range []string{"Snapshot session", "Preview", "window 0", "pane 0.0"} {
		if hasEntryLabelContaining(entries, absent) {
			t.Fatalf("session state entries = %#v, did not want current snapshot tree label %q", entries, absent)
		}
	}
	for _, want := range []string{
		settingsSessionStateAutosaveDetail,
		settingsSessionStateSidebarStartupPickerDetail,
	} {
		if !hasEntryValue(entries, want) {
			t.Fatalf("session state entries = %#v, want %q", entries, want)
		}
	}
	for _, absent := range []string{
		settingsActionPrefixSessionState + "autosave:on",
		settingsActionPrefixSessionState + "autosave:off",
		settingsActionPrefixSessionState + "autorestore:on",
		settingsActionPrefixSessionState + "autorestore:off",
		settingsActionPrefixSessionState + "sidebar-startup:on",
		settingsActionPrefixSessionState + "sidebar-startup:off",
	} {
		if hasEntryValue(entries, absent) {
			t.Fatalf("session state entries = %#v, want no direct mutation row %q", entries, absent)
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
	if !strings.Contains(options.Title, "settings") {
		t.Fatalf("project session state title = %q, want settings", options.Title)
	}
	for _, want := range []string{
		"Project",
		"projmux",
		"Project path",
		project,
		"PROJMUX_CWD env",
		"Session identity",
		"repos-projmux",
		"Project auto-save",
		"inherit",
		"Effective auto-save",
		"off",
		"global default",
		"Global auto-save",
		"Snapshot actions",
		"preview/delete available",
	} {
		if !hasEntryLabelContaining(options.Entries, want) {
			t.Fatalf("project session state entries = %#v, want label containing %q", options.Entries, want)
		}
	}
	for _, absent := range []string{"Snapshot session", "window 1", "pane 1.0", "Pane cwd", "Pane recipe"} {
		if hasEntryLabelContaining(options.Entries, absent) {
			t.Fatalf("project session state entries = %#v, did not want primary snapshot tree label %q", options.Entries, absent)
		}
	}
	if hasEntryLabelContaining(options.Entries, "live-session") {
		t.Fatalf("project session state entries = %#v, want derived identity instead of live tmux session", options.Entries)
	}
	for _, want := range []string{settingsProjectSessionStateAutosaveDetail, settingsProjectSessionStateActionsDetail} {
		if !hasEntryValue(options.Entries, want) {
			t.Fatalf("project session state entries = %#v, want project action %q", options.Entries, want)
		}
	}
	for _, absent := range []string{settingsProjectSessionStateSaveLatest, settingsProjectSessionStateSaveNamed, settingsProjectSessionStatePreview, settingsProjectSessionStateDelete} {
		if hasEntryValue(options.Entries, absent) {
			t.Fatalf("project session state entries = %#v, want no direct mutation action %q", options.Entries, absent)
		}
	}
}

func TestSettingsSessionStateGlobalDefaultAutosaveOffAndNoTree(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{homeDir: func() (string, error) { return t.TempDir(), nil }}
	entries := cmd.sessionStateEntries()
	for _, want := range []string{"Auto-save", "off", "default", "Sidebar startup picker", "Storage", "Retention"} {
		if !hasEntryLabelContaining(entries, want) {
			t.Fatalf("session state entries = %#v, want %q", entries, want)
		}
	}
	for _, absent := range []string{"Window", "Pane", "Snapshot session", "Preview restore", "Delete snapshot"} {
		if hasEntryLabelContaining(entries, absent) {
			t.Fatalf("session state entries = %#v, did not want %q", entries, absent)
		}
	}
}

func TestSettingsSessionStateSidebarStartupPickerDetailPersistsExistingFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if got, want := options.UI, "settings-sessionstate"; got != want {
				t.Fatalf("session state UI = %q, want %q", got, want)
			}
			if !hasEntryValue(options.Entries, settingsSessionStateSidebarStartupPickerDetail) {
				t.Fatalf("session state entries = %#v, want sidebar startup picker detail row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateSidebarStartupPickerDetail}, nil
		case 2:
			if got, want := options.UI, "settings-sessionstate-detail"; got != want {
				t.Fatalf("sidebar startup detail UI = %q, want %q", got, want)
			}
			if got, want := options.Title, "Session State - Sidebar startup picker"; got != want {
				t.Fatalf("sidebar startup detail title = %q, want %q", got, want)
			}
			if got, want := options.Prompt, "Settings > Session State > Sidebar startup picker > "; got != want {
				t.Fatalf("sidebar startup detail prompt = %q, want %q", got, want)
			}
			if strings.Contains(options.Title, "Labs") || strings.Contains(options.Prompt, "Labs") {
				t.Fatalf("sidebar startup detail chrome = title %q prompt %q, want no Labs path", options.Title, options.Prompt)
			}
			if !hasEntryValue(options.Entries, settingsActionPrefixSessionState+"sidebar-startup:on") ||
				!hasEntryValue(options.Entries, settingsActionPrefixSessionState+"sidebar-startup:off") {
				t.Fatalf("sidebar startup detail entries = %#v, want on/off mutation rows", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSessionState + "sidebar-startup:on"}, nil
		case 3:
			if !hasEntryLabelContaining(options.Entries, "on") {
				t.Fatalf("sidebar startup detail entries after save = %#v, want on state", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 4:
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
			if name == "XDG_CONFIG_HOME" {
				return filepath.Join(home, "config")
			}
			return ""
		},
	}

	if err := cmd.runSessionStateSection(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSessionStateSection() error = %v", err)
	}
	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, "config")}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := config.LoadSessionStateToggleFile(paths.SidebarStartupPickerFile()); err != nil || got != config.SessionStateToggleOn {
		t.Fatalf("sidebar startup picker file = %q, %v; want on, nil", got, err)
	}
	if got := filepath.Base(paths.SidebarStartupPickerFile()); got != config.SidebarStartupPickerFileName {
		t.Fatalf("sidebar startup picker file name = %q, want %q", got, config.SidebarStartupPickerFileName)
	}
}

func TestSettingsLabsSidebarStartupPickerRedirectsToSessionStateDetail(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			if got, want := options.UI, "settings-labs"; got != want {
				t.Fatalf("labs UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsCompatSidebarStartupPicker}, nil
		case 2:
			if got, want := options.UI, "settings-sessionstate-detail"; got != want {
				t.Fatalf("redirect detail UI = %q, want %q", got, want)
			}
			if strings.Contains(options.Title, "Labs") || strings.Contains(options.Prompt, "Labs") {
				t.Fatalf("redirect detail chrome = title %q prompt %q, want Session State path", options.Title, options.Prompt)
			}
			if got, want := options.Title, "Session State - Sidebar startup picker"; got != want {
				t.Fatalf("redirect detail title = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
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
		lookupEnv:    func(string) string { return "" },
	}

	if err := cmd.runLabsSection(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runLabsSection() error = %v", err)
	}
}

func TestSettingsProjectSessionStateShowsEffectiveAutosaveSource(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, "config")}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSessionStateToggleFile(paths.SessionStateAutosaveFile(), config.SessionStateToggleOn); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSessionStateProjectToggleFile(paths.ProjectSessionStateAutosaveFile("repos-projmux"), config.SessionStateProjectOff); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(home, "source", "repos", "projmux")
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return filepath.Join(home, "config")
			case "PROJMUX_CWD":
				return project
			default:
				return ""
			}
		},
	}

	entries := cmd.projectSessionStateEntries()
	for _, want := range []string{"Project auto-save", "off", "saved", "Effective auto-save", "project override", "Global auto-save", "on"} {
		if !hasEntryLabelContaining(entries, want) {
			t.Fatalf("project session state entries = %#v, want %q", entries, want)
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
	for _, want := range []string{"Project auto-save", "inherit", "Effective auto-save", "off", "Snapshot actions", "save unavailable: live project session not found", "snapshot missing"} {
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
	for _, absent := range []string{"Window", "Pane", "Save latest snapshot", "Delete snapshot"} {
		if hasEntryLabelContaining(invalidOptions.Entries, absent) {
			t.Fatalf("invalid snapshot entries = %#v, want no primary snapshot/action label %q", invalidOptions.Entries, absent)
		}
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
	if err := cmd.executeSessionStateAction("autosave-interval:90s", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("autosave interval error = %v", err)
	}
	paths, err := config.Homes{HomeDir: home, StateHome: xdgState}.Paths()
	if err != nil {
		t.Fatalf("Paths() error = %v", err)
	}
	if got, err := config.LoadSessionStateToggleFile(paths.SessionStateAutosaveFile()); err != nil || got != config.SessionStateToggleOff {
		t.Fatalf("autosave file = %q, %v; want off, nil", got, err)
	}
	if got, err := config.LoadSessionStateDurationFileDefault(paths.SessionStateAutosaveIntervalFile(), time.Minute); err != nil || got != 90*time.Second {
		t.Fatalf("autosave interval file = %s, %v; want 90s, nil", got, err)
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
			if hasEntryValue(options.Entries, settingsSessionStateDelete) {
				t.Fatalf("session state entries = %#v, want no direct delete action", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateAutosaveDetail}, nil
		case 2:
			if got, want := options.UI, "settings-sessionstate-detail"; got != want {
				t.Fatalf("detail UI = %q, want %q", got, want)
			}
			if !hasEntryValue(options.Entries, settingsActionPrefixSessionState+"autosave:off") {
				t.Fatalf("session state detail entries = %#v, want autosave mutation row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
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
		t.Fatalf("Load() after view-first navigation error = %v, want snapshot preserved", err)
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
			if got, want := options.UI, "settings-sessionstate"; got != want {
				t.Fatalf("session state UI = %q, want %q", got, want)
			}
			if hasEntryValue(options.Entries, settingsSessionStateDelete) {
				t.Fatalf("session state entries = %#v, want no direct delete action", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateAutosaveDetail}, nil
		case 2:
			if got, want := options.UI, "settings-sessionstate-detail"; got != want {
				t.Fatalf("detail UI = %q, want %q", got, want)
			}
			if !hasEntryValue(options.Entries, settingsSessionStateAutosaveIntervalSet) {
				t.Fatalf("session state detail entries = %#v, want autosave interval row", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateAutosaveIntervalSet}, nil
		case 3:
			if got, want := options.UI, "settings-sessionstate-autosave-interval"; got != want {
				t.Fatalf("interval UI = %q, want %q", got, want)
			}
			if !options.AcceptQuery {
				t.Fatalf("interval picker AcceptQuery = false, want true")
			}
			return intpickercompat.Result{Key: "enter", Query: "2m"}, nil
		case 4:
			if got, want := options.UI, "settings-sessionstate-detail"; got != want {
				t.Fatalf("detail UI after apply = %q, want %q", got, want)
			}
			if !hasEntryLabelContaining(options.Entries, "2m") {
				t.Fatalf("session state detail entries = %#v, want applied 2m interval", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
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
	paths, err := config.Homes{HomeDir: home, StateHome: xdgState}.Paths()
	if err != nil {
		t.Fatalf("Paths() error = %v", err)
	}
	if got, err := config.LoadSessionStateDurationFileDefault(paths.SessionStateAutosaveIntervalFile(), time.Minute); err != nil || got != 2*time.Minute {
		t.Fatalf("autosave interval file = %s, %v; want 2m, nil", got, err)
	}
	if _, err := store.Load("workspace"); err != nil {
		t.Fatalf("Load() after autosave interval detail change error = %v, want snapshot preserved", err)
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

func TestSettingsProjectSessionStateSaveNamedSnapshotUsesPortablePaths(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgState := t.TempDir()
	project := filepath.Join(home, "source", "repos", "projmux")
	service := filepath.Join(project, "service")
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
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "has-session", "-t", "repos-projmux"}, "\x00"):                        "",
			strings.Join([]string{"tmux", "list-windows", "-t", "repos-projmux", "-F", windowFormat}, "\x00"):   "0\x1fmain\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "repos-projmux", "-F", paneFormat}, "\x00"): "0\x1f0\x1feditor\x1f1\x1f" + service + "\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\n",
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
	if err := cmd.executeSessionStateAction("project-save-named:team", &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("project-save-named error = %v", err)
	}
	preset, err := corelayout.NewStore(project).Load("team")
	if err != nil {
		t.Fatalf("Load(named snapshot) error = %v", err)
	}
	if got, want := preset.DefaultCWD, "${PROJMUX_CWD}/service"; got != want {
		t.Fatalf("default cwd = %q, want portable %q", got, want)
	}
	if got, want := preset.Windows[0].Panes[0].CWD, "${PROJMUX_CWD}/service"; got != want {
		t.Fatalf("pane cwd = %q, want portable %q", got, want)
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
			if hasEntryValue(options.Entries, settingsProjectSessionStateDelete) {
				t.Fatalf("project session state entries = %#v, want no direct delete action", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsProjectSessionStateActionsDetail}, nil
		case 2:
			if got, want := options.UI, "settings-project-sessionstate-actions"; got != want {
				t.Fatalf("project actions UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsProjectSessionStateDelete}, nil
		case 3:
			if got, want := options.UI, "settings-project-sessionstate-delete-confirm"; got != want {
				t.Fatalf("confirm UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateConfirmNo}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
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
			return intpickercompat.Result{Key: "enter", Value: settingsProjectSessionStateActionsDetail}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsProjectSessionStateDelete}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsSessionStateConfirmYes}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 5:
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
	if hasEntryLabelContaining(entries, "Snapshot") || hasEntryLabelContaining(entries, "missing") {
		t.Fatalf("session state entries = %#v, want global settings only", entries)
	}
	if hasEntryValue(entries, settingsSessionStateDelete) {
		t.Fatalf("session state entries = %#v, want delete disabled when missing", entries)
	}
}

func TestSettingsHubRunsProjectPickerActions(t *testing.T) {
	t.Parallel()

	store := &stubSwitchPinStore{}
	switcher := testSettingsSwitchCommand(t, store)
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings"; got != want {
				t.Fatalf("settings UI = %q, want %q", got, want)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-project-picker"; got != want {
				t.Fatalf("project settings UI = %q, want %q", got, want)
			}
			if !hasEntryValue(o.Entries, settingsBackValue) {
				t.Fatalf("project settings entries = %#v, want back entry", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "add:/home/tester/source/repos/app"}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.ProjectSidebarToggle]\nplain = \"M-a\"\nprefix = \"A\"\n")

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
	if !hasEntryLabelContaining(keybindingOptions.Entries, "All catalog actions are listed") {
		t.Fatalf("keybindings entries = %#v, want catalog-complete note", keybindingOptions.Entries)
	}
	for _, absent := range []string{"Bindings", "Diagnostic", "Probe", "Init", "terminal mappings"} {
		if hasEntryLabelContaining(keybindingOptions.Entries, absent) {
			t.Fatalf("keybindings entries = %#v, did not want first-class %q surface", keybindingOptions.Entries, absent)
		}
	}
	if !hasEntryValue(keybindingOptions.Entries, settingsActionPrefixKeymap+"ProjectSidebarToggle") {
		t.Fatalf("keybindings entries = %#v, want canonical ProjectSidebarToggle action", keybindingOptions.Entries)
	}
	if !hasEntryValue(keybindingOptions.Entries, settingsActionPrefixKeymap+"Sidebar:PinProject") {
		t.Fatalf("keybindings entries = %#v, want native picker internal action in Settings list", keybindingOptions.Entries)
	}
	if !hasEntryValue(keybindingOptions.Entries, settingsActionPrefixKeymap+"previous-window") {
		t.Fatalf("keybindings entries = %#v, want previous-window action in Settings list", keybindingOptions.Entries)
	}
	if !hasEntryValue(keybindingOptions.Entries, settingsActionPrefixKeymap+"select-pane-left") {
		t.Fatalf("keybindings entries = %#v, want select-pane-left action in Settings list", keybindingOptions.Entries)
	}
	if !hasEntryLabelContaining(keybindingOptions.Entries, "keys Alt-A (M-a) (custom)") {
		t.Fatalf("keybindings entries = %#v, want custom plain value", keybindingOptions.Entries)
	}
	if hasEntryLabelContaining(keybindingOptions.Entries, "prefix") {
		t.Fatalf("keybindings entries = %#v, did not want prefix value", keybindingOptions.Entries)
	}
}

func TestSettingsHubKeybindingsUsesReadablePrimaryLabels(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	entries, err := cmd.keybindingEntries()
	if err != nil {
		t.Fatalf("keybindingEntries() error = %v", err)
	}

	projectIndex := entryIndexValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle")
	if projectIndex < 0 {
		t.Fatalf("keybindings entries = %#v, want project sidebar row", entries)
	}
	notifyIndex := entryIndexValue(entries, settingsActionPrefixKeymap+"NotifySidebarToggle")
	if notifyIndex < 0 {
		t.Fatalf("keybindings entries = %#v, want notify sidebar row", entries)
	}
	if projectIndex > notifyIndex {
		t.Fatalf("project sidebar row index = %d, notify row index = %d; want project sidebar listed first", projectIndex, notifyIndex)
	}
	projectLabel := entries[projectIndex].Label
	for _, want := range []string{"Toggle Project Sidebar", "Alt-1", "M-1"} {
		if !strings.Contains(projectLabel, want) {
			t.Fatalf("project sidebar label = %q, want %q", projectLabel, want)
		}
	}
	if strings.Contains(projectLabel, "ProjectSidebarToggle") {
		t.Fatalf("project sidebar label = %q, want primary label without internal action ID", projectLabel)
	}

	detailEntries, title, err := cmd.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatalf("keybindingDetailEntries() error = %v", err)
	}
	if got, want := title, "Keybinding - Toggle Project Sidebar"; got != want {
		t.Fatalf("detail title = %q, want %q", got, want)
	}
	if !hasEntryLabelContainingAll(detailEntries, "Keybinding", "Alt-1", "M-1") {
		t.Fatalf("detail entries = %#v, want readable default alias with tmux chord", detailEntries)
	}
	if hasEntryLabelContaining(detailEntries, "Action ID") {
		t.Fatalf("detail entries = %#v, did not want internal action ID in simplified detail", detailEntries)
	}
}

func TestSettingsHubKeybindingsListsPopupLocalAndMovementActions(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	entries, err := cmd.keybindingEntries()
	if err != nil {
		t.Fatalf("keybindingEntries() error = %v", err)
	}

	cases := []struct {
		id             string
		primaryLabel   string
		wants          []string
		wantSearchText string
	}{
		{settingsActionPrefixKeymap + "Sidebar:PinProject", "Project Sidebar: Pin Project", []string{"Alt-P", "M-p", "Sidebar"}, "Project Sidebar"},
		{settingsActionPrefixKeymap + "Sidebar:KillSession", "Project Sidebar: Kill Session", []string{"Ctrl-X", "C-x", "Sidebar"}, "Project Sidebar"},
		{settingsActionPrefixKeymap + "SessionPopup:KillSession", "Session Popup: Kill Session", []string{"Ctrl-X", "C-x", "SessionPopup"}, "Session Popup"},
		{settingsActionPrefixKeymap + "SessionPopup:CyclePreviewWindowPrev", "Session Popup: Preview Previous Window", []string{"Left", "SessionPopup"}, "Session Popup"},
		{settingsActionPrefixKeymap + "SessionPopup:CyclePreviewPanePrev", "Session Popup: Preview Previous Pane", []string{"Alt-Up", "M-Up", "SessionPopup"}, "Session Popup"},
		{settingsActionPrefixKeymap + "NotifySidebar:Ack", "Notify Sidebar: Acknowledge", []string{"A", "a", "NotifySidebar"}, "Notify Sidebar"},
		{settingsActionPrefixKeymap + "NotifySidebar:ClearAll", "Notify Sidebar: Clear All", []string{"Ctrl-X", "C-x", "NotifySidebar"}, "Notify Sidebar"},
		{settingsActionPrefixKeymap + "Settings:SwitchTabNext", "Next Settings Tab", []string{"Alt-Shift-Right", "M-S-Right", "Settings"}, "Next Settings Tab"},
		{settingsActionPrefixKeymap + "previous-window", "Previous Window", []string{"Alt-Shift-Left", "M-S-Left", "transport-dependent"}, "Previous Window"},
		{settingsActionPrefixKeymap + "next-window", "Next Window", []string{"Alt-Shift-Right", "M-S-Right", "transport-dependent"}, "Next Window"},
		{settingsActionPrefixKeymap + "select-pane-left", "Select Pane Left", []string{"Alt-Left", "M-Left", "transport-dependent"}, "Select Pane Left"},
		{settingsActionPrefixKeymap + "select-pane-right", "Select Pane Right", []string{"Alt-Right", "M-Right", "transport-dependent"}, "Select Pane Right"},
		{settingsActionPrefixKeymap + "select-pane-up", "Select Pane Up", []string{"Alt-Up", "M-Up", "transport-dependent"}, "Select Pane Up"},
		{settingsActionPrefixKeymap + "select-pane-down", "Select Pane Down", []string{"Alt-Down", "M-Down", "transport-dependent"}, "Select Pane Down"},
	}
	for _, tc := range cases {
		idx := entryIndexValue(entries, tc.id)
		if idx < 0 {
			t.Fatalf("keybindings entries missing %q: %#v", tc.id, entries)
		}
		label := entries[idx].Label
		if !strings.Contains(label, tc.primaryLabel) {
			t.Fatalf("entry %q label = %q, want primary label %q", tc.id, label, tc.primaryLabel)
		}
		for _, want := range tc.wants {
			if !strings.Contains(label, want) {
				t.Fatalf("entry %q label = %q, want substring %q", tc.id, label, want)
			}
		}
		if strings.Contains(label, strings.TrimPrefix(tc.id, settingsActionPrefixKeymap)) {
			t.Fatalf("entry %q label = %q, want primary label without internal action ID", tc.id, label)
		}
		if !strings.Contains(entries[idx].SearchKey, tc.wantSearchText) {
			t.Fatalf("entry %q search key = %q, want searchable text %q", tc.id, entries[idx].SearchKey, tc.wantSearchText)
		}
	}
}

func TestSettingsHubKeybindingsPopupLocalDetailIsEditableAndTransportDetailAllowsPlainAliases(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	var popupDetail, transportDetail intpickercompat.Options
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "Sidebar:PinProject"}, nil
		case 3:
			popupDetail = options
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "previous-window"}, nil
		case 5:
			transportDetail = options
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 6:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		case 7:
			return intpickercompat.Result{}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := popupDetail.UI, "settings-keybinding-detail"; got != want {
		t.Fatalf("popup detail UI = %q, want %q", got, want)
	}
	if hasEntryLabelContaining(popupDetail.Entries, "Action ID") {
		t.Fatalf("popup detail entries = %#v, did not want internal action ID in simplified detail", popupDetail.Entries)
	}
	if !hasEntryLabelContainingAll(popupDetail.Entries, "Keybinding", "Alt-P", "M-p") {
		t.Fatalf("popup detail entries = %#v, want readable picker-local keys", popupDetail.Entries)
	}
	for _, want := range []string{"Add alias", "Reset aliases/default"} {
		if !hasEntryLabelContaining(popupDetail.Entries, want) {
			t.Fatalf("popup detail entries = %#v, want edit action %q", popupDetail.Entries, want)
		}
	}
	for _, absent := range []string{"Type key chord", "Replace primary", "Disable default", "Tier", "Delivery path"} {
		if hasEntryLabelContaining(popupDetail.Entries, absent) {
			t.Fatalf("popup detail entries = %#v, did not want %q", popupDetail.Entries, absent)
		}
	}

	if got, want := transportDetail.UI, "settings-keybinding-detail"; got != want {
		t.Fatalf("transport detail UI = %q, want %q", got, want)
	}
	if hasEntryLabelContaining(transportDetail.Entries, "Action ID") {
		t.Fatalf("transport detail entries = %#v, did not want internal action ID in simplified detail", transportDetail.Entries)
	}
	if !hasEntryLabelContainingAll(transportDetail.Entries, "Keybinding", "Alt-Shift-Left", "M-S-Left") {
		t.Fatalf("transport detail entries = %#v, want readable transport keys", transportDetail.Entries)
	}
	if !hasEntryLabelContainingAll(transportDetail.Entries, "Aliases", "(none)") {
		t.Fatalf("transport detail entries = %#v, want separate plain aliases row", transportDetail.Entries)
	}
	if !hasEntryLabelContaining(transportDetail.Entries, "Add alias") {
		t.Fatalf("transport detail entries = %#v, want additive alias action", transportDetail.Entries)
	}
	if !hasEntryLabelContaining(transportDetail.Entries, "Reset aliases") {
		t.Fatalf("transport detail entries = %#v, want reset aliases action", transportDetail.Entries)
	}
	for _, absent := range []string{"Add plain alias", "Replace primary", "Disable default", "Default transport key", "Tier", "Delivery path"} {
		if hasEntryLabelContaining(transportDetail.Entries, absent) {
			t.Fatalf("transport detail entries = %#v, did not want %q", transportDetail.Entries, absent)
		}
	}

	paneDetailEntries, _, err := cmd.keybindingDetailEntries("select-pane-left")
	if err != nil {
		t.Fatalf("keybindingDetailEntries(select-pane-left) error = %v", err)
	}
	if !hasEntryLabelContainingAll(paneDetailEntries, "Keybinding", "Alt-Left", "M-Left") {
		t.Fatalf("pane detail entries = %#v, want readable transport key", paneDetailEntries)
	}
	if !hasEntryLabelContaining(paneDetailEntries, "Add alias") {
		t.Fatalf("pane detail entries = %#v, want safe plain alias entry", paneDetailEntries)
	}
	if hasEntryLabelContaining(paneDetailEntries, "(unbound)") {
		t.Fatalf("pane detail entries = %#v, did not want unbound restored default", paneDetailEntries)
	}
}

func TestSettingsHubKeybindingsTypedTransportAliasWritesOnlyAlias(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "previous-window"}, nil
		case 3:
			if !hasEntryLabelContaining(options.Entries, "Add alias") {
				t.Fatalf("transport detail entries = %#v, want Add alias", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "previous-window:type"}, nil
		case 4:
			if got, want := options.UI, "settings-keybinding-type"; got != want {
				t.Fatalf("typed keybinding UI = %q, want %q", got, want)
			}
			if got, want := options.Prompt, "Add alias > "; got != want {
				t.Fatalf("typed keybinding prompt = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Query: "M-["}, nil
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
	})

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	keymap := readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if !strings.Contains(keymap, "[bindings.previous-window]\nkeys = [\"M-[\"]\n") {
		t.Fatalf("keymap = %q, want transport alias only", keymap)
	}
	if strings.Contains(keymap, "M-S-Left") {
		t.Fatalf("keymap = %q, did not want transport default stored as alias", keymap)
	}
}

func TestSettingsHubKeybindingsTypedPopupLocalAliasWritesQuotedKeymap(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "Sidebar:PinProject"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "Sidebar:PinProject:type"}, nil
		case 4:
			if got, want := options.UI, "settings-keybinding-type"; got != want {
				t.Fatalf("typed keybinding UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Query: "p"}, nil
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
	})

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	keymap := readFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"))
	if !strings.Contains(keymap, "[bindings.\"Sidebar:PinProject\"]\nkeys = [\"M-p\", \"p\"]\n") {
		t.Fatalf("keymap = %q, want quoted picker-local keys array", keymap)
	}
}

func TestSettingsHubKeybindingsPopupLocalConflictIsRejected(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "Sidebar:PinProject"}, nil
		case 3:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "Sidebar:PinProject:type"}, nil
		case 4:
			return intpickercompat.Result{Key: "enter", Query: "C-x"}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `key "C-x" is bound to both Sidebar:PinProject and Sidebar:KillSession in Sidebar`) {
		t.Fatalf("Run() error = %v, want same-surface conflict", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "projmux", "keymap.toml")); !os.IsNotExist(err) {
		t.Fatalf("keymap stat error = %v, want no invalid keymap written", err)
	}
}

func TestKeyBindingDisplayNameSeparatesUserLabelFromInternalID(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	cases := map[string]string{
		"ProjectSidebarToggle":      "Toggle Project Sidebar",
		"NotifySidebarToggle":       "Toggle Notify Sidebar",
		"SessionPopupToggle":        "Toggle Session Popup",
		"AISplitPickerToggle":       "Toggle AI Split Picker Popup",
		"SettingsToggle":            "Toggle Settings",
		"ProjectSwitcherToggle":     "Toggle Project Switcher",
		"Sidebar:PinProject":        "Project Sidebar: Pin Project",
		"Sidebar:KillSession":       "Project Sidebar: Kill Session",
		"SessionPopup:KillSession":  "Session Popup: Kill Session",
		"NotifySidebar:Ack":         "Notify Sidebar: Acknowledge",
		"NotifySidebar:ClearAll":    "Notify Sidebar: Clear All",
		"NotifySidebar:FocusAndAck": "Notify Sidebar: Focus and Acknowledge",
		"Settings:SwitchTabPrev":    "Previous Settings Tab",
		"rename-window":             "Rename Window",
		"current-project-session":   "Current Project Session",
		"ai-split-right":            "Open/Focus AI Split Right",
		"ai-split-down":             "Open/Focus AI Split Down",
	}
	for id, want := range cases {
		action, ok := keyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("catalog missing %q", id)
		}
		if got := keyBindingDisplayName(action); got != want {
			t.Fatalf("keyBindingDisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestKeyBindingDisplayNameKeepsLaunchToggleLabelsHumanReadable(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	cases := map[string]string{
		"ProjectSidebarToggle": "Toggle Project Sidebar",
		"NotifySidebarToggle":  "Toggle Notify Sidebar",
		"SessionPopupToggle":   "Toggle Session Popup",
	}
	for id, want := range cases {
		action, ok := keyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("catalog missing %q", id)
		}
		got := keyBindingDisplayName(action)
		if got != want {
			t.Fatalf("keyBindingDisplayName(%q) = %q, want %q", id, got, want)
		}
		if strings.Contains(got, ":") {
			t.Fatalf("launch toggle label %q should stay human-readable without a surface prefix", got)
		}
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
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "ProjectSidebarToggle"}, nil
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
	if !strings.Contains(keymap, "[bindings.ProjectSidebarToggle]\nkeys = [\"M-1\", \"M-a\"]\n") {
		t.Fatalf("keymap = %q, want custom keys binding", keymap)
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

func TestSettingsHubKeybindingsDirectActionsHideTypedFallback(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	entries, _, err := cmd.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatalf("keybindingDetailEntries() error = %v", err)
	}
	if !hasEntryLabelContaining(entries, "Add alias") {
		t.Fatalf("detail entries = %#v, want Add alias", entries)
	}
	for _, absent := range []string{"Type key chord", "Replace primary", "Disable default"} {
		if hasEntryLabelContaining(entries, absent) {
			t.Fatalf("detail entries = %#v, did not want %q", entries, absent)
		}
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
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "ProjectSidebarToggle"}, nil
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
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "ProjectSidebarToggle"}, nil
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

func TestSettingsHubKeybindingsDoesNotExposeDisableDefault(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{}
	entries, _, err := cmd.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatalf("keybindingDetailEntries() error = %v", err)
	}
	if hasEntryLabelContaining(entries, "Disable default") {
		t.Fatalf("detail entries = %#v, did not want Disable default", entries)
	}
	if op, ok := parseKeymapDetailAction(settingsActionPrefixKeymap+"sessionizer-sidebar:disable", "ProjectSidebarToggle"); ok || op != "" {
		t.Fatalf("parse disable = %q, %v; want rejected", op, ok)
	}
}

func TestSettingsHubKeybindingsResetRemovesOverride(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.ProjectSidebarToggle]\nplain = \"M-a\"\n")

	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "ProjectSidebarToggle"}, nil
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
	if strings.Contains(keymap, "[bindings.ProjectSidebarToggle]") || strings.Contains(keymap, "plain =") {
		t.Fatalf("keymap = %q, want override removed", keymap)
	}
}

func TestSettingsHubKeybindingsInvalidKeymapShowsErrorRow(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.ProjectSidebarToggle]\nplain = \"M-a\" # ok\nplain = \"M-b\"\n")

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
	if hasEntryValue(keybindingOptions.Entries, settingsActionPrefixKeymap+"ProjectSidebarToggle") {
		t.Fatalf("keybindings entries = %#v, want no editable action rows when parse failed", keybindingOptions.Entries)
	}
}

func TestSettingsKeybindingsLegacyModeOptionsReturnRootList(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), "[bindings.ProjectSidebarToggle]\nplain = \"M-a\"\n")
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		return intpickercompat.Result{}, nil
	})
	cmd.lookupEnv = func(name string) string {
		if name == "TERM_PROGRAM" {
			return "ghostty"
		}
		return ""
	}

	listOptions := cmd.keybindingsOptions(settingsKeybindingsDiagnostic)
	if got, want := listOptions.UI, "settings-keybindings"; got != want {
		t.Fatalf("keybindings UI = %q, want %q", got, want)
	}
	if hasEntryLabelContaining(listOptions.Entries, "Ghostty") {
		t.Fatalf("keybindings entries = %#v, did not want diagnostic terminal rows", listOptions.Entries)
	}
	if !hasEntryValue(listOptions.Entries, settingsActionPrefixKeymap+"ProjectSidebarToggle") {
		t.Fatalf("keybindings entries = %#v, want root action list", listOptions.Entries)
	}
	if !hasEntryLabelContaining(listOptions.Entries, "Toggle Project Sidebar") {
		t.Fatalf("keybindings entries = %#v, want readable action label", listOptions.Entries)
	}
	if !hasEntryLabelContaining(listOptions.Entries, "keys Alt-A (M-a) (custom)") {
		t.Fatalf("keybindings entries = %#v, want custom plain summary", listOptions.Entries)
	}
}

func TestSettingsKeybindingsRejectsLegacyModeActions(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsSectionKeybindings}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsKeybindingsDiagnostic}, nil
		default:
			t.Fatalf("unexpected settings picker call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unknown keybinding settings action: "+settingsKeybindingsDiagnostic) {
		t.Fatalf("Run() error = %v, want rejected legacy mode action", err)
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

func TestSettingsKeybindingsDoesNotExposeTerminalMappingRows(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testKeybindingSettingsCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		return intpickercompat.Result{}, nil
	})
	cmd.lookupEnv = func(name string) string {
		if name == "TERM_PROGRAM" {
			return "ghostty"
		}
		return ""
	}

	options := cmd.keybindingsOptions(settingsKeybindingsInit)
	for _, absent := range []string{"Terminal", "Preview terminal mappings", "Apply terminal mappings", "projmux init"} {
		if hasEntryLabelContaining(options.Entries, absent) {
			t.Fatalf("keybindings entries = %#v, did not want %q", options.Entries, absent)
		}
	}
	if !hasEntryValue(options.Entries, settingsActionPrefixKeymap+"ProjectSidebarToggle") {
		t.Fatalf("keybindings entries = %#v, want root action list", options.Entries)
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

	var aboutOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}},
		{observe: func(o intpickercompat.Options) { aboutOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsNoopValue}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-about"; got != want {
				t.Fatalf("settings about UI after noop = %q, want %q", got, want)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		update:       update,
		runner:       runner,
		nativePicker: native,
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
	if got, want := aboutOptions.Footer, "Enter: action  |  Back row: parent"; got != want {
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
	if !hasEntryValue(aboutOptions.Entries, settingsQuitOpen) {
		t.Fatalf("settings about entries = %#v, want quit action", aboutOptions.Entries)
	}
	for _, want := range []string{
		"projmux " + version.String(),
		"https://github.com/crevissepartners/projmux",
		"Update Now",
		"Check Updates",
		"Quit projmux",
		latest,
		"update_available",
		"Installer",
		"Installed with Go tooling",
		"https://github.com/crevissepartners/projmux/releases/tag/" + latest,
		"sidebar, sessions, projects",
		"new window, rename window/pane",
		"try shortcuts in projmux shell",
		"projmux setup reports swallowed shortcuts",
		"projmux init previews supported terminal key delivery mappings",
		"projmux doctor checks tmux",
		"configure a plain alias",
		"Alt Meta defaults",
		"tmux/meta sequences",
		"docs/keybindings.md",
	} {
		if !hasEntryLabelContaining(aboutOptions.Entries, want) {
			t.Fatalf("settings about entries = %#v, want label containing %q", aboutOptions.Entries, want)
		}
	}
}

func TestSettingsAboutQuitRowRoutesThroughQuitPicker(t *testing.T) {
	t.Parallel()

	tmuxRunner := &recordingTmuxRunner{}
	var quitOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsQuitOpen}},
		{observe: func(o intpickercompat.Options) { quitOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: quitActionCancel}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		{reply: intpickercompat.Result{}},
	})
	quit := &quitCommand{
		runner:       tmuxRunner,
		nativePicker: native,
	}
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		update:       nil,
		quit:         quit,
		runner:       runner,
		nativePicker: native,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if quitOptions.UI != "quit" {
		t.Fatalf("quit options UI = %q, want quit", quitOptions.UI)
	}
	if !hasEntryValue(quitOptions.Entries, quitActionQuit) || !hasEntryValue(quitOptions.Entries, quitActionCancel) {
		t.Fatalf("quit options entries = %#v, want quit and cancel actions", quitOptions.Entries)
	}
	if len(tmuxRunner.calls) != 0 {
		t.Fatalf("about quit row caused tmux calls before explicit quit selection: %#v", tmuxRunner.calls)
	}
}

func TestSettingsAboutWelcomeOpensVisibleViewer(t *testing.T) {
	t.Parallel()

	tmuxRunner := &recordingTmuxRunner{}
	var welcomeOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWelcomeShow}},
		{observe: func(o intpickercompat.Options) { welcomeOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsNoopValue}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-about-welcome"; got != want {
				t.Fatalf("welcome viewer UI after noop = %q, want %q", got, want)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		{reply: intpickercompat.Result{}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		update:       nil,
		runner:       runner,
		nativePicker: native,
		lookupEnv:    func(string) string { return "" },
		tmuxRunner:   tmuxRunner,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := welcomeOptions.UI, "settings-about-welcome"; got != want {
		t.Fatalf("welcome viewer UI = %q, want %q", got, want)
	}
	if got, want := welcomeOptions.Title, "About - Welcome"; got != want {
		t.Fatalf("welcome viewer title = %q, want %q", got, want)
	}
	if got, want := welcomeOptions.Prompt, "Settings > About > Welcome > "; got != want {
		t.Fatalf("welcome viewer prompt = %q, want %q", got, want)
	}
	if !welcomeOptions.DisableSearch {
		t.Fatalf("welcome viewer DisableSearch = false, want navigation-only viewer")
	}
	if !hasEntryValue(welcomeOptions.Entries, settingsBackValue) {
		t.Fatalf("welcome viewer entries = %#v, want back row", welcomeOptions.Entries)
	}
	if !hasEntryLabelContaining(welcomeOptions.Entries, "Welcome to projmux shell") {
		t.Fatalf("welcome viewer entries = %#v, want welcome payload", welcomeOptions.Entries)
	}
	if !hasEntryLabelContaining(welcomeOptions.Entries, "Enter continues for this run") {
		t.Fatalf("welcome viewer entries = %#v, want shell prompt guidance", welcomeOptions.Entries)
	}
	if len(tmuxRunner.calls) != 0 {
		t.Fatalf("tmux calls = %#v, want Settings-native welcome viewer without nested popup", tmuxRunner.calls)
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

	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}},
		{observe: func(o intpickercompat.Options) {
			if !hasEntryValue(o.Entries, settingsUpdateApply) {
				t.Fatalf("settings about entries = %#v, want update apply action", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsUpdateApply}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		update:       update,
		runner:       runner,
		nativePicker: native,
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

	var refreshedAbout intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionAbout}},
		{observe: func(o intpickercompat.Options) {
			if !hasEntryValue(o.Entries, settingsUpdateCheck) {
				t.Fatalf("settings about entries = %#v, want update check action", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsUpdateCheck}},
		{observe: func(o intpickercompat.Options) { refreshedAbout = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		update:       update,
		runner:       runner,
		nativePicker: native,
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
	app := filepath.Join(home, "source", "repos", "app")
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{observe: func(o intpickercompat.Options) {
			if hasEntryValue(o.Entries, settingsProjectAdd) {
				t.Fatalf("project settings entries = %#v, want Add Project moved out of root", o.Entries)
			}
			if !hasEntryValue(o.Entries, settingsProjectPins) {
				t.Fatalf("project settings entries = %#v, want Pinned Projects", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsProjectPins}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-project-pins"; got != want {
				t.Fatalf("pinned projects UI = %q, want %q", got, want)
			}
			if got := entryIndexValue(o.Entries, settingsProjectAdd); got != 1 {
				t.Fatalf("pinned project entries = %#v, want Add Project at index 1, got %d", o.Entries, got)
			}
			if got := entryIndexLabelContaining(o.Entries, "Add Current Project"); got != 2 {
				t.Fatalf("pinned project entries = %#v, want Add Current Project at index 2, got %d", o.Entries, got)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsProjectAdd}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-project-add"; got != want {
				t.Fatalf("add project UI = %q, want %q", got, want)
			}
			if !hasEntryValue(o.Entries, settingsActionPrefixSwitch+"add:"+app) {
				t.Fatalf("add project entries = %#v, want scanned app", o.Entries)
			}
			if !hasEntryValue(o.Entries, settingsActionPrefixSwitch+"add:"+filepath.Join(home, ".config")) {
				t.Fatalf("add project entries = %#v, want hidden whitelist entry", o.Entries)
			}
			if hasEntryValue(o.Entries, settingsActionPrefixSwitch+"add:"+filepath.Join(home, ".cache")) {
				t.Fatalf("add project entries = %#v, want hidden non-whitelist skipped", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "add:" + app}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsProjectPins}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-project-pins"; got != want {
				t.Fatalf("pinned projects UI = %q, want %q", got, want)
			}
			if !hasEntryValue(o.Entries, settingsActionPrefixSwitch+"clear") {
				t.Fatalf("pinned project entries = %#v, want clear", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsActionPrefixSwitch + "pin:" + pin}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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

	appPath := filepath.Join(home, "source", "repos", "app")
	addAction := settingsActionPrefixWorkdir + "add:" + appPath
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{observe: func(o intpickercompat.Options) {
			if hasEntryValue(o.Entries, settingsWorkdirAdd) {
				t.Fatalf("project settings entries = %#v, want Add Workdir moved out of root", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-workdirs"; got != want {
				t.Fatalf("workdirs list UI = %q, want %q", got, want)
			}
			if got := entryIndexValue(o.Entries, settingsWorkdirAdd); got < 0 {
				t.Fatalf("workdirs list entries = %#v, want Add Workdir row", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-workdir-add"; got != want {
				t.Fatalf("add workdir UI = %q, want %q", got, want)
			}
			if !hasEntryValue(o.Entries, addAction) {
				t.Fatalf("add workdir entries = %#v, want value %q", o.Entries, addAction)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: addAction}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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

	removeAction := settingsActionPrefixWorkdir + "remove:" + target
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-workdirs"; got != want {
				t.Fatalf("workdirs list UI = %q, want %q", got, want)
			}
			if !hasEntryValue(o.Entries, removeAction) {
				t.Fatalf("workdirs list entries = %#v, want %q", o.Entries, removeAction)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: removeAction}},
		// After remove, list should be empty (just back + placeholder).
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}},
		{observe: func(o intpickercompat.Options) { addOptions = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}},
		{observe: func(o intpickercompat.Options) {
			if !hasEntryValue(o.Entries, settingsWorkdirTyped) {
				t.Fatalf("add workdir entries = %#v, want typed row", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirTyped}},
		{observe: func(o intpickercompat.Options) { typedOptions = o },
			reply: intpickercompat.Result{Key: "enter", Query: typed}},
		// After typed flow returns, the workdirs list reopens. Close it.
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		// After typed flow returns, the project picker reopens. Close it.
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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

	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirList}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirAdd}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsWorkdirTyped}},
		{reply: intpickercompat.Result{Key: "enter", Query: "relative/path"}},
		// After typed-flow falls back, settings should return to the
		// workdirs list. Close it.
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		// After typed-flow falls back, settings should return to the
		// project picker section. Close to terminate the run.
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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

	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionAI}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings"; got != want {
				t.Fatalf("settings UI after back = %q, want %q", got, want)
			}
		}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		runner:       runner,
		nativePicker: native,
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

	runner, native := scriptedPicker(t, nil)
	return &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{filepath.Join(home, "source", "repos", "app")}, nil
		},
		pinStore:     func() (switchPinStore, error) { return store, nil },
		runner:       runner,
		nativePicker: native,
		sessions:     &capturingSwitchSessionExecutor{},
		identity:     stubSwitchIdentityResolver{name: "app"},
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return home, nil },
		workingDir:   func() (string, error) { return filepath.Join(home, "source", "repos", "app"), nil },
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
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{observe: func(o intpickercompat.Options) {
			if !hasEntryValue(o.Entries, settingsProjectRootManage) {
				t.Fatalf("project picker entries = %#v, want project root management row", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}},
		{observe: func(o intpickercompat.Options) {
			if got, want := o.UI, "settings-project-root"; got != want {
				t.Fatalf("project root UI = %q, want %q", got, want)
			}
			if got, want := o.Title, "Project Root - Effective and saved root"; got != want {
				t.Fatalf("project root title = %q, want %q", got, want)
			}
			if got := o.Header; got != "" {
				t.Fatalf("project root header = %q, want description only in title", got)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsProjdirSetTyped}},
		{observe: func(o intpickercompat.Options) { typedOptions = o },
			reply: intpickercompat.Result{Key: "enter", Query: target}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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

	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}},
		{observe: func(o intpickercompat.Options) {
			if !hasEntryLabelContaining(o.Entries, "Use Current Project as Root") {
				t.Fatalf("project root entries = %#v, want current project action", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsProjdirSetCurrent}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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

	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: settingsSectionProject}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsProjectRootManage}},
		{observe: func(o intpickercompat.Options) {
			if !hasEntryLabelContaining(o.Entries, "/saved/root") {
				t.Fatalf("project root entries = %#v, want saved value", o.Entries)
			}
		}, reply: intpickercompat.Result{Key: "enter", Value: settingsProjdirClear}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	cmd := &settingsCommand{
		ai:           testAICommand(t.TempDir()),
		switcher:     switcher,
		runner:       runner,
		nativePicker: native,
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

func settingsEntryLabelsText(entries []intpickercompat.Entry) string {
	labels := make([]string, 0, len(entries))
	for _, entry := range entries {
		labels = append(labels, entry.Label)
	}
	return strings.Join(labels, "\n")
}

func hasEntryLabelContainingAll(entries []intpickercompat.Entry, values ...string) bool {
	for _, entry := range entries {
		matches := true
		for _, value := range values {
			if !strings.Contains(entry.Label, value) {
				matches = false
				break
			}
		}
		if matches {
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

func hasRecordedTmuxCall(calls []recordedTmuxCall, want recordedTmuxCall) bool {
	for _, call := range calls {
		if call.name == want.name && reflect.DeepEqual(call.args, want.args) {
			return true
		}
	}
	return false
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

// pickerStep scripts a single picker invocation: observe the incoming
// options (optional) then return the next reply.
type pickerStep struct {
	observe func(intpickercompat.Options)
	reply   intpickercompat.Result
	err     error
}

// scriptedPicker returns a (runner, nativePicker) pair backed by a single
// step list. It collapses the previously doubled lambda body that tests
// used to populate both fields with the same call-counting switch.
func scriptedPicker(t *testing.T, steps []pickerStep) (switchRunner, intpicker.Runner) {
	t.Helper()
	var calls int
	fn := func(options intpickercompat.Options) (intpickercompat.Result, error) {
		idx := calls
		calls++
		if idx >= len(steps) {
			return intpickercompat.Result{}, nil
		}
		s := steps[idx]
		if s.observe != nil {
			s.observe(options)
		}
		return s.reply, s.err
	}
	runner := switchRunnerFunc(fn)
	return runner, nativePickerFromCompatRunner(runner)
}
