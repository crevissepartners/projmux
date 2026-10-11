package app

import (
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/settings"
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
	return settings.LoadCentralLocaleSetting(homeDir, lookupEnv)
}

// saveCentralLocale validates value before touching any file, then stores it
// as `[ui] locale`. It returns the trimmed value it saved. An unsupported value
// is a usage error; path and write failures are not.
func saveCentralLocale(homeDir func() (string, error), lookupEnv func(string) string, value string) (string, error) {
	return settings.SaveCentralLocale(homeDir, lookupEnv, value)
}

// loadCentralAgentQuestionAnswering and loadCentralAgentQuestionWindowSeconds
// read the same files, through the same loaders, the question hook reads.
func loadCentralAgentQuestionAnswering(homeDir func() (string, error), lookupEnv func(string) string) config.AgentQuestionAnswering {
	return settings.LoadCentralAgentQuestionAnswering(homeDir, lookupEnv)
}

func loadCentralAgentQuestionWindowSeconds(homeDir func() (string, error), lookupEnv func(string) string) int {
	return settings.LoadCentralAgentQuestionWindowSeconds(homeDir, lookupEnv)
}

// loadCentralAgentApprovalAnswering and loadCentralAgentApprovalWindowSeconds
// read the same files, through the same loaders, the permission hook reads.
func loadCentralAgentApprovalAnswering(homeDir func() (string, error), lookupEnv func(string) string) config.AgentApprovalAnswering {
	return settings.LoadCentralAgentApprovalAnswering(homeDir, lookupEnv)
}

func loadCentralAgentApprovalWindowSeconds(homeDir func() (string, error), lookupEnv func(string) string) int {
	return settings.LoadCentralAgentApprovalWindowSeconds(homeDir, lookupEnv)
}

func saveCentralAgentQuestionAnswering(homeDir func() (string, error), lookupEnv func(string) string, way config.AgentQuestionAnswering) (config.AgentQuestionAnswering, error) {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return "", err
	}
	return settings.SaveQuestionAnswering(paths, way)
}
func saveCentralAgentQuestionWindowSeconds(homeDir func() (string, error), lookupEnv func(string) string, seconds int) error {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return err
	}
	return settings.SaveQuestionWindow(paths, seconds)
}

func saveCentralAgentApprovalAnswering(homeDir func() (string, error), lookupEnv func(string) string, way config.AgentApprovalAnswering) error {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return err
	}
	return settings.SaveApprovalAnswering(paths, way)
}

func saveCentralAgentApprovalWindowSeconds(homeDir func() (string, error), lookupEnv func(string) string, seconds int) error {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return err
	}
	return settings.SaveApprovalWindow(paths, seconds)
}
