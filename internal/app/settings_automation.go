package app

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// Automation is the product concept for Projmux-owned user scripts and the
// policy that gates the project-local ones. It deliberately does not own
// provider wiring or desktop delivery: those are Notification concepts and
// live under Notifications.
//
// The technical `[hooks.<event>]` spelling is preserved in config, in the
// state rows and in the guidance copy; only the primary navigation uses the
// product concept.

// settingsAutomationLifecycleEvents are the session lifecycle events, in the
// order they run. `send-noti` is deliberately not a member: it is not a
// session lifecycle event, and the target IA gives it its own sibling row.
var settingsAutomationLifecycleEvents = []string{
	string(hooks.EventPreCreate),
	string(hooks.EventPostCreate),
	string(hooks.EventPostAttach),
}

// settingsAutomationEventLabels maps the `[hooks.*]` event key to its product
// label. The config key itself never changes.
var settingsAutomationEventLabels = map[string]string{
	string(hooks.EventPreCreate):  "Before session create",
	string(hooks.EventPostCreate): "After session create",
	string(hooks.EventPostAttach): "After session attach",
	string(hooks.EventSendNoti):   "After notification queued",
}

func settingsAutomationEventLabel(event string) string {
	if label, ok := settingsAutomationEventLabels[event]; ok {
		return label
	}
	return event
}

func settingsHookEventValue(scope, event string) string {
	return settingsActionPrefixHookEvent + scope + ":" + event
}

func parseSettingsHookEventValue(value string) (scope, event string, ok bool) {
	rest, found := strings.CutPrefix(value, settingsActionPrefixHookEvent)
	if !found {
		return "", "", false
	}
	scope, event, found = strings.Cut(rest, ":")
	if !found || strings.TrimSpace(scope) == "" || strings.TrimSpace(event) == "" {
		return "", "", false
	}
	return scope, event, true
}

// runAutomationSection is the Global > Automation container.
func (c *settingsCommand) runAutomationSection(stdout, stderr io.Writer) error {
	// Same best-effort migration the previous Hooks root performed on entry,
	// so a user with legacy single-line scripts sees declarative rows.
	_, _ = hooks.MigrateGlobalLegacyScripts(c.lookupEnv, c.homeDir, "", stderr)
	for {
		options, err := c.sectionOptions(settingsSectionAutomation)
		if err != nil {
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
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case action == settingsAutomationLifecycle:
			if err := c.runAutomationLifecycleSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsAutomationSendNoti:
			if err := c.runHookEventDetailSection(hookScopeGlobal, string(hooks.EventSendNoti), stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixHooks):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown automation settings action: %s", action)
		}
	}
}

func (c *settingsCommand) automationEntries() []intpickercompat.Entry {
	locale := c.locale()
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	entries = append(entries, intpickercompat.Entry{
		Label:     c.nodeRowLabel(settingsNavAutomationLifecycle, settingsGlyphOpen, settingsColorType, c.automationLifecycleSummary()),
		Value:     settingsAutomationLifecycle,
		SearchKey: "automation lifecycle hooks pre-create post-create post-attach session",
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     c.nodeRowLabel(settingsNavAutomationSendNoti, settingsGlyphOpen, settingsColorType, c.automationEventSummary(hookScopeGlobal, string(hooks.EventSendNoti))),
		Value:     settingsAutomationSendNoti,
		SearchKey: "automation send-noti notification queued user script fan-out",
	})
	entries = append(entries, c.automationProjectPolicyEntry())
	return entries
}

// automationProjectPolicyEntry renders the promoted Labs toggle. It is a
// simple boolean with a trust/source badge, so it stays a Toggle inside the
// Automation view instead of gaining a detail route of its own.
func (c *settingsCommand) automationProjectPolicyEntry() intpickercompat.Entry {
	mode, source := c.currentProjectHooksMode()
	glyph := settingsGlyphInactive
	color := settingsColorDim
	next := config.ProjectHooksOn
	if mode == config.ProjectHooksOn {
		glyph = settingsGlyphToggle
		color = settingsColorAdd
		next = config.ProjectHooksOff
	}
	return intpickercompat.Entry{
		Label:     c.nodeRowLabel(settingsNavAutomation+".project-policy", glyph, color, string(mode)+" - "+source),
		Value:     settingsActionPrefixHooks + string(next),
		SearchKey: "project automation policy trusted project hooks on off " + source,
	}
}

