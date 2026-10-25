package app

import (
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/hookcatalog"
)

// The catalogs and their parsing live in hookcatalog, shared with the SDK.
// These names keep the command's spelling of them.
const (
	aiHookProviderClaude      = hookcatalog.ProviderClaude
	aiHookProviderCodex       = hookcatalog.ProviderCodex
	aiHookProviderAntigravity = hookcatalog.ProviderAntigravity

	aiHookActionNotify = hookcatalog.ActionNotify
	aiHookActionQuiet  = hookcatalog.ActionQuiet
	aiHookActionState  = hookcatalog.ActionState
)

type (
	aiHookCatalog      = hookcatalog.Catalog
	aiHookCatalogEvent = hookcatalog.Event
)

func (c *aiCommand) aiHookInstallEvents(provider string) ([]string, error) {
	catalog, err := c.loadAIHookCatalog(provider)
	if err != nil {
		return nil, err
	}
	return aiHookCatalogInstallEvents(catalog), nil
}

func (c *aiCommand) aiHookCatalogAction(provider, name string) (string, bool, error) {
	catalog, err := c.loadAIHookCatalog(provider)
	if err != nil {
		return "", false, err
	}
	for _, event := range catalog.Events {
		if event.Name == name {
			return event.Action, true, nil
		}
	}
	return "", false, nil
}

func (c *aiCommand) aiHookObservedVersion(provider string) string {
	catalog, err := c.loadAIHookCatalog(provider)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(catalog.ObservedVersion)
}

func aiHookCatalogInstallEvents(catalog aiHookCatalog) []string {
	return hookcatalog.InstallEvents(catalog)
}

func (c *aiCommand) loadAIHookCatalog(provider string) (aiHookCatalog, error) {
	catalog, err := defaultAIHookCatalog(provider)
	if err != nil {
		return aiHookCatalog{}, err
	}

	overridePath, err := c.aiHookCatalogOverridePath(provider)
	if isMissingHome(err) {
		// No config home: there is no override to read.
		return catalog, nil
	}
	if err != nil {
		return aiHookCatalog{}, err
	}
	return hookcatalog.ApplyOverride(catalog, overridePath, c.readFile)
}

func defaultAIHookCatalog(provider string) (aiHookCatalog, error) {
	return hookcatalog.Default(provider)
}

func defaultAIHookInstallEvents(provider string) []string {
	return hookcatalog.DefaultInstallEvents(provider)
}

func validAIHookAction(action string) bool {
	return hookcatalog.ValidAction(action)
}

func (c *aiCommand) aiHookCatalogOverridePath(provider string) (string, error) {
	paths, err := c.aiConfigPaths()
	if err != nil {
		return "", err
	}
	return paths.AIHookCatalogOverrideFile(provider), nil
}

// aiConfigPaths resolves the config directory from the command's own
// environment and home directory, so integration reads the same files a test
// with a fake HOME or XDG_CONFIG_HOME prepared. Without either it returns the
// missing-HOME reason, never a path relative to the working directory.
func (c *aiCommand) aiConfigPaths() (config.Paths, error) {
	if c.processCreateContext != nil {
		return c.processCreateContext.Paths, nil
	}
	configHome, err := config.ResolveConfigHome("", c.env("XDG_CONFIG_HOME"))
	if err != nil {
		homeDir := c.homeDir
		if homeDir == nil {
			homeDir = os.UserHomeDir
		}
		if configHome, err = config.ResolveConfigHome(resolvedHome(homeDir), c.env("XDG_CONFIG_HOME")); err != nil {
			return config.Paths{}, err
		}
	}
	return config.DefaultPaths(configHome, ""), nil
}
