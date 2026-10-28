package core

// WindowCreateIntent is one operator intent: a new Window in an explicit
// Project, with either the shell Pane a Window is created with (Agent nil) or
// a new Agent that replaces that shell in the same transaction, the shape
// `create window --provider` commits. The Window and its Agent commit together
// or not at all.
//
// Name and Labels belong to the Window; its Agent takes a generated name, as
// on the CLI route. Host is tmux (the empty default, as in CreateAgentRequest)
// or process, which requires Detach and an Agent. No host is inferred from the
// config.
type WindowCreateIntent struct {
	Project    string
	Name       string
	Labels     []string
	Host       string
	Detach     bool
	SocketPath string
	// RequireEnabledProvider refuses an Agent whose provider the providers
	// setting has switched off, in PreflightWindowIntent and again in
	// CreateWindowFromIntent before the first effect, on either host. Without
	// it the intent keeps CreateAgent's own rule for its host.
	RequireEnabledProvider bool
	Agent                  *WindowIntentAgent
}

// WindowIntentAgent is the new Agent of a WindowCreateIntent. Its fields mean
// what the same CreateAgentRequest fields mean and are checked by the same
// validator.
//
// CwdFrom is project (the default) or pane. Pane starts the Agent in the
// current directory of CwdPane, an exact uid: Pane of the same Project, when
// that directory is inside the Project tree; otherwise the Agent starts in the
// Project root and the result carries the CLI's fallback notice. Process
// Agents take only project.
//
// Creator declares an exact Agent (uid:<agent>). Operator records an
// in-process operator client and takes precedence over Creator, as in
// CreateAgentRequest; request data cannot produce one.
//
// Resume names an existing session to continue in the new Agent. It is
// reserved: a non-nil Resume is refused before any effect.
type WindowIntentAgent struct {
	Provider, Profile, Instructions, Model, Effort string
	Payload                                        []string
	Labels                                         []string
	CwdFrom, CwdPane                               string
	Creator                                        string
	Operator                                       OperatorCreator
	Resume                                         *ResumeSelection
}

// ResumeSelection names one recorded provider session by the provider, the
// conversation id, the source that listed it and its working directory.
type ResumeSelection struct {
	Provider, Conversation, Source, Cwd string
}

// WindowIntentPreview is what PreflightWindowIntent resolved for an intent it
// did not refuse. Shell reports a Window with no Agent. Provider is the
// provider the create will run, the profile's when the intent names none;
// Profile is the profile the create will record, empty for none.
type WindowIntentPreview struct {
	Shell    bool
	Host     string
	Provider string
	Profile  string
	CwdFrom  string
}

// WindowIntentResult is the committed Window and, for an Agent intent, its
// Agent. Projection lists the Window, then the Agent. Receipt is the
// create.window receipt with the Agent row the CLI adds. PaneID is the
// Window's remaining tmux Pane; Binding identifies a detached process
// generation and is zero for tmux. With an activation-unconfirmed error the
// result still names the committed resources to preserve; with a process
// startup error the Refusal's CreateProcessError keeps the recovery refs.
type WindowIntentResult struct {
	Window     Window
	Agent      *Agent
	Binding    ProcessBinding
	PaneID     string
	Notices    []string
	Projection ResourceList
	Receipt    OperationReceipt
}
