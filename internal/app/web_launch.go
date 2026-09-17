package app

import (
	"context"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
)

// webLaunchProvider is one row of the web launcher, as the terminal's AI
// launch picker shows it.
type webLaunchProvider struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}

type webLaunchOptions struct {
	Providers []webLaunchProvider `json:"providers"`
	// DefaultMode is the configured default split target: a provider,
	// "shell", "selective" (ask each time), or "resume".
	DefaultMode   string   `json:"defaultMode"`
	ClaudeModels  []string `json:"claudeModels"`
	ClaudeEfforts []string `json:"claudeEfforts"`
}

// webLaunchCommand builds the launcher the options are read from. A variable
// so a test can supply one without a real home.
var webLaunchCommand = newAICommand

// LaunchOptions answers with the same rows the terminal's AI launch picker
// draws: the picker-eligible providers the operator enabled, in order.
func (b *webBackend) LaunchOptions(context.Context) (any, error) {
	launcher := webLaunchCommand()
	enabled := launcher.enabledAIAgents()
	out := webLaunchOptions{
		Providers:     []webLaunchProvider{},
		DefaultMode:   launcher.getMode(),
		ClaudeModels:  claudeModelAliases,
		ClaudeEfforts: claudeEffortLevels,
	}
	for _, provider := range aiprovider.PickerEligible() {
		if !aiEnabledAgentsContains(enabled, config.AIAgentProvider(provider.ID)) {
			continue
		}
		out.Providers = append(out.Providers, webLaunchProvider{
			ID:    string(provider.ID),
			Name:  provider.DisplayName,
			Ready: launcher.agentAvailable(string(provider.ID)),
		})
	}
	return out, nil
}
