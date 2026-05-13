package app

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	aiHookProviderClaude = "claude"
	aiHookProviderCodex  = "codex"

	aiHookActionNotify = "notify"
	aiHookActionQuiet  = "quiet"
	aiHookActionState  = "state"
)

//go:embed ai_hook_catalogs/claude.json
var defaultClaudeHookCatalogJSON string

//go:embed ai_hook_catalogs/codex.json
var defaultCodexHookCatalogJSON string

type aiHookCatalog struct {
	Provider        string               `json:"provider"`
	ObservedVersion string               `json:"observed_version,omitempty"`
	Events          []aiHookCatalogEvent `json:"events"`
}

type aiHookCatalogEvent struct {
	Name    string `json:"name"`
	Install bool   `json:"install"`
	Action  string `json:"action"`
}

type rawAIHookCatalog struct {
	Provider        string                  `json:"provider"`
	ObservedVersion string                  `json:"observed_version"`
	Events          []rawAIHookCatalogEvent `json:"events"`
}

type rawAIHookCatalogEvent struct {
	Name    string `json:"name"`
	Install *bool  `json:"install"`
	Action  string `json:"action"`
}

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

func aiHookCatalogInstallEvents(catalog aiHookCatalog) []string {
	events := make([]string, 0, len(catalog.Events))
	for _, event := range catalog.Events {
		if event.Install {
			events = append(events, event.Name)
		}
	}
	return events
}

func (c *aiCommand) loadAIHookCatalog(provider string) (aiHookCatalog, error) {
	catalog, err := defaultAIHookCatalog(provider)
	if err != nil {
		return aiHookCatalog{}, err
	}

	overridePath, err := c.aiHookCatalogOverridePath(provider)
	if err != nil {
		return aiHookCatalog{}, err
	}
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(overridePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return catalog, nil
		}
		return aiHookCatalog{}, fmt.Errorf("read AI hook catalog override %s: %w", overridePath, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return catalog, nil
	}

	override, err := parseAIHookCatalog(data, provider)
	if err != nil {
		return aiHookCatalog{}, fmt.Errorf("parse AI hook catalog override %s: %w", overridePath, err)
	}
	return mergeAIHookCatalog(catalog, override), nil
}

func defaultAIHookCatalog(provider string) (aiHookCatalog, error) {
	var raw string
	switch provider {
	case aiHookProviderClaude:
		raw = defaultClaudeHookCatalogJSON
	case aiHookProviderCodex:
		raw = defaultCodexHookCatalogJSON
	default:
		return aiHookCatalog{}, fmt.Errorf("unknown AI hook catalog provider %q", provider)
	}
	return parseAIHookCatalog([]byte(raw), provider)
}

func defaultAIHookInstallEvents(provider string) []string {
	catalog, err := defaultAIHookCatalog(provider)
	if err != nil {
		return nil
	}
	return aiHookCatalogInstallEvents(catalog)
}

func parseAIHookCatalog(data []byte, provider string) (aiHookCatalog, error) {
	var raw rawAIHookCatalog
	if err := json.Unmarshal(data, &raw); err != nil {
		return aiHookCatalog{}, err
	}
	raw.Provider = strings.TrimSpace(raw.Provider)
	if raw.Provider == "" {
		raw.Provider = provider
	}
	if raw.Provider != provider {
		return aiHookCatalog{}, fmt.Errorf("provider = %q, want %q", raw.Provider, provider)
	}

	catalog := aiHookCatalog{
		Provider:        raw.Provider,
		ObservedVersion: strings.TrimSpace(raw.ObservedVersion),
		Events:          make([]aiHookCatalogEvent, 0, len(raw.Events)),
	}
	seen := map[string]bool{}
	for _, rawEvent := range raw.Events {
		name := strings.TrimSpace(rawEvent.Name)
		if name == "" {
			return aiHookCatalog{}, errors.New("event name must not be empty")
		}
		if seen[name] {
			return aiHookCatalog{}, fmt.Errorf("duplicate event %q", name)
		}
		seen[name] = true

		action := strings.TrimSpace(rawEvent.Action)
		if action == "" {
			action = aiHookActionQuiet
		}
		if !validAIHookAction(action) {
			return aiHookCatalog{}, fmt.Errorf("event %s has invalid action %q", name, action)
		}
		install := true
		if rawEvent.Install != nil {
			install = *rawEvent.Install
		}
		catalog.Events = append(catalog.Events, aiHookCatalogEvent{
			Name:    name,
			Install: install,
			Action:  action,
		})
	}
	return catalog, nil
}

func mergeAIHookCatalog(base, override aiHookCatalog) aiHookCatalog {
	if strings.TrimSpace(override.ObservedVersion) != "" {
		base.ObservedVersion = override.ObservedVersion
	}
	index := map[string]int{}
	for i, event := range base.Events {
		index[event.Name] = i
	}
	for _, event := range override.Events {
		if i, ok := index[event.Name]; ok {
			base.Events[i] = event
			continue
		}
		index[event.Name] = len(base.Events)
		base.Events = append(base.Events, event)
	}
	return base
}

func validAIHookAction(action string) bool {
	switch action {
	case aiHookActionNotify, aiHookActionQuiet, aiHookActionState:
		return true
	default:
		return false
	}
}

func (c *aiCommand) aiHookCatalogOverridePath(provider string) (string, error) {
	configHome := strings.TrimSpace(c.env("XDG_CONFIG_HOME"))
	if configHome == "" {
		homeDir := c.homeDir
		if homeDir == nil {
			homeDir = os.UserHomeDir
		}
		home, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "projmux", "ai-hooks.d", provider+".json"), nil
}
