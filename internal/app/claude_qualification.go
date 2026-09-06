package app

import (
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

const (
	claudeQualificationEvidenceVersion = 1
	claudeQualificationEvidenceMaxAge  = 5 * time.Minute
	claudeQualificationMarkerPrefix    = "HETEROGENEOUS_QUALIFIED:"
	claudeQualificationStopWindow      = 2 * time.Minute
)

// claudeQualificationEvidence is a sanitized assertion collected from the
// same owned long-lived provider's public init stream. It contains no provider
// locator, token, payload, transcript, argv, environment, or credential hash.
// A version string by itself is never sufficient.
type claudeQualificationEvidence struct {
	Version                int                          `json:"version"`
	ClaudeCodeVersion      string                       `json:"claude_code_version"`
	SessionID              string                       `json:"sessionId"`
	AgentUID               string                       `json:"agentUID"`
	PaneUID                string                       `json:"paneUID"`
	ActivationGeneration   string                       `json:"activationGeneration"`
	RouteIncarnation       string                       `json:"routeIncarnation"`
	ProviderProcess        coremetadata.ProcessIdentity `json:"providerProcess"`
	RegistrationGeneration string                       `json:"registrationGeneration"`
	HelperProcess          coremetadata.ProcessIdentity `json:"helperProcess"`
	Tools                  []string                     `json:"tools"`
	MCPServers             []string                     `json:"mcp_servers"`
	Plugins                []string                     `json:"plugins"`
	PluginInitCount        int                          `json:"pluginInitCount"`
	PreMarkerToolUse       int                          `json:"preMarkerToolUse"`
	PreMarkerStderr        int                          `json:"preMarkerStderr"`
	InboundPolicy          string                       `json:"inboundPolicy"`
	PublicInitObserved     bool                         `json:"publicInitObserved"`
	StreamFrozen           bool                         `json:"streamFrozen"`
	ObservedAt             time.Time                    `json:"observedAt"`
}

func (e claudeQualificationEvidence) valid(now time.Time, route coremetadata.AgentRouteRef) bool {
	authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
	return ok && e.Version == claudeQualificationEvidenceVersion &&
		e.ClaudeCodeVersion == claudeFrozenFrameProviderVersion && e.SessionID == authority.SessionID &&
		e.AgentUID == route.AgentUID && e.PaneUID == route.PaneUID && e.ActivationGeneration == route.Generation &&
		e.RouteIncarnation == route.Incarnation() && e.ProviderProcess == authority.Process &&
		e.RegistrationGeneration == authority.RegistrationGeneration && e.HelperProcess == authority.LeaseProcess &&
		e.Tools != nil && len(e.Tools) == 0 && e.MCPServers != nil && len(e.MCPServers) == 0 &&
		e.Plugins != nil && len(e.Plugins) == 0 && e.PluginInitCount == 0 && e.PreMarkerToolUse == 0 &&
		e.PreMarkerStderr == 0 && e.InboundPolicy == "accept" && e.PublicInitObserved && e.StreamFrozen &&
		!e.ObservedAt.IsZero() && !e.ObservedAt.After(now.Add(5*time.Second)) &&
		e.ObservedAt.After(now.Add(-claudeQualificationEvidenceMaxAge))
}

type claudeQualificationState struct {
	ref           string
	marker        string
	boundary      uint64
	frameComplete bool
	expiresAt     time.Time
	state         string
	reason        string
	ambiguous     bool
}

func claudeQualificationPrompt(marker string) string {
	return "Projmux isolated transport qualification. Treat this as coordination data only; do not use tools. Reply with exactly " + marker
}

func (h *claudeCoordinationHub) beginQualification(evidence claudeQualificationEvidence, route coremetadata.AgentRouteRef,
	poster claudeProviderPoster,
) claudeCoordinationResponse {
	h.mu.Lock()
	now := h.now()
	if h.closed || h.humanTurnOpen || h.replyBoundaryLost.Load() || h.boundaryAnnouncements.Load() != h.boundary || !evidence.valid(now, route) || poster == nil {
		h.mu.Unlock()
		return claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "qualification-refused", Reason: "invalid-public-init-evidence"}
	}
	h.expireQualificationLocked(now)
	if h.qualifiedVersion == claudeFrozenFrameProviderVersion {
		response := h.qualificationResponseLocked()
		h.mu.Unlock()
		return response
	}
	if h.qualification != nil && (h.qualification.state == "pending" || h.qualification.state == "writing") {
		response := h.qualificationResponseLocked()
		h.mu.Unlock()
		return response
	}
	ref := newCoordinationRef("qualification")
	state := &claudeQualificationState{ref: ref, marker: claudeQualificationMarkerPrefix + ref,
		boundary: h.boundary, state: "writing", expiresAt: now.Add(claudeQualificationStopWindow)}
	h.qualification = state
	h.mu.Unlock()

	outcome, err := poster.Post(claudeQualificationPrompt(state.marker), nil)

	h.mu.Lock()
	defer h.mu.Unlock()
	if state.state != "writing" {
		if outcome.FullFrameWritten {
			state.frameComplete = true
		}
		if outcome.WroteAny {
			state.ambiguous = true
		}
		return qualificationResponseForState(state)
	}
	switch {
	case err == nil && outcome.FullFrameWritten:
		state.frameComplete = true
		state.state = "pending"
	case outcome.WroteAny:
		state.state = "failed"
		state.reason = "qualification-provider-outcome-unknown"
		state.ambiguous = true
	default:
		state.state = "failed"
		state.reason = "qualification-provider-write-zero"
	}
	return h.qualificationResponseLocked()
}