func (c *settingsCommand) automationLifecycleSummary() string {
	cfg, path := c.globalHookConfig()
	if path == "" {
		return "global config unavailable"
	}
	active := 0
	for _, event := range settingsAutomationLifecycleEvents {
		if strings.TrimSpace(declaredHookRun(cfg, event)) != "" {
			active++
		}
	}
	return settingsLifecycleSummaryLocale(c.locale(), active, len(settingsAutomationLifecycleEvents))
}

// settingsLifecycleSummaryLocale renders the "<n> of <m> events have a
// command" tail through the catalog so the sentence order can differ per
// locale.
func settingsLifecycleSummaryLocale(locale i18n.Locale, active, total int) string {
	return strings.NewReplacer(
		"{active}", strconv.Itoa(active),
		"{total}", strconv.Itoa(total),
	).Replace(localizeText(locale, "settings.text.automation_lifecycle_summary", "{active} of {total} events have a command"))
}

func (c *settingsCommand) automationEventSummary(scope, event string) string {
	command, _, _ := c.hookEventState(scope, event)
	if strings.TrimSpace(command) == "" {
		return "no command"
	}
	return "run = " + command
}

// globalHookConfig loads the global hook config plus the path it came from.
func (c *settingsCommand) globalHookConfig() (hooks.ProjectConfig, string) {
	path, err := c.globalConfigPath()
	if err != nil {
		return hooks.ProjectConfig{}, ""
	}
	cfg, _ := hooks.LoadGlobalConfig(path)
	return cfg, path
}

// hookEventState resolves the declared command, the owning config path and the
// legacy script record for one scope/event pair.
func (c *settingsCommand) hookEventState(scope, event string) (command, configPath string, legacy settingsLegacyScript) {
	if scope == hookScopeProject {
		ctx := c.resolveSettingsProjectContext()
		if !ctx.hasProject() {
			return "", "", settingsLegacyScript{}
		}
		configPath = filepath.Join(ctx.Path, ".projmux", "config.toml")
		cfg, _ := loadProjectConfigForRead(configPath)
		return declaredHookRun(cfg, event), configPath, projectLegacyScriptMap(ctx.Path)[event]
	}
	cfg, path := c.globalHookConfig()
	if path == "" {
		return "", "", settingsLegacyScript{}
	}
	return declaredHookRun(cfg, event), path, globalLegacyScriptMap(c.homeDir, c.lookupEnv)[event]
}

func (c *settingsCommand) runAutomationLifecycleSection(stdout, stderr io.Writer) error {
	return c.runHookLifecycleSection(hookScopeGlobal, stdout, stderr)
}

// runHookLifecycleSection lists the three session lifecycle events for one
// scope. Each row is a View: the event's command state and its mutation rows
// live in the event detail, so pressing a lifecycle row never runs or clears a
// hook.
func (c *settingsCommand) runHookLifecycleSection(scope string, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(c.hookLifecycleOptions(scope))
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		}
		eventScope, event, ok := parseSettingsHookEventValue(action)
		if !ok || eventScope != scope {
			return fmt.Errorf("unknown automation lifecycle action: %s", action)
		}
		if err := c.runHookEventDetailSection(scope, event, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) hookLifecycleOptions(scope string) intpickercompat.Options {
	ui := "settings-automation-lifecycle"
	prompt := "Settings > Automation > Projmux session lifecycle > "
	title := "Automation - Projmux session lifecycle"
	if scope == hookScopeProject {
		ui = "settings-project-automation-lifecycle"
		prompt = "Settings > Project > Automation > Project hooks > Session lifecycle > "
		title = "Project automation - Session lifecycle"
	}
	return intpickercompat.Options{
		UI:         ui,
		Entries:    c.hookLifecycleEntries(scope),
		Title:      title,
		Prompt:     prompt,
		Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   c.settingsCloseBindings(),
	}
}

func (c *settingsCommand) hookLifecycleEntries(scope string) []intpickercompat.Entry {
	locale := c.locale()
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	for _, event := range settingsAutomationLifecycleEvents {
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, settingsAutomationEventLabel(event), c.automationEventSummary(scope, event)),
			Value:     settingsHookEventValue(scope, event),
			SearchKey: "automation " + scope + " " + event + " hooks." + event + " lifecycle command",
		})
	}
	return entries
}

