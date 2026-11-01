package core

// RelaunchAgentRequest restarts one exact Claude or Codex Agent in its
// recorded conversation. Nil Profile/Instructions preserve the layer; a pointer
// to an empty string selects none. SocketPath is an explicit absolute socket.
// No actor, active Pane, destination or first prompt is inferred.
// Process destinations require Detach; Binding optionally fences the source generation.
// AllowHostLost opts a same-host process relaunch into recovering an Agent whose
// host process and child are both gone, like PrepareAgentResumeRequest.AllowHostLost.
// Prepare asks a same-host process Claude relaunch with an empty Prompt and
// Detach to leave a frozen, ownerless recipe and return its proof in Preparation.
type RelaunchAgentRequest struct {
	Ref, Project, Window  string
	Host, Prompt          string
	Detach                bool
	Binding               ProcessBinding
	Model, Effort         string
	Profile, Instructions *string
	Reset                 []string
	SocketPath            string
	DryRun, Confirm       bool
	AllowHostLost         bool
	Prepare               bool
}

// RelaunchAgentResult keeps the plan even when execution fails. Recovery names
// the exact Agent and conversation when an immediate recovery is available.
// Recovery is empty after a fresh phase refusal; inspect the error, notices and
// receipt before further action. Operations are never automatically replayed.
//
// CurrentModel and CurrentEffort are the model and effort the Agent records
// from its last launch, the CLI's currentModel and currentEffort. They can
// differ from CurrentSettings, which holds what the layers resolve to now.
// PromptRequired and RelaunchCommand are the CLI's promptRequired and
// relaunchCommand: a lost-host Claude relaunch with an empty Prompt needs a first
// prompt, and RelaunchCommand names the recovery. They also accompany the refusal
// of that execution. Preparation is non-nil only when Prepare froze a recipe.
// SettingsApplication is non-nil only once the resume step has completed; a
// dry run, an unchanged outcome or a refusal before resume leaves it nil.
type RelaunchAgentResult struct {
	AgentUID, AgentName, Provider                    string
	CurrentHost, TargetHost                          string
	CurrentModel, CurrentEffort                      string
	Binding                                          ProcessBinding
	ClaimWaiting                                     bool
	PromptRequired                                   bool
	RelaunchCommand                                  string
	Preparation                                      *AgentResumePreparation
	Phase                                            AgentPhase
	Interaction                                      string
	PaneUID, NewPaneUID                              string
	ConversationID                                   string
	Outcome                                          string
	DryRun, Restart, ConfirmationRequired, Unchanged bool
	CurrentSettings, NewSettings                     RelaunchAgentSettings
	RelaunchReasons                                  []string
	Notices                                          []string
	Recovery                                         string
	Stop                                             DeleteResult
	Resume                                           ResumeAgentResult
	SettingsApplication                              *AgentSettingsApplication
}

// AgentSettingsApplication reports how one launch applied each Agent setting,
// the CLI's settingsApplication. NotAppliedCount counts not-applied entries.
type AgentSettingsApplication struct {
	Entries         []AgentSettingApplication
	NotAppliedCount int
}

// AgentSettingApplication is one setting's application. Setting, Status and
// Reason use the CLI's tokens. A nil Value or RequestedValue is absent; a
// pointer to "" is an explicit none. NextAction is the fallback-locale text
// for a not-applied entry and empty otherwise.
type AgentSettingApplication struct {
	Setting, Status, Reason string
	Value, RequestedValue   *string
	NextAction              string
}

// RelaunchAgentSettings exposes layer values without internal package imports.
type RelaunchAgentSettings struct {
	Profile                     RelaunchAgentProfile
	Instructions, Model, Effort RelaunchAgentSetting
}
type RelaunchAgentProfile struct{ Name, Digest, Source string }
type RelaunchAgentSetting struct {
	Value, Source, ProfileValue string
	Override                    bool
}
