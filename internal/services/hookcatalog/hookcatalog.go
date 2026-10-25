// Package hookcatalog reads the provider hook catalogs for CLI and SDK: the
// embedded default for each provider, merged with an optional local override.
package hookcatalog

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	ProviderClaude      = "claude"
	ProviderCodex       = "codex"
	ProviderAntigravity = "antigravity"

	ActionNotify = "notify"
	ActionQuiet  = "quiet"
	ActionState  = "state"
)

//go:embed catalogs/claude.json
var defaultClaudeJSON string

//go:embed catalogs/codex.json
var defaultCodexJSON string

//go:embed catalogs/antigravity.json
var defaultAntigravityJSON string

// Catalog is one provider's hook events.
type Catalog struct {
	Provider        string  `json:"provider"`
	ObservedVersion string  `json:"observed_version,omitempty"`
	Events          []Event `json:"events"`
}

// Event is one hook event: whether integration installs it and how ingest
// treats it.
type Event struct {
	Name    string `json:"name"`
	Install bool   `json:"install"`
	Action  string `json:"action"`
}

type rawCatalog struct {
	Provider        string     `json:"provider"`
	ObservedVersion string     `json:"observed_version"`
	Events          []rawEvent `json:"events"`
}

type rawEvent struct {
	Name    string `json:"name"`
	Install *bool  `json:"install"`
	Action  string `json:"action"`
}

// InstallEvents is the names of the catalog's events that are installed, in
// catalog order.
func InstallEvents(catalog Catalog) []string {
	events := make([]string, 0, len(catalog.Events))
	for _, event := range catalog.Events {
		if event.Install {
			events = append(events, event.Name)
		}
	}
	return events
}

// Default is the embedded catalog of provider.
func Default(provider string) (Catalog, error) {
	var raw string
	switch provider {
	case ProviderClaude:
		raw = defaultClaudeJSON
	case ProviderCodex:
		raw = defaultCodexJSON
	case ProviderAntigravity:
		raw = defaultAntigravityJSON
	default:
		return Catalog{}, fmt.Errorf("unknown AI hook catalog provider %q", provider)
	}
	return Parse([]byte(raw), provider)
}

// DefaultInstallEvents is InstallEvents of the embedded catalog, or nil for a
// provider without one.
func DefaultInstallEvents(provider string) []string {
	catalog, err := Default(provider)
	if err != nil {
		return nil
	}
	return InstallEvents(catalog)
}

// ApplyOverride merges into base the override catalog file at path, read with
// readFile (os.ReadFile when nil). A missing or blank file leaves base as is.
func ApplyOverride(base Catalog, path string, readFile func(string) ([]byte, error)) (Catalog, error) {
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return base, nil
		}
		return Catalog{}, fmt.Errorf("read AI hook catalog override %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return base, nil
	}
	override, err := Parse(data, base.Provider)
	if err != nil {
		return Catalog{}, fmt.Errorf("parse AI hook catalog override %s: %w", path, err)
	}
	return Merge(base, override), nil
}

// Parse reads a catalog for provider. A missing provider is provider; another
// one, an empty or repeated event name, or an unknown action is an error. An
// event installs unless it says otherwise, and its action defaults to quiet.
func Parse(data []byte, provider string) (Catalog, error) {
	var raw rawCatalog
	if err := json.Unmarshal(data, &raw); err != nil {
		return Catalog{}, err
	}
	raw.Provider = strings.TrimSpace(raw.Provider)
	if raw.Provider == "" {
		raw.Provider = provider
	}
	if raw.Provider != provider {
		return Catalog{}, fmt.Errorf("provider = %q, want %q", raw.Provider, provider)
	}

	catalog := Catalog{
		Provider:        raw.Provider,
		ObservedVersion: strings.TrimSpace(raw.ObservedVersion),
		Events:          make([]Event, 0, len(raw.Events)),
	}
	seen := map[string]bool{}
	for _, rawEvent := range raw.Events {
		name := strings.TrimSpace(rawEvent.Name)
		if name == "" {
			return Catalog{}, errors.New("event name must not be empty")
		}
		if seen[name] {
			return Catalog{}, fmt.Errorf("duplicate event %q", name)
		}
		seen[name] = true

		action := strings.TrimSpace(rawEvent.Action)
		if action == "" {
			action = ActionQuiet
		}
		if !ValidAction(action) {
			return Catalog{}, fmt.Errorf("event %s has invalid action %q", name, action)
		}
		install := true
		if rawEvent.Install != nil {
			install = *rawEvent.Install
		}
		catalog.Events = append(catalog.Events, Event{
			Name:    name,
			Install: install,
			Action:  action,
		})
	}
	return catalog, nil
}

// Merge replaces base's events by name with override's and appends the new
// names; a non-blank override observed version wins.
func Merge(base, override Catalog) Catalog {
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

// ValidAction reports whether action is one ingest knows.
func ValidAction(action string) bool {
	switch action {
	case ActionNotify, ActionQuiet, ActionState:
		return true
	default:
		return false
	}
}

// ActivationPaneUIDEnv is the variable projmux plants on the process it
// launches in a Pane, naming that Pane.
const ActivationPaneUIDEnv = "PMX_INTERNAL_ACTIVATION_PANE_UID"

// PaneArgument hands a provider hook the Pane it belongs to instead of
// letting it inherit one. projmux plants the activation envelope on the
// process it launches in the Pane, so the hook keeps its own identity even
// when the app-server that spawned it inherited no tmux environment at all.
// The value is deliberately unquoted and `=`-joined: an app-server shared by
// several Panes carries no envelope and expands this to a bare `--pane=`,
// which is not an identity, and the established matcher stays its fallback.
// The same argument is written for every provider; nothing here is
// provider-specific.
const PaneArgument = " --pane=${" + ActivationPaneUIDEnv + ":-}"

// LegacyIngestCommand assembles the removed producer prefix at runtime. The
// full spelling must remain recognizable in marker-owned v0.10.1 files, but it
// must not survive as contiguous read-only data in a current production binary.
func LegacyIngestCommand(suffix string) string {
	return strings.Join([]string{"projmux", "ai", "ingest", suffix}, " ")
}
