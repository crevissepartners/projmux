// Package core defines the experimental v0 resource read types.
// These types mirror CLI JSON; minor releases may change the API.
package core

import "time"

type Agent struct {
	APIVersion string      `json:"apiVersion"`
	Kind       Kind        `json:"kind"`
	Metadata   ObjectMeta  `json:"metadata"`
	Spec       AgentSpec   `json:"spec"`
	Status     AgentStatus `json:"status"`
}
type AgentActivation struct {
	State      AgentActivationState `json:"state"`
	ObservedAt time.Time            `json:"observedAt,omitzero"`
	Source     string               `json:"source,omitempty"`
	Reason     string               `json:"reason,omitempty"`
}

func (a AgentActivation) IsZero() bool {
	return (a.State == "" || a.State == ActivationNotRequested) && a.ObservedAt.IsZero() && a.Source == "" && a.Reason == ""
}

type AgentActivationState string
type AgentInteraction struct {
	Kind       AgentInteractionKind `json:"kind"`
	ObservedAt time.Time            `json:"observedAt,omitzero"`
	Source     string               `json:"source,omitempty"`
}

func (i AgentInteraction) IsZero() bool {
	return (i.Kind == "" || i.Kind == InteractionUnknown) && i.ObservedAt.IsZero() && i.Source == ""
}

type AgentInteractionKind string
type AgentPhase string
type AgentProgress struct {
	TurnRef         string                `json:"turnRef"`
	Activity        AgentProgressActivity `json:"activity,omitempty"`
	PlanCompleted   uint8                 `json:"planCompleted,omitempty"`
	PlanInProgress  uint8                 `json:"planInProgress,omitempty"`
	PlanTotal       uint8                 `json:"planTotal,omitempty"`
	PlanTruncated   bool                  `json:"planTruncated,omitempty"`
	ChangedFiles    uint16                `json:"changedFiles,omitempty"`
	FilesTruncated  bool                  `json:"filesTruncated,omitempty"`
	ActiveItemCount uint8                 `json:"activeItemCount,omitempty"`
	StartedAt       time.Time             `json:"startedAt,omitzero"`
	ObservedAt      time.Time             `json:"observedAt,omitzero"`
	Source          string                `json:"source"`
}

func (p AgentProgress) IsZero() bool {
	return p.TurnRef == "" && p.Activity == "" && p.PlanCompleted == 0 &&
		p.PlanInProgress == 0 && p.PlanTotal == 0 && !p.PlanTruncated &&
		p.ChangedFiles == 0 && !p.FilesTruncated && p.ActiveItemCount == 0 &&
		p.StartedAt.IsZero() && p.ObservedAt.IsZero() && p.Source == ""
}

type AgentProgressActivity string
type AgentSessionRef struct {
	Provider string `json:"provider"`

	ObservedAt time.Time `json:"observedAt"`

	Claude      *ClaudeSessionRef      `json:"claude,omitempty"`
	Codex       *CodexSessionRef       `json:"codex,omitempty"`
	Antigravity *AntigravitySessionRef `json:"antigravity,omitempty"`
}
type AgentSpec struct {
	Provider  string         `json:"provider,omitempty"`
	Workspace AgentWorkspace `json:"workspace,omitzero"`
}
type AgentStatus struct {
	Phase       AgentPhase       `json:"phase"`
	PaneRef     string           `json:"paneRef,omitempty"`
	SessionRef  *AgentSessionRef `json:"sessionRef,omitempty"`
	Interaction AgentInteraction `json:"interaction,omitzero"`
	Activation  AgentActivation  `json:"activation,omitzero"`
	Progress    AgentProgress    `json:"progress,omitzero"`
	Reason      string           `json:"reason,omitempty"`

	LastTermination  *TerminationEvidence `json:"lastTermination,omitempty"`
	LastTransitionAt time.Time            `json:"lastTransitionAt"`
}
type AgentWorkspace struct {
	CWD                     string   `json:"cwd"`
	AdditionalWritableRoots []string `json:"additionalWritableRoots,omitempty"`
}

func (w AgentWorkspace) IsZero() bool {
	return w.CWD == "" && len(w.AdditionalWritableRoots) == 0
}

