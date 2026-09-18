package app

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/web"
)

// The Settings the browser reads back. Only settings a web page consumes are
// here: what a split launches, the status bar parts, and the language.
//
// Front settings -- `ai.splitCwdFrom` and the status bar parts other than
// resources -- are the web's own: a change is saved to web.toml only, and the
// web shows web.toml's value over the TUI's (web_settings_layer.go). Central
// settings -- enabled providers, locale, live resources -- are saved with the
// same function the TUI Settings uses, so both surfaces end in the same files.

// webSettingsEnv is the environment the Settings functions see from the web
// server: the process environment without any tmux client evidence. The
// server never runs inside tmux, and a stale TMUX here would aim a reload at
// whatever server the variable named.
func webSettingsEnv(key string) string {
	switch key {
	case "TMUX", "TMUX_PANE", runtimeMutationAnchorPaneEnv:
		return ""
	}
	return os.Getenv(key)
}

// webSettingsCommand builds the Settings command the web saves through. A
// variable so a test can point it at a temporary home and a fake tmux.
var webSettingsCommand = func() *settingsCommand {
	return &settingsCommand{
		homeDir:         os.UserHomeDir,
		lookupEnv:       webSettingsEnv,
		tmuxRunner:      inttmux.ExecRunner{},
		reloadAppServer: true,
	}
}

type webSettingChoice struct {
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type webUsageWindowSetting struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Visible bool   `json:"visible"`
}

type webUsageProviderSetting struct {
	ID      string                  `json:"id"`
	Name    string                  `json:"name"`
	Visible bool                    `json:"visible"`
	Windows []webUsageWindowSetting `json:"windows"`
}

type webSettings struct {
	AI struct {
		DefaultMode  string             `json:"defaultMode"`
		Modes        []webSettingChoice `json:"modes"`
		Providers    []webSettingChoice `json:"providers"`
		SplitCwdFrom string             `json:"splitCwdFrom"`
		SplitOrigin  string             `json:"splitCwdOrigin"`
	} `json:"ai"`
	Statusbar struct {
		webStatusbar
		ResourcesSupported bool                      `json:"resourcesSupported"`
		UsageProviders     []webUsageProviderSetting `json:"usageProviders"`
	} `json:"statusbar"`
	Locale struct {
		Value   string   `json:"value"`
		Choices []string `json:"choices"`
	} `json:"locale"`
	// Origins names, per front-setting PATCH key, the layer its value came
	// from: "web" (web.toml), "tui" (a saved TUI file), or "default"; for
	// ai.splitCwdFrom, "web", "global" or "default".
	Origins map[string]string `json:"origins"`
}

func (b *webBackend) Settings(ctx context.Context) (any, error) {
	c := webSettingsCommand()
	launcher := webLaunchCommand()
	var out webSettings

	out.AI.DefaultMode = launcher.getMode()
	enabled := aiEnabledAgents(c.homeDir, c.lookupEnv)
	modes := []string{aiModeSelective, aiModeResume}
	for _, provider := range aiprovider.SettingsVisible() {
		modes = append(modes, string(provider.ID))
	}
	modes = append(modes, aiModeShell)
	for _, mode := range modes {
		on := true
		if provider, ok := aiModeProvider(mode); ok {
			on = aiEnabledAgentsContains(enabled, provider)
		}
		out.AI.Modes = append(out.AI.Modes, webSettingChoice{Value: mode, Enabled: on})
	}
	for _, provider := range config.KnownAIAgentProviders() {
		out.AI.Providers = append(out.AI.Providers, webSettingChoice{Value: string(provider), Enabled: aiEnabledAgentsContains(enabled, provider)})
	}
	layer, err := loadWebSettingsLayer(c.homeDir, c.lookupEnv)
	if err != nil {
		return nil, err
	}
	out.Origins = map[string]string{}
	split, err := resolveWebSplitCWDSource("", "", c.homeDir, c.lookupEnv)
	if err != nil {
		return nil, err
	}
	out.AI.SplitCwdFrom, out.AI.SplitOrigin = string(split.Source), string(split.Origin)
	out.Origins[webSettingSplitCWDFrom] = string(split.Origin)

	out.Statusbar.webStatusbar = webStatusbarOf(layer)
	for _, key := range webStatusbarSettingKeys {
		_, out.Origins[key] = layer.visible(key)
	}
	out.Statusbar.ResourcesSupported = systemstatus.Supported()
	out.Statusbar.UsageProviders = []webUsageProviderSetting{}
	for _, capability := range usagecmd.HUDProviderCapabilities() {
		id := string(capability.ID)
		key := webUsageSettingKey(id, "")
		row := webUsageProviderSetting{ID: id, Name: capability.DisplayName, Windows: []webUsageWindowSetting{}}
		row.Visible, out.Origins[key] = layer.visible(key)
		for _, window := range capability.Windows {
			key := webUsageSettingKey(id, window.Key)
			setting := webUsageWindowSetting{Key: window.Key, Label: window.Label}
			setting.Visible, out.Origins[key] = layer.visible(key)
			row.Windows = append(row.Windows, setting)
		}
		out.Statusbar.UsageProviders = append(out.Statusbar.UsageProviders, row)
	}

	locale, _, err := c.currentGlobalLocaleSetting()
	if err != nil {
		return nil, err
	}
	out.Locale.Value = locale
	out.Locale.Choices = []string{i18n.LocaleSettingAuto, string(i18n.FallbackLocale), "ko-KR"}
	return out, nil
}

