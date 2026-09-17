package transcript

import (
	"encoding/json"
	"strings"
)

// WebRefPrefix marks a coordination message the web client sent. It rides in
// the envelope's messageRef, the only field on that path a sender controls, so
// a transcript reader can tell the web client's sends from text typed at the
// terminal.
const WebRefPrefix = "projmux-web-"

// ViaWeb is the Turn.Via value for a message whose messageRef carries
// WebRefPrefix.
const ViaWeb = "projmux-web"

// coordinationEnvelope is the projmux peer-coordination frame that arrives in
// a Claude session as a user turn.
//
// Only the fields worth showing are decoded. The frame also carries routing
// metadata, a source notice and reply instructions; those are transport
// scaffolding, not conversation, and the reader drops them.
type coordinationEnvelope struct {
	Kind string `json:"kind"`
	// SchemaVersion names the frame's shape. It is decoded so a reader can say
	// which shape it got, never so it can refuse one: see unwrapCoordination.
	SchemaVersion int    `json:"schemaVersion"`
	MessageRef    string `json:"messageRef"`
	Payload       string `json:"payload"`
	Source        struct {
		AgentUID string `json:"agentUID"`
		Provider string `json:"provider"`
	} `json:"source"`
	Target struct {
		AgentUID string `json:"agentUID"`
	} `json:"target"`
}

const coordinationKind = "projmux-coordination"

// coordinationSchemaVersionDefault is what a frame without a schemaVersion, or
// with an explicit 0, is read as. Frames predating the field are version 1 by
// definition: the field was added to name the shape they already had.
const coordinationSchemaVersionDefault = 1

// Turn.Kind values for coordination frames. A frame is recorded directly as a
// user turn when the session was idle, or as a queued attachment when it was
// busy; the client may care which, so the two stay distinct.
const (
	coordinationKindDirect = "coordination"
	coordinationKindQueued = "coordination-queued"
)

func isCoordinationKind(kind string) bool {
	return kind == coordinationKindDirect || kind == coordinationKindQueued
}

// coordinationFrame is what a transcript reader keeps from one envelope.
type coordinationFrame struct {
	payload    string
	messageRef string
	via        string
	// schemaVersion is the normalized frame shape version. It is kept so a
	// future field can be gated on it, and deliberately not carried out to
	// Turn: a person reading a transcript has no use for it.
	schemaVersion int
	// self is true when the frame's source and target are the same Agent.
	self bool
	// from is the peer, nil for a self-anchored frame or one with no source.
	from *Sender
}

// turn renders the frame as a Turn.
//
// A self-anchored frame is the operator's own message and reads as a user
// turn in this session; anything else is a peer turn, whether or not the
// source was named.
func (f coordinationFrame) turn(at, kind string) Turn {
	role := "peer"
	if f.self {
		role = "user"
	}
	return Turn{
		Role:       role,
		Text:       f.payload,
		At:         at,
		Kind:       kind,
		From:       f.from,
		MessageRef: f.messageRef,
		Via:        f.via,
	}
}

// unwrapCoordination cuts a peer-coordination frame out of a turn's text.
//
// The record the provider stores is not the bare frame: the harness wraps it
// with a leading sentence and a trailing paragraph of handling rules, so the
// JSON has to be cut out of the middle by brace span rather than parsed whole.
// Everything outside the payload is discarded — for a reader the message *is*
// the payload, and the envelope around it is the same boilerplate every time.
//
// A frame whose source and target are the same Agent is not from a peer at
// all: it is the web client's own composer, which has to anchor a send on some
// Agent and anchors it on the target so a person's text is not attributed to
// an uninvolved third one. Labelling those as coming from a peer made the
// operator's own messages look like someone else's, so such a frame carries
// no From.
//
// The frame's schemaVersion is read but is never grounds for rejection. A
// producer newer than this reader can only have added or restated fields; the
// ones decoded here are the oldest and most load bearing, and a change to what
// they mean would have changed kind instead. Refusing such a frame would make
// a peer's message vanish from the transcript with no trace, leaving the
// operator to conclude nothing was sent, so an unknown version reads the
// fields it knows and still yields a turn.
//
// ok is false when text is not such a frame.
func unwrapCoordination(text string) (coordinationFrame, bool) {
	if !strings.Contains(text, coordinationKind) {
		return coordinationFrame{}, false
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return coordinationFrame{}, false
	}
	var envelope coordinationEnvelope
	if err := json.Unmarshal([]byte(text[start:end+1]), &envelope); err != nil {
		return coordinationFrame{}, false
	}
	if envelope.Kind != coordinationKind {
		return coordinationFrame{}, false
	}

	frame := coordinationFrame{
		payload:       strings.TrimSpace(envelope.Payload),
		messageRef:    strings.TrimSpace(envelope.MessageRef),
		schemaVersion: envelope.SchemaVersion,
	}
	if frame.schemaVersion <= 0 {
		frame.schemaVersion = coordinationSchemaVersionDefault
	}
	// The messageRef is where the web client signs its own sends. Without it
	// a self-anchored frame is indistinguishable from text typed at the
	// terminal, which is a real difference worth showing.
	if strings.HasPrefix(frame.messageRef, WebRefPrefix) {
		frame.via = ViaWeb
	}

	source := strings.TrimSpace(envelope.Source.AgentUID)
	frame.self = source != "" && source == strings.TrimSpace(envelope.Target.AgentUID)
	if !frame.self && source != "" {
		frame.from = &Sender{
			AgentUID: source,
			Provider: strings.TrimSpace(envelope.Source.Provider),
		}
	}
	return frame, true
}