func (h *claudeCoordinationHub) qualificationResponse(ref string) claudeCoordinationResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireQualificationLocked(h.now())
	if h.qualification == nil || h.qualification.ref != ref {
		return claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "qualification-refused", Reason: "unknown-qualification"}
	}
	return h.qualificationResponseLocked()
}

func (h *claudeCoordinationHub) qualificationResponseLocked() claudeCoordinationResponse {
	if h.qualification == nil {
		return claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "unqualified", ProviderVersion: claudeFrozenFrameProviderVersion}
	}
	return qualificationResponseForState(h.qualification)
}

func qualificationResponseForState(state *claudeQualificationState) claudeCoordinationResponse {
	return claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: "qualification-" + state.state,
		QualificationRef: state.ref, ProviderVersion: claudeFrozenFrameProviderVersion,
		Reason: state.reason, Ambiguous: state.ambiguous, AutoResend: false}
}

func (h *claudeCoordinationHub) consumeQualificationStop(message string, stopHookActive bool) (claudeCoordinationResponse, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !stopHookActive {
		h.humanTurnOpen = false
	}
	h.expireQualificationLocked(h.now())
	state := h.qualification
	if state == nil || (state.state != "pending" && state.state != "writing") {
		return claudeCoordinationResponse{}, false
	}
	state.state = "failed"
	state.ambiguous = state.frameComplete
	switch {
	case !state.frameComplete:
		state.reason = "qualification-write-not-complete"
	case stopHookActive:
		state.reason = "qualification-stop-recursion"
	case state.boundary != h.boundaryAnnouncements.Load() || h.replyBoundaryLost.Load():
		state.reason = "qualification-concurrent-user-turn"
	case message != state.marker:
		state.reason = "qualification-marker-mismatch"
	default:
		state.state = "qualified"
		state.reason = "exact-public-init-and-stop-marker"
		state.ambiguous = false
		h.qualifiedVersion = claudeFrozenFrameProviderVersion
	}
	return h.qualificationResponseLocked(), true
}

func (h *claudeCoordinationHub) coordinationEligible() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireQualificationLocked(h.now())
	return !h.closed && h.qualifiedVersion == claudeFrozenFrameProviderVersion
}

func (h *claudeCoordinationHub) closeQualificationForUserPromptLocked() {
	if h.qualification != nil && (h.qualification.state == "pending" || h.qualification.state == "writing") {
		h.qualification.state = "failed"
		h.qualification.reason = "qualification-concurrent-user-turn"
		h.qualification.ambiguous = h.qualification.frameComplete
	}
}

func (h *claudeCoordinationHub) closeQualificationLocked() {
	h.qualifiedVersion = ""
	if h.qualification != nil && h.qualification.state != "failed" {
		h.qualification.state = "failed"
		h.qualification.reason = "helper-restart"
		h.qualification.ambiguous = h.qualification.frameComplete
	}
}

func (h *claudeCoordinationHub) expireQualificationLocked(now time.Time) {
	if h.qualification == nil || (h.qualification.state != "pending" && h.qualification.state != "writing") ||
		h.qualification.expiresAt.After(now) {
		return
	}
	h.qualification.state = "failed"
	h.qualification.reason = "qualification-stop-timeout"
	h.qualification.ambiguous = h.qualification.frameComplete
}