// UpdateSetting changes one setting. key names it; value is the new value,
// "on"/"off" for a toggle.
func (b *webBackend) UpdateSetting(ctx context.Context, req web.SettingRequest) (any, error) {
	c := webSettingsCommand()
	value := strings.TrimSpace(req.Value)
	onOff := func() (config.StatusbarVisibility, error) {
		switch value {
		case "on":
			return config.StatusbarVisibilityOn, nil
		case "off":
			return config.StatusbarVisibilityOff, nil
		}
		return "", web.InvalidRequest("value must be on or off")
	}
	b.mutations.Lock()
	err := func() error {
		key := strings.TrimSpace(req.Key)
		switch {
		case key == "ai.defaultMode":
			if normalizeAIMode(value) != value {
				return web.InvalidRequest("unknown launch target " + value)
			}
			return webLaunchCommand().setMode(value)
		case strings.HasPrefix(key, "ai.provider."):
			provider := config.AIAgentProvider(strings.TrimPrefix(key, "ai.provider."))
			mode, err := onOff()
			if err != nil {
				return err
			}
			current := aiEnabledAgentsContains(aiEnabledAgents(c.homeDir, c.lookupEnv), provider)
			if current == (mode == config.StatusbarVisibilityOn) {
				return nil
			}
			return c.toggleAIEnabledAgent(string(provider))
		case key == webSettingSplitCWDFrom:
			source, ok := parseSplitCWDSource(value)
			if !ok {
				return web.InvalidRequest("splitCwdFrom must be project or pane")
			}
			return saveWebSetting(c.homeDir, c.lookupEnv, key, string(source))
		case key == "locale":
			return c.setGlobalLocale(value)
		case key == "statusbar.resources":
			if _, err := onOff(); err != nil {
				return err
			}
			return c.setLiveResourcesMode(value)
		case strings.HasPrefix(key, "statusbar.usage."):
			mode, err := onOff()
			if err != nil {
				return err
			}
			provider, window, _ := strings.Cut(strings.TrimPrefix(key, "statusbar.usage."), ".")
			leaf := agentUsageVisibilityLeaf{provider: provider, window: window}
			if paths, err := configPaths(c.homeDir, c.lookupEnv); err != nil {
				return err
			} else if _, ok := agentUsageVisibilityPath(paths, leaf); !ok {
				return web.InvalidRequest("unknown usage setting " + key)
			}
			// Saved under the capability's own spelling, which is what
			// web.toml accepts back.
			capability, _ := agentUsageProviderCapability(provider)
			canonical := webUsageSettingKey(string(capability.ID), "")
			if window != "" {
				found, _ := agentUsageWindowCapability(provider, window)
				canonical = webUsageSettingKey(string(capability.ID), found.Key)
			}
			return saveWebSetting(c.homeDir, c.lookupEnv, canonical, string(mode))
		case strings.HasPrefix(key, "statusbar."):
			mode, err := onOff()
			if err != nil {
				return err
			}
			if slices.Contains(webStatusbarSettingKeys, key) {
				return saveWebSetting(c.homeDir, c.lookupEnv, key, string(mode))
			}
		}
		return web.InvalidRequest("unknown setting " + key)
	}()
	b.mutations.Unlock()
	if err != nil {
		var typed *web.Error
		if errors.As(err, &typed) {
			return nil, err
		}
		return nil, web.NewError(409, web.CodeRefused, err.Error())
	}
	return b.Settings(ctx)
}
