package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const (
	processProviderClaude = "claude"
	processProviderCodex  = "codex"

	ProcessClaudePermissionMode = "auto"
)

// The launch outlives a claim. It carries resolved arguments and snapshot
// proofs, never a first frame or the caller's environment.
type DeferredLaunchRecord struct {
	Version                        int
	Agent                          string
	Retired                        coremetadata.ProcessSessionRecord
	OldSpec, NewSpec               coremetadata.AgentSpec
	OldAnnotations, NewAnnotations map[string]string
	Command                        processhost.Command
	Files                          map[string]string
	Model, Effort                  string
	Attempt                        *DeferredLaunchAttempt
	Previous                       *DeferredLaunchRecord
	HostLost                       *coremetadata.ProcessActivation `json:",omitempty"`
}

type DeferredLaunchAttempt struct {
	Source coremetadata.ProcessSessionRecord
	Target coremetadata.ProcessBinding
}

func DeferredRefused(detail string) error {
	return fmt.Errorf("%s: %w: %s", ProcessResumeRefused, processhost.ErrResumeRefused, detail)
}

func DeferredStatePath(registryPath, kind, uid string) string {
	path := DeferredClaimPath(registryPath, uid)
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(path)), kind, filepath.Base(path))
}

// Sidecars are bounded, private, regular files owned by this OS user. The
// persistent claim lock is the only writer lock for an Agent's sidecars.
func ReadDeferredState(path string, value any) (bool, error) {
	if path == "" {
		return false, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return false, DeferredRefused("unsafe deferred state")
	}
	file, err := os.Open(path) // #nosec G304 -- private state path derived from the Agent digest.
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	err = errors.Join(err, file.Close())
	if err != nil {
		return false, err
	}
	if len(data) > 65536 || json.Unmarshal(data, value) != nil {
		return false, DeferredRefused("damaged deferred state")
	}
	return true, nil
}

func ReadDeferredLaunch(registryPath, uid string) (*DeferredLaunchRecord, error) {
	var record DeferredLaunchRecord
	found, err := ReadDeferredState(DeferredStatePath(registryPath, "deferred-launches", uid), &record)
	if err != nil || !found {
		return nil, err
	}
	if record.Version != 1 || record.Agent != uid || record.Retired.Provider != processProviderClaude || record.Retired.SessionID == "" || record.Retired.Binding.AgentUID != uid || record.Command.Env != nil || record.Command.Path == "" || record.Command.Dir == "" {
		return nil, DeferredRefused("damaged deferred launch")
	}
	if record.Previous != nil && (record.Previous.Previous != nil || record.Previous.Agent != uid || record.Previous.Version != 1 || record.Previous.Retired.Provider != processProviderClaude || record.Previous.Retired.Binding.AgentUID != uid || record.Previous.Retired.SessionID == "" || record.Previous.Command.Env != nil || record.Previous.Command.Path == "" || record.Previous.Command.Dir == "" || record.Attempt == nil) {
		return nil, DeferredRefused("damaged relaunch intent")
	}
	return &record, nil
}

func DeferredLaunchDigest(record *DeferredLaunchRecord) string {
	if record == nil {
		return ""
	}
	data, _ := json.Marshal(record)
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func deferredSnapshotDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", DeferredRefused("launch snapshot is not a regular file")
	}
	file, err := os.Open(path) // #nosec G304 -- resolved launch snapshot, never provider input.
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	err = errors.Join(err, file.Close())
	return fmt.Sprintf("%x", hash.Sum(nil)), err
}

// Claude's resolved grammar has two file-valued flags. Workspace roots are
// directories; resume IDs, model/effort and stream arguments are literals.
func DeferredLaunchFiles(args []string) (map[string]string, error) {
	files := map[string]string{}
	for i := 0; i < len(args); i++ {
		arg, path := args[i], ""
		for _, flag := range []string{"--settings", "--append-system-prompt-file"} {
			if arg == flag {
				if i+1 >= len(args) {
					return nil, DeferredRefused("missing snapshot path")
				}
				i++
				path = args[i]
			}
			if after, ok := strings.CutPrefix(arg, flag+"="); ok {
				path = after
			}
		}
		if path != "" {
			digest, err := deferredSnapshotDigest(path)
			if err != nil {
				return nil, err
			}
			files[path] = digest
		}
	}
	return files, nil
}

func (record *DeferredLaunchRecord) ValidateFiles() error {
	files, err := DeferredLaunchFiles(record.Command.Args)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(files, record.Files) {
		return DeferredRefused("launch snapshot changed; configuration retained")
	}
	return nil
}

func (record *DeferredLaunchRecord) Matches(candidate ProcessResumeCandidate) bool {
	return reflect.DeepEqual(record.HostLost, candidate.HostLost) && record.Agent == candidate.Agent.Metadata.UID && ProcessResumeRecordEqual(&record.Retired, &candidate.Record) && reflect.DeepEqual(candidate.Agent.Spec, record.NewSpec) && reflect.DeepEqual(candidate.Agent.Metadata.Annotations, record.NewAnnotations)
}

// Ordinary recovery may restore Previous before returning a command. Refuse
// a legacy current or prior recipe before either recovery can write durable state.
func (record *DeferredLaunchRecord) ValidatePermissionMode() error {
	if err := ValidateDeferredClaudePermissionMode(record.Command.Args); err != nil {
		return err
	}
	if record.Previous != nil {
		return ValidateDeferredClaudePermissionMode(record.Previous.Command.Args)
	}
	return nil
}

// Validate the frozen argv without repairing it. Explicit relaunch replacement
// uses the common planner and retains the existing digest/claim/CAS authority.
func ValidateDeferredClaudePermissionMode(args []string) error {
	count := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		mode, found := strings.CutPrefix(arg, "--permission-mode=")
		if arg == "--permission-mode" {
			found = true
			i++
			if i < len(args) {
				mode = args[i]
			}
		}
		if found {
			count++
			if mode != ProcessClaudePermissionMode || count > 1 {
				return DeferredRefused("prepared Claude launch requires exactly one --permission-mode auto; use agent relaunch to replace the prepared launch")
			}
		}
	}
	if count != 1 {
		return DeferredRefused("prepared Claude launch requires exactly one --permission-mode auto; use agent relaunch to replace the prepared launch")
	}
	return nil
}
