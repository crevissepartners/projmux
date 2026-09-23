package app

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// Settings > AI > Agent questions holds the two central Claude question
// settings: how a question is answered (way 1 or way 2) and how long way 2
// holds it. Both are the files the question hook rereads for every question,
// so a change here applies to the next question with no integrate run.

const (
	agentQuestionsScopeKey          i18n.Key = "settings.desc.agent_questions_scope"
	agentQuestionsPerAgentKey       i18n.Key = "settings.desc.agent_questions_per_agent"
	agentQuestionsPerAgentNameKey   i18n.Key = "settings.text.agent_questions_per_agent"
	agentQuestionAnsweringClaudeKey i18n.Key = "settings.text.agent_question_answering_claude"
	agentQuestionAnsweringTmuxKey   i18n.Key = "settings.text.agent_question_answering_projmux"
	agentQuestionWindowSecondsKey   i18n.Key = "settings.text.agent_question_window_seconds"
	agentQuestionWindowDefaultKey   i18n.Key = "settings.text.agent_question_window_default"
	agentQuestionWindowUnlimitedKey i18n.Key = "settings.text.agent_question_window_unlimited"
	agentQuestionWindowCustomKey    i18n.Key = "settings.desc.agent_question_window_custom"
)

// The cost of the setting is shown as two passive rows, one sentence each, so
// each fits a narrow terminal better than one long row.
const (
	agentQuestionsScopeText    = "Applies to every Claude Agent on this machine: each question waits in a projmux popup for the wait window (with Unlimited, until you answer)."
	agentQuestionsPerAgentText = "To change one Agent only, run projmux agent question enable <agent>."
)

// agentQuestionWindowPresets are the chooser's fixed windows, in seconds.
var agentQuestionWindowPresets = []int{60, 300, 600, config.DefaultAgentQuestionWindowSeconds, 1800, 3600}

func agentQuestionAnsweringLabelLocale(locale i18n.Locale, answering config.AgentQuestionAnswering) string {
	if answering == config.AgentQuestionAnsweringProjmux {
		return localizeText(locale, agentQuestionAnsweringTmuxKey, "projmux popup (way 2)")
	}
	return localizeText(locale, agentQuestionAnsweringClaudeKey, "Claude Code prompt (way 1, default)")
}

func agentQuestionWindowLabelLocale(locale i18n.Locale, seconds int) string {
	switch seconds {
	case config.UnlimitedAgentQuestionWindowSeconds:
		return localizeText(locale, agentQuestionWindowUnlimitedKey, "Unlimited — until answered (at most 2147468s, about 24.8 days)")
	case config.DefaultAgentQuestionWindowSeconds:
		return strings.NewReplacer("{seconds}", strconv.Itoa(seconds)).Replace(localizeText(locale, agentQuestionWindowDefaultKey, "{seconds}s (default)"))
	}
	return strings.NewReplacer("{seconds}", strconv.Itoa(seconds)).Replace(localizeText(locale, agentQuestionWindowSecondsKey, "{seconds}s"))
}

// currentAgentQuestionAnswering and currentAgentQuestionWindowSeconds read the
// same files, through the same loaders, the question hook reads.
func (c *settingsCommand) currentAgentQuestionAnswering() config.AgentQuestionAnswering {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.AgentQuestionAnsweringClaude
	}
	return claudeQuestionAnsweringFromPaths(paths)
}

func (c *settingsCommand) currentAgentQuestionWindowSeconds() int {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.DefaultAgentQuestionWindowSeconds
	}
	seconds, _ := config.LoadAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile())
	return seconds
}

// agentQuestionsSummary is the AI root row tail: both current values.
func (c *settingsCommand) agentQuestionsSummary() string {
	locale := c.locale()
	return agentQuestionAnsweringLabelLocale(locale, c.currentAgentQuestionAnswering()) + " - " +
		agentQuestionWindowLabelLocale(locale, c.currentAgentQuestionWindowSeconds())
}

