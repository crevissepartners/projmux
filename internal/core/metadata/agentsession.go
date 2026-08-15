package metadata

import (
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
)

// AgentSessionRef is the durable pointer from an Agent to the provider
// conversation that Agent belongs to.
//
// It lives in status, not spec, because nothing declares it. A provider hook
// reports it after the fact and the Agent keeps whatever was last observed —
// exactly the contract status.paneRef already has. The difference between the
// two is the question each answers: status.paneRef is the *current* managed
// Pane binding and is cleared the moment that Pane is released, while this ref
// answers "which conversation is this Agent" and deliberately survives
// ReleaseAgentPane, so an Offline Agent still knows what it was.
//
// This is also not a duplicate of the tmux pane option `@projmux_ai_session_id`.
// That option is a *live routing index*: hook ingest scans the live pane list
// and matches on it to decide which pane an incoming event belongs to, so
// following pane lifetime is correct for it. This field is the durable
// conversation pointer and must outlive the Pane.
//
// The shape is a per-provider discriminated union rather than one flat string
// or one shared bag of ids. Providers do not agree on what identifies a
// conversation: Claude reports a session id plus a transcript path, Codex
// reports a thread id and a session id, Antigravity reports a conversation id.
// Flattening them would either assert a false equivalence between a Codex
// thread id and a Claude session id, or leave a reader holding an opaque
// string with no way to tell which spelling it has. Provider is the
// discriminator and exactly one member is populated.
type AgentSessionRef struct {
	// Provider is the normalized provider id and is the union discriminator.
	// It always matches the populated member.
	Provider string `json:"provider"`
	// ObservedAt is when the reporting hook was ingested. It is an observation
	// timestamp about projmux, not a timestamp the provider supplied.
	ObservedAt time.Time `json:"observedAt"`

	Claude      *ClaudeSessionRef      `json:"claude,omitempty"`
	Codex       *CodexSessionRef       `json:"codex,omitempty"`
	Antigravity *AntigravitySessionRef `json:"antigravity,omitempty"`
}