type AntigravitySessionRef struct {
	ConversationID string `json:"conversationId"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
}
type ClaudeActivationBinding struct {
	Process                ProcessIdentity     `json:"process"`
	RegistrationSessionID  string              `json:"registrationSessionId,omitempty"`
	RegistrationGeneration string              `json:"registrationGeneration,omitempty"`
	Registration           *ClaudeRegistration `json:"registration,omitempty"`
}
type ClaudeAuthorityRef struct {
	SessionID              string          `json:"sessionId"`
	Process                ProcessIdentity `json:"process"`
	RegistrationGeneration string          `json:"registrationGeneration"`
	LeaseProcess           ProcessIdentity `json:"leaseProcess"`
}
type ClaudeRegistration struct {
	Authority ClaudeAuthorityRef `json:"authority"`
	Ready     bool               `json:"ready"`
}
type ClaudeSessionRef struct {
	SessionID      string `json:"sessionId"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
}
type CodexActivationBinding struct {
	ThreadID  string             `json:"threadId"`
	TurnID    string             `json:"turnId,omitempty"`
	Authority *CodexAuthorityRef `json:"authority,omitempty"`
}
type CodexAuthorityRef struct {
	StateDomainID        string `json:"stateDomainID"`
	EndpointGenerationID string `json:"endpointGenerationID"`
	BrokerRuntimeID      string `json:"brokerRuntimeID"`
	ConnectionEpoch      uint64 `json:"connectionEpoch"`
	BindingEpoch         uint64 `json:"bindingEpoch"`
}
type CodexEndpointRef struct {
	StateDomainID        string `json:"stateDomainID"`
	EndpointGenerationID string `json:"endpointGenerationID"`
}
type CodexGenerationLifecycleRef struct {
	State     CodexGenerationState         `json:"state"`
	Operation *CodexGenerationOperationRef `json:"operation,omitempty"`
}
type CodexGenerationOperationRef struct {
	ID       string           `json:"id"`
	Endpoint CodexEndpointRef `json:"endpoint"`
}
type CodexGenerationState string
type CodexHandoverResumeReceipt struct {
	OperationID       string           `json:"operationID"`
	SuccessorEndpoint CodexEndpointRef `json:"successorEndpoint"`
	AgentUID          string           `json:"agentUID"`
	PaneUID           string           `json:"paneUID"`
	PaneRuntimeID     string           `json:"paneRuntimeID"`
	PaneGeneration    string           `json:"paneGeneration"`
	ThreadID          string           `json:"threadID"`
}
type CodexSessionRef struct {
	ThreadID  string `json:"threadId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`

	HasStartedTurn bool              `json:"hasStartedTurn,omitempty"`
	Endpoint       *CodexEndpointRef `json:"endpoint,omitempty"`

	Lifecycle *CodexGenerationLifecycleRef `json:"lifecycle,omitempty"`

	HandoverResume *CodexHandoverResumeReceipt `json:"handoverResume,omitempty"`
}
type Condition struct {
	Type             string    `json:"type"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason,omitempty"`
	Message          string    `json:"message,omitempty"`
	FirstObservedAt  time.Time `json:"firstObservedAt"`
	LastTransitionAt time.Time `json:"lastTransitionAt"`
}
type Kind string
type ObjectMeta struct {
	UID         string            `json:"uid"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	OwnerRef    *OwnerRef         `json:"ownerRef,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
}
type OwnerRef struct {
	Kind Kind   `json:"kind"`
	UID  string `json:"uid"`
}
type Pane struct {
	APIVersion string     `json:"apiVersion"`
	Kind       Kind       `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       PaneSpec   `json:"spec"`
	Status     PaneStatus `json:"status"`
}
type PaneActivation struct {
	Kind        RuntimeKind        `json:"kind,omitempty"`
	Process     *ProcessActivation `json:"process,omitempty"`
	Generation  string             `json:"generation"`
	RuntimeID   string             `json:"runtimeID,omitempty"`
	AgentUID    string             `json:"agentUID,omitempty"`
	OperationID string             `json:"operationID,omitempty"`
	StartedAt   time.Time          `json:"startedAt,omitzero"`

	Codex  *CodexActivationBinding  `json:"codex,omitempty"`
	Claude *ClaudeActivationBinding `json:"claude,omitempty"`
}

func (a PaneActivation) IsZero() bool {
	return a.Kind == "" && a.Process == nil && a.Generation == "" && a.RuntimeID == "" && a.AgentUID == "" &&
		a.OperationID == "" && a.StartedAt.IsZero() && a.Codex == nil && a.Claude == nil
}

type PaneRole string
type PaneRuntimeSpec struct {
	Kind RuntimeKind `json:"kind,omitempty"`
}

func (r PaneRuntimeSpec) EffectiveKind() RuntimeKind {
	if r.Kind == "" {
		return RuntimeTmux
	}
	return r.Kind
}

type PaneSpec struct {
	Runtime PaneRuntimeSpec `json:"runtime,omitzero"`
	Role    PaneRole        `json:"role"`
	CWD     string          `json:"cwd,omitempty"`
	Command string          `json:"command,omitempty"`
}
type PaneStatus struct {
	ProcessSession *ProcessSessionRecord `json:"processSession,omitempty"`
	Conditions     []Condition           `json:"conditions,omitempty"`

	Activation PaneActivation `json:"activation,omitzero"`

	LastTermination *TerminationEvidence `json:"lastTermination,omitempty"`

	Teardown *PaneTeardownEvidence `json:"teardown,omitempty"`
}
type PaneTeardownEvidence struct {
	SocketIdentity   string                    `json:"socketIdentity"`
	RuntimeSessionID string                    `json:"runtimeSessionID"`
	RuntimePaneID    string                    `json:"runtimePaneID"`
	RuntimeWindowID  string                    `json:"runtimeWindowID"`
	WindowUID        string                    `json:"windowUID"`
	RootKind         Kind                      `json:"rootKind"`
	RootUID          string                    `json:"rootUID"`
	Generation       string                    `json:"generation"`
	Classification   TerminationClassification `json:"classification"`
	ObservedAt       time.Time                 `json:"observedAt"`
}
type ProcessActivation struct {
	Binding     ProcessBinding  `json:"binding"`
	HostProcess ProcessIdentity `json:"hostProcess"`
	Child       ProcessIdentity `json:"child"`
}
type ProcessBinding struct {
	HostInstanceID string `json:"hostInstanceID"`
	ProjectUID     string `json:"projectUID"`
	WindowUID      string `json:"windowUID"`
	AgentUID       string `json:"agentUID"`
	PaneUID        string `json:"paneUID"`
	Generation     string `json:"generation"`
	OperationID    string `json:"operationID"`
}
type ProcessIdentity struct {
	PID      int    `json:"pid"`
	OwnerUID uint32 `json:"ownerUID"`
	Start    string `json:"start"`
}
type ProcessRecordedControl struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	ConnectionID string `json:"connectionID"`
	SessionID    string `json:"sessionID"`
	TurnID       string `json:"turnID"`
}
type ProcessResumeHistory struct {
	Binding           ProcessBinding           `json:"binding"`
	SessionID         string                   `json:"sessionID"`
	InterruptedTurnID string                   `json:"interruptedTurnID,omitempty"`
	Expired           []ProcessRecordedControl `json:"expired,omitempty"`
}
type ProcessResumeState string
type ProcessSessionRecord struct {
	Provider     string             `json:"provider"`
	Binding      ProcessBinding     `json:"binding"`
	SessionID    string             `json:"sessionID,omitempty"`
	ThreadID     string             `json:"threadID,omitempty"`
	ConnectionID string             `json:"connectionID,omitempty"`
	TurnID       string             `json:"turnID,omitempty"`
	ResumeState  ProcessResumeState `json:"resumeState"`
	// StopRequested observes an explicit stop; it grants no ownership or resume authority.
	StopRequested bool                     `json:"stopRequested,omitempty"`
	Pending       []ProcessRecordedControl `json:"pending,omitempty"`
	History       *ProcessResumeHistory    `json:"history,omitempty"`
}
type Project struct {
	APIVersion string        `json:"apiVersion"`
	Kind       Kind          `json:"kind"`
	Metadata   ObjectMeta    `json:"metadata"`
	Spec       ProjectSpec   `json:"spec"`
	Status     ProjectStatus `json:"status"`
}
type ProjectSpec struct {
	Root             string `json:"root"`
	PrimaryWindowRef string `json:"primaryWindowRef"`
}
type ProjectStatus struct {
	Session    *SessionProjection `json:"session,omitempty"`
	Conditions []Condition        `json:"conditions,omitempty"`
}
type RuntimeKind string
type SessionProjection struct {
	Name string `json:"name"`
	Live bool   `json:"live"`

	SocketPath string `json:"socketPath,omitempty"`
}
type TerminationClassification string
type TerminationEvidence struct {
	Source         TerminationSource         `json:"source"`
	Classification TerminationClassification `json:"classification"`
	ObservedAt     time.Time                 `json:"observedAt,omitzero"`
	PaneUID        string                    `json:"paneUID,omitempty"`
	AgentUID       string                    `json:"agentUID,omitempty"`
	Generation     string                    `json:"generation,omitempty"`

	ExitCode    *int   `json:"exitCode,omitempty"`
	Signal      string `json:"signal,omitempty"`
	OperationID string `json:"operationID,omitempty"`
}

func (t TerminationEvidence) IsZero() bool {
	return t.Source == "" && t.Classification == "" && t.ObservedAt.IsZero() &&
		t.PaneUID == "" && t.AgentUID == "" && t.Generation == "" &&
		t.ExitCode == nil && t.Signal == "" && t.OperationID == ""
}

type TerminationSource string
type Window struct {
	APIVersion string       `json:"apiVersion"`
	Kind       Kind         `json:"kind"`
	Metadata   ObjectMeta   `json:"metadata"`
	Spec       WindowSpec   `json:"spec"`
	Status     WindowStatus `json:"status,omitzero"`
}
type WindowSpec struct {
	AnchorPaneRef       string `json:"anchorPaneRef"`
	DefaultShellPaneRef string `json:"defaultShellPaneRef,omitempty"`
}
type WindowStatus struct {
	Conditions       []Condition `json:"conditions,omitempty"`
	RuntimeSessionID string      `json:"runtimeSessionID,omitempty"`
	RuntimeID        string      `json:"runtimeID,omitempty"`
}

const (
	CodexGenerationPreparing       CodexGenerationState = "preparing"
	CodexGenerationCurrent         CodexGenerationState = "current"
	CodexGenerationDraining        CodexGenerationState = "draining"
	CodexGenerationHandoverPending CodexGenerationState = "handover-pending"
	CodexGenerationRetired         CodexGenerationState = "retired"
	CodexGenerationRecovering      CodexGenerationState = "recovering"
	CodexGenerationBlocked         CodexGenerationState = "blocked"
)
const (
	KindProject Kind = "Project"
	KindWindow  Kind = "Window"
	KindPane    Kind = "Pane"
	KindAgent   Kind = "Agent"

	KindControlSession Kind = "ControlSession"
)
const (
	PaneRoleShell PaneRole = "shell"
	PaneRoleAgent PaneRole = "agent"
)
const (
	PhasePending AgentPhase = "Pending"

	PhaseRunning AgentPhase = "Running"

	PhaseOffline AgentPhase = "Offline"

	PhaseFailed AgentPhase = "Failed"
)
const (
	InteractionUnknown          AgentInteractionKind = "unknown"
	InteractionIdle             AgentInteractionKind = "idle"
	InteractionInProgress       AgentInteractionKind = "in_progress"
	InteractionApprovalRequired AgentInteractionKind = "approval_required"
	InteractionInputRequired    AgentInteractionKind = "input_required"
	InteractionResponseComplete AgentInteractionKind = "response_complete"
)
const (
	ProgressPlanning   AgentProgressActivity = "planning"
	ProgressCommand    AgentProgressActivity = "command"
	ProgressFileChange AgentProgressActivity = "file-change"
	ProgressTool       AgentProgressActivity = "tool"
	ProgressWebSearch  AgentProgressActivity = "web-search"
	ProgressDelegation AgentProgressActivity = "delegation"
	ProgressImage      AgentProgressActivity = "image"
	ProgressReview     AgentProgressActivity = "review"
	ProgressCompaction AgentProgressActivity = "compaction"
	ProgressOther      AgentProgressActivity = "other"
)
const (
	ActivationNotRequested   AgentActivationState = "not_requested"
	ActivationPending        AgentActivationState = "pending"
	ActivationAcknowledged   AgentActivationState = "acknowledged"
	ActivationUnconfirmed    AgentActivationState = "unconfirmed"
	ActivationReasonTimedOut                      = "provider activation acknowledgement timed out"
	ActivationReasonFailed                        = "provider activation acknowledgement failed"
)
const (
	RuntimeTmux    RuntimeKind = "tmux"
	RuntimeProcess RuntimeKind = "process"
)
const (
	ProcessResumeUnknown ProcessResumeState = "unknown"
	ProcessResumable     ProcessResumeState = "resumable"
)
const (
	TerminationSourceSupervisor TerminationSource = "supervisor"

	TerminationSourceControlAction TerminationSource = "control-action"

	TerminationSourceReconcile TerminationSource = "reconcile"
)
const (
	TerminationIntentional TerminationClassification = "intentional"

	TerminationInterrupted TerminationClassification = "interrupted"

	TerminationNormal TerminationClassification = "normal"

	TerminationKilled TerminationClassification = "killed"

	TerminationAbnormal TerminationClassification = "abnormal"

	TerminationUnknown TerminationClassification = "unknown"
)
