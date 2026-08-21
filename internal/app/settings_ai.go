package app

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func (c *settingsCommand) runAISection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionAI)
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
		case action == settingsAIDefaultMode:
			if err := c.runAIDefaultModeSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsAIEnabledAgents:
			if err := c.runAIEnabledAgentsSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsAIResumePicker:
			if err := c.runAIResumePickerSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixAI):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIEnabledAgentsSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-enabled-agents",
			Entries:    c.aiEnabledAgentEntries(),
			Title:      "AI - Enabled providers",
			Prompt:     "Settings > AI > Enabled providers > ",
			Footer:     projmuxFooter("Enter: toggle  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
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
		case strings.HasPrefix(action, settingsActionPrefixAIEnabledAgent):
			provider := strings.TrimPrefix(action, settingsActionPrefixAIEnabledAgent)
			if err := c.runSettingsMutation("Enabled providers", stdout, stderr, func(io.Writer, io.Writer) error {
				return c.toggleAIEnabledAgent(provider)
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI enabled agents action: %s", action)
		}
	}
}

// runAIResumePickerSection drives the Settings > AI Settings > Resume picker
// submenu. It is a navigation view: it shows two drill-in rows (Picker limit
// and Scan depth) with their current value + source, and routes each into a
// deeper section that owns the preset toggles + custom input. The preset/custom
// wiring itself is unchanged — only relocated one level deeper.
func (c *settingsCommand) runAIResumePickerSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-resume-picker",
			Entries:    c.aiResumePickerEntries(),
			Title:      "AI - Agent Resume Picker",
			Prompt:     "Settings > AI > Agent Resume Picker > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
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
		case action == settingsAIResumePickerLimit:
			if err := c.runAIResumePickerLimitSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsAIResumePickerDepth:
			if err := c.runAIResumePickerDepthSection(stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI resume picker settings action: %s", action)
		}
	}
}

// runAIResumePickerLimitSection drives the deeper Picker limit view: an info
// header plus the preset toggles + custom row. Preset/custom wiring is the same
// as before; it just lives one level down now.
func (c *settingsCommand) runAIResumePickerLimitSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-resume-picker-limit",
			Entries:    c.aiResumePickerLimitEntries(),
			Title:      "AI - Picker limit",
			Prompt:     "Settings > AI > Agent Resume Picker > Picker limit > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
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
		case action == settingsActionPrefixAIResumeLimit+"custom":
			if err := c.runAIResumePickerLimitCustom(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixAIResumeLimit):
			limit, err := parseAIResumePickerLimit(strings.TrimPrefix(action, settingsActionPrefixAIResumeLimit))
			if err != nil {
				return err
			}
			if err := c.runSettingsMutation("Resume picker limit", stdout, stderr, func(out, _ io.Writer) error {
				return c.setAIResumePickerLimit(limit, out)
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI resume picker limit action: %s", action)
		}
	}
}

