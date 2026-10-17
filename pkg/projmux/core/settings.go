package core

// ConfigKey identifies one of the central public config surfaces.
type ConfigKey string

const (
	ConfigStatusbarResources  ConfigKey = "statusbar.resources"
	ConfigAIDefaultMode       ConfigKey = "ai.defaultMode"
	ConfigStatusbarVisibility ConfigKey = "statusbar.visibility"
	ConfigProviders           ConfigKey = "providers"
	ConfigLocale              ConfigKey = "locale"
	ConfigAgentQuestions      ConfigKey = "agent-questions"
	ConfigAgentApprovals      ConfigKey = "agent-approvals"
	ConfigAgentHost           ConfigKey = "agent-host"
)

type ProviderState struct {
	ID      string
	Enabled bool
}

// ConfigToggleState separates the effective setting from its stored value.
type ConfigToggleState struct{ Effective, Saved, Source, Invalid string }

// ConfigVisibilityState describes one canonical central visibility leaf.
type ConfigVisibilityState struct{ Key, Effective, Saved, Source, Invalid string }

// ConfigValue contains the fields for its Key. Source is the locale's config
// file path. Policy Window is whole seconds, or "unlimited" for questions.
type ConfigValue struct {
	Resources   *ConfigToggleState      `json:",omitempty"`
	DefaultMode string                  `json:",omitempty"`
	Visibility  []ConfigVisibilityState `json:",omitempty"`
	Key         ConfigKey
	Providers   []ProviderState
	Locale      string
	Source      string
	Answering   string
	Window      string
	// DefaultHost is tmux (default) or process for ConfigAgentHost.
	DefaultHost string
}

// ConfigUpdate enables/disables Provider, sets Locale, or changes supplied
// policy pointers. Nil policy fields preserve the existing setting. All policy
// values are validated before the first write, as on the CLI.
type ConfigUpdate struct {
	// Resources and DefaultMode preserve their saved values when nil.
	Resources   *string
	DefaultMode *string
	// Visibility patches canonical leaves, preserving unspecified fields.
	// Concurrent writers are not serialized and there is no compare-and-swap.
	Visibility map[string]string
	Key        ConfigKey
	Provider   string
	Enabled    bool
	Locale     string
	Answering  *string
	Window     *string
	// DefaultHost changes ConfigAgentHost; nil preserves its saved value.
	DefaultHost *string
}
