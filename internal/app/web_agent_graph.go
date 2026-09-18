package app

import (
	"cmp"
	"context"
	"slices"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/web"
)

// The agent graph reads: which Agents exchanged peer messages, from the live
// message store and the two retained reclaim-log generations, and which Agent
// created which, from the Registry's creator annotations. Both message reads
// take no lock and create no file; the store and the log are only opened for
// reading. docs/web-api.md ("Agent graph") is the contract.

// peerMessage is one retained message between two different Agents, after the
// merge in readConversations.
type peerMessage struct {
	MessageRef      string
	ConversationRef string
	ReplyTo         string
	Source          string
	Target          string
	State           coremessage.State
	AcceptedAt      time.Time
	PayloadBytes    int
	// BodyRetained is true only for a message still in the live store; the
	// reclaim log never keeps the payload.
	BodyRetained bool
	Payload      string
}

// conversations is everything both routes answer from.
type conversations struct {
	messages []peerMessage
	// since is the oldest acceptedAt among the messages kept; nil when none.
	since   *time.Time
	skipped int
}

// readConversations is the one place the merge rules live:
//   - store records come first, then log lines, and a messageRef is kept once,
//     so the store copy, which has the payload, wins over its log line;
//   - a self message (same Agent at both ends) or one with an empty Agent uid
//     at either end is dropped here and counted nowhere, not even in since.
//
// The store validates every Route, so an empty Agent uid can only come from a
// log line.
func (b *webBackend) readConversations() (conversations, error) {
	paths, err := b.statePaths()
	if err != nil {
		return conversations{}, err
	}
	read := b.readMessages
	if read == nil {
		read = messagestore.ReadArchive
	}
	archive, err := read(paths.StateDir)
	if err != nil {
		return conversations{}, err
	}
	out := conversations{skipped: archive.Skipped}
	seen := make(map[string]bool, len(archive.Records)+len(archive.History))
	keep := func(message peerMessage) {
		if seen[message.MessageRef] {
			return
		}
		seen[message.MessageRef] = true
		if message.Source == "" || message.Target == "" || message.Source == message.Target {
			return
		}
		if out.since == nil || message.AcceptedAt.Before(*out.since) {
			since := message.AcceptedAt
			out.since = &since
		}
		out.messages = append(out.messages, message)
	}
	for _, record := range archive.Records {
		envelope := record.Envelope
		keep(peerMessage{
			MessageRef: envelope.MessageRef, ConversationRef: envelope.ConversationRef, ReplyTo: envelope.ReplyTo,
			Source: envelope.Source.AgentUID, Target: envelope.Target.AgentUID, State: record.Delivery.State,
			AcceptedAt: envelope.AcceptedAt, PayloadBytes: len(envelope.Payload), BodyRetained: true, Payload: envelope.Payload,
		})
	}
	for _, entry := range archive.History {
		keep(peerMessage{
			MessageRef: entry.MessageRef, ConversationRef: entry.ConversationRef, ReplyTo: entry.ReplyTo,
			Source: entry.Source.AgentUID, Target: entry.Target.AgentUID, State: entry.State,
			AcceptedAt: entry.AcceptedAt, PayloadBytes: entry.PayloadBytes,
		})
	}
	return out, nil
}