// aiAgentQuestionsEntries builds the Agent questions view: the two cost rows,
// then the two value rows with their current values.
func (c *settingsCommand) aiAgentQuestionsEntries() []intpickercompat.Entry {
	locale := c.locale()
	return []intpickercompat.Entry{
		c.backEntry(),
		{
			Label:     settingsResolvedLabelDimLocale(locale, settingsCatalogTextLocale(locale, "Scope"), localizeText(locale, agentQuestionsScopeKey, agentQuestionsScopeText)),
			Value:     settingsNoopValue,
			SearchKey: "agent questions scope every claude agent machine popup wait window unlimited",
		},
		{
			Label:     settingsResolvedLabelDimLocale(locale, localizeText(locale, agentQuestionsPerAgentNameKey, "One Agent"), localizeText(locale, agentQuestionsPerAgentKey, agentQuestionsPerAgentText)),
			Value:     settingsNoopValue,
			SearchKey: "agent question enable per agent one agent",
		},
		{
			Label:     settingsNodeRowLabelLocale(locale, settingsNavAIQuestions+".answering", settingsGlyphOpen, settingsColorType, agentQuestionAnsweringLabelLocale(locale, c.currentAgentQuestionAnswering())),
			Value:     settingsAIAgentQuestionAnswering,
			SearchKey: "agent question answering AskUserQuestion way claude code prompt projmux popup agent-question-answering",
		},
		{
			Label:     settingsNodeRowLabelLocale(locale, settingsNavAIQuestions+".window", settingsGlyphOpen, settingsColorType, agentQuestionWindowLabelLocale(locale, c.currentAgentQuestionWindowSeconds())),
			Value:     settingsAIAgentQuestionWindow,
			SearchKey: "agent question wait window timeout seconds unlimited AskUserQuestion agent-question-window-seconds",
		},
	}
}

// aiAgentQuestionAnsweringEntries is the compact way chooser.
func (c *settingsCommand) aiAgentQuestionAnsweringEntries() []intpickercompat.Entry {
	locale := c.locale()
	current := c.currentAgentQuestionAnswering()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: settingsNodeRowLabelInfoLocale(locale, settingsNavAIQuestions+".answering", agentQuestionAnsweringLabelLocale(locale, current), ""),
			Value: settingsNoopValue,
		},
	}
	for _, way := range []config.AgentQuestionAnswering{config.AgentQuestionAnsweringClaude, config.AgentQuestionAnsweringProjmux} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if way == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsResolvedLabelLocale(locale, glyph, color, agentQuestionAnsweringLabelLocale(locale, way), ""),
			Value:     settingsActionPrefixAIQuestionAnswering + string(way),
			SearchKey: "agent question answering AskUserQuestion " + string(way),
		})
	}
	return entries
}

// aiAgentQuestionWindowEntries is the window chooser: the presets, a saved
// in-range value that is not a preset, Unlimited, and the custom input row.
func (c *settingsCommand) aiAgentQuestionWindowEntries() []intpickercompat.Entry {
	locale := c.locale()
	current := c.currentAgentQuestionWindowSeconds()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: settingsNodeRowLabelInfoLocale(locale, settingsNavAIQuestions+".window", agentQuestionWindowLabelLocale(locale, current), ""),
			Value: settingsNoopValue,
		},
	}
	choices := slices.Clone(agentQuestionWindowPresets)
	if current != config.UnlimitedAgentQuestionWindowSeconds && !slices.Contains(choices, current) {
		choices = append(choices, current)
		slices.Sort(choices)
	}
	choices = append(choices, config.UnlimitedAgentQuestionWindowSeconds)
	// The rows carry no SearchKey, like the dedupe presets: a key such as
	// "60" would let a typed "600s" match the 60s row, so the picker's own
	// scored match over the rendered label decides.
	for _, seconds := range choices {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if seconds == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		value := strconv.Itoa(seconds)
		if seconds == config.UnlimitedAgentQuestionWindowSeconds {
			value = config.AgentQuestionWindowUnlimitedWord
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsResolvedLabelLocale(locale, glyph, color, agentQuestionWindowLabelLocale(locale, seconds), ""),
			Value: settingsActionPrefixAIQuestionWindow + value,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsResolvedLabelLocale(locale, settingsGlyphType, settingsColorType, settingsCatalogTextLocale(locale, "Custom seconds"), localizeText(locale, agentQuestionWindowCustomKey, "store 60 to 3600 seconds")),
		Value: settingsActionPrefixAIQuestionWindow + "custom",
	})
	return entries
}

// parseAgentQuestionWindow reads one chooser value or typed input: the
// unlimited word, or whole seconds in 60..3600.
func parseAgentQuestionWindow(raw string) (int, error) {
	text := strings.TrimSpace(raw)
	if strings.EqualFold(text, config.AgentQuestionWindowUnlimitedWord) {
		return config.UnlimitedAgentQuestionWindowSeconds, nil
	}
	seconds, err := strconv.Atoi(text)
	if err != nil || seconds < config.MinAgentQuestionWindowSeconds || seconds > config.MaxAgentQuestionWindowSeconds {
		return 0, fmt.Errorf("agent question window %q must be %d..%d seconds", raw, config.MinAgentQuestionWindowSeconds, config.MaxAgentQuestionWindowSeconds)
	}
	return seconds, nil
}

