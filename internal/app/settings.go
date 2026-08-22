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

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/platformkeys"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/version"
)

type settingsCommand struct {
	sessionStateDiagnostics  *diagnostics.SessionStateRecorder
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
	preferNativeKeyCapture   func() bool
	nativeKeyCaptureGrace    time.Duration
	physicalCaptureAvailable func() bool
	aiNotifyDiagnostics      func() []doctorAINotifyIntegration
	appServerHealth          func(hookAvailable bool) codexappserver.Health
	// resourceRegistry is the read-only Registry projection the Project surfaces
	// display. It is a seam so a fixture can declare one instead of reaching for
	// whatever Registry the host machine has.
	resourceRegistry func() (coremetadata.Registry, error)
	feedback         *settingsFeedback
}

type settingsFeedback struct {
	Summary string
	Detail  string
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
		tmuxRunner:             inttmux.ExecRunner{},
		nativeKeyCapture:       platformkeys.CaptureModifiedChord,
		preferNativeKeyCapture: platformkeys.Available,
		nativeKeyCaptureGrace:  100 * time.Millisecond,
		appServerHealth: func(hookAvailable bool) codexappserver.Health {
			health, _ := codexappserver.EnsureDefaultProxyReady(context.Background(), codexappserver.TriggerSettings, version.String(), hookAvailable)
			return health
		},
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
	if section == settingsSectionAutomation {
		return c.runAutomationSection(stdout, stderr)
	}
	if section == settingsSectionProjectAutomation {
		return c.runProjectAutomationSection(stdout, stderr)
	}
	if section == settingsSectionProjectHooks {
		return c.runProjectHooksSection(stdout, stderr)
	}
	if section == settingsSectionProjectTrust {
		return c.runProjectTrustSection(stdout, stderr)
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
	if section == settingsSectionAbout {
		return c.runAboutSection(stdout, stderr)
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
		if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) runPicker(options intpickercompat.Options) (intpickercompat.Result, error) {
	options = c.withSettingsFeedback(options)
	options = c.withSettingsScopeTabs(options)
	options = c.withSettingsClosePolicy(options)
	options = c.localizeSettingsOptions(options)
	if err := validateSettingsEntryContracts(options); err != nil {
		return intpickercompat.Result{}, err
	}
	if options.Theme == nil {
		if source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, c.resolveSettingsProjectContext().Path); err == nil {
			options = source.pickerCompatOptions(options)
		}
	}
	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, settingsDirectionalPickerRunner{next: c.nativePicker}, options)
	if err != nil {
		if isNoSelectionExit(err) {
			return intpickercompat.Result{}, errSettingsClosed
		}
		return intpickercompat.Result{}, fmt.Errorf("run settings picker: %w", err)
	}
	c.clearSettingsFeedbackFor(strings.TrimSpace(result.Value))
	return result, nil
}

// withSettingsClosePolicy makes the command's effective SettingsToggle alias
// authoritative at every depth, including older option builders that still
// carry the fallback close bindings. Non-close bindings remain untouched.
func (c *settingsCommand) withSettingsClosePolicy(options intpickercompat.Options) intpickercompat.Options {
	bindings := make([]string, 0, len(options.Bindings)+3)
	seen := map[string]bool{}
	for _, binding := range options.Bindings {
		_, action, ok := strings.Cut(strings.TrimSpace(binding), ":")
		if ok && strings.TrimSpace(action) == "abort" {
			continue
		}
		if binding = strings.TrimSpace(binding); binding != "" && !seen[binding] {
			bindings = append(bindings, binding)
			seen[binding] = true
		}
	}
	for _, binding := range c.settingsCloseBindings() {
		if binding = strings.TrimSpace(binding); binding != "" && !seen[binding] {
			bindings = append(bindings, binding)
			seen[binding] = true
		}
	}
	options.Bindings = bindings
	return options
}

// settingsDirectionalPickerRunner adds hierarchy navigation only at the
// Settings list boundary. Query forms, the key recorder, and the color grid
// retain their native Left/Right input semantics because they are transient
// input controls rather than catalogued Settings Views.
type settingsDirectionalPickerRunner struct {
	next intpicker.Runner
}

