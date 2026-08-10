//go:build !linux

package systemstatus

func Supported() bool { return false }
