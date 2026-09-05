package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// claudeLeaseOwnerReceipt is crash-cleanup evidence, not a provider locator.
// Its file contains only identities already safe for Registry persistence.
type claudeLeaseOwnerReceipt struct {
	AgentUID   string                          `json:"agentUID"`
	PaneUID    string                          `json:"paneUID"`
	Generation string                          `json:"generation"`
	Authority  coremetadata.ClaudeAuthorityRef `json:"authority"`
}

func writeClaudeLeaseOwner(path string, bootstrap claudeEndpointBootstrap) error {
	// #nosec G304 -- the caller derives this receipt from hashed exact activation
	// and registration identities under an owned 0700 lease directory. Exclusive
	// creation refuses an existing path or symlink; no provider locator is used.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(claudeLeaseOwnerReceipt{AgentUID: bootstrap.AgentUID, PaneUID: bootstrap.PaneUID,
		Generation: bootstrap.Generation, Authority: bootstrap.Registration.Authority})
}

func readClaudeLeaseOwner(path string, spec superviseSpec) (claudeLeaseOwnerReceipt, bool) {
	var receipt claudeLeaseOwnerReceipt
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return receipt, false
	}
	file := os.NewFile(uintptr(fd), "Claude lease owner")
	if file == nil {
		_ = syscall.Close(fd)
		return receipt, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 8192 {
		return receipt, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) {
		return receipt, false
	}
	if json.NewDecoder(io.LimitReader(file, 8192)).Decode(&receipt) != nil || !receipt.Authority.Valid() ||
		receipt.AgentUID != spec.AgentUID || receipt.PaneUID != spec.PaneUID || receipt.Generation != spec.Generation {
		return receipt, false
	}
	return receipt, true
}

func watchClaudeActivationLeases(ctx context.Context, spec superviseSpec) {
	if spec.AgentUID == "" || exactActivationRegistryPath(spec.RegistryPath) != nil {
		return
	}
	ticker := time.NewTicker(2 * claudeEndpointPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reapDeadClaudeLeases(spec)
		}
	}
}

func reapDeadClaudeLeases(spec superviseSpec) {
	dir := claudeActivationLeaseDir(spec.RegistryPath, spec.PaneUID, spec.Generation)
	if !privateClaudeLeaseDir(dir) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sock.json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		receipt, ok := readClaudeLeaseOwner(path, spec)
		if !ok {
			continue
		}
		socket := claudeLeaseSocket(spec.RegistryPath, spec.PaneUID, spec.Generation, receipt.Authority.RegistrationGeneration)
		if path != socket+".json" {
			continue
		}
		expected := receipt.Authority.LeaseProcess
		if actual, _, err := claudeadapter.Process(expected.PID); err == nil && actual == expected {
			continue
		}
		store := intmetadata.NewStore(spec.RegistryPath)
		_, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
			intmetadata.DefaultMutator().ClearClaudeRegistration(reg, spec.PaneUID, spec.AgentUID, spec.Generation, receipt.Authority)
			return nil
		})
		if err != nil {
			continue
		}
		if _, err := inspectClaudeSocket(socket); err == nil {
			_ = os.Remove(socket)
		}
		_ = os.Remove(path)
	}
	_ = os.Remove(dir)
}