func (r settingsDirectionalPickerRunner) Run(options intpicker.Options) (intpicker.Result, error) {
	if r.next == nil {
		return intpicker.Result{}, errors.New("native picker is not configured")
	}
	if !settingsDirectionalListView(options) {
		return r.next.Run(options)
	}

	hasParent := false
	for _, item := range options.Items {
		if strings.TrimSpace(item.Value) == settingsBackValue {
			hasParent = true
			break
		}
	}
	options.Footer = settingsDirectionalFooter(options.Locale, options.Footer, hasParent)
	options.Actions = append(options.Actions,
		intpicker.Action{
			Key:    "right",
			Intent: intpicker.ActionCustom,
			Mutate: func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
				if settingsDirectionalIntentFor(ctx.Key, ctx.Value, hasParent) != settingsDirectionalForward {
					return intpicker.DeferredUpdate{}, nil
				}
				return intpicker.DeferredUpdate{Result: &intpicker.Result{Key: "enter", Value: ctx.Value, Query: ctx.Query}}, nil
			},
		},
		intpicker.Action{
			Key:    "left",
			Intent: intpicker.ActionCustom,
			Mutate: func(ctx intpicker.ActionContext) (intpicker.DeferredUpdate, error) {
				if settingsDirectionalIntentFor(ctx.Key, ctx.Value, hasParent) != settingsDirectionalBack {
					return intpicker.DeferredUpdate{}, nil
				}
				return intpicker.DeferredUpdate{Result: &intpicker.Result{Key: "enter", Value: settingsBackValue, Query: ctx.Query}}, nil
			},
		},
	)
	return r.next.Run(options)
}

func settingsDirectionalListView(options intpicker.Options) bool {
	return strings.HasPrefix(strings.TrimSpace(options.UI), "settings") &&
		len(options.Items) > 0 && options.Recorder == nil && !options.AcceptQuery && !options.ColorGrid
}

func settingsDirectionalFooter(locale i18n.Locale, footer string, hasParent bool) string {
	hint := settingsCatalogTextLocale(locale, "→: open row")
	if hasParent {
		hint += "  |  " + settingsCatalogTextLocale(locale, "←: back")
	}
	footer = strings.TrimSpace(footer)
	if footer == "" {
		return hint
	}
	return footer + "  |  " + hint
}

// withSettingsFeedback projects the most recent handled mutation result into
// the next Settings frame. The row deliberately reuses the Phase 0 passive
// entry contract: it cannot be selected as an action and cannot fall through
// to an unknown-action branch.
func (c *settingsCommand) withSettingsFeedback(options intpickercompat.Options) intpickercompat.Options {
	if c == nil || c.feedback == nil || !strings.HasPrefix(strings.TrimSpace(options.UI), "settings") {
		return options
	}
	entry := intpickercompat.Entry{
		Label:     settingsLabelInfoLocale(c.locale(), "Feedback", settingsFeedbackSummaryLocale(c.locale(), c.feedback.Summary), c.feedback.Detail),
		Value:     settingsNoopValue,
		SearchKey: "feedback mutation result success failure",
	}
	insertAt := 0
	if len(options.Entries) > 0 && options.Entries[0].Value == settingsBackValue {
		insertAt = 1
	}
	options.Entries = append(options.Entries, intpickercompat.Entry{})
	copy(options.Entries[insertAt+1:], options.Entries[insertAt:])
	options.Entries[insertAt] = entry
	return options
}

func settingsFeedbackSummaryLocale(locale i18n.Locale, summary string) string {
	for _, status := range []string{"complete", "failed"} {
		suffix := " " + status
		if label, ok := strings.CutSuffix(strings.TrimSpace(summary), suffix); ok {
			return settingsCatalogTextLocale(locale, label) + " " + settingsCatalogTextLocale(locale, status)
		}
	}
	return summary
}

func (c *settingsCommand) clearSettingsFeedbackFor(value string) {
	if c == nil || c.feedback == nil || value == "" || value == settingsNoopValue {
		return
	}
	c.feedback = nil
}

func (c *settingsCommand) setSettingsFeedback(summary, detail string) {
	if c == nil {
		return
	}
	c.feedback = &settingsFeedback{Summary: strings.TrimSpace(summary), Detail: strings.TrimSpace(detail)}
}

// runSettingsMutation is the common popup feedback boundary for Settings
// writes. Output remains mirrored to the command writers for CLI/test
// compatibility, while the last meaningful line is also made visible inside
// the alternate-screen picker. Mutation errors are handled here so the user
// can see and recover from them without losing the Settings popup.
func (c *settingsCommand) runSettingsMutation(label string, stdout, stderr io.Writer, mutate func(io.Writer, io.Writer) error) error {
	return c.runSettingsMutationResult(label, stdout, stderr, true, mutate)
}

