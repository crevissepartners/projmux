package core

// ResumeHostLostPolicy states whether a process Agent resume must or must not
// recover an Agent whose host process and child are both gone. The empty value
// keeps the existing behavior: a host-lost Agent is accepted unless the request
// is WaitForPeer.
type ResumeHostLostPolicy string

const (
	// ResumeHostLostRequire resumes only a host-lost Agent. An Agent that is not
	// host-lost is refused with process-resume-not-resumable (or
	// process-resume-owned while an owner is alive) and nothing changes.
	ResumeHostLostRequire ResumeHostLostPolicy = "require"
	// ResumeHostLostRefuse refuses a host-lost Agent with the token an ordinary
	// resume uses when host-lost recovery is not admitted, and changes nothing.
	ResumeHostLostRefuse ResumeHostLostPolicy = "refuse"
)

// ResumeAgentRequest resumes one Agent. Process Agents require Detach. Project and
// Window optionally scope Ref. No caller, active Pane, or prompt is inferred.
type ResumeAgentRequest struct {
	Ref, Project, Window string
	Model, Effort        string
	DialogueReplyOnly    bool
	SocketPath           string
	// Process-only inputs. Binding, when nonzero, fences the observed generation.
	Detach, WaitForPeer bool
	Prompt              string
	Binding             ProcessBinding
	// Preparation fences a prepared Claude first input. Nil preserves ordinary Resume.
	Preparation *AgentResumePreparation
	// HostLost is a process-only policy for a host-lost Agent. Empty preserves
	// ordinary Resume. Unknown values, require with WaitForPeer, and any value on a
	// tmux Agent are refused with resume-request-invalid before effects.
	HostLost ResumeHostLostPolicy
}

// ResumeAgentResult identifies the committed rebind. Resume returns immediately
// after commit, without waiting for provider activation, just like the CLI.
type ResumeAgentResult struct {
	Agent      Agent
	PaneID     string
	Notices    []string
	Projection ResourceList
	Receipt    OperationReceipt
	Binding    ProcessBinding
	// ClaimWaiting proves a child-free owner claim, not a new provider generation.
	ClaimWaiting bool
	// MayBeTruncated copies the successful process Resume owner's existing CORE
	// Previous.MayBeTruncated advisory. True means prior input may have been cut
	// off; it proves neither actual truncation nor a recorded Wait. It is false
	// for failures, tmux, claim waiting and CORE results without that advisory.
	MayBeTruncated bool
	// SettingsApplication is the settings application the CLI reports for the
	// same successful resume, from a process (frozen record or not, ordinary or
	// host-lost) or tmux Agent. It is nil only for failures and claim waiting,
	// and when a process owner built before this field omits it.
	SettingsApplication *AgentSettingsApplication
	// Preparation is nil on success. On failure it may acknowledge the unchanged
	// plan or the same frozen plan readdressed after an exact failed child Wait.
	Preparation *AgentResumePreparation
}
