package core

// ConfigKey identifies one of the four central public config surfaces.
type ConfigKey string

const (
	ConfigProviders      ConfigKey = "providers"
	ConfigLocale         ConfigKey = "locale"
	ConfigAgentQuestions ConfigKey = "agent-questions"
	ConfigAgentApprovals ConfigKey = "agent-approvals"
)

type ProviderState struct {
	ID      string
	Enabled bool
}

// ConfigValue contains the fields for its Key. Source is the locale's config
// file path. Policy Window is whole seconds, or "unlimited" for questions.
type ConfigValue struct {
	Key       ConfigKey
	Providers []ProviderState
	Locale    string
	Source    string
	Answering string
	Window    string
}

// ConfigUpdate enables/disables Provider, sets Locale, or changes supplied
// policy pointers. Nil policy fields preserve the existing setting. All policy
// values are validated before the first write, as on the CLI.
type ConfigUpdate struct {
	Key       ConfigKey
	Provider  string
	Enabled   bool
	Locale    string
	Answering *string
	Window    *string
}
