package core

// ReadAgentSettingsRequest selects the Agents ReadAgentSettings resolves:
// every Agent in the Registry when AgentUIDs is empty, otherwise exactly those
// Agent UIDs (bare UIDs, without a "uid:" prefix; duplicates are read once).
type ReadAgentSettingsRequest struct {
	AgentUIDs []string
}

// ReadAgentSettingsResult is one resolution per selected Agent, in AgentUID
// order.
type ReadAgentSettingsResult struct {
	Agents []AgentSettingsResolution
}

// AgentSettingsResolution is the settings an Agent's plain `agent relaunch`
// would resolve: Current is what the Agent recorded, New what that relaunch
// would run, and Reasons why they differ (empty when they do not). The values
// are the ones `agent relaunch --dry-run -o json` reports as currentSettings,
// newSettings and relaunchReasons.
//
// Provider is the provider the relaunch resolves for and Host the host it
// launches on ("tmux" or "process"). Only Claude and Codex Agents are layered;
// any other Agent has Layered false and no settings.
//
// Unavailable is set when the settings cannot be resolved now and that
// relaunch would be refused for it: it is the relaunch refusal reason token
// (relaunch-no-conversation, or the profile's own token), or
// resource-not-found for a requested UID the Registry does not hold. The other
// settings fields are then zero.
//
// Cacheable is set only by ReadAgentDisplaySettingsFromSnapshot: true when the
// row was planned and resolved without a failure this call observed, false
// when a prompt part could not be read, snapshotted or migrated, the
// instructions could not be read, or Unavailable is set. A prompt part that is
// off or omitted for a missing Project is not a failure. It describes this
// call only and promises nothing about later reads. ReadAgentSettings leaves
// it false, so its JSON is unchanged.
type AgentSettingsResolution struct {
	AgentUID     string
	Provider     string
	Host         string
	Layered      bool
	Current, New AgentLaunchSettings
	Reasons      []string
	Unavailable  string
	Cacheable    bool `json:"cacheable,omitempty"`
}

// AgentLaunchSettings are an Agent's layered settings on one side of a
// relaunch. Its JSON is the CLI's currentSettings and newSettings JSON byte
// for byte.
type AgentLaunchSettings struct {
	Profile      AgentLaunchProfile `json:"profile"`
	Instructions AgentLaunchSetting `json:"instructions"`
	Model        AgentLaunchSetting `json:"model"`
	Effort       AgentLaunchSetting `json:"effort"`
}

// AgentLaunchProfile is the profile layer: the profile's name, the digest of
// its content and where the choice of it came from.
type AgentLaunchProfile struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Source string `json:"source"`
}

// AgentLaunchSetting is one layered item: the value it resolves to and its
// source, the value the profile alone gives, and whether an override sets it.
type AgentLaunchSetting struct {
	Value        string `json:"value"`
	Source       string `json:"source"`
	ProfileValue string `json:"profileValue"`
	Override     bool   `json:"override"`
}

// SettingsInputPaths are the configuration files and directory, beneath one
// resolved configuration directory, whose content the agent guidance and one
// Project's prompt parts are read from: the user guidance, its policy, its
// fragments directory (whose .md files are read), the one-time migration
// receipt and its backup, and the Project's guidance and label link rules.
// The Project fields are empty when no Project is named. Nothing says the
// paths exist or can be read.
type SettingsInputPaths struct {
	GuidanceFile                 string
	GuidancePolicyFile           string
	GuidanceFragmentsDir         string
	GuidanceMigrationReceiptFile string
	GuidanceMigrationBackupFile  string
	ProjectGuidanceFile          string
	ProjectLinkRulesFile         string
}
