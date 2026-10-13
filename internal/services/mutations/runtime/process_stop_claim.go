package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Stop's claim phase is distinct from provider Stop. The same guard used by
// reservation serializes birth and exact claim cancellation. A claim is never
// provider Wait evidence; no Registry termination record is written here.
func (s *ProcessAgentStopper) stopDetachedClaim(ctx, admission context.Context, agent coremetadata.Agent, pane coremetadata.Pane) (bool, error) {
	path := DeferredClaimPath(s.RegistryPath, agent.Metadata.UID)
	unlock, err := LockDeferredClaimContext(admission, path)
	if err != nil {
		return false, err
	}
	if err := admission.Err(); err != nil {
		unlock()
		return false, err
	}
	record, err := ReadDeferredClaim(path)
	if err != nil {
		unlock()
		return false, err
	}
	// Delete freezes the claimant observed at preflight. Ordinary Stop keeps
	// its current-generation admission when no expected claimant is supplied.
	if s.ExpectedClaimNonce != "" && (record.Nonce != s.ExpectedClaimNonce || record.Process != s.ExpectedClaimOwner) {
		unlock()
		return true, fmt.Errorf("expected claim nonce %q owner %+v, observed nonce %q owner %+v: %w", s.ExpectedClaimNonce, s.ExpectedClaimOwner, record.Nonce, record.Process, processhost.ErrStale)
	}
	if !record.Detached {
		unlock()
		return false, nil
	}
	reg, err := s.Load()
	if err != nil {
		unlock()
		return true, err
	}
	current, found := reg.Pane(pane.Metadata.UID)
	if !found || current.Status.ProcessSession == nil {
		unlock()
		return true, processhost.ErrStale
	}
	target, found := reg.Agent(agent.Metadata.UID)
	if !found || current.Status.ProcessSession.Binding != pane.Status.ProcessSession.Binding || target.Status.PaneRef != pane.Metadata.UID {
		unlock()
		return true, processhost.ErrStale
	}
	// Birth publishes a fresh binding under this guard. Once its activation is
	// available, Stop uses that provider's authority and actual Wait. A dead
	// old claim must not block a later, independently admitted generation.
	if !current.Status.Activation.IsZero() || current.Status.ProcessSession.Binding != record.Binding {
		unlock()
		if current.Status.Activation.IsZero() && target.Status.Phase == coremetadata.PhaseOffline && DeferredClaimLive(record) {
			return true, processhost.ErrStale
		}
		return false, nil
	}
	if !DeferredClaimLive(record) {
		unlock()
		return true, stopFailure("agent-stop-claim-release-unconfirmed", errors.New("agent-stop-claim-release-unconfirmed: exact claimant unavailable; inspect the Agent before retrying"))
	}
	if target.Status.Phase != coremetadata.PhaseOffline || !reflect.DeepEqual(current.Status.ProcessSession, pane.Status.ProcessSession) || record.Provider != current.Status.ProcessSession.Provider || !DeferredClaimMatches(record, reg, agent.Metadata.UID) {
		unlock()
		return true, processhost.ErrStale
	}
	identity, _, err := localipc.Process(record.Process.PID)
	if err != nil || identity != record.Process {
		unlock()
		return true, processhost.ErrStale
	}
	if err := admission.Err(); err != nil {
		unlock()
		return true, err
	}
	apply := func() error {
		if err := admission.Err(); err != nil {
			return err
		}
		record.StopRequested = true
		if err := WriteDeferredClaim(path, record); err != nil {
			return err
		}
		if s.ClaimStopAdmission == nil {
			SignalProcessOwner(record.Process)
		}
		return nil
	}
	if s.ClaimStopAdmission != nil {
		err = s.ClaimStopAdmission(reg, apply)
	} else {
		err = apply()
	}
	if err != nil {
		unlock()
		return true, err
	}
	unlock()
	ticker := time.NewTicker(ProcessAgentStopPoll)
	defer ticker.Stop()
	for {
		latest, err := ReadDeferredClaim(path)
		if err != nil {
			return true, err
		}
		reg, err = s.Load()
		if err != nil {
			return true, err
		}
		p, ok := reg.Pane(pane.Metadata.UID)
		a, exists := reg.Agent(agent.Metadata.UID)
		if !ok || !exists || !reflect.DeepEqual(p.Status.ProcessSession, pane.Status.ProcessSession) || !p.Status.Activation.IsZero() || a.Status.PaneRef != pane.Metadata.UID {
			return true, processhost.ErrStale
		}
		if latest.Nonce != "" && latest.Nonce != record.Nonce {
			return true, processhost.ErrStale
		}
		if ProcessIdentityAbsent(record.Process, localipc.Process) && latest.Nonce == "" {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, stopFailure("agent-stop-claim-release-unconfirmed", fmt.Errorf("agent-stop-claim-release-unconfirmed: agent uid:%s pane uid:%s: %w", agent.Metadata.UID, pane.Metadata.UID, errors.Join(ctx.Err(), err)))
		case <-ticker.C:
		}
	}
}

func DeferredClaimMatches(record DeferredClaimRecord, reg coremetadata.Registry, uid string) bool {
	pane, ambiguous := ProcessAgentPane(reg, uid)
	return !ambiguous && record.Agent == uid && pane != nil && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.Binding == record.Binding && pane.Status.ProcessSession.SessionID == record.Session && pane.Status.ProcessSession.ThreadID == record.Thread
}
func SignalProcessOwner(identity coremetadata.ProcessIdentity) {
	current, _, err := localipc.Process(identity.PID)
	if err == nil && current == identity {
		if process, err := os.FindProcess(identity.PID); err == nil {
			_ = process.Signal(syscall.SIGTERM)
		}
	}
}
