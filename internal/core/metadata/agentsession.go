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
	// HasStartedTurn is monotonic content-free evidence that this thread has
	// owned at least one turn. False keeps a payload-free native create in the
	// generation obligation ledger even after its Pane goes Offline.
	HasStartedTurn bool              `json:"hasStartedTurn,omitempty"`
	Endpoint       *CodexEndpointRef `json:"endpoint,omitempty"`
	// Lifecycle is the durable generation/operation authority consumed by the
	// presentation reconciler. Provider observations never create or change it;
	// later control-plane phases may set it alongside Endpoint.
	Lifecycle *CodexGenerationLifecycleRef `json:"lifecycle,omitempty"`
}

// CodexGenerationState is the closed durable generation vocabulary shared by
// the pool model and the presentation reconciler.
type CodexGenerationState string

const (
	CodexGenerationPreparing       CodexGenerationState = "preparing"
	CodexGenerationCurrent         CodexGenerationState = "current"
	CodexGenerationDraining        CodexGenerationState = "draining"
	CodexGenerationHandoverPending CodexGenerationState = "handover-pending"
	CodexGenerationRetired         CodexGenerationState = "retired"
	CodexGenerationRecovering      CodexGenerationState = "recovering"
	CodexGenerationBlocked         CodexGenerationState = "blocked"
)

// CodexGenerationOperationRef distinguishes an explicitly planned generation
// transition from ordinary provider/process failure. It is content-free and
// exact to one endpoint generation.
type CodexGenerationOperationRef struct {
	ID       string           `json:"id"`
	Endpoint CodexEndpointRef `json:"endpoint"`
}

// ValidFor requires an exact endpoint match. A syntactically valid operation
// from another endpoint is never authority.
func (r CodexGenerationOperationRef) ValidFor(endpoint *CodexEndpointRef) bool {
	return endpoint != nil && validCodexIdentityToken(r.ID) && r.Endpoint.Same(*endpoint)
}

// CodexGenerationLifecycleRef is the durable semantic input to lifecycle
// projection. It deliberately has no exit, process, executable, version,
// socket, or tmux field: those observations cannot invent maintenance state.
type CodexGenerationLifecycleRef struct {
	State     CodexGenerationState         `json:"state"`
	Operation *CodexGenerationOperationRef `json:"operation,omitempty"`
}

// ValidFor closes the state/operation relation for one exact endpoint.
func (r CodexGenerationLifecycleRef) ValidFor(endpoint *CodexEndpointRef) bool {
	if endpoint == nil || !endpoint.Valid() {
		return false
	}
	switch r.State {
	case CodexGenerationDraining, CodexGenerationHandoverPending, CodexGenerationRecovering, CodexGenerationBlocked:
		return r.Operation != nil && r.Operation.ValidFor(endpoint)
	case CodexGenerationPreparing, CodexGenerationCurrent, CodexGenerationRetired:
		return r.Operation == nil
	default:
		return false
	}
}

// CodexEndpointRef is the durable endpoint identity of one Codex thread.
//
// It is deliberately independent of PaneActivation.Generation. A Pane
// generation identifies one materialized child process, while this reference
// survives that process and pins the provider thread to the state domain and
// app-server generation that currently owns it. Both fields are opaque,
// content-free identifiers; neither is a path, socket, version, or credential.
// A nil pointer is the only legacy spelling and is never inferred as current.
type CodexEndpointRef struct {
	StateDomainID        string `json:"stateDomainID"`
	EndpointGenerationID string `json:"endpointGenerationID"`
}

// Valid reports whether both dimensions of an endpoint identity are present.
func (r CodexEndpointRef) Valid() bool {
	return validCodexIdentityToken(r.StateDomainID) && validCodexIdentityToken(r.EndpointGenerationID)
}

// Same reports exact endpoint identity. Empty or partial references never
// compare equal as authority.
func (r CodexEndpointRef) Same(other CodexEndpointRef) bool {
	return r.Valid() && other.Valid() &&
		r.StateDomainID == other.StateDomainID && r.EndpointGenerationID == other.EndpointGenerationID
}

func validCodexIdentityToken(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
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
	Endpoint       *CodexEndpointRef
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
		if obs.Endpoint != nil {
			endpoint := *obs.Endpoint
			if !endpoint.Valid() {
				return nil, false
			}
			ref.Codex.Endpoint = &endpoint
		}
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
		if r.Codex.Endpoint != nil {
			endpoint := *r.Codex.Endpoint
			codex.Endpoint = &endpoint
		}
		if r.Codex.Lifecycle != nil {
			lifecycle := *r.Codex.Lifecycle
			if r.Codex.Lifecycle.Operation != nil {
				operation := *r.Codex.Lifecycle.Operation
				lifecycle.Operation = &operation
			}
			codex.Lifecycle = &lifecycle
		}
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
		return sameCodexSessionRef(r.Codex, other.Codex)
	case r.Antigravity != nil || other.Antigravity != nil:
		return r.Antigravity != nil && other.Antigravity != nil && *r.Antigravity == *other.Antigravity
	default:
		return true
	}
}

