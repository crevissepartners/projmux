package core

// Agent provider ids, as in ProviderState.ID and the Agent spec.
const (
	ProviderClaude      = "claude"
	ProviderCodex       = "codex"
	ProviderAntigravity = "antigravity"
)

// SettingsProvider is one agent provider a settings screen offers. It carries
// only what such a screen reads, not the provider's capability flags.
type SettingsProvider struct {
	// ID is the lower-case provider id.
	ID string
	// DisplayName is the provider's name in a settings list.
	DisplayName string
	// SettingsVisible is true for every provider the list functions return.
	SettingsVisible bool
	// IntegrationCommand is the CLI command that installs the provider's hook
	// integration, or empty when the provider has none.
	IntegrationCommand string
}
