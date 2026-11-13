package core

// AttachAgentPersonaRequest selects an exact Claude Agent and a named persona.
// DryRun has no effects; Confirm permits interrupting a busy conversation.
// SocketPath is an absolute tmux socket path naming one live server. Empty
// falls back to Options.SocketPath, and when that is empty too to the logical
// app socket "projmux". A real resume of an Offline or Failed Agent starts the
// app server when none runs; a dry run never does, and a Running Agent whose
// server is gone is refused.
type AttachAgentPersonaRequest struct {
	Ref, Project, Window, Persona, SocketPath string
	DryRun, Confirm                           bool
}

// DetachAgentPersonaRequest selects an exact Claude Agent whose persona is cleared.
// SocketPath has the meaning it has on AttachAgentPersonaRequest.
type DetachAgentPersonaRequest struct {
	Ref, Project, Window, SocketPath string
	DryRun, Confirm                  bool
}

// AgentPersonaResult preserves the persona CLI projection, including partial
// progress on failure. Notices and Recovery describe residual state.
type AgentPersonaResult struct {
	Action               string               `json:"action"`
	DryRun               bool                 `json:"dryRun"`
	Outcome              string               `json:"outcome"`
	AgentUID             string               `json:"agentUID"`
	AgentName            string               `json:"agentName"`
	Provider             string               `json:"provider"`
	Phase                AgentPhase           `json:"phase"`
	Interaction          AgentInteractionKind `json:"interaction"`
	PaneUID              string               `json:"paneUID,omitempty"`
	NewPaneUID           string               `json:"newPaneUID,omitempty"`
	CurrentPersona       string               `json:"currentPersona,omitempty"`
	CurrentPersonaDigest string               `json:"currentPersonaDigest,omitempty"`
	NewPersona           string               `json:"newPersona,omitempty"`
	NewPersonaDigest     string               `json:"newPersonaDigest,omitempty"`
	SystemPromptSnapshot string               `json:"systemPromptSnapshot"`
	Restart              bool                 `json:"restart"`
	ConfirmationRequired bool                 `json:"confirmationRequired"`
	Unchanged            bool                 `json:"unchanged"`
	Notices              []string             `json:"notices,omitempty"`
	Recovery             string               `json:"recovery,omitempty"`
}
