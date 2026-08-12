//go:build !windows

package app

import (
	"io"
	"os"
	"syscall"
)

func doctorPathWritable(path string, directory bool) bool {
	const (
		accessExecute = 1
		accessWrite   = 2
	)
	mode := uint32(accessWrite)
	if directory {
		mode |= accessExecute
	}
	return syscall.Access(path, mode) == nil
}

func doctorPathPrivacyPrivate(info os.FileInfo) (bool, bool) {
	return info.Mode().Perm()&0o077 == 0, true
}

func doctorReadRegularFileBounded(path string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errDoctorUnsafeFileType
	}
	if info.Size() > limit {
		return nil, errDoctorInputTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errDoctorInputTooLarge
	}
	return body, nil
}
