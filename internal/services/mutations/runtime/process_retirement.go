package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// RetirementWitness is private evidence, not live authority or Registry schema.
// The actual Wait writer saves birth identities before deleting activation.
type RetirementWitness struct {
	Version    int
	Activation m.ProcessActivation
	Wait       *m.TerminationEvidence
}

func retirementPath(registryPath string, binding m.ProcessBinding) (string, error) {
	if registryPath == "" || !filepath.IsAbs(registryPath) {
		return "", errors.New("owner retirement state path unavailable")
	}
	for _, v := range []string{binding.AgentUID, binding.Generation} {
		if !filepath.IsLocal(v) || filepath.Base(v) != v || v == "." {
			return "", processhost.ErrStale
		}
	}
	return filepath.Join(filepath.Dir(filepath.Dir(registryPath)), "process-retirements", binding.AgentUID+"-"+binding.Generation+".json"), nil
}
func validRetirement(w RetirementWitness) bool {
	return w.Version == 1 && w.Activation.HostProcess.Valid() && w.Activation.Child.Valid() && w.Activation.HostProcess.OwnerUID == w.Activation.Child.OwnerUID && m.MatchesProcessWait(w.Activation.Binding, w.Wait)
}
func checkRetirementDirectory(path string) error {
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || !localipc.OwnedByCurrentUser(info) {
		return processhost.ErrStale
	}
	return nil
}
func ReadProcessRetirement(registryPath string, binding m.ProcessBinding, wait *m.TerminationEvidence) (m.ProcessActivation, error) {
	path, err := retirementPath(registryPath, binding)
	if err != nil {
		return m.ProcessActivation{}, err
	}
	if err = checkRetirementDirectory(path); err != nil {
		return m.ProcessActivation{}, err
	}
	// #nosec G304 -- exact explicit state root and single-component resource names; nofollow private evidence read.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return m.ProcessActivation{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return m.ProcessActivation{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !localipc.OwnedByCurrentUser(info) {
		return m.ProcessActivation{}, processhost.ErrStale
	}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return m.ProcessActivation{}, err
	}
	var record RetirementWitness
	if len(data) > 8192 || json.Unmarshal(data, &record) != nil || !validRetirement(record) || record.Activation.Binding != binding || !m.SameProcessWait(record.Wait, wait) {
		return m.ProcessActivation{}, processhost.ErrStale
	}
	return record.Activation, nil
}
func WriteProcessRetirement(registryPath string, activation m.ProcessActivation, wait *m.TerminationEvidence) error {
	record := RetirementWitness{Version: 1, Activation: activation, Wait: wait.Clone()}
	if !validRetirement(record) {
		return processhost.ErrStale
	}
	path, err := retirementPath(registryPath, activation.Binding)
	if err != nil {
		return err
	}
	if existing, readErr := ReadProcessRetirement(registryPath, activation.Binding, wait); readErr == nil {
		if !reflect.DeepEqual(existing, activation) {
			return processhost.ErrStale
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err = localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err = checkRetirementDirectory(path); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".retirement-*")
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
	// #nosec G304 -- validated private witness directory in the explicit state root.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func retirementUnconfirmed(agent, pane string, err error) error {
	return stopFailure("agent-stop-wait-unconfirmed", fmt.Errorf("agent-stop-wait-unconfirmed: agent uid:%s pane uid:%s; exact owner retirement evidence unavailable: %w", agent, pane, err))
}
