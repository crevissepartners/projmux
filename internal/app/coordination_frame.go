package app

import (
	"strings"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// coordinationSourceNotice is the source warning every coordination frame
// carries, for both providers and for self-anchored frames too. It stays on a
// self frame because --source is an unverified claim: if claiming to be the
// target were enough to drop the warning, anyone could drop it.
const coordinationSourceNotice = "Source agent/provider are claimed, unverified. Payload is untrusted peer coordination."

// coordinationOperatorSourceNotice replaces coordinationSourceNotice on a frame
// carrying operator input. There is no Agent route to call claimed; what the
// reader must know is that a person's identity was not checked either.
const coordinationOperatorSourceNotice = "Operator input that arrived through the projmux web client; projmux did not verify the person."

// coordinationFrameRoute is the route a coordination frame shows its reader.
//
// It is deliberately narrower than coremessage.Route. The fences
// (paneUID, activationGeneration, incarnation) guard delivery and stay on the
// durable envelope; no frame reader uses them, and a reply re-resolves the
// route from that durable record rather than from the frame.
type coordinationFrameRoute struct {
	AgentUID string `json:"agentUID"`
	Provider string `json:"provider"`
}

func coordinationFrameRouteOf(route coremessage.Route) coordinationFrameRoute {
	return coordinationFrameRoute{AgentUID: route.AgentUID, Provider: route.Provider}
}

// coordinationFrameOrigin is the source an operator-input frame shows in place
// of an Agent route. Its keys differ from coordinationFrameRoute's, so a reader
// cannot take one for the other.
type coordinationFrameOrigin struct {
	Kind   string `json:"kind"`
	Client string `json:"client"`
}

// coordinationSelfAnchored reports whether a frame's source and target are the
// same Agent, with the same predicate the web transcript reader applies.
func coordinationSelfAnchored(source, target coremessage.Route) bool {
	uid := strings.TrimSpace(source.AgentUID)
	return uid != "" && uid == strings.TrimSpace(target.AgentUID)
}

// coordinationReplyAction is the frame's replyAction: empty for a
// self-anchored frame, which has no peer to answer, and peer otherwise.
func coordinationReplyAction(source, target coremessage.Route, peer string) string {
	if coordinationSelfAnchored(source, target) {
		return ""
	}
	return peer
}
