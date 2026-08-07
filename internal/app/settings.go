package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/i18n"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/platformkeys"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

type settingsCommand struct {
	ai                       settingsAI
	switcher                 settingsSwitcher
	update                   updateRunner
	quit                     quitRunner
	newAI                    func() aiHookSettingsReader
	runner                   intpickercompat.Runner
	nativePicker             intpicker.Runner
	homeDir                  func() (string, error)
	lookupEnv                func(string) string
	osStat                   func(string) (os.FileInfo, error)
	runCommand               func(name string, args ...string) error
	runOutput                func(name string, args ...string) ([]byte, error)
	tmuxRunner               tmuxRunner
	probeKeybinding          func(probeKey, time.Duration) (probeResult, error)
	nativeKeyCapture         func(context.Context) (string, bool, error)
	physicalCaptureAvailable func() bool
	aiNotifyDiagnostics      func() []doctorAINotifyIntegration
}

var errSettingsClosed = errors.New("settings closed")

func newSettingsCommand(ai *aiCommand, switcher *switchCommand, update *updateCommand, quit *quitCommand) *settingsCommand {
	c := &settingsCommand{
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		homeDir:      os.UserHomeDir,
		lookupEnv:    os.Getenv,
		osStat:       os.Stat,
		runCommand: func(name string, args ...string) error {
			return exec.Command(name, args...).Run()
		},
		runOutput: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		},
		tmuxRunner:       inttmux.ExecRunner{},
		nativeKeyCapture: platformkeys.CaptureModifiedChord,
	}
	// The concrete commands satisfy the settings role interfaces structurally.
	// Guard the nil pointers so the `c.<dep> == nil` checks keep their
	// pre-interface semantics (a nil *T stored in an interface is non-nil).
	if ai != nil {
		c.ai = ai
	}
	if switcher != nil {
		c.switcher = switcher
	}
	if update != nil {
		c.update = update
	}
	if quit != nil {
		c.quit = quit
	}
	c.newAI = func() aiHookSettingsReader {
		return newSettingsAIFallback(c.homeDir, c.lookupEnv)
	}
	return c
}

// statFile checks paths via the injected osStat seam, falling back to os.Stat
// for struct-literal constructions that leave the field nil.
func (c *settingsCommand) statFile(path string) (os.FileInfo, error) {
	if c.osStat != nil {
		return c.osStat(path)
	}
	return os.Stat(path)
}

func (c *settingsCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		printSettingsUsage(stderr)
		return errors.New("settings does not accept positional arguments")
	}
	if c.nativePicker == nil {
		return errors.New("native picker is not configured")
	}
	defer applyNativeUIThemeFromConfig(c.homeDir, c.lookupEnv, c.resolveSettingsProjectContext().Path)()

	tab := settingsRootTabGlobal
	for {
		result, err := c.runPicker(c.rootOptions(tab))
		if err != nil {
			if errors.Is(err, errSettingsClosed) {
				return nil
			}
			return err
		}
		if next, ok := settingsRootTabFromResultWithCurrent(result, tab); ok {
			ctx := c.resolveSettingsProjectContext()
			if next == settingsRootTabProject && !ctx.hasProject() {
				// Single-tab environment (no project context): the Project
				// chip renders disabled and Alt-Shift-Left/Alt-Shift-Right
				// (or a chip click) is a no-op. We stay on the current tab
				// rather than navigating into an empty Project scope.
				tab = settingsRootTabGlobal
				continue
			}
			tab = next
			continue
		}
		section := strings.TrimSpace(result.Value)
		if result.Key != "enter" || section == "" {
			return nil
		}
		if section == settingsNoopValue {
			continue
		}

		if err := c.runSection(section, stdout, stderr); err != nil {
			if errors.Is(err, errSettingsClosed) {
				return nil
			}
			return err
		}
	}
}

