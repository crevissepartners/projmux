package app

import (
	"io"
	"os"
	"strconv"
	"strings"
)

// codexProcessScanLimit bounds one process-table read. A diagnostics section
// that walked an unbounded table would turn `projmux doctor` into a scan of the
// machine; this sits far above any real process count and exists only so that
// a pathological /proc cannot hold the command open.
const codexProcessScanLimit = 8192

// codexProcessCmdlineLimit bounds one argv read. The routes this reader matches
// are a few hundred bytes; anything past this bound is not one of them.
const codexProcessCmdlineLimit = 8 << 10

// defaultCodexProcessImages reads the local process table.
//
// It reads two files per process and never writes, signals, or opens anything
// the process owns. A link it cannot read is skipped, which is the ordinary
// answer for a process belonging to another user.
func defaultCodexProcessImages() ([]codexProcessImage, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	images := make([]codexProcessImage, 0, 64)
	for _, entry := range entries {
		if len(images) >= codexProcessScanLimit {
			break
		}
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		exe, err := os.Readlink("/proc/" + entry.Name() + "/exe")
		if err != nil || strings.TrimSpace(exe) == "" {
			continue
		}
		images = append(images, codexProcessImage{PID: pid, Exe: exe, Cmdline: readProcCmdline(entry.Name())})
	}
	return images, true
}

// readProcCmdline reads one process's argv. An unreadable or empty argv yields
// no words, which classifies the process into no control-plane role rather than
// into the wrong one.
func readProcCmdline(pid string) []string {
	// #nosec G304 -- pid is one numeric directory entry of /proc, rendered by
	// the kernel; the fixed procfs path cannot carry caller traversal.
	file, err := os.Open("/proc/" + pid + "/cmdline")
	if err != nil {
		return nil
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, codexProcessCmdlineLimit))
	if err != nil || len(payload) == 0 {
		return nil
	}
	words := strings.Split(strings.TrimRight(string(payload), "\x00"), "\x00")
	kept := words[:0]
	for _, word := range words {
		if word != "" {
			kept = append(kept, word)
		}
	}
	return kept
}