func sameCodexSessionRef(a, b *CodexSessionRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.ThreadID != b.ThreadID || a.SessionID != b.SessionID {
		return false
	}
	if a.Endpoint == nil || b.Endpoint == nil {
		return a.Endpoint == nil && b.Endpoint == nil
	}
	return *a.Endpoint == *b.Endpoint
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
		if r.Codex.Endpoint != nil {
			add("StateDomainID", r.Codex.Endpoint.StateDomainID)
			add("EndpointGenerationID", r.Codex.Endpoint.EndpointGenerationID)
		}
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
	// Provider hooks in the legacy/default lane know the thread but not the
	// generation endpoint. A re-observation of the same exact conversation
	// must not erase a ref written by a generation-aware producer, and it must
	// not guess a ref when there is none.
	if ref.Codex != nil && ref.Codex.Endpoint == nil && agent.Status.SessionRef != nil &&
		sameCodexThreadObservation(agent.Status.SessionRef.Codex, ref.Codex) &&
		agent.Status.SessionRef.Codex.Endpoint != nil {
		previous := agent.Status.SessionRef.Codex
		endpoint := *previous.Endpoint
		ref.Codex.Endpoint = &endpoint
		ref.Codex.HasStartedTurn = previous.HasStartedTurn
		if previous.Lifecycle != nil {
			lifecycle := *previous.Lifecycle
			if lifecycle.Operation != nil {
				operation := *lifecycle.Operation
				lifecycle.Operation = &operation
			}
			ref.Codex.Lifecycle = &lifecycle
		}
		// SessionID is optional hook metadata. Its omission must not turn an
		// otherwise exact thread re-observation into a destructive rewrite.
		if ref.Codex.SessionID == "" {
			ref.Codex.SessionID = previous.SessionID
		}
	}
	if agent.Status.SessionRef.SameConversation(ref) {
		return agent.Clone(), false, nil
	}
	agent.Status.SessionRef = ref
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), true, nil
}

func sameCodexThreadObservation(previous, observed *CodexSessionRef) bool {
	return previous != nil && observed != nil && previous.ThreadID != "" && previous.ThreadID == observed.ThreadID
}

// SetCodexGenerationLifecycle applies one exact, content-free generation
// transition marker. It never changes the endpoint, thread, Pane, provider
// binding, interaction, or Agent phase. Reapplying the identical marker is a
// semantic no-op so crash recovery can converge Registry after a journaled
// admission switch without duplicating a write.
func (m Mutator) SetCodexGenerationLifecycle(reg *Registry, agentUID string, endpoint CodexEndpointRef, lifecycle CodexGenerationLifecycleRef) (Agent, bool, error) {
	const op = "set Codex generation lifecycle"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, false, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	ref := agent.Status.SessionRef
	if ref == nil || ref.Provider != "codex" || ref.Codex == nil || ref.Codex.Endpoint == nil ||
		!ref.Codex.Endpoint.Same(endpoint) {
		return Agent{}, false, inputErr(op, ErrInvalidRegistry, "agent %q does not own exact Codex endpoint", agentUID)
	}
	if !lifecycle.ValidFor(&endpoint) {
		return Agent{}, false, inputErr(op, ErrInvalidRegistry, "Codex lifecycle authority is invalid for agent %q", agentUID)
	}
	if sameCodexGenerationLifecycle(ref.Codex.Lifecycle, &lifecycle) {
		return agent.Clone(), false, nil
	}
	copy := lifecycle
	if lifecycle.Operation != nil {
		operation := *lifecycle.Operation
		copy.Operation = &operation
	}
	ref.Codex.Lifecycle = &copy
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), true, nil
}

func sameCodexGenerationLifecycle(a, b *CodexGenerationLifecycleRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.State != b.State || (a.Operation == nil) != (b.Operation == nil) {
		return false
	}
	return a.Operation == nil || *a.Operation == *b.Operation
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
	if ref.Codex != nil && ref.Codex.Endpoint != nil && !ref.Codex.Endpoint.Valid() {
		return stateErr(op, ErrInvalidRegistry, "agent %q Codex sessionRef has an incomplete endpoint identity", agent.Metadata.Name)
	}
	if ref.Codex != nil && ref.Codex.Lifecycle != nil && !ref.Codex.Lifecycle.ValidFor(ref.Codex.Endpoint) {
		return stateErr(op, ErrInvalidRegistry, "agent %q Codex sessionRef has invalid generation lifecycle authority", agent.Metadata.Name)
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
