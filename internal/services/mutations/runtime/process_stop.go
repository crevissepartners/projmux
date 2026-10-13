package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const ProcessAgentStopTimeout = 10 * time.Second
const ProcessAgentStopPoll = 20 * time.Millisecond

type ProcessStopFailure struct {
	Code  string
	Cause error
}

func (e *ProcessStopFailure) Error() string    { return e.Cause.Error() }
func (e *ProcessStopFailure) Unwrap() error    { return e.Cause }
func stopFailure(code string, err error) error { return &ProcessStopFailure{Code: code, Cause: err} }

type ProcessIdentityReader func(int) (coremetadata.ProcessIdentity, int, error)

// ProcessAgentStopper is the shared CLI/SDK exact-generation executor. Paths
// and readers are explicit. It never deletes resources or writes Offline.
type ProcessAgentStopper struct {
	RegistryPath  string
	ClaimReleased bool
	Termination   *coremetadata.TerminationEvidence
	Load          func() (coremetadata.Registry, error)
	ReadIdentity  ProcessIdentityReader
	CallStop      func(context.Context, coremetadata.Registry, coremetadata.Agent) error
}

func (s *ProcessAgentStopper) identityReader() ProcessIdentityReader {
	if s.ReadIdentity != nil {
		return s.ReadIdentity
	}
	return localipc.Process
}
func (s *ProcessAgentStopper) stop(ctx context.Context, reg coremetadata.Registry, agent coremetadata.Agent) error {
	if s.CallStop != nil {
		return s.CallStop(ctx, reg, agent)
	}
	return CallProcessAgentStop(ctx, s.RegistryPath, reg, agent)
}
func ProcessIdentityAbsent(recorded coremetadata.ProcessIdentity, read ProcessIdentityReader) bool {
	if !recorded.Valid() || int64(recorded.OwnerUID) != int64(os.Getuid()) {
		return false
	}
	current, _, err := read(recorded.PID)
	return errors.Is(err, localipc.ErrProcessAbsent) || (err == nil && current.Valid() && current != recorded)
}
func ProcessActivationAbsent(a coremetadata.ProcessActivation, read ProcessIdentityReader) bool {
	return ProcessIdentityAbsent(a.HostProcess, read) && ProcessIdentityAbsent(a.Child, read)
}
func ProcessAgentPane(reg coremetadata.Registry, uid string) (*coremetadata.Pane, bool) {
	var found *coremetadata.Pane
	for i := range reg.Panes {
		pane := &reg.Panes[i]
		if pane.Metadata.OwnerUID() == uid && pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
			if found != nil {
				return nil, true
			}
			found = pane
		}
	}
	return found, false
}

func (s *ProcessAgentStopper) StopExact(ctx context.Context, reg coremetadata.Registry, agent coremetadata.Agent, pane coremetadata.Pane) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pane.Status.ProcessSession == nil {
		return processhost.ErrStale
	}
	s.ClaimReleased = false
	s.Termination = pane.Status.LastTermination.Clone()
	admission := ctx
	timeout := ProcessAgentStopTimeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	latest, err := s.Load()
	if err != nil {
		return err
	}
	current, ok := latest.Pane(pane.Metadata.UID)
	target, found := latest.Agent(agent.Metadata.UID)
	exact, ambiguous := ProcessAgentPane(latest, agent.Metadata.UID)
	if !ok || !found || ambiguous || exact == nil || exact.Metadata.UID != pane.Metadata.UID || current.Metadata.OwnerUID() != agent.Metadata.UID || target.Status.PaneRef != pane.Metadata.UID || current.Status.ProcessSession == nil || current.Status.ProcessSession.Binding != pane.Status.ProcessSession.Binding {
		return processhost.ErrStale
	}
	if err := admission.Err(); err != nil {
		return err
	}
	if handled, err := s.stopDetachedClaim(ctx, admission, agent, pane); handled || err != nil {
		s.ClaimReleased = handled && err == nil
		return err
	}
	if pane.Status.Activation.IsZero() && agent.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) && coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) {
		return nil
	}
	if pane.Status.Activation.Process == nil {
		return stopFailure("agent-stop-wait-unconfirmed", errors.New("agent-stop-wait-unconfirmed: exact process owner unavailable"))
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
		if err := admission.Err(); err != nil {
			return err
		}
		if err := s.stop(ctx, reg, agent); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(ProcessAgentStopPoll)
	defer ticker.Stop()
	for {
		latest, err := s.Load()
		if err != nil {
			return err
		}
		current, ok := latest.Pane(pane.Metadata.UID)
		target, found := latest.Agent(agent.Metadata.UID)
		if !ok || !found || current.Metadata.OwnerUID() != agent.Metadata.UID || current.Status.ProcessSession == nil || current.Status.ProcessSession.Binding != pane.Status.ProcessSession.Binding {
			return processhost.ErrStale
		}
		s.Termination = current.Status.LastTermination.Clone()
		if target.Status.Phase == coremetadata.PhaseOffline && coremetadata.MatchesProcessWait(current.Status.ProcessSession.Binding, current.Status.LastTermination) && coremetadata.SameProcessWait(current.Status.LastTermination, target.Status.LastTermination) && ProcessActivationAbsent(*pane.Status.Activation.Process, s.identityReader()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return stopFailure("agent-stop-wait-unconfirmed", fmt.Errorf("agent-stop-wait-unconfirmed: agent uid:%s pane uid:%s; inspect with projmux describe agent uid:%s: %w", agent.Metadata.UID, pane.Metadata.UID, agent.Metadata.UID, ctx.Err()))
		case <-ticker.C:
		}
	}
}