// runAIResumePickerDepthSection drives the deeper Scan depth view: an info
// header plus the preset toggles + custom row. Preset/custom wiring is the same
// as before; it just lives one level down now.
func (c *settingsCommand) runAIResumePickerDepthSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-resume-picker-depth",
			Entries:    c.aiResumePickerDepthEntries(),
			Title:      "AI - Scan depth",
			Prompt:     "Settings > AI > Agent Resume Picker > Scan depth > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
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
		case action == settingsActionPrefixAIResumeDepth+"custom":
			if err := c.runAIResumePickerDepthCustom(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixAIResumeDepth):
			depth, err := parseAIResumeScanDepth(strings.TrimPrefix(action, settingsActionPrefixAIResumeDepth))
			if err != nil {
				return err
			}
			if err := c.runSettingsMutation("Resume scan depth", stdout, stderr, func(out, _ io.Writer) error {
				return c.setAIResumeScanDepth(depth, out)
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI resume picker depth action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIResumePickerLimitCustom(stdout, stderr io.Writer) error {
	current := c.currentAIResumePickerLimit()
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-ai-resume-picker-custom",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: strconv.Itoa(current.Limit),
		Title:        "AI - Custom picker limit",
		Prompt:       "Resume picker limit > ",
		Footer:       projmuxFooter("Enter: save  |  Example: 30 "),
		ExpectKeys:   []string{"enter"},
		Bindings:     c.settingsCloseBindings(),
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	limit, err := parseAIResumePickerLimit(result.Query)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		c.setSettingsFeedback("Resume picker limit failed", err.Error())
		return nil
	}
	return c.runSettingsMutation("Resume picker limit", stdout, stderr, func(out, _ io.Writer) error {
		return c.setAIResumePickerLimit(limit, out)
	})
}

func (c *settingsCommand) runAIResumePickerDepthCustom(stdout, stderr io.Writer) error {
	current := c.currentAIResumeScanDepth()
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-ai-resume-picker-depth-custom",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: strconv.Itoa(current.Depth),
		Title:        "AI - Custom scan depth",
		Prompt:       "Resume scan depth > ",
		Footer:       projmuxFooter("Enter: save  |  Example: 2 "),
		ExpectKeys:   []string{"enter"},
		Bindings:     c.settingsCloseBindings(),
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	depth, err := parseAIResumeScanDepth(result.Query)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		c.setSettingsFeedback("Resume scan depth failed", err.Error())
		return nil
	}
	return c.runSettingsMutation("Resume scan depth", stdout, stderr, func(out, _ io.Writer) error {
		return c.setAIResumeScanDepth(depth, out)
	})
}

// aiResumePickerEntries builds the Agent Resume Picker view. The two read-only
// rows name what resume actually operates on: an existing Agent in an Offline
// or Failed phase, and a new-action label that creates a new Agent rather than
// resuming one. The two value rows keep their own chooser.
func (c *settingsCommand) aiResumePickerEntries() []intpickercompat.Entry {
	return []intpickercompat.Entry{
		c.backEntry(),
		{
			Label:     c.rowLabelInfo("Effective behavior", "resume an existing Agent", "eligible phases: Offline, Failed"),
			Value:     settingsNoopValue,
			SearchKey: "agent resume picker eligible phases offline failed existing agent",
		},
		{
			Label:     c.rowLabelInfo("New action label", "Create New Agent", "always creates a new Agent"),
			Value:     settingsNoopValue,
			SearchKey: "create new agent resume picker new action label",
		},
		{
			Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavAIResumePicker+".limit"), c.aiResumePickerLimitSummary()),
			Value:     settingsAIResumePickerLimit,
			SearchKey: "resume picker limit max sessions resume_picker_limit",
		},
		{
			Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavAIResumePicker+".depth"), c.aiResumeScanDepthSummary()),
			Value:     settingsAIResumePickerDepth,
			SearchKey: "resume scan depth cwd child directories resume_scan_depth",
		},
	}
}

// aiResumePickerLimitEntries builds the deeper Picker limit view: an info
// header (current value + source) followed by the preset toggles and the custom
// input row.
func (c *settingsCommand) aiResumePickerLimitEntries() []intpickercompat.Entry {
	current := c.currentAIResumePickerLimit()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("Resume picker limit", fmt.Sprintf("%d sessions", current.Limit), string(current.Source)),
			Value: settingsNoopValue,
		},
	}
	for _, limit := range []int{20, 30, 50, 100} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if limit == current.Limit && current.Source != aiResumePickerLimitSourceDefault {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(glyph, color, fmt.Sprintf("%d sessions", limit), "max sessions listed in the AI resume picker"),
			Value: settingsActionPrefixAIResumeLimit + strconv.Itoa(limit),
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphType, settingsColorType, "Custom limit", "store a session count between 1 and 100"),
		Value: settingsActionPrefixAIResumeLimit + "custom",
	})
	return entries
}

// aiResumePickerDepthEntries builds the deeper Scan depth view: an info header
// (current value + source) followed by the preset toggles and the custom input
// row.
func (c *settingsCommand) aiResumePickerDepthEntries() []intpickercompat.Entry {
	depth := c.currentAIResumeScanDepth()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("Resume scan depth", fmt.Sprintf("depth %d", depth.Depth), string(depth.Source)),
			Value: settingsNoopValue,
		},
	}
	for _, level := range []int{0, 1, 2, 3} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		// Depth 0 is the default, so highlight it only when explicitly set; any
		// non-zero preset highlights when it matches the resolved depth.
		active := level == depth.Depth
		if level == 0 && depth.Source == aiResumeScanDepthSourceDefault {
			active = false
		}
		if active {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(glyph, color, fmt.Sprintf("depth %d", level), "include sessions from cwd child directories"),
			Value: settingsActionPrefixAIResumeDepth + strconv.Itoa(level),
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphType, settingsColorType, "Custom depth", "store a cwd child depth between 0 and 8"),
		Value: settingsActionPrefixAIResumeDepth + "custom",
	})
	return entries
}

