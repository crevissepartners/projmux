package runtime

import (
	"context"
	"errors"
	"fmt"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

// ProcessResumeCandidateRequest selects one process Agent from a Registry the
// caller already holds. Nothing here reads the Registry file.
type ProcessResumeCandidateRequest struct {
	Agent, Project, Window selector.Ref
	AllowHostLost          bool
	// RegistryPath locates the deferred launch and claim sidecars.
	RegistryPath string
	// ReadIdentity is the kernel liveness seam; nil reads /proc.
	ReadIdentity ProcessIdentityReader
	// CheckClaim replaces the read-only claim check for a claim holder.
	CheckClaim func(uid string) error
}

func (r ProcessResumeCandidateRequest) identityReader() ProcessIdentityReader {
	if r.ReadIdentity != nil {
		return r.ReadIdentity
	}
	return localipc.Process
}

// ResumePreparationStaleError wraps ErrResumePreparationStale with its detail.
func ResumePreparationStaleError(detail string) error {
	return fmt.Errorf("%w: %s", ErrResumePreparationStale, detail)
}

// CheckDeferredClaimUnowned refuses an Agent whose deferred claim is live.
func CheckDeferredClaimUnowned(registryPath, uid string) error {
	record, err := ReadDeferredClaim(DeferredClaimPath(registryPath, uid))
	if err != nil {
		return err
	}
	if DeferredClaimLive(record) {
		return fmt.Errorf("%s: %w: deferred process Agent has a live claimant", ProcessResumeOwned, processhost.ErrResumeRefused)
	}
	return nil
}

// ResolveProcessResumeCandidate resolves the selected Agent against reg and
// returns its durable resume candidate without writing anything. Selector
// failures are returned as the selector reports them.
func ResolveProcessResumeCandidate(reg coremetadata.Registry, req ProcessResumeCandidateRequest) (ProcessResumeCandidate, error) {
	query := selector.Query{Agents: []selector.Ref{req.Agent}}
	if req.Project != (selector.Ref{}) {
		query.Project = &req.Project
	}
	if req.Window != (selector.Ref{}) {
		query.Windows = []selector.Ref{req.Window}
	}
	resolution, err := selector.New(reg).ResolveAgents(query)
	if err != nil {
		return ProcessResumeCandidate{}, err
	}
	if err := selector.Enforce(selector.Target{Verb: selector.VerbResume, Kind: coremetadata.KindAgent}, selector.DescribeSelector(query), resolution); err != nil {
		return ProcessResumeCandidate{}, err
	}
	uid := resolution.Matches[0].UID
	if req.CheckClaim != nil {
		err = req.CheckClaim(uid)
	} else {
		err = CheckDeferredClaimUnowned(req.RegistryPath, uid)
	}
	if err != nil {
		return ProcessResumeCandidate{}, err
	}
	read := req.identityReader()
	pane, _ := ProcessAgentPane(reg, uid)
	alive := false
	if pane != nil && pane.Status.Activation.Process != nil {
		identity, _, e := read(pane.Status.Activation.Process.HostProcess.PID)
		alive = e == nil && identity == pane.Status.Activation.Process.HostProcess
	}
	if req.AllowHostLost && !alive && pane != nil && pane.Status.Activation.Process != nil {
		if candidate, ok := HostLostResumeCandidate(reg, uid, read); ok {
			return candidate, nil
		}
	}
	if err = ProcessResumeRefusal(reg, uid, alive); err != nil {
		return ProcessResumeCandidate{}, err
	}
	for _, candidate := range ListResumableProcessAgents(reg, ProcessResumeFilter{}) {
		if candidate.Agent.Metadata.UID == uid {
			return candidate, nil
		}
	}
	return ProcessResumeCandidate{}, fmt.Errorf("%s: %w: selected durable candidate is unavailable", ProcessResumeRefused, processhost.ErrResumeRefused)
}

// PreparedResumeProjection is the proof a prepared launch record gives for its
// candidate.
func PreparedResumeProjection(candidate ProcessResumeCandidate, record *DeferredLaunchRecord) ProcessResumePreparation {
	return ProcessResumePreparation{AgentUID: record.Agent, PaneUID: record.Retired.Binding.PaneUID, Conversation: record.Retired.SessionID, LaunchDigest: DeferredLaunchDigest(record), Binding: candidate.Record.Binding, HostLost: candidate.HostLost != nil}
}

// ReadProcessResumePreparation does not reconcile: reconciliation can write a
// sidecar even when the caller only wants to read. A supplied HostLost bit is
// evidence to compare, not admission authority, so the caller admits host-lost
// candidates whenever expected is set. reg is used as given; no Registry file
// is read.
func ReadProcessResumePreparation(ctx context.Context, reg coremetadata.Registry, req ProcessResumeCandidateRequest, expected *ProcessResumePreparation) (ProcessResumePreparation, *DeferredLaunchRecord, error) {
	var zero ProcessResumePreparation
	candidate, err := ResolveProcessResumeCandidate(reg, req)
	if err != nil {
		return zero, nil, err
	}
	if candidate.Record.Provider != processProviderClaude {
		return zero, nil, &createUsageError{message: "agent resume: preparation requires process Claude"}
	}
	record, err := ReadDeferredLaunch(req.RegistryPath, candidate.Agent.Metadata.UID)
	if err != nil || record == nil {
		return zero, nil, ResumePreparationStaleError("prepared launch missing or damaged")
	}
	if !record.Matches(candidate) {
		return zero, nil, ResumePreparationStaleError("prepared source changed")
	}
	if err = record.ValidatePermissionMode(); err != nil {
		return zero, nil, ResumePreparationStaleError("prepared permission changed")
	}
	if err = record.ValidateFiles(); err != nil {
		return zero, nil, ResumePreparationStaleError("prepared files changed")
	}
	proof := PreparedResumeProjection(candidate, record)
	if expected != nil && proof != *expected {
		return zero, nil, ResumePreparationStaleError("acknowledged preparation changed")
	}
	if err = ctx.Err(); err != nil {
		return zero, nil, err
	}
	return proof, record, nil
}

// ProcessResumeErrorCode classifies a resume failure for the detached reply.
// isLocalProfile recognizes the caller's own profile refusal type.
func ProcessResumeErrorCode(err error, isLocalProfile func(error) bool) string {
	if errors.Is(err, ErrResumePreparationStale) {
		return ResumePreparationStale
	}
	var failure *mutations.Failure
	if errors.As(err, &failure) {
		return string(failure.Code)
	}
	var refusal *AgentCreateRefusal
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	var profile *ProfileResumeError
	if errors.As(err, &profile) || (isLocalProfile != nil && isLocalProfile(err)) {
		return "profile-resume-unavailable"
	}
	if coremetadata.IsUsageError(err) {
		return "resume-request-invalid"
	}
	if errors.Is(err, processhost.ErrStale) {
		return "process-resume-refused"
	}
	if errors.Is(err, processhost.ErrResumeRefused) {
		return "process-resume-refused"
	}
	return "resume-execution-failed"
}
