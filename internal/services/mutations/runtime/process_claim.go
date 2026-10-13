package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

type DeferredClaimRecord struct {
	Version                   int
	Agent                     string
	Binding                   coremetadata.ProcessBinding
	Provider, Session, Thread string
	Process                   coremetadata.ProcessIdentity
	Nonce                     string
	// Inflight is persisted before the first provider frame. A replacement
	// claimant fails it as unknown rather than writing the frame twice.
	Inflight      string
	Detached      bool `json:",omitempty"`
	StopRequested bool `json:",omitempty"`
}

func DeferredClaimPath(registryPath, uid string) string {
	if registryPath == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(uid))
	return filepath.Join(filepath.Dir(filepath.Dir(registryPath)), "deferred-claims", fmt.Sprintf("%x.json", digest[:]))
}

func LockDeferredClaim(path string) (func(), error) {
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

func ReadDeferredClaim(path string) (DeferredClaimRecord, error) {
	var record DeferredClaimRecord
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
		return record, fmt.Errorf("%s: %w: damaged deferred claim", "process-resume-refused", processhost.ErrResumeRefused)
	}
	return record, nil
}

func WriteDeferredClaim(path string, record DeferredClaimRecord) error {
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

func DeferredClaimLive(record DeferredClaimRecord) bool {
	if record.Nonce == "" {
		return false
	}
	identity, _, err := localipc.Process(record.Process.PID)
	return err == nil && identity == record.Process
}