func (c *settingsCommand) runSection(section string, stdout, stderr io.Writer) error {
	if section == settingsSectionProject {
		return c.runProjectPickerSection(stdout, stderr)
	}
	if section == settingsSectionProjectHooks {
		return c.runProjectHooksSection(stdout, stderr)
	}
	if section == settingsSectionGlobalHooks {
		return c.runGlobalHooksSection(stdout, stderr)
	}
	if section == settingsSectionProjectConfig {
		return c.runProjectConfigSection(stdout, stderr)
	}
	if section == settingsSectionProjectTrust {
		return c.runProjectTrustSection(stdout, stderr)
	}
	if section == settingsSectionEffectiveMerge {
		return c.runEffectiveMergeSection(stdout, stderr)
	}
	if section == settingsSectionGlobalTheme {
		return c.runThemeSection(stdout, stderr)
	}
	if section == settingsSectionProjectSessionState {
		return c.runProjectSessionStateSection(stdout, stderr)
	}
	if section == settingsSectionAI {
		return c.runAISection(stdout, stderr)
	}
	if section == settingsSectionNotifications {
		return c.runNotificationsSection(stdout, stderr)
	}
	if section == settingsSectionKeybindings {
		return c.runKeybindingsSection(stdout, stderr)
	}
	if section == settingsSectionSessionState {
		return c.runSessionStateSection(stdout, stderr)
	}
	if section == settingsSectionStatusbar {
		return c.runAppearanceSection(stdout, stderr)
	}
	if section == settingsSectionLabs {
		return c.runLabsSection(stdout, stderr)
	}

	for {
		options, err := c.sectionOptions(section)
		if err != nil {
			printSettingsUsage(stderr)
			return err
		}
		result, err := c.runPicker(options)
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) runPicker(options intpickercompat.Options) (intpickercompat.Result, error) {
	options = c.withSettingsScopeTabs(options)
	options = c.localizeSettingsOptions(options)
	if options.Theme == nil {
		if source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, c.resolveSettingsProjectContext().Path); err == nil {
			options = source.pickerCompatOptions(options)
		}
	}
	result, err := runPickerOptionBackend(c.homeDir, c.lookupEnv, c.nativePicker, c.runner, options)
	if err != nil {
		if isNoSelectionExit(err) {
			return intpickercompat.Result{}, errSettingsClosed
		}
		return intpickercompat.Result{}, fmt.Errorf("run settings picker: %w", err)
	}
	return result, nil
}

// localizeSettingsOptions resolves the Settings-scoped locale (which honors the
// global config override via c.homeDir) and then delegates the field
// translation to the shared picker choke point. Because options.Locale is set
// here, the choke point reuses this locale rather than re-resolving from the
// environment, and translation is idempotent so there is no double-translation.
func (c *settingsCommand) localizeSettingsOptions(options intpickercompat.Options) intpickercompat.Options {
	options.Locale = c.locale()
	return localizePickerOptions(c.homeDir, c.lookupEnv, options)
}

func (c *settingsCommand) locale() i18n.Locale {
	return appLocale(c.homeDir, c.lookupEnv)
}

func (c *settingsCommand) backEntry() intpickercompat.Entry {
	return settingsBackEntryLocale(c.locale())
}

func (c *settingsCommand) rowLabel(glyph, color, name, description string) string {
	return settingsLabelLocale(c.locale(), glyph, color, name, description)
}

func (c *settingsCommand) rowLabelDim(name, description string) string {
	return settingsLabelDimLocale(c.locale(), name, description)
}

func (c *settingsCommand) rowLabelInfo(name, value, source string) string {
	return settingsLabelInfoLocale(c.locale(), name, value, source)
}

func (c *settingsCommand) withSettingsScopeTabs(options intpickercompat.Options) intpickercompat.Options {
	if !strings.HasPrefix(strings.TrimSpace(options.UI), "settings") || len(options.TitleChips) != 0 {
		return options
	}
	active := settingsRootTabGlobal
	if strings.HasPrefix(strings.TrimSpace(options.Prompt), "Settings > Project >") || strings.HasPrefix(strings.TrimSpace(options.UI), "settings-project") {
		active = settingsRootTabProject
	}
	options.TitleChips = settingsPassiveRootTabChipsLocale(active, c.resolveSettingsProjectContext().hasProject(), c.locale())
	return options
}

