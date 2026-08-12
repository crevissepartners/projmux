package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

const (
	aiHookActionSourceCatalog = "catalog"
	aiHookActionSourceRuntime = "runtime"
)

type aiHookActionResolution struct {
	Action string
	Source string
	Known  bool
}

func (c *aiCommand) aiHookEffectiveAction(provider, event string) aiHookActionResolution {
	action, known, _ := c.aiHookCatalogAction(provider, event)
	if strings.TrimSpace(action) == "" {
		action = aiHookActionQuiet
	}
	resolution := aiHookActionResolution{
		Action: action,
		Source: aiHookActionSourceCatalog,
		Known:  known,
	}
	if runtimeAction, ok := c.aiHookRuntimeAction(provider, event); ok {
		resolution.Action = runtimeAction
		resolution.Source = aiHookActionSourceRuntime
		resolution.Known = true
	}
	if !validAIHookAction(resolution.Action) {
		resolution.Action = aiHookActionQuiet
	}
	return resolution
}

func (c *aiCommand) aiHookRuntimeAction(provider, event string) (string, bool) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", false
	}
	file, err := config.LoadAIHookActionsFile(paths.AIHookActionsFile())
	if err != nil {
		return "", false
	}
	actions, ok := file.Providers[strings.TrimSpace(provider)]
	if !ok || actions.Events == nil {
		return "", false
	}
	action := strings.TrimSpace(actions.Events[strings.TrimSpace(event)])
	if !validAIHookAction(action) {
		return "", false
	}
	return action, true
}

func aiHookQuietReason(resolution aiHookActionResolution) string {
	switch resolution.Source {
	case aiHookActionSourceRuntime:
		return "runtime quiet event"
	default:
		if !resolution.Known {
			return "unknown event"
		}
		return "catalog quiet event"
	}
}

func aiHookStateReason(resolution aiHookActionResolution) string {
	if resolution.Source == aiHookActionSourceRuntime {
		return "runtime state event"
	}
	return "catalog state event"
}

func aiHookNoHandlerReason(resolution aiHookActionResolution) string {
	if resolution.Action == aiHookActionQuiet {
		return aiHookQuietReason(resolution)
	}
	if resolution.Source == aiHookActionSourceRuntime {
		return "runtime " + resolution.Action + " event has no specialized handler"
	}
	if !resolution.Known {
		return "unknown event"
	}
	return "catalog " + resolution.Action + " event has no specialized handler"
}

func (c *settingsCommand) aiHookActionsPath() (string, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", err
	}
	return paths.AIHookActionsFile(), nil
}

func (c *settingsCommand) currentAIHookActionsFile() config.AIHookActionsFile {
	path, err := c.aiHookActionsPath()
	if err != nil {
		return config.AIHookActionsFile{}
	}
	file, err := config.LoadAIHookActionsFile(path)
	if err != nil {
		return config.AIHookActionsFile{}
	}
	return file
}

func (c *settingsCommand) setAIHookAction(provider, event, action string, stdout io.Writer) error {
	provider = strings.TrimSpace(provider)
	event = strings.TrimSpace(event)
	action = strings.TrimSpace(action)
	if provider == "" || event == "" {
		return fmt.Errorf("AI hook action requires provider and event")
	}
	if !validAIHookAction(action) {
		return fmt.Errorf("AI hook action %q must be notify, state, or quiet", action)
	}
	path, err := c.aiHookActionsPath()
	if err != nil {
		return err
	}
	file, err := config.LoadAIHookActionsFile(path)
	if err != nil {
		return err
	}
	if file.Version == 0 {
		file.Version = 1
	}
	if file.Providers == nil {
		file.Providers = map[string]config.AIHookProviderActions{}
	}
	actions := file.Providers[provider]
	if actions.Events == nil {
		actions.Events = map[string]string{}
	}
	actions.Events[event] = action
	file.Providers[provider] = actions
	if err := config.SaveAIHookActionsFile(path, file); err != nil {
		return err
	}
	if stdout != nil {
		_, _ = fmt.Fprintf(stdout, "AI hook action: %s %s = %s\n", provider, event, action)
	}
	return nil
}

func (c *settingsCommand) clearAIHookAction(provider, event string, stdout io.Writer) error {
	provider = strings.TrimSpace(provider)
	event = strings.TrimSpace(event)
	if provider == "" || event == "" {
		return fmt.Errorf("AI hook action requires provider and event")
	}
	path, err := c.aiHookActionsPath()
	if err != nil {
		return err
	}
	file, err := config.LoadAIHookActionsFile(path)
	if err != nil {
		return err
	}
	if actions, ok := file.Providers[provider]; ok && actions.Events != nil {
		delete(actions.Events, event)
		if len(actions.Events) == 0 {
			delete(file.Providers, provider)
		} else {
			file.Providers[provider] = actions
		}
	}
	if err := config.SaveAIHookActionsFile(path, file); err != nil {
		return err
	}
	if stdout != nil {
		_, _ = fmt.Fprintf(stdout, "AI hook action: %s %s = catalog default\n", provider, event)
	}
	return nil
}