func (c *settingsCommand) runAIDefaultModeSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-default-mode",
			Entries:    c.aiEntries(),
			Title:      "AI - Default launch target",
			Prompt:     "Settings > AI > Default launch target > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
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
		case strings.HasPrefix(action, settingsActionPrefixAI):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI default mode action: %s", action)
		}
	}
}

func (c *settingsCommand) aiRootEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.ai == nil {
		return entries
	}
	current := c.ai.getMode()
	defaultDesc := current
	if warning := c.aiDefaultModeDisabledWarning(); warning != "" {
		defaultDesc += " - " + warning
	}
	entries = append(entries,
		intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavAI+".launch-target"), defaultDesc),
			Value:     settingsAIDefaultMode,
			SearchKey: "default launch target agent provider claude codex antigravity shell pane choose at launch",
		},
		intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavAIProviders), c.aiEnabledAgentsSummary()),
			Value:     settingsAIEnabledAgents,
			SearchKey: "enabled providers claude codex antigravity",
		},
		intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavAIResumePicker), c.aiResumePickerSummary()),
			Value:     settingsAIResumePicker,
			SearchKey: "agent resume picker limit sessions resume_picker_limit scan depth cwd resume_scan_depth offline failed",
		},
	)
	if c.appServerHealth != nil {
		health := c.appServerHealth(codexHookFallbackAvailable(c.currentAINotifyDiagnostics()))
		detail := fmt.Sprintf("%s - %s, %s", health.Source.Label(), health.Connection, health.Reason)
		if health.Version != "" {
			detail += " - " + health.Version
		}
		if health.Lifecycle != "" {
			detail += fmt.Sprintf(" - %s/%s", health.Lifecycle, health.LifecycleReason)
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabelInfo("Codex control plane", detail, "read-only capability health"),
			Value:     settingsNoopValue,
			SearchKey: "codex app server hook fallback unavailable health read only",
		})
	}
	return entries
}

func (c *settingsCommand) aiResumePickerSummary() string {
	current := c.currentAIResumePickerLimit()
	depth := c.currentAIResumeScanDepth()
	return fmt.Sprintf("%d sessions, depth %d - %s", current.Limit, depth.Depth, current.Source)
}

// aiResumePickerLimitSummary renders the "<value> - <source>" tail used on the
// Picker limit drill-in row, reusing the resolved limit + source.
func (c *settingsCommand) aiResumePickerLimitSummary() string {
	current := c.currentAIResumePickerLimit()
	return fmt.Sprintf("%d - %s", current.Limit, current.Source)
}

// aiResumeScanDepthSummary renders the "<value> - <source>" tail used on the
// Scan depth drill-in row, reusing the resolved depth + source.
func (c *settingsCommand) aiResumeScanDepthSummary() string {
	depth := c.currentAIResumeScanDepth()
	return fmt.Sprintf("%d - %s", depth.Depth, depth.Source)
}

func (c *settingsCommand) aiEntries() []intpickercompat.Entry {
	if c.ai == nil {
		return nil
	}

	current := c.ai.getMode()
	enabled := c.currentAIEnabledAgents()
	modes := []struct {
		mode string
		desc string
	}{
		{aiModeSelective, "show picker each time"},
	}
	for _, provider := range aiprovider.SettingsVisible() {
		modes = append(modes, struct {
			mode string
			desc string
		}{
			mode: string(provider.ID),
			desc: "always run " + provider.DisplayName + " split",
		})
	}
	modes = append(modes, struct {
		mode string
		desc string
	}{aiModeShell, "always open plain shell split"})

	entries := make([]intpickercompat.Entry, 0, len(modes)+1)
	entries = append(entries, c.backEntry())
	if warning := c.aiDefaultModeDisabledWarning(); warning != "" {
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabelDim("Warning", "saved Default launch target "+warning),
			Value:     settingsNoopValue,
			SearchKey: "default split mode disabled enabled agents",
		})
	}
	for _, item := range modes {
		if provider, ok := aiModeProvider(item.mode); ok && !aiEnabledAgentsContains(enabled, provider) {
			continue
		}
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(glyph, color, item.mode, item.desc),
			Value: settingsActionPrefixAI + item.mode,
		})
	}
	return entries
}

