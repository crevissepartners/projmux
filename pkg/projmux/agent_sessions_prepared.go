package projmux

import (
	"context"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	"github.com/crevissepartners/projmux/internal/services/agentsessions"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ListAgentSessionsFromPrepared returns what ListAgentSessions returns for the
// same Registry and history, taking the Agent row from a prepared Registry
// snapshot instead of reading the Registry again. agent follows the grammar of
// ListAgentSessionsRequest.Agent (a name, or uid:<agent-uid> for an exact
// Agent); the Project, Window and label scopes are not offered. Only the
// history file is read, so a Registry file that changed after the handle was
// prepared does not change the result. Refusals match ListAgentSessions, and a
// nil or zero handle, or one prepared by a Client with a different state root,
// is a CodePreparedSnapshotInvalid refusal. Results are caller-owned and the
// handle is never written, so concurrent calls with one handle are supported.
// Cancellation before admission performs no reads.
func (c *Client) ListAgentSessionsFromPrepared(ctx context.Context, prepared *PreparedRegistrySnapshot, agent string) (core.AgentSessionsList, error) {
	if err := c.admitPreparedSessions(ctx, prepared); err != nil {
		return core.AgentSessionsList{}, err
	}
	if agent == "" {
		return core.AgentSessionsList{}, &Refusal{Code: CodeSelectorInvalid, message: "agent sessions list requires <agent-ref>"}
	}
	ref, err := selector.ParseRef(metadata.KindAgent, agent)
	if err != nil {
		return core.AgentSessionsList{}, resourceRefusal(selectorInputError(err, "agent"))
	}
	listed, err := c.preparedSessionsService(prepared).ListAgent(ctx, selector.Query{Agents: []selector.Ref{ref}})
	if err != nil {
		return core.AgentSessionsList{}, sessionReadRefusal(err)
	}
	return publicAgentSessionsList(listed.AgentUID, listed.AgentName, listed.Sessions, listed.CorruptLines), nil
}

// ListProjectSessionsFromPrepared returns what ListProjectSessions returns for
// the same Registry and history, taking the Registry rows from a prepared
// snapshot instead of reading the Registry again. project follows
// ListProjectSessionsRequest.Project, including the exact-UID rule for a
// removed Project. A Registry file that changed after the handle was prepared
// does not change the result. A nil or zero handle, or one prepared by a
// Client with a different state root, is a CodePreparedSnapshotInvalid
// refusal. Results are caller-owned and the handle is never written.
func (c *Client) ListProjectSessionsFromPrepared(ctx context.Context, prepared *PreparedRegistrySnapshot, project string) (core.ProjectSessionsList, error) {
	if err := c.admitPreparedSessions(ctx, prepared); err != nil {
		return core.ProjectSessionsList{}, err
	}
	if project == "" {
		return core.ProjectSessionsList{}, &Refusal{Code: CodeSelectorInvalid, message: "agent sessions project requires exactly one <project-ref>"}
	}
	listed, err := c.preparedSessionsService(prepared).ListProject(ctx, project)
	if err != nil {
		return core.ProjectSessionsList{}, sessionReadRefusal(err)
	}
	return publicProjectSessionsList(listed.ProjectUID, listed.ProjectName, listed.Sessions, listed.Unattributed, listed.Ambiguous, listed.CorruptLines), nil
}

// MergeAgentSessions joins history rows with an Agent's current row into one
// row per (agent, provider, session), in time order, by the rule the session
// lists use: the latest observation wins, a current row outranks an observed
// one and an observed row an estimated one, and each affiliation key keeps the
// latest non-empty value. Rows keep their input order at equal instants, with
// current last. current may be nil. It is a pure function: it reads no file,
// returns a non-nil slice, and the result owns its pointers independently of
// both arguments.
func MergeAgentSessions(history []core.AgentSession, current *core.AgentSession) []core.AgentSession {
	rows := make([]sessionhistory.Record, len(history))
	for i, row := range history {
		rows[i] = internalSessionRecord(row)
	}
	var now *sessionhistory.Record
	if current != nil {
		record := internalSessionRecord(*current)
		now = &record
	}
	merged := sessionhistory.Merge(rows, now)
	out := make([]core.AgentSession, len(merged))
	for i, row := range merged {
		out[i] = publicAgentSession(row)
	}
	return out
}

// CurrentAgentSession builds the current session row of an Agent from the
// session reference its status stores: the Claude session and transcript path,
// or the Codex thread. The row has the current source and no affiliation. It is
// false for an Agent without a UID or a stored reference, or with an empty
// session id, and for a provider with no session history. It is a pure
// function, and the result owns its pointers.
func CurrentAgentSession(agent core.Agent) (core.AgentSession, bool) {
	ref := agent.Status.SessionRef
	if ref == nil {
		return core.AgentSession{}, false
	}
	internal := &metadata.AgentSessionRef{Provider: ref.Provider, ObservedAt: ref.ObservedAt}
	if ref.Claude != nil {
		internal.Claude = &metadata.ClaudeSessionRef{SessionID: ref.Claude.SessionID, TranscriptPath: ref.Claude.TranscriptPath}
	}
	if ref.Codex != nil {
		internal.Codex = &metadata.CodexSessionRef{ThreadID: ref.Codex.ThreadID}
	}
	row, ok := sessionhistory.RecordFor(agent.Metadata.UID, internal, sessionhistory.SourceCurrent)
	if !ok {
		return core.AgentSession{}, false
	}
	return publicAgentSession(row), true
}

// admitPreparedSessions applies the entry checks of the prepared-snapshot
// reads in the order ResolveResourceGraphFromPrepared uses.
func (c *Client) admitPreparedSessions(ctx context.Context, prepared *PreparedRegistrySnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.pathsErr != nil {
		return resourceRefusal(c.pathsErr)
	}
	if !prepared.valid() {
		return &Refusal{Code: CodePreparedSnapshotInvalid, message: "prepared Registry snapshot was not prepared by PrepareRegistrySnapshot"}
	}
	if prepared.root != c.preparedRoot() {
		return &Refusal{Code: CodePreparedSnapshotInvalid, message: "prepared Registry snapshot belongs to a different state root"}
	}
	return nil
}

// preparedSessionsService serves the Registry from the handle. Each call gets
// its own copy, so the shared handle is never written.
func (c *Client) preparedSessionsService(prepared *PreparedRegistrySnapshot) agentsessions.Service {
	service := c.agentSessionsService()
	service.LoadRegistry = func() (metadata.Registry, error) {
		return prepared.registry.Clone(), nil
	}
	return service
}