func (c *settingsCommand) runAIAgentQuestionsSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-agent-questions",
			Entries:    c.aiAgentQuestionsEntries(),
			Title:      "AI - Agent questions",
			Prompt:     "Settings > AI > Agent questions > ",
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
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		case settingsAIAgentQuestionAnswering:
			if err := c.runAIAgentQuestionAnsweringSection(stdout, stderr); err != nil {
				return err
			}
		case settingsAIAgentQuestionWindow:
			if err := c.runAIAgentQuestionWindowSection(stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown agent questions settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIAgentQuestionAnsweringSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-agent-question-answering",
			Entries:    c.aiAgentQuestionAnsweringEntries(),
			Title:      "AI - Answering",
			Prompt:     "Settings > AI > Agent questions > Answering > ",
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
		case strings.HasPrefix(action, settingsActionPrefixAIQuestionAnswering):
			way := config.AgentQuestionAnswering(strings.TrimPrefix(action, settingsActionPrefixAIQuestionAnswering))
			if way != config.AgentQuestionAnsweringClaude && way != config.AgentQuestionAnsweringProjmux {
				return fmt.Errorf("unknown agent question answering way: %s", action)
			}
			if err := c.runSettingsMutation("Agent question answering", stdout, stderr, func(out, _ io.Writer) error {
				return c.setAgentQuestionAnswering(way, out)
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown agent question answering action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIAgentQuestionWindowSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-agent-question-window",
			Entries:    c.aiAgentQuestionWindowEntries(),
			Title:      "AI - Wait window",
			Prompt:     "Settings > AI > Agent questions > Wait window > ",
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
		case action == settingsActionPrefixAIQuestionWindow+"custom":
			if err := c.runAIAgentQuestionWindowCustom(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixAIQuestionWindow):
			seconds, err := parseAgentQuestionWindow(strings.TrimPrefix(action, settingsActionPrefixAIQuestionWindow))
			if err != nil {
				return err
			}
			if err := c.runSettingsMutation("Agent question window", stdout, stderr, func(out, _ io.Writer) error {
				return c.setAgentQuestionWindowSeconds(seconds, out)
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown agent question window action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIAgentQuestionWindowCustom(stdout, stderr io.Writer) error {
	initial := strconv.Itoa(config.DefaultAgentQuestionWindowSeconds)
	if current := c.currentAgentQuestionWindowSeconds(); current != config.UnlimitedAgentQuestionWindowSeconds {
		initial = strconv.Itoa(current)
	}
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-ai-agent-question-window-custom",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: initial,
		Title:        "AI - Custom wait window",
		Prompt:       "Wait window seconds > ",
		Footer:       projmuxFooter("Enter: save  |  Example: 900 "),
		ExpectKeys:   []string{"enter"},
		Bindings:     c.settingsCloseBindings(),
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	seconds, err := parseAgentQuestionWindow(result.Query)
	if err == nil && seconds == config.UnlimitedAgentQuestionWindowSeconds {
		// The custom row takes seconds only; Unlimited has its own row.
		err = fmt.Errorf("agent question window %q must be %d..%d seconds", result.Query, config.MinAgentQuestionWindowSeconds, config.MaxAgentQuestionWindowSeconds)
	}
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		c.setSettingsFeedback("Agent question window failed", err.Error())
		return nil
	}
	return c.runSettingsMutation("Agent question window", stdout, stderr, func(out, _ io.Writer) error {
		return c.setAgentQuestionWindowSeconds(seconds, out)
	})
}

// setAgentQuestionAnswering writes the central answering file. An Agent opted
// in with `projmux agent question enable` stays way 2 whatever is saved here.
func (c *settingsCommand) setAgentQuestionAnswering(way config.AgentQuestionAnswering, stdout io.Writer) error {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), way); err != nil {
		return err
	}
	way = config.NormalizeAgentQuestionAnswering(string(way))
	if _, err := fmt.Fprintf(stdout, "Agent question answering: %s\n", way); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "Agent question answering: "+string(way))
	}
	return nil
}

// setAgentQuestionWindowSeconds writes the central window file. The installed
// hook timeout is a fixed ceiling, so the next question already waits this
// long; nothing is re-integrated.
func (c *settingsCommand) setAgentQuestionWindowSeconds(seconds int, stdout io.Writer) error {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile(), seconds); err != nil {
		return err
	}
	shown := strconv.Itoa(seconds) + "s"
	if seconds == config.UnlimitedAgentQuestionWindowSeconds {
		shown = config.AgentQuestionWindowUnlimitedWord
	}
	if _, err := fmt.Fprintf(stdout, "Agent question window: %s\n", shown); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "Agent question window: "+shown)
	}
	return nil
}
