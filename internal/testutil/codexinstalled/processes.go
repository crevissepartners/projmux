package codexinstalled

import (
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// OwnedProcess is a content-free process-birth observation in the private PID
// namespace. Kind separates owned executable observations from unreaped
// residuals whose environment/ownership is unknown. Neither grants signal
// authority. A residual is present, never an absent or clean process result.
type OwnedProcess struct {
	Kind       string `json:"kind"`
	PID        int    `json:"pid"`
	Birth      string `json:"birth"`
	Executable string `json:"executable,omitempty"`
	State      string `json:"state,omitempty"`
	ParentPID  int    `json:"parentPID,omitempty"`
}

const (
	ProcessOwnedExecutable  = "owned-executable"
	ProcessUnreapedResidual = "unreaped-residual"
)

type processInventory struct {
	mu         sync.Mutex
	unresolved bool
	residuals  map[int]OwnedProcess
}

func (inventory *processInventory) requireResolved() error {
	inventory.mu.Lock()
	defer inventory.mu.Unlock()
	if inventory.unresolved || len(inventory.residuals) != 0 {
		return errors.New("fixture cleanup refused: process inventory is unresolved or unreaped")
	}
	return nil
}

// OwnedProcessObservationError retains only the failing observation boundary.
// Its cause remains available for errno inspection, but Error never emits a
// proc path, process contents, environment or arbitrary underlying message.
// This evidence grants no process ownership or cleanup authority.
type OwnedProcessObservationError struct {
	Operation string
	PID       int
	Leaf      string
	cause     error
}

func (*OwnedProcessObservationError) Error() string     { return "owned process observation failed" }
func (err *OwnedProcessObservationError) Unwrap() error { return err.cause }

func (fixture *Fixture) OwnedProcesses() ([]OwnedProcess, error) {
	return fixture.ownedProcesses(processReader{readDir: os.ReadDir, readFile: os.ReadFile, readlink: os.Readlink, birth: managedProcessBirth, stat: readResidualStat, verifyPrivate: fixture.verifyResidualIsolation, self: os.Getpid()})
}

// Residual classification is available only after this fixture has completed a
// verified official lifecycle, in the same private namespaces with an init
// reaper. This does not establish ownership of any residual or its parent.
func (fixture *Fixture) verifyResidualIsolation() error {
	if !fixture.ownsState || !fixture.strictManaged || !fixture.strictManagedStopped || fixture.strictManagedUnresolved || len(fixture.processNamespaces) != 3 {
		return errors.New("residual private namespace is unproved")
	}
	observed, err := fixture.processIsolation.Verify()
	if err != nil || !maps.Equal(observed, fixture.processNamespaces) {
		return errors.New("residual private namespace changed or is unproved")
	}
	return nil
}

// processReader keeps deterministic proc observations independent of host processes.
type processReader struct {
	readDir       func(string) ([]os.DirEntry, error)
	readFile      func(string) ([]byte, error)
	readlink      func(string) (string, error)
	birth         func(int) (string, error)
	stat          func(int) ([]byte, error)
	verifyPrivate func() error
	self          int
}

func (fixture *Fixture) ownedProcesses(reader processReader) ([]OwnedProcess, error) {
	fixture.processInventory.mu.Lock()
	defer fixture.processInventory.mu.Unlock()
	// Strict recovery cleanup cannot turn a failed census into cleanup success.
	// Unrelated legacy fixtures retain their existing cleanup semantics.
	fixture.processInventory.unresolved = fixture.strictManaged
	entries, err := reader.readDir("/proc")
	if err != nil {
		return nil, &OwnedProcessObservationError{Operation: "list-proc", cause: err}
	}
	processes := []OwnedProcess{}
	if fixture.processInventory.residuals == nil {
		fixture.processInventory.residuals = map[int]OwnedProcess{}
	}
	next := maps.Clone(fixture.processInventory.residuals)
	observedResiduals := map[int]bool{}
	for _, pid := range slices.Sorted(maps.Keys(next)) {
		if err := reader.verifyPrivate(); err != nil {
			return nil, &OwnedProcessObservationError{Operation: "read-residual", PID: pid, Leaf: "stat", cause: err}
		}
		residual, err := confirmResidual(reader.stat, pid, next[pid].Birth, true)
		if err != nil {
			return nil, &OwnedProcessObservationError{Operation: "read-residual", PID: pid, Leaf: "stat", cause: err}
		}
		if residual.PID == 0 {
			delete(next, pid) // A later exact stat ENOENT proves normal reaping.
			continue
		}
		next[pid] = residual
		processes = append(processes, residual)
		observedResiduals[pid] = true
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || strconv.Itoa(pid) != entry.Name() || pid == reader.self || observedResiduals[pid] {
			continue
		}
		raw, err := reader.readFile(filepath.Join("/proc", entry.Name(), "environ"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			// The denied environment remains unknown. Two bounded exact stat
			// observations can classify an unreaped residual, never ownership.
			if (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)) && reader.verifyPrivate() == nil {
				if residual, statErr := confirmResidual(reader.stat, pid, "", false); statErr == nil {
					fixture.processInventory.residuals[pid], next[pid] = residual, residual
					processes = append(processes, residual)
					continue
				}
			}
			return nil, &OwnedProcessObservationError{Operation: "read-environment", PID: pid, Leaf: "environ", cause: err}
		}
		owned := false
		for value := range strings.SplitSeq(string(raw), "\x00") {
			if value == "CODEX_HOME="+fixture.CodexHome {
				owned = true
			}
		}
		if !owned {
			continue
		}
		birth, err := reader.birth(pid)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, &OwnedProcessObservationError{Operation: "read-birth", PID: pid, Leaf: "stat", cause: err}
		}
		executable, err := reader.readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, &OwnedProcessObservationError{Operation: "read-executable", PID: pid, Leaf: "exe", cause: err}
		}
		processes = append(processes, OwnedProcess{Kind: ProcessOwnedExecutable, PID: pid, Birth: birth, Executable: executable})
	}
	fixture.processInventory.residuals = next
	fixture.processInventory.unresolved = false
	return processes, nil
}

