package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// The central settings are the settings every front (TUI, web, CLI) shares:
// `[ui] locale` in config.toml, the agent-question-answering file, and the
// agent-question-window-seconds file. These functions read, validate, and
// store them from the home and env resolvers alone; they never read a front
// (TUI) layer setting. A front adds only its own display on top, such as
// output lines or a tmux display-message.

// loadCentralLocaleSetting returns the saved `[ui] locale` (auto when unset)
// and the config.toml path it came from.
func loadCentralLocaleSetting(homeDir func() (string, error), lookupEnv func(string) string) (setting string, source string, err error) {
	path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
	if err != nil {
		return i18n.LocaleSettingAuto, "", err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return i18n.LocaleSettingAuto, path, err
	}
	setting = strings.TrimSpace(cfg.UI.Locale)
	if setting == "" {
		setting = i18n.LocaleSettingAuto
	}
	return setting, path, nil
}

// saveCentralLocale validates value before touching any file, then stores it
// as `[ui] locale`. It returns the trimmed value it saved. An unsupported value
// is a usage error; path and write failures are not.
func saveCentralLocale(homeDir func() (string, error), lookupEnv func(string) string, value string) (string, error) {
	value = strings.TrimSpace(value)
	switch value {
	case i18n.LocaleSettingAuto, string(i18n.FallbackLocale), "ko-KR":
	default:
		return "", usageError("unsupported locale setting: " + value)
	}
	path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
	if err != nil {
		return "", err
	}
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		cfg.UI.Locale = value
		return nil
	}); err != nil {
		return "", err
	}
	return value, nil
}

// loadCentralAgentQuestionAnswering and loadCentralAgentQuestionWindowSeconds
// read the same files, through the same loaders, the question hook reads.
func loadCentralAgentQuestionAnswering(homeDir func() (string, error), lookupEnv func(string) string) config.AgentQuestionAnswering {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.AgentQuestionAnsweringClaude
	}
	return claudeQuestionAnsweringFromPaths(paths)
}

func loadCentralAgentQuestionWindowSeconds(homeDir func() (string, error), lookupEnv func(string) string) int {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultAgentQuestionWindowSeconds
	}
	seconds, _ := config.LoadAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile())
	return seconds
}

// saveCentralAgentQuestionAnswering writes the central answering file and
// returns the normalized way it now holds.
func saveCentralAgentQuestionAnswering(homeDir func() (string, error), lookupEnv func(string) string, way config.AgentQuestionAnswering) (config.AgentQuestionAnswering, error) {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return "", err
	}
	if err := config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), way); err != nil {
		return "", err
	}
	return config.NormalizeAgentQuestionAnswering(string(way)), nil
}

// saveCentralAgentQuestionWindowSeconds writes the central window file. An
// out-of-range value is refused and nothing is written.
func saveCentralAgentQuestionWindowSeconds(homeDir func() (string, error), lookupEnv func(string) string, seconds int) error {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return err
	}
	return config.SaveAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile(), seconds)
}
