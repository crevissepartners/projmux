//go:build windows

package app

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func doctorPathWritable(path string, _ bool) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm()&0o200 != 0
}

// Windows FileMode permission bits do not describe the file's ACL. Report the
// privacy check as unverified instead of incorrectly claiming POSIX-style
// private or insecure permissions. Doctor never changes the ACL.
func doctorPathPrivacyPrivate(os.FileInfo) (bool, bool) { return false, false }

func doctorReadRegularFileBounded(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, errDoctorUnsafeFileType
	}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		path16,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
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
