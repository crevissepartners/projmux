package app

import (
	"sync"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Only shape/effect assertions cross the existing coordination UDS. No public
// stdout body, thinking, tool command, credential or model input is forwarded.
type claudeDialogueObservation struct {
	Kind            string                       `json:"kind"`
	ToolActions     []claudeDialogueObservedTool `json:"toolActions,omitempty"`
	SessionID       string                       `json:"sessionId"`
	ProviderVersion string                       `json:"providerVersion,omitempty"`
	Tools           []string                     `json:"tools"`
	MCPServers      []string                     `json:"mcpServers"`
	Plugins         []string                     `json:"plugins"`
}

type claudeDialogueObservedState struct {
	mu          sync.Mutex
	peer        coremetadata.ProcessIdentity
	evidence    claudeQualificationEvidence
	initialized bool
	ready       bool
	invalid     bool
	tools       map[string]claudeDialogueObservedTool
}

func (s *claudeCoordinationServer) recordDialogueObservation(peer coremetadata.ProcessIdentity, parent int, observation *claudeDialogueObservation) bool {
	if s.profile == nil || s.tool == nil || !s.tool.ready() || !s.dialogueProfileRouteMatches() || observation == nil {
		return false
	}
	authority, ok := s.route.Authority().(coremetadata.ClaudeAuthorityRef)
	if !ok || parent != authority.Process.PID || observation.SessionID != authority.SessionID || !s.tool.currentExecutable(peer) {
		return false
	}
	state := s.profile
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.peer != peer {
		return false
	}
	if state.invalid {
		state.invalid = true
		return false
	}
	if observation.Kind == "invalid" {
		state.invalid = true
		return true
	}
	if observation.Kind == "init" {
		evidence := claudeQualificationEvidence{Version: claudeQualificationEvidenceVersion, ClaudeCodeVersion: observation.ProviderVersion,
			SessionID: authority.SessionID, AgentUID: s.route.AgentUID, PaneUID: s.route.PaneUID, ActivationGeneration: s.route.Generation,
			RouteIncarnation: s.route.Incarnation(), ProviderProcess: authority.Process, RegistrationGeneration: authority.RegistrationGeneration,
			HelperProcess: authority.LeaseProcess, ReplyExecutionGate: true, Tools: observation.Tools, MCPServers: observation.MCPServers,
			Plugins: observation.Plugins, InboundPolicy: "accept", PublicInitObserved: true, StreamFrozen: true, ObservedAt: time.Now()}
		if !evidence.validExplicit(time.Now(), s.route) {
			state.invalid = true
			return false
		}
		if !state.initialized {
			state.evidence = evidence
			state.peer = peer
			state.initialized = true
		}
		return true
	}
	if !state.initialized || state.peer != peer {
		state.invalid = true
		return false
	}
	switch observation.Kind {
	case "ready":
		if state.ready {
			return true
		}
		state.ready = true
		return true
	case "tool", "tool-result":
		s.hub.mu.Lock()
		admitted := s.hub.qualifiedVersion == claudeFrozenFrameProviderVersion || (s.hub.qualification != nil && s.hub.qualification.state == "pending" && s.hub.qualification.frameComplete)
		s.hub.mu.Unlock()
		if !admitted {
			state.invalid = true
			return false
		}
		if len(observation.ToolActions) == 0 || len(observation.ToolActions) > 32 {
			state.invalid = true
			return false
		}
		if state.tools == nil {
			state.tools = make(map[string]claudeDialogueObservedTool)
		}
		for _, action := range observation.ToolActions {
			if !action.valid() {
				state.invalid = true
				return false
			}
			previous, exists := state.tools[action.ToolUseID]
			if observation.Kind == "tool" {
				if exists || action.ResultObserved || action.ReplyRef != "" || len(state.tools) >= 32 {
					state.invalid = true
					return false
				}
			} else if !exists || previous.ResultObserved || !action.ResultObserved || previous.MessageRef != action.MessageRef || previous.TargetAgentUID != action.TargetAgentUID {
				state.invalid = true
				return false
			}
			state.tools[action.ToolUseID] = action
		}
		return true
	default:
		state.invalid = true
		return false
	}
}

func (s *claudeCoordinationServer) dialogueProfileEvidence() (claudeQualificationEvidence, bool) {
	if s.profile == nil {
		return claudeQualificationEvidence{}, false
	}
	state := s.profile
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.invalid || !state.ready || !state.initialized || !s.dialogueProfileRouteMatches() {
		return claudeQualificationEvidence{}, false
	}
	current, _, err := localipc.Process(state.peer.PID)
	if err != nil || current != state.peer || s.tool == nil || !s.tool.currentExecutable(state.peer) {
		state.invalid = true
		return claudeQualificationEvidence{}, false
	}
	return state.evidence, true
}

func (s *claudeCoordinationServer) dialogueProfileCurrent() bool {
	if s.profile == nil {
		return true
	}
	_, ok := s.dialogueProfileEvidence()
	return ok
}

func (s *claudeCoordinationServer) dialogueProfileRouteMatches() bool {
	if s.tool == nil || s.tool.profile == nil {
		return false
	}
	p := s.tool.profile
	return p.AgentUID == s.route.AgentUID && p.PaneUID == s.route.PaneUID && p.Generation == s.route.Generation
}
