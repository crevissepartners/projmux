package app

import (
	"golang.org/x/sys/unix"
)

// One selected-PID sysctl per snapshot; no command, argv, or provider reads.
func relaunchReadKernelSnapshot(pid int) relaunchKernelSnapshot {
	s := relaunchKernelSnapshot{Begin: relaunchSnapshotTime(), PID: pid}
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	s.Stat = relaunchDiagnosticReadState(err)
	s.Status = "unavailable:linux-status-not-applicable"
	if err == nil {
		switch len(infos) {
		case 0:
			s.Stat = "absent:empty-sysctl"
		case 1:
			info := infos[0]
			s.PID, s.Parent, s.State = int(info.Proc.P_pid), int(info.Eproc.Ppid), uint64(info.Proc.P_stat)
			s.Start = uint64(info.Proc.P_starttime.Sec)*1000000 + uint64(info.Proc.P_starttime.Usec)
			s.UIDs[1] = uint64(info.Eproc.Ucred.Uid)
			s.Status = "observed:effective-uid-only"
		default:
			s.Stat = "unavailable:sysctl-count"
		}
	}
	s.End = relaunchSnapshotTime()
	return s
}
