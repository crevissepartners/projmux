package metadata

import (
	"slices"
	"strings"
)

// RuntimeKind is the closed Pane host vocabulary. An omitted kind means tmux.
type RuntimeKind string

const (
	RuntimeTmux    RuntimeKind = "tmux"
	RuntimeProcess RuntimeKind = "process"
)

type PaneRuntimeSpec struct {
	Kind RuntimeKind `json:"kind,omitempty"`
}

// EffectiveKind preserves the meaning of recipes written before schema v5.
func (r PaneRuntimeSpec) EffectiveKind() RuntimeKind {
	if r.Kind == "" {
		return RuntimeTmux
	}
	return r.Kind
}

// ProcessBinding names one host instance and immutable ownership generation.
// It carries no endpoint address, credential, or provider payload.
type ProcessBinding struct {
	HostInstanceID string `json:"hostInstanceID"`
	ProjectUID     string `json:"projectUID"`
	WindowUID      string `json:"windowUID"`
	AgentUID       string `json:"agentUID"`
	PaneUID        string `json:"paneUID"`
	Generation     string `json:"generation"`
	OperationID    string `json:"operationID"`
}

// ProcessActivation is the process member of the tagged Pane activation.
// Child and host identities are evidence, never tmux runtime handles.
type ProcessActivation struct {
	Binding     ProcessBinding  `json:"binding"`
	HostProcess ProcessIdentity `json:"hostProcess"`
	Child       ProcessIdentity `json:"child"`
}

type ProcessResumeState string

const (
	ProcessResumeUnknown ProcessResumeState = "unknown"
	ProcessResumable     ProcessResumeState = "resumable"
)

// ProcessRecordedControl preserves identities only. Text and decisions belong
// in provider stores, and cannot become authority in a replacement generation.
type ProcessRecordedControl struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	ConnectionID string `json:"connectionID"`
	SessionID    string `json:"sessionID"`
	TurnID       string `json:"turnID"`
}

// ProcessResumeHistory records interrupted work from the retired generation.
type ProcessResumeHistory struct {
	Binding           ProcessBinding           `json:"binding"`
	SessionID         string                   `json:"sessionID"`
	InterruptedTurnID string                   `json:"interruptedTurnID,omitempty"`
	Expired           []ProcessRecordedControl `json:"expired,omitempty"`
}

// ProcessSessionRecord is durable resume evidence, independent of host liveness.
// Resumable means a recorded candidate, not proof the provider accepts resume.
// Claude uses sessionID; Codex uses threadID, and the other member is absent.
type ProcessSessionRecord struct {
	Provider     string                   `json:"provider"`
	Binding      ProcessBinding           `json:"binding"`
	SessionID    string                   `json:"sessionID,omitempty"`
	ThreadID     string                   `json:"threadID,omitempty"`
	ConnectionID string                   `json:"connectionID,omitempty"`
	TurnID       string                   `json:"turnID,omitempty"`
	ResumeState  ProcessResumeState       `json:"resumeState"`
	Pending      []ProcessRecordedControl `json:"pending,omitempty"`
	History      *ProcessResumeHistory    `json:"history,omitempty"`
}

func (r *ProcessSessionRecord) Clone() *ProcessSessionRecord {
	if r == nil {
		return nil
	}
	out := *r
	out.Pending = slices.Clone(r.Pending)
	if r.History != nil {
		history := *r.History
		history.Expired = slices.Clone(history.Expired)
		out.History = &history
	}
	return &out
}

func processIdentityToken(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n\t")
}

func (r Registry) validProcessBinding(pane Pane, b ProcessBinding) bool {
	for _, value := range []string{b.HostInstanceID, b.ProjectUID, b.WindowUID, b.AgentUID, b.PaneUID, b.Generation, b.OperationID} {
		if !processIdentityToken(value) {
			return false
		}
	}
	agent, ok := r.Agent(b.AgentUID)
	if !ok || pane.Spec.Role != PaneRoleAgent || pane.Metadata.UID != b.PaneUID || pane.Metadata.OwnerUID() != b.AgentUID || agent.Metadata.OwnerUID() != b.WindowUID {
		return false
	}
	window, ok := r.Window(b.WindowUID)
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != KindProject || window.Metadata.OwnerUID() != b.ProjectUID {
		return false
	}
	_, ok = r.Project(b.ProjectUID)
	return ok
}