// agentProject is the Project an Agent belongs to through its Window, as the
// resource graph derives it. ok is false for an Agent not in the Registry; an
// Agent whose Window is not Project-owned is in the Registry with no Project.
func agentProject(registry *coremetadata.Registry, uid string) (string, bool) {
	agent, ok := registry.Agent(uid)
	if !ok {
		return "", false
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject {
		return "", true
	}
	return window.Metadata.OwnerUID(), true
}

type agentGraph struct {
	Project string            `json:"project"`
	Agents  []agentGraphAgent `json:"agents"`
	Edges   []agentGraphEdge  `json:"edges"`
	Omitted omittedCount      `json:"omitted"`
	Since   *time.Time        `json:"since"`
	Skipped int               `json:"skipped"`
}

type agentGraphAgent struct {
	UID        string `json:"uid"`
	ProjectUID string `json:"projectUID"`
}

// agentGraphEdge is one edge of the graph. A conversation edge is one
// unordered pair, A sorting before B, and carries its counts; a created edge
// runs from the creator A to the Agent B it created and carries none.
type agentGraphEdge struct {
	Kind string `json:"kind"`
	A    string `json:"a"`
	B    string `json:"b"`
	*conversationCounts
}

type conversationCounts struct {
	AToB           int       `json:"aToB"`
	BToA           int       `json:"bToA"`
	LastAcceptedAt time.Time `json:"lastAcceptedAt"`
}

type omittedCount struct {
	Pairs    int `json:"pairs"`
	Messages int `json:"messages"`
	Created  int `json:"created"`
}

// AgentGraph answers which Agents of a Project talked, and with whom.
//
// An edge joins two Agents that are both in the Registry when at least one of
// them belongs to the Project. A pair with an Agent missing from the Registry
// is never an edge; it is counted in omitted when its other Agent belongs to
// the Project. A pair of two missing Agents cannot be placed in any Project
// and is counted nowhere.
//
// A created edge runs from the Agent named in another Agent's creator
// annotation to that Agent, under the same at-least-one-end rule. A creator
// missing from the Registry is no edge and is counted in omitted when the
// created Agent belongs to the Project; an annotation naming the Agent itself
// is no edge and is counted nowhere.
func (b *webBackend) AgentGraph(_ context.Context, project string) (any, error) {
	registry, err := b.loadRegistry()
	if err != nil {
		return nil, err
	}
	if _, ok := registry.Project(project); !ok {
		return nil, web.NotFound("no project " + project)
	}
	conv, err := b.readConversations()
	if err != nil {
		return nil, err
	}
	type endpoint struct {
		uid, project string
		known        bool
	}
	lookup := func(uid string) endpoint {
		projectUID, known := agentProject(&registry, uid)
		return endpoint{uid: uid, project: projectUID, known: known}
	}
	edges := make(map[[2]string]*agentGraphEdge)
	omittedPairs := make(map[[2]string]bool)
	graph := agentGraph{Project: project, Agents: []agentGraphAgent{}, Edges: []agentGraphEdge{}, Since: conv.since, Skipped: conv.skipped}
	for _, message := range conv.messages {
		first, second := lookup(message.Source), lookup(message.Target)
		if second.uid < first.uid {
			first, second = second, first
		}
		key := [2]string{first.uid, second.uid}
		inProject := (first.known && first.project == project) || (second.known && second.project == project)
		if !inProject {
			continue
		}
		if !first.known || !second.known {
			omittedPairs[key] = true
			graph.Omitted.Messages++
			continue
		}
		edge := edges[key]
		if edge == nil {
			edge = &agentGraphEdge{Kind: "conversation", A: first.uid, B: second.uid, conversationCounts: &conversationCounts{}}
			edges[key] = edge
		}
		if message.Source == first.uid {
			edge.AToB++
		} else {
			edge.BToA++
		}
		if message.AcceptedAt.After(edge.LastAcceptedAt) {
			edge.LastAcceptedAt = message.AcceptedAt
		}
	}
	graph.Omitted.Pairs = len(omittedPairs)
	for _, edge := range edges {
		graph.Edges = append(graph.Edges, *edge)
	}

	for _, child := range registry.Agents {
		creator := child.Metadata.Annotations[coremetadata.AnnotationCreatorAgent]
		if creator == "" || creator == child.Metadata.UID {
			continue
		}
		childProject, _ := agentProject(&registry, child.Metadata.UID)
		creatorProject, known := agentProject(&registry, creator)
		switch {
		case !known:
			if childProject == project {
				graph.Omitted.Created++
			}
		case childProject == project || creatorProject == project:
			graph.Edges = append(graph.Edges, agentGraphEdge{Kind: "created", A: creator, B: child.Metadata.UID})
		}
	}

	listed := make(map[string]bool)
	for _, agent := range registry.Agents {
		if projectUID, _ := agentProject(&registry, agent.Metadata.UID); projectUID == project {
			listed[agent.Metadata.UID] = true
			graph.Agents = append(graph.Agents, agentGraphAgent{UID: agent.Metadata.UID, ProjectUID: projectUID})
		}
	}
	for _, edge := range graph.Edges {
		for _, uid := range []string{edge.A, edge.B} {
			if !listed[uid] {
				listed[uid] = true
				projectUID, _ := agentProject(&registry, uid)
				graph.Agents = append(graph.Agents, agentGraphAgent{UID: uid, ProjectUID: projectUID})
			}
		}
	}
	slices.SortFunc(graph.Agents, func(x, y agentGraphAgent) int { return cmp.Compare(x.UID, y.UID) })
	slices.SortFunc(graph.Edges, func(x, y agentGraphEdge) int {
		return cmp.Or(cmp.Compare(x.A, y.A), cmp.Compare(x.B, y.B), cmp.Compare(x.Kind, y.Kind))
	})
	return graph, nil
}

type peerMessages struct {
	Agent    string            `json:"agent"`
	Peer     string            `json:"peer"`
	Messages []peerMessageItem `json:"messages"`
	Since    *time.Time        `json:"since"`
	Skipped  int               `json:"skipped"`
}

type peerMessageItem struct {
	MessageRef      string            `json:"messageRef"`
	ConversationRef string            `json:"conversationRef"`
	ReplyTo         string            `json:"replyTo,omitempty"`
	Direction       string            `json:"direction"`
	Source          string            `json:"source"`
	Target          string            `json:"target"`
	State           coremessage.State `json:"state"`
	AcceptedAt      time.Time         `json:"acceptedAt"`
	PayloadBytes    int               `json:"payloadBytes"`
	BodyRetained    bool              `json:"bodyRetained"`
	Payload         *string           `json:"payload,omitempty"`
}

// PeerMessages lists the retained messages between two Agents, both ways, in
// acceptance order. Direction is relative to agent.
func (b *webBackend) PeerMessages(_ context.Context, agent, peer string) (any, error) {
	if agent == peer {
		return nil, web.InvalidRequest("agent and peer are the same Agent " + agent)
	}
	registry, err := b.loadRegistry()
	if err != nil {
		return nil, err
	}
	for _, uid := range []string{agent, peer} {
		if _, ok := registry.Agent(uid); !ok {
			return nil, web.NotFound("no agent " + uid)
		}
	}
	conv, err := b.readConversations()
	if err != nil {
		return nil, err
	}
	out := peerMessages{Agent: agent, Peer: peer, Messages: []peerMessageItem{}, Since: conv.since, Skipped: conv.skipped}
	for _, message := range conv.messages {
		direction := ""
		switch {
		case message.Source == agent && message.Target == peer:
			direction = "outgoing"
		case message.Source == peer && message.Target == agent:
			direction = "incoming"
		default:
			continue
		}
		item := peerMessageItem{
			MessageRef: message.MessageRef, ConversationRef: message.ConversationRef, ReplyTo: message.ReplyTo,
			Direction: direction, Source: message.Source, Target: message.Target, State: message.State,
			AcceptedAt: message.AcceptedAt, PayloadBytes: message.PayloadBytes, BodyRetained: message.BodyRetained,
		}
		if message.BodyRetained {
			payload := message.Payload
			item.Payload = &payload
		}
		out.Messages = append(out.Messages, item)
	}
	slices.SortFunc(out.Messages, func(x, y peerMessageItem) int {
		return cmp.Or(x.AcceptedAt.Compare(y.AcceptedAt), cmp.Compare(x.MessageRef, y.MessageRef))
	})
	return out, nil
}
