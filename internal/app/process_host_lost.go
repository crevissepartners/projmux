package app

import (
	"fmt"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"os"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

type processIdentityReader = mutationruntime.ProcessIdentityReader

func processIdentityAbsent(recorded coremetadata.ProcessIdentity, read processIdentityReader) bool {
	return mutationruntime.ProcessIdentityAbsent(recorded, read)
}

func processActivationAbsent(a coremetadata.ProcessActivation, read processIdentityReader) bool {
	return mutationruntime.ProcessActivationAbsent(a, read)
}

func hostLostResumeCandidate(reg coremetadata.Registry, uid string, read processIdentityReader) (processResumeCandidate, bool) {
	return mutationruntime.HostLostResumeCandidate(reg, uid, read)
}

// Remove only the empty directory we inspected, never residual sockets/files.
func removeHostLostLease(registryPath string, a coremetadata.ProcessActivation) {
	path := claudeadapter.ActivationLeaseDir(registryPath, a.Binding.PaneUID, a.Binding.Generation)
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

// A private reader seam keeps kernel absence races deterministic in unit tests.
func (c *agentCommand) readProcessIdentity() processIdentityReader {
	if c.processIdentity != nil {
		return c.processIdentity
	}
	return localipc.Process
}