func validProcessControls(controls []ProcessRecordedControl, connection, session, turn string) bool {
	if len(controls) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, q := range controls {
		if !processIdentityToken(q.ID) || seen[q.ID] || (q.Kind != "question" && q.Kind != "permission") || !processIdentityToken(q.ConnectionID) || !processIdentityToken(q.SessionID) || !processIdentityToken(q.TurnID) || q.ConnectionID != connection || q.SessionID != session || q.TurnID != turn {
			return false
		}
		seen[q.ID] = true
	}
	return true
}

func (r Registry) validatePaneRuntime(pane Pane) error {
	const op = "validate registry"
	kind := pane.Spec.Runtime.EffectiveKind()
	if kind != RuntimeTmux && kind != RuntimeProcess {
		return stateErr(op, ErrInvalidRegistry, "runtime-kind-unsupported: pane %q runtime kind %q", pane.Metadata.Name, kind)
	}
	a := pane.Status.Activation
	if kind == RuntimeTmux {
		if a.Process != nil || (a.Kind != "" && a.Kind != RuntimeTmux) || pane.Status.ProcessSession != nil {
			return stateErr(op, ErrInvalidRegistry, "runtime-binding-invalid: tmux Pane contains process evidence")
		}
		return nil
	}
	if a.RuntimeID != "" || a.Codex != nil || a.Claude != nil || pane.Status.Teardown != nil {
		return stateErr(op, ErrInvalidRegistry, "runtime-binding-invalid: process Pane contains tmux evidence")
	}
	if !a.IsZero() {
		p := a.Process
		if a.Kind != RuntimeProcess || p == nil || !r.validProcessBinding(pane, p.Binding) || p.Binding.Generation != a.Generation || p.Binding.AgentUID != a.AgentUID || p.Binding.OperationID != a.OperationID || !p.HostProcess.Valid() || !p.Child.Valid() {
			return stateErr(op, ErrInvalidRegistry, "runtime-binding-invalid: process activation has incomplete or foreign ownership evidence")
		}
	}
	if s := pane.Status.ProcessSession; s != nil {
		agent, ok := r.Agent(pane.Metadata.OwnerUID())
		session := s.SessionID
		if s.Provider == "codex" {
			session = s.ThreadID
		}
		if !ok || (s.Provider != "claude" && s.Provider != "codex") || agent.Spec.Provider != s.Provider || !r.validProcessBinding(pane, s.Binding) || (s.Provider == "codex" && s.SessionID != "") || (s.Provider == "claude" && s.ThreadID != "") || (session != "" && !processIdentityToken(session)) || (s.ConnectionID != "" && !processIdentityToken(s.ConnectionID)) || (s.TurnID != "" && !processIdentityToken(s.TurnID)) || (s.ResumeState != ProcessResumeUnknown && s.ResumeState != ProcessResumable) || (s.ResumeState == ProcessResumable && (session == "" || s.ConnectionID == "")) || !validProcessControls(s.Pending, s.ConnectionID, session, s.TurnID) {
			return stateErr(op, ErrInvalidRegistry, "process-session-invalid: incomplete or foreign durable session evidence")
		}
		if h := s.History; h != nil {
			if !r.validProcessBinding(pane, h.Binding) || h.Binding.Generation == s.Binding.Generation || !processIdentityToken(h.SessionID) || h.SessionID != session || (h.InterruptedTurnID != "" && !processIdentityToken(h.InterruptedTurnID)) {
				return stateErr(op, ErrInvalidRegistry, "process-session-invalid: resume history is not a retired generation")
			}
			connection := ""
			if len(h.Expired) != 0 {
				connection = h.Expired[0].ConnectionID
			}
			if !validProcessControls(h.Expired, connection, h.SessionID, h.InterruptedTurnID) {
				return stateErr(op, ErrInvalidRegistry, "process-session-invalid: inconsistent expired control identities")
			}
		}
	}
	return nil
}