func (c *settingsCommand) execute(value string, stdout, stderr io.Writer) error {
	switch {
	case strings.HasPrefix(value, settingsActionPrefixAI):
		mode := strings.TrimPrefix(value, settingsActionPrefixAI)
		if c.ai == nil {
			return errors.New("ai settings are not configured")
		}
		return c.ai.setMode(mode)
	case strings.HasPrefix(value, settingsActionPrefixDesktopNotifyMode):
		return c.setDesktopNotifyMode(strings.TrimPrefix(value, settingsActionPrefixDesktopNotifyMode))
	case strings.HasPrefix(value, settingsActionPrefixHooks):
		return c.setProjectHooksMode(strings.TrimPrefix(value, settingsActionPrefixHooks))
	case strings.HasPrefix(value, settingsActionPrefixPicker):
		return c.setPickerBackend(strings.TrimPrefix(value, settingsActionPrefixPicker))
	case strings.HasPrefix(value, settingsActionPrefixProjdir):
		action := strings.TrimPrefix(value, settingsActionPrefixProjdir)
		if c.switcher == nil {
			return errors.New("project root settings are not configured")
		}
		return c.switcher.executeProjdirSettingsAction(action, stdout, stderr)
	case strings.HasPrefix(value, settingsActionPrefixSessionState):
		return c.executeSessionStateAction(strings.TrimPrefix(value, settingsActionPrefixSessionState), stdout, stderr)
	case strings.HasPrefix(value, settingsActionPrefixStatusbar):
		return c.setStatusbarDecoration(strings.TrimPrefix(value, settingsActionPrefixStatusbar))
	case strings.HasPrefix(value, settingsActionPrefixSwitch):
		action := strings.TrimPrefix(value, settingsActionPrefixSwitch)
		if c.switcher == nil {
			return errors.New("project picker settings are not configured")
		}
		return c.switcher.executeSettingsAction(action, stdout, stderr)
	case strings.HasPrefix(value, settingsActionPrefixUpdate):
		if c.update == nil {
			return errors.New("update settings are not configured")
		}
		action := strings.TrimPrefix(value, settingsActionPrefixUpdate)
		switch action {
		case "apply":
			// Surface success/failure inline and keep Settings open rather
			// than closing the whole UI on a handled update error (e.g. a
			// source/unknown install that cannot auto-update). The Installer
			// row already carries the manual guidance.
			if err := c.update.Run([]string{"apply"}, stdout, stderr); err != nil {
				_, _ = fmt.Fprintf(stdout, "Update failed: %v\n", err)
				return nil
			}
			_, _ = fmt.Fprintln(stdout, "Update complete. Restart projmux to run the new version.")
			return nil
		case "check":
			if err := c.update.Run([]string{"check"}, stdout, stderr); err != nil {
				_, _ = fmt.Fprintf(stdout, "Update check failed: %v\n", err)
				return nil
			}
			return nil
		default:
			return fmt.Errorf("unknown update settings action: %s", action)
		}
	case strings.HasPrefix(value, settingsActionPrefixWelcome):
		switch strings.TrimPrefix(value, settingsActionPrefixWelcome) {
		case "show":
			return c.runWelcomeSettingsViewer()
		default:
			return fmt.Errorf("unknown welcome settings action: %s", value)
		}
	case strings.HasPrefix(value, settingsActionPrefixQuit):
		if c.quit == nil {
			return errors.New("quit settings are not configured")
		}
		switch strings.TrimPrefix(value, settingsActionPrefixQuit) {
		case "open":
			return c.quit.Run(nil, stdout, stderr)
		default:
			return fmt.Errorf("unknown quit settings action: %s", value)
		}
	case strings.HasPrefix(value, settingsActionPrefixWorkdir):
		action := strings.TrimPrefix(value, settingsActionPrefixWorkdir)
		if c.switcher == nil {
			return errors.New("project picker settings are not configured")
		}
		return c.switcher.executeWorkdirSettingsAction(action, stdout, stderr)
	default:
		printSettingsUsage(stderr)
		return fmt.Errorf("unknown settings action: %s", value)
	}
}

func (c *settingsCommand) runWelcomeSettingsViewer() error {
	for {
		result, err := c.runPicker(c.welcomeSettingsViewerOptions())
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		default:
			return fmt.Errorf("unknown welcome viewer action: %s", action)
		}
	}
}

func (c *settingsCommand) welcomeSettingsViewerOptions() intpickercompat.Options {
	status, hasStatus := resolveWelcomeUpdateStatusFrom(c.update)
	var body strings.Builder
	locale := appLocale(c.homeDir, c.lookupEnv)
	_ = writeShellWelcome(&body, welcomeCurrentVersion(), status, hasStatus, false, false, false, welcomeWidthFromEnv(c.lookupEnv), locale)
	entries := []intpickercompat.Entry{c.backEntry()}
	for line := range strings.SplitSeq(strings.Trim(body.String(), "\n"), "\n") {
		entries = append(entries, intpickercompat.Entry{
			Label:     line,
			Value:     settingsNoopValue,
			SearchKey: "welcome shell bootstrap update skip",
		})
	}
	return intpickercompat.Options{
		UI:            "settings-about-welcome",
		Entries:       entries,
		Title:         settingsCatalogTextLocale(locale, "About") + " - " + settingsCatalogTextLocale(locale, "Welcome"),
		Prompt:        settingsCatalogTextLocale(locale, "Settings") + " > " + settingsCatalogTextLocale(locale, "About") + " > " + settingsCatalogTextLocale(locale, "Welcome") + " > ",
		Footer:        projmuxFooter("Back row: About  |  Esc: close settings"),
		DisableSearch: true,
		Bindings:      c.settingsCloseBindings(),
	}
}

func settingsBackEntry() intpickercompat.Entry {
	return settingsBackEntryLocale(settingsLocale())
}

func settingsBackEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	return intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphBack, settingsColorBack, "Back", ""),
		Value: settingsBackValue,
	}
}

func (c *settingsCommand) settingsCloseBindings() []string {
	if c == nil {
		return settingsCloseBindings()
	}
	return pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, "ai-split-settings", "esc", "ctrl-c", "ctrl-alt-s")
}

func settingsCloseBindings() []string {
	return pickerCloseBindingsForPopupToggleMode(nil, nil, "ai-split-settings", "esc", "ctrl-c", "ctrl-alt-s")
}

func printSettingsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux settings")
}
