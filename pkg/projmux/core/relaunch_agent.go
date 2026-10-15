package core

// RelaunchAgentRequest restarts one exact tmux Claude or Codex Agent in its
// recorded conversation. Nil Profile/Instructions preserve the layer; a pointer
// to an empty string selects none. SocketPath is an explicit absolute socket.
// No actor, active Pane, process destination or first prompt is inferred.
type RelaunchAgentRequest struct {
	Ref, Project, Window  string
	Model, Effort         string
	Profile, Instructions *string
	Reset                 []string
	SocketPath            string
	DryRun, Confirm       bool
}

// RelaunchAgentResult keeps the plan even when execution fails. Recovery names
// the exact Agent and conversation; callers must inspect the error and receipt
// before deciding what to do. Operations are never automatically replayed.
type RelaunchAgentResult struct {
	AgentUID, AgentName, Provider                    string
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
