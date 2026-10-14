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
// provider state. Procfs inode ownership can change when a task loses its mm;
// use the task's effective credential, bracketed by birth and state checks.
func Process(pid int) (coremetadata.ProcessIdentity, int, error) {
	return readLinuxProcess(pid, os.ReadFile)
}

type linuxProcessStat struct {
	state, start string
	parent       int
}

func sameLinuxLiveBirth(a, b linuxProcessStat) bool {
	live := func(state string) bool {
		switch state {
		case "R", "S", "D", "T", "t", "I", "W", "K", "P":
			return true
		default:
			return false
		}
	}
	return live(a.state) && live(b.state) && a.start == b.start && a.parent == b.parent
}

func readLinuxProcess(pid int, read func(string) ([]byte, error)) (coremetadata.ProcessIdentity, int, error) {
	unavailable := errors.New("process identity unavailable")
	if pid <= 0 {
		return coremetadata.ProcessIdentity{}, 0, unavailable
	}
	path := "/proc/" + strconv.Itoa(pid)
	stat := func() (linuxProcessStat, error) {
		data, err := read(path + "/stat")
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				return linuxProcessStat{}, ErrProcessAbsent
			}
			return linuxProcessStat{}, unavailable
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return linuxProcessStat{}, unavailable
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 20 {
			return linuxProcessStat{}, unavailable
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil || parent < 0 {
			return linuxProcessStat{}, unavailable
		}
		if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
			return linuxProcessStat{}, unavailable
		}
		return linuxProcessStat{fields[0], fields[19], parent}, nil
	}
	uid := func() (uint32, error) {
		data, err := read(path + "/status")
		if err != nil {
			return 0, unavailable
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 5 && fields[0] == "Uid:" {
				value, err := strconv.ParseUint(fields[2], 10, 32)
				if err == nil {
					return uint32(value), nil
				}
			}
		}
		return 0, unavailable
	}
	first, err := stat()
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, err
	}
	if first.state == "Z" || first.state == "X" {
		last, err := stat()
		if err != nil {
			return coremetadata.ProcessIdentity{}, 0, err
		}
		if last == first {
			return coremetadata.ProcessIdentity{}, 0, ErrProcessAbsent
		}
		return coremetadata.ProcessIdentity{}, 0, unavailable
	}
	owner, err := uid()
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, err
	}
	middle, err := stat()
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, err
	}
	if !sameLinuxLiveBirthIgnoringParent(first, middle) {
		return coremetadata.ProcessIdentity{}, 0, unavailable
	}
	finalOwner, err := uid()
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, err
	}
	last, err := stat()
	if err != nil {
		return coremetadata.ProcessIdentity{}, 0, err
	}
	if !sameLinuxLiveBirthIgnoringParent(first, last) || finalOwner != owner {
		return coremetadata.ProcessIdentity{}, 0, unavailable
	}
	boot, err := read("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) == "" {
		return coremetadata.ProcessIdentity{}, 0, unavailable
	}
	identity := coremetadata.ProcessIdentity{PID: pid, OwnerUID: owner, Start: "linux:" + strings.TrimSpace(string(boot)) + ":" + first.start}
	if first.parent != middle.parent || first.parent != last.parent {
		if first.parent != last.parent && (middle.parent == first.parent || middle.parent == last.parent) {
			return coremetadata.ProcessIdentity{}, 0, ParentTransitionWitness{Process: identity, OldParent: first.parent, NewParent: last.parent}
		}
		return coremetadata.ProcessIdentity{}, 0, unavailable
	}
	return identity, first.parent, nil
}

func sameLinuxLiveBirthIgnoringParent(a, b linuxProcessStat) bool {
	b.parent = a.parent
	return sameLinuxLiveBirth(a, b)
}
