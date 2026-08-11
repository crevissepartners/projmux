//go:build darwin && cgo

package systemstatus

func Supported() bool { return true }
