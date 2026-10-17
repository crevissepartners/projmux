package settings

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

// This set mirrors the existing central seed and HUD capability authorities.
// The app sweep test detects drift without reversing the service dependency.
var centralVisibilityKeys = []string{
	"agent-usage-hud", "agent-usage-provider-claude", "agent-usage-provider-codex",
	"agent-usage-window-claude-5h", "agent-usage-window-claude-weekly",
	"agent-usage-window-codex-5h", "agent-usage-window-codex-weekly",
	"clock", "git", "notifications-hud", "project", "working-directory",
}

func centralFacadeKey(key string) bool {
	switch key {
	case "statusbar.resources", "ai.defaultMode", "statusbar.visibility":
		return true
	}
	return false
}

func readCentralFacade(p config.Paths, key string) (Value, error) {
	out := Value{Key: key}
	switch key {
	case "statusbar.resources":
		v, err := config.LoadLiveResourcesStateFile(p.LiveResourcesFile())
		if err != nil {
			return out, err
		}
		out.Resources = &ToggleState{Effective: string(v.Effective), Saved: v.Saved, Source: string(v.Source), Invalid: v.Invalid}
	case "ai.defaultMode":
		value, saved, err := config.LoadAINewWindowModeFile(p.AINewWindowModeFile())
		if err != nil {
			return out, err
		}
		if !saved {
			value = "selective"
		}
		out.DefaultMode = value
	case "statusbar.visibility":
		out.Visibility = make([]VisibilityState, 0, len(centralVisibilityKeys))
		for _, key := range centralVisibilityKeys {
			defaultValue := config.StatusbarVisibilityOn
			if key == "agent-usage-window-codex-5h" {
				defaultValue = config.StatusbarVisibilityOff
			}
			v, err := config.LoadCentralStatusbarVisibility(p.StatusbarDefaultsFile(), filepath.Join(p.ConfigDir, "statusbar-visibility-"+key), defaultValue)
			if err != nil {
				return out, err
			}
			out.Visibility = append(out.Visibility, VisibilityState{Key: key, Effective: string(v.Effective), Saved: v.Saved, Source: string(v.Source), Invalid: v.Invalid})
		}
	}
	return out, nil
}

func centralToggle(route, value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "on" && value != "off" {
		return "", usageError(fmt.Sprintf("config %s --set: invalid value %q; want on or off", route, value))
	}
	return value, nil
}

func (s Service) setCentralFacade(ctx context.Context, u Update) (Value, error) {
	var value string
	var err error
	patch := map[string]string{}
	switch u.Key {
	case "statusbar.resources":
		if u.Resources != nil {
			value, err = centralToggle("resources", *u.Resources)
		}
	case "ai.defaultMode":
		if u.DefaultMode != nil {
			var ok bool
			value, ok = config.ValidAINewWindowMode(*u.DefaultMode)
			if !ok {
				err = usageError(fmt.Sprintf("config ai-default-mode --set: invalid mode %q; want %s", *u.DefaultMode, strings.Join(config.AINewWindowModes, ", ")))
			}
		}
	case "statusbar.visibility":
		keys := make([]string, 0, len(u.Visibility))
		for key := range u.Visibility {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if !slices.Contains(centralVisibilityKeys, key) {
				return Value{}, &InputError{Code: "config-key-invalid", Message: "unknown central visibility leaf: " + key}
			}
			patch[key], err = centralToggle("statusbar-visibility", u.Visibility[key])
			if err != nil {
				return Value{}, err
			}
		}
	}
	if err != nil {
		return Value{}, err
	}
	p, err := s.Paths()
	if err != nil {
		return Value{}, err
	}
	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	switch u.Key {
	case "statusbar.resources":
		if u.Resources != nil {
			err = config.SaveLiveResourcesFile(p.LiveResourcesFile(), config.LiveResourcesMode(value))
		}
	case "ai.defaultMode":
		if u.DefaultMode != nil {
			err = config.SaveAINewWindowModeFile(p.AINewWindowModeFile(), value)
		}
	case "statusbar.visibility":
		if _, err = readCentralFacade(p, u.Key); err != nil {
			return Value{}, err
		}
		err = config.PatchStatusbarDefaults(ctx, p.StatusbarDefaultsFile(), patch)
	}
	if err != nil {
		return Value{}, err
	}
	return readCentralFacade(p, u.Key)
}