// runHookEventDetailSection is the per-event View. It shows the command, its
// effective state and its source first, then the mutation rows the scope
// actually supports. Global `[hooks.*]` remains read-only in app, so its
// mutation rows render as disabled rows carrying the reason and the canonical
// CLI next step rather than as silent no-ops.
func (c *settingsCommand) runHookEventDetailSection(scope, event string, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(c.hookEventDetailOptions(scope, event))
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case strings.HasPrefix(action, settingsActionPrefixHookAdd),
			strings.HasPrefix(action, settingsActionPrefixHookEdit),
			strings.HasPrefix(action, settingsActionPrefixHookRemove),
			strings.HasPrefix(action, settingsActionPrefixHookView):
			ctx := settingsProjectContext{}
			if scope == hookScopeProject {
				ctx = c.resolveSettingsProjectContext()
			}
			if err := c.runHookMakerActionWithFeedback(ctx, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown automation event action: %s", action)
		}
	}
}

func (c *settingsCommand) hookEventDetailOptions(scope, event string) intpickercompat.Options {
	label := settingsAutomationEventLabel(event)
	ui := "settings-automation-event"
	prompt := "Settings > Automation > " + label + " > "
	if event != string(hooks.EventSendNoti) {
		prompt = "Settings > Automation > Projmux session lifecycle > " + label + " > "
	}
	title := "Automation - " + label
	if scope == hookScopeProject {
		ui = "settings-project-automation-event"
		prompt = "Settings > Project > Automation > Project hooks > " + label + " > "
		if event != string(hooks.EventSendNoti) {
			prompt = "Settings > Project > Automation > Project hooks > Session lifecycle > " + label + " > "
		}
		title = "Project automation - " + label
	}
	return intpickercompat.Options{
		UI:         ui,
		Entries:    c.hookEventDetailEntries(scope, event),
		Title:      title,
		Prompt:     prompt,
		Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   c.settingsCloseBindings(),
	}
}

func (c *settingsCommand) hookEventDetailEntries(scope, event string) []intpickercompat.Entry {
	locale := c.locale()
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}

	command, configPath, legacy := c.hookEventState(scope, event)
	if scope == hookScopeProject && configPath == "" {
		return append(entries, intpickercompat.Entry{
			Label: c.rowLabelDim(settingsAutomationEventLabel(event), "disabled - no project context"),
			Value: settingsNoopValue,
		})
	}
	if configPath == "" {
		return append(entries, intpickercompat.Entry{
			Label: c.rowLabelDim(settingsAutomationEventLabel(event), "global config unavailable"),
			Value: settingsNoopValue,
		})
	}

	state := "no command"
	if strings.TrimSpace(command) != "" {
		state = "run = " + command
	}
	source := configPath + " [hooks." + event + "]"
	if event == string(hooks.EventSendNoti) {
		// Say the boundary from this side too: the fan-out script and the
		// desktop sender override are different jobs, not two ways to do one.
		source += " - " + settingsSendNotiBoundary
	}
	entries = append(entries, intpickercompat.Entry{
		Label:     c.rowLabelInfo("Command", state, source),
		Value:     settingsNoopValue,
		SearchKey: "command effective source hooks." + event + " " + configPath,
	})
	if scope == hookScopeProject {
		entries = append(entries, intpickercompat.Entry{
			Label: c.nodeRowLabelInfo(settingsNavProjectTrust, c.projectAutomationTrustSummary(), "project automation runs only from an approved config"),
			Value: settingsNoopValue,
		})
	}

	entries = append(entries, c.hookEventEditEntry(scope, event, command, configPath))
	entries = append(entries, c.hookEventRemoveEntry(scope, event, command, configPath))
	if legacy.Path != "" {
		entries = append(entries, settingsHookLegacyEntry(settingsHookRow{
			Event: event, Scope: scope, Declared: command, ConfigPath: configPath, Legacy: legacy,
		}))
	}
	return entries
}

