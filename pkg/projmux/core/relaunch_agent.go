package core

// RelaunchAgentRequest restarts one exact Claude or Codex Agent in its
// recorded conversation. Nil Profile/Instructions preserve the layer; a pointer
// to an empty string selects none. SocketPath is an explicit absolute socket.
// No actor, active Pane, destination or first prompt is inferred.
// Process destinations require Detach; Binding optionally fences the source generation.
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
}

// RelaunchAgentResult keeps the plan even when execution fails. Recovery names
// the exact Agent and conversation when an immediate recovery is available.
// Recovery is empty after a fresh phase refusal; inspect the error, notices and
// receipt before further action. Operations are never automatically replayed.
type RelaunchAgentResult struct {
	AgentUID, AgentName, Provider                    string
	CurrentHost, TargetHost                          string
	Binding                                          ProcessBinding
	ClaimWaiting                                     bool
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
