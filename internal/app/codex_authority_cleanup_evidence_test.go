package app

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

type installedCleanupFailure struct {
	Stage     string                           `json:"stage"`
	Operation string                           `json:"operation"`
	Code      string                           `json:"code"`
	Errno     int                              `json:"errno,omitempty"`
	PID       int                              `json:"pid,omitempty"`
	Leaf      string                           `json:"leaf,omitempty"`
	LaterStat *installedCleanupProcessSnapshot `json:"laterStat,omitempty"`
}

type installedCleanupProcessSnapshot struct {
	At        string `json:"at"`
	PID       int    `json:"pid"`
	Birth     string `json:"birth,omitempty"`
	State     string `json:"state,omitempty"`
	ParentPID int    `json:"parentPID,omitempty"`
	Code      string `json:"code,omitempty"`
	Errno     int    `json:"errno,omitempty"`
}

func cleanupErrno(err error) int {
	var errno syscall.Errno
	if errors.As(err, &errno) && errno > 0 && errno < 4096 {
		return int(errno)
	}
	return 0
}

func cleanupProcPath(path string) (int, string) {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "proc" {
		return 0, ""
	}
	pid, err := strconv.Atoi(parts[2])
	if err != nil || pid <= 0 || strconv.Itoa(pid) != parts[2] {
		return 0, ""
	}
	switch parts[3] {
	case "environ", "stat", "status", "exe":
		return pid, parts[3]
	default:
		return 0, ""
	}
}

func observeInstalledCleanupFailure(stage string, err error, readStat func(int) ([]byte, error)) *installedCleanupFailure {
	if err == nil {
		return nil
	}
	out := &installedCleanupFailure{Stage: recoveryToken(stage, "owned-processes", "fixture-cleanup", "remove-root", "remove-auth", "manager-stop"), Operation: "unclassified", Code: recoveryError(err), Errno: cleanupErrno(err)}
	var observation *codexinstalled.OwnedProcessObservationError
	var pathError *os.PathError
	if errors.As(err, &observation) {
		switch {
		case observation.Operation == "list-proc" && observation.PID == 0 && observation.Leaf == "":
			out.Operation = observation.Operation
		case observation.PID > 0 && ((observation.Operation == "read-environment" && observation.Leaf == "environ") || (observation.Operation == "read-birth" && observation.Leaf == "stat") || (observation.Operation == "read-executable" && observation.Leaf == "exe")):
			out.Operation, out.PID, out.Leaf = observation.Operation, observation.PID, observation.Leaf
		}
	} else if errors.As(err, &pathError) {
		out.Operation = recoveryToken(pathError.Op, "open", "read", "readlink", "stat", "lstat", "readdirent", "unlinkat", "remove", "rmdir")
		out.PID, out.Leaf = cleanupProcPath(pathError.Path)
	}
	if out.PID != 0 && readStat != nil {
		// One later stat snapshot may already describe a vanished/reused PID.
		// It never repairs the failed observation or grants cleanup authority.
		raw, statErr := readStat(out.PID)
		out.LaterStat = projectInstalledCleanupStat(out.PID, raw, statErr)
	}
	return out
}

func readInstalledCleanupStat(pid int) ([]byte, error) {
	if pid <= 0 {
		return nil, errors.New("invalid diagnostic PID")
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
		return nil, errors.New("bounded process stat unavailable")
	}
	return raw, nil
}

func projectInstalledCleanupStat(pid int, raw []byte, err error) *installedCleanupProcessSnapshot {
	out := &installedCleanupProcessSnapshot{At: time.Now().UTC().Format(time.RFC3339Nano), PID: pid, Code: recoveryError(err), Errno: cleanupErrno(err)}
	if err != nil {
		return out
	}
	refuse := func() *installedCleanupProcessSnapshot { out.Code = "invalid-stat"; return out }
	text := string(raw)
	begin, end := strings.Index(text, " ("), strings.LastIndex(text, ") ")
	if len(raw) > 4096 || begin < 1 || end < begin || text[:begin] != strconv.Itoa(pid) {
		return refuse()
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 20 || len(fields[0]) != 1 || !strings.Contains("RSDZTtXxKWPI", fields[0]) {
		return refuse()
	}
	birth, birthErr := strconv.ParseUint(fields[19], 10, 64)
	parent, parentErr := strconv.Atoi(fields[1])
	if birthErr != nil || strconv.FormatUint(birth, 10) != fields[19] || parentErr != nil || parent < 0 {
		return refuse()
	}
	out.Birth, out.State, out.ParentPID = fields[19], fields[0], parent
	return out
}

// The original process-inspection result is returned unchanged. A diagnostic
// read cannot retry/skip an error or convert an unknown inventory into empty.
func inspectInstalledCleanupProcesses(inspect func() ([]codexinstalled.OwnedProcess, error), readStat func(int) ([]byte, error)) ([]codexinstalled.OwnedProcess, *installedCleanupFailure, error) {
	processes, err := inspect()
	return processes, observeInstalledCleanupFailure("owned-processes", err, readStat), err
}
