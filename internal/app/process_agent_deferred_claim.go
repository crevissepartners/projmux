package app

import (
	"context"
	"errors"
	"fmt"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"os"
	"sync"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const deferredHoldReason = "target-awaiting-resume"

type deferredClaimRecord = mutationruntime.DeferredClaimRecord

type deferredProcessClaim struct {
	mu             sync.Mutex
	inputOperation string
	launchDigest   string
	command        *agentCommand
	options        processAgentResumeOptions
	record         deferredClaimRecord
	path           string
}

func (c *agentCommand) deferredClaimPath(uid string) string {
	return mutationruntime.DeferredClaimPath(c.messagePaths.registryPath, uid)
}

func lockDeferredClaim(path string) (func(), error) { return mutationruntime.LockDeferredClaim(path) }
func readDeferredClaim(path string) (deferredClaimRecord, error) {
	return mutationruntime.ReadDeferredClaim(path)
}
func writeDeferredClaim(path string, record deferredClaimRecord) error {
	return mutationruntime.WriteDeferredClaim(path, record)
}
func deferredClaimLive(record deferredClaimRecord) bool {
	return mutationruntime.DeferredClaimLive(record)
}

func deferredClaimOwned() error {
	return fmt.Errorf("%s: %w: deferred process Agent has a live claimant", processResumeOwned, processhost.ErrResumeRefused)
}

// Inflight and StopRequested are mutable witnesses, not claim identity.
// Cleanup may clear an inflight witness after Stop, while birth stays refused.
func deferredClaimIdentityEqual(a, b deferredClaimRecord) bool {
	a.Inflight, b.Inflight = "", ""
	a.StopRequested, b.StopRequested = false, false
	return a == b
}

func (c *agentCommand) checkDeferredClaim(uid string, claim *deferredProcessClaim) error {
	record, err := readDeferredClaim(c.deferredClaimPath(uid))
	if err != nil {
		return err
	}
	if claim != nil {
		if claim.command != c || claim.record.Agent != uid || !deferredClaimIdentityEqual(record, claim.record) || record.StopRequested || !deferredClaimLive(record) {
			return fmt.Errorf("%s: %w: deferred claim changed", processResumeRefused, processhost.ErrResumeRefused)
		}
	} else if deferredClaimLive(record) {
		return deferredClaimOwned()
	}
	return nil
}

func (c *agentCommand) claimDeferredProcessAgent(ctx context.Context, options processAgentResumeOptions, replacement ...bool) (*deferredProcessClaim, error) {
	request, err := newProcessAgentResumeRequest(options)
	if err != nil {
		return nil, err
	}
	candidate, err := c.processResumeCandidate(request)
	if err != nil {
		return nil, err
	}
	explicit := len(replacement) > 0 && replacement[0]
	candidate, prepared, err := c.prepareDeferredLaunchMode(ctx, candidate, options, explicit)
	if err != nil {
		return nil, err
	}
	path := c.deferredClaimPath(candidate.Agent.Metadata.UID)
	if path == "" {
		return nil, errors.New("process-host-unavailable: deferred claim store unavailable")
	}
	unlock, err := lockDeferredClaim(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	old, err := readDeferredClaim(path)
	if err != nil {
		return nil, err
	}
	if deferredClaimLive(old) {
		return nil, deferredClaimOwned()
	}
	if explicit {
		current, e := c.readDeferredLaunch(candidate.Agent.Metadata.UID)
		if e != nil {
			return nil, e
		}
		if deferredLaunchDigest(current) != deferredLaunchDigest(prepared) {
			return nil, deferredRefused("replacement launch changed")
		}
	}
	// Re-read under the guard: ordinary resume reservations take this same lock.
	candidate, err = c.processResumeCandidate(request)
	if err != nil {
		return nil, err
	}
	if old.Inflight != "" {
		if record, found, e := c.messageStore.Get(old.Inflight); e != nil {
			return nil, e
		} else if found && !record.Delivery.State.Terminal() {
			if _, e = c.terminalCoordination(record, "fail", "provider-handoff-outcome-unknown", true, nil); e != nil {
				return nil, e
			}
		}
	}
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil {
		return nil, err
	}
	nonce, err := newCreateOperationID()
	if err != nil {
		return nil, err
	}
	claim := &deferredProcessClaim{command: c, options: options, path: path, launchDigest: deferredLaunchDigest(prepared), record: deferredClaimRecord{Version: 1, Agent: candidate.Agent.Metadata.UID, Binding: candidate.Record.Binding, Provider: candidate.Record.Provider, Session: candidate.Record.SessionID, Thread: candidate.Record.ThreadID, Process: identity, Nonce: nonce, Detached: processOwnerMode(ctx) == processHostOwnerDetached}}
	if err = writeDeferredClaim(path, claim.record); err != nil {
		return nil, err
	}
	if err = c.reclaimDeferredInput(claim.record); err != nil {
		return nil, errors.Join(err, removeDeferredState(path))
	}
	return claim, nil
}

func (claim *deferredProcessClaim) Close() error {
	unlock, err := lockDeferredClaim(claim.path)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := readDeferredClaim(claim.path)
	if err != nil {
		return err
	}
	if record.Nonce != claim.record.Nonce {
		return nil
	}
	// Keep an unsettled write witness across claimant death/close.
	if record.Inflight != "" {
		return nil
	}
	if err = claim.command.closeDeferredInput(claim.record); err != nil {
		return err
	}
	return os.Remove(claim.path)
}

// Explicit reply acceptance runs under the producer's target claim guard.
// The helper reads its private proof; source authority remains the broker's
// ordinary live-route check and all explicit-reply gates stay in place.
func deferredReplyTargetCurrent(registryPath string, registry coremetadata.Registry, route coremessage.Route) bool {
	c := &agentCommand{messagePaths: agentMessagePaths{registryPath: registryPath}}
	record, err := readDeferredClaim(c.deferredClaimPath(route.AgentUID))
	return err == nil && deferredClaimLive(record) && processResumeCandidateToken(registry, route.AgentUID) == "" && deferredClaimMatches(record, registry, route.AgentUID) && deferredMessageRoute(record) == route
}
