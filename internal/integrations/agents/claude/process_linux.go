package claude

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

// Process reads kernel birth identity. No command line, environment, transcript,
// or provider registration store is inspected.
func Process(pid int) (metadata.ProcessIdentity, int, error) {
	if pid <= 0 {
		return metadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	data, err := os.ReadFile(path)
	if err != nil {
		return metadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return metadata.ProcessIdentity{}, 0, errors.New("process identity unavailable")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return metadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return metadata.ProcessIdentity{}, 0, errors.New("process identity unavailable")
	}
	if _, err = strconv.ParseUint(fields[19], 10, 64); err != nil {
		return metadata.ProcessIdentity{}, 0, errors.New("process identity unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return metadata.ProcessIdentity{}, 0, errors.New("process unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return metadata.ProcessIdentity{}, 0, errors.New("process owner unavailable")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return metadata.ProcessIdentity{}, 0, errors.New("process boot identity unavailable")
	}
	return metadata.ProcessIdentity{PID: pid, OwnerUID: stat.Uid, Start: "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19]}, parent, nil
}
