package projmux

import (
	"context"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ReadAgentHostLostFromPrepared judges whether one process Agent lost its host,
// from a Registry snapshot the caller already holds. It writes nothing and
// starts no process; the only observation outside the handle is the kernel
// identity of the recorded host process and provider child, each read once
// per call. It reads no Registry file, deferred launch record or claim, so
// Candidate=true is evidence for the host-lost recovery branch of
// PrepareAgentResume(AllowHostLost), not an acceptance, and it is not limited
// to Claude. A cancellation during the kernel reads returns the context error
// and a zero verdict. A tmux
// Agent, a live host or an Agent without a recorded activation is a nil-error
// verdict with Candidate=false. A nil, zero or foreign-root handle is a
// CodePreparedSnapshotInvalid refusal, an ambiguous Pane pair is
// CodeResumePlanInvalid, and selector failures match the other resource
// reads. The handle is never written, so concurrent calls may share it.
func (c *Client) ReadAgentHostLostFromPrepared(ctx context.Context, prepared *PreparedRegistrySnapshot, req core.ReadAgentHostLostRequest) (core.AgentHostLostVerdict, error) {
	var verdict core.AgentHostLostVerdict
	if err := c.admitPreparedSessions(ctx, prepared); err != nil {
		return verdict, err
	}
	if req.Ref == "" {
		return verdict, &Refusal{Code: CodeSelectorInvalid, message: "host-lost verdict requires one Agent reference"}
	}
	query, err := mutationQuery(core.KindAgent, req.Ref, req.Project, req.Window, "ReadAgentHostLostRequest")
	if err != nil {
		return verdict, resourceRefusal(err)
	}
	reg := prepared.registry
	resolution, err := resources.Resolve(reg, m.RuntimeObservation{}, nil, m.KindAgent, query)
	if err != nil {
		return verdict, resourceRefusal(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbReview, Kind: m.KindAgent}, query, resolution); err != nil {
		return verdict, resourceRefusal(err)
	}
	verdict.AgentUID = resolution.Matches[0].UID
	pane, ambiguous := mutationruntime.ProcessAgentPane(reg, verdict.AgentUID)
	if ambiguous {
		return verdict, &Refusal{Code: CodeResumePlanInvalid, AgentUID: verdict.AgentUID, message: "host-lost verdict requires one process Pane"}
	}
	if pane == nil || pane.Status.Activation.Process == nil {
		return verdict, nil
	}
	activation := *pane.Status.Activation.Process
	verdict.PaneUID = pane.Metadata.UID
	if err = unregisterCopy(activation.Binding, &verdict.Binding); err != nil {
		return verdict, err
	}
	read := c.onceIdentityReader()
	verdict.HostIdentityAbsent = mutationruntime.ProcessIdentityAbsent(activation.HostProcess, read)
	verdict.ChildIdentityAbsent = mutationruntime.ProcessIdentityAbsent(activation.Child, read)
	verdict.ActivationAbsent = verdict.HostIdentityAbsent && verdict.ChildIdentityAbsent
	if candidate, ok := mutationruntime.HostLostResumeCandidate(reg, verdict.AgentUID, read); ok {
		verdict.Candidate = true
		verdict.Conversation = candidate.Record.SessionID
		if candidate.Record.Provider == "codex" {
			verdict.Conversation = candidate.Record.ThreadID
		}
		if err = unregisterCopy(candidate.Record.History, &verdict.History); err != nil {
			return core.AgentHostLostVerdict{}, err
		}
		if err = unregisterCopy(candidate.Pane.Status.LastTermination, &verdict.LastTermination); err != nil {
			return core.AgentHostLostVerdict{}, err
		}
	}
	// A cancellation during the kernel reads is not a verdict.
	if err = ctx.Err(); err != nil {
		return core.AgentHostLostVerdict{}, err
	}
	return verdict, nil
}

// onceIdentityReader reads each pid once, so the reasons and the candidate
// judgement of one verdict come from the same kernel observation.
func (c *Client) onceIdentityReader() mutationruntime.ProcessIdentityReader {
	read := c.processIdentity
	if read == nil {
		read = localipc.Process
	}
	type observed struct {
		identity m.ProcessIdentity
		extra    int
		err      error
	}
	seen := map[int]observed{}
	return func(pid int) (m.ProcessIdentity, int, error) {
		if o, ok := seen[pid]; ok {
			return o.identity, o.extra, o.err
		}
		identity, extra, err := read(pid)
		seen[pid] = observed{identity, extra, err}
		return identity, extra, err
	}
}
