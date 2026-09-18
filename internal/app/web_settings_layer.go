package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/web"
)

// The web settings layer. A front setting -- one that changes only how the
// web looks or launches -- is saved to web.toml when the web changes it, and
// the web reads it as `web.toml value ?? the TUI/central value ?? default`.
// The TUI never reads web.toml, and a web change never writes a TUI file.
// Central settings (enabled providers, locale, live resources) are not front
// settings: the web still saves them through the TUI Settings functions.

// Where a front setting's value came from, reported in the settings
// response's `origins`.
const (
	webSettingOriginWeb     = "web"
	webSettingOriginTUI     = "tui"
	webSettingOriginDefault = "default"
)

const webSettingSplitCWDFrom = "ai.splitCwdFrom"

// webStatusbarSettingKeys are the status bar parts the web keeps in its own
// layer, by PATCH key. statusbar.resources is central and is not here.
var webStatusbarSettingKeys = []string{
	"statusbar.notifications",
	"statusbar.usage",
	"statusbar.project",
	"statusbar.working-directory",
	"statusbar.git",
	"statusbar.clock",
}

// webSettingsLayer is web.toml as the web reads it, over the TUI files it
// falls back to.
type webSettingsLayer struct {
	homeDir   func() (string, error)
	lookupEnv func(string) string
	values    config.WebSettings
}

func webSettingsFilePath(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return "", err
	}
	return paths.WebSettingsFile(), nil
}

// knownWebUsageSetting answers the dynamic web.toml keys: a usage provider or
// window exists when the HUD capabilities name it, spelled exactly as they do.
func knownWebUsageSetting(key string) bool {
	rest, ok := strings.CutPrefix(key, "statusbar.usage.")
	if !ok {
		return false
	}
	provider, window, hasWindow := strings.Cut(rest, ".")
	capability, ok := agentUsageProviderCapability(provider)
	if !ok || string(capability.ID) != provider {
		return false
	}
	if !hasWindow {
		return true
	}
	for _, candidate := range capability.Windows {
		if candidate.Key == window {
			return true
		}
	}
	return false
}

// webUsageSettingKey is the PATCH key of a usage provider ("" window) or one
// of its windows.
func webUsageSettingKey(provider, window string) string {
	if window == "" {
		return "statusbar.usage." + provider
	}
	return "statusbar.usage." + provider + "." + window
}

// webSettingsLayerError turns a web.toml that cannot be read into the error
// the web answers with: 409 refused, naming the file and line in the message
// and in details. Any other error passes through.
func webSettingsLayerError(err error) error {
	var typed *config.WebSettingsError
	if errors.As(err, &typed) {
		refused := web.NewError(http.StatusConflict, web.CodeRefused, typed.Error())
		refused.Details = map[string]any{"file": typed.Path, "line": typed.Line}
		return refused
	}
	return err
}

func loadWebSettingsLayer(homeDir func() (string, error), lookupEnv func(string) string) (webSettingsLayer, error) {
	path, err := webSettingsFilePath(homeDir, lookupEnv)
	if err != nil {
		return webSettingsLayer{}, err
	}
	values, err := config.LoadWebSettingsFile(path, knownWebUsageSetting)
	if err != nil {
		return webSettingsLayer{}, webSettingsLayerError(err)
	}
	return webSettingsLayer{homeDir: homeDir, lookupEnv: lookupEnv, values: values}, nil
}

// saveWebSetting records one front setting in web.toml and nowhere else.
func saveWebSetting(homeDir func() (string, error), lookupEnv func(string) string, key, value string) error {
	path, err := webSettingsFilePath(homeDir, lookupEnv)
	if err != nil {
		return err
	}
	err = config.UpdateWebSettingsFile(path, knownWebUsageSetting, func(settings *config.WebSettings) error {
		return settings.Set(key, value)
	})
	return webSettingsLayerError(err)
}

// tuiVisibility is the TUI's saved state of a status bar front key, read with
// the functions the TUI renders from.
func (l webSettingsLayer) tuiVisibility(key string) config.StatusbarVisibilityState {
	switch key {
	case "statusbar.notifications":
		return loadStatusbarHUDVisibilityState(l.homeDir, l.lookupEnv, statusbarHUDNotifications)
	case "statusbar.usage":
		return loadStatusbarHUDVisibilityState(l.homeDir, l.lookupEnv, statusbarHUDAgentUsage)
	case "statusbar.project", "statusbar.working-directory", "statusbar.git", "statusbar.clock":
		return loadStatusbarRowOneVisibilityState(l.homeDir, l.lookupEnv, statusbarRowOneComponent(strings.TrimPrefix(key, "statusbar.")))
	}
	if rest, ok := strings.CutPrefix(key, "statusbar.usage."); ok {
		provider, window, _ := strings.Cut(rest, ".")
		return loadAgentUsageVisibilityState(l.homeDir, l.lookupEnv, agentUsageVisibilityLeaf{provider: provider, window: window})
	}
	return config.DefaultStatusbarVisibilityState()
}

// visible is what the web shows for a status bar front key, and where that
// came from: the web's own value, else the TUI's saved value, else the
// default (which for a usage window is the window's capability default).
func (l webSettingsLayer) visible(key string) (bool, string) {
	if value, ok := l.values.Value(key); ok {
		return value == string(config.StatusbarVisibilityOn), webSettingOriginWeb
	}
	state := l.tuiVisibility(key)
	origin := webSettingOriginDefault
	if state.Source == config.StatusbarVisibilitySourceSaved {
		origin = webSettingOriginTUI
	}
	return state.Effective == config.StatusbarVisibilityOn, origin
}

// usageVisible is the per-leaf usage HUD visibility the web renders with.
// Each leaf is overlaid on its own; the provider gating of windows is applied
// by the HUD selection exactly as for the TUI.
func (l webSettingsLayer) usageVisible(provider aiprovider.ID, window string) bool {
	visible, _ := l.visible(webUsageSettingKey(string(provider), window))
	return visible
}

// resolveWebSplitCWDSource is where a web split starts: the request's value,
// then `[ai] split_cwd_from` in the owner Project's `.projmux/config.toml`,
// then the web layer, then the global config, then project. web.toml takes
// the place the web's old global write held, so a Project's config still
// beats a web change. A web.toml that cannot be read is the error, not a
// skipped tier. The TUI surfaces use resolveUISplitCWDSource, which never
// reads web.toml.
func resolveWebSplitCWDSource(flagValue, projectRoot string, homeDir func() (string, error), lookupEnv func(string) string) (splitCWDResolution, error) {
	// Without a home the UI resolver reads only the flag and project tiers.
	if resolved := resolveUISplitCWDSource(flagValue, projectRoot, nil, nil); resolved.Origin != splitCWDOriginDefault {
		return resolved, nil
	}
	layer, err := loadWebSettingsLayer(homeDir, lookupEnv)
	if err != nil {
		return splitCWDResolution{}, err
	}
	if value, ok := layer.values.Value(webSettingSplitCWDFrom); ok {
		if source, ok := parseSplitCWDSource(value); ok {
			return splitCWDResolution{Source: source, Origin: splitCWDOriginWeb}, nil
		}
	}
	return resolveUISplitCWDSource("", "", homeDir, lookupEnv), nil
}
