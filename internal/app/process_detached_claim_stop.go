package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Stop's claim phase is distinct from provider Stop. The same guard used by
// reservation serializes birth and exact claim cancellation. A claim is never
// provider Wait evidence; no Registry termination record is written here.
func (c *agentCommand) stopDetachedClaim(ctx context.Context, agent coremetadata.Agent, pane coremetadata.Pane) (bool, error) {
	path := c.deferredClaimPath(agent.Metadata.UID)
	unlock, err := lockDeferredClaim(path)
	if err != nil {
		return false, err
	}
	record, err := readDeferredClaim(path)
	if err != nil {
		unlock()
		return false, err
	}
	if !record.Detached {
		unlock()
		return false, nil
	}
	reg, err := c.loadRegistry()
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
		if current.Status.Activation.IsZero() && target.Status.Phase == coremetadata.PhaseOffline && deferredClaimLive(record) {
			return true, processhost.ErrStale
		}
		return false, nil
	}
	if !deferredClaimLive(record) {
		unlock()
		return true, errors.New("agent-stop-claim-release-unconfirmed: exact claimant unavailable; inspect the Agent before retrying")
	}
	if target.Status.Phase != coremetadata.PhaseOffline || !reflect.DeepEqual(current.Status.ProcessSession, pane.Status.ProcessSession) || record.Provider != current.Status.ProcessSession.Provider || !deferredClaimMatches(record, reg, agent.Metadata.UID) {
		unlock()
		return true, processhost.ErrStale
	}
	identity, _, err := localipc.Process(record.Process.PID)
	if err != nil || identity != record.Process {
		unlock()
		return true, processhost.ErrStale
	}
	record.StopRequested = true
	if err = writeDeferredClaim(path, record); err != nil {
		unlock()
		return true, err
	}
	// Once the durable request is set, checkDeferredClaim refuses all birth
	// attempts, even if the signal arrives between planning and reservation.
	signalDetachedProcessOwner(record.Process)
	unlock()
	ticker := time.NewTicker(processRelaunchRetirementPoll)
	defer ticker.Stop()
	for {
		latest, err := readDeferredClaim(path)
		if err != nil {
			return true, err
		}
		reg, err = c.loadRegistry()
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
		if processIdentityAbsent(record.Process, localipc.Process) && latest.Nonce == "" {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, fmt.Errorf("agent-stop-claim-release-unconfirmed: agent uid:%s pane uid:%s: %w", agent.Metadata.UID, pane.Metadata.UID, errors.Join(ctx.Err(), err))
		case <-ticker.C:
		}
	}
}
