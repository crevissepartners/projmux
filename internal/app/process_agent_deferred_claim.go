package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const deferredHoldReason = "target-awaiting-resume"

type deferredClaimRecord struct {
	Version                   int
	Agent                     string
	Binding                   coremetadata.ProcessBinding
	Provider, Session, Thread string
	Process                   coremetadata.ProcessIdentity
	Nonce                     string
	// Inflight is persisted before the first provider frame. A replacement
	// claimant fails it as unknown rather than writing the frame twice.
	Inflight string
}

type deferredProcessClaim struct {
	mu      sync.Mutex
	command *agentCommand
	options processAgentResumeOptions
	record  deferredClaimRecord
	path    string
}

func (c *agentCommand) deferredClaimPath(uid string) string {
	if c.messagePaths.registryPath == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(uid))
	return filepath.Join(filepath.Dir(filepath.Dir(c.messagePaths.registryPath)), "deferred-claims", fmt.Sprintf("%x.json", digest[:]))
}

func lockDeferredClaim(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	lock, err := localstate.AcquireFileLock(path+".lock", 2*time.Second, nil)
	if err != nil {
		return nil, err
	}
	// Closing releases flock. Never unlink its persistent inode.
	return func() { _ = lock.Close() }, nil
}

func readDeferredClaim(path string) (deferredClaimRecord, error) {
	var record deferredClaimRecord
	if path == "" {
		return record, nil
	}
	file, err := os.Open(path) // #nosec G304 -- private state path derived from Agent digest.
	if errors.Is(err, os.ErrNotExist) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	err = errors.Join(err, file.Close())
	if err != nil {
		return record, err
	}
	if len(data) > 8192 || json.Unmarshal(data, &record) != nil || record.Version != 1 || record.Nonce == "" || record.Agent == "" || !record.Process.Valid() {
		return record, fmt.Errorf("%s: %w: damaged deferred claim", processResumeRefused, processhost.ErrResumeRefused)
	}
	return record, nil
}

func writeDeferredClaim(path string, record deferredClaimRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".claim-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path)) // #nosec G304 -- private claim parent validated by EnsurePrivateDir.
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func deferredClaimLive(record deferredClaimRecord) bool {
	if record.Nonce == "" {
		return false
	}
	identity, _, err := localipc.Process(record.Process.PID)
	return err == nil && identity == record.Process
}

func deferredClaimOwned() error {
	return fmt.Errorf("%s: %w: deferred process Agent has a live claimant", processResumeOwned, processhost.ErrResumeRefused)
}

func (c *agentCommand) checkDeferredClaim(uid string, claim *deferredProcessClaim) error {
	record, err := readDeferredClaim(c.deferredClaimPath(uid))
	if err != nil {
		return err
	}
	if claim != nil {
		if claim.command != c || claim.record.Agent != uid || record.Nonce != claim.record.Nonce || !deferredClaimLive(record) {
			return fmt.Errorf("%s: %w: deferred claim changed", processResumeRefused, processhost.ErrResumeRefused)
		}
	} else if deferredClaimLive(record) {
		return deferredClaimOwned()
	}
	return nil
}

func (c *agentCommand) claimDeferredProcessAgent(ctx context.Context, options processAgentResumeOptions) (*deferredProcessClaim, error) {
	request, err := newProcessAgentResumeRequest(options)
	if err != nil {
		return nil, err
	}
	candidate, err := c.processResumeCandidate(request)
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
	claim := &deferredProcessClaim{command: c, options: options, path: path, record: deferredClaimRecord{Version: 1, Agent: candidate.Agent.Metadata.UID, Binding: candidate.Record.Binding, Provider: candidate.Record.Provider, Session: candidate.Record.SessionID, Thread: candidate.Record.ThreadID, Process: identity, Nonce: nonce}}
	if err = writeDeferredClaim(path, claim.record); err != nil {
		return nil, err
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
