package app

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

type processIdentityReader func(int) (coremetadata.ProcessIdentity, int, error)

func processIdentityAbsent(recorded coremetadata.ProcessIdentity, read processIdentityReader) bool {
	if !recorded.Valid() || int64(recorded.OwnerUID) != int64(os.Getuid()) {
		return false
	}
	current, _, err := read(recorded.PID)
	return errors.Is(err, localipc.ErrProcessAbsent) || (err == nil && current.Valid() && current != recorded)
}

func processActivationAbsent(a coremetadata.ProcessActivation, read processIdentityReader) bool {
	return processIdentityAbsent(a.HostProcess, read) && processIdentityAbsent(a.Child, read)
}

// The projection is read-only. Its original activation and session are retained
// for the atomic reservation's CAS and second kernel check.
func hostLostResumeCandidate(reg coremetadata.Registry, uid string, read processIdentityReader) (processResumeCandidate, bool) {
	pane, ambiguous := processResumePane(reg, uid)
	if ambiguous || pane == nil || pane.Status.Activation.Process == nil || reg.Validate() != nil {
		return processResumeCandidate{}, false
	}
	a := *pane.Status.Activation.Process
	if !processActivationAbsent(a, read) {
		return processResumeCandidate{}, false
	}
	projected := reg.Clone()
	p, _ := projected.Pane(pane.Metadata.UID)
	agent, _ := projected.Agent(uid)
	record := p.Status.ProcessSession
	if record == nil || record.Binding != a.Binding || record.ConnectionID != a.Binding.OperationID || (record.Provider == aiModeClaude && record.SessionID == "") || (record.Provider == aiModeCodex && record.ThreadID == "") {
		return processResumeCandidate{}, false
	}
	p.Status.Activation = coremetadata.PaneActivation{}
	agent.Status.Phase = coremetadata.PhaseOffline
	record.ResumeState = coremetadata.ProcessResumable
	for _, candidate := range listResumableProcessAgents(projected, processResumeFilter{}) {
		if candidate.Agent.Metadata.UID == uid {
			candidate.Record = *pane.Status.ProcessSession.Clone()
			candidate.HostLost = &a
			candidate.Previous.MayBeTruncated = true
			return candidate, true
		}
	}
	return processResumeCandidate{}, false
}

// Remove only the empty directory we inspected, never residual sockets/files.
func removeHostLostLease(registryPath string, a coremetadata.ProcessActivation) {
	path := claudeActivationLeaseDir(registryPath, a.Binding.PaneUID, a.Binding.Generation)
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode().Perm() != 0700 || before.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != a.HostProcess.OwnerUID || int64(stat.Uid) != int64(os.Getuid()) {
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		return
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() {
		return
	}
	_ = os.Remove(path) // rmdir also refuses a directory that became nonempty.
}

func reserveHostLostResume(reg *coremetadata.Registry, candidate processResumeCandidate, binding coremetadata.ProcessBinding, read processIdentityReader, mutator coremetadata.Mutator) error {
	if candidate.HostLost == nil || !processActivationAbsent(*candidate.HostLost, read) {
		return fmt.Errorf("%s: %w: owner or provider absence is unproven", processResumeNotResumable, processhost.ErrResumeRefused)
	}
	pane, _ := processResumePane(*reg, binding.AgentUID)
	if pane == nil || !processResumeRecordEqual(pane.Status.ProcessSession, &candidate.Record) {
		return fmt.Errorf("%s: %w: recorded generation changed", processResumeRefused, processhost.ErrResumeRefused)
	}
	return mutator.ReserveProcessHostLostResume(reg, *candidate.HostLost, binding)
}