func (c *settingsCommand) hookEventEditEntry(scope, event, command, configPath string) intpickercompat.Entry {
	label := c.navLabel(settingsNavAutomationSendNoti + ".edit")
	if scope != hookScopeProject {
		// Global `[hooks.*]` is read-only in app. Show why and where the
		// change belongs instead of offering a row that cannot act.
		return intpickercompat.Entry{
			Label:     c.rowLabelDim(label, "read-only here - edit "+configPath+" or run projmux hook edit "+event),
			Value:     settingsNoopValue,
			SearchKey: "add edit command read-only global hook " + event,
		}
	}
	prefix := settingsActionPrefixHookAdd
	if strings.TrimSpace(command) != "" {
		prefix = settingsActionPrefixHookEdit
	}
	return intpickercompat.Entry{
		Label:     c.rowLabel(settingsGlyphAdd, settingsColorAdd, label, "one-line command written to [hooks."+event+"]"),
		Value:     prefix + scope + ":" + event,
		SearchKey: "add edit command project hook " + event,
	}
}

func (c *settingsCommand) hookEventRemoveEntry(scope, event, command, configPath string) intpickercompat.Entry {
	label := c.navLabel(settingsNavAutomationSendNoti + ".remove")
	if scope != hookScopeProject {
		return intpickercompat.Entry{
			Label:     c.rowLabelDim(label, "read-only here - edit "+configPath+" or run projmux hook edit "+event),
			Value:     settingsNoopValue,
			SearchKey: "remove command read-only global hook " + event,
		}
	}
	if strings.TrimSpace(command) == "" {
		return intpickercompat.Entry{
			Label:     c.rowLabelDim(label, "unavailable - no command to remove"),
			Value:     settingsNoopValue,
			SearchKey: "remove command project hook " + event,
		}
	}
	return intpickercompat.Entry{
		Label:     c.rowLabel(settingsGlyphRemove, settingsColorRemove, label, "clears [hooks."+event+"] in "+configPath),
		Value:     settingsActionPrefixHookRemove + scope + ":" + event,
		SearchKey: "remove command project hook " + event,
	}
}

// runProjectAutomationSection is the Project > Automation container: trust and
// the project-local lifecycle scripts under one product concept.
func (c *settingsCommand) runProjectAutomationSection(stdout, stderr io.Writer) error {
	if ctx := c.resolveSettingsProjectContext(); ctx.hasProject() {
		_, _ = hooks.MigrateProjectLegacyScripts(ctx.Path, "", stderr)
	}
	for {
		options, err := c.sectionOptions(settingsSectionProjectAutomation)
		if err != nil {
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
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		case settingsSectionProjectTrust:
			if err := c.runProjectTrustSection(stdout, stderr); err != nil {
				return err
			}
		case settingsSectionProjectHooks:
			if err := c.runProjectHooksSection(stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project automation action: %s", action)
		}
	}
}

func (c *settingsCommand) projectAutomationEntries() []intpickercompat.Entry {
	locale := c.locale()
	ctx := c.resolveSettingsProjectContext()
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if !ctx.hasProject() {
		return append(entries, intpickercompat.Entry{
			Label:     c.rowLabelDim("Project context", "open Settings from a managed project to enable project automation"),
			Value:     settingsNoopValue,
			SearchKey: "project context automation trust hooks",
		})
	}
	entries = append(entries, c.projectTrustEntry(ctx))
	entries = append(entries, intpickercompat.Entry{
		Label:     c.nodeRowLabel(settingsNavProjectHooks, settingsGlyphOpen, settingsColorType, filepath.Join(ctx.Path, ".projmux", "config.toml")),
		Value:     settingsSectionProjectHooks,
		SearchKey: "project hooks lifecycle send-noti config.toml automation",
	})
	return entries
}

// projectAutomationTrustSummary is the short trust state shown on project
// automation event details. It reuses the trust store the Trust view owns.
func (c *settingsCommand) projectAutomationTrustSummary() string {
	ctx := c.resolveSettingsProjectContext()
	if !ctx.hasProject() {
		return "no project context"
	}
	report, err := c.inspectProjectTrust(ctx)
	if err != nil {
		return "trust state unavailable"
	}
	return string(report.State)
}

// runProjectHooksLifecycleSection is the Project hooks > Session lifecycle
// view.
func (c *settingsCommand) runProjectHooksLifecycleSection(stdout, stderr io.Writer) error {
	return c.runHookLifecycleSection(hookScopeProject, stdout, stderr)
}
