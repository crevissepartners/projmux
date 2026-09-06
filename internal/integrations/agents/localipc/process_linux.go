package localipc

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Process reads kernel birth identity without inspecting argv, environment, or
// provider state.
func Process(pid int) (coremetadata.ProcessIdentity, int, error) {
	if pid <= 0 {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	// #nosec G304 -- pid is a checked decimal integer in a fixed procfs path.
	data, err := os.ReadFile(path)
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process identity unavailable")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process identity unavailable")
	}
	if _, err = strconv.ParseUint(fields[19], 10, 64); err != nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process identity unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process owner unavailable")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, errors.New("process boot identity unavailable")
	}
	return coremetadata.ProcessIdentity{PID: pid, OwnerUID: stat.Uid, Start: "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19]}, parent, nil
}
