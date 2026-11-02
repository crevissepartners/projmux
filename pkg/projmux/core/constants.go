package core

// ResourceAPIVersion is the apiVersion stamped on every Registry resource and
// carried by the APIVersion fields of the resource views.
const ResourceAPIVersion = "projmux.io/v1alpha1"

// Agent annotation keys under metadata.annotations. An absent key means the
// fact was not recorded.
const (
	// AnnotationAgentProfile names the profile the Agent was created from.
	AnnotationAgentProfile = "projmux.io/profile"
	// AnnotationAgentModel and AnnotationAgentEffort record the model and
	// effort the Agent was created with.
	AnnotationAgentModel  = "projmux.io/model"
	AnnotationAgentEffort = "projmux.io/effort"
	// AnnotationAgentProjectLinkRulesDigest is the digest of the Project label
	// link rules the Agent's instructions were built with.
	AnnotationAgentProjectLinkRulesDigest = "projmux.io/project-link-rules-digest"
	// AnnotationCreatorAgent is the bare UID of the Agent that created this one.
	AnnotationCreatorAgent = "projmux.io/creator-agent"
)

// NotifyFileName is the basename of the notification queue file inside the
// state directory.
const NotifyFileName = "notify.json"

// AgentQuestionAnswering is the way a Claude question is answered.
type AgentQuestionAnswering string

// AgentApprovalAnswering is the way a Claude permission request is answered.
type AgentApprovalAnswering string

const (
	// AgentQuestionAnsweringClaude is the default: Claude Code shows its own
	// question prompt.
	AgentQuestionAnsweringClaude AgentQuestionAnswering = "claude"
	// AgentQuestionAnsweringProjmux records the question so projmux answers it.
	AgentQuestionAnsweringProjmux AgentQuestionAnswering = "projmux"

	// AgentApprovalAnsweringClaude is the default: Claude Code shows its own
	// permission prompt.
	AgentApprovalAnsweringClaude AgentApprovalAnswering = "claude"
	// AgentApprovalAnsweringProjmux captures the request so projmux answers it.
	AgentApprovalAnsweringProjmux AgentApprovalAnswering = "projmux"
)

const (
	// DefaultAgentQuestionWindowSeconds and DefaultAgentApprovalWindowSeconds
	// are the windows used when nothing valid is saved.
	DefaultAgentQuestionWindowSeconds = 900
	DefaultAgentApprovalWindowSeconds = 900
)