// ClaudeSessionRef is Claude's conversation identity as its hook reports it.
//
// TranscriptPath is stored as a path only. Nothing in projmux reads the
// transcript contents to populate this ref, and nothing may start: parsing
// another tool's conversation store is permanently out of scope. Only what the
// hook hands over is recorded.
type ClaudeSessionRef struct {
	SessionID      string `json:"sessionId"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
}

// CodexSessionRef is Codex's conversation identity as its hook reports it.
//
// Codex also reports a turn id. It is deliberately absent: a turn id addresses
// one turn inside the conversation and changes on every hook event, so it does
// not point at the conversation. Storing it would give the Agent a durable
// field that is stale the moment it is written.
type CodexSessionRef struct {
	ThreadID  string `json:"threadId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// AntigravitySessionRef is Antigravity's conversation identity as its hook
// reports it. Antigravity carries a single conversation id and reports it in
// both the thread and session slots of the shared hook seam.
type AntigravitySessionRef struct {
	ConversationID string `json:"conversationId"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
}

// AgentSessionObservation is the raw identifier set one provider hook handed
// over. It is the ingest-side input shape; folding it onto the right union
// member is this package's job so the app layer never encodes provider shape
// knowledge of its own.
type AgentSessionObservation struct {
	Provider       string
	SessionID      string
	ThreadID       string
	TranscriptPath string
}

// NewAgentSessionRef folds one hook observation onto the provider member that
// owns it.
//
// It reports false — rather than returning a half-populated ref — when the
// provider is not a known provider id or when the observation carries no
// usable conversation identifier at all. A hook that fires before the provider
// has a conversation id is normal, and recording an empty pointer would be
// worse than recording nothing.
func NewAgentSessionRef(obs AgentSessionObservation, observedAt time.Time) (*AgentSessionRef, bool) {
	provider := NormalizeProvider(obs.Provider)
	sessionID := strings.TrimSpace(obs.SessionID)
	threadID := strings.TrimSpace(obs.ThreadID)
	transcript := strings.TrimSpace(obs.TranscriptPath)

	ref := &AgentSessionRef{Provider: provider, ObservedAt: observedAt.UTC()}
	switch aiprovider.ID(provider) {
	case aiprovider.Claude:
		if sessionID == "" {
			return nil, false
		}
		ref.Claude = &ClaudeSessionRef{SessionID: sessionID, TranscriptPath: transcript}
	case aiprovider.Codex:
		if threadID == "" && sessionID == "" {
			return nil, false
		}
		ref.Codex = &CodexSessionRef{ThreadID: threadID, SessionID: sessionID}
	case aiprovider.Antigravity:
		conversationID := threadID
		if conversationID == "" {
			conversationID = sessionID
		}
		if conversationID == "" {
			return nil, false
		}
		ref.Antigravity = &AntigravitySessionRef{ConversationID: conversationID, TranscriptPath: transcript}
	default:
		return nil, false
	}
	return ref, true
}

// Clone returns a deep copy. Every member is a pointer, so a shallow copy would
// let two registry snapshots alias the same conversation record.
func (r *AgentSessionRef) Clone() *AgentSessionRef {
	if r == nil {
		return nil
	}
	out := *r
	if r.Claude != nil {
		claude := *r.Claude
		out.Claude = &claude
	}
	if r.Codex != nil {
		codex := *r.Codex
		out.Codex = &codex
	}
	if r.Antigravity != nil {
		antigravity := *r.Antigravity
		out.Antigravity = &antigravity
	}
	return &out
}

// Empty reports whether the ref points at no conversation at all.
func (r *AgentSessionRef) Empty() bool {
	return r == nil || (r.Claude == nil && r.Codex == nil && r.Antigravity == nil)
}

// SameConversation reports whether two refs point at the same conversation.
// ObservedAt is deliberately excluded: it records when projmux last saw the
// conversation, not which conversation it is, so a re-observation of the same
// ids is not a change and must not trigger a registry write.
func (r *AgentSessionRef) SameConversation(other *AgentSessionRef) bool {
	if r == nil || other == nil {
		return r == nil && other == nil
	}
	if r.Provider != other.Provider {
		return false
	}
	switch {
	case r.Claude != nil || other.Claude != nil:
		return r.Claude != nil && other.Claude != nil && *r.Claude == *other.Claude
	case r.Codex != nil || other.Codex != nil:
		return r.Codex != nil && other.Codex != nil && *r.Codex == *other.Codex
	case r.Antigravity != nil || other.Antigravity != nil:
		return r.Antigravity != nil && other.Antigravity != nil && *r.Antigravity == *other.Antigravity
	default:
		return true
	}
}

// ConversationID returns the identifier that names the conversation for the
// populated provider. It is the one value a human reads to recognize which
// conversation an Agent belongs to.
func (r *AgentSessionRef) ConversationID() string {
	switch {
	case r == nil:
		return ""
	case r.Claude != nil:
		return r.Claude.SessionID
	case r.Codex != nil:
		if r.Codex.ThreadID != "" {
			return r.Codex.ThreadID
		}
		return r.Codex.SessionID
	case r.Antigravity != nil:
		return r.Antigravity.ConversationID
	default:
		return ""
	}
}

// Summary renders the provider-qualified conversation pointer for a one-line
// human projection.
func (r *AgentSessionRef) Summary() string {
	if r.Empty() {
		return ""
	}
	return r.Provider + ":" + r.ConversationID()
}

// Fields returns the populated provider's identifier set as ordered display
// key/value pairs. The keys differ per provider on purpose: that difference is
// the whole reason the shape is a union rather than one flat record.
func (r *AgentSessionRef) Fields() [][2]string {
	if r == nil {
		return nil
	}
	var rows [][2]string
	add := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			rows = append(rows, [2]string{key, value})
		}
	}
	switch {
	case r.Claude != nil:
		add("SessionID", r.Claude.SessionID)
		add("TranscriptPath", r.Claude.TranscriptPath)
	case r.Codex != nil:
		add("ThreadID", r.Codex.ThreadID)
		add("SessionID", r.Codex.SessionID)
	case r.Antigravity != nil:
		add("ConversationID", r.Antigravity.ConversationID)
		add("TranscriptPath", r.Antigravity.TranscriptPath)
	}
	return rows
}

// RecordAgentSessionRef stores one hook-observed provider session ref on an
// Agent and reports whether the registry actually changed.
//
// The write is deliberately narrow. It touches status.sessionRef and nothing
// else: not the phase, not lastTransitionAt, not paneRef. Observing which
// conversation an Agent belongs to is not a lifecycle event, and letting an
// ingest hook move an Agent through the phase machine would make the closed
// transition table answerable by an external tool.
//
// A re-observation of the same conversation returns changed=false so a hook
// that fires on every turn does not rewrite the registry file each time.
//
// A conversation already claimed by another Agent is NOT refused. Uniqueness is
// not an invariant this model can honestly hold: the same provider conversation
// really can be attached twice (a manual resume of the same session id in a
// second pane already does exactly that today), an Offline Agent keeps its ref
// forever, and refusing the write would make the registry describe a world that
// does not exist rather than the one that does. Choosing between several Agents
// that point at one conversation is a resume-time decision and belongs to the
// resume materialization Phase, not to this observation write.
func (m Mutator) RecordAgentSessionRef(reg *Registry, agentUID string, obs AgentSessionObservation) (Agent, bool, error) {
	const op = "record agent session ref"

	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, false, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	ref, ok := NewAgentSessionRef(obs, m.clock()().UTC())
	if !ok {
		return Agent{}, false, inputErr(op, ErrInvalidRegistry, "observation carries no usable %q conversation id", obs.Provider)
	}
	if agent.Spec.Provider != "" && agent.Spec.Provider != ref.Provider {
		return Agent{}, false, inputErr(op, ErrInvalidRegistry, "agent %s is a %s Agent; refusing to record a %s conversation",
			agent.Metadata.Name, agent.Spec.Provider, ref.Provider)
	}
	if agent.Status.SessionRef.SameConversation(ref) {
		return agent.Clone(), false, nil
	}
	agent.Status.SessionRef = ref
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), true, nil
}

// validateSessionRef checks the union invariants of one Agent session ref.
//
// The rules are structural only. There is deliberately NO "at most one Agent
// per conversation" rule here: see the package documentation on
// AgentStatus.SessionRef for why that uniqueness is not enforced.
func validateSessionRef(op string, agent Agent) error {
	ref := agent.Status.SessionRef
	if ref == nil {
		return nil
	}
	if _, ok := aiprovider.Lookup(ref.Provider); !ok {
		return stateErr(op, ErrInvalidRegistry, "agent %q sessionRef has unknown provider %q", agent.Metadata.Name, ref.Provider)
	}
	populated := 0
	if ref.Claude != nil {
		populated++
	}
	if ref.Codex != nil {
		populated++
	}
	if ref.Antigravity != nil {
		populated++
	}
	if populated != 1 {
		return stateErr(op, ErrInvalidRegistry, "agent %q sessionRef must populate exactly one provider member; got %d", agent.Metadata.Name, populated)
	}
	member := ""
	switch {
	case ref.Claude != nil:
		member = string(aiprovider.Claude)
	case ref.Codex != nil:
		member = string(aiprovider.Codex)
	case ref.Antigravity != nil:
		member = string(aiprovider.Antigravity)
	}
	if member != ref.Provider {
		return stateErr(op, ErrInvalidRegistry, "agent %q sessionRef provider %q does not match its populated %q member", agent.Metadata.Name, ref.Provider, member)
	}
	if ref.ConversationID() == "" {
		return stateErr(op, ErrInvalidRegistry, "agent %q sessionRef carries no conversation id", agent.Metadata.Name)
	}
	// spec.provider is only cross-checked when the Agent actually declares one.
	// An Agent created from an unrecognized provider spelling normalizes to "",
	// and refusing to record its observed conversation would punish the Agent
	// for the registry's own gap.
	if agent.Spec.Provider != "" && agent.Spec.Provider != ref.Provider {
		return stateErr(op, ErrInvalidRegistry, "agent %q is a %s Agent but its sessionRef is a %s conversation", agent.Metadata.Name, agent.Spec.Provider, ref.Provider)
	}
	return nil
}