func readResidualStat(pid int) ([]byte, error) {
	if pid <= 0 {
		return nil, errors.New("invalid residual PID")
	}
	file, err := os.Open("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, err
	}
	if len(raw) > 4096 {
		return nil, errors.New("residual stat exceeds bound")
	}
	return raw, nil
}

func parseResidualStat(pid int, raw []byte) (OwnedProcess, error) {
	refuse := errors.New("residual stat is not an exact zombie identity")
	if len(raw) > 4096 || !strings.HasPrefix(string(raw), strconv.Itoa(pid)+" (") {
		return OwnedProcess{}, refuse
	}
	at := strings.LastIndex(string(raw), ") ")
	if at < 0 {
		return OwnedProcess{}, refuse
	}
	fields := strings.Fields(string(raw)[at+2:])
	if len(fields) < 20 || fields[0] != "Z" {
		return OwnedProcess{}, refuse
	}
	parent, parentErr := strconv.Atoi(fields[1])
	birth, birthErr := strconv.ParseUint(fields[19], 10, 64)
	if parentErr != nil || parent < 0 || strconv.Itoa(parent) != fields[1] || birthErr != nil || birth == 0 || strconv.FormatUint(birth, 10) != fields[19] {
		return OwnedProcess{}, refuse
	}
	return OwnedProcess{Kind: ProcessUnreapedResidual, PID: pid, Birth: fields[19], State: "Z", ParentPID: parent}, nil
}

func confirmResidual(readStat func(int) ([]byte, error), pid int, birth string, allowReaped bool) (OwnedProcess, error) {
	raw, err := readStat(pid)
	if allowReaped && errors.Is(err, fs.ErrNotExist) {
		return OwnedProcess{}, nil
	}
	if err != nil {
		return OwnedProcess{}, err
	}
	first, err := parseResidualStat(pid, raw)
	if err != nil || (birth != "" && first.Birth != birth) {
		return OwnedProcess{}, errors.New("residual stat changed or is unproved")
	}
	raw, err = readStat(pid)
	if err != nil {
		return OwnedProcess{}, err
	}
	second, err := parseResidualStat(pid, raw)
	if err != nil || second.Birth != first.Birth {
		return OwnedProcess{}, errors.New("residual stat changed or is unproved")
	}
	return second, nil
}
