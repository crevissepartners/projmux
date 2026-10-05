package app

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

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// This private operation journal is not Registry schema or provider history.
// Source is the actual complete pre-Stop record; Target is this request's own
// reservation. A retained file carries recovery authority, never ready control.
type codexHostTransferRecord struct {
	Version          int
	Source           coremetadata.Agent
	Pane             coremetadata.Pane
	Retired          *coremetadata.Agent
	Receipt          codexbroker.TransferReceipt
	Target           processhost.Binding
	Expected         *coremetadata.Agent
	TerminatedTarget *coremetadata.Pane
	NativeTerminated []coremetadata.Pane
	NativeBinding    *codexappserver.ThreadBinding
	NativeTarget     *codexbroker.NativeTransferTarget
	NativeExpected   *coremetadata.Agent
	Phase            string
}

func (c *agentCommand) codexHostTransferPath(uid string) (string, error) {
	if c.store == nil || c.store.stateDir == nil {
		return "", errors.New("agent relaunch: Codex transfer state root is unavailable")
	}
	state, err := c.store.stateDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(uid))
	return filepath.Join(state, "codex-host-transfers", fmt.Sprintf("%x.json", sum[:])), nil
}
func readCodexHostTransfer(path string) (*codexHostTransferRecord, error) {
	data, err := readCodexHostTransferBytes(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record codexHostTransferRecord
	if len(data) > 1<<20 || json.Unmarshal(data, &record) != nil || record.Version != 1 || record.Source.Metadata.UID == "" || record.Pane.Metadata.UID == "" || record.Receipt.Token == "" || record.Receipt.Source.Agent != record.Source.Metadata.UID || record.Receipt.Source.Pane != record.Pane.Metadata.UID {
		return nil, errors.New("agent relaunch: damaged Codex host transfer journal; inspect exact source and target")
	}
	return &record, nil
}
func writeCodexHostTransfer(path string, record *codexHostTransferRecord) error {
	if err := localstate.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("agent relaunch: Codex transfer journal exceeds its bounded record size; previous evidence retained")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".transfer-*")
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
	return syncCodexTransferDir(path)
}
func syncCodexTransferDir(path string) error {
	dir, err := os.Open(filepath.Dir(path)) // #nosec G304 -- validated private state directory.
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func removeCodexHostTransfer(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncCodexTransferDir(path)
}

func codexTransferRecoveryCommand(uid string, request agentRelaunchRequest) string {
	args := []string{"projmux", "agent", "relaunch", "uid:" + uid, "--host", "tmux", "--yes"}
	if request.socket.socketPath != "" {
		args = append(args, "--socket-path", request.socket.socketPath)
	} else if request.socket.socket != "" {
		args = append(args, "--socket", request.socket.socket)
	}
	for i, arg := range args {
		args[i] = shellQuote(arg)
	}
	return strings.Join(args, " ")
}

// Completed journals retain exact source history and any failed target Wait.
// They are owner-private operational evidence, not reusable control authority.
func completeCodexHostTransfer(path string) error {
	record, err := readCodexHostTransfer(path)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("missing Codex transfer journal")
	}
	record.Phase = "completed"
	if err := publishCodexHostTransferArchive(path, record); err != nil {
		return err
	}
	return removeCodexHostTransfer(path)
}

// Every archive publisher consumes the same full-record conflict rule. Only
// phase advancement and adding the exact observed target Wait are monotonic.
func publishCodexHostTransferArchive(path string, record *codexHostTransferRecord) error {
	if err := validateCodexHostTransferArchive(path, record); err != nil {
		return err
	}
	return writeCodexHostTransfer(path+"."+record.Receipt.Token+".completed.json", record)
}

// Validation is read-only: recovery checks conflicts before restoring Registry
// state, without publishing an archive or granting any writer authority.
func validateCodexHostTransferArchive(path string, record *codexHostTransferRecord) error {
	archivePath := path + "." + record.Receipt.Token + ".completed.json"
	if previous, readErr := readCodexHostTransfer(archivePath); readErr != nil {
		return readErr
	} else if previous != nil {
		old := *previous
		old.Phase = record.Phase
		if old.TerminatedTarget == nil {
			old.TerminatedTarget = record.TerminatedTarget
		}
		if !reflect.DeepEqual(old, *record) {
			return errors.New("agent relaunch: completed archive changed; evidence retained")
		}
	}
	return nil
}

// All transfer-journal readers, including raw byte CAS, share the same bound.
func readCodexHostTransferBytes(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- operation digest path under private application state.
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	err = errors.Join(readErr, file.Close())
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("agent relaunch: Codex transfer journal exceeds its bounded record size; evidence retained")
	}
	return data, nil
}