func (c *settingsCommand) runObservedSettingsMutation(label string, stdout, stderr io.Writer, mutate func(io.Writer, io.Writer) error) error {
	return c.runSettingsMutationResult(label, stdout, stderr, false, mutate)
}

func (c *settingsCommand) runSettingsMutationResult(label string, stdout, stderr io.Writer, successIfSilent bool, mutate func(io.Writer, io.Writer) error) error {
	var out, errOut strings.Builder
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	outWriter := io.MultiWriter(stdout, &out)
	errWriter := io.MultiWriter(stderr, &errOut)
	if err := mutate(outWriter, errWriter); err != nil {
		c.setSettingsFeedback(label+" failed", err.Error())
		return nil
	}
	detail := lastSettingsFeedbackLine(errOut.String())
	if detail != "" {
		c.setSettingsFeedback(label+" failed", detail)
		return nil
	}
	detail = lastSettingsFeedbackLine(out.String())
	if detail == "" {
		if !successIfSilent {
			return nil
		}
		detail = "saved"
	}
	c.setSettingsFeedback(label+" complete", detail)
	return nil
}

func lastSettingsFeedbackLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
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
	case strings.HasPrefix(value, settingsActionPrefixLiveResources):
		return c.setLiveResourcesMode(strings.TrimPrefix(value, settingsActionPrefixLiveResources))
	case strings.HasPrefix(value, settingsActionPrefixHUDVisibility):
		raw := strings.TrimPrefix(value, settingsActionPrefixHUDVisibility)
		if leaf, mode, ok := parseAgentUsageVisibilityAction(raw); ok {
			return c.setAgentUsageVisibility(leaf, mode)
		}
		if component, mode, ok := parseStatusbarHUDVisibilityAction(raw); ok {
			return c.setStatusbarHUDVisibility(component, mode)
		}
		component, mode, ok := parseStatusbarRowOneVisibilityAction(raw)
		if !ok {
			return fmt.Errorf("unknown status bar visibility action: %s", value)
		}
		return c.setStatusbarRowOneVisibility(component, mode)
	case strings.HasPrefix(value, settingsActionPrefixProjdir):
		action := strings.TrimPrefix(value, settingsActionPrefixProjdir)
		if c.switcher == nil {
			return errors.New("project root settings are not configured")
		}
		return c.switcher.executeProjdirSettingsAction(action, stdout, stderr)
	case strings.HasPrefix(value, settingsActionPrefixRuntimeDiagnostics):
		return c.setRuntimeDiagnosticsVisibility(strings.TrimPrefix(value, settingsActionPrefixRuntimeDiagnostics))
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
				return err
			}
			_, _ = fmt.Fprintln(stdout, "Update complete. Restart projmux to run the new version.")
			return nil
		case "check":
			if err := c.update.Run([]string{"check"}, stdout, stderr); err != nil {
				_, _ = fmt.Fprintf(stdout, "Update check failed: %v\n", err)
				return err
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

func (c *settingsCommand) executeWithFeedback(value string, stdout, stderr io.Writer) error {
	label, mutation := settingsMutationLabel(value)
	if !mutation {
		return c.execute(value, stdout, stderr)
	}
	return c.runSettingsMutation(label, stdout, stderr, func(out, errOut io.Writer) error {
		return c.execute(value, out, errOut)
	})
}

// settingsMutationLabel is the executable Settings mutation inventory. Keep
// this list closed: navigation/viewer values (Welcome, Quit, diagnostics and
// key capture/probe flows) must not be projected as generic mutation feedback.
func settingsMutationLabel(value string) (string, bool) {
	if value == settingsActionPrefixSessionState+"project-preview" {
		return "", false
	}
	for _, candidate := range []struct {
		prefix string
		label  string
	}{
		{settingsActionPrefixAI, "Default launch target"},
		{settingsActionPrefixDesktopNotifyMode, "Desktop delivery"},
		{settingsActionPrefixHooks, "Project automation policy"},
		{settingsActionPrefixLiveResources, "Resources"},
		{settingsActionPrefixHUDVisibility, "Status Bar visibility"},
		{settingsActionPrefixProjdir, "Primary discovery root"},
		{settingsActionPrefixSessionState, "Snapshots"},
		{settingsActionPrefixStatusbar, "Status Bar"},
		{settingsActionPrefixSwitch, "Pinned Project"},
		{settingsActionPrefixUpdate, "Update"},
		{settingsActionPrefixWorkdir, "Discovery root"},
	} {
		if strings.HasPrefix(value, candidate.prefix) {
			return candidate.label, true
		}
	}
	return "", false
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
