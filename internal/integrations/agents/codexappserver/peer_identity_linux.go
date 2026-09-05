package codexappserver

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"golang.org/x/sys/unix"
)

func unixPeerIdentity(conn *net.UnixConn) (coremetadata.ProcessIdentity, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return coremetadata.ProcessIdentity{}, errors.New("peer unavailable")
	}
	var credential *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		credential, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || peerErr != nil || credential == nil {
		return coremetadata.ProcessIdentity{}, errors.New("peer unavailable")
	}
	identity, err := processIdentity(int(credential.Pid))
	if err != nil || identity.OwnerUID != credential.Uid {
		return coremetadata.ProcessIdentity{}, errors.New("peer unavailable")
	}
	return identity, nil
}

// processIdentity reads only kernel birth and ownership identity. It never
// inspects command lines, environment, provider payload, or transcript state.
func processIdentity(pid int) (coremetadata.ProcessIdentity, error) {
	if pid <= 0 {
		return coremetadata.ProcessIdentity{}, errors.New("process unavailable")
	}
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	// #nosec G304 -- pid is rendered from a checked positive integer.
	data, err := os.ReadFile(path)
	if err != nil {
		return coremetadata.ProcessIdentity{}, errors.New("process unavailable")
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return coremetadata.ProcessIdentity{}, errors.New("process identity unavailable")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return coremetadata.ProcessIdentity{}, errors.New("process unavailable")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return coremetadata.ProcessIdentity{}, errors.New("process identity unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return coremetadata.ProcessIdentity{}, errors.New("process unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return coremetadata.ProcessIdentity{}, errors.New("process owner unavailable")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return coremetadata.ProcessIdentity{}, errors.New("process boot identity unavailable")
	}
	return coremetadata.ProcessIdentity{
		PID: pid, OwnerUID: stat.Uid,
		Start: "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19],
	}, nil
}