func (c *settingsCommand) aiEnabledAgentEntries() []intpickercompat.Entry {
	enabled := c.currentAIEnabledAgents()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("Enabled providers", c.aiEnabledAgentsSummary(), "~/.config/projmux/"+config.AIEnabledAgentsFileName),
			Value: settingsNoopValue,
		},
	}
	if warning := c.aiDefaultModeDisabledWarning(); warning != "" {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabelDim("Warning", "saved Default launch target "+warning),
			Value: settingsNoopValue,
		})
	}

	for _, provider := range aiprovider.SettingsVisible() {
		configProvider := config.AIAgentProvider(provider.ID)
		on := aiEnabledAgentsContains(enabled, configProvider)
		glyph := settingsGlyphInactive
		color := settingsColorDim
		state := "disabled"
		if on {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
			state = "enabled"
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(glyph, color, provider.DisplayName, state+" - "+provider.DisplayName+" split"),
			Value:     settingsActionPrefixAIEnabledAgent + string(provider.ID),
			SearchKey: strings.Join([]string{"enabled agents", provider.DisplayName, string(provider.ID), state}, " "),
		})
	}
	return entries
}

func (c *settingsCommand) currentAIEnabledAgents() []config.AIAgentProvider {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	agents, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	return agents
}

func (c *settingsCommand) toggleAIEnabledAgent(provider string) error {
	normalized := config.NormalizeAIEnabledAgents([]string{provider})
	if len(normalized) != 1 {
		return fmt.Errorf("unknown AI agent provider: %s", provider)
	}
	target := normalized[0]
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	current, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		return err
	}
	enabled := map[config.AIAgentProvider]bool{}
	for _, agent := range current {
		enabled[agent] = true
	}
	enabled[target] = !enabled[target]

	next := make([]config.AIAgentProvider, 0, len(config.DefaultAIEnabledAgents))
	for _, agent := range config.KnownAIAgentProviders() {
		if enabled[agent] {
			next = append(next, agent)
		}
	}
	return config.SaveAIEnabledAgentsFile(paths.AIEnabledAgentsFile(), next)
}

func (c *settingsCommand) aiEnabledAgentsSummary() string {
	enabled := c.currentAIEnabledAgents()
	if len(enabled) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(enabled))
	for _, agent := range enabled {
		names = append(names, aiEnabledAgentDisplayName(agent))
	}
	return strings.Join(names, ", ")
}

func (c *settingsCommand) aiDefaultModeDisabledWarning() string {
	if c.ai == nil {
		return ""
	}
	mode := config.NormalizeAIEnabledAgents([]string{c.ai.getMode()})
	if len(mode) != 1 {
		return ""
	}
	if aiEnabledAgentsContains(c.currentAIEnabledAgents(), mode[0]) {
		return ""
	}
	return string(mode[0]) + " disabled"
}

func aiEnabledAgentsContains(agents []config.AIAgentProvider, provider config.AIAgentProvider) bool {
	return slices.Contains(agents, provider)
}

func aiEnabledAgentDisplayName(provider config.AIAgentProvider) string {
	if meta, ok := aiprovider.Lookup(string(provider)); ok && meta.DisplayName != "" {
		return meta.DisplayName
	}
	return string(provider)
}

func (c *settingsCommand) aiForSettings() aiHookSettingsReader {
	if c.ai != nil {
		return c.ai
	}
	if c.newAI != nil {
		return c.newAI()
	}
	// Struct-literal constructions leave the factory nil; keep the previous
	// nil-ai fallback of a minimally wired aiCommand without naming the
	// struct here.
	return newSettingsAIFallback(c.homeDir, c.lookupEnv)
}
